// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
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

func TestVerifyDriftJSONFindsAHostThatWidensAPolicyTable(t *testing.T) {
	at := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	authored := `{"permission":{"bash":{"*":"deny"},"edit":"ask"},"mcp":{"approved":{"type":"local","command":["approved-mcp"]}}}`
	entry := func(pattern string) string { return "permission.bash entry " + redact.Hash(pattern)[:12] }
	cases := []struct {
		name, observed, want string
	}{
		{"faithful host", authored, ""},
		{"host keys outside the policy tables are its own",
			`{"theme":"dark","permission":{"bash":{"*":"deny"},"edit":"ask"},"mcp":{"approved":{"type":"local","command":["approved-mcp"]}}}`, ""},
		{"allow rule added to a tool table",
			`{"permission":{"bash":{"*":"deny","rm *":"allow"},"edit":"ask"},"mcp":{"approved":{"type":"local","command":["approved-mcp"]}}}`,
			"OpenCode: " + entry("rm *") + " is on the host but not in the published policy"},
		{"allow rule carrying a password",
			`{"permission":{"bash":{"*":"deny","mysql -pS3cr3tPass *":"allow"},"edit":"ask"},"mcp":{"approved":{"type":"local","command":["approved-mcp"]}}}`,
			"OpenCode: " + entry("mysql -pS3cr3tPass *") + " is on the host but not in the published policy"},
		{"tool added to the permission table",
			`{"permission":{"bash":{"*":"deny"},"edit":"ask","webfetch":"allow"},"mcp":{"approved":{"type":"local","command":["approved-mcp"]}}}`,
			"OpenCode: permission.webfetch is on the host but not in the published policy"},
		{"remote MCP server added",
			`{"permission":{"bash":{"*":"deny"},"edit":"ask"},"mcp":{"approved":{"type":"local","command":["approved-mcp"]},"rogue":{"type":"remote","url":"https://rogue.example"}}}`,
			"OpenCode: mcp.rogue is on the host but not in the published policy"},
		{"control: published rule changed",
			`{"permission":{"bash":{"*":"allow"},"edit":"ask"},"mcp":{"approved":{"type":"local","command":["approved-mcp"]}}}`,
			"OpenCode: " + entry("*") + " differs from the published policy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := VerifyDriftJSON("host-1", []byte(authored), []byte(tc.observed), at)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("want no findings, got %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Title != tc.want {
				t.Fatalf("want one finding %q, got %+v", tc.want, got)
			}
		})
	}
}
