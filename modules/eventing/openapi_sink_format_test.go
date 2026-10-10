// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package eventing_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/modules/internal/oastest"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// TestEventingSubscriptionBodyDeclaresTheCatalogEnum pins the eventing
// subscription authoring routes: sink_format is rendered from the SDK catalog
// (empty spelling first — unset selects the surface default) and sink_kind is
// the closed vocabulary enforced by the handler.
func TestEventingSubscriptionBodyDeclaresTheCatalogEnum(t *testing.T) {
	module := eventing.New()

	set := siemwire.EventingSinkFormats()
	wantEnum := []any{""}
	for _, tok := range set.Tokens() {
		wantEnum = append(wantEnum, string(tok))
	}

	for _, tc := range []struct{ method, path string }{
		{"post", "/v1/m/eventing/subscriptions"},
		{"put", "/v1/m/eventing/subscriptions/{id}"},
	} {
		op := oastest.Op(t, module, tc.method, tc.path)
		body, ok := op["requestBody"].(map[string]any)
		if !ok {
			t.Fatalf("%s %s: requestBody missing", tc.method, tc.path)
		}
		if body["required"] != true {
			t.Errorf("%s %s: requestBody must be required — the handler rejects an absent body as 400",
				tc.method, tc.path)
		}
		schema := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		props := schema["properties"].(map[string]any)

		sf := props["sink_format"].(map[string]any)
		if got := sf["enum"]; !reflect.DeepEqual(got, wantEnum) {
			t.Errorf("%s %s: sink_format enum = %v, want the catalog's eventing surface %v",
				tc.method, tc.path, got, wantEnum)
		}
		if desc, _ := sf["description"].(string); !strings.Contains(desc, string(set.Default())) {
			t.Errorf("%s %s: sink_format description %q does not name the surface default %q",
				tc.method, tc.path, desc, set.Default())
		}

		sk, ok := props["sink_kind"].(map[string]any)
		if !ok {
			t.Fatalf("%s %s: sink_kind property missing", tc.method, tc.path)
		}
		wantKinds := []any{"", "https", "splunk_hec", "sentinel_dcr", "datadog", "newrelic"}
		if got := sk["enum"]; !reflect.DeepEqual(got, wantKinds) {
			t.Errorf("%s %s: sink_kind enum = %v, want handler vocabulary %v",
				tc.method, tc.path, got, wantKinds)
		}
	}

	// The declaration is keyed, not sprayed: the list route stays without a
	// request body.
	if _, ok := oastest.Op(t, module, "get", "/v1/m/eventing/subscriptions")["requestBody"]; ok {
		t.Error("GET /v1/m/eventing/subscriptions: unexpected requestBody — the declaration must stay keyed to the subscription authoring routes")
	}
}
