// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// instanceDTO is a self-service instantiation of a catalog entry: which approved
// entry version it came from, its target and its governance status.
type instanceDTO struct {
	ID           string `json:"id,omitempty"`
	EntryID      string `json:"entry_id"`
	EntryKind    string `json:"entry_kind,omitempty"`
	EntrySlug    string `json:"entry_slug,omitempty"`
	EntryVersion string `json:"entry_version,omitempty"`
	Name         string `json:"name"`
	TargetRef    string `json:"target_ref,omitempty"`
	Status       string `json:"status,omitempty"`
	RequestedBy  string `json:"requested_by,omitempty"`
	DecidedBy    string `json:"decided_by,omitempty"`
	Note         string `json:"note,omitempty"`
}

func toInstanceDTO(rec model.Record) instanceDTO {
	return instanceDTO{
		ID: rec.String(model.ColID), EntryID: rec.String(colEntryID),
		EntryKind: rec.String(colEntryKind), EntrySlug: rec.String(colEntrySlug),
		EntryVersion: rec.String(colEntryVersion), Name: rec.String(colInstName),
		TargetRef: rec.String(colTargetRef), Status: rec.String(colInstStatus),
		RequestedBy: rec.String(colRequestedBy), DecidedBy: rec.String(colDecidedBy),
		Note: rec.String(colNote),
	}
}

// instantiateDTO is the request body to instantiate an approved entry.
type instantiateDTO struct {
	Name      string `json:"name"`
	TargetRef string `json:"target_ref,omitempty"`
	Note      string `json:"note,omitempty"`
}

// handleInstantiate creates a self-service instantiation request FROM an approved
// catalog entry. It records the request, its provenance (the exact entry version)
// and a self-audit. The approval DECISION (HITL/policy) is governance's and
// the actual provisioning is deployment's; this module exposes the flow and
// records it, it does not enforce the approval policy.
func (m *Module) handleInstantiate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	entryID := model.ID(chi.URLParam(r, "id"))
	if entryID.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in instantiateDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("name is required"))
		return
	}
	var (
		out          instanceDTO
		notApproved  bool
		entryMissing bool
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		entries, err := sc.Ext(entryKind)
		if err != nil {
			return err
		}
		entry, err := entries.Get(r.Context(), entryID)
		if err != nil {
			if isNotFound(err) {
				entryMissing = true
				return nil
			}
			return err
		}
		if entry.String(colStatus) != statusApproved {
			notApproved = true
			return nil
		}
		repo, err := sc.Ext(instanceKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), model.Record{
			colEntryID:      entryID.String(),
			colEntryKind:    entry.String(colEntryKind),
			colEntrySlug:    entry.String(colSlug),
			colEntryVersion: entry.String(colVersion),
			colInstName:     in.Name,
			colTargetRef:    in.TargetRef,
			colInstStatus:   instRequested,
			colRequestedBy:  mc.Principal.Actor(),
			colNote:         in.Note,
		})
		if err != nil {
			return err
		}
		out = toInstanceDTO(rec)
		return auditInstance(r.Context(), sc, mc, "instantiate", model.ID(rec.String(model.ColID)), map[string]any{
			"entry_id": entryID.String(), "entry_slug": entry.String(colSlug), "entry_version": entry.String(colVersion),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if entryMissing {
		writeJSON(w, http.StatusNotFound, errorBody("catalog entry not found"))
		return
	}
	if notApproved {
		writeJSON(w, http.StatusConflict, errorBody("only an approved catalog entry can be instantiated"))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleListInstances lists instantiations, optionally filtered by status/entry_id.
func (m *Module) handleListInstances(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("status"); v != "" {
		q.Filters = append(q.Filters, eq(colInstStatus, v))
	}
	if v := r.URL.Query().Get("entry_id"); v != "" {
		q.Filters = append(q.Filters, eq(colEntryID, v))
	}
	out := listResponse[instanceDTO]{Items: []instanceDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(instanceKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toInstanceDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetInstance returns one instance.
func (m *Module) handleGetInstance(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out   instanceDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(instanceKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		found, out = true, toInstanceDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// transitionDTO is the request body to move an instance through its governance
// flow.
type transitionDTO struct {
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// instanceTransitions encodes the allowed governance flow. The module enforces a
// sane state machine; WHO may transition and under what policy is governance's
// — exposed here as an admin-tier, audited action.
var instanceTransitions = map[string]map[string]bool{
	instRequested: {instApproved: true, instRejected: true},
	instApproved:  {instActive: true, instRejected: true},
}

// handleTransitionInstance records a governance decision on an instance (approve/
// reject/activate). It is privileged (admin-tier) and self-audited.
func (m *Module) handleTransitionInstance(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in transitionDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Status = strings.TrimSpace(strings.ToLower(in.Status))
	if in.Status != instApproved && in.Status != instRejected && in.Status != instActive {
		writeJSON(w, http.StatusBadRequest, errorBody("status must be one of approved, rejected, active"))
		return
	}
	var (
		out     instanceDTO
		badFlow bool
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(instanceKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		cur := rec.String(colInstStatus)
		if !instanceTransitions[cur][in.Status] {
			badFlow = true
			return nil
		}
		rec[colInstStatus] = in.Status
		rec[colDecidedBy] = mc.Principal.Actor()
		if in.Note != "" {
			rec[colNote] = in.Note
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toInstanceDTO(rec)
		return auditInstance(r.Context(), sc, mc, "transition", id, map[string]any{"from": cur, "to": in.Status})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if badFlow {
		writeJSON(w, http.StatusConflict, errorBody("invalid status transition for this instance"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// auditInstance appends an instance-governance audit event attributed to the real
// principal, in the caller's transaction (docs/SECURITY-HARDENING.md self-audit).
func auditInstance(ctx context.Context, sc store.Scope, mc api.ModuleContext, verb string, id model.ID, meta map[string]any) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     "catalog.instance." + verb,
		TargetKind: instanceKind,
		TargetID:   id,
		Meta:       meta,
	})
	return err
}
