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

func approvalPolicyAvailable(spec approvalSpec) bool {
	if spec.RiskTier == "" || spec.RiskTier == string(RiskTierCritical) {
		return true
	}
	// The empty action matches every action, including the default CRITICAL set.
	if spec.Match.Action == "" {
		return false
	}
	return tierRank(spec.RiskTier) >= tierRank(string(defaultActionRiskTier(spec.Match.Action)))
}
