// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestAllocateFansAgentCostOverSharedResources seeds the access graph (agent A
// touches a shared resource R and an exclusive resource RA; agent B also touches R)
// and a cost attributed to A via a session, then asserts the allocation splits A's
// cost across its resources, flags R as shared with 2 co-consumers, and conserves
// the total (no lost micro-USD).
func TestAllocateFansAgentCostOverSharedResources(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()

	var agentA model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(ctx, model.Agent{Name: "agent-a", ExternalID: "agent-a", Kind: "claude-code", Status: model.StatusActive})
		if err != nil {
			return err
		}
		agentA = a.ID
		b, err := sc.Agents().Create(ctx, model.Agent{Name: "agent-b", ExternalID: "agent-b", Kind: "claude-code", Status: model.StatusActive})
		if err != nil {
			return err
		}
		// A session for A so the cost stream resolves to agent A.
		if _, err := sc.Sessions().Create(ctx, model.Session{ExternalID: "sess-1", AgentID: a.ID, State: model.SessionState("active")}); err != nil {
			return err
		}
		shared, err := sc.Resources().Create(ctx, model.Resource{Name: "shared.table", Kind: "postgres.table"})
		if err != nil {
			return err
		}
		exclusive, err := sc.Resources().Create(ctx, model.Resource{Name: "exclusive.bucket", Kind: "s3.bucket"})
		if err != nil {
			return err
		}
		// Edges: A→shared, A→exclusive, B→shared (so shared has two consumers).
		for _, e := range []model.AccessEdge{
			{OriginKind: "agent", OriginID: a.ID, ResourceID: shared.ID, Mode: sdkmodel.ModeRead, Observed: true, OccurrenceCount: 1, Confidence: sdkmodel.ConfidenceAttributed},
			{OriginKind: "agent", OriginID: a.ID, ResourceID: exclusive.ID, Mode: sdkmodel.ModeReadWrite, Observed: true, OccurrenceCount: 1, Confidence: sdkmodel.ConfidenceAttributed},
			{OriginKind: "agent", OriginID: b.ID, ResourceID: shared.ID, Mode: sdkmodel.ModeRead, Observed: true, OccurrenceCount: 1, Confidence: sdkmodel.ConfidenceAttributed},
		} {
			if _, err := sc.AccessEdges().Upsert(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = agentA

	// Cost attributed to agent A via session sess-1.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "sess-1", 100, 50, 400, baseTime))

	var out allocationResponse
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var e error
		out, e = allocate(ctx, sc, time.Time{}, false, time.Time{}, false)
		return e
	}); err != nil {
		t.Fatal(err)
	}

	if len(out.Agents) != 1 {
		t.Fatalf("agents = %d, want 1 (only A has attributed cost): %+v", len(out.Agents), out.Agents)
	}
	a := out.Agents[0]
	if a.AgentRef != "agent-a" || !a.Resolved || a.Confidence != "attributed" {
		t.Fatalf("agent = %+v, want agent-a resolved attributed", a)
	}
	if a.CostMicroUSD != 400 {
		t.Fatalf("agent cost = %d, want 400", a.CostMicroUSD)
	}
	if len(a.Resources) != 2 {
		t.Fatalf("resources = %d, want 2", len(a.Resources))
	}
	// Allocation conserves the total exactly (rounding remainder reattributed).
	var sum int64
	var sawShared, sawExclusive bool
	for _, rsc := range a.Resources {
		sum += rsc.AllocatedMicroUSD
		if rsc.Shared {
			sawShared = true
			if rsc.CoConsumerAgents != 2 {
				t.Errorf("shared resource co-consumers = %d, want 2", rsc.CoConsumerAgents)
			}
		} else if rsc.CoConsumerAgents == 1 {
			sawExclusive = true
		}
	}
	if sum != 400 {
		t.Errorf("allocated sum = %d, want 400 (conserved)", sum)
	}
	if !sawShared || !sawExclusive {
		t.Errorf("expected one shared and one exclusive resource: %+v", a.Resources)
	}
}
