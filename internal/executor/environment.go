// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package executor

import (
	"context"
	"fmt"
	"os"

	"go.starlark.net/starlark"
)

// SOTaskCtx is the `ctx` value passed to the Starlark project() function. It
// exposes the task's Runtime to the build script.
type SOTaskCtx struct {
	ctx context.Context
	rt  Runtime
}

var _ starlark.HasAttrs = (*SOTaskCtx)(nil)

func (c *SOTaskCtx) Type() string          { return "ctx" }
func (c *SOTaskCtx) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: ctx") }
func (c *SOTaskCtx) Truth() starlark.Bool  { return true }
func (c *SOTaskCtx) String() string        { return "ctx" }

func (c *SOTaskCtx) Freeze() {}

func (c *SOTaskCtx) AttrNames() []string { return []string{"shell"} }
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

	res, err := c.rt.Exec(c.ctx, Command{
		Args:   []string{"bash", "-c", cmd},
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("shell: %q exited with code %d", cmd, res.Code)
	}

	return starlark.None, nil
}
