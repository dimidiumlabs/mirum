// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

// Runtime executing the Starlark pipeline in one of the supported runtimes
package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

const entry = ".mirum/project.star"

// Run selects a Runtime, clones the repository into it, runs the Starlark
// build script, and discards the Runtime afterwards.
func Run(ctx context.Context, cloneURL, branch string) error {
	rt, err := NewRuntime()
	if err != nil {
		return fmt.Errorf("create runtime: %w", err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			slog.Warn("executor: runtime cleanup failed", "err", err)
		}
	}()

	var clone bytes.Buffer
	res, err := rt.Exec(ctx, Command{
		Args:   []string{"git", "clone", "--depth=1", "--branch", branch, cloneURL, "."},
		Stdout: &clone,
		Stderr: &clone,
	})
	if err != nil {
		return fmt.Errorf("git clone: %w", err)
	}
	if res.Code != 0 {
		return fmt.Errorf("git clone exited with code %d: %s", res.Code, clone.String())
	}

	var script bytes.Buffer
	if err := rt.FileRecv(ctx, entry, &script); err != nil {
		return fmt.Errorf("read %s: %w", entry, err)
	}

	thread := &starlark.Thread{Name: "mirum"}
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, entry, script.Bytes(), nil)
	if err != nil {
		return err
	}

	projectFn, ok := globals["project"]
	if !ok {
		return fmt.Errorf("%s: project() not defined", entry)
	}
	fn, ok := projectFn.(starlark.Callable)
	if !ok {
		return fmt.Errorf("%s: project is not a function", entry)
	}

	tctx := &SOTaskCtx{ctx: ctx, rt: rt}
	_, err = starlark.Call(thread, fn, starlark.Tuple{tctx}, nil)
	return err
}
