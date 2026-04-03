// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"dimidiumlabs/mirum/internal/protocol/pb"
	"dimidiumlabs/mirum/internal/protocol/pb/pbconnect"
)

func NewAdminServer(srv *server) *http.Server {
	as := &adminService{srv: srv}
	path, handler := pbconnect.NewAdminHandler(as)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	return &http.Server{Handler: mux}
}

type adminService struct {
	pbconnect.UnimplementedAdminHandler
	srv *server
}

func (a *adminService) CreateUser(ctx context.Context, req *connect.Request[pb.CreateUserRequest]) (*connect.Response[pb.CreateUserResponse], error) {
	id, err := a.srv.db.CreateUser(ctx, req.Msg.Email, req.Msg.Password, []byte(a.srv.cfg.Pepper))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.CreateUserResponse{Id: id}), nil
}

func (a *adminService) SetPassword(ctx context.Context, req *connect.Request[pb.SetPasswordRequest]) (*connect.Response[pb.SetPasswordResponse], error) {
	if err := a.srv.db.SetPassword(ctx, req.Msg.Email, req.Msg.Password, []byte(a.srv.cfg.Pepper)); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.SetPasswordResponse{}), nil
}

func (a *adminService) DeleteUser(ctx context.Context, req *connect.Request[pb.DeleteUserRequest]) (*connect.Response[pb.DeleteUserResponse], error) {
	if err := a.srv.db.DeleteUser(ctx, req.Msg.Email); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.DeleteUserResponse{}), nil
}

func (a *adminService) WorkerAdd(ctx context.Context, req *connect.Request[pb.WorkerAddRequest]) (*connect.Response[pb.WorkerAddResponse], error) {
	if len(req.Msg.PublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key: expected %d bytes, got %d", ed25519.PublicKeySize, len(req.Msg.PublicKey))
	}
	id, err := a.srv.db.AddWorker(ctx, req.Msg.PublicKey)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.WorkerAddResponse{Id: id}), nil
}

func (a *adminService) WorkerRevoke(ctx context.Context, req *connect.Request[pb.WorkerRevokeRequest]) (*connect.Response[pb.WorkerRevokeResponse], error) {
	if err := a.srv.db.RevokeWorker(ctx, req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.WorkerRevokeResponse{}), nil
}

func (a *adminService) WorkerList(ctx context.Context, req *connect.Request[pb.WorkerListRequest]) (*connect.Response[pb.WorkerListResponse], error) {
	workers, err := a.srv.db.ListWorkers(ctx)
	if err != nil {
		return nil, err
	}
	pbWorkers := make([]*pb.Worker, len(workers))
	for i, w := range workers {
		pbWorkers[i] = &pb.Worker{
			Id:        w.ID,
			PublicKey: w.PublicKey,
			CreatedAt: timestamppb.New(w.CreatedAt),
		}
	}
	return connect.NewResponse(&pb.WorkerListResponse{Workers: pbWorkers}), nil
}
