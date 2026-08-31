// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestRecordingRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, pattern string
		kind            recordingRequestBodyKind
	}{
		{http.MethodPost, "/ack", recordingBodyless},
		{http.MethodPost, "/sessions/{id}/seal", recordingBodyless},
		{http.MethodPost, "/sessions/{id}/summarize", recordingBodyless},
		{http.MethodPost, "/sweep", recordingBodyless},
		{http.MethodPut, "/config", recordingBodyful},
	}
	counts := map[recordingRequestBodyKind]int{}
	for _, test := range tests {
		route := moduleRoute{ns: "recording", method: test.method, pattern: test.pattern}
		decl, ok := recordingRequestBodyDeclarationFor(route)
		if !ok || decl.kind != test.kind {
			t.Fatalf("%s %s = (%#v, %t), want %v", test.method, test.pattern, decl, ok, test.kind)
		}
		_, hasBody := recordingRequestBody(route)
		if hasBody != (test.kind == recordingBodyful) {
			t.Fatalf("%s %s requestBody presence = %t", test.method, test.pattern, hasBody)
		}
		counts[test.kind]++
	}
	want := map[recordingRequestBodyKind]int{recordingBodyful: 1, recordingBodyless: 4}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("census = %#v, want %#v", counts, want)
	}
}

func TestRecordingConfigSchemaMatchesHandlerDTO(t *testing.T) {
	t.Parallel()
	schema := recordingConfigSchema()
	if schema["additionalProperties"] != false {
		t.Fatal("recording config decoder is strict")
	}
	if got := capabilitiesSortedStrings(schema["required"]); !reflect.DeepEqual(got, []string{"consent", "idle_seconds", "retention_days"}) {
		t.Fatalf("required = %v", got)
	}
	properties := schema["properties"].(map[string]any)
	if got := len(properties); got != 5 {
		t.Fatalf("property count = %d, want 5", got)
	}
	idle := properties["idle_seconds"].(map[string]any)
	if idle["minimum"] != 60 || idle["maximum"] != 86400 {
		t.Fatalf("idle bounds = %#v", idle)
	}
}
