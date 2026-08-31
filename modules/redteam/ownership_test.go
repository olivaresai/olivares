// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestResolveOwnedAgentIsTenantScoped(t *testing.T) {
	h := newHarness(t, fakeSandbox{})
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")
	agentID := h.seedAgent(tenantA, "agent-owned")

	if err := h.st.View(context.Background(), tenantA, func(sc store.Scope) error {
		id, ok, err := resolveOwnedAgent(context.Background(), sc, "agent-owned")
		if err != nil {
			return err
		}
		if !ok || id != agentID {
			t.Fatalf("resolve external id = %q/%v, want %q/true", id, ok, agentID)
		}
		id, ok, err = resolveOwnedAgent(context.Background(), sc, agentID.String())
		if err != nil {
			return err
		}
		if !ok || id != agentID {
			t.Fatalf("resolve canonical id = %q/%v, want %q/true", id, ok, agentID)
		}
		return nil
	}); err != nil {
		t.Fatalf("tenant A resolve: %v", err)
	}

	if err := h.st.View(context.Background(), tenantB, func(sc store.Scope) error {
		id, ok, err := resolveOwnedAgent(context.Background(), sc, "agent-owned")
		if err != nil {
			return err
		}
		if ok || id != "" {
			t.Fatalf("tenant B resolved tenant A agent = %q/%v", id, ok)
		}
		return nil
	}); err != nil {
		t.Fatalf("tenant B resolve: %v", err)
	}
}
