// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package agenttoolsapi

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestOperationDocumentationRequiredFields(t *testing.T) {
	for _, tt := range []struct {
		pattern  string
		required []string
	}{
		{"/ollama/start", nil},
		{"/plans", []string{"driver", "version"}},
		{"/sign-in", []string{"driver", "tenant_id"}},
	} {
		t.Run(tt.pattern, func(t *testing.T) {
			doc, ok := new(Module).OperationDocumentation("POST", tt.pattern)
			if !ok {
				t.Fatal("operation documentation missing")
			}
			data, err := json.Marshal(doc.RequestBody)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Content map[string]struct {
					Schema struct {
						Type     string          `json:"type"`
						Required json.RawMessage `json:"required"`
					} `json:"schema"`
				} `json:"content"`
			}
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatal(err)
			}
			schema := body.Content["application/json"].Schema
			if schema.Type != "object" {
				t.Fatalf("schema type = %q, want object", schema.Type)
			}
			raw := schema.Required
			if string(raw) == "null" {
				t.Fatal("schema.required must be an array or omitted, got null")
			}
			var got []string
			if len(raw) != 0 {
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(got, tt.required) {
				t.Fatalf("required = %v, want %v", got, tt.required)
			}
		})
	}
}
