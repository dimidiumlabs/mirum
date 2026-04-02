// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"

	"dimidiumlabs/mirum/internal/protocol/pb"

	"google.golang.org/grpc"
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
