// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/modulespec"
)

// titleOf is the title modules.json gives a namespace: the summary names what the table names.
func titleOf(t *testing.T, namespace string) string {
	t.Helper()
	for _, spec := range modulespec.All() {
		if spec.Namespace == namespace {
			return spec.Title
		}
	}
	t.Fatalf("no module %q in modules.json", namespace)
	return ""
}

// placedStubModule mounts one read route under the namespace of a module the table places
// in a higher edition, so the summary has something to list.
type placedStubModule struct{ ns string }

func (m placedStubModule) APINamespace() string { return m.ns }

func (m placedStubModule) Permissions() []auth.Permission {
	return []auth.Permission{auth.Permission(m.ns + ":thing:read")}
}

func (m placedStubModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/ping", auth.Permission(m.ns+":thing:read"), func(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
		w.WriteHeader(http.StatusNoContent)
	})
}

// businessLicense is the display a valid Business licence gives a Business build.
type businessLicense struct{ stubLicenseService }

func (businessLicense) LicenseDisplay() api.LicenseDisplayInfo {
	return api.LicenseDisplayInfo{Status: "valid", LicensedEdition: "business"}
}

// THE UPGRADE SUMMARY (dev step 8). An administrator whose server holds modules placed in
// a higher edition than the one running reads ONE summary: which of them run, under the edition
// each needs. Nothing is deleted or switched off by it. A module that is off, or one the running
// edition holds, is not listed; a Business licence on the Business build lists nothing.
// The 0.9 data-dir fixture of the step is built by the lab, not here: this test makes the same
// modules run on a fresh store.
func TestEditionSummaryListsMovedModulesThatRun(t *testing.T) {
	modules := []api.Module{
		placedStubModule{"skills"},    // engineer
		placedStubModule{"knowledge"}, // engineer, switched off below
		placedStubModule{"adoption"},  // business
	}
	type need struct{ edition, capability, title string }
	read := func(t *testing.T, h *harness) (string, []need) {
		t.Helper()
		r := h.do("GET", "/v1/console/edition/summary", h.adminLogin(), nil, nil)
		if r.code != http.StatusOK {
			t.Fatalf("GET /v1/console/edition/summary = %d %s", r.code, r.raw)
		}
		var out []need
		for _, group := range r.body["needs"].([]any) {
			g := group.(map[string]any)
			for _, c := range g["capabilities"].([]any) {
				cm := c.(map[string]any)
				out = append(out, need{g["edition"].(string), cm["capability"].(string), cm["title"].(string)})
			}
		}
		return r.body["edition"].(string), out
	}

	t.Run("a Community build lists the moved modules that run, by the edition each needs", func(t *testing.T) {
		h := newHarnessOpts(t, func(o *api.Options) {
			o.Edition = "community"
			o.ModuleEnabled = func(ns string) bool { return ns != "knowledge" }
		}, modules...)
		edition, got := read(t, h)
		if edition != "community" {
			t.Fatalf("edition = %q, want community", edition)
		}
		want := []need{{"engineer", "skills", titleOf(t, "skills")}, {"business", "adoption", titleOf(t, "adoption")}}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("needs = %v, want %v (knowledge is off, so it is not listed)", got, want)
		}
	})

	t.Run("a Business licence on the Business build lists nothing", func(t *testing.T) {
		h := newHarnessOpts(t, func(o *api.Options) {
			o.Edition = "enterprise"
			o.License = businessLicense{}
		}, modules...)
		edition, got := read(t, h)
		if edition != "business" || len(got) != 0 {
			t.Fatalf("edition = %q, needs = %v; want business and nothing", edition, got)
		}
	})
}
