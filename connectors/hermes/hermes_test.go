// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package hermes

import (
	"context"
	"os"
	"path/filepath"
	"sort"
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

func gatherWith(t *testing.T, cfg map[string]string) (*Source, *captureSink) {
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
	return s, sink
}

func TestRedFlagsCatalogFindings(t *testing.T) {
	clearHermesProcessEnv(t)
	t.Setenv("HERMES_YOLO_MODE", "true")
	t.Setenv("HERMES_ENABLE_PROJECT_PLUGINS", "true")
	t.Setenv("HERMES_MODEL", "gpt-4.1")

	home, managedDir := prepareInstall(t, "red-flags", false)
	_, sink := gatherWith(t, map[string]string{"hermes_home": home, "managed_dir": managedDir})
	refs := findingRefs(sink.findings())

	want := []string{
		"approvals.off",
		"skills.self_write_ungoverned",
		"skills.guard_off",
		"channels.allow_all",
		"channels.dm_open",
		"security.ssrf_opt_out",
		"security.lazy_installs",
		"security.redaction_off",
		"sandbox.weakened",
		"credentials.sudo_password",
		"credentials.api_key_literal",
		"command_allowlist.present",
		"exposure.api_server",
		"exposure.dashboard_basic_password",
		"plugins.project_enabled",
		"managed_scope.absent",
		"skills.pending_writes",
		"skills.community_taps",
		"memory.write_ungoverned",
		"migration.openclaw_absorbed",
	}
	for _, ref := range want {
		if _, ok := refs[ref]; !ok {
			t.Errorf("missing expected posture finding %q", ref)
		}
	}
	for ref, f := range refs {
		if f.SubjectKind != subjectPosture {
			t.Errorf("%s SubjectKind = %q, want %q", ref, f.SubjectKind, subjectPosture)
		}
		if !strings.HasPrefix(f.SubjectRef, "hermes/") {
			t.Errorf("%s SubjectRef = %q, want install/ref encoding", ref, f.SubjectRef)
		}
	}
	if _, ok := refs["admin_override"]; ok {
		t.Fatal("retired admin_override finding must not be emitted")
	}
	if _, ok := refs["telemetry"]; ok {
		t.Fatal("retired OTEL telemetry finding must not be emitted")
	}
}

func TestTerminalUnsandboxedFinding(t *testing.T) {
	clearHermesProcessEnv(t)
	home, managedDir := prepareInstall(t, "minimal", false)
	mustWrite(t, filepath.Join(home, ".env"), "SLACK_BOT_TOKEN=secret-value\n")

	_, sink := gatherWith(t, map[string]string{"hermes_home": home, "managed_dir": managedDir})
	if _, ok := findingRefs(sink.findings())["terminal.unsandboxed"]; !ok {
		t.Fatal("missing terminal.unsandboxed finding for local backend plus messaging channel")
	}
}

func TestHardenedConfigSuppressesCatalogPostureFindings(t *testing.T) {
	clearHermesProcessEnv(t)
	t.Setenv("HERMES_YOLO_MODE", "false")
	t.Setenv("HERMES_ENABLE_PROJECT_PLUGINS", "false")
	t.Setenv("HERMES_DISABLE_LAZY_INSTALLS", "true")
	t.Setenv("HERMES_REDACT_SECRETS", "true")
	t.Setenv("HERMES_MODEL", "")

	home, managedDir := prepareInstall(t, "hardened", true)
	mustWrite(t, filepath.Join(home, ".env"), "SLACK_BOT_TOKEN=secret-value\nHERMES_LANGFUSE_PUBLIC_KEY=public-key\nAPI_SERVER_KEY=server-key\n")

	_, sink := gatherWith(t, map[string]string{"hermes_home": home, "managed_dir": managedDir})
	refs := findingRefs(sink.findings())
	for _, ref := range allCatalogPostureRefs() {
		if _, ok := refs[ref]; ok {
			t.Errorf("hardened config should suppress posture finding %q", ref)
		}
	}
}

func TestInvalidConfigFinding(t *testing.T) {
	clearHermesProcessEnv(t)
	home := t.TempDir()
	managedDir := filepath.Join(t.TempDir(), "managed")
	mustWrite(t, filepath.Join(home, configFileName), "model: [unterminated\n")

	_, sink := gatherWith(t, map[string]string{"hermes_home": home, "managed_dir": managedDir})
	if _, ok := findingRefs(sink.findings())["config.invalid"]; !ok {
		t.Fatal("missing config.invalid finding")
	}
}

func TestManagedScopeLeafMergeOverridesUserConfig(t *testing.T) {
	clearHermesProcessEnv(t)
	home := t.TempDir()
	managedDir := filepath.Join(t.TempDir(), "managed")
	mustCopyFixture(t, "managed-scope/user-config.yaml", filepath.Join(home, configFileName))
	mustCopyFixture(t, "managed-scope/managed-config.yaml", filepath.Join(managedDir, configFileName))

	_, sink := gatherWith(t, map[string]string{"hermes_home": home, "managed_dir": managedDir})
	refs := findingRefs(sink.findings())
	for _, ref := range []string{"approvals.off", "skills.self_write_ungoverned", "skills.guard_off", "security.lazy_installs", "memory.write_ungoverned", "managed_scope.absent", "terminal.unsandboxed"} {
		if _, ok := refs[ref]; ok {
			t.Errorf("managed scope should suppress %q", ref)
		}
	}
}

func TestEdgesAndInventoryFromStateTree(t *testing.T) {
	clearHermesProcessEnv(t)
	home, managedDir := prepareInstall(t, "red-flags", false)

	_, sink := gatherWith(t, map[string]string{"hermes_home": home, "managed_dir": managedDir})
	edges := edgeRefs(sink.edges())
	for _, want := range []string{
		resourceChannel + ":slack",
		resourceChannel + ":weixin",
		resourceSkill + ":community-shell",
		resourceSkill + ":shell-helper",
		resourceModel + ":anthropic/gpt-4.1",
		resourceModel + ":local-lab/gpt-4.1",
		resourceModel + ":openai/gpt-4.1",
		resourceMCPServer + ":filesystem",
	} {
		if !contains(edges, want) {
			t.Errorf("missing edge %s in %v", want, edges)
		}
	}
	if !hasFindingKind(sink.findings(), "inventory") {
		t.Fatal("missing inventory finding")
	}
	if !hasFindingKind(sink.findings(), "coverage") {
		t.Fatal("missing coverage finding")
	}
}

func TestCoverageFindingReflectsLangfuseKey(t *testing.T) {
	clearHermesProcessEnv(t)
	home, managedDir := prepareInstall(t, "minimal", false)
	_, withoutKey := gatherWith(t, map[string]string{"hermes_home": home, "managed_dir": managedDir})
	if coverageSeverity(withoutKey.findings()) != model.SeverityMedium {
		t.Fatalf("coverage without Langfuse key = %q, want medium", coverageSeverity(withoutKey.findings()))
	}

	homeWithKey, managedWithKey := prepareInstall(t, "minimal", false)
	mustWrite(t, filepath.Join(homeWithKey, ".env"), "HERMES_LANGFUSE_PUBLIC_KEY=public-key\n")
	_, withKey := gatherWith(t, map[string]string{"hermes_home": homeWithKey, "managed_dir": managedWithKey})
	if coverageSeverity(withKey.findings()) != model.SeverityLow {
		t.Fatalf("coverage with Langfuse key = %q, want low", coverageSeverity(withKey.findings()))
	}
}

func TestGenericProcessEnvDoesNotCreateHermesEvidence(t *testing.T) {
	clearHermesProcessEnv(t)
	t.Setenv("SLACK_BOT_TOKEN", "control-plane-slack-token")
	t.Setenv("GATEWAY_ALLOW_ALL_USERS", "true")
	t.Setenv("SLACK_ALLOW_ALL_USERS", "true")
	t.Setenv("API_SERVER_ENABLED", "true")
	t.Setenv("API_SERVER_HOST", "0.0.0.0")

	home, managedDir := prepareInstall(t, "minimal", false)
	_, sink := gatherWith(t, map[string]string{"hermes_home": home, "managed_dir": managedDir})
	refs := findingRefs(sink.findings())
	for _, ref := range []string{"channels.allow_all", "exposure.api_server", "terminal.unsandboxed"} {
		if _, ok := refs[ref]; ok {
			t.Fatalf("generic process env alone produced %q", ref)
		}
	}
	for _, edge := range sink.edges() {
		if edge.ResourceKind == resourceChannel {
			t.Fatalf("generic process env alone produced channel edge %+v", edge)
		}
	}
}

func TestHermesDotEnvValuesAreHonored(t *testing.T) {
	clearHermesProcessEnv(t)
	home, managedDir := prepareInstall(t, "minimal", false)
	mustWrite(t, filepath.Join(home, ".env"), "HERMES_YOLO_MODE=true\nHERMES_ENABLE_PROJECT_PLUGINS=true\nHERMES_DISABLE_LAZY_INSTALLS=true\n")

	_, sink := gatherWith(t, map[string]string{"hermes_home": home, "managed_dir": managedDir})
	refs := findingRefs(sink.findings())
	for _, ref := range []string{"approvals.off", "plugins.project_enabled"} {
		if _, ok := refs[ref]; !ok {
			t.Fatalf("missing %q from HERMES_* .env value", ref)
		}
	}
	if _, ok := refs["security.lazy_installs"]; ok {
		t.Fatal("HERMES_DISABLE_LAZY_INSTALLS .env value should suppress lazy install finding")
	}
}

func TestRootHubTapsFallback(t *testing.T) {
	clearHermesProcessEnv(t)
	home, managedDir := prepareInstall(t, "minimal", false)
	mustWrite(t, filepath.Join(home, ".hub", "taps.json"), `{"taps":[{"name":"root-community","trust":"community"}]}`)

	_, sink := gatherWith(t, map[string]string{"hermes_home": home, "managed_dir": managedDir})
	if _, ok := findingRefs(sink.findings())["skills.community_taps"]; !ok {
		t.Fatal("missing community tap finding from root .hub/taps.json fallback")
	}
}

func TestDiscoveryDefaultAndProfiles(t *testing.T) {
	clearHermesProcessEnv(t)
	root := t.TempDir()
	t.Setenv("HOME", root)
	managedDir := filepath.Join(t.TempDir(), "managed")
	defaultHome := filepath.Join(root, ".hermes")
	profileHome := filepath.Join(defaultHome, "profiles", "work")
	mustCopyFixture(t, "minimal/config.yaml", filepath.Join(defaultHome, configFileName))
	mustCopyFixture(t, "minimal/config.yaml", filepath.Join(profileHome, configFileName))

	_, sink := gatherWith(t, map[string]string{"managed_dir": managedDir})
	subjects := inventorySubjects(sink.findings())
	for _, want := range []string{"hermes", "hermes/work"} {
		if !contains(subjects, want) {
			t.Errorf("missing discovered install subject %q in %v", want, subjects)
		}
	}
}

func TestMissingInstallNoop(t *testing.T) {
	clearHermesProcessEnv(t)
	_, sink := gatherWith(t, map[string]string{"hermes_home": filepath.Join(t.TempDir(), "absent"), "managed_dir": filepath.Join(t.TempDir(), "managed")})
	if len(sink.obs) != 0 {
		t.Fatalf("missing install emitted %d observations, want 0", len(sink.obs))
	}
}

func TestMeterProviderAwareness(t *testing.T) {
	clearHermesProcessEnv(t)
	home, managedDir := prepareInstall(t, "red-flags", false)
	src, _ := gatherWith(t, map[string]string{"hermes_home": home, "managed_dir": managedDir})

	sample, ok := src.Meter("gpt-4.1", 1000, 500, 0, time.Time{})
	if ok {
		t.Fatal("expected ok=false for non-Anthropic provider")
	}
	if sample.ProviderRef != "openai" {
		t.Fatalf("ProviderRef = %q, want openai", sample.ProviderRef)
	}
	if sample.CostMicroUSD != 0 {
		t.Fatalf("non-Anthropic cost = %d, want 0", sample.CostMicroUSD)
	}

	sample, ok = src.Meter("anthropic/claude-sonnet-4-20250514", 1000, 500, 200, time.Time{})
	if !ok {
		t.Fatal("expected ok=true for known Anthropic model")
	}
	if sample.ProviderRef != "anthropic" {
		t.Fatalf("ProviderRef = %q, want anthropic", sample.ProviderRef)
	}
	if sample.CostType != CostType {
		t.Fatalf("CostType = %q, want %q", sample.CostType, CostType)
	}
	if sample.CostMicroUSD <= 0 {
		t.Fatal("expected non-zero cost for known Anthropic model")
	}
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name {
		t.Errorf("Descriptor.Name = %q, want %q", d.Name, Name)
	}
	if d.Version != "0.2.0" {
		t.Errorf("Descriptor.Version = %q, want 0.2.0", d.Version)
	}
	keys := map[string]struct{}{}
	for _, field := range d.ConfigFields {
		keys[field.Key] = struct{}{}
	}
	for _, key := range []string{"agent_ref", "hermes_home", "managed_dir", "config_path"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("descriptor missing config field %q", key)
		}
	}
}

func prepareInstall(t *testing.T, fixture string, managed bool) (string, string) {
	t.Helper()
	home := t.TempDir()
	managedDir := filepath.Join(t.TempDir(), "managed")
	mustCopyFixture(t, fixture+"/config.yaml", filepath.Join(home, configFileName))
	if managed {
		mustCopyFixture(t, "hardened/config.yaml", filepath.Join(managedDir, configFileName))
	}
	if fixture == "red-flags" {
		mustWrite(t, filepath.Join(home, ".env"), strings.Join([]string{
			"SLACK_BOT_TOKEN=secret-value",
			"HERMES_LANGFUSE_PUBLIC_KEY=public-key",
			"GATEWAY_ALLOW_ALL_USERS=true",
			"SLACK_ALLOW_ALL_USERS=true",
			"API_SERVER_ENABLED=true",
			"API_SERVER_HOST=0.0.0.0",
			"",
		}, "\n"))
		mustWrite(t, filepath.Join(home, "skills", "community-shell", "SKILL.md"), "---\nname: community-shell\n---\n")
		mustWrite(t, filepath.Join(home, "pending", "skills", "community-shell", "SKILL.md"), "pending\n")
		mustWrite(t, filepath.Join(home, "skills", ".hub", "taps.json"), `{"taps":[{"name":"builtin","trust":"builtin"},{"name":"official","trust":"official"},{"name":"community-tools","trust":"community"}]}`)
		mustWrite(t, filepath.Join(home, "pairing", "slack-approved.json"), `["user-a","user-b"]`)
		mustWrite(t, filepath.Join(home, "migration", "openclaw", "2026-07-04", ".keep"), "")
		mustWrite(t, filepath.Join(home, "memories", "MEMORY.md"), "memory fixture not read\n")
		mustWrite(t, filepath.Join(home, "hermes-agent", "pyproject.toml"), "[project]\nversion = \"0.18.0\"\n")
		mustWrite(t, filepath.Join(home, "AGENTS.md"), "# fixture\n")
	}
	return home, managedDir
}

func mustCopyFixture(t *testing.T, rel, dst string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", rel))
	if err != nil {
		t.Fatalf("read fixture %s: %v", rel, err)
	}
	mustWrite(t, dst, string(data))
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func clearHermesProcessEnv(t *testing.T) {
	t.Helper()
	exact := map[string]struct{}{}
	for _, key := range []string{
		"HERMES_HOME",
		"HERMES_MANAGED_DIR",
		"HERMES_OAUTH_FILE",
		"HERMES_YOLO_MODE",
		"HERMES_ALLOW_PRIVATE_URLS",
		"HERMES_REDACT_SECRETS",
		"HERMES_WRITE_SAFE_ROOT",
		"HERMES_ENABLE_PROJECT_PLUGINS",
		"HERMES_SAFE_MODE",
		"HERMES_DISABLE_LAZY_INSTALLS",
		"GATEWAY_ALLOWED_USERS",
		"GATEWAY_ALLOW_ALL_USERS",
		"API_SERVER_ENABLED",
		"API_SERVER_HOST",
		"API_SERVER_PORT",
		"API_SERVER_KEY",
		"HERMES_MODEL",
		"HERMES_INFERENCE_MODEL",
		"HERMES_LANGFUSE_PUBLIC_KEY",
		"TELEGRAM_BOT_TOKEN",
		"DISCORD_BOT_TOKEN",
		"SLACK_BOT_TOKEN",
		"SLACK_ALLOW_ALL_USERS",
		"WEIXIN_DM_POLICY",
		"WEIXIN_ALLOW_ALL_USERS",
		"EMAIL_ADDRESS",
	} {
		exact[key] = struct{}{}
	}
	prefixes := []string{"WHATSAPP_", "SIGNAL_"}
	for _, kv := range os.Environ() {
		key, value, _ := strings.Cut(kv, "=")
		if _, ok := exact[key]; !ok && !hasPrefix(key, prefixes) {
			continue
		}
		oldValue := value
		oldPresent := true
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if oldPresent {
				_ = os.Setenv(key, oldValue)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

func hasPrefix(s string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func findingRefs(fs []model.FindingReport) map[string]model.FindingReport {
	out := map[string]model.FindingReport{}
	for _, f := range fs {
		if f.SubjectKind != subjectPosture {
			continue
		}
		out[findingRef(f)] = f
	}
	return out
}

func allCatalogPostureRefs() []string {
	return []string{
		"terminal.unsandboxed",
		"approvals.off",
		"skills.self_write_ungoverned",
		"skills.guard_off",
		"channels.allow_all",
		"channels.dm_open",
		"security.ssrf_opt_out",
		"security.lazy_installs",
		"security.redaction_off",
		"sandbox.weakened",
		"credentials.sudo_password",
		"credentials.api_key_literal",
		"command_allowlist.present",
		"exposure.api_server",
		"exposure.dashboard_basic_password",
		"plugins.project_enabled",
		"managed_scope.absent",
		"skills.pending_writes",
		"skills.community_taps",
		"memory.write_ungoverned",
		"migration.openclaw_absorbed",
		"config.invalid",
	}
}

func edgeRefs(edges []model.EdgeObservation) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.ResourceKind+":"+e.ResourceRef)
	}
	sort.Strings(out)
	return out
}

func inventorySubjects(fs []model.FindingReport) []string {
	var out []string
	for _, f := range fs {
		if f.Kind == "inventory" {
			out = append(out, f.SubjectRef)
		}
	}
	sort.Strings(out)
	return out
}

func coverageSeverity(fs []model.FindingReport) model.Severity {
	for _, f := range fs {
		if f.Kind == "coverage" {
			return f.Severity
		}
	}
	return ""
}

func hasFindingKind(fs []model.FindingReport, kind string) bool {
	for _, f := range fs {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
