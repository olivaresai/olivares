// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestPrincipalWithAgentIdentity(t *testing.T) {
	p := ScopedPrincipal(model.ID("tok-1"), "test", model.TenantID("t1"), "viewer")
	if p.AgentIdentity != "" {
		t.Fatal("expected empty AgentIdentity on a fresh ScopedPrincipal")
	}
	p2 := p.WithAgentIdentity("agent-ext-42")
	if p2.AgentIdentity != "agent-ext-42" {
		t.Fatalf("expected AgentIdentity agent-ext-42, got %q", p2.AgentIdentity)
	}
	// Original unchanged (value semantics).
	if p.AgentIdentity != "" {
		t.Fatal("original principal mutated")
	}
}
