//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "github.com/spf13/cobra"

// Preserve published command names and flags as a hidden Business seam.
func governanceBreakGlassCmd(_ *authClientFlags) *cobra.Command {
	requiresBusiness := func(*cobra.Command, []string) error { return notInEdition() }
	root := &cobra.Command{Use: "breakglass", Hidden: true, RunE: requiresBusiness}
	list := &cobra.Command{Use: "ls", Aliases: []string{"list"}, Args: cobra.NoArgs, RunE: requiresBusiness}
	list.Flags().String("status", "", "filter by status")
	addObservePageFlags(list, &observePageFlags{})
	root.AddCommand(list,
		&cobra.Command{Use: "get <grant-id>", Args: cobra.ExactArgs(1), RunE: requiresBusiness},
		&cobra.Command{Use: "uses <grant-id>", Args: cobra.ExactArgs(1), RunE: requiresBusiness})
	return root
}
