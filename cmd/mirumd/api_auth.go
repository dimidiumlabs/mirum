// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"slices"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"dimidiumlabs/mirum/internal/database"
	"dimidiumlabs/mirum/internal/protocol/pb"
	"dimidiumlabs/mirum/internal/protocol/pb/pbconnect"
)

// Role → permissions mapping.
var rolePermissions = map[pb.Role][]pb.Perm{
	pb.Role_ROLE_OWNER: {
		pb.Perm_PERM_ORG_READ, pb.Perm_PERM_ORG_WRITE, pb.Perm_PERM_ORG_DELETE,
		pb.Perm_PERM_ORG_MEMBER_READ, pb.Perm_PERM_ORG_MEMBER_WRITE,
		pb.Perm_PERM_WORKER_READ, pb.Perm_PERM_WORKER_WRITE,
	},
	pb.Role_ROLE_ADMIN: {
		pb.Perm_PERM_ORG_READ, pb.Perm_PERM_ORG_WRITE,
		pb.Perm_PERM_ORG_MEMBER_READ, pb.Perm_PERM_ORG_MEMBER_WRITE,
		pb.Perm_PERM_WORKER_READ, pb.Perm_PERM_WORKER_WRITE,
	},
	pb.Role_ROLE_MEMBER: {
		pb.Perm_PERM_ORG_READ,
		pb.Perm_PERM_ORG_MEMBER_READ,
		pb.Perm_PERM_WORKER_READ,
	},
}

// ApiAuthInterceptor enforces authorization on admin RPCs.
// Authentication is handled upstream: web sessionMiddleware for TCP,
// ConnContext for unix socket.
type ApiAuthInterceptor struct {
	srv *server
}

func (a *ApiAuthInterceptor) authorize(ctx context.Context, procedure string, req connect.AnyRequest) error {
	// Public routes — no auth required, visibility enforced in handler/DB.
	switch procedure {
	case pbconnect.AdminOrgListProcedure,
		pbconnect.AdminOrgGetProcedure:
		return nil
	}

	caller := CallerFromContext(ctx)
	if caller == nil {
		return connect.NewError(connect.CodeUnauthenticated, nil)
	}
	if caller.Superuser {
		return nil
	}

	switch procedure {
	// User — self or superuser
	case pbconnect.AdminUserGetProcedure,
		pbconnect.AdminUserUpdateProcedure,
		pbconnect.AdminUserDeleteProcedure:
		if req == nil {
			return errDenied
		}

		if isSelf(*caller, req) {
			return nil
		}
		return errDenied

	// User — superuser only
	case pbconnect.AdminUserCreateProcedure,
		pbconnect.AdminUserListProcedure:
		return errDenied

	// Org — any authenticated
	case pbconnect.AdminOrgCreateProcedure:
		return nil
	case pbconnect.AdminOrgUpdateProcedure:
		return a.checkOrgPerm(ctx, *caller, req, pb.Perm_PERM_ORG_WRITE)
	case pbconnect.AdminOrgDeleteProcedure:
		return a.checkOrgPerm(ctx, *caller, req, pb.Perm_PERM_ORG_DELETE)

	// OrgMember — org-scoped
	case pbconnect.AdminOrgMemberGetProcedure,
		pbconnect.AdminOrgMemberListProcedure:
		return a.checkOrgPerm(ctx, *caller, req, pb.Perm_PERM_ORG_MEMBER_READ)
	case pbconnect.AdminOrgMemberAddProcedure,
		pbconnect.AdminOrgMemberUpdateProcedure,
		pbconnect.AdminOrgMemberRemoveProcedure:
		return a.checkOrgPerm(ctx, *caller, req, pb.Perm_PERM_ORG_MEMBER_WRITE)

	// Worker — any authenticated can read
	case pbconnect.AdminWorkerGetProcedure,
		pbconnect.AdminWorkerListProcedure:
		return nil

	// Worker create — with org: org perm, without: superuser
	case pbconnect.AdminWorkerCreateProcedure:
		if orgRefFromRequest(req) == nil {
			return errDenied
		}
		return a.checkOrgPerm(ctx, *caller, req, pb.Perm_PERM_WORKER_WRITE)

	// Worker delete — lookup worker's org, then check
	case pbconnect.AdminWorkerDeleteProcedure:
		return a.checkWorkerDelete(ctx, *caller, req)

	default:
		return errDenied
	}
}

var errDenied = connect.NewError(connect.CodePermissionDenied, nil)

// checkOrgPerm extracts OrgRef from request and checks caller's role permission.
func (a *ApiAuthInterceptor) checkOrgPerm(ctx context.Context, caller callerInfo, req connect.AnyRequest, perm pb.Perm) error {
	ref := orgRefFromRequest(req)
	if ref == nil {
		return errDenied
	}
	member, err := a.srv.db.GetOrgMember(ctx, caller.UserID, orgRef(ref), database.UserByID(caller.UserID))
	if err != nil {
		return errDenied
	}
	if !slices.Contains(rolePermissions[roleToProto[member.Role]], perm) {
		return errDenied
	}
	return nil
}

// checkWorkerDelete looks up the worker's org and checks permission.
func (a *ApiAuthInterceptor) checkWorkerDelete(ctx context.Context, caller callerInfo, req connect.AnyRequest) error {
	m, ok := req.Any().(*pb.WorkerDeleteRequest)
	if !ok {
		return errDenied
	}
	w, err := a.srv.db.GetWorker(ctx, caller.UserID, uuid.UUID(m.Id))
	if err != nil {
		return errDenied
	}
	if w.OrgID == nil {
		return errDenied // global worker — superuser only
	}
	member, err := a.srv.db.GetOrgMember(ctx, caller.UserID, database.OrgByID(*w.OrgID), database.UserByID(caller.UserID))
	if err != nil {
		return errDenied
	}
	if !slices.Contains(rolePermissions[roleToProto[member.Role]], pb.Perm_PERM_WORKER_WRITE) {
		return errDenied
	}
	return nil
}

// isSelf checks if the request targets the caller's own user.
func isSelf(caller callerInfo, req connect.AnyRequest) bool {
	var ref *pb.UserRef
	switch m := req.Any().(type) {
	case *pb.UserGetRequest:
		ref = m.User
	case *pb.UserUpdateRequest:
		ref = m.User
	case *pb.UserDeleteRequest:
		ref = m.User
	}
	if ref == nil {
		return false
	}
	switch v := ref.GetRef().(type) {
	case *pb.UserRef_Id:
		return uuid.UUID(v.Id) == caller.UserID
	case *pb.UserRef_Email:
		return v.Email == caller.Email
	default:
		return false
	}
}

// orgRefFromRequest extracts the OrgRef from requests that carry one.
func orgRefFromRequest(req connect.AnyRequest) *pb.OrgRef {
	if req == nil {
		return nil
	}
	switch m := req.Any().(type) {
	case *pb.OrgGetRequest:
		return m.Org
	case *pb.OrgUpdateRequest:
		return m.Org
	case *pb.OrgDeleteRequest:
		return m.Org
	case *pb.OrgMemberAddRequest:
		return m.Org
	case *pb.OrgMemberGetRequest:
		return m.Org
	case *pb.OrgMemberListRequest:
		return m.Org
	case *pb.OrgMemberUpdateRequest:
		return m.Org
	case *pb.OrgMemberRemoveRequest:
		return m.Org
	case *pb.WorkerCreateRequest:
		return m.Org
	default:
		return nil
	}
}

func (a *ApiAuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := a.authorize(ctx, req.Spec().Procedure, req); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (a *ApiAuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		panic("admin service does not make outbound streaming calls")
	}
}

func (a *ApiAuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := a.authorize(ctx, conn.Spec().Procedure, nil); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}
