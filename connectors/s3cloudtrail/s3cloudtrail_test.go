// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3cloudtrail_test

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/s3cloudtrail"
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

func openSource(t *testing.T, settings map[string]string) *s3cloudtrail.Source {
	t.Helper()
	s := s3cloudtrail.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gather(t *testing.T, s *s3cloudtrail.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.edges
}

func rfc(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
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

func goldenCloudTrail(t *testing.T) []model.EdgeObservation {
	return []model.EdgeObservation{
		{OriginKind: "identity", OriginRef: "arn:aws:iam::123456789012:user/analyst", ResourceKind: "s3.object", ResourceRef: "arn:aws:s3:::sales-data/reports/q2.csv", Mode: model.ModeRead, Source: model.SignalCloudTrail, Confidence: model.ConfidenceAttributed, ToolRef: "GetObject", ObservedAt: rfc(t, "2026-06-03T10:23:45Z")},
		{OriginKind: "identity", OriginRef: "arn:aws:sts::123456789012:assumed-role/AppRole/claude-agent-7", ResourceKind: "s3.object", ResourceRef: "arn:aws:s3:::sales-data/uploads/new.csv", Mode: model.ModeWrite, Source: model.SignalCloudTrail, Confidence: model.ConfidenceApproximate, ToolRef: "PutObject", ObservedAt: rfc(t, "2026-06-03T10:23:46Z")},
		{OriginKind: "identity", OriginRef: "arn:aws:sts::123456789012:assumed-role/CleanupRole/batch-9", ResourceKind: "s3.object", ResourceRef: "arn:aws:s3:::sales-data/tmp/old.csv", Mode: model.ModeWrite, Source: model.SignalCloudTrail, Confidence: model.ConfidenceAttributed, ToolRef: "DeleteObject", ObservedAt: rfc(t, "2026-06-03T10:23:47Z")},
		{OriginKind: "identity", OriginRef: "arn:aws:iam::123456789012:user/analyst", ResourceKind: "s3.bucket", ResourceRef: "arn:aws:s3:::sales-data", Mode: model.ModeRead, Source: model.SignalCloudTrail, Confidence: model.ConfidenceAttributed, ToolRef: "ListBucket", ObservedAt: rfc(t, "2026-06-03T10:23:48Z")},
		{OriginKind: "identity", OriginRef: "replication.s3.amazonaws.com", ResourceKind: "s3.object", ResourceRef: "arn:aws:s3:::sales-data-replica/reports/q2.csv", Mode: model.ModeWrite, Source: model.SignalCloudTrail, Confidence: model.ConfidenceApproximate, ToolRef: "PutObject", ObservedAt: rfc(t, "2026-06-03T10:23:49Z")},
	}
}

func TestGatherGoldenEdges(t *testing.T) {
	s := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "cloudtrail.json"),
		"shared_accounts": "arn:aws:iam::123456789012:role/AppRole",
	})
	assertEdgesEqual(t, gather(t, s), goldenCloudTrail(t))
}

func TestNDJSON(t *testing.T) {
	s := openSource(t, map[string]string{"path": filepath.Join("testdata", "events.ndjson.json")})
	got := gather(t, s)
	if len(got) != 2 {
		t.Fatalf("got %d edges, want 2", len(got))
	}
	if got[0].Mode != model.ModeRead || got[1].Mode != model.ModeWrite {
		t.Errorf("modes = %q, %q", got[0].Mode, got[1].Mode)
	}
	if got[0].OriginRef != "arn:aws:iam::123456789012:user/ci" {
		t.Errorf("origin = %q", got[0].OriginRef)
	}
}

func TestDirectory(t *testing.T) {
	s := openSource(t, map[string]string{
		"path":            "testdata",
		"shared_accounts": "arn:aws:iam::123456789012:role/AppRole",
	})
	got := gather(t, s)
	// Sorted filenames: cloudtrail.json (5) then events.ndjson.json (2).
	if len(got) != 7 {
		t.Fatalf("got %d edges, want 7", len(got))
	}
	if got[5].OriginRef != "arn:aws:iam::123456789012:user/ci" {
		t.Errorf("edge[5] origin = %q, want the ci NDJSON record", got[5].OriginRef)
	}
}

func TestGzip(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "cloudtrail.json"))
	if err != nil {
		t.Fatal(err)
	}
	gzPath := filepath.Join(t.TempDir(), "cloudtrail.json.gz")
	f, err := os.Create(gzPath)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	if _, err := gw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	s := openSource(t, map[string]string{"path": gzPath, "shared_accounts": "arn:aws:iam::123456789012:role/AppRole"})
	assertEdgesEqual(t, gather(t, s), goldenCloudTrail(t))
}

func TestOpenValidation(t *testing.T) {
	if err := s3cloudtrail.New().Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
}

func TestDescriptor(t *testing.T) {
	d := s3cloudtrail.New().Descriptor()
	if d.Name != "olivares.s3-cloudtrail" || d.Type != sdk.TypeSource {
		t.Errorf("descriptor = %+v", d)
	}
}
