// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package executor

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"go.starlark.net/starlark"
)

const entry = ".mirum/project.star"

func RunCmd(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	return buf.String(), err
}

type SOTaskCtx struct {
	dir string
}

var _ starlark.HasAttrs = (*SOTaskCtx)(nil)

func (c *SOTaskCtx) String() string        { return "ctx" }
func (c *SOTaskCtx) Type() string          { return "ctx" }
func (c *SOTaskCtx) Freeze()               {}
func (c *SOTaskCtx) Truth() starlark.Bool  { return true }
func (c *SOTaskCtx) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: ctx") }
func (c *SOTaskCtx) AttrNames() []string   { return []string{"shell"} }

func (c *SOTaskCtx) Attr(name string) (starlark.Value, error) {
	switch name {
	case "shell":
		return starlark.NewBuiltin("ctx.shell", c.shell), nil
	}
	return nil, nil
}

func (c *SOTaskCtx) shell(
	thread *starlark.Thread,
	fn *starlark.Builtin,
	args starlark.Tuple,
	kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var cmd string
	if err := starlark.UnpackPositionalArgs(fn.Name(), args, kwargs, 1, &cmd); err != nil {
		return nil, err
	}

	proc := exec.Command("bash", "-c", cmd)
	proc.Dir = c.dir
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	err := proc.Run()
	if err != nil {
		return nil, err
	}

	return starlark.None, nil
}

// Run clones the repository into a temporary directory, runs the Starlark
// build script, and cleans up afterwards.
func Run(cloneURL, branch string) error {
	dir, err := os.MkdirTemp("", "mirum-*")
	if err != nil {
		return fmt.Errorf("create workdir: %w", err)
	}
	defer os.RemoveAll(dir)

	if out, err := RunCmd(dir, "git", "clone", "--depth=1", "--branch", branch, cloneURL, "."); err != nil {
		return fmt.Errorf("git clone: %s: %w", out, err)
	}

	return runStarlark(dir)
}

func runStarlark(dir string) error {
	thread := &starlark.Thread{Name: "mirum"}
	globals, err := starlark.ExecFile(thread, filepath.Join(dir, entry), nil, nil)
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

	ctx := &SOTaskCtx{dir: dir}
	_, err = starlark.Call(thread, fn, starlark.Tuple{ctx}, nil)
	return err
}
