// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build dev

package main

import "embed"

// viteDevURL points the HTML shell at a running Vite dev server when the
// binary is built with `go build -tags dev`. Hot module replacement and
// source modules are fetched from there instead of the embedded static/.
const viteDevWs = "ws://localhost:5173"
const viteDevURL = "http://localhost:5173"

const cspBase = "default-src 'self'; " +
	"base-uri 'none'; " +
	"img-src 'self' data: " + viteDevURL + "; " +
	"font-src 'self' " + viteDevURL + "; " +
	"style-src-attr 'unsafe-inline'; " +
	"object-src 'none'; " +
	"connect-src 'self' " + viteDevURL + " " + viteDevWs + "; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// csp applied globally to every response as a safety net.
// For HTML pages it is overridden per-entry in renderPage.
// 'unsafe-inline' is necessary for the @vitejs/plugin-react HMR preamble
// that renderPage injects inline in dev mode.
const csp = cspBase +
	"; script-src 'self' 'unsafe-inline' " + viteDevURL +
	"; style-src 'self' 'unsafe-inline' " + viteDevURL

// Empty in dev builds: assets are served by the Vite dev server.
var manifestEnGB []byte
var manifestEnUS []byte
var assetsFS embed.FS
