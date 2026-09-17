// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The inventory observation history publishes a closed contract (openapi_modules.go,
// inventory section). These tests hold the GENERATOR to it, so that a regression to
// the generic envelope — no limit or cursor, a 200 of {type: object}, no 500 — or an
// opening of any closed shape fails here, before regeneration and review. The
// artifact checks (openapi:check, sdk:check) only prove that a snapshot matches its
// generator; they cannot notice a generator that describes the route wrongly, which
// is exactly how the first delivery of this route shipped without its contract.

var inventoryObservationRoute = moduleRoute{
	ns: "inventory", method: http.MethodGet,
	pattern: "/entities/{kind}/{id}/observations", perm: "inventory:catalog:read",
}

// TestInventoryObservationHistoryRouteIsExact: the arm recognizes the one route and
// nothing that merely resembles it. Every look-alike keeps the generic contract —
// no parameters beyond the reflector's, the generic 200 and no 500.
func TestInventoryObservationHistoryRouteIsExact(t *testing.T) {
	t.Parallel()
	if !inventoryObservationHistoryRoute(inventoryObservationRoute) {
		t.Fatal("the exact observation history route is not recognized")
	}
	for _, other := range []moduleRoute{
		{ns: "sessions", method: http.MethodGet, pattern: "/entities/{kind}/{id}/observations"},
		{ns: "inventory", method: http.MethodPost, pattern: "/entities/{kind}/{id}/observations"},
		{ns: "inventory", method: http.MethodGet, pattern: "/entities/{kind}/{id}"},
		{ns: "inventory", method: http.MethodGet, pattern: "/entities"},
		{ns: "inventory", method: http.MethodGet, pattern: "/summary"},
		{ns: "inventory", method: http.MethodGet, pattern: "/entities/{kind}/{id}/observations/{receipt}"},
		{ns: "inventory", method: http.MethodGet, pattern: "/entities/{kind}/{id}/observation"},
	} {
		label := other.ns + " " + other.method + " " + other.pattern
		if inventoryObservationHistoryRoute(other) {
			t.Errorf("%s is recognized as the observation history", label)
		}
		if params := moduleRouteParameters(other); len(params) != 0 {
			t.Errorf("%s gained route parameters %v", label, params)
		}
		responses := moduleResponses(other)
		if responses["500"] != nil {
			t.Errorf("%s gained a 500", label)
		}
		if !reflect.DeepEqual(responses["200"], oaJSONResp("OK")) {
			t.Errorf("%s 200 is no longer the generic envelope: %v", label, responses["200"])
		}
		op := moduleOperation(other)
		for _, p := range inventoryParams(t, op) {
			if p["in"] == "path" && p["name"] == "id" && !reflect.DeepEqual(p["schema"], oaObj("type", "string")) {
				t.Errorf("%s path id schema changed: %v", label, p["schema"])
			}
		}
	}
}

// TestInventoryObservationHistoryOpenAPIContract pins the whole published operation:
// query parameters, path id, statuses, and the closed envelope, item and registration
// shapes with their exact property and required sets, types and vocabularies. The
// descriptions that carry what OpenAPI cannot express (a repeated parameter is 400,
// the snapshot is historical, has_more certifies no integrity, deliveries and
// reception instants exclude conflicting variants) are asserted too.
func TestInventoryObservationHistoryOpenAPIContract(t *testing.T) {
	t.Parallel()
	op := moduleOperation(inventoryObservationRoute)
	if op["x-required-permission"] != "inventory:catalog:read" {
		t.Fatalf("x-required-permission = %v", op["x-required-permission"])
	}
	if op["requestBody"] != nil {
		t.Fatalf("a GET publishes a request body: %v", op["requestBody"])
	}

	// --- parameters -----------------------------------------------------------
	params := inventoryParams(t, op)
	if got := inventoryParamNames(params); !reflect.DeepEqual(got, []string{"X-Olivares-Tenant", "kind", "id", "limit", "cursor"}) {
		t.Fatalf("parameters = %v", got)
	}
	kind := params[1]
	if kind["in"] != "path" || kind["required"] != true || !reflect.DeepEqual(kind["schema"], oaObj("type", "string")) {
		t.Errorf("kind = %v", kind)
	}
	id := params[2]
	if id["in"] != "path" || id["required"] != true {
		t.Errorf("id = %v", id)
	}
	inventoryAssertCanonicalID(t, mustMap(t, id["schema"], "id.schema"), "path id")
	for _, want := range []string{"nonzero", "400", "404"} {
		if !strings.Contains(id["description"].(string), want) {
			t.Errorf("path id description lacks %q: %q", want, id["description"])
		}
	}
	limit := params[3]
	if limit["in"] != "query" || limit["required"] != false {
		t.Errorf("limit = %v", limit)
	}
	if !reflect.DeepEqual(limit["schema"], oaObj("type", "integer", "minimum", 1, "maximum", 25, "default", 25)) {
		t.Errorf("limit schema = %v, want integer 1..25 default 25", limit["schema"])
	}
	for _, want := range []string{"at most once", "400"} {
		if !strings.Contains(limit["description"].(string), want) {
			t.Errorf("limit description lacks %q: %q", want, limit["description"])
		}
	}
	cursor := params[4]
	if cursor["in"] != "query" || cursor["required"] != false {
		t.Errorf("cursor = %v", cursor)
	}
	inventoryAssertCanonicalID(t, mustMap(t, cursor["schema"], "cursor.schema"), "cursor")
	for _, want := range []string{"at most once", "strictly after", "nonzero", "400"} {
		if !strings.Contains(cursor["description"].(string), want) {
			t.Errorf("cursor description lacks %q: %q", want, cursor["description"])
		}
	}

	// --- statuses -------------------------------------------------------------
	responses := mustMap(t, op["responses"], "responses")
	if got := sortedMapKeys(responses); !reflect.DeepEqual(got, []string{"200", "400", "401", "403", "404", "409", "429", "500"}) {
		t.Fatalf("statuses = %v", got)
	}
	if !reflect.DeepEqual(inventoryResponseSchema(t, responses, "500"), oaObj("type", "object")) {
		t.Errorf("500 must stay the generic error body: %v", responses["500"])
	}
	if desc := mustMap(t, responses["500"], "500")["description"].(string); !strings.Contains(desc, "generic") || !strings.Contains(desc, "operator") {
		t.Errorf("500 description = %q", desc)
	}

	// --- the page envelope ------------------------------------------------------
	page := inventoryResponseSchema(t, responses, "200")
	inventoryAssertClosed(t, page, "200", []string{"cursor", "has_more", "items"}, []string{"has_more", "items"})
	props := mustMap(t, page["properties"], "200.properties")
	items := mustMap(t, props["items"], "items")
	if items["type"] != "array" || items["maxItems"] != 25 {
		t.Errorf("items = %v, want an array of at most 25", items)
	}
	inventoryAssertCanonicalID(t, mustMap(t, props["cursor"], "cursor"), "200.cursor")
	if desc := mustMap(t, props["cursor"], "cursor")["description"].(string); !strings.Contains(desc, "has_more") {
		t.Errorf("cursor property description does not tie it to has_more: %q", desc)
	}
	hasMore := mustMap(t, props["has_more"], "has_more")
	if hasMore["type"] != "boolean" || !strings.Contains(hasMore["description"].(string), "integrity") {
		t.Errorf("has_more = %v", hasMore)
	}

	// --- the item ----------------------------------------------------------------
	item := mustMap(t, items["items"], "items.items")
	inventoryAssertClosed(t, item, "item",
		[]string{"conflicting_redelivery", "deliveries", "event_type", "first_received_at", "last_received_at", "receipt_id", "registration", "source_occurred_at"},
		[]string{"conflicting_redelivery", "deliveries", "event_type", "first_received_at", "last_received_at", "receipt_id", "registration"})
	ip := mustMap(t, item["properties"], "item.properties")
	inventoryAssertCanonicalID(t, mustMap(t, ip["receipt_id"], "receipt_id"), "receipt_id")
	eventType := mustMap(t, ip["event_type"], "event_type")
	if eventType["type"] != "string" || !reflect.DeepEqual(eventType["enum"], oaEnum("edge.observed", "cost.sampled")) {
		t.Errorf("event_type = %v", eventType)
	}
	for _, name := range []string{"source_occurred_at", "first_received_at", "last_received_at"} {
		s := mustMap(t, ip[name], name)
		if s["type"] != "string" || s["format"] != "date-time" {
			t.Errorf("%s = %v, want a date-time string", name, s)
		}
	}
	if desc := mustMap(t, ip["source_occurred_at"], "source_occurred_at")["description"].(string); !strings.Contains(desc, "SOURCE") || !strings.Contains(desc, "Omitted") {
		t.Errorf("source_occurred_at description = %q", desc)
	}
	if desc := mustMap(t, ip["last_received_at"], "last_received_at")["description"].(string); !strings.Contains(desc, "equal") || !strings.Contains(desc, "conflicting") {
		t.Errorf("last_received_at description = %q", desc)
	}
	deliveries := mustMap(t, ip["deliveries"], "deliveries")
	if deliveries["type"] != "integer" || deliveries["minimum"] != 1 || !strings.Contains(deliveries["description"].(string), "conflicting") {
		t.Errorf("deliveries = %v", deliveries)
	}
	conflicting := mustMap(t, ip["conflicting_redelivery"], "conflicting_redelivery")
	if conflicting["type"] != "boolean" || !strings.Contains(conflicting["description"].(string), "not published") {
		t.Errorf("conflicting_redelivery = %v", conflicting)
	}

	// --- the registration snapshot, three closed forms ---------------------------
	registration := mustMap(t, ip["registration"], "registration")
	if desc := registration["description"].(string); !strings.Contains(desc, "historical") || !strings.Contains(desc, "now") {
		t.Errorf("registration description does not say the snapshot is historical: %q", desc)
	}
	arms, ok := registration["oneOf"].([]any)
	if !ok || len(arms) != 3 {
		t.Fatalf("registration oneOf = %v, want three arms", registration["oneOf"])
	}
	byState := map[string]map[string]any{}
	for _, raw := range arms {
		arm := mustMap(t, raw, "registration arm")
		state := mustMap(t, mustMap(t, arm["properties"], "arm.properties")["registration_state"], "registration_state")
		value, _ := state["const"].(string)
		if state["type"] != "string" || value == "" {
			t.Fatalf("registration_state = %v, want a string const", state)
		}
		byState[value] = arm
	}
	if got := sortedMapKeys(map[string]any{"invalid": nil, "registered_snapshot": nil, "unattributed": nil}); len(byState) != 3 || !reflect.DeepEqual(inventorySortedKeysOfArms(byState), got) {
		t.Fatalf("registration states = %v", inventorySortedKeysOfArms(byState))
	}
	registered := byState["registered_snapshot"]
	inventoryAssertClosed(t, registered, "registered_snapshot",
		[]string{"environment_ref", "registration_state", "source_id", "source_revision"},
		[]string{"environment_ref", "registration_state", "source_id", "source_revision"})
	rp := mustMap(t, registered["properties"], "registered.properties")
	revision := mustMap(t, rp["source_revision"], "source_revision")
	if revision["type"] != "integer" || revision["minimum"] != 1 {
		t.Errorf("source_revision = %v", revision)
	}
	for _, name := range []string{"source_id", "environment_ref"} {
		s := mustMap(t, rp[name], name)
		if s["type"] != "string" || s["minLength"] != 1 {
			t.Errorf("%s = %v, want a non-empty string", name, s)
		}
	}
	if !strings.Contains(mustMap(t, rp["source_id"], "source_id")["description"].(string), "not a grant") {
		t.Error("source_id description does not deny that it grants access")
	}
	for _, state := range []string{"unattributed", "invalid"} {
		inventoryAssertClosed(t, byState[state], state, []string{"registration_state"}, []string{"registration_state"})
	}

	// --- closure and the allowlist, everywhere under the 200 body ----------------
	names := map[string]bool{}
	inventoryWalkSchema(t, page, "200", names)
	for _, forbidden := range []string{
		"event_id", "receipt_key", "facts", "facts_hash", "hash", "source_label", "label", "labels",
		"binding", "binding_ref", "native", "name", "ref", "signal", "host", "hosts",
		"edge", "edges", "cost", "costs", "payload", "envelope_occurred_at", "member_count", "members",
		"health", "owner", "grant", "grants", "permissions", "roster", "coverage", "status", "workspace_id",
	} {
		if names[forbidden] {
			t.Errorf("the published page schema names the withheld field %q", forbidden)
		}
	}
}

// TestInventoryObservationHistoryIsPublishedInTheBetaDocument drives the document
// builder end to end for this one route: the path key, the beta stamp, the derived
// description, and the same parameters and responses moduleOperation publishes.
func TestInventoryObservationHistoryIsPublishedInTheBetaDocument(t *testing.T) {
	t.Parallel()
	doc := buildModuleOpenAPI([]moduleRoute{inventoryObservationRoute})
	paths := mustMap(t, doc["paths"], "paths")
	item := mustMap(t, paths["/v1/m/inventory/entities/{kind}/{id}/observations"], "path item")
	op := mustMap(t, item["get"], "get")
	if op["x-stability"] != "beta" {
		t.Errorf("x-stability = %v", op["x-stability"])
	}
	if desc, _ := op["description"].(string); !strings.Contains(desc, "one item per distinct receipt") {
		t.Errorf("the handler-derived description is missing: %q", desc)
	}
	want := moduleOperation(inventoryObservationRoute)
	if !reflect.DeepEqual(op["parameters"], want["parameters"]) {
		t.Error("published parameters differ from moduleOperation's")
	}
	if !reflect.DeepEqual(op["responses"], want["responses"]) {
		t.Error("published responses differ from moduleOperation's")
	}
}

// TestInventoryObservationHistorySchemasAreFresh: the builders return new maps on
// every call, so an annotation added by one caller cannot leak into another
// operation (the same guarantee the FinOps and catalog registries assert).
func TestInventoryObservationHistorySchemasAreFresh(t *testing.T) {
	t.Parallel()
	first := inventoryResponseSchema(t, moduleResponses(inventoryObservationRoute), "200")
	mustMap(t, first["properties"], "properties")["not_a_real_field"] = oaObj("type", "string")
	second := inventoryResponseSchema(t, moduleResponses(inventoryObservationRoute), "200")
	if _, leaked := mustMap(t, second["properties"], "properties")["not_a_real_field"]; leaked {
		t.Fatal("the page schema builder shares a mutable property map")
	}
	firstParams := inventoryObservationParameters()
	mustMap(t, firstParams[0], "limit")["schema"] = oaObj("type", "string")
	if !reflect.DeepEqual(mustMap(t, inventoryObservationParameters()[0], "limit")["schema"], oaObj("type", "integer", "minimum", 1, "maximum", 25, "default", 25)) {
		t.Fatal("the parameter builder shares a mutable schema")
	}
}

// --- helpers ------------------------------------------------------------------

func inventoryParams(t *testing.T, op map[string]any) []map[string]any {
	t.Helper()
	raw, ok := op["parameters"].([]any)
	if !ok {
		t.Fatalf("parameters = %#v, want a list", op["parameters"])
	}
	out := make([]map[string]any, 0, len(raw))
	for i, p := range raw {
		out = append(out, mustMap(t, p, "parameter "+string(rune('0'+i))))
	}
	return out
}

func inventoryParamNames(params []map[string]any) []string {
	out := make([]string, 0, len(params))
	for _, p := range params {
		out = append(out, p["name"].(string))
	}
	return out
}

func inventoryResponseSchema(t *testing.T, responses map[string]any, status string) map[string]any {
	t.Helper()
	resp := mustMap(t, responses[status], status)
	content := mustMap(t, resp["content"], status+".content")
	if got := sortedMapKeys(content); !reflect.DeepEqual(got, []string{"application/json"}) {
		t.Fatalf("%s content types = %v", status, got)
	}
	return mustMap(t, mustMap(t, content["application/json"], status+".media")["schema"], status+".schema")
}

// inventoryAssertClosed: an object schema with additionalProperties false, exactly
// the given property names and exactly the given required names (both sorted).
func inventoryAssertClosed(t *testing.T, schema map[string]any, label string, properties, required []string) {
	t.Helper()
	if schema["type"] != "object" {
		t.Errorf("%s type = %v, want object", label, schema["type"])
	}
	if schema["additionalProperties"] != false {
		t.Errorf("%s additionalProperties = %v, want false", label, schema["additionalProperties"])
	}
	if got := sortedMapKeys(mustMap(t, schema["properties"], label+".properties")); !reflect.DeepEqual(got, properties) {
		t.Errorf("%s properties = %v, want %v", label, got, properties)
	}
	if got := sortedStrings(schema["required"]); !reflect.DeepEqual(got, required) {
		t.Errorf("%s required = %v, want %v", label, got, required)
	}
}

func inventoryAssertCanonicalID(t *testing.T, schema map[string]any, label string) {
	t.Helper()
	if schema["type"] != "string" || schema["format"] != "uuid" || schema["pattern"] != inventoryCanonicalIDPattern {
		t.Errorf("%s = %v, want the canonical uuid schema", label, schema)
	}
}

// inventoryWalkSchema records every property name reachable under schema and
// requires every object it meets to be closed. It follows properties, array items
// and the oneOf/anyOf/allOf arms, which is every composition keyword this contract
// uses; an unknown keyword would hide a property from the allowlist check, so the
// walk is what the forbidden-name assertion stands on.
func inventoryWalkSchema(t *testing.T, schema map[string]any, path string, names map[string]bool) {
	t.Helper()
	if schema["type"] == "object" && schema["additionalProperties"] != false {
		t.Errorf("%s is an open object", path)
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for name, raw := range props {
			names[name] = true
			inventoryWalkSchema(t, mustMap(t, raw, path+"."+name), path+"."+name, names)
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		inventoryWalkSchema(t, items, path+"[]", names)
	}
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		if arms, ok := schema[keyword].([]any); ok {
			for i, raw := range arms {
				inventoryWalkSchema(t, mustMap(t, raw, path+"."+keyword), path+"."+keyword+"["+string(rune('0'+i))+"]", names)
			}
		}
	}
}

func inventorySortedKeysOfArms(arms map[string]map[string]any) []string {
	out := make([]string, 0, len(arms))
	for k := range arms {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
