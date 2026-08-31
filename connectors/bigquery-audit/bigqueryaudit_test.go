// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bigqueryaudit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bigqueryaudit "github.com/olivaresai/olivares/connectors/bigquery-audit"
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

func openSource(t *testing.T, settings map[string]string) *bigqueryaudit.Source {
	t.Helper()
	s := bigqueryaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *bigqueryaudit.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
}

const fixture = "audit.ndjson"

func TestGatherGoldenEdges(t *testing.T) {
	// The fixture has four entries: a tableDataRead, a tableDataChange (query), a
	// shared-account tableDataChange (WRITE_API), and a job-level entry with no
	// table-data event (skipped, not an edge). Only the first three are emitted.
	want := []model.EdgeObservation{
		{
			OriginKind: "identity", OriginRef: "analyst@acme.iam.gserviceaccount.com",
			ResourceKind: "bigquery.table", ResourceRef: "acme-prod.sales.customers",
			Mode: model.ModeRead, Source: model.SignalSource("bigquery_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "google.cloud.bigquery.v2.JobService.InsertJob", ObservedAt: mustTime(t, "2026-06-03T10:23:45.123456Z"),
		},
		{
			OriginKind: "identity", OriginRef: "analyst@acme.iam.gserviceaccount.com",
			ResourceKind: "bigquery.table", ResourceRef: "acme-prod.sales.orders",
			Mode: model.ModeWrite, Source: model.SignalSource("bigquery_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "google.cloud.bigquery.v2.JobService.InsertJob", ObservedAt: mustTime(t, "2026-06-03T10:23:46.200000Z"),
		},
		{
			OriginKind: "identity", OriginRef: "etl-pool@acme.iam.gserviceaccount.com",
			ResourceKind: "bigquery.table", ResourceRef: "acme-prod.staging.events_2026",
			Mode: model.ModeWrite, Source: model.SignalSource("bigquery_audit"), Confidence: model.ConfidenceApproximate,
			ToolRef: "google.cloud.bigquery.storage.v1.BigQueryWrite.AppendRows", ObservedAt: mustTime(t, "2026-06-03T10:23:47.050000Z"),
		},
	}

	s := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", fixture),
		"follow":          "false",
		"shared_accounts": "etl-pool@acme.iam.gserviceaccount.com",
	})
	assertEdgesEqual(t, gatherBatch(t, s), want)
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

// TestNoQueryBodyEmitted proves the connector never leaks the SQL/query body or
// any non-edge metadata into an emitted edge field (docs/SECURITY-HARDENING.md, minimal data).
// The fixture deliberately embeds query text in jobChange.* fields the connector
// does not read.
func TestNoQueryBodyEmitted(t *testing.T) {
	s := openSource(t, map[string]string{"path": filepath.Join("testdata", fixture)})
	forbidden := []string{
		"SELECT id, email", "FROM sales.customers", "INSERT INTO sales.orders",
		"VALUES", "CREATE TEMP FUNCTION", "queryConfig", "bquxjob_read_001",
		"insertedRowsCount", "WRITE_API", "streams/abc", "WHERE region",
	}
	for _, e := range gatherBatch(t, s) {
		for _, field := range []string{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef, string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef} {
			for _, frag := range forbidden {
				if strings.Contains(field, frag) {
					t.Errorf("edge field %q leaked forbidden fragment %q", field, frag)
				}
			}
		}
	}
}

// TestSharedAccountFlipsConfidence verifies the shared-service-account handling:
// declaring a principalEmail shared drops its accesses to approximate (the raw
// identity is still emitted), and removing the declaration restores attributed.
func TestSharedAccountFlipsConfidence(t *testing.T) {
	// analyst declared shared -> its two edges become approximate.
	sShared := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", fixture),
		"shared_accounts": "analyst@acme.iam.gserviceaccount.com",
	})
	got := gatherBatch(t, sShared)
	if len(got) != 3 {
		t.Fatalf("got %d edges, want 3", len(got))
	}
	for i := 0; i < 2; i++ {
		if got[i].Confidence != model.ConfidenceApproximate {
			t.Errorf("edge[%d] confidence = %q, want approximate", i, got[i].Confidence)
		}
		if got[i].OriginRef != "analyst@acme.iam.gserviceaccount.com" {
			t.Errorf("edge[%d] OriginRef = %q; raw identity must still be emitted", i, got[i].OriginRef)
		}
	}
	// etl-pool was NOT declared this time -> attributed.
	if got[2].Confidence != model.ConfidenceAttributed {
		t.Errorf("edge[2] confidence = %q, want attributed (etl-pool not declared shared)", got[2].Confidence)
	}

	// No shared accounts -> every edge attributed.
	sOpen := openSource(t, map[string]string{"path": filepath.Join("testdata", fixture)})
	for i, e := range gatherBatch(t, sOpen) {
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("without shared_accounts, edge[%d] confidence = %q, want attributed", i, e.Confidence)
		}
	}
}

// TestNoTableDataEventSkipped confirms an audit entry whose metadata carries
// neither tableDataRead nor tableDataChange (a job-level event) is NOT emitted as
// an edge — the read/write nature is never guessed, and a non-data-access entry
// is not a resource edge (ARCHITECTURE.md). The fixture's 4th entry is exactly this.
func TestNoTableDataEventSkipped(t *testing.T) {
	s := openSource(t, map[string]string{"path": filepath.Join("testdata", fixture)})
	got := gatherBatch(t, s)
	if len(got) != 3 {
		t.Fatalf("got %d edges, want 3 (the job-level no-table-data entry must be skipped)", len(got))
	}
	for _, e := range got {
		if e.Mode == model.ModeUnknown {
			t.Errorf("no edge should be ModeUnknown: bigquery classifies every emitted edge, got %+v", e)
		}
		if !strings.HasPrefix(e.ResourceRef, "acme-prod.") {
			t.Errorf("edge resource %q is not a fully-qualified table", e.ResourceRef)
		}
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := bigqueryaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
	if err := bigqueryaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := bigqueryaudit.New().Descriptor()
	if d.Name != "olivares.bigquery-audit" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
	// path is required.
	var hasPath bool
	for _, f := range d.ConfigFields {
		if f.Key == "path" && f.Required {
			hasPath = true
		}
	}
	if !hasPath {
		t.Error("Descriptor must declare a required `path` config field")
	}
}

// TestFollow exercises the streaming path: an NDJSON export that grows while the
// connector follows it, then a context cancel that stops Gather cleanly.
func TestFollow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bq.ndjson")
	line1 := `{"timestamp":"2026-06-03T10:23:45.123456Z","protoPayload":{"authenticationInfo":{"principalEmail":"a@acme.iam.gserviceaccount.com"},"methodName":"google.cloud.bigquery.v2.JobService.InsertJob","resourceName":"projects/acme-prod/datasets/sales/tables/customers","metadata":{"tableDataRead":{"reason":"JOB"}}}}` + "\n"
	if err := os.WriteFile(p, []byte(line1), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openSource(t, map[string]string{"path": p, "follow": "true"})

	sink := &capturingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	waitFor(t, func() bool { return len(sink.snapshot()) >= 1 })

	line2 := `{"timestamp":"2026-06-03T10:23:46.000000Z","protoPayload":{"authenticationInfo":{"principalEmail":"a@acme.iam.gserviceaccount.com"},"methodName":"google.cloud.bigquery.v2.JobService.InsertJob","resourceName":"projects/acme-prod/datasets/sales/tables/orders","metadata":{"tableDataChange":{"insertedRowsCount":"1"}}}}` + "\n"
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
	if edges[0].ResourceRef != "acme-prod.sales.customers" || edges[0].Mode != model.ModeRead {
		t.Errorf("edge[0] = {%q,%q}, want {acme-prod.sales.customers, read}", edges[0].ResourceRef, edges[0].Mode)
	}
	if edges[1].ResourceRef != "acme-prod.sales.orders" || edges[1].Mode != model.ModeWrite {
		t.Errorf("edge[1] = {%q,%q}, want {acme-prod.sales.orders, write}", edges[1].ResourceRef, edges[1].Mode)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	// Follow polls EOF once per second in production. Three seconds gave the race runner only
	// three scheduling windows; run 33254801927 spent 3.43 s here under host contention even
	// though the same test passes 20/20 under -race in isolation. This is a liveness bound, not
	// the behavior under test: the assertions below still require both exact edges and modes.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}
