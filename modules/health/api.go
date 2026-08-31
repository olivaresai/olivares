// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// APINamespace roots the module's routes at /v1/m/health/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so the built-in roles grant them
// by verb tier (viewer→read, editor→write, admin/owner→admin).
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permStatusRead, permCheckRead, permCheckWrite, permCheckAdmin}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check, and pins the data handle to
// the resolved tenant.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Current health, the live stream, reliability/SLA and the dependency map
	// (privileged, RBAC-gated reads; the stream open is self-audited).
	reg.Handle("GET", "/status", permStatusRead, m.handleStatus)
	reg.Handle("GET", "/stream", permStatusRead, m.handleStream)
	reg.Handle("GET", "/sla", permStatusRead, m.handleSLA)
	reg.Handle("GET", "/dependencies", permStatusRead, m.handleDependencies)
	reg.Handle("GET", "/events", permStatusRead, m.handleListEvents)
	reg.Handle("GET", "/incidents", permStatusRead, m.handleListIncidents)
	reg.Handle("GET", "/incidents/{id}", permStatusRead, m.handleGetIncident)

	// Declaring a monitored subject and posting a probe result are write-tier;
	// deleting a check or manually resolving an incident is admin-tier.
	reg.Handle("GET", "/checks", permCheckRead, m.handleListChecks)
	reg.Handle("POST", "/checks", permCheckWrite, m.handleCreateCheck)
	reg.Handle("GET", "/checks/{id}", permCheckRead, m.handleGetCheck)
	reg.Handle("PUT", "/checks/{id}", permCheckWrite, m.handleUpdateCheck)
	reg.Handle("DELETE", "/checks/{id}", permCheckAdmin, m.handleDeleteCheck)
	reg.Handle("POST", "/checks/{id}/report", permCheckWrite, m.handleReport)
	reg.Handle("POST", "/incidents/{id}/resolve", permCheckAdmin, m.handleResolveIncident)
}

// handleStatus lists the current health of monitored subjects (the check rows
// projected as status), optionally filtered by ?state and ?subject_kind.
func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if sk := r.URL.Query().Get("subject_kind"); sk != "" {
		q.Filters = append(q.Filters, eq(colSubjectKind, sk))
	}
	if st := r.URL.Query().Get("state"); st != "" {
		q.Filters = append(q.Filters, eq(colLastState, st))
	}
	m.listChecksAs(w, r, mc, q)
}

// handleListChecks lists declared checks, optionally filtered by ?subject_kind and
// ?desired_status. It shares the status projection (a check IS the status row).
func (m *Module) handleListChecks(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if sk := r.URL.Query().Get("subject_kind"); sk != "" {
		q.Filters = append(q.Filters, eq(colSubjectKind, sk))
	}
	if ds := r.URL.Query().Get("desired_status"); ds != "" {
		q.Filters = append(q.Filters, eq(colDesiredStat, ds))
	}
	m.listChecksAs(w, r, mc, q)
}

// listChecksAs runs a check List and writes the paginated status projection.
func (m *Module) listChecksAs(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, q model.Query) {
	out := []statusDTO{}
	var page model.Page
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(checkKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		page = p
		for _, rec := range recs {
			out = append(out, toStatusDTO(rec))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[statusDTO]{Items: out, Cursor: page.Cursor, HasMore: page.HasMore})
}

// handleListEvents lists the append-only reliability transition ledger, optionally
// filtered by ?subject_kind and ?subject_ref.
func (m *Module) handleListEvents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if sk := r.URL.Query().Get("subject_kind"); sk != "" {
		q.Filters = append(q.Filters, eq(colEvSubjectKind, sk))
	}
	if sr := r.URL.Query().Get("subject_ref"); sr != "" {
		q.Filters = append(q.Filters, eq(colEvSubjectRef, sr))
	}
	out := []eventDTO{}
	var page model.Page
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		page = p
		for _, rec := range recs {
			out = append(out, toEventDTO(rec))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[eventDTO]{Items: out, Cursor: page.Cursor, HasMore: page.HasMore})
}

// chiID reads the {id} path param as a validated model.ID.
func chiID(r *http.Request) (model.ID, bool) {
	return idParam(chi.URLParam(r, "id"))
}
