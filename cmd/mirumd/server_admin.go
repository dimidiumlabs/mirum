// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"dimidiumlabs/mirum/internal/database"
	"dimidiumlabs/mirum/internal/protocol/pb"
	"dimidiumlabs/mirum/internal/protocol/pb/pbconnect"
)

func NewAdminServer(ctx context.Context, srv *server) *http.Server {
	as := &adminService{srv: srv}

	path, handler := pbconnect.NewAdminHandler(as,
		connect.WithInterceptors(validate.NewInterceptor()),
	)

	mux := http.NewServeMux()
	mux.Handle(path, handler)

	return &http.Server{
		Handler: mux,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}
}

type adminService struct {
	pbconnect.UnimplementedAdminHandler
	srv *server
}

// --- Error mapping ---

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, database.ErrUserNotFound),
		errors.Is(err, database.ErrOrgNotFound),
		errors.Is(err, database.ErrWorkerNotFound),
		errors.Is(err, database.ErrNotMember):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, database.ErrAlreadyMember),
		errors.Is(err, database.ErrSlugTaken),
		errors.Is(err, database.ErrEmailTaken):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, database.ErrLastOwner),
		errors.Is(err, database.ErrSoleOwner):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, database.ErrInvalidSlug),
		errors.Is(err, database.ErrInvalidRole):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, database.ErrFilterNotImplemented):
		return connect.NewError(connect.CodeUnimplemented, err)
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("internal error"))
	}
}

// --- Ref converters ---

func userRef(r *pb.UserRef) database.UserRef {
	switch v := r.GetRef().(type) {
	case *pb.UserRef_Id:
		return database.UserByID(uuid.UUID(v.Id))
	case *pb.UserRef_Email:
		return database.UserByEmail(v.Email)
	default:
		return database.UserByID(uuid.Nil)
	}
}

func orgRef(r *pb.OrgRef) database.OrgRef {
	switch v := r.GetRef().(type) {
	case *pb.OrgRef_Id:
		return database.OrgByID(uuid.UUID(v.Id))
	case *pb.OrgRef_Slug:
		return database.OrgBySlug(v.Slug)
	default:
		return database.OrgByID(uuid.Nil)
	}
}

// --- Role converters ---

var roleToString = map[pb.Role]string{
	pb.Role_ROLE_OWNER:  "owner",
	pb.Role_ROLE_ADMIN:  "admin",
	pb.Role_ROLE_MEMBER: "member",
}

var roleToProto = map[string]pb.Role{
	"owner":  pb.Role_ROLE_OWNER,
	"admin":  pb.Role_ROLE_ADMIN,
	"member": pb.Role_ROLE_MEMBER,
}

// --- Page helpers ---

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

func pageParams(p *pb.PageRequest) (cursor uuid.UUID, limit int) {
	limit = defaultPageSize
	if p != nil {
		if p.PageSize > 0 && int(p.PageSize) < maxPageSize {
			limit = int(p.PageSize)
		} else if int(p.PageSize) >= maxPageSize {
			limit = maxPageSize
		}
		if len(p.Cursor) == 16 {
			cursor = uuid.UUID(p.Cursor)
		}
	}
	return
}

func pageResponse(items int, limit int, lastID uuid.UUID, total int) *pb.PageResponse {
	resp := &pb.PageResponse{TotalCount: int32(total)}
	if items == limit {
		resp.NextCursor = lastID[:]
	}
	return resp
}

// --- Proto converters ---

func userToProto(u database.User) *pb.User {
	return &pb.User{
		Id: u.ID[:], Email: u.Email, CreatedAt: timestamppb.New(u.CreatedAt),
	}
}

func orgToProto(o database.Organization) *pb.Org {
	return &pb.Org{
		Id: o.ID[:], Name: o.Name, Slug: o.Slug,
		Public: o.Public, CreatedAt: timestamppb.New(o.CreatedAt),
	}
}

func memberToProto(m database.OrgMember) *pb.OrgMemberInfo {
	return &pb.OrgMemberInfo{
		User: userToProto(m.User), Role: roleToProto[m.Role],
		JoinedAt: timestamppb.New(m.JoinedAt),
	}
}

func workerToProto(w database.Worker) *pb.Worker {
	pw := &pb.Worker{
		Id: w.ID[:], PublicKey: w.PublicKey, CreatedAt: timestamppb.New(w.CreatedAt),
	}
	if w.OrgID != nil {
		pw.OrgId = w.OrgID[:]
	}
	return pw
}

// --- User handlers ---

func (a *adminService) UserCreate(ctx context.Context, req *connect.Request[pb.UserCreateRequest]) (*connect.Response[pb.UserCreateResponse], error) {
	id, err := a.srv.db.UserCreate(ctx, req.Msg.Email, req.Msg.Password, []byte(a.srv.cfg.Pepper))
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.UserCreateResponse{Id: id[:]}), nil
}

func (a *adminService) UserGet(ctx context.Context, req *connect.Request[pb.UserGetRequest]) (*connect.Response[pb.UserGetResponse], error) {
	u, err := a.srv.db.GetUser(ctx, userRef(req.Msg.User))
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.UserGetResponse{User: userToProto(*u)}), nil
}

func (a *adminService) UserList(ctx context.Context, req *connect.Request[pb.UserListRequest]) (*connect.Response[pb.UserListResponse], error) {
	cursor, limit := pageParams(req.Msg.Page)
	filter := ""
	if req.Msg.Filter != nil {
		filter = *req.Msg.Filter
	}

	users, total, err := a.srv.db.ListUsers(ctx, cursor, limit, filter)
	if err != nil {
		return nil, mapErr(err)
	}

	out := make([]*pb.User, len(users))
	for i := range users {
		out[i] = userToProto(users[i])
	}

	var lastID uuid.UUID
	if len(users) > 0 {
		lastID = users[len(users)-1].ID
	}

	return connect.NewResponse(&pb.UserListResponse{
		Page:  pageResponse(len(users), limit, lastID, total),
		Users: out,
	}), nil
}

func (a *adminService) UserUpdate(ctx context.Context, req *connect.Request[pb.UserUpdateRequest]) (*connect.Response[pb.UserUpdateResponse], error) {
	if err := a.srv.db.UserUpdate(ctx, userRef(req.Msg.User), req.Msg.Email, req.Msg.Password, []byte(a.srv.cfg.Pepper)); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.UserUpdateResponse{}), nil
}

func (a *adminService) UserDelete(ctx context.Context, req *connect.Request[pb.UserDeleteRequest]) (*connect.Response[pb.UserDeleteResponse], error) {
	if err := a.srv.db.UserDelete(ctx, userRef(req.Msg.User)); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.UserDeleteResponse{}), nil
}

// --- Org handlers ---

func (a *adminService) OrgCreate(ctx context.Context, req *connect.Request[pb.OrgCreateRequest]) (*connect.Response[pb.OrgCreateResponse], error) {
	slug, err := database.ValidateSlug(req.Msg.Slug)
	if err != nil {
		return nil, mapErr(err)
	}
	id, err := a.srv.db.CreateOrganization(ctx, req.Msg.Name, slug, req.Msg.Public, userRef(req.Msg.Owner))
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgCreateResponse{Id: id[:]}), nil
}

func (a *adminService) OrgGet(ctx context.Context, req *connect.Request[pb.OrgGetRequest]) (*connect.Response[pb.OrgGetResponse], error) {
	o, err := a.srv.db.GetOrg(ctx, orgRef(req.Msg.Org))
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgGetResponse{Org: orgToProto(*o)}), nil
}

func (a *adminService) OrgList(ctx context.Context, req *connect.Request[pb.OrgListRequest]) (*connect.Response[pb.OrgListResponse], error) {
	cursor, limit := pageParams(req.Msg.Page)
	filter := ""
	if req.Msg.Filter != nil {
		filter = *req.Msg.Filter
	}

	orgs, total, err := a.srv.db.ListOrganizations(ctx, cursor, limit, filter)
	if err != nil {
		return nil, mapErr(err)
	}

	out := make([]*pb.Org, len(orgs))
	for i := range orgs {
		out[i] = orgToProto(orgs[i])
	}

	var lastID uuid.UUID
	if len(orgs) > 0 {
		lastID = orgs[len(orgs)-1].ID
	}

	return connect.NewResponse(&pb.OrgListResponse{
		Page:          pageResponse(len(orgs), limit, lastID, total),
		Organizations: out,
	}), nil
}

func (a *adminService) OrgUpdate(ctx context.Context, req *connect.Request[pb.OrgUpdateRequest]) (*connect.Response[pb.OrgUpdateResponse], error) {
	var slug *string
	if req.Msg.Slug != nil {
		s, err := database.ValidateSlug(*req.Msg.Slug)
		if err != nil {
			return nil, mapErr(err)
		}
		slug = &s
	}
	if err := a.srv.db.UpdateOrganization(ctx, orgRef(req.Msg.Org), req.Msg.Name, slug, req.Msg.Public); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgUpdateResponse{}), nil
}

func (a *adminService) OrgDelete(ctx context.Context, req *connect.Request[pb.OrgDeleteRequest]) (*connect.Response[pb.OrgDeleteResponse], error) {
	if err := a.srv.db.DeleteOrganization(ctx, orgRef(req.Msg.Org)); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgDeleteResponse{}), nil
}

// --- OrgMember handlers ---

func (a *adminService) OrgMemberAdd(ctx context.Context, req *connect.Request[pb.OrgMemberAddRequest]) (*connect.Response[pb.OrgMemberAddResponse], error) {
	role, ok := roleToString[req.Msg.Role]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid role"))
	}
	if err := a.srv.db.AddOrgMember(ctx, orgRef(req.Msg.Org), userRef(req.Msg.User), role); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgMemberAddResponse{}), nil
}

func (a *adminService) OrgMemberGet(ctx context.Context, req *connect.Request[pb.OrgMemberGetRequest]) (*connect.Response[pb.OrgMemberGetResponse], error) {
	m, err := a.srv.db.GetOrgMember(ctx, orgRef(req.Msg.Org), userRef(req.Msg.User))
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgMemberGetResponse{Member: memberToProto(*m)}), nil
}

func (a *adminService) OrgMemberList(ctx context.Context, req *connect.Request[pb.OrgMemberListRequest]) (*connect.Response[pb.OrgMemberListResponse], error) {
	cursor, limit := pageParams(req.Msg.Page)
	filter := ""
	if req.Msg.Filter != nil {
		filter = *req.Msg.Filter
	}

	members, total, err := a.srv.db.ListOrgMembers(ctx, orgRef(req.Msg.Org), cursor, limit, filter)
	if err != nil {
		return nil, mapErr(err)
	}

	out := make([]*pb.OrgMemberInfo, len(members))
	for i := range members {
		out[i] = memberToProto(members[i])
	}

	var lastID uuid.UUID
	if len(members) > 0 {
		lastID = members[len(members)-1].User.ID
	}

	return connect.NewResponse(&pb.OrgMemberListResponse{
		Page:    pageResponse(len(members), limit, lastID, total),
		Members: out,
	}), nil
}

func (a *adminService) OrgMemberUpdate(ctx context.Context, req *connect.Request[pb.OrgMemberUpdateRequest]) (*connect.Response[pb.OrgMemberUpdateResponse], error) {
	role, ok := roleToString[req.Msg.Role]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid role"))
	}
	if err := a.srv.db.UpdateOrgMemberRole(ctx, orgRef(req.Msg.Org), userRef(req.Msg.User), role); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgMemberUpdateResponse{}), nil
}

func (a *adminService) OrgMemberRemove(ctx context.Context, req *connect.Request[pb.OrgMemberRemoveRequest]) (*connect.Response[pb.OrgMemberRemoveResponse], error) {
	if err := a.srv.db.RemoveOrgMember(ctx, orgRef(req.Msg.Org), userRef(req.Msg.User)); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgMemberRemoveResponse{}), nil
}

// --- Worker handlers ---

func (a *adminService) WorkerCreate(ctx context.Context, req *connect.Request[pb.WorkerCreateRequest]) (*connect.Response[pb.WorkerCreateResponse], error) {
	var org *database.OrgRef
	if req.Msg.Org != nil {
		r := orgRef(req.Msg.Org)
		org = &r
	}
	id, err := a.srv.db.CreateWorker(ctx, req.Msg.PublicKey, org)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.WorkerCreateResponse{Id: id[:]}), nil
}

func (a *adminService) WorkerGet(ctx context.Context, req *connect.Request[pb.WorkerGetRequest]) (*connect.Response[pb.WorkerGetResponse], error) {
	w, err := a.srv.db.GetWorker(ctx, uuid.UUID(req.Msg.Id))
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.WorkerGetResponse{Worker: workerToProto(*w)}), nil
}

func (a *adminService) WorkerList(ctx context.Context, req *connect.Request[pb.WorkerListRequest]) (*connect.Response[pb.WorkerListResponse], error) {
	cursor, limit := pageParams(req.Msg.Page)
	filter := ""
	if req.Msg.Filter != nil {
		filter = *req.Msg.Filter
	}

	workers, total, err := a.srv.db.ListWorkers(ctx, cursor, limit, filter)
	if err != nil {
		return nil, mapErr(err)
	}

	out := make([]*pb.Worker, len(workers))
	for i := range workers {
		out[i] = workerToProto(workers[i])
	}

	var lastID uuid.UUID
	if len(workers) > 0 {
		lastID = workers[len(workers)-1].ID
	}

	return connect.NewResponse(&pb.WorkerListResponse{
		Page:    pageResponse(len(workers), limit, lastID, total),
		Workers: out,
	}), nil
}

func (a *adminService) WorkerDelete(ctx context.Context, req *connect.Request[pb.WorkerDeleteRequest]) (*connect.Response[pb.WorkerDeleteResponse], error) {
	if err := a.srv.db.DeleteWorker(ctx, uuid.UUID(req.Msg.Id)); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.WorkerDeleteResponse{}), nil
}
