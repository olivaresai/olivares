// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package geminicli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// captureSink records emitted observations.
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

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func gather(t *testing.T, cfg map[string]string) *captureSink {
	t.Helper()
	s := New()
	s.now = func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

const governedSystem = `{
  "security": { "disableYoloMode": true, "auth": { "selectedType": "oauth-personal", "enforcedType": "oauth-personal" } },
  "tools": { "core": ["GlobTool", "ReadFileTool"] },
  "mcp": { "allowed": ["mainServer"] },
  "mcpServers": { "mainServer": { "httpUrl": "https://mcp.example.com" } },
  "telemetry": { "enabled": true, "target": "local", "logPrompts": false },
  "general": { "defaultApprovalMode": "default" },
  "privacy": { "usageStatisticsEnabled": false }
}`

func TestGovernedConfigHasNoPostureGaps(t *testing.T) {
	dir := t.TempDir()
	sysPath := filepath.Join(dir, "settings.json")
	writeFile(t, sysPath, governedSystem)
	policyDir := filepath.Join(dir, "policies")
	if err := os.Mkdir(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(policyDir, "rules.toml"), "[[rules]]\ntoolName = \"ShellTool\"\ndecision = \"deny\"\n")

	sink := gather(t, map[string]string{
		"system_settings_path": sysPath,
		"system_defaults_path": filepath.Join(dir, "missing-defaults.json"),
		"admin_policy_dir":     policyDir,
	})

	gaps := postureRefs(sink.findings())
	if len(gaps) != 0 {
		t.Fatalf("a fully-governed config should raise no posture gaps, got %d: %v", len(gaps), keysOf(gaps))
	}

	// PERMITTED edges: one MCP server + two allowed tools, all Source=config.
	var mcp, tools int
	for _, e := range sink.edges() {
		if e.Source != model.SignalConfig || e.OriginRef != defaultAgentRef {
			t.Errorf("edge wrong source/origin: %+v", e)
		}
		switch e.ResourceKind {
		case resourceMCPServer:
			mcp++
			if e.ResourceRef != "https://mcp.example.com" {
				t.Errorf("mcp edge resource = %q", e.ResourceRef)
			}
		case resourceTool:
			tools++
		}
	}
	if mcp != 1 || tools != 2 {
		t.Fatalf("permitted edges: mcp=%d tools=%d, want 1 and 2", mcp, tools)
	}

	// Coverage finding present and Info (telemetry local).
	var cov model.FindingReport
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectCoverage {
			cov = f
		}
	}
	if cov.Severity != model.SeverityInfo {
		t.Errorf("coverage severity = %q, want info (telemetry local)", cov.Severity)
	}

	// Policy presence: active with 1 rule file.
	var pol bool
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectPolicy {
			pol = true
			if f.Severity != model.SeverityInfo {
				t.Errorf("policy presence severity = %q, want info (present)", f.Severity)
			}
		}
	}
	if !pol {
		t.Error("expected a policy-presence finding")
	}
}

func TestUngovernedConfigRaisesGaps(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.json")
	writeFile(t, userPath, `{ "general": { "defaultApprovalMode": "auto_edit" } }`)

	sink := gather(t, map[string]string{
		"system_settings_path": filepath.Join(dir, "no-system.json"), // absent → no system layer
		"system_defaults_path": filepath.Join(dir, "no-defaults.json"),
		"user_settings_path":   userPath,
		"admin_policy_dir":     filepath.Join(dir, "no-policies"), // absent → not flagged
	})

	gaps := postureRefs(sink.findings())
	wantHigh := map[string]bool{"system_settings": true, "security.disableYoloMode": true, "telemetry.enabled": true}
	for ref := range wantHigh {
		f, ok := gaps[ref]
		if !ok {
			t.Errorf("missing expected posture gap %q", ref)
			continue
		}
		if f.Severity != model.SeverityHigh {
			t.Errorf("%q severity = %q, want high", ref, f.Severity)
		}
	}
	if _, ok := gaps["general.defaultApprovalMode"]; !ok {
		t.Error("auto_edit approval mode should be flagged")
	}
	if _, ok := gaps["mcp.allowed"]; !ok {
		t.Error("missing mcp.allowed gap")
	}
	if _, ok := gaps["security.auth.enforcedType"]; !ok {
		t.Error("missing enforcedType gap")
	}
	// No policy dir exists → no policy finding (optional surface, not flagged absent).
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectPolicy {
			t.Errorf("absent optional policy dir must not be flagged: %q", f.Title)
		}
	}
}

func TestSystemOverrideWinsPrecedence(t *testing.T) {
	dir := t.TempDir()
	// User wants YOLO enabled (disableYoloMode false); SYSTEM forbids it (true). System wins.
	userPath := filepath.Join(dir, "user.json")
	sysPath := filepath.Join(dir, "system.json")
	writeFile(t, userPath, `{ "security": { "disableYoloMode": false }, "telemetry": { "enabled": true, "target": "gcp" } }`)
	writeFile(t, sysPath, `{ "security": { "disableYoloMode": true } }`)

	sink := gather(t, map[string]string{
		"system_settings_path": sysPath,
		"system_defaults_path": filepath.Join(dir, "none.json"),
		"user_settings_path":   userPath,
		"admin_policy_dir":     filepath.Join(dir, "none"),
	})
	gaps := postureRefs(sink.findings())
	if _, ok := gaps["security.disableYoloMode"]; ok {
		t.Error("system override (disableYoloMode=true) must win over the user's false — no YOLO gap expected")
	}
	// Telemetry target=gcp → coverage Low (exports to Google, not the control plane).
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectCoverage && f.Severity != model.SeverityLow {
			t.Errorf("target=gcp coverage severity = %q, want low", f.Severity)
		}
	}
}

func TestInvalidLayerFlagged(t *testing.T) {
	dir := t.TempDir()
	sysPath := filepath.Join(dir, "system.json")
	writeFile(t, sysPath, `{ this is not json `)
	sink := gather(t, map[string]string{
		"system_settings_path": sysPath,
		"system_defaults_path": filepath.Join(dir, "none.json"),
		"admin_policy_dir":     filepath.Join(dir, "none"),
	})
	var invalid bool
	for _, f := range sink.findings() {
		if f.SubjectRef == "settings.invalid."+scopeSystem {
			invalid = true
		}
	}
	if !invalid {
		t.Fatal("a present-but-malformed settings layer must be flagged")
	}
}

func keysOf(m map[string]model.FindingReport) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
