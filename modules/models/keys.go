// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// maxHintLen bounds the masked-hint field. A hint is a short masked partial the
// provider Admin API returns (e.g. "sk-ant-…AbCd"); a value longer than this is
// rejected as a defensive guard against an operator pasting a full credential
// into a field that is meant to hold only a non-sensitive display hint.
const maxHintLen = 64

// keyRefDTO is a governance reference to a provider API key or workspace. It is
// MINIMAL-DATA: there is deliberately no field that can hold a usable credential
// — only the masked Hint, which is safe to display (docs/SECURITY-HARDENING.md).
type keyRefDTO struct {
	ID           string `json:"id,omitempty"`
	RefKind      string `json:"ref_kind"`
	ProviderRef  string `json:"provider_ref"`
	ExtID        string `json:"ext_id"`
	Name         string `json:"name"`
	WorkspaceRef string `json:"workspace_ref,omitempty"`
	Status       string `json:"status"`
	Hint         string `json:"hint,omitempty"`
	OwnerRef     string `json:"owner_ref,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// validate checks an incoming key reference and normalizes it. It enforces the
// minimal-data invariant (no full credential in the hint) and the required keys.
func (d *keyRefDTO) validate() string {
	d.RefKind = strings.TrimSpace(d.RefKind)
	switch d.RefKind {
	case keyRefAPIKey, keyRefWorkspace:
	default:
		return "ref_kind must be api_key or workspace"
	}
	d.ProviderRef = strings.TrimSpace(d.ProviderRef)
	if d.ProviderRef == "" {
		return "provider_ref is required"
	}
	d.ExtID = strings.TrimSpace(d.ExtID)
	if d.ExtID == "" {
		return "ext_id is required"
	}
	if len(d.Hint) > maxHintLen {
		return "hint must be a short masked partial, never a full credential"
	}
	if d.Status == "" {
		d.Status = "active"
	}
	return ""
}

// toRecord renders the DTO to a store Record (entity columns only; the engine
// stamps the base columns).
func (d keyRefDTO) toRecord() model.Record {
	rec := model.Record{
		colProviderRef:  d.ProviderRef,
		colRefKind:      d.RefKind,
		colExtID:        d.ExtID,
		colKeyName:      d.Name,
		colWorkspaceRef: d.WorkspaceRef,
		colKeyStatus:    d.Status,
		colHint:         d.Hint,
		colOwnerRef:     d.OwnerRef,
	}
	if ts, err := model.ParseTimestamp(d.CreatedAt); err == nil && !ts.IsZero() {
		rec[colCreatedAt] = ts.String()
	}
	return rec
}

// toKeyRefDTO renders a stored record to the DTO.
func toKeyRefDTO(rec model.Record) keyRefDTO {
	return keyRefDTO{
		ID:           rec.String(model.ColID),
		RefKind:      rec.String(colRefKind),
		ProviderRef:  rec.String(colProviderRef),
		ExtID:        rec.String(colExtID),
		Name:         rec.String(colKeyName),
		WorkspaceRef: rec.String(colWorkspaceRef),
		Status:       rec.String(colKeyStatus),
		Hint:         rec.String(colHint),
		OwnerRef:     rec.String(colOwnerRef),
		CreatedAt:    rec.String(colCreatedAt),
	}
}

// handleListKeys lists key/workspace governance references, optionally filtered
// by ref_kind, provider_ref or status.
func (m *Module) handleListKeys(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("ref_kind"); v != "" {
		q.Filters = append(q.Filters, eq(colRefKind, v))
	}
	if v := r.URL.Query().Get("provider_ref"); v != "" {
		q.Filters = append(q.Filters, eq(colProviderRef, v))
	}
	if v := r.URL.Query().Get("status"); v != "" {
		q.Filters = append(q.Filters, eq(colKeyStatus, v))
	}
	out := listResponse[keyRefDTO]{Items: []keyRefDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(keyRefKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toKeyRefDTO(rec))
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

// handleCreateKey registers a new key/workspace governance reference.
func (m *Module) handleCreateKey(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in keyRefDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out keyRefDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(keyRefKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), in.toRecord())
		if err != nil {
			return err
		}
		out = toKeyRefDTO(rec)
		return auditKey(r.Context(), sc, mc, "create", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleUpdateKey updates a key/workspace governance reference in place.
func (m *Module) handleUpdateKey(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in keyRefDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out keyRefDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(keyRefKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		// Apply the mutable fields onto the loaded record (preserving base columns
		// so the optimistic-concurrency version check holds).
		for k, v := range in.toRecord() {
			rec[k] = v
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toKeyRefDTO(rec)
		return auditKey(r.Context(), sc, mc, "update", id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteKey removes a key/workspace governance reference.
func (m *Module) handleDeleteKey(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(keyRefKind)
		if err != nil {
			return err
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditKey(r.Context(), sc, mc, "delete", id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// auditKey appends a key-governance audit event attributed to the real principal,
// in the caller's transaction — so the evidence ledger records WHO changed which
// credential reference (docs/SECURITY-HARDENING.md self-audit), not the system actor.
func auditKey(ctx context.Context, sc store.Scope, mc api.ModuleContext, verb string, id model.ID) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     "models.key_ref." + verb,
		TargetKind: keyRefKind,
		TargetID:   id,
	})
	return err
}
