// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mysqlaudit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/mysqlaudit"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

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

func openSource(t *testing.T, settings map[string]string) *mysqlaudit.Source {
	t.Helper()
	s := mysqlaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *mysqlaudit.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	// follow=false so the batch terminates at EOF.
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.edges
}

func mariaTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.ParseInLocation("20060102 15:04:05", s, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return tt.UTC()
}

func rfcTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return tt.UTC()
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

func TestMariaDBAuditGoldenEdges(t *testing.T) {
	s := openSource(t, map[string]string{
		"log_path":        filepath.Join("testdata", "server_audit.log"),
		"format":          "mariadb_audit",
		"follow":          "false",
		"shared_accounts": "etl",
	})
	want := []model.EdgeObservation{
		{OriginKind: "identity", OriginRef: "app_rw@10.0.0.5", ResourceKind: "mysql.table", ResourceRef: "salesdb.customers", Mode: model.ModeRead, Source: mysqlaudit.SignalMySQLAudit, Confidence: model.ConfidenceAttributed, ToolRef: "READ", ObservedAt: mariaTime(t, "20260603 10:23:45")},
		{OriginKind: "identity", OriginRef: "app_rw@10.0.0.5", ResourceKind: "mysql.table", ResourceRef: "salesdb.orders", Mode: model.ModeWrite, Source: mysqlaudit.SignalMySQLAudit, Confidence: model.ConfidenceAttributed, ToolRef: "WRITE", ObservedAt: mariaTime(t, "20260603 10:23:46")},
		{OriginKind: "identity", OriginRef: "etl@10.0.0.9", ResourceKind: "mysql.table", ResourceRef: "salesdb.staging", Mode: model.ModeWrite, Source: mysqlaudit.SignalMySQLAudit, Confidence: model.ConfidenceApproximate, ToolRef: "CREATE", ObservedAt: mariaTime(t, "20260603 10:23:47")},
		{OriginKind: "identity", OriginRef: "app_rw@10.0.0.5", ResourceKind: "mysql.database", ResourceRef: "salesdb", Mode: model.ModeRead, Source: mysqlaudit.SignalMySQLAudit, Confidence: model.ConfidenceAttributed, ToolRef: "SELECT", ObservedAt: mariaTime(t, "20260603 10:23:48")},
	}
	assertEdgesEqual(t, gatherBatch(t, s), want)
}

func TestGeneralLogGoldenEdges(t *testing.T) {
	s := openSource(t, map[string]string{
		"log_path": filepath.Join("testdata", "general.log"),
		"format":   "general_log",
		"follow":   "false",
	})
	want := []model.EdgeObservation{
		{OriginKind: "identity", OriginRef: "app_rw@10.0.0.5", ResourceKind: "mysql.database", ResourceRef: "salesdb", Mode: model.ModeRead, Source: mysqlaudit.SignalMySQLAudit, Confidence: model.ConfidenceAttributed, ToolRef: "SELECT", ObservedAt: rfcTime(t, "2026-06-03T10:23:45.100000Z")},
		{OriginKind: "identity", OriginRef: "app_rw@10.0.0.5", ResourceKind: "mysql.database", ResourceRef: "salesdb", Mode: model.ModeWrite, Source: mysqlaudit.SignalMySQLAudit, Confidence: model.ConfidenceAttributed, ToolRef: "UPDATE", ObservedAt: rfcTime(t, "2026-06-03T10:23:46.200000Z")},
		{OriginKind: "identity", OriginRef: "reporting@10.0.0.9", ResourceKind: "mysql.database", ResourceRef: "analytics", Mode: model.ModeRead, Source: mysqlaudit.SignalMySQLAudit, Confidence: model.ConfidenceAttributed, ToolRef: "SELECT", ObservedAt: rfcTime(t, "2026-06-03T10:23:48.000000Z")},
		{OriginKind: "identity", OriginRef: "app_rw@10.0.0.5", ResourceKind: "mysql.database", ResourceRef: "warehouse", Mode: model.ModeWrite, Source: mysqlaudit.SignalMySQLAudit, Confidence: model.ConfidenceAttributed, ToolRef: "DELETE", ObservedAt: rfcTime(t, "2026-06-03T10:23:49.500000Z")},
	}
	assertEdgesEqual(t, gatherBatch(t, s), want)
}

// TestNoRawSQLEmitted proves the SQL body never leaks into an emitted edge field.
func TestNoRawSQLEmitted(t *testing.T) {
	forbidden := []string{"id, name", "FROM customers", "SET total", "count(*)", "FROM events", "FROM staging", "WHERE id"}
	for _, tc := range []struct{ format, file string }{
		{"mariadb_audit", "server_audit.log"},
		{"general_log", "general.log"},
	} {
		s := openSource(t, map[string]string{"log_path": filepath.Join("testdata", tc.file), "format": tc.format, "follow": "false"})
		for _, e := range gatherBatch(t, s) {
			for _, field := range []string{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef, string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef} {
				for _, frag := range forbidden {
					if strings.Contains(field, frag) {
						t.Errorf("[%s] edge field %q leaked SQL fragment %q", tc.format, field, frag)
					}
				}
			}
		}
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := mysqlaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing log_path should be an error")
	}
	if err := mysqlaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"log_path": "x", "format": "binlog"}}); err == nil {
		t.Error("unsupported format should be an error")
	}
	if err := mysqlaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"log_path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := mysqlaudit.New().Descriptor()
	if d.Name != "olivares.mysql-audit" || d.Type != sdk.TypeSource {
		t.Errorf("descriptor = %+v", d)
	}
}

// TestGeneralLogConnReuseNoStaleDB is a regression test: when a connection id is
// reused by a new session without its own database, the new session must NOT
// inherit the previous session's database (a fresh Connect resets the state).
func TestGeneralLogConnReuseNoStaleDB(t *testing.T) {
	lines := strings.Join([]string{
		"2026-06-03T12:00:00.000000Z\t    7 Connect\tapp_rw@10.0.0.5 on salesdb",
		"2026-06-03T12:00:01.000000Z\t    7 Query\tSELECT 1",
		"2026-06-03T12:00:02.000000Z\t    7 Connect\treporting@10.0.0.9 on  using SSL/TLS",
		"2026-06-03T12:00:03.000000Z\t    7 Query\tSELECT 2",
	}, "\n") + "\n"
	p := filepath.Join(t.TempDir(), "g.log")
	if err := os.WriteFile(p, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openSource(t, map[string]string{"log_path": p, "format": "general_log", "follow": "false"})
	got := gatherBatch(t, s)
	// Only the first connection's SELECT (db salesdb) yields an edge; the reused
	// connection has no database, so its query is not misattributed to salesdb.
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1 (reused conn must not inherit salesdb): %+v", len(got), got)
	}
	if got[0].ResourceRef != "salesdb" || got[0].OriginRef != "app_rw@10.0.0.5" {
		t.Errorf("edge = %+v", got[0])
	}
}
