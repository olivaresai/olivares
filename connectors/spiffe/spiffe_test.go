// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// fixedClock returns a deterministic UTC time for CapturedAt assertions.
func fixedClock() time.Time { return time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC) }

// openFile opens a Source over the testdata fixture file, with optional extra
// settings (e.g. a trust_domain filter).
func openFile(t *testing.T, extra map[string]string) *Source {
	t.Helper()
	s := New()
	s.now = fixedClock
	settings := map[string]string{"entries_file": filepath.Join("testdata", "entries.json")}
	for k, v := range extra {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// TestSnapshotFromFile is the core mapping test against the recorded fixture: it
// asserts NHI/workload classification, full SPIFFE id assembly from BOTH the
// structured and the flat export forms, selector flattening, parent grouping and
// the parentage memberships.
func TestSnapshotFromFile(t *testing.T) {
	s := openFile(t, nil)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if g.Source != identitysource.SourceSPIFFE {
		t.Errorf("source = %q, want %q", g.Source, identitysource.SourceSPIFFE)
	}
	if !g.CapturedAt.Equal(fixedClock()) {
		t.Errorf("CapturedAt = %v, want %v", g.CapturedAt, fixedClock())
	}
	if len(g.Identities) != 4 {
		t.Fatalf("want 4 identities, got %d", len(g.Identities))
	}

	// Structured spiffe_id form -> full id assembled, NHI/workload, path label.
	web, ok := g.FindIdentity("spiffe://corp.example/ns/prod/sa/web")
	if !ok {
		t.Fatal("missing web workload (structured spiffe_id form)")
	}
	if web.Type != identitysource.PrincipalNHI {
		t.Errorf("web type = %q, want nhi", web.Type)
	}
	if web.Kind != "workload" {
		t.Errorf("web kind = %q, want workload", web.Kind)
	}
	if web.DisplayName != "/ns/prod/sa/web" {
		t.Errorf("web display = %q, want path", web.DisplayName)
	}
	if web.Attributes["trust_domain"] != "corp.example" {
		t.Errorf("web trust_domain = %q", web.Attributes["trust_domain"])
	}
	if got := web.Attributes["selectors"]; got != "k8s:ns:prod,k8s:sa:web" {
		t.Errorf("web selectors = %q, want sorted flattened", got)
	}
	if web.Attributes["parent_id"] != "spiffe://corp.example/spire/agent/k8s/node1" {
		t.Errorf("web parent_id = %q", web.Attributes["parent_id"])
	}
	if _, present := web.Attributes["admin"]; present {
		t.Errorf("web admin attribute should be absent for admin:false, got %q", web.Attributes["admin"])
	}
	if web.Attributes["x509_svid_ttl"] != "3600" {
		t.Errorf("web x509_svid_ttl = %q, want 3600", web.Attributes["x509_svid_ttl"])
	}

	// Flat spiffe_id form -> same full-id assembly, admin flag surfaced.
	worker, ok := g.FindIdentity("spiffe://corp.example/ns/prod/sa/worker")
	if !ok {
		t.Fatal("missing worker workload (flat spiffe_id form)")
	}
	if worker.Type != identitysource.PrincipalNHI || worker.Kind != "workload" {
		t.Errorf("worker = %q/%q, want nhi/workload", worker.Type, worker.Kind)
	}
	if worker.Attributes["admin"] != "true" {
		t.Errorf("worker admin = %q, want true", worker.Attributes["admin"])
	}

	// Parent grouping: node1 (two workloads), node2 (one), edge1 (one) => 3 agents.
	if len(g.Collections) != 3 {
		t.Fatalf("want 3 parent collections, got %d", len(g.Collections))
	}
	node1 := findCollection(g, "spiffe://corp.example/spire/agent/k8s/node1")
	if node1 == nil {
		t.Fatal("missing node1 parent collection")
	}
	if node1.Kind != identitysource.KindGroup {
		t.Errorf("node1 kind = %q, want group", node1.Kind)
	}
	if node1.Attributes["agent"] != "spire_node" {
		t.Errorf("node1 agent label = %q, want spire_node", node1.Attributes["agent"])
	}
	if node1.DisplayName != "/spire/agent/k8s/node1" {
		t.Errorf("node1 display = %q", node1.DisplayName)
	}

	// Memberships: every workload is parented; all are MemberIdentity edges.
	if len(g.Memberships) != 4 {
		t.Fatalf("want 4 memberships, got %d", len(g.Memberships))
	}
	var node1Members int
	for _, m := range g.Memberships {
		if m.MemberKind != identitysource.MemberIdentity {
			t.Errorf("membership %+v should be MemberIdentity", m)
		}
		if m.Source != identitysource.SourceSPIFFE {
			t.Errorf("membership source = %q", m.Source)
		}
		if m.CollectionRef == "spiffe://corp.example/spire/agent/k8s/node1" {
			node1Members++
		}
	}
	if node1Members != 2 {
		t.Errorf("want 2 workloads under node1, got %d", node1Members)
	}
}

// TestTrustDomainFilter asserts the optional filter includes only matching entries
// and that the filter accepts a bare trust-domain name.
func TestTrustDomainFilter(t *testing.T) {
	s := openFile(t, map[string]string{"trust_domain": "corp.example"})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(g.Identities) != 3 {
		t.Fatalf("want 3 corp.example identities, got %d", len(g.Identities))
	}
	for _, id := range g.Identities {
		if !strings.HasPrefix(id.Ref, "spiffe://corp.example/") {
			t.Errorf("filtered identity leaked: %q", id.Ref)
		}
	}
	// other.example's payments workload and its edge1 parent must be gone.
	if _, ok := g.FindIdentity("spiffe://other.example/svc/payments"); ok {
		t.Error("other.example workload should be filtered out")
	}
	if findCollection(g, "spiffe://other.example/spire/agent/vm/edge1") != nil {
		t.Error("other.example parent should be filtered out")
	}
	if len(g.Collections) != 2 {
		t.Errorf("want 2 corp.example parents after filter, got %d", len(g.Collections))
	}
}

// TestTrustDomainFilterWithScheme asserts the filter normalizes a "spiffe://"
// prefixed value to the bare trust-domain name.
func TestTrustDomainFilterWithScheme(t *testing.T) {
	s := openFile(t, map[string]string{"trust_domain": "spiffe://other.example"})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(g.Identities) != 1 {
		t.Fatalf("want 1 other.example identity, got %d", len(g.Identities))
	}
	if g.Identities[0].Ref != "spiffe://other.example/svc/payments" {
		t.Errorf("unexpected identity %q", g.Identities[0].Ref)
	}
}

// stubDoer serves the fixture bytes for any request, recording the requested URL
// so the test asserts the connector does a plain read-only GET with no credential.
type stubDoer struct {
	body       []byte
	lastReq    *http.Request
	gotAuthHdr bool
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	d.lastReq = req
	if req.Header.Get("Authorization") != "" || req.Header.Get("X-Vault-Token") != "" {
		d.gotAuthHdr = true
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(d.body)),
		Request:    req,
	}, nil
}

// TestSnapshotFromURL exercises the HTTP export path end-to-end against a real
// httptest server, asserting it produces the same roster as the file path and
// that the GET is read-only with no credential header.
func TestSnapshotFromURL(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "entries.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "" {
			t.Errorf("connector sent a credential header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"entries_url": srv.URL + "/entries"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET (read-only)", gotMethod)
	}
	// httpx treats the configured URL as the base and follows an empty path
	// verbatim, which canonicalizes to a trailing slash; the operator's endpoint
	// must serve the export at that path. We assert the resource, slash-tolerant.
	if strings.TrimSuffix(gotPath, "/") != "/entries" {
		t.Errorf("path = %q, want /entries", gotPath)
	}
	if len(g.Identities) != 4 || len(g.Collections) != 3 || len(g.Memberships) != 4 {
		t.Errorf("URL snapshot shape: %d ids / %d cols / %d members", len(g.Identities), len(g.Collections), len(g.Memberships))
	}
	if _, ok := g.FindIdentity("spiffe://corp.example/ns/prod/sa/web"); !ok {
		t.Error("URL snapshot missing web workload")
	}
}

// TestSnapshotURLErrorStatus asserts a non-2xx export endpoint surfaces as an
// error (the error path) and never leaks a credential (there is none to leak).
func TestSnapshotURLErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"insufficient scope"}`))
	}))
	defer srv.Close()

	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"entries_url": srv.URL}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err := s.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected an error from a 403 export endpoint")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should carry the status: %v", err)
	}
}

// TestOfflineNoSource asserts the offline contract: neither file nor URL yields an
// empty graph with the right Source and clock, and no error.
func TestOfflineNoSource(t *testing.T) {
	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("offline Snapshot should not error: %v", err)
	}
	if len(g.Identities) != 0 || len(g.Collections) != 0 || len(g.Memberships) != 0 {
		t.Errorf("offline graph should be empty, got %+v", g)
	}
	if g.Source != identitysource.SourceSPIFFE {
		t.Errorf("offline source = %q", g.Source)
	}
}

// TestMissingFileErrors asserts a configured-but-unreadable file surfaces on
// Snapshot (Open never does I/O), not as a silent empty graph.
func TestMissingFileErrors(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"entries_file": filepath.Join("testdata", "does-not-exist.json")}}); err != nil {
		t.Fatalf("Open should not do I/O: %v", err)
	}
	if _, err := s.Snapshot(context.Background()); err == nil {
		t.Fatal("expected an error for a missing entries file")
	}
}

// TestGatherEmitsNothing asserts the no-emit contract: a SPIRE roster travels
// Snapshot, never the Sink.
func TestGatherEmitsNothing(t *testing.T) {
	s := openFile(t, nil)
	err := s.Gather(context.Background(), sinkFunc(func() error {
		t.Fatal("SPIFFE Gather must not emit observations")
		return nil
	}))
	if err != nil {
		t.Fatalf("Gather should return nil, got %v", err)
	}
}

// TestDescriptor asserts the descriptor identity and the absence of any Secret
// config field — a SPIRE entries export carries no credential, so the connector
// declares none (the security invariant at the config layer).
func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if Name != "olivares.spiffe" {
		t.Errorf("Name constant = %q, want olivares.spiffe", Name)
	}
	if d.Name != Name {
		t.Errorf("descriptor name = %q, want %q", d.Name, Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("type = %q, want source", d.Type)
	}
	if d.APIVersion != sdk.APIVersion {
		t.Errorf("api version = %q", d.APIVersion)
	}
	for _, f := range d.ConfigFields {
		if f.Secret {
			t.Errorf("SPIFFE export carries no secret; field %q must not be Secret", f.Key)
		}
	}
}

// TestNoSecretLeak is the security invariant: nothing the connector reads from an
// export is a credential, and nothing about an SVID/key/CA ever appears in the
// produced graph. Even if a hostile export injects secret-looking selector values,
// the connector treats them as opaque matching metadata, never as auth.
func TestNoSecretLeak(t *testing.T) {
	hostile := []byte(`{"entries":[{
		"id":"x",
		"spiffe_id":"spiffe://corp.example/ns/prod/sa/web",
		"parent_id":"spiffe://corp.example/spire/agent/node1",
		"selectors":[{"type":"k8s","value":"ns:prod"}],
		"x509_svid":"-----BEGIN CERTIFICATE-----HOSTILE-----END CERTIFICATE-----",
		"private_key":"super-secret-key",
		"federates_with":["spiffe://attacker.example"]
	}]}`)
	d := &stubDoer{body: hostile}
	s := New()
	s.doer = d
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"entries_url": "https://spire.internal/entries"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if d.gotAuthHdr {
		t.Error("connector must send no credential header for an export read")
	}
	// The connector must never have transcribed the planted secret material into
	// any field of any identity, collection or membership.
	forbidden := []string{"private_key", "super-secret-key", "BEGIN CERTIFICATE", "HOSTILE", "x509_svid"}
	blob := graphBlob(g)
	for _, f := range forbidden {
		if strings.Contains(blob, f) {
			t.Errorf("graph leaked secret-shaped field %q: %s", f, blob)
		}
	}
	// It DID still ingest the workload's identity metadata.
	if _, ok := g.FindIdentity("spiffe://corp.example/ns/prod/sa/web"); !ok {
		t.Error("expected the workload identity to be present")
	}
}

// graphBlob renders every string field of a graph for the leak assertion.
func graphBlob(g identitysource.Graph) string {
	var b strings.Builder
	for _, id := range g.Identities {
		b.WriteString(id.Ref + " " + id.DisplayName + " " + string(id.Kind) + " ")
		for k, v := range id.Attributes {
			b.WriteString(k + "=" + v + " ")
		}
	}
	for _, c := range g.Collections {
		b.WriteString(c.Ref + " " + c.DisplayName + " ")
		for k, v := range c.Attributes {
			b.WriteString(k + "=" + v + " ")
		}
	}
	for _, m := range g.Memberships {
		b.WriteString(m.MemberRef + " " + m.CollectionRef + " ")
	}
	return b.String()
}

// TestParseSpiffeForms unit-tests the dual-form SPIFFE ID parsing directly.
func TestParseSpiffeForms(t *testing.T) {
	cases := []struct {
		name, in, full, td, path string
	}{
		{"flat with path", "spiffe://corp.example/ns/prod/sa/web", "spiffe://corp.example/ns/prod/sa/web", "corp.example", "/ns/prod/sa/web"},
		{"flat bare td", "spiffe://corp.example", "spiffe://corp.example", "corp.example", ""},
		{"no scheme", "corp.example/svc/x", "spiffe://corp.example/svc/x", "corp.example", "/svc/x"},
		{"empty", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseSpiffe(c.in)
			if got.String() != c.full {
				t.Errorf("full = %q, want %q", got.String(), c.full)
			}
			if got.trustDomain() != c.td {
				t.Errorf("td = %q, want %q", got.trustDomain(), c.td)
			}
			if got.pth != c.path {
				t.Errorf("path = %q, want %q", got.pth, c.path)
			}
		})
	}
}

// findCollection returns the collection with ref, or nil.
func findCollection(g identitysource.Graph, ref string) *identitysource.Collection {
	for i := range g.Collections {
		if g.Collections[i].Ref == ref {
			return &g.Collections[i]
		}
	}
	return nil
}

// sinkFunc adapts a func to sdk.Sink for the no-emit assertion.
type sinkFunc func() error

func (f sinkFunc) Emit(context.Context, model.Observation) error { return f() }
