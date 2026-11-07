// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// newManCmd builds the hidden `__man DIR` command: the native package builder runs
// the release binary with it to write one section 1 page per visible command, from
// the same Short, Long, Example and flags `--help` prints. cobra/doc skips hidden
// commands and help topics, and reads SOURCE_DATE_EPOCH for the page date.
func newManCmd() *cobra.Command {
	return &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "__man DIR",
		Hidden:      true,
		Args:        cobra.ExactArgs(1),
		Short:       "internal: write the man pages of every visible command into DIR",
		Long: "__man is an internal command, not a user command. The package build runs it to\n" +
			"write olivares.1 and one olivares-<command>.1 page per visible command into DIR.",
		Example: "  olivares __man dist/man",
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			root.DisableAutoGenTag = true
			return doc.GenManTree(root, &doc.GenManHeader{Section: "1", Source: "Olivares AI", Manual: "Olivares AI Manual"}, args[0])
		},
	}
}
