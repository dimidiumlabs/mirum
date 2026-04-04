// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"dimidiumlabs/mirum/internal/database"
	"dimidiumlabs/mirum/internal/forges"
	"dimidiumlabs/mirum/internal/protocol/pb"
	"dimidiumlabs/mirum/internal/protocol/pb/pbconnect"
	"dimidiumlabs/mirum/internal/supervisor"

	"github.com/coreos/go-systemd/v22/activation"
	"github.com/spf13/cobra"
)

func main() {
	var socketPath string

	root := &cobra.Command{Use: "mirumd", Short: "Mirum CI server"}
	root.PersistentFlags().StringVar(&socketPath, "socket", "", "admin socket path (default from config or /run/mirumd/admin.sock)")

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

	workerCmd := &cobra.Command{Use: "worker", Short: "Manage workers"}
	root.AddCommand(workerCmd)

	workerAddCmd := &cobra.Command{
		Use:   "add",
		Short: "Register a worker",
		Run: func(cmd *cobra.Command, args []string) {
			pubkey, _ := cmd.Flags().GetString("pubkey")
			workerAdd(socketPath, pubkey)
		},
	}
	workerCmd.AddCommand(workerAddCmd)
	workerAddCmd.Flags().String("pubkey", "", "base64-encoded ed25519 public key")
	_ = workerAddCmd.MarkFlagRequired("pubkey")

	workerRevokeCmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke a worker",
		Run: func(cmd *cobra.Command, args []string) {
			id, _ := cmd.Flags().GetString("id")
			workerRevoke(socketPath, id)
		},
	}
	workerCmd.AddCommand(workerRevokeCmd)
	workerRevokeCmd.Flags().String("id", "", "worker ID")
	_ = workerRevokeCmd.MarkFlagRequired("id")

	workerListCmd := &cobra.Command{
		Use:   "list",
		Short: "List active workers",
		Run: func(cmd *cobra.Command, args []string) {
			workerList(socketPath)
		},
	}
	workerCmd.AddCommand(workerListCmd)

	orgCmd := &cobra.Command{Use: "org", Short: "Manage organizations"}
	root.AddCommand(orgCmd)

	orgCreateCmd := &cobra.Command{
		Use:   "create",
		Short: "Create an organization",
		Run: func(cmd *cobra.Command, args []string) {
			name, _ := cmd.Flags().GetString("name")
			slug, _ := cmd.Flags().GetString("slug")
			public, _ := cmd.Flags().GetBool("public")
			owner, _ := cmd.Flags().GetString("owner")
			orgCreate(socketPath, name, slug, public, owner)
		},
	}
	orgCmd.AddCommand(orgCreateCmd)
	orgCreateCmd.Flags().String("name", "", "display name")
	orgCreateCmd.Flags().String("slug", "", "URL slug")
	orgCreateCmd.Flags().Bool("public", false, "public visibility")
	orgCreateCmd.Flags().String("owner", "", "owner email")
	_ = orgCreateCmd.MarkFlagRequired("name")
	_ = orgCreateCmd.MarkFlagRequired("slug")
	_ = orgCreateCmd.MarkFlagRequired("owner")

	orgDeleteCmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete an organization",
		Run: func(cmd *cobra.Command, args []string) {
			slug, _ := cmd.Flags().GetString("slug")
			orgDelete(socketPath, slug)
		},
	}
	orgCmd.AddCommand(orgDeleteCmd)
	orgDeleteCmd.Flags().String("slug", "", "org slug")
	_ = orgDeleteCmd.MarkFlagRequired("slug")

	orgRenameCmd := &cobra.Command{
		Use:   "rename",
		Short: "Rename an organization",
		Run: func(cmd *cobra.Command, args []string) {
			slug, _ := cmd.Flags().GetString("slug")
			newName, _ := cmd.Flags().GetString("name")
			newSlug, _ := cmd.Flags().GetString("new-slug")
			orgRename(socketPath, slug, newName, newSlug)
		},
	}
	orgCmd.AddCommand(orgRenameCmd)
	orgRenameCmd.Flags().String("slug", "", "current slug")
	orgRenameCmd.Flags().String("name", "", "new display name")
	orgRenameCmd.Flags().String("new-slug", "", "new slug")
	_ = orgRenameCmd.MarkFlagRequired("slug")
	_ = orgRenameCmd.MarkFlagRequired("name")
	_ = orgRenameCmd.MarkFlagRequired("new-slug")

	orgListCmd := &cobra.Command{
		Use:   "list",
		Short: "List organizations",
		Run: func(cmd *cobra.Command, args []string) {
			orgList(socketPath)
		},
	}
	orgCmd.AddCommand(orgListCmd)

	orgMemberAddCmd := &cobra.Command{
		Use:   "add-member",
		Short: "Add a member to an organization",
		Run: func(cmd *cobra.Command, args []string) {
			org, _ := cmd.Flags().GetString("org")
			email, _ := cmd.Flags().GetString("email")
			role, _ := cmd.Flags().GetString("role")
			orgMemberAdd(socketPath, org, email, role)
		},
	}
	orgCmd.AddCommand(orgMemberAddCmd)
	orgMemberAddCmd.Flags().String("org", "", "org slug")
	orgMemberAddCmd.Flags().String("email", "", "user email")
	orgMemberAddCmd.Flags().String("role", "member", "role (owner, admin, member)")
	_ = orgMemberAddCmd.MarkFlagRequired("org")
	_ = orgMemberAddCmd.MarkFlagRequired("email")

	orgMemberRemoveCmd := &cobra.Command{
		Use:   "remove-member",
		Short: "Remove a member from an organization",
		Run: func(cmd *cobra.Command, args []string) {
			org, _ := cmd.Flags().GetString("org")
			email, _ := cmd.Flags().GetString("email")
			orgMemberRemove(socketPath, org, email)
		},
	}
	orgCmd.AddCommand(orgMemberRemoveCmd)
	orgMemberRemoveCmd.Flags().String("org", "", "org slug")
	orgMemberRemoveCmd.Flags().String("email", "", "user email")
	_ = orgMemberRemoveCmd.MarkFlagRequired("org")
	_ = orgMemberRemoveCmd.MarkFlagRequired("email")

	orgSetRoleCmd := &cobra.Command{
		Use:   "set-role",
		Short: "Change a member's role",
		Run: func(cmd *cobra.Command, args []string) {
			org, _ := cmd.Flags().GetString("org")
			email, _ := cmd.Flags().GetString("email")
			role, _ := cmd.Flags().GetString("role")
			orgMemberSetRole(socketPath, org, email, role)
		},
	}
	orgCmd.AddCommand(orgSetRoleCmd)
	orgSetRoleCmd.Flags().String("org", "", "org slug")
	orgSetRoleCmd.Flags().String("email", "", "user email")
	orgSetRoleCmd.Flags().String("role", "", "new role (owner, admin, member)")
	_ = orgSetRoleCmd.MarkFlagRequired("org")
	_ = orgSetRoleCmd.MarkFlagRequired("email")
	_ = orgSetRoleCmd.MarkFlagRequired("role")

	orgMemberListCmd := &cobra.Command{
		Use:   "list-members",
		Short: "List members of an organization",
		Run: func(cmd *cobra.Command, args []string) {
			org, _ := cmd.Flags().GetString("org")
			orgMemberList(socketPath, org)
		},
	}
	orgCmd.AddCommand(orgMemberListCmd)
	orgMemberListCmd.Flags().String("org", "", "org slug")
	_ = orgMemberListCmd.MarkFlagRequired("org")

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

	sup := supervisor.Detect()
	ctx := sup.WaitForStop(context.Background())

	db, err := database.Open(ctx, cfg.DatabaseUri)
	if err != nil {
		slog.Error("couldn't open database: %w", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		slog.Error("migration failed: %w", "err", err)
		os.Exit(1)
	}

	slog.Info("database ready")

	srv := &server{
		db:    db,
		cfg:   cfg,
		forge: &forges.GitHub{Secret: cfg.WebhookSecret, Token: cfg.GitHubToken},
		queue: make(chan *pb.Task, 100),
	}

	go srv.PurgeSessions(ctx)

	webSrv := NewWebServer(ctx, srv)
	grpcSrv := NewGrpcServer(ctx, srv)
	adminSrv := NewAdminServer(ctx, srv)

	grpcLn, webLn, adminLn, err := listeners(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	slog.Info("listening", "grpc", grpcLn.Addr(), "web", webLn.Addr(), "admin", cfg.AdminSocket)

	go func() {
		var err error
		if webSrv.TLSConfig != nil {
			err = webSrv.ServeTLS(webLn, "", "")
		} else {
			err = webSrv.Serve(webLn)
		}
		if err != nil && err != http.ErrServerClosed {
			slog.Error("web server failed", "err", err)
			os.Exit(1)
		}
	}()
	go func() {
		if err := grpcSrv.ServeTLS(grpcLn, "", ""); err != nil && err != http.ErrServerClosed {
			slog.Error("grpc server failed", "err", err)
			os.Exit(1)
		}
	}()
	go func() {
		if err := adminSrv.Serve(adminLn); err != nil && err != http.ErrServerClosed {
			slog.Error("admin server failed", "err", err)
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

	if err := webSrv.Shutdown(context.Background()); err != nil {
		slog.Error("web server shutdown", "err", err)
	}
	if err := grpcSrv.Shutdown(context.Background()); err != nil {
		slog.Error("grpc server shutdown", "err", err)
	}
	if err := adminSrv.Shutdown(context.Background()); err != nil {
		slog.Error("admin server shutdown", "err", err)
	}
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

	grp, err := user.LookupGroup("workerd")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("lookup group workerd: %w", err)
	}

	gid, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse gid: %w", err)
	}

	if err = os.Chown(cfg.AdminSocket, 0, gid); err != nil {
		return nil, nil, nil, fmt.Errorf("chown admin socket: %w", err)
	}

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

func cliUserRef(email string) *pb.UserRef {
	return &pb.UserRef{Ref: &pb.UserRef_Email{Email: email}}
}

func cliOrgRef(slug string) *pb.OrgRef {
	return &pb.OrgRef{Ref: &pb.OrgRef_Slug{Slug: slug}}
}

func cliRoleToProto(role string) pb.Role {
	switch role {
	case "owner":
		return pb.Role_ROLE_OWNER
	case "admin":
		return pb.Role_ROLE_ADMIN
	case "member":
		return pb.Role_ROLE_MEMBER
	default:
		return pb.Role_ROLE_NONE
	}
}

func cliUUID(b []byte) string {
	if len(b) == 16 {
		u, _ := uuid.FromBytes(b)
		return u.String()
	}
	return fmt.Sprintf("%x", b)
}

func userCreate(socketPath, email, password string) {
	resp, err := adminClient(socketPath).UserCreate(context.Background(), connect.NewRequest(&pb.UserCreateRequest{
		Email:    email,
		Password: password,
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(cliUUID(resp.Msg.Id))
}

func userSetPassword(socketPath, email, password string) {
	_, err := adminClient(socketPath).UserUpdate(context.Background(), connect.NewRequest(&pb.UserUpdateRequest{
		User:     cliUserRef(email),
		Password: &password,
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}

func userDelete(socketPath, email string) {
	_, err := adminClient(socketPath).UserDelete(context.Background(), connect.NewRequest(&pb.UserDeleteRequest{
		User: cliUserRef(email),
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}

func workerAdd(socketPath, pubkeyB64 string) {
	der, err := base64.StdEncoding.DecodeString(pubkeyB64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid base64:", err)
		os.Exit(1)
	}
	pubkey, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid public key:", err)
		os.Exit(1)
	}
	edKey, ok := pubkey.(ed25519.PublicKey)
	if !ok {
		fmt.Fprintln(os.Stderr, "not an ed25519 key")
		os.Exit(1)
	}
	resp, err := adminClient(socketPath).WorkerCreate(context.Background(), connect.NewRequest(&pb.WorkerCreateRequest{
		PublicKey: edKey,
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(cliUUID(resp.Msg.Id))
}

func workerRevoke(socketPath, id string) {
	uid, err := uuid.Parse(id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid uuid:", err)
		os.Exit(1)
	}
	_, err = adminClient(socketPath).WorkerDelete(context.Background(), connect.NewRequest(&pb.WorkerDeleteRequest{
		Id: uid[:],
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}

func workerList(socketPath string) {
	resp, err := adminClient(socketPath).WorkerList(context.Background(), connect.NewRequest(&pb.WorkerListRequest{}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, w := range resp.Msg.Workers {
		created := w.CreatedAt.AsTime().Format(time.DateOnly)
		fmt.Printf("%s\t%s\t%s\n", cliUUID(w.Id), base64.StdEncoding.EncodeToString(w.PublicKey), created)
	}
}

func orgCreate(socketPath, name, slug string, public bool, ownerEmail string) {
	resp, err := adminClient(socketPath).OrgCreate(context.Background(), connect.NewRequest(&pb.OrgCreateRequest{
		Name:   name,
		Slug:   slug,
		Public: public,
		Owner:  cliUserRef(ownerEmail),
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(cliUUID(resp.Msg.Id))
}

func orgDelete(socketPath, slug string) {
	_, err := adminClient(socketPath).OrgDelete(context.Background(), connect.NewRequest(&pb.OrgDeleteRequest{
		Org: cliOrgRef(slug),
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}

func orgRename(socketPath, slug, newName, newSlug string) {
	_, err := adminClient(socketPath).OrgUpdate(context.Background(), connect.NewRequest(&pb.OrgUpdateRequest{
		Org:  cliOrgRef(slug),
		Name: &newName,
		Slug: &newSlug,
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}

func orgList(socketPath string) {
	resp, err := adminClient(socketPath).OrgList(context.Background(), connect.NewRequest(&pb.OrgListRequest{}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, o := range resp.Msg.Organizations {
		visibility := "private"
		if o.Public {
			visibility = "public"
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", o.Slug, o.Name, visibility, o.CreatedAt.AsTime().Format(time.DateOnly))
	}
}

func orgMemberAdd(socketPath, orgSlug, email, role string) {
	_, err := adminClient(socketPath).OrgMemberAdd(context.Background(), connect.NewRequest(&pb.OrgMemberAddRequest{
		Org:  cliOrgRef(orgSlug),
		User: cliUserRef(email),
		Role: cliRoleToProto(role),
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}

func orgMemberRemove(socketPath, orgSlug, email string) {
	_, err := adminClient(socketPath).OrgMemberRemove(context.Background(), connect.NewRequest(&pb.OrgMemberRemoveRequest{
		Org:  cliOrgRef(orgSlug),
		User: cliUserRef(email),
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}

func orgMemberSetRole(socketPath, orgSlug, email, role string) {
	_, err := adminClient(socketPath).OrgMemberUpdate(context.Background(), connect.NewRequest(&pb.OrgMemberUpdateRequest{
		Org:  cliOrgRef(orgSlug),
		User: cliUserRef(email),
		Role: cliRoleToProto(role),
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}

func orgMemberList(socketPath, orgSlug string) {
	resp, err := adminClient(socketPath).OrgMemberList(context.Background(), connect.NewRequest(&pb.OrgMemberListRequest{
		Org: cliOrgRef(orgSlug),
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, m := range resp.Msg.Members {
		fmt.Printf("%s\t%s\t%s\n", m.User.Email, m.Role, m.JoinedAt.AsTime().Format(time.DateOnly))
	}
}
