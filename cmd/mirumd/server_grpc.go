// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"

	"dimidiumlabs/mirum/internal/config"
	"dimidiumlabs/mirum/internal/protocol"
	"dimidiumlabs/mirum/internal/protocol/pb"
	"dimidiumlabs/mirum/internal/protocol/pb/pbconnect"
)

func NewGrpcServer(ctx context.Context, srv *server) *http.Server {
	gsrv := &grpcService{srv: srv}

	path, handler := pbconnect.NewMirumHandler(gsrv,
		connect.WithInterceptors(validate.NewInterceptor()),
	)

	mux := http.NewServeMux()
	mux.Handle(path, workerLog(handler))

	certs := newCertReloader(srv.cfg.GrpcTls.Cert, srv.cfg.GrpcTls.Key)

	return &http.Server{
		Handler: mux,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
		TLSConfig: &tls.Config{
			NextProtos:     []string{"h2"},
			MinVersion:     tls.VersionTLS13,
			ClientAuth:     tls.RequireAnyClientCert,
			GetCertificate: certs.GetCertificate,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return errors.New("client certificate required")
				}

				c, err := x509.ParseCertificate(rawCerts[0])
				if err != nil {
					return fmt.Errorf("parse client cert: %w", err)
				}

				pubKey, ok := c.PublicKey.(ed25519.PublicKey)
				if !ok {
					return errors.New("ed25519 certificate required")
				}

				if _, err := srv.db.WorkerLookup(context.Background(), SystemActor(), pubKey); err != nil {
					return fmt.Errorf("unknown worker: %w", err)
				}

				// Clock skew: NotBefore is set to time.Now() when the cert was generated.
				// Checked here (once per TLS handshake), not in the interceptor,
				// because HTTP/2 reuses the connection and NotBefore would go stale.
				if skew := time.Since(c.NotBefore).Abs(); skew > config.WorkerClockSkewLimit {
					return fmt.Errorf("%w: %s", protocol.ErrClockSkew, skew.Truncate(time.Second))
				}

				return nil
			},
		},
	}
}

// grpcService is the ConnectRPC transport adapter over server.
type grpcService struct {
	pbconnect.UnimplementedMirumHandler
	srv *server
}

func (g *grpcService) Poll(ctx context.Context, req *connect.Request[pb.PollRequest]) (*connect.Response[pb.Task], error) {
	select {
	case task, ok := <-g.srv.queue:
		if !ok {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("server is shutting down"))
		}
		slog.Info("task dispatched", "id", task.Id, "repo", task.RepoFullName)
		return connect.NewResponse(task), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *grpcService) Complete(ctx context.Context, req *connect.Request[pb.TaskResult]) (*connect.Response[pb.CompleteResponse], error) {
	if err := g.srv.complete(ctx, req.Msg.TaskId, req.Msg.Success, req.Msg.Error); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.CompleteResponse{}), nil
}

// workerLog logs worker metadata from the mTLS client certificate
// and sets the server version response header.
func workerLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			if meta := protocol.ParseWorkerMeta(r.TLS.PeerCertificates[0]); meta != nil {
				slog.Info("worker request",
					"name", meta.Name,
					"version", meta.Version,
					"path", r.URL.Path,
				)
			}
		}
		w.Header().Set("X-Server-Version", protocol.VersionString())
		next.ServeHTTP(w, r)
	})
}
