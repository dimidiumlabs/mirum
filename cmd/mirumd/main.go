// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"mrdimidium/mirum/internal/forges"
	"mrdimidium/mirum/internal/protocol"
	"mrdimidium/mirum/internal/protocol/pb"
	"mrdimidium/mirum/internal/supervisor"

	"github.com/coreos/go-systemd/v22/activation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gopkg.in/yaml.v3"
)

type config struct {
	GrpcAddr string `yaml:"grpc_addr"`
	WwwAddr  string `yaml:"www_addr"`
	Secret   string `yaml:"secret"`
	Token    string `yaml:"token"`
}

var cfg = config{
	GrpcAddr: ":2026",
	WwwAddr:  ":3000",
}

var configFile = flag.String("config", "", "path to config file")

type taskMeta struct {
	forge forges.Forge
	event *forges.PushEvent
}

var taskCounter int64

func nextTaskID() string {
	taskCounter++
	return fmt.Sprintf("task-%d", taskCounter)
}

func main() {
	flag.Parse()

	if *configFile != "" {
		data, err := os.ReadFile(*configFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	if cfg.Token == "" {
		fmt.Fprintln(os.Stderr, "error: token is required")
		os.Exit(1)
	}

	forge := &forges.GitHub{Secret: cfg.Secret, Token: cfg.Token}

	srv := &mirumServer{
		secret: []byte(cfg.Secret),
		queue:  make(chan *pb.Task, 100),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Mirum</title></head><body><h1>Mirum</h1><p>CI server is running.</p></body></html>`)
	})
	mux.HandleFunc("POST /webhook", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		ev, err := forge.Webhook(r, body)
		if errors.Is(err, forges.ErrInvalidSignature) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if ev == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		id := nextTaskID()
		slog.Info("push", "repo", ev.Owner+"/"+ev.Repo, "branch", ev.Branch, "sha", ev.SHA[:8], "task", id)

		srv.tasks.Store(id, taskMeta{forge: forge, event: ev})
		_ = forge.SetStatus(context.Background(), ev, forges.StatusPending, "Queued")

		srv.queue <- &pb.Task{
			Id:           id,
			CloneUrl:     forge.AuthURL(ev.CloneURL),
			Branch:       ev.Branch,
			Sha:          ev.SHA,
			RepoFullName: ev.Owner + "/" + ev.Repo,
		}

		w.WriteHeader(http.StatusAccepted)
	})

	grpcLn, httpLn, err := listeners()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	grpcSrv := grpc.NewServer(
		grpc.UnaryInterceptor(srv.unaryInterceptor),
		grpc.StreamInterceptor(srv.streamInterceptor),
		grpc.StatsHandler(&connTracker{server: srv}),
	)
	pb.RegisterMirumServer(grpcSrv, srv)
	httpSrv := &http.Server{Handler: mux}

	slog.Info("listening", "grpc", grpcLn.Addr(), "http", httpLn.Addr())

	sup := supervisor.Detect()
	ctx := sup.WaitForStop(context.Background())

	go func() {
		<-ctx.Done()
		slog.Info("shutting down")
		sup.Stopping()

		grpcSrv.GracefulStop()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
	}()

	go grpcSrv.Serve(grpcLn)

	sup.Ready()
	go sup.StartWatchdog()

	if err := httpSrv.Serve(httpLn); err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type mirumServer struct {
	pb.UnimplementedMirumServer
	secret      []byte
	queue       chan *pb.Task
	tasks       sync.Map // task_id → taskMeta
	authedPeers sync.Map // peer addr string → true
}

func (s *mirumServer) Poll(ctx context.Context, req *pb.PollRequest) (*pb.Task, error) {
	select {
	case task := <-s.queue:
		slog.Info("task dispatched", "id", task.Id, "repo", task.RepoFullName)
		return task, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *mirumServer) Complete(ctx context.Context, result *pb.TaskResult) (*pb.CompleteResponse, error) {
	meta, ok := s.tasks.LoadAndDelete(result.TaskId)
	if !ok {
		return nil, fmt.Errorf("unknown task: %s", result.TaskId)
	}
	m := meta.(taskMeta)

	status := forges.StatusSuccess
	desc := "Build passed"
	if !result.Success {
		status = forges.StatusFailure
		desc = "Build failed"
		if result.Error != "" {
			desc = result.Error
		}
	}

	if err := m.forge.SetStatus(ctx, m.event, status, desc); err != nil {
		slog.Error("set status", "task", result.TaskId, "err", err)
	}

	slog.Info("task complete", "id", result.TaskId, "success", result.Success)
	return &pb.CompleteResponse{}, nil
}

func (s *mirumServer) Handshake(stream pb.Mirum_HandshakeServer) error {
	// Step 1: receive worker nonce
	in, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("recv worker challenge: %w", err)
	}
	wc := in.GetWorkerChallenge()
	if wc == nil {
		return fmt.Errorf("expected WorkerChallenge")
	}
	workerNonce := wc.GetNonce()
	if len(workerNonce) != protocol.NonceSize {
		return fmt.Errorf("invalid nonce size: %d", len(workerNonce))
	}

	// Step 2: send server nonce + proof
	serverNonce, err := protocol.GenerateNonce()
	if err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	if err := stream.Send(&pb.HandshakeOut{
		Step: &pb.HandshakeOut_ServerChallenge{
			ServerChallenge: &pb.ServerChallenge{
				Nonce: serverNonce,
				Proof: protocol.ComputeProof(s.secret, workerNonce, serverNonce),
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

	if !protocol.VerifyProof(s.secret, serverNonce, workerNonce, wp.GetProof()) {
		return s.reject(stream, "invalid secret")
	}

	// Check clock skew
	wt := wp.GetWorkerTime()
	if wt == nil {
		return s.reject(stream, "worker_time is required")
	}
	skew := time.Since(wt.AsTime()).Abs()
	if skew > time.Minute {
		return s.reject(stream, fmt.Sprintf("clock skew too large: %s", skew.Truncate(time.Second)))
	}
	if skew > 10*time.Second {
		slog.Warn("clock skew", "worker", wp.GetName(), "skew", skew.Truncate(time.Second))
	}

	slog.Info("worker connected",
		"id", fmt.Sprintf("%x", wp.GetId()),
		"name", wp.GetName(),
		"os", wp.GetOs(),
		"arch", wp.GetArch(),
		"runtime", wp.GetRuntime(),
	)

	// Step 4: accept
	if p, ok := peer.FromContext(stream.Context()); ok {
		s.authedPeers.Store(p.Addr.String(), true)
	}
	return s.sendResult(stream, nil)
}

func (s *mirumServer) sendResult(stream pb.Mirum_HandshakeServer, errMsg *string) error {
	return stream.Send(&pb.HandshakeOut{
		Step: &pb.HandshakeOut_ServerResult{
			ServerResult: &pb.ServerResult{
				Error:         errMsg,
				ServerVersion: protocol.VersionProto(),
				ServerTime:    timestamppb.Now(),
			},
		},
	})
}

func (s *mirumServer) reject(stream pb.Mirum_HandshakeServer, reason string) error {
	if err := s.sendResult(stream, &reason); err != nil {
		return err
	}
	return fmt.Errorf("%s", reason)
}

func peerAddr(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok {
		return p.Addr.String()
	}
	return ""
}

func (s *mirumServer) requireAuth(ctx context.Context, method string) error {
	if method == pb.Mirum_Handshake_FullMethodName {
		return nil
	}
	if _, ok := s.authedPeers.Load(peerAddr(ctx)); !ok {
		return status.Error(codes.Unauthenticated, "handshake required")
	}
	return nil
}

func (s *mirumServer) unaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := s.requireAuth(ctx, info.FullMethod); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (s *mirumServer) streamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := s.requireAuth(ss.Context(), info.FullMethod); err != nil {
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
	server *mirumServer
}

func (t *connTracker) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	return ctx
}

func (t *connTracker) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	return ctx
}

func (t *connTracker) HandleRPC(ctx context.Context, s stats.RPCStats) {}

func (t *connTracker) HandleConn(ctx context.Context, s stats.ConnStats) {
	if _, ok := s.(*stats.ConnEnd); ok {
		addr := peerAddr(ctx)
		if addr != "" {
			t.server.authedPeers.Delete(addr)
			slog.Debug("peer disconnected", "addr", addr)
		}
	}
}

// listeners returns gRPC and HTTP listeners.
// With systemd socket activation it expects two named fds: "grpc" and "http".
// Without socket activation it falls back to cfg.GrpcAddr and cfg.WwwAddr.
func listeners() (grpcLn, httpLn net.Listener, err error) {
	named, err := activation.ListenersWithNames()
	if err != nil {
		return nil, nil, fmt.Errorf("socket activation: %w", err)
	}

	if lns := named["grpc"]; len(lns) > 0 {
		grpcLn = lns[0]
	}
	if lns := named["http"]; len(lns) > 0 {
		httpLn = lns[0]
	}

	if grpcLn == nil {
		grpcLn, err = net.Listen("tcp", cfg.GrpcAddr)
		if err != nil {
			return nil, nil, err
		}
	}
	if httpLn == nil {
		httpLn, err = net.Listen("tcp", cfg.WwwAddr)
		if err != nil {
			grpcLn.Close()
			return nil, nil, err
		}
	}

	return grpcLn, httpLn, nil
}
