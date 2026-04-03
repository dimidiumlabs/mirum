// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/secret"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"dimidiumlabs/mirum/internal/executor"
	"dimidiumlabs/mirum/internal/protocol"
	"dimidiumlabs/mirum/internal/protocol/pb"
	"dimidiumlabs/mirum/internal/protocol/pb/pbconnect"
)

type client struct {
	cfg     *config
	http    *http.Client
	handle  pbconnect.MirumClient
	tlsMu   sync.Mutex
	tlsConn *tls.Conn
}

func dial(ctx context.Context, cfg *config) (*client, error) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13, // required for reliable EKM (channel binding)
		NextProtos: []string{"h2"},   // required for gRPC over TLS (ALPN)
	}
	if cfg.TLSCA != "" {
		caCert, err := os.ReadFile(cfg.TLSCA)
		if err != nil {
			return nil, fmt.Errorf("read CA cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA cert")
		}
		tlsCfg.RootCAs = pool
	}

	c := &client{cfg: cfg}

	c.http = &http.Client{
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, err := tls.Dial(network, addr, tlsCfg)
				if err != nil {
					return nil, err
				}
				c.tlsMu.Lock()
				c.tlsConn = conn
				c.tlsMu.Unlock()
				return conn, nil
			},
			ForceAttemptHTTP2: true,
		},
	}
	c.handle = pbconnect.NewMirumClient(c.http, "https://"+cfg.Server, connect.WithGRPC())

	slog.Info("dialing", "server", cfg.Server)

	hsCtx, hsCancel := context.WithTimeout(ctx, 30*time.Second)
	defer hsCancel()

	if err := c.handshake(hsCtx); err != nil {
		c.close()
		return nil, err
	}

	return c, nil
}

func (c *client) close() {
	c.tlsMu.Lock()
	conn := c.tlsConn
	c.tlsMu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

func (c *client) work(ctx context.Context) error {
	for ctx.Err() == nil {
		resp, err := c.handle.Poll(ctx, connect.NewRequest(&pb.PollRequest{}))
		if err != nil {
			return fmt.Errorf("poll: %w", err)
		}
		task := resp.Msg

		slog.Info("task received", "id", task.Id, "repo", task.RepoFullName)

		execErr := executor.Run(task.CloneUrl, task.Branch)

		result := &pb.TaskResult{TaskId: task.Id, Success: execErr == nil}
		if execErr != nil {
			result.Error = execErr.Error()
			slog.Error("task failed", "id", task.Id, "err", execErr)
		} else {
			slog.Info("task passed", "id", task.Id)
		}

		if _, err := c.handle.Complete(ctx, connect.NewRequest(result)); err != nil {
			return fmt.Errorf("complete: %w", err)
		}
	}
	return ctx.Err()
}

func (c *client) handshake(ctx context.Context) error {
	stream := c.handle.Handshake(ctx)

	// Step 1: send worker public key
	pubKey, err := protocol.LoadPublicKey(c.cfg.PubKeyFile)
	if err != nil {
		return fmt.Errorf("load public key: %w", err)
	}
	if err := stream.Send(&pb.HandshakeIn{
		Step: &pb.HandshakeIn_WorkerChallenge{
			WorkerChallenge: &pb.WorkerChallenge{PublicKey: pubKey},
		},
	}); err != nil {
		return fmt.Errorf("send challenge: %w", err)
	}

	// Step 2: receive server nonce
	out, err := stream.Receive()
	if err != nil {
		return fmt.Errorf("recv server challenge: %w", err)
	}
	sc := out.GetServerChallenge()
	if sc == nil {
		return fmt.Errorf("expected ServerChallenge")
	}

	// Extract EKM if server requested channel binding
	var ekm []byte
	if sc.GetBinded() {
		c.tlsMu.Lock()
		conn := c.tlsConn
		c.tlsMu.Unlock()
		if conn == nil {
			return fmt.Errorf("channel binding requested but TLS connection not available")
		}
		state := conn.ConnectionState()
		ekm, err = state.ExportKeyingMaterial(protocol.EKMLabel, nil, protocol.EKMLength)
		if err != nil {
			return fmt.Errorf("export keying material: %w", err)
		}
	}

	// Step 3: sign nonce (+ optional EKM) inside secret.Do
	var signature []byte
	var signErr error
	secret.Do(func() {
		key, err := protocol.LoadPrivateKey(c.cfg.KeyFile)
		if err != nil {
			signErr = fmt.Errorf("load key: %w", err)
			return
		}
		hs := protocol.NewClientHandshake(key)
		signature, signErr = hs.Sign(sc.GetNonce(), ekm)
	})
	if signErr != nil {
		return signErr
	}

	name := c.cfg.Name
	if name == "" {
		name, _ = os.Hostname()
	}
	if err := stream.Send(&pb.HandshakeIn{
		Step: &pb.HandshakeIn_WorkerProof{
			WorkerProof: &pb.WorkerProof{
				Signature:  signature,
				Name:       name,
				Version:    protocol.VersionProto(),
				Os:         protocol.DetectOs(),
				Arch:       protocol.DetectArch(),
				Runtime:    workerRuntime,
				WorkerTime: timestamppb.Now(),
			},
		},
	}); err != nil {
		return fmt.Errorf("send proof: %w", err)
	}

	// Step 4: receive result
	out, err = stream.Receive()
	if err != nil {
		return fmt.Errorf("recv result: %w", err)
	}
	sr := out.GetServerResult()
	if sr == nil {
		return fmt.Errorf("expected ServerResult")
	}
	if sr.Error != nil {
		return fmt.Errorf("%w: %s", protocol.ErrServerRejected, *sr.Error)
	}

	for _, w := range sr.GetWarnings() {
		slog.Warn("server warning", "msg", w)
	}

	if err := stream.CloseRequest(); err != nil {
		return fmt.Errorf("close stream: %w", err)
	}
	if err := stream.CloseResponse(); err != nil {
		return fmt.Errorf("close stream: %w", err)
	}

	v := sr.GetServerVersion()
	slog.Info("handshake ok", "server_version", fmt.Sprintf("%d.%d.%d", v.GetMajor(), v.GetMinor(), v.GetPatch()))

	return nil
}
