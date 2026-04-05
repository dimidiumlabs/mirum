// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"context"
	"math/rand/v2"
	"time"

	"dimidiumlabs/mirum/internal/config"
)

type Backoff struct {
	attempt  int
	Min, Max time.Duration
}

func NewBackoff() *Backoff {
	return &Backoff{Min: config.WorkerBackoffMin, Max: config.WorkerBackoffMax}
}

func (b *Backoff) Reset() {
	b.attempt = 0
}

// Wait sleeps with exponential backoff + jitter. Returns false if ctx is cancelled.
func (b *Backoff) Wait(ctx context.Context) bool {
	d := min(b.Max, b.Min<<b.attempt)

	// Add jitter: 50%-100% of the computed duration
	d = d/2 + time.Duration(rand.Int64N(int64(d/2)))

	b.attempt++

	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}
