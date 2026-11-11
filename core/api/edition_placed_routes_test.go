// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/license"
)

// TestEveryNonModuleRouteMeetsItsPlacement walks every route the router mounts outside the
// modules and asks the Community gate about it. The routes the table places are listed here
// with the edition and the verbs placed; every other route is open. A new row in placedRoutes
// goes red until it is listed here, and a listed row whose routes are gone is red too.
func TestEveryNonModuleRouteMeetsItsPlacement(t *testing.T) {
	placed := []struct {
		prefix, edition string
		verbs           []string // the others (reading and removing what is stored) stay open
	}{
		{"/v1/console/sso", license.EditionBusiness, []string{"PUT", "POST"}},
		{"/v1/agent-groups", license.EditionBusiness, []string{"POST", "PUT", "PATCH"}},
		{"/v1/console/mcp-gateway", license.EditionEngineer, []string{"POST", "PUT"}},
	}
	h := newHarness(t)
	router, ok := h.srv.Handler().(chi.Routes)
	if !ok {
		t.Fatal("handler is not a chi router")
	}
	walked := 0
	refusedBy := map[string]int{}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/v1/") || strings.HasPrefix(route, "/v1/m/") {
			return nil
		}
		walked++
		pattern := route
		if len(pattern) > 1 {
			pattern = strings.TrimSuffix(pattern, "/")
		}
		wantEdition, wantRefused, rowPrefix := "", false, ""
		for _, row := range placed {
			if pattern == row.prefix || strings.HasPrefix(pattern, row.prefix+"/") {
				wantEdition, rowPrefix = row.edition, row.prefix
				for _, verb := range row.verbs {
					wantRefused = wantRefused || verb == method
				}
			}
		}
		err := api.EditionRouteRefusal(license.EditionCommunity, method, pattern)
		var refusal *api.EditionRequiredError
		switch {
		case wantRefused && !errors.As(err, &refusal):
			t.Errorf("%s %s in Community = %v, want a refusal naming %s", method, pattern, err, wantEdition)
		case wantRefused && refusal.Edition != wantEdition:
			t.Errorf("%s %s names edition %s, want %s", method, pattern, refusal.Edition, wantEdition)
		case !wantRefused && err != nil:
			t.Errorf("%s %s in Community = %v, want it open: it is read or removal of what is stored, or not placed", method, pattern, err)
		}
		if wantRefused {
			refusedBy[rowPrefix+" "+method]++
		}
		return nil
	}); err != nil {
		t.Fatalf("walking the router: %v", err)
	}
	if walked < 100 {
		t.Fatalf("walked %d core routes: the walk is partial and this test would pass vacuously", walked)
	}
	for _, row := range placed {
		for _, verb := range row.verbs {
			if refusedBy[row.prefix+" "+verb] == 0 {
				t.Errorf("no mounted %s route under %s is refused: the row is stale", verb, row.prefix)
			}
		}
	}
	keys := make([]string, 0, len(refusedBy))
	for k := range refusedBy {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t.Logf("%d core routes walked; refused in Community: %v", walked, keys)
}
