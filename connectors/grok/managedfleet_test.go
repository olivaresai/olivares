// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package grok

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRequirementsTOML(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"profile and allowlist", "[sandbox]\nprofile = \"strict\"\n[mcp_servers.a]\n", ""},
		{"empty allowlist is a lockdown", "[mcp_servers]\n", ""},
		{"empty document", "", "document is empty"},
		{"not TOML", "a = = b", "not valid TOML"},
		{"authors nothing", "[other]\nx = 1\n", "authors no requirements"},
		{"unknown profile", "[sandbox]\nprofile = \"wide-open\"\n", `sandbox.profile "wide-open" is not one of`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := ValidateRequirementsTOML([]byte(tt.content))
			switch {
			case tt.want == "" && len(issues) != 0:
				t.Fatalf("issues = %v, want none", issues)
			case tt.want != "" && (len(issues) != 1 || !strings.Contains(issues[0], tt.want)):
				t.Fatalf("issues = %v, want one containing %q", issues, tt.want)
			}
		})
	}
}

func TestRenderedRequirementsValidateAndVerifyClean(t *testing.T) {
	servers := []string{"files", "git"}
	out, err := RenderRequirements(ManagedPolicy{SandboxProfile: "strict", AllowedMCPServers: &servers})
	if err != nil {
		t.Fatal(err)
	}
	if issues := ValidateRequirementsTOML(out); len(issues) != 0 {
		t.Fatalf("what we render must validate: %v", issues)
	}
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	if got, err := VerifyDriftTOML("host-1", out, out, at); err != nil || len(got) != 0 {
		t.Fatalf("rendered file on the host: err=%v findings=%+v", err, got)
	}
}

func TestVerifyDriftTOMLFindsABroadenedHost(t *testing.T) {
	servers := []string{"files"}
	authored, _ := RenderRequirements(ManagedPolicy{SandboxProfile: "strict", AllowedMCPServers: &servers})
	at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	host := "[sandbox]\nprofile = \"off\"\n[mcp_servers.files]\n[mcp_servers.shell]\n"
	got, err := VerifyDriftTOML("host-1", authored, []byte(host), at)
	if err != nil || len(got) != 2 {
		t.Fatalf("err=%v findings=%+v", err, got)
	}
	if got[0].Title != "Grok Build: mcp_servers.shell is on the host but not in the published policy" ||
		got[1].Title != "Grok Build: sandbox.profile differs from the published policy" {
		t.Fatalf("titles: %q, %q", got[0].Title, got[1].Title)
	}
	if _, err := VerifyDriftTOML("host-1", []byte("[sandbox]\nprofile = \"wide-open\"\n"), []byte(host), at); err == nil {
		t.Fatal("an invalid authored document must be an error")
	}
}
