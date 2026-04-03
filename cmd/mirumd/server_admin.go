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

	"dimidiumlabs/mirum/internal/database"
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

func (a *adminService) UserCreate(ctx context.Context, req *connect.Request[pb.UserCreateRequest]) (*connect.Response[pb.UserCreateResponse], error) {
	id, err := a.srv.db.UserCreate(ctx, req.Msg.Email, req.Msg.Password, []byte(a.srv.cfg.Pepper))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.UserCreateResponse{Id: id}), nil
}

func (a *adminService) UserSetPassword(ctx context.Context, req *connect.Request[pb.UserSetPasswordRequest]) (*connect.Response[pb.UserSetPasswordResponse], error) {
	if err := a.srv.db.UserSetPassword(ctx, req.Msg.Email, req.Msg.Password, []byte(a.srv.cfg.Pepper)); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.UserSetPasswordResponse{}), nil
}

func (a *adminService) UserDelete(ctx context.Context, req *connect.Request[pb.UserDeleteRequest]) (*connect.Response[pb.UserDeleteResponse], error) {
	if err := a.srv.db.UserDelete(ctx, req.Msg.Email); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.UserDeleteResponse{}), nil
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
	if err := a.srv.db.WorkerRevoke(ctx, req.Msg.Id); err != nil {
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

func (a *adminService) OrgCreate(ctx context.Context, req *connect.Request[pb.OrgCreateRequest]) (*connect.Response[pb.OrgCreateResponse], error) {
	id, err := a.srv.db.CreateOrganization(ctx, req.Msg.Name, req.Msg.Slug, req.Msg.Public, req.Msg.OwnerEmail)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.OrgCreateResponse{Id: id}), nil
}

func (a *adminService) OrgDelete(ctx context.Context, req *connect.Request[pb.OrgDeleteRequest]) (*connect.Response[pb.OrgDeleteResponse], error) {
	if err := a.srv.db.DeleteOrganization(ctx, req.Msg.Slug); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.OrgDeleteResponse{}), nil
}

func (a *adminService) OrgRename(ctx context.Context, req *connect.Request[pb.OrgRenameRequest]) (*connect.Response[pb.OrgRenameResponse], error) {
	if err := a.srv.db.RenameOrganization(ctx, req.Msg.Slug, req.Msg.NewName, req.Msg.NewSlug); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.OrgRenameResponse{}), nil
}

func (a *adminService) OrgList(ctx context.Context, req *connect.Request[pb.OrgListRequest]) (*connect.Response[pb.OrgListResponse], error) {
	var (
		orgs []database.Organization
		err  error
	)
	if req.Msg.UserEmail != nil {
		orgs, err = a.srv.db.ListUserOrganizations(ctx, *req.Msg.UserEmail)
	} else {
		orgs, err = a.srv.db.ListAllOrganizations(ctx)
	}
	if err != nil {
		return nil, err
	}
	pbOrgs := make([]*pb.Org, len(orgs))
	for i, o := range orgs {
		pbOrgs[i] = &pb.Org{
			Id:        o.ID,
			Name:      o.Name,
			Slug:      o.Slug,
			Public:    o.Public,
			CreatedAt: timestamppb.New(o.CreatedAt),
		}
	}
	return connect.NewResponse(&pb.OrgListResponse{Organizations: pbOrgs}), nil
}

func (a *adminService) OrgMemberAdd(ctx context.Context, req *connect.Request[pb.OrgMemberAddRequest]) (*connect.Response[pb.OrgMemberAddResponse], error) {
	if err := a.srv.db.AddOrgMember(ctx, req.Msg.OrgSlug, req.Msg.Email, req.Msg.Role); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.OrgMemberAddResponse{}), nil
}

func (a *adminService) OrgMemberRemove(ctx context.Context, req *connect.Request[pb.OrgMemberRemoveRequest]) (*connect.Response[pb.OrgMemberRemoveResponse], error) {
	if err := a.srv.db.RemoveOrgMember(ctx, req.Msg.OrgSlug, req.Msg.Email); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.OrgMemberRemoveResponse{}), nil
}

func (a *adminService) OrgMemberSetRole(ctx context.Context, req *connect.Request[pb.OrgMemberSetRoleRequest]) (*connect.Response[pb.OrgMemberSetRoleResponse], error) {
	if err := a.srv.db.ChangeOrgMemberRole(ctx, req.Msg.OrgSlug, req.Msg.Email, req.Msg.Role); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.OrgMemberSetRoleResponse{}), nil
}

func (a *adminService) OrgMemberList(ctx context.Context, req *connect.Request[pb.OrgMemberListRequest]) (*connect.Response[pb.OrgMemberListResponse], error) {
	members, err := a.srv.db.ListOrgMembers(ctx, req.Msg.OrgSlug)
	if err != nil {
		return nil, err
	}
	pbMembers := make([]*pb.OrgMemberInfo, len(members))
	for i, m := range members {
		pbMembers[i] = &pb.OrgMemberInfo{
			Email:    m.User.Email,
			Role:     m.Role,
			JoinedAt: timestamppb.New(m.JoinedAt),
		}
	}
	return connect.NewResponse(&pb.OrgMemberListResponse{Members: pbMembers}), nil
}
