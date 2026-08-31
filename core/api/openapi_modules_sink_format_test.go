// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package api_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// eventingRoutesModule registers the real subscription-authoring patterns under
// the real eventing namespace, with nil handlers: the beta document is built
// from the REGISTRATION, and the builder keys the one declared request body on
// (namespace, method, pattern). core/api cannot import modules/eventing (the
// dependency points the other way), which is exactly why the enum must come
// from the sdk/siemwire catalog both sides derive from.
type eventingRoutesModule struct{}

func (eventingRoutesModule) APINamespace() string { return "eventing" }
func (eventingRoutesModule) Permissions() []auth.Permission {
	return []auth.Permission{"eventing:sub:read", "eventing:sub:write"}
}
func (eventingRoutesModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/subscriptions", "eventing:sub:read", nil)
	reg.Handle("POST", "/subscriptions", "eventing:sub:write", nil)
	reg.Handle("PUT", "/subscriptions/{id}", "eventing:sub:write", nil)
}

// TestEventingSubscriptionBodyDeclaresTheCatalogEnum pins the eventing
// subscription authoring routes: sink_format is rendered from the SDK catalog
// (empty spelling first — unset selects the surface default) and sink_kind is
// the closed vocabulary enforced by the handler.
func TestEventingSubscriptionBodyDeclaresTheCatalogEnum(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{eventingRoutesModule{}, betaTestModule{}})
	paths := doc["paths"].(map[string]any)

	set := siemwire.EventingSinkFormats()
	wantEnum := []any{""}
	for _, tok := range set.Tokens() {
		wantEnum = append(wantEnum, string(tok))
	}

	for _, tc := range []struct{ method, path string }{
		{"post", "/v1/m/eventing/subscriptions"},
		{"put", "/v1/m/eventing/subscriptions/{id}"},
	} {
		op := mustOp(t, paths, tc.path, tc.method)
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

	// The declaration is keyed, not sprayed: the list route and another
	// namespace's POST stay without a request body.
	for _, tc := range []struct{ method, path string }{
		{"get", "/v1/m/eventing/subscriptions"},
		{"post", "/v1/m/demoapi/things"},
	} {
		op := mustOp(t, paths, tc.path, tc.method)
		if _, ok := op["requestBody"]; ok {
			t.Errorf("%s %s: unexpected requestBody — the declaration must stay keyed to the subscription authoring routes",
				tc.method, tc.path)
		}
	}
}
