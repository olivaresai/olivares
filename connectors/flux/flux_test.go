// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package flux_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/flux"
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
	s := flux.New()
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
	sink := gather(t, filepath.Join("testdata", "flux.yaml"))

	// Reconciled GitRepository: Info Ready, no drift finding.
	gr := find(t, sink.findings, "flux-system/apps-repo", "Ready=True")
	if gr.Severity != model.SeverityInfo {
		t.Errorf("Ready=True severity = %s, want info", gr.Severity)
	}
	if gr.SubjectKind != "flux.gitrepository" {
		t.Errorf("SubjectKind = %q, want flux.gitrepository", gr.SubjectKind)
	}

	// Failing Kustomization: High Ready=False, reason token in the Title.
	ks := find(t, sink.findings, "flux-system/billing-stack", "Ready=False")
	if ks.Severity != model.SeverityHigh {
		t.Errorf("Ready=False severity = %s, want high", ks.Severity)
	}
	if ks.SubjectKind != "flux.kustomization" {
		t.Errorf("SubjectKind = %q, want flux.kustomization", ks.SubjectKind)
	}
	if !strings.Contains(ks.Title, "ArtifactFailed") {
		t.Errorf("failing Title missing reason token: %q", ks.Title)
	}

	// Drifted-but-reconciled HelmRelease: Info Ready + Medium drift.
	hr := find(t, sink.findings, "flux-system/redis-cache", "Ready=True")
	if hr.Severity != model.SeverityInfo {
		t.Errorf("HelmRelease Ready severity = %s, want info", hr.Severity)
	}
	if hr.SubjectKind != "flux.helmrelease" {
		t.Errorf("SubjectKind = %q, want flux.helmrelease", hr.SubjectKind)
	}
	dr := find(t, sink.findings, "flux-system/redis-cache", "drifted")
	if dr.Severity != model.SeverityMedium {
		t.Errorf("drift severity = %s, want medium", dr.Severity)
	}

	// The ConfigMap must be ignored: no finding references it.
	for _, f := range sink.findings {
		if strings.Contains(f.SubjectRef, "flux-cm") {
			t.Errorf("ConfigMap leaked into a finding: %+v", f)
		}
	}

	// apps-repo: 1 Ready, billing-stack: 1 Ready, redis-cache: 1 Ready + 1 drift => 4.
	if len(sink.findings) != 4 || len(sink.all) != 4 {
		t.Errorf("finding count = %d (all=%d), want 4", len(sink.findings), len(sink.all))
	}
}

// TestUnknownReadyIsMediumNeverHealthy asserts an object with no Ready condition is
// classified honestly as Unknown (Medium), never silently treated as reconciled.
func TestUnknownReadyIsMediumNeverHealthy(t *testing.T) {
	dir := t.TempDir()
	manifest := "" +
		"apiVersion: kustomize.toolkit.fluxcd.io/v1\n" +
		"kind: Kustomization\n" +
		"metadata:\n" +
		"  name: fresh\n" +
		"  namespace: flux-system\n"
	if err := writeFile(t, dir, "fresh.yaml", manifest); err != nil {
		t.Fatal(err)
	}
	sink := gather(t, dir)
	f := find(t, sink.findings, "flux-system/fresh", "Ready=Unknown")
	if f.Severity != model.SeverityMedium {
		t.Errorf("Unknown Ready severity = %s, want medium", f.Severity)
	}
	if len(sink.findings) != 1 {
		t.Errorf("finding count = %d, want 1 (no drift on an Unknown object)", len(sink.findings))
	}
}

// TestObservedGenerationDrift asserts a reconciled object whose observedGeneration
// lags metadata.generation is reported as drifted even with matching revisions.
func TestObservedGenerationDrift(t *testing.T) {
	dir := t.TempDir()
	manifest := "" +
		"apiVersion: kustomize.toolkit.fluxcd.io/v1\n" +
		"kind: Kustomization\n" +
		"metadata:\n" +
		"  name: lagging\n" +
		"  namespace: flux-system\n" +
		"  generation: 9\n" +
		"status:\n" +
		"  observedGeneration: 8\n" +
		"  conditions:\n" +
		"    - type: Ready\n" +
		"      status: \"True\"\n" +
		"      reason: ReconciliationSucceeded\n"
	if err := writeFile(t, dir, "lagging.yaml", manifest); err != nil {
		t.Fatal(err)
	}
	sink := gather(t, dir)
	find(t, sink.findings, "flux-system/lagging", "Ready=True")
	dr := find(t, sink.findings, "flux-system/lagging", "drifted")
	if dr.Severity != model.SeverityMedium {
		t.Errorf("generation-lag drift severity = %s, want medium", dr.Severity)
	}
}

// TestNoRawLeak asserts no manifest content (a revision SHA, a chart version, an
// artifact URL, a condition message) leaks into any emitted field — minimal data
// (docs/SECURITY-HARDENING.md). DetailHash is a hash, never the value.
func TestNoRawLeak(t *testing.T) {
	sink := gather(t, filepath.Join("testdata", "flux.yaml"))
	// Tokens that appear in the fixture's status (revisions, URLs, message bodies).
	leaks := []string{
		"a1b2c3d", "deadbeef", "19.5.0", "19.6.4", "cafef00d",
		"source-controller", "git.example.com", "flux.example.com",
		"404 not found", "Helm install succeeded",
	}
	for _, f := range sink.findings {
		blob := f.Kind + "|" + string(f.Severity) + "|" + f.SubjectKind + "|" + f.SubjectRef + "|" + f.Title + "|" + f.DetailHash
		for _, l := range leaks {
			if strings.Contains(blob, l) {
				t.Errorf("raw manifest content %q leaked into a finding: %s", l, blob)
			}
		}
	}
}

func TestOpenRequiresPath(t *testing.T) {
	if err := flux.New().Open(context.Background(), sdk.Config{}); err == nil {
		t.Fatal("Open with no path = nil, want error")
	}
}

func TestDescriptor(t *testing.T) {
	d := flux.New().Descriptor()
	if d.Name != "olivares.flux" || d.Type != sdk.TypeSource {
		t.Errorf("unexpected descriptor: %+v", d)
	}
	// The descriptor must not carry any sensitive example value.
	if strings.Contains(d.Description, "a1b2c3d") {
		t.Errorf("descriptor leaks a revision token: %q", d.Description)
	}
}

// TestDirectoryInput confirms a directory of manifests is read the same as a file.
func TestDirectoryInput(t *testing.T) {
	sink := gather(t, "testdata")
	if len(sink.findings) == 0 {
		t.Fatal("expected findings from the testdata directory")
	}
}

// writeFile is a tiny test helper to drop a manifest into a temp dir.
func writeFile(t *testing.T, dir, name, content string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
}
