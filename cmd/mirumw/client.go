// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"runtime/secret"
	"time"

	"dimidiumlabs/mirum/internal/executor"
	"dimidiumlabs/mirum/internal/protocol"
	"dimidiumlabs/mirum/internal/protocol/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type client struct {
	cfg    *config
	conn   *grpc.ClientConn
	handle pb.MirumClient
}

func connect(ctx context.Context, cfg *config) (*client, error) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13, // required for reliable EKM (channel binding)
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

	conn, err := grpc.NewClient(cfg.Server,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		return nil, err
	}

	c := &client{
		cfg:    cfg,
		conn:   conn,
		handle: pb.NewMirumClient(conn),
	}

	hsCtx, hsCancel := context.WithTimeout(ctx, 30*time.Second)
	defer hsCancel()

	if err := c.handshake(hsCtx); err != nil {
		if err := c.close(); err != nil {
			slog.Warn("handshake: %w", "err", err)
		}

		return nil, err
	}

	return c, nil
}

func (c *client) close() error {
	return c.conn.Close()
}

func (c *client) work(ctx context.Context) error {
	for ctx.Err() == nil {
		task, err := c.handle.Poll(ctx, &pb.PollRequest{})
		if err != nil {
			return fmt.Errorf("poll: %w", err)
		}

		slog.Info("task received", "id", task.Id, "repo", task.RepoFullName)

		execErr := executor.Run(task.CloneUrl, task.Branch)

		result := &pb.TaskResult{TaskId: task.Id, Success: execErr == nil}
		if execErr != nil {
			result.Error = execErr.Error()
			slog.Error("task failed", "id", task.Id, "err", execErr)
		} else {
			slog.Info("task passed", "id", task.Id)
		}

		if _, err := c.handle.Complete(ctx, result); err != nil {
			return fmt.Errorf("complete: %w", err)
		}
	}
	return ctx.Err()
}

func (c *client) handshake(ctx context.Context) error {
	var p peer.Peer
	stream, err := c.handle.Handshake(ctx, grpc.Peer(&p))
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}

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
	out, err := stream.Recv()
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
		ti, ok := p.AuthInfo.(credentials.TLSInfo)
		if !ok {
			return fmt.Errorf("channel binding requested but TLS info not available")
		}
		ekm, err = ti.State.ExportKeyingMaterial(protocol.EKMLabel, nil, protocol.EKMLength)
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
	out, err = stream.Recv()
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

	v := sr.GetServerVersion()
	slog.Info("handshake ok", "server_version", fmt.Sprintf("%d.%d.%d", v.GetMajor(), v.GetMinor(), v.GetPatch()))
	return nil
}
