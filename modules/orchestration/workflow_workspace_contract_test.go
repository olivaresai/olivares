// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSessionLaunchWorkspaceConfig(t *testing.T) {
	for _, mode := range []string{"", "native", "container", "sandbox"} {
		t.Run("isolation="+mode, func(t *testing.T) {
			input := `{"runtime_profile_ref":"worker","workspace_ref":"source","isolation":"` + mode + `"}`
			var cfg sessionLaunchConfig
			decoder := json.NewDecoder(strings.NewReader(input))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&cfg); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if got["workspace_ref"] != "source" || (mode != "" && got["isolation"] != mode) {
				t.Fatalf("workspace selection lost during snapshot round trip: %s", encoded)
			}
		})
	}
	t.Run("legacy", func(t *testing.T) {
		var cfg sessionLaunchConfig
		if err := json.Unmarshal([]byte(`{"runtime_profile_ref":"worker"}`), &cfg); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "workspace_ref") || strings.Contains(string(encoded), "isolation") {
			t.Fatalf("legacy configuration acquired workspace defaults: %s", encoded)
		}
	})
}

func TestSessionLaunchWorkspaceSchema(t *testing.T) {
	schema := orchestrationSessionLaunchConfigSchema()
	properties := schema["properties"].(map[string]any)
	for _, name := range []string{"workspace_ref", "isolation"} {
		if properties[name] == nil {
			t.Errorf("closed session-launch schema rejects %s", name)
		}
	}
	if got := schema["required"]; !reflect.DeepEqual(got, []any{"runtime_profile_ref"}) {
		t.Fatalf("optional workspace selection changed required fields: %#v", got)
	}
	isolation, ok := properties["isolation"].(map[string]any)
	if !ok {
		t.Fatal("isolation has no schema")
	}
	choices := isolation["anyOf"].([]any)[0].(map[string]any)["enum"]
	if !reflect.DeepEqual(choices, []any{"", "native", "container", "sandbox"}) {
		t.Errorf("isolation choices = %#v", choices)
	}
	port := reflect.TypeOf(WorkLaunchRequest{})
	for _, name := range []string{"WorkspaceRef", "Isolation"} {
		field, ok := port.FieldByName(name)
		if !ok || field.Type.Kind() != reflect.String {
			t.Errorf("launch port cannot carry %s as a string", name)
		}
	}
}
