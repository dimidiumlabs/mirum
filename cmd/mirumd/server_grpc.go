// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/types/known/timestamppb"

	"dimidiumlabs/mirum/internal/protocol"
	"dimidiumlabs/mirum/internal/protocol/pb"
	"dimidiumlabs/mirum/internal/protocol/pb/pbconnect"
)

// connKey is the context key for the underlying net.Conn.
type connKey struct{}

// tlsStateKey is the context key for TLS connection state.
type tlsStateKey struct{}

func NewGrpcServer(ctx context.Context, srv *server, tlsCfg *tls.Config) *http.Server {
	gsrv := &grpcService{
		ctx: ctx,
		srv: srv,
		tls: tlsCfg != nil,
	}
	opts := []connect.HandlerOption{
		connect.WithInterceptors(gsrv),
	}
	path, handler := pbconnect.NewMirumHandler(gsrv, opts...)
	mux := http.NewServeMux()
	mux.Handle(path, requireGRPC(tlsMiddleware(handler)))
	return &http.Server{
		Handler:     h2c.NewHandler(mux, &http2.Server{}),
		ConnContext: gsrv.connContext,
		ConnState:   gsrv.connState,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}
}

// grpcService is the ConnectRPC transport adapter over server.
type grpcService struct {
	pbconnect.UnimplementedMirumHandler

	ctx         context.Context
	srv         *server
	tls         bool     // whether TLS is enabled (for channel binding)
	authedConns sync.Map // net.Conn → true
}

func (g *grpcService) Poll(ctx context.Context, req *connect.Request[pb.PollRequest]) (*connect.Response[pb.Task], error) {
	select {
	case task, ok := <-g.srv.queue:
		if !ok {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("server is shutting down"))
		}
		slog.Info("task dispatched", "id", task.Id, "repo", task.RepoFullName)
		return connect.NewResponse(task), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *grpcService) Complete(ctx context.Context, req *connect.Request[pb.TaskResult]) (*connect.Response[pb.CompleteResponse], error) {
	if err := g.srv.complete(ctx, req.Msg.TaskId, req.Msg.Success, req.Msg.Error); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.CompleteResponse{}), nil
}

func (g *grpcService) Handshake(ctx context.Context, stream *connect.BidiStream[pb.HandshakeIn, pb.HandshakeOut]) error {
	// Step 1: receive worker public key
	in, err := stream.Receive()
	if err != nil {
		return fmt.Errorf("recv worker challenge: %w", err)
	}
	wc := in.GetWorkerChallenge()
	if wc == nil {
		return fmt.Errorf("expected WorkerChallenge")
	}

	// Look up the worker in the database
	worker, err := g.srv.db.LookupWorker(ctx, wc.GetPublicKey())
	if err != nil {
		return g.reject(stream, "unknown worker")
	}

	// Extract TLS EKM for channel binding (nil if no TLS)
	ekm := g.extractEKM(ctx)

	hs := protocol.NewServerHandshake()

	// Step 2: generate challenge nonce
	serverNonce, err := hs.Challenge(wc.GetPublicKey(), ekm)
	if err != nil {
		return err
	}
	if err := stream.Send(&pb.HandshakeOut{
		Step: &pb.HandshakeOut_ServerChallenge{
			ServerChallenge: &pb.ServerChallenge{
				Nonce:  serverNonce,
				Binded: ekm != nil,
			},
		},
	}); err != nil {
		return fmt.Errorf("send server challenge: %w", err)
	}

	// Step 3: receive worker signature + metadata
	in, err = stream.Receive()
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

	if err := hs.Verify(wp.GetSignature(), wt.AsTime()); err != nil {
		return g.reject(stream, err.Error())
	}

	var warnings []string
	if skew := time.Since(wt.AsTime()).Abs(); skew > 10*time.Second {
		warnings = append(warnings, fmt.Sprintf("clock drift: %s", skew.Truncate(time.Second)))
	}

	slog.Info("worker connected",
		"worker_id", worker.ID,
		"name", wp.GetName(),
		"os", wp.GetOs(),
		"arch", wp.GetArch(),
		"runtime", wp.GetRuntime(),
	)

	// Step 4: accept
	if conn, ok := ctx.Value(connKey{}).(net.Conn); ok {
		g.authedConns.Store(conn, true)
	}
	return g.sendResult(stream, nil, warnings)
}

func (g *grpcService) sendResult(stream *connect.BidiStream[pb.HandshakeIn, pb.HandshakeOut], errMsg *string, warnings []string) error {
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

func (g *grpcService) reject(stream *connect.BidiStream[pb.HandshakeIn, pb.HandshakeOut], reason string) error {
	if err := g.sendResult(stream, &reason, nil); err != nil {
		return err
	}
	return fmt.Errorf("%s", reason)
}

func (g *grpcService) requireAuth(ctx context.Context, procedure string) error {
	if procedure == pbconnect.MirumHandshakeProcedure {
		return nil
	}
	conn, ok := ctx.Value(connKey{}).(net.Conn)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("handshake required"))
	}
	if _, ok := g.authedConns.Load(conn); !ok {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("handshake required"))
	}
	return nil
}

// WrapUnary implements connect.Interceptor for unary RPCs (Poll, Complete).
func (g *grpcService) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := g.requireAuth(ctx, req.Spec().Procedure); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient is a no-op (server-side only).
func (g *grpcService) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler implements connect.Interceptor for streaming RPCs (Handshake).
func (g *grpcService) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := g.requireAuth(ctx, conn.Spec().Procedure); err != nil {
			return err
		}
		if conn.Spec().Procedure == pbconnect.MirumHandshakeProcedure {
			done := make(chan error, 1)
			go func() { done <- next(ctx, conn) }()
			select {
			case err := <-done:
				return err
			case <-time.After(30 * time.Second):
				return fmt.Errorf("handshake timeout")
			}
		}
		return next(ctx, conn)
	}
}

// connContext stores the net.Conn in the context for connection tracking.
func (g *grpcService) connContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

// connState cleans up authedConns when a connection closes.
func (g *grpcService) connState(c net.Conn, state http.ConnState) {
	if state == http.StateClosed {
		g.authedConns.Delete(c)
		slog.Debug("peer disconnected")
	}
}

// extractEKM returns TLS Exported Keying Material from the context,
// or nil if TLS is not enabled.
func (g *grpcService) extractEKM(ctx context.Context) []byte {
	if !g.tls {
		return nil
	}
	state, ok := ctx.Value(tlsStateKey{}).(*tls.ConnectionState)
	if !ok || state == nil {
		return nil
	}
	ekm, err := state.ExportKeyingMaterial(protocol.EKMLabel, nil, protocol.EKMLength)
	if err != nil {
		slog.Warn("failed to export keying material", "err", err)
		return nil
	}
	return ekm
}

// requireGRPC rejects requests that do not use the gRPC wire protocol.
func requireGRPC(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if ct != "application/grpc" && !strings.HasPrefix(ct, "application/grpc+") {
			http.Error(w, "only gRPC protocol is supported", http.StatusUnsupportedMediaType)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tlsMiddleware injects the TLS connection state into the request context.
func tlsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			ctx := context.WithValue(r.Context(), tlsStateKey{}, r.TLS)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}
