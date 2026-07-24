// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package host implements an executor.Runtime that runs task commands
// directly on the worker. It provides no isolation — it is the fast path
// for local iteration (mirum task) and trusted workloads.
package host

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"dimidiumlabs/mirum/internal/executor"
)

// Host is an executor.Runtime backed by a temporary directory on the worker.
// Commands run as child processes of the worker, with no sandboxing.
type Host struct {
	root string
}

var _ executor.Runtime = (*Host)(nil)

// New creates a Host runtime rooted at a fresh temporary directory.
func New() (*Host, error) {
	root, err := os.MkdirTemp("", "mirum-host-*")
	if err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}
	return &Host{root: root}, nil
}

func init() {
	executor.RegisterRuntime(executor.RuntimeBackend{
		Name:     "host",
		Priority: executor.PriorityHost,
		New:      func() (executor.Runtime, error) { return New() },
	})
}

// Platform reports the worker's own OS and architecture.
func (h *Host) Platform() executor.Platform {
	return executor.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// resolve maps an env-relative slash path to an absolute path inside root,
// rejecting paths that would escape it.
func (h *Host) resolve(p string) (string, error) {
	if p == "" {
		return h.root, nil
	}
	local, err := filepath.Localize(p)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", p, err)
	}
	return filepath.Join(h.root, local), nil
}

// Exec runs cmd as a child process of the worker.
func (h *Host) Exec(ctx context.Context, cmd executor.Command) (executor.Result, error) {
	if len(cmd.Args) == 0 {
		return executor.Result{}, fmt.Errorf("exec: empty args")
	}

	dir, err := h.resolve(cmd.Dir)
	if err != nil {
		return executor.Result{}, err
	}

	c := exec.CommandContext(ctx, cmd.Args[0], cmd.Args[1:]...)
	c.Dir = dir
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	if len(cmd.Env) > 0 {
		c.Env = os.Environ()
		for k, v := range cmd.Env {
			c.Env = append(c.Env, k+"="+v)
		}
	}

	start := time.Now()
	runErr := c.Run()
	wall := time.Since(start)

	if ctx.Err() != nil {
		return executor.Result{}, ctx.Err()
	}

	ps := c.ProcessState
	if ps == nil {
		// The process never started, e.g. the executable was not found.
		return executor.Result{}, fmt.Errorf("exec %s: %w", cmd.Args[0], runErr)
	}

	return executor.Result{
		Code: ps.ExitCode(),
		Usage: executor.Usage{
			Wall:      wall,
			CPUUser:   ps.UserTime(),
			CPUSystem: ps.SystemTime(),
			MaxRSS:    maxRSS(ps),
		},
	}, nil
}

// FileSend writes a single file into the work directory, creating parent
// directories as needed.
func (h *Host) FileSend(ctx context.Context, path string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dst, err := h.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// FileRecv streams a single file out of the work directory.
func (h *Host) FileRecv(ctx context.Context, path string, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	src, err := h.resolve(path)
	if err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(w, f)
	return err
}

// FileList returns the env-relative paths of every file under dir.
func (h *Host) FileList(ctx context.Context, dir string) ([]string, error) {
	base, err := h.resolve(dir)
	if err != nil {
		return nil, err
	}

	var out []string
	err = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(h.root, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Close removes the work directory.
func (h *Host) Close() error {
	return os.RemoveAll(h.root)
}
