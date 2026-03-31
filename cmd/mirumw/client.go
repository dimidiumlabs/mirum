// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"mrdimidium/mirum/internal/protocol"
	"mrdimidium/mirum/internal/protocol/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type client struct {
	cfg    *config
	conn   *grpc.ClientConn
	handle pb.MirumClient
}

func connect(ctx context.Context, cfg *config) (*client, error) {
	var creds grpc.DialOption
	if cfg.Insecure {
		host, _, _ := net.SplitHostPort(cfg.Server)
		if !isPrivateHost(host) {
			return nil, fmt.Errorf("tls_insecure is only allowed for private/loopback addresses, got %s", host)
		}

		slog.Warn("TLS disabled, connection is not encrypted")
		creds = grpc.WithTransportCredentials(insecure.NewCredentials())
	} else {
		creds = grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{}))
	}

	conn, err := grpc.NewClient(cfg.Server, creds)
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

func (c *client) handshake(ctx context.Context) error {
	stream, err := c.handle.Handshake(ctx)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}

	// Step 1: send worker nonce
	workerNonce, err := protocol.GenerateNonce()
	if err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	if err := stream.Send(&pb.HandshakeIn{
		Step: &pb.HandshakeIn_WorkerChallenge{
			WorkerChallenge: &pb.WorkerChallenge{Nonce: workerNonce},
		},
	}); err != nil {
		return fmt.Errorf("send challenge: %w", err)
	}

	// Step 2: receive server challenge, verify server
	out, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("recv server challenge: %w", err)
	}

	sc := out.GetServerChallenge()
	if sc == nil {
		return fmt.Errorf("expected ServerChallenge")
	}
	if !protocol.VerifyProof([]byte(c.cfg.Secret), workerNonce, sc.GetNonce(), sc.GetProof()) {
		return fmt.Errorf("server proof verification failed")
	}

	// Step 3: send worker proof + metadata
	name := c.cfg.Name
	if name == "" {
		name, _ = os.Hostname()
	}
	if err := stream.Send(&pb.HandshakeIn{
		Step: &pb.HandshakeIn_WorkerProof{
			WorkerProof: &pb.WorkerProof{
				Proof:      protocol.ComputeProof([]byte(c.cfg.Secret), sc.GetNonce(), workerNonce),
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
		return fmt.Errorf("server rejected: %s", *sr.Error)
	}

	v := sr.GetServerVersion()
	slog.Info("handshake ok", "server_version", fmt.Sprintf("%d.%d.%d", v.GetMajor(), v.GetMinor(), v.GetPatch()))
	return nil
}
