// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrAddWorker      = errors.New("database: failed to add worker")
	ErrRevokeWorker   = errors.New("database: failed to revoke worker")
	ErrWorkerNotFound = errors.New("database: worker not found")
)

// Worker holds info about a registered worker.
type Worker struct {
	ID        string
	PublicKey []byte
	CreatedAt time.Time
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
		return "", errors.Join(ErrAddWorker, err)
	}
	return id, nil
}

// WorkerRevoke soft-deletes a worker by ID.
func (db *DB) WorkerRevoke(ctx context.Context, id string) error {
	tag, err := db.Pool.Exec(ctx,
		`UPDATE workers SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`,
		id,
	)
	if err != nil {
		return errors.Join(ErrRevokeWorker, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrWorkerNotFound
	}
	return nil
}
