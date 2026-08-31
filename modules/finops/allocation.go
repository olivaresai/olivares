// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// FIN-09 — multi-agent cost allocation over the access-map graph.
//
// The FinOps Foundation states there is NO accepted framework for allocating
// multi-agent / model-output-consumer cost, so this is an EXPLICIT, honest heuristic,
// not a magic split (ARCHITECTURE.md). It bridges module III's agent→resource access
// graph into FinOps: an agent's recorded cost is allocated across the resources it
// actually touched, weighted by how often (the only weight the graph carries,
// AccessEdge.OccurrenceCount), and each resource is flagged when OTHER agents also
// consumed it (shared-resource fan-out a downstream chargeback can re-split). It is
// READ-TIME (it writes no rows), so it never perturbs the dedup/refund symmetry of
// the cost ledger. It allocates only what resolves to a concrete agent at attributed
// confidence; shared/pooled-credential origins are surfaced as approximate and never
// split to a fabricated agent (mirroring the access-map's own honesty invariant).
const allocationMethod = "occurrence_weighted_shared_resource"

// allocationResourceDTO is one resource an agent's cost was allocated to.
type allocationResourceDTO struct {
	ResourceID        string `json:"resource_id"`
	OccurrenceCount   int64  `json:"occurrence_count"`
	AllocatedMicroUSD int64  `json:"allocated_micro_usd"`
	CoConsumerAgents  int    `json:"co_consumer_agents"` // distinct agents touching it (incl. this one)
	Shared            bool   `json:"shared"`
}

// allocationAgentDTO is one agent's cost and its allocation across resources.
type allocationAgentDTO struct {
	AgentRef     string                  `json:"agent_ref"`
	Resolved     bool                    `json:"resolved"`
	Confidence   string                  `json:"confidence"` // attributed|approximate
	CostMicroUSD int64                   `json:"cost_micro_usd"`
	Resources    []allocationResourceDTO `json:"resources"`
}

// allocationResponse is the FOCUS-Split-Cost-Allocation-shaped view: the method, its
// explicit assumptions, and the per-agent allocation. The AllocatedMethodId/Details
// map to FOCUS Split Cost Allocation columns when exported.
type allocationResponse struct {
	Since         string `json:"since,omitempty"`
	Until         string `json:"until,omitempty"`
	Method        string `json:"allocated_method_id"`
	MethodDetails string `json:"allocated_method_details"`
	// JSONArray, not a plain slice: allocation appends per attributed agent, so a
	// window with no attributed spend — every window of a new install — returned
	// agents:null while the sibling Resources list (explicitly initialized) returned [].
	Agents    api.JSONArray[allocationAgentDTO] `json:"agents"`
	Note      string                            `json:"note"`
	Truncated bool                              `json:"truncated,omitempty"`
}

// allocate computes the per-agent cost allocation over the access graph for the
// window. It aggregates cost by agent from the estimated stream, then for each agent
// traverses its outgoing edges to the resources it touched and each resource's
// incoming edges to find co-consumers.
func allocate(ctx context.Context, sc store.Scope, since time.Time, hasSince bool, until time.Time, hasUntil bool) (allocationResponse, error) {
	out := allocationResponse{
		Method:        allocationMethod,
		MethodDetails: "an agent's cost is split across the resources it accessed, weighted by observed occurrence count; resources accessed by more than one agent are flagged shared for downstream split-cost. Multi-agent allocation is an open FinOps problem — this is a heuristic with explicit assumptions, not a settled cost.",
	}
	// Cost by agent (estimated stream, agent attributed).
	costByAgent := map[string]int64{}
	filters := windowFilters([]model.Filter{{Column: colAgentRef, Op: model.OpNe, Value: ""}}, since, hasSince, until, hasUntil)
	trunc, err := scanSamples(ctx, sc, filters, func(r model.Record) {
		if ar := r.String(colAgentRef); ar != "" {
			costByAgent[ar] += r.Int(colCostMicroUSD)
		}
	})
	if err != nil {
		return allocationResponse{}, err
	}
	out.Truncated = trunc

	edges := sc.AccessEdges()
	for agentRef, cost := range costByAgent {
		ag := allocationAgentDTO{AgentRef: agentRef, CostMicroUSD: cost, Confidence: "attributed", Resources: []allocationResourceDTO{}}
		agentID, ok, err := resolveAgentID(ctx, sc, agentRef)
		if err != nil {
			return allocationResponse{}, err
		}
		if !ok {
			ag.Resolved = false // cost stays with the agent; graph could not place it
			out.Agents = append(out.Agents, ag)
			continue
		}
		ag.Resolved = true

		outgoing, err := edges.Neighbors(ctx, model.NodeRef{Kind: "agent", ID: agentID}, model.Outgoing)
		if err != nil {
			return allocationResponse{}, err
		}
		resOcc := map[model.ID]int64{}
		var totalOcc int64
		for _, e := range outgoing {
			if e.OriginKind != "agent" {
				continue
			}
			occ := e.OccurrenceCount
			if occ <= 0 {
				occ = 1
			}
			resOcc[e.ResourceID] += occ
			totalOcc += occ
			if e.Confidence == "approximate" {
				ag.Confidence = "approximate" // a shared/pooled origin — never fabricate a firm split
			}
		}
		if totalOcc == 0 {
			out.Agents = append(out.Agents, ag) // no graph edges: nothing to fan out
			continue
		}
		var allocated int64
		for resID, occ := range resOcc {
			inc, err := edges.Neighbors(ctx, model.NodeRef{Kind: "resource", ID: resID}, model.Incoming)
			if err != nil {
				return allocationResponse{}, err
			}
			consumers := map[model.ID]bool{}
			for _, e := range inc {
				if e.OriginKind == "agent" {
					consumers[e.OriginID] = true
				}
			}
			share := cost * occ / totalOcc
			allocated += share
			ag.Resources = append(ag.Resources, allocationResourceDTO{
				ResourceID: resID.String(), OccurrenceCount: occ, AllocatedMicroUSD: share,
				CoConsumerAgents: len(consumers), Shared: len(consumers) > 1,
			})
		}
		// Integer division can leave a remainder; attribute it to the largest bucket
		// so the allocation sums EXACTLY to the agent's cost (no lost micro-USD).
		if rem := cost - allocated; rem != 0 && len(ag.Resources) > 0 {
			ag.Resources[largestResource(ag.Resources)].AllocatedMicroUSD += rem
		}
		out.Agents = append(out.Agents, ag)
	}
	if len(out.Agents) == 0 {
		out.Note = "No agent-attributed cost in this window. The model/provider cost stream carries no session/agent, so allocation applies to cooperative-agent cost (e.g. Claude Code) where the session resolves to an agent."
	}
	if hasSince {
		out.Since = since.UTC().Format(time.RFC3339)
	}
	if hasUntil {
		out.Until = until.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// largestResource returns the index of the resource with the largest allocation, so
// a rounding remainder lands on the bucket where it least distorts the split.
func largestResource(rs []allocationResourceDTO) int {
	best := 0
	for i := 1; i < len(rs); i++ {
		if rs[i].AllocatedMicroUSD > rs[best].AllocatedMicroUSD {
			best = i
		}
	}
	return best
}

// resolveAgentID resolves an agent's natural reference (external id, then name) to
// its core entity id, so the access graph (keyed by id) can be traversed. ok=false
// when the agent is not found — the cost then stays unallocated rather than guessed.
func resolveAgentID(ctx context.Context, sc store.Scope, agentRef string) (model.ID, bool, error) {
	if a, ok, err := findOne(ctx, sc.Agents(), eq("external_id", agentRef)); err != nil {
		return "", false, err
	} else if ok {
		return a.ID, true, nil
	}
	if a, ok, err := findOne(ctx, sc.Agents(), eq("name", agentRef)); err != nil {
		return "", false, err
	} else if ok {
		return a.ID, true, nil
	}
	return "", false, nil
}
