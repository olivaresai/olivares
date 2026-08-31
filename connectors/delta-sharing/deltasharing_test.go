// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package deltasharing_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	deltasharing "github.com/olivaresai/olivares/connectors/delta-sharing"
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
	tt, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tt.UTC()
}

func openSource(t *testing.T, settings map[string]string) *deltasharing.Source {
	t.Helper()
	s := deltasharing.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *deltasharing.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
}

// TestGatherGoldenEdges asserts every emitted field for every record in the
// fixture, including ObservedAt. partner-pool is declared shared, so its read is
// approximate; the adminRotateToken record is an action the protocol vocabulary
// does not recognize as a recipient read, so it is Mode=unknown (not dropped, not
// guessed). The share-level listShares is a deltasharing.share resource.
func TestGatherGoldenEdges(t *testing.T) {
	want := []model.EdgeObservation{
		{
			OriginKind: "identity", OriginRef: "acme-corp",
			ResourceKind: "deltasharing.table", ResourceRef: "sales_share.public.q3",
			Mode: model.ModeRead, Source: model.SignalSource("delta_sharing"), Confidence: model.ConfidenceAttributed,
			ToolRef: "queryTable", ObservedAt: mustTime(t, "2026-06-04T09:15:01Z"),
		},
		{
			OriginKind: "identity", OriginRef: "acme-corp",
			ResourceKind: "deltasharing.table", ResourceRef: "sales_share.public.q3",
			Mode: model.ModeRead, Source: model.SignalSource("delta_sharing"), Confidence: model.ConfidenceAttributed,
			ToolRef: "getTableData", ObservedAt: mustTime(t, "2026-06-04T09:15:02.500Z"),
		},
		{
			OriginKind: "identity", OriginRef: "acme-corp",
			ResourceKind: "deltasharing.table", ResourceRef: "sales_share.public.q4",
			Mode: model.ModeRead, Source: model.SignalSource("delta_sharing"), Confidence: model.ConfidenceAttributed,
			ToolRef: "getTableVersion", ObservedAt: mustTime(t, "2026-06-04T09:15:03Z"),
		},
		{
			OriginKind: "identity", OriginRef: "partner-pool",
			ResourceKind: "deltasharing.table", ResourceRef: "sales_share.public.forecast",
			Mode: model.ModeRead, Source: model.SignalSource("delta_sharing"), Confidence: model.ConfidenceApproximate,
			ToolRef: "queryTable", ObservedAt: mustTime(t, "2026-06-04T09:15:04Z"),
		},
		{
			OriginKind: "identity", OriginRef: "acme-corp",
			ResourceKind: "deltasharing.share", ResourceRef: "sales_share",
			Mode: model.ModeRead, Source: model.SignalSource("delta_sharing"), Confidence: model.ConfidenceAttributed,
			ToolRef: "listShares", ObservedAt: mustTime(t, "2026-06-04T09:15:05Z"),
		},
		{
			OriginKind: "identity", OriginRef: "acme-corp",
			ResourceKind: "deltasharing.table", ResourceRef: "sales_share.public.audit_internal",
			Mode: model.ModeUnknown, Source: model.SignalSource("delta_sharing"), Confidence: model.ConfidenceAttributed,
			ToolRef: "adminRotateToken", ObservedAt: mustTime(t, "2026-06-04T09:15:06Z"),
		},
	}

	s := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "audit.jsonl"),
		"shared_accounts": "partner-pool",
		"follow":          "false",
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

// TestSourceConstant pins the SignalSource value the contract requires.
func TestSourceConstant(t *testing.T) {
	if deltasharing.SignalDeltaSharing != model.SignalSource("delta_sharing") {
		t.Errorf("SignalDeltaSharing = %q, want delta_sharing", deltasharing.SignalDeltaSharing)
	}
}

// TestNoRawLeak proves the connector never leaks a query predicate, a pre-signed
// file URL, a recipient bearer token, or a returned-row value into any emitted
// edge field — only the egress edge is emitted (docs/SECURITY-HARDENING.md, minimal data).
func TestNoRawLeak(t *testing.T) {
	s := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "audit.jsonl"),
		"follow": "false",
	})
	forbidden := []string{
		"WHERE region", "EMEA", "revenue > 1000000",
		"SELECT customer_email", "customer_email",
		"recipient-bearer-SECRET-xyz", "pool-bearer-SECRET-987", "SECRET",
		"presigned.example.com", "DEADBEEF", "sig=",
		"forecast_year = 2027", "42000",
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

// TestSharedAccountFlipsConfidence verifies the shared-recipient handling: a
// recipient declared shared/pooled is approximate; without the declaration the
// same recipient is attributed. The raw recipient identity is always emitted in
// OriginRef regardless (docs/contracts).
func TestSharedAccountFlipsConfidence(t *testing.T) {
	// acme-corp declared shared -> all its reads are approximate.
	sShared := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "audit.jsonl"),
		"shared_accounts": "acme-corp, partner-pool",
		"follow":          "false",
	})
	for _, e := range gatherBatch(t, sShared) {
		if e.Confidence != model.ConfidenceApproximate {
			t.Errorf("with both recipients shared, edge %q/%q confidence = %q, want approximate", e.OriginRef, e.ToolRef, e.Confidence)
		}
		if e.OriginRef == "" {
			t.Error("raw recipient identity must still be emitted when shared")
		}
	}

	// No shared accounts -> acme-corp is attributed (the partner-pool record is
	// the only approximate one in the default fixture run, asserted by golden).
	sOpen := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "audit.jsonl"),
		"follow": "false",
	})
	got := gatherBatch(t, sOpen)
	for _, e := range got {
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("without shared_accounts, edge %q/%q = %q, want attributed", e.OriginRef, e.ToolRef, e.Confidence)
		}
	}
}

// TestUnknownMode asserts that an action the protocol vocabulary does not
// recognize as a recipient read yields Mode=unknown (explicit, never guessed).
func TestUnknownMode(t *testing.T) {
	s := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "audit.jsonl"),
		"follow": "false",
	})
	var seen bool
	for _, e := range gatherBatch(t, s) {
		if e.ToolRef == "adminRotateToken" {
			seen = true
			if e.Mode != model.ModeUnknown {
				t.Errorf("adminRotateToken Mode = %q, want unknown", e.Mode)
			}
		} else if e.Mode != model.ModeRead {
			t.Errorf("recipient read %q Mode = %q, want read", e.ToolRef, e.Mode)
		}
	}
	if !seen {
		t.Fatal("fixture must contain an unrecognized action exercising Mode=unknown")
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := deltasharing.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
	if err := deltasharing.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := deltasharing.New().Descriptor()
	if d.Name != "olivares.delta-sharing" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
}

// TestFollow exercises the streaming path: an audit log that grows while the
// connector follows it, then a context cancel that stops Gather cleanly.
func TestFollow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "delta-sharing.jsonl")
	line := `{"timestamp":"2026-06-04T09:15:01Z","recipient":"acme-corp","share":"sales_share","schema":"public","table":"q3","action":"queryTable"}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openSource(t, map[string]string{"path": p, "follow": "true"})

	sink := &capturingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	waitFor(t, func() bool { return len(sink.snapshot()) >= 1 })

	// Append a second record; the follower must pick it up.
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	line2 := `{"timestamp":"2026-06-04T09:15:02Z","recipient":"acme-corp","share":"sales_share","schema":"public","table":"q4","action":"getTableData"}` + "\n"
	if _, err := f.WriteString(line2); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	waitFor(t, func() bool { return len(sink.snapshot()) >= 2 })
	cancel()
	if err := <-done; err != nil && ctx.Err() == nil {
		t.Fatalf("Gather returned unexpected error: %v", err)
	}
	edges := sink.snapshot()
	if edges[0].ResourceRef != "sales_share.public.q3" || edges[1].ResourceRef != "sales_share.public.q4" {
		t.Errorf("followed edges = %q, %q", edges[0].ResourceRef, edges[1].ResourceRef)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}
