// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
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
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	var socketPath string

	root := &cobra.Command{Use: "mirumd", Short: "Mirum CI server"}
	root.PersistentFlags().StringVar(&socketPath, "socket", "", "admin socket path (default from config or /run/mirum/admin.sock)")

	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the server",
		Run: func(cmd *cobra.Command, args []string) {
			configFile, _ := cmd.Flags().GetString("config")
			daemon(configFile, socketPath)
		},
	}
	root.AddCommand(daemonCmd)
	daemonCmd.Flags().String("config", "", "path to config file")
	_ = daemonCmd.MarkFlagRequired("config")

	userCmd := &cobra.Command{Use: "user", Short: "Manage users"}
	root.AddCommand(userCmd)

	userCreateCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a user",
		Run: func(cmd *cobra.Command, args []string) {
			email, _ := cmd.Flags().GetString("email")
			password, _ := cmd.Flags().GetString("password")
			userCreate(socketPath, email, password)
		},
	}
	userCmd.AddCommand(userCreateCmd)
	userCreateCmd.Flags().String("email", "", "user email")
	userCreateCmd.Flags().String("password", "", "user password")
	_ = userCreateCmd.MarkFlagRequired("email")
	_ = userCreateCmd.MarkFlagRequired("password")

	setPasswordCmd := &cobra.Command{
		Use:   "set-password",
		Short: "Set user password",
		Run: func(cmd *cobra.Command, args []string) {
			email, _ := cmd.Flags().GetString("email")
			password, _ := cmd.Flags().GetString("password")
			userSetPassword(socketPath, email, password)
		},
	}
	userCmd.AddCommand(setPasswordCmd)
	setPasswordCmd.Flags().String("email", "", "user email")
	setPasswordCmd.Flags().String("password", "", "new password")
	_ = setPasswordCmd.MarkFlagRequired("email")
	_ = setPasswordCmd.MarkFlagRequired("password")

	deleteUserCmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a user",
		Run: func(cmd *cobra.Command, args []string) {
			email, _ := cmd.Flags().GetString("email")
			userDelete(socketPath, email)
		},
	}
	userCmd.AddCommand(deleteUserCmd)
	deleteUserCmd.Flags().String("email", "", "user email")
	_ = deleteUserCmd.MarkFlagRequired("email")

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func daemon(configFile, socketFlag string) {
	cfg, err := getConfig(configFile)
	if err != nil {
		slog.Error("config parsing failed", "err", err)
		os.Exit(1)
	}

	if socketFlag != "" {
		cfg.AdminSocket = socketFlag
	}

	slog.Info("config loaded", "configfile", configFile)

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
	adminSrv := NewAdminServer(srv)

	grpcLn, webLn, adminLn, err := listeners(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	slog.Info("listening", "grpc", grpcLn.Addr(), "web", webLn.Addr(), "admin", cfg.AdminSocket)

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
	go func() {
		if err := adminSrv.Serve(adminLn); err != nil {
			slog.Error("admin server failed", "err", err)
			os.Exit(1)
		}
	}()

	go srv.PurgeSessions(ctx)

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

	adminSrv.GracefulStop()
	wwwSrv.Shutdown(context.Background())
	grpcSrv.GracefulStop()
}

// listeners returns gRPC, web, and admin listeners.
// With systemd socket activation it expects two named fds: "grpc" and "web".
// Without socket activation it falls back to configured addresses.
func listeners(cfg *config) (grpcLn, webLn, adminLn net.Listener, err error) {
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
		if err != nil {
			_ = grpcLn.Close()
		}
	}()

	if lns := named["web"]; len(lns) > 0 {
		webLn = lns[0]
	} else if webLn, err = net.Listen("tcp", cfg.WwwAddr); err != nil {
		return nil, nil, nil, err
	}
	defer func() {
		if err != nil {
			_ = webLn.Close()
		}
	}()

	if adminLn, err = net.Listen("unix", cfg.AdminSocket); err != nil {
		return nil, nil, nil, err
	}
	defer func() {
		if err != nil {
			_ = adminLn.Close()
		}
	}()

	return grpcLn, webLn, adminLn, nil
}

func adminClient(socketPath string) pb.AdminClient {
	if socketPath == "" {
		socketPath = "/run/mirum/admin.sock"
	}
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return pb.NewAdminClient(conn)
}

func userCreate(socketPath, email, password string) {
	resp, err := adminClient(socketPath).CreateUser(context.Background(), &pb.CreateUserRequest{
		Email:    email,
		Password: password,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(resp.Id)
}

func userSetPassword(socketPath, email, password string) {
	_, err := adminClient(socketPath).SetPassword(context.Background(), &pb.SetPasswordRequest{
		Email:    email,
		Password: password,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}

func userDelete(socketPath, email string) {
	_, err := adminClient(socketPath).DeleteUser(context.Background(), &pb.DeleteUserRequest{
		Email: email,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}
