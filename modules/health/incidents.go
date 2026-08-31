// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// handleListIncidents lists incidents, optionally filtered by ?state (open|
// resolved), ?subject_kind and ?subject_ref.
func (m *Module) handleListIncidents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if st := r.URL.Query().Get("state"); st != "" {
		q.Filters = append(q.Filters, eq(colInState, st))
	}
	if sk := r.URL.Query().Get("subject_kind"); sk != "" {
		q.Filters = append(q.Filters, eq(colInSubjectKind, sk))
	}
	if sr := r.URL.Query().Get("subject_ref"); sr != "" {
		q.Filters = append(q.Filters, eq(colInSubjectRef, sr))
	}
	out := []incidentDTO{}
	var page model.Page
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(incidentKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		page = p
		for _, rec := range recs {
			out = append(out, toIncidentDTO(rec))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[incidentDTO]{Items: out, Cursor: page.Cursor, HasMore: page.HasMore})
}

// handleGetIncident returns one incident.
func (m *Module) handleGetIncident(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto incidentDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(incidentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = toIncidentDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleResolveIncident manually resolves an open incident — an operator
// acknowledgement that the period is closed. It does NOT assert the subject
// recovered (recovery comes from a real liveness signal); it only closes the
// incident record. Admin-tier, self-audited. Resolving an already-resolved or
// absent incident is a no-op 200 / 404 respectively.
func (m *Module) handleResolveIncident(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	at := m.clock.Now().Time()
	var dto incidentDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(incidentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if rec.String(colInState) == "open" {
			rec[colInState] = "resolved"
			rec[colInResolvedAt] = model.NewTimestamp(at).String()
			rec, err = repo.Update(r.Context(), rec)
			if err != nil {
				return err
			}
			if err := auditEvent(r.Context(), sc, mc, "health.incident.resolve", incidentKind, id, map[string]any{
				"subject_kind": rec.String(colInSubjectKind), "subject_ref": clamp(rec.String(colInSubjectRef), maxRefLen),
			}); err != nil {
				return err
			}
		}
		dto = toIncidentDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}
