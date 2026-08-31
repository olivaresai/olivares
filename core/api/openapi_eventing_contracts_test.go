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

	"github.com/olivaresai/olivares/sdk/siemwire"
)

func TestEventingRequestBodiesClassifyAllMutations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method  string
		pattern string
		kind    eventingRequestBodyKind
	}{
		{http.MethodPost, "/egress-policy/check", eventingBodyful},
		{http.MethodPost, "/subscriptions", eventingBodyful},
		{http.MethodPut, "/subscriptions/{id}", eventingBodyful},
		{http.MethodDelete, "/subscriptions/{id}", eventingBodyless},
		{http.MethodPost, "/subscriptions/{id}/restore", eventingBodyful},
		{http.MethodPost, "/subscriptions/{id}/rotate-secret", eventingBodyless},
		{http.MethodPost, "/subscriptions/{id}/rotate-auth", eventingBodyful},
		{http.MethodPost, "/subscriptions/{id}/test", eventingBodyless},
		{http.MethodPost, "/subscriptions/{id}/replay", eventingBodyful},
		{http.MethodPost, "/deliveries/{id}/redeliver", eventingBodyless},
	}

	wantCounts := map[eventingRequestBodyKind]int{
		eventingBodyful:         6,
		eventingBodyless:        4,
		eventingBodyNoDerivable: 0,
		eventingBodyPending:     0,
	}
	gotCounts := map[eventingRequestBodyKind]int{
		eventingBodyful:         0,
		eventingBodyless:        0,
		eventingBodyNoDerivable: 0,
		eventingBodyPending:     0,
	}
	seen := make(map[string]struct{}, len(tests))
	for _, tt := range tests {
		key := tt.method + " " + tt.pattern
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate mutation in test catalog: %s", key)
		}
		seen[key] = struct{}{}
		decl, ok := eventingRequestBodyDeclarationFor(moduleRoute{
			ns: "eventing", method: tt.method, pattern: tt.pattern,
		})
		if !ok {
			t.Errorf("%s is not classified", key)
			continue
		}
		if decl.kind != tt.kind {
			t.Errorf("%s kind = %v, want %v", key, decl.kind, tt.kind)
		}
		gotCounts[decl.kind]++
		body, hasBody := eventingRequestBody(moduleRoute{
			ns: "eventing", method: tt.method, pattern: tt.pattern,
		})
		if hasBody != (tt.kind == eventingBodyful) {
			t.Errorf("%s requestBody present = %t, want %t", key, hasBody, tt.kind == eventingBodyful)
		}
		if !hasBody && body != nil {
			t.Errorf("%s returned a body with ok=false: %#v", key, body)
		}
	}
	if len(seen) != 10 {
		t.Fatalf("classified %d mutations, want 10", len(seen))
	}
	if !reflect.DeepEqual(gotCounts, wantCounts) {
		t.Fatalf("classification counts = %#v, want %#v", gotCounts, wantCounts)
	}
}

func TestEventingRequestBodiesMatchHandlerDTOs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method     string
		pattern    string
		properties []string
		required   []string
	}{
		{http.MethodPost, "/egress-policy/check", []string{"endpoint", "subscription_id"}, []string{"endpoint"}},
		{http.MethodPost, "/subscriptions", []string{"auth_header_name", "auth_type", "auth_value", "description", "enabled", "endpoint", "event_types", "initial_interval_seconds", "match_sources", "max_attempts", "name", "role", "sink_cred", "sink_format", "sink_kind", "sink_opts"}, []string{"endpoint", "event_types", "name"}},
		{http.MethodPut, "/subscriptions/{id}", []string{"auth_header_name", "auth_type", "auth_value", "description", "enabled", "endpoint", "event_types", "initial_interval_seconds", "match_sources", "max_attempts", "name", "role", "sink_cred", "sink_format", "sink_kind", "sink_opts"}, []string{"endpoint", "event_types", "name"}},
		{http.MethodPost, "/subscriptions/{id}/restore", []string{"revision_id"}, []string{"revision_id"}},
		{http.MethodPost, "/subscriptions/{id}/rotate-auth", []string{"auth_value"}, []string{"auth_value"}},
		{http.MethodPost, "/subscriptions/{id}/replay", []string{"from_seq", "to_seq"}, []string{"from_seq"}},
	}

	for _, tt := range tests {
		body, ok := eventingRequestBody(moduleRoute{
			ns: "eventing", method: tt.method, pattern: tt.pattern,
		})
		if !ok {
			t.Fatalf("%s %s requestBody missing", tt.method, tt.pattern)
		}
		if body["required"] != true {
			t.Errorf("%s %s requestBody.required = %#v", tt.method, tt.pattern, body["required"])
		}
		schema := eventingSchemaFromBody(t, body)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s %s is not a closed object: %#v", tt.method, tt.pattern, schema)
		}
		properties := schema["properties"].(map[string]any)
		if got := eventingSortedMapKeys(properties); !reflect.DeepEqual(got, tt.properties) {
			t.Errorf("%s %s properties = %v, want %v", tt.method, tt.pattern, got, tt.properties)
		}
		gotRequired, _ := schema["required"].([]string)
		sort.Strings(gotRequired)
		if !reflect.DeepEqual(gotRequired, tt.required) {
			t.Errorf("%s %s required = %v, want %v", tt.method, tt.pattern, gotRequired, tt.required)
		}
	}
}

func TestEventingSubscriptionRequestBodyIsComplete(t *testing.T) {
	t.Parallel()

	createBody, _ := eventingRequestBody(moduleRoute{
		ns: "eventing", method: http.MethodPost, pattern: "/subscriptions",
	})
	create := eventingSchemaFromBody(t, createBody)
	props := create["properties"].(map[string]any)

	format := props["sink_format"].(map[string]any)
	if got := format["enum"]; !reflect.DeepEqual(got, sinkFormatEnum()) {
		t.Errorf("sink_format enum = %#v, want catalog enum %#v", got, sinkFormatEnum())
	}
	if desc, _ := format["description"].(string); !strings.Contains(desc, string(siemwire.EventingSinkFormats().Default())) {
		t.Errorf("sink_format description %q omits default", desc)
	}
	if got := props["sink_kind"].(map[string]any)["enum"]; !reflect.DeepEqual(got, oaEnum("", "https", "splunk_hec", "sentinel_dcr", "datadog", "newrelic")) {
		t.Errorf("sink_kind enum = %#v", got)
	}

	eventTypes := props["event_types"].(map[string]any)
	if eventTypes["minItems"] != 1 || eventTypes["maxItems"] != 32 {
		t.Errorf("event_types bounds = %#v", eventTypes)
	}
	sinkOpts := props["sink_opts"].(map[string]any)
	if sinkOpts["maxProperties"] != 32 {
		t.Errorf("sink_opts maxProperties = %#v", sinkOpts["maxProperties"])
	}
	if !reflect.DeepEqual(sinkOpts["additionalProperties"], oaObj("type", "string", "maxLength", 2048)) {
		t.Errorf("sink_opts values = %#v", sinkOpts["additionalProperties"])
	}
	if got := len(create["allOf"].([]any)); got != 3 {
		t.Errorf("create conditional count = %d, want header + auth value + sink credential", got)
	}

	updateBody, _ := eventingRequestBody(moduleRoute{
		ns: "eventing", method: http.MethodPut, pattern: "/subscriptions/{id}",
	})
	update := eventingSchemaFromBody(t, updateBody)
	if got := len(update["allOf"].([]any)); got != 1 {
		t.Errorf("update conditional count = %d, want only custom-header requirement", got)
	}
}

func TestEventingRequestBodyDoesNotClaimUnknownRoutes(t *testing.T) {
	t.Parallel()

	for _, route := range []moduleRoute{
		{ns: "eventing", method: http.MethodGet, pattern: "/subscriptions"},
		{ns: "eventing", method: http.MethodPost, pattern: "/unknown"},
		{ns: "evals", method: http.MethodPost, pattern: "/subscriptions"},
	} {
		if decl, ok := eventingRequestBodyDeclarationFor(route); ok {
			t.Errorf("unexpected declaration %#v for %#v", decl, route)
		}
		if body, ok := eventingRequestBody(route); ok || body != nil {
			t.Errorf("unexpected requestBody %#v for %#v", body, route)
		}
	}
}

func eventingSchemaFromBody(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	content := body["content"].(map[string]any)
	media := content["application/json"].(map[string]any)
	return media["schema"].(map[string]any)
}

func eventingSortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
