// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pgaudit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/pgaudit"
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
	tt, err := time.Parse("2006-01-02 15:04:05.999 MST", s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tt.UTC()
}

func openSource(t *testing.T, settings map[string]string) *pgaudit.Source {
	t.Helper()
	s := pgaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *pgaudit.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
}

func TestGatherGoldenEdges(t *testing.T) {
	want := []model.EdgeObservation{
		{
			OriginKind: "identity", OriginRef: "claude-agent-7",
			ResourceKind: "postgres.table", ResourceRef: "salesdb.public.customers",
			Mode: model.ModeRead, Source: model.SignalPGAudit, Confidence: model.ConfidenceAttributed,
			ToolRef: "SELECT", ObservedAt: mustTime(t, "2026-06-03 10:23:45.123 UTC"),
		},
		{
			OriginKind: "identity", OriginRef: "claude-agent-7",
			ResourceKind: "postgres.table", ResourceRef: "salesdb.public.orders",
			Mode: model.ModeWrite, Source: model.SignalPGAudit, Confidence: model.ConfidenceAttributed,
			ToolRef: "INSERT", ObservedAt: mustTime(t, "2026-06-03 10:23:46.200 UTC"),
		},
		{
			OriginKind: "identity", OriginRef: "etl_pool",
			ResourceKind: "postgres.table", ResourceRef: "salesdb.public.staging_2026",
			Mode: model.ModeWrite, Source: model.SignalPGAudit, Confidence: model.ConfidenceApproximate,
			ToolRef: "CREATE TABLE", ObservedAt: mustTime(t, "2026-06-03 10:23:47.050 UTC"),
		},
	}

	for _, f := range []struct{ name, file string }{
		{"csvlog", "audit.csvlog"},
		{"jsonlog", "audit.jsonlog"},
	} {
		t.Run(f.name, func(t *testing.T) {
			s := openSource(t, map[string]string{
				"log_path":        filepath.Join("testdata", f.file),
				"format":          f.name,
				"shared_accounts": "etl_pool",
				"follow":          "false",
			})
			got := gatherBatch(t, s)
			assertEdgesEqual(t, got, want)
		})
	}
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

// TestNoRawSQLEmitted proves the connector never leaks the SQL body into any
// emitted edge field (docs/SECURITY-HARDENING.md, minimal data).
func TestNoRawSQLEmitted(t *testing.T) {
	s := openSource(t, map[string]string{
		"log_path": filepath.Join("testdata", "audit.csvlog"),
		"format":   "csvlog",
	})
	forbidden := []string{
		"SELECT c.id", "FROM customers", "INSERT INTO orders", "VALUES",
		"'x,y'", "LIKE orders", "search_path", "<not logged>",
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
// a shared application_name is not trusted as a per-agent identity (falls back
// to the role and approximate), and removing the shared declaration restores
// attributed confidence.
func TestSharedAccountFlipsConfidence(t *testing.T) {
	// claude-agent-7 declared shared -> its accesses fall back to the role and
	// are approximate; etl_pool also shared.
	sShared := openSource(t, map[string]string{
		"log_path":        filepath.Join("testdata", "audit.csvlog"),
		"shared_accounts": "claude-agent-7, etl_pool",
	})
	got := gatherBatch(t, sShared)
	if len(got) != 3 {
		t.Fatalf("got %d edges, want 3", len(got))
	}
	for i, e := range got {
		if e.Confidence != model.ConfidenceApproximate {
			t.Errorf("edge[%d] confidence = %q, want approximate", i, e.Confidence)
		}
	}
	// With claude-agent-7 shared, the app_name is not used; the role is.
	if got[0].OriginRef != "claude_rw" {
		t.Errorf("edge[0] OriginRef = %q, want claude_rw (shared app_name must fall back to role)", got[0].OriginRef)
	}

	// No shared accounts -> etl_pool is attributed.
	sOpen := openSource(t, map[string]string{
		"log_path": filepath.Join("testdata", "audit.csvlog"),
	})
	got2 := gatherBatch(t, sOpen)
	if got2[2].OriginRef != "etl_pool" || got2[2].Confidence != model.ConfidenceAttributed {
		t.Errorf("without shared_accounts, etl_pool edge = {%q,%q}, want {etl_pool, attributed}", got2[2].OriginRef, got2[2].Confidence)
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := pgaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing log_path should be an error")
	}
	if err := pgaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"log_path": "x", "format": "xml"}}); err == nil {
		t.Error("unsupported format should be an error")
	}
	if err := pgaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"log_path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := pgaudit.New().Descriptor()
	if d.Name != "olivares.pg-audit" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
}

// TestJSONLogFollow exercises the streaming path: a jsonlog file that grows while
// the connector follows it, then a context cancel that stops Gather cleanly.
func TestJSONLogFollow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pg.jsonlog")
	line := `{"timestamp":"2026-06-03 10:23:45.123 UTC","user":"claude_rw","dbname":"salesdb","application_name":"claude-agent-7","message":"AUDIT: SESSION,1,1,READ,SELECT,TABLE,public.customers,SELECT 1,1"}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openSource(t, map[string]string{"log_path": p, "format": "jsonlog", "follow": "true"})

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
	line2 := `{"timestamp":"2026-06-03 10:23:46.000 UTC","user":"etl_pool","dbname":"salesdb","application_name":"","message":"AUDIT: SESSION,2,1,WRITE,INSERT,TABLE,public.orders,INSERT INTO orders DEFAULT VALUES,1"}` + "\n"
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
	if edges[0].ResourceRef != "salesdb.public.customers" || edges[1].ResourceRef != "salesdb.public.orders" {
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
