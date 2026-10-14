// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package main

import (
	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/spf13/cobra"
)

// Keep the published invocation and alias so scripts receive the edition refusal.
// Community links no organization-management verbs or HTTP client implementation.
func newTenantsCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "tenants",
		Aliases:            []string{"orgs"},
		Hidden:             true,
		Short:              "Organization management requires Enterprise",
		Long:               "Community keeps the organization created by setup. Managing several organizations requires Enterprise.",
		Example:            "  olivares tenants # exits 9 in Community",
		DisableFlagParsing: true,
		RunE: func(*cobra.Command, []string) error {
			return sentence(exitcode.Edition, "Managing several organizations requires Enterprise: %s", pricingURL)
		},
	}
}
