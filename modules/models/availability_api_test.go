// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
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

// The models of a provider key, a tool account or a tool are listed to whoever may use it
// (#2420): the reference and the model IDs of another department's account, tool or key are
// not shown, and an unreadable assignment fails the answer.
type accountDiscovery struct{ tenant model.TenantID }

func (f accountDiscovery) Sources(_ context.Context, tenant model.TenantID) ([]models.AvailabilitySource, error) {
	if tenant != f.tenant {
		return nil, nil
	}
	return []models.AvailabilitySource{
		{Ref: "prv_fixture", ProviderRef: "prv_fixture", ProviderKind: "ollama", Revision: "1"},
		{Ref: "ppf_engineering", AccountRef: "ppf_engineering", Driver: "claude", Revision: "1"},
		{Ref: "ppf_sales", AccountRef: "ppf_sales", Driver: "claude", Revision: "1"},
		{Ref: "login:codex", Driver: "codex", Revision: "1"}, // a tool's own login: a tool, no account
	}, nil
}
func (accountDiscovery) Discover(context.Context, model.TenantID, models.AvailabilitySource) ([]string, error) {
	return []string{"model-x"}, nil
}

func TestAvailableModelsOfAToolAccountFollowItsAssignment(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "models-assigned")
	m.UseAvailabilitySource(accountDiscovery{tenant: tenant}, func(context.Context) ([]model.TenantID, error) { return nil, nil })
	_ = m.Start(context.Background())
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	if err := m.RefreshAvailability(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	viewer := h.roleToken(admin, tenant, "reader@x.io", "viewer")
	var fail error
	hidden := map[string]bool{"ppf_sales": true}
	asked := map[string]models.AvailabilitySource{}
	m.UseAvailabilityAccess(func(_ context.Context, got model.TenantID, _ auth.Principal, source models.AvailabilitySource) (bool, error) {
		if got != tenant {
			t.Errorf("asked for tenant %q, want %q", got, tenant)
		}
		asked[source.Ref] = source
		return !hidden[source.Ref] && source.Driver != "codex", fail
	})
	r := h.do("GET", "/v1/m/models/availability", viewer, nil, nil)
	if r.code != http.StatusOK || strings.Contains(r.raw, "ppf_sales") || !strings.Contains(r.raw, "ppf_engineering") || !strings.Contains(r.raw, "prv_fixture") {
		t.Fatalf("availability = %d %s, want the assigned account and the provider, not ppf_sales", r.code, r.raw)
	}
	if strings.Contains(r.raw, "codex") {
		t.Fatalf("availability = %s, want the tool this caller was not assigned left out, also with no account", r.raw)
	}
	if len(asked) != 4 || asked["ppf_engineering"].Driver != "claude" || asked["ppf_engineering"].AccountRef != "ppf_engineering" ||
		asked["login:codex"].Driver != "codex" || asked["login:codex"].AccountRef != "" || asked["prv_fixture"].ProviderRef != "prv_fixture" {
		t.Fatalf("asked %+v, want the two accounts with their tool, the tool's own login, and the provider key by its reference", asked)
	}
	// A provider key is a source like the others: one the caller may use no profile on is not listed.
	hidden["prv_fixture"] = true
	if r := h.do("GET", "/v1/m/models/availability", viewer, nil, nil); r.code != http.StatusOK || strings.Contains(r.raw, "prv_fixture") || !strings.Contains(r.raw, "ppf_engineering") {
		t.Fatalf("availability with the provider key hidden = %d %s, want nothing of the key and the assigned account", r.code, r.raw)
	}
	fail = errors.New("store unavailable")
	if r := h.do("GET", "/v1/m/models/availability", viewer, nil, nil); r.code != http.StatusServiceUnavailable || strings.Contains(r.raw, "ppf_") || strings.Contains(r.raw, "prv_") {
		t.Fatalf("unreadable assignment = %d %s, want 503 and no account", r.code, r.raw)
	}
}
