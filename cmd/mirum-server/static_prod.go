// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !dev

package main

import "embed"

// viteDevURL is empty in production builds: the HTML shell loads hashed
// assets from the embedded static/ directory via the Vite manifest.
const viteDevURL = ""

const cspBase = "default-src 'self'; " +
	"base-uri 'none'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"style-src-attr 'unsafe-inline'; " +
	"object-src 'none'; " +
	"connect-src 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// csp applied globally to every response as a safety net.
// For HTML pages it is overridden per-entry in renderPage.
const csp = cspBase + "; script-src 'self'; style-src 'self'"

//go:embed static/.vite/manifest-en-GB.json
var manifestEnGB []byte

//go:embed static/.vite/manifest-en-US.json
var manifestEnUS []byte

//go:embed static/assets
var assetsFS embed.FS
