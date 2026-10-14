// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// --- helpers -----------------------------------------------------------------

// seedIdentity inserts a roster identity directly into the store (no connector).
func (h *harness) seedIdentity(tenant model.TenantID, externalID, kind, provider, principalType string, disabled bool) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Identities().Create(context.Background(), model.Identity{
			Name: externalID, Kind: kind, ExternalID: externalID, Provider: provider,
			Metadata: map[string]any{"principal_type": principalType, "disabled": disabled},
		})
		return err
	}); err != nil {
		h.t.Fatalf("seed identity %s: %v", externalID, err)
	}
}

// nhiAdmin sets up a tenant + an admin-tier session token for the NHI endpoints.
func (h *harness) nhiTenant() (model.TenantID, string) {
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, tok := h.roleUser(admin, tenant, "nhiadmin@x.io", "admin")
	return tenant, tok
}

// --- enforcement query (the PEP risk-conditional deny seam) -------------------

func TestNHIEnforcementDefaultsOpenForUnmanaged(t *testing.T) {
	h := newHarness(t)
	tenant, _ := h.nhiTenant()
	// An NHI with no lifecycle row is NOT blocked (day-1 operations keep working).
	blocked, _, err := h.gov.NHIEnforcement(context.Background(), tenant, "unmanaged:thing")
	if err != nil || blocked {
		t.Fatalf("unmanaged NHI must default open, got blocked=%v err=%v", blocked, err)
	}
	// An unknown agent ref likewise defaults open.
	bl, _, err := h.gov.NHIEnforcementForAgentRef(context.Background(), tenant, "no-such-agent")
	if err != nil || bl {
		t.Fatalf("unknown agent must default open, got blocked=%v err=%v", bl, err)
	}
}

// --- test-only store helpers (kept here to avoid touching the shared harness) -

func (h *harness) identityID(tenant model.TenantID, externalID string) model.ID {
	h.t.Helper()
	var id model.ID
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		list, _, err := sc.Identities().List(context.Background(), model.Query{Filters: []model.Filter{{Column: "external_id", Op: model.OpEq, Value: externalID}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(list) > 0 {
			id = list[0].ID
		}
		return nil
	}); err != nil {
		h.t.Fatalf("identityID: %v", err)
	}
	return id
}

func (h *harness) bindAgentIdentity(tenant model.TenantID, agentID, identityID model.ID) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Get(context.Background(), agentID)
		if err != nil {
			return err
		}
		a.IdentityID = identityID
		_, err = sc.Agents().Update(context.Background(), a)
		return err
	}); err != nil {
		h.t.Fatalf("bind agent: %v", err)
	}
}

func (h *harness) setIdentityDisabled(tenant model.TenantID, externalID string, disabled bool) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		list, _, err := sc.Identities().List(context.Background(), model.Query{Filters: []model.Filter{{Column: "external_id", Op: model.OpEq, Value: externalID}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return nil
		}
		id := list[0]
		if id.Metadata == nil {
			id.Metadata = map[string]any{}
		}
		id.Metadata["disabled"] = disabled
		_, err = sc.Identities().Update(context.Background(), id)
		return err
	}); err != nil {
		h.t.Fatalf("set disabled: %v", err)
	}
}

func bodyContains(raw, sub string) bool {
	return len(raw) >= len(sub) && (indexOf(raw, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
