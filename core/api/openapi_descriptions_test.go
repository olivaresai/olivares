// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"strings"
	"testing"
)

func TestFirstOperationSentenceKeepsNestedPunctuation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		description string
		want        string
	}{
		{
			name:        "comma remains part of the sentence",
			description: "Lists catalog entries, optionally filtered by kind/status/slug.",
			want:        "Lists catalog entries, optionally filtered by kind/status/slug.",
		},
		{
			name: "period in parentheses",
			description: "Returns the policy (for example v1. default). " +
				"The policy is tenant-scoped.",
			want: "Returns the policy (for example v1. default).",
		},
		{
			name: "period in quoted literal",
			description: "Returns the \"ready. waiting\" state. " +
				"The state comes from the worker.",
			want: "Returns the \"ready. waiting\" state.",
		},
		{
			name:        "period in version",
			description: "Returns schema version 1.2 for the tenant.",
			want:        "Returns schema version 1.2 for the tenant.",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := firstOperationSentence(test.description); got != test.want {
				t.Fatalf("firstOperationSentence() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestModuleOperationSummariesPreserveSourceDistinctions(t *testing.T) {
	t.Parallel()

	descriptions := map[string]string{
		"PUT /v1/m/catalog/connector-admission/policy": "Upserts the per-tenant trust root. Admin, audited.",
		"PUT /v1/m/catalog/mcp-admission/policy":       "Upserts the per-tenant trust root. Admin, audited.",
		"PUT /v1/m/models/admission-policy":            "Upserts the per-tenant trust root. Admin-tier, audited.",
		"GET /v1/m/catalog/entries":                    "Lists catalog entries, optionally filtered by kind/status/slug.",
		"GET /v1/agents":                               "Lists agents. This stable description is not a beta summary.",
	}
	handlerDocKeys := map[string]struct{}{
		"PUT /v1/m/catalog/connector-admission/policy": {},
		"PUT /v1/m/catalog/mcp-admission/policy":       {},
		"PUT /v1/m/models/admission-policy":            {},
		"GET /v1/m/catalog/entries":                    {},
		"GET /v1/agents":                               {},
	}

	got := moduleOperationSummaries(descriptions, handlerDocKeys)
	want := map[string]string{
		"PUT /v1/m/catalog/connector-admission/policy": "Upserts the per-tenant trust root. Admin, audited.",
		"PUT /v1/m/catalog/mcp-admission/policy":       "Upserts the per-tenant trust root. Admin, audited.",
		"PUT /v1/m/models/admission-policy":            "Upserts the per-tenant trust root. Admin-tier, audited.",
		"GET /v1/m/catalog/entries":                    "Lists catalog entries, optionally filtered by kind/status/slug.",
	}
	for key, summary := range want {
		if got[key] != summary {
			t.Errorf("summary for %s = %q, want %q", key, got[key], summary)
		}
	}
	if _, ok := got["GET /v1/agents"]; ok {
		t.Error("stable operation received a derived beta summary")
	}
	if got["PUT /v1/m/catalog/connector-admission/policy"] !=
		got["PUT /v1/m/catalog/mcp-admission/policy"] {
		t.Error("identical handler-backed descriptions did not retain identical summaries")
	}
}

func TestHandlerDocDescriptionsAllDeriveSummaries(t *testing.T) {
	t.Parallel()

	summaries := moduleOperationSummaries(
		operationDescriptions,
		handlerDocOperationDescriptions,
	)
	seen := 0
	for key := range handlerDocOperationDescriptions {
		_, path, ok := strings.Cut(key, " ")
		if !ok || !strings.HasPrefix(path, "/v1/m/") {
			t.Errorf("handler-doc provenance contains non-beta operation %q", key)
			continue
		}
		seen++
		description, ok := operationDescriptions[key]
		if !ok {
			t.Errorf("%s has handler-doc provenance but no description", key)
			continue
		}
		summary, ok := summaries[key]
		if !ok || summary == "" {
			t.Errorf("%s has no derived summary", key)
			continue
		}
		if !strings.HasPrefix(normalizeOperationProse(description), summary) {
			t.Errorf("%s summary %q is not source prose", key, summary)
		}
	}
	if seen == 0 {
		t.Fatal("generated catalog contains no beta operations")
	}
}

func TestPublishedOperationDescriptionsHidePrivateImplementationNames(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		"qbusiness:",
		"enterprise packager",
		"enterprise depth packager",
		"enterprise provider builder",
		"enterprise report engine",
	}
	for key, description := range operationDescriptions {
		lower := strings.ToLower(description)
		for _, fragment := range forbidden {
			if strings.Contains(lower, fragment) {
				t.Errorf("%s publishes private implementation fragment %q", key, fragment)
			}
		}
	}
}

func TestApplyOperationDescriptionsFiltersCatalogOnlySummaries(t *testing.T) {
	t.Parallel()

	paths := map[string]any{
		"/v1/agents": map[string]any{
			"get": map[string]any{"summary": "List agents"},
		},
		"/v1/m/finops/spend": map[string]any{
			"get": map[string]any{"summary": "finops module route"},
		},
		"/v1/m/catalog/entries": map[string]any{
			"get": map[string]any{"summary": "catalog module route"},
		},
	}
	applyOperationDescriptions(paths)

	stable := paths["/v1/agents"].(map[string]any)["get"].(map[string]any)
	if stable["summary"] != "List agents" {
		t.Fatalf("stable summary = %q, want %q", stable["summary"], "List agents")
	}
	catalogOnly := paths["/v1/m/finops/spend"].(map[string]any)["get"].(map[string]any)
	if catalogOnly["summary"] != "finops module route" {
		t.Fatalf("catalog-only summary = %q, want registration summary", catalogOnly["summary"])
	}
	handlerDoc := paths["/v1/m/catalog/entries"].(map[string]any)["get"].(map[string]any)
	want := "Lists catalog entries, optionally filtered by kind/status/slug."
	if handlerDoc["summary"] != want {
		t.Fatalf("handler-doc summary = %q, want %q", handlerDoc["summary"], want)
	}
}
