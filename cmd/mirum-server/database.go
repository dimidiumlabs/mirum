// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"dimidiumlabs/mirum/cmd/mirum-server/apipb"
	"dimidiumlabs/mirum/internal/config"

	sb "github.com/huandu/go-sqlbuilder"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/tern/v2/migrate"
	"golang.org/x/crypto/argon2"
)

var (
	ErrAcquire        = errors.New("database: failed to acquire connection")
	ErrAlreadyMember  = errors.New("database: already a member")
	ErrEmailTaken     = errors.New("database: email already taken")
	ErrInvalidCreds   = errors.New("database: invalid credentials")
	ErrInvalidDateFormat = errors.New("database: invalid date format")
	ErrInvalidEmail      = errors.New("database: invalid email")
	ErrInvalidRole       = errors.New("database: invalid role")
	ErrInvalidTimezone   = errors.New("database: invalid timezone")
	ErrInvalidSlug    = errors.New("database: invalid slug")
	ErrLastOwner      = errors.New("database: last owner")
	ErrMigrate        = errors.New("database: failed to create migrator")
	ErrNotImplemented = errors.New("database: filter not implemented")
	ErrNotMember      = errors.New("database: not a member")
	ErrOpen           = errors.New("database: failed to open")
	ErrOrgNotFound    = errors.New("database: organization not found")
	ErrPing           = errors.New("database: failed to ping")
	ErrReservedEmail  = errors.New("database: email uses a reserved domain")
	ErrSlugTaken      = errors.New("database: slug already taken")
	ErrSoleOwner      = errors.New("database: sole owner of an organization")
	ErrUserNotFound   = errors.New("database: user not found")
	ErrWorkerNotFound = errors.New("database: worker not found")
)

// reservedEmailSuffix is the domain carved out for synthetic actors
// (system/operator/anon). Real users cannot register with this suffix.
const reservedEmailSuffix = "@mirum.local"

const (
	saltLen = 16

	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MB
	argonKeyLen  = 32
	argonThreads = 2
)

var slugRe = regexp.MustCompile(`^[a-zA-Z0-9]+(?:-[a-zA-Z0-9]+)*$`)

// UserRef identifies a user by ID or email.
type UserRef struct {
	id    UserID
	email string
}

func UserByID(id UserID) UserRef { return UserRef{id: id} }

func UserByEmail(email string) UserRef { return UserRef{email: email} }

func (r UserRef) where() (string, any) {
	if !r.id.IsZero() {
		return "id", r.id
	}
	return "email", r.email
}

// OrgRef identifies an organization by ID or slug.
type OrgRef struct {
	id   OrgID
	slug string
}

func OrgByID(id OrgID) OrgRef { return OrgRef{id: id} }

func OrgBySlug(slug string) OrgRef { return OrgRef{slug: slug} }

func (r OrgRef) IsZero() bool { return r.id.IsZero() && r.slug == "" }

func (r OrgRef) where() (string, any) {
	if !r.id.IsZero() {
		return "id", r.id
	}
	return "slug", r.slug
}

// DB wraps a pgx connection pool.
type DB struct {
	Pool *pgxpool.Pool
}

type DateFormat string

const (
	DateFormatDMY DateFormat = "dmy"
	DateFormatMDY DateFormat = "mdy"
	DateFormatYMD DateFormat = "ymd"
)

// LocaleSettings holds a user's language and date-format preferences,
// stored as a composite column in the database.
type LocaleSettings struct {
	Language   *string
	DateFormat *DateFormat
}

// User holds info about a user.
type User struct {
	ID        UserID
	Email     string
	CreatedAt time.Time
	Locale    *LocaleSettings
	Timezone  *string
}

type UserUpdateParams struct {
	Email    *string
	Password *string
	Pepper   []byte
	Locale   *LocaleSettings
	Timezone *string
}

// Organization holds info about an organization.
type Organization struct {
	ID        OrgID
	Name      string
	Slug      string
	Public    bool
	CreatedAt time.Time
}

// OrgMember pairs a user with their role in an organization.
type OrgMember struct {
	User     User
	Role     string
	JoinedAt time.Time
}

// Worker holds info about a registered worker.
type Worker struct {
	ID        WorkerID
	OrgID     *OrgID
	PublicKey []byte
	CreatedAt time.Time
}

// DatabaseOpen connects to PostgreSQL and returns a DB.
func DatabaseOpen(ctx context.Context, dsn string) (*DB, error) {
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

// apicall starts a transaction and sets the RLS actor. Both app.user_id
// and app.actor_kind are populated: app_issuper() checks actor_kind for
// System/Operator principals, and app.user_id for real user superusers.
func (db *DB) apicall(ctx context.Context, actor Actor, access, validate, doit func(pgx.Tx) error) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			slog.Error("rollback failed", "err", err)
		}
	}()

	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.user_id', $1, true),
		        set_config('app.actor_kind', $2, true)`,
		actor.dbID().String(), actor.kindString(),
	); err != nil {
		return err
	}

	if err := access(tx); err != nil {
		slog.Debug("access denied", "err", err)
		return err
	}
	if validate != nil {
		if err := validate(tx); err != nil {
			slog.Debug("validation failed", "err", err)
			return err
		}
	}
	if err := doit(tx); err != nil {
		slog.Debug("exec failed", "err", err)
		return err
	}

	return tx.Commit(ctx)
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

	migrator.AppendMigration("create_users", `
		CREATE TABLE users (
			id         UUID PRIMARY KEY DEFAULT uuidv7(),
			email      TEXT NOT NULL UNIQUE,
			password   TEXT NOT NULL,
			superuser  BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			deleted_at TIMESTAMPTZ
		 );

		CREATE FUNCTION app_user_id() RETURNS uuid STABLE AS $$
			SELECT current_setting('app.user_id', true)::uuid;
		$$ LANGUAGE sql;

		-- app_issuper has two independent branches:
		--  (a) runtime setting app.actor_kind is 'system' or 'operator' —
		--      set only by apicall from Go for synthetic principals and by
		--      DML migrations; cannot be injected via login since there is
		--      no matching users row to authenticate against.
		--  (b) the current app.user_id resolves to a users row with
		--      superuser = true — real support-agent style superusers.
		CREATE FUNCTION app_issuper() RETURNS boolean STABLE AS $$
			SELECT
				current_setting('app.actor_kind', true) IN ('system', 'operator')
				OR EXISTS (
					SELECT 1 FROM users
					WHERE id = current_setting('app.user_id', true)::uuid
					  AND superuser = true
				);
		$$ LANGUAGE sql;
	`, `
		DROP FUNCTION app_issuper;
		DROP FUNCTION app_user_id;
		DROP TABLE users;
	`)

	migrator.AppendMigration("create_sessions", `
		CREATE TABLE sessions (
			token      TEXT PRIMARY KEY,
			user_id    UUID NOT NULL REFERENCES users(id),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			expires_at TIMESTAMPTZ NOT NULL
		);
		CREATE INDEX sessions_expires_at ON sessions (expires_at);
	`, `
		DROP TABLE sessions;
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
		DROP TABLE organizations;
	`)

	migrator.AppendMigration("create_org_members", `
		CREATE TABLE org_members (
			org_id     UUID NOT NULL REFERENCES organizations(id),
			user_id    UUID NOT NULL REFERENCES users(id),
			role       TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'admin', 'member')),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (org_id, user_id)
		);
		CREATE INDEX org_members_user_id ON org_members (user_id);
	
		CREATE FUNCTION is_member(org uuid) RETURNS boolean STABLE AS $$
			SELECT EXISTS (
				SELECT 1 FROM org_members
				WHERE org_id = org AND user_id = app_user_id()
			);
		$$ LANGUAGE sql;

		CREATE FUNCTION is_authenticated() RETURNS boolean STABLE AS $$
			SELECT current_setting('app.actor_kind', true) NOT IN ('', 'anon');
		$$ LANGUAGE sql;
	`, `
		DROP FUNCTION is_authenticated;
		DROP FUNCTION is_member;
		DROP TABLE org_members;
	`)

	migrator.AppendMigration("create_workers", `
		CREATE TABLE workers (
			id         UUID PRIMARY KEY DEFAULT uuidv7(),
			org_id     UUID REFERENCES organizations(id),
			public_key BYTEA NOT NULL UNIQUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			revoked_at TIMESTAMPTZ
		);
	`, `
		DROP TABLE workers
	`)

	migrator.AppendMigration("rls_users", `
		ALTER TABLE users ENABLE ROW LEVEL SECURITY;
		ALTER TABLE users FORCE ROW LEVEL SECURITY;

		CREATE POLICY superuser ON users FOR ALL USING (app_issuper());
		CREATE POLICY self ON users FOR ALL USING (id = app_user_id());
		CREATE POLICY shared_org ON users FOR SELECT USING (EXISTS (
			SELECT 1 FROM org_members target
			JOIN org_members mine ON mine.org_id = target.org_id
			WHERE target.user_id = users.id
			  AND mine.user_id = app_user_id()
		));
		CREATE POLICY write_auth ON users AS RESTRICTIVE FOR INSERT WITH CHECK (is_authenticated());
		CREATE POLICY update_auth ON users AS RESTRICTIVE FOR UPDATE USING (is_authenticated());
		CREATE POLICY delete_auth ON users AS RESTRICTIVE FOR DELETE USING (is_authenticated());
	`, `
		DROP POLICY write_auth ON users;
		DROP POLICY update_auth ON users;
		DROP POLICY delete_auth ON users;
		DROP POLICY superuser ON users;
		DROP POLICY self ON users;
		DROP POLICY shared_org ON users;

		ALTER TABLE users DISABLE ROW LEVEL SECURITY;
	`)

	migrator.AppendMigration("rls_sessions", `
		ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
		ALTER TABLE sessions FORCE ROW LEVEL SECURITY;

		CREATE POLICY superuser ON sessions FOR ALL USING (app_issuper());
		CREATE POLICY own_sessions ON sessions FOR ALL USING (user_id = app_user_id());
		CREATE POLICY write_auth ON sessions AS RESTRICTIVE FOR INSERT WITH CHECK (is_authenticated());
		CREATE POLICY update_auth ON sessions AS RESTRICTIVE FOR UPDATE USING (is_authenticated());
		CREATE POLICY delete_auth ON sessions AS RESTRICTIVE FOR DELETE USING (is_authenticated());
	`, `
		DROP POLICY write_auth ON sessions;
		DROP POLICY update_auth ON sessions;
		DROP POLICY delete_auth ON sessions;
		DROP POLICY superuser ON sessions;
		DROP POLICY own_sessions ON sessions;

		ALTER TABLE sessions DISABLE ROW LEVEL SECURITY;
	`)

	migrator.AppendMigration("rls_organizations", `
		ALTER TABLE organizations ENABLE ROW LEVEL SECURITY;
		ALTER TABLE organizations FORCE ROW LEVEL SECURITY;

		CREATE POLICY superuser   ON organizations FOR ALL USING (app_issuper());
		CREATE POLICY public_org  ON organizations FOR ALL USING (public);
		CREATE POLICY member_org  ON organizations FOR ALL USING (is_member(id));
		CREATE POLICY write_auth  ON organizations AS RESTRICTIVE FOR INSERT WITH CHECK (is_authenticated());
		CREATE POLICY update_auth ON organizations AS RESTRICTIVE FOR UPDATE USING (is_authenticated());
		CREATE POLICY delete_auth ON organizations AS RESTRICTIVE FOR DELETE USING (is_authenticated());
	`, `
		DROP POLICY write_auth ON organizations;
		DROP POLICY update_auth ON organizations;
		DROP POLICY delete_auth ON organizations;
		DROP POLICY superuser ON organizations;
		DROP POLICY public_org ON organizations;
		DROP POLICY member_org ON organizations;

		ALTER TABLE organizations DISABLE ROW LEVEL SECURITY;
	`)

	migrator.AppendMigration("rls_org_members", `
		ALTER TABLE org_members ENABLE ROW LEVEL SECURITY;
		ALTER TABLE org_members FORCE ROW LEVEL SECURITY;

		CREATE POLICY superuser    ON org_members FOR ALL USING (app_issuper());

		-- Self path (own membership rows) uses a pure column predicate
		-- so is_member's inner query can resolve without recursion.
		CREATE POLICY self_member  ON org_members FOR ALL USING (user_id = app_user_id());
		CREATE POLICY org_member   ON org_members FOR ALL USING (is_member(org_id));
		CREATE POLICY write_auth   ON org_members AS RESTRICTIVE FOR INSERT WITH CHECK (is_authenticated());
		CREATE POLICY update_auth  ON org_members AS RESTRICTIVE FOR UPDATE USING (is_authenticated());
		CREATE POLICY delete_auth  ON org_members AS RESTRICTIVE FOR DELETE USING (is_authenticated());
	`, `
		DROP POLICY write_auth ON org_members;
		DROP POLICY update_auth ON org_members;
		DROP POLICY delete_auth ON org_members;
		DROP POLICY superuser ON org_members;
		DROP POLICY self_member ON org_members;
		DROP POLICY org_member ON org_members;

		ALTER TABLE org_members DISABLE ROW LEVEL SECURITY;
	`)

	migrator.AppendMigration("rls_workers", `
		ALTER TABLE workers ENABLE ROW LEVEL SECURITY;
		ALTER TABLE workers FORCE ROW LEVEL SECURITY;

		CREATE POLICY superuser   ON workers FOR ALL USING (app_issuper());
		CREATE POLICY org_worker  ON workers FOR ALL USING (
			org_id IS NOT NULL AND is_member(org_id)
		);
		CREATE POLICY write_auth  ON workers AS RESTRICTIVE FOR INSERT WITH CHECK (is_authenticated());
		CREATE POLICY update_auth ON workers AS RESTRICTIVE FOR UPDATE USING (is_authenticated());
		CREATE POLICY delete_auth ON workers AS RESTRICTIVE FOR DELETE USING (is_authenticated());
	`, `
		DROP POLICY write_auth ON workers;
		DROP POLICY update_auth ON workers;
		DROP POLICY delete_auth ON workers;
		DROP POLICY superuser ON workers;
		DROP POLICY org_worker ON workers;

		ALTER TABLE workers DISABLE ROW LEVEL SECURITY;
	`)

	migrator.AppendMigration("add_user_settings", `
		CREATE DOMAIN timezone_t AS TEXT
			CHECK (VALUE IS NULL OR EXISTS (
				SELECT 1 FROM pg_timezone_names WHERE name = VALUE
			));

		CREATE TYPE date_format_t AS ENUM ('dmy', 'mdy', 'ymd');
		CREATE TYPE locale_settings AS (
			language    TEXT,
			date_format date_format_t,
			timezone    timezone_t
		);
		ALTER TABLE users ADD COLUMN locale locale_settings;
	`, `
		ALTER TABLE users DROP COLUMN locale;
		DROP TYPE locale_settings;
		DROP DOMAIN timezone_t;
		DROP TYPE date_format_t;
	`)

	return migrator.Migrate(ctx)
}

// UserCreate hashes the password with argon2id and inserts a new user.
// The pepper is a server-side secret not stored in the database.
func (db *DB) UserCreate(ctx context.Context, actor Actor, email, password string, pepper []byte) (UserID, error) {
	var id UserID
	err := db.apicall(
		ctx, actor,
		func(tx pgx.Tx) error {
			if !actor.IsSuperuser() {
				return ErrPermissionDenied
			}

			return nil
		},
		func(tx pgx.Tx) error {
			if strings.HasSuffix(strings.ToLower(email), reservedEmailSuffix) {
				return ErrReservedEmail
			}

			return nil
		},
		func(tx pgx.Tx) error {
			hash, err := hashPassword(password, pepper)
			if err != nil {
				return err
			}

			if err := tx.QueryRow(ctx,
				`INSERT INTO users (email, password) VALUES ($1, $2) RETURNING id`,
				email, hash,
			).Scan(&id); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
					return ErrEmailTaken
				}

				return err
			}
			return nil
		},
	)
	return id, err
}

// UserGet returns a user by ref (ID or email).
func (db *DB) UserGet(ctx context.Context, actor Actor, ref UserRef) (*User, error) {
	var u User
	err := db.apicall(
		ctx, actor,
		func(tx pgx.Tx) error { return checkGlobal(actor, apipb.Perm_PERM_USER_READ) },
		nil,
		func(tx pgx.Tx) error {
			col, val := ref.where()

			q := sb.PostgreSQL.NewSelectBuilder()
			sql, args := q.Select("id", "email", "created_at",
				"(locale).language", "(locale).date_format::text", "(locale).timezone").
				From("users").
				Where(q.Equal(col, val), q.IsNull("deleted_at")).
				Build()

			var language, dateFormatStr *string
			if err := tx.QueryRow(ctx, sql, args...).Scan(
				&u.ID, &u.Email, &u.CreatedAt, &language, &dateFormatStr, &u.Timezone,
			); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrUserNotFound
				}
				return err
			}
			if language != nil || dateFormatStr != nil {
				ls := &LocaleSettings{Language: language}
				if dateFormatStr != nil {
					df := DateFormat(*dateFormatStr)
					ls.DateFormat = &df
				}
				u.Locale = ls
			}

			return nil
		},
	)
	return &u, err
}

// UserList returns a page of users and the total count.
func (db *DB) UserList(ctx context.Context, actor Actor, cursor UserID, limit int, filter string) ([]User, int, error) {
	var users []User
	var total int
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error { return checkGlobal(actor, apipb.Perm_PERM_USER_READ) },
		func(tx pgx.Tx) error {
			if filter != "" {
				return ErrNotImplemented
			}
			return nil
		},
		func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM users WHERE deleted_at IS NULL`,
			).Scan(&total); err != nil {
				return err
			}

			q := sb.PostgreSQL.NewSelectBuilder()
			q.Select("id", "email", "created_at").
				From("users").
				Where(q.IsNull("deleted_at")).
				OrderBy("id").
				Limit(limit)
			if !cursor.IsZero() {
				q.Where(q.GreaterThan("id", cursor))
			}

			sql, args := q.Build()
			rows, err := tx.Query(ctx, sql, args...)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				var u User
				if err := rows.Scan(&u.ID, &u.Email, &u.CreatedAt); err != nil {
					return err
				}
				users = append(users, u)
			}
			return rows.Err()
		},
	)
	return users, total, err
}

// UserUpdate updates a user's email, password, and/or preferences.
// Invalidates all sessions when password changes.
func (db *DB) UserUpdate(ctx context.Context, actor Actor, ref UserRef, p UserUpdateParams) error {
	if p.Email == nil && p.Password == nil && p.Locale == nil && p.Timezone == nil {
		return nil
	}

	var id UserID
	return db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			var err error
			id, err = resolveUser(ctx, tx, ref)
			if err != nil {
				return err
			}
			return checkSelf(actor, id)
		},
		func(tx pgx.Tx) error {
			if p.Email != nil && strings.HasSuffix(strings.ToLower(*p.Email), reservedEmailSuffix) {
				return ErrReservedEmail
			}

			return nil
		},
		func(tx pgx.Tx) error {
			ub := sb.PostgreSQL.NewUpdateBuilder()
			ub.Update("users")

			if p.Email != nil {
				ub.SetMore(ub.Assign("email", *p.Email))
			}
			if p.Password != nil {
				hash, err := hashPassword(*p.Password, p.Pepper)
				if err != nil {
					return err
				}
				ub.SetMore(ub.Assign("password", hash))
			}
			if p.Locale != nil || p.Timezone != nil {
				var lang *string
				var dateFormat *DateFormat
				if p.Locale != nil {
					lang = p.Locale.Language
					dateFormat = p.Locale.DateFormat
				}
				ub.SetMore(fmt.Sprintf(
					"locale = ROW(COALESCE(%s, (locale).language), COALESCE(%s::date_format_t, (locale).date_format), COALESCE(%s, (locale).timezone))::locale_settings",
					ub.Var(lang), ub.Var(dateFormat), ub.Var(p.Timezone),
				))
			}
			ub.Where(ub.Equal("id", id))

			sql, args := ub.Build()
			if _, err := tx.Exec(ctx, sql, args...); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
					return ErrEmailTaken
				}
				return err
			}

			if p.Password != nil {
				if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
					return err
				}
			}
			return nil
		},
	)
}

// UserDelete soft-deletes a user.
// Fails if the user is the sole owner of any organization.
func (db *DB) UserDelete(ctx context.Context, actor Actor, ref UserRef) error {
	var id UserID
	return db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			var err error
			id, err = resolveUser(ctx, tx, ref)
			if err != nil {
				return err
			}
			return checkSelf(actor, id)
		},
		func(tx pgx.Tx) error {
			return checkNotSoleOwner(ctx, tx, id)
		},
		func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM org_members WHERE user_id = $1`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE users SET email = id::text, password = '', deleted_at = now() WHERE id = $1`, id,
			); err != nil {
				return err
			}
			return nil
		},
	)
}

// UserVerifyPassword checks credentials and returns the user ID.
func (db *DB) UserVerifyPassword(ctx context.Context, actor Actor, email, password string, pepper []byte) (UserID, error) {
	var id UserID
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error { return checkSystem(actor) },
		nil,
		func(tx pgx.Tx) error {
			var hash string
			if err := tx.QueryRow(ctx,
				`SELECT id, password FROM users WHERE email = $1 AND deleted_at IS NULL`,
				email,
			).Scan(&id, &hash); err != nil {
				return ErrInvalidCreds
			}
			if !verifyHash(password, hash, pepper) {
				return ErrInvalidCreds
			}
			return nil
		},
	)
	return id, err
}


// UserSessionGet resolves a session token into the Actor it authenticates.
// Returns a zero Actor on any error; callers must check err.
func (db *DB) UserSessionGet(ctx context.Context, actor Actor, token string) (Actor, error) {
	var (
		userID    UserID
		email     string
		superuser bool
	)
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error { return checkSystem(actor) },
		nil,
		func(tx pgx.Tx) error {
			var expiresAt time.Time
			h := hashToken(token)

			if err := tx.QueryRow(ctx,
				`SELECT s.user_id, u.email, u.superuser, s.expires_at
				 FROM sessions s JOIN users u ON u.id = s.user_id
				 WHERE s.token = $1 AND s.expires_at > now() AND u.deleted_at IS NULL`,
				h,
			).Scan(&userID, &email, &superuser, &expiresAt); err != nil {
				return err
			}

			if time.Until(expiresAt) < config.SessionTTL/2 {
				if _, err := tx.Exec(ctx,
					`UPDATE sessions SET expires_at = now() + $2 WHERE token = $1`,
					h, config.SessionTTL,
				); err != nil {
					return err
				}
			}
			return nil
		},
	)
	if err != nil {
		return Actor{}, err
	}
	return UserActor(userID, email, superuser), nil
}

// UserSessionCreate generates a random token, stores its hash, and returns the token.
func (db *DB) UserSessionCreate(ctx context.Context, actor Actor, userID UserID) (string, error) {
	var token string
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error { return checkSystem(actor) },
		nil,
		func(tx pgx.Tx) error {
			buf := make([]byte, 32)
			if _, err := rand.Read(buf); err != nil {
				return err
			}
			token = base64.RawURLEncoding.EncodeToString(buf)

			if _, err := tx.Exec(ctx,
				`INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, now() + $3)`,
				hashToken(token), userID, config.SessionTTL,
			); err != nil {
				return err
			}
			return nil
		},
	)
	return token, err
}

// UserSessionDelete removes a session (logout).
func (db *DB) UserSessionDelete(ctx context.Context, actor Actor, token string) error {
	return db.apicall(ctx, actor,
		func(tx pgx.Tx) error { return checkSystem(actor) },
		nil,
		func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, hashToken(token))
			return err
		},
	)
}

// UserSessionPurgeExpired deletes all expired sessions. Runs as SystemActor.
func (db *DB) UserSessionPurgeExpired(ctx context.Context) error {
	return db.apicall(ctx, SystemActor(),
		func(tx pgx.Tx) error { return checkSystem(SystemActor()) },
		nil,
		func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
			return err
		},
	)
}

// OrgGet returns an org by ref (ID or slug).
func (db *DB) OrgGet(ctx context.Context, actor Actor, ref OrgRef) (*Organization, error) {
	var o Organization
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error { return checkGlobal(actor, apipb.Perm_PERM_ORG_READ) },
		nil,
		func(tx pgx.Tx) error {
			col, val := ref.where()
			q := sb.PostgreSQL.NewSelectBuilder()

			sql, args := q.Select("id", "name", "slug", "public", "created_at").
				From("organizations").
				Where(q.Equal(col, val), q.IsNull("deleted_at")).
				Build()

			if err := tx.QueryRow(ctx, sql, args...).Scan(&o.ID, &o.Name, &o.Slug, &o.Public, &o.CreatedAt); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrOrgNotFound
				}
				return err
			}
			return nil
		},
	)
	return &o, err
}

// OrgCreate creates an org and adds the owner as the first member.
func (db *DB) OrgCreate(ctx context.Context, actor Actor, name, slug string, public bool, owner UserRef) (OrgID, error) {
	var orgID OrgID
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error { return checkGlobal(actor, apipb.Perm_PERM_ORG_WRITE) },
		nil,
		func(tx pgx.Tx) error {
			userID, err := resolveUser(ctx, tx, owner)
			if err != nil {
				return err
			}

			if err := tx.QueryRow(ctx,
				`INSERT INTO organizations (name, slug, public) VALUES ($1, $2, $3) RETURNING id`,
				name, slug, public,
			).Scan(&orgID); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
					return ErrSlugTaken
				}
				return err
			}

			_, err = tx.Exec(ctx,
				`INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`,
				orgID, userID,
			)
			return err
		},
	)
	return orgID, err
}

// OrgUpdate updates an org's name, slug, and/or public flag.
func (db *DB) OrgUpdate(ctx context.Context, actor Actor, ref OrgRef, name *string, slug *string, public *bool) error {
	if name == nil && slug == nil && public == nil {
		return nil
	}

	var id OrgID
	return db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			var err error
			id, err = resolveOrg(ctx, tx, ref)
			if err != nil {
				return err
			}
			return checkPerm(ctx, tx, actor, id, apipb.Perm_PERM_ORG_WRITE)
		},
		nil,
		func(tx pgx.Tx) error {
			ub := sb.PostgreSQL.NewUpdateBuilder()
			ub.Update("organizations")
			if name != nil {
				ub.SetMore(ub.Assign("name", *name))
			}
			if slug != nil {
				ub.SetMore(ub.Assign("slug", *slug))
			}
			if public != nil {
				ub.SetMore(ub.Assign("public", *public))
			}
			ub.Where(ub.Equal("id", id))

			sql, args := ub.Build()
			tag, err := tx.Exec(ctx, sql, args...)
			if err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
					return ErrSlugTaken
				}
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrOrgNotFound
			}
			return nil
		},
	)
}

// OrgDelete soft-deletes an org and removes all members.
func (db *DB) OrgDelete(ctx context.Context, actor Actor, ref OrgRef) error {
	var id OrgID
	return db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			var err error
			id, err = resolveOrg(ctx, tx, ref)
			if err != nil {
				return err
			}
			return checkPerm(ctx, tx, actor, id, apipb.Perm_PERM_ORG_DELETE)
		},
		nil,
		func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM org_members WHERE org_id = $1`, id); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx,
				`UPDATE organizations SET slug = id::text, deleted_at = now() WHERE id = $1`, id,
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrOrgNotFound
			}
			return nil
		},
	)
}

// OrgList returns a page of orgs and the total count.
func (db *DB) OrgList(ctx context.Context, actor Actor, cursor OrgID, limit int, filter string) ([]Organization, int, error) {
	var orgs []Organization
	var total int
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error { return checkGlobal(actor, apipb.Perm_PERM_ORG_READ) },
		func(tx pgx.Tx) error {
			if filter != "" {
				return ErrNotImplemented
			}
			return nil
		},
		func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM organizations WHERE deleted_at IS NULL`,
			).Scan(&total); err != nil {
				return err
			}

			q := sb.PostgreSQL.NewSelectBuilder()
			q.Select("id", "name", "slug", "public", "created_at").
				From("organizations").
				Where(q.IsNull("deleted_at")).
				OrderBy("id").
				Limit(limit)
			if !cursor.IsZero() {
				q.Where(q.GreaterThan("id", cursor))
			}

			sql, args := q.Build()
			rows, err := tx.Query(ctx, sql, args...)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				var o Organization
				if err := rows.Scan(&o.ID, &o.Name, &o.Slug, &o.Public, &o.CreatedAt); err != nil {
					return err
				}
				orgs = append(orgs, o)
			}
			return rows.Err()
		},
	)
	return orgs, total, err
}

// OrgMemberGet returns a single member's info.
func (db *DB) OrgMemberGet(ctx context.Context, actor Actor, org OrgRef, user UserRef) (*OrgMember, error) {
	var m OrgMember
	var orgID OrgID
	var userID UserID
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			var err error
			orgID, err = resolveOrg(ctx, tx, org)
			if err != nil {
				return err
			}
			return checkPerm(ctx, tx, actor, orgID, apipb.Perm_PERM_ORG_MEMBER_READ)
		},
		nil,
		func(tx pgx.Tx) error {
			var err error
			userID, err = resolveUser(ctx, tx, user)
			if err != nil {
				return err
			}

			if err := tx.QueryRow(ctx,
				`SELECT u.id, u.email, u.created_at, om.role, om.created_at
				 FROM org_members om
				 JOIN users u ON u.id = om.user_id
				 WHERE om.org_id = $1 AND om.user_id = $2`, orgID, userID,
			).Scan(&m.User.ID, &m.User.Email, &m.User.CreatedAt, &m.Role, &m.JoinedAt); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotMember
				}
				return err
			}
			return nil
		},
	)
	return &m, err
}

// OrgMembersList returns a page of members for an org.
func (db *DB) OrgMembersList(ctx context.Context, actor Actor, org OrgRef, cursor UserID, limit int, filter string) ([]OrgMember, int, error) {
	var members []OrgMember
	var total int
	var orgID OrgID
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			var err error
			orgID, err = resolveOrg(ctx, tx, org)
			if err != nil {
				return err
			}
			return checkPerm(ctx, tx, actor, orgID, apipb.Perm_PERM_ORG_MEMBER_READ)
		},
		func(tx pgx.Tx) error {
			if filter != "" {
				return ErrNotImplemented
			}
			return nil
		},
		func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM org_members WHERE org_id = $1`, orgID,
			).Scan(&total); err != nil {
				return err
			}

			q := sb.PostgreSQL.NewSelectBuilder()
			q.Select("u.id", "u.email", "u.created_at", "m.role", "m.created_at").
				From("org_members m").
				Join("users u", "u.id = m.user_id").
				Where(q.Equal("m.org_id", orgID), q.IsNull("u.deleted_at")).
				OrderBy("u.id").
				Limit(limit)
			if !cursor.IsZero() {
				q.Where(q.GreaterThan("u.id", cursor))
			}

			sql, args := q.Build()
			rows, err := tx.Query(ctx, sql, args...)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				var m OrgMember
				if err := rows.Scan(&m.User.ID, &m.User.Email, &m.User.CreatedAt, &m.Role, &m.JoinedAt); err != nil {
					return err
				}
				members = append(members, m)
			}
			return rows.Err()
		},
	)
	return members, total, err
}

// OrgMemberAdd adds a user to an org with the given role.
func (db *DB) OrgMemberAdd(ctx context.Context, actor Actor, org OrgRef, user UserRef, role string) error {
	var orgID OrgID
	return db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			var err error
			orgID, err = resolveOrg(ctx, tx, org)
			if err != nil {
				return err
			}
			return checkPerm(ctx, tx, actor, orgID, apipb.Perm_PERM_ORG_MEMBER_WRITE)
		},
		nil,
		func(tx pgx.Tx) error {
			userID, err := resolveUser(ctx, tx, user)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, $3)`,
				orgID, userID, role,
			); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
					return ErrAlreadyMember
				}
				return err
			}
			return nil
		},
	)
}

// OrgMemberUpdateRole changes a member's role. Fails if demoting the last owner.
func (db *DB) OrgMemberUpdateRole(ctx context.Context, actor Actor, org OrgRef, user UserRef, newRole string) error {
	var orgID OrgID
	var userID UserID
	return db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			var err error
			orgID, err = resolveOrg(ctx, tx, org)
			if err != nil {
				return err
			}
			return checkPerm(ctx, tx, actor, orgID, apipb.Perm_PERM_ORG_MEMBER_WRITE)
		},
		nil,
		func(tx pgx.Tx) error {
			var err error
			userID, err = resolveUser(ctx, tx, user)
			if err != nil {
				return err
			}

			var currentRole string
			if err := tx.QueryRow(ctx,
				`SELECT role FROM org_members WHERE org_id = $1 AND user_id = $2 FOR UPDATE`,
				orgID, userID,
			).Scan(&currentRole); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotMember
				}
				return err
			}

			if currentRole == "owner" && newRole != "owner" {
				var ownerCount int
				if err := tx.QueryRow(ctx,
					`SELECT count(*) FROM org_members WHERE org_id = $1 AND role = 'owner'`,
					orgID,
				).Scan(&ownerCount); err != nil {
					return err
				}
				if ownerCount <= 1 {
					return ErrLastOwner
				}
			}

			_, err = tx.Exec(ctx,
				`UPDATE org_members SET role = $1 WHERE org_id = $2 AND user_id = $3`,
				newRole, orgID, userID,
			)
			return err
		},
	)
}

// OrgMemberRemove removes a user from an org. Fails if they are the last owner.
func (db *DB) OrgMemberRemove(ctx context.Context, actor Actor, org OrgRef, user UserRef) error {
	var orgID OrgID
	return db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			var err error
			orgID, err = resolveOrg(ctx, tx, org)
			if err != nil {
				return err
			}
			return checkPerm(ctx, tx, actor, orgID, apipb.Perm_PERM_ORG_MEMBER_WRITE)
		},
		nil,
		func(tx pgx.Tx) error {
			userID, err := resolveUser(ctx, tx, user)
			if err != nil {
				return err
			}

			var role string
			if err := tx.QueryRow(ctx,
				`SELECT role FROM org_members WHERE org_id = $1 AND user_id = $2 FOR UPDATE`,
				orgID, userID,
			).Scan(&role); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotMember
				}
				return err
			}

			if role == "owner" {
				var ownerCount int
				if err := tx.QueryRow(ctx,
					`SELECT count(*) FROM org_members WHERE org_id = $1 AND role = 'owner'`,
					orgID,
				).Scan(&ownerCount); err != nil {
					return err
				}
				if ownerCount <= 1 {
					return ErrLastOwner
				}
			}

			_, err = tx.Exec(ctx,
				`DELETE FROM org_members WHERE org_id = $1 AND user_id = $2`,
				orgID, userID,
			)
			return err
		},
	)
}

// WorkerGet returns a worker by ID.
func (db *DB) WorkerGet(ctx context.Context, actor Actor, id WorkerID) (*Worker, error) {
	var w Worker
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			var orgID *OrgID
			if err := tx.QueryRow(ctx,
				`SELECT org_id FROM workers WHERE id = $1 AND revoked_at IS NULL`, id,
			).Scan(&orgID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrWorkerNotFound
				}
				return err
			}
			if orgID != nil {
				return checkPerm(ctx, tx, actor, *orgID, apipb.Perm_PERM_WORKER_READ)
			}
			if !actor.IsSuperuser() {
				return ErrPermissionDenied
			}
			return nil
		},
		nil,
		func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT id, public_key, org_id, created_at FROM workers WHERE id = $1 AND revoked_at IS NULL`, id,
			).Scan(&w.ID, &w.PublicKey, &w.OrgID, &w.CreatedAt)
		},
	)
	return &w, err
}

// WorkerCreate registers a new worker with the given public key and optional org.
func (db *DB) WorkerCreate(ctx context.Context, actor Actor, publicKey []byte, org *OrgRef) (WorkerID, error) {
	var workerID WorkerID
	var orgID *OrgID
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			if org != nil {
				id, err := resolveOrg(ctx, tx, *org)
				if err != nil {
					return err
				}
				orgID = &id
				return checkPerm(ctx, tx, actor, id, apipb.Perm_PERM_WORKER_WRITE)
			}
			if !actor.IsSuperuser() {
				return ErrPermissionDenied
			}
			return nil
		},
		nil,
		func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO workers (public_key, org_id) VALUES ($1, $2) RETURNING id`,
				publicKey, orgID,
			).Scan(&workerID)
		},
	)
	return workerID, err
}

// WorkerDelete soft-deletes a worker by ID.
func (db *DB) WorkerDelete(ctx context.Context, actor Actor, id WorkerID) error {
	return db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			var orgID *OrgID
			if err := tx.QueryRow(ctx,
				`SELECT org_id FROM workers WHERE id = $1 AND revoked_at IS NULL`, id,
			).Scan(&orgID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrWorkerNotFound
				}
				return err
			}
			if orgID != nil {
				return checkPerm(ctx, tx, actor, *orgID, apipb.Perm_PERM_WORKER_WRITE)
			}
			if !actor.IsSuperuser() {
				return ErrPermissionDenied
			}
			return nil
		},
		nil,
		func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx,
				`UPDATE workers SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id,
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrWorkerNotFound
			}
			return nil
		},
	)
}

// WorkerList returns a page of workers and the total count.
func (db *DB) WorkerList(ctx context.Context, actor Actor, cursor WorkerID, limit int, filter string) ([]Worker, int, error) {
	var workers []Worker
	var total int
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error {
			if actor.kind == actorAnon {
				return ErrUnauthenticated
			}
			return nil
		},
		func(tx pgx.Tx) error {
			if filter != "" {
				return ErrNotImplemented
			}
			return nil
		},
		func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM workers WHERE revoked_at IS NULL`,
			).Scan(&total); err != nil {
				return err
			}

			q := sb.PostgreSQL.NewSelectBuilder()
			q.Select("id", "public_key", "org_id", "created_at").
				From("workers").
				Where(q.IsNull("revoked_at")).
				OrderBy("id").
				Limit(limit)
			if !cursor.IsZero() {
				q.Where(q.GreaterThan("id", cursor))
			}

			sql, args := q.Build()
			rows, err := tx.Query(ctx, sql, args...)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				var w Worker
				if err := rows.Scan(&w.ID, &w.PublicKey, &w.OrgID, &w.CreatedAt); err != nil {
					return err
				}
				workers = append(workers, w)
			}
			return rows.Err()
		},
	)
	return workers, total, err
}

// WorkerLookup finds an active worker by its ed25519 public key.
func (db *DB) WorkerLookup(ctx context.Context, actor Actor, publicKey []byte) (*Worker, error) {
	var w Worker
	err := db.apicall(ctx, actor,
		func(tx pgx.Tx) error { return checkSystem(actor) },
		nil,
		func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx,
				`SELECT id, public_key, org_id, created_at FROM workers WHERE public_key = $1 AND revoked_at IS NULL`,
				publicKey,
			).Scan(&w.ID, &w.PublicKey, &w.OrgID, &w.CreatedAt); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrWorkerNotFound
				}
				return err
			}
			return nil
		},
	)
	return &w, err
}

// hashToken returns the hex-encoded SHA-256 of a session token.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// verifyHash parses a PHC-format argon2id string and compares.
// Format: $argon2id$v=19$m=65536,t=3,p=2$<salt>$<key>
func verifyHash(password, encoded string, pepper []byte) bool {
	// $argon2id$v=19$m=65536,t=3,p=2$salt$key → 6 parts
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}

	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expectedKey, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte(password))
	peppered := mac.Sum(nil)

	key := argon2.IDKey(peppered, salt, time, memory, threads, uint32(len(expectedKey)))

	return subtle.ConstantTimeCompare(key, expectedKey) == 1
}

// hashPassword produces a PHC-format string:
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
func hashPassword(password string, pepper []byte) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	// Apply pepper: HMAC-SHA256(pepper, password)
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte(password))
	peppered := mac.Sum(nil)

	key := argon2.IDKey(peppered, salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// checkNotSoleOwner returns ErrSoleOwner if the user is the only owner of any org.
func checkNotSoleOwner(ctx context.Context, tx pgx.Tx, userID UserID) error {
	var slug string
	err := tx.QueryRow(ctx,
		`SELECT o.slug FROM org_members m
		 JOIN organizations o ON o.id = m.org_id
		 WHERE m.role = 'owner' AND o.deleted_at IS NULL
		 GROUP BY o.id, o.slug
		 HAVING count(*) = 1 AND bool_or(m.user_id = $1)
		 LIMIT 1`, userID,
	).Scan(&slug)
	if err == nil {
		return ErrSoleOwner
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// resolveUser locks and returns the user ID within a transaction.
func resolveUser(ctx context.Context, tx pgx.Tx, ref UserRef) (UserID, error) {
	col, val := ref.where()
	q := sb.PostgreSQL.NewSelectBuilder()

	sql, args := q.Select("id").From("users").
		Where(q.Equal(col, val), q.IsNull("deleted_at")).
		ForUpdate().
		Build()

	var id UserID
	if err := tx.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		var zero UserID
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, ErrUserNotFound
		}

		return zero, err
	}

	return id, nil
}

// resolveOrg locks and returns the org ID within a transaction.
func resolveOrg(ctx context.Context, tx pgx.Tx, ref OrgRef) (OrgID, error) {
	col, val := ref.where()
	q := sb.PostgreSQL.NewSelectBuilder()

	sql, args := q.Select("id").From("organizations").
		Where(q.Equal(col, val), q.IsNull("deleted_at")).
		ForUpdate().
		Build()

	var id OrgID
	if err := tx.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		var zero OrgID
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, ErrOrgNotFound
		}

		return zero, err
	}

	return id, nil
}

// ValidateEmail checks that the value is a valid email address.
func ValidateEmail(value string) error {
	if _, err := mail.ParseAddress(value); err != nil {
		return ErrInvalidEmail
	}
	return nil
}

// ValidateSlug checks format and returns the normalized (lowercased) slug.
func ValidateSlug(value string) (string, error) {
	if len(value) < 2 || len(value) > 64 || !slugRe.MatchString(value) {
		return "", ErrInvalidSlug
	}
	return strings.ToLower(value), nil
}

// ValidateRole checks that the value is a valid role string.
// Derives valid roles from rolePermissions — single source of truth.
func ValidateRole(value string) error {
	if _, ok := rolePermissions[value]; !ok {
		return ErrInvalidRole
	}
	return nil
}
