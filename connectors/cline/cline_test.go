// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cline

import (
	"context"
	"os"
	"path/filepath"
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
	s.now = func() time.Time { return time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

func TestGovernedClineConfig(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(userPath, []byte(`{
		"cline.apiProvider": "anthropic",
		"cline.apiModelId": "claude-sonnet-4-20250514",
		"cline.mcpServers": {
			"mainServer": { "url": "https://mcp.example.com" }
		},
		"cline.allowedTools": ["read_file", "write_file"]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := gatherWith(t, map[string]string{"user_settings_path": userPath})

	gaps := postureRefs(sink.findings())
	// Provider is set — no provider.model gap.
	if _, ok := gaps["provider.model"]; ok {
		t.Error("provider.model gap should not be present when both are set")
	}
	// No auto-approve — no autoApprove gap.
	if _, ok := gaps["autoApprove"]; ok {
		t.Error("autoApprove gap should not be present when auto-approve is off")
	}
	// No API key in settings — no apiKey gap.
	if _, ok := gaps["apiKey"]; ok {
		t.Error("apiKey gap should not be present when no key in settings")
	}

	// PERMITTED edges: one MCP server + two tools.
	var mcp, tools int
	for _, e := range sink.edges() {
		switch e.ResourceKind {
		case resourceMCPServer:
			mcp++
		case resourceTool:
			tools++
		}
	}
	if mcp != 1 {
		t.Fatalf("MCP edges = %d, want 1", mcp)
	}
	if tools != 2 {
		t.Fatalf("tool edges = %d, want 2", tools)
	}
}

func TestAutoApproveAndCredentialFlagged(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(userPath, []byte(`{
		"cline.autoApprove": ["read_file", "write_file"],
		"cline.apiKey": "sk-ant-secret"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := gatherWith(t, map[string]string{"user_settings_path": userPath})
	gaps := postureRefs(sink.findings())

	if f, ok := gaps["autoApprove"]; !ok {
		t.Error("expected autoApprove gap")
	} else if f.Severity != model.SeverityHigh {
		t.Errorf("autoApprove severity = %q, want high", f.Severity)
	}

	if f, ok := gaps["apiKey"]; !ok {
		t.Error("expected apiKey gap")
	} else if f.Severity != model.SeverityHigh {
		t.Errorf("apiKey severity = %q, want high", f.Severity)
	}
}

func TestKiloCodeVariant(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(userPath, []byte(`{
		"kilocode.apiProvider": "anthropic",
		"kilocode.apiModelId": "claude-sonnet-4-20250514"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := gatherWith(t, map[string]string{
		"user_settings_path": userPath,
		"variant":            "kilocode",
	})

	// Should find the provider/model from kilocode.* keys.
	var inv model.FindingReport
	for _, f := range sink.findings() {
		if f.Kind == "inventory" {
			inv = f
		}
	}
	if inv.Title == "" {
		t.Fatal("expected inventory finding")
	}
	if inv.SubjectKind != subjectConfig {
		t.Errorf("inventory SubjectKind = %q, want %q", inv.SubjectKind, subjectConfig)
	}
}

func TestMissingConfigEmitsFindings(t *testing.T) {
	dir := t.TempDir()
	sink := gatherWith(t, map[string]string{"user_settings_path": filepath.Join(dir, "absent.json")})
	var hasCoverage, hasInventory bool
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectCoverage {
			hasCoverage = true
		}
		if f.Kind == "inventory" {
			hasInventory = true
		}
	}
	if !hasCoverage {
		t.Error("expected coverage finding")
	}
	if !hasInventory {
		t.Error("expected inventory finding")
	}
}

func TestInvalidJSONFlagged(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(userPath, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := gatherWith(t, map[string]string{"user_settings_path": userPath})
	var hasInvalid bool
	for _, f := range sink.findings() {
		if f.SubjectRef == "settings.invalid."+scopeUser {
			hasInvalid = true
		}
	}
	if !hasInvalid {
		t.Fatal("a present-but-malformed settings file must be flagged")
	}
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
}
