// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package supervisor provides a platform-agnostic interface for process
// supervision (systemd, launchd, Windows Services, etc.).
//
// On systems without a recognized supervisor the functions are no-ops.
package supervisor

import (
	"context"
	"net"
	"os/signal"
	"syscall"
)

// Supervisor communicates lifecycle events to the process supervisor
// and handles platform-specific shutdown signals.
type Supervisor interface {
	// Ready signals that the service has started and is ready to serve.
	Ready()

	// Stopping signals that the service has begun graceful shutdown.
	Stopping()

	// StartWatchdog begins sending periodic keepalive pings.
	// Blocks until ctx is cancelled; call as a goroutine. Returns
	// immediately if the supervisor does not require keepalives.
	StartWatchdog(ctx context.Context)

	// WaitForStop blocks until the supervisor or OS requests shutdown.
	WaitForStop(ctx context.Context) context.Context

	// ActivationListeners returns named listeners passed by the service
	// manager (e.g. systemd socket activation). Returns nil when the
	// platform does not support listener inheritance.
	ActivationListeners() (map[string][]net.Listener, error)
}

var detectors []func() Supervisor

func register(fn func() Supervisor) {
	detectors = append(detectors, fn)
}

// Detect returns a Supervisor for the current platform.
func Detect() Supervisor {
	for _, fn := range detectors {
		if s := fn(); s != nil {
			return s
		}
	}
	return &noop{}
}

type noop struct{}

func (*noop) Ready() {}

func (*noop) Stopping() {}

func (*noop) StartWatchdog(context.Context) {}

func (*noop) WaitForStop(ctx context.Context) context.Context {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	_ = stop
	return ctx
}

func (*noop) ActivationListeners() (map[string][]net.Listener, error) {
	return nil, nil
}
