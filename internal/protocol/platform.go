// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	mirum "dimidiumlabs/mirum"
	"dimidiumlabs/mirum/internal/protocol/wirepb"
)

var ErrInvalidVersion = errors.New("invalid version string")

var (
	Major uint32
	Minor uint32
	Patch uint32
)

var osMap = map[string]wirepb.Os{
	"linux":     wirepb.Os_OS_LINUX,
	"darwin":    wirepb.Os_OS_DARWIN,
	"windows":   wirepb.Os_OS_WINDOWS,
	"freebsd":   wirepb.Os_OS_FREEBSD,
	"openbsd":   wirepb.Os_OS_OPENBSD,
	"netbsd":    wirepb.Os_OS_NETBSD,
	"dragonfly": wirepb.Os_OS_DRAGONFLY,
	"illumos":   wirepb.Os_OS_ILLUMOS,
	"solaris":   wirepb.Os_OS_SOLARIS,
	"aix":       wirepb.Os_OS_AIX,
	"plan9":     wirepb.Os_OS_PLAN9,
	"android":   wirepb.Os_OS_ANDROID,
	"ios":       wirepb.Os_OS_IOS,
	"js":        wirepb.Os_OS_JS,
	"wasip1":    wirepb.Os_OS_WASIP1,
}

var archMap = map[string]wirepb.Arch{
	"amd64":    wirepb.Arch_ARCH_AMD64,
	"arm64":    wirepb.Arch_ARCH_ARM64,
	"386":      wirepb.Arch_ARCH_386,
	"arm":      wirepb.Arch_ARCH_ARM,
	"riscv64":  wirepb.Arch_ARCH_RISCV64,
	"ppc64le":  wirepb.Arch_ARCH_PPC64LE,
	"ppc64":    wirepb.Arch_ARCH_PPC64,
	"s390x":    wirepb.Arch_ARCH_S390X,
	"mips64le": wirepb.Arch_ARCH_MIPS64LE,
	"mips64":   wirepb.Arch_ARCH_MIPS64,
	"mipsle":   wirepb.Arch_ARCH_MIPSLE,
	"mips":     wirepb.Arch_ARCH_MIPS,
	"loong64":  wirepb.Arch_ARCH_LOONG64,
	"wasm":     wirepb.Arch_ARCH_WASM,
}

func DetectOs() wirepb.Os {
	if v, ok := osMap[runtime.GOOS]; ok {
		return v
	}
	return wirepb.Os_OS_UNSPECIFIED
}

func DetectArch() wirepb.Arch {
	if v, ok := archMap[runtime.GOARCH]; ok {
		return v
	}
	return wirepb.Arch_ARCH_UNSPECIFIED
}

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

func VersionProto() *wirepb.Version {
	return &wirepb.Version{Major: Major, Minor: Minor, Patch: Patch}
}
