// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcsaudit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gcsaudit "github.com/olivaresai/olivares/connectors/gcs-audit"
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

func openSource(t *testing.T, settings map[string]string) *gcsaudit.Source {
	t.Helper()
	s := gcsaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *gcsaudit.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
}

const signalGCS = model.SignalSource("gcs_audit")

// TestGatherGoldenEdges asserts every emitted field for the fixture, including
// ObservedAt, the verbatim method->mode mapping (read/write/unknown), gs:// refs,
// and that the non-storage (BigQuery) record is skipped.
func TestGatherGoldenEdges(t *testing.T) {
	want := []model.EdgeObservation{
		{
			OriginKind: "identity", OriginRef: "claude-agent@my-project.iam.gserviceaccount.com",
			ResourceKind: "gcs.object", ResourceRef: "gs://sales-data/reports/q2-2026.csv",
			Mode: model.ModeRead, Source: signalGCS, Confidence: model.ConfidenceAttributed,
			ToolRef: "storage.objects.get", ObservedAt: mustTime(t, "2026-06-03T10:23:45.123Z"),
		},
		{
			OriginKind: "identity", OriginRef: "claude-agent@my-project.iam.gserviceaccount.com",
			ResourceKind: "gcs.object", ResourceRef: "gs://sales-data/uploads/secret-token-abc123.bin",
			Mode: model.ModeWrite, Source: signalGCS, Confidence: model.ConfidenceAttributed,
			ToolRef: "storage.objects.create", ObservedAt: mustTime(t, "2026-06-03T10:23:46.200Z"),
		},
		{
			OriginKind: "identity", OriginRef: "etl-pool@my-project.iam.gserviceaccount.com",
			ResourceKind: "gcs.object", ResourceRef: "gs://sales-data/uploads/old-export.csv",
			Mode: model.ModeWrite, Source: signalGCS, Confidence: model.ConfidenceApproximate,
			ToolRef: "storage.objects.delete", ObservedAt: mustTime(t, "2026-06-03T10:23:47.050Z"),
		},
		{
			OriginKind: "identity", OriginRef: "etl-pool@my-project.iam.gserviceaccount.com",
			ResourceKind: "gcs.object", ResourceRef: "gs://sales-data/reports/q2-2026.csv",
			Mode: model.ModeWrite, Source: signalGCS, Confidence: model.ConfidenceApproximate,
			ToolRef: "storage.objects.update", ObservedAt: mustTime(t, "2026-06-03T10:23:48.000Z"),
		},
		{
			OriginKind: "identity", OriginRef: "auditor@example.com",
			ResourceKind: "gcs.bucket", ResourceRef: "gs://sales-data",
			Mode: model.ModeRead, Source: signalGCS, Confidence: model.ConfidenceAttributed,
			ToolRef: "storage.buckets.list", ObservedAt: mustTime(t, "2026-06-03T10:23:49.000Z"),
		},
		{
			OriginKind: "identity", OriginRef: "auditor@example.com",
			ResourceKind: "gcs.object", ResourceRef: "gs://sales-data/reports/q2-2026.csv",
			Mode: model.ModeUnknown, Source: signalGCS, Confidence: model.ConfidenceAttributed,
			ToolRef: "storage.objects.getIamPolicy", ObservedAt: mustTime(t, "2026-06-03T10:23:50.000Z"),
		},
	}

	s := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "gcs_audit.ndjson"),
		"shared_accounts": "etl-pool@my-project.iam.gserviceaccount.com",
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

// TestUnknownMode proves an unmapped methodName is emitted as an access with
// Mode=unknown (explicit, not guessed) rather than dropped or coerced to read.
func TestUnknownMode(t *testing.T) {
	s := openSource(t, map[string]string{"path": filepath.Join("testdata", "gcs_audit.ndjson")})
	for _, e := range gatherBatch(t, s) {
		if e.ToolRef == "storage.objects.getIamPolicy" {
			if e.Mode != model.ModeUnknown {
				t.Fatalf("getIamPolicy mode = %q, want unknown", e.Mode)
			}
			return
		}
	}
	t.Fatal("expected an edge for the unmapped getIamPolicy method")
}

// TestNoRawLeak proves the connector never leaks any request/response/payload
// fragment from the fixture into an emitted edge field (docs/SECURITY-HARDENING.md, minimal data).
func TestNoRawLeak(t *testing.T) {
	s := openSource(t, map[string]string{"path": filepath.Join("testdata", "gcs_audit.ndjson")})
	forbidden := []string{
		"google.storage.objects.get", "google.storage.objects.insert",
		"application/octet-stream", "d41d8cd98f00b204e9800998ecf8427e",
		"203.0.113.7", "callerIp", "generation", "1717410225", "contentType",
		"google.cloud.audit.AuditLog", "bigquery", "InsertJob",
	}
	for _, e := range gatherBatch(t, s) {
		fields := []string{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef,
			string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef}
		for _, f := range fields {
			for _, frag := range forbidden {
				if strings.Contains(f, frag) {
					t.Errorf("edge field %q leaked forbidden fragment %q", f, frag)
				}
			}
		}
	}
}

// TestSharedAccountFlipsConfidence verifies a declared shared principal flips its
// edges to approximate, and removing the declaration restores attributed (the raw
// principal is emitted unchanged in both cases).
func TestSharedAccountFlipsConfidence(t *testing.T) {
	const etl = "etl-pool@my-project.iam.gserviceaccount.com"

	sShared := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "gcs_audit.ndjson"),
		"shared_accounts": etl,
	})
	for _, e := range gatherBatch(t, sShared) {
		if e.OriginRef == etl && e.Confidence != model.ConfidenceApproximate {
			t.Errorf("shared principal %q confidence = %q, want approximate", etl, e.Confidence)
		}
		if e.OriginRef != etl && e.Confidence != model.ConfidenceAttributed {
			t.Errorf("non-shared principal %q confidence = %q, want attributed", e.OriginRef, e.Confidence)
		}
	}

	sOpen := openSource(t, map[string]string{"path": filepath.Join("testdata", "gcs_audit.ndjson")})
	var sawETL bool
	for _, e := range gatherBatch(t, sOpen) {
		if e.OriginRef == etl {
			sawETL = true
			if e.Confidence != model.ConfidenceAttributed {
				t.Errorf("without shared_accounts, %q confidence = %q, want attributed", etl, e.Confidence)
			}
		}
	}
	if !sawETL {
		t.Fatal("expected at least one etl-pool edge")
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := gcsaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
	if err := gcsaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "  "}}); err == nil {
		t.Error("blank path should be an error")
	}
	if err := gcsaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := gcsaudit.New().Descriptor()
	if d.Name != "olivares.gcs-audit" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
}

// TestFollow exercises the streaming path: a file that grows while the connector
// follows it, then a context cancel that stops Gather cleanly.
func TestFollow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gcs.ndjson")
	line := `{"timestamp":"2026-06-03T10:23:45.123Z","protoPayload":{"serviceName":"storage.googleapis.com","methodName":"storage.objects.get","resourceName":"projects/_/buckets/b/objects/a.txt","authenticationInfo":{"principalEmail":"p@example.com"}}}` + "\n"
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
	line2 := `{"timestamp":"2026-06-03T10:23:46.000Z","protoPayload":{"serviceName":"storage.googleapis.com","methodName":"storage.objects.create","resourceName":"projects/_/buckets/b/objects/c.txt","authenticationInfo":{"principalEmail":"p@example.com"}}}` + "\n"
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
	if edges[0].ResourceRef != "gs://b/a.txt" || edges[1].ResourceRef != "gs://b/c.txt" {
		t.Errorf("followed edges = %q, %q", edges[0].ResourceRef, edges[1].ResourceRef)
	}
	if edges[0].Mode != model.ModeRead || edges[1].Mode != model.ModeWrite {
		t.Errorf("followed modes = %q, %q, want read, write", edges[0].Mode, edges[1].Mode)
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
