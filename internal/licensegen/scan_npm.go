// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const webDir = "cmd/mirum-server/web"

// scanNPM reads package-lock.json (v3+), filters to runtime packages, reads
// each package's LICENSE verbatim (or synthesizes a copyright notice when
// none ships — canonical SPDX text is never substituted), and collapses the
// scopes listed in collapsedScopes into single "@scope" rows.
func scanNPM(root string) ([]Dep, error) {
	var lock struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Version                      string `json:"version"`
			License                      any    `json:"license"`
			Dev, DevOptional, Link, Peer bool
		} `json:"packages"`
	}
	raw, err := os.ReadFile(filepath.Join(root, webDir, "package-lock.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil, err
	}
	if lock.LockfileVersion < 3 {
		return nil, fmt.Errorf("lockfileVersion %d unsupported, regenerate with npm v7+", lock.LockfileVersion)
	}

	// npm hoists identical name@version under multiple paths; skip duplicates
	// before the expensive LICENSE read.
	seen := map[string]bool{}
	var deps []Dep
	for path, pkg := range lock.Packages {
		if path == "" || pkg.Link || pkg.Dev || pkg.DevOptional {
			continue
		}
		name := npmName(path)
		key := name + "@" + pkg.Version
		if seen[key] {
			continue
		}
		seen[key] = true

		expr, err := npmSPDX(pkg.License)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}

		atoms, err := validateSPDX(expr)
		if err != nil {
			return nil, fmt.Errorf("%s declares %q: %w", key, expr, err)
		}

		pkgDir := filepath.Join(root, webDir, path)
		deps = append(deps, Dep{
			Name:    name,
			Version: pkg.Version,
			SPDX:    expr,
			URL:     "https://www.npmjs.com/package/" + name + "/v/" + pkg.Version,
			atoms:   atoms,
			text:    npmText(pkgDir, name),
		})
	}

	scopeOf := func(name string) string {
		if !strings.HasPrefix(name, "@") {
			return ""
		}
		scope, _, ok := strings.Cut(name, "/")
		if !ok {
			return ""
		}
		return scope
	}

	buckets := map[string][]Dep{}
	var out []Dep
	for _, d := range deps {
		if s := scopeOf(d.Name); slices.Contains(collapsedScopes, s) {
			buckets[s] = append(buckets[s], d)
			continue
		}
		out = append(out, d)
	}

	for scope, items := range buckets {
		spdx := items[0].SPDX
		version := items[0].Version
		for _, d := range items[1:] {
			if d.SPDX != spdx {
				return nil, fmt.Errorf("scope %s: mixed SPDX %q vs %q (%s)", scope, spdx, d.SPDX, d.Name)
			}
			if d.Version != version {
				version = ""
			}
		}

		// Prefer umbrella LICENSE, then any sub-package's own.
		text, _, err := readLicenseFile(filepath.Join(root, webDir, "node_modules", strings.TrimPrefix(scope, "@")))
		if err != nil {
			text = items[0].text
			for _, d := range items {
				if !strings.HasPrefix(d.text, "Copyright (c) contributors to ") {
					text = d.text
					break
				}
			}
		}

		out = append(out, Dep{
			Name:    strings.TrimPrefix(scope, "@"),
			Version: version,
			SPDX:    spdx,
			URL:     "https://www.npmjs.com/~" + strings.TrimPrefix(scope, "@"),
			Count:   len(items),
			atoms:   items[0].atoms,
			text:    text,
		})
	}
	return out, nil
}

// npmName extracts the package name from an npm lockfile key like
// "node_modules/foo" or "node_modules/foo/node_modules/@scope/bar".
func npmName(path string) string {
	i := strings.LastIndex(path, "node_modules/")
	if i < 0 {
		return ""
	}
	n := path[i+len("node_modules/"):]
	if strings.HasPrefix(n, "@") {
		return n // scoped "@scope/name" is one name
	}
	head, _, _ := strings.Cut(n, "/")
	return head
}

// npmSPDX normalises package.json's `license` field. Modern packages use a
// string; we accept legacy array-of-objects too. Outer parens are stripped
// so "(MIT OR Apache-2.0)" displays as "MIT OR Apache-2.0".
func npmSPDX(v any) (string, error) {
	var s string
	switch x := v.(type) {
	case string:
		s = strings.TrimSpace(x)
	case []any:
		var ids []string
		for _, it := range x {
			if m, ok := it.(map[string]any); ok {
				if t, ok := m["type"].(string); ok && t != "" {
					ids = append(ids, t)
				}
			}
		}
		s = strings.Join(ids, " OR ")
	}
	if s == "" {
		return "", fmt.Errorf("no license field")
	}
	for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s, nil
}

// npmText returns the LICENSE text shipped with a package, or a copyright
// notice derived from package.json when no LICENSE file exists. Canonical
// SPDX text is never substituted — doing so would claim the author wrote
// something they didn't ship.
func npmText(pkgDir, name string) string {
	if text, _, err := readLicenseFile(pkgDir); err == nil {
		return text
	}
	if c := copyrightFromPackageJSON(pkgDir); c != "" {
		return c + "\n"
	}
	return "Copyright (c) contributors to " + name + "\n"
}

// copyrightFromPackageJSON builds a "Copyright (c) ..." line from the
// package's author/contributors fields. Returns empty if neither is present.
func copyrightFromPackageJSON(pkgDir string) string {
	data, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		return ""
	}
	var pj struct {
		Author       any   `json:"author"`
		Contributors []any `json:"contributors"`
	}
	if err := json.Unmarshal(data, &pj); err != nil {
		return ""
	}
	var names []string
	for _, v := range append([]any{pj.Author}, pj.Contributors...) {
		if s := personName(v); s != "" {
			names = append(names, s)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "Copyright (c) " + strings.Join(names, ", ")
}

// personName renders an npm author/contributor entry (string or {name,email})
// as "Name <email>" or just "Name".
func personName(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case map[string]any:
		name, _ := x["name"].(string)
		email, _ := x["email"].(string)
		name = strings.TrimSpace(name)
		email = strings.TrimSpace(email)
		if name == "" {
			return ""
		}
		if email == "" {
			return name
		}
		return name + " <" + email + ">"
	}
	return ""
}
