// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Runtime type of this worker binary. Different worker types
// (mirum-worker-vm, mirum-worker-docker, etc.) will have different values.
const workerRuntime = "host"

type config struct {
	Name    string `yaml:"name"`
	Server  string `yaml:"server"`
	KeyFile string `yaml:"key_file"`
	TLSCA   string `yaml:"tls_ca"` // custom CA cert for self-signed/dev
}

func getConfig(filename string) (*config, error) {
	cfg := &config{
		Server: "localhost:2026",
	}

	if filename != "" {
		data, err := os.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("couldn't read config: %w", err)
		}

		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("couldn't parse config: %w", err)
		}
	}

	if cfg.KeyFile == "" {
		return nil, fmt.Errorf("error: key_file is required")
	}

	return cfg, nil
}
