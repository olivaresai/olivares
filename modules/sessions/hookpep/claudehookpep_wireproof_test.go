// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
)

func TestValidateHookPolicyAbsolutePathPatterns(t *testing.T) {
	// buildClaudeHookPEPServer calls validateHookPolicy before inserting into the
	// tenant map; an error leaves the tenant unmounted and resolveTenant denies closed.
	tests := []struct {
		name    string
		pol     PolicyDoc
		wantErr bool
	}{
		{
			name: "relative path glob rejected",
			pol: PolicyDoc{Rules: []PolicyRule{{
				Tool:     "Read",
				Paths:    []string{"repo/**"},
				Decision: claude.DecisionDeny,
			}}},
			wantErr: true,
		},
		{
			name: "relative subtree rejected",
			pol: PolicyDoc{Rules: []PolicyRule{{
				Tool:     "Read",
				Subtree:  "Finance",
				Decision: claude.DecisionDeny,
			}}},
			wantErr: true,
		},
		{
			name: "absolute path glob accepted",
			pol: PolicyDoc{Rules: []PolicyRule{{
				Tool:     "Read",
				Paths:    []string{"/etc/**"},
				Decision: claude.DecisionDeny,
			}}},
		},
		{
			name: "absolute subtree accepted",
			pol: PolicyDoc{Rules: []PolicyRule{{
				Tool:     "Read",
				Subtree:  "/srv/x",
				Decision: claude.DecisionDeny,
			}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHookPolicy(tc.pol)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateHookPolicy() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func BenchmarkHookDecidePathHot(b *testing.B) {
	// The hot path keeps policy in memory and does not read store, network, or files.
	pol := PolicyDoc{
		Default:        claude.DecisionAllow,
		PathPrecedence: "deny-overrides",
		Rules: []PolicyRule{
			{Tool: "Read", ResourceKind: ResourceKindFile, Paths: []string{"/srv/acme/**"}, Decision: claude.DecisionAllow},
			{Tool: "Read", ResourceKind: ResourceKindFile, Subtree: "/srv/acme/Finance", Decision: claude.DecisionDeny},
			{Tool: "Write", ResourceKind: ResourceKindFile, Paths: []string{"/srv/acme/Finance/**"}, Decision: claude.DecisionDeny},
		},
	}
	in := claude.HookDecisionInput{
		Event:        "PreToolUse",
		Tool:         "Read",
		ResourceKind: ResourceKindFile,
		ResourceRef:  "/srv/acme/Finance/q3.xlsx",
		Mode:         "read",
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		disp, matched := evalHookPolicy(pol, in)
		if !matched || disp.decision != claude.DecisionDeny {
			b.Fatalf("unexpected hot-path decision: matched=%v disp=%+v", matched, disp)
		}
	}
}
