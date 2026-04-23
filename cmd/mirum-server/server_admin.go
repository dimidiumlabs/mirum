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

	"dimidiumlabs/mirum/cmd/mirum-server/apipb"
	"dimidiumlabs/mirum/cmd/mirum-server/apipb/apipbconnect"
)

// NewConsoleHandler creates the ConnectRPC handler with validation.
// Authorization is handled inside DB methods, not by an interceptor.
func NewConsoleHandler(srv *server) (string, http.Handler) {
	as := &consoleService{srv: srv}
	return apipbconnect.NewConsoleHandler(as,
		connect.WithInterceptors(validate.NewInterceptor()),
	)
}

type consoleService struct {
	apipbconnect.UnimplementedConsoleHandler
	srv *server
}

// --- Error mapping ---

// newAPIError builds a ConnectError with an empty message string and attaches
// an ErrorInfo detail carrying the domain reason. Clients switch on reason to
// pick user-facing text; the wire never carries human-readable strings.
func newAPIError(code connect.Code, reason apipb.ErrorReason, metadata map[string]string) error {
	e := connect.NewError(code, nil)
	if d, err := connect.NewErrorDetail(&apipb.ErrorInfo{Reason: reason, Metadata: metadata}); err == nil {
		e.AddDetail(d)
	}
	return e
}

var errSpecs = []struct {
	err    error
	code   connect.Code
	reason apipb.ErrorReason
}{
	{ErrUserNotFound, connect.CodeNotFound, apipb.ErrorReason_ERROR_REASON_USER_NOT_FOUND},
	{ErrOrgNotFound, connect.CodeNotFound, apipb.ErrorReason_ERROR_REASON_ORG_NOT_FOUND},
	{ErrWorkerNotFound, connect.CodeNotFound, apipb.ErrorReason_ERROR_REASON_WORKER_NOT_FOUND},
	{ErrNotMember, connect.CodeNotFound, apipb.ErrorReason_ERROR_REASON_MEMBER_NOT_FOUND},
	{ErrEmailTaken, connect.CodeAlreadyExists, apipb.ErrorReason_ERROR_REASON_EMAIL_TAKEN},
	{ErrSlugTaken, connect.CodeAlreadyExists, apipb.ErrorReason_ERROR_REASON_SLUG_TAKEN},
	{ErrAlreadyMember, connect.CodeAlreadyExists, apipb.ErrorReason_ERROR_REASON_ALREADY_MEMBER},
	{ErrLastOwner, connect.CodeFailedPrecondition, apipb.ErrorReason_ERROR_REASON_LAST_OWNER},
	{ErrSoleOwner, connect.CodeFailedPrecondition, apipb.ErrorReason_ERROR_REASON_SOLE_OWNER},
	{ErrInvalidSlug, connect.CodeInvalidArgument, apipb.ErrorReason_ERROR_REASON_INVALID_SLUG},
	{ErrInvalidRole, connect.CodeInvalidArgument, apipb.ErrorReason_ERROR_REASON_INVALID_ROLE},
	{ErrReservedEmail, connect.CodeInvalidArgument, apipb.ErrorReason_ERROR_REASON_RESERVED_EMAIL},
	{ErrInvalidDateFormat, connect.CodeInvalidArgument, apipb.ErrorReason_ERROR_REASON_INVALID_DATE_FORMAT},
	{ErrInvalidTimezone, connect.CodeInvalidArgument, apipb.ErrorReason_ERROR_REASON_INVALID_TIMEZONE},
	{ErrPermissionDenied, connect.CodePermissionDenied, apipb.ErrorReason_ERROR_REASON_PERMISSION_DENIED},
	{ErrUnauthenticated, connect.CodeUnauthenticated, apipb.ErrorReason_ERROR_REASON_UNAUTHENTICATED},
	{ErrNotImplemented, connect.CodeUnimplemented, apipb.ErrorReason_ERROR_REASON_UNIMPLEMENTED},
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
	return newAPIError(connect.CodeInternal, apipb.ErrorReason_ERROR_REASON_INTERNAL, nil)
}

// --- Ref converters ---

func userRef(r *apipb.UserRef) (UserRef, error) {
	switch v := r.GetRef().(type) {
	case *apipb.UserRef_Id:
		id, err := IDFromBytes[UserKind](v.Id)
		if err != nil {
			return UserRef{}, err
		}
		return UserByID(id), nil
	case *apipb.UserRef_Email:
		return UserByEmail(v.Email), nil
	default:
		return UserRef{}, nil
	}
}

func orgRef(r *apipb.OrgRef) (OrgRef, error) {
	switch v := r.GetRef().(type) {
	case *apipb.OrgRef_Id:
		id, err := IDFromBytes[OrgKind](v.Id)
		if err != nil {
			return OrgRef{}, err
		}
		return OrgByID(id), nil
	case *apipb.OrgRef_Slug:
		return OrgBySlug(v.Slug), nil
	default:
		return OrgRef{}, nil
	}
}

// --- Role converters ---

var roleToString = map[apipb.Role]string{
	apipb.Role_ROLE_OWNER:  "owner",
	apipb.Role_ROLE_ADMIN:  "admin",
	apipb.Role_ROLE_MEMBER: "member",
}

var roleToProto = map[string]apipb.Role{
	"owner":  apipb.Role_ROLE_OWNER,
	"admin":  apipb.Role_ROLE_ADMIN,
	"member": apipb.Role_ROLE_MEMBER,
}

// --- Page helpers ---

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

func pageParams[K IDKind](p *apipb.PageRequest) (cursor ID[K], limit int, err error) {
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

func pageResponse[K IDKind](items int, limit int, lastID ID[K], total int) *apipb.PageResponse {
	resp := &apipb.PageResponse{TotalCount: int32(total)}
	if items == limit {
		resp.NextCursor = lastID.Bytes()
	}
	return resp
}

// --- Proto converters ---

func userToProto(u User) *apipb.User {
	pu := &apipb.User{
		Id:        u.ID.Bytes(),
		Email:     u.Email,
		CreatedAt: timestamppb.New(u.CreatedAt),
		Timezone:  u.Timezone,
	}
	if u.Locale != nil {
		ls := &apipb.LocaleSettings{}
		if u.Locale.Language != nil {
			ls.Language = *u.Locale.Language
		}
		if u.Locale.DateFormat != nil {
			switch *u.Locale.DateFormat {
			case DateFormatDMY:
				ls.DateFormat = apipb.DateFormat_DATE_FORMAT_DMY
			case DateFormatMDY:
				ls.DateFormat = apipb.DateFormat_DATE_FORMAT_MDY
			case DateFormatYMD:
				ls.DateFormat = apipb.DateFormat_DATE_FORMAT_YMD
			}
		}
		pu.Locale = ls
	}
	return pu
}

func orgToProto(o Organization) *apipb.Org {
	return &apipb.Org{
		Id: o.ID.Bytes(), Name: o.Name, Slug: o.Slug,
		Public: o.Public, CreatedAt: timestamppb.New(o.CreatedAt),
	}
}

func memberToProto(m OrgMember) *apipb.OrgMemberInfo {
	return &apipb.OrgMemberInfo{
		User: userToProto(m.User), Role: roleToProto[m.Role],
		JoinedAt: timestamppb.New(m.JoinedAt),
	}
}

func workerToProto(w Worker) *apipb.Worker {
	pw := &apipb.Worker{
		Id: w.ID.Bytes(), PublicKey: w.PublicKey, CreatedAt: timestamppb.New(w.CreatedAt),
	}
	if w.OrgID != nil {
		pw.OrgId = w.OrgID.Bytes()
	}
	return pw
}

// --- User handlers ---

func (a *consoleService) UserCreate(ctx context.Context, req *connect.Request[apipb.UserCreateRequest]) (*connect.Response[apipb.UserCreateResponse], error) {
	id, err := a.srv.db.UserCreate(ctx, ActorFromContext(ctx), req.Msg.Email, req.Msg.Password, []byte(a.srv.cfg.Pepper))
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&apipb.UserCreateResponse{Id: id.Bytes()}), nil
}

func (a *consoleService) UserGet(ctx context.Context, req *connect.Request[apipb.UserGetRequest]) (*connect.Response[apipb.UserGetResponse], error) {
	ref, err := userRef(req.Msg.User)
	if err != nil {
		return nil, mapErr(err)
	}
	u, err := a.srv.db.UserGet(ctx, ActorFromContext(ctx), ref)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&apipb.UserGetResponse{User: userToProto(*u)}), nil
}

func (a *consoleService) UserList(ctx context.Context, req *connect.Request[apipb.UserListRequest]) (*connect.Response[apipb.UserListResponse], error) {
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

	out := make([]*apipb.User, len(users))
	for i := range users {
		out[i] = userToProto(users[i])
	}

	var lastID UserID
	if len(users) > 0 {
		lastID = users[len(users)-1].ID
	}

	return connect.NewResponse(&apipb.UserListResponse{
		Page:  pageResponse(len(users), limit, lastID, total),
		Users: out,
	}), nil
}

func (a *consoleService) UserUpdate(ctx context.Context, req *connect.Request[apipb.UserUpdateRequest]) (*connect.Response[apipb.UserUpdateResponse], error) {
	ref, err := userRef(req.Msg.User)
	if err != nil {
		return nil, mapErr(err)
	}
	var locale *LocaleSettings
	if req.Msg.Locale != nil {
		locale = &LocaleSettings{}
		if req.Msg.Locale.Language != "" {
			locale.Language = &req.Msg.Locale.Language
		}
		if req.Msg.Locale.DateFormat != apipb.DateFormat_DATE_FORMAT_UNSPECIFIED {
			switch req.Msg.Locale.DateFormat {
			case apipb.DateFormat_DATE_FORMAT_DMY:
				df := DateFormatDMY
				locale.DateFormat = &df
			case apipb.DateFormat_DATE_FORMAT_MDY:
				df := DateFormatMDY
				locale.DateFormat = &df
			case apipb.DateFormat_DATE_FORMAT_YMD:
				df := DateFormatYMD
				locale.DateFormat = &df
			}
		}
	}
	if err := a.srv.db.UserUpdate(ctx, ActorFromContext(ctx), ref, UserUpdateParams{
		Email:    req.Msg.Email,
		Password: req.Msg.Password,
		Pepper:   []byte(a.srv.cfg.Pepper),
		Locale:   locale,
		Timezone: req.Msg.Timezone,
	}); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&apipb.UserUpdateResponse{}), nil
}

func (a *consoleService) UserDelete(ctx context.Context, req *connect.Request[apipb.UserDeleteRequest]) (*connect.Response[apipb.UserDeleteResponse], error) {
	ref, err := userRef(req.Msg.User)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := a.srv.db.UserDelete(ctx, ActorFromContext(ctx), ref); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&apipb.UserDeleteResponse{}), nil
}

// --- Org handlers ---

func (a *consoleService) OrgCreate(ctx context.Context, req *connect.Request[apipb.OrgCreateRequest]) (*connect.Response[apipb.OrgCreateResponse], error) {
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
	return connect.NewResponse(&apipb.OrgCreateResponse{Id: id.Bytes()}), nil
}

func (a *consoleService) OrgGet(ctx context.Context, req *connect.Request[apipb.OrgGetRequest]) (*connect.Response[apipb.OrgGetResponse], error) {
	ref, err := orgRef(req.Msg.Org)
	if err != nil {
		return nil, mapErr(err)
	}
	o, err := a.srv.db.OrgGet(ctx, ActorFromContext(ctx), ref)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&apipb.OrgGetResponse{Org: orgToProto(*o)}), nil
}

func (a *consoleService) OrgList(ctx context.Context, req *connect.Request[apipb.OrgListRequest]) (*connect.Response[apipb.OrgListResponse], error) {
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

	out := make([]*apipb.Org, len(orgs))
	for i := range orgs {
		out[i] = orgToProto(orgs[i])
	}

	var lastID OrgID
	if len(orgs) > 0 {
		lastID = orgs[len(orgs)-1].ID
	}

	return connect.NewResponse(&apipb.OrgListResponse{
		Page:          pageResponse(len(orgs), limit, lastID, total),
		Organizations: out,
	}), nil
}

func (a *consoleService) OrgUpdate(ctx context.Context, req *connect.Request[apipb.OrgUpdateRequest]) (*connect.Response[apipb.OrgUpdateResponse], error) {
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
	return connect.NewResponse(&apipb.OrgUpdateResponse{}), nil
}

func (a *consoleService) OrgDelete(ctx context.Context, req *connect.Request[apipb.OrgDeleteRequest]) (*connect.Response[apipb.OrgDeleteResponse], error) {
	ref, err := orgRef(req.Msg.Org)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := a.srv.db.OrgDelete(ctx, ActorFromContext(ctx), ref); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&apipb.OrgDeleteResponse{}), nil
}

// --- OrgMember handlers ---

func (a *consoleService) OrgMemberAdd(ctx context.Context, req *connect.Request[apipb.OrgMemberAddRequest]) (*connect.Response[apipb.OrgMemberAddResponse], error) {
	role, ok := roleToString[req.Msg.Role]
	if !ok {
		return nil, newAPIError(connect.CodeInvalidArgument, apipb.ErrorReason_ERROR_REASON_INVALID_ROLE, nil)
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
	return connect.NewResponse(&apipb.OrgMemberAddResponse{}), nil
}

func (a *consoleService) OrgMemberGet(ctx context.Context, req *connect.Request[apipb.OrgMemberGetRequest]) (*connect.Response[apipb.OrgMemberGetResponse], error) {
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
	return connect.NewResponse(&apipb.OrgMemberGetResponse{Member: memberToProto(*m)}), nil
}

func (a *consoleService) OrgMemberList(ctx context.Context, req *connect.Request[apipb.OrgMemberListRequest]) (*connect.Response[apipb.OrgMemberListResponse], error) {
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

	out := make([]*apipb.OrgMemberInfo, len(members))
	for i := range members {
		out[i] = memberToProto(members[i])
	}

	var lastID UserID
	if len(members) > 0 {
		lastID = members[len(members)-1].User.ID
	}

	return connect.NewResponse(&apipb.OrgMemberListResponse{
		Page:    pageResponse(len(members), limit, lastID, total),
		Members: out,
	}), nil
}

func (a *consoleService) OrgMemberUpdate(ctx context.Context, req *connect.Request[apipb.OrgMemberUpdateRequest]) (*connect.Response[apipb.OrgMemberUpdateResponse], error) {
	role, ok := roleToString[req.Msg.Role]
	if !ok {
		return nil, newAPIError(connect.CodeInvalidArgument, apipb.ErrorReason_ERROR_REASON_INVALID_ROLE, nil)
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
	return connect.NewResponse(&apipb.OrgMemberUpdateResponse{}), nil
}

func (a *consoleService) OrgMemberRemove(ctx context.Context, req *connect.Request[apipb.OrgMemberRemoveRequest]) (*connect.Response[apipb.OrgMemberRemoveResponse], error) {
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
	return connect.NewResponse(&apipb.OrgMemberRemoveResponse{}), nil
}

// --- Worker handlers ---

func (a *consoleService) WorkerCreate(ctx context.Context, req *connect.Request[apipb.WorkerCreateRequest]) (*connect.Response[apipb.WorkerCreateResponse], error) {
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
	return connect.NewResponse(&apipb.WorkerCreateResponse{Id: id.Bytes()}), nil
}

func (a *consoleService) WorkerGet(ctx context.Context, req *connect.Request[apipb.WorkerGetRequest]) (*connect.Response[apipb.WorkerGetResponse], error) {
	wid, err := IDFromBytes[WorkerKind](req.Msg.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	w, err := a.srv.db.WorkerGet(ctx, ActorFromContext(ctx), wid)
	if err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&apipb.WorkerGetResponse{Worker: workerToProto(*w)}), nil
}

func (a *consoleService) WorkerList(ctx context.Context, req *connect.Request[apipb.WorkerListRequest]) (*connect.Response[apipb.WorkerListResponse], error) {
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

	out := make([]*apipb.Worker, len(workers))
	for i := range workers {
		out[i] = workerToProto(workers[i])
	}

	var lastID WorkerID
	if len(workers) > 0 {
		lastID = workers[len(workers)-1].ID
	}

	return connect.NewResponse(&apipb.WorkerListResponse{
		Page:    pageResponse(len(workers), limit, lastID, total),
		Workers: out,
	}), nil
}

func (a *consoleService) WorkerDelete(ctx context.Context, req *connect.Request[apipb.WorkerDeleteRequest]) (*connect.Response[apipb.WorkerDeleteResponse], error) {
	wid, err := IDFromBytes[WorkerKind](req.Msg.Id)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := a.srv.db.WorkerDelete(ctx, ActorFromContext(ctx), wid); err != nil {
		return nil, mapErr(err)
	}
	return connect.NewResponse(&apipb.WorkerDeleteResponse{}), nil
}
