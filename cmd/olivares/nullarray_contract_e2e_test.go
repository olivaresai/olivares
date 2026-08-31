// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
)

// TestCleanInstallServesNoNullCollections is the Behavioral half of the
// no-null-collections contract: it drives the REAL router the binary mounts —
// engine routes plus every production module — and asserts that a correct,
// freshly-installed deployment never answers a collection with null.
//
// Why a sweep and not three assertions: the defect that motivated it was measured
// with curl against a clean install (12 compliance endpoints served
// `{"items":null}` and the console's `.map()` died on three of them), but the
// CAUSE was general — a nil accumulator in a handler — so a test that pinned the
// three measured routes would have left the other nine, and every route added
// next month, unguarded. The static invariant in core/api makes the envelope
// structurally incapable of emitting null; this one covers what no type can:
// a hand-rolled response DTO with an array field that nobody initialized.
//
// The oracle is four rules. R1–R3 each name a specific broken promise; R0 is the
// blanket one that makes the set complete for a clean install:
//
//	R0  ANY null in a 200 body served to the EMPTY tenant, except a named
//	    allowlist. This is affordable — and therefore mandatory — because it was
//	    MEASURED: across 224 JSON responses on an untouched tenant the whole API
//	    produces exactly ONE deliberately-nullable field (see nullIsTheContract).
//	    R1–R3 alone let two real defects survive their mutants (finops `agents`
//	    and eventing `authorities`: arrays nobody named "items", absent from the
//	    core spec, and empty for the seeded tenant too, so nothing could see them).
//	R1  the wire key "items" is null — the envelope contract every module list
//	    route and the core spec's listOf() declare. Kept for its message.
//	R2  a key that is an ARRAY for the seeded tenant is null for the EMPTY tenant
//	    — a differential that needs no schema, and the only rule that also covers
//	    a tenant WITH data. Its reach is bounded by what the seed populates.
//	R3  the PUBLISHED core contract declares a property `"type":"array"` and the
//	    body has null there — the engine surface answering to its own OpenAPI.
//
// The allowlist is the honest part of R0: a field that is genuinely "value or
// nothing" (a rate with no decisions behind it, where 0.0 would be a LIE) belongs
// there, named, with the source line that declares it. A field that lands there to
// silence this test is a contract someone has to defend in review.
func TestCleanInstallServesNoNullCollections(t *testing.T) {
	h := newHarness(t)

	routes := parameterlessGETRoutes(t, h)
	if len(routes) < 100 {
		// Failing to LOOK is not the same as finding nothing. If the router ever
		// stops yielding a realistic surface (a mount error, a harness change),
		// this test must say so instead of passing on an empty sweep.
		t.Fatalf("walked only %d parameterless GET routes; the sweep is not covering the API surface", len(routes))
	}

	coreDoc := api.OpenAPIDocument()

	var (
		checked     int // routes that answered 200 with a JSON body on the empty tenant
		arrays      int // array-valued keys observed (R2's working set)
		specSchemas int // routes the published core document actually describes (R3's reach)
		allowedHit  = map[string]bool{}
		unreached   []string
	)
	// Keyed by route+path so the four rules cannot report the same null twice; the
	// first rule to fire owns the message, and they are ordered most-specific first.
	findings := map[string]string{}
	report := func(route, path, msg string) {
		key := route + "\x00" + path
		if _, seen := findings[key]; !seen {
			findings[key] = msg
		}
	}

	for _, route := range routes {
		// The EMPTY tenant is the clean install: nothing has ever been written to it.
		emptyBody, emptyOK, returned := getJSONBody(t, h, route, h.tenantB)
		if !returned {
			unreached = append(unreached, route)
			continue
		}
		if !emptyOK {
			continue
		}
		checked++

		// R1 and R3 run against BOTH tenants: R0's blanket rule only holds for the
		// empty one (a tenant WITH data legitimately carries nulls that mean
		// "absent"), but "items is null" and "the spec says array" are broken
		// promises whatever the tenant holds.
		seededBody, seededOK, seededReturned := getJSONBody(t, h, route, h.tenantA)
		bodies := map[string]map[string]any{"empty tenant": emptyBody}
		if seededReturned && seededOK {
			bodies["seeded tenant"] = seededBody
		}
		if coreResponseSchema(coreDoc, route) != nil {
			specSchemas++
		}
		for label, body := range bodies {
			// R1 — the envelope key, at any depth.
			walkJSON(body, "", func(path string, key string, v any) {
				if key == "items" && v == nil {
					report(route, jsonPath(path, key), fmt.Sprintf(
						"GET %s (%s): %s is null; an empty page is [] — the console maps over it",
						route, label, jsonPath(path, key)))
				}
			})
			// R3 — the published core contract for this route, when it describes one.
			if schema := coreResponseSchema(coreDoc, route); schema != nil {
				for _, v := range violatesDeclaredArray(coreDoc, schema, body, "") {
					report(route, v, fmt.Sprintf(
						"GET %s (%s): %s is null but the published OpenAPI declares it an array", route, label, v))
				}
			}
		}

		// R2 — the shape differential between the two.
		if seededReturned && seededOK {
			seededArrays := map[string]bool{}
			walkJSON(seededBody, "", func(path, key string, v any) {
				if _, isArray := v.([]any); isArray {
					seededArrays[jsonPath(path, key)] = true
				}
			})
			arrays += len(seededArrays)
			walkJSON(emptyBody, "", func(path, key string, v any) {
				p := jsonPath(path, key)
				if v == nil && seededArrays[p] {
					report(route, p, fmt.Sprintf(
						"GET %s: %s is an array for a tenant WITH data and null for a tenant without", route, p))
				}
			})
		}

		// R0 — the blanket rule: nothing a clean install serves is null unless the
		// contract says the field is value-or-nothing.
		walkJSON(emptyBody, "", func(path, key string, v any) {
			if v != nil {
				return
			}
			p := jsonPath(path, key)
			if _, declared := nullIsTheContract[p]; declared {
				allowedHit[p] = true
				return
			}
			report(route, p, fmt.Sprintf(
				"GET %s (empty tenant): %s is null. A correct, empty install must serve a VALUE — "+
					"[] for a collection, 0 for a count, \"\" for a string. If null is genuinely the "+
					"contract for this field, add it to nullIsTheContract with the line that declares it",
				route, p))
		})
	}

	keys := make([]string, 0, len(findings))
	for k := range findings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Errorf("null collection: %s", findings[k])
	}
	for p, why := range nullIsTheContract {
		if !allowedHit[p] {
			t.Errorf("stale nullIsTheContract entry %q (%s): nothing serves null there any more — delete it", p, why)
		}
	}
	t.Logf("swept %d parameterless GET routes; %d answered 200 JSON on the empty tenant; "+
		"%d array-valued keys observed on the seeded tenant; %d matched a published core schema; "+
		"%d did not return in time (%v)",
		len(routes), checked, arrays, specSchemas, len(unreached), unreached)
	if checked < 50 {
		t.Fatalf("only %d routes produced a JSON body to inspect; the sweep proves nothing", checked)
	}
	if specSchemas == 0 {
		// R3 silently matching nothing (a path-spelling drift between chi and the
		// document) would leave a rule that can never fire, which is worse than not
		// having it: it would read as coverage in review.
		t.Fatal("R3 matched no published core response schema at all; the rule cannot fire")
	}
	if arrays == 0 {
		t.Fatal("R2 observed no array anywhere on the seeded tenant; the differential cannot fire")
	}
}

// TestDeclaredArrayRuleDiscriminates is the unit proof for R3's schema walk: it
// must fire on null-where-the-document-says-array, through a $ref and inside a
// nested object, and stay silent on a null the document does NOT declare an array
// (and on an absent property). Without this, a bug in the walk would make R3 a
// rule that never fires, and the sweep above would still pass.
func TestDeclaredArrayRuleDiscriminates(t *testing.T) {
	doc := map[string]any{
		"components": map[string]any{"schemas": map[string]any{
			"Page": map[string]any{"type": "object", "properties": map[string]any{
				"items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"note":  map[string]any{"type": "string"},
			}},
		}},
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"page":  map[string]any{"$ref": "#/components/schemas/Page"},
		"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"label": map[string]any{"type": "string"},
	}}

	cases := []struct {
		name string
		body map[string]any
		want []string
	}{
		{"null array through a $ref", map[string]any{"page": map[string]any{"items": nil}}, []string{"page.items"}},
		{"null array at the top level", map[string]any{"tags": nil}, []string{"tags"}},
		{"null string is not our business", map[string]any{"label": nil}, nil},
		{"null under an undeclared key", map[string]any{"whatever": nil}, nil},
		{"empty array is correct", map[string]any{"tags": []any{}}, nil},
		{"absent property", map[string]any{}, nil},
		{"both halves", map[string]any{"tags": nil, "page": map[string]any{"items": nil, "note": nil}},
			[]string{"page.items", "tags"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := violatesDeclaredArray(doc, schema, tc.body, "")
			sort.Strings(got)
			if len(got) != len(tc.want) {
				t.Fatalf("= %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// nullIsTheContract lists the JSON paths where null is the DECLARED answer, not an
// accident — every one of them a pointer field whose source says so. Keep it
// exhaustive and keep it small: an entry here is a field the console must handle
// explicitly, so each one is a cost, and a stale entry fails this test rather than
// quietly widening the exemption.
var nullIsTheContract = map[string]string{
	"analytics.totals.acceptance_rate": "*float64, modules/claudeadoption/dto.go:50 — null when no decisions; 0.0 would read as 0% accepted",
	"telemetry.totals.acceptance_rate": "*float64, modules/claudeadoption/dto.go:65 — same field on the telemetry half of the summary",
}

// parameterlessGETRoutes walks the mounted router for GET routes with no path
// parameter — the ones a sweep can call without inventing an id.
func parameterlessGETRoutes(t *testing.T, h *harness) []string {
	t.Helper()
	router, ok := h.h.(chi.Routes)
	if !ok {
		t.Fatal("handler is not a chi router")
	}
	seen := map[string]bool{}
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method != http.MethodGet || strings.Contains(route, "{") || strings.Contains(route, "*") {
			return nil
		}
		if len(route) > 1 && strings.HasSuffix(route, "/") {
			route = route[:len(route)-1] // chi spells a collection with a trailing slash
		}
		seen[route] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// getJSONBody issues one authenticated GET against the real handler and decodes a
// JSON object body. ok is false for a non-200 or a non-JSON body (an SSE stream, a
// CSV export); returned is false when the handler did not finish within the
// deadline, which the caller reports rather than hanging the suite.
func getJSONBody(t *testing.T, h *harness, path, tenant string) (body map[string]any, ok bool, returned bool) {
	t.Helper()
	// A streaming handler runs until its request context ends; give every request
	// a deadline so the sweep needs no hand-maintained list of stream routes.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	r.RemoteAddr = "10.0.0.1:4321"
	r.Header.Set("Authorization", "Bearer "+h.adminToken)
	r.Header.Set("X-Olivares-Tenant", tenant)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.h.ServeHTTP(rec, r)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		return nil, false, false
	}
	if rec.Code != http.StatusOK {
		return nil, false, true
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		return nil, false, true
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		return nil, false, true // a JSON array or scalar at the root: nothing keyed to inspect
	}
	return m, true, true
}

// walkJSON visits every key of every object in a decoded body, depth-first,
// reporting the parent path, the key and its value.
func walkJSON(v any, path string, visit func(path, key string, val any)) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			visit(path, k, t[k])
			walkJSON(t[k], jsonPath(path, k), visit)
		}
	case []any:
		for _, e := range t {
			// Elements share their parent's path: two rows of the same list are the
			// same contract, and a per-index path would make findings unstable.
			walkJSON(e, path+"[]", visit)
		}
	}
}

func jsonPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// coreResponseSchema returns the 200 application/json schema the published core
// OpenAPI declares for GET path, or nil when the document does not describe it
// (the module surface publishes a separate, deliberately shape-free document).
func coreResponseSchema(doc map[string]any, path string) map[string]any {
	paths, _ := doc["paths"].(map[string]any)
	item, _ := paths[path].(map[string]any)
	op, _ := item["get"].(map[string]any)
	resps, _ := op["responses"].(map[string]any)
	ok200, _ := resps["200"].(map[string]any)
	content, _ := ok200["content"].(map[string]any)
	appJSON, _ := content["application/json"].(map[string]any)
	schema, _ := appJSON["schema"].(map[string]any)
	return schema
}

// violatesDeclaredArray returns the JSON paths where the body holds null and the
// schema declares an array. It resolves $ref against components/schemas and
// descends only where the body actually carries a value, so an omitted optional
// property is never reported.
func violatesDeclaredArray(doc, schema map[string]any, value any, path string) []string {
	schema = resolveRef(doc, schema)
	if schema == nil {
		return nil
	}
	declared, _ := schema["type"].(string)
	if declared == "array" && value == nil {
		return []string{path}
	}
	var out []string
	switch v := value.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		for key, sub := range props {
			val, present := v[key]
			subSchema, isObj := sub.(map[string]any)
			if !present || !isObj {
				continue
			}
			out = append(out, violatesDeclaredArray(doc, subSchema, val, jsonPath(path, key))...)
		}
	case []any:
		elem, isObj := schema["items"].(map[string]any)
		if !isObj {
			return out
		}
		for _, e := range v {
			out = append(out, violatesDeclaredArray(doc, elem, e, path+"[]")...)
		}
	}
	return out
}

// resolveRef follows a single "#/components/schemas/Name" reference. The document
// is built in Go from one function, so references are one level and never cyclic.
func resolveRef(doc, schema map[string]any) map[string]any {
	ref, ok := schema["$ref"].(string)
	if !ok {
		return schema
	}
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		return nil
	}
	comps, _ := doc["components"].(map[string]any)
	schemas, _ := comps["schemas"].(map[string]any)
	resolved, _ := schemas[strings.TrimPrefix(ref, prefix)].(map[string]any)
	return resolved
}
