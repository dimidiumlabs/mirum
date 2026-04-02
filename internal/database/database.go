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
	errOpen    = errors.New("database: failed to open")
	errPing    = errors.New("database: failed to ping")
	errAcquire = errors.New("database: failed to acquire connection")
	errMigrate = errors.New("database: failed to create migrator")
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
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`DROP TABLE users`,
	)

	return migrator.Migrate(ctx)
}
