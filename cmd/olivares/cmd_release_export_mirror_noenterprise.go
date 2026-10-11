//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/spf13/cobra"
	"time"
)

// Retain the published command and flags so callers receive an edition refusal.
func newReleaseExportMirrorCmd() *cobra.Command {
	c := &cobra.Command{Use: "export-mirror", Short: "Export an offline mirror (requires Enterprise)", Hidden: true, Args: cobra.NoArgs,
		Long: "export-mirror walks the licensed /download gate with your token and writes an air-gap\n" +
			"bundle that `olivares upgrade --bundle` installs with no network at all.\n\n" +
			"Exporting an offline mirror requires Enterprise; this build refuses with exit code 9.",
		Example: "  olivares release export-mirror --token $TOKEN --set biz+reg --out ./mirror",
		RunE: func(*cobra.Command, []string) error {
			return sentence(exitcode.Edition, "Offline mirror export requires Enterprise: %s", pricingURL)
		}}
	f := c.Flags()
	for _, flag := range []string{"endpoint", "token", "set", "out", "pubkey"} {
		f.String(flag, "", flag)
	}
	f.String("channel", "stable", "release channel")
	f.StringSlice("platform", nil, "os/arch to mirror; repeatable")
	f.Duration("timeout", 10*time.Minute, "request timeout")
	f.Bool("force", false, "replace output")
	return c
}
