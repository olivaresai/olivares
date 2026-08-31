// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openclaw

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func (s *captureSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range s.obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return out
}

func (s *captureSink) edges() []model.EdgeObservation {
	var out []model.EdgeObservation
	for _, o := range s.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func findingRefs(fs []model.FindingReport) map[string][]model.FindingReport {
	out := map[string][]model.FindingReport{}
	for _, f := range fs {
		ref := findingRef(f)
		if ref != "" {
			out[ref] = append(out[ref], f)
		}
	}
	return out
}

func gatherWith(t *testing.T, cfg map[string]string) *captureSink {
	t.Helper()
	isolateProcessEnv(t)
	return gatherWithoutEnvIsolation(t, cfg)
}

func gatherWithoutEnvIsolation(t *testing.T, cfg map[string]string) *captureSink {
	t.Helper()
	s := New()
	s.now = func() time.Time { return time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

func isolateProcessEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "GOOGLE_API_KEY",
		"GEMINI_API_KEY", "AWS_ACCESS_KEY_ID", "AWS_PROFILE", "AZURE_OPENAI_API_KEY",
		"MISTRAL_API_KEY", "XAI_API_KEY", "OLLAMA_API_KEY", "OPENCLAW_CONFIG_PATH",
		"OPENCLAW_STATE_DIR", "OPENCLAW_HOME", "OPENCLAW_PROFILE",
	} {
		t.Setenv(key, "")
	}
}

func fixtureInstall(t *testing.T, fixture string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	data, err := os.ReadFile(testdataPath(fixture))
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, configFileName)
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir, cfgPath
}

func copyFixture(t *testing.T, dir, name, dest string) {
	t.Helper()
	data, err := os.ReadFile(testdataPath(name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, dest), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testdataPath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", name)
}

var catalogPostureRefs = []string{
	"sandbox.off",
	"exec.unrestricted",
	"exec.patch_escape",
	"gateway.exposed",
	"gateway.funnel",
	"gateway.control_ui_insecure",
	"gateway.tls_off_lan",
	"channels.dm_open",
	"channels.group_open",
	"channels.dangerous_flags",
	"channels.config_writes",
	"elevated.enabled",
	"skills.unpinned_sources",
	"skills.symlink_targets",
	"skills.uploaded_archives",
	"plugins.hook_grants",
	"plugins.install_policy",
	"discovery.broadcast",
	"logging.redaction_weakened",
	"logging.tmp_logfile",
	"models.no_allowlist",
	"credentials.literal",
	"session.dm_shared",
	"state.legacy_era",
	"config.invalid",
	"update.dev_channel",
}

func TestRedFlagsCatalogFindings(t *testing.T) {
	dir, cfgPath := fixtureInstall(t, "red-flags.json5")
	if err := os.MkdirAll(filepath.Join(dir, "skills-extra", "community-shell"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills-extra", "community-shell", "SKILL.md"), []byte("---\nname: community-shell\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacyConfigFileName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := gatherWith(t, map[string]string{"config_path": cfgPath, "state_dir": dir})
	refs := findingRefs(sink.findings())
	for _, ref := range catalogPostureRefs {
		if ref == "config.invalid" {
			continue
		}
		if _, ok := refs[ref]; !ok {
			t.Errorf("missing expected finding ref %q", ref)
		}
	}
	if got := refs["gateway.funnel"][0].Severity; got != model.SeverityHigh {
		t.Fatalf("gateway.funnel severity = %s, want high when auth.mode is not password", got)
	}
	if got := refs["coverage"][0].Severity; got != model.SeverityMedium {
		t.Fatalf("coverage severity = %s, want medium when diagnostics.otel.enabled=false", got)
	}
	if refs["gateway.exposed"][0].SubjectKind != subjectPosture {
		t.Fatalf("SubjectKind = %q, want %q", refs["gateway.exposed"][0].SubjectKind, subjectPosture)
	}
	if got := refs["gateway.exposed"][0].SubjectRef; got != "openclaw/gateway.exposed" {
		t.Fatalf("gateway.exposed SubjectRef = %q", got)
	}
	if got := refs["sandbox.off"][0].SubjectRef; got != "openclaw/sandbox.off" {
		t.Fatalf("sandbox.off SubjectRef = %q", got)
	}
}

func TestHardenedConfigSuppressesCatalogPostureFindings(t *testing.T) {
	dir, cfgPath := fixtureInstall(t, "hardened.json5")
	sink := gatherWith(t, map[string]string{"config_path": cfgPath, "state_dir": dir})
	refs := findingRefs(sink.findings())
	for _, ref := range catalogPostureRefs {
		if _, ok := refs[ref]; ok {
			t.Errorf("hardened fixture unexpectedly emitted %q", ref)
		}
	}
	if got := refs["coverage"][0].Severity; got != model.SeverityLow {
		t.Fatalf("coverage severity = %s, want low when diagnostics.otel.enabled=true", got)
	}
}

func TestInvalidConfigFindingForIncludeEscape(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.json5"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, configFileName)
	if err := os.WriteFile(cfgPath, []byte(`{$include: "../outside.json5"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	sink := gatherWith(t, map[string]string{"config_path": cfgPath, "state_dir": dir})
	if _, ok := findingRefs(sink.findings())["config.invalid"]; !ok {
		t.Fatal("missing config.invalid finding for include escape")
	}
}

func TestIncludesEnvSubstitutionAndEdges(t *testing.T) {
	dir, cfgPath := fixtureInstall(t, "includes.json5")
	copyFixture(t, dir, "base.inc.json5", "base.inc.json5")
	copyFixture(t, dir, "overlay.inc.json5", "overlay.inc.json5")
	t.Setenv("OPENCLAW_INCLUDED_MODEL", "anthropic/claude-sonnet-4-20250514")

	sink := gatherWith(t, map[string]string{"config_path": cfgPath, "state_dir": dir})
	refs := findingRefs(sink.findings())
	if _, ok := refs["config.invalid"]; ok {
		t.Fatal("includes fixture should resolve without config.invalid")
	}
	assertEdge(t, sink.edges(), "openclaw", resourceChannel, "slack")
	assertEdge(t, sink.edges(), "openclaw", resourceModel, "anthropic/claude-sonnet-4-20250514")
	assertEdge(t, sink.edges(), "openclaw", resourceModel, "anthropic/claude-haiku-3-5-20241022")
}

func TestMultiAgentEdgesUseAgentSubjects(t *testing.T) {
	dir, cfgPath := fixtureInstall(t, "multi-agent.json5")
	sink := gatherWith(t, map[string]string{"config_path": cfgPath, "state_dir": dir})

	assertEdge(t, sink.edges(), "openclaw", resourceChannel, "telegram")
	assertNoEdge(t, sink.edges(), "openclaw/research", resourceChannel, "telegram")
	assertEdge(t, sink.edges(), "openclaw/research", resourceModel, "openai/gpt-4.1")
	assertEdge(t, sink.edges(), "openclaw/research", resourceSkill, "researcher")
	assertEdge(t, sink.edges(), "openclaw", resourceSkill, "baseline")
	assertNoEdge(t, sink.edges(), "openclaw/research", resourceSkill, "baseline")
}

func TestProviderEvidenceUsesStateDotEnvOnly(t *testing.T) {
	dir, cfgPath := fixtureInstall(t, "minimal.json5")
	t.Setenv("ANTHROPIC_API_KEY", "control-plane-key")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("ANTHROPIC_API_KEY=present\nOPENAI_API_KEY=present\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := gatherWith(t, map[string]string{"config_path": cfgPath, "state_dir": dir})
	if _, ok := findingRefs(sink.findings())["models.no_allowlist"]; !ok {
		t.Fatal("expected models.no_allowlist from state-dir .env provider evidence")
	}

	dir, cfgPath = fixtureInstall(t, "minimal.json5")
	t.Setenv("ANTHROPIC_API_KEY", "control-plane-key")
	t.Setenv("OPENAI_API_KEY", "control-plane-key")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("ANTHROPIC_API_KEY=present\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.WriteFile(".env", []byte("OPENAI_API_KEY=present\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sink = gatherWithoutEnvIsolation(t, map[string]string{"config_path": cfgPath, "state_dir": dir})
	if _, ok := findingRefs(sink.findings())["models.no_allowlist"]; ok {
		t.Fatal("CWD .env or process env leaked into provider evidence")
	}
}

func TestChannelEvidenceComesFromConfigOnly(t *testing.T) {
	dir, cfgPath := fixtureInstall(t, "hardened.json5")
	t.Setenv("DISCORD_BOT_TOKEN", "control-plane-token")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SLACK_BOT_TOKEN=present\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := gatherWith(t, map[string]string{"config_path": cfgPath, "state_dir": dir})
	assertEdge(t, sink.edges(), "openclaw", resourceChannel, "telegram")
	assertNoEdge(t, sink.edges(), "openclaw", resourceChannel, "discord")
	assertNoEdge(t, sink.edges(), "openclaw", resourceChannel, "slack")
}

func TestDiscoveryDefaultLegacyAndProfiles(t *testing.T) {
	isolateProcessEnv(t)
	minimal, err := os.ReadFile(testdataPath("minimal.json5"))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile(testdataPath("legacy-era.json5"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	defaultDir := filepath.Join(home, ".openclaw")
	profileDir := filepath.Join(home, ".openclaw-blue")
	legacyDir := filepath.Join(home, legacyStateDirName)
	for _, dir := range []string{defaultDir, profileDir, legacyDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(defaultDir, configFileName), minimal, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, configFileName), minimal, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, legacyConfigFileName), legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	s := New()
	s.now = func() time.Time { return time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	subjects := inventorySubjects(sink.findings())
	for _, want := range []string{"openclaw", "openclaw/blue", "openclaw/legacy"} {
		if !slices.Contains(subjects, want) {
			t.Fatalf("inventory subjects = %v, missing %s", subjects, want)
		}
	}

	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Chdir(emptyHome)
	sink = &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("absent install emitted %d observations, want 0", len(sink.obs))
	}
}

func TestMCPEdgesAndPerAgentRouting(t *testing.T) {
	dir, cfgPath := fixtureInstall(t, "mcp.json5")
	sink := gatherWith(t, map[string]string{"config_path": cfgPath, "state_dir": dir})
	edges := sink.edges()

	// The default agent inherits the full configured global set.
	for _, server := range []string{"postgres", "webfetch", "localfs", "vault", "loopback"} {
		assertEdge(t, edges, "openclaw", resourceMCP, server)
	}
	// The research agent is routed to only postgres; it must not reach the rest.
	assertEdge(t, edges, "openclaw/research", resourceMCP, "postgres")
	for _, server := range []string{"webfetch", "localfs", "vault", "loopback"} {
		assertNoEdge(t, edges, "openclaw/research", resourceMCP, server)
	}
	// The mcp ToolRef is set on the edge.
	for _, e := range edges {
		if e.ResourceKind == resourceMCP && e.ToolRef != "mcp" {
			t.Fatalf("mcp edge ToolRef = %q, want mcp", e.ToolRef)
		}
	}

	// Inventory reports the configured MCP server count (empty stanzas excluded).
	var invMCP bool
	for _, f := range sink.findings() {
		if f.Kind == "inventory" && f.SubjectRef == "openclaw" {
			if !strings.Contains(f.Title, "mcp=5") {
				t.Fatalf("inventory title missing mcp=5: %q", f.Title)
			}
			invMCP = true
		}
	}
	if !invMCP {
		t.Fatal("no inventory finding for the default install")
	}
}

func TestMCPPostureFindings(t *testing.T) {
	dir, cfgPath := fixtureInstall(t, "mcp.json5")
	sink := gatherWith(t, map[string]string{"config_path": cfgPath, "state_dir": dir})
	refs := findingRefs(sink.findings())

	cases := []struct {
		ref string
		sev model.Severity
	}{
		{"mcp.postgres.remote_runner", model.SeverityMedium},
		{"mcp.webfetch.remote_url", model.SeverityMedium},
		{"mcp.vault.env_credential", model.SeverityHigh},
	}
	for _, tc := range cases {
		got, ok := refs[tc.ref]
		if !ok {
			t.Errorf("missing expected MCP posture finding %q", tc.ref)
			continue
		}
		if got[0].Severity != tc.sev {
			t.Errorf("%s severity = %s, want %s", tc.ref, got[0].Severity, tc.sev)
		}
		if got[0].SubjectRef != "openclaw/"+tc.ref {
			t.Errorf("%s SubjectRef = %q, want openclaw/%s", tc.ref, got[0].SubjectRef, tc.ref)
		}
	}

	// A pinned local binary and a loopback URL must NOT produce posture findings.
	for _, ref := range []string{
		"mcp.localfs.remote_runner", "mcp.localfs.remote_url", "mcp.localfs.env_credential",
		"mcp.loopback.remote_url", "mcp.postgres.remote_url", "mcp.postgres.env_credential",
	} {
		if _, ok := refs[ref]; ok {
			t.Errorf("unexpected MCP posture finding %q", ref)
		}
	}
}

func TestMCPCredentialInEnvAndHeaders(t *testing.T) {
	// Header-based auth (Authorization: Bearer …) is the common remote-MCP pattern
	// and must be flagged alongside stdio env credentials.
	if !mcpEnvLiteralCredential(mcpServer{Headers: map[string]any{"Authorization": "Bearer sk-live-abc123"}}) {
		t.Fatal("literal credential in an MCP header was not detected")
	}
	if !mcpEnvLiteralCredential(mcpServer{Env: map[string]any{"API_TOKEN": "sk-inline"}}) {
		t.Fatal("literal credential in MCP env was not detected")
	}
	// An env/secret reference (${VAR}) is the secure pattern and must NOT flag.
	if mcpEnvLiteralCredential(mcpServer{Headers: map[string]any{"Authorization": "${MCP_TOKEN}"}}) {
		t.Fatal("a ${VAR}-referenced header credential must not be flagged")
	}
	// A non-credential header must not flag.
	if mcpEnvLiteralCredential(mcpServer{Headers: map[string]any{"Accept": "application/json"}}) {
		t.Fatal("a benign header was wrongly flagged as a credential")
	}
}

func TestSystemdUnitInventorySignal(t *testing.T) {
	dir, cfgPath := fixtureInstall(t, "minimal.json5")
	unitDir := t.TempDir()
	for _, name := range []string{"openclaw.service", "openclaw-blue.service", "unrelated.service"} {
		if err := os.WriteFile(filepath.Join(unitDir, name), []byte("[Unit]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	isolateProcessEnv(t)
	s := New()
	s.now = func() time.Time { return time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) }
	s.systemdRoots = []string{unitDir}
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"config_path": cfgPath, "state_dir": dir}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if got := s.discoverSystemdUnits(); len(got) != 2 {
		t.Fatalf("discoverSystemdUnits = %v, want the 2 openclaw units (unrelated excluded)", got)
	}
	var found bool
	for _, f := range sink.findings() {
		if f.Kind == "inventory" && strings.Contains(f.Title, "systemd_units=2") {
			found = true
		}
	}
	if !found {
		t.Fatal("inventory finding did not carry systemd_units=2")
	}
}

func TestMeterProviderSplit(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) }

	sample, ok := s.Meter("anthropic/claude-sonnet-4-20250514", 1000, 500, 200, time.Time{})
	if !ok {
		t.Fatal("expected ok=true for a known Anthropic model")
	}
	if sample.ProviderRef != "anthropic" {
		t.Errorf("ProviderRef = %q, want anthropic", sample.ProviderRef)
	}
	if sample.ModelRef != "claude-sonnet-4-20250514" {
		t.Errorf("ModelRef = %q, want claude-sonnet-4-20250514", sample.ModelRef)
	}
	if sample.CostType != CostType || sample.CostMicroUSD <= 0 {
		t.Fatalf("unexpected sample: %#v", sample)
	}

	nonAnthropic, ok := s.Meter("openai/gpt-4.1", 1000, 500, 0, time.Time{})
	if ok {
		t.Fatal("expected ok=false for non-Anthropic pricing")
	}
	if nonAnthropic.ProviderRef != "openai" || nonAnthropic.ModelRef != "gpt-4.1" || nonAnthropic.CostMicroUSD != 0 {
		t.Fatalf("unexpected non-Anthropic sample: %#v", nonAnthropic)
	}

	bare, ok := s.Meter("claude-sonnet-4-20250514", 1000, 500, 0, time.Time{})
	if !ok || bare.ProviderRef != "anthropic" {
		t.Fatalf("bare Claude model should keep anthropic fallback: %#v ok=%v", bare, ok)
	}
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name {
		t.Errorf("Descriptor.Name = %q, want %q", d.Name, Name)
	}
	if d.Version != "0.3.0" {
		t.Errorf("Descriptor.Version = %q, want 0.3.0", d.Version)
	}
	var fields []string
	for _, f := range d.ConfigFields {
		fields = append(fields, f.Key)
	}
	for _, want := range []string{"agent_ref", "state_dir", "config_path"} {
		if !slices.Contains(fields, want) {
			t.Fatalf("descriptor fields = %v, missing %s", fields, want)
		}
	}
}

func assertEdge(t *testing.T, edges []model.EdgeObservation, origin, kind, ref string) {
	t.Helper()
	for _, e := range edges {
		if e.OriginRef == origin && e.ResourceKind == kind && e.ResourceRef == ref {
			return
		}
	}
	t.Fatalf("missing edge origin=%s kind=%s ref=%s in %#v", origin, kind, ref, edges)
}

func assertNoEdge(t *testing.T, edges []model.EdgeObservation, origin, kind, ref string) {
	t.Helper()
	for _, e := range edges {
		if e.OriginRef == origin && e.ResourceKind == kind && e.ResourceRef == ref {
			t.Fatalf("unexpected edge origin=%s kind=%s ref=%s", origin, kind, ref)
		}
	}
}

func inventorySubjects(fs []model.FindingReport) []string {
	var out []string
	for _, f := range fs {
		if f.Kind == "inventory" {
			out = append(out, f.SubjectRef)
		}
	}
	slices.Sort(out)
	return out
}
