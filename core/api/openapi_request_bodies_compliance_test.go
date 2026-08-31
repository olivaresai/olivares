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
)

func TestComplianceRequestBodiesClassifyEveryMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method  string
		pattern string
		kind    complianceRequestBodyKind
	}{
		{http.MethodDelete, "/aims/pack/{id}", complianceBodyless},
		{http.MethodDelete, "/depth/fedramp/{id}", complianceBodyless},
		{http.MethodDelete, "/depth/sector/{id}", complianceBodyless},
		{http.MethodDelete, "/depth/us-law/{id}", complianceBodyless},
		{http.MethodDelete, "/dora/incidents/{id}", complianceBodyless},
		{http.MethodDelete, "/dora/register/{id}", complianceBodyless},
		{http.MethodDelete, "/nis2/incidents/{id}", complianceBodyless},
		{http.MethodDelete, "/oscal/profiles/{id}", complianceBodyless},
		{http.MethodDelete, "/retention/policies/{class}", complianceBodyless},
		{http.MethodPost, "/aims/pack", complianceBodyOpaque},
		{http.MethodPost, "/claude-files/{id}/erase", complianceBodyful},
		{http.MethodPost, "/data-subjects/{id}/erase", complianceBodyful},
		{http.MethodPost, "/depth/ccm/drift", complianceBodyless},
		{http.MethodPost, "/depth/ccm/snapshot", complianceBodyful},
		{http.MethodPost, "/depth/fedramp", complianceBodyOpaque},
		{http.MethodPost, "/depth/sector", complianceBodyOpaque},
		{http.MethodPost, "/depth/us-law", complianceBodyOpaque},
		{http.MethodPost, "/dora/incidents", complianceBodyOpaque},
		{http.MethodPost, "/dora/register", complianceBodyOpaque},
		{http.MethodPost, "/erasure", complianceBodyful},
		{http.MethodPost, "/erasure/{id}/execute", complianceBodyful},
		{http.MethodPost, "/frameworks/{id}/evidence", complianceBodyful},
		{http.MethodPost, "/holds", complianceBodyful},
		{http.MethodPost, "/holds/{id}/release", complianceBodyful},
		{http.MethodPost, "/nis2/incidents/classify", complianceBodyOpaque},
		{http.MethodPost, "/oscal/profiles", complianceBodyOpaque},
		{http.MethodPost, "/residency", complianceBodyful},
		{http.MethodPost, "/residency/scan", complianceBodyless},
		{http.MethodPost, "/retention/sweep", complianceBodyless},
		{http.MethodPost, "/risk/classify", complianceBodyful},
		{http.MethodPost, "/risk/{id}/review", complianceBodyful},
		{http.MethodPut, "/nis2/incidents/{id}", complianceBodyful},
		{http.MethodPut, "/retention/policies/{class}", complianceBodyful},
	}

	wantCounts := map[complianceRequestBodyKind]int{
		complianceBodyful:    13,
		complianceBodyless:   12,
		complianceBodyOpaque: 8,
	}
	gotCounts := make(map[complianceRequestBodyKind]int)
	seen := make(map[string]struct{}, len(tests))
	for _, tt := range tests {
		key := tt.method + " " + tt.pattern
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate mutation in test catalog: %s", key)
		}
		seen[key] = struct{}{}

		decl, ok := complianceRequestBodyDeclarationFor(moduleRoute{
			ns: "compliance", method: tt.method, pattern: tt.pattern,
		})
		if !ok {
			t.Errorf("%s is not classified", key)
			continue
		}
		if decl.kind != tt.kind {
			t.Errorf("%s kind = %v, want %v", key, decl.kind, tt.kind)
		}
		gotCounts[decl.kind]++

		body, hasBody := complianceRequestBody(moduleRoute{
			ns: "compliance", method: tt.method, pattern: tt.pattern,
		})
		wantBody := tt.kind == complianceBodyful || tt.kind == complianceBodyOpaque
		if hasBody != wantBody {
			t.Errorf("%s requestBody present = %t, want %t", key, hasBody, wantBody)
		}
		if !hasBody && body != nil {
			t.Errorf("%s returned a body with ok=false: %#v", key, body)
		}
	}
	if !reflect.DeepEqual(gotCounts, wantCounts) {
		t.Fatalf("classification counts = %#v, want %#v", gotCounts, wantCounts)
	}
	if len(seen) != 33 {
		t.Fatalf("classified %d mutations, want 33", len(seen))
	}
}

func TestComplianceRequestBodiesMatchHandlerDTOs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method       string
		pattern      string
		bodyRequired bool
		properties   []string
		required     []string
	}{
		{http.MethodPost, "/frameworks/{id}/evidence", false, []string{"scope_note"}, nil},
		{http.MethodPost, "/depth/ccm/snapshot", false, []string{"frameworks", "scope_note"}, nil},
		{http.MethodPost, "/residency", true, []string{"data_classes", "encryption_at_rest", "note", "perimeter", "region", "self_hosted"}, []string{"region"}},
		{http.MethodPost, "/risk/classify", true, []string{"agent_id", "subject_kind", "subject_ref"}, []string{"subject_ref"}},
		{http.MethodPost, "/risk/{id}/review", true, []string{"note", "tier"}, []string{"tier"}},
		{http.MethodPut, "/nis2/incidents/{id}", true, []string{"note", "phase"}, nil},
		{http.MethodPut, "/retention/policies/{class}", true, []string{"basis", "disposition", "enabled", "retention_days"}, []string{"disposition", "retention_days"}},
		{http.MethodPost, "/holds", true, []string{"data_class", "matter_ref", "on_behalf_of", "reason", "scope_kind", "subject_kind", "subject_ref", "title"}, []string{"matter_ref", "reason", "scope_kind"}},
		{http.MethodPost, "/holds/{id}/release", false, []string{"on_behalf_of", "reason"}, nil},
		{http.MethodPost, "/erasure", true, []string{"aliases", "case_ref", "data_classes", "reason", "subject_kind", "subject_ref"}, []string{"case_ref", "subject_kind", "subject_ref"}},
		{http.MethodPost, "/erasure/{id}/execute", false, []string{"provider_user_ids", "reason"}, nil},
		{http.MethodPost, "/data-subjects/{id}/erase", false, []string{"aliases", "case_ref", "data_classes", "provider_user_ids", "reason", "subject_kind"}, nil},
		{http.MethodPost, "/claude-files/{id}/erase", false, []string{"reason"}, nil},
	}

	for _, tt := range tests {
		row := tt
		t.Run(strings.TrimPrefix(row.pattern, "/"), func(t *testing.T) {
			body, ok := complianceRequestBody(moduleRoute{
				ns: "compliance", method: row.method, pattern: row.pattern,
			})
			if !ok {
				t.Fatal("requestBody missing")
			}
			if got := body["required"]; got != row.bodyRequired {
				t.Fatalf("requestBody.required = %#v, want %t", got, row.bodyRequired)
			}
			content := body["content"].(map[string]any)
			media := content["application/json"].(map[string]any)
			schema := media["schema"].(map[string]any)
			if schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatalf("schema is not a closed object: %#v", schema)
			}
			properties := schema["properties"].(map[string]any)
			if got := complianceSortedMapKeys(properties); !reflect.DeepEqual(got, row.properties) {
				t.Errorf("properties = %v, want %v", got, row.properties)
			}
			gotRequired, _ := schema["required"].([]string)
			sort.Strings(gotRequired)
			if !reflect.DeepEqual(gotRequired, row.required) {
				t.Errorf("required = %v, want %v", gotRequired, row.required)
			}
		})
	}
}

func TestComplianceRequestBodyClosedVocabularies(t *testing.T) {
	t.Parallel()

	assertProperty := func(method, pattern, property string) map[string]any {
		t.Helper()
		body, ok := complianceRequestBody(moduleRoute{
			ns: "compliance", method: method, pattern: pattern,
		})
		if !ok {
			t.Fatalf("%s %s requestBody missing", method, pattern)
		}
		content := body["content"].(map[string]any)
		media := content["application/json"].(map[string]any)
		schema := media["schema"].(map[string]any)
		properties := schema["properties"].(map[string]any)
		return properties[property].(map[string]any)
	}

	if got := assertProperty(http.MethodPost, "/risk/{id}/review", "tier")["enum"]; !reflect.DeepEqual(got, oaEnum("unacceptable", "high", "limited", "minimal")) {
		t.Errorf("risk tier enum = %#v", got)
	}
	if got := assertProperty(http.MethodPut, "/nis2/incidents/{id}", "phase")["enum"]; !reflect.DeepEqual(got, oaEnum("early_warning", "notification", "intermediate", "final")) {
		t.Errorf("NIS2 phase enum = %#v", got)
	}
	retentionDays := assertProperty(http.MethodPut, "/retention/policies/{class}", "retention_days")
	if retentionDays["minimum"] != 1 || retentionDays["maximum"] != 36500 {
		t.Errorf("retention day bounds = %#v", retentionDays)
	}
	if got := assertProperty(http.MethodPost, "/erasure", "subject_kind")["enum"]; !reflect.DeepEqual(got, oaEnum("user", "agent", "session", "document", "identity")) {
		t.Errorf("erasure subject kind enum = %#v", got)
	}
}

func TestComplianceOpaqueBodiesPublishOnlyRawJSON(t *testing.T) {
	t.Parallel()

	patterns := []string{
		"/oscal/profiles",
		"/dora/register",
		"/dora/incidents",
		"/aims/pack",
		"/depth/us-law",
		"/depth/sector",
		"/depth/fedramp",
		"/nis2/incidents/classify",
	}
	for _, pattern := range patterns {
		decl, ok := complianceRequestBodyDeclarationFor(moduleRoute{
			ns: "compliance", method: http.MethodPost, pattern: pattern,
		})
		if !ok || decl.kind != complianceBodyOpaque {
			t.Errorf("POST %s declaration = %#v, %t", pattern, decl, ok)
			continue
		}
		if !decl.required || len(decl.schema) != 0 {
			t.Errorf("POST %s opaque declaration = %#v", pattern, decl)
		}

		body, hasBody := complianceRequestBody(moduleRoute{
			ns: "compliance", method: http.MethodPost, pattern: pattern,
		})
		if !hasBody {
			t.Errorf("POST %s opaque requestBody missing", pattern)
			continue
		}
		if body["required"] != true {
			t.Errorf("POST %s requestBody.required = %#v, want true", pattern, body["required"])
		}
		content := body["content"].(map[string]any)
		if got := complianceSortedMapKeys(content); !reflect.DeepEqual(got, []string{"application/json"}) {
			t.Errorf("POST %s media types = %v, want [application/json]", pattern, got)
			continue
		}
		media := content["application/json"].(map[string]any)
		schema := media["schema"].(map[string]any)
		if len(schema) != 0 {
			t.Errorf("POST %s opaque schema invents constraints: %#v", pattern, schema)
		}
	}
}

func TestComplianceRequestBodyDoesNotClaimUnknownRoutes(t *testing.T) {
	t.Parallel()

	for _, route := range []moduleRoute{
		{ns: "compliance", method: http.MethodGet, pattern: "/residency"},
		{ns: "compliance", method: http.MethodPost, pattern: "/unknown"},
		{ns: "governance", method: http.MethodPost, pattern: "/holds"},
	} {
		if decl, ok := complianceRequestBodyDeclarationFor(route); ok {
			t.Errorf("unexpected declaration %#v for %#v", decl, route)
		}
		if body, ok := complianceRequestBody(route); ok || body != nil {
			t.Errorf("unexpected requestBody %#v for %#v", body, route)
		}
	}
}

func complianceSortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
