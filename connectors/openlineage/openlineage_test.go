// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openlineage_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/openlineage"
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

func openSource(t *testing.T, settings map[string]string) *openlineage.Source {
	t.Helper()
	s := openlineage.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *openlineage.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
}

// TestGatherGoldenEdges asserts every emitted field for the fixture. The fixture
// has a START and a RUNNING event for the same run (both must be skipped so the
// START+COMPLETE pair is not double-counted), one COMPLETE with 2 inputs + 1
// output, and one COMPLETE (numeric-offset eventTime) on a shared job.
func TestGatherGoldenEdges(t *testing.T) {
	want := []model.EdgeObservation{
		{
			OriginKind: "identity", OriginRef: "airflow://etl-prod/daily_sales.transform",
			ResourceKind: "openlineage.dataset", ResourceRef: "postgres://wh:5432/salesdb.public.customers",
			Mode: model.ModeRead, Source: model.SignalSource("openlineage"), Confidence: model.ConfidenceAttributed,
			ToolRef: "COMPLETE", ObservedAt: mustTime(t, "2026-06-03T10:23:47.123Z"),
		},
		{
			OriginKind: "identity", OriginRef: "airflow://etl-prod/daily_sales.transform",
			ResourceKind: "openlineage.dataset", ResourceRef: "postgres://wh:5432/salesdb.public.orders",
			Mode: model.ModeRead, Source: model.SignalSource("openlineage"), Confidence: model.ConfidenceAttributed,
			ToolRef: "COMPLETE", ObservedAt: mustTime(t, "2026-06-03T10:23:47.123Z"),
		},
		{
			OriginKind: "identity", OriginRef: "airflow://etl-prod/daily_sales.transform",
			ResourceKind: "openlineage.dataset", ResourceRef: "postgres://wh:5432/salesdb.public.sales_summary",
			Mode: model.ModeWrite, Source: model.SignalSource("openlineage"), Confidence: model.ConfidenceAttributed,
			ToolRef: "COMPLETE", ObservedAt: mustTime(t, "2026-06-03T10:23:47.123Z"),
		},
		{
			OriginKind: "identity", OriginRef: "spark://analytics/shared_runner.aggregate",
			ResourceKind: "openlineage.dataset", ResourceRef: "s3://lake/events.raw_clicks",
			Mode: model.ModeRead, Source: model.SignalSource("openlineage"), Confidence: model.ConfidenceAttributed,
			ToolRef: "COMPLETE", ObservedAt: mustTime(t, "2026-06-03T02:08:00.001Z"),
		},
		{
			OriginKind: "identity", OriginRef: "spark://analytics/shared_runner.aggregate",
			ResourceKind: "openlineage.dataset", ResourceRef: "s3://lake/events.agg_clicks",
			Mode: model.ModeWrite, Source: model.SignalSource("openlineage"), Confidence: model.ConfidenceAttributed,
			ToolRef: "COMPLETE", ObservedAt: mustTime(t, "2026-06-03T02:08:00.001Z"),
		},
	}

	s := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "events.ndjson"),
		"follow": "false",
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

// TestNoRawLeak proves the connector never leaks an SQL body, a facet payload or
// a run id into any emitted edge field (docs/SECURITY-HARDENING.md, minimal data).
func TestNoRawLeak(t *testing.T) {
	s := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "events.ndjson"),
		"follow": "false",
	})
	forbidden := []string{
		"INSERT INTO sales_summary", "SELECT * FROM customers", "JOIN orders",
		"secret_token", "SELECT secret_token",
		"rowCount", "dataQualityMetrics", "outputStatistics", "99999",
		"3f2504e0", "a1b2c3d4", "runId", "dataSource",
	}
	for _, e := range gatherBatch(t, s) {
		fields := []string{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef, string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef}
		for _, field := range fields {
			for _, frag := range forbidden {
				if strings.Contains(field, frag) {
					t.Errorf("edge field %q leaked fragment %q", field, frag)
				}
			}
		}
	}
}

// TestSharedAccountFlipsConfidence verifies that declaring the job (its
// namespace/name reference) as a shared account flips its edges to approximate,
// while undeclared jobs stay attributed; removing the declaration restores
// attributed.
func TestSharedAccountFlipsConfidence(t *testing.T) {
	sShared := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "events.ndjson"),
		"follow":          "false",
		"shared_accounts": "spark://analytics/shared_runner.aggregate",
	})
	for _, e := range gatherBatch(t, sShared) {
		switch e.OriginRef {
		case "spark://analytics/shared_runner.aggregate":
			if e.Confidence != model.ConfidenceApproximate {
				t.Errorf("shared job edge confidence = %q, want approximate", e.Confidence)
			}
		default:
			if e.Confidence != model.ConfidenceAttributed {
				t.Errorf("undeclared job %q confidence = %q, want attributed", e.OriginRef, e.Confidence)
			}
		}
	}

	// Without the declaration, the same job is attributed.
	sOpen := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "events.ndjson"),
		"follow": "false",
	})
	for _, e := range gatherBatch(t, sOpen) {
		if e.OriginRef == "spark://analytics/shared_runner.aggregate" && e.Confidence != model.ConfidenceAttributed {
			t.Errorf("without shared_accounts, edge = {%q,%q}, want attributed", e.OriginRef, e.Confidence)
		}
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := openlineage.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
	if err := openlineage.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := openlineage.New().Descriptor()
	if d.Name != "olivares.openlineage" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
}

// TestFollow exercises the streaming path: a RunEvents file that grows while the
// connector follows it, then a context cancel that stops Gather cleanly.
func TestFollow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "events.ndjson")
	line := `{"eventType":"COMPLETE","eventTime":"2026-06-03T10:23:47.123Z","run":{"runId":"r1"},"job":{"namespace":"airflow://etl-prod","name":"daily_sales.transform"},"inputs":[{"namespace":"postgres://wh:5432","name":"salesdb.public.customers"}],"outputs":[]}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openSource(t, map[string]string{"path": p, "follow": "true"})

	sink := &capturingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	waitFor(t, func() bool { return len(sink.snapshot()) >= 1 })

	// Append a second COMPLETE; the follower must pick it up.
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	line2 := `{"eventType":"COMPLETE","eventTime":"2026-06-03T10:23:48.000Z","run":{"runId":"r2"},"job":{"namespace":"airflow://etl-prod","name":"daily_sales.transform"},"inputs":[],"outputs":[{"namespace":"postgres://wh:5432","name":"salesdb.public.orders"}]}` + "\n"
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
	if edges[0].ResourceRef != "postgres://wh:5432/salesdb.public.customers" || edges[0].Mode != model.ModeRead {
		t.Errorf("followed edge[0] = {%q,%q}", edges[0].ResourceRef, edges[0].Mode)
	}
	if edges[1].ResourceRef != "postgres://wh:5432/salesdb.public.orders" || edges[1].Mode != model.ModeWrite {
		t.Errorf("followed edge[1] = {%q,%q}", edges[1].ResourceRef, edges[1].Mode)
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
