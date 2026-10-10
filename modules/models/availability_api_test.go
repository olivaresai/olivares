// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
)

type apiModelDiscovery struct {
	tenant model.TenantID
	calls  int
}

func (f *apiModelDiscovery) Sources(_ context.Context, tenant model.TenantID) ([]models.AvailabilitySource, error) {
	if tenant != f.tenant {
		return nil, nil
	}
	return []models.AvailabilitySource{{Ref: "prv_fixture", ProviderRef: "prv_fixture", ProviderKind: "ollama", Revision: "private-config-digest"}}, nil
}
func (f *apiModelDiscovery) Discover(context.Context, model.TenantID, models.AvailabilitySource) ([]string, error) {
	f.calls++
	return []string{"qwen3:8b"}, nil
}

func TestAvailableModelsAPIAuthIsolationAndReadOnly(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "models-one")
	other := h.createOrg(admin, "models-two")
	f := &apiModelDiscovery{tenant: tenant}
	m.UseAvailabilitySource(f, func(context.Context) ([]model.TenantID, error) { return nil, nil })
	_ = m.Start(context.Background())
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	if r := h.do("GET", "/v1/m/models/availability", "", nil, nil); r.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d %s", r.code, r.raw)
	}
	viewer := h.roleToken(admin, tenant, "reader@x.io", "viewer")
	first := h.do("GET", "/v1/m/models/availability", viewer, nil, nil)
	if first.code != http.StatusOK || !strings.Contains(first.raw, `"state":"fresh"`) || !strings.Contains(first.raw, `"id":"qwen3:8b"`) || strings.Contains(first.raw, "private-config-digest") {
		t.Fatalf("availability: %d %s", first.code, first.raw)
	}
	if r := h.do("GET", "/v1/m/models/availability?provider_ref=prv_other", viewer, nil, nil); r.code != 200 || !strings.Contains(r.raw, `"items":[]`) {
		t.Fatalf("filter: %d %s", r.code, r.raw)
	}
	outsider := h.roleToken(admin, other, "outside@x.io", "viewer")
	if r := h.do("GET", "/v1/m/models/availability", outsider, nil, nil); r.code != 200 || !strings.Contains(r.raw, `"items":[]`) {
		t.Fatalf("tenant leak: %d %s", r.code, r.raw)
	}
	if f.calls != 1 {
		t.Fatal("GET performed provider I/O")
	}
	reads := 0
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error {
			if e.Action == "models.availability.read" {
				reads++
				if e.ActorKind != "user" || e.Actor == "system" {
					t.Errorf("read audit lost its caller: %s/%s", e.ActorKind, e.Actor)
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Fatalf("catalog reads were not audited: got %d, want 2", reads)
	}
}
