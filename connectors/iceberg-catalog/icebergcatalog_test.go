// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package icebergcatalog_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	icebergcatalog "github.com/olivaresai/olivares/connectors/iceberg-catalog"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// capturingSink collects emitted edge observations (race-safe).
type capturingSink struct {
	mu    sync.Mutex
	edges []model.EdgeObservation
}

func (c *capturingSink) Emit(_ context.Context, obs model.Observation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := obs.(model.EdgeObservation); ok {
		c.edges = append(c.edges, e)
	}
	return nil
}

func (c *capturingSink) snapshot() []model.EdgeObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.EdgeObservation(nil), c.edges...)
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tt.UTC()
}

func openSource(t *testing.T, settings map[string]string) *icebergcatalog.Source {
	t.Helper()
	s := icebergcatalog.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *icebergcatalog.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
}

// TestGatherGoldenEdges asserts every field of every emitted edge, including
// ObservedAt (the snapshot_at, never time.Now). role:shared_svc is declared
// shared so its grant is approximate; every vended-credential edge is approximate
// regardless of config; TABLE_CREATE and TABLE_READ_PROPERTIES (not data R/RW
// privileges) and TABLE_DROP (in a vended set) produce no edge.
func TestGatherGoldenEdges(t *testing.T) {
	ts := mustTime(t, "2026-06-03T14:05:00Z")
	want := []model.EdgeObservation{
		{
			OriginKind: "identity", OriginRef: "role:analysts",
			ResourceKind: "iceberg.table", ResourceRef: "prod.sales.customers",
			Mode: model.ModeRead, Source: model.SignalPolicy, Confidence: model.ConfidenceAttributed,
			ToolRef: "TABLE_READ_DATA", ObservedAt: ts,
		},
		{
			OriginKind: "identity", OriginRef: "role:etl_writer",
			ResourceKind: "iceberg.table", ResourceRef: "prod.sales.orders",
			Mode: model.ModeWrite, Source: model.SignalPolicy, Confidence: model.ConfidenceAttributed,
			ToolRef: "TABLE_WRITE_DATA", ObservedAt: ts,
		},
		{
			OriginKind: "identity", OriginRef: "role:shared_svc",
			ResourceKind: "iceberg.table", ResourceRef: "prod.sales.customers",
			Mode: model.ModeRead, Source: model.SignalPolicy, Confidence: model.ConfidenceApproximate,
			ToolRef: "TABLE_READ_DATA", ObservedAt: ts,
		},
		{
			OriginKind: "identity", OriginRef: "vended:abc123",
			ResourceKind: "iceberg.table", ResourceRef: "prod.sales.customers",
			Mode: model.ModeRead, Source: model.SignalPolicy, Confidence: model.ConfidenceApproximate,
			ToolRef: "TABLE_READ_DATA", ObservedAt: ts,
		},
		{
			OriginKind: "identity", OriginRef: "vended:def456",
			ResourceKind: "iceberg.table", ResourceRef: "prod.sales.orders",
			Mode: model.ModeRead, Source: model.SignalPolicy, Confidence: model.ConfidenceApproximate,
			ToolRef: "TABLE_READ_DATA", ObservedAt: ts,
		},
		{
			OriginKind: "identity", OriginRef: "vended:def456",
			ResourceKind: "iceberg.table", ResourceRef: "prod.sales.orders",
			Mode: model.ModeWrite, Source: model.SignalPolicy, Confidence: model.ConfidenceApproximate,
			ToolRef: "TABLE_WRITE_DATA", ObservedAt: ts,
		},
	}

	s := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "snapshot.json"),
		"shared_accounts": "role:shared_svc",
	})
	got := gatherBatch(t, s)
	assertEdgesEqual(t, got, want)
}

func assertEdgesEqual(t *testing.T, got, want []model.EdgeObservation) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d edges, want %d\n got=%+v", len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.OriginKind != w.OriginKind || g.OriginRef != w.OriginRef ||
			g.ResourceKind != w.ResourceKind || g.ResourceRef != w.ResourceRef ||
			g.Mode != w.Mode || g.Source != w.Source || g.Confidence != w.Confidence ||
			g.ToolRef != w.ToolRef || !g.ObservedAt.Equal(w.ObservedAt) {
			t.Errorf("edge[%d]\n got=%+v\nwant=%+v", i, g, w)
		}
	}
}

// TestAllEdgesArePolicySource proves every emitted edge is the PERMITTED side
// (Source==model.SignalPolicy), i.e. a declared grant, NOT an observed signal.
// This is the core invariant of this connector: it never emits an observed
// access; module III/VI does the permitted-vs-observed diff (ARCHITECTURE.md).
func TestAllEdgesArePolicySource(t *testing.T) {
	s := openSource(t, map[string]string{"path": filepath.Join("testdata", "snapshot.json")})
	edges := gatherBatch(t, s)
	if len(edges) == 0 {
		t.Fatal("expected edges")
	}
	for i, e := range edges {
		if e.Source != model.SignalPolicy {
			t.Errorf("edge[%d] Source = %q, want %q (permitted policy side, not an observed signal)", i, e.Source, model.SignalPolicy)
		}
	}
}

// TestNoCredentialLeak proves the connector never leaks vended-credential material
// (storage credential, secret access key, session token) or the expires_at field
// into any emitted edge field (docs/SECURITY-HARDENING.md, minimal data). These tokens are present
// in the fixture's vended_credentials objects but are never read fields.
func TestNoCredentialLeak(t *testing.T) {
	s := openSource(t, map[string]string{"path": filepath.Join("testdata", "snapshot.json")})
	forbidden := []string{
		"AKIAEXAMPLESECRET0001", "AKIAEXAMPLESECRET0002",
		"wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY",
		"abcdefgVENDEDSECRETMUSTNOTAPPEAR1234567",
		"FQoGZXIvYXdzEExampleSessionTokenDoNotLeak==",
		"2026-06-03T15:05:00Z", "2026-06-03T15:10:00Z", // expires_at, never emitted
	}
	for _, e := range gatherBatch(t, s) {
		for _, field := range []string{
			e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef,
			string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef,
		} {
			for _, frag := range forbidden {
				if strings.Contains(field, frag) {
					t.Errorf("edge field %q leaked forbidden fragment %q", field, frag)
				}
			}
		}
	}
}

// TestSharedAccountFlipsConfidence verifies the shared-service-account handling on
// the static grant side: declaring a grant's principal shared flips its confidence
// to approximate; removing the declaration restores attributed. (Vended-credential
// edges are always approximate and are not affected by this knob — asserted in the
// golden test.)
func TestSharedAccountFlipsConfidence(t *testing.T) {
	// role:analysts declared shared -> its grant is approximate.
	sShared := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "snapshot.json"),
		"shared_accounts": "role:analysts",
	})
	got := gatherBatch(t, sShared)
	if got[0].OriginRef != "role:analysts" || got[0].Confidence != model.ConfidenceApproximate {
		t.Errorf("with role:analysts shared, edge[0] = {%q,%q}, want {role:analysts, approximate}", got[0].OriginRef, got[0].Confidence)
	}

	// No shared accounts -> role:analysts is attributed.
	sOpen := openSource(t, map[string]string{
		"path": filepath.Join("testdata", "snapshot.json"),
	})
	got2 := gatherBatch(t, sOpen)
	if got2[0].OriginRef != "role:analysts" || got2[0].Confidence != model.ConfidenceAttributed {
		t.Errorf("without shared_accounts, edge[0] = {%q,%q}, want {role:analysts, attributed}", got2[0].OriginRef, got2[0].Confidence)
	}
}

// TestUnparseableSnapshotTimestamp verifies a snapshot whose snapshot_at cannot be
// parsed yields NO edges (rather than edges with a fabricated time): ObservedAt is
// the dedup natural key and must be the source clock.
func TestUnparseableSnapshotTimestamp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad-ts.json")
	body := `{"snapshot_at":"not-a-timestamp","grants":[{"principal":"role:x","table":"c.n.t","privilege":"TABLE_READ_DATA"}]}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openSource(t, map[string]string{"path": p})
	if got := gatherBatch(t, s); len(got) != 0 {
		t.Errorf("got %d edges for an unparseable snapshot_at, want 0\n%+v", len(got), got)
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := icebergcatalog.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
	if err := icebergcatalog.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := icebergcatalog.New().Descriptor()
	if d.Name != "olivares.iceberg-catalog" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
}
