// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openhands

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

const governedConfig = `
[llm]
model = "claude-sonnet-4-20250514"
provider = "anthropic"

[sandbox]
sandbox_type = "docker"

[core]
max_iterations = 100
otel_exporter_otlp_endpoint = "http://localhost:4318"

[mcp]
[mcp.servers]
[mcp.servers.mainServer]
url = "https://mcp.example.com"
`

func TestGovernedConfigHasNoPostureGaps(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(governedConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := gatherWith(t, map[string]string{"config_path": cfgPath})

	gaps := postureRefs(sink.findings())
	if len(gaps) != 0 {
		refs := make([]string, 0, len(gaps))
		for k := range gaps {
			refs = append(refs, k)
		}
		t.Fatalf("a governed config should raise no posture gaps, got %d: %v", len(gaps), refs)
	}

	// PERMITTED edges: one MCP server.
	var mcp int
	for _, e := range sink.edges() {
		if e.ResourceKind == resourceMCPServer {
			mcp++
			if e.ResourceRef != "https://mcp.example.com" {
				t.Errorf("mcp edge resource = %q", e.ResourceRef)
			}
		}
	}
	if mcp != 1 {
		t.Fatalf("permitted MCP edges = %d, want 1", mcp)
	}

	// Coverage finding: Info (OTEL configured).
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectCoverage && f.Severity != model.SeverityInfo {
			t.Errorf("coverage severity = %q, want info (OTEL configured)", f.Severity)
		}
	}
}

func TestUngovernedConfigRaisesGaps(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	// Minimal config with credential exposure and no sandbox.
	if err := os.WriteFile(cfgPath, []byte(`
[llm]
api_key = "sk-ant-secret-key"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := gatherWith(t, map[string]string{"config_path": cfgPath})

	gaps := postureRefs(sink.findings())
	wantGaps := []string{"sandbox.type", "llm.model", "llm.api_key", "otel.exporter"}
	for _, ref := range wantGaps {
		if _, ok := gaps[ref]; !ok {
			t.Errorf("missing expected posture gap %q", ref)
		}
	}

	if f, ok := gaps["sandbox.type"]; ok && f.Severity != model.SeverityHigh {
		t.Errorf("sandbox.type severity = %q, want high", f.Severity)
	}
	if f, ok := gaps["llm.api_key"]; ok && f.Severity != model.SeverityHigh {
		t.Errorf("llm.api_key severity = %q, want high", f.Severity)
	}
}

func TestMissingConfigEmitsFindings(t *testing.T) {
	dir := t.TempDir()
	sink := gatherWith(t, map[string]string{"config_path": filepath.Join(dir, "absent.toml")})
	// Missing config = ungoverned, should still emit coverage + inventory.
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
		t.Error("expected coverage finding even with missing config")
	}
	if !hasInventory {
		t.Error("expected inventory finding even with missing config")
	}
}

func TestInvalidConfigFlagged(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("{ this is not toml"), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := gatherWith(t, map[string]string{"config_path": cfgPath})
	var hasInvalid bool
	for _, f := range sink.findings() {
		if f.SubjectRef == "config.invalid" {
			hasInvalid = true
		}
	}
	if !hasInvalid {
		t.Fatal("a present-but-malformed config must be flagged")
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
	if len(d.ConfigFields) < 2 {
		t.Errorf("expected at least 2 config fields, got %d", len(d.ConfigFields))
	}
}

func TestMaxIterationsEnvOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(`
[core]
max_iterations = 50
`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MAX_ITERATIONS", "9999")

	sink := gatherWith(t, map[string]string{"config_path": cfgPath})

	gaps := postureRefs(sink.findings())
	f, ok := gaps["core.max_iterations"]
	if !ok {
		t.Fatal("MAX_ITERATIONS=9999 should trigger a posture gap but did not")
	}
	if f.Severity != model.SeverityLow {
		t.Errorf("max_iterations severity = %q, want low", f.Severity)
	}
}

func TestMaxIterationsEnvInvalid(t *testing.T) {
	c := config{envMaxIter: "not-a-number", present: true}
	_, ok := c.effectiveMaxIterations()
	if ok {
		t.Error("non-numeric MAX_ITERATIONS should return ok=false")
	}
}
