// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"google.golang.org/protobuf/types/known/timestamppb"

	"dimidiumlabs/mirum/internal/protocol/pb"
	"dimidiumlabs/mirum/internal/protocol/pb/pbconnect"
)

// NewAdminHandler creates the ConnectRPC handler with validation.
// Authorization is handled inside DB methods, not by an interceptor.
func NewAdminHandler(srv *server) (string, http.Handler) {
	as := &adminService{srv: srv}
	return pbconnect.NewAdminHandler(as,
		connect.WithInterceptors(validate.NewInterceptor()),
	)
}

type adminService struct {
	pbconnect.UnimplementedAdminHandler
	srv *server
}

// --- Error mapping ---

// newAPIError builds a ConnectError with an empty message string and attaches
// an ErrorInfo detail carrying the domain reason. Clients switch on reason to
// pick user-facing text; the wire never carries human-readable strings.
func newAPIError(code connect.Code, reason pb.ErrorReason, metadata map[string]string) error {
	e := connect.NewError(code, nil)
	if d, err := connect.NewErrorDetail(&pb.ErrorInfo{Reason: reason, Metadata: metadata}); err == nil {
		e.AddDetail(d)
	}
	return e
}

var errSpecs = []struct {
	err    error
	code   connect.Code
	reason pb.ErrorReason
}{
	{ErrUserNotFound, connect.CodeNotFound, pb.ErrorReason_ERROR_REASON_USER_NOT_FOUND},
	{ErrOrgNotFound, connect.CodeNotFound, pb.ErrorReason_ERROR_REASON_ORG_NOT_FOUND},
	{ErrWorkerNotFound, connect.CodeNotFound, pb.ErrorReason_ERROR_REASON_WORKER_NOT_FOUND},
	{ErrNotMember, connect.CodeNotFound, pb.ErrorReason_ERROR_REASON_MEMBER_NOT_FOUND},
	{ErrEmailTaken, connect.CodeAlreadyExists, pb.ErrorReason_ERROR_REASON_EMAIL_TAKEN},
	{ErrSlugTaken, connect.CodeAlreadyExists, pb.ErrorReason_ERROR_REASON_SLUG_TAKEN},
	{ErrAlreadyMember, connect.CodeAlreadyExists, pb.ErrorReason_ERROR_REASON_ALREADY_MEMBER},
	{ErrLastOwner, connect.CodeFailedPrecondition, pb.ErrorReason_ERROR_REASON_LAST_OWNER},
	{ErrSoleOwner, connect.CodeFailedPrecondition, pb.ErrorReason_ERROR_REASON_SOLE_OWNER},
	{ErrInvalidSlug, connect.CodeInvalidArgument, pb.ErrorReason_ERROR_REASON_INVALID_SLUG},
	{ErrInvalidRole, connect.CodeInvalidArgument, pb.ErrorReason_ERROR_REASON_INVALID_ROLE},
	{ErrReservedEmail, connect.CodeInvalidArgument, pb.ErrorReason_ERROR_REASON_RESERVED_EMAIL},
	{ErrPermissionDenied, connect.CodePermissionDenied, pb.ErrorReason_ERROR_REASON_PERMISSION_DENIED},
	{ErrUnauthenticated, connect.CodeUnauthenticated, pb.ErrorReason_ERROR_REASON_UNAUTHENTICATED},
	{ErrNotImplemented, connect.CodeUnimplemented, pb.ErrorReason_ERROR_REASON_UNIMPLEMENTED},
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	for _, s := range errSpecs {
		if errors.Is(err, s.err) {
			return newAPIError(s.code, s.reason, nil)
		}
	}
	slog.Error("unmapped handler error", "err", err)
	return newAPIError(connect.CodeInternal, pb.ErrorReason_ERROR_REASON_INTERNAL, nil)
}

// --- Ref converters ---

func userRef(r *pb.UserRef) (UserRef, error) {
	switch v := r.GetRef().(type) {
	case *pb.UserRef_Id:
		id, err := IDFromBytes[UserKind](v.Id)
		if err != nil {
			return UserRef{}, err
		}
		return UserByID(id), nil
	case *pb.UserRef_Email:
		return UserByEmail(v.Email), nil
	default:
		return UserRef{}, nil
	}
}

func orgRef(r *pb.OrgRef) (OrgRef, error) {
	switch v := r.GetRef().(type) {
	case *pb.OrgRef_Id:
		id, err := IDFromBytes[OrgKind](v.Id)
		if err != nil {
			return OrgRef{}, err
		}
		return OrgByID(id), nil
	case *pb.OrgRef_Slug:
		return OrgBySlug(v.Slug), nil
	default:
		return OrgRef{}, nil
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

func pageParams[K IDKind](p *pb.PageRequest) (cursor ID[K], limit int, err error) {
	limit = defaultPageSize
	if p != nil {
		if p.PageSize > 0 && int(p.PageSize) < maxPageSize {
			limit = int(p.PageSize)
		} else if int(p.PageSize) >= maxPageSize {
			limit = maxPageSize
		}
		if len(p.Cursor) == 16 {
			cursor, err = IDFromBytes[K](p.Cursor)
			if err != nil {
				return
			}
		}
	}
	return
}

func pageResponse[K IDKind](items int, limit int, lastID ID[K], total int) *pb.PageResponse {
	resp := &pb.PageResponse{TotalCount: int32(total)}
	if items == limit {
		resp.NextCursor = lastID.Bytes()
	}
	return resp
}

// --- Proto converters ---

func userToProto(u User) *pb.User {
	return &pb.User{
		Id: u.ID.Bytes(), Email: u.Email, CreatedAt: timestamppb.New(u.CreatedAt),
	}
}

func orgToProto(o Organization) *pb.Org {
	return &pb.Org{
		Id: o.ID.Bytes(), Name: o.Name, Slug: o.Slug,
		Public: o.Public, CreatedAt: timestamppb.New(o.CreatedAt),
	}
}

func memberToProto(m OrgMember) *pb.OrgMemberInfo {
	return &pb.OrgMemberInfo{
		User: userToProto(m.User), Role: roleToProto[m.Role],
		JoinedAt: timestamppb.New(m.JoinedAt),
	}
}

func workerToProto(w Worker) *pb.Worker {
	pw := &pb.Worker{
		Id: w.ID.Bytes(), PublicKey: w.PublicKey, CreatedAt: timestamppb.New(w.CreatedAt),
	}
	if w.OrgID != nil {
		pw.OrgId = w.OrgID.Bytes()
	}
	return pw
}

// --- User handlers ---

func (a *adminService) UserCreate(ctx context.Context, req *connect.Request[pb.UserCreateRequest]) (*connect.Response[pb.UserCreateResponse], error) {
	id, err := a.srv.db.UserCreate(ctx, ActorFromContext(ctx), req.Msg.Email, req.Msg.Password, []byte(a.srv.cfg.Pepper))
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.UserCreateResponse{Id: id.Bytes()}), nil
}

func (a *adminService) UserGet(ctx context.Context, req *connect.Request[pb.UserGetRequest]) (*connect.Response[pb.UserGetResponse], error) {
	ref, err := userRef(req.Msg.User)
	if err != nil {
		return nil, mapErr(err)
	}
	u, err := a.srv.db.UserGet(ctx, ActorFromContext(ctx), ref)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.UserGetResponse{User: userToProto(*u)}), nil
}

func (a *adminService) UserList(ctx context.Context, req *connect.Request[pb.UserListRequest]) (*connect.Response[pb.UserListResponse], error) {
	cursor, limit, err := pageParams[UserKind](req.Msg.Page)
	if err != nil {
		return nil, mapErr(err)
	}
	filter := ""
	if req.Msg.Filter != nil {
		filter = *req.Msg.Filter
	}

	users, total, err := a.srv.db.UserList(ctx, ActorFromContext(ctx), cursor, limit, filter)
	if err != nil {
		return nil, mapErr(err)
	}

	out := make([]*pb.User, len(users))
	for i := range users {
		out[i] = userToProto(users[i])
	}

	var lastID UserID
	if len(users) > 0 {
		lastID = users[len(users)-1].ID
	}

	return connect.NewResponse(&pb.UserListResponse{
		Page:  pageResponse(len(users), limit, lastID, total),
		Users: out,
	}), nil
}

func (a *adminService) UserUpdate(ctx context.Context, req *connect.Request[pb.UserUpdateRequest]) (*connect.Response[pb.UserUpdateResponse], error) {
	ref, err := userRef(req.Msg.User)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := a.srv.db.UserUpdate(ctx, ActorFromContext(ctx), ref, req.Msg.Email, req.Msg.Password, []byte(a.srv.cfg.Pepper)); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.UserUpdateResponse{}), nil
}

func (a *adminService) UserDelete(ctx context.Context, req *connect.Request[pb.UserDeleteRequest]) (*connect.Response[pb.UserDeleteResponse], error) {
	ref, err := userRef(req.Msg.User)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := a.srv.db.UserDelete(ctx, ActorFromContext(ctx), ref); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.UserDeleteResponse{}), nil
}

// --- Org handlers ---

func (a *adminService) OrgCreate(ctx context.Context, req *connect.Request[pb.OrgCreateRequest]) (*connect.Response[pb.OrgCreateResponse], error) {
	slug, err := ValidateSlug(req.Msg.Slug)
	if err != nil {
		return nil, mapErr(err)
	}
	owner, err := userRef(req.Msg.Owner)
	if err != nil {
		return nil, mapErr(err)
	}
	id, err := a.srv.db.OrgCreate(ctx, ActorFromContext(ctx), req.Msg.Name, slug, req.Msg.Public, owner)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgCreateResponse{Id: id.Bytes()}), nil
}

func (a *adminService) OrgGet(ctx context.Context, req *connect.Request[pb.OrgGetRequest]) (*connect.Response[pb.OrgGetResponse], error) {
	ref, err := orgRef(req.Msg.Org)
	if err != nil {
		return nil, mapErr(err)
	}
	o, err := a.srv.db.OrgGet(ctx, ActorFromContext(ctx), ref)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgGetResponse{Org: orgToProto(*o)}), nil
}

func (a *adminService) OrgList(ctx context.Context, req *connect.Request[pb.OrgListRequest]) (*connect.Response[pb.OrgListResponse], error) {
	cursor, limit, err := pageParams[OrgKind](req.Msg.Page)
	if err != nil {
		return nil, mapErr(err)
	}
	filter := ""
	if req.Msg.Filter != nil {
		filter = *req.Msg.Filter
	}

	orgs, total, err := a.srv.db.OrgList(ctx, ActorFromContext(ctx), cursor, limit, filter)
	if err != nil {
		return nil, mapErr(err)
	}

	out := make([]*pb.Org, len(orgs))
	for i := range orgs {
		out[i] = orgToProto(orgs[i])
	}

	var lastID OrgID
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
		s, err := ValidateSlug(*req.Msg.Slug)
		if err != nil {
			return nil, mapErr(err)
		}
		slug = &s
	}
	ref, err := orgRef(req.Msg.Org)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := a.srv.db.OrgUpdate(ctx, ActorFromContext(ctx), ref, req.Msg.Name, slug, req.Msg.Public); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgUpdateResponse{}), nil
}

func (a *adminService) OrgDelete(ctx context.Context, req *connect.Request[pb.OrgDeleteRequest]) (*connect.Response[pb.OrgDeleteResponse], error) {
	ref, err := orgRef(req.Msg.Org)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := a.srv.db.OrgDelete(ctx, ActorFromContext(ctx), ref); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgDeleteResponse{}), nil
}

// --- OrgMember handlers ---

func (a *adminService) OrgMemberAdd(ctx context.Context, req *connect.Request[pb.OrgMemberAddRequest]) (*connect.Response[pb.OrgMemberAddResponse], error) {
	role, ok := roleToString[req.Msg.Role]
	if !ok {
		return nil, newAPIError(connect.CodeInvalidArgument, pb.ErrorReason_ERROR_REASON_INVALID_ROLE, nil)
	}
	org, err := orgRef(req.Msg.Org)
	if err != nil {
		return nil, mapErr(err)
	}
	user, err := userRef(req.Msg.User)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := a.srv.db.OrgMemberAdd(ctx, ActorFromContext(ctx), org, user, role); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgMemberAddResponse{}), nil
}

func (a *adminService) OrgMemberGet(ctx context.Context, req *connect.Request[pb.OrgMemberGetRequest]) (*connect.Response[pb.OrgMemberGetResponse], error) {
	org, err := orgRef(req.Msg.Org)
	if err != nil {
		return nil, mapErr(err)
	}
	user, err := userRef(req.Msg.User)
	if err != nil {
		return nil, mapErr(err)
	}
	m, err := a.srv.db.OrgMemberGet(ctx, ActorFromContext(ctx), org, user)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgMemberGetResponse{Member: memberToProto(*m)}), nil
}

func (a *adminService) OrgMemberList(ctx context.Context, req *connect.Request[pb.OrgMemberListRequest]) (*connect.Response[pb.OrgMemberListResponse], error) {
	cursor, limit, err := pageParams[UserKind](req.Msg.Page)
	if err != nil {
		return nil, mapErr(err)
	}
	filter := ""
	if req.Msg.Filter != nil {
		filter = *req.Msg.Filter
	}
	org, err := orgRef(req.Msg.Org)
	if err != nil {
		return nil, mapErr(err)
	}

	members, total, err := a.srv.db.OrgMembersList(ctx, ActorFromContext(ctx), org, cursor, limit, filter)
	if err != nil {
		return nil, mapErr(err)
	}

	out := make([]*pb.OrgMemberInfo, len(members))
	for i := range members {
		out[i] = memberToProto(members[i])
	}

	var lastID UserID
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
		return nil, newAPIError(connect.CodeInvalidArgument, pb.ErrorReason_ERROR_REASON_INVALID_ROLE, nil)
	}
	org, err := orgRef(req.Msg.Org)
	if err != nil {
		return nil, mapErr(err)
	}
	user, err := userRef(req.Msg.User)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := a.srv.db.OrgMemberUpdateRole(ctx, ActorFromContext(ctx), org, user, role); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgMemberUpdateResponse{}), nil
}

func (a *adminService) OrgMemberRemove(ctx context.Context, req *connect.Request[pb.OrgMemberRemoveRequest]) (*connect.Response[pb.OrgMemberRemoveResponse], error) {
	org, err := orgRef(req.Msg.Org)
	if err != nil {
		return nil, mapErr(err)
	}
	user, err := userRef(req.Msg.User)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := a.srv.db.OrgMemberRemove(ctx, ActorFromContext(ctx), org, user); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.OrgMemberRemoveResponse{}), nil
}

// --- Worker handlers ---

func (a *adminService) WorkerCreate(ctx context.Context, req *connect.Request[pb.WorkerCreateRequest]) (*connect.Response[pb.WorkerCreateResponse], error) {
	var org *OrgRef
	if req.Msg.Org != nil {
		r, err := orgRef(req.Msg.Org)
		if err != nil {
			return nil, mapErr(err)
		}
		org = &r
	}
	id, err := a.srv.db.WorkerCreate(ctx, ActorFromContext(ctx), req.Msg.PublicKey, org)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.WorkerCreateResponse{Id: id.Bytes()}), nil
}

func (a *adminService) WorkerGet(ctx context.Context, req *connect.Request[pb.WorkerGetRequest]) (*connect.Response[pb.WorkerGetResponse], error) {
	wid, err := IDFromBytes[WorkerKind](req.Msg.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	w, err := a.srv.db.WorkerGet(ctx, ActorFromContext(ctx), wid)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.WorkerGetResponse{Worker: workerToProto(*w)}), nil
}

func (a *adminService) WorkerList(ctx context.Context, req *connect.Request[pb.WorkerListRequest]) (*connect.Response[pb.WorkerListResponse], error) {
	cursor, limit, err := pageParams[WorkerKind](req.Msg.Page)
	if err != nil {
		return nil, mapErr(err)
	}
	filter := ""
	if req.Msg.Filter != nil {
		filter = *req.Msg.Filter
	}

	workers, total, err := a.srv.db.WorkerList(ctx, ActorFromContext(ctx), cursor, limit, filter)
	if err != nil {
		return nil, mapErr(err)
	}

	out := make([]*pb.Worker, len(workers))
	for i := range workers {
		out[i] = workerToProto(workers[i])
	}

	var lastID WorkerID
	if len(workers) > 0 {
		lastID = workers[len(workers)-1].ID
	}

	return connect.NewResponse(&pb.WorkerListResponse{
		Page:    pageResponse(len(workers), limit, lastID, total),
		Workers: out,
	}), nil
}

func (a *adminService) WorkerDelete(ctx context.Context, req *connect.Request[pb.WorkerDeleteRequest]) (*connect.Response[pb.WorkerDeleteResponse], error) {
	wid, err := IDFromBytes[WorkerKind](req.Msg.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := a.srv.db.WorkerDelete(ctx, ActorFromContext(ctx), wid); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&pb.WorkerDeleteResponse{}), nil
}
