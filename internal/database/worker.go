// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	sb "github.com/huandu/go-sqlbuilder"
	"github.com/jackc/pgx/v5"
)

var (
	ErrWorkerNotFound = errors.New("database: worker not found")
)

// Worker holds info about a registered worker.
type Worker struct {
	ID        uuid.UUID
	OrgID     *uuid.UUID
	PublicKey []byte
	CreatedAt time.Time
}

// GetWorker returns a worker by ID.
func (db *DB) GetWorker(ctx context.Context, actor uuid.UUID, id uuid.UUID) (*Worker, error) {
	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var w Worker
	if err := tx.QueryRow(ctx,
		`SELECT id, public_key, org_id, created_at FROM workers WHERE id = $1 AND revoked_at IS NULL`,
		id,
	).Scan(&w.ID, &w.PublicKey, &w.OrgID, &w.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWorkerNotFound
		}
		return nil, err
	}

	return &w, nil
}

// CreateWorker registers a new worker with the given public key and optional org.
func (db *DB) CreateWorker(ctx context.Context, actor uuid.UUID, publicKey []byte, org *OrgRef) (uuid.UUID, error) {
	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)

	var orgID *uuid.UUID
	if org != nil {
		id, err := resolveOrg(ctx, tx, *org)
		if err != nil {
			return uuid.Nil, err
		}
		orgID = &id
	}

	var workerID uuid.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO workers (public_key, org_id) VALUES ($1, $2) RETURNING id`,
		publicKey, orgID,
	).Scan(&workerID); err != nil {
		return uuid.Nil, err
	}

	return workerID, tx.Commit(ctx)
}

// DeleteWorker soft-deletes a worker by ID.
func (db *DB) DeleteWorker(ctx context.Context, actor uuid.UUID, id uuid.UUID) error {
	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`UPDATE workers SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWorkerNotFound
	}

	return tx.Commit(ctx)
}

// ListWorkers returns a page of workers and the total count.
func (db *DB) ListWorkers(ctx context.Context, actor uuid.UUID, cursor uuid.UUID, limit int, filter string) ([]Worker, int, error) {
	if filter != "" {
		return nil, 0, ErrFilterNotImplemented
	}

	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)

	var total int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM workers WHERE revoked_at IS NULL`,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := sb.PostgreSQL.NewSelectBuilder()
	q.Select("id", "public_key", "org_id", "created_at").
		From("workers").
		Where(q.IsNull("revoked_at")).
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

	var workers []Worker
	for rows.Next() {
		var w Worker
		if err := rows.Scan(&w.ID, &w.PublicKey, &w.OrgID, &w.CreatedAt); err != nil {
			return nil, 0, err
		}
		workers = append(workers, w)
	}
	return workers, total, rows.Err()
}

// LookupWorker finds an active worker by its ed25519 public key.
func (db *DB) LookupWorker(ctx context.Context, actor uuid.UUID, publicKey []byte) (*Worker, error) {
	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var w Worker
	if err := tx.QueryRow(ctx,
		`SELECT id, public_key, org_id, created_at FROM workers WHERE public_key = $1 AND revoked_at IS NULL`,
		publicKey,
	).Scan(&w.ID, &w.PublicKey, &w.OrgID, &w.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWorkerNotFound
		}
		return nil, err
	}
	return &w, nil
}
