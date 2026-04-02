// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/tern/v2/migrate"
	"golang.org/x/crypto/argon2"
)

const (
	argonMemory  = 64 * 1024 // 64 MB
	argonTime    = 3
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

var (
	errOpen           = errors.New("database: failed to open")
	errPing           = errors.New("database: failed to ping")
	errAcquire        = errors.New("database: failed to acquire connection")
	errMigrate        = errors.New("database: failed to create migrator")
	errCreateUser     = errors.New("database: failed to create user")
	errSetPassword    = errors.New("database: failed to set password")
	errDeleteUser     = errors.New("database: failed to delete user")
	errInvalidCreds   = errors.New("invalid credentials")
	errAddWorker      = errors.New("database: failed to add worker")
	errRevokeWorker   = errors.New("database: failed to revoke worker")
	ErrWorkerNotFound = errors.New("database: worker not found")
)

// DB wraps a pgx connection pool.
type DB struct {
	Pool *pgxpool.Pool
}

// Open connects to PostgreSQL and returns a DB.
func Open(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, errors.Join(errOpen, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.Join(errPing, err)
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
		return errors.Join(errAcquire, err)
	}
	defer conn.Release()

	migrator, err := migrate.NewMigrator(ctx, conn.Conn(), "schema_version")
	if err != nil {
		return errors.Join(errMigrate, err)
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

	return migrator.Migrate(ctx)
}

const SessionTTL = 14 * 24 * time.Hour // 14 days

// hashToken returns the hex-encoded SHA-256 of a session token.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", h)
}

// CreateSession generates a random token, stores its hash, and returns the token.
func (db *DB) CreateSession(ctx context.Context, userID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)

	_, err := db.Pool.Exec(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, now() + $3)`,
		hashToken(token), userID, SessionTTL,
	)
	if err != nil {
		return "", err
	}
	return token, nil
}

// Session holds info about an authenticated session.
type Session struct {
	UserID string
	Email  string
}

// GetSession returns session info for a valid, non-expired session.
// It extends the session expiry only when less than half the TTL remains,
// avoiding a write on every request.
func (db *DB) GetSession(ctx context.Context, token string) (*Session, error) {
	h := hashToken(token)
	var s Session
	var expiresAt time.Time
	err := db.Pool.QueryRow(ctx,
		`SELECT s.user_id, u.email, s.expires_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token = $1 AND s.expires_at > now() AND u.deleted_at IS NULL`,
		h,
	).Scan(&s.UserID, &s.Email, &expiresAt)
	if err != nil {
		return nil, err
	}

	if time.Until(expiresAt) < SessionTTL/2 {
		db.Pool.Exec(ctx,
			`UPDATE sessions SET expires_at = now() + $2 WHERE token = $1`,
			h, SessionTTL,
		)
	}

	return &s, nil
}

// DeleteSession removes a session (logout).
func (db *DB) DeleteSession(ctx context.Context, token string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, hashToken(token))
	return err
}

// PurgeExpiredSessions deletes all expired sessions.
func (db *DB) PurgeExpiredSessions(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return err
}

// CreateUser hashes the password with argon2id and inserts a new user.
// The pepper is a server-side secret not stored in the database.
func (db *DB) CreateUser(ctx context.Context, email, password string, pepper []byte) (string, error) {
	hash, err := hashPassword(password, pepper)
	if err != nil {
		return "", err
	}

	var id string
	err = db.Pool.QueryRow(ctx,
		`INSERT INTO users (email, password) VALUES ($1, $2) RETURNING id`,
		email, hash,
	).Scan(&id)
	if err != nil {
		return "", errors.Join(errCreateUser, err)
	}

	return id, nil
}

// SetPassword updates the password and invalidates all existing sessions.
func (db *DB) SetPassword(ctx context.Context, email, password string, pepper []byte) error {
	hash, err := hashPassword(password, pepper)
	if err != nil {
		return err
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return errors.Join(errSetPassword, err)
	}
	defer tx.Rollback(ctx)

	var id string
	err = tx.QueryRow(ctx,
		`UPDATE users SET password = $1 WHERE email = $2 AND deleted_at IS NULL RETURNING id`,
		hash, email,
	).Scan(&id)
	if err != nil {
		return errors.Join(errSetPassword, err)
	}

	_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id)
	if err != nil {
		return errors.Join(errSetPassword, err)
	}

	return tx.Commit(ctx)
}

// DeleteUser clears all fields but keeps the row to preserve the id.
// Also deletes all sessions for that user in a single transaction.
func (db *DB) DeleteUser(ctx context.Context, email string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return errors.Join(errDeleteUser, err)
	}
	defer tx.Rollback(ctx)

	var id string
	err = tx.QueryRow(ctx,
		`UPDATE users SET email = id::text, password = '', deleted_at = now() WHERE email = $1 RETURNING id`,
		email,
	).Scan(&id)
	if err != nil {
		return errors.Join(errDeleteUser, err)
	}

	_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id)
	if err != nil {
		return errors.Join(errDeleteUser, err)
	}

	return tx.Commit(ctx)
}

// VerifyPassword checks credentials and returns the user ID.
func (db *DB) VerifyPassword(ctx context.Context, email, password string, pepper []byte) (string, error) {
	var id, hash string
	err := db.Pool.QueryRow(ctx,
		`SELECT id, password FROM users WHERE email = $1 AND deleted_at IS NULL`,
		email,
	).Scan(&id, &hash)
	if err != nil {
		return "", errInvalidCreds
	}

	if !verifyHash(password, hash, pepper) {
		return "", errInvalidCreds
	}

	return id, nil
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

// Worker holds info about a registered worker.
type Worker struct {
	ID        string
	PublicKey []byte
	CreatedAt time.Time
}

// LookupWorker finds an active worker by its ed25519 public key.
func (db *DB) LookupWorker(ctx context.Context, publicKey []byte) (*Worker, error) {
	var w Worker
	err := db.Pool.QueryRow(ctx,
		`SELECT id, public_key, created_at FROM workers WHERE public_key = $1 AND revoked_at IS NULL`,
		publicKey,
	).Scan(&w.ID, &w.PublicKey, &w.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWorkerNotFound
		}
		return nil, err
	}
	return &w, nil
}

// AddWorker registers a new worker with the given public key.
func (db *DB) AddWorker(ctx context.Context, publicKey []byte) (string, error) {
	var id string
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO workers (public_key) VALUES ($1) RETURNING id`,
		publicKey,
	).Scan(&id)
	if err != nil {
		return "", errors.Join(errAddWorker, err)
	}
	return id, nil
}

// RevokeWorker soft-deletes a worker by ID.
func (db *DB) RevokeWorker(ctx context.Context, id string) error {
	tag, err := db.Pool.Exec(ctx,
		`UPDATE workers SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`,
		id,
	)
	if err != nil {
		return errors.Join(errRevokeWorker, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrWorkerNotFound
	}
	return nil
}

// ListWorkers returns all active (non-revoked) workers.
func (db *DB) ListWorkers(ctx context.Context) ([]Worker, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, public_key, created_at FROM workers WHERE revoked_at IS NULL ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []Worker
	for rows.Next() {
		var w Worker
		if err := rows.Scan(&w.ID, &w.PublicKey, &w.CreatedAt); err != nil {
			return nil, err
		}
		workers = append(workers, w)
	}
	return workers, rows.Err()
}
