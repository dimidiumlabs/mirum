// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"

	"dimidiumlabs/mirum/internal/config"
	"dimidiumlabs/mirum/internal/forges"
	"dimidiumlabs/mirum/internal/protocol/pb"
	"dimidiumlabs/mirum/internal/protocol/pb/pbconnect"
	"dimidiumlabs/mirum/internal/supervisor"

	"github.com/coreos/go-systemd/v22/activation"
	"github.com/spf13/cobra"
)

func hardenServer(s *http.Server) *http.Server {
	s.IdleTimeout = config.HTTPIdleTimeout
	s.MaxHeaderBytes = config.HTTPMaxHeaderBytes
	s.ReadHeaderTimeout = config.HTTPReadHeaderTimeout
	return s
}

func main() {
	var socketPath string

	root := &cobra.Command{Use: "mirumd", Short: "Mirum CI server"}
	root.PersistentFlags().StringVar(&socketPath, "socket", "", "admin socket path (default from config or /run/mirumd/admin.sock)")
	root.AddGroup(&cobra.Group{ID: "main", Title: "Commands:"})

	daemonCmd := &cobra.Command{
		Use:           "daemon",
		Short:         "Start the server",
		GroupID:       "main",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			configFile, _ := cmd.Flags().GetString("config")
			return daemon(configFile, socketPath)
		},
	}
	daemonCmd.Flags().String("config", "", "path to config file")
	_ = daemonCmd.MarkFlagRequired("config")
	root.AddCommand(daemonCmd)

	// Admin subcommands are generated from admin.proto via reflection.
	buildAdminCLI(root, func() pbconnect.AdminClient { return adminClient(socketPath) })
	for _, c := range root.Commands() {
		if c.GroupID == "" {
			c.GroupID = "main"
		}
	}

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func daemon(configFile, socketFlag string) error {
	cfg, err := getConfig(configFile)
	if err != nil {
		slog.Error("config parsing failed", "err", err)
		return err
	}

	if socketFlag != "" {
		cfg.AdminSocket = socketFlag
	}

	slog.Info("config loaded", "configfile", configFile)

	sup := supervisor.Detect()
	ctx, cancel := context.WithCancel(sup.WaitForStop(context.Background()))
	defer cancel()

	db, err := DatabaseOpen(ctx, cfg.DatabaseUri)
	if err != nil {
		slog.Error("couldn't open database", "err", err)
		return err
	}

	srv := &server{
		db:    db,
		cfg:   cfg,
		forge: &forges.GitHub{Secret: cfg.WebhookSecret, Token: cfg.GitHubToken},
		queue: make(chan *pb.Task, config.TaskQueueCapacity),
	}
	defer srv.Close()

	if err := db.Migrate(ctx); err != nil {
		slog.Error("migration failed", "err", err)
		return err
	}

	slog.Info("database ready")

	go srv.PurgeSessions(ctx)

	adminPath, adminHandler := NewAdminHandler(srv)

	webSrv := hardenServer(NewWebServer(ctx, srv, adminPath, adminHandler))
	grpcSrv := hardenServer(NewGrpcServer(ctx, srv))

	adminMux := http.NewServeMux()
	adminMux.Handle(adminPath, adminHandler)
	adminSrv := hardenServer(&http.Server{
		Handler: adminMux,
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, actorKey{}, OperatorActor())
		},
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	})

	grpcLn, webLn, adminLn, err := listeners(cfg)
	if err != nil {
		slog.Error("listeners failed", "err", err)
		return err
	}

	slog.Info("listening", "grpc", grpcLn.Addr(), "web", webLn.Addr(), "admin", cfg.AdminSocket)

	errs := make(chan error, 3)
	serve := func(name string, fn func() error) {
		go func() {
			err := fn()
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			if err != nil {
				err = fmt.Errorf("%s server: %w", name, err)
			}
			errs <- err
		}()
	}
	serve("web", func() error {
		if webSrv.TLSConfig != nil {
			return webSrv.ServeTLS(webLn, "", "")
		}
		return webSrv.Serve(webLn)
	})
	serve("grpc", func() error { return grpcSrv.ServeTLS(grpcLn, "", "") })
	serve("admin", func() error { return adminSrv.Serve(adminLn) })

	sup.Ready()
	go sup.StartWatchdog(ctx)

	var runErr error
	select {
	case <-ctx.Done():
		slog.Info("shutting down")
	case err := <-errs:
		runErr = err
		// Propagate the crash to all handler contexts so Poll and
		// other long-lived RPCs exit via ctx.Done(); Shutdown below
		// then completes without waiting on them.
		cancel()
		if err != nil {
			slog.Error("server exited, shutting down peers", "err", err)
		} else {
			slog.Warn("server exited unexpectedly, shutting down peers")
		}
	}

	sup.Stopping()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), config.HTTPShutdownTimeout)
	defer shutdownCancel()

	shutdown := func(name string, s *http.Server) {
		if err := s.Shutdown(shutdownCtx); err != nil {
			slog.Error("server shutdown", "name", name, "err", err)
		}
	}

	shutdown("web", webSrv)
	shutdown("grpc", grpcSrv)
	shutdown("admin", adminSrv)

	return runErr
}

// listeners returns gRPC, web, and admin listeners.
// With systemd socket activation it expects two named fds: "grpc" and "web".
// Without socket activation it falls back to configured addresses.
func listeners(cfg *appConfig) (grpcLn, webLn, adminLn net.Listener, err error) {
	named, err := activation.ListenersWithNames()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("socket activation: %w", err)
	}

	if lns := named["grpc"]; len(lns) > 0 {
		grpcLn = lns[0]
	} else if grpcLn, err = net.Listen("tcp", cfg.GrpcAddr); err != nil {
		return nil, nil, nil, err
	}
	defer func() {
		if err != nil && grpcLn != nil {
			_ = grpcLn.Close()
		}
	}()

	if lns := named["web"]; len(lns) > 0 {
		webLn = lns[0]
	} else if webLn, err = net.Listen("tcp", cfg.WebAddr); err != nil {
		return nil, nil, nil, err
	}
	defer func() {
		if err != nil && webLn != nil {
			_ = webLn.Close()
		}
	}()

	_ = os.Remove(cfg.AdminSocket)
	if adminLn, err = net.Listen("unix", cfg.AdminSocket); err != nil {
		return nil, nil, nil, err
	}
	defer func() {
		if err != nil && adminLn != nil {
			_ = adminLn.Close()
		}
	}()

	if err = os.Chmod(cfg.AdminSocket, 0o660); err != nil {
		return nil, nil, nil, fmt.Errorf("chmod admin socket: %w", err)
	}

	return grpcLn, webLn, adminLn, nil
}

func adminClient(socketPath string) pbconnect.AdminClient {
	if socketPath == "" {
		socketPath = "/run/mirumd/admin.sock"
	}
	return pbconnect.NewAdminClient(
		&http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		},
		"http://localhost.unix",
	)
}
