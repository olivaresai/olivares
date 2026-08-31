// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package egressproxy_test

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	egressproxy "github.com/olivaresai/olivares/connectors/egress-proxy"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// The recognizable secrets the no-leak test asserts NEVER survive into an emitted
// observation. The AKIA key is one the shared redactor recognizes; the payload string
// stands in for a body the proxy logged in a free-text field.
const (
	fixtureAWSKey  = "AKIAIOSFODNN7EXAMPLE"
	fixturePayload = "SUPERSECRETPAYLOAD_MUST_NOT_LEAK"
)

func testTime() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }

// capturingSink records every observation, mirroring the awskms/envoy fakes.
type capturingSink struct{ obs []model.Observation }

func (c *capturingSink) Emit(_ context.Context, o model.Observation) error {
	c.obs = append(c.obs, o)
	return nil
}

func (c *capturingSink) edges() []model.EdgeObservation {
	var e []model.EdgeObservation
	for _, o := range c.obs {
		if ed, ok := o.(model.EdgeObservation); ok {
			e = append(e, ed)
		}
	}
	return e
}

func (c *capturingSink) findings() []model.FindingReport {
	var f []model.FindingReport
	for _, o := range c.obs {
		if fr, ok := o.(model.FindingReport); ok {
			f = append(f, fr)
		}
	}
	return f
}

// gather opens the connector on a fixture path and runs Gather once.
func gather(t *testing.T, path string) *capturingSink {
	t.Helper()
	s := egressproxy.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": path}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return sink
}

func TestGatherEmitsEdgesAndFindings(t *testing.T) {
	sink := gather(t, "testdata/verdicts.jsonl")

	// Fixture: 2 allows (api.anthropic.com POST, files.internal GET) -> 2 edges;
	// 2 denies (evil.example.com, raw.githubusercontent.com) -> 2 findings.
	// Skipped: blank line, garbage line, "quarantine" (unknown decision), and the
	// allow with no destination host.
	if got := len(sink.edges()); got != 2 {
		t.Fatalf("got %d edges, want 2: %+v", got, sink.edges())
	}
	if got := len(sink.findings()); got != 2 {
		t.Fatalf("got %d findings, want 2: %+v", got, sink.findings())
	}

	// Every edge carries the package-local source, the identity origin and approximate
	// confidence (a log line is not a cryptographic identity).
	edgeByFQDN := map[string]model.EdgeObservation{}
	for _, e := range sink.edges() {
		if e.Source != egressproxy.SignalEgressProxy {
			t.Errorf("edge source = %q, want egress_proxy", e.Source)
		}
		if e.OriginKind != "identity" {
			t.Errorf("edge OriginKind = %q, want identity", e.OriginKind)
		}
		if e.Confidence != model.ConfidenceApproximate {
			t.Errorf("edge confidence = %q, want approximate", e.Confidence)
		}
		if e.ToolRef != "egress_proxy.verdict" {
			t.Errorf("edge ToolRef = %q", e.ToolRef)
		}
		edgeByFQDN[e.ResourceRef] = e
	}

	// The POST allow to api.anthropic.com is an http.api edge, ModeReadWrite (POST),
	// origin from the "identity" alias.
	post, ok := edgeByFQDN["api.anthropic.com"]
	if !ok {
		t.Fatalf("missing api.anthropic.com edge: %v", edgeByFQDN)
	}
	if post.ResourceKind != "http.api" || post.Mode != model.ModeReadWrite {
		t.Errorf("anthropic edge = kind %q mode %q, want http.api/readwrite", post.ResourceKind, post.Mode)
	}
	if post.OriginRef != "ns/payments-agent" {
		t.Errorf("anthropic edge origin = %q, want ns/payments-agent", post.OriginRef)
	}
	if !post.ObservedAt.Equal(time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("anthropic edge ObservedAt = %v", post.ObservedAt)
	}

	// The GET allow (alias: principal/destination/permitted) -> http.api ModeRead.
	get, ok := edgeByFQDN["files.internal"]
	if !ok {
		t.Fatalf("missing files.internal edge: %v", edgeByFQDN)
	}
	if get.Mode != model.ModeRead || get.OriginRef != "ns/reader" {
		t.Errorf("files.internal edge = mode %q origin %q, want read/ns/reader", get.Mode, get.OriginRef)
	}

	// The denies map onto findings keyed by FQDN, Kind "egress_denied".
	findingByFQDN := map[string]model.FindingReport{}
	for _, f := range sink.findings() {
		if f.Kind != "egress_denied" || f.SubjectKind != "net.egress" {
			t.Errorf("finding kind/subject = %q/%q", f.Kind, f.SubjectKind)
		}
		if f.DetailHash == "" {
			t.Errorf("finding has no DetailHash: %+v", f)
		}
		findingByFQDN[f.SubjectRef] = f
	}
	for _, want := range []string{"evil.example.com", "raw.githubusercontent.com"} {
		if _, ok := findingByFQDN[want]; !ok {
			t.Errorf("missing finding for %q in %v", want, findingByFQDN)
		}
	}
}

// TestFieldAliases proves the tolerant ingest accepts divergent field spellings: a
// log written with verdict/source/dest/dest_port still produces the right edge.
func TestFieldAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliased.log")
	line := `{"time":"2026-06-06T11:00:00Z","source":"svc/web","dest":"db.svc.cluster.local","dest_port":"5432","verdict":"PASS"}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := gather(t, path)
	if len(sink.edges()) != 1 {
		t.Fatalf("alias log produced %d edges, want 1", len(sink.edges()))
	}
	e := sink.edges()[0]
	// No method => net.endpoint with the tcp:// scheme + port (from the string "5432").
	if e.ResourceKind != "net.endpoint" || e.ResourceRef != "tcp://db.svc.cluster.local:5432" {
		t.Errorf("alias edge = %q/%q, want net.endpoint/tcp://db.svc.cluster.local:5432", e.ResourceKind, e.ResourceRef)
	}
	if e.OriginRef != "svc/web" {
		t.Errorf("alias edge origin = %q, want svc/web", e.OriginRef)
	}
	if e.Mode != model.ModeReadWrite { // no method => bidirectional socket
		t.Errorf("alias edge mode = %q, want readwrite", e.Mode)
	}
}

// TestNoSecretLeaks is the minimal-data negative test (docs/SECURITY-HARDENING.md): the fixture
// embeds an AWS key and a payload string in deny "reason"/"message" fields; neither
// may appear in json.Marshal of ANY emitted observation.
func TestNoSecretLeaks(t *testing.T) {
	sink := gather(t, "testdata/verdicts.jsonl")
	if len(sink.obs) == 0 {
		t.Fatal("expected observations")
	}
	blob, err := json.Marshal(sink.obs)
	if err != nil {
		t.Fatal(err)
	}
	s := string(blob)
	if strings.Contains(s, fixtureAWSKey) {
		t.Fatalf("AWS key leaked into emitted observations: %s", s)
	}
	if strings.Contains(s, fixturePayload) {
		t.Fatalf("payload string leaked into emitted observations: %s", s)
	}
}

// TestUnknownDecisionAndNoHostSkipped asserts a verdict that is neither allow nor
// deny, and an allow with no destination, are skipped (never guessed into an edge).
func TestUnknownDecisionAndNoHostSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edge-cases.log")
	lines := strings.Join([]string{
		`{"ts":"2026-06-06T12:00:00Z","identity":"a","host":"x.example","decision":"quarantine"}`, // unknown verdict
		`{"ts":"2026-06-06T12:01:00Z","identity":"b","decision":"allow"}`,                         // no host
		`{"ts":"2026-06-06T12:02:00Z","identity":"c","host":"ok.example","decision":"allow"}`,     // valid
	}, "\n")
	if err := os.WriteFile(path, []byte(lines+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := gather(t, path)
	if len(sink.obs) != 1 {
		t.Fatalf("got %d observations, want 1 (only the valid allow): %+v", len(sink.obs), sink.obs)
	}
}

// TestMissingTimestampUsesClock asserts a record with no/unparseable timestamp falls
// back to the connector clock, never a fabricated zero time.
func TestMissingTimestampUsesClock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-ts.log")
	if err := os.WriteFile(path, []byte(`{"identity":"a","host":"x.example","decision":"allow"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := egressproxy.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": path}}); err != nil {
		t.Fatal(err)
	}
	egressproxy.SetClockForTest(s, testTime)
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.edges()) != 1 {
		t.Fatalf("want 1 edge, got %d", len(sink.edges()))
	}
	if !sink.edges()[0].ObservedAt.Equal(testTime()) {
		t.Errorf("ObservedAt = %v, want clock fallback %v", sink.edges()[0].ObservedAt, testTime())
	}
}

func TestOpenRequiresPath(t *testing.T) {
	s := egressproxy.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestGarbageLinesTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noisy.log")
	content := "\n   \nthis is not json\n{not valid}\n[]\n{\"identity\":\"a\",\"host\":\"good.example\",\"decision\":\"allow\"}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := gather(t, path)
	if len(sink.edges()) != 1 {
		t.Fatalf("garbage-laden log produced %d edges, want 1", len(sink.edges()))
	}
}

// TestNoNetworkListener is the structural guarantee that this connector is a PURE
// FILE PARSER ("no construir proxy"): it scans the package's own .go
// source and asserts NO listener call (net.Listen*, http.ListenAndServe, grpc.Serve)
// AND no outbound dial (net.Dial*) appears. The connector observes a verdict log a
// proxy already wrote; it never opens a listener, builds a proxy, or connects out.
func TestNoNetworkListener(t *testing.T) {
	forbidden := map[string]bool{
		"Listen":            true, // net.Listen
		"ListenPacket":      true,
		"ListenTCP":         true,
		"ListenUDP":         true,
		"ListenUnix":        true,
		"ListenAndServe":    true, // http.ListenAndServe
		"ListenAndServeTLS": true,
		"Serve":             true, // grpc.Server.Serve / http.Serve
		"Dial":              true, // net.Dial — no outbound connection (not a proxy/MITM)
		"DialContext":       true,
		"DialTCP":           true,
		"DialUDP":           true,
		"DialUnix":          true,
		"DialIP":            true,
	}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if forbidden[sel.Sel.Name] {
				t.Errorf("%s references a network-listener call %q — this connector must be a pure file parser (no listener, no proxy)", name, sel.Sel.Name)
			}
			return true
		})
	}
}
