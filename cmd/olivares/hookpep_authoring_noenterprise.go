//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "github.com/spf13/cobra"

func newHookPEPPublishCmd(cfg *hookPEPClientConfig) *cobra.Command {
	return newHookPEPPublishForEngines(cfg, []string{"opa"})
}
func newHookPEPRollbackCmd(cfg *hookPEPClientConfig) *cobra.Command {
	return newHookPEPRollbackForEngines(cfg, []string{"opa"})
}
