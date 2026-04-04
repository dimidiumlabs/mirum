// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type tlsConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type config struct {
	WebAddr     string `yaml:"web_addr"`
	GrpcAddr    string `yaml:"grpc_addr"`
	AdminSocket string `yaml:"admin_socket"`
	DatabaseUri string `yaml:"database_uri"`
	Pepper      string `yaml:"pepper"`

	GrpcTls tlsConfig  `yaml:"grpc_tls"`
	WebTls  *tlsConfig `yaml:"web_tls"` // optional

	GitHubToken   string `yaml:"token"`
	WebhookSecret string `yaml:"webhook_secret"`
}

func getConfig(filename string) (*config, error) {
	cfg := &config{
		GrpcAddr:    ":2026",
		WebAddr:     ":3000",
		AdminSocket: "/run/mirumd/admin.sock",
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	if cfg.DatabaseUri == "" {
		return nil, fmt.Errorf("error: database_uri is required")
	}
	if cfg.WebhookSecret == "" {
		return nil, fmt.Errorf("error: webhook_secret is required")
	}
	if cfg.Pepper == "" {
		return nil, fmt.Errorf("error: pepper is required")
	}
	if cfg.GrpcTls.Cert == "" || cfg.GrpcTls.Key == "" {
		return nil, fmt.Errorf("error: grpc_tls.cert and grpc_tls.key are required")
	}

	return cfg, nil
}
