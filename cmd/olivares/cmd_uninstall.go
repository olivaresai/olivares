// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/localinstall"
)

func newUninstallCmd() *cobra.Command {
	var dataDir, manifestPath, offlineRoot string
	var plan, preserve, purge, yes bool
	cmd := &cobra.Command{
		Use:   "uninstall (--plan|--preserve|--purge)",
		Short: "Plan or remove a local installation without crossing its signed-release layout",
		Long: "uninstall consumes the local ownership manifest and validates every listed path against\n" +
			"the install_layout published in the release distribution index before touching anything.\n" +
			"--plan only reports. --preserve removes managed software and service files while retaining\n" +
			"configuration, data, logs, keys and their service identity. --purge removes those retained\n" +
			"paths too and requires an explicit confirmation. An unexpected path refuses the whole plan\n" +
			"with exit code 2 before the first mutation.",
		Example: "  olivares uninstall --plan --data-dir /var/lib/olivares\n" +
			"  olivares uninstall --preserve --data-dir /var/lib/olivares\n" +
			"  olivares uninstall --purge --data-dir /var/lib/olivares --yes",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selected := 0
			var op localinstall.Operation
			if plan {
				selected++
				op = localinstall.Plan
			}
			if preserve {
				selected++
				op = localinstall.Preserve
			}
			if purge {
				selected++
				op = localinstall.Purge
			}
			if selected != 1 {
				return exitcode.New(exitcode.Usage, fmt.Errorf("choose exactly one of --plan, --preserve or --purge"))
			}
			logicalDataDir := dataDir
			if logicalDataDir == "" {
				resolved, err := defaultDataDir()
				if err != nil {
					return err
				}
				logicalDataDir = resolved
			}
			if !filepath.IsAbs(logicalDataDir) {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--data-dir must be absolute"))
			}
			logicalManifest := filepath.Join(logicalDataDir, "install-manifest.json")
			manifestOnDisk := logicalManifest
			if manifestPath != "" {
				manifestOnDisk = manifestPath
			} else if offlineRoot != "" {
				manifestOnDisk = filepath.Join(offlineRoot, logicalManifest[1:])
			}
			m, err := localinstall.Load(manifestOnDisk, offlineRoot == "")
			if err != nil {
				return exitcode.New(exitcode.Usage, err)
			}
			if m.DataDir != logicalDataDir || m.ManifestPath != logicalManifest {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"manifest owns data directory %q, not requested %q", m.DataDir, logicalDataDir))
			}
			if purge {
				if err := confirmDestructive(cmd, yes, "purge the Olivares installation, data, logs and keys at "+m.DataDir); err != nil {
					return err
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Olivares AI uninstall %s\n", op)
			if err := localinstall.Execute(m, localinstall.Options{
				Operation: op, Root: offlineRoot, Out: cmd.OutOrStdout(),
			}); err != nil {
				return err
			}
			switch op {
			case localinstall.Plan:
				fmt.Fprintln(cmd.OutOrStdout(), "result: plan only; no filesystem, account or init mutation")
			case localinstall.Preserve:
				fmt.Fprintf(cmd.OutOrStdout(), "result: removed managed software; retained config, data, logs and keys under %s\n", m.DataDir)
			case localinstall.Purge:
				fmt.Fprintf(cmd.OutOrStdout(), "result: purged the allowlisted installation rooted at %s\n", m.DataDir)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "logical data directory containing install-manifest.json")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "read the ownership manifest from this path (must name the requested data dir)")
	cmd.Flags().BoolVar(&plan, "plan", false, "print every remove/keep action without mutation")
	cmd.Flags().BoolVar(&preserve, "preserve", false, "remove managed software and service files; retain data, config, logs and keys")
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove allowlisted data, config, logs and keys (requires confirmation)")
	addYesFlag(cmd, &yes)
	cmd.Flags().StringVar(&offlineRoot, "root", "", "prefix logical paths with an offline staging root (no host init/account mutation)")
	_ = cmd.Flags().MarkHidden("root")
	return cmd
}
