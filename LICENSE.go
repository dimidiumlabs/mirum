// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !licensegen

// This package is a small hack for embedding LICENSE into a project.
// Go sensibly prohibits embedding files from parent folders,
// so this file is only needed to bypass the restriction.
package mirum

import _ "embed"

//go:embed LICENSE
var License string

//go:embed build/licenses.json
var Licenses []byte
