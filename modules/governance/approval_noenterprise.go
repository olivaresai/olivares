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

// approvalPolicyAvailable is the Community line: a policy that asks for one review
// of the matched action. Changing the risk tier or the approval count from the
// action's default, in either direction, or deciding by what a change touches,
// is Business. An explicit tier or quorum equal to the default changes nothing.
func approvalPolicyAvailable(spec approvalSpec) bool {
	if spec.usesChangeClass() {
		return false
	}
	def := defaultActionRiskTier(spec.Match.Action)
	// The empty action matches every action, so no one default describes it.
	if spec.RiskTier != "" && (spec.Match.Action == "" || tierRank(spec.RiskTier) != tierRank(string(def))) {
		return false
	}
	return int64(spec.RequiredApprovals) <= floorRequiredApprovals(1, def)
}
