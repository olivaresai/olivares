// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"

	"github.com/spf13/cobra"
)

// Risk verbs reuse the existing API, authority and CLI transport. A classification
// records a governed suggestion; declared intent never proves observed safety.
func newComplianceRiskCmd(flags *authClientFlags) *cobra.Command {
	risk := &cobra.Command{
		Use: "risk", Short: "Classify agents and read the governed risk register",
		Long:    "List recorded risk classifications; use olivares compliance risk classify to record a suggestion.",
		Example: "  olivares compliance risk ls",
	}
	risk.AddCommand(&cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "Read retained risk classifications and their evidence", Args: cobra.NoArgs,
		Long:    "List recorded classifications and their evidence for this tenant; use olivares compliance risk classify to record a suggestion.",
		Example: "  olivares compliance risk ls",
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := agentExecCall{flags: flags, module: "compliance", method: http.MethodGet, path: "/risk"}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no risk classifications recorded", []string{"subject_ref", "tier", "suggested_tier", "state", "signals"})
		},
	})
	var subjectKind, agentID string
	classify := &cobra.Command{
		Use: "classify SUBJECT-REF", Short: "Record a risk suggestion from observed signals and declared intent", Args: cobra.ExactArgs(1),
		Long:    "Record a risk suggestion for one subject; inspect the register with olivares compliance risk ls.",
		Example: "  olivares compliance risk classify AGENT-ID -o json",
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"subject_kind": subjectKind, "subject_ref": args[0]}
			if agentID != "" {
				body["agent_id"] = agentID
			}
			res, err := agentExecCall{flags: flags, module: "compliance", method: http.MethodPost, path: "/risk/classify", body: body}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
	classify.Flags().StringVar(&subjectKind, "subject-kind", "agent", "native subject kind")
	classify.Flags().StringVar(&agentID, "agent-id", "", "explicit native agent ID when the subject reference differs")
	risk.AddCommand(classify)
	return risk
}
