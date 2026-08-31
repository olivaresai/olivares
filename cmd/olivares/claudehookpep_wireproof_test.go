// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
)

func TestValidateHookPolicyAbsolutePathPatterns(t *testing.T) {
	// buildClaudeHookPEPServer calls validateHookPolicy before inserting into the
	// tenant map; an error leaves the tenant unmounted and resolveTenant denies closed.
	tests := []struct {
		name    string
		pol     hookPolicyDoc
		wantErr bool
	}{
		{
			name: "relative path glob rejected",
			pol: hookPolicyDoc{Rules: []hookPolicyRule{{
				Tool:     "Read",
				Paths:    []string{"repo/**"},
				Decision: claude.DecisionDeny,
			}}},
			wantErr: true,
		},
		{
			name: "relative subtree rejected",
			pol: hookPolicyDoc{Rules: []hookPolicyRule{{
				Tool:     "Read",
				Subtree:  "Finance",
				Decision: claude.DecisionDeny,
			}}},
			wantErr: true,
		},
		{
			name: "absolute path glob accepted",
			pol: hookPolicyDoc{Rules: []hookPolicyRule{{
				Tool:     "Read",
				Paths:    []string{"/etc/**"},
				Decision: claude.DecisionDeny,
			}}},
		},
		{
			name: "absolute subtree accepted",
			pol: hookPolicyDoc{Rules: []hookPolicyRule{{
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

func TestHookPEPWireProofPathAndBashPolicies(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-wireproof@e2e.test")
	pol := hookPolicyDoc{
		Default: claude.DecisionAllow,
		Rules: []hookPolicyRule{
			{
				ResourceKind: hookResourceKindFile,
				Subtree:      "/srv/acme/Finance",
				Decision:     claude.DecisionDeny,
			},
			{
				Tool:     "Bash",
				Paths:    []string{"/etc/secrets/**"},
				Decision: claude.DecisionDeny,
			},
		},
	}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, true)

	tests := []struct {
		name  string
		tool  string
		input map[string]any
		want  string
	}{
		{
			name:  "read denied under finance subtree",
			tool:  "Read",
			input: map[string]any{"file_path": "/srv/acme/Finance/q3.xlsx"},
			want:  claude.DecisionDeny,
		},
		{
			name:  "read allowed outside finance subtree",
			tool:  "Read",
			input: map[string]any{"file_path": "/srv/acme/Public/x"},
			want:  claude.DecisionAllow,
		},
		{
			name:  "write denied under finance subtree",
			tool:  "Write",
			input: map[string]any{"file_path": "/srv/acme/Finance/new.txt"},
			want:  claude.DecisionDeny,
		},
		{
			name:  "bash denied on absolute secret path",
			tool:  "Bash",
			input: map[string]any{"command": "cat /etc/secrets/db.pem"},
			want:  claude.DecisionDeny,
		},
		{
			name:  "bash asks on unresolved relative traversal",
			tool:  "Bash",
			input: map[string]any{"command": "cat ../../etc/secrets/db.pem"},
			want:  claude.DecisionAsk,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := f.call(t, tc.tool, tc.input, tok, h.tenantA)
			if got := decisionOf(out); got != tc.want {
				t.Fatalf("decision = %q, want %q (%v)", got, tc.want, out)
			}
		})
	}
}

func BenchmarkHookDecidePathHot(b *testing.B) {
	// The hot path keeps policy in memory and does not read store, network, or files.
	pol := hookPolicyDoc{
		Default:        claude.DecisionAllow,
		PathPrecedence: "deny-overrides",
		Rules: []hookPolicyRule{
			{Tool: "Read", ResourceKind: hookResourceKindFile, Paths: []string{"/srv/acme/**"}, Decision: claude.DecisionAllow},
			{Tool: "Read", ResourceKind: hookResourceKindFile, Subtree: "/srv/acme/Finance", Decision: claude.DecisionDeny},
			{Tool: "Write", ResourceKind: hookResourceKindFile, Paths: []string{"/srv/acme/Finance/**"}, Decision: claude.DecisionDeny},
		},
	}
	in := claude.HookDecisionInput{
		Event:        "PreToolUse",
		Tool:         "Read",
		ResourceKind: hookResourceKindFile,
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
