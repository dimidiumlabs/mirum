// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"mrdimidium/mirum/internal/supervisor"

	"gopkg.in/yaml.v3"
)

type config struct {
	Server string `yaml:"server"`
}

var cfg = config{
	Server: "localhost:2026",
}

var configFile = flag.String("config", "", "path to config file")

func main() {
	flag.Parse()

	if *configFile != "" {
		data, err := os.ReadFile(*configFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	sup := supervisor.Detect()
	ctx := sup.WaitForStop(context.Background())

	slog.Info("connecting", "server", cfg.Server)

	sup.Ready()
	go sup.StartWatchdog()

	<-ctx.Done()
	slog.Info("shutting down")
	sup.Stopping()
}
