// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package supervisor

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
)

type systemd struct{}

func detectSystemd() bool {
	return os.Getenv("NOTIFY_SOCKET") != ""
}

func (*systemd) WaitForStop(ctx context.Context) context.Context {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	_ = stop // stop allows you to cancel the observer, but it's not particularly useful
	return ctx
}

func (*systemd) Ready() {
	if _, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
		slog.Warn("sd_notify ready", "err", err)
	}
}

func (*systemd) Stopping() {
	if _, err := daemon.SdNotify(false, daemon.SdNotifyStopping); err != nil {
		slog.Warn("sd_notify stopping", "err", err)
	}
}

func (*systemd) StartWatchdog(ctx context.Context) {
	usecStr := os.Getenv("WATCHDOG_USEC")
	if usecStr == "" {
		return
	}

	usec, err := strconv.ParseInt(usecStr, 10, 64)
	if err != nil || usec <= 0 {
		return
	}

	interval := time.Duration(usec) * time.Microsecond / 2
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if _, err := daemon.SdNotify(false, daemon.SdNotifyWatchdog); err != nil {
			slog.Warn("sd_notify watchdog", "err", err)
			return
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}
