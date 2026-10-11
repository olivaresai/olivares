// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise || !addon_ids

package main

import (
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/spf13/cobra"
	"log/slog"
)

func newOrchestrationCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "orchestration",
		Short:              "Orchestration requires Business Identity & Scale",
		Long:               "Orchestration is a Business Identity & Scale feature.",
		Example:            "  olivares help orchestration",
		Hidden:             true,
		DisableFlagParsing: true,
		// DisableFlagParsing hands --help to RunE as an argument, so cobra never
		// answers it; help is answered here, and every other argument is refused.
		RunE: func(cmd *cobra.Command, args []string) error {
			if completionWantsHelp(args) {
				return cmd.Help()
			}
			return notInEdition()
		},
	}
}

type orchCadencePump struct{}
type orchWorkflowPump struct{}

func newOrchCadencePump(func(string) string, store.Store, *orchestration.Module, *slog.Logger) *orchCadencePump {
	return nil
}
func newOrchWorkflowPump(func(string) string, store.Store, *orchestration.Module, *slog.Logger) *orchWorkflowPump {
	return nil
}
func (*orchCadencePump) register(*runtime.Runtime) error  { return nil }
func (*orchWorkflowPump) register(*runtime.Runtime) error { return nil }
