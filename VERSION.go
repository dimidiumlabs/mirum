// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

// This package is a small hack for embedding VERSION into a project.
// Go sensibly prohibits embedding files from parent folders,
// so this file is only needed to bypass the restriction.
package mirum

import _ "embed"

//go:embed VERSION
var Version string
