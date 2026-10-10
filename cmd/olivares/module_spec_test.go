// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"slices"
	"testing"
)

// These are compatibility snapshots, not another source for new module policy.
func TestModuleSpecPreservesPublishedSelectionsAndRetirementOrder(t *testing.T) {
	if got, want := standardModuleSelection(), []string{"capabilities", "claude-policy", "consoleviews", "eventing", "identity", "liveingest"}; !slices.Equal(got, want) {
		t.Fatalf("standard selection = %v, want %v", got, want)
	}
	if got, want := published26100ModuleSelection(), []string{
		"accessmap", "adoption", "capabilities", "catalog", "claude-agents",
		"claude-policy", "compliance", "consoleviews", "deploy", "evals", "eventing",
		"finops", "gitpublish", "governance", "health", "identity", "inferenceproxy",
		"inventory", "knowledge", "liveingest", "models", "notify", "observability",
		"orchestration", "posture", "recording", "redteam", "reporting", "sandbox",
		"security", "sessions", "siemforward", "sourcescope", "voice",
	}; !slices.Equal(got, want) {
		t.Fatalf("26.10.0 selection = %v, want %v", got, want)
	}
	if got, want := communityCensusContribution().Modules, []string{
		"governance", "sourcescope", "models", "sessions", "orchestration", "eventing", "finops", "compliance",
	}; !slices.Equal(got, want) {
		t.Fatalf("retirement order = %v, want %v", got, want)
	}
}
