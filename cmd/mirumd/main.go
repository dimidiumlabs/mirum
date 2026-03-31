// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mrdimidium/mirum/internal/protocol"
	"mrdimidium/mirum/internal/protocol/pb"
	"mrdimidium/mirum/internal/supervisor"

	"github.com/coreos/go-systemd/v22/activation"
	"go.starlark.net/starlark"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gopkg.in/yaml.v3"
)

type config struct {
	GrpcAddr string `yaml:"grpc_addr"`
	WwwAddr  string `yaml:"www_addr"`
	Secret   string `yaml:"secret"`
	Token    string `yaml:"token"`
	Script   string `yaml:"script"`
}

var cfg = config{
	GrpcAddr: ":2026",
	WwwAddr:  ":3000",
	Script:   ".mirum/main.star",
}

var configFile = flag.String("config", "", "path to config file")

type pushEvent struct {
	Ref   string `json:"ref"`
	After string `json:"after"`
	Repo  struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
}

func processPush(push pushEvent) {
	owner, repo := splitFullName(push.Repo.FullName)
	sha := push.After
	log := slog.With("repo", push.Repo.FullName, "sha", sha[:8])

	if err := setStatus(owner, repo, sha, "pending", "Build started"); err != nil {
		log.Error("set pending status", "err", err)
	}

	dir, err := os.MkdirTemp("", "mirum-*")
	if err != nil {
		log.Error("build failed", "err", err)
		_ = setStatus(owner, repo, sha, "failure", "Build failed")
		return
	}
	defer os.RemoveAll(dir)

	branch := strings.TrimPrefix(push.Ref, "refs/heads/")
	cloneURL := authURL(push.Repo.CloneURL)

	if out, err := runCmd(dir, "git", "clone", "--depth=1", "--branch", branch, cloneURL, "."); err != nil {
		log.Error("build failed", "err", err, "output", out)
		_ = setStatus(owner, repo, sha, "failure", "Build failed")
		return
	}

	if err := runStarlark(dir); err != nil {
		log.Error("build failed", "err", err)
		_ = setStatus(owner, repo, sha, "failure", "Build failed")
		return
	}

	log.Info("build passed")
	_ = setStatus(owner, repo, sha, "success", "Build passed")
}

func setStatus(owner, repo, sha, state, description string) error {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/statuses/%s", owner, repo, sha)

	body, _ := json.Marshal(map[string]string{
		"state":       state,
		"description": description,
		"context":     "mirum",
	})

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github api %d: %s", resp.StatusCode, b)
	}
	return nil
}

func verifySignature(payload []byte, signature string) bool {
	sig, ok := strings.CutPrefix(signature, "sha256=")
	if !ok {
		return false
	}
	decoded, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(cfg.Secret))
	mac.Write(payload)
	return hmac.Equal(mac.Sum(nil), decoded)
}

func authURL(cloneURL string) string {
	if cfg.Token == "" {
		return cloneURL
	}
	u, err := url.Parse(cloneURL)
	if err != nil {
		return cloneURL
	}
	u.User = url.UserPassword("x-access-token", cfg.Token)
	return u.String()
}

func splitFullName(fullName string) (string, string) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 {
		return fullName, ""
	}
	return parts[0], parts[1]
}

type taskCtx struct {
	dir string
}

var _ starlark.HasAttrs = (*taskCtx)(nil)

func (c *taskCtx) String() string        { return "ctx" }
func (c *taskCtx) Type() string          { return "ctx" }
func (c *taskCtx) Freeze()               {}
func (c *taskCtx) Truth() starlark.Bool  { return true }
func (c *taskCtx) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: ctx") }
func (c *taskCtx) AttrNames() []string   { return []string{"shell"} }

func (c *taskCtx) Attr(name string) (starlark.Value, error) {
	if name == "shell" {
		return starlark.NewBuiltin("ctx.shell", c.shell), nil
	}
	return nil, nil
}

func (c *taskCtx) shell(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var cmd string
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &cmd); err != nil {
		return nil, err
	}
	proc := exec.Command("bash", "-c", cmd)
	proc.Dir = c.dir
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	err := proc.Run()
	if err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func runStarlark(dir string) error {
	thread := &starlark.Thread{Name: "mirum"}
	globals, err := starlark.ExecFile(thread, filepath.Join(dir, cfg.Script), nil, nil)
	if err != nil {
		return err
	}

	projectFn, ok := globals["project"]
	if !ok {
		return fmt.Errorf("%s: project() not defined", cfg.Script)
	}
	fn, ok := projectFn.(starlark.Callable)
	if !ok {
		return fmt.Errorf("%s: project is not a function", cfg.Script)
	}

	ctx := &taskCtx{dir: dir}
	_, err = starlark.Call(thread, fn, starlark.Tuple{ctx}, nil)
	return err
}

func runCmd(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
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

		if len(cfg.Secret) > 0 && !verifySignature(body, r.Header.Get("X-Hub-Signature-256")) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		event := r.Header.Get("X-GitHub-Event")
		if event == "ping" {
			fmt.Fprintln(w, "pong")
			return
		}

		if event != "push" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var push pushEvent
		if err := json.Unmarshal(body, &push); err != nil {
			http.Error(w, "parse payload", http.StatusBadRequest)
			return
		}

		if push.After == "" || push.After == "0000000000000000000000000000000000000000" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if !strings.HasPrefix(push.Ref, "refs/heads/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		slog.Info("push", "repo", push.Repo.FullName, "ref", push.Ref, "sha", push.After[:8])
		w.WriteHeader(http.StatusAccepted)

		go processPush(push)
	})

	// gRPC server
	grpcLn, err := net.Listen("tcp", cfg.GrpcAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	grpcSrv := grpc.NewServer(grpc.StreamInterceptor(streamTimeoutInterceptor))
	pb.RegisterMirumServer(grpcSrv, &mirumServer{secret: []byte(cfg.Secret)})

	// HTTP server
	httpLn, err := socketActivationListener()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
	secret []byte
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

func streamTimeoutInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
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

func socketActivationListener() (net.Listener, error) {
	listeners, _ := activation.Listeners()
	if len(listeners) > 0 {
		return listeners[0], nil
	}
	return net.Listen("tcp", cfg.WwwAddr)
}
