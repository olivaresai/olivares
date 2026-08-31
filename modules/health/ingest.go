// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Dependency relation kinds (the dependency map edges auto-discovered from
// observed activity).
const (
	relUsesMCP     = "uses_mcp"     // a session/agent uses an MCP server
	relUsesTool    = "uses_tool"    // a session/agent uses an MCP tool
	relDelegatesTo = "delegates_to" // a supervisor session delegates to a worker (Task)
)

// Subject kinds this module tracks the health of.
const (
	subjAgent = "agent"
	subjMCP   = "mcp"
)

// subjectRef is one (kind, ref) a check may monitor.
type subjectRefT struct {
	kind string
	ref  string
}

// aliveSubjects returns the subjects an observed edge is positive evidence are
// alive: the MCP server a session/agent just touched, and the agent if the origin
// IS an agent. These are the subjects whose check (if declared) the edge refreshes.
func aliveSubjects(e sdkmodel.EdgeObservation) []subjectRefT {
	var out []subjectRefT
	if e.ResourceKind == "mcp.server" && e.ResourceRef != "" {
		out = append(out, subjectRefT{kind: subjMCP, ref: e.ResourceRef})
	}
	if e.OriginKind == "agent" && e.OriginRef != "" {
		out = append(out, subjectRefT{kind: subjAgent, ref: e.OriginRef})
	}
	return out
}

// dependency is one classified dependency edge derived from an observation.
type dependency struct {
	fromKind, fromRef string
	toKind, toRef     string
	relation          string
	ok                bool
}

// classifyDependency maps an observed edge to a dependency-map relation, or
// ok=false when the edge is not a dependency this map models. It mirrors the
// signals orchestration derives: MCP topology and sub-agent delegation
// (the CURRENT "Agent" tool and the legacy "Task" — both emit an agent.task edge,
// kept in sync with orchestration.isDelegationTool).
func classifyDependency(e sdkmodel.EdgeObservation) dependency {
	if e.OriginRef == "" {
		return dependency{}
	}
	fromKind := e.OriginKind
	if fromKind == "" {
		fromKind = "session"
	}
	switch {
	case e.ResourceKind == "mcp.server" && e.ResourceRef != "":
		return dependency{fromKind: fromKind, fromRef: e.OriginRef, toKind: subjMCP, toRef: e.ResourceRef, relation: relUsesMCP, ok: true}
	case e.ResourceKind == "mcp.tool" && e.ResourceRef != "":
		return dependency{fromKind: fromKind, fromRef: e.OriginRef, toKind: "mcp_tool", toRef: e.ResourceRef, relation: relUsesTool, ok: true}
	case e.ResourceKind == "agent.task" && (e.ToolRef == "Task" || e.ToolRef == "Agent") && e.ResourceRef != "":
		return dependency{fromKind: fromKind, fromRef: e.OriginRef, toKind: subjAgent, toRef: e.ResourceRef, relation: relDelegatesTo, ok: true}
	default:
		return dependency{}
	}
}

// onEdge folds one observed edge into the dependency map (always) and into the
// health of any DECLARED check for the subjects it proves alive (liveness refresh
// → recovery). It persists relations and status only — never the payload, the
// prompt or the tool arguments. Findings/SSE are emitted after the commit.
func (m *Module) onEdge(ctx context.Context, tenant model.TenantID, edge sdkmodel.EdgeObservation) error {
	m.markSeen(tenant)
	at := nonZeroTime(edge.ObservedAt, m.clock)

	var trans []transition
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if dep := classifyDependency(edge); dep.ok {
			if err := m.upsertDependencyTx(ctx, sc, dep, at); err != nil {
				return err
			}
		}
		for _, subj := range aliveSubjects(edge) {
			t, tracked, err := m.refreshLivenessTx(ctx, sc, subj.kind, subj.ref, at)
			if err != nil {
				return err
			}
			if tracked {
				trans = append(trans, t)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, t := range trans {
		m.publishTransition(ctx, tenant, t)
	}
	return nil
}

// refreshLivenessTx refreshes the health of a subject's DECLARED check on positive
// liveness evidence. tracked=false when no check exists for the subject (the
// dependency map still records the edge; health is only tracked for declared
// checks). A paused/retired check is left untouched.
func (m *Module) refreshLivenessTx(ctx context.Context, sc store.Scope, subjectKind, subjectRef string, at time.Time) (transition, bool, error) {
	repo, err := sc.Ext(checkKind)
	if err != nil {
		return transition{}, false, err
	}
	rec, ok, err := findOne(ctx, repo, eq(colSubjectKind, subjectKind), eq(colSubjectRef, subjectRef))
	if err != nil || !ok {
		return transition{}, false, err
	}
	if rec.String(colDesiredStat) != "active" {
		return transition{}, false, nil
	}
	t, err := m.applyStateTx(ctx, sc, rec, stateHealthy, causeEdge, -1, "", at)
	if err != nil {
		return transition{}, false, err
	}
	if _, err := repo.Update(ctx, rec); err != nil {
		return transition{}, false, err
	}
	return t, true, nil
}

// upsertDependencyTx find-or-creates the dependency edge for (from_ref, to_ref,
// relation) and accumulates its observed_count and recency, returning nothing —
// the dependency map is read on demand. Idempotent within the single subscriber
// goroutine and backed by the unique index across restarts (a raced/redelivered
// create that hits the index re-reads and updates, mirroring the AccessEdge merge).
func (m *Module) upsertDependencyTx(ctx context.Context, sc store.Scope, dep dependency, at time.Time) error {
	repo, err := sc.Ext(dependencyKind)
	if err != nil {
		return err
	}
	fromRef := clamp(dep.fromRef, maxRefLen)
	toRef := clamp(dep.toRef, maxRefLen)
	filters := []model.Filter{eq(colDepFromRef, fromRef), eq(colDepToRef, toRef), eq(colDepRelation, dep.relation)}
	apply := func(rec model.Record) {
		rec[colDepObserved] = rec.Int(colDepObserved) + 1
		advanceLast(rec, colDepLastAt, at)
	}

	if rec, ok, err := findOne(ctx, repo, filters...); err != nil {
		return err
	} else if ok {
		apply(rec)
		_, err := repo.Update(ctx, rec)
		return err
	}

	atTS := model.NewTimestamp(at).String()
	rec := model.Record{
		colDepFromKind: dep.fromKind, colDepFromRef: fromRef,
		colDepToKind: dep.toKind, colDepToRef: toRef, colDepRelation: dep.relation,
		colDepObserved: int64(0), colDepFirstAt: atTS, colDepLastAt: atTS,
	}
	apply(rec)
	if _, err := repo.Create(ctx, rec); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrConflict) {
		return err
	}
	// A redelivered/raced create can hit the unique index; re-read and update.
	if again, ok, lerr := findOne(ctx, repo, filters...); lerr != nil {
		return lerr
	} else if ok {
		apply(again)
		_, uerr := repo.Update(ctx, again)
		return uerr
	}
	return nil
}

// publishTransition emits the bus finding and the SSE snapshot for a state CHANGE
// (a same-state refresh publishes nothing). Best-effort: a publish failure is
// logged, never swallowed, but the caller's primary outcome does not depend on it.
func (m *Module) publishTransition(ctx context.Context, tenant model.TenantID, t transition) {
	if !t.happened {
		return
	}
	m.emitFinding(ctx, tenant, t.kind, t.severity, t.subjectKind, t.subjectRef, t.title, t.detail)
	m.broker.publish(statusSnapshot{tenant: tenant, dto: t.snapshot})
}
