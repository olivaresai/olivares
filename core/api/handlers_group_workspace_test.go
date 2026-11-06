// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"testing"
)

func TestGroupWorkspaceOpenAPI(t *testing.T) {
	h := newHarness(t)
	doc := decodeDoc(t, rawGet(h, "/openapi.json", "", nil))
	paths := doc["paths"].(map[string]any)
	path, ok := paths["/v1/groups/{id}/workspace"].(map[string]any)
	if !ok || path["put"] == nil {
		t.Fatal("OpenAPI omits the user group placement route")
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	props := schemas["DirectoryGroup"].(map[string]any)["properties"].(map[string]any)
	if props["workspace_id"] == nil {
		t.Fatal("OpenAPI omits the user group workspace_id")
	}
}
