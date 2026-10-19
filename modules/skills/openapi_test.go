// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/modules/skills"
)

func TestSkillsMutationDocumentation(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{skills.New(skills.Options{})})
	paths := doc["paths"].(map[string]any)
	for _, route := range []struct {
		method, path, permission, header string
		body                             bool
	}{
		{"post", "/assignments", "skills:assignment:write", "", true},
		{"put", "/assignments/{id}", "skills:assignment:write", "If-Match", true},
		{"post", "/packs", "skills:catalog:write", "Idempotency-Key", true},
		{"post", "/packs/{id}/revisions", "skills:catalog:write", "Idempotency-Key", true},
		{"delete", "/assignments/{id}", "skills:assignment:write", "If-Match", false},
		{"delete", "/packs/{id}", "skills:catalog:admin", "If-Match", false},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			op := paths["/v1/m/skills"+route.path].(map[string]any)[route.method].(map[string]any)
			want := "bodyless"
			if route.body {
				want = "schema-published"
			}
			if got := op["x-olivares-request-body-disposition"]; got != want {
				t.Fatalf("request-body disposition = %v, want %s", got, want)
			}
			if op["x-required-permission"] != route.permission || op["x-olivares-scope"] == "system" {
				t.Fatal("documentation changed tenant authority")
			}
			if route.header != "" {
				found := false
				for _, raw := range op["parameters"].([]any) {
					p := raw.(map[string]any)
					if p["in"] == "header" && p["name"] == route.header && p["required"] == true {
						found = true
					}
				}
				if !found {
					t.Fatalf("required %s header absent", route.header)
				}
			}
			if !route.body {
				if _, exists := op["requestBody"]; exists {
					t.Fatal("bodyless delete publishes a body")
				}
				return
			}
			body := op["requestBody"].(map[string]any)
			content := body["content"].(map[string]any)
			if body["required"] != true || len(content) != 1 {
				t.Fatal("SDK request must declare one required JSON media type")
			}
			schema := content["application/json"].(map[string]any)["schema"].(map[string]any)
			properties := schema["properties"].(map[string]any)
			if schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatal("request must preserve the closed JSON decoder")
			}
			if strings.HasPrefix(route.path, "/assignments") {
				if len(properties) != 5 || !reflect.DeepEqual(schema["required"], []string{"target_kind", "target_id", "pack_revision_id"}) {
					t.Fatal("assignment fields differ from its wire contract")
				}
				if !reflect.DeepEqual(properties["target_kind"].(map[string]any)["enum"], []string{"workspace", "profile", "folder", "template", "agent_group", "agent", "session"}) || properties["members"] == nil || properties["target_id"] == nil || properties["pack_revision_id"] == nil {
					t.Fatal("assignment target or member selection is missing")
				}
				if properties["target_id"].(map[string]any)["format"] == "uuid" {
					t.Fatal("profile refs cannot satisfy a UUID-only target contract")
				}
				return
			}
			if len(properties) != 2 || properties["name"] == nil || properties["source"] == nil {
				t.Fatal("pack fields differ from the JSON import contract")
			}
			required := []string{"source"}
			if route.path == "/packs" {
				required = []string{"name", "source"}
			}
			if !reflect.DeepEqual(schema["required"], required) {
				t.Fatal("new packs require a name; revisions may omit it")
			}
			source := properties["source"].(map[string]any)
			fields := source["properties"].(map[string]any)
			for _, name := range []string{"kind", "url", "ref", "subdir", "expected_digest", "workspace_ref", "directory"} {
				if fields[name] == nil {
					t.Fatalf("source field %s absent", name)
				}
			}
			if len(fields) != 7 || source["additionalProperties"] != false || !strings.Contains(body["description"].(string), "multipart/form-data") {
				t.Fatal("source decoder or archive alternative is misrepresented")
			}
		})
	}
}

func TestSkillsUpdateSuccessResponses(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{skills.New(skills.Options{})})
	op := doc["paths"].(map[string]any)["/v1/m/skills/packs/{id}/update"].(map[string]any)["post"].(map[string]any)
	responses := op["responses"].(map[string]any)
	for status, response := range api.GenericModuleResponses() {
		if status != "200" && !reflect.DeepEqual(responses[status], response) {
			t.Fatalf("update changed existing error response %s", status)
		}
	}
	for _, status := range []string{"200", "201"} {
		response, ok := responses[status].(map[string]any)
		if !ok {
			t.Fatalf("update omits success response %s", status)
		}
		content := response["content"].(map[string]any)
		if content["application/json"] == nil {
			t.Fatalf("update response %s omits JSON content", status)
		}
	}
}
