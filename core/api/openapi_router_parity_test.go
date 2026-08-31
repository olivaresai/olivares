// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// A PUBLISHED OPERATION MUST EXIST IN THE ROUTER (2026-08-05).
//
// The only guard the stable document had was `len(paths) != 49` — a COUNT. A count
// cannot see three declared operations swapped for three real ones, and that is
// exactly what had happened: `testConnector`, `testSSOConfig` and `rotateToken`
// were declared on their PARENT path while chi registers them on /test, /test and
// /{id}/rotate. A client generated from the contract POSTed to a URL the router
// never matches and got a bare 405 with an empty body and no Content-Type — from
// three operations the contract calls stable.
//
// This compares SETS instead: every (method, path) the document publishes must be
// registered. The opposite direction — routes the document does not publish — is
// deliberately NOT asserted here: there are many, they are a known and separately
// tracked gap, and conflating the two would make this test unfixable and therefore
// ignorable.
func TestEveryPublishedOperationExistsInTheRouter(t *testing.T) {
	h := newHarness(t)

	router, ok := h.srv.Handler().(chi.Routes)
	if !ok {
		t.Fatal("handler is not a chi router")
	}
	routed := map[string]bool{}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routed[method+" "+normaliseRoute(route)] = true
		return nil
	}); err != nil {
		t.Fatalf("walking the router: %v", err)
	}
	if len(routed) == 0 {
		t.Fatal("the router walk found no routes at all; this test would pass vacuously")
	}

	doc := decodeDoc(t, rawGet(h, "/openapi.json", "", nil))
	paths, _ := doc["paths"].(map[string]any)
	if len(paths) == 0 {
		t.Fatal("the published document has no paths; this test would pass vacuously")
	}

	checked := 0
	for path, item := range paths {
		ops, _ := item.(map[string]any)
		for method := range ops {
			m := strings.ToUpper(method)
			switch m {
			case "GET", "PUT", "POST", "DELETE", "PATCH", "HEAD", "OPTIONS":
			default:
				continue // parameters, summary, and other non-operation keys
			}
			checked++
			if !routed[m+" "+normaliseRoute(path)] {
				t.Errorf("published %s %s is not registered in the router: a generated client "+
					"calling it gets 405 with an empty body", m, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no operations were checked; the document shape changed and this test stopped discriminating")
	}
	t.Logf("%d published operations checked against %d routed endpoints", checked, len(routed))
}

// normaliseRoute reduces chi's and OpenAPI's spellings of the same route to one
// form: chi emits trailing slashes for subrouters and both use {param}, but the
// parameter NAMES differ between the two ("{id}" vs "{tenant}"), so they are
// erased. That keeps the comparison about SHAPE, which is what a client's URL
// depends on.
func normaliseRoute(r string) string {
	var b strings.Builder
	depth := 0
	for _, c := range r {
		switch {
		case c == '{':
			depth++
			b.WriteString("{}")
		case c == '}':
			depth--
		case depth == 0:
			b.WriteRune(c)
		}
	}
	out := b.String()
	if len(out) > 1 {
		out = strings.TrimSuffix(out, "/")
	}
	return out
}
