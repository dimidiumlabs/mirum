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
	"golang.org/x/crypto/argon2"
)

var (
	ErrCreateUser   = errors.New("database: failed to create user")
	ErrSetPassword  = errors.New("database: failed to set password")
	ErrDeleteUser   = errors.New("database: failed to delete user")
	ErrInvalidCreds = errors.New("invalid credentials")
	ErrSoleOwner    = errors.New("database: user is the sole owner of an organization")
	ErrUserNotFound = errors.New("database: user not found")
)

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
	ID        string
	Email     string
	CreatedAt time.Time
}

// Session holds info about an authenticated session.
type Session struct {
	UserID string
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
func (db *DB) UserCreate(ctx context.Context, email, password string, pepper []byte) (string, error) {
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
		return "", errors.Join(ErrCreateUser, err)
	}

	return id, nil
}

// UserDelete clears all fields but keeps the row to preserve the id.
// Also deletes all sessions for that user in a single transaction.
func (db *DB) UserDelete(ctx context.Context, email string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return errors.Join(ErrDeleteUser, err)
	}
	defer tx.Rollback(ctx)

	var id string
	err = tx.QueryRow(ctx,
		`UPDATE users SET email = id::text, password = '', deleted_at = now() WHERE email = $1 RETURNING id`,
		email,
	).Scan(&id)
	if err != nil {
		return errors.Join(ErrDeleteUser, err)
	}

	var soloOwnedSlug string
	err = tx.QueryRow(ctx,
		`SELECT o.slug FROM org_members m
		 JOIN organizations o ON o.id = m.org_id
		 WHERE m.role = 'owner' AND o.deleted_at IS NULL
		 GROUP BY o.id, o.slug
		 HAVING count(*) = 1 AND bool_or(m.user_id = $1)
		 LIMIT 1`, id,
	).Scan(&soloOwnedSlug)
	if err == nil {
		return ErrSoleOwner
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return errors.Join(ErrDeleteUser, err)
	}

	if _, err = tx.Exec(ctx, `DELETE FROM org_members WHERE user_id = $1`, id); err != nil {
		return errors.Join(ErrDeleteUser, err)
	}

	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
		return errors.Join(ErrDeleteUser, err)
	}

	return tx.Commit(ctx)
}

// UserSetPassword updates the password and invalidates all existing sessions.
func (db *DB) UserSetPassword(ctx context.Context, email, password string, pepper []byte) error {
	hash, err := hashPassword(password, pepper)
	if err != nil {
		return err
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return errors.Join(ErrSetPassword, err)
	}
	defer tx.Rollback(ctx)

	var id string
	err = tx.QueryRow(ctx,
		`UPDATE users SET password = $1 WHERE email = $2 AND deleted_at IS NULL RETURNING id`,
		hash, email,
	).Scan(&id)
	if err != nil {
		return errors.Join(ErrSetPassword, err)
	}

	_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id)
	if err != nil {
		return errors.Join(ErrSetPassword, err)
	}

	return tx.Commit(ctx)
}

// UserVerifyPassword checks credentials and returns the user ID.
func (db *DB) UserVerifyPassword(ctx context.Context, email, password string, pepper []byte) (string, error) {
	var id, hash string
	err := db.Pool.QueryRow(ctx,
		`SELECT id, password FROM users WHERE email = $1 AND deleted_at IS NULL`,
		email,
	).Scan(&id, &hash)
	if err != nil {
		return "", ErrInvalidCreds
	}

	if !verifyHash(password, hash, pepper) {
		return "", ErrInvalidCreds
	}

	return id, nil
}

// UserGetSession returns session info for a valid, non-expired session.
// It extends the session expiry only when less than half the TTL remains,
// avoiding a write on every request.
func (db *DB) UserGetSession(ctx context.Context, token string) (*Session, error) {
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

// UserCreateSession generates a random token, stores its hash, and returns the token.
func (db *DB) UserCreateSession(ctx context.Context, userID string) (string, error) {
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
