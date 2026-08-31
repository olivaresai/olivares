// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	snowflakeaudit "github.com/olivaresai/olivares/connectors/snowflake-audit"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// edgeCaptureModule is a minimal in-process Module that records every
// edge.observed event it sees on the bus. It is the test-side proof that a
// SourceConnector's emissions are elevated to the bus by the runtime.
type edgeCaptureModule struct {
	mu     sync.Mutex
	edges  []model.EdgeObservation
	cancel func()
}

func (m *edgeCaptureModule) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.edge-capture", Version: "0.0.1", APIVersion: sdk.APIVersion, Type: sdk.TypeModule}
}

func (m *edgeCaptureModule) Init(_ context.Context, host sdk.Host) error {
	cancel, err := host.Subscribe([]event.Type{event.TypeEdgeObserved}, func(_ context.Context, e event.Event) error {
		if edge, ok := event.EdgeOf(e); ok {
			m.mu.Lock()
			m.edges = append(m.edges, edge)
			m.mu.Unlock()
		}
		return nil
	})
	m.cancel = cancel
	return err
}

func (m *edgeCaptureModule) Start(context.Context) error { return nil }

func (m *edgeCaptureModule) Stop(context.Context) error {
	if m.cancel != nil {
		m.cancel()
	}
	return nil
}

func (m *edgeCaptureModule) snapshot() []model.EdgeObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.EdgeObservation(nil), m.edges...)
}

// TestDataPlatformSourceEmitsToBus is the "real bus, no seed" proof (DoD): a
// REAL data-platform connector (snowflake-audit), wired into the runtime through
// the seam (AddPollSource) and reading its REAL audit fixture, actually emits
// EdgeObservations that the runtime elevates to edge.observed events on the bus —
// not a seed source, not a unit-test sink. It exercises the full Open → Gather →
// Emit → bus path the composition root drives in production. It lives here (AGPL
// cmd/olivares), not in the connector package, because the connector must
// never import /core (the license boundary); only the composition root may.
func TestDataPlatformSourceEmitsToBus(t *testing.T) {
	ctx := context.Background()

	capture := &edgeCaptureModule{}
	rt := runtime.New(runtime.Options{Logger: quietLog()})
	if err := rt.AddModule(capture, sdk.Config{}); err != nil {
		t.Fatalf("add capture module: %v", err)
	}

	// The connector's own real ACCESS_HISTORY fixture (no seed). A batch source
	// (follow=false): Gather reads it to EOF and returns; AddPollSource with a 0
	// interval runs it once.
	fixture := filepath.Join("..", "..", "connectors", "snowflake-audit", "testdata", "access_history.ndjson")
	cfg := sdk.Config{Settings: map[string]string{"path": fixture, "follow": "false"}}
	if err := rt.AddPollSource(snowflakeaudit.New(), cfg, "tenant-ref", 0); err != nil {
		t.Fatalf("wire snowflake-audit source: %v", err)
	}

	if err := rt.Start(ctx); err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(c)
	})

	// The source's edges must reach the bus (modules subscribe before sources
	// start, so none is missed). Assert a real snowflake edge with the verbatim
	// signal arrives.
	deadline := time.After(3 * time.Second)
	for {
		for _, e := range capture.snapshot() {
			if e.Source == snowflakeaudit.SignalSnowflakeAccessHistory && strings.HasPrefix(e.ResourceKind, "snowflake.") {
				if e.OriginKind != "identity" {
					t.Errorf("edge OriginKind = %q, want identity", e.OriginKind)
				}
				if e.Mode != model.ModeRead && e.Mode != model.ModeWrite {
					t.Errorf("edge Mode = %q, want read or write", e.Mode)
				}
				return // real connector → runtime → bus, via the seam (no seed)
			}
		}
		select {
		case <-deadline:
			t.Fatalf("no snowflake edge reached the bus (captured %d edges) — source not elevated to the bus", len(capture.snapshot()))
		case <-time.After(20 * time.Millisecond):
		}
	}
}
