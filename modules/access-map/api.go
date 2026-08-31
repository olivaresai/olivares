// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"net/http"
	"strconv"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// edgeFilterColumns are the AccessEdge columns a caller may filter the graph and
// drift views by. The store validates every filter column against the entity
// descriptor, so an unlisted/typo column is rejected before SQL is built; this
// list is the documented, supported surface (and keeps the handler honest).
var edgeFilterColumns = []string{"origin_kind", "origin_id", "resource_id", "mode", "confidence", "signal_source"}

// handleGraph returns the R/RW graph as a React Flow node+edge contract, filtered
// by any of the supported edge columns. The read is privileged and self-audited
// (in m.Graph); the engine has already checked permGraphRead and pinned the tenant.
func (m *Module) handleGraph(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := edgeQuery(r)
	edges, err := m.Graph(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), q)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toGraphResponse(edges))
}

// handleNeighbors returns the edges touching one node (id required) in a
// direction (outgoing|incoming|both, default both), as the same node+edge
// contract. Privileged and self-audited (in m.AuditedNeighbors).
func (m *Module) handleNeighbors(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(r.URL.Query().Get("id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("id query parameter is required"))
		return
	}
	node := model.NodeRef{Kind: r.URL.Query().Get("kind"), ID: id}
	edges, err := m.AuditedNeighbors(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), node, direction(r.URL.Query().Get("direction")))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toGraphResponse(edges))
}

// handleDrift returns the permitted-vs-observed least-privilege drift: unexpected
// accesses (observed, not permitted — the headline) and unused grants. Privileged
// and self-audited (in m.Diff).
func (m *Module) handleDrift(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := edgeQuery(r)
	diff, err := m.Diff(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), q)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDiffResponse(diff))
}

// edgeQuery builds a List/Drift query from the supported edge filters plus
// ?limit and ?cursor. Only the allow-listed columns are honored.
func edgeQuery(r *http.Request) model.Query {
	q := model.Query{}
	for _, col := range edgeFilterColumns {
		if v := r.URL.Query().Get(col); v != "" {
			q.Filters = append(q.Filters, eq(col, v))
		}
	}
	if c := r.URL.Query().Get("cursor"); c != "" {
		q.Cursor = c
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			q.Limit = n
		}
	}
	return q
}

// direction maps a query value to a graph traversal direction (default both).
func direction(s string) model.Direction {
	switch s {
	case string(model.Outgoing):
		return model.Outgoing
	case string(model.Incoming):
		return model.Incoming
	default:
		return model.Both
	}
}

// writeStoreError maps a store error to an HTTP status. THE MAPPING ITSELF IS NOT
// HERE: it is api.StoreErrorStatus (core/api/moduleerrors.go), which derives the
// status from the same statusFor that answers core/api's own routes. This module
// therefore cannot answer a sentinel differently from core, or from the other
// thirty-five copies of this function, and a sentinel added to statusFor tomorrow
// reaches this module without anyone editing it.
//
// That is not hypothetical: on 2026-08-12 four sentinels core/api had long mapped —
// tenant_suspended, tenant_not_in_service, not_leader and residency_violation —
// were absent from all but two of the thirty-six copies, so the same refusal was
// answered 423/503/403 by a core route and 500 "internal error" by every module
// route. The per-arm reasoning (ADR-0024 Q2 for the audit spool/B-03 for
// workspace confinement for the standby) now lives beside statusFor, once.
func writeStoreError(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	status, msg, _ := api.StoreErrorStatus(err)
	writeJSON(w, status, errorBody(msg))
}
