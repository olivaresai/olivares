//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "github.com/spf13/cobra"

// governanceApprovalsReviewQualityCmd is the Business hook for
// `governance approvals review-quality` (approval rate, time to decision and
// rubber-stamp flags). Community has no such command, so it returns nil.
func governanceApprovalsReviewQualityCmd(*authClientFlags) *cobra.Command { return nil }
