// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"sort"
	"testing"
)

func TestCapabilitiesRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, pattern string
		kind            capabilitiesRequestBodyKind
	}{
		{http.MethodPost, "/configs", capabilitiesBodyful},
		{http.MethodPut, "/configs/{id}", capabilitiesBodyful},
		{http.MethodDelete, "/configs/{id}", capabilitiesBodyless},
		{http.MethodPost, "/toolpins/approve", capabilitiesBodyful},
		{http.MethodPost, "/toolpins/unpin", capabilitiesBodyful},
	}
	counts := map[capabilitiesRequestBodyKind]int{}
	for _, test := range tests {
		route := moduleRoute{ns: "capabilities", method: test.method, pattern: test.pattern}
		decl, ok := capabilitiesRequestBodyDeclarationFor(route)
		if !ok || decl.kind != test.kind {
			t.Fatalf("%s %s = (%#v, %t), want %v", test.method, test.pattern, decl, ok, test.kind)
		}
		body, found := capabilitiesRequestBody(route)
		if (test.kind == capabilitiesBodyful) != found || (!found && body != nil) {
			t.Fatalf("%s %s requestBody = (%#v, %t)", test.method, test.pattern, body, found)
		}
		counts[test.kind]++
	}
	want := map[capabilitiesRequestBodyKind]int{capabilitiesBodyful: 4, capabilitiesBodyless: 1}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("census = %#v, want %#v", counts, want)
	}
}

func TestCapabilitiesSchemasMatchDecoderBoundaries(t *testing.T) {
	t.Parallel()
	create := capabilitiesConfigSchema(true)
	update := capabilitiesConfigSchema(false)
	if create["additionalProperties"] != false || update["additionalProperties"] != false {
		t.Fatal("config decoder is strict")
	}
	if got := capabilitiesSortedStrings(create["required"]); !reflect.DeepEqual(got, []string{"server_ref", "transport"}) {
		t.Fatalf("create required = %v", got)
	}
	if got := capabilitiesSortedStrings(update["required"]); !reflect.DeepEqual(got, []string{"transport"}) {
		t.Fatalf("update required = %v", got)
	}
	for _, approve := range []bool{true, false} {
		pin := capabilitiesToolPinSchema(approve)
		if pin["additionalProperties"] != true {
			t.Fatal("tool-pin decoder must remain open")
		}
		if got := capabilitiesSortedStrings(pin["required"]); !reflect.DeepEqual(got, []string{"expected_version", "tool"}) {
			t.Fatalf("tool-pin required = %v", got)
		}
	}
	if _, ok := capabilitiesToolPinSchema(true)["anyOf"]; !ok {
		t.Fatal("approve must require an explicit or reviewed drift fingerprint")
	}
}

func capabilitiesSortedStrings(value any) []string {
	values, _ := value.([]any)
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.(string))
	}
	sort.Strings(out)
	return out
}
