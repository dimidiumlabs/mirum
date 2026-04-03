// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/tern/v2/migrate"
)

var (
	ErrOpen    = errors.New("database: failed to open")
	ErrPing    = errors.New("database: failed to ping")
	ErrAcquire = errors.New("database: failed to acquire connection")
	ErrMigrate = errors.New("database: failed to create migrator")
)

// DB wraps a pgx connection pool.
type DB struct {
	Pool *pgxpool.Pool
}

// Open connects to PostgreSQL and returns a DB.
func Open(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, errors.Join(ErrOpen, err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.Join(ErrPing, err)
	}

	return &DB{Pool: pool}, nil
}

// Close closes the connection pool.
func (db *DB) Close() {
	db.Pool.Close()
}

// Migrate applies all pending migrations.
func (db *DB) Migrate(ctx context.Context) error {
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		return errors.Join(ErrAcquire, err)
	}
	defer conn.Release()

	migrator, err := migrate.NewMigrator(ctx, conn.Conn(), "schema_version")
	if err != nil {
		return errors.Join(ErrMigrate, err)
	}

	migrator.AppendMigration("create_users",
		`CREATE TABLE users (
			id         UUID PRIMARY KEY DEFAULT uuidv7(),
			email      TEXT NOT NULL UNIQUE,
			password   TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			deleted_at TIMESTAMPTZ
		)`,
		`DROP TABLE users`,
	)

	migrator.AppendMigration("create_sessions",
		`CREATE TABLE sessions (
			token      TEXT PRIMARY KEY,
			user_id    UUID NOT NULL REFERENCES users(id),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			expires_at TIMESTAMPTZ NOT NULL
		);
		CREATE INDEX sessions_expires_at ON sessions (expires_at)`,
		`DROP TABLE sessions`,
	)

	migrator.AppendMigration("create_workers",
		`CREATE TABLE workers (
			id         UUID PRIMARY KEY DEFAULT uuidv7(),
			public_key BYTEA NOT NULL UNIQUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			revoked_at TIMESTAMPTZ
		)`,
		`DROP TABLE workers`,
	)

	migrator.AppendMigration("create_organizations",
		`CREATE TABLE organizations (
			id         UUID PRIMARY KEY DEFAULT uuidv7(),
			name       TEXT NOT NULL,
			slug       TEXT NOT NULL UNIQUE,
			public     BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			deleted_at TIMESTAMPTZ
		)`,
		`DROP TABLE organizations`,
	)

	migrator.AppendMigration("create_org_members",
		`CREATE TABLE org_members (
			org_id     UUID NOT NULL REFERENCES organizations(id),
			user_id    UUID NOT NULL REFERENCES users(id),
			role       TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'admin', 'member')),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (org_id, user_id)
		);
		CREATE INDEX org_members_user_id ON org_members (user_id)`,
		`DROP TABLE org_members`,
	)

	return migrator.Migrate(ctx)
}
