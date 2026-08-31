// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// Claude Code 2.1.17x key parity (VERIFIED 2026-06-10 against
// docs.claude.com settings/hooks/managed-mcp pages and the raw changelog).
// Per new key these tests prove the LOCKSTEP property the audit flagged as the
// failure mode: a key must survive ParsePolicyFromWire→Render (CanonicalJSON),
// count in HasAnyKeys, validate server-side, and drift against a bare host.

// wire2117x is a managed-settings.json document exercising every new key in its
// verified wire shape (fallbackModel as array; forceLoginOrgUUID as array; MCP
// predicates by name and URL glob; a prompt hook with continueOnBlock).
const wire2117x = `{
  "requiredMinimumVersion": "2.1.150",
  "requiredMaximumVersion": "2.1.170",
  "fallbackModel": ["claude-sonnet-4-6", "claude-haiku-4-5"],
  "allowedMcpServers": [
    {"serverName": "github"},
    {"serverUrl": "https://mcp.internal.example/*"}
  ],
  "deniedMcpServers": [{"serverUrl": "*://evil.example/*"}],
  "pluginSuggestionMarketplaces": ["acme-corp-plugins"],
  "forceLoginOrgUUID": ["11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"],
  "channelsEnabled": true,
  "parentSettingsBehavior": "first-wins",
  "disableBundledSkills": true,
  "hooks": {
    "MessageDisplay": [{"hooks": [{"type": "command", "command": "/usr/local/bin/scrub-display"}]}],
    "PostToolUse": [{"hooks": [{"type": "prompt", "prompt": "Flag any secret in the output.", "continueOnBlock": true}]}]
  }
}`

func TestS148RoundTripPreservesNewKeys(t *testing.T) {
	canon, err := CanonicalJSON([]byte(wire2117x))
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	s := string(canon)
	for _, want := range []string{
		`"requiredMinimumVersion": "2.1.150"`,
		`"requiredMaximumVersion": "2.1.170"`,
		`"claude-sonnet-4-6"`, `"claude-haiku-4-5"`,
		`"serverName": "github"`,
		`"serverUrl": "https://mcp.internal.example/*"`,
		`"serverUrl": "*://evil.example/*"`,
		`"acme-corp-plugins"`,
		`"11111111-1111-1111-1111-111111111111"`,
		`"channelsEnabled": true`,
		`"parentSettingsBehavior": "first-wins"`,
		`"disableBundledSkills": true`,
		`"MessageDisplay"`,
		`"prompt": "Flag any secret in the output."`,
		`"continueOnBlock": true`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("canonical round-trip lost %q\n%s", want, s)
		}
	}
	// The document must also count as delivering keys (the no-merge preview
	// depends on it) — pre every one of these keys read as "delivers nothing".
	if !HasAnyKeys([]byte(wire2117x)) {
		t.Error("HasAnyKeys must report a 2.1.17x-only document as governing")
	}
	// And it is valid as authored.
	if issues := ValidateJSON([]byte(wire2117x)); len(issues) != 0 {
		t.Errorf("fixture must validate clean, got %v", issues)
	}
}

func TestS148FallbackModelStringFormNormalizes(t *testing.T) {
	// The wire accepts the bare-string shorthand; the canonical form is an array.
	canon, err := CanonicalJSON([]byte(`{"fallbackModel": "claude-haiku-4-5"}`))
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if !strings.Contains(string(canon), `"fallbackModel": [`) {
		t.Errorf("string shorthand must normalize to the array form: %s", canon)
	}
}

func TestS148MCPLockdownRoundTrips(t *testing.T) {
	// The `[]` complete-lockdown posture must survive the round trip (a slice+
	// omitempty wire field would silently drop it — the pre bug class).
	canon, err := CanonicalJSON([]byte(`{"allowedMcpServers": []}`))
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if !strings.Contains(string(canon), `"allowedMcpServers": []`) {
		t.Errorf("MCP lockdown [] lost in round trip: %s", canon)
	}
	if !HasAnyKeys([]byte(`{"allowedMcpServers": []}`)) {
		t.Error("the MCP lockdown posture delivers a key")
	}
}

func TestS148ForceLoginOrgUUIDSingleStringForm(t *testing.T) {
	canon, err := CanonicalJSON([]byte(`{"forceLoginOrgUUID": "org-123"}`))
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if !strings.Contains(string(canon), `"forceLoginOrgUUID": "org-123"`) {
		t.Errorf("single-string org pin must stay a string (it pre-selects the org): %s", canon)
	}
}

func TestS148ValidateJSONNegatives(t *testing.T) {
	cases := map[string]struct {
		doc  string
		want string // substring of the expected issue
	}{
		"bad forceLoginMethod":      {`{"forceLoginMethod": "sso"}`, "claudeai|console"},
		"empty forceLoginOrgUUID":   {`{"forceLoginOrgUUID": []}`, "blocks ALL login"},
		"bad parentBehavior":        {`{"parentSettingsBehavior": "child-wins"}`, "first-wins|merge"},
		"fallback chain too long":   {`{"fallbackModel": ["a","b","c","d"]}`, "capped at 3"},
		"fallback bad shape":        {`{"fallbackModel": 7}`, "model string or an array"},
		"mcp allow non-array":       {`{"allowedMcpServers": true}`, "EMPTY allowlist (fail-closed)"},
		"mcp glob in name":          {`{"allowedMcpServers": [{"serverName": "git*"}]}`, "matches nothing"},
		"mcp both predicates":       {`{"allowedMcpServers": [{"serverName": "a", "serverUrl": "b"}]}`, "exactly ONE"},
		"mcp deny empty predicate":  {`{"deniedMcpServers": [{}]}`, "serverName or serverUrl"},
		"bad requiredMin":           {`{"requiredMinimumVersion": "latest"}`, "FAILS OPEN"},
		"empty version range":       {`{"requiredMinimumVersion": "2.2.0", "requiredMaximumVersion": "2.1.0"}`, "range is empty"},
		"empty suggestion entry":    {`{"pluginSuggestionMarketplaces": [" "]}`, "is empty"},
		"non-mcp allow glob":        {`{"permissions": {"allow": ["Read*"]}}`, "rejects non-MCP globs"},
		"glob in mcp server seg":    {`{"permissions": {"allow": ["mcp__git*__pull"]}}`, "glob-free"},
		"continueOnBlock on cmd":    {`{"hooks": {"PostToolUse": [{"hooks": [{"type": "command", "command": "/x", "continueOnBlock": true}]}]}}`, "prompt-hook config field"},
		"empty prompt hook":         {`{"hooks": {"PostToolUse": [{"hooks": [{"type": "prompt"}]}]}}`, "empty prompt"},
		"MessageDisplay w/ matcher": {`{"hooks": {"MessageDisplay": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "/x"}]}]}}`, "does not support matchers"},
	}
	for name, tc := range cases {
		issues := ValidateJSON([]byte(tc.doc))
		found := false
		for _, is := range issues {
			if strings.Contains(is, tc.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want an issue containing %q, got %v", name, tc.want, issues)
		}
	}
	// Valid forms must NOT be flagged.
	for name, doc := range map[string]string{
		"deny glob star":     `{"permissions": {"deny": ["*"]}}`,
		"mcp allow glob ok":  `{"permissions": {"allow": ["mcp__github__get_*"]}}`,
		"url glob predicate": `{"allowedMcpServers": [{"serverUrl": "*://mcp.example/*"}]}`,
		// forceLoginMethod=gateway is a CURRENT valid value (VERIFIED 2026-07-20).
		"forceLoginMethod gateway": `{"forceLoginMethod": "gateway"}`,
	} {
		if issues := ValidateJSON([]byte(doc)); len(issues) != 0 {
			t.Errorf("%s must be valid, got %v", name, issues)
		}
	}
}

func TestS148DriftFindings(t *testing.T) {
	allowed := []MCPServerRule{{Name: "github"}, {URL: "https://mcp.internal.example/*"}}
	expected := Policy{
		RequiredMinimumVersion:       "2.1.150",
		RequiredMaximumVersion:       "2.1.170",
		FallbackModels:               []string{"claude-sonnet-4-6", "claude-haiku-4-5"},
		AllowedMCPServers:            &allowed,
		DeniedMCPServers:             []MCPServerRule{{URL: "*://evil.example/*"}},
		PluginSuggestionMarketplaces: []string{"acme-corp-plugins"},
		ForceLoginOrgUUIDs:           []string{"org-a", "org-b"},
		ChannelsEnabled:              true,
		ParentSettingsBehavior:       ParentFirstWins,
		DisableBundledSkills:         true,
	}

	// A bare host drifts on every authored key, with the designed severities.
	d := driftFindings("host-a", expected, managedSettings{}, testNow())
	bySubstr := func(sub string) (model.Severity, bool) {
		for _, f := range d {
			if strings.Contains(f.Title, sub) {
				return f.Severity, true
			}
		}
		return "", false
	}
	for sub, want := range map[string]model.Severity{
		"requiredMinimumVersion":     model.SeverityMedium,
		"requiredMaximumVersion":     model.SeverityMedium,
		"fallbackModel":              model.SeverityMedium,
		"allowedMcpServers":          model.SeverityMedium,
		"MCP denylist entry missing": model.SeverityMedium,
		"plugin-suggestion":          model.SeverityMedium,
		"allowed-org set drifts":     model.SeverityHigh,
		"channelsEnabled":            model.SeverityLow,
		"parentSettingsBehavior":     model.SeverityMedium,
		"bundled skills":             model.SeverityMedium,
	} {
		if sev, ok := bySubstr(sub); !ok || sev != want {
			t.Errorf("drift %q: got (%v, found=%v), want %v", sub, sev, ok, want)
		}
	}

	// The MCP `[]` lockdown absent on host is the HIGH case.
	lockdown := []MCPServerRule{}
	dl := driftFindings("host-a", Policy{AllowedMCPServers: &lockdown}, managedSettings{}, testNow())
	if len(dl) != 1 || dl[0].Severity != model.SeverityHigh || !strings.Contains(dl[0].Title, "LOCKDOWN") {
		t.Errorf("MCP lockdown drift = %+v, want one HIGH finding", dl)
	}

	// A host that fully matches the authored intent does not drift.
	rendered, err := Render(expected)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	matching, err := parseLive(rendered)
	if err != nil {
		t.Fatalf("parse rendered: %v", err)
	}
	if d := driftFindings("host-a", expected, matching, testNow()); len(d) != 0 {
		t.Errorf("matching host must not drift, got %+v", d)
	}

	// fallbackModel is an ORDERED chain: same set, different order = drift.
	reordered := managedSettings{FallbackModel: fallbackModelsToRaw([]string{"claude-haiku-4-5", "claude-sonnet-4-6"})}
	dOrder := driftFindings("host-a", Policy{FallbackModels: []string{"claude-sonnet-4-6", "claude-haiku-4-5"}}, reordered, testNow())
	if len(dOrder) != 1 || !strings.Contains(dOrder[0].Title, "fallbackModel") {
		t.Errorf("reordered fallback chain must drift, got %+v", dOrder)
	}
}

func TestS148PrecedencePreviewCarriesNewFacts(t *testing.T) {
	var sawFallback, sawParent, sawSafeMode bool
	for _, l := range PrecedencePreview() {
		switch l.Scope {
		case "fallback-model-no-merge":
			sawFallback = true
		case "parent-settings-tier":
			sawParent = true
		case "safe-mode":
			sawSafeMode = true
		}
	}
	if !sawFallback || !sawParent || !sawSafeMode {
		t.Errorf("precedence preview missing 2.1.17x facts: fallback=%v parent=%v safeMode=%v",
			sawFallback, sawParent, sawSafeMode)
	}
}
