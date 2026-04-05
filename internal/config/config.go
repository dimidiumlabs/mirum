// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config holds hard-coded tunables shared across mirumd and mirumw:
// timeouts, sizes, intervals, and limits that are not (yet) exposed through
// the YAML user config. Grouping them here keeps magic numbers out of call
// sites and gives a single place to audit defaults.
package config

import "time"

// HTTP server hardening — applied to every *http.Server by hardenServer.
const (
	HTTPIdleTimeout       = 120 * time.Second
	HTTPReadHeaderTimeout = 10 * time.Second
	HTTPMaxHeaderBytes    = 1 << 16 // 64 KiB
	HTTPShutdownTimeout   = 30 * time.Second
)

// Web router middleware — chi.
const (
	WebRequestTimeout = 30 * time.Second
	WebMaxBodyBytes   = 64 << 20 // 64 MiB

	// /auth routes: tighter body cap and per-IP rate limit.
	AuthMaxBodyBytes = 4096
	AuthRateLimit    = 10
	AuthRateWindow   = time.Minute

	// /api/v1 routes: SPA-friendly per-IP rate limit.
	APIRateLimit  = 300
	APIRateWindow = time.Minute
)

// Task queue — channel buffer between webhook handlers and gRPC Poll.
const TaskQueueCapacity = 100

// Sessions.
const (
	SessionTTL           = 14 * 24 * time.Hour // cookie + DB row lifetime
	SessionPurgeInterval = time.Hour           // background PurgeExpiredSessions
)

// Worker mTLS handshake.
const (
	WorkerCertLifetime   = 24 * time.Hour // self-signed worker cert NotAfter
	WorkerClockSkewLimit = time.Minute    // max allowed skew between worker and server
)

// Worker reconnection backoff (exponential, jittered).
const (
	WorkerBackoffMin = time.Second
	WorkerBackoffMax = 60 * time.Second
)
