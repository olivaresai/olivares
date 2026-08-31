// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// POST /baselines PINS a baseline run per (suite, subject) — the explicit reference a
// later run's regression is measured against (contract §2.5). Re-pinning replaces the
// pin and self-audits with old_run_ref + new_run_ref (the history lives in the
// append-only ledger). Admin-tier (the decision surface).

type pinBaselineRequest struct {
	SuiteRef   string `json:"suite_ref"`
	SubjectRef string `json:"subject_ref"`
	RunRef     string `json:"run_ref"`
}

type baselineDTO struct {
	ID         string `json:"id"`
	SuiteRef   string `json:"suite_ref"`
	SubjectRef string `json:"subject_ref"`
	RunRef     string `json:"run_ref"`
	PinnedBy   string `json:"pinned_by,omitempty"`
}

func toBaselineDTO(rec model.Record) baselineDTO {
	return baselineDTO{
		ID: rec.String(model.ColID), SuiteRef: rec.String(colSuiteRef), SubjectRef: rec.String(colSubjectRef),
		RunRef: rec.String(colBaseRunRef), PinnedBy: rec.String(colPinnedBy),
	}
}

// handlePinBaseline find-or-creates the (suite, subject) baseline and points it at
// run_ref, recording the previous run_ref in the self-audit.
func (m *Module) handlePinBaseline(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req pinBaselineRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	suiteID, ok := idParam(req.SuiteRef)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("suite_ref is required"))
		return
	}
	runID, ok := idParam(req.RunRef)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("run_ref is required"))
		return
	}
	subjectRef := clamp(req.SubjectRef, maxRefLen)

	var out baselineDTO
	notFound := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// The pinned run must exist (and belong to this tenant — the Scope pins it).
		runRepo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		if _, gerr := runRepo.Get(r.Context(), runID); gerr != nil {
			if isNotFound(gerr) {
				notFound = true
				return nil
			}
			return gerr
		}
		baseRepo, err := sc.Ext(baseKind)
		if err != nil {
			return err
		}
		existing, err := listAll(r.Context(), baseRepo, eq(colSuiteRef, suiteID.String()), eq(colSubjectRef, subjectRef))
		if err != nil {
			return err
		}
		oldRunRef := ""
		var rec model.Record
		if len(existing) > 0 {
			rec = existing[0]
			oldRunRef = rec.String(colBaseRunRef)
			rec[colBaseRunRef] = runID.String()
			rec[colPinnedBy] = mc.Principal.Actor()
			updated, uerr := baseRepo.Update(r.Context(), rec)
			if uerr != nil {
				return uerr
			}
			out = toBaselineDTO(updated)
		} else {
			created, cerr := baseRepo.Create(r.Context(), model.Record{
				colSuiteRef: suiteID.String(), colSubjectRef: subjectRef,
				colBaseRunRef: runID.String(), colPinnedBy: mc.Principal.Actor(),
			})
			if cerr != nil {
				return cerr
			}
			out = toBaselineDTO(created)
		}
		return auditEvent(r.Context(), sc, mc, "evals.baseline.pin", baseKind, model.ID(out.ID), map[string]any{
			"suite_ref": suiteID.String(), "subject_ref": subjectRef,
			"old_run_ref": oldRunRef, "new_run_ref": runID.String(),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFound {
		writeJSON(w, http.StatusNotFound, errorBody("run not found"))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
