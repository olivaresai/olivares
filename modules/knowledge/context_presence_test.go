// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"net/http"
	"testing"
)

func TestContextPolicyPresenceTracksTenantRows(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	first, second := h.createOrg(admin, "first-context"), h.createOrg(admin, "second-context")
	m := h.module()
	if found, err := m.HasContextPolicies(t.Context(), first); err != nil || found {
		t.Fatalf("new tenant has policy: found=%v err=%v", found, err)
	}
	r := h.do("POST", "/v1/m/knowledge/context-policies", admin, map[string]any{
		"scope_kind": "tenant", "scope_ref": first.String(), "max_tokens": 2048, "strategy": "summarize",
	}, tenantHdr(first))
	if r.code != http.StatusCreated && r.code != http.StatusOK {
		t.Fatalf("author policy: %d %s", r.code, r.raw)
	}
	if found, err := m.HasContextPolicies(t.Context(), first); err != nil || !found {
		t.Fatalf("authored tenant policy absent: found=%v err=%v", found, err)
	}
	if found, err := m.HasContextPolicies(t.Context(), second); err != nil || found {
		t.Fatalf("another tenant inherited policy: found=%v err=%v", found, err)
	}
}

func TestContextPolicyPresenceRefusesUnboundStore(t *testing.T) {
	if found, err := (&Module{}).HasContextPolicies(t.Context(), "tenant"); err == nil || found {
		t.Fatalf("unbound policy store appeared empty: found=%v err=%v", found, err)
	}
}
