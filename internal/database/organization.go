// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	sb "github.com/huandu/go-sqlbuilder"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrOrgNotFound   = errors.New("database: organization not found")
	ErrSlugTaken     = errors.New("database: slug already taken")
	ErrAlreadyMember = errors.New("database: already a member")
	ErrNotMember     = errors.New("database: not a member")
	ErrLastOwner     = errors.New("database: last owner")
)

// OrgRef identifies an organization by ID or slug.
type OrgRef struct {
	id   uuid.UUID
	slug string
}

func OrgByID(id uuid.UUID) OrgRef  { return OrgRef{id: id} }
func OrgBySlug(slug string) OrgRef { return OrgRef{slug: slug} }

func (r OrgRef) where() (string, any) {
	if r.id != uuid.Nil {
		return "id", r.id
	}
	return "slug", r.slug
}

// Organization holds info about an organization.
type Organization struct {
	ID        uuid.UUID
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

// resolveOrg locks and returns the org ID within a transaction.
func resolveOrg(ctx context.Context, tx pgx.Tx, ref OrgRef) (uuid.UUID, error) {
	col, val := ref.where()
	q := sb.PostgreSQL.NewSelectBuilder()

	sql, args := q.Select("id").From("organizations").
		Where(q.Equal(col, val), q.IsNull("deleted_at")).
		ForUpdate().
		Build()

	var id uuid.UUID
	if err := tx.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrOrgNotFound
		}

		return uuid.Nil, err
	}

	return id, nil
}

// GetOrg returns an org by ref (ID or slug).
func (db *DB) GetOrg(ctx context.Context, actor Actor, ref OrgRef) (*Organization, error) {
	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	col, val := ref.where()
	q := sb.PostgreSQL.NewSelectBuilder()

	sql, args := q.Select("id", "name", "slug", "public", "created_at").
		From("organizations").
		Where(q.Equal(col, val), q.IsNull("deleted_at")).
		Build()

	var o Organization
	if err := tx.QueryRow(ctx, sql, args...).Scan(&o.ID, &o.Name, &o.Slug, &o.Public, &o.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOrgNotFound
		}
		return nil, err
	}

	return &o, nil
}

// CreateOrganization creates an org and adds the owner as the first member.
func (db *DB) CreateOrganization(ctx context.Context, actor Actor, name, slug string, public bool, owner UserRef) (uuid.UUID, error) {
	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)

	userID, err := resolveUser(ctx, tx, owner)
	if err != nil {
		return uuid.Nil, err
	}

	var orgID uuid.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO organizations (name, slug, public) VALUES ($1, $2, $3) RETURNING id`,
		name, slug, public,
	).Scan(&orgID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return uuid.Nil, ErrSlugTaken
		}

		return uuid.Nil, err
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`,
		orgID, userID,
	); err != nil {
		return uuid.Nil, err
	}

	return orgID, tx.Commit(ctx)
}

// UpdateOrganization updates an org's name, slug, and/or public flag.
func (db *DB) UpdateOrganization(ctx context.Context, actor Actor, ref OrgRef, name *string, slug *string, public *bool) error {
	if name == nil && slug == nil && public == nil {
		return nil
	}

	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	id, err := resolveOrg(ctx, tx, ref)
	if err != nil {
		return err
	}

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
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return ErrSlugTaken
		}

		return err
	}

	return tx.Commit(ctx)
}

// DeleteOrganization soft-deletes an org and removes all members.
func (db *DB) DeleteOrganization(ctx context.Context, actor Actor, ref OrgRef) error {
	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	id, err := resolveOrg(ctx, tx, ref)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM org_members WHERE org_id = $1`, id); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE organizations SET slug = id::text, deleted_at = now() WHERE id = $1`, id,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ListOrganizations returns a page of orgs and the total count.
func (db *DB) ListOrganizations(ctx context.Context, actor Actor, cursor uuid.UUID, limit int, filter string) ([]Organization, int, error) {
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
		`SELECT count(*) FROM organizations WHERE deleted_at IS NULL`,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := sb.PostgreSQL.NewSelectBuilder()
	q.Select("id", "name", "slug", "public", "created_at").
		From("organizations").
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

	var orgs []Organization
	for rows.Next() {
		var o Organization
		if err := rows.Scan(&o.ID, &o.Name, &o.Slug, &o.Public, &o.CreatedAt); err != nil {
			return nil, 0, err
		}
		orgs = append(orgs, o)
	}
	return orgs, total, rows.Err()
}

// GetOrgMember returns a single member's info.
func (db *DB) GetOrgMember(ctx context.Context, actor Actor, org OrgRef, user UserRef) (*OrgMember, error) {
	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	orgID, err := resolveOrg(ctx, tx, org)
	if err != nil {
		return nil, err
	}
	userID, err := resolveUser(ctx, tx, user)
	if err != nil {
		return nil, err
	}

	var m OrgMember
	if err := tx.QueryRow(ctx,
		`SELECT u.id, u.email, u.created_at, om.role, om.created_at
		 FROM org_members om
		 JOIN users u ON u.id = om.user_id
		 WHERE om.org_id = $1 AND om.user_id = $2`, orgID, userID,
	).Scan(&m.User.ID, &m.User.Email, &m.User.CreatedAt, &m.Role, &m.JoinedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotMember
		}
		return nil, err
	}
	return &m, nil
}

// ListOrgMembers returns a page of members for an org.
func (db *DB) ListOrgMembers(ctx context.Context, actor Actor, org OrgRef, cursor uuid.UUID, limit int, filter string) ([]OrgMember, int, error) {
	if filter != "" {
		return nil, 0, ErrFilterNotImplemented
	}

	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)

	orgID, err := resolveOrg(ctx, tx, org)
	if err != nil {
		return nil, 0, err
	}

	var total int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM org_members WHERE org_id = $1`, orgID,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := sb.PostgreSQL.NewSelectBuilder()
	q.Select("u.id", "u.email", "u.created_at", "m.role", "m.created_at").
		From("org_members m").
		Join("users u", "u.id = m.user_id").
		Where(q.Equal("m.org_id", orgID), q.IsNull("u.deleted_at")).
		OrderBy("u.id").
		Limit(limit)
	if cursor != uuid.Nil {
		q.Where(q.GreaterThan("u.id", cursor))
	}

	sql, args := q.Build()
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var members []OrgMember
	for rows.Next() {
		var m OrgMember
		if err := rows.Scan(&m.User.ID, &m.User.Email, &m.User.CreatedAt, &m.Role, &m.JoinedAt); err != nil {
			return nil, 0, err
		}

		members = append(members, m)
	}

	return members, total, rows.Err()
}

// AddOrgMember adds a user to an org with the given role.
func (db *DB) AddOrgMember(ctx context.Context, actor Actor, org OrgRef, user UserRef, role string) error {
	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	orgID, err := resolveOrg(ctx, tx, org)
	if err != nil {
		return err
	}
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

	return tx.Commit(ctx)
}

// UpdateOrgMemberRole changes a member's role. Fails if demoting the last owner.
func (db *DB) UpdateOrgMemberRole(ctx context.Context, actor Actor, org OrgRef, user UserRef, newRole string) error {
	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	orgID, err := resolveOrg(ctx, tx, org)
	if err != nil {
		return err
	}
	userID, err := resolveUser(ctx, tx, user)
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

	if _, err := tx.Exec(ctx,
		`UPDATE org_members SET role = $1 WHERE org_id = $2 AND user_id = $3`,
		newRole, orgID, userID,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// RemoveOrgMember removes a user from an org. Fails if they are the last owner.
func (db *DB) RemoveOrgMember(ctx context.Context, actor Actor, org OrgRef, user UserRef) error {
	tx, err := db.beginAs(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	orgID, err := resolveOrg(ctx, tx, org)
	if err != nil {
		return err
	}
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

	if _, err := tx.Exec(ctx,
		`DELETE FROM org_members WHERE org_id = $1 AND user_id = $2`,
		orgID, userID,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
