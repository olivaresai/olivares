// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// auditRead seals a privileged comm-graph read to the append-only ledger,
// attributed to the real principal (docs/SECURITY-HARDENING.md: who viewed which agents talk to
// whom is itself recorded). It runs in a committed Mutate (an Append inside a View
// would roll back); a failed audit denies the read rather than serving an
// unaudited view of a recon-sensitive graph.
func (m *Module) auditRead(r *http.Request, mc api.ModuleContext, action string, meta map[string]any) error {
	return mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		_, err := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: action, TargetKind: relationKind, Meta: meta,
		})
		return err
	})
}

// scheduleStatusMap builds subject_ref → schedule status ("missed" when an active
// schedule's sticky marker is set, else its desired_status) for the tenant, so the
// graph can annotate a node with its governed-schedule health.
func (m *Module) scheduleStatusMap(ctx context.Context, sc store.Scope) (map[string]string, error) {
	repo, err := sc.Ext(scheduleKind)
	if err != nil {
		return nil, err
	}
	all, err := listAll(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(all))
	for _, s := range all {
		ref := s.String(colSubjectRef)
		status := s.String(colDesiredStat)
		if status == "active" && s.String(colMissedAt) != "" {
			status = "missed"
		}
		// A subject with multiple schedules surfaces the most severe status.
		if prev, ok := out[ref]; !ok || rankStatus(status) > rankStatus(prev) {
			out[ref] = status
		}
	}
	return out, nil
}

func rankStatus(s string) int {
	switch s {
	case "missed":
		return 3
	case "active":
		return 2
	case "paused":
		return 1
	default:
		return 0
	}
}

// handleGraph returns the React-Flow communication/delegation graph, derived from
// the relation table, with honest coverage. It is a privileged, self-audited read
// and runs the read-time cadence scan first so a just-missed schedule is reflected.
func (m *Module) handleGraph(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.runCadenceScan(r.Context(), mc)
	if err := m.auditRead(r, mc, "orchestration.graph.read", map[string]any{"scope": "graph"}); err != nil {
		writeStoreError(w, err)
		return
	}
	q := listQuery(r)
	q.Filters = append(q.Filters, edgeFilters(r)...)
	resp := graphResponse{}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(relationKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		schedStatus, err := m.scheduleStatusMap(r.Context(), sc)
		if err != nil {
			return err
		}
		edges, active := m.projectEdges(recs)
		resp = toGraphResponse(edges, schedStatus, m.clock, active)
		resp.Cursor, resp.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleNeighbors returns the subgraph around one node (its incoming and/or
// outgoing relations) — a privileged, self-audited read.
func (m *Module) handleNeighbors(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	node := r.URL.Query().Get("node")
	if node == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("node is required"))
		return
	}
	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "both"
	}
	if err := m.auditRead(r, mc, "orchestration.graph.neighbors", map[string]any{"node": clamp(node, maxRefLen), "direction": direction}); err != nil {
		writeStoreError(w, err)
		return
	}
	resp := graphResponse{}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(relationKind)
		if err != nil {
			return err
		}
		var recs []model.Record
		if direction == "outgoing" || direction == "both" {
			out, lerr := listAll(r.Context(), repo, eq(colSupervisorRef, node))
			if lerr != nil {
				return lerr
			}
			recs = append(recs, out...)
		}
		if direction == "incoming" || direction == "both" {
			in, lerr := listAll(r.Context(), repo, eq(colWorkerRef, node))
			if lerr != nil {
				return lerr
			}
			recs = append(recs, in...)
		}
		schedStatus, err := m.scheduleStatusMap(r.Context(), sc)
		if err != nil {
			return err
		}
		edges, active := m.projectEdges(recs)
		resp = toGraphResponse(edges, schedStatus, m.clock, active)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleFlows returns the derived multi-agent flows (a supervisor and the workers it
// delegates to) with a read-time-derived lifecycle state — a privileged, self-audited
// read. A ?state filter narrows the result in memory.
func (m *Module) handleFlows(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.runCadenceScan(r.Context(), mc)
	if err := m.auditRead(r, mc, "orchestration.flows.read", map[string]any{"scope": "flows"}); err != nil {
		writeStoreError(w, err)
		return
	}
	stateFilter := r.URL.Query().Get("state")
	out := listResponse[flowDTO]{Items: []flowDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		flows, err := m.deriveFlows(r.Context(), sc)
		if err != nil {
			return err
		}
		for _, f := range flows {
			if stateFilter != "" && f.State != stateFilter {
				continue
			}
			out.Items = append(out.Items, f)
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTimeline returns one subject's merged orchestration history (its delegation
// activity and its fire/miss decisions) in reverse-chronological order — a
// privileged, self-audited read.
func (m *Module) handleTimeline(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	subject := chi.URLParam(r, "subject")
	if subject == "" {
		subject = r.URL.Query().Get("subject")
	}
	if subject == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("subject is required"))
		return
	}
	if err := m.auditRead(r, mc, "orchestration.timeline.read", map[string]any{"subject": clamp(subject, maxRefLen)}); err != nil {
		writeStoreError(w, err)
		return
	}
	out := listResponse[timelineDTO]{Items: []timelineDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		entries, err := m.deriveTimeline(r.Context(), sc, subject)
		if err != nil {
			return err
		}
		out.Items = entries
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// projectEdges maps relation records to edge DTOs and computes, per edge, whether it
// is "animated" (last_seen within the active window) for the live view.
func (m *Module) projectEdges(recs []model.Record) ([]edgeDTO, map[string]bool) {
	edges := make([]edgeDTO, 0, len(recs))
	active := make(map[string]bool, len(recs))
	for _, rec := range recs {
		d := toEdgeDTO(rec)
		if deriveRecency(d.LastSeenAt, m.clock, m.activeWindow, m.idleWindow) == stateActive {
			active[d.ID] = true
		}
		edges = append(edges, d)
	}
	return edges, active
}

// edgeFilters builds the optional edge filters a /graph request may carry.
func edgeFilters(r *http.Request) []model.Filter {
	var f []model.Filter
	if v := r.URL.Query().Get("supervisor"); v != "" {
		f = append(f, eq(colSupervisorRef, v))
	}
	if v := r.URL.Query().Get("worker"); v != "" {
		f = append(f, eq(colWorkerRef, v))
	}
	if v := r.URL.Query().Get("link_kind"); v != "" {
		f = append(f, eq(colLinkKind, v))
	}
	return f
}

// deriveFlows builds the supervisor→workers clusters from delegation relations,
// recency-sorted, with a read-time lifecycle state (stalled overrides when the
// supervisor has an active overdue schedule).
func (m *Module) deriveFlows(ctx context.Context, sc store.Scope) ([]flowDTO, error) {
	repo, err := sc.Ext(relationKind)
	if err != nil {
		return nil, err
	}
	rels, err := listAll(ctx, repo, eq(colLinkKind, linkDelegation))
	if err != nil {
		return nil, err
	}
	schedStatus, err := m.scheduleStatusMap(ctx, sc)
	if err != nil {
		return nil, err
	}
	type acc struct {
		workers   map[string]bool
		total     int64
		firstSeen string
		lastSeen  string
	}
	flows := map[string]*acc{}
	order := []string{}
	for _, rel := range rels {
		sup := rel.String(colSupervisorRef)
		a := flows[sup]
		if a == nil {
			a = &acc{workers: map[string]bool{}}
			flows[sup] = a
			order = append(order, sup)
		}
		a.workers[rel.String(colWorkerRef)] = true
		a.total += rel.Int(colDelegationCnt)
		if a.firstSeen == "" || rel.String(colFirstSeenAt) < a.firstSeen {
			a.firstSeen = rel.String(colFirstSeenAt)
		}
		a.lastSeen = maxTS(a.lastSeen, rel.String(colLastSeenAt))
	}
	out := make([]flowDTO, 0, len(order))
	for _, sup := range order {
		a := flows[sup]
		state := deriveRecency(a.lastSeen, m.clock, m.activeWindow, m.idleWindow)
		if schedStatus[sup] == "missed" {
			state = stateStalled
		}
		workers := make([]string, 0, len(a.workers))
		for wkr := range a.workers {
			workers = append(workers, wkr)
		}
		sort.Strings(workers)
		out = append(out, flowDTO{
			SupervisorRef: sup, Workers: workers, WorkerCount: len(workers), DelegationTotal: a.total,
			State: state, FirstSeenAt: a.firstSeen, LastSeenAt: a.lastSeen,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeenAt > out[j].LastSeenAt })
	return out, nil
}

// deriveTimeline merges a subject's relation activity and decision-ledger rows into
// one reverse-chronological history.
func (m *Module) deriveTimeline(ctx context.Context, sc store.Scope, subject string) ([]timelineDTO, error) {
	relRepo, err := sc.Ext(relationKind)
	if err != nil {
		return nil, err
	}
	entries := []timelineDTO{}
	for _, col := range []string{colSupervisorRef, colWorkerRef} {
		rels, lerr := listAll(ctx, relRepo, eq(col, subject))
		if lerr != nil {
			return nil, lerr
		}
		for _, rel := range rels {
			// Avoid double-counting a self-loop (subject as both ends).
			if col == colWorkerRef && rel.String(colSupervisorRef) == subject {
				continue
			}
			entries = append(entries, timelineDTO{
				At: rel.String(colLastSeenAt), Kind: rel.String(colLinkKind),
				Source: rel.String(colSupervisorRef), Target: rel.String(colWorkerRef),
				Title: rel.String(colLinkKind) + " (" + rel.String(colToolRef) + ")",
			})
		}
	}
	decRepo, err := sc.Ext(decisionKind)
	if err != nil {
		return nil, err
	}
	decs, err := listAll(ctx, decRepo, eq(colDecSubjectRef, subject))
	if err != nil {
		return nil, err
	}
	for _, d := range decs {
		entries = append(entries, timelineDTO{
			At: d.String(colOccurredAt), Kind: d.String(colOp),
			Title: d.String(colOp) + ": " + d.String(colOpStatus),
		})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].At > entries[j].At })
	return entries, nil
}
