// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"dimidiumlabs/mirum/internal/database"
	"dimidiumlabs/mirum/internal/forges"
	"dimidiumlabs/mirum/internal/protocol/pb"
	"dimidiumlabs/mirum/internal/supervisor"

	"github.com/coreos/go-systemd/v22/activation"
)

func main() {
	configFile := flag.String("config", "", "path to config file")
	flag.Parse()

	if *configFile == "" {
		slog.Error("-config required")
		os.Exit(1)
	}

	cfg, err := getConfig(*configFile)
	if err != nil {
		slog.Error("config parsing failed", "err", err)
		os.Exit(1)
	}

	slog.Info("config loaded", "configfile", *configFile)

	db, err := database.Open(context.Background(), cfg.DatabaseUri)
	if err != nil {
		slog.Error("couldn't open database: %w", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Migrate(context.Background()); err != nil {
		slog.Error("migration failed: %w", "err", err)
		os.Exit(1)
	}

	slog.Info("database ready")

	srv := &server{
		cfg:   cfg,
		db:    db,
		forge: &forges.GitHub{Secret: cfg.WebhookSecret, Token: cfg.GitHubToken},
		queue: make(chan *pb.Task, 100),
	}

	sup := supervisor.Detect()
	ctx := sup.WaitForStop(context.Background())

	wwwSrv := NewWwwServer(ctx, srv)
	grpcSrv := NewGrpcServer(ctx, srv, []byte(cfg.WorkerSecret))

	grpcLn, webLn, err := listeners(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	slog.Info("listening", "grpc", grpcLn.Addr(), "web", webLn.Addr())

	go func() {
		if err := wwwSrv.Serve(webLn); err != nil && err != http.ErrServerClosed {
			slog.Error("web server failed", "err", err)
			os.Exit(1)
		}
	}()
	go func() {
		if err := grpcSrv.Serve(grpcLn); err != nil {
			slog.Error("grpc server failed", "err", err)
			os.Exit(1)
		}
	}()

	sup.Ready()
	go sup.StartWatchdog(ctx)

	<-ctx.Done()
	slog.Info("shutting down")
	sup.Stopping()

	// Hard deadline: if graceful shutdown takes too long, exit.
	time.AfterFunc(30*time.Second, func() {
		slog.Error("shutdown timed out, forcing exit")
		os.Exit(1)
	})

	srv.Close()

	wwwSrv.Shutdown(ctx)
	grpcSrv.GracefulStop()
}

// listeners returns gRPC and HTTP listeners.
// With systemd socket activation it expects two named fds: "grpc" and "http".
// Without socket activation it falls back to configured addresses.
func listeners(cfg *config) (grpcLn, webLn net.Listener, err error) {
	named, err := activation.ListenersWithNames()
	if err != nil {
		return nil, nil, fmt.Errorf("socket activation: %w", err)
	}

	if lns := named["grpc"]; len(lns) > 0 {
		grpcLn = lns[0]
	} else if grpcLn, err = net.Listen("tcp", cfg.GrpcAddr); err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			_ = grpcLn.Close()
		}
	}()

	if lns := named["web"]; len(lns) > 0 {
		webLn = lns[0]
	} else if webLn, err = net.Listen("tcp", cfg.WwwAddr); err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			_ = webLn.Close()
		}
	}()

	return grpcLn, webLn, nil
}
