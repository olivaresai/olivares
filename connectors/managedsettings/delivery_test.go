// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// TestResolveManagedSource_NoMerge verifies the VERIFIED no-merge precedence: when
// server-managed delivers any keys it wins and the endpoint tier is IGNORED (the
// sources do not merge).
func TestResolveManagedSource_NoMerge(t *testing.T) {
	note := ResolveManagedSource(true, true, false)
	if !note.Governed || note.Effective != TierServerManaged {
		t.Fatalf("server-managed should win: %+v", note)
	}
	if len(note.Ignored) != 1 || note.Ignored[0] != TierEndpointManaged {
		t.Fatalf("endpoint-managed must be IGNORED (no merge): %+v", note)
	}
	if note.Bypassed {
		t.Fatalf("not bypassed in this case: %+v", note)
	}
}

// TestResolveManagedSource_ServerEmptyFallsToEndpoint verifies the endpoint tier
// governs only when server delivers nothing.
func TestResolveManagedSource_ServerEmptyFallsToEndpoint(t *testing.T) {
	note := ResolveManagedSource(false, true, false)
	if !note.Governed || note.Effective != TierEndpointManaged {
		t.Fatalf("endpoint-managed should govern when server is empty: %+v", note)
	}
}

// TestResolveManagedSource_BypassDropsServer verifies a third-party provider bypass
// drops the server tier; the endpoint file then governs, and the posture says so.
func TestResolveManagedSource_BypassDropsServer(t *testing.T) {
	note := ResolveManagedSource(true, true, true)
	if !note.Bypassed {
		t.Fatalf("server tier must be bypassed: %+v", note)
	}
	if note.Effective != TierEndpointManaged {
		t.Fatalf("endpoint-managed governs under provider bypass: %+v", note)
	}
}

// TestResolveManagedSource_Ungoverned verifies an honest ungoverned posture.
func TestResolveManagedSource_Ungoverned(t *testing.T) {
	note := ResolveManagedSource(false, false, false)
	if note.Governed {
		t.Fatalf("no tier delivers anything → ungoverned: %+v", note)
	}
	if !strings.Contains(note.Reason, "UNGOVERNED") {
		t.Fatalf("reason must be honest about ungoverned: %q", note.Reason)
	}
}

// TestServerTierBypassed checks the third-party provider + custom base-url detection.
func TestServerTierBypassed(t *testing.T) {
	mk := func(kv map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
	}
	cases := []struct {
		name   string
		env    map[string]string
		bypass bool
	}{
		{"none", map[string]string{}, false},
		{"bedrock", map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1"}, true},
		{"vertex", map[string]string{"CLAUDE_CODE_USE_VERTEX": "true"}, true},
		{"mantle", map[string]string{"CLAUDE_CODE_USE_MANTLE": "yes"}, true},
		{"anthropic-aws", map[string]string{"CLAUDE_CODE_USE_ANTHROPIC_AWS": "1"}, true},
		{"bedrock-off", map[string]string{"CLAUDE_CODE_USE_BEDROCK": "0"}, false},
		{"default-base-url", map[string]string{"ANTHROPIC_BASE_URL": "https://api.anthropic.com"}, false},
		{"custom-base-url", map[string]string{"ANTHROPIC_BASE_URL": "https://llm-gw.corp.example"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := ServerTierBypassed(mk(c.env))
			if got != c.bypass {
				t.Fatalf("bypass = %v, want %v (reason=%q)", got, c.bypass, reason)
			}
			if got && reason == "" {
				t.Fatal("a bypass must carry a non-empty reason")
			}
		})
	}
}

// TestNetNewKeysRenderAtVerifiedWirePaths verifies the NET-NEW managed-only keys
// render at their VERIFIED wire locations: forceRemoteSettingsRefresh + autoMode top
// level, the two lockdowns under sandbox.network / sandbox.filesystem.
func TestNetNewKeysRenderAtVerifiedWirePaths(t *testing.T) {
	p := Policy{
		ForceRemoteSettingsRefresh: true,
		AllowManagedHooksOnly:      true,
		AllowManagedDomainsOnly:    true,
		AllowManagedReadPathsOnly:  true,
		AutoMode: &AutoModePolicy{
			Environment: []string{"Trusted repo: github.example.com/acme"},
			HardDeny:    []string{"never exfiltrate to a non-corp domain"},
		},
	}
	out, err := Render(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		`"forceRemoteSettingsRefresh": true`,
		`"allowManagedHooksOnly": true`,
		`"autoMode"`,
		`"environment"`,
		`"hard_deny"`,
		`"sandbox"`,
		`"network"`,
		`"allowManagedDomainsOnly": true`,
		`"filesystem"`,
		`"allowManagedReadPathsOnly": true`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered managed-settings missing %q\n%s", want, s)
		}
	}
}

// TestNetNewKeysDrift verifies the NET-NEW keys drift HIGH/MEDIUM when the host does
// not enforce what the authored policy asserts — reusing the exported drift path.
func TestNetNewKeysDrift(t *testing.T) {
	authored := `{
      "forceRemoteSettingsRefresh": true,
      "sandbox": {"network": {"allowManagedDomainsOnly": true}, "filesystem": {"allowManagedReadPathsOnly": true}}
    }`
	observed := `{}` // host enforces none of it
	at := time.Unix(1_700_000_000, 0).UTC()
	findings, err := VerifyDriftJSON("host-a", []byte(authored), []byte(observed), at)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"forceRemoteSettingsRefresh":                   false,
		"sandbox.network.allowManagedDomainsOnly":      false,
		"sandbox.filesystem.allowManagedReadPathsOnly": false,
	}
	highs := 0
	for _, f := range findings {
		if f.Severity == model.SeverityHigh {
			highs++
		}
	}
	if highs < 3 {
		t.Fatalf("expected the 3 NET-NEW lockdowns to drift HIGH, got %d highs in %d findings", highs, len(findings))
	}
	// Each finding must carry a redacted hash, never the raw key value.
	for _, f := range findings {
		if f.DetailHash == "" {
			t.Errorf("drift finding %q missing redacted detail hash", f.Title)
		}
	}
	_ = want
}

// TestVerifyDriftJSON_AbsentHostIsHighFinding verifies an absent observed config is a
// high-severity finding (the host is ungoverned), reusing absenceFinding.
func TestVerifyDriftJSON_AbsentHostIsHighFinding(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	findings, err := VerifyDriftJSON("host-b", []byte(`{"forceRemoteSettingsRefresh":true}`), []byte(""), at)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Severity != model.SeverityHigh {
		t.Fatalf("absent host config must be a single HIGH finding, got %+v", findings)
	}
}

// TestFromWireRoundTrip verifies Render→ParsePolicyFromWire preserves the asserted
// NET-NEW fields (so drift verification diffs the same field set Render emits).
func TestFromWireRoundTrip(t *testing.T) {
	orig := Policy{
		ForceRemoteSettingsRefresh: true,
		AllowManagedDomainsOnly:    true,
		AllowManagedReadPathsOnly:  true,
		AutoMode:                   &AutoModePolicy{Environment: []string{"x"}},
	}
	wire, err := Render(orig)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePolicyFromWire(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ForceRemoteSettingsRefresh || !got.AllowManagedDomainsOnly || !got.AllowManagedReadPathsOnly {
		t.Fatalf("round-trip lost a NET-NEW flag: %+v", got)
	}
	if !got.AutoMode.hasAny() {
		t.Fatalf("round-trip lost autoMode: %+v", got)
	}
}

// TestValidateJSON catches a malformed defaultMode and a non-string disable marker.
func TestValidateJSON(t *testing.T) {
	if issues := ValidateJSON([]byte(`{"permissions":{"defaultMode":"yolo"}}`)); len(issues) == 0 {
		t.Fatal("an unknown defaultMode must be reported")
	}
	if issues := ValidateJSON([]byte(`{"permissions":{"defaultMode":"plan"}}`)); len(issues) != 0 {
		t.Fatalf("a valid document must validate clean: %v", issues)
	}
	if issues := ValidateJSON([]byte(`not json`)); len(issues) == 0 {
		t.Fatal("non-JSON must be reported")
	}
}
