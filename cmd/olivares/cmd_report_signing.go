// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"

	"github.com/spf13/cobra"
)

func newReportingSigningCmd(flags *authClientFlags) *cobra.Command {
	root := &cobra.Command{
		Use: "signing", Short: "Manage evidence bundle signing in the engine",
		Long:    "signing manages the deployment's reporting key through your authenticated engine context. A system administrator can enable or disable signing without a restart. The engine creates and encrypts the private key; status shows only the public key. Existing startup file configuration is a legacy fallback until a product setting is recorded.",
		Example: "  olivares reporting signing status\n  olivares reporting signing enable\n  olivares reporting signing disable",
	}
	for _, entry := range []struct{ verb, short, long string }{
		{"enable", "Turn on evidence bundle signing", "Enable creates a dedicated reporting key in the engine when needed, stores it encrypted, and applies signing immediately without a restart. It reuses an existing managed key."},
		{"status", "Show evidence bundle signing and its public key", "Status reads the engine's reporting signing setting and public verification key. It does not change the key or return private key material."},
		{"disable", "Turn off evidence bundle signing", "Disable applies immediately without a restart. It preserves the managed key for re-enable and verification of earlier bundles."},
	} {
		verb := entry.verb
		cmd := &cobra.Command{
			Use: verb, Short: entry.short, Args: cobra.NoArgs,
			Long:    entry.long,
			Example: "  olivares reporting signing " + verb,
			RunE: func(cmd *cobra.Command, _ []string) error {
				method := http.MethodGet
				var input any
				if verb != "status" {
					method = http.MethodPut
					input = map[string]bool{"enabled": verb == "enable"}
				}
				raw, err := (bootstrapClient{flags: flags, surface: "reporting signing"}).expect(cmd, method, "/v1/m/reporting/signing", input, http.StatusOK)
				if err != nil {
					return err
				}
				return observeValue(cmd, raw, "")
			},
		}
		root.AddCommand(cmd)
	}
	return root
}
