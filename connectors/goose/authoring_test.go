// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package goose

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRenderFullPolicy(t *testing.T) {
	approve := true
	p := Policy{
		ProfileName:     "governed",
		Provider:        "anthropic",
		Model:           "claude-sonnet-4-20250514",
		RequireApproval: &approve,
		AllowedTools:    []string{"read_file", "write_file"},
		Extensions: map[string]PolicyExtension{
			"governance": {Type: "sse", URL: "https://mcp.example.com"},
		},
	}
	out, err := Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid YAML: %v", err)
	}

	prof, ok := parsed["governed"]
	if !ok {
		t.Fatal("expected 'governed' profile in output")
	}
	pm, ok := prof.(map[string]any)
	if !ok {
		t.Fatalf("profile is %T, want map", prof)
	}
	if pm["provider"] != "anthropic" {
		t.Errorf("provider = %v", pm["provider"])
	}
	if pm["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("model = %v", pm["model"])
	}
}

func TestRenderDefaultProfile(t *testing.T) {
	out, err := Render(Policy{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "default:") {
		t.Error("empty ProfileName should default to 'default'")
	}
}

func TestRenderEmptyPolicy(t *testing.T) {
	out, err := Render(Policy{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid YAML: %v", err)
	}
}
