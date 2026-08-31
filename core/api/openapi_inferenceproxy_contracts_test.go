// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"testing"
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
		route := moduleRoute{ns: "inferenceproxy", method: test.method, pattern: test.pattern}
		decl, ok := inferenceProxyRequestBodyDeclarationFor(route)
		if !ok || decl.kind != test.kind {
			t.Fatalf("%s %s = (%#v, %t), want %v", test.method, test.pattern, decl, ok, test.kind)
		}
		_, hasBody := inferenceProxyRequestBody(route)
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
	if got := capabilitiesSortedStrings(dlp["required"]); !reflect.DeepEqual(got, []string{"action", "class"}) {
		t.Fatalf("DLP required = %v", got)
	}
	if got := len(dlp["properties"].(map[string]any)); got != 5 {
		t.Fatalf("DLP property count = %d, want 5", got)
	}
}
