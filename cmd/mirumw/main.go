// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"dimidiumlabs/mirum/internal/protocol"
	"dimidiumlabs/mirum/internal/supervisor"
)

func main() {
	configFile := flag.String("config", "", "path to config file")
	flag.Parse()

	cfg, err := getConfig(*configFile)
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	sup := supervisor.Detect()
	ctx := sup.WaitForStop(context.Background())

	sup.Ready()
	go sup.StartWatchdog(ctx)

	backoff := protocol.NewBackoff()

	for ctx.Err() == nil {
		c, err := connect(ctx, cfg)
		if err != nil {
			slog.Error("connect failed", "err", err)
			if !backoff.Wait(ctx) {
				break
			}
			continue
		}

		slog.Info("connected", "server", cfg.Server)
		backoff.Reset()

		if err := c.work(ctx); err != nil && ctx.Err() == nil {
			slog.Error("work loop failed", "err", err)
		}

		c.close()
	}

	slog.Info("shutting down")
	sup.Stopping()
}
