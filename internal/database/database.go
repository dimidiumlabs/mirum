// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/tern/v2/migrate"
)

var (
	ErrOpen                 = errors.New("database: failed to open")
	ErrPing                 = errors.New("database: failed to ping")
	ErrAcquire              = errors.New("database: failed to acquire connection")
	ErrMigrate              = errors.New("database: failed to create migrator")
	ErrFilterNotImplemented = errors.New("database: filter not implemented")
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

// beginAs starts a transaction and sets the RLS actor.
func (db *DB) beginAs(ctx context.Context, actor uuid.UUID) (pgx.Tx, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.user_id', $1, true)", actor.String()); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// Migrate applies all pending migrations.
func (db *DB) Migrate(ctx context.Context) error {
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		return errors.Join(ErrAcquire, err)
	}
	defer conn.Release()

	// NOTE: RLS is active on all tables. Migrations with DML (INSERT/UPDATE/DELETE)
	// must prefix the SQL with:
	//   SELECT set_config('app.user_id', '00000000-0000-0000-0000-000000000000', true);
	// This sets the root superuser for the migration's transaction only.
	migrator, err := migrate.NewMigrator(ctx, conn.Conn(), "schema_version")
	if err != nil {
		return errors.Join(ErrMigrate, err)
	}

	migrator.AppendMigration("create_users", `
		CREATE TABLE users (
			id         UUID PRIMARY KEY DEFAULT uuidv7(),
			email      TEXT NOT NULL UNIQUE,
			password   TEXT NOT NULL,
			superuser  BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			deleted_at TIMESTAMPTZ
		 );

		INSERT INTO users (id, email, password, superuser)
		VALUES ('00000000-0000-0000-0000-000000000000', 'root@localhost', '', true);

		CREATE FUNCTION app_user_id() RETURNS uuid STABLE AS $$
			SELECT current_setting('app.user_id', true)::uuid;
		$$ LANGUAGE sql;

		CREATE FUNCTION app_issuper() RETURNS boolean STABLE AS $$
			SELECT EXISTS (
				SELECT 1 FROM users
				WHERE id = current_setting('app.user_id', true)::uuid
				  AND superuser = true
			);
		$$ LANGUAGE sql;
	`, `
		DROP FUNCTION app_issuper;
		DROP FUNCTION app_user_id;
		DROP TABLE users
	`)

	migrator.AppendMigration("create_sessions", `
		CREATE TABLE sessions (
			token      TEXT PRIMARY KEY,
			user_id    UUID NOT NULL REFERENCES users(id),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			expires_at TIMESTAMPTZ NOT NULL
		);
		CREATE INDEX sessions_expires_at ON sessions (expires_at)
	`, `
		DROP TABLE sessions
	`)

	migrator.AppendMigration("create_organizations", `
		CREATE TABLE organizations (
			id         UUID PRIMARY KEY DEFAULT uuidv7(),
			name       TEXT NOT NULL,
			slug       TEXT NOT NULL UNIQUE,
			public     BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			deleted_at TIMESTAMPTZ
		);
	`, `
		DROP TABLE organizations
	`)

	migrator.AppendMigration("create_org_members", `
		CREATE TABLE org_members (
			org_id     UUID NOT NULL REFERENCES organizations(id),
			user_id    UUID NOT NULL REFERENCES users(id),
			role       TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'admin', 'member')),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (org_id, user_id)
		);
		CREATE INDEX org_members_user_id ON org_members (user_id)
	`, `
		DROP TABLE org_members
	`)

	migrator.AppendMigration("create_workers", `
		CREATE TABLE workers (
			id         UUID PRIMARY KEY DEFAULT uuidv7(),
			org_id     UUID REFERENCES organizations(id),
			public_key BYTEA NOT NULL UNIQUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			revoked_at TIMESTAMPTZ
		)
	`, `
		DROP TABLE workers
	`)

	migrator.AppendMigration("rls_helper_functions", `
		CREATE FUNCTION has_org_role(org uuid, roles text[]) RETURNS boolean STABLE AS $$
			SELECT EXISTS (
				SELECT 1 FROM org_members
				WHERE org_id = org AND user_id = app_user_id() AND role = ANY(roles)
			);
		$$ LANGUAGE sql
	`, `
		DROP FUNCTION has_org_role
	`)

	migrator.AppendMigration("rls_users", `
		ALTER TABLE users ENABLE ROW LEVEL SECURITY;
		ALTER TABLE users FORCE ROW LEVEL SECURITY;

		CREATE POLICY superuser ON users FOR ALL USING (app_issuper());
		CREATE POLICY user_select ON users FOR SELECT USING (id = app_user_id());
		CREATE POLICY user_update ON users FOR UPDATE USING (id = app_user_id());
		CREATE POLICY user_delete ON users FOR DELETE USING (id = app_user_id());
		CREATE POLICY user_insert ON users FOR INSERT WITH CHECK (app_issuper());
	`, `
		DROP POLICY superuser ON users;
		DROP POLICY user_select ON users;
		DROP POLICY user_insert ON users;
		DROP POLICY user_update ON users;
		DROP POLICY user_delete ON users;

		ALTER TABLE users DISABLE ROW LEVEL SECURITY;
	`)

	migrator.AppendMigration("rls_sessions", `
		ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
		ALTER TABLE sessions FORCE ROW LEVEL SECURITY;

		CREATE POLICY superuser ON sessions FOR ALL USING (app_issuper());
		CREATE POLICY own_sessions ON sessions FOR ALL USING (user_id = app_user_id());
	`, `
		DROP POLICY superuser ON sessions;
		DROP POLICY own_sessions ON sessions;

		ALTER TABLE sessions DISABLE ROW LEVEL SECURITY;
	`)

	migrator.AppendMigration("rls_organizations", `
		ALTER TABLE organizations ENABLE ROW LEVEL SECURITY;
		ALTER TABLE organizations FORCE ROW LEVEL SECURITY;

		CREATE POLICY superuser ON organizations FOR ALL USING (app_issuper());
		CREATE POLICY select_org ON organizations FOR SELECT USING (public OR has_org_role(id, '{owner,admin,member}'));
		CREATE POLICY update_org ON organizations FOR UPDATE USING (has_org_role(id, '{owner,admin}'));
		CREATE POLICY delete_org ON organizations FOR DELETE USING (has_org_role(id, '{owner}'));
		CREATE POLICY insert_org ON organizations FOR INSERT WITH CHECK (true);
	`, `
		DROP POLICY superuser ON organizations;
		DROP POLICY select_org ON organizations;
		DROP POLICY update_org ON organizations;
		DROP POLICY delete_org ON organizations;
		DROP POLICY insert_org ON organizations;
	
		ALTER TABLE organizations DISABLE ROW LEVEL SECURITY;
	`)

	migrator.AppendMigration("rls_org_members", `
		ALTER TABLE org_members ENABLE ROW LEVEL SECURITY;
		ALTER TABLE org_members FORCE ROW LEVEL SECURITY;

		CREATE POLICY superuser ON org_members FOR ALL USING (app_issuper());
		CREATE POLICY select_member ON org_members FOR SELECT USING (has_org_role(org_id, '{owner,admin,member}'));
		CREATE POLICY insert_member ON org_members FOR INSERT WITH CHECK (
			has_org_role(org_id, '{owner,admin}')
			OR (
				user_id = app_user_id()
				AND role = 'owner'
				AND NOT EXISTS (SELECT 1 FROM org_members existing WHERE existing.org_id = org_members.org_id)
			)
		);
		CREATE POLICY update_member ON org_members FOR UPDATE USING (has_org_role(org_id, '{owner,admin}'));
		CREATE POLICY delete_member ON org_members FOR DELETE USING (has_org_role(org_id, '{owner,admin}'));
	`, `
		DROP POLICY superuser ON org_members;
		DROP POLICY select_member ON org_members;
		DROP POLICY insert_member ON org_members;
		DROP POLICY update_member ON org_members;
		DROP POLICY delete_member ON org_members;

		ALTER TABLE org_members DISABLE ROW LEVEL SECURITY;
	`)

	migrator.AppendMigration("rls_workers", `
		ALTER TABLE workers ENABLE ROW LEVEL SECURITY;
		ALTER TABLE workers FORCE ROW LEVEL SECURITY;

		CREATE POLICY superuser ON workers FOR ALL USING (app_issuper());
		CREATE POLICY select_worker ON workers FOR SELECT USING (org_id IS NOT NULL AND has_org_role(org_id, '{owner,admin,member}'));
		CREATE POLICY insert_worker ON workers FOR INSERT WITH CHECK (org_id IS NULL OR has_org_role(org_id, '{owner,admin}'));
		CREATE POLICY update_worker ON workers FOR UPDATE USING (org_id IS NOT NULL AND has_org_role(org_id, '{owner,admin}'));
		CREATE POLICY delete_worker ON workers FOR DELETE USING (org_id IS NOT NULL AND has_org_role(org_id, '{owner,admin}'));
	`, `
		DROP POLICY superuser ON workers;
		DROP POLICY select_worker ON workers;
		DROP POLICY insert_worker ON workers;
		DROP POLICY update_worker ON workers;
		DROP POLICY delete_worker ON workers;

		ALTER TABLE workers DISABLE ROW LEVEL SECURITY;
	`)

	return migrator.Migrate(ctx)
}
