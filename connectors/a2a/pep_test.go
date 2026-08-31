// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import "testing"

// TestAllowlistDenyByDefault asserts an empty/nil allowlist denies everything.
func TestAllowlistDenyByDefault(t *testing.T) {
	if (*Allowlist)(nil).Allowed("a", "s", "sc") {
		t.Error("nil allowlist must deny")
	}
	if NewAllowlist(nil).Allowed("a", "s", "sc") {
		t.Error("empty allowlist must deny")
	}
}

// TestAllowlistMatching exercises agent (exact), skill (exact + "*"), and scope rules.
func TestAllowlistMatching(t *testing.T) {
	al := NewAllowlist([]AllowRule{
		{Agent: "billing-agent", Skill: "summarize", Scopes: []string{"reports:read"}},
		{Agent: "ops-agent", Skill: "*", Scopes: []string{"*"}},
		{Agent: "scopeless-agent", Skill: "ping", Scopes: nil},
	})
	cases := []struct {
		agent, skill, scope string
		want                bool
	}{
		{"billing-agent", "summarize", "reports:read", true},   // exact match
		{"billing-agent", "summarize", "reports:write", false}, // scope not granted
		{"billing-agent", "translate", "reports:read", false},  // skill not granted
		{"other-agent", "summarize", "reports:read", false},    // agent not listed (no wildcard on agent)
		{"ops-agent", "anything", "any-scope", true},           // skill "*" + scope "*"
		{"scopeless-agent", "ping", "", true},                  // empty scope-set grants scopeless
		{"scopeless-agent", "ping", "elevated", false},         // empty scope-set never grants a real scope
	}
	for _, c := range cases {
		if got := al.Allowed(c.agent, c.skill, c.scope); got != c.want {
			t.Errorf("Allowed(%q,%q,%q) = %v, want %v", c.agent, c.skill, c.scope, got, c.want)
		}
	}
}

// TestPlanHashStableAndSensitive asserts the plan hash is deterministic for the same
// tuple and changes for ANY field change (anti-TOCTOU: a re-target/re-scope voids it).
func TestPlanHashStableAndSensitive(t *testing.T) {
	base := PlanHash("agent", "skill", "scope", "ph")
	if base == "" {
		t.Fatal("plan hash must be non-empty")
	}
	if base != PlanHash("agent", "skill", "scope", "ph") {
		t.Error("plan hash must be deterministic for the same tuple")
	}
	for _, alt := range [][4]string{
		{"AGENT2", "skill", "scope", "ph"},
		{"agent", "skill2", "scope", "ph"},
		{"agent", "skill", "scope2", "ph"},
		{"agent", "skill", "scope", "ph2"},
	} {
		if PlanHash(alt[0], alt[1], alt[2], alt[3]) == base {
			t.Errorf("plan hash must change when the tuple changes: %v", alt)
		}
	}
	// Length-prefixing must prevent a boundary-shift collision (agent|skill vs agentskill|).
	if PlanHash("ab", "c", "", "") == PlanHash("a", "bc", "", "") {
		t.Error("length-prefixing must prevent a separator-shift collision")
	}
}
