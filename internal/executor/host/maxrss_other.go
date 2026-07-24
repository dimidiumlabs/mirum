// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !unix

package host

import "os"

// maxRSS returns 0 on platforms without POSIX getrusage.
func maxRSS(*os.ProcessState) int64 { return 0 }
