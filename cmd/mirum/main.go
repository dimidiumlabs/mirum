// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command mirum is the developer-facing CLI for the Mirum CI system.
//
// In a future iteration it will host the full set of commands described in
// docs/whitepaper.md (`mirum task`, `mirum run`, `mirum list`, `mirum try`,
// `mirum ssh`, `mirum eval`). The current revision is a scaffold that
// compiles and ships through the existing build pipeline so that subsequent
// changes only have to add subcommand implementations.
package main

import (
	"os"

	"dimidiumlabs/mirum/internal/protocol"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:     "mirum",
		Short:   "Mirum CI client",
		Long:    "Mirum CI client. Reads Mirumfile from the repository root.",
		Version: protocol.VersionString(),
	}
	root.SetVersionTemplate("mirum {{.Version}}\n")

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
