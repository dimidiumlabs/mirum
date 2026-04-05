// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import "github.com/google/uuid"

// Actor is the principal making a database request. It carries identity,
// display metadata, and coarse capability. Zero value is invalid: dbID
// panics, so a missing initialisation cannot silently grant privileges.
//
// Synthetic actors (System/Operator/Anon) live only as Go constants —
// they are not rows in the users table, so they cannot be logged in as
// even if somebody writes a password into the DB.
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
	systemUUID   = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	operatorUUID = uuid.MustParse("00000000-0000-0000-0000-000000000002")
	anonUUID     = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
)

// UserActor identifies an authenticated user from a session or token.
func UserActor(id uuid.UUID, email string, superuser bool) Actor {
	if id == uuid.Nil {
		panic("database: UserActor with nil UUID")
	}
	if email == "" {
		panic("database: UserActor with empty email")
	}
	return Actor{kind: actorUser, id: id, email: email, superuser: superuser}
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

func (a Actor) UserID() uuid.UUID { return a.id }
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
