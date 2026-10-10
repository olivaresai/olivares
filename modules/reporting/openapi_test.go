// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/modules/internal/oastest"
)

func TestReportingRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, pattern string
		kind            reportingRequestBodyKind
		mediaType       string
	}{
		{http.MethodPost, "/schedules", reportingBodyful, "application/json"},
		{http.MethodDelete, "/schedules/{id}", reportingBodyless, ""},
		{http.MethodPut, "/branding", reportingBodyful, "application/json"},
		{http.MethodPut, "/templates/{type}", reportingBodyful, "text/html"},
		{http.MethodDelete, "/templates/{type}", reportingBodyless, ""},
	}
	counts := map[reportingRequestBodyKind]int{}
	for _, test := range tests {
		route := operation{method: test.method, pattern: test.pattern}
		decl, ok := reportingRequestBodyDeclarationFor(route.method, route.pattern)
		if !ok || decl.kind != test.kind || decl.mediaType != test.mediaType {
			t.Fatalf("%s %s = (%#v, %t)", test.method, test.pattern, decl, ok)
		}
		_, hasBody := reportingRequestBody(route.method, route.pattern)
		if hasBody != (test.kind == reportingBodyful) {
			t.Fatalf("%s %s requestBody presence = %t", test.method, test.pattern, hasBody)
		}
		counts[test.kind]++
	}
	want := map[reportingRequestBodyKind]int{reportingBodyful: 3, reportingBodyless: 2}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("census = %#v, want %#v", counts, want)
	}
}

func TestReportingSchemasMatchHandlerDecoders(t *testing.T) {
	t.Parallel()
	schedule := reportingScheduleSchema()
	if schedule["additionalProperties"] != true {
		t.Fatal("schedule decoder intentionally accepts unknown fields")
	}
	if got := oastest.Strings(schedule["required"]); !reflect.DeepEqual(got, []string{"cron", "report_type"}) {
		t.Fatalf("schedule required = %v", got)
	}
	branding := reportingBrandingSchema()
	if branding["additionalProperties"] != true {
		t.Fatal("branding decoder intentionally accepts unknown fields")
	}
	templateRoute := operation{method: http.MethodPut, pattern: "/templates/{type}"}
	body, ok := reportingRequestBody(templateRoute.method, templateRoute.pattern)
	if !ok {
		t.Fatal("template body missing")
	}
	if _, ok := body["content"].(map[string]any)["text/html"]; !ok {
		t.Fatalf("template media type = %#v", body["content"])
	}
}

// operation is one route of this module: method and module-relative pattern.
type operation struct{ method, pattern string }
