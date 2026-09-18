// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/connectors/grok"
)

// newGrokCmd authors Grok Build system-tier requirements.toml — the Grok sibling
// of `olivares codex managed-config` and `olivares agent managed-settings`.
func newGrokCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grok",
		Short: "Author Grok Build governance artifacts (managed requirements)",
		Long: "grok renders the system-tier requirements.toml that Grok Build clamps as its\n" +
			"highest configuration layer (sandbox profile and MCP allowlist). It writes a file;\n" +
			"it does not talk to a control plane.\n\n" +
			"Can-enforce: sandbox profile and MCP server names in /etc/grok/requirements.toml.\n" +
			"Can-only-observe: ~/.grok/disabled-hooks (a user can disable a managed hook by name).\n" +
			"This command does not write disabled-hooks and does not claim authentication.",
		Example: "  olivares grok managed-config --policy policy.json --validate",
	}
	cmd.AddCommand(newGrokManagedConfigCmd())
	return cmd
}

func newGrokManagedConfigCmd() *cobra.Command {
	var policyPath, requirementsOut string
	var validateOnly bool
	cmd := &cobra.Command{
		Use:   "managed-config",
		Short: "Render /etc/grok/requirements.toml from a governance Policy JSON",
		Long: "managed-config renders Grok Build requirements.toml from {\"sandbox_profile\": \"strict\",\n" +
			"\"allowed_mcp_servers\": []}. An empty allowed_mcp_servers array is a lockdown (no MCP\n" +
			"servers). Omit the key to leave MCP unauthored. Unknown sandbox profiles fail closed.",
		Example: `  olivares grok managed-config --policy policy.json --validate
  olivares grok managed-config --policy policy.json --requirements-out /etc/grok/requirements.toml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := readPolicyInput(cmd, policyPath)
			if err != nil {
				return err
			}
			var pol grok.ManagedPolicy
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&pol); err != nil {
				return fmt.Errorf("invalid policy JSON: %w", err)
			}
			reqTOML, err := grok.RenderRequirements(pol)
			if err != nil {
				return fmt.Errorf("render requirements.toml: %w", err)
			}
			if validateOnly {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "ok: policy renders to valid Grok requirements.toml")
				return nil
			}
			return emitTOML(cmd, "requirements.toml", reqTOML, requirementsOut)
		},
	}
	cmd.Flags().StringVar(&policyPath, "policy", "-", "path to the governance Policy JSON ('-' = stdin)")
	cmd.Flags().StringVar(&requirementsOut, "requirements-out", "-", "output path for requirements.toml ('-' = stdout)")
	cmd.Flags().BoolVar(&validateOnly, "validate", false, "validate the policy renders to valid TOML, but write nothing")
	return cmd
}
