// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package snowflakeaudit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	snowflakeaudit "github.com/olivaresai/olivares/connectors/snowflake-audit"
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

func sfTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse("2006-01-02 15:04:05.999 -0700", s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tt.UTC()
}

func openSource(t *testing.T, settings map[string]string) *snowflakeaudit.Source {
	t.Helper()
	s := snowflakeaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *snowflakeaudit.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
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

// TestGatherGoldenEdges asserts every emitted field for the fixture, including
// the column/table granularity split and the verbatim per-bucket R/RW mapping.
func TestGatherGoldenEdges(t *testing.T) {
	s := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "access_history.ndjson"),
		"follow":          "false",
		"shared_accounts": "ETL_POOL",
	})

	mkRead := func(kind, ref, tool, ts string) model.EdgeObservation {
		return model.EdgeObservation{
			OriginKind: "identity", OriginRef: "CLAUDE_AGENT_7",
			ResourceKind: kind, ResourceRef: ref,
			Mode: model.ModeRead, Source: snowflakeaudit.SignalSnowflakeAccessHistory,
			Confidence: model.ConfidenceAttributed, ToolRef: tool, ObservedAt: sfTime(t, ts),
		}
	}

	want := []model.EdgeObservation{
		// Row 1: DIRECT customers (ID, EMAIL), then BASE customers (ID).
		mkRead("snowflake.column", "SALESDB.PUBLIC.CUSTOMERS.ID", "direct_objects_accessed", "2026-06-03 10:23:45.123 +0000"),
		mkRead("snowflake.column", "SALESDB.PUBLIC.CUSTOMERS.EMAIL", "direct_objects_accessed", "2026-06-03 10:23:45.123 +0000"),
		mkRead("snowflake.column", "SALESDB.PUBLIC.CUSTOMERS.ID", "base_objects_accessed", "2026-06-03 10:23:45.123 +0000"),
		// Row 2: DIRECT view (no columns -> table-grained), then BASE customers (EMAIL).
		mkRead("snowflake.table", "SALESDB.PUBLIC.ACTIVE_CUSTOMERS_V", "direct_objects_accessed", "2026-06-03 10:23:46.200 +0000"),
		mkRead("snowflake.column", "SALESDB.PUBLIC.CUSTOMERS.EMAIL", "base_objects_accessed", "2026-06-03 10:23:46.200 +0000"),
		// Row 3: OBJECTS_MODIFIED orders (STATUS) -> write column edge.
		{
			OriginKind: "identity", OriginRef: "CLAUDE_AGENT_7",
			ResourceKind: "snowflake.column", ResourceRef: "SALESDB.PUBLIC.ORDERS.STATUS",
			Mode: model.ModeWrite, Source: snowflakeaudit.SignalSnowflakeAccessHistory,
			Confidence: model.ConfidenceAttributed, ToolRef: "objects_modified", ObservedAt: sfTime(t, "2026-06-03 10:23:47.050 +0000"),
		},
		// Row 4: ETL_SVC w/ shared ETL_POOL role -> write table edge, approximate.
		{
			OriginKind: "identity", OriginRef: "ETL_SVC",
			ResourceKind: "snowflake.table", ResourceRef: "SALESDB.PUBLIC.STAGING_2026",
			Mode: model.ModeWrite, Source: snowflakeaudit.SignalSnowflakeAccessHistory,
			Confidence: model.ConfidenceApproximate, ToolRef: "objects_modified", ObservedAt: sfTime(t, "2026-06-03 10:23:48.000 +0000"),
		},
		// Row 5: a Function read (no columns) -> table-grained read; domain does not
		// change the verbatim read/write mapping.
		mkRead("snowflake.table", "SALESDB.PUBLIC.NORMALIZE_EMAIL", "direct_objects_accessed", "2026-06-03 10:23:49.000 +0000"),
	}
	assertEdgesEqual(t, gatherBatch(t, s), want)
}

// TestNoRawLeak proves that no SQL/identifier fragment that is NOT part of the
// emitted edge contract (a query id, an argument signature, a lineage detail)
// leaks into any emitted field.
func TestNoRawLeak(t *testing.T) {
	s := openSource(t, map[string]string{"path": filepath.Join("testdata", "access_history.ndjson")})
	forbidden := []string{
		"01b2c3d4",       // QUERY_ID
		"(VARCHAR)",      // argumentSignature
		"objectId",       // lineage/internal key
		"68610", "66564", // numeric object/column ids
		"argumentSignature", "baseSources", "directSources",
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

// TestSharedAccountFlipsConfidence checks the shared-service-account handling:
// declaring the role (or the user) shared flips that user's edges to approximate;
// undeclaring restores attributed. The raw USER_NAME is emitted in both cases.
func TestSharedAccountFlipsConfidence(t *testing.T) {
	// ETL_POOL shared -> ETL_SVC edge approximate; CLAUDE_AGENT_7 still attributed.
	sShared := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "access_history.ndjson"),
		"shared_accounts": "ETL_POOL",
	})
	for _, e := range gatherBatch(t, sShared) {
		if e.OriginRef == "ETL_SVC" {
			if e.Confidence != model.ConfidenceApproximate {
				t.Errorf("ETL_SVC edge confidence = %q, want approximate (role ETL_POOL is shared)", e.Confidence)
			}
		} else if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("%s edge confidence = %q, want attributed", e.OriginRef, e.Confidence)
		}
	}

	// No shared accounts -> ETL_SVC attributed (raw user still emitted).
	sOpen := openSource(t, map[string]string{"path": filepath.Join("testdata", "access_history.ndjson")})
	var sawETL bool
	for _, e := range gatherBatch(t, sOpen) {
		if e.OriginRef == "ETL_SVC" {
			sawETL = true
			if e.Confidence != model.ConfidenceAttributed {
				t.Errorf("without shared_accounts, ETL_SVC edge confidence = %q, want attributed", e.Confidence)
			}
		}
	}
	if !sawETL {
		t.Fatal("expected an ETL_SVC edge in the fixture")
	}

	// Declaring the USER_NAME (not the role) shared also flips confidence.
	sUser := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "access_history.ndjson"),
		"shared_accounts": "claude_agent_7",
	})
	for _, e := range gatherBatch(t, sUser) {
		if e.OriginRef == "CLAUDE_AGENT_7" && e.Confidence != model.ConfidenceApproximate {
			t.Errorf("CLAUDE_AGENT_7 edge confidence = %q, want approximate (user declared shared)", e.Confidence)
		}
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := snowflakeaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
	if err := snowflakeaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := snowflakeaudit.New().Descriptor()
	if d.Name != "olivares.snowflake-audit" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
}

// TestFollow exercises the streaming path: the connector follows the export
// while it grows, then a context cancel stops Gather cleanly.
func TestFollow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ah.ndjson")
	line1 := `{"QUERY_ID":"q1","QUERY_START_TIME":"2026-06-03 10:23:45.123 +0000","USER_NAME":"CLAUDE_AGENT_7","ROLE_NAME":"ANALYST_RW","DIRECT_OBJECTS_ACCESSED":[{"objectDomain":"Table","objectName":"SALESDB.PUBLIC.CUSTOMERS","columns":[{"columnName":"ID"}]}],"BASE_OBJECTS_ACCESSED":[],"OBJECTS_MODIFIED":[]}` + "\n"
	if err := os.WriteFile(p, []byte(line1), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openSource(t, map[string]string{"path": p, "follow": "true"})

	sink := &capturingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	waitFor(t, func() bool { return len(sink.snapshot()) >= 1 })

	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	line2 := `{"QUERY_ID":"q2","QUERY_START_TIME":"2026-06-03 10:23:46.000 +0000","USER_NAME":"CLAUDE_AGENT_7","ROLE_NAME":"ANALYST_RW","DIRECT_OBJECTS_ACCESSED":[],"BASE_OBJECTS_ACCESSED":[],"OBJECTS_MODIFIED":[{"objectDomain":"Table","objectName":"SALESDB.PUBLIC.ORDERS"}]}` + "\n"
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
	if edges[0].ResourceRef != "SALESDB.PUBLIC.CUSTOMERS.ID" || edges[0].Mode != model.ModeRead {
		t.Errorf("edge[0] = %+v", edges[0])
	}
	if edges[1].ResourceRef != "SALESDB.PUBLIC.ORDERS" || edges[1].Mode != model.ModeWrite {
		t.Errorf("edge[1] = %+v", edges[1])
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
