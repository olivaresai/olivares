// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package oracleaudit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	oracleaudit "github.com/olivaresai/olivares/connectors/oracle-audit"
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

// mustTime parses an Oracle UTC timestamp the way the connector does, in UTC.
func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", s, time.UTC)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tt.UTC()
}

func openSource(t *testing.T, settings map[string]string) *oracleaudit.Source {
	t.Helper()
	s := oracleaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *oracleaudit.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
}

// TestGatherGoldenEdges asserts every field of every emitted edge, including
// ObservedAt (which must come from EVENT_TIMESTAMP_UTC, not EVENT_TIMESTAMP or
// time.Now). ETL_POOL is declared shared -> approximate; everything else is
// attributed. The LOGON row (no object) is skipped. The EXECUTE and LOCK rows are
// emitted with Mode=unknown (honest, not dropped).
func TestGatherGoldenEdges(t *testing.T) {
	s := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "unified_audit_trail.ndjson"),
		"shared_accounts": "etl_pool",
		"follow":          "false",
	})
	got := gatherBatch(t, s)

	want := []model.EdgeObservation{
		{OriginKind: "identity", OriginRef: "APP_SVC", ResourceKind: "oracle.table", ResourceRef: "SALES.CUSTOMERS",
			Mode: model.ModeRead, Source: "oracle_audit", Confidence: model.ConfidenceAttributed,
			ToolRef: "SELECT", ObservedAt: mustTime(t, "2026-06-03 10:23:45.123456")},
		{OriginKind: "identity", OriginRef: "APP_SVC", ResourceKind: "oracle.table", ResourceRef: "SALES.ORDERS",
			Mode: model.ModeWrite, Source: "oracle_audit", Confidence: model.ConfidenceAttributed,
			ToolRef: "INSERT", ObservedAt: mustTime(t, "2026-06-03 10:23:46.200000")},
		{OriginKind: "identity", OriginRef: "APP_SVC", ResourceKind: "oracle.table", ResourceRef: "SALES.ACCOUNTS",
			Mode: model.ModeWrite, Source: "oracle_audit", Confidence: model.ConfidenceAttributed,
			ToolRef: "UPDATE", ObservedAt: mustTime(t, "2026-06-03 10:23:47.000000")},
		{OriginKind: "identity", OriginRef: "APP_SVC", ResourceKind: "oracle.table", ResourceRef: "SALES.SESSIONS",
			Mode: model.ModeWrite, Source: "oracle_audit", Confidence: model.ConfidenceAttributed,
			ToolRef: "DELETE", ObservedAt: mustTime(t, "2026-06-03 10:23:48.000000")},
		// A MERGE statement is audited under its underlying INSERT/UPDATE row
		// actions; ACTION_NAME='MERGE' is not a UNIFIED_AUDIT_TRAIL value.
		{OriginKind: "identity", OriginRef: "APP_SVC", ResourceKind: "oracle.table", ResourceRef: "SALES.INVENTORY",
			Mode: model.ModeWrite, Source: "oracle_audit", Confidence: model.ConfidenceAttributed,
			ToolRef: "UPDATE", ObservedAt: mustTime(t, "2026-06-03 10:23:49.000000")},
		{OriginKind: "identity", OriginRef: "ETL_POOL", ResourceKind: "oracle.table", ResourceRef: "STAGING.LOAD_2026",
			Mode: model.ModeWrite, Source: "oracle_audit", Confidence: model.ConfidenceApproximate,
			ToolRef: "CREATE TABLE", ObservedAt: mustTime(t, "2026-06-03 10:23:50.050000")},
		{OriginKind: "identity", OriginRef: "ETL_POOL", ResourceKind: "oracle.table", ResourceRef: "STAGING.LOAD_2025",
			Mode: model.ModeWrite, Source: "oracle_audit", Confidence: model.ConfidenceApproximate,
			ToolRef: "TRUNCATE TABLE", ObservedAt: mustTime(t, "2026-06-03 10:23:51.000000")},
		{OriginKind: "identity", OriginRef: "ETL_POOL", ResourceKind: "oracle.table", ResourceRef: "STAGING.LOAD_2024",
			Mode: model.ModeWrite, Source: "oracle_audit", Confidence: model.ConfidenceApproximate,
			ToolRef: "DROP TABLE", ObservedAt: mustTime(t, "2026-06-03 10:23:52.000000")},
		{OriginKind: "identity", OriginRef: "APP_SVC", ResourceKind: "oracle.table", ResourceRef: "BILLING.RUN_CHARGES",
			Mode: model.ModeUnknown, Source: "oracle_audit", Confidence: model.ConfidenceAttributed,
			ToolRef: "EXECUTE", ObservedAt: mustTime(t, "2026-06-03 10:23:53.000000")},
		{OriginKind: "identity", OriginRef: "APP_SVC", ResourceKind: "oracle.table", ResourceRef: "SALES.LEDGER",
			Mode: model.ModeUnknown, Source: "oracle_audit", Confidence: model.ConfidenceAttributed,
			ToolRef: "LOCK", ObservedAt: mustTime(t, "2026-06-03 10:23:54.000000")},
		// LOGON (no object) is skipped here.
		{OriginKind: "identity", OriginRef: "REPORTER", ResourceKind: "oracle.table", ResourceRef: "V_REVENUE",
			Mode: model.ModeRead, Source: "oracle_audit", Confidence: model.ConfidenceAttributed,
			ToolRef: "SELECT", ObservedAt: mustTime(t, "2026-06-03 10:23:56.000000")},
	}

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

// TestNoRawSQLEmitted proves the connector never leaks SQL_TEXT (or any statement
// fragment) into an emitted edge field (docs/SECURITY-HARDENING.md, minimal data). The fixture
// carries a real SQL body, including a PII column, in every row.
func TestNoRawSQLEmitted(t *testing.T) {
	s := openSource(t, map[string]string{
		"path": filepath.Join("testdata", "unified_audit_trail.ndjson"),
	})
	forbidden := []string{
		"FROM sales.customers", "c.ssn", "VALUES (42", "balance - 100",
		"WHEN MATCHED", "payload CLOB", "EXCLUSIVE MODE", "p_batch", "SELECT *",
		"DELETE FROM", "billing.run_charges",
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

// TestSharedAccountFlipsConfidence verifies that declaring a user shared flips its
// edges to approximate, and that without the declaration the same user is
// attributed. The raw identity is emitted unchanged in both cases.
func TestSharedAccountFlipsConfidence(t *testing.T) {
	sShared := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "unified_audit_trail.ndjson"),
		"shared_accounts": "APP_SVC, ETL_POOL, REPORTER",
	})
	for _, e := range gatherBatch(t, sShared) {
		if e.Confidence != model.ConfidenceApproximate {
			t.Errorf("with every emitting user shared, edge %s confidence = %q, want approximate", e.OriginRef, e.Confidence)
		}
	}

	sOpen := openSource(t, map[string]string{
		"path": filepath.Join("testdata", "unified_audit_trail.ndjson"),
	})
	for _, e := range gatherBatch(t, sOpen) {
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("without shared_accounts, edge %s confidence = %q, want attributed", e.OriginRef, e.Confidence)
		}
		if e.OriginRef == "" {
			t.Error("raw identity must always be emitted")
		}
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := oracleaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
	if err := oracleaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := oracleaudit.New().Descriptor()
	if d.Name != "olivares.oracle-audit" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
}

// TestFollow exercises the streaming path: an NDJSON export that grows while the
// connector follows it, then a context cancel that stops Gather cleanly.
func TestFollow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "uat.ndjson")
	line := `{"DBUSERNAME":"APP_SVC","ACTION_NAME":"SELECT","OBJECT_SCHEMA":"SALES","OBJECT_NAME":"CUSTOMERS","EVENT_TIMESTAMP_UTC":"2026-06-03 10:23:45.123456","SQL_TEXT":"SELECT 1"}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
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
	line2 := `{"DBUSERNAME":"APP_SVC","ACTION_NAME":"INSERT","OBJECT_SCHEMA":"SALES","OBJECT_NAME":"ORDERS","EVENT_TIMESTAMP_UTC":"2026-06-03 10:23:46.000000","SQL_TEXT":"INSERT INTO sales.orders VALUES (1)"}` + "\n"
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
	if edges[0].ResourceRef != "SALES.CUSTOMERS" || edges[1].ResourceRef != "SALES.ORDERS" {
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
