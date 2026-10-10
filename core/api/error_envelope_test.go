// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"testing"
)

func TestStableOpenAPIMatchesUnchangedSnapshot(t *testing.T) {
	want, err := os.ReadFile("../../web/openapi/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(OpenAPIDocument(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bytes.TrimSpace(want)) {
		t.Fatal("stable OpenAPI differs from the published snapshot")
	}
}

func TestBetaErrorSchemaMatchesStableEnvelope(t *testing.T) {
	stable := OpenAPIDocument()["components"].(map[string]any)["schemas"].(map[string]any)["Error"]
	beta := buildModuleOpenAPI([]moduleRoute{{ns: "demo", method: http.MethodGet, pattern: "/things"}})
	got := beta["components"].(map[string]any)["schemas"].(map[string]any)["Error"]
	if !reflect.DeepEqual(got, stable) {
		t.Errorf("beta error schema = %#v; want stable schema %#v", got, stable)
	}
}
