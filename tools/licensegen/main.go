// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command licensegen writes build/licenses.json — the third-party dependency
// manifest embedded into mirum binaries. Output is pre-grouped: each
// ecosystem contains SPDX groups; each group contains text variants (packages
// sharing identical LICENSE text collapse into one variant); each variant
// lists its deps. The frontend renders without further transformation.
package main

import (
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/github/go-spdx/v2/spdxexp"
)

// allowedSPDX are SPDX ids approved for runtime deps. mirum sells a
// commercial license, so GPL-family ids are excluded even though the
// upstream distribution is AGPL-3.0-or-later.
var allowedSPDX = []string{
	"0BSD", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause",
	"CC0-1.0", "ISC", "MIT", "OFL-1.1",
	"Unicode-3.0", "Unlicense", "Zlib",
}

// collapsedScopes are npm scopes whose sub-packages come from a single
// upstream monorepo and should render as one "@scope" row. All sub-packages
// of a collapsed scope must declare the same SPDX — mismatch aborts.
var collapsedScopes = []string{"@radix-ui"}

type Dep struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	SPDX    string `json:"spdx"`
	URL     string `json:"url,omitempty"`
	Count   int    `json:"count,omitempty"` // >0 means a collapsed scope entry covering N sub-packages

	atoms []string // internal: atomic SPDX ids for cross-listing
	text  string   // internal: verbatim LICENSE text
}

type Variant struct {
	Text string `json:"text"`
	Deps []Dep  `json:"deps"`
}

type Group struct {
	SPDX     string    `json:"spdx"`
	Total    int       `json:"total"`
	Variants []Variant `json:"variants"`
}

type Ecosystem struct {
	Total  int     `json:"total"`
	Groups []Group `json:"groups"`
}

type Manifest struct {
	GeneratedAt string    `json:"generated_at"`
	Go          Ecosystem `json:"go"`
	NPM         Ecosystem `json:"npm"`
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("licensegen: ")

	out := flag.String("out", "", "output path for licenses.json")
	repo := flag.String("repo", "", "repo root (defaults to walking up from cwd)")
	flag.Parse()

	if *out == "" {
		log.Fatal("missing required -out flag")
	}
	root, err := resolveRepoRoot(*repo)
	if err != nil {
		log.Fatal(err)
	}

	goDeps, err := scanGo(root)
	if err != nil {
		log.Fatalf("scan go: %v", err)
	}
	npmDeps, err := scanNPM(root)
	if err != nil {
		log.Fatalf("scan npm: %v", err)
	}

	m := Manifest{
		GeneratedAt: sourceDateEpoch().UTC().Format(time.RFC3339),
		Go:          group(goDeps),
		NPM:         group(npmDeps),
	}

	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, append(body, '\n'), 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	log.Printf("wrote %s (go=%d npm=%d)", *out, m.Go.Total, m.NPM.Total)
}

// group assembles deps into SPDX atoms × text variants. A dep with a compound
// expression ("A AND B") is cross-listed under every atom.
// Unexported Dep fields (atoms, text) are dropped by encoding/json.
func group(deps []Dep) Ecosystem {
	// atom → text → *Variant
	byAtom := map[string]map[string]*Variant{}
	for _, d := range deps {
		for _, atom := range d.atoms {
			byText := byAtom[atom]
			if byText == nil {
				byText = map[string]*Variant{}
				byAtom[atom] = byText
			}
			v := byText[d.text]
			if v == nil {
				v = &Variant{Text: d.text}
				byText[d.text] = v
			}
			v.Deps = append(v.Deps, d)
		}
	}

	groups := make([]Group, 0, len(byAtom))
	for atom, byText := range byAtom {
		variants := make([]Variant, 0, len(byText))
		total := 0
		for _, v := range byText {
			slices.SortFunc(v.Deps, func(a, b Dep) int { return strings.Compare(a.Name, b.Name) })
			variants = append(variants, *v)
			total += len(v.Deps)
		}
		slices.SortFunc(variants, func(a, b Variant) int {
			return cmp.Or(
				cmp.Compare(len(b.Deps), len(a.Deps)), // desc
				cmp.Compare(a.Deps[0].Name, b.Deps[0].Name),
			)
		})
		groups = append(groups, Group{SPDX: atom, Total: total, Variants: variants})
	}
	slices.SortFunc(groups, func(a, b Group) int {
		return cmp.Or(cmp.Compare(b.Total, a.Total), cmp.Compare(a.SPDX, b.SPDX))
	})
	return Ecosystem{Total: len(deps), Groups: groups}
}

// validateSPDX checks expr against allowedSPDX and returns its atomic ids.
func validateSPDX(expr string) ([]string, error) {
	ok, err := spdxexp.Satisfies(expr, allowedSPDX)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("SPDX %q not allowed", expr)
	}
	return spdxexp.ExtractLicenses(expr)
}

// readLicenseFile returns the contents and path of the first LICENSE-like
// file in dir. "LICENSE", "LICENCE", "COPYING" prefixes with any extension.
func readLicenseFile(dir string) (string, string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", err
	}
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if strings.HasPrefix(n, "license") || strings.HasPrefix(n, "licence") || strings.HasPrefix(n, "copying") {
			p := filepath.Join(dir, e.Name())
			if b, err := os.ReadFile(p); err == nil {
				return normalize(string(b)), p, nil
			}
		}
	}
	return "", "", os.ErrNotExist
}

// normalize strips BOM + trims whitespace + LF line endings, so near-identical
// texts (differing only by trailing blank lines or CRLF) dedupe.
func normalize(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(s) + "\n"
}

func sourceDateEpoch() time.Time {
	if v, _ := strconv.ParseInt(os.Getenv("SOURCE_DATE_EPOCH"), 10, 64); v > 0 {
		return time.Unix(v, 0)
	}
	return time.Now()
}

func resolveRepoRoot(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		p := filepath.Dir(dir)
		if p == dir {
			return "", os.ErrNotExist
		}
		dir = p
	}
}
