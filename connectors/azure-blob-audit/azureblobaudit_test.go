// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureblobaudit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	azureblobaudit "github.com/olivaresai/olivares/connectors/azure-blob-audit"
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

func openSource(t *testing.T, settings map[string]string) *azureblobaudit.Source {
	t.Helper()
	s := azureblobaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gatherBatch(t *testing.T, s *azureblobaudit.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.snapshot()
}

// TestGatherGoldenEdges asserts every emitted field — including ObservedAt — for
// the fixture, covering each Mode and the operationName/category mappings.
func TestGatherGoldenEdges(t *testing.T) {
	want := []model.EdgeObservation{
		{
			OriginKind: "identity", OriginRef: "app-claude-agent-7",
			ResourceKind: "azureblob.object", ResourceRef: "contoso/reports/q2/summary.parquet",
			Mode: model.ModeRead, Source: model.SignalSource("azureblob_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "GetBlob", ObservedAt: mustTime(t, "2026-06-03T10:23:45.1234567Z"),
		},
		{
			OriginKind: "identity", OriginRef: "app-claude-agent-7",
			ResourceKind: "azureblob.object", ResourceRef: "contoso/ingest/2026/raw.json",
			Mode: model.ModeWrite, Source: model.SignalSource("azureblob_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "PutBlob", ObservedAt: mustTime(t, "2026-06-03T10:23:46.2000000Z"),
		},
		{
			OriginKind: "identity", OriginRef: "etl-pool-sp",
			ResourceKind: "azureblob.object", ResourceRef: "contoso/ingest/2026/stale.json",
			Mode: model.ModeWrite, Source: model.SignalSource("azureblob_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "DeleteBlob", ObservedAt: mustTime(t, "2026-06-03T10:23:47.0500000Z"),
		},
		{
			OriginKind: "identity", OriginRef: "app-claude-agent-7",
			ResourceKind: "azureblob.container", ResourceRef: "contoso/reports",
			Mode: model.ModeRead, Source: model.SignalSource("azureblob_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "ListBlobs", ObservedAt: mustTime(t, "2026-06-03T10:23:48.3300000Z"),
		},
		{
			// Unrecognized operationName (AbortCopyBlob) -> category fallback (StorageWrite => write).
			OriginKind: "identity", OriginRef: "app-claude-agent-7",
			ResourceKind: "azureblob.object", ResourceRef: "contoso/ingest/2026/partial.json",
			Mode: model.ModeWrite, Source: model.SignalSource("azureblob_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "AbortCopyBlob", ObservedAt: mustTime(t, "2026-06-03T10:23:49.7000000Z"),
		},
		{
			// Unrecognized operationName AND unrecognized category (StorageOther) -> unknown.
			// No OAuth requester appId -> identity falls back to the auth type (AccountKey).
			OriginKind: "identity", OriginRef: "AccountKey",
			ResourceKind: "azureblob.object", ResourceRef: "contoso/diag/healthcheck.txt",
			Mode: model.ModeUnknown, Source: model.SignalSource("azureblob_audit"), Confidence: model.ConfidenceAttributed,
			ToolRef: "GetBlobServiceProperties", ObservedAt: mustTime(t, "2026-06-03T10:23:50.1000000Z"),
		},
	}

	s := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "storagebloblogs.json"),
		"follow": "false",
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

// TestNoRawLeak proves no SAS token, token hash, query string, URL or header
// fragment from the fixture ever appears in any emitted edge field (docs/SECURITY-HARDENING.md).
func TestNoRawLeak(t *testing.T) {
	s := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "storagebloblogs.json"),
		"follow": "false",
	})
	forbidden := []string{
		"SECRETSASTOKEN123", "ANOTHERSECRET", "LISTSECRET",
		"sig=", "blockid=", "comp=list", "timeout=30",
		"B3CC9D5C64B3351573D806751312317FE4E910877E7CBAFA9D95E0BE923DD25C",
		"AA11BB22CC33DD44EE55FF6677889900",
		"key1(", "azsdk-python", "tokenHash", "https://",
	}
	for _, e := range gatherBatch(t, s) {
		fields := []string{e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef,
			string(e.Mode), string(e.Source), string(e.Confidence), e.ToolRef}
		for _, field := range fields {
			for _, frag := range forbidden {
				if strings.Contains(field, frag) {
					t.Errorf("edge field %q leaked forbidden fragment %q", field, frag)
				}
			}
		}
	}
}

// TestSharedAccountFlipsConfidence verifies the shared-service-account handling:
// declaring the requester app id shared drops its accesses to approximate while
// the raw identity is still emitted; removing the declaration restores attributed.
func TestSharedAccountFlipsConfidence(t *testing.T) {
	sShared := openSource(t, map[string]string{
		"path":            filepath.Join("testdata", "storagebloblogs.json"),
		"follow":          "false",
		"shared_accounts": "app-claude-agent-7, etl-pool-sp",
	})
	for i, e := range gatherBatch(t, sShared) {
		// Every fixture record is attributed to one of the two shared app ids
		// EXCEPT the AccountKey record, whose origin is the auth type.
		if e.OriginRef == "AccountKey" {
			if e.Confidence != model.ConfidenceAttributed {
				t.Errorf("edge[%d] AccountKey confidence = %q, want attributed (not declared shared)", i, e.Confidence)
			}
			continue
		}
		if e.Confidence != model.ConfidenceApproximate {
			t.Errorf("edge[%d] (%s) confidence = %q, want approximate", i, e.OriginRef, e.Confidence)
		}
		if e.OriginRef == "" {
			t.Errorf("edge[%d] raw identity must still be emitted when shared", i)
		}
	}

	// No shared declaration -> the same requesters are attributed.
	sOpen := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "storagebloblogs.json"),
		"follow": "false",
	})
	for i, e := range gatherBatch(t, sOpen) {
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("edge[%d] (%s) confidence = %q, want attributed without shared_accounts", i, e.OriginRef, e.Confidence)
		}
	}
}

// TestUnknownMode asserts the explicit unknown path: an operationName and a
// category neither of which the source classifies yields Mode=unknown rather
// than a guess (ARCHITECTURE.md).
func TestUnknownMode(t *testing.T) {
	s := openSource(t, map[string]string{
		"path":   filepath.Join("testdata", "storagebloblogs.json"),
		"follow": "false",
	})
	var found bool
	for _, e := range gatherBatch(t, s) {
		if e.ToolRef == "GetBlobServiceProperties" {
			found = true
			if e.Mode != model.ModeUnknown {
				t.Errorf("GetBlobServiceProperties (StorageOther) Mode = %q, want unknown", e.Mode)
			}
		}
	}
	if !found {
		t.Fatal("expected the unknown-mode record in the fixture")
	}
}

func TestOpenValidation(t *testing.T) {
	ctx := context.Background()
	if err := azureblobaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Error("missing path should be an error")
	}
	if err := azureblobaudit.New().Open(ctx, sdk.Config{Settings: map[string]string{"path": "x"}}); err != nil {
		t.Errorf("valid config should open: %v", err)
	}
}

func TestDescriptor(t *testing.T) {
	d := azureblobaudit.New().Descriptor()
	if d.Name != "olivares.azure-blob-audit" {
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
	p := filepath.Join(dir, "blob.json")
	line := `{"time":"2026-06-03T10:23:45.1234567Z","category":"StorageRead","operationName":"GetBlob","uri":"https://contoso.blob.core.windows.net/reports/q2/summary.parquet?sig=SECRET","identity":{"type":"OAuth","requester":{"appId":"app-claude-agent-7"}},"properties":{"accountName":"contoso"}}` + "\n"
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
	line2 := `{"time":"2026-06-03T10:23:46.2000000Z","category":"StorageWrite","operationName":"PutBlob","uri":"https://contoso.blob.core.windows.net/ingest/2026/raw.json","identity":{"type":"OAuth","requester":{"appId":"app-claude-agent-7"}},"properties":{"accountName":"contoso"}}` + "\n"
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
	if edges[0].ResourceRef != "contoso/reports/q2/summary.parquet" || edges[1].ResourceRef != "contoso/ingest/2026/raw.json" {
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
