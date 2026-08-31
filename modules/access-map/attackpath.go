// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// attack-path graph: reachability, privilege-escalation, and exfil-route
// queries over the existing AccessEdge data. These are PRIVILEGED, self-audited
// reads (same pattern as Graph/Diff). Each path carries the weakest attribution
// tier from its constituent edges — never a fabricated confidence.
//
// The graph is the AGPL core of the attack-path feature. The enterprise depth
// (continuous scanning, risk scoring, anomaly detection) builds on top.

const actionAttackPathRead = "access_map.attack_path.read"

const maxPathDepth = 5

// AttackPathKind classifies the analysis type.
type AttackPathKind string

const (
	PathReachability AttackPathKind = "reachability"
	PathEscalation   AttackPathKind = "escalation"
	PathExfil        AttackPathKind = "exfil"
)

// AttackStep is one hop in an attack path.
type attackStepDTO struct {
	NodeKind string `json:"node_kind"`
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name,omitempty"`
	Mode     string `json:"mode,omitempty"`
	ToolID   string `json:"tool_id,omitempty"`
}

// AttackPath is a chain of edges representing a potential attack vector.
type attackPathDTO struct {
	Kind           AttackPathKind  `json:"kind"`
	Steps          []attackStepDTO `json:"steps"`
	MaxSensitivity string          `json:"max_sensitivity,omitempty"`
	Attribution    string          `json:"attribution"`
	Confidence     string          `json:"min_confidence"`
}

// attackPathSummaryDTO is the estate-wide attack surface summary.
type attackPathSummaryDTO struct {
	TotalAgents      int `json:"total_agents"`
	ReachablePaths   int `json:"reachable_paths"`
	EscalationPaths  int `json:"escalation_paths"`
	ExfilRoutes      int `json:"exfil_routes"`
	CriticalAgents   int `json:"critical_agents"`
	SensitiveTargets int `json:"sensitive_targets"`
}

// Reachability computes the set of resources an agent can reach through its
// access edges, following agent→resource and agent→tool→resource chains.
// It is a PRIVILEGED, self-audited read.
func (m *Module) Reachability(ctx context.Context, tenant model.TenantID, actor, actorKind string, agentID model.ID) ([]attackPathDTO, error) {
	if m.data == nil {
		return nil, errNoData
	}
	var paths []attackPathDTO
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: actor, ActorKind: actorKind, Action: actionAttackPathRead,
			TargetKind: "agent", TargetID: agentID,
			Meta: map[string]any{"analysis": "reachability"},
		}); err != nil {
			return err
		}

		edges, _, err := sc.AccessEdges().List(ctx, model.Query{
			Filters: []model.Filter{
				{Column: "origin_kind", Op: model.OpEq, Value: originAgent},
				{Column: "origin_id", Op: model.OpEq, Value: agentID.String()},
			},
			Limit: 1000,
		})
		if err != nil {
			return err
		}

		agent, gerr := sc.Agents().Get(ctx, agentID)
		if gerr != nil {
			return gerr
		}

		seen := map[string]bool{}
		for _, e := range edges {
			key := e.ResourceID.String()
			if seen[key] {
				continue
			}
			seen[key] = true

			resourceName := ""
			if r, rerr := sc.Resources().Get(ctx, e.ResourceID); rerr == nil {
				resourceName = r.Name
			}

			steps := []attackStepDTO{
				{NodeKind: "agent", NodeID: agentID.String(), NodeName: agent.Name},
			}
			if !e.ToolID.IsZero() {
				toolName := ""
				if t, terr := sc.Tools().Get(ctx, e.ToolID); terr == nil {
					toolName = t.Name
				}
				steps = append(steps, attackStepDTO{
					NodeKind: "tool", NodeID: e.ToolID.String(), NodeName: toolName,
				})
			}
			steps = append(steps, attackStepDTO{
				NodeKind: "resource", NodeID: e.ResourceID.String(),
				NodeName: resourceName, Mode: string(e.Mode),
			})

			sens := ""
			if r, rerr := sc.Resources().Get(ctx, e.ResourceID); rerr == nil {
				sens = r.Sensitivity
			}

			paths = append(paths, attackPathDTO{
				Kind:           PathReachability,
				Steps:          steps,
				Attribution:    weakestAttribution(e),
				Confidence:     string(e.Confidence),
				MaxSensitivity: sens,
			})
		}
		return nil
	})
	return paths, err
}

// EscalationPaths finds paths where an agent could reach higher-privileged
// identities via shared resources. An escalation exists when agent A accesses
// a resource R that is also accessed by agent B (via identity B), and B has
// more access (more RW edges) than A.
func (m *Module) EscalationPaths(ctx context.Context, tenant model.TenantID, actor, actorKind string, agentID model.ID) ([]attackPathDTO, error) {
	if m.data == nil {
		return nil, errNoData
	}
	var paths []attackPathDTO
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: actor, ActorKind: actorKind, Action: actionAttackPathRead,
			TargetKind: "agent", TargetID: agentID,
			Meta: map[string]any{"analysis": "escalation"},
		}); err != nil {
			return err
		}

		myEdges, _, err := sc.AccessEdges().List(ctx, model.Query{
			Filters: []model.Filter{
				{Column: "origin_kind", Op: model.OpEq, Value: originAgent},
				{Column: "origin_id", Op: model.OpEq, Value: agentID.String()},
			},
			Limit: 1000,
		})
		if err != nil {
			return err
		}

		myRWCount := 0
		resourceIDs := map[string]model.AccessEdge{}
		for _, e := range myEdges {
			if e.Mode == sdkmodel.ModeReadWrite || e.Mode == sdkmodel.ModeWrite {
				myRWCount++
			}
			resourceIDs[e.ResourceID.String()] = e
		}

		agent, _ := sc.Agents().Get(ctx, agentID)

		for rid, myEdge := range resourceIDs {
			incoming, ierr := sc.AccessEdges().Neighbors(ctx, model.NodeRef{Kind: "resource", ID: model.ID(rid)}, model.Incoming)
			if ierr != nil {
				continue
			}

			for _, other := range incoming {
				if other.OriginKind != originAgent || other.OriginID == agentID {
					continue
				}

				otherEdges, _, oerr := sc.AccessEdges().List(ctx, model.Query{
					Filters: []model.Filter{
						{Column: "origin_kind", Op: model.OpEq, Value: originAgent},
						{Column: "origin_id", Op: model.OpEq, Value: other.OriginID.String()},
					},
					Limit: 1000,
				})
				if oerr != nil {
					continue
				}
				otherRW := 0
				for _, oe := range otherEdges {
					if oe.Mode == sdkmodel.ModeReadWrite || oe.Mode == sdkmodel.ModeWrite {
						otherRW++
					}
				}

				if otherRW <= myRWCount {
					continue
				}

				otherAgent, _ := sc.Agents().Get(ctx, other.OriginID)
				resourceName := ""
				if r, rerr := sc.Resources().Get(ctx, model.ID(rid)); rerr == nil {
					resourceName = r.Name
				}

				paths = append(paths, attackPathDTO{
					Kind: PathEscalation,
					Steps: []attackStepDTO{
						{NodeKind: "agent", NodeID: agentID.String(), NodeName: agent.Name},
						{NodeKind: "resource", NodeID: rid, NodeName: resourceName, Mode: string(myEdge.Mode)},
						{NodeKind: "agent", NodeID: other.OriginID.String(), NodeName: otherAgent.Name},
					},
					Attribution: weakestOfTwo(weakestAttribution(myEdge), weakestAttribution(other)),
					Confidence:  weakerConfidence(myEdge.Confidence, other.Confidence),
				})

				if len(paths) >= 100 {
					return nil
				}
			}
		}
		return nil
	})
	return paths, err
}

// ExfilRoutes finds paths from sensitive resources outward through agents that
// have write access to external resources (potential data exfiltration paths).
func (m *Module) ExfilRoutes(ctx context.Context, tenant model.TenantID, actor, actorKind string, resourceID model.ID) ([]attackPathDTO, error) {
	if m.data == nil {
		return nil, errNoData
	}
	var paths []attackPathDTO
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: actor, ActorKind: actorKind, Action: actionAttackPathRead,
			TargetKind: "resource", TargetID: resourceID,
			Meta: map[string]any{"analysis": "exfil"},
		}); err != nil {
			return err
		}

		resource, rerr := sc.Resources().Get(ctx, resourceID)
		if rerr != nil {
			return rerr
		}

		incoming, ierr := sc.AccessEdges().Neighbors(ctx, model.NodeRef{Kind: "resource", ID: resourceID}, model.Incoming)
		if ierr != nil {
			return ierr
		}

		for _, readEdge := range incoming {
			if readEdge.OriginKind != originAgent {
				continue
			}

			agentName := ""
			if a, aerr := sc.Agents().Get(ctx, readEdge.OriginID); aerr == nil {
				agentName = a.Name
			}

			writeEdges, _, werr := sc.AccessEdges().List(ctx, model.Query{
				Filters: []model.Filter{
					{Column: "origin_kind", Op: model.OpEq, Value: originAgent},
					{Column: "origin_id", Op: model.OpEq, Value: readEdge.OriginID.String()},
				},
				Limit: 1000,
			})
			if werr != nil {
				continue
			}

			for _, we := range writeEdges {
				if we.Mode != sdkmodel.ModeWrite && we.Mode != sdkmodel.ModeReadWrite {
					continue
				}
				if we.ResourceID == resourceID {
					continue // same resource, not exfil
				}

				destName := ""
				if d, derr := sc.Resources().Get(ctx, we.ResourceID); derr == nil {
					destName = d.Name
				}

				paths = append(paths, attackPathDTO{
					Kind: PathExfil,
					Steps: []attackStepDTO{
						{NodeKind: "resource", NodeID: resourceID.String(), NodeName: resource.Name},
						{NodeKind: "agent", NodeID: readEdge.OriginID.String(), NodeName: agentName},
						{NodeKind: "resource", NodeID: we.ResourceID.String(), NodeName: destName, Mode: string(we.Mode)},
					},
					MaxSensitivity: resource.Sensitivity,
					Attribution:    weakestOfTwo(weakestAttribution(readEdge), weakestAttribution(we)),
					Confidence:     weakerConfidence(readEdge.Confidence, we.Confidence),
				})

				if len(paths) >= 100 {
					return nil
				}
			}
		}
		return nil
	})
	return paths, err
}

func weakestAttribution(e model.AccessEdge) string {
	if e.Metadata == nil {
		return "unknown"
	}
	if tier, ok := e.Metadata["attribution_tier"].(string); ok && tier != "" {
		return tier
	}
	return "unknown"
}

func weakestOfTwo(a, b string) string {
	rank := map[string]int{"firm": 3, "approximate": 2, "unknown": 1}
	ra, rb := rank[a], rank[b]
	if ra == 0 {
		ra = 1
	}
	if rb == 0 {
		rb = 1
	}
	if ra <= rb {
		return a
	}
	return b
}

func weakerConfidence(a, b sdkmodel.Confidence) string {
	rank := map[sdkmodel.Confidence]int{
		sdkmodel.ConfidenceAttributed:  2,
		sdkmodel.ConfidenceApproximate: 1,
	}
	ra, rb := rank[a], rank[b]
	if ra == 0 {
		ra = 1
	}
	if rb == 0 {
		rb = 1
	}
	if ra <= rb {
		return string(a)
	}
	return string(b)
}

// handleReachability serves GET /attack-paths/reachability?agent_id=...
func (m *Module) handleReachability(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	agentID := model.ID(r.URL.Query().Get("agent_id"))
	if agentID.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agent_id is required"})
		return
	}
	paths, err := m.Reachability(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), agentID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if paths == nil {
		paths = []attackPathDTO{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"paths": paths})
}

// handleEscalation serves GET /attack-paths/escalation?agent_id=...
func (m *Module) handleEscalation(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	agentID := model.ID(r.URL.Query().Get("agent_id"))
	if agentID.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agent_id is required"})
		return
	}
	paths, err := m.EscalationPaths(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), agentID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if paths == nil {
		paths = []attackPathDTO{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"paths": paths})
}

// handleExfil serves GET /attack-paths/exfil?resource_id=...
func (m *Module) handleExfil(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	resourceID := model.ID(r.URL.Query().Get("resource_id"))
	if resourceID.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "resource_id is required"})
		return
	}
	paths, err := m.ExfilRoutes(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), resourceID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if paths == nil {
		paths = []attackPathDTO{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"paths": paths})
}

// handleAttackPathSummary serves GET /attack-paths/summary
func (m *Module) handleAttackPathSummary(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var out attackPathSummaryDTO
	err := m.data.Mutate(r.Context(), mc.Tenant, func(sc store.Scope) error {
		if _, err := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: actionAttackPathRead,
			Meta:   map[string]any{"analysis": "summary"},
		}); err != nil {
			return err
		}

		agents, _, aerr := sc.Agents().List(r.Context(), model.Query{Limit: 1000})
		if aerr != nil {
			return aerr
		}
		out.TotalAgents = len(agents)
		for _, a := range agents {
			if a.RiskTier == "critical" || a.RiskTier == "high" {
				out.CriticalAgents++
			}
		}

		resources, _, rerr := sc.Resources().List(r.Context(), model.Query{Limit: 1000})
		if rerr != nil {
			return rerr
		}
		for _, rs := range resources {
			if rs.Sensitivity != "" {
				out.SensitiveTargets++
			}
		}

		edges, _, eerr := sc.AccessEdges().List(r.Context(), model.Query{Limit: 1000})
		if eerr != nil {
			return eerr
		}
		out.ReachablePaths = len(edges)

		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
