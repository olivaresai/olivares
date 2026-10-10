//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "github.com/spf13/cobra"

// Preserve published command names and flags as a hidden Business seam.
func governanceBreakGlassCmd(_ *authClientFlags) *cobra.Command {
	const availability = "Break-glass requires the Business edition. Community returns exit code 9 without contacting the engine."
	requiresBusiness := func(*cobra.Command, []string) error { return notInEdition() }
	root := &cobra.Command{
		Use: "breakglass", Short: "Break-glass grants (requires Business edition)",
		Long: availability, Example: "  olivares governance breakglass --help",
		Hidden: true, RunE: requiresBusiness,
	}
	list := &cobra.Command{
		Use: "ls", Short: "List break-glass grants (requires Business edition)",
		Long: availability, Example: "  olivares governance breakglass ls",
		Aliases: []string{"list"}, Args: cobra.NoArgs, RunE: requiresBusiness,
	}
	list.Flags().String("status", "", "filter by status")
	addObservePageFlags(list, &observePageFlags{})
	root.AddCommand(list,
		&cobra.Command{
			Use: "get <grant-id>", Short: "Show a break-glass grant (requires Business edition)",
			Long: availability, Example: "  olivares governance breakglass get <grant-id>",
			Args: cobra.ExactArgs(1), RunE: requiresBusiness,
		},
		&cobra.Command{
			Use: "uses <grant-id>", Short: "Show actions under a break-glass grant (requires Business edition)",
			Long: availability, Example: "  olivares governance breakglass uses <grant-id>",
			Args: cobra.ExactArgs(1), RunE: requiresBusiness,
		})
	return root
}
