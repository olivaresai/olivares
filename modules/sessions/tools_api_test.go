// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/driverfacts"
)

// The console names and offers tools from GET /v1/m/sessions/tools, and its tests read
// the engine's answer from one fixture. These tests hold the route to the driver table
// and the fixture to the route, so neither can drift from driverfacts.
const toolsFixture = "../../web/src/features/agentops/engine-tools.fixture.json"

func toolsAnswer(t *testing.T) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	New().handleListTools(rec, httptest.NewRequest(http.MethodGet, "/v1/m/sessions/tools", nil), api.ModuleContext{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func TestToolsReadIsPublishedWithRunRead(t *testing.T) {
	t.Parallel()
	doc := api.ModuleOpenAPIDocument([]api.Module{New()})
	paths, _ := doc["paths"].(map[string]any)
	item, _ := paths["/v1/m/sessions/tools"].(map[string]any)
	op, ok := item["get"].(map[string]any)
	if !ok {
		t.Fatalf("the beta document does not publish GET /v1/m/sessions/tools")
	}
	if got := op["x-required-permission"]; got != string(permRunRead) {
		t.Fatalf("x-required-permission = %v, want %s", got, permRunRead)
	}
}

func TestToolsReadIsTheDriverTable(t *testing.T) {
	t.Parallel()
	tools, _ := toolsAnswer(t)["tools"].([]any)
	all := driverfacts.All()
	if len(tools) != len(all) {
		t.Fatalf("%d tools listed, the driver table has %d", len(tools), len(all))
	}
	for i, f := range all {
		row, _ := tools[i].(map[string]any)
		if row["key"] != f.Key || row["name"] != f.Name || row["name_stem"] != f.NameStem() || row["session"] != f.Session {
			t.Fatalf("row %d = %v, want %s %q stem %q session %v", i, row, f.Key, f.Name, f.NameStem(), f.Session)
		}
		bindings, _ := row["bindings"].([]any)
		if len(bindings) != len(f.Bindings) {
			t.Fatalf("%s: %d bindings listed, the table has %d", f.Key, len(bindings), len(f.Bindings))
		}
		for j, b := range f.Bindings {
			got, _ := bindings[j].(map[string]any)
			if got["kind"] != b.Kind || got["egress"] != b.Egress {
				t.Fatalf("%s binding %d = %v, want %s %s", f.Key, j, got, b.Kind, b.Egress)
			}
		}
	}
}

func TestToolsConsoleFixtureIsTheEngineAnswer(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(toolsFixture)
	if err != nil {
		t.Fatalf("read the console fixture: %v", err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode %s: %v", toolsFixture, err)
	}
	if got := toolsAnswer(t); !reflect.DeepEqual(got, fixture) {
		want, _ := json.MarshalIndent(got, "", "  ")
		t.Fatalf("%s is not the engine's answer; write this:\n%s", toolsFixture, want)
	}
}
