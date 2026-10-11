// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package permcensus

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/modules/registry"
)

func TestCommunityRouteSurfaces(t *testing.T) {
	if Business {
		t.Fatal("Community build reports Business")
	}
	if len(editionRouteSurfaces) != len(communityRouteSurfaces) {
		t.Fatalf("Community table has %d entries, want its own %d: the Business base leaked in",
			len(editionRouteSurfaces), len(communityRouteSurfaces))
	}
	b, err := json.Marshal(Build(testModules(t)))
	if err != nil {
		t.Fatal(err)
	}
	var inv struct {
		Modules []struct {
			Namespace string `json:"namespace"`
			Routes    []struct {
				Method  string `json:"method"`
				Pattern string `json:"pattern"`
				Surface string `json:"surface"`
			} `json:"routes"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(b, &inv); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range inv.Modules {
		for _, r := range m.Routes {
			got[m.Namespace+" "+r.Method+" "+r.Pattern] = r.Surface
		}
	}
	for route := range editionRouteSurfaces {
		if _, ok := got[route]; !ok {
			t.Errorf("surface annotation names an unmounted route: %s", route)
		}
	}
	for route, want := range map[string]string{
		"finops GET /spend/export":       "api-only",
		"finops POST /cost":              "api-only",
		"finops GET /spend":              "edition-refusal",
		"finops GET /budgets":            "console",
		"finops POST /admission/reserve": "console",
		"redteam POST /runs":             "edition-refusal",
		"orchestration GET /graph":       "edition-refusal",
		"governance POST /breakglass":    "edition-refusal",
		// Business classifies these two api-only; Community must override them.
		"finops POST /seats":                  "edition-refusal",
		"governance POST /breakglass/consume": "edition-refusal",
		"compliance POST /aims/pack":          "edition-refusal",
		"compliance GET /aims/pack":           "api-only",
		"compliance PUT /nis2/incidents/{id}": "api-only",
		"compliance POST /risk/classify":      "console",
		"reporting GET /reports":              "edition-refusal",
		"posture GET /export":                 "edition-refusal",
	} {
		if got[route] != want {
			t.Errorf("%s surface = %q, want %q", route, got[route], want)
		}
	}
}

func TestEditionRefusalMustReturn501(t *testing.T) {
	for _, status := range []int{200, 401, 403, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("non-501 handler silently received edition-refusal credit")
				}
			}()
			routeSurface("finops", "GET", "/spend", func(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
				w.WriteHeader(status)
			})
		})
	}
}

func testModules(t *testing.T) []api.Module {
	t.Helper()
	modules, err := registry.Build(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return modules
}
