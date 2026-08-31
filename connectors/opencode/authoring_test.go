// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"encoding/json"
	"testing"
)

func TestAuthoring_RenderFragment(t *testing.T) {
	enabled := true
	timeout := 30
	out, err := Render(Policy{
		EditAction: "deny",
		BashAction: "ask",
		MCPServers: map[string]PolicyMCPServer{
			"local":  {Type: "local", Command: []string{"node", "server.js"}, Enabled: &enabled, Timeout: &timeout},
			"remote": {Type: "remote", URL: "https://mcp.example.com/rpc"},
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	perm, ok := parsed["permission"].(map[string]any)
	if !ok {
		t.Fatal("permission object missing")
	}
	if perm["edit"] != "deny" {
		t.Errorf("permission.edit = %v, want deny", perm["edit"])
	}
	if perm["bash"] != "ask" {
		t.Errorf("permission.bash = %v, want ask", perm["bash"])
	}
	if parsed["share"] != "disabled" {
		t.Errorf("share = %v, want disabled", parsed["share"])
	}
	exp, ok := parsed["experimental"].(map[string]any)
	if !ok || exp["openTelemetry"] != true {
		t.Fatalf("experimental.openTelemetry = %v, want true", parsed["experimental"])
	}
	mcp, ok := parsed["mcp"].(map[string]any)
	if !ok || len(mcp) != 2 {
		t.Fatalf("mcp allowlist = %#v, want 2 entries", parsed["mcp"])
	}
}

func TestAuthoringRejectsAllow(t *testing.T) {
	if _, err := Render(Policy{EditAction: "allow"}); err == nil {
		t.Fatal("managed hardening fragment must reject permission allow")
	}
}
