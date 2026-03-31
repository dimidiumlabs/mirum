// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"fmt"
	"strconv"
	"strings"

	"mrdimidium/mirum/internal/protocol/pb"
)

// Set via -ldflags "-X mrdimidium/mirum/internal/protocol.raw=0.1.0"
var raw string

var (
	Major uint32
	Minor uint32
	Patch uint32
)

func init() {
	parts := strings.SplitN(strings.TrimSpace(raw), ".", 3)

	if len(parts) == 3 {
		Major = parseUint(parts[0])
		Minor = parseUint(parts[1])
		Patch = parseUint(parts[2])
	}
}

func VersionString() string {
	return fmt.Sprintf("%d.%d.%d", Major, Minor, Patch)
}

func VersionProto() *pb.Version {
	return &pb.Version{Major: Major, Minor: Minor, Patch: Patch}
}

func parseUint(s string) uint32 {
	n, _ := strconv.ParseUint(s, 10, 32)
	return uint32(n)
}
