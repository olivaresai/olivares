// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"errors"
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

// edgeReadCap is the most rows one store read returns (the store's own maxLimit).
// A read that fills it may have more rows behind it, so what was built from it is
// a floor and the answer says so (truncated).
const edgeReadCap = 1000

// maxAttackPaths bounds the paths one analysis returns.
const maxAttackPaths = 100

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

// attackPathsDTO is the answer of one analysis. Truncated is set when a cap cut
// it (a store read that filled edgeReadCap, or more than maxAttackPaths paths):
// the paths returned are then a subset, never presented as the whole answer.
type attackPathsDTO struct {
	Paths     []attackPathDTO `json:"paths"`
	Truncated bool            `json:"truncated,omitempty"`
}

// attackPathSummaryDTO is the estate-wide attack surface summary. The counts are
// floors when Truncated is set.
type attackPathSummaryDTO struct {
	TotalAgents      int  `json:"total_agents"`
	ReachablePaths   int  `json:"reachable_paths"`
	EscalationPaths  int  `json:"escalation_paths"`
	ExfilRoutes      int  `json:"exfil_routes"`
	CriticalAgents   int  `json:"critical_agents"`
	SensitiveTargets int  `json:"sensitive_targets"`
	Truncated        bool `json:"truncated,omitempty"`
}

// attackReader is what one analysis reads through. It returns store errors to
// the caller (a failed read is never a shorter answer), remembers whether any read
// hit its cap, and caches rows so an estate-wide pass reads each agent's edges,
// each resource's readers and each name once.
type attackReader struct {
	sc        store.Scope
	truncated bool
	edges     map[model.ID][]model.AccessEdge // edges whose origin is the agent
	incoming  map[model.ID][]model.AccessEdge // edges that reach the resource
	agents    map[model.ID]model.Agent
	resources map[model.ID]model.Resource
	writes    map[model.ID]int // write-capable edges per origin agent
}

func newAttackReader(sc store.Scope) *attackReader {
	return &attackReader{
		sc:        sc,
		edges:     map[model.ID][]model.AccessEdge{},
		incoming:  map[model.ID][]model.AccessEdge{},
		agents:    map[model.ID]model.Agent{},
		resources: map[model.ID]model.Resource{},
		writes:    map[model.ID]int{},
	}
}

// seed primes the row caches with rows the caller already listed, so an
// estate-wide pass does not read each of them a second time.
func (r *attackReader) seed(agents []model.Agent, resources []model.Resource) {
	for _, a := range agents {
		r.agents[a.ID] = a
	}
	for _, x := range resources {
		r.resources[x.ID] = x
	}
}

// writeTotal is how many of the agent's edges write, counted once per agent.
func (r *attackReader) writeTotal(ctx context.Context, agent model.ID) (int, error) {
	if n, ok := r.writes[agent]; ok {
		return n, nil
	}
	edges, err := r.agentEdges(ctx, agent)
	if err != nil {
		return 0, err
	}
	n := writeCount(edges)
	r.writes[agent] = n
	return n, nil
}

// agentEdges returns the access edges the agent originates.
func (r *attackReader) agentEdges(ctx context.Context, agent model.ID) ([]model.AccessEdge, error) {
	if e, ok := r.edges[agent]; ok {
		return e, nil
	}
	e, page, err := r.sc.AccessEdges().List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: "origin_kind", Op: model.OpEq, Value: originAgent},
			{Column: "origin_id", Op: model.OpEq, Value: agent.String()},
		},
		Limit: edgeReadCap,
	})
	if err != nil {
		return nil, err
	}
	if page.HasMore {
		r.truncated = true
	}
	r.edges[agent] = e
	return e, nil
}

// readers returns the edges that reach the resource. The store's Neighbors has no
// page token, so a read that fills the cap is treated as possibly cut.
func (r *attackReader) readers(ctx context.Context, resource model.ID) ([]model.AccessEdge, error) {
	if e, ok := r.incoming[resource]; ok {
		return e, nil
	}
	e, err := r.sc.AccessEdges().Neighbors(ctx, model.NodeRef{Kind: "resource", ID: resource}, model.Incoming)
	if err != nil {
		return nil, err
	}
	if len(e) >= edgeReadCap {
		r.truncated = true
	}
	r.incoming[resource] = e
	return e, nil
}

// agent and resource return the row, or the store's error (ErrNotFound for a row
// that is gone). Callers that only want a display name pass the result to nameOf.
func (r *attackReader) agent(ctx context.Context, id model.ID) (model.Agent, error) {
	if a, ok := r.agents[id]; ok {
		return a, nil
	}
	a, err := r.sc.Agents().Get(ctx, id)
	if err == nil {
		r.agents[id] = a
	}
	return a, err
}

func (r *attackReader) resource(ctx context.Context, id model.ID) (model.Resource, error) {
	if x, ok := r.resources[id]; ok {
		return x, nil
	}
	x, err := r.sc.Resources().Get(ctx, id)
	if err == nil {
		r.resources[id] = x
	}
	return x, err
}

// nameOf is the display name of a row looked up for a path step. A row that is
// gone (a dangling edge) has no name; any other store error is returned, so a
// cancelled context or a failed read is not rendered as an unnamed node.
func nameOf(name string, err error) (string, error) {
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return name, err
}

func isWriteMode(m sdkmodel.AccessMode) bool {
	return m == sdkmodel.ModeWrite || m == sdkmodel.ModeReadWrite
}

func writeCount(edges []model.AccessEdge) int {
	n := 0
	for _, e := range edges {
		if isWriteMode(e.Mode) {
			n++
		}
	}
	return n
}

// reachability lists the resources the agent reaches through its access edges,
// following agent→resource and agent→tool→resource chains.
func (r *attackReader) reachability(ctx context.Context, agentID model.ID) ([]attackPathDTO, error) {
	edges, err := r.agentEdges(ctx, agentID)
	if err != nil {
		return nil, err
	}
	agent, err := r.agent(ctx, agentID)
	if err != nil {
		return nil, err
	}

	var paths []attackPathDTO
	seen := map[string]bool{}
	for _, e := range edges {
		key := e.ResourceID.String()
		if seen[key] {
			continue
		}
		seen[key] = true

		res, err := r.resource(ctx, e.ResourceID)
		resourceName, err := nameOf(res.Name, err)
		if err != nil {
			return nil, err
		}

		steps := []attackStepDTO{
			{NodeKind: "agent", NodeID: agentID.String(), NodeName: agent.Name},
		}
		if !e.ToolID.IsZero() {
			t, err := r.sc.Tools().Get(ctx, e.ToolID)
			toolName, err := nameOf(t.Name, err)
			if err != nil {
				return nil, err
			}
			steps = append(steps, attackStepDTO{
				NodeKind: "tool", NodeID: e.ToolID.String(), NodeName: toolName,
			})
		}
		steps = append(steps, attackStepDTO{
			NodeKind: "resource", NodeID: e.ResourceID.String(),
			NodeName: resourceName, Mode: string(e.Mode),
		})

		paths = append(paths, attackPathDTO{
			Kind:           PathReachability,
			Steps:          steps,
			Attribution:    weakestAttribution(e),
			Confidence:     string(e.Confidence),
			MaxSensitivity: res.Sensitivity,
		})
	}
	return paths, nil
}

// escalation finds paths where an agent could reach higher-privileged identities
// via shared resources. An escalation exists when agent A accesses a resource R
// that is also accessed by agent B (via identity B), and B has more access (more
// RW edges) than A. It returns at most limit paths and sets r.truncated when
// another one existed.
func (r *attackReader) escalation(ctx context.Context, agentID model.ID, limit int) ([]attackPathDTO, error) {
	myEdges, err := r.agentEdges(ctx, agentID)
	if err != nil {
		return nil, err
	}
	agent, err := r.agent(ctx, agentID)
	if err != nil {
		return nil, err
	}

	myRWCount, err := r.writeTotal(ctx, agentID)
	if err != nil {
		return nil, err
	}
	byResource := map[model.ID]model.AccessEdge{}
	for _, e := range myEdges {
		byResource[e.ResourceID] = e
	}

	var paths []attackPathDTO
	for rid, myEdge := range byResource {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		incoming, err := r.readers(ctx, rid)
		if err != nil {
			return nil, err
		}

		for _, other := range incoming {
			if other.OriginKind != originAgent || other.OriginID == agentID {
				continue
			}

			otherRW, err := r.writeTotal(ctx, other.OriginID)
			if err != nil {
				return nil, err
			}
			if otherRW <= myRWCount {
				continue
			}

			oa, err := r.agent(ctx, other.OriginID)
			otherName, err := nameOf(oa.Name, err)
			if err != nil {
				return nil, err
			}
			res, err := r.resource(ctx, rid)
			resourceName, err := nameOf(res.Name, err)
			if err != nil {
				return nil, err
			}

			if len(paths) >= limit {
				r.truncated = true
				return paths, nil
			}
			paths = append(paths, attackPathDTO{
				Kind: PathEscalation,
				Steps: []attackStepDTO{
					{NodeKind: "agent", NodeID: agentID.String(), NodeName: agent.Name},
					{NodeKind: "resource", NodeID: rid.String(), NodeName: resourceName, Mode: string(myEdge.Mode)},
					{NodeKind: "agent", NodeID: other.OriginID.String(), NodeName: otherName},
				},
				Attribution: weakestOfTwo(weakestAttribution(myEdge), weakestAttribution(other)),
				Confidence:  weakerConfidence(myEdge.Confidence, other.Confidence),
			})
		}
	}
	return paths, nil
}

// exfil finds paths from a resource outward through the agents that read it and
// have write access to another resource (potential data exfiltration paths). It
// returns at most limit paths and sets r.truncated when another one existed.
func (r *attackReader) exfil(ctx context.Context, resourceID model.ID, limit int) ([]attackPathDTO, error) {
	resource, err := r.resource(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	incoming, err := r.readers(ctx, resourceID)
	if err != nil {
		return nil, err
	}

	var paths []attackPathDTO
	for _, readEdge := range incoming {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if readEdge.OriginKind != originAgent {
			continue
		}

		a, err := r.agent(ctx, readEdge.OriginID)
		agentName, err := nameOf(a.Name, err)
		if err != nil {
			return nil, err
		}

		writeEdges, err := r.agentEdges(ctx, readEdge.OriginID)
		if err != nil {
			return nil, err
		}
		for _, we := range writeEdges {
			if !isWriteMode(we.Mode) {
				continue
			}
			if we.ResourceID == resourceID {
				continue // same resource, not exfil
			}

			d, err := r.resource(ctx, we.ResourceID)
			destName, err := nameOf(d.Name, err)
			if err != nil {
				return nil, err
			}

			if len(paths) >= limit {
				r.truncated = true
				return paths, nil
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
		}
	}
	return paths, nil
}

// analyse runs one self-audited, privileged analysis: the read is audited in the
// same transaction that answers it, and a failure of any read is the error.
func (m *Module) analyse(ctx context.Context, tenant model.TenantID, actor, actorKind string, targetKind model.Kind, target model.ID, analysis string,
	run func(*attackReader) ([]attackPathDTO, error)) (attackPathsDTO, error) {
	if m.data == nil {
		return attackPathsDTO{}, errNoData
	}
	out := attackPathsDTO{Paths: []attackPathDTO{}}
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: actor, ActorKind: actorKind, Action: actionAttackPathRead,
			TargetKind: targetKind, TargetID: target,
			Meta: map[string]any{"analysis": analysis},
		}); err != nil {
			return err
		}
		r := newAttackReader(sc)
		paths, err := run(r)
		if err != nil {
			return err
		}
		if paths != nil {
			out.Paths = paths
		}
		out.Truncated = r.truncated
		return nil
	})
	return out, err
}

// Reachability computes the set of resources an agent can reach through its
// access edges, following agent→resource and agent→tool→resource chains.
// It is a PRIVILEGED, self-audited read.
func (m *Module) Reachability(ctx context.Context, tenant model.TenantID, actor, actorKind string, agentID model.ID) (attackPathsDTO, error) {
	return m.analyse(ctx, tenant, actor, actorKind, "agent", agentID, "reachability",
		func(r *attackReader) ([]attackPathDTO, error) { return r.reachability(ctx, agentID) })
}

// EscalationPaths finds paths where an agent could reach higher-privileged
// identities via shared resources (see attackReader.escalation).
func (m *Module) EscalationPaths(ctx context.Context, tenant model.TenantID, actor, actorKind string, agentID model.ID) (attackPathsDTO, error) {
	return m.analyse(ctx, tenant, actor, actorKind, "agent", agentID, "escalation",
		func(r *attackReader) ([]attackPathDTO, error) { return r.escalation(ctx, agentID, maxAttackPaths) })
}

// ExfilRoutes finds paths from a sensitive resource outward through agents that
// have write access to other resources (see attackReader.exfil).
func (m *Module) ExfilRoutes(ctx context.Context, tenant model.TenantID, actor, actorKind string, resourceID model.ID) (attackPathsDTO, error) {
	return m.analyse(ctx, tenant, actor, actorKind, "resource", resourceID, "exfil",
		func(r *attackReader) ([]attackPathDTO, error) { return r.exfil(ctx, resourceID, maxAttackPaths) })
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
		writeJSON(w, http.StatusBadRequest, api.ErrorBody("bad_request", "agent_id is required"))
		return
	}
	out, err := m.Reachability(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), agentID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleEscalation serves GET /attack-paths/escalation?agent_id=...
func (m *Module) handleEscalation(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	agentID := model.ID(r.URL.Query().Get("agent_id"))
	if agentID.IsZero() {
		writeJSON(w, http.StatusBadRequest, api.ErrorBody("bad_request", "agent_id is required"))
		return
	}
	out, err := m.EscalationPaths(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), agentID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleExfil serves GET /attack-paths/exfil?resource_id=...
func (m *Module) handleExfil(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	resourceID := model.ID(r.URL.Query().Get("resource_id"))
	if resourceID.IsZero() {
		writeJSON(w, http.StatusBadRequest, api.ErrorBody("bad_request", "resource_id is required"))
		return
	}
	out, err := m.ExfilRoutes(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), resourceID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAttackPathSummary serves GET /attack-paths/summary
func (m *Module) handleAttackPathSummary(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// The escalation and exfil counts are what the per-agent and per-resource
	// routes return, summed over every agent and every labeled-sensitive resource,
	// so the number to trend agrees with the answers an operator then drills into.
	// Any cap that cut a read or a path list makes every count a floor (truncated).
	if m.data == nil {
		writeStoreError(w, errNoData)
		return
	}
	ctx := r.Context()
	var out attackPathSummaryDTO
	err := m.data.Mutate(ctx, mc.Tenant, func(sc store.Scope) error {
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: actionAttackPathRead,
			Meta:   map[string]any{"analysis": "summary"},
		}); err != nil {
			return err
		}

		agents, page, err := sc.Agents().List(ctx, model.Query{Limit: edgeReadCap})
		if err != nil {
			return err
		}
		out.Truncated = page.HasMore
		out.TotalAgents = len(agents)
		for _, a := range agents {
			if a.RiskTier == "critical" || a.RiskTier == "high" {
				out.CriticalAgents++
			}
		}

		resources, page, err := sc.Resources().List(ctx, model.Query{Limit: edgeReadCap})
		if err != nil {
			return err
		}
		out.Truncated = out.Truncated || page.HasMore

		edges, page, err := sc.AccessEdges().List(ctx, model.Query{Limit: edgeReadCap})
		if err != nil {
			return err
		}
		out.Truncated = out.Truncated || page.HasMore
		out.ReachablePaths = len(edges)

		rd := newAttackReader(sc)
		rd.seed(agents, resources)
		for _, a := range agents {
			if err := ctx.Err(); err != nil {
				return err
			}
			paths, err := rd.escalation(ctx, a.ID, maxAttackPaths)
			if err != nil {
				return err
			}
			out.EscalationPaths += len(paths)
		}
		for _, rs := range resources {
			if rs.Sensitivity == "" {
				continue
			}
			out.SensitiveTargets++
			if err := ctx.Err(); err != nil {
				return err
			}
			paths, err := rd.exfil(ctx, rs.ID, maxAttackPaths)
			if err != nil {
				return err
			}
			out.ExfilRoutes += len(paths)
		}
		out.Truncated = out.Truncated || rd.truncated
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
