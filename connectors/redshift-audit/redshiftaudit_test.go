// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redshiftaudit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	redshiftaudit "github.com/olivaresai/olivares/connectors/redshift-audit"
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

func openSource(t *testing.T, settings map[string]string) *redshiftaudit.Source {
	t.Helper()
	s := redshiftaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *redshiftaudit.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
}

// TestGatherGoldenEdges asserts every emitted edge field, including ObservedAt,
// over the full fixture: each verb-class (read/write/DDL/DCL), the by-verb
// unknown case (VACUUM), and the shared etl_pool user (approximate).
func TestGatherGoldenEdges(t *testing.T) {
	want := []model.EdgeObservation{
		{OriginKind: "identity", OriginRef: "analyst", ResourceKind: "redshift.database", ResourceRef: "salesdb",
			Mode: model.ModeRead, Source: model.SignalSource("redshift_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "SELECT", ObservedAt: mustTime(t, "2026-06-03T10:23:45Z")},
		{OriginKind: "identity", OriginRef: "analyst", ResourceKind: "redshift.database", ResourceRef: "salesdb",
			Mode: model.ModeWrite, Source: model.SignalSource("redshift_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "INSERT", ObservedAt: mustTime(t, "2026-06-03T10:23:46Z")},
		{OriginKind: "identity", OriginRef: "analyst", ResourceKind: "redshift.database", ResourceRef: "salesdb",
			Mode: model.ModeWrite, Source: model.SignalSource("redshift_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "UPDATE", ObservedAt: mustTime(t, "2026-06-03T10:23:47Z")},
		{OriginKind: "identity", OriginRef: "analyst", ResourceKind: "redshift.database", ResourceRef: "salesdb",
			Mode: model.ModeWrite, Source: model.SignalSource("redshift_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "DELETE", ObservedAt: mustTime(t, "2026-06-03T10:23:48Z")},
		{OriginKind: "identity", OriginRef: "etl_pool", ResourceKind: "redshift.database", ResourceRef: "warehouse",
			Mode: model.ModeWrite, Source: model.SignalSource("redshift_audit"), Confidence: model.ConfidenceApproximate,
			ToolRef: "COPY", ObservedAt: mustTime(t, "2026-06-03T10:23:49Z")},
		{OriginKind: "identity", OriginRef: "etl_pool", ResourceKind: "redshift.database", ResourceRef: "warehouse",
			Mode: model.ModeRead, Source: model.SignalSource("redshift_audit"), Confidence: model.ConfidenceApproximate,
			ToolRef: "UNLOAD", ObservedAt: mustTime(t, "2026-06-03T10:23:50Z")},
		{OriginKind: "identity", OriginRef: "analyst", ResourceKind: "redshift.database", ResourceRef: "salesdb",
			Mode: model.ModeWrite, Source: model.SignalSource("redshift_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "TRUNCATE", ObservedAt: mustTime(t, "2026-06-03T10:23:51Z")},
		{OriginKind: "identity", OriginRef: "dbadmin", ResourceKind: "redshift.database", ResourceRef: "salesdb",
			Mode: model.ModeWrite, Source: model.SignalSource("redshift_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "CREATE", ObservedAt: mustTime(t, "2026-06-03T10:23:52Z")},
		{OriginKind: "identity", OriginRef: "dbadmin", ResourceKind: "redshift.database", ResourceRef: "salesdb",
			Mode: model.ModeWrite, Source: model.SignalSource("redshift_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "GRANT", ObservedAt: mustTime(t, "2026-06-03T10:23:53Z")},
		{OriginKind: "identity", OriginRef: "dbadmin", ResourceKind: "redshift.database", ResourceRef: "salesdb",
			Mode: model.ModeUnknown, Source: model.SignalSource("redshift_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "VACUUM", ObservedAt: mustTime(t, "2026-06-03T10:23:54Z")},
		{OriginKind: "identity", OriginRef: "analyst", ResourceKind: "redshift.database", ResourceRef: "salesdb",
			Mode: model.ModeRead, Source: model.SignalSource("redshift_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "SHOW", ObservedAt: mustTime(t, "2026-06-03T10:23:55Z")},
	}

	s := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "useractivitylog.log"),
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

// TestNoRawSQLEmitted proves the connector never leaks any statement fragment
// (SQL body, column names, literals, paths) into an emitted edge field — only the
// edge (db, user, verb, mode) survives (docs/SECURITY-HARDENING.md, minimal data).
func TestNoRawSQLEmitted(t *testing.T) {
	s := openSource(t, map[string]string{
		"path": filepath.Join("testdata", "useractivitylog.log"),
	})
	forbidden := []string{
		"c.id", "c.email", "FROM customers", "WHERE", "EMEA",
		"VALUES", "INTO orders", "SET total", "search_path", "scratch",
		"s3://bucket", "IAM_ROLE", "arn:aws:iam", "audit_archive", "TIMESTAMP",
		"select * from facts", "FULL orders", "TO analyst", "pid=", "userid=", "xid=",
	}
	for _, e := range gatherBatch(t, s) {
		for _, field := range []string{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef, string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef} {
			for _, frag := range forbidden {
				if strings.Contains(field, frag) {
					t.Errorf("edge field %q leaked statement fragment %q", field, frag)
				}
			}
		}
	}
}

// TestSharedAccountFlipsConfidence verifies the shared-service-account handling:
// declaring a user shared flips its edges to approximate; removing the
// declaration restores attributed. The raw identity is emitted in both cases.
func TestSharedAccountFlipsConfidence(t *testing.T) {
	sShared := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "useractivitylog.log"),
		"shared_accounts": "etl_pool",
	})
	for _, e := range gatherBatch(t, sShared) {
		if e.OriginRef == "etl_pool" && e.Confidence != model.ConfidenceApproximate {
			t.Errorf("shared etl_pool edge confidence = %q, want approximate", e.Confidence)
		}
		if e.OriginRef == "analyst" && e.Confidence != model.ConfidenceAttributed {
			t.Errorf("non-shared analyst edge confidence = %q, want attributed", e.Confidence)
		}
	}

	sOpen := openSource(t, map[string]string{
		"path": filepath.Join("testdata", "useractivitylog.log"),
	})
	var sawPool bool
	for _, e := range gatherBatch(t, sOpen) {
		if e.OriginRef == "etl_pool" {
			sawPool = true
			if e.Confidence != model.ConfidenceAttributed {
				t.Errorf("without shared_accounts, etl_pool confidence = %q, want attributed", e.Confidence)
			}
		}
	}
	if !sawPool {
		t.Fatal("etl_pool edge not found")
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := redshiftaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
	if err := redshiftaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := redshiftaudit.New().Descriptor()
	if d.Name != "olivares.redshift-audit" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
}

// TestFollow exercises the streaming path: a log file that grows while the
// connector follows it, then a context cancel that stops Gather cleanly.
func TestFollow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "useractivitylog.log")
	line := `'2026-06-03T10:23:45Z UTC [ db=salesdb user=analyst pid=1 userid=1 xid=1 ]' LOG: SELECT 1` + "\n"
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
	line2 := `'2026-06-03T10:23:46Z UTC [ db=salesdb user=analyst pid=1 userid=1 xid=2 ]' LOG: INSERT INTO t DEFAULT VALUES` + "\n"
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
	if edges[0].Mode != model.ModeRead || edges[1].Mode != model.ModeWrite {
		t.Errorf("followed edge modes = %q, %q; want read, write", edges[0].Mode, edges[1].Mode)
	}
	if edges[1].ToolRef != "INSERT" {
		t.Errorf("followed edge[1] ToolRef = %q, want INSERT", edges[1].ToolRef)
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
