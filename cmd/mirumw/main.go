// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"mrdimidium/mirum/internal/protocol"
	"mrdimidium/mirum/internal/protocol/pb"
	"mrdimidium/mirum/internal/supervisor"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gopkg.in/yaml.v3"
)

// Runtime type of this worker binary. Different worker types
// (mirumw-vm, mirumw-docker, etc.) will have different values.
const workerRuntime = "host"

type config struct {
	Name     string `yaml:"name"`
	Server   string `yaml:"server"`
	Secret   string `yaml:"secret"`
	Insecure bool   `yaml:"tls_insecure"`
}

func GetConfig(filename string) (*config, error) {
	cfg := &config{
		Server: "localhost:2026",
	}

	if filename != "" {
		data, err := os.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("couldn't read config: %w", err)
		}

		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("couldn't parse config: %w", err)
		}
	}

	return cfg, nil
}

type client struct {
	cfg    *config
	conn   *grpc.ClientConn
	handle pb.MirumClient
}

func ClientConnect(cfg *config) (*client, error) {
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

	return &client{
		cfg:    cfg,
		conn:   conn,
		handle: pb.NewMirumClient(conn),
	}, nil
}

func (c *client) Close() error {
	return c.conn.Close()
}

func (c *client) Handshake(ctx context.Context) error {
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

func main() {
	configFile := flag.String("config", "", "path to config file")
	flag.Parse()

	sup := supervisor.Detect()
	ctx := sup.WaitForStop(context.Background())

	cfg, err := GetConfig(*configFile)
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	client, err := ClientConnect(cfg)
	if err != nil {
		slog.Error("failed grpc client", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := client.Close(); err != nil {
			slog.Warn("close grpc client", "err", err)
		}
	}()

	handshakeCtx, handshakeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer handshakeCancel()
	if err := client.Handshake(handshakeCtx); err != nil {
		slog.Error("handshake failed", "err", err)
		os.Exit(1)
	} else {
		slog.Info("connected", "server", cfg.Server)
	}

	sup.Ready()
	go sup.StartWatchdog()

	<-ctx.Done()
	slog.Info("shutting down")
	sup.Stopping()
}

func isPrivateHost(host string) bool {
	ips, err := net.LookupIP(host)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() {
			return false
		}
	}
	return len(ips) > 0
}
