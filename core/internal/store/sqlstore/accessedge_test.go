// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func ts() model.Timestamp { return model.NewTimestamp(time.Now()) }

func TestAccessEdgeUpsertMerge(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")

	origin := model.NewID()
	resource := model.NewID()
	base := model.AccessEdge{
		OriginKind: "agent", OriginID: origin, ResourceID: resource,
		Mode: sdkmodel.ModeRead, SignalSource: sdkmodel.SignalOTEL,
		Confidence: sdkmodel.ConfidenceApproximate, Observed: true,
		FirstSeen: ts(), LastSeen: ts(),
	}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		first, err := sc.AccessEdges().Upsert(ctx, base)
		if err != nil {
			return err
		}
		if first.OccurrenceCount != 1 {
			t.Fatalf("first upsert: occurrence = %d, want 1", first.OccurrenceCount)
		}

		// Second observation with a higher-trust confidence and a known grant.
		base.Confidence = sdkmodel.ConfidenceAttributed
		base.Permitted = true
		second, err := sc.AccessEdges().Upsert(ctx, base)
		if err != nil {
			return err
		}
		if second.ID != first.ID {
			t.Fatalf("upsert created a new row: %s != %s", second.ID, first.ID)
		}
		if second.OccurrenceCount != 2 {
			t.Fatalf("second upsert: occurrence = %d, want 2", second.OccurrenceCount)
		}
		if !second.Observed || !second.Permitted {
			t.Fatalf("second upsert: observed/permitted = %v/%v, want true/true", second.Observed, second.Permitted)
		}
		if second.Confidence != sdkmodel.ConfidenceAttributed {
			t.Fatalf("second upsert: confidence = %q", second.Confidence)
		}
		return nil
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

func TestAccessEdgeDrift(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	origin := model.NewID()

	edge := func(permitted, observed bool) model.AccessEdge {
		return model.AccessEdge{
			OriginKind: "agent", OriginID: origin, ResourceID: model.NewID(),
			Mode: sdkmodel.ModeReadWrite, SignalSource: sdkmodel.SignalPGAudit,
			Confidence: sdkmodel.ConfidenceAttributed, Permitted: permitted, Observed: observed,
			FirstSeen: ts(), LastSeen: ts(),
		}
	}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.AccessEdges().Create(ctx, edge(true, false)); err != nil { // unused grant
			return err
		}
		if _, err := sc.AccessEdges().Create(ctx, edge(false, true)); err != nil { // violation
			return err
		}
		if _, err := sc.AccessEdges().Create(ctx, edge(true, true)); err != nil { // no drift
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed edges: %v", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		drifts, err := sc.AccessEdges().Drift(ctx, model.Query{})
		if err != nil {
			return err
		}
		if len(drifts) != 2 {
			t.Fatalf("drift count = %d, want 2", len(drifts))
		}
		kinds := map[model.DriftKind]int{}
		for _, d := range drifts {
			kinds[d.Kind]++
		}
		if kinds[model.DriftUnusedGrant] != 1 || kinds[model.DriftViolation] != 1 {
			t.Fatalf("drift kinds = %v, want one of each", kinds)
		}
		return nil
	}); err != nil {
		t.Fatalf("drift: %v", err)
	}
}

func TestAccessEdgeNeighbors(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	origin := model.NewID()
	resource := model.NewID()

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.AccessEdges().Create(ctx, model.AccessEdge{
			OriginKind: "agent", OriginID: origin, ResourceID: resource,
			Mode: sdkmodel.ModeRead, SignalSource: sdkmodel.SignalOTEL,
			Confidence: sdkmodel.ConfidenceAttributed, Observed: true, FirstSeen: ts(), LastSeen: ts(),
		})
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		out, err := sc.AccessEdges().Neighbors(ctx, model.NodeRef{Kind: "agent", ID: origin}, model.Outgoing)
		if err != nil {
			return err
		}
		if len(out) != 1 || out[0].ResourceID != resource {
			t.Fatalf("outgoing neighbors = %v, want 1 to %s", out, resource)
		}
		in, err := sc.AccessEdges().Neighbors(ctx, model.NodeRef{Kind: "resource", ID: resource}, model.Incoming)
		if err != nil {
			return err
		}
		if len(in) != 1 || in[0].OriginID != origin {
			t.Fatalf("incoming neighbors = %v, want 1 from %s", in, origin)
		}
		return nil
	}); err != nil {
		t.Fatalf("neighbors: %v", err)
	}
}

// TestAccessEdgeNeighborsSelfLoop proves a self-loop edge (origin == resource)
// is returned once, not twice, by Neighbors(Both).
func TestAccessEdgeNeighborsSelfLoop(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	node := model.NewID()

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.AccessEdges().Create(ctx, model.AccessEdge{
			OriginKind: "agent", OriginID: node, ResourceID: node, // self-loop
			Mode: sdkmodel.ModeReadWrite, SignalSource: sdkmodel.SignalEBPF,
			Confidence: sdkmodel.ConfidenceAttributed, Observed: true, FirstSeen: ts(), LastSeen: ts(),
		})
		return err
	}); err != nil {
		t.Fatalf("seed self-loop: %v", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		both, err := sc.AccessEdges().Neighbors(ctx, model.NodeRef{ID: node}, model.Both)
		if err != nil {
			return err
		}
		if len(both) != 1 {
			t.Fatalf("self-loop Neighbors(Both) = %d edges, want 1", len(both))
		}
		return nil
	}); err != nil {
		t.Fatalf("self-loop neighbors: %v", err)
	}
}
