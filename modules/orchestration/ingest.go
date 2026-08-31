// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Link kinds for a derived relation edge.
const (
	linkDelegation = "delegation" // supervisor session → worker (subagent), via the Task tool
	linkMCPServer  = "mcp_server" // session → MCP server (a comms/capability endpoint)
	linkMCPTool    = "mcp_tool"   // session → a specific MCP tool
	linkA2A        = "a2a"        // agent → agent, peer-to-peer A2A communication (a2a connector, AIP-05)
)

// resA2AAgent is the EdgeObservation ResourceKind the a2a connector emits for a
// remote/peer agent (origin "agent" → resource "a2a.agent"). It is matched here so
// the comm graph spans non-Claude agents (module IV no longer admits "no a2a
// connector"; see dto.go coverageNote).
const resA2AAgent = "a2a.agent"

// resGenAIAgent is the EdgeObservation ResourceKind the Claude connector emits for a
// nested sub-agent observed via the OpenTelemetry GenAI conventions (OBS-01,
// connectors/claude/genai.go, opt-in profile). Matched so cooperative delegation
// spans non-Claude sub-agents too — a session delegating through the gen_ai
// convention — not only the Claude Code Agent/Task tool. Structural/attribution
// only: the worker is the redacted agent ref, never any message content.
const resGenAIAgent = "genai.agent"

// isDelegationTool reports whether a Claude Code tool name spawns a sub-agent. The
// CURRENT tool is "Agent"; "Task" is the legacy name — both emit the same
// subagent_type and produce an agent.task edge (verified vs
// https://code.claude.com/docs/en/monitoring-usage, jun-2026). Accepting both means
// delegation observed via the current tool name is classified as delegation, not
// mis-filed as a generic tool-usage edge (the bug before only matched "Task").
func isDelegationTool(toolRef string) bool {
	return toolRef == "Task" || toolRef == "Agent"
}

// linkClass is the classification of an observed edge into a comm-graph relation.
type linkClass struct {
	kind    string
	worker  string
	toolRef string
	ok      bool
}

// classifyLink maps an observed edge to a communication/delegation relation, or
// ok=false when the edge is not agent communication this graph models. The live
// signals (OBS-01/AIP-05): Claude Code sub-agent delegation (the current Agent
// tool and legacy Task tool, supervisor→worker), gen_ai nested-agent delegation
// (non-Claude sub-agents), MCP topology (session↔server/tool), and peer-to-peer A2A
// (agent→agent). Everything else — file/shell/http access, identity attribution — is
// ignored: that is inventory/sessions territory, not the comm graph. Structural and
// attribution only; never a payload, prompt or tool argument.
func classifyLink(e sdkmodel.EdgeObservation) linkClass {
	switch {
	case e.OriginKind == "session" && e.ResourceKind == "agent.task" && isDelegationTool(e.ToolRef):
		// supervisor session → worker subagent, via the current "Agent" tool (or the
		// legacy "Task"). toolRef carries the actual tool name observed, not a hardcode.
		return linkClass{kind: linkDelegation, worker: e.ResourceRef, toolRef: e.ToolRef, ok: e.ResourceRef != ""}
	case e.OriginKind == "session" && e.ResourceKind == resGenAIAgent:
		// gen_ai nested-agent delegation (OBS-01): a session delegated to a non-Claude
		// sub-agent observed via the OpenTelemetry GenAI conventions. ToolRef carries the
		// gen_ai tool/operation when named.
		return linkClass{kind: linkDelegation, worker: e.ResourceRef, toolRef: e.ToolRef, ok: e.ResourceRef != ""}
	case e.ResourceKind == "mcp.server":
		return linkClass{kind: linkMCPServer, worker: e.ResourceRef, toolRef: "", ok: e.ResourceRef != ""}
	case e.ResourceKind == "mcp.tool":
		return linkClass{kind: linkMCPTool, worker: e.ResourceRef, toolRef: e.ToolRef, ok: e.ResourceRef != ""}
	case e.ResourceKind == resA2AAgent:
		// AIP-05: an A2A peer-to-peer communication edge (agent → agent), observed by
		// the a2a connector. ToolRef carries the skill, when named.
		return linkClass{kind: linkA2A, worker: e.ResourceRef, toolRef: e.ToolRef, ok: e.ResourceRef != ""}
	default:
		return linkClass{}
	}
}

// onEdge folds one observed edge into the communication/delegation graph: it
// upserts the supervisor→worker relation (accumulating the delegation count and
// recency) and publishes a snapshot to the live SSE broker. It persists the
// RELATION only — never the payload, the prompt or the tool arguments. Schedule
// liveness is NOT touched here: a scheduled subject's observed activity is derived
// at read time from this same relation table (the cadence scan), so a high-volume
// edge stream costs one upsert, not a schedule query per edge.
func (m *Module) onEdge(ctx context.Context, tenantRef string, edge sdkmodel.EdgeObservation) error {
	if edge.OriginRef == "" {
		return nil
	}
	link := classifyLink(edge)
	if !link.ok {
		return nil
	}
	tenant, ok := tenantOf(tenantRef)
	if !ok {
		return nil
	}
	at := nonZeroTime(edge.ObservedAt, m.clock)

	var snap *relSnapshot
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		dto, err := m.upsertRelation(ctx, sc, edge.OriginRef, link, edge, at)
		if err != nil {
			return err
		}
		s := relSnapshot{tenant: tenant, dto: dto}
		snap = &s
		return nil
	})
	if err == nil && snap != nil {
		m.broker.publish(*snap)
	}
	return err
}

// upsertRelation find-or-creates the relation row for (supervisor, worker,
// link_kind, tool_ref) and accumulates its count and recency, returning the
// projected edge DTO. It is idempotent within the single subscriber goroutine and
// backed by the unique index across restarts (a raced/redelivered create that hits
// the index re-reads and updates, mirroring sessions.upsertLive).
func (m *Module) upsertRelation(ctx context.Context, sc store.Scope, supervisor string, link linkClass, edge sdkmodel.EdgeObservation, at time.Time) (edgeDTO, error) {
	repo, err := sc.Ext(relationKind)
	if err != nil {
		return edgeDTO{}, err
	}
	supervisor = clamp(supervisor, maxRefLen)
	worker := clamp(link.worker, maxRefLen)
	toolRef := clamp(link.toolRef, maxNameLen)
	filters := []model.Filter{
		eq(colSupervisorRef, supervisor), eq(colWorkerRef, worker),
		eq(colLinkKind, link.kind), eq(colToolRef, toolRef),
	}
	apply := func(rec model.Record) {
		rec[colDelegationCnt] = rec.Int(colDelegationCnt) + 1
		rec[colMode] = string(edge.Mode)
		rec[colSignalSource] = string(edge.Source)
		rec[colConfidence] = string(edge.Confidence)
		advanceLast(rec, colLastSeenAt, at)
	}

	if rec, ok, err := findOne(ctx, repo, filters...); err != nil {
		return edgeDTO{}, err
	} else if ok {
		apply(rec)
		updated, err := repo.Update(ctx, rec)
		if err != nil {
			return edgeDTO{}, err
		}
		return toEdgeDTO(updated), nil
	}

	atTS := model.NewTimestamp(at).String()
	rec := model.Record{
		colSupervisorRef: supervisor, colWorkerRef: worker, colLinkKind: link.kind, colToolRef: toolRef,
		colDelegationCnt: int64(0), colFirstSeenAt: atTS, colLastSeenAt: atTS,
	}
	apply(rec)
	created, err := repo.Create(ctx, rec)
	if err == nil {
		return toEdgeDTO(created), nil
	}
	// A redelivered/raced create can hit the unique index; re-read and update.
	if errors.Is(err, store.ErrConflict) {
		if again, ok, lerr := findOne(ctx, repo, filters...); lerr != nil {
			return edgeDTO{}, lerr
		} else if ok {
			apply(again)
			updated, uerr := repo.Update(ctx, again)
			if uerr != nil {
				return edgeDTO{}, uerr
			}
			return toEdgeDTO(updated), nil
		}
	}
	return edgeDTO{}, err
}
