// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package policytext_test

import (
	"testing"

	"github.com/olivaresai/olivares/modules/governance/policytext"
)

func TestCedarRenderIsStableUnderRuleOrder(t *testing.T) {
	rules := []policytext.Rule{
		{Subject: `User::"z"`, Actions: []string{"z:write", "a:read"}, When: `when { resource in Workspace::"w" }`},
		{Subject: `Group::"a"`, Actions: []string{"governance:rbac:read", "governance:rbac:admin"}},
	}
	const first = "permit(principal in User::\"z\", action in [Action::\"z:write\", Action::\"a:read\"], resource) when { resource in Workspace::\"w\" };\n"
	const second = "permit(principal in Group::\"a\", action in [Action::\"governance:rbac:read\", Action::\"governance:rbac:admin\"], resource);\n"
	for range 2 {
		if got := policytext.Render(rules); got != first+second {
			t.Fatalf("Render = %q, want %q", got, first+second)
		}
	}
	if got := policytext.Render([]policytext.Rule{rules[1], rules[0]}); got != second+first {
		t.Fatalf("reversed rules = %q, want %q", got, second+first)
	}
}

func TestCedarRenderEmptyRulesAndActions(t *testing.T) {
	if got := policytext.Render(nil); got != "" {
		t.Fatalf("empty rules = %q, want empty text", got)
	}
	const want = "permit(principal in Role::\"viewer\", action in [], resource);\n"
	if got := policytext.Render([]policytext.Rule{{Subject: `Role::"viewer"`}}); got != want {
		t.Fatalf("empty actions = %q, want %q", got, want)
	}
}

func TestCedarStrEscapesQuoteAndBackslash(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", `""`},
		{`a"b\c`, `"a\"b\\c"`},
		{"café\n\t", "\"café\n\t\""},
	}
	for _, tt := range tests {
		if got := policytext.Str(tt.in); got != tt.want {
			t.Errorf("Str(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	const want = "permit(principal in User::\"u\", action in [Action::\"a\\\"b\\\\c\"], resource);\n"
	if got := policytext.Render([]policytext.Rule{{Subject: `User::"u"`, Actions: []string{`a"b\c`}}}); got != want {
		t.Fatalf("escaped action = %q, want %q", got, want)
	}
}

func TestCedarSubjectText(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) string
		want string
	}{
		{"user", policytext.SubjectUser, `User::"a\"b\\c"`},
		{"role", policytext.SubjectRole, `Role::"a\"b\\c"`},
		{"group", policytext.SubjectGroup, `Group::"a\"b\\c"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.fn(`a"b\c`); got != tt.want {
				t.Fatalf("subject = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCedarScopeWhen(t *testing.T) {
	tests := []struct{ tree, want string }{
		{"workspace", `when { resource in Workspace::"a\"b\\c" }`},
		{"agent_group", `when { resource in AgentGroup::"a\"b\\c" }`},
		{"folder", `when { resource in Resource::"a\"b\\c" }`},
		{"tenant", ""},
		{"", ""},
		{"unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.tree, func(t *testing.T) {
			if got := policytext.ScopeWhen(tt.tree, `a"b\c`); got != tt.want {
				t.Fatalf("ScopeWhen = %q, want %q", got, tt.want)
			}
		})
	}
}
