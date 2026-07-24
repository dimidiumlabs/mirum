// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"
)

// Platform identifies the OS and architecture of a Runtime, as observed by
// the task itself — the values behind tctx.os and tctx.arch.
type Platform struct {
	OS   string // "linux", "darwin", "windows", ...
	Arch string // "amd64", "arm64", "riscv64", ...
}

// Command is a single process to run inside a Runtime.
type Command struct {
	Args   []string          // argv; Args[0] is the executable
	Dir    string            // working directory, relative to the env root
	Env    map[string]string // variables added to the environment's own
	Stdin  io.Reader         // standard input; nil means none
	Stdout io.Writer         // streamed as produced; nil means discard
	Stderr io.Writer         // streamed as produced; nil means discard
}

// Usage reports the resources a Command consumed. It is collected from
// POSIX rusage — os.ProcessState.SysUsage on the Host runtime, wait4 inside
// the VM for the Qemu runtime — and feeds log output and billing metrics. A
// backend leaves a field zero when it cannot measure it.
type Usage struct {
	Wall      time.Duration // wall-clock time from start to exit
	CPUUser   time.Duration // CPU time spent in user mode
	CPUSystem time.Duration // CPU time spent in kernel mode
	MaxRSS    int64         // peak resident set size, in bytes
}

// Result is the outcome of a Command that ran to completion.
type Result struct {
	Code  int   // process exit code; 0 means success
	Usage Usage // resources the command consumed
}

// Runtime is an isolation backend: the environment in which one task's
// commands run. The executor drives every Runtime identically and is
// unaware of how isolation is achieved. A Runtime hosts one task and is
// then discarded.
type Runtime interface {
	// Platform reports the environment's OS and architecture.
	Platform() Platform

	// Exec runs cmd to completion. The error covers failures to start or
	// communicate with the process; a process that runs and exits non-zero
	// is a successful Exec with a non-zero Result.Code.
	Exec(ctx context.Context, cmd Command) (Result, error)

	// List returns the env-relative paths of the files under dir, so the
	// executor can resolve upload globs and walk a source tree.
	FileList(ctx context.Context, dir string) ([]string, error)

	// Send streams a single file into the environment at the env-relative
	// path, creating parent directories as needed. Bytes flow straight from
	// r — the worker never stages them on its own filesystem.
	FileSend(ctx context.Context, path string, r io.Reader) error

	// Recv streams a single file out of the environment to w. Like Send,
	// nothing is staged on the worker's filesystem.
	FileRecv(ctx context.Context, path string, w io.Writer) error

	// Close discards the environment and releases its resources.
	Close() error
}

// Selection priorities for RuntimeBackend.Priority; NewRuntime prefers higher.
const (
	PriorityHost = 0   // unisolated, runs on the worker itself
	PriorityVM   = 100 // hardware-isolated guest (QEMU)
)

// A RuntimeBackend describes one Runtime implementation. Backends register
// themselves with RegisterRuntime, so the executor never imports the backend
// packages directly.
type RuntimeBackend struct {
	Name     string
	Priority int

	// New builds a fresh Runtime for one task. A nil Runtime with nil error
	// means the backend does not apply on this worker, so NewRuntime falls
	// through to the next.
	New func() (Runtime, error)
}

// runtimeBackends is kept sorted by descending priority.
var runtimeBackends []RuntimeBackend

// RegisterRuntime adds a Runtime backend. Backends call it from an init
// function.
func RegisterRuntime(b RuntimeBackend) {
	runtimeBackends = append(runtimeBackends, b)
	sort.SliceStable(runtimeBackends, func(i, j int) bool {
		return runtimeBackends[i].Priority > runtimeBackends[j].Priority
	})
}

// NewRuntime builds a fresh Runtime for one task, choosing the highest-priority
// backend that applies. The caller must Close the returned Runtime.
func NewRuntime() (Runtime, error) {
	for _, b := range runtimeBackends {
		rt, err := b.New()
		if err != nil {
			return nil, fmt.Errorf("runtime %s: %w", b.Name, err)
		}
		if rt != nil {
			slog.Info("executor: runtime selected", "backend", b.Name)
			return rt, nil
		}
	}
	return nil, errors.New("executor: no applicable runtime backend")
}
