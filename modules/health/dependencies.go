// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// handleDependencies returns the dependency map as a React-Flow node+edge graph: a
// page of auto-discovered dependency edges, plus the deduplicated node set derived
// from them, each node annotated with its subject's health. A node is "healthy"/
// "degraded"/… when a declared check tracks it; "observed" when an edge on this page
// proves it alive but no check is declared (honest: seen alive, health not measured);
// and "unknown" when it is only ever named (e.g. a delegation target) with no
// liveness evidence. Privileged read.
func (m *Module) handleDependencies(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	resp := depGraphResponse{Nodes: []depNodeDTO{}, Edges: []dependencyDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(dependencyKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		resp.Cursor, resp.HasMore = page.Cursor, page.HasMore

		// Health annotation: the current state of every checked subject in the
		// tenant, keyed by ref. Read once, applied to the nodes below.
		health, err := m.subjectHealth(r.Context(), sc)
		if err != nil {
			return err
		}
		// Liveness annotation: the subjects this page's edges prove alive, with the
		// SAME origin-vs-target asymmetry as ingest's aliveSubjects (the MCP server a
		// uses_mcp edge touched, and the agent ORIGIN that acted — never a bare
		// delegation/tool target). Lets an untracked-but-live node read "observed"
		// instead of collapsing to "unknown".
		alive := aliveRefsFromDeps(recs)

		seen := map[string]bool{}
		note := func(kind, ref string) {
			if ref == "" || seen[ref] {
				return
			}
			seen[ref] = true
			state := healthOr(health[ref])
			if health[ref] == "" && alive[ref] {
				// No declared check, but observed alive on this page: honest
				// intermediate, not fabricated "healthy", not silent "unknown".
				state = stateObserved
			}
			resp.Nodes = append(resp.Nodes, depNodeDTO{ID: ref, Kind: kind, Ref: ref, Health: state})
		}
		for _, rec := range recs {
			dep := toDependencyDTO(rec)
			resp.Edges = append(resp.Edges, dep)
			note(dep.FromKind, dep.Source)
			note(dep.ToKind, dep.Target)
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// aliveRefsFromDeps returns the set of subject refs that this page of dependency
// edges proves alive, mirroring ingest's aliveSubjects exactly so the "observed"
// annotation never over-claims: an MCP server is alive iff it is the target of a
// uses_mcp edge (it was touched), and an agent is alive iff it is the ORIGIN of an
// edge (it acted). A delegation TARGET or an mcp_tool target is only named — not
// proven alive — and is deliberately excluded.
func aliveRefsFromDeps(recs []model.Record) map[string]bool {
	alive := map[string]bool{}
	for _, rec := range recs {
		dep := toDependencyDTO(rec)
		if dep.FromKind == subjAgent && dep.Source != "" {
			alive[dep.Source] = true
		}
		if dep.Relation == relUsesMCP && dep.Target != "" {
			alive[dep.Target] = true
		}
	}
	return alive
}

// subjectHealth returns the current state of every checked subject in the tenant,
// keyed by subject ref, so dependency-map nodes can be annotated without a query
// per node.
func (m *Module) subjectHealth(ctx context.Context, sc store.Scope) (map[string]string, error) {
	repo, err := sc.Ext(checkKind)
	if err != nil {
		return nil, err
	}
	checks, err := listAll(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(checks))
	for _, c := range checks {
		out[c.String(colSubjectRef)] = stateOr(c.String(colLastState))
	}
	return out, nil
}

// healthOr returns s, or "unknown" when no check tracks the subject.
func healthOr(s string) string {
	if s == "" {
		return stateUnknown
	}
	return s
}
