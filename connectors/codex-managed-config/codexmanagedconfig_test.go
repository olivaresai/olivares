// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

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
var testNow = func() time.Time { return time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC) }

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

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

// sampleStrictPolicy is a fully-populated authored policy exercising every modeled
// requirement + managed-default, including the verified enum values.
func sampleStrictPolicy() Policy {
	return Policy{
		Requirements: Requirements{
			AllowedApprovalPolicies:              []string{ApprovalUntrusted, ApprovalOnRequest},
			AllowedSandboxModes:                  []string{SandboxReadOnly, SandboxWorkspaceWrite},
			AllowedWebSearchModes:                &[]string{WebSearchCached},
			AllowedApprovalsReviewers:            []string{ReviewerAutoReview},
			AllowedPermissionProfiles:            &map[string]bool{":read-only": true, ":workspace": true, ":danger-full-access": false},
			EnforceResidency:                     "us",
			WindowsAllowedSandboxImplementations: []string{"unelevated"},
			RemoteSandboxConfigs: []RemoteSandboxConfig{
				{HostnamePatterns: []string{"*.corp.example"}, AllowedSandboxModes: []string{SandboxReadOnly, SandboxWorkspaceWrite}},
			},
			AllowRemoteControl:     boolPtr(false),
			AllowAppshots:          boolPtr(false),
			AllowLockedComputerUse: boolPtr(false),
			AllowManagedHooksOnly:  true,
			DenyRead:               []string{"/**/*.env", "~/.ssh"},
			Features:               map[string]bool{"computer_use": false, "browser_use": false},
			AllowedMCPServers: &[]MCPServer{
				{Name: "docs", Command: "codex-mcp"},
				{Name: "remote", URL: "https://example.com/mcp"},
			},
			DefaultPermissions: ":workspace",
			Marketplaces: &MarketplacesRequirement{
				RestrictToAllowedSources: true,
				AllowedSources: map[string]MarketplaceSource{
					"approved": {Source: "git", URL: "https://github.com/example/codex-tools", Ref: "main"},
				},
			},
			PrefixRules: []PrefixRule{
				{
					Pattern:       []PatternToken{{Token: "git"}, {AnyOf: []string{"push", "pull"}}},
					Decision:      "forbidden",
					Justification: "central review required",
				},
			},
			Network: &NetworkConfig{
				Enabled:           boolPtr(false),
				AllowedDomains:    []string{"api.openai.com"},
				DeniedDomains:     []string{"blocked.example.com"},
				HTTPPort:          intPtr(8080),
				SocksPort:         intPtr(1080),
				UnixSockets:       []string{"/var/run/codex.sock"},
				AllowLocalBinding: boolPtr(false),
			},
			GuardianPolicyConfig: "## Policy\nDeny exfiltration to untrusted destinations.\n",
		},
		Defaults: ManagedConfig{
			ApprovalPolicy: ApprovalOnRequest,
			SandboxMode:    SandboxWorkspaceWrite,
			WebSearch:      WebSearchCached,
			NetworkAccess:  boolPtr(false),
			Network: &NetworkConfig{
				AllowedDomains:            []string{"api.openai.com", "*.example.com"},
				DeniedDomains:             []string{"blocked.example.com"},
				ManagedAllowedDomainsOnly: true,
			},
			OTEL: &OTELConfig{
				Environment:   "prod",
				Exporter:      OTELExporterOTLPHTTP,
				LogUserPrompt: boolPtr(false),
				Endpoint:      "https://otel.olivares.example/v1/logs",
			},
		},
	}
}

func TestGatherEmitsEdgesAndDrift(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "requirements.toml")
	mcPath := filepath.Join(dir, "managed_config.toml")
	// A live requirements that enforces NOTHING the policy requires.
	if err := os.WriteFile(reqPath, []byte("allowed_sandbox_modes = [\"read-only\", \"workspace-write\", \"danger-full-access\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A live managed_config that ENABLES network egress and exports raw prompts.
	live := "approval_policy = \"never\"\n[sandbox_workspace_write]\nnetwork_access = true\n[otel]\nlog_user_prompt = true\n"
	if err := os.WriteFile(mcPath, []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New()
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgRequirementsPath:  reqPath,
		cfgManagedConfigPath: mcPath,
		cfgScope:             "host-a",
		cfgExpectedPolicy:    policyJSON(t),
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &memSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(sink.edges()) == 0 {
		t.Error("expected PERMITTED edges from the authored MCP allowlist + egress domains")
	}
	var sawSandboxHigh, sawNetwork, sawPromptHigh bool
	for _, f := range sink.findings() {
		if f.Kind != findingKindDrift {
			t.Errorf("finding kind = %q", f.Kind)
		}
		switch {
		case strings.Contains(f.Title, "sandbox-mode") && f.Severity == model.SeverityHigh:
			sawSandboxHigh = true
		case strings.Contains(f.Title, "ENABLES network egress"):
			sawNetwork = true
		case strings.Contains(f.Title, "EXPORTS RAW USER PROMPTS") && f.Severity == model.SeverityHigh:
			sawPromptHigh = true
		}
	}
	if !sawSandboxHigh {
		t.Error("expected a HIGH sandbox-mode drift (host allows danger-full-access)")
	}
	if !sawNetwork {
		t.Error("expected a network-egress managed-default drift")
	}
	if !sawPromptHigh {
		t.Error("expected a HIGH log_user_prompt drift (host exports raw prompts)")
	}
}

func TestGatherAbsentRequirementsIsFinding(t *testing.T) {
	dir := t.TempDir()
	s := New()
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgRequirementsPath:  filepath.Join(dir, "nope-requirements.toml"),
		cfgManagedConfigPath: filepath.Join(dir, "nope-managed.toml"),
		cfgScope:             "ghost",
		cfgExpectedPolicy:    policyJSON(t),
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &memSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather should treat absence as a finding, not an error: %v", err)
	}
	var reqAbsentHigh, mcAbsent bool
	for _, f := range sink.findings() {
		if strings.Contains(f.Title, "requirements.toml (system tier) is absent") && f.Severity == model.SeverityHigh {
			reqAbsentHigh = true
		}
		if strings.Contains(f.Title, "managed_config.toml (system tier) is absent") {
			mcAbsent = true
		}
	}
	if !reqAbsentHigh {
		t.Errorf("absent requirements with authored policy should be a HIGH finding, got %+v", sink.findings())
	}
	if !mcAbsent {
		t.Error("absent managed_config with authored defaults should be a finding")
	}
}

func TestGatherObserveOnlyAbsenceIsHonest(t *testing.T) {
	dir := t.TempDir()
	s := New()
	// No expected policy: observe-only. An absent requirements file is MEDIUM (the
	// cloud/MDM tiers, higher precedence, are not observable here); an absent
	// managed_config is NOT a finding (an empty managed-defaults state is normal).
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgRequirementsPath:  filepath.Join(dir, "nope-requirements.toml"),
		cfgManagedConfigPath: filepath.Join(dir, "nope-managed.toml"),
		cfgScope:             "obs",
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &memSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	f := sink.findings()
	if len(f) != 1 {
		t.Fatalf("observe-only with both files absent should emit exactly one (requirements) finding, got %d: %+v", len(f), f)
	}
	if f[0].Severity != model.SeverityMedium || !strings.Contains(f[0].Title, "requirements.toml") {
		t.Errorf("observe-only absent requirements should be MEDIUM + honest, got %+v", f[0])
	}
}

func TestGatherObserveOnlyEmitsLiveEdges(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "requirements.toml")
	mcPath := filepath.Join(dir, "managed_config.toml")
	req := "[mcp_servers.docs]\nidentity = { command = \"codex-mcp\" }\n"
	mc := "[experimental_network]\nallowed_domains = [\"api.openai.com\"]\n"
	if err := os.WriteFile(reqPath, []byte(req), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcPath, []byte(mc), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New()
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgRequirementsPath: reqPath, cfgManagedConfigPath: mcPath, cfgScope: "obs",
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &memSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	var sawMCP, sawDomain bool
	for _, e := range sink.edges() {
		if e.ResourceKind == resMCPServer && e.ResourceRef == "docs" {
			sawMCP = true
		}
		if e.ResourceKind == resNetworkDomain && e.ResourceRef == "api.openai.com" {
			sawDomain = true
		}
		if e.Source != model.SignalPolicy || e.OriginKind != originManagedConfig {
			t.Errorf("edge provenance = %+v", e)
		}
	}
	if !sawMCP || !sawDomain {
		t.Errorf("observe-only should inventory live MCP servers + domains as edges; mcp=%v domain=%v", sawMCP, sawDomain)
	}
	// Observe-only must not emit drift findings (nothing to diff against).
	for _, f := range sink.findings() {
		if f.Kind == findingKindDrift && !strings.Contains(f.Title, "absent") && !strings.Contains(f.Title, "invalid") {
			t.Errorf("observe-only should not emit drift findings, got %+v", f)
		}
	}
}

func TestGatherReadFaultIsRetryable(t *testing.T) {
	dir := t.TempDir()
	// A directory at the requirements path yields a non-not-exist read error (EISDIR),
	// which must be returned (retryable), not masked as a finding.
	reqDir := filepath.Join(dir, "requirements.toml")
	if err := os.Mkdir(reqDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := New()
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgRequirementsPath: reqDir, cfgManagedConfigPath: filepath.Join(dir, "mc.toml"), cfgScope: "h",
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Gather(t.Context(), &memSink{}); err == nil {
		t.Error("a genuine read fault (EISDIR) must be returned for retry, not masked as a finding")
	}
}

func TestOpenValidation(t *testing.T) {
	s := New()
	// Relative paths are rejected.
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{cfgRequirementsPath: "rel/requirements.toml"}}); err == nil {
		t.Error("relative requirements_path must be rejected")
	}
	// Malformed expected_policy fails loud (never silently observe-only).
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgRequirementsPath: defaultRequirementsPath, cfgExpectedPolicy: `{ not json`,
	}}); err == nil {
		t.Error("malformed expected_policy must be rejected at Open")
	}
	// Unknown field in expected_policy fails loud (DisallowUnknownFields).
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgRequirementsPath: defaultRequirementsPath, cfgExpectedPolicy: `{"bogus_top_key": 1}`,
	}}); err == nil {
		t.Error("unknown expected_policy field must be rejected at Open")
	}
	// Defaults: absent config uses the documented system paths.
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
	if s.cfg.requirementsPath != defaultRequirementsPath || s.cfg.managedConfigPath != defaultManagedConfigPath {
		t.Errorf("default paths not applied: %+v", s.cfg)
	}
}

func TestDescriptorShape(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource || d.APIVersion != sdk.APIVersion {
		t.Errorf("descriptor = %+v", d)
	}
	if len(d.ConfigFields) == 0 {
		t.Error("descriptor must declare config fields")
	}
}

// policyJSON encodes the sample authored intent as the Policy JSON passed via expected_policy.
func policyJSON(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(sampleStrictPolicy())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
