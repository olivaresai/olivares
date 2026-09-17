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

func TestSessionsCommunicationPublishedContractDiscriminator(t *testing.T) {
	t.Parallel()
	type expectation struct {
		method, pattern string
		parameters      map[string]bool
		bodyFields      map[string]bool
		statuses        []string
		resultFields    []string
		hasBody         bool
	}
	commonRead := []string{"200", "401", "403", "404", "429", "503"}
	mutation := []string{"200", "400", "401", "403", "404", "409", "412", "428", "429", "503"}
	tests := []expectation{
		{http.MethodGet, "/channels", map[string]bool{"workspace_id": true, "continuation": false, "limit": false}, nil, []string{"200", "400", "401", "403", "404", "429", "503"}, []string{"items", "has_more"}, false},
		{http.MethodPost, "/channels", map[string]bool{}, map[string]bool{"workspace_id": true, "initial_grants": true}, []string{"201", "400", "401", "403", "404", "409", "429", "503"}, []string{"channel", "etag", "audit_seq"}, true},
		{http.MethodPatch, "/channels", map[string]bool{"If-Match": true}, map[string]bool{"channel_id": true}, mutation, []string{"channel", "etag", "audit_seq"}, true},
		{http.MethodGet, "/channels/administration", map[string]bool{"workspace_id": true, "state": false, "continuation": false, "limit": false}, nil, []string{"200", "400", "401", "403", "404", "429", "503"}, []string{"items", "has_more"}, false},
		{http.MethodGet, "/channels/{id}", map[string]bool{}, nil, commonRead, []string{"id", "tenant_id", "workspace_id", "version", "acl_revision"}, false},
		{http.MethodGet, "/channels/{id}/grants", map[string]bool{"workspace_id": true, "state": false, "subject_kind": false, "subject_ref": false, "continuation": false, "limit": false}, nil, []string{"200", "400", "401", "403", "404", "409", "429", "503"}, []string{"channel", "etag", "observed_at", "items", "has_more"}, false},
		{http.MethodPost, "/channels/{id}/grants", map[string]bool{"If-Match": true}, map[string]bool{"subject": true, "can_read": true, "can_write": true, "can_admin": true}, mutation, []string{"channel", "etag", "audit_seq"}, true},
		{http.MethodPost, "/channels/{id}/grants/{grant_id}/revoke", map[string]bool{"If-Match": true}, nil, mutation, []string{"channel", "etag", "audit_seq"}, false},
		{http.MethodPost, "/messages/send", map[string]bool{"Idempotency-Key": true, "If-Plan-Hash": false}, map[string]bool{"channel_id": true, "recipient": true, "content": true, "available_at": false}, []string{"200", "201", "400", "401", "403", "404", "409", "412", "429", "503"}, []string{"command_id", "message_id", "delivery_id", "event_id", "plan_hash", "replayed"}, true},
		{http.MethodGet, "/messages/{id}", map[string]bool{}, nil, commonRead, []string{"message", "delivery", "fulfillment"}, false},
		{http.MethodGet, "/deliveries/{id}", map[string]bool{}, nil, commonRead, []string{"message", "delivery", "fulfillment"}, false},
		{http.MethodGet, "/inbox", map[string]bool{"workspace_id": true, "continuation": false, "limit": false}, nil, []string{"200", "400", "401", "403", "404", "412", "429", "503"}, []string{"items", "has_more"}, false},
		{http.MethodGet, "/inbox/handoffs", map[string]bool{"workspace_id": true, "state": false, "continuation": false, "limit": false}, nil, []string{"200", "400", "401", "403", "404", "429", "503"}, []string{"items", "has_more"}, false},
		{http.MethodGet, "/deliveries/{id}/handoff", map[string]bool{}, nil, commonRead, []string{"handoff", "carrier", "work_item", "observed_at", "deadline_elapsed", "content", "offer_context"}, false},
		{http.MethodGet, "/inbox/cursors/personal/{recipient}", map[string]bool{"workspace_id": true, "target": true}, nil, []string{"200", "400", "401", "403", "404", "412", "429", "503"}, []string{"cursor", "version", "etag"}, false},
		{http.MethodPut, "/inbox/cursors/personal/{recipient}", map[string]bool{"If-Match": true, "Idempotency-Key": true}, map[string]bool{"cursor": true, "delivery_id": true}, []string{"200", "400", "401", "403", "404", "409", "412", "428", "429", "503"}, []string{"command_id", "cursor_id", "version", "etag", "projection", "audit_seq", "replayed"}, true},
		{http.MethodPost, "/deliveries/{id}/ack", map[string]bool{"If-Match": true, "Idempotency-Key": true}, nil, mutation, []string{"command_id", "ack_id", "delivery_id", "event_id", "etag", "replayed"}, false},
		{http.MethodPost, "/handoffs", map[string]bool{"If-Match": true, "Idempotency-Key": true}, map[string]bool{"channel_id": true, "work_item_id": true, "recipient": true, "handoff": true, "ack_deadline": true, "expected_owner_epoch": false}, []string{"200", "201", "400", "401", "403", "404", "409", "412", "428", "429", "503"}, []string{"command_id", "handoff_id", "delivery_id", "work_item_id", "etag", "replayed"}, true},
		{http.MethodPost, "/handoffs/{id}/responses", map[string]bool{"If-Match": true, "Idempotency-Key": true}, map[string]bool{"transition": true}, mutation, []string{"command_id", "handoff_id", "ack_id", "work_item_id", "owner_epoch", "resulting_lease_fence", "replayed"}, true},
	}
	// FIFTEEN operations were published before either composed increment. The
	// incoming-handoff read surface added two and the administrative read model
	// added two more; both sets are additive and none of the fifteen loses
	// coverage. The number is a consequence of the rows above, which are the real
	// census: each row names its method, pattern, parameters, statuses and body.
	if len(tests) != 19 {
		t.Fatalf("communication contract census = %d, want 19 (15 + 2 handoff + 2 administrative)",
			len(tests))
	}
	for _, test := range tests {
		test := test
		t.Run(test.method+" "+test.pattern, func(t *testing.T) {
			route := moduleRoute{ns: "sessions", method: test.method, pattern: test.pattern}
			op := moduleOperation(route)
			if op["x-olivares-sdk-family"] != sessionsCommunicationSDKFamily {
				t.Fatalf("typed SDK discriminator = %#v", op["x-olivares-sdk-family"])
			}
			actualParameters := map[string]bool{}
			for _, raw := range op["parameters"].([]any) {
				parameter := raw.(map[string]any)
				name := parameter["name"].(string)
				if name == "X-Olivares-Tenant" || parameter["in"] == "path" {
					continue
				}
				actualParameters[name], _ = parameter["required"].(bool)
			}
			if !reflect.DeepEqual(actualParameters, test.parameters) {
				t.Fatalf("parameters = %#v, want %#v", actualParameters, test.parameters)
			}
			if _, found := actualParameters["after_delivery_seq"]; found {
				t.Fatal("raw delivery sequence leaked into the public contract")
			}
			_, hasBody := op["requestBody"]
			if hasBody != test.hasBody {
				t.Fatalf("request body presence = %t, want %t", hasBody, test.hasBody)
			}
			if hasBody {
				body := op["requestBody"].(map[string]any)
				schema := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
				if body["required"] != true || schema["type"] != "object" || len(schema["properties"].(map[string]any)) == 0 {
					t.Fatalf("request schema is not concrete and required: %#v", body)
				}
				required := map[string]bool{}
				for _, rawField := range schema["required"].([]any) {
					required[rawField.(string)] = true
				}
				properties := schema["properties"].(map[string]any)
				for field, wantRequired := range test.bodyFields {
					if properties[field] == nil || required[field] != wantRequired {
						t.Errorf("request field %q = schema:%t required:%t, want required:%t",
							field, properties[field] != nil, required[field], wantRequired)
					}
				}
			}
			responses := op["responses"].(map[string]any)
			// The two administrative reads declare `Cache-Control: no-store` on
			// EVERY response they produce, 429 included: the route's declared
			// response metadata is resolved before the global rate limiter can
			// answer, so the published contract and the server agree there too.
			// Every other operation of the family declares no response header at
			// all, so this increment cannot have changed one by accident.
			administrative := test.method == http.MethodGet &&
				(test.pattern == "/channels/administration" || test.pattern == "/channels/{id}/grants")
			if administrative {
				for _, required := range []string{"429", "401"} {
					if _, present := responses[required]; !present {
						t.Errorf("%s %s declares no %s response, so the header census "+
							"below cannot cover it", test.method, test.pattern, required)
					}
				}
			}
			for status, raw := range responses {
				response := raw.(map[string]any)
				headers, declared := response["headers"].(map[string]any)
				switch {
				case !administrative:
					if declared {
						t.Errorf("%s response declares unexpected headers %#v", status, headers)
					}
				case !declared:
					t.Errorf("%s response does not declare Cache-Control: no-store", status)
				default:
					cacheControl, ok := headers["Cache-Control"].(map[string]any)
					if !ok {
						t.Errorf("%s response headers lack Cache-Control: %#v", status, headers)
						continue
					}
					if cacheControl["required"] != true {
						t.Errorf("%s Cache-Control is not required", status)
					}
					schema, ok := cacheControl["schema"].(map[string]any)
					if !ok || !reflect.DeepEqual(schema["enum"], []any{"no-store"}) {
						t.Errorf("%s Cache-Control schema = %#v, want the no-store enum", status, schema)
					}
				}
			}
			statuses := make([]string, 0, len(responses))
			for status := range responses {
				statuses = append(statuses, status)
			}
			sort.Strings(statuses)
			wantStatuses := append([]string(nil), test.statuses...)
			sort.Strings(wantStatuses)
			if !reflect.DeepEqual(statuses, wantStatuses) {
				t.Fatalf("statuses = %v, want %v", statuses, wantStatuses)
			}
			success := "200"
			if responses[success] == nil {
				success = "201"
			}
			schema := responses[success].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			properties := schema["properties"].(map[string]any)
			for _, field := range test.resultFields {
				if properties[field] == nil {
					t.Errorf("success schema missing concrete field %q", field)
				}
			}
		})
	}
}
