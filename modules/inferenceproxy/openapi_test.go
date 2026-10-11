// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/modules/internal/oastest"
)

func TestInferenceProxyRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, pattern string
		kind            inferenceProxyRequestBodyKind
	}{
		{http.MethodPut, "/config", inferenceProxyBodyful},
		{http.MethodPost, "/device/approve", inferenceProxyBodyful},
		{http.MethodPut, "/dlp/rules", inferenceProxyBodyful},
		{http.MethodDelete, "/dlp/rules/{id}", inferenceProxyBodyless},
	}
	counts := map[inferenceProxyRequestBodyKind]int{}
	for _, test := range tests {
		route := operation{method: test.method, pattern: test.pattern}
		decl, ok := inferenceProxyRequestBodyDeclarationFor(route.method, route.pattern)
		if !ok || decl.kind != test.kind {
			t.Fatalf("%s %s = (%#v, %t), want %v", test.method, test.pattern, decl, ok, test.kind)
		}
		_, hasBody := inferenceProxyRequestBody(route.method, route.pattern)
		if hasBody != (test.kind == inferenceProxyBodyful) {
			t.Fatalf("%s %s requestBody presence = %t", test.method, test.pattern, hasBody)
		}
		counts[test.kind]++
	}
	want := map[inferenceProxyRequestBodyKind]int{inferenceProxyBodyful: 3, inferenceProxyBodyless: 1}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("census = %#v, want %#v", counts, want)
	}
}

func TestInferenceProxyContentFirewallResponseContract(t *testing.T) {
	t.Parallel()
	if !inferenceProxyContentFirewallRoute(http.MethodGet, "/content-firewall") {
		t.Fatal("GET /content-firewall is not recognized")
	}
	for _, other := range []operation{
		{method: http.MethodPut, pattern: "/content-firewall"},
		{method: http.MethodGet, pattern: "/config"},
	} {
		if inferenceProxyContentFirewallRoute(other.method, other.pattern) {
			t.Fatalf("%s %s matched the content-firewall contract", other.method, other.pattern)
		}
	}

	published := oastest.Op(t, New(), "get", "/v1/m/inferenceproxy/content-firewall")["responses"].(map[string]any)["200"].(map[string]any)
	if !reflect.DeepEqual(published, inferenceProxyContentFirewallResponse()) {
		t.Fatalf("published 200 = %#v, want the content-firewall contract", published)
	}
	schema := published["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema must be a closed object: %#v", schema)
	}
	if got := oastest.Strings(schema["required"]); !reflect.DeepEqual(got, []string{"note", "pep", "state"}) {
		t.Fatalf("required = %v, want [note pep state]", got)
	}
	properties := schema["properties"].(map[string]any)
	if len(properties) != 3 {
		t.Fatalf("property count = %d, want 3", len(properties))
	}
	if got := properties["pep"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []any{"messages_proxy"}) {
		t.Fatalf("pep enum = %#v", got)
	}
	wantStates := []any{"unobserved", "pep_not_composed", "inspector_absent", "inspector_attached"}
	if got := properties["state"].(map[string]any)["enum"]; !reflect.DeepEqual(got, wantStates) {
		t.Fatalf("state enum = %#v, want %#v", got, wantStates)
	}
	if properties["note"].(map[string]any)["type"] != "string" {
		t.Fatalf("note = %#v", properties["note"])
	}

	config := oastest.Op(t, New(), "get", "/v1/m/inferenceproxy/config")["responses"].(map[string]any)["200"]
	if !reflect.DeepEqual(config, oas.JSONResp("OK")) {
		t.Fatalf("GET /config 200 changed: %#v", config)
	}
}

func TestInferenceProxySchemasMatchHandlerDTOs(t *testing.T) {
	t.Parallel()
	config := inferenceProxyConfigSchema()
	if config["additionalProperties"] != false {
		t.Fatal("config decoder is strict")
	}
	if _, required := config["required"]; required {
		t.Fatal("PUT config accepts an empty JSON object")
	}
	if _, conditional := config["allOf"]; !conditional {
		t.Fatal("ceiling enforcement condition is missing")
	}
	device := inferenceProxyDeviceApprovalSchema()
	if _, required := device["required"]; required {
		t.Fatal("empty user_code is handled as not found, not rejected by decoding")
	}
	dlp := inferenceProxyDLPRuleSchema()
	if got := oastest.Strings(dlp["required"]); !reflect.DeepEqual(got, []string{"action", "class"}) {
		t.Fatalf("DLP required = %v", got)
	}
	if got := len(dlp["properties"].(map[string]any)); got != 5 {
		t.Fatalf("DLP property count = %d, want 5", got)
	}
}

// operation is one route of this module: method and module-relative pattern.
type operation struct{ method, pattern string }
