//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

func resolveEditionRiskTier(explicit string, fallback ActionRiskTier) ActionRiskTier {
	tier := ActionRiskTier(explicit)
	if tierRank(string(tier)) < tierRank(string(fallback)) {
		return fallback
	}
	return tier
}

// approvalPolicyAvailable is the Community line (withinCommunityApprovalLine).
func approvalPolicyAvailable(spec approvalSpec) bool { return withinCommunityApprovalLine(spec) }
