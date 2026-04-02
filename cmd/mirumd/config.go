// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type config struct {
	WwwAddr     string `yaml:"www_addr"`
	GrpcAddr    string `yaml:"grpc_addr"`
	AdminSocket string `yaml:"admin_socket"`
	DatabaseUri string `yaml:"database_uri"`
	Pepper      string `yaml:"pepper"`

	TLSCert string `yaml:"tls_cert"`
	TLSKey  string `yaml:"tls_key"`

	GitHubToken   string `yaml:"token"`
	WebhookSecret string `yaml:"webhook_secret"`
}

func getConfig(filename string) (*config, error) {
	cfg := &config{
		GrpcAddr:    ":2026",
		WwwAddr:     ":3000",
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

	return cfg, nil
}
