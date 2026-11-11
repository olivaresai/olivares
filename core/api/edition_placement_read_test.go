// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/license"
)

// THE PLACEMENT IS READABLE (dev). The console keeps no copy of where a capability lives:
// server-info carries the engine's own table, the minimum edition of each placed module with
// its reads_below, and the placed routes outside the modules with their verbs. Each row must
// say what the Community gate says, so the read and the gate cannot drift: both come from one table.
func TestServerInfoPublishesThePlacementTheGateEnforces(t *testing.T) {
	h := newHarness(t)
	r := h.do("GET", "/v1/server-info", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /v1/server-info = %d", r.code)
	}
	placement, _ := r.body["placement"].(map[string]any)
	modules, _ := placement["modules"].([]any)
	routes, _ := placement["routes"].([]any)
	if len(modules) < 10 || len(routes) < 3 {
		t.Fatalf("placement = %d modules, %d routes; want the engine's whole table", len(modules), len(routes))
	}
	sawKnowledge := false
	for _, row := range modules {
		m := row.(map[string]any)
		namespace, edition := m["namespace"].(string), m["edition"].(string)
		readsBelow, _ := m["reads_below"].(bool)
		if m["title"] == "" || m["title"] == nil {
			t.Errorf("module %s has no title", namespace)
		}
		refusal := api.EditionRouteRefusal(license.EditionCommunity, http.MethodPost, "/v1/m/"+namespace+"/x")
		if refusal == nil {
			t.Errorf("module %s is published as %s and the Community gate serves it", namespace, edition)
		}
		if served := api.EditionRouteRefusal(license.EditionCommunity, http.MethodGet, "/v1/m/"+namespace+"/x") == nil; served != readsBelow {
			t.Errorf("module %s publishes reads_below=%v and the gate serves its GET = %v", namespace, readsBelow, served)
		}
		if namespace == "knowledge" {
			sawKnowledge = edition == "engineer" && readsBelow
		}
	}
	if !sawKnowledge {
		t.Error("knowledge is not published as engineer with reads_below")
	}
	sawSSO := false
	for _, row := range routes {
		p := row.(map[string]any)
		prefix, edition := p["prefix"].(string), p["edition"].(string)
		for _, verb := range p["methods"].([]any) {
			if api.EditionRouteRefusal(license.EditionCommunity, verb.(string), prefix) == nil {
				t.Errorf("route %s %s is published as %s and the Community gate serves it", verb, prefix, edition)
			}
		}
		if prefix == "/v1/console/sso" {
			sawSSO = edition == "business"
		}
	}
	if !sawSSO {
		t.Error("/v1/console/sso is not published as business")
	}
}
