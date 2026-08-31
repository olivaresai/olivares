// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Prompt statuses (the colStatus column on a prompt).
const (
	promptActive     = "active"
	promptDeprecated = "deprecated"
)

// promptRequest creates a prompt with its first revision. The template is REDACTED
// before storage (a template can carry a secret example, docs/SECURITY-HARDENING.md).
type promptRequest struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	Label    string `json:"label,omitempty"`
	Note     string `json:"note,omitempty"`
}

// revisionRequest appends an immutable revision to an existing prompt.
type revisionRequest struct {
	Template string `json:"template"`
	Label    string `json:"label,omitempty"`
	Note     string `json:"note,omitempty"`
}

// rollbackRequest points a prompt's current revision at a prior (existing) revision.
type rollbackRequest struct {
	Rev int64 `json:"rev"`
}

type promptDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CurrentRev int64  `json:"current_rev"`
	LatestHash string `json:"latest_hash,omitempty"`
	Status     string `json:"status"`
}

type revisionDTO struct {
	PromptID     string `json:"prompt_id"`
	Rev          int64  `json:"rev"`
	Label        string `json:"label,omitempty"`
	Template     string `json:"template"` // redacted
	TemplateHash string `json:"template_hash"`
	Note         string `json:"note,omitempty"`
	CreatedBy    string `json:"created_by"`
}

func toPromptDTO(rec model.Record) promptDTO {
	return promptDTO{
		ID: rec.String(model.ColID), Name: rec.String(colName), CurrentRev: rec.Int(colCurrentRev),
		LatestHash: rec.String(colLatestHash), Status: rec.String(colStatus),
	}
}

func toRevisionDTO(rec model.Record) revisionDTO {
	return revisionDTO{
		PromptID: rec.String(colPromptRef), Rev: rec.Int(colRevNum), Label: rec.String(colLabel),
		Template: rec.String(colTemplate), TemplateHash: rec.String(colTemplateHash), Note: rec.String(colNote),
		CreatedBy: rec.String(colCreatedBy),
	}
}

// validateTemplate redacts and bounds a prompt template, returning the clean
// template or a non-empty client message.
func (m *Module) validateTemplate(raw string) (string, string) {
	if strings.TrimSpace(raw) == "" {
		return "", "template is required"
	}
	if len(raw) > maxTemplateLen {
		return "", "template too long"
	}
	clean, _, _ := m.scrubWith(raw)
	return clean, ""
}

// handleCreatePrompt creates a prompt and its first immutable revision.
func (m *Module) handleCreatePrompt(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req promptRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > maxNameLen {
		writeJSON(w, http.StatusBadRequest, errorBody("name is required and must be short"))
		return
	}
	clean, msg := m.validateTemplate(req.Template)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out promptDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		pRepo, err := sc.Ext(promptKind)
		if err != nil {
			return err
		}
		if _, ok, err := findOne(r.Context(), pRepo, eq(colName, req.Name)); err != nil {
			return err
		} else if ok {
			return store.ErrConflict
		}
		hash := hashHex(clean)
		prompt, err := pRepo.Create(r.Context(), model.Record{
			colName: req.Name, colCurrentRev: int64(1), colLatestHash: hash, colOwnerRef: mc.Principal.Actor(), colStatus: promptActive,
		})
		if err != nil {
			return err
		}
		pid := model.ID(prompt.String(model.ColID))
		if err := m.appendRevision(r.Context(), sc, pid, 1, req.Label, clean, hash, req.Note, mc.Principal.Actor()); err != nil {
			return err
		}
		out = toPromptDTO(prompt)
		return auditEvent(r.Context(), sc, mc, "knowledge.prompt.create", promptKind, pid, map[string]any{"name": req.Name, "rev": 1})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleAddRevision appends an immutable revision and advances the current pointer.
func (m *Module) handleAddRevision(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req revisionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	clean, msg := m.validateTemplate(req.Template)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var newRev int64
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		pRepo, err := sc.Ext(promptKind)
		if err != nil {
			return err
		}
		prompt, err := pRepo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		revRepo, err := sc.Ext(revisionKind)
		if err != nil {
			return err
		}
		// rev_num is monotone = count of existing revisions + 1 (revisions are
		// append-only, never deleted, so count == max — robust against rollback).
		// This counts-then-inserts within the writer's transaction; on the
		// single-node SQLite deployment the single writer serializes it. On Postgres
		// with concurrent writers two racing appends could compute the same rev_num;
		// the UNIQUE (tenant_id, prompt_ref, rev_num) index is the backstop (the
		// loser's insert is rejected — no duplicate/corruption, the caller retries).
		// Tightening this to a row-locked counter is the same Postgres-concurrency
		// follow-up documented for the bootstrap advisory lock.
		existing, err := listAll(r.Context(), revRepo, eq(colPromptRef, id.String()))
		if err != nil {
			return err
		}
		newRev = int64(len(existing)) + 1
		hash := hashHex(clean)
		if err := m.appendRevision(r.Context(), sc, id, newRev, req.Label, clean, hash, req.Note, mc.Principal.Actor()); err != nil {
			return err
		}
		prompt[colCurrentRev] = newRev
		prompt[colLatestHash] = hash
		if _, err := pRepo.Update(r.Context(), prompt); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "knowledge.prompt.revision", promptKind, id, map[string]any{"rev": newRev})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"prompt_id": id.String(), "rev": newRev})
}

// appendRevision writes one immutable revision row (append-only).
func (m *Module) appendRevision(ctx context.Context, sc store.Scope, promptID model.ID, rev int64, label, template, hash, note, actor string) error {
	repo, err := sc.Ext(revisionKind)
	if err != nil {
		return err
	}
	_, err = repo.Create(ctx, model.Record{
		colPromptRef: promptID.String(), colRevNum: rev, colLabel: label, colTemplate: template,
		colTemplateHash: hash, colNote: note, colCreatedBy: actor,
	})
	return err
}

// handleRollback points the prompt's current revision at a prior, existing
// revision. Revisions are immutable; rollback only moves the pointer (history
// retained). The named revision must exist.
func (m *Module) handleRollback(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req rollbackRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Rev <= 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("rev must be a positive revision number"))
		return
	}
	notFoundRev := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		pRepo, err := sc.Ext(promptKind)
		if err != nil {
			return err
		}
		prompt, err := pRepo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		revRepo, err := sc.Ext(revisionKind)
		if err != nil {
			return err
		}
		rec, ok, err := findOne(r.Context(), revRepo, eq(colPromptRef, id.String()), eq(colRevNum, req.Rev))
		if err != nil {
			return err
		}
		if !ok {
			notFoundRev = true
			return nil
		}
		prompt[colCurrentRev] = req.Rev
		prompt[colLatestHash] = rec.String(colTemplateHash)
		if _, err := pRepo.Update(r.Context(), prompt); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "knowledge.prompt.rollback", promptKind, id, map[string]any{"rev": req.Rev})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFoundRev {
		writeJSON(w, http.StatusBadRequest, errorBody("no such revision"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompt_id": id.String(), "current_rev": req.Rev})
}

// handleGetPrompt returns one prompt (its current pointer + status).
func (m *Module) handleGetPrompt(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out promptDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(promptKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		found, out = true, toPromptDTO(rec)
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

// handleListPrompts lists prompts.
func (m *Module) handleListPrompts(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	out := listResponse[promptDTO]{Items: []promptDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(promptKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toPromptDTO(rec))
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

// handleListRevisions lists a prompt's immutable revision history.
func (m *Module) handleListRevisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	out := listResponse[revisionDTO]{Items: []revisionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(revisionKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colPromptRef, id.String()))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toRevisionDTO(rec))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetRevision returns one immutable revision by number.
func (m *Module) handleGetRevision(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	rev, err := strconv.ParseInt(chi.URLParam(r, "rev"), 10, 64)
	if err != nil || rev <= 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid revision"))
		return
	}
	var out revisionDTO
	found := false
	verr := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(revisionKind)
		if err != nil {
			return err
		}
		rec, ok, err := findOne(r.Context(), repo, eq(colPromptRef, id.String()), eq(colRevNum, rev))
		if err != nil || !ok {
			return err
		}
		found, out = true, toRevisionDTO(rec)
		return nil
	})
	if verr != nil {
		writeStoreError(w, verr)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}
