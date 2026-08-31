// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"github.com/olivaresai/olivares/core/model"
)

// coverageNote is the honest-coverage caveat carried on every graph response. The
// comm graph is derived from edge.observed only. As of the a2a connector
// (AIP-05) observes peer-to-peer A2A (signed Agent Cards + observed Task lifecycle),
// so the graph now spans non-Claude agents — but frameworks WITHOUT an emitting
// connector are still ABSENT, not zero, and remain honestly disclosed.
const coverageNote = "derived from edge.observed: Claude Code Task delegation, MCP topology, and peer-to-peer A2A (a2a connector — signed Agent Cards + observed Task lifecycle). Swarm cross-talk and non-Task frameworks without an emitting connector (e.g. LangGraph/CrewAI/AutoGen, unless observed via OTel gen_ai) remain ABSENT, not zero."

// Node kinds derived from a ref's role in the edge set.
const (
	nodeSession = "session"
	nodeAgent   = "agent"
)

// Node roles derived from delegation in/out degree.
const (
	roleSupervisor = "supervisor"
	roleWorker     = "worker"
	rolePeer       = "peer"
)

// edgeDTO projects one relation row for the React-Flow comm graph (sibling to the
// access-map edge contract). source/target are the
// already-redacted refs; there is no payload field by construction.
type edgeDTO struct {
	ID              string `json:"id"`
	Source          string `json:"source"` // supervisor_ref (React-Flow source)
	Target          string `json:"target"` // worker_ref (React-Flow target)
	LinkKind        string `json:"link_kind"`
	ToolRef         string `json:"tool_ref,omitempty"`
	Mode            string `json:"mode"`
	SignalSource    string `json:"signal_source"`
	Confidence      string `json:"confidence"`
	DelegationCount int64  `json:"delegation_count"`
	FirstSeenAt     string `json:"first_seen_at"`
	LastSeenAt      string `json:"last_seen_at"`
	Animated        bool   `json:"animated"` // last_seen within the active window (set per request)
}

// toEdgeDTO projects a relation record. Animated is left false; the graph handler
// sets it per request from the clock.
func toEdgeDTO(rec model.Record) edgeDTO {
	return edgeDTO{
		ID:              rec.String(model.ColID),
		Source:          rec.String(colSupervisorRef),
		Target:          rec.String(colWorkerRef),
		LinkKind:        rec.String(colLinkKind),
		ToolRef:         rec.String(colToolRef),
		Mode:            rec.String(colMode),
		SignalSource:    rec.String(colSignalSource),
		Confidence:      rec.String(colConfidence),
		DelegationCount: rec.Int(colDelegationCnt),
		FirstSeenAt:     rec.String(colFirstSeenAt),
		LastSeenAt:      rec.String(colLastSeenAt),
	}
}

// nodeDTO is a graph node derived from the edges (its id is the redacted ref). role
// comes from delegation in/out degree; schedule_status/health surface the
// governed-schedule cadence-miss the module owns and can prove (no fabricated
// finding-derived health).
type nodeDTO struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Ref            string `json:"ref"`
	Role           string `json:"role"`
	ScheduleStatus string `json:"schedule_status"` // "none" | "active" | "paused" | "missed"
	Health         string `json:"health"`          // "ok" | "missed"
}

// coverageDTO declares the honest provenance + caveats of a graph response.
type coverageDTO struct {
	Source  string   `json:"source"`
	Caveats []string `json:"caveats"`
}

// graphResponse is the React-Flow data contract: a deduplicated node set, the edges
// between them, the coverage descriptor and the keyset pagination envelope.
type graphResponse struct {
	Nodes    []nodeDTO   `json:"nodes"`
	Edges    []edgeDTO   `json:"edges"`
	Coverage coverageDTO `json:"coverage"`
	Cursor   string      `json:"cursor,omitempty"`
	HasMore  bool        `json:"has_more"`
}

// targetKindFor maps a link kind to the kind of its worker node.
func targetKindFor(linkKind string) string {
	switch linkKind {
	case linkMCPServer:
		return "mcp_server"
	case linkMCPTool:
		return "mcp_tool"
	default:
		// delegation worker and a2a peer are both agent nodes.
		return nodeAgent
	}
}

// sourceKindFor maps a link kind to the kind of its source node. Delegation and MCP
// edges originate from a session; an A2A edge originates from an agent (the calling
// peer), so the source must NOT be mislabeled a session (AIP-05).
func sourceKindFor(linkKind string) string {
	if linkKind == linkA2A {
		return nodeAgent
	}
	return nodeSession
}

// toGraphResponse builds the node+edge contract from relation rows, deriving each
// node once by ref. Delegation degree decides role; schedStatus (built from the
// active schedules of this tenant) decides schedule_status/health; an edge is
// animated when its last_seen is within the active window.
func toGraphResponse(edges []edgeDTO, schedStatus map[string]string, clock model.Clock, active map[string]bool) graphResponse {
	out := graphResponse{
		Nodes:    []nodeDTO{},
		Edges:    []edgeDTO{},
		Coverage: coverageDTO{Source: "edge.observed", Caveats: []string{coverageNote}},
	}
	type deg struct{ in, out int }
	degree := map[string]deg{}
	kind := map[string]string{}
	order := []string{}
	note := func(ref, k string) {
		if ref == "" {
			return
		}
		if _, seen := kind[ref]; !seen {
			kind[ref] = k
			order = append(order, ref)
		}
	}
	for _, e := range edges {
		e.Animated = active[e.ID]
		note(e.Source, sourceKindFor(e.LinkKind))
		note(e.Target, targetKindFor(e.LinkKind))
		if e.LinkKind == linkDelegation {
			d := degree[e.Source]
			d.out++
			degree[e.Source] = d
			t := degree[e.Target]
			t.in++
			degree[e.Target] = t
		}
		out.Edges = append(out.Edges, e)
	}
	for _, ref := range order {
		ss := schedStatus[ref]
		if ss == "" {
			ss = "none"
		}
		health := "ok"
		if ss == "missed" {
			health = "missed"
		}
		out.Nodes = append(out.Nodes, nodeDTO{
			ID: ref, Kind: kind[ref], Ref: ref,
			Role: roleOf(degree[ref].in, degree[ref].out), ScheduleStatus: ss, Health: health,
		})
	}
	return out
}

// roleOf derives a node's role from its delegation in/out degree.
func roleOf(in, out int) string {
	switch {
	case out > 0 && in == 0:
		return roleSupervisor
	case in > 0 && out == 0:
		return roleWorker
	default:
		return rolePeer
	}
}

// flowDTO is one derived multi-agent flow: a supervisor and the workers it
// delegates to, with a read-time-derived lifecycle state.
type flowDTO struct {
	SupervisorRef   string   `json:"supervisor_ref"`
	Workers         []string `json:"workers"`
	WorkerCount     int      `json:"worker_count"`
	DelegationTotal int64    `json:"delegation_total"`
	State           string   `json:"state"` // active | idle | stalled | completed
	FirstSeenAt     string   `json:"first_seen_at"`
	LastSeenAt      string   `json:"last_seen_at"`
}

// timelineDTO is one chronological entry in a subject's orchestration history,
// merged from relation activity and the decision ledger.
type timelineDTO struct {
	At     string `json:"at"`
	Kind   string `json:"kind"` // "delegation" | "mcp_server" | "mcp_tool" | "<decision op>"
	Source string `json:"source,omitempty"`
	Target string `json:"target,omitempty"`
	Title  string `json:"title"`
}

// scheduleDTO projects a governed schedule with its derived health.
type scheduleDTO struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	SubjectKind   string `json:"subject_kind"`
	SubjectRef    string `json:"subject_ref"`
	TriggerKind   string `json:"trigger_kind"`
	CadenceSpec   string `json:"cadence_spec,omitempty"`
	ExpectedIvl   int64  `json:"expected_interval_seconds"`
	GraceFactor   int64  `json:"grace_factor"`
	DesiredStatus string `json:"desired_status"`
	OwnerActor    string `json:"owner_actor"`
	LastFiredAt   string `json:"last_fired_at,omitempty"`
	LastObserved  string `json:"last_observed_at,omitempty"`
	MissedAt      string `json:"missed_at,omitempty"`
	Health        string `json:"health"` // active | paused | retired | stalled
	CreatedAt     string `json:"created_at"`
}

// toScheduleDTO projects a schedule record, deriving its health from desired_status
// and the sticky cadence-miss marker. observedAt is the subject's last observed
// activity, derived at read time from the relation table (not a stored column).
func toScheduleDTO(rec model.Record, observedAt string) scheduleDTO {
	health := rec.String(colDesiredStat)
	if health == "active" && rec.String(colMissedAt) != "" {
		health = stateStalled
	}
	return scheduleDTO{
		ID:            rec.String(model.ColID),
		Name:          rec.String(colSchedName),
		SubjectKind:   rec.String(colSubjectKind),
		SubjectRef:    rec.String(colSubjectRef),
		TriggerKind:   rec.String(colTriggerKind),
		CadenceSpec:   rec.String(colCadenceSpec),
		ExpectedIvl:   rec.Int(colExpectedIvl),
		GraceFactor:   rec.Int(colGraceFactor),
		DesiredStatus: rec.String(colDesiredStat),
		OwnerActor:    rec.String(colOwnerActor),
		LastFiredAt:   rec.String(colLastFiredAt),
		LastObserved:  observedAt,
		MissedAt:      rec.String(colMissedAt),
		Health:        health,
		CreatedAt:     rec.String(model.ColCreatedAt),
	}
}

// decisionDTO projects one append-only fire/miss governance-evidence row.
type decisionDTO struct {
	ID          string `json:"id"`
	SubjectKind string `json:"subject_kind"`
	SubjectRef  string `json:"subject_ref"`
	ScheduleRef string `json:"schedule_ref,omitempty"`
	Op          string `json:"op"`
	PlanHash    string `json:"plan_hash,omitempty"`
	ApprovalRef string `json:"approval_ref,omitempty"`
	GateStatus  string `json:"gate_status"`
	OpStatus    string `json:"op_status"`
	DispatchRef string `json:"dispatch_ref,omitempty"`
	Actor       string `json:"actor"`
	ActorKind   string `json:"actor_kind"`
	Result      string `json:"result,omitempty"`
	OccurredAt  string `json:"occurred_at"`
}

func toDecisionDTO(rec model.Record) decisionDTO {
	return decisionDTO{
		ID:          rec.String(model.ColID),
		SubjectKind: rec.String(colDecSubjectKind),
		SubjectRef:  rec.String(colDecSubjectRef),
		ScheduleRef: rec.String(colScheduleRef),
		Op:          rec.String(colOp),
		PlanHash:    rec.String(colPlanHash),
		ApprovalRef: rec.String(colApprovalRef),
		GateStatus:  rec.String(colGateStatus),
		OpStatus:    rec.String(colOpStatus),
		DispatchRef: rec.String(colDispatchRef),
		Actor:       rec.String(colActor),
		ActorKind:   rec.String(colActorKind),
		Result:      rec.String(colResult),
		OccurredAt:  rec.String(colOccurredAt),
	}
}
