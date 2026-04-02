// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

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
	errOpen        = errors.New("database: failed to open")
	errPing        = errors.New("database: failed to ping")
	errAcquire     = errors.New("database: failed to acquire connection")
	errMigrate     = errors.New("database: failed to create migrator")
	errCreateUser  = errors.New("database: failed to create user")
	errSetPassword = errors.New("database: failed to set password")
	errDeleteUser  = errors.New("database: failed to delete user")
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

	return migrator.Migrate(ctx)
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

// SetPassword updates the password for a user identified by email.
func (db *DB) SetPassword(ctx context.Context, email, password string, pepper []byte) error {
	hash, err := hashPassword(password, pepper)
	if err != nil {
		return err
	}

	tag, err := db.Pool.Exec(ctx,
		`UPDATE users SET password = $1 WHERE email = $2`,
		hash, email,
	)
	if err != nil {
		return errors.Join(errSetPassword, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user not found: %s", email)
	}

	return nil
}

// DeleteUser clears all fields but keeps the row to preserve the id.
func (db *DB) DeleteUser(ctx context.Context, email string) error {
	tag, err := db.Pool.Exec(ctx,
		`UPDATE users SET email = '<invalid>', password = '<invalid>', deleted_at = now() WHERE email = $1`,
		email,
	)
	if err != nil {
		return errors.Join(errDeleteUser, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user not found: %s", email)
	}
	return nil
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
