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

	"github.com/google/uuid"
	sb "github.com/huandu/go-sqlbuilder"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/argon2"
)

var (
	ErrSoleOwner    = errors.New("database: sole owner of an organization")
	ErrEmailTaken   = errors.New("database: email already taken")
	ErrInvalidCreds = errors.New("database: invalid credentials")
	ErrUserNotFound = errors.New("database: user not found")
)

// UserRef identifies a user by ID or email.
type UserRef struct {
	id    uuid.UUID
	email string
}

func UserByID(id uuid.UUID) UserRef    { return UserRef{id: id} }
func UserByEmail(email string) UserRef { return UserRef{email: email} }

func (r UserRef) where() (string, any) {
	if r.id != uuid.Nil {
		return "id", r.id
	}
	return "email", r.email
}

const (
	saltLen = 16

	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MB
	argonKeyLen  = 32
	argonThreads = 2

	SessionTTL = 14 * 24 * time.Hour // 14 days
)

// User holds info about a user.
type User struct {
	ID        uuid.UUID
	Email     string
	CreatedAt time.Time
}

// Session holds info about an authenticated session.
type Session struct {
	UserID uuid.UUID
	Email  string
}

// hashToken returns the hex-encoded SHA-256 of a session token.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", h)
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

// UserCreate hashes the password with argon2id and inserts a new user.
// The pepper is a server-side secret not stored in the database.
func (db *DB) UserCreate(ctx context.Context, email, password string, pepper []byte) (uuid.UUID, error) {
	hash, err := hashPassword(password, pepper)
	if err != nil {
		return uuid.Nil, err
	}

	var id uuid.UUID
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO users (email, password) VALUES ($1, $2) RETURNING id`,
		email, hash,
	).Scan(&id); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return uuid.Nil, ErrEmailTaken
		}
		return uuid.Nil, err
	}

	return id, nil
}

// GetUser returns a user by ref (ID or email).
func (db *DB) GetUser(ctx context.Context, ref UserRef) (*User, error) {
	col, val := ref.where()

	q := sb.PostgreSQL.NewSelectBuilder()
	sql, args := q.Select("id", "email", "created_at").
		From("users").
		Where(q.Equal(col, val), q.IsNull("deleted_at")).
		Build()

	var u User
	if err := db.Pool.QueryRow(ctx, sql, args...).Scan(&u.ID, &u.Email, &u.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	return &u, nil
}

// ListUsers returns a page of users and the total count.
func (db *DB) ListUsers(ctx context.Context, cursor uuid.UUID, limit int, filter string) ([]User, int, error) {
	if filter != "" {
		return nil, 0, ErrFilterNotImplemented
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)

	var total int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE deleted_at IS NULL`,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := sb.PostgreSQL.NewSelectBuilder()
	q.Select("id", "email", "created_at").
		From("users").
		Where(q.IsNull("deleted_at")).
		OrderBy("id").
		Limit(limit)
	if cursor != uuid.Nil {
		q.Where(q.GreaterThan("id", cursor))
	}

	sql, args := q.Build()
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.CreatedAt); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}

	return users, total, rows.Err()
}

// checkNotSoleOwner returns ErrSoleOwner if the user is the only owner of any org.
func checkNotSoleOwner(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
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
func resolveUser(ctx context.Context, tx pgx.Tx, ref UserRef) (uuid.UUID, error) {
	col, val := ref.where()
	q := sb.PostgreSQL.NewSelectBuilder()

	sql, args := q.Select("id").From("users").
		Where(q.Equal(col, val), q.IsNull("deleted_at")).
		ForUpdate().
		Build()

	var id uuid.UUID
	if err := tx.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrUserNotFound
		}

		return uuid.Nil, err
	}

	return id, nil
}

// UserUpdate updates a user's email and/or password.
// Invalidates all sessions when password changes.
func (db *DB) UserUpdate(ctx context.Context, ref UserRef, email *string, password *string, pepper []byte) error {
	if email == nil && password == nil {
		return nil
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	id, err := resolveUser(ctx, tx, ref)
	if err != nil {
		return err
	}

	ub := sb.PostgreSQL.NewUpdateBuilder()
	ub.Update("users")

	if email != nil {
		ub.SetMore(ub.Assign("email", *email))
	}

	if password != nil {
		hash, err := hashPassword(*password, pepper)
		if err != nil {
			return err
		}

		ub.SetMore(ub.Assign("password", hash))
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

	if password != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// UserDelete soft-deletes a user.
// Fails if the user is the sole owner of any organization.
func (db *DB) UserDelete(ctx context.Context, ref UserRef) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	id, err := resolveUser(ctx, tx, ref)
	if err != nil {
		return err
	}

	if err := checkNotSoleOwner(ctx, tx, id); err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `DELETE FROM org_members WHERE user_id = $1`, id); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE users SET email = id::text, password = '', deleted_at = now() WHERE id = $1`, id,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// UserVerifyPassword checks credentials and returns the user ID.
func (db *DB) UserVerifyPassword(ctx context.Context, email, password string, pepper []byte) (uuid.UUID, error) {
	var id uuid.UUID
	var hash string

	if err := db.Pool.QueryRow(ctx,
		`SELECT id, password FROM users WHERE email = $1 AND deleted_at IS NULL`,
		email,
	).Scan(&id, &hash); err != nil {
		return uuid.Nil, ErrInvalidCreds
	}

	if !verifyHash(password, hash, pepper) {
		return uuid.Nil, ErrInvalidCreds
	}

	return id, nil
}

// UserGetSession returns session info for a valid, non-expired session.
func (db *DB) UserGetSession(ctx context.Context, token string) (*Session, error) {
	var s Session
	h := hashToken(token)
	var expiresAt time.Time

	if err := db.Pool.QueryRow(ctx,
		`SELECT s.user_id, u.email, s.expires_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token = $1 AND s.expires_at > now() AND u.deleted_at IS NULL`,
		h,
	).Scan(&s.UserID, &s.Email, &expiresAt); err != nil {
		return nil, err
	}

	if time.Until(expiresAt) < SessionTTL/2 {
		if _, err := db.Pool.Exec(ctx,
			`UPDATE sessions SET expires_at = now() + $2 WHERE token = $1`,
			h, SessionTTL,
		); err != nil {
			return nil, err
		}
	}

	return &s, nil
}

// UserCreateSession generates a random token, stores its hash, and returns the token.
func (db *DB) UserCreateSession(ctx context.Context, userID uuid.UUID) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)

	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, now() + $3)`,
		hashToken(token), userID, SessionTTL,
	); err != nil {
		return "", err
	}

	return token, nil
}

// UserDeleteSession removes a session (logout).
func (db *DB) UserDeleteSession(ctx context.Context, token string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, hashToken(token))
	return err
}

// PurgeExpiredSessions deletes all expired sessions.
func (db *DB) PurgeExpiredSessions(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return err
}
