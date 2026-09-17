// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// LineageEpochKind names the generation of one closed authority relation. Each
// kind has one row per tenant; its ID is the tenant, including for an empty set.
func LineageEpochKind(relation Kind) (Kind, bool) {
	switch relation {
	case "core.session", "core.agent", "core.resource", "core.workspace", "core.agent_group", "core.agent_group_member":
		return relation + "_lineage_epoch", true
	default:
		return "", false
	}
}

func IsLineageEpochKind(kind Kind) bool {
	for _, relation := range []Kind{"core.session", "core.agent", "core.resource", "core.workspace", "core.agent_group", "core.agent_group_member"} {
		epoch, _ := LineageEpochKind(relation)
		if kind == epoch {
			return true
		}
	}
	return false
}
