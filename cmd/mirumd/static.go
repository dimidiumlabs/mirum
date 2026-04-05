// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"crypto/sha512"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
)

//go:embed web/*.html
var templateFS embed.FS

var shellTmpl = template.Must(template.ParseFS(templateFS, "web/shell.html"))

type assetRef struct {
	Href      string
	Integrity string // "sha384-BASE64" or empty
}

type pageAssets struct {
	CSS      []assetRef // <link rel="stylesheet">
	Scripts  []assetRef // <script type="module">
	Preloads []assetRef // <link rel="modulepreload">
	Preamble string     // inline <script type="module"> body, dev only

	// Full Content-Security-Policy header for this entry, with hash-based script-src.
	// Empty in dev mode; renderPage falls back to the global csp in that case.
	CSP string
}

func buildCSP(scripts, preloads, css []assetRef) string {
	var sb strings.Builder
	sb.WriteString(cspBase)

	writeHashes := func(refs []assetRef) {
		for _, r := range refs {
			if r.Integrity == "" {
				continue
			}
			sb.WriteString(" '")
			sb.WriteString(r.Integrity)
			sb.WriteString("'")
		}
	}

	sb.WriteString("; script-src")
	writeHashes(scripts)
	writeHashes(preloads)

	// style-src hash-sources for external stylesheets are inconsistently
	// supported (Firefox blocks them even when the hash matches). Allow
	// same-origin loading and rely on the integrity="" attribute rendered
	// into the <link> tag for tamper detection.
	sb.WriteString("; style-src 'self'")
	writeHashes(css)

	return sb.String()
}

type assetResolver struct {
	assets map[string]pageAssets // keyed by entry name (e.g. "dashboard")
}

func newAssetResolver() *assetResolver {
	if viteDevURL != "" {
		return &assetResolver{}
	}

	if len(manifestJSON) == 0 {
		slog.Warn("vite manifest not found, frontend assets unavailable")
		return &assetResolver{assets: map[string]pageAssets{}}
	}

	type viteManifestEntry struct {
		Name    string   `json:"name"`
		File    string   `json:"file"`
		Src     string   `json:"src"`
		CSS     []string `json:"css"`
		Imports []string `json:"imports"`
		IsEntry bool     `json:"isEntry"`
	}

	var manifest map[string]viteManifestEntry
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		slog.Error("failed to parse vite manifest", "err", err)
		return &assetResolver{assets: map[string]pageAssets{}}
	}

	refOf := func(file string) assetRef {
		integrity := ""

		data, err := assetsFS.ReadFile("static/" + file)
		if err != nil {
			slog.Error("asset not found for integrity hash", "file", file, "err", err)
		} else {
			sum := sha512.Sum384(data)
			integrity = "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
		}

		return assetRef{Href: "/" + file, Integrity: integrity}
	}

	assets := make(map[string]pageAssets)
	for key, entry := range manifest {
		if !entry.IsEntry {
			continue
		}

		// Entry key is e.g. "entries/dashboard.tsx", derive "dashboard".
		name := strings.TrimSuffix(path.Base(key), path.Ext(key))

		seen := make(map[string]bool)
		var scripts, preloads, css []assetRef

		var walk func(keys []string)
		walk = func(keys []string) {
			for _, k := range keys {
				if seen[k] {
					continue
				}
				seen[k] = true
				chunk, ok := manifest[k]
				if !ok {
					continue
				}
				if chunk.IsEntry {
					scripts = append(scripts, refOf(chunk.File))
				} else {
					preloads = append(preloads, refOf(chunk.File))
				}
				for _, c := range chunk.CSS {
					if seen[c] {
						continue
					}
					seen[c] = true
					css = append(css, refOf(c))
				}
				walk(chunk.Imports)
			}
		}
		walk([]string{key})

		assets[name] = pageAssets{
			Scripts:  scripts,
			Preloads: preloads,
			CSS:      css,
			CSP:      buildCSP(scripts, preloads, css),
		}
	}

	return &assetResolver{assets: assets}
}

// resolve returns page assets for the given entry name. Integrity is omitted
// in dev mode because Vite's HMR mutates file contents between requests.
func (ar *assetResolver) resolve(name string) pageAssets {
	if viteDevURL != "" {
		return pageAssets{
			Scripts: []assetRef{
				{Href: viteDevURL + "/@vite/client"},
				{Href: viteDevURL + "/entries/" + name + ".tsx"},
			},
			// @vitejs/plugin-react requires this preamble to install the
			// React Refresh hooks before any component module runs. Vite
			// auto-injects it when it serves HTML, so we reproduce it here.
			Preamble: `import RefreshRuntime from "` + viteDevURL + `/@react-refresh"
RefreshRuntime.injectIntoGlobalHook(window)
window.$RefreshReg$ = () => {}
window.$RefreshSig$ = () => (type) => type
window.__vite_plugin_react_preamble_installed__ = true`,
		}
	}
	return ar.assets[name]
}

func (ar *assetResolver) renderPage(w http.ResponseWriter, entry string, data any) {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	assets := ar.resolve(entry)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if assets.CSP != "" {
		w.Header().Set("Content-Security-Policy", assets.CSP)
	}

	if err := shellTmpl.Execute(w, struct {
		DataJSON template.JS
		Scripts  []assetRef
		Preloads []assetRef
		CSS      []assetRef
		Preamble template.JS
	}{
		DataJSON: template.JS(dataJSON),
		Scripts:  assets.Scripts,
		Preloads: assets.Preloads,
		CSS:      assets.CSS,
		Preamble: template.JS(assets.Preamble),
	}); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func assetsHandler() http.Handler {
	sub, err := fs.Sub(assetsFS, "static")
	if err != nil {
		panic(fmt.Sprintf("static fs: %v", err))
	}

	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
