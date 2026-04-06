// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"slices"

	"dimidiumlabs/mirum/internal/protocol/pb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrPermissionDenied = errors.New("database: permission denied")
	ErrUnauthenticated  = errors.New("database: authentication required")
)

var anonPermissions = []pb.Perm{
	pb.Perm_PERM_ORG_READ,
}

var userGlobalPermissions = []pb.Perm{
	pb.Perm_PERM_ORG_READ,
	pb.Perm_PERM_ORG_WRITE,
	pb.Perm_PERM_USER_READ,
}

// rolePermissions is the single source of truth for role → perm bundles.
// RLS checks only tenancy (membership); action authz lives here.
var rolePermissions = map[string][]pb.Perm{
	"owner": {
		pb.Perm_PERM_ORG_READ,
		pb.Perm_PERM_ORG_WRITE,
		pb.Perm_PERM_ORG_DELETE,
		pb.Perm_PERM_ORG_MEMBER_READ,
		pb.Perm_PERM_ORG_MEMBER_WRITE,
		pb.Perm_PERM_WORKER_READ,
		pb.Perm_PERM_WORKER_WRITE,
	},
	"admin": {
		pb.Perm_PERM_ORG_READ,
		pb.Perm_PERM_ORG_WRITE,
		pb.Perm_PERM_ORG_MEMBER_READ,
		pb.Perm_PERM_ORG_MEMBER_WRITE,
		pb.Perm_PERM_WORKER_READ,
		pb.Perm_PERM_WORKER_WRITE,
	},
	"member": {
		pb.Perm_PERM_ORG_READ,
		pb.Perm_PERM_ORG_MEMBER_READ,
		pb.Perm_PERM_WORKER_READ,
	},
}

// Actor is the principal making a database request. It carries identity,
// display metadata, and coarse capability. Zero value is invalid: dbID
// panics, so a missing initialisation cannot silently grant privileges.
//
// Synthetic actors (System/Operator/Anon) live only as Go constants —
// they are not rows in the users table, so they cannot be logged in as
// even if somebody writes a password into the DB.
//
// Authorization is divided into two planes:
//   - Tenancy: an actor can only see a subset of resources to which
//     they have access (public or through organization membership).
//     Any select statement will return only records accessible to the actor.
//   - RBAC: what the actor can do with records (create/read/write) is implemented here.
//     Any rights we grant here are a strict subset of the Tenancy rights.
//     The list of perms can be either explicit (for tokens) or implied (for user roles).
type Actor struct {
	kind      actorKind
	id        uuid.UUID
	email     string
	superuser bool
}

type actorKind uint8

const (
	actorInvalid actorKind = iota
	actorUser
	actorOperator
	actorSystem
	actorAnon
)

// ActorKind is the exported form of actorKind for audit sinks and logging.
type ActorKind uint8

const (
	KindInvalid ActorKind = iota
	KindUser
	KindOperator
	KindSystem
	KindAnon
)

var (
	anonUUID     = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	systemUUID   = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	operatorUUID = uuid.MustParse("00000000-0000-0000-0000-000000000002")
)

// UserActor identifies an authenticated user from a session or token.
func UserActor(id UserID, email string, superuser bool) Actor {
	if id.IsZero() {
		panic("database: UserActor with nil UUID")
	}
	if email == "" {
		panic("database: UserActor with empty email")
	}
	return Actor{kind: actorUser, id: id.UUID(), email: email, superuser: superuser}
}

// OperatorActor is the principal for externally invoked privileged
// operations (admin socket). Distinguishable from System in audit logs.
func OperatorActor() Actor {
	return Actor{kind: actorOperator, id: operatorUUID, email: "operator@mirum.local", superuser: true}
}

// SystemActor is the principal for internal machinery (mTLS handshake,
// session bootstrap, background jobs). Not an operator action.
func SystemActor() Actor {
	return Actor{kind: actorSystem, id: systemUUID, email: "system@mirum.local", superuser: true}
}

// AnonActor is the principal for unauthenticated public requests.
func AnonActor() Actor {
	return Actor{kind: actorAnon, id: anonUUID, email: "anonymous@mirum.local"}
}

func (a Actor) Kind() ActorKind {
	switch a.kind {
	case actorUser:
		return KindUser
	case actorOperator:
		return KindOperator
	case actorSystem:
		return KindSystem
	case actorAnon:
		return KindAnon
	}
	return KindInvalid
}

func (a Actor) UserID() UserID    { return UserID(a.id) }
func (a Actor) Email() string     { return a.email }
func (a Actor) IsSuperuser() bool { return a.superuser }

// dbID returns the UUID to write into app.user_id. Panics on zero value.
func (a Actor) dbID() uuid.UUID {
	if a.id == uuid.Nil {
		panic("database: zero-value Actor; use UserActor/SystemActor/OperatorActor/AnonActor")
	}
	return a.id
}

// kindString returns the string written into app.actor_kind.
// It must match the values tested by app_issuper() in the SQL migration.
func (a Actor) kindString() string {
	switch a.kind {
	case actorUser:
		return "user"
	case actorOperator:
		return "operator"
	case actorSystem:
		return "system"
	case actorAnon:
		return "anon"
	}
	panic("database: zero-value Actor; use UserActor/SystemActor/OperatorActor/AnonActor")
}

// checkGlobal checks a global-scope perm (no specific org). Pure, no DB.
func checkGlobal(actor Actor, perm pb.Perm) error {
	switch actor.kind {
	case actorOperator, actorSystem:
		return nil
	case actorAnon:
		if slices.Contains(anonPermissions, perm) {
			return nil
		}
		return ErrUnauthenticated
	case actorUser:
		if actor.superuser {
			return nil
		}
		if slices.Contains(userGlobalPermissions, perm) {
			return nil
		}
		return ErrPermissionDenied
	default:
		return ErrPermissionDenied
	}
}

// checkPerm checks an org-scoped perm within an existing transaction.
func checkPerm(ctx context.Context, tx pgx.Tx, actor Actor, orgID OrgID, perm pb.Perm) error {
	switch actor.kind {
	case actorOperator, actorSystem:
		return nil
	case actorAnon:
		return ErrUnauthenticated
	case actorUser:
		if actor.superuser {
			return nil
		}
		var role string
		err := tx.QueryRow(ctx,
			`SELECT role FROM org_members WHERE org_id = $1 AND user_id = $2`,
			orgID, actor.id,
		).Scan(&role)
		if err != nil {
			return ErrPermissionDenied
		}
		if !slices.Contains(rolePermissions[role], perm) {
			return ErrPermissionDenied
		}
		return nil
	default:
		return ErrPermissionDenied
	}
}

// checkSystem checks that actor is the internal system principal.
func checkSystem(actor Actor) error {
	if actor.kind == actorSystem {
		return nil
	}
	return ErrPermissionDenied
}

// checkSelf checks that actor is the target user or superuser.
func checkSelf(actor Actor, targetID UserID) error {
	switch actor.kind {
	case actorOperator, actorSystem:
		return nil
	case actorAnon:
		return ErrUnauthenticated
	case actorUser:
		if actor.superuser || targetID == actor.UserID() {
			return nil
		}
		return ErrPermissionDenied
	default:
		return ErrPermissionDenied
	}
}
