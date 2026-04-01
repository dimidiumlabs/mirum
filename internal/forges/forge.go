// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package forges

import (
	"context"
	"errors"
	"net/http"
)

// ErrInvalidSignature is returned when webhook signature verification fails.
var ErrInvalidSignature = errors.New("invalid webhook signature")

// Status represents a normalized build status.
// Each forge maps these to its native values.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusSuccess Status = "success"
	StatusFailure Status = "failure"
)

// PushEvent is a forge-agnostic push event.
type PushEvent struct {
	Owner    string // repository owner or namespace (e.g. "group/subgroup" for GitLab)
	Repo     string // repository name
	Branch   string
	SHA      string
	CloneURL string
}

// Forge abstracts a Git hosting platform.
type Forge interface {
	// Webhook validates and parses an incoming webhook request.
	// Returns (nil, nil) for events that should be silently ignored.
	// Returns ErrInvalidSignature if signature verification fails.
	Webhook(r *http.Request, body []byte) (*PushEvent, error)

	// SetStatus reports build status for a commit.
	SetStatus(ctx context.Context, ev *PushEvent, status Status, desc string) error

	// AuthURL returns a clone URL with embedded credentials.
	AuthURL(cloneURL string) string
}
