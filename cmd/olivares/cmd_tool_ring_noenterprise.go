//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "github.com/spf13/cobra"

// toolRingCmd is the Business hook for `tool ring` (a candidate tool version tried on
// one agent group before it reaches every folder). Community has no such command, so
// it returns nil.
func toolRingCmd() *cobra.Command { return nil }
