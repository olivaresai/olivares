// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

func postureRefs(fs []model.FindingReport) map[string]model.FindingReport {
	out := map[string]model.FindingReport{}
	for _, f := range fs {
		if f.SubjectKind == subjectPosture {
			out[f.SubjectRef] = f
		}
	}
	return out
}

func gatherWith(t *testing.T, cfg map[string]string) *captureSink {
	t.Helper()
	s := New()
	s.now = func() time.Time { return time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC) }
	if _, ok := cfg["managed_dir"]; !ok {
		t.Setenv("OPENCODE_TEST_MANAGED_CONFIG_DIR", t.TempDir())
	}
	delete(cfg, "managed_dir")
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

func fixturePath(name string) string {
	return filepath.Join("testdata", name)
}

func TestDescriptor(t *testing.T) {
	s := New()
	d := s.Descriptor()
	if d.Name != Name {
		t.Errorf("Descriptor.Name = %q, want %q", d.Name, Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Descriptor.Type = %q, want source", d.Type)
	}
	if len(d.ConfigFields) != 3 {
		t.Errorf("Descriptor.ConfigFields = %d, want 3", len(d.ConfigFields))
	}
}

func TestGather_PermissiveDefaultPosture(t *testing.T) {
	sink := gatherWith(t, map[string]string{"project_config_path": fixturePath("permissive-default.json")})
	refs := postureRefs(sink.findings())
	for _, ref := range []string{"admin_override.absent", "permission.default", "permission.bash", "permission.edit"} {
		if _, ok := refs[ref]; !ok {
			t.Errorf("missing posture finding %q", ref)
		}
	}
}

func TestGather_BlanketAllowPosture(t *testing.T) {
	sink := gatherWith(t, map[string]string{"project_config_path": fixturePath("blanket-allow.json")})
	refs := postureRefs(sink.findings())
	if _, ok := refs["permission.allow.top-level"]; !ok {
		t.Fatal("blanket permission allow should be flagged")
	}
	if _, ok := refs["permission.bash"]; !ok {
		t.Error("blanket allow should leave bash ungated")
	}
	if _, ok := refs["permission.edit"]; !ok {
		t.Error("blanket allow should leave edit ungated")
	}
}

func TestGather_HardenedNoFindings(t *testing.T) {
	managedDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(managedDir, "opencode.json"), []byte(`{"permission":{"edit":"ask","bash":"deny"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_TEST_MANAGED_CONFIG_DIR", managedDir)
	sink := gatherWith(t, map[string]string{
		"project_config_path": fixturePath("hardened.json"),
		"managed_dir":         managedDir,
	})
	refs := postureRefs(sink.findings())
	for _, ref := range []string{
		"admin_override.absent",
		"permission.default",
		"permission.allow.top-level",
		"permission.bash",
		"permission.edit",
		"share.auto",
		"autoupdate.true",
		"experimental.continue_loop_on_deny",
	} {
		if _, ok := refs[ref]; ok {
			t.Errorf("hardened config should not emit %q", ref)
		}
	}
	if f, ok := refs["admin_override.present"]; !ok {
		t.Fatal("managed config should be detected")
	} else if f.Severity != model.SeverityInfo {
		t.Errorf("admin present severity = %q, want info", f.Severity)
	}
}

func TestGather_AdminOverrideDetected(t *testing.T) {
	managedDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(managedDir, "opencode.jsonc"), []byte(`{"permission":{"edit":"ask","bash":"ask"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_TEST_MANAGED_CONFIG_DIR", managedDir)
	sink := gatherWith(t, map[string]string{
		"project_config_path": fixturePath("hardened.json"),
		"managed_dir":         managedDir,
	})
	refs := postureRefs(sink.findings())
	if _, ok := refs["admin_override.present"]; !ok {
		t.Fatal("expected managed admin config presence finding")
	}
}

func TestGather_NoAdminOverride(t *testing.T) {
	sink := gatherWith(t, map[string]string{"project_config_path": fixturePath("hardened.json")})
	refs := postureRefs(sink.findings())
	if _, ok := refs["admin_override.absent"]; !ok {
		t.Fatal("expected admin override absent finding")
	}
}

func TestGather_MCPEdges(t *testing.T) {
	sink := gatherWith(t, map[string]string{"project_config_path": fixturePath("remote-local-mcp.json")})
	var refs []string
	for _, e := range sink.edges() {
		if e.ResourceKind == resourceMCPServer {
			refs = append(refs, e.ResourceRef)
			if e.OriginKind != "agent" || e.OriginRef != defaultAgentRef {
				t.Errorf("unexpected origin on MCP edge: %#v", e)
			}
			if e.Mode != model.ModeUnknown || e.Source != model.SignalConfig || e.Confidence != model.ConfidenceAttributed {
				t.Errorf("unexpected signal metadata on MCP edge: %#v", e)
			}
		}
	}
	got := strings.Join(refs, ",")
	if !strings.Contains(got, "node") {
		t.Errorf("missing local MCP command edge in %q", got)
	}
	if !strings.Contains(got, "https://mcp.example.com/rpc") {
		t.Errorf("missing remote MCP URL edge in %q", got)
	}
	if strings.Contains(got, "disabled") {
		t.Errorf("disabled MCP server emitted edge: %q", got)
	}
}

func TestGather_CredentialInConfig(t *testing.T) {
	literal := gatherWith(t, map[string]string{"project_config_path": fixturePath("apiKey-literal.json")})
	if _, ok := postureRefs(literal.findings())["provider.apiKey"]; !ok {
		t.Fatal("literal provider.options.apiKey should be flagged")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(path, []byte(`{"provider":{"anthropic":{"options":{"apiKey":"{env:ANTHROPIC_API_KEY}"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	envRef := gatherWith(t, map[string]string{"project_config_path": path})
	if _, ok := postureRefs(envRef.findings())["provider.apiKey"]; ok {
		t.Fatal("{env:} apiKey token must not be flagged as a literal credential")
	}
}

func TestGather_ShareAutoEgress(t *testing.T) {
	sink := gatherWith(t, map[string]string{"project_config_path": fixturePath("share-auto.json")})
	if _, ok := postureRefs(sink.findings())["share.auto"]; !ok {
		t.Fatal("share=auto should be flagged")
	}
}

func TestGather_GlobalProjectDeepMerge(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global.json")
	projectPath := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(globalPath, []byte(`{
		"permission": {"edit": "ask", "bash": "ask"},
		"mcp": {"global": {"type": "remote", "url": "https://global.example.com/mcp"}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, []byte(`{
		"tools": {"read": true},
		"mcp": {"project": {"type": "local", "command": ["node", "project.js"]}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := gatherWith(t, map[string]string{
		"global_config_path":  globalPath,
		"project_config_path": projectPath,
	})
	refs := postureRefs(sink.findings())
	if _, ok := refs["permission.default"]; ok {
		t.Fatal("project layer should deep-merge over global without dropping global permission")
	}

	var globalMCP, projectMCP, readTool bool
	for _, e := range sink.edges() {
		if e.ResourceKind == resourceMCPServer && e.ResourceRef == "https://global.example.com/mcp" {
			globalMCP = true
		}
		if e.ResourceKind == resourceMCPServer && e.ResourceRef == "node" {
			projectMCP = true
		}
		if e.ResourceKind == resourceTool && e.ResourceRef == "read" {
			readTool = true
		}
	}
	if !globalMCP || !projectMCP || !readTool {
		t.Fatalf("deep merge edges missing: globalMCP=%v projectMCP=%v readTool=%v", globalMCP, projectMCP, readTool)
	}
}

func TestGather_AutonomyFlags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(path, []byte(`{
		"permission": {"edit": "ask", "bash": "ask"},
		"autoupdate": true,
		"experimental": {"continue_loop_on_deny": true}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := gatherWith(t, map[string]string{"project_config_path": path})
	refs := postureRefs(sink.findings())
	for _, ref := range []string{"autoupdate.true", "experimental.continue_loop_on_deny"} {
		if _, ok := refs[ref]; !ok {
			t.Fatalf("missing autonomy posture finding %q", ref)
		}
	}
}

func TestCoverage_OTelOnOff(t *testing.T) {
	off := gatherWith(t, map[string]string{"project_config_path": fixturePath("permissive-default.json")})
	var offCoverage model.FindingReport
	for _, f := range off.findings() {
		if f.SubjectKind == subjectCoverage {
			offCoverage = f
		}
	}
	if offCoverage.Severity != model.SeverityMedium {
		t.Fatalf("OTEL off coverage severity = %q, want medium", offCoverage.Severity)
	}

	on := gatherWith(t, map[string]string{"project_config_path": fixturePath("hardened.json")})
	var onCoverage model.FindingReport
	for _, f := range on.findings() {
		if f.SubjectKind == subjectCoverage {
			onCoverage = f
		}
	}
	if onCoverage.Severity != model.SeverityInfo {
		t.Fatalf("OTEL on coverage severity = %q, want info", onCoverage.Severity)
	}
}

func TestGather_InvalidJSONC(t *testing.T) {
	sink := gatherWith(t, map[string]string{"project_config_path": fixturePath("invalid.jsonc")})
	if _, ok := postureRefs(sink.findings())["config.invalid.project"]; !ok {
		t.Fatal("invalid JSONC should emit config.invalid.project")
	}
}

func TestGather_JSONCCommentsTolerated(t *testing.T) {
	sink := gatherWith(t, map[string]string{"project_config_path": fixturePath("jsonc-comments.jsonc")})
	refs := postureRefs(sink.findings())
	if _, ok := refs["config.invalid.project"]; ok {
		t.Fatal("JSONC comments/trailing commas should be tolerated")
	}
	var foundTool bool
	for _, e := range sink.edges() {
		if e.ResourceKind == resourceTool && e.ResourceRef == "read" {
			foundTool = true
		}
	}
	if !foundTool {
		t.Fatal("expected enabled tool edge from JSONC fixture")
	}
}

func TestMinimalData(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("supersecret-file-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "opencode.json")
	cfg := fmt.Sprintf(`{
		"permission": {"edit": "ask", "bash": "ask"},
		"provider": {"anthropic": {"options": {"apiKey": "{file:%s}"}}},
		"agent": {"researcher": {"prompt": "PROMPT_SECRET_DO_NOT_EMIT", "tools": {"read": true}}},
		"instructions": ["AGENTS.md", "https://example.com/policy?token=do-not-emit"]
	}`, secretPath)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := gatherWith(t, map[string]string{"project_config_path": cfgPath})
	var customAgent bool
	for _, e := range sink.edges() {
		if e.ResourceKind == resourceAgent && e.ResourceRef == "researcher" {
			customAgent = true
		}
	}
	if !customAgent {
		t.Fatal("custom agent should emit a permitted config edge")
	}
	dump := fmt.Sprintf("%#v", sink.obs)
	for _, forbidden := range []string{
		"PROMPT_SECRET_DO_NOT_EMIT",
		"supersecret-file-content",
		secretPath,
		"do-not-emit",
	} {
		if strings.Contains(dump, forbidden) {
			t.Fatalf("minimal-data emission leaked %q in %#v", forbidden, sink.obs)
		}
	}
}
