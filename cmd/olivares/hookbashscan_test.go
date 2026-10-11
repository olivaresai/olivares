// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

func TestHookBashDecideStep4bRestrictsDefaultAllow(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-bash-path@e2e.test")
	pol := hookpep.PolicyDoc{
		Default: "allow",
		Rules: []hookpep.PolicyRule{{
			Tool:     "Bash",
			Paths:    []string{"/etc/secrets/**"},
			Decision: "deny",
			Reason:   "secret subtree",
		}},
	}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, false)
	out := f.call(t, "Bash", map[string]any{"command": "cat /etc/secrets/db.pem"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("Decide step 4b must deny Bash path hit under default allow, got %q (%v)", got, out)
	}
	reason, _ := out["permissionDecisionReason"].(string)
	if strings.Contains(reason, "/etc/secrets/db.pem") {
		t.Fatalf("deny reason must not echo the raw path, got %q", reason)
	}
}
