// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

func TestHookPEPWireProofPathAndBashPolicies(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-wireproof@e2e.test")
	pol := hookpep.PolicyDoc{
		Default: claude.DecisionAllow,
		Rules: []hookpep.PolicyRule{
			{
				ResourceKind: hookpep.ResourceKindFile,
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
