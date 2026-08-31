// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// Model-governance managed-settings keys (VERIFIED 2026-06-27 against
// code.claude.com/docs/en/{settings,model-config,server-managed-settings,mcp}).
// Per new key these tests prove the lockstep property: a key survives
// ParsePolicyFromWire→Render (CanonicalJSON), counts in HasAnyKeys, validates
// server-side, and drifts against a bare host.

// wireS268 exercises every key in its VERIFIED wire shape.
const wireS268 = `{
  "availableModels": ["sonnet", "haiku"],
  "enforceAvailableModels": true,
  "disableClaudeAiConnectors": true
}`

func TestS268RoundTripPreservesNewKeys(t *testing.T) {
	canon, err := CanonicalJSON([]byte(wireS268))
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	s := string(canon)
	for _, want := range []string{
		`"availableModels"`,
		`"sonnet"`,
		`"haiku"`,
		`"enforceAvailableModels": true`,
		`"disableClaudeAiConnectors": true`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("canonical round-trip lost %q\n%s", want, s)
		}
	}
	if !HasAnyKeys([]byte(wireS268)) {
		t.Error("HasAnyKeys must report a keys-only document as governing")
	}
	if issues := ValidateJSON([]byte(wireS268)); len(issues) != 0 {
		t.Errorf("fixture must validate clean, got %v", issues)
	}
}

func TestS268HasAnyKeysPerKey(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		want bool
	}{
		"available models":         {`{"availableModels": ["opus"]}`, true},
		"available models empty":   {`{"availableModels": []}`, false},
		"enforce available models": {`{"enforceAvailableModels": true}`, true},
		"enforce available false":  {`{"enforceAvailableModels": false}`, false},
		"disable connectors":       {`{"disableClaudeAiConnectors": true}`, true},
		"disable connectors false": {`{"disableClaudeAiConnectors": false}`, false},
	} {
		if got := HasAnyKeys([]byte(tc.doc)); got != tc.want {
			t.Errorf("%s: HasAnyKeys = %v, want %v", name, got, tc.want)
		}
	}
}

func TestS268ValidateJSONNegatives(t *testing.T) {
	cases := map[string]struct {
		doc  string
		want string
	}{
		"empty model alias": {
			`{"availableModels": ["sonnet", ""]}`,
			"availableModels[1] is empty",
		},
		"enforce without allowlist": {
			`{"enforceAvailableModels": true}`,
			"enforceAvailableModels is true but availableModels is empty",
		},
		"enforce non-bool": {
			`{"enforceAvailableModels": "yes"}`,
			"wrong type",
		},
		"disable connectors non-bool": {
			`{"disableClaudeAiConnectors": "yes"}`,
			"wrong type",
		},
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
}

func TestS268ValidateJSONPositives(t *testing.T) {
	cases := map[string]string{
		"valid allowlist + enforce": `{"availableModels": ["sonnet", "haiku"], "enforceAvailableModels": true}`,
		"allowlist only":            `{"availableModels": ["opus"]}`,
		"disable connectors":        `{"disableClaudeAiConnectors": true}`,
	}
	for name, doc := range cases {
		if issues := ValidateJSON([]byte(doc)); len(issues) != 0 {
			t.Errorf("%s: want clean validation, got %v", name, issues)
		}
	}
}

func TestS268DriftAvailableModels(t *testing.T) {
	expected := Policy{
		AvailableModels: []string{"sonnet", "haiku"},
	}
	live := managedSettings{AvailableModels: []string{"opus"}}
	findings := driftFindings("test", expected, live, testNow())
	if !titleContains(findings, "model allowlist") {
		t.Error("expected drift finding for availableModels set mismatch")
	}
	live2 := managedSettings{AvailableModels: []string{"haiku", "sonnet"}}
	findings2 := driftFindings("test", expected, live2, testNow())
	if titleContains(findings2, "model allowlist") {
		t.Error("no drift expected when sets match (order-independent)")
	}
}

func TestS268DriftEnforceAvailableModels(t *testing.T) {
	expected := Policy{EnforceAvailableModels: true}
	live := managedSettings{}
	findings := driftFindings("test", expected, live, testNow())
	if !titleContains(findings, "enforceAvailableModels") {
		t.Error("expected drift finding for enforceAvailableModels")
	}
	live2 := managedSettings{EnforceAvailableModels: true}
	findings2 := driftFindings("test", expected, live2, testNow())
	if titleContains(findings2, "enforceAvailableModels") {
		t.Error("no drift expected when host matches")
	}
}

func TestS268DriftDisableClaudeAiConnectors(t *testing.T) {
	expected := Policy{DisableClaudeAiConnectors: true}
	live := managedSettings{}
	findings := driftFindings("test", expected, live, testNow())
	if !titleContains(findings, "MCP connectors are NOT disabled") {
		t.Error("expected drift finding for disableClaudeAiConnectors")
	}
	live2 := managedSettings{DisableClaudeAiConnectors: true}
	findings2 := driftFindings("test", expected, live2, testNow())
	if titleContains(findings2, "MCP connectors are NOT disabled") {
		t.Error("no drift expected when host matches")
	}
}

func TestS268ModelLockBypassedByBaseURL(t *testing.T) {
	expected := Policy{
		AvailableModels:          []string{"sonnet"},
		EnforceAvailableModels:   true,
		AuthorizedGatewayBaseURL: "https://gateway.corp.example.com",
		Env: map[string]string{
			EnvEnableTelemetry: "1",
		},
	}
	live := managedSettings{
		Env: map[string]string{
			EnvBaseURL:         "https://rogue.endpoint.example.com",
			EnvEnableTelemetry: "1",
		},
	}
	findings := driftFindings("test", expected, live, testNow())
	if !titleContains(findings, "ANTHROPIC_BASE_URL") {
		t.Error("expected base-URL bypass finding")
	}
	if !titleContains(findings, "model allowlist") {
		t.Error("expected availableModels drift (host has none)")
	}
	if !titleContains(findings, "enforceAvailableModels") {
		t.Error("expected enforceAvailableModels drift (host has none)")
	}
	for _, f := range findings {
		if strings.Contains(f.Title, "ANTHROPIC_BASE_URL") && f.Severity != model.SeverityHigh {
			t.Errorf("base-URL bypass must be High, got %s", f.Severity)
		}
	}
}

// titleContains reports whether any finding's Title contains the substring.
func titleContains(findings []model.FindingReport, sub string) bool {
	for _, f := range findings {
		if strings.Contains(f.Title, sub) {
			return true
		}
	}
	return false
}
