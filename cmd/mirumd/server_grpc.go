// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"dimidiumlabs/mirum/internal/protocol"
	"dimidiumlabs/mirum/internal/protocol/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// connIDKey is the context key for the unique connection identifier.
type connIDKey struct{}

func NewGrpcServer(ctx context.Context, srv *server, secret []byte) *grpc.Server {
	gsrv := &grpcService{
		ctx:    ctx,
		srv:    srv,
		secret: secret,
	}
	s := grpc.NewServer(
		grpc.UnaryInterceptor(gsrv.unaryInterceptor),
		grpc.StreamInterceptor(gsrv.streamInterceptor),
		grpc.StatsHandler(&connTracker{gsrv: gsrv}),
	)
	pb.RegisterMirumServer(s, gsrv)
	return s
}

// grpcService is the gRPC transport adapter over server.
type grpcService struct {
	pb.UnimplementedMirumServer

	ctx         context.Context
	srv         *server
	secret      []byte
	authedConns sync.Map // conn ID (uint64) → true
	nextConnID  atomic.Uint64
}

func (g *grpcService) Poll(ctx context.Context, req *pb.PollRequest) (*pb.Task, error) {
	select {
	case task, ok := <-g.srv.queue:
		if !ok {
			return nil, status.Error(codes.Unavailable, "server is shutting down")
		}
		slog.Info("task dispatched", "id", task.Id, "repo", task.RepoFullName)
		return task, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *grpcService) Complete(ctx context.Context, result *pb.TaskResult) (*pb.CompleteResponse, error) {
	if err := g.srv.complete(ctx, result.TaskId, result.Success, result.Error); err != nil {
		return nil, err
	}
	return &pb.CompleteResponse{}, nil
}

func (g *grpcService) Handshake(stream pb.Mirum_HandshakeServer) error {
	hs := protocol.NewServerHandshake(g.secret)

	// Step 1: receive worker nonce
	in, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("recv worker challenge: %w", err)
	}
	wc := in.GetWorkerChallenge()
	if wc == nil {
		return fmt.Errorf("expected WorkerChallenge")
	}

	// Step 2: send server nonce + proof
	serverNonce, proof, err := hs.Challenge(wc.GetNonce())
	if err != nil {
		return err
	}
	if err := stream.Send(&pb.HandshakeOut{
		Step: &pb.HandshakeOut_ServerChallenge{
			ServerChallenge: &pb.ServerChallenge{
				Nonce: serverNonce,
				Proof: proof,
			},
		},
	}); err != nil {
		return fmt.Errorf("send server challenge: %w", err)
	}

	// Step 3: receive worker proof + metadata
	in, err = stream.Recv()
	if err != nil {
		return fmt.Errorf("recv worker proof: %w", err)
	}
	wp := in.GetWorkerProof()
	if wp == nil {
		return fmt.Errorf("expected WorkerProof")
	}

	wt := wp.GetWorkerTime()
	if wt == nil {
		return g.reject(stream, "worker_time is required")
	}

	if err := hs.Verify(wp.GetProof(), wt.AsTime()); err != nil {
		return g.reject(stream, err.Error())
	}

	var warnings []string
	if skew := time.Since(wt.AsTime()).Abs(); skew > 10*time.Second {
		warnings = append(warnings, fmt.Sprintf("clock drift: %s", skew.Truncate(time.Second)))
	}

	slog.Info("worker connected",
		"id", fmt.Sprintf("%x", wp.GetId()),
		"name", wp.GetName(),
		"os", wp.GetOs(),
		"arch", wp.GetArch(),
		"runtime", wp.GetRuntime(),
	)

	// Step 4: accept
	if id, ok := stream.Context().Value(connIDKey{}).(uint64); ok {
		g.authedConns.Store(id, true)
	}
	return g.sendResult(stream, nil, warnings)
}

func (g *grpcService) sendResult(stream pb.Mirum_HandshakeServer, errMsg *string, warnings []string) error {
	return stream.Send(&pb.HandshakeOut{
		Step: &pb.HandshakeOut_ServerResult{
			ServerResult: &pb.ServerResult{
				Error:         errMsg,
				ServerVersion: protocol.VersionProto(),
				ServerTime:    timestamppb.Now(),
				Warnings:      warnings,
			},
		},
	})
}

func (g *grpcService) reject(stream pb.Mirum_HandshakeServer, reason string) error {
	if err := g.sendResult(stream, &reason, nil); err != nil {
		return err
	}
	return fmt.Errorf("%s", reason)
}

func (g *grpcService) requireAuth(ctx context.Context, method string) error {
	if method == pb.Mirum_Handshake_FullMethodName {
		return nil
	}
	id, ok := ctx.Value(connIDKey{}).(uint64)
	if !ok {
		return status.Error(codes.Unauthenticated, "handshake required")
	}
	if _, ok := g.authedConns.Load(id); !ok {
		return status.Error(codes.Unauthenticated, "handshake required")
	}
	return nil
}

func (g *grpcService) unaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := g.requireAuth(ctx, info.FullMethod); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (g *grpcService) streamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := g.requireAuth(ss.Context(), info.FullMethod); err != nil {
		return err
	}
	if info.FullMethod == pb.Mirum_Handshake_FullMethodName {
		done := make(chan error, 1)
		go func() { done <- handler(srv, ss) }()
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			return fmt.Errorf("handshake timeout")
		}
	}
	return handler(srv, ss)
}

// connTracker implements stats.Handler to clean up authedPeers on disconnect.
type connTracker struct {
	stats.Handler
	gsrv *grpcService
}

func (t *connTracker) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	id := t.gsrv.nextConnID.Add(1)
	return context.WithValue(ctx, connIDKey{}, id)
}

func (t *connTracker) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	return ctx
}

func (t *connTracker) HandleRPC(ctx context.Context, s stats.RPCStats) {}

func (t *connTracker) HandleConn(ctx context.Context, s stats.ConnStats) {
	if _, ok := s.(*stats.ConnEnd); ok {
		if id, ok := ctx.Value(connIDKey{}).(uint64); ok {
			t.gsrv.authedConns.Delete(id)
			slog.Debug("peer disconnected", "conn", id)
		}
	}
}
