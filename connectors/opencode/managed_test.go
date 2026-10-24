// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"strings"
	"testing"
	"time"
)

func TestValidateConfigJSON(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string // substring of the one expected issue; "" means valid
	}{
		{"rendered fragment is valid", mustRender(t), ""},
		{"unknown keys pass", `{"theme":"x","permission":{"edit":"deny"}}`, ""},
		{"empty", "  ", "document is empty"},
		{"not an object", `[1]`, "must be a JSON object"},
		{"null", `null`, "must be a JSON object"},
		{"permission wrong shape", `{"permission":"deny"}`, "permission must be an object"},
		{"permission unknown action", `{"permission":{"bash":"maybe"}}`, "permission.bash must be ask, allow or deny"},
		{"share unknown", `{"share":"yes"}`, "share must be manual, auto or disabled"},
		{"mcp local without command", `{"mcp":{"x":{"type":"local"}}}`, "mcp.x is local and needs a command"},
		{"mcp remote without url", `{"mcp":{"x":{"type":"remote"}}}`, "mcp.x is remote and needs a url"},
		{"mcp unknown type", `{"mcp":{"x":{"type":"pipe"}}}`, "mcp.x type must be local or remote"},
		{"mcp wrong shape", `{"mcp":[]}`, "mcp must be an object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := ValidateConfigJSON([]byte(tt.content))
			switch {
			case tt.want == "" && len(issues) != 0:
				t.Fatalf("issues = %v, want none", issues)
			case tt.want != "" && (len(issues) != 1 || !strings.Contains(issues[0], tt.want)):
				t.Fatalf("issues = %v, want one containing %q", issues, tt.want)
			}
		})
	}
}

func mustRender(t *testing.T) string {
	t.Helper()
	out, err := Render(Policy{EditAction: "deny", MCPServers: map[string]PolicyMCPServer{"r": {Type: "remote", URL: "https://mcp.example.com/rpc"}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestVerifyDriftJSONFindsAWeakenedHost(t *testing.T) {
	authored := mustRender(t)
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	clean, err := VerifyDriftJSON("host-1", []byte(authored), []byte(authored), at)
	if err != nil || len(clean) != 0 {
		t.Fatalf("the rendered file on the host: err=%v findings=%d", err, len(clean))
	}
	weak := strings.Replace(authored, `"edit": "deny"`, `"edit": "allow"`, 1)
	got, err := VerifyDriftJSON("host-1", []byte(authored), []byte(weak), at)
	if err != nil || len(got) != 1 || got[0].Title != "OpenCode: permission.edit differs from the published policy" || got[0].SubjectKind != "opencode_managed_config" {
		t.Fatalf("weakened edit permission: err=%v findings=%+v", err, got)
	}
}
