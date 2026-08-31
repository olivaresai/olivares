// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package meshobs_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/meshobs"
	"github.com/olivaresai/olivares/connectors/internal/tracecontext"
	"github.com/olivaresai/olivares/sdk/model"
)

const testSource model.SignalSource = "envoy_als"

func ts() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }

// capturingSink records every observation a record emits (edges AND findings).
type capturingSink struct{ obs []model.Observation }

func (c *capturingSink) Emit(_ context.Context, o model.Observation) error {
	c.obs = append(c.obs, o)
	return nil
}

// recCorrelator records the last Correlate call.
type recCorrelator struct {
	n   int
	key string
	tc  tracecontext.TraceContext
}

func (r *recCorrelator) Correlate(key string, t tracecontext.TraceContext) {
	r.n++
	r.key = key
	r.tc = t
}

func TestMethodToMode(t *testing.T) {
	cases := map[string]model.AccessMode{
		"GET":     model.ModeRead,
		"head":    model.ModeRead,
		"OPTIONS": model.ModeRead,
		"TRACE":   model.ModeRead,
		"POST":    model.ModeReadWrite,
		"PUT":     model.ModeReadWrite,
		"PATCH":   model.ModeReadWrite,
		"DELETE":  model.ModeWrite,
		"CONNECT": model.ModeReadWrite,
		"":        model.ModeReadWrite, // non-HTTP L4 flow
		"FROBNIC": model.ModeUnknown,   // never guessed
	}
	for method, want := range cases {
		if got := meshobs.MethodToMode(method); got != want {
			t.Errorf("MethodToMode(%q) = %q, want %q", method, got, want)
		}
	}
}

func TestEdgeHTTP(t *testing.T) {
	r := meshobs.Record{
		OriginRef:      "spiffe://c/ns/default/sa/payments",
		OriginVerified: true,
		FQDN:           "API.Anthropic.com",
		Port:           443,
		Method:         "POST",
		Source:         testSource,
		Tool:           "envoy.als",
		ObservedAt:     ts(),
	}
	e := r.Edge()
	if e.OriginKind != "identity" {
		t.Errorf("OriginKind = %q, want identity", e.OriginKind)
	}
	if e.ResourceKind != meshobs.ResourceKindHTTPAPI {
		t.Errorf("ResourceKind = %q, want %q", e.ResourceKind, meshobs.ResourceKindHTTPAPI)
	}
	if e.ResourceRef != "api.anthropic.com" {
		t.Errorf("ResourceRef = %q, want lowercased host (no port, no scheme)", e.ResourceRef)
	}
	if e.Mode != model.ModeReadWrite {
		t.Errorf("Mode = %q, want readwrite (POST)", e.Mode)
	}
	if e.Confidence != model.ConfidenceAttributed {
		t.Errorf("Confidence = %q, want attributed (mTLS-verified)", e.Confidence)
	}
	if e.Source != testSource || e.ToolRef != "envoy.als" || !e.ObservedAt.Equal(ts()) {
		t.Errorf("provenance/time fields wrong: %+v", e)
	}
}

func TestEdgeNonHTTPDegradesToNetEndpoint(t *testing.T) {
	r := meshobs.Record{FQDN: "db.internal", Port: 5432, Source: testSource, ObservedAt: ts()}
	e := r.Edge()
	if e.ResourceKind != meshobs.ResourceKindNetEndpoint {
		t.Fatalf("ResourceKind = %q, want net.endpoint for non-HTTP", e.ResourceKind)
	}
	if e.ResourceRef != "tcp://db.internal:5432" {
		t.Fatalf("ResourceRef = %q, want eBPF-compatible tcp://host:port", e.ResourceRef)
	}
	if e.Mode != model.ModeReadWrite {
		t.Fatalf("Mode = %q, want readwrite for a bidirectional socket", e.Mode)
	}
}

func TestEdgeUnverifiedIsApproximate(t *testing.T) {
	r := meshobs.Record{FQDN: "x.example", Method: "GET", Source: testSource, ObservedAt: ts()}
	if e := r.Edge(); e.Confidence != model.ConfidenceApproximate {
		t.Fatalf("Confidence = %q, want approximate for unverified identity", e.Confidence)
	}
	if r.Edge().OriginRef != "unknown" {
		t.Fatalf("empty OriginRef should become 'unknown'")
	}
}

func TestDenyFindingDefaultsAndHashes(t *testing.T) {
	r := meshobs.Record{
		OriginRef:  "payments.default",
		FQDN:       "evil.example",
		Verdict:    meshobs.VerdictDenied,
		DenyReason: "fqdn not in egress allowlist",
		Source:     testSource,
		ObservedAt: ts(),
	}
	f := r.DenyFinding()
	if f.Kind != meshobs.DefaultDenyKind {
		t.Errorf("Kind = %q", f.Kind)
	}
	if f.Severity != model.SeverityMedium {
		t.Errorf("Severity = %q, want default medium", f.Severity)
	}
	if f.SubjectKind != "net.egress" || f.SubjectRef != "evil.example" {
		t.Errorf("subject wrong: %+v", f)
	}
	if !strings.Contains(f.Title, "payments.default") || !strings.Contains(f.Title, "evil.example") {
		t.Errorf("Title should name origin and fqdn: %q", f.Title)
	}
	if len(f.DetailHash) != 64 { // hex SHA-256
		t.Errorf("DetailHash should be a 64-char hex SHA-256, got %q", f.DetailHash)
	}
}

func TestDenyFindingCustomSeverityAndTaxonomy(t *testing.T) {
	r := meshobs.Record{
		FQDN: "x", Verdict: meshobs.VerdictDenied, DenySeverity: model.SeverityHigh,
		OWASPLLM: []string{"LLM02:2025"}, ATLAS: []string{"AML.T0024"}, ObservedAt: ts(),
	}
	f := r.DenyFinding()
	if f.Severity != model.SeverityHigh {
		t.Errorf("Severity = %q, want high", f.Severity)
	}
	if len(f.OWASPLLM) != 1 || f.OWASPLLM[0] != "LLM02:2025" || len(f.ATLAS) != 1 {
		t.Errorf("taxonomy not carried through: %+v", f)
	}
}

func TestDenyReasonSecretNeverLeaks(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	r := meshobs.Record{
		FQDN: "x.example", Verdict: meshobs.VerdictDenied,
		DenyReason: "blocked request carrying " + secret, ObservedAt: ts(),
	}
	b, err := json.Marshal(r.DenyFinding())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("a secret in DenyReason leaked into the emitted finding: %s", b)
	}

	// Prove scrub-BEFORE-hash, not merely that a hash can't contain plaintext: two
	// records whose only difference is a DIFFERENT secret OF THE SAME SHAPE must hash
	// to the SAME DetailHash, because both scrub to the same labeled placeholder. If
	// the code hashed the raw (unscrubbed) reason, the two distinct keys would produce
	// different hashes and this assertion would fail.
	r2 := meshobs.Record{
		FQDN: "x.example", Verdict: meshobs.VerdictDenied,
		DenyReason: "blocked request carrying " + "AKIAXXXXXXXXXXXXXXXX", ObservedAt: ts(),
	}
	if r.DenyFinding().DetailHash != r2.DenyFinding().DetailHash {
		t.Fatal("two different AWS keys produced different DetailHashes — the deny reason is hashed BEFORE redaction (scrub-before-hash invariant broken)")
	}
}

func TestObservationsAllowedVsDenied(t *testing.T) {
	allow := meshobs.Record{FQDN: "a.example", Method: "GET", Source: testSource, ObservedAt: ts()}
	obs := allow.Observations()
	if len(obs) != 1 {
		t.Fatalf("allowed: want 1 observation, got %d", len(obs))
	}
	if _, ok := obs[0].(model.EdgeObservation); !ok {
		t.Fatalf("allowed should yield an EdgeObservation, got %T", obs[0])
	}

	deny := meshobs.Record{FQDN: "b.example", Verdict: meshobs.VerdictDenied, Source: testSource, ObservedAt: ts()}
	obs = deny.Observations()
	if len(obs) != 1 {
		t.Fatalf("denied: want 1 observation, got %d", len(obs))
	}
	if _, ok := obs[0].(model.FindingReport); !ok {
		t.Fatalf("denied should yield a FindingReport, got %T", obs[0])
	}
}

func TestObservationsEmptyFQDN(t *testing.T) {
	if obs := (meshobs.Record{Method: "GET"}).Observations(); obs != nil {
		t.Fatalf("a record with no FQDN must yield no observations, got %v", obs)
	}
}

func TestTraceCorrelatorFired(t *testing.T) {
	rc := &recCorrelator{}
	r := meshobs.Record{
		OriginRef: "svc", FQDN: "dst.example", Method: "GET", Source: testSource,
		Trace:      tracecontext.TraceContext{TraceParent: sampleTP},
		Correlator: rc,
		ObservedAt: ts(),
	}
	_ = r.Observations()
	if rc.n != 1 {
		t.Fatalf("correlator fired %d times, want 1", rc.n)
	}
	if rc.key != "svc->dst.example" {
		t.Fatalf("correlation key = %q", rc.key)
	}
	if rc.tc.TraceParent != sampleTP {
		t.Fatalf("trace context not handed through: %+v", rc.tc)
	}

	// No trace context present -> correlator never called.
	rc2 := &recCorrelator{}
	r2 := meshobs.Record{FQDN: "x", Method: "GET", Correlator: rc2, ObservedAt: ts()}
	_ = r2.Observations()
	if rc2.n != 0 {
		t.Fatalf("correlator must not fire without trace context (n=%d)", rc2.n)
	}
}

const sampleTP = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func TestEmitToSink(t *testing.T) {
	sink := &capturingSink{}
	rec := meshobs.Record{FQDN: "a.example", Method: "GET", Source: testSource, ObservedAt: ts()}
	if err := rec.Emit(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.obs) != 1 {
		t.Fatalf("want 1 emitted observation, got %d", len(sink.obs))
	}
}
