// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package crossplane_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/crossplane"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// capturingSink records the observations a Gather emits.
type capturingSink struct {
	findings []model.FindingReport
	all      []model.Observation
}

func (c *capturingSink) Emit(_ context.Context, obs model.Observation) error {
	c.all = append(c.all, obs)
	if f, ok := obs.(model.FindingReport); ok {
		c.findings = append(c.findings, f)
	}
	return nil
}

func gather(t *testing.T, path string) *capturingSink {
	t.Helper()
	s := crossplane.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": path}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

// find returns the single finding for subject, or fails.
func find(t *testing.T, fs []model.FindingReport, subject string) model.FindingReport {
	t.Helper()
	var hits []model.FindingReport
	for _, f := range fs {
		if f.SubjectRef == subject {
			hits = append(hits, f)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly 1 finding for %q, got %d (%+v)", subject, len(hits), fs)
	}
	return hits[0]
}

func TestGatherInventoriesXRDs(t *testing.T) {
	sink := gather(t, filepath.Join("testdata", "xrds.yaml"))

	// Exactly the two XRDs produce findings; the Composition is ignored.
	if len(sink.findings) != 2 || len(sink.all) != 2 {
		t.Fatalf("finding count = %d (all=%d), want 2 (the Composition must be skipped)", len(sink.findings), len(sink.all))
	}

	// The two-version XRD: group, kind, and BOTH versions with their served posture.
	db := find(t, sink.findings, "xdatabases.custom-api.example.org")
	if db.Severity != model.SeverityInfo {
		t.Errorf("XRD severity = %s, want info (inventory)", db.Severity)
	}
	if db.Kind != "crossplane_xrd" {
		t.Errorf("Kind = %q, want crossplane_xrd", db.Kind)
	}
	if db.SubjectKind != "crossplane.xrd" {
		t.Errorf("SubjectKind = %q, want crossplane.xrd", db.SubjectKind)
	}
	for _, want := range []string{
		"xdatabases.custom-api.example.org", // <plural>.<group> surface
		"kind XDatabase",                    // composite kind
		"v1alpha1[served]",                  // served + referenceable version
		"v1beta1[not served]",               // the deprecated version, posture intact
	} {
		if !strings.Contains(db.Title, want) {
			t.Errorf("Title %q does not contain %q", db.Title, want)
		}
	}

	// The single-version XRD.
	net := find(t, sink.findings, "xnetworks.custom-api.example.org")
	if net.Severity != model.SeverityInfo {
		t.Errorf("XRD severity = %s, want info", net.Severity)
	}
	if !strings.Contains(net.Title, "kind XNetwork") || !strings.Contains(net.Title, "v1[served]") {
		t.Errorf("Title %q missing kind/version surface", net.Title)
	}

	// DetailHash is a 64-hex SHA-256, never the raw key.
	if len(db.DetailHash) != 64 {
		t.Errorf("DetailHash len = %d, want 64 hex chars", len(db.DetailHash))
	}
}

// TestNoRawLeak asserts no schema property value (the connection-secret namespace in
// the fixture) leaks into any emitted field — minimal data (docs/SECURITY-HARDENING.md). DetailHash
// is a hash, never the value; the connector reads only structural API names.
func TestNoRawLeak(t *testing.T) {
	sink := gather(t, filepath.Join("testdata", "xrds.yaml"))
	for _, f := range sink.findings {
		blob := f.Kind + "|" + string(f.Severity) + "|" + f.SubjectKind + "|" + f.SubjectRef + "|" + f.Title + "|" + f.DetailHash
		if strings.Contains(blob, "team-platform-secrets") || strings.Contains(blob, "a1b2c3d") || strings.Contains(blob, "secret-namespace") {
			t.Errorf("raw schema content leaked into a finding: %s", blob)
		}
	}
}

func TestOpenRequiresPath(t *testing.T) {
	if err := crossplane.New().Open(context.Background(), sdk.Config{}); err == nil {
		t.Fatal("Open with no path = nil, want error")
	}
}

func TestDescriptor(t *testing.T) {
	d := crossplane.New().Descriptor()
	if d.Name != "olivares.crossplane" || d.Type != sdk.TypeSource {
		t.Errorf("unexpected descriptor: %+v", d)
	}
	if len(d.ConfigFields) != 1 || d.ConfigFields[0].Key != "path" || !d.ConfigFields[0].Required {
		t.Errorf("descriptor must declare a required 'path' field: %+v", d.ConfigFields)
	}
}

// TestDirectoryInput confirms a directory of manifests is read the same as a file.
func TestDirectoryInput(t *testing.T) {
	sink := gather(t, "testdata")
	if len(sink.findings) == 0 {
		t.Fatal("expected findings from the testdata directory")
	}
}
