// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package istiotelemetry_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	istiotelemetry "github.com/olivaresai/olivares/connectors/istio-telemetry"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// capturingSink records the observations a Gather emits. It mirrors the awskms test
// sink but keeps findings, since this connector emits FindingReport only.
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
	s := istiotelemetry.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": path}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

// findBySignal returns the single finding whose Title names the signal phrase, or
// fails. It distinguishes enabled vs disabled by the presence of "DISABLED".
func requireFinding(t *testing.T, fs []model.FindingReport, subject, titleContains string) model.FindingReport {
	t.Helper()
	var hits []model.FindingReport
	for _, f := range fs {
		if f.SubjectRef == subject && strings.Contains(f.Title, titleContains) {
			hits = append(hits, f)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly 1 finding for subject=%q containing %q, got %d: %+v", subject, titleContains, len(hits), fs)
	}
	return hits[0]
}

// TestEnabledCoverage: a Telemetry that configures access logging and tracing yields
// Info coverage findings, never an edge.
func TestEnabledCoverage(t *testing.T) {
	sink := gather(t, "testdata/enabled.yaml")
	if len(sink.findings) != 2 {
		t.Fatalf("want 2 coverage findings, got %d: %+v", len(sink.findings), sink.findings)
	}
	// No edges/cost — posture only.
	for _, o := range sink.all {
		if o.ObservationType() != model.ObsFinding {
			t.Fatalf("posture connector emitted a non-finding observation: %T", o)
		}
	}
	subject := "istio-system/mesh-default"
	al := requireFinding(t, sink.findings, subject, "access logging enabled")
	if al.Kind != "mesh_telemetry_posture" || al.SubjectKind != "istio.telemetry" {
		t.Errorf("bad finding metadata: %+v", al)
	}
	if al.Severity != model.SeverityInfo {
		t.Errorf("access logging coverage severity = %q, want info", al.Severity)
	}
	if al.DetailHash == "" {
		t.Errorf("coverage finding has empty DetailHash")
	}
	tr := requireFinding(t, sink.findings, subject, "tracing enabled")
	if tr.Severity != model.SeverityInfo {
		t.Errorf("tracing coverage severity = %q, want info", tr.Severity)
	}
	if al.DetailHash == tr.DetailHash {
		t.Errorf("two distinct postures must hash to distinct detail keys")
	}
}

// TestDisabledBlindSpots: disabled access logging + disabled span reporting + a
// metrics override disabled all become Medium blind-spot findings; a non-Telemetry
// document in the same stream is ignored; the scope names the selector workloads.
func TestDisabledBlindSpots(t *testing.T) {
	sink := gather(t, "testdata/disabled.yaml")
	subject := "payments/payments-quiet"

	al := requireFinding(t, sink.findings, subject, "access logging DISABLED")
	if al.Severity != model.SeverityMedium {
		t.Errorf("disabled access logging severity = %q, want medium", al.Severity)
	}
	if !strings.Contains(al.Title, "blind spot") {
		t.Errorf("disabled finding should name the blind spot: %q", al.Title)
	}
	if !strings.Contains(al.Title, "app=payments") || !strings.Contains(al.Title, "version=v2") {
		t.Errorf("disabled finding should name the selector scope: %q", al.Title)
	}

	tr := requireFinding(t, sink.findings, subject, "tracing DISABLED")
	if tr.Severity != model.SeverityMedium {
		t.Errorf("disableSpanReporting severity = %q, want medium", tr.Severity)
	}

	mt := requireFinding(t, sink.findings, subject, "metrics DISABLED")
	if mt.Severity != model.SeverityMedium {
		t.Errorf("metrics override disabled severity = %q, want medium", mt.Severity)
	}

	// The VirtualService in the same multi-doc file must NOT produce a finding.
	for _, f := range sink.findings {
		if strings.Contains(f.SubjectRef, "payments-route") {
			t.Fatalf("non-Telemetry kind leaked a finding: %+v", f)
		}
	}
	// Every finding for this manifest is a blind spot => all Medium.
	for _, f := range sink.findings {
		if f.Severity != model.SeverityMedium {
			t.Errorf("unexpected non-medium finding in disabled manifest: %+v", f)
		}
	}
	if len(sink.findings) != 3 {
		t.Fatalf("want 3 blind-spot findings, got %d: %+v", len(sink.findings), sink.findings)
	}
}

// TestLegacyAPIAndWrapperBool: the legacy v1alpha1 apiVersion is accepted and the
// BoolValue WRAPPER form (disabled: {value: true}) is detected as a disable.
func TestLegacyAPIAndWrapperBool(t *testing.T) {
	sink := gather(t, "testdata/legacy-wrapper.yaml")
	f := requireFinding(t, sink.findings, "legacy/legacy-quiet", "access logging DISABLED")
	if f.Severity != model.SeverityMedium {
		t.Errorf("wrapper-form disable severity = %q, want medium", f.Severity)
	}
}

// TestNoSecretLeaks is the minimal-data negative test (docs/SECURITY-HARDENING.md): a secret embedded
// in a selector label value must NEVER appear in any emitted finding field.
func TestNoSecretLeaks(t *testing.T) {
	sink := gather(t, "testdata/leak.yaml")
	if len(sink.findings) == 0 {
		t.Fatal("expected at least one finding")
	}
	blob, err := json.Marshal(sink.findings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("a secret leaked into the emitted findings: %s", blob)
	}
	// The scrubbed scope should still describe the label structure (no raw value).
	f := sink.findings[0]
	if !strings.Contains(f.Title, "token=") {
		t.Errorf("scope should keep the label key while scrubbing the value: %q", f.Title)
	}
}

// TestDirectoryAndDeterminism: pointing path at a directory parses every manifest,
// and re-gathering produces identical detail hashes (stable, replayable posture).
func TestDirectoryAndDeterminism(t *testing.T) {
	a := gather(t, "testdata")
	b := gather(t, "testdata")
	if len(a.findings) == 0 || len(a.findings) != len(b.findings) {
		t.Fatalf("directory gather mismatch: %d vs %d", len(a.findings), len(b.findings))
	}
	for i := range a.findings {
		if a.findings[i].DetailHash != b.findings[i].DetailHash || a.findings[i].Title != b.findings[i].Title {
			t.Fatalf("non-deterministic finding at %d: %+v vs %+v", i, a.findings[i], b.findings[i])
		}
	}
}

// TestOpenRequiresPath: a missing path is a configuration error, not a silent no-op.
func TestOpenRequiresPath(t *testing.T) {
	s := istiotelemetry.New()
	if err := s.Open(context.Background(), sdk.Config{}); err == nil {
		t.Fatal("Open without path should error")
	}
}

// TestContextCancel: a canceled context stops Gather promptly with the ctx error.
func TestContextCancel(t *testing.T) {
	s := istiotelemetry.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": "testdata"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Gather(ctx, &capturingSink{})
	if err == nil {
		t.Fatal("Gather with a canceled context should return the ctx error")
	}
}
