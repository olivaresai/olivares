// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestNotifyRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, pattern string
		kind            notifyRequestBodyKind
	}{
		{http.MethodPost, "/routes", notifyBodyful},
		{http.MethodPut, "/routes/{id}", notifyBodyful},
		{http.MethodDelete, "/routes/{id}", notifyBodyless},
		{http.MethodPost, "/routes/{id}/test", notifyBodyless},
		{http.MethodPost, "/routes/{id}/restore", notifyBodyful},
		{http.MethodPost, "/routes/evaluate", notifyBodyful},
		{http.MethodPost, "/outbox/{id}/redeliver", notifyBodyless},
	}
	counts := map[notifyRequestBodyKind]int{}
	for _, test := range tests {
		route := moduleRoute{ns: "notify", method: test.method, pattern: test.pattern}
		decl, ok := notifyRequestBodyDeclarationFor(route)
		if !ok || decl.kind != test.kind {
			t.Fatalf("%s %s = (%#v, %t), want %v", test.method, test.pattern, decl, ok, test.kind)
		}
		_, hasBody := notifyRequestBody(route)
		if hasBody != (test.kind == notifyBodyful) {
			t.Fatalf("%s %s requestBody presence = %t", test.method, test.pattern, hasBody)
		}
		counts[test.kind]++
	}
	want := map[notifyRequestBodyKind]int{notifyBodyful: 4, notifyBodyless: 3}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("census = %#v, want %#v", counts, want)
	}
}

func TestNotifySchemasMatchHandlerDTOs(t *testing.T) {
	t.Parallel()
	create := notifyRouteSchema(true)
	update := notifyRouteSchema(false)
	if create["additionalProperties"] != false || update["additionalProperties"] != false {
		t.Fatal("route decoder is strict")
	}
	if got := capabilitiesSortedStrings(create["required"]); !reflect.DeepEqual(got, []string{"destination", "name"}) {
		t.Fatalf("create required = %v", got)
	}
	if got := capabilitiesSortedStrings(update["required"]); !reflect.DeepEqual(got, []string{"destination"}) {
		t.Fatalf("update required = %v", got)
	}
	evaluate := notifyEvaluateSchema()
	if got := capabilitiesSortedStrings(evaluate["required"]); !reflect.DeepEqual(got, []string{"event_type"}) {
		t.Fatalf("evaluate required = %v", got)
	}
	restore := notifyRestoreSchema()
	if got := capabilitiesSortedStrings(restore["required"]); !reflect.DeepEqual(got, []string{"revision_id"}) {
		t.Fatalf("restore required = %v", got)
	}
}
