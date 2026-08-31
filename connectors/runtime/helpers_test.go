// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// sinkConfig builds an sdk.Config from a settings map (test convenience).
func sinkConfig(settings map[string]string) sdk.Config {
	return sdk.Config{Settings: settings}
}

// --- fakeSink ----------------------------------------------------------------

// fakeSink collects every emitted observation so tests can assert the exact set
// of edges and findings a discoverer produced. It mirrors the mcp fakeSink.
type fakeSink struct {
	mu  sync.Mutex
	obs []model.Observation
}

func (f *fakeSink) Emit(_ context.Context, o model.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obs = append(f.obs, o)
	return nil
}

func (f *fakeSink) edges() []model.EdgeObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.EdgeObservation
	for _, o := range f.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeSink) findings() []model.FindingReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.FindingReport
	for _, o := range f.obs {
		if r, ok := o.(model.FindingReport); ok {
			out = append(out, r)
		}
	}
	return out
}

// --- edge-set helpers --------------------------------------------------------

// edgeKey is a stable, comparable rendering of an edge's identity fields plus its
// invariant provenance, so golden tests assert both the topology and that every
// edge carries Mode=unknown / source=runtime / confidence=attributed.
func edgeKey(e model.EdgeObservation) string {
	return fmt.Sprintf("%s|%s -> %s|%s tool=%s mode=%s src=%s conf=%s",
		e.OriginKind, e.OriginRef, e.ResourceKind, e.ResourceRef,
		e.ToolRef, e.Mode, e.Source, e.Confidence)
}

// sortedEdgeKeys returns the sorted edgeKey set of the collected edges.
func (f *fakeSink) sortedEdgeKeys() []string {
	edges := f.edges()
	keys := make([]string, 0, len(edges))
	for _, e := range edges {
		keys = append(keys, edgeKey(e))
	}
	sort.Strings(keys)
	return keys
}
