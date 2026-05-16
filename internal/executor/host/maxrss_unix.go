// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package host

import (
	"os"
	"runtime"
	"syscall"
)

// maxRSS extracts the peak resident set size of a finished process,
// normalized to bytes. POSIX getrusage reports ru_maxrss in kilobytes on
// Linux and the BSDs, but in bytes on macOS.
func maxRSS(ps *os.ProcessState) int64 {
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	rss := int64(ru.Maxrss)
	if runtime.GOOS != "darwin" {
		rss *= 1024
	}
	return rss
}
