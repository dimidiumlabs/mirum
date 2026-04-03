// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrOrgNotFound   = errors.New("database: organization not found")
	ErrAlreadyMember = errors.New("database: user is already a member")
	ErrNotMember     = errors.New("database: user is not a member")
	ErrLastOwner     = errors.New("database: cannot remove or demote the last owner")
	ErrSlugTaken     = errors.New("database: slug already taken")
)

// Organization holds info about an organization.
type Organization struct {
	ID        string
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

// CreateOrganization creates an org and adds the owner as the first member.
func (db *DB) CreateOrganization(ctx context.Context, name, slug string, public bool, ownerEmail string) (string, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var userID string
	if err := tx.QueryRow(ctx,
		`SELECT id FROM users WHERE email = $1 AND deleted_at IS NULL`, ownerEmail,
	).Scan(&userID); err != nil {
		return "", ErrUserNotFound
	}

	var orgID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO organizations (name, slug, public) VALUES ($1, $2, $3) RETURNING id`, name, slug, public,
	).Scan(&orgID); err != nil {
		return "", err
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, userID,
	); err != nil {
		return "", err
	}

	return orgID, tx.Commit(ctx)
}

// DeleteOrganization soft-deletes an org and removes all members.
func (db *DB) DeleteOrganization(ctx context.Context, slug string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var orgID string
	if err := tx.QueryRow(ctx,
		`UPDATE organizations SET slug = id::text, deleted_at = now()
		 WHERE slug = $1 AND deleted_at IS NULL RETURNING id`, slug,
	).Scan(&orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrgNotFound
		}
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM org_members WHERE org_id = $1`, orgID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// RenameOrganization updates the name and/or slug.
func (db *DB) RenameOrganization(ctx context.Context, currentSlug, newName, newSlug string) error {
	tag, err := db.Pool.Exec(ctx,
		`UPDATE organizations SET name = $1, slug = $2
		 WHERE slug = $3 AND deleted_at IS NULL`,
		newName, newSlug, currentSlug,
	)
	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			return ErrSlugTaken
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrOrgNotFound
	}
	return nil
}

// GetOrganizationBySlug returns an org by its slug.
func (db *DB) GetOrganizationBySlug(ctx context.Context, slug string) (*Organization, error) {
	var o Organization
	err := db.Pool.QueryRow(ctx,
		`SELECT id, name, slug, public, created_at FROM organizations WHERE slug = $1 AND deleted_at IS NULL`,
		slug,
	).Scan(&o.ID, &o.Name, &o.Slug, &o.Public, &o.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOrgNotFound
		}
		return nil, err
	}
	return &o, nil
}

// ListAllOrganizations returns all active orgs.
func (db *DB) ListAllOrganizations(ctx context.Context) ([]Organization, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, name, slug, public, created_at FROM organizations WHERE deleted_at IS NULL ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgs []Organization
	for rows.Next() {
		var o Organization
		if err := rows.Scan(&o.ID, &o.Name, &o.Slug, &o.Public, &o.CreatedAt); err != nil {
			return nil, err
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

// ListUserOrganizations returns all orgs a user belongs to.
func (db *DB) ListUserOrganizations(ctx context.Context, email string) ([]Organization, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT o.id, o.name, o.slug, o.public, o.created_at
		 FROM organizations o
		 JOIN org_members m ON m.org_id = o.id
		 JOIN users u ON u.id = m.user_id
		 WHERE u.email = $1 AND u.deleted_at IS NULL AND o.deleted_at IS NULL
		 ORDER BY o.name`,
		email,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgs []Organization
	for rows.Next() {
		var o Organization
		if err := rows.Scan(&o.ID, &o.Name, &o.Slug, &o.Public, &o.CreatedAt); err != nil {
			return nil, err
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

// ListOrgMembers returns all members of an org.
func (db *DB) ListOrgMembers(ctx context.Context, orgSlug string) ([]OrgMember, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT u.id, u.email, u.created_at, m.role, m.created_at
		 FROM org_members m
		 JOIN users u ON u.id = m.user_id
		 JOIN organizations o ON o.id = m.org_id
		 WHERE o.slug = $1 AND o.deleted_at IS NULL AND u.deleted_at IS NULL
		 ORDER BY m.created_at`,
		orgSlug,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []OrgMember
	for rows.Next() {
		var m OrgMember
		if err := rows.Scan(&m.User.ID, &m.User.Email, &m.User.CreatedAt, &m.Role, &m.JoinedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// AddOrgMember adds a user to an org with the given role.
func (db *DB) AddOrgMember(ctx context.Context, orgSlug, email, role string) error {
	tag, err := db.Pool.Exec(ctx,
		`INSERT INTO org_members (org_id, user_id, role)
		 SELECT o.id, u.id, $3
		 FROM organizations o, users u
		 WHERE o.slug = $1 AND u.email = $2 AND o.deleted_at IS NULL AND u.deleted_at IS NULL`,
		orgSlug, email, role,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return ErrAlreadyMember
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrOrgNotFound
	}
	return nil
}

// RemoveOrgMember removes a user from an org. Fails if they are the last owner.
func (db *DB) RemoveOrgMember(ctx context.Context, orgSlug, email string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var orgID, userID, role string
	if err := tx.QueryRow(ctx,
		`SELECT o.id, u.id, m.role
		 FROM org_members m
		 JOIN organizations o ON o.id = m.org_id
		 JOIN users u ON u.id = m.user_id
		 WHERE o.slug = $1 AND u.email = $2 AND o.deleted_at IS NULL`,
		orgSlug, email,
	).Scan(&orgID, &userID, &role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotMember
		}
		return err
	}

	if role == "owner" {
		var ownerCount int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM org_members WHERE org_id = $1 AND role = 'owner'`, orgID,
		).Scan(&ownerCount); err != nil {
			return err
		}
		if ownerCount <= 1 {
			return ErrLastOwner
		}
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM org_members WHERE org_id = $1 AND user_id = $2`, orgID, userID,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ChangeOrgMemberRole changes a member's role. Fails if demoting the last owner.
func (db *DB) ChangeOrgMemberRole(ctx context.Context, orgSlug, email, newRole string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var orgID, userID, currentRole string
	if err := tx.QueryRow(ctx,
		`SELECT o.id, u.id, m.role
		 FROM org_members m
		 JOIN organizations o ON o.id = m.org_id
		 JOIN users u ON u.id = m.user_id
		 WHERE o.slug = $1 AND u.email = $2 AND o.deleted_at IS NULL`,
		orgSlug, email,
	).Scan(&orgID, &userID, &currentRole); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotMember
		}
		return err
	}

	if currentRole == "owner" && newRole != "owner" {
		var ownerCount int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM org_members WHERE org_id = $1 AND role = 'owner'`, orgID,
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
