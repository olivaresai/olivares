// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import "testing"

func TestAgentToolsOfflineOpenAPICarriesAllSystemRoutes(t *testing.T) {
	doc, err := moduleOpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	for path, method := range map[string]string{"inventory": "get", "detect": "get", "plans": "post", "installs": "post", "jobs/{id}": "get"} {
		raw, ok := paths["/v1/m/agenttools/"+path].(map[string]any)
		if !ok {
			t.Fatalf("missing %s", path)
		}
		op := raw[method].(map[string]any)
		if op["x-olivares-scope"] != "system" || op["x-required-permission"] != "system:admin" {
			t.Fatalf("authority: %#v", op)
		}
		for _, parameter := range op["parameters"].([]any) {
			if parameter.(map[string]any)["name"] == "X-Olivares-Tenant" {
				t.Fatal("system route advertises tenant header")
			}
		}
		if method == "post" && (op["requestBody"] == nil || op["x-olivares-request-body-disposition"] != "schema-published") {
			t.Fatal("mutation body is not published")
		}
		if path == "installs" {
			responses := op["responses"].(map[string]any)
			if responses["202"] == nil || responses["200"] != nil || op["x-required-assurance"] != 3 {
				t.Fatalf("install contract: %#v", op)
			}
		}
	}
}
