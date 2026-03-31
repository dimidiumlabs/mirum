// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package supervisor

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
)

type systemd struct{}

func detectSystemd() Supervisor {
	if os.Getenv("NOTIFY_SOCKET") == "" {
		return nil
	}
	return &systemd{}
}

func (*systemd) WaitForStop(ctx context.Context) context.Context {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	_ = stop
	return ctx
}

func (*systemd) Ready() {
	daemon.SdNotify(false, daemon.SdNotifyReady)
}

func (*systemd) Stopping() {
	daemon.SdNotify(false, daemon.SdNotifyStopping)
}

func (*systemd) StartWatchdog() {
	usecStr := os.Getenv("WATCHDOG_USEC")
	if usecStr == "" {
		return
	}
	usec, err := strconv.ParseInt(usecStr, 10, 64)
	if err != nil || usec <= 0 {
		return
	}
	interval := time.Duration(usec) * time.Microsecond / 2
	for {
		daemon.SdNotify(false, daemon.SdNotifyWatchdog)
		time.Sleep(interval)
	}
}
