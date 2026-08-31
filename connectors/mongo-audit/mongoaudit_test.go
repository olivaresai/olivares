// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mongoaudit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mongoaudit "github.com/olivaresai/olivares/connectors/mongo-audit"
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

func openSource(t *testing.T, settings map[string]string) *mongoaudit.Source {
	t.Helper()
	s := mongoaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *mongoaudit.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
}

// TestGatherGoldenEdges asserts every emitted field, including ObservedAt, against
// the fixture. It also proves the four skip rules: a denied authCheck (result!=0),
// a non-authCheck atype, and a userless record are dropped; an unclassified command
// is emitted with Mode=unknown (it is still a real access to the namespace).
func TestGatherGoldenEdges(t *testing.T) {
	want := []model.EdgeObservation{
		{
			OriginKind: "identity", OriginRef: "claude_agent_7@admin",
			ResourceKind: "mongo.collection", ResourceRef: "salesdb.customers",
			Mode: model.ModeRead, Source: mongoaudit.SignalMongoAudit, Confidence: model.ConfidenceAttributed,
			ToolRef: "find", ObservedAt: mustTime(t, "2026-06-03T10:23:45.806Z"),
		},
		{
			OriginKind: "identity", OriginRef: "claude_agent_7@admin",
			ResourceKind: "mongo.collection", ResourceRef: "salesdb.orders",
			Mode: model.ModeWrite, Source: mongoaudit.SignalMongoAudit, Confidence: model.ConfidenceAttributed,
			ToolRef: "insert", ObservedAt: mustTime(t, "2026-06-03T10:23:46.200Z"),
		},
		{
			OriginKind: "identity", OriginRef: "etl_pool@admin",
			ResourceKind: "mongo.collection", ResourceRef: "salesdb.events",
			Mode: model.ModeRead, Source: mongoaudit.SignalMongoAudit, Confidence: model.ConfidenceApproximate,
			ToolRef: "aggregate", ObservedAt: mustTime(t, "2026-06-03T10:23:47.050Z"),
		},
		{
			OriginKind: "identity", OriginRef: "etl_pool@admin",
			ResourceKind: "mongo.database", ResourceRef: "salesdb",
			Mode: model.ModeRead, Source: mongoaudit.SignalMongoAudit, Confidence: model.ConfidenceApproximate,
			ToolRef: "listCollections", ObservedAt: mustTime(t, "2026-06-03T10:23:48.000Z"),
		},
		{
			// admin.$cmd is a full "db.collection" namespace, so the resource is a
			// collection per the verbatim rule; the command replSetGetStatus is not in
			// MongoDB's known R/RW set, so Mode is explicitly unknown (never guessed).
			OriginKind: "identity", OriginRef: "claude_agent_7@admin",
			ResourceKind: "mongo.collection", ResourceRef: "admin.$cmd",
			Mode: model.ModeUnknown, Source: mongoaudit.SignalMongoAudit, Confidence: model.ConfidenceAttributed,
			ToolRef: "replSetGetStatus", ObservedAt: mustTime(t, "2026-06-03T10:23:49.500Z"),
		},
	}

	s := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "audit.json"),
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

// TestNoRawLeak proves the connector never leaks any command body fragment
// (param.args: filters, documents, secrets, PII) into any emitted edge field
// (docs/SECURITY-HARDENING.md, minimal data). These fragments all live in the fixture's args.
func TestNoRawLeak(t *testing.T) {
	s := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "audit.json"),
		"follow": "false",
	})
	forbidden := []string{
		"filter", "$gt", "1800", "documents", "A-100", "secret_total", "4242",
		"pipeline", "$match", "hidden_token", "s3cr3t", "ssn", "123-45-6789",
		"SCRAM-SHA-256", "mechanism",
	}
	for _, e := range gatherBatch(t, s) {
		for _, field := range []string{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef, string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef} {
			for _, frag := range forbidden {
				if strings.Contains(field, frag) {
					t.Errorf("edge field %q leaked command-body fragment %q", field, frag)
				}
			}
		}
	}
}

// TestSharedAccountFlipsConfidence verifies the shared-service-account handling: a
// declared shared identity (by bare "user" or by "user@db") drops confidence to
// approximate; removing the declaration restores attributed. The raw identity is
// emitted in both cases — only the trust changes.
func TestSharedAccountFlipsConfidence(t *testing.T) {
	// Declare claude_agent_7 shared by bare user, etl_pool shared too.
	sShared := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "audit.json"),
		"shared_accounts": "claude_agent_7, etl_pool@admin",
		"follow":          "false",
	})
	for i, e := range gatherBatch(t, sShared) {
		if e.Confidence != model.ConfidenceApproximate {
			t.Errorf("edge[%d] (%s) confidence = %q, want approximate", i, e.OriginRef, e.Confidence)
		}
		// Raw identity must still be present.
		if e.OriginRef == "" {
			t.Errorf("edge[%d] OriginRef empty — raw identity must always be emitted", i)
		}
	}

	// No shared accounts -> every identity is attributed.
	sOpen := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "audit.json"),
		"follow": "false",
	})
	for i, e := range gatherBatch(t, sOpen) {
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("edge[%d] (%s) confidence = %q, want attributed", i, e.OriginRef, e.Confidence)
		}
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := mongoaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
	if err := mongoaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := mongoaudit.New().Descriptor()
	if d.Name != "olivares.mongo-audit" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
}

// TestFollow exercises the streaming path: a JSON audit log that grows while the
// connector follows it, then a context cancel that stops Gather cleanly.
func TestFollow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mongo-audit.json")
	line := `{"atype":"authCheck","ts":{"$date":"2026-06-03T10:23:45.806Z"},"users":[{"user":"claude_agent_7","db":"admin"}],"roles":[],"param":{"command":"find","ns":"salesdb.customers","args":{"find":"customers"}},"result":0}` + "\n"
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
	line2 := `{"atype":"authCheck","ts":{"$date":"2026-06-03T10:23:46.000Z"},"users":[{"user":"etl_pool","db":"admin"}],"roles":[],"param":{"command":"update","ns":"salesdb.orders","args":{"update":"orders"}},"result":0}` + "\n"
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
	if edges[0].ResourceRef != "salesdb.customers" || edges[0].Mode != model.ModeRead {
		t.Errorf("followed edge[0] = {%q,%q}", edges[0].ResourceRef, edges[0].Mode)
	}
	if edges[1].ResourceRef != "salesdb.orders" || edges[1].Mode != model.ModeWrite {
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
