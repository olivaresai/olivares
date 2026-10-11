// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/modules/internal/oastest"
)

// The inventory observation history publishes a closed contract (openapi.go). These tests hold the GENERATOR to it, so that a regression to
// the generic envelope — no limit or cursor, a 200 of {type: object}, no 500 — or an
// opening of any closed shape fails here, before regeneration and review. The
// artifact checks (openapi:check, sdk:check) only prove that a snapshot matches its
// generator; they cannot notice a generator that describes the route wrongly, which
// is exactly how the first delivery of this route shipped without its contract.

const inventoryObservationPath = "/v1/m/inventory/entities/{kind}/{id}/observations"

// TestInventoryObservationHistoryRouteIsExact: the arm recognizes the one route and
// nothing that merely resembles it. Every look-alike stays undocumented, so it keeps
// the generic contract — no parameters beyond the reflector's, the generic 200 and
// no 500.
func TestInventoryObservationHistoryRouteIsExact(t *testing.T) {
	t.Parallel()
	if !inventoryObservationHistoryRoute(http.MethodGet, "/entities/{kind}/{id}/observations") {
		t.Fatal("the exact observation history route is not recognized")
	}
	for _, other := range []operation{
		{method: http.MethodPost, pattern: "/entities/{kind}/{id}/observations"},
		{method: http.MethodGet, pattern: "/entities/{kind}/{id}"},
		{method: http.MethodGet, pattern: "/entities"},
		{method: http.MethodGet, pattern: "/summary"},
		{method: http.MethodGet, pattern: "/entities/{kind}/{id}/observations/{receipt}"},
		{method: http.MethodGet, pattern: "/entities/{kind}/{id}/observation"},
	} {
		label := other.method + " " + other.pattern
		if inventoryObservationHistoryRoute(other.method, other.pattern) {
			t.Errorf("%s is recognized as the observation history", label)
		}
		if doc, documented := New().OperationDocumentation(other.method, other.pattern); documented {
			t.Errorf("%s gained a documented contract: %#v", label, doc)
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
	op := oastest.Op(t, New(), "get", inventoryObservationPath)
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
	if kind["in"] != "path" || kind["required"] != true || !reflect.DeepEqual(kind["schema"], oas.Obj("type", "string")) {
		t.Errorf("kind = %v", kind)
	}
	id := params[2]
	if id["in"] != "path" || id["required"] != true {
		t.Errorf("id = %v", id)
	}
	inventoryAssertCanonicalID(t, oastest.Map(t, id["schema"], "id.schema"), "path id")
	for _, want := range []string{"nonzero", "400", "404"} {
		if !strings.Contains(id["description"].(string), want) {
			t.Errorf("path id description lacks %q: %q", want, id["description"])
		}
	}
	limit := params[3]
	if limit["in"] != "query" || limit["required"] != false {
		t.Errorf("limit = %v", limit)
	}
	if !reflect.DeepEqual(limit["schema"], oas.Obj("type", "integer", "minimum", 1, "maximum", 25, "default", 25)) {
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
	inventoryAssertCanonicalID(t, oastest.Map(t, cursor["schema"], "cursor.schema"), "cursor")
	for _, want := range []string{"at most once", "strictly after", "nonzero", "400"} {
		if !strings.Contains(cursor["description"].(string), want) {
			t.Errorf("cursor description lacks %q: %q", want, cursor["description"])
		}
	}

	// --- statuses -------------------------------------------------------------
	responses := oastest.Map(t, op["responses"], "responses")
	if got := oastest.Keys(responses); !reflect.DeepEqual(got, []string{"200", "400", "401", "403", "404", "409", "429", "500"}) {
		t.Fatalf("statuses = %v", got)
	}
	if !reflect.DeepEqual(inventoryResponseSchema(t, responses, "500"), oas.Obj("type", "object")) {
		t.Errorf("500 must stay the generic error body: %v", responses["500"])
	}
	if desc := oastest.Map(t, responses["500"], "500")["description"].(string); !strings.Contains(desc, "generic") || !strings.Contains(desc, "operator") {
		t.Errorf("500 description = %q", desc)
	}

	// --- the page envelope ------------------------------------------------------
	page := inventoryResponseSchema(t, responses, "200")
	inventoryAssertClosed(t, page, "200", []string{"cursor", "has_more", "items"}, []string{"has_more", "items"})
	props := oastest.Map(t, page["properties"], "200.properties")
	items := oastest.Map(t, props["items"], "items")
	if items["type"] != "array" || items["maxItems"] != 25 {
		t.Errorf("items = %v, want an array of at most 25", items)
	}
	inventoryAssertCanonicalID(t, oastest.Map(t, props["cursor"], "cursor"), "200.cursor")
	if desc := oastest.Map(t, props["cursor"], "cursor")["description"].(string); !strings.Contains(desc, "has_more") {
		t.Errorf("cursor property description does not tie it to has_more: %q", desc)
	}
	hasMore := oastest.Map(t, props["has_more"], "has_more")
	if hasMore["type"] != "boolean" || !strings.Contains(hasMore["description"].(string), "integrity") {
		t.Errorf("has_more = %v", hasMore)
	}

	// --- the item ----------------------------------------------------------------
	item := oastest.Map(t, items["items"], "items.items")
	inventoryAssertClosed(t, item, "item",
		[]string{"conflicting_redelivery", "deliveries", "event_type", "first_received_at", "last_received_at", "receipt_id", "registration", "source_occurred_at"},
		[]string{"conflicting_redelivery", "deliveries", "event_type", "first_received_at", "last_received_at", "receipt_id", "registration"})
	ip := oastest.Map(t, item["properties"], "item.properties")
	inventoryAssertCanonicalID(t, oastest.Map(t, ip["receipt_id"], "receipt_id"), "receipt_id")
	eventType := oastest.Map(t, ip["event_type"], "event_type")
	if eventType["type"] != "string" || !reflect.DeepEqual(eventType["enum"], oas.Enum("edge.observed", "cost.sampled")) {
		t.Errorf("event_type = %v", eventType)
	}
	for _, name := range []string{"source_occurred_at", "first_received_at", "last_received_at"} {
		s := oastest.Map(t, ip[name], name)
		if s["type"] != "string" || s["format"] != "date-time" {
			t.Errorf("%s = %v, want a date-time string", name, s)
		}
	}
	if desc := oastest.Map(t, ip["source_occurred_at"], "source_occurred_at")["description"].(string); !strings.Contains(desc, "SOURCE") || !strings.Contains(desc, "Omitted") {
		t.Errorf("source_occurred_at description = %q", desc)
	}
	if desc := oastest.Map(t, ip["last_received_at"], "last_received_at")["description"].(string); !strings.Contains(desc, "equal") || !strings.Contains(desc, "conflicting") {
		t.Errorf("last_received_at description = %q", desc)
	}
	deliveries := oastest.Map(t, ip["deliveries"], "deliveries")
	if deliveries["type"] != "integer" || deliveries["minimum"] != 1 || !strings.Contains(deliveries["description"].(string), "conflicting") {
		t.Errorf("deliveries = %v", deliveries)
	}
	conflicting := oastest.Map(t, ip["conflicting_redelivery"], "conflicting_redelivery")
	if conflicting["type"] != "boolean" || !strings.Contains(conflicting["description"].(string), "not published") {
		t.Errorf("conflicting_redelivery = %v", conflicting)
	}

	// --- the registration snapshot, three closed forms ---------------------------
	registration := oastest.Map(t, ip["registration"], "registration")
	if desc := registration["description"].(string); !strings.Contains(desc, "historical") || !strings.Contains(desc, "now") {
		t.Errorf("registration description does not say the snapshot is historical: %q", desc)
	}
	arms, ok := registration["oneOf"].([]any)
	if !ok || len(arms) != 3 {
		t.Fatalf("registration oneOf = %v, want three arms", registration["oneOf"])
	}
	byState := map[string]map[string]any{}
	for _, raw := range arms {
		arm := oastest.Map(t, raw, "registration arm")
		state := oastest.Map(t, oastest.Map(t, arm["properties"], "arm.properties")["registration_state"], "registration_state")
		value, _ := state["const"].(string)
		if state["type"] != "string" || value == "" {
			t.Fatalf("registration_state = %v, want a string const", state)
		}
		byState[value] = arm
	}
	if got := oastest.Keys(map[string]any{"invalid": nil, "registered_snapshot": nil, "unattributed": nil}); len(byState) != 3 || !reflect.DeepEqual(inventorySortedKeysOfArms(byState), got) {
		t.Fatalf("registration states = %v", inventorySortedKeysOfArms(byState))
	}
	registered := byState["registered_snapshot"]
	inventoryAssertClosed(t, registered, "registered_snapshot",
		[]string{"environment_ref", "registration_state", "source_id", "source_revision"},
		[]string{"environment_ref", "registration_state", "source_id", "source_revision"})
	rp := oastest.Map(t, registered["properties"], "registered.properties")
	revision := oastest.Map(t, rp["source_revision"], "source_revision")
	if revision["type"] != "integer" || revision["minimum"] != 1 {
		t.Errorf("source_revision = %v", revision)
	}
	for _, name := range []string{"source_id", "environment_ref"} {
		s := oastest.Map(t, rp[name], name)
		if s["type"] != "string" || s["minLength"] != 1 {
			t.Errorf("%s = %v, want a non-empty string", name, s)
		}
	}
	if !strings.Contains(oastest.Map(t, rp["source_id"], "source_id")["description"].(string), "not a grant") {
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
// description, and the same parameters and responses the module documents.
func TestInventoryObservationHistoryIsPublishedInTheBetaDocument(t *testing.T) {
	t.Parallel()
	op := oastest.Op(t, New(), "get", inventoryObservationPath)
	if op["x-stability"] != "beta" {
		t.Errorf("x-stability = %v", op["x-stability"])
	}
	if desc, _ := op["description"].(string); !strings.Contains(desc, "one item per distinct receipt") {
		t.Errorf("the handler-derived description is missing: %q", desc)
	}
	want := inventoryObservationDocumentation()
	params := op["parameters"].([]any)
	trailing := params[len(params)-len(want.TrailingParameters):]
	for i, p := range want.TrailingParameters {
		if !reflect.DeepEqual(trailing[i], p) {
			t.Errorf("published parameter %d = %v, want %v", i, trailing[i], p)
		}
	}
	if !reflect.DeepEqual(op["responses"], want.Responses) {
		t.Error("published responses differ from the module's documentation")
	}
}

// TestInventoryObservationHistorySchemasAreFresh: the builders return new maps on
// every call, so an annotation added by one caller cannot leak into another
// operation (the same guarantee the FinOps and catalog registries assert).
func TestInventoryObservationHistorySchemasAreFresh(t *testing.T) {
	t.Parallel()
	first := inventoryResponseSchema(t, inventoryObservationDocumentation().Responses, "200")
	oastest.Map(t, first["properties"], "properties")["not_a_real_field"] = oas.Obj("type", "string")
	second := inventoryResponseSchema(t, inventoryObservationDocumentation().Responses, "200")
	if _, leaked := oastest.Map(t, second["properties"], "properties")["not_a_real_field"]; leaked {
		t.Fatal("the page schema builder shares a mutable property map")
	}
	firstParams := inventoryObservationParameters()
	oastest.Map(t, firstParams[0], "limit")["schema"] = oas.Obj("type", "string")
	if !reflect.DeepEqual(oastest.Map(t, inventoryObservationParameters()[0], "limit")["schema"], oas.Obj("type", "integer", "minimum", 1, "maximum", 25, "default", 25)) {
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
		out = append(out, oastest.Map(t, p, "parameter "+string(rune('0'+i))))
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
	resp := oastest.Map(t, responses[status], status)
	content := oastest.Map(t, resp["content"], status+".content")
	if got := oastest.Keys(content); !reflect.DeepEqual(got, []string{"application/json"}) {
		t.Fatalf("%s content types = %v", status, got)
	}
	return oastest.Map(t, oastest.Map(t, content["application/json"], status+".media")["schema"], status+".schema")
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
	if got := oastest.Keys(oastest.Map(t, schema["properties"], label+".properties")); !reflect.DeepEqual(got, properties) {
		t.Errorf("%s properties = %v, want %v", label, got, properties)
	}
	if got := oastest.Strings(schema["required"]); !reflect.DeepEqual(got, required) {
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
			inventoryWalkSchema(t, oastest.Map(t, raw, path+"."+name), path+"."+name, names)
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		inventoryWalkSchema(t, items, path+"[]", names)
	}
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		if arms, ok := schema[keyword].([]any); ok {
			for i, raw := range arms {
				inventoryWalkSchema(t, oastest.Map(t, raw, path+"."+keyword), path+"."+keyword+"["+string(rune('0'+i))+"]", names)
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

// TestInventoryCollectionsOpenAPIContract pins the same document builder used by
// the public beta endpoint. Generic inference cannot describe these selectors.
func TestInventoryCollectionsOpenAPIContract(t *testing.T) {
	t.Parallel()
	op := oastest.Op(t, New(), "get", "/v1/m/inventory/collections")
	if op["x-required-permission"] != "inventory:catalog:read" || op["requestBody"] != nil {
		t.Fatalf("authority/body changed: %v", op)
	}
	params := inventoryParams(t, op)
	if got := inventoryParamNames(params); !reflect.DeepEqual(got, []string{"X-Olivares-Tenant", "source_id", "source_revision", "environment_ref"}) {
		t.Fatalf("selectors = %v", got)
	}
	for _, p := range params[1:] {
		if p["in"] != "query" || p["required"] != true {
			t.Errorf("selector not required: %v", p)
		}
		schema := oastest.Map(t, p["schema"], "selector")
		if p["name"] == "source_revision" {
			if schema["type"] != "integer" || schema["format"] != "int64" || schema["minimum"] != 1 {
				t.Errorf("revision schema: %v", schema)
			}
		} else if schema["type"] != "string" || schema["minLength"] != 1 || !strings.Contains(p["description"].(string), "128 bytes") {
			t.Errorf("identity bound: %v", p)
		}
	}
	responses := oastest.Map(t, op["responses"], "responses")
	for _, code := range []string{"200", "400", "401", "403", "423", "500"} {
		if responses[code] == nil {
			t.Errorf("missing status %s", code)
		}
	}
	page := inventoryResponseSchema(t, responses, "200")
	inventoryAssertClosed(t, page, "page", []string{"cursor", "has_more", "items"}, []string{"has_more", "items"})
	pageProps := oastest.Map(t, page["properties"], "page.properties")
	item := oastest.Map(t, oastest.Map(t, pageProps["items"], "items")["items"], "item")
	inventoryAssertClosed(t, item, "item", []string{"current", "last_qualified_success"}, []string{"current"})
	props := oastest.Map(t, item["properties"], "item.properties")
	current := oastest.Map(t, props["current"], "current")
	inventoryAssertClosed(t, current, "current",
		[]string{"admitted_count", "committed_count", "coverage", "environment_ref", "expected_count", "family", "fulfilled_scope", "host_finished_at", "host_started_at", "producer_finished_at", "producer_started_at", "projection", "qualified_at", "reason", "rejection_reason", "requested_scope", "run_id", "run_order", "scope_contract", "source_id", "source_revision"},
		[]string{"admitted_count", "committed_count", "coverage", "environment_ref", "expected_count", "host_started_at", "projection", "reason", "run_id", "run_order", "source_id", "source_revision"})
	cp := oastest.Map(t, current["properties"], "current.properties")
	if !reflect.DeepEqual(oastest.Map(t, cp["coverage"], "coverage")["enum"], oas.Enum("complete", "partial", "unavailable", "unsupported", "unknown")) {
		t.Error("coverage vocabulary lost")
	}
	if !reflect.DeepEqual(oastest.Map(t, cp["projection"], "projection")["enum"], oas.Enum("pending", "committed", "failed")) {
		t.Error("projection vocabulary lost")
	}
	// Unknown selection serializes empty run_id and host_started_at; the API
	// cannot promise a UUID or date-time for these two current fields.
	for _, name := range []string{"run_id", "host_started_at"} {
		schema := oastest.Map(t, cp[name], name)
		if schema["format"] != nil || !strings.Contains(schema["description"].(string), "empty") {
			t.Errorf("unknown selection invalidated: %v", schema)
		}
	}
	for _, name := range []string{"admitted_count", "committed_count", "expected_count"} {
		schema := oastest.Map(t, cp[name], name)
		if schema["type"] != "integer" || schema["format"] != "int64" || schema["minimum"] != 0 || schema["maximum"] != 100000 {
			t.Errorf("member bound: %v", schema)
		}
	}
	history := oastest.Map(t, props["last_qualified_success"], "history")
	fields := []string{"environment_ref", "expected_count", "family", "fulfilled_scope", "host_finished_at", "host_started_at", "producer_finished_at", "producer_started_at", "qualified_at", "requested_scope", "run_id", "scope_contract", "source_id", "source_revision"}
	inventoryAssertClosed(t, history, "history", fields, fields)
	hp := oastest.Map(t, history["properties"], "history.properties")
	for _, name := range []string{"qualified_at", "host_started_at", "host_finished_at", "producer_started_at", "producer_finished_at"} {
		if oastest.Map(t, hp[name], name)["format"] != "date-time" {
			t.Errorf("historical instant %s not typed", name)
		}
	}
	description := oastest.Map(t, responses["200"], "200")["description"].(string)
	for _, required := range []string{"CURRENT", "unknown", "historical", "authorization", "projection"} {
		if !strings.Contains(description, required) {
			t.Errorf("missing contract meaning %q", required)
		}
	}
	names := map[string]bool{}
	inventoryWalkSchema(t, page, "page", names)
	for _, forbidden := range []string{"binding_ref", "selectors", "tenant_id", "raw_facts", "facts_hash", "event_id"} {
		if names[forbidden] {
			t.Errorf("private field published: %s", forbidden)
		}
	}
}

func TestInventoryCollectionsContractIsExact(t *testing.T) {
	t.Parallel()
	for _, r := range []operation{
		{method: http.MethodPost, pattern: "/collections"},
		{method: http.MethodGet, pattern: "/collections/{id}"},
	} {
		if doc, documented := New().OperationDocumentation(r.method, r.pattern); documented {
			t.Errorf("look-alike %v gained a documented contract: %#v", r, doc)
		}
	}
}

// operation is one route of this module: method and module-relative pattern.
type operation struct{ method, pattern string }
