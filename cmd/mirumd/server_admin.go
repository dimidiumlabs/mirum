// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/ed25519"
	"fmt"

	"dimidiumlabs/mirum/internal/protocol/pb"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func NewAdminServer(srv *server) *grpc.Server {
	as := &adminService{srv: srv}
	s := grpc.NewServer()
	pb.RegisterAdminServer(s, as)
	return s
}

type adminService struct {
	pb.UnimplementedAdminServer
	srv *server
}

func (a *adminService) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.CreateUserResponse, error) {
	id, err := a.srv.db.CreateUser(ctx, req.Email, req.Password, []byte(a.srv.cfg.Pepper))
	if err != nil {
		return nil, err
	}
	return &pb.CreateUserResponse{Id: id}, nil
}

func (a *adminService) SetPassword(ctx context.Context, req *pb.SetPasswordRequest) (*pb.SetPasswordResponse, error) {
	if err := a.srv.db.SetPassword(ctx, req.Email, req.Password, []byte(a.srv.cfg.Pepper)); err != nil {
		return nil, err
	}
	return &pb.SetPasswordResponse{}, nil
}

func (a *adminService) DeleteUser(ctx context.Context, req *pb.DeleteUserRequest) (*pb.DeleteUserResponse, error) {
	if err := a.srv.db.DeleteUser(ctx, req.Email); err != nil {
		return nil, err
	}
	return &pb.DeleteUserResponse{}, nil
}

func (a *adminService) WorkerAdd(ctx context.Context, req *pb.WorkerAddRequest) (*pb.WorkerAddResponse, error) {
	if len(req.PublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key: expected %d bytes, got %d", ed25519.PublicKeySize, len(req.PublicKey))
	}
	id, err := a.srv.db.AddWorker(ctx, req.PublicKey)
	if err != nil {
		return nil, err
	}
	return &pb.WorkerAddResponse{Id: id}, nil
}

func (a *adminService) WorkerRevoke(ctx context.Context, req *pb.WorkerRevokeRequest) (*pb.WorkerRevokeResponse, error) {
	if err := a.srv.db.RevokeWorker(ctx, req.Id); err != nil {
		return nil, err
	}
	return &pb.WorkerRevokeResponse{}, nil
}

func (a *adminService) WorkerList(ctx context.Context, req *pb.WorkerListRequest) (*pb.WorkerListResponse, error) {
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
	return &pb.WorkerListResponse{Workers: pbWorkers}, nil
}
