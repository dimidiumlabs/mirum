// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/google/licensecheck"
)

// goEntrypoints are the main packages whose linker inputs form the runtime
// graph. Every production binary we ship lives under cmd/.
var goEntrypoints = []string{
	"./cmd/mirum-server",
	"./cmd/mirum-worker",
	"./cmd/mirum",
}

// scanGo reads the prod dependency graph via `go list -deps -json` and
// classifies each module's LICENSE file with google/licensecheck at a 75%
// coverage threshold. Below that we refuse to guess.
func scanGo(root string) ([]Dep, error) {
	args := append([]string{"list", "-tags=licensegen", "-deps", "-json"}, goEntrypoints...)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	type mod struct {
		Path, Version, Dir string
		Main               bool
		Replace            *mod
	}
	type pkg struct {
		Standard bool
		Module   *mod
	}

	mods := map[string]*mod{}
	dec := json.NewDecoder(stdout)
	for {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if p.Standard || p.Module == nil || p.Module.Main {
			continue
		}
		m := p.Module
		if m.Replace != nil {
			m = m.Replace
		}
		if m.Dir == "" {
			return nil, fmt.Errorf("%s: empty Dir (run `go mod download`)", m.Path)
		}
		mods[m.Path+"@"+m.Version] = m
	}
	if err := cmd.Wait(); err != nil {
		return nil, err
	}

	deps := make([]Dep, 0, len(mods))
	for _, m := range mods {
		text, path, err := readLicenseFile(m.Dir)
		if err != nil {
			return nil, fmt.Errorf("%s@%s: no LICENSE file", m.Path, m.Version)
		}
		cov := licensecheck.Scan([]byte(text))
		if cov.Percent < 75 || len(cov.Match) == 0 {
			return nil, fmt.Errorf("%s@%s: cannot classify %s (%.0f%%)", m.Path, m.Version, path, cov.Percent)
		}
		spdx := cov.Match[0].ID
		atoms, err := validateSPDX(spdx)
		if err != nil {
			return nil, fmt.Errorf("%s@%s: %w", m.Path, m.Version, err)
		}
		deps = append(deps, Dep{
			Name:    m.Path,
			Version: m.Version,
			SPDX:    spdx,
			URL:     "https://pkg.go.dev/" + m.Path + "@" + m.Version,
			atoms:   atoms,
			text:    text,
		})
	}
	return deps, nil
}
