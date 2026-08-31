// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package goose

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

const governedProfile = `
default:
  provider: anthropic
  model: claude-sonnet-4-20250514
  extensions:
    mainServer:
      type: sse
      url: https://mcp.example.com
    disabledExt:
      type: stdio
      command: /usr/bin/noop
      enabled: false
  toolshim:
    require_approval: true
    allowed_tools:
      - read_file
      - write_file
`

func TestGovernedProfileEmitsExpectedFindings(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "profiles.yaml")
	if err := os.WriteFile(cfgPath, []byte(governedProfile), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := gatherWith(t, map[string]string{"config_path": cfgPath})

	gaps := postureRefs(sink.findings())
	// Admin override is always flagged (Goose has no admin settings).
	if _, ok := gaps["admin_override"]; !ok {
		t.Error("admin_override finding should always be present")
	}
	// Provider is pinned — no provider.model gap.
	if _, ok := gaps["provider.model"]; ok {
		t.Error("provider.model gap should not be present when provider+model are set")
	}
	// Tool approval is true — no toolshim gap.
	if _, ok := gaps["toolshim.require_approval"]; ok {
		t.Error("toolshim.require_approval gap should not be present when approval is required")
	}

	// PERMITTED edges: one MCP server (disabled one excluded) + two tools.
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
		t.Fatalf("permitted MCP edges = %d, want 1 (disabled excluded)", mcp)
	}
	if tools != 2 {
		t.Fatalf("permitted tool edges = %d, want 2", tools)
	}
}

func TestMissingConfigEmitsFindings(t *testing.T) {
	dir := t.TempDir()
	sink := gatherWith(t, map[string]string{"config_path": filepath.Join(dir, "absent.yaml")})
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

func TestInvalidYAMLFlagged(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "profiles.yaml")
	if err := os.WriteFile(cfgPath, []byte("{{invalid yaml"), 0o600); err != nil {
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
		t.Fatal("a present-but-malformed profiles.yaml must be flagged")
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
