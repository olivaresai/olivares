// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cline

import (
	"encoding/json"
	"testing"
)

func TestRenderFullPolicy(t *testing.T) {
	p := Policy{
		Variant:            variantCline,
		Provider:           "anthropic",
		Model:              "claude-sonnet-4-20250514",
		DisableAutoApprove: true,
		MCPServers: map[string]PolicyMCPServer{
			"governance": {URL: "https://mcp.example.com"},
		},
		AllowedTools: []string{"read_file", "write_file"},
	}
	out, err := Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	if _, ok := parsed["cline.apiProvider"]; !ok {
		t.Error("missing cline.apiProvider")
	}
	if _, ok := parsed["cline.apiModelId"]; !ok {
		t.Error("missing cline.apiModelId")
	}
	if _, ok := parsed["cline.autoApproveReadOnly"]; !ok {
		t.Error("missing cline.autoApproveReadOnly (should be false)")
	}
	if v, ok := parsed["cline.autoApproveReadOnly"]; ok && v != false {
		t.Errorf("cline.autoApproveReadOnly = %v, want false", v)
	}
	if _, ok := parsed["cline.mcpServers"]; !ok {
		t.Error("missing cline.mcpServers")
	}
	if _, ok := parsed["cline.allowedTools"]; !ok {
		t.Error("missing cline.allowedTools")
	}
}

func TestRenderKiloCodeVariant(t *testing.T) {
	p := Policy{
		Variant:  variantKiloCode,
		Provider: "anthropic",
	}
	out, err := Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	if _, ok := parsed["kilocode.apiProvider"]; !ok {
		t.Error("kilocode variant should use kilocode.* prefix")
	}
	if _, ok := parsed["cline.apiProvider"]; ok {
		t.Error("kilocode variant should not use cline.* prefix")
	}
}

func TestRenderEmptyPolicy(t *testing.T) {
	out, err := Render(Policy{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(parsed) != 0 {
		t.Errorf("empty policy should produce empty JSON object, got %d keys", len(parsed))
	}
}
