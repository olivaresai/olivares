// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package databricksuc_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	databricksuc "github.com/olivaresai/olivares/connectors/databricks-uc"
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

func openSource(t *testing.T, settings map[string]string) *databricksuc.Source {
	t.Helper()
	s := databricksuc.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *databricksuc.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
}

// TestGatherGoldenEdges asserts every field of every edge derived from the
// fixture, including ObservedAt. A table_lineage row with both sides yields a
// read of the source and a write of the target; a source-only row yields only a
// read; a target-only row yields only a write; a column_lineage row uses the
// databricks.column kind with the column appended to the table full name.
func TestGatherGoldenEdges(t *testing.T) {
	want := []model.EdgeObservation{
		// Row 1: table_lineage, both sides (jane reads customers, writes summary).
		{
			OriginKind: "identity", OriginRef: "jane.doe@acme.com",
			ResourceKind: "databricks.table", ResourceRef: "sales.public.customers",
			Mode: model.ModeRead, Source: model.SignalSource("databricks_uc"), Confidence: model.ConfidenceAttributed,
			ToolRef: "", ObservedAt: mustTime(t, "2026-06-03T10:23:45.123Z"),
		},
		{
			OriginKind: "identity", OriginRef: "jane.doe@acme.com",
			ResourceKind: "databricks.table", ResourceRef: "sales.public.customers_summary",
			Mode: model.ModeWrite, Source: model.SignalSource("databricks_uc"), Confidence: model.ConfidenceAttributed,
			ToolRef: "", ObservedAt: mustTime(t, "2026-06-03T10:23:45.123Z"),
		},
		// Row 2: source-only (read of orders, no target).
		{
			OriginKind: "identity", OriginRef: "jane.doe@acme.com",
			ResourceKind: "databricks.table", ResourceRef: "sales.public.orders",
			Mode: model.ModeRead, Source: model.SignalSource("databricks_uc"), Confidence: model.ConfidenceAttributed,
			ToolRef: "", ObservedAt: mustTime(t, "2026-06-03T10:24:01Z"),
		},
		// Row 3: target-only (write of events_raw); created_by is a declared shared
		// account in this test -> approximate.
		{
			OriginKind: "identity", OriginRef: "etl_service_principal",
			ResourceKind: "databricks.table", ResourceRef: "sales.staging.events_raw",
			Mode: model.ModeWrite, Source: model.SignalSource("databricks_uc"), Confidence: model.ConfidenceApproximate,
			ToolRef: "", ObservedAt: mustTime(t, "2026-06-03T10:24:30Z"),
		},
		// Row 4: column_lineage, both sides.
		{
			OriginKind: "identity", OriginRef: "jane.doe@acme.com",
			ResourceKind: "databricks.column", ResourceRef: "sales.public.customers.email",
			Mode: model.ModeRead, Source: model.SignalSource("databricks_uc"), Confidence: model.ConfidenceAttributed,
			ToolRef: "", ObservedAt: mustTime(t, "2026-06-03T10:25:00.5Z"),
		},
		{
			OriginKind: "identity", OriginRef: "jane.doe@acme.com",
			ResourceKind: "databricks.column", ResourceRef: "sales.public.customers_summary.email_domain",
			Mode: model.ModeWrite, Source: model.SignalSource("databricks_uc"), Confidence: model.ConfidenceAttributed,
			ToolRef: "", ObservedAt: mustTime(t, "2026-06-03T10:25:00.5Z"),
		},
	}

	s := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "lineage.ndjson"),
		"shared_accounts": "etl_service_principal",
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

// TestNoRawSQLEmitted proves the connector never leaks any statement/payload
// fragment (the entity_metadata SQL in the fixture) into an emitted edge field
// (docs/SECURITY-HARDENING.md, minimal data).
func TestNoRawSQLEmitted(t *testing.T) {
	s := openSource(t, map[string]string{
		"path": filepath.Join("testdata", "lineage.ndjson"),
	})
	forbidden := []string{
		"CREATE TABLE", "SELECT", "INSERT INTO", "VALUES", "WHERE",
		"split(email", "current_timestamp", "total > 100", "01ef-",
	}
	for _, e := range gatherBatch(t, s) {
		for _, field := range []string{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef, string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef} {
			for _, frag := range forbidden {
				if strings.Contains(field, frag) {
					t.Errorf("edge field %q leaked fragment %q", field, frag)
				}
			}
		}
	}
}

// TestSharedAccountFlipsConfidence verifies the shared-service-account handling:
// declaring created_by shared drops its edges to approximate, while the raw
// identity is still emitted; removing the declaration restores attributed.
func TestSharedAccountFlipsConfidence(t *testing.T) {
	sShared := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "lineage.ndjson"),
		"shared_accounts": "etl_service_principal",
	})
	for _, e := range gatherBatch(t, sShared) {
		if e.OriginRef == "etl_service_principal" {
			if e.Confidence != model.ConfidenceApproximate {
				t.Errorf("shared etl_service_principal edge confidence = %q, want approximate", e.Confidence)
			}
		} else if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("non-shared edge %q confidence = %q, want attributed", e.OriginRef, e.Confidence)
		}
	}

	sOpen := openSource(t, map[string]string{
		"path": filepath.Join("testdata", "lineage.ndjson"),
	})
	for _, e := range gatherBatch(t, sOpen) {
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("without shared_accounts, edge %q confidence = %q, want attributed", e.OriginRef, e.Confidence)
		}
		// The raw identity is always emitted regardless of confidence.
		if e.OriginRef == "" {
			t.Error("OriginRef must never be empty")
		}
	}
}

// TestModesAreVerbatim asserts every emitted mode is the verbatim structural
// classification (read for a source side, write for a target side) and never
// ModeUnknown — lineage classifies every emittable side, so a row with no
// classifiable side yields no edge rather than an unknown one.
func TestModesAreVerbatim(t *testing.T) {
	s := openSource(t, map[string]string{
		"path": filepath.Join("testdata", "lineage.ndjson"),
	})
	for _, e := range gatherBatch(t, s) {
		if e.Mode != model.ModeRead && e.Mode != model.ModeWrite {
			t.Errorf("edge %q has non-verbatim mode %q", e.ResourceRef, e.Mode)
		}
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := databricksuc.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
	if err := databricksuc.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := databricksuc.New().Descriptor()
	if d.Name != "olivares.databricks-uc" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
}

// TestNDJSONFollow exercises the streaming path: an NDJSON export that grows
// while the connector follows it, then a context cancel that stops Gather
// cleanly.
func TestNDJSONFollow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lineage.ndjson")
	line := `{"source_table_full_name":"sales.public.customers","target_table_full_name":"","created_by":"jane.doe@acme.com","event_time":"2026-06-03T10:23:45.123Z"}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openSource(t, map[string]string{"path": p, "follow": "true"})

	sink := &capturingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	waitFor(t, func() bool { return len(sink.snapshot()) >= 1 })

	line2 := `{"source_table_full_name":"","target_table_full_name":"sales.public.orders","created_by":"jane.doe@acme.com","event_time":"2026-06-03T10:24:00Z"}` + "\n"
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
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
	if edges[0].ResourceRef != "sales.public.customers" || edges[0].Mode != model.ModeRead {
		t.Errorf("first followed edge = {%q,%q}, want {sales.public.customers, read}", edges[0].ResourceRef, edges[0].Mode)
	}
	if edges[1].ResourceRef != "sales.public.orders" || edges[1].Mode != model.ModeWrite {
		t.Errorf("second followed edge = {%q,%q}, want {sales.public.orders, write}", edges[1].ResourceRef, edges[1].Mode)
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
