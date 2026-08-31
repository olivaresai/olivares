// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// testNow is a fixed timestamp for deterministic edge/finding tests.
var testNow = func() time.Time { return time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC) }

// memSink is a minimal sdk.Sink that records observations.
type memSink struct{ obs []model.Observation }

func (s *memSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}
func (s *memSink) edges() (out []model.EdgeObservation) {
	for _, o := range s.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return
}
func (s *memSink) findings() (out []model.FindingReport) {
	for _, o := range s.obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return
}

func sampleStrictPolicy() Policy {
	return Policy{
		Permissions: Permissions{
			Allow:                        []string{"Read(/data/**)", "Bash(npm:*)"},
			Deny:                         []string{"Read(/etc/**)"},
			DefaultMode:                  ModePlan,
			DisableBypassPermissionsMode: true,
			DisableAutoMode:              true,
		},
		AllowedMCPServers:          &[]MCPServerRule{{Name: "github"}},
		AllowManagedMCPServersOnly: true,
		StrictKnownMarketplaces: &[]Marketplace{
			{Source: MarketplaceSourceGitHub, Repo: "acme-corp/approved-plugins"},
			{Source: MarketplaceSourceGitHub, Repo: "acme-corp/security-tools", Ref: "v2.0"},
			{Source: MarketplaceSourceURL, URL: "https://plugins.example.com/marketplace.json"},
		},
		BlockedMarketplaces: []Marketplace{
			{Source: MarketplaceSourceGitHub, Repo: "untrusted/plugins"},
		},
		DisableSideloadFlags: true,
		ForceLoginMethod:     "console",
		ForceLoginOrgUUID:    "org-123",
		MinimumVersion:       "2.0.0",
	}
}

func TestRenderRoundTrip(t *testing.T) {
	out, err := Render(sampleStrictPolicy())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`"disableBypassPermissionsMode": "disable"`,
		`"disableAutoMode": "disable"`,
		`"acme-corp/approved-plugins"`,
		`"untrusted/plugins"`,
		`"allowManagedMcpServersOnly": true`,
		`"forceLoginOrgUUID": "org-123"`,
		`"serverName": "github"`,
		`"Read(/data/**)"`,
		`"defaultMode": "plan"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered managed-settings missing %q\n%s", want, s)
		}
	}
	// The rendered JSON must parse back as a valid live config with bypass disabled.
	live, err := parseLive(out)
	if err != nil {
		t.Fatalf("rendered output does not parse: %v", err)
	}
	if live.Permissions == nil || !live.Permissions.bypassDisabled() {
		t.Error("round-trip lost disableBypassPermissionsMode")
	}
}

func TestRenderEmptyPolicy(t *testing.T) {
	out, err := Render(Policy{})
	if err != nil {
		t.Fatalf("render empty: %v", err)
	}
	// An empty policy renders an empty object (no permissions section).
	if strings.Contains(string(out), "permissions") {
		t.Errorf("empty policy should not emit a permissions section: %s", out)
	}
}

func TestInferRuleMode(t *testing.T) {
	cases := map[string]model.AccessMode{
		"Read(/x)":  model.ModeRead,
		"Glob(/x)":  model.ModeRead,
		"Write(/x)": model.ModeWrite,
		"Edit(/x)":  model.ModeWrite,
		"Bash(ls)":  model.ModeUnknown,
		"mcp__x__y": model.ModeUnknown,
	}
	for rule, want := range cases {
		if got := inferRuleMode(rule); got != want {
			t.Errorf("inferRuleMode(%q) = %s, want %s", rule, got, want)
		}
	}
}

func TestPermittedEdges(t *testing.T) {
	edges := permittedEdges("host-a", []string{"Read(/data)", "  ", "Write(/out)"}, testNow())
	if len(edges) != 2 {
		t.Fatalf("want 2 edges (blank skipped), got %d", len(edges))
	}
	for _, e := range edges {
		if e.OriginKind != originManagedPolicy || e.OriginRef != "host-a" || e.Source != model.SignalPolicy {
			t.Errorf("edge provenance = %+v", e)
		}
		if e.ResourceKind != resPermission {
			t.Errorf("edge resource kind = %q", e.ResourceKind)
		}
	}
}

func TestDriftFindings(t *testing.T) {
	expected := sampleStrictPolicy()

	// A live config that enforces NOTHING the policy requires → drift on every key.
	bare := managedSettings{}
	d := driftFindings("host-a", expected, bare, testNow())
	keys := map[string]model.Severity{}
	for _, f := range d {
		keys[f.Title] = f.Severity
	}
	if len(d) < 7 {
		t.Fatalf("expected drift on many keys, got %d: %+v", len(d), d)
	}
	// The dangerous gaps are HIGH severity.
	var sawBypassHigh, sawOrgHigh, sawMcpHigh bool
	for _, f := range d {
		if strings.Contains(f.Title, "bypassPermissions") && f.Severity == model.SeverityHigh {
			sawBypassHigh = true
		}
		if strings.Contains(f.Title, "forceLoginOrgUUID") && f.Severity == model.SeverityHigh {
			sawOrgHigh = true
		}
		if strings.Contains(f.Title, "managed-MCP-only") && f.Severity == model.SeverityHigh {
			sawMcpHigh = true
		}
		if f.Kind != findingKindDrift {
			t.Errorf("finding kind = %q", f.Kind)
		}
	}
	if !sawBypassHigh || !sawOrgHigh || !sawMcpHigh {
		t.Errorf("expected HIGH drift for bypass/org/mcp; got bypass=%v org=%v mcp=%v", sawBypassHigh, sawOrgHigh, sawMcpHigh)
	}

	// A live config that fully matches the authored intent → ZERO drift.
	rendered, _ := Render(expected)
	matching, _ := parseLive(rendered)
	if d := driftFindings("host-a", expected, matching, testNow()); len(d) != 0 {
		t.Errorf("a host that matches the policy must not drift, got %+v", d)
	}
}

func TestGatherEmitsEdgesAndDrift(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed-settings.json")
	// Live file: bypass NOT disabled, missing the org pin → should drift.
	live := `{"permissions":{"allow":["Read(/data/**)"],"defaultMode":"acceptEdits"}}`
	if err := os.WriteFile(path, []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New()
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgConfigPath: path, cfgScope: "host-a", cfgExpectedPolicy: policyJSON(t),
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &memSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(sink.edges()) == 0 {
		t.Error("expected PERMITTED edges from the authored allow rules")
	}
	var drift bool
	for _, f := range sink.findings() {
		if strings.Contains(f.Title, "bypassPermissions") {
			drift = true
		}
	}
	if !drift {
		t.Errorf("expected a bypassPermissions drift finding, got %+v", sink.findings())
	}
}

func TestGatherAbsentFileIsFinding(t *testing.T) {
	s := New()
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgConfigPath: "/nonexistent/managed-settings.json", cfgScope: "ghost",
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &memSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather should treat absence as a finding, not an error: %v", err)
	}
	f := sink.findings()
	if len(f) != 1 || f[0].Severity != model.SeverityHigh || !strings.Contains(f[0].Title, "ungoverned") {
		t.Errorf("absent file should be a HIGH ungoverned finding, got %+v", f)
	}
}

func TestOpenValidation(t *testing.T) {
	s := New()
	// Relative config_path is rejected.
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{cfgConfigPath: "relative/path.json"}}); err == nil {
		t.Error("relative config_path must be rejected")
	}
	// Malformed expected_policy fails loud (never silently observe-only).
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgConfigPath: "/etc/claude-code/managed-settings.json", cfgExpectedPolicy: `{ not json`,
	}}); err == nil {
		t.Error("malformed expected_policy must be rejected at Open")
	}
}

// --- helpers ----------------------------------------------------------------

// policyJSON encodes the sample authored intent as the Policy JSON passed via the
// expected_policy config (not the rendered managed-settings wire shape).
func policyJSON(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(sampleStrictPolicy())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
