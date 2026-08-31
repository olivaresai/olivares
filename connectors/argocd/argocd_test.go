// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package argocd_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/argocd"
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
	s := argocd.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": path}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

// find returns the single finding matching subject + a Title substring, or fails.
func find(t *testing.T, fs []model.FindingReport, subject, contains string) model.FindingReport {
	t.Helper()
	var hits []model.FindingReport
	for _, f := range fs {
		if f.SubjectRef == subject && strings.Contains(f.Title, contains) {
			hits = append(hits, f)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly 1 finding for %q containing %q, got %d (%+v)", subject, contains, len(hits), fs)
	}
	return hits[0]
}

func TestGatherClassifiesPosture(t *testing.T) {
	sink := gather(t, filepath.Join("testdata", "applications.yaml"))

	// Healthy/Synced app: Info sync + Info health, NO operation finding.
	sy := find(t, sink.findings, "argocd/payments-api", "sync=Synced")
	if sy.Severity != model.SeverityInfo {
		t.Errorf("Synced severity = %s, want info", sy.Severity)
	}
	he := find(t, sink.findings, "argocd/payments-api", "health=Healthy")
	if he.Severity != model.SeverityInfo {
		t.Errorf("Healthy severity = %s, want info", he.Severity)
	}
	if he.SubjectKind != "argocd.application" {
		t.Errorf("SubjectKind = %q, want argocd.application", he.SubjectKind)
	}

	// Drifted/Degraded/Failed app.
	drift := find(t, sink.findings, "argocd/billing-worker", "OutOfSync")
	if drift.Severity != model.SeverityMedium {
		t.Errorf("OutOfSync severity = %s, want medium", drift.Severity)
	}
	deg := find(t, sink.findings, "argocd/billing-worker", "health=Degraded")
	if deg.Severity != model.SeverityHigh {
		t.Errorf("Degraded severity = %s, want high", deg.Severity)
	}
	op := find(t, sink.findings, "argocd/billing-worker", "operation Failed")
	if op.Severity != model.SeverityHigh {
		t.Errorf("Failed operation severity = %s, want high", op.Severity)
	}

	// Status-less app classified honestly as Unknown (Medium), never Synced/Healthy.
	us := find(t, sink.findings, "argocd/fresh-app", "sync=Unknown")
	if us.Severity != model.SeverityMedium {
		t.Errorf("Unknown sync severity = %s, want medium", us.Severity)
	}
	find(t, sink.findings, "argocd/fresh-app", "health=Unknown")

	// The ConfigMap must be ignored: no finding references it, and only the three
	// Applications produced findings.
	for _, f := range sink.findings {
		if strings.Contains(f.SubjectRef, "argocd-cm") {
			t.Errorf("ConfigMap leaked into a finding: %+v", f)
		}
	}
	// payments-api: 2, billing-worker: 3, fresh-app: 2 => 7 total, all findings.
	if len(sink.findings) != 7 || len(sink.all) != 7 {
		t.Errorf("finding count = %d (all=%d), want 7", len(sink.findings), len(sink.all))
	}
}

// TestNoRawLeak asserts no manifest content (the ConfigMap URL) leaks into any
// emitted field — minimal data (docs/SECURITY-HARDENING.md). DetailHash is a hash, never the value.
func TestNoRawLeak(t *testing.T) {
	sink := gather(t, filepath.Join("testdata", "applications.yaml"))
	for _, f := range sink.findings {
		blob := f.Kind + "|" + string(f.Severity) + "|" + f.SubjectKind + "|" + f.SubjectRef + "|" + f.Title + "|" + f.DetailHash
		if strings.Contains(blob, "argocd.example.com") || strings.Contains(blob, "a1b2c3d") || strings.Contains(blob, "deadbee") {
			t.Errorf("raw manifest content leaked into a finding: %s", blob)
		}
	}
}

func TestOpenRequiresPath(t *testing.T) {
	if err := argocd.New().Open(context.Background(), sdk.Config{}); err == nil {
		t.Fatal("Open with no path = nil, want error")
	}
}

func TestDescriptor(t *testing.T) {
	d := argocd.New().Descriptor()
	if d.Name != "olivares.argocd" || d.Type != sdk.TypeSource {
		t.Errorf("unexpected descriptor: %+v", d)
	}
}

// TestDirectoryInput confirms a directory of manifests is read the same as a file.
func TestDirectoryInput(t *testing.T) {
	sink := gather(t, "testdata")
	if len(sink.findings) == 0 {
		t.Fatal("expected findings from the testdata directory")
	}
}
