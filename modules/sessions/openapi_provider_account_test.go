// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

func TestProviderAccountOpenAPIMetadataContract(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{New()})
	op := doc["paths"].(map[string]any)["/v1/m/sessions/provider-accounts/{ref}"].(map[string]any)["patch"].(map[string]any)
	body := op["requestBody"].(map[string]any)
	schema := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if body["required"] != true || schema["additionalProperties"] != false || schema["minProperties"] != 1 || schema["required"] != nil {
		t.Fatalf("metadata body must be closed and require at least one field: %v", body)
	}
	properties := schema["properties"].(map[string]any)
	if len(properties) != 3 || properties["name"].(map[string]any)["type"] != "string" || properties["display_name"].(map[string]any)["type"] != "string" || !reflect.DeepEqual(properties["accent"].(map[string]any)["enum"], []any{"", "orange", "green", "amber", "red", "blue"}) {
		t.Fatalf("metadata patch accepts fields other than a string label: %v", properties)
	}
	responses := op["responses"].(map[string]any)
	for _, code := range []string{"200", "404", "409", "422"} {
		if responses[code] == nil {
			t.Errorf("account patch omits response %s", code)
		}
	}
	// Only a malformed name is 422; a bad label or accent is 400.
	if got := responses["422"].(map[string]any)["description"]; got != "Invalid account name" {
		t.Errorf("account patch 422 = %q, want %q", got, "Invalid account name")
	}
}
func TestProviderAccountOpenAPIFailureContract(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{New()})
	op := doc["paths"].(map[string]any)["/v1/m/sessions/provider-accounts"].(map[string]any)["post"].(map[string]any)
	properties := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)
	if properties["idempotency_key"] == nil {
		t.Error("account create omits retry identity")
	}
	responses := op["responses"].(map[string]any)
	for _, code := range []string{"201", "403", "409", "422", "503"} {
		if responses[code] == nil {
			t.Errorf("account create omits response %s", code)
		}
	}
	if responses["200"] != nil {
		t.Error("account create advertises an unsupported 200")
	}
}
