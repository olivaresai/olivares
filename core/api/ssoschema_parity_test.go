// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// THE PUBLISHED SSO SCHEMAS MUST BE THE GO SHAPES (2026-08-06).
//
// Both stable SSO schemas were fiction. `SSOConfig` published ten properties — enabled,
// issuer, client_id, tenant, auto_provision, enforce, enforce_mfa, default_role — against a
// handler that returns twenty-nine entirely different ones. `SSOConfigInput` declared
// issuer, client_id and tenant REQUIRED while the decoder rejects unknown fields and wants
// oidc_issuer / oidc_client_id / oidc_client_secret. Only `protocol` and `enabled` survived
// by name, so a payload built from the published contract answered 400 invalid JSON body:
// a customer generating an SDK from our stable contract could neither read nor write the
// SSO configuration.
//
// A hand-written document beside a hand-written struct drifts, and the drift is invisible
// because each side is internally consistent. This test removes the hand: the property SET
// of each schema is derived from the struct's json tags by reflection, so adding a field to
// either struct without publishing it — or publishing one that does not exist — is red.
//
// It compares NAMES and the JSON kind, not formats or descriptions. Those are editorial and
// a test that pinned them would be a diff, not a guarantee; the failure this exists to stop
// is a name that no decoder accepts.
func TestPublishedSSOSchemasMatchTheGoShapes(t *testing.T) {
	doc := OpenAPIDocument()
	schemas, ok := doc["components"].(map[string]any)["schemas"].(map[string]any)
	if !ok {
		t.Fatal("the document has no components.schemas; this test would pass vacuously")
	}

	for _, tc := range []struct {
		schema string
		goType any
	}{
		{"SSOConfig", ssoConfigDTO{}},
		{"SSOConfigInput", ssoConfigInput{}},
	} {
		want, wantKind := jsonShapeOf(t, tc.goType)
		got, gotKind := schemaShapeOf(t, schemas, tc.schema)

		if len(want) == 0 {
			t.Fatalf("%s: reflection produced no fields; the struct shape changed and this test stopped discriminating", tc.schema)
		}
		if d := diff(want, got); len(d) > 0 {
			t.Errorf("%s: the Go type carries these and the contract does NOT publish them: %v", tc.schema, d)
		}
		if d := diff(got, want); len(d) > 0 {
			t.Errorf("%s: the contract publishes these and no decoder accepts them: %v", tc.schema, d)
		}
		for _, name := range want {
			w, g := wantKind[name], gotKind[name]
			if g == "" || w == "" {
				continue // covered by the set comparison above
			}
			if w != g {
				t.Errorf("%s.%s: Go kind %q, published %q", tc.schema, name, w, g)
			}
		}
	}
}

// jsonShapeOf returns the json field names of a struct and the JSON type each maps to.
func jsonShapeOf(t *testing.T, v any) ([]string, map[string]string) {
	t.Helper()
	rt := reflect.TypeOf(v)
	names := []string{}
	kinds := map[string]string{}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			continue
		}
		names = append(names, name)
		switch f.Type.Kind() {
		case reflect.Bool:
			kinds[name] = "boolean"
		case reflect.String:
			kinds[name] = "string"
		case reflect.Slice, reflect.Array:
			kinds[name] = "array"
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			kinds[name] = "integer"
		default:
			kinds[name] = "" // not pinned: object/interface shapes are editorial here
		}
	}
	sort.Strings(names)
	return names, kinds
}

// schemaShapeOf returns the property names of a published schema and their declared types.
func schemaShapeOf(t *testing.T, schemas map[string]any, name string) ([]string, map[string]string) {
	t.Helper()
	s, ok := schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("schema %q is not published at all", name)
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema %q has no properties; this test would pass vacuously", name)
	}
	names := []string{}
	kinds := map[string]string{}
	for k, v := range props {
		names = append(names, k)
		if m, ok := v.(map[string]any); ok {
			if ty, ok := m["type"].(string); ok {
				kinds[k] = ty
			}
		}
	}
	sort.Strings(names)
	return names, kinds
}

// diff returns the members of a that are absent from b.
func diff(a, b []string) []string {
	inB := map[string]bool{}
	for _, s := range b {
		inB[s] = true
	}
	out := []string{}
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	return out
}
