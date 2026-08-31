// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Release-artifact truth diagnostics: two hidden subcommands that let the
// release smoke (scripts/release-smoke.sh) interrogate the FINAL artifact instead
// of trusting the pipeline that was supposed to have built it.
//
//   - `commands` prints the full cobra command tree, sorted, one command path per
//     line — a stable snapshot to diff against a binary built from the same
//     source, so a stale or divergent packaged binary is caught before a tag.
//   - `firstparty-bins` lists the first-party connector plugin binaries embedded
//     in THIS build and proves each one through the real firstparty.Extract path
//     (the same call `serve` uses to launch a plugin source). --require makes an
//     absent plugin a hard failure — the assertion the release smoke runs.

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/firstparty"
)

// newCommandsCmd is a hidden diagnostic that prints the binary's full command
// tree. It exists so the release smoke can compare the PACKAGED artifact's tree
// against newRootCmd() at the same commit (a --help walk is neither stable nor
// machine-diffable; this is).
func newCommandsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "commands",
		Short: "Print the full command tree of this binary (diagnostic)",
		Long: "commands is a hidden diagnostic that prints every command path registered in this\n" +
			"binary (including hidden ones), sorted and one per line. The release smoke diffs this\n" +
			"output between the packaged artifact and a source build to detect a stale binary.",
		Example: "  olivares commands",
		Hidden:  true,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths := commandPaths(cmd.Root())
			sort.Strings(paths)
			for _, p := range paths {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), p); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// commandPaths walks the cobra tree rooted at c and returns every command path,
// hidden commands included — the tree must be compared whole, not just the part
// `--help` happens to show.
func commandPaths(c *cobra.Command) []string {
	out := []string{c.CommandPath()}
	for _, sub := range c.Commands() {
		out = append(out, commandPaths(sub)...)
	}
	return out
}

// newFirstPartyBinsCmd is a hidden diagnostic that lists the embedded
// first-party connector plugin binaries and PROVES each through
// firstparty.Extract — the exact mechanism `serve` uses — so "listed" always
// means "launchable", never just "present in a manifest".
func newFirstPartyBinsCmd() *cobra.Command {
	var require []string
	cmd := &cobra.Command{
		Use:   "firstparty-bins",
		Short: "List the first-party connector plugins embedded in this binary (diagnostic)",
		Long: "firstparty-bins is a hidden diagnostic that lists every first-party connector plugin\n" +
			"binary embedded in this build and extract-verifies each one through the same\n" +
			"firstparty.Extract path serve uses. A plain dev build honestly reports zero\n" +
			"(populate with `task build:connectors`); --require turns missing plugins into a hard\n" +
			"failure, which is what the release smoke asserts against the final artifact.",
		Example: "  olivares firstparty-bins\n" +
			"  olivares firstparty-bins --require claude-source,kafka-source",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			// A private scratch dir: extraction (write+chmod, never exec) is the
			// proof; the dir is removed before returning.
			dir, err := os.MkdirTemp("", "olivares-firstparty-diag-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(dir)
			embedded := firstparty.Available()
			for _, name := range embedded {
				path, xerr := firstparty.Extract(dir, name)
				if xerr != nil {
					return fmt.Errorf("embedded plugin %q failed to extract: %w", name, xerr)
				}
				info, serr := os.Stat(path)
				if serr != nil {
					return serr
				}
				if _, werr := fmt.Fprintf(out, "%s\t%d bytes\n", name, info.Size()); werr != nil {
					return werr
				}
			}
			if _, err := fmt.Fprintf(out, "%d embedded connector plugin(s)\n", len(embedded)); err != nil {
				return err
			}
			var missing []string
			for _, want := range require {
				found := false
				for _, name := range embedded {
					if name == want {
						found = true
						break
					}
				}
				if !found {
					missing = append(missing, want)
				}
			}
			if len(missing) > 0 {
				return fmt.Errorf("required connector plugin(s) not embedded in this build: %s (build them with `task build:connectors`)",
					strings.Join(missing, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&require, "require", nil,
		"comma-separated plugin binary names that MUST be embedded (exit non-zero otherwise)")
	return cmd
}
