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
	"dimidiumlabs/mirum/internal/protocol/pb"
)

var ErrInvalidVersion = errors.New("invalid version string")

var (
	Major uint32
	Minor uint32
	Patch uint32
)

var osMap = map[string]pb.Os{
	"linux":     pb.Os_OS_LINUX,
	"darwin":    pb.Os_OS_DARWIN,
	"windows":   pb.Os_OS_WINDOWS,
	"freebsd":   pb.Os_OS_FREEBSD,
	"openbsd":   pb.Os_OS_OPENBSD,
	"netbsd":    pb.Os_OS_NETBSD,
	"dragonfly": pb.Os_OS_DRAGONFLY,
	"illumos":   pb.Os_OS_ILLUMOS,
	"solaris":   pb.Os_OS_SOLARIS,
	"aix":       pb.Os_OS_AIX,
	"plan9":     pb.Os_OS_PLAN9,
	"android":   pb.Os_OS_ANDROID,
	"ios":       pb.Os_OS_IOS,
	"js":        pb.Os_OS_JS,
	"wasip1":    pb.Os_OS_WASIP1,
}

var archMap = map[string]pb.Arch{
	"amd64":    pb.Arch_ARCH_AMD64,
	"arm64":    pb.Arch_ARCH_ARM64,
	"386":      pb.Arch_ARCH_386,
	"arm":      pb.Arch_ARCH_ARM,
	"riscv64":  pb.Arch_ARCH_RISCV64,
	"ppc64le":  pb.Arch_ARCH_PPC64LE,
	"ppc64":    pb.Arch_ARCH_PPC64,
	"s390x":    pb.Arch_ARCH_S390X,
	"mips64le": pb.Arch_ARCH_MIPS64LE,
	"mips64":   pb.Arch_ARCH_MIPS64,
	"mipsle":   pb.Arch_ARCH_MIPSLE,
	"mips":     pb.Arch_ARCH_MIPS,
	"loong64":  pb.Arch_ARCH_LOONG64,
	"wasm":     pb.Arch_ARCH_WASM,
}

func DetectOs() pb.Os {
	if v, ok := osMap[runtime.GOOS]; ok {
		return v
	}
	return pb.Os_OS_UNSPECIFIED
}

func DetectArch() pb.Arch {
	if v, ok := archMap[runtime.GOARCH]; ok {
		return v
	}
	return pb.Arch_ARCH_UNSPECIFIED
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

func VersionProto() *pb.Version {
	return &pb.Version{Major: Major, Minor: Minor, Patch: Patch}
}
