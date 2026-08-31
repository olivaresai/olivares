// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mssqlaudit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mssqlaudit "github.com/olivaresai/olivares/connectors/mssql-audit"
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

// mustTime parses an mssql datetime2-style UTC timestamp for the golden table.
func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.ParseInLocation("2006-01-02T15:04:05.9999999", s, time.UTC)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tt.UTC()
}

func openSource(t *testing.T, settings map[string]string) *mssqlaudit.Source {
	t.Helper()
	s := mssqlaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *mssqlaudit.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
}

// TestGatherGoldenEdges asserts every emitted field, including ObservedAt, for the
// fixture. It exercises read (SELECT), write (INSERT/UPDATE/DELETE), unknown
// (EXECUTE), the action_name fallback (SELECT on a view -> mssql.object), and a
// shared-account approximate flip (etl_pool). The RECEIVE ("RC") row is dropped
// (not a DML/EXECUTE data access), proving the skip.
func TestGatherGoldenEdges(t *testing.T) {
	want := []model.EdgeObservation{
		{
			OriginKind: "identity", OriginRef: "claude-agent-7",
			ResourceKind: "mssql.table", ResourceRef: "salesdb.dbo.customers",
			Mode: model.ModeRead, Source: model.SignalSource("mssql_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "SELECT", ObservedAt: mustTime(t, "2026-06-03T10:23:45.1230000"),
		},
		{
			OriginKind: "identity", OriginRef: "claude-agent-7",
			ResourceKind: "mssql.table", ResourceRef: "salesdb.dbo.orders",
			Mode: model.ModeWrite, Source: model.SignalSource("mssql_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "INSERT", ObservedAt: mustTime(t, "2026-06-03T10:23:46.2000000"),
		},
		{
			OriginKind: "identity", OriginRef: "claude-agent-7",
			ResourceKind: "mssql.table", ResourceRef: "salesdb.dbo.orders",
			Mode: model.ModeWrite, Source: model.SignalSource("mssql_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "UPDATE", ObservedAt: mustTime(t, "2026-06-03T10:23:47.0500000"),
		},
		{
			OriginKind: "identity", OriginRef: "claude-agent-7",
			ResourceKind: "mssql.table", ResourceRef: "salesdb.staging.import_2026",
			Mode: model.ModeWrite, Source: model.SignalSource("mssql_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "DELETE", ObservedAt: mustTime(t, "2026-06-03T10:23:48.0100000"),
		},
		{
			// EXECUTE -> unknown (the audit does not say what the procedure does).
			// etl_pool is declared shared -> approximate. class_type "P" -> mssql.object.
			OriginKind: "identity", OriginRef: "etl_pool",
			ResourceKind: "mssql.object", ResourceRef: "salesdb.dbo.usp_RebuildIndexes",
			Mode: model.ModeUnknown, Source: model.SignalSource("mssql_audit"), Confidence: model.ConfidenceApproximate,
			ToolRef: "EXECUTE", ObservedAt: mustTime(t, "2026-06-03T10:23:49.5000000"),
		},
		{
			// action_name fallback (no action_id); class_type "V" -> mssql.object.
			OriginKind: "identity", OriginRef: "claude-agent-7",
			ResourceKind: "mssql.object", ResourceRef: "salesdb.reporting.v_daily_totals",
			Mode: model.ModeRead, Source: model.SignalSource("mssql_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "SELECT", ObservedAt: mustTime(t, "2026-06-03T10:23:50.0000000"),
		},
	}

	s := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "audit.ndjson"),
		"shared_accounts": "etl_pool",
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

// TestExecuteIsUnknown proves the EXECUTE row maps to ModeUnknown — the connector
// never fakes what a procedure does (ARCHITECTURE.md).
func TestExecuteIsUnknown(t *testing.T) {
	s := openSource(t, map[string]string{"path": filepath.Join("testdata", "audit.ndjson"), "follow": "false"})
	var found bool
	for _, e := range gatherBatch(t, s) {
		if e.ToolRef == "EXECUTE" {
			found = true
			if e.Mode != model.ModeUnknown {
				t.Errorf("EXECUTE edge Mode = %q, want unknown", e.Mode)
			}
		}
	}
	if !found {
		t.Fatal("no EXECUTE edge emitted by fixture")
	}
}

// TestNoRawSQLEmitted proves the connector never leaks the T-SQL statement into
// any emitted edge field (docs/SECURITY-HARDENING.md, minimal data). The fixture's `statement`
// column carries real SQL fragments; none may appear in an edge.
func TestNoRawSQLEmitted(t *testing.T) {
	s := openSource(t, map[string]string{"path": filepath.Join("testdata", "audit.ndjson"), "follow": "false"})
	forbidden := []string{
		"SELECT c.id", "FROM dbo.customers", "INSERT INTO dbo.orders", "VALUES",
		"'1,299.00'", "total * 1.1", "EXEC dbo.usp_RebuildIndexes", "@table",
		"DELETE FROM staging", "RECEIVE TOP", "WHERE", "c.region",
	}
	for _, e := range gatherBatch(t, s) {
		for _, field := range []string{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef, string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef} {
			for _, frag := range forbidden {
				if strings.Contains(field, frag) {
					t.Errorf("edge field %q leaked SQL fragment %q", field, frag)
				}
			}
		}
	}
}

// TestSharedAccountFlipsConfidence verifies the shared-service-account handling:
// declaring an identity shared flips its edges to approximate (the raw identity is
// still emitted), and removing the declaration restores attributed confidence.
func TestSharedAccountFlipsConfidence(t *testing.T) {
	// claude-agent-7 declared shared -> all its accesses are approximate; the raw
	// login is still emitted.
	sShared := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "audit.ndjson"),
		"shared_accounts": "claude-agent-7",
		"follow":          "false",
	})
	for _, e := range gatherBatch(t, sShared) {
		if e.OriginRef == "claude-agent-7" && e.Confidence != model.ConfidenceApproximate {
			t.Errorf("shared login edge confidence = %q, want approximate", e.Confidence)
		}
	}

	// No shared accounts -> claude-agent-7 is attributed, etl_pool is attributed.
	sOpen := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "audit.ndjson"),
		"follow": "false",
	})
	got := gatherBatch(t, sOpen)
	for _, e := range got {
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("without shared_accounts, edge %q->%q confidence = %q, want attributed", e.OriginRef, e.ResourceRef, e.Confidence)
		}
	}
	// And the etl_pool EXECUTE edge keeps its raw identity.
	var sawEtl bool
	for _, e := range got {
		if e.OriginRef == "etl_pool" {
			sawEtl = true
		}
	}
	if !sawEtl {
		t.Error("etl_pool edge missing")
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := mssqlaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
	if err := mssqlaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := mssqlaudit.New().Descriptor()
	if d.Name != "olivares.mssql-audit" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
}

// TestNDJSONFollow exercises the streaming path: an NDJSON audit export that grows
// while the connector follows it, then a context cancel that stops Gather cleanly.
func TestNDJSONFollow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.ndjson")
	line := `{"event_time":"2026-06-03T10:23:45.1230000","action_id":"SL","succeeded":true,"server_principal_name":"claude-agent-7","database_name":"salesdb","schema_name":"dbo","object_name":"customers","class_type":"U","statement":"SELECT 1"}` + "\n"
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
	line2 := `{"event_time":"2026-06-03T10:23:46.2000000","action_id":"IN","succeeded":true,"server_principal_name":"etl_pool","database_name":"salesdb","schema_name":"dbo","object_name":"orders","class_type":"U","statement":"INSERT INTO dbo.orders DEFAULT VALUES"}` + "\n"
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
	if edges[0].ResourceRef != "salesdb.dbo.customers" || edges[1].ResourceRef != "salesdb.dbo.orders" {
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
