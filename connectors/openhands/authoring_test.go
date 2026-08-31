// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"strings"
	"testing"
)

func TestRenderFullPolicy(t *testing.T) {
	p := Policy{
		SandboxType:   "docker",
		Model:         "claude-sonnet-4-20250514",
		Provider:      "anthropic",
		MaxIterations: 100,
		OTELEndpoint:  "http://collector:4318",
		MCPServers: map[string]string{
			"main": "https://mcp.example.com",
		},
	}
	out, err := Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(out)

	checks := []string{
		`model = "claude-sonnet-4-20250514"`,
		`provider = "anthropic"`,
		`sandbox_type = "docker"`,
		`max_iterations = 100`,
		`otel_exporter_otlp_endpoint = "http://collector:4318"`,
		`url = "https://mcp.example.com"`,
		"[mcp.servers.main]",
	}
	for _, want := range checks {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestRenderEmptyPolicy(t *testing.T) {
	out, err := Render(Policy{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "[llm]") {
		t.Error("empty policy should not emit [llm] section")
	}
	if strings.Contains(s, "[sandbox]") {
		t.Error("empty policy should not emit [sandbox] section")
	}
	if strings.Contains(s, "[core]") {
		t.Error("empty policy should not emit [core] section")
	}
}

func TestRenderPartialPolicy(t *testing.T) {
	out, err := Render(Policy{SandboxType: "docker"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `sandbox_type = "docker"`) {
		t.Error("missing sandbox_type")
	}
	if strings.Contains(s, "[llm]") {
		t.Error("partial policy should not emit unset sections")
	}
}
