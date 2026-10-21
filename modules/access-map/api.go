// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// edgeFilterColumns are the AccessEdge columns a caller may filter the graph and
// drift views by. The store validates every filter column against the entity
// descriptor, so an unlisted/typo column is rejected before SQL is built; this
// list is the documented, supported surface (and keeps the handler honest).
var edgeFilterColumns = []string{"origin_kind", "origin_id", "resource_id", "mode", "confidence", "signal_source"}

// handleGraph returns the R/RW graph as a React Flow node+edge contract, filtered
// by any of the supported edge columns. The read is privileged and self-audited
// (in m.Graph); the engine has already checked permGraphRead and pinned the tenant.
// Invalid limits retain the default page and carry a deprecation Warning header.
func (m *Module) handleGraph(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q, err := edgeQuery(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	edges, page, err := m.graphPage(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), q)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toGraphResponse(edges, page))
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
	dir, err := direction(r.URL.Query().Get("direction"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	node := model.NodeRef{Kind: r.URL.Query().Get("kind"), ID: id}
	edges, page, err := m.neighborsPage(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), node, dir)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toGraphResponse(edges, page))
}

// handleDrift returns the permitted-vs-observed least-privilege drift: unexpected
// accesses (observed, not permitted — the headline) and unused grants. Privileged
// and self-audited (in m.Diff). The edge filters narrow it; limit and cursor do
// not: the whole drift window is reconciled (drainDrift), because a reconciliation
// over a page fabricates drift. Paging parameters remain accepted with a Warning
// header; omit them to reconcile the complete window.
func (m *Module) handleDrift(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if r.URL.Query().Has("limit") || r.URL.Query().Has("cursor") {
		w.Header().Add("Warning", `299 olivares "Drift paging is deprecated and ignored; the complete window is reconciled."`)
	}
	q, err := edgeQuery(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	q.Limit, q.Cursor = 0, "" // deprecated paging is ignored: the whole window is reconciled
	diff, err := m.Diff(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), q)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDiffResponse(diff))
}

// edgeQuery builds a List/Drift query from the supported edge filters plus
// ?limit and ?cursor. Only the allow-listed columns are honored. Invalid limits
// retain the published default-page behavior with a deprecation warning.
func edgeQuery(w http.ResponseWriter, r *http.Request) (model.Query, error) {
	q := model.Query{}
	if v := r.URL.Query().Get("mode"); v != "" && !sdkmodel.AccessMode(v).Valid() {
		return q, errors.New("mode must be one of unknown, read, write, readwrite")
	}
	if v := r.URL.Query().Get("confidence"); v != "" && !sdkmodel.Confidence(v).Valid() {
		return q, errors.New("confidence must be one of attributed, approximate")
	}
	for _, col := range edgeFilterColumns {
		if v := r.URL.Query().Get(col); v != "" {
			q.Filters = append(q.Filters, eq(col, v))
		}
	}
	if c := r.URL.Query().Get("cursor"); c != "" {
		q.Cursor = c
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 0 {
			w.Header().Add("Warning", `299 olivares "Invalid limit is deprecated; use a non-negative integer. The default page is returned."`)
			return q, nil
		}
		q.Limit = n
	}
	return q, nil
}

// direction validates a graph traversal direction (empty defaults to both).
func direction(s string) (model.Direction, error) {
	switch s {
	case "", string(model.Both):
		return model.Both, nil
	case string(model.Outgoing), string(model.Incoming):
		return model.Direction(s), nil
	default:
		return "", errors.New("direction must be one of outgoing, incoming, both")
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
// route. The per-arm reasoning (audit-spool policy for audit spool capacity and
// standby workspace confinement) now lives beside statusFor, once.
func writeStoreError(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	status, body, _ := api.StoreErrorBody(err)
	writeJSON(w, status, body)
}
