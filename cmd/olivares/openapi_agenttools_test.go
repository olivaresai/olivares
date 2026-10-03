// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"strings"
	"testing"
)

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
			if responses["202"] == nil || responses["200"] != nil || responses["403"] == nil {
				t.Fatalf("install contract: %#v", op)
			}
			// The install asks the deployment's administrative step-up policy
			// (agenttoolsapi handleInstall -> guard(true) -> auth.StepUpSatisfied): nothing
			// beyond the sign-in by default, an authenticator code under totp, a passkey
			// (AAL3) under passkey. No fixed assurance holds on every deployment, so none
			// is published; the text names the step-up (TestInstallFollowsTheStepUpPolicy).
			description, _ := op["description"].(string)
			if _, fixed := op["x-required-assurance"]; fixed || !strings.Contains(description, "administrative step-up") {
				t.Fatalf("install step-up: %#v", op)
			}
		}
	}
}
