// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	mirum "dimidiumlabs/mirum"
	"dimidiumlabs/mirum/internal/protocol/pb"
)

var ErrInvalidVersion = errors.New("invalid version string")

var (
	Major uint32
	Minor uint32
	Patch uint32
)

func init() {
	var err error
	Major, Minor, Patch, err = ParseVersion(mirum.Version)
	if err != nil {
		panic(fmt.Sprintf("version: %v", err))
	}
}

// ParseVersion parses a "major.minor.patch" string.
func ParseVersion(s string) (major, minor, patch uint32, err error) {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return 0, 0, 0, ErrInvalidVersion
	}
	n0, err0 := strconv.ParseUint(parts[0], 10, 32)
	n1, err1 := strconv.ParseUint(parts[1], 10, 32)
	n2, err2 := strconv.ParseUint(parts[2], 10, 32)
	if err0 != nil || err1 != nil || err2 != nil {
		return 0, 0, 0, ErrInvalidVersion
	}
	return uint32(n0), uint32(n1), uint32(n2), nil
}

func VersionString() string {
	return fmt.Sprintf("%d.%d.%d", Major, Minor, Patch)
}

func VersionProto() *pb.Version {
	return &pb.Version{Major: Major, Minor: Minor, Patch: Patch}
}
