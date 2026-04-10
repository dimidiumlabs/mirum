// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"

	"connectrpc.com/connect"

	"dimidiumlabs/mirum/internal/executor"
	"dimidiumlabs/mirum/internal/protocol"
	"dimidiumlabs/mirum/internal/protocol/wirepb"
	"dimidiumlabs/mirum/internal/protocol/wirepb/wirepbconnect"
)

type client struct {
	cfg    *config
	http   *http.Client
	handle wirepbconnect.WorkerClient
}

func dial(ctx context.Context, cfg *config) (*client, error) {
	name := cfg.Name
	if name == "" {
		name, _ = os.Hostname()
	}

	meta := &protocol.WorkerMeta{
		Os:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Name:    name,
		Runtime: workerRuntime,
		Version: protocol.VersionString(),
	}

	tlsCfg := &tls.Config{
		NextProtos: []string{"h2"},
		MinVersion: tls.VersionTLS13,
		GetClientCertificate: func(_ *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			key, err := protocol.LoadPrivateKey(cfg.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("load key: %w", err)
			}

			cert, err := protocol.SelfSignedCert(key, meta)
			return &cert, err
		},
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

	c := &client{
		cfg: cfg,
		http: &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg, ForceAttemptHTTP2: true},
		},
	}
	c.handle = wirepbconnect.NewWorkerClient(c.http, "https://"+cfg.Server, connect.WithGRPC())

	slog.Info("dialing", "server", cfg.Server)

	return c, nil
}

func (c *client) close() {}

func (c *client) work(ctx context.Context) error {
	for ctx.Err() == nil {
		resp, err := c.handle.Poll(ctx, connect.NewRequest(&wirepb.PollRequest{}))
		if err != nil {
			return fmt.Errorf("poll: %w", err)
		}

		for _, w := range resp.Header().Values("X-Warning") {
			slog.Warn("server warning", "msg", w)
		}

		task := resp.Msg
		slog.Info("task received", "id", task.Id, "repo", task.RepoFullName)

		execErr := executor.Run(task.CloneUrl, task.Branch)

		result := &wirepb.TaskResult{TaskId: task.Id, Success: execErr == nil}
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
