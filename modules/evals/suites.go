// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the SUITE surface: the versioned golden dataset definitions and their
// immutable cases. A suite is mutable (its lifecycle: active→archived); a case is
// append-only (correcting a case is a new suite_version, never an in-place edit —
// contract §2.1). All caller-supplied fixture text is clamped before Create
// (minimal-data, docs/SECURITY-HARDENING.md).

// validSubjectKinds are the subjects a suite may evaluate.
var validSubjectKinds = map[string]bool{
	"agent": true, "model": true, "prompt": true, "session": true, "sandbox_run": true,
}

// ---- suite DTOs ------------------------------------------------------------------

type suiteDTO struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Description   string  `json:"description,omitempty"`
	SubjectKind   string  `json:"subject_kind"`
	Scorer        string  `json:"scorer"`
	Criterion     string  `json:"criterion,omitempty"`
	PassThreshold float64 `json:"pass_threshold"`
	RegThreshold  float64 `json:"regression_threshold"`
	JudgeModel    string  `json:"judge_model,omitempty"`
	SuiteVersion  int64   `json:"suite_version"`
	Status        string  `json:"status"`
}

func toSuiteDTO(rec model.Record) suiteDTO {
	return suiteDTO{
		ID: rec.String(model.ColID), Name: rec.String(colName), Description: rec.String(colDescr),
		SubjectKind: rec.String(colSubjKind), Scorer: rec.String(colScorer), Criterion: rec.String(colCriterion),
		PassThreshold: rec.Float(colPassThresh), RegThreshold: rec.Float(colRegThresh),
		JudgeModel: rec.String(colJudgeModel), SuiteVersion: rec.Int(colSuiteVer), Status: rec.String(colSuiteStat),
	}
}

type createSuiteRequest struct {
	Name          string  `json:"name"`
	Description   string  `json:"description,omitempty"`
	SubjectKind   string  `json:"subject_kind"`
	Scorer        string  `json:"scorer"`
	Criterion     string  `json:"criterion,omitempty"`
	PassThreshold float64 `json:"pass_threshold,omitempty"`
	RegThreshold  float64 `json:"regression_threshold,omitempty"`
	JudgeModel    string  `json:"judge_model,omitempty"`
	SuiteVersion  int64   `json:"suite_version,omitempty"`
}

// handleCreateSuite creates a versioned golden suite. Write-tier + self-audited.
func (m *Module) handleCreateSuite(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req createSuiteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := clamp(strings.TrimSpace(req.Name), maxNameLen)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("name is required"))
		return
	}
	subjectKind := strings.TrimSpace(req.SubjectKind)
	if !validSubjectKinds[subjectKind] {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_kind must be agent, model, prompt, session or sandbox_run"))
		return
	}
	scorer := strings.TrimSpace(req.Scorer)
	if scorer == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("scorer is required"))
		return
	}
	version := req.SuiteVersion
	if version <= 0 {
		version = 1
	}
	pass := req.PassThreshold
	if pass <= 0 {
		pass = 1.0 // default: every scored case must pass
	}
	var out suiteDTO
	conflict := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(suiteKind)
		if err != nil {
			return err
		}
		created, err := repo.Create(r.Context(), model.Record{
			colName: name, colDescr: clamp(strings.TrimSpace(req.Description), maxFixtureLen),
			colSubjKind: subjectKind, colScorer: scorer,
			colCriterion:  clamp(strings.TrimSpace(req.Criterion), maxFixtureLen),
			colPassThresh: pass, colRegThresh: req.RegThreshold,
			colJudgeModel: clamp(strings.TrimSpace(req.JudgeModel), maxRefLen),
			colSuiteVer:   version, colSuiteStat: "active",
		})
		if err != nil {
			if isConflict(err) {
				conflict = true
				return nil
			}
			return err
		}
		out = toSuiteDTO(created)
		return auditEvent(r.Context(), sc, mc, "evals.suite.create", suiteKind, model.ID(created.String(model.ColID)),
			map[string]any{"name": name, "suite_version": version, "scorer": scorer})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if conflict {
		writeJSON(w, http.StatusConflict, errorBody("a suite with this name and suite_version already exists"))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleListSuites lists the tenant's suites (filterable by name/status).
func (m *Module) handleListSuites(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("name")); v != "" {
		q.Filters = append(q.Filters, eq(colName, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		q.Filters = append(q.Filters, eq(colSuiteStat, v))
	}
	out := listResponse[suiteDTO]{Items: []suiteDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(suiteKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toSuiteDTO(rec))
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

// handleGetSuite returns one suite.
func (m *Module) handleGetSuite(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid suite_id"))
		return
	}
	var out suiteDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(suiteKind)
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
		found, out = true, toSuiteDTO(rec)
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

// handleArchiveSuite marks a suite archived. Admin-tier + self-audited.
func (m *Module) handleArchiveSuite(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid suite_id"))
		return
	}
	var out suiteDTO
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(suiteKind)
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
		rec[colSuiteStat] = "archived"
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		found, out = true, toSuiteDTO(updated)
		return auditEvent(r.Context(), sc, mc, "evals.suite.archive", suiteKind, id, map[string]any{"name": rec.String(colName)})
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

// ---- case DTOs -------------------------------------------------------------------

type caseDTO struct {
	ID           string         `json:"id"`
	SuiteRef     string         `json:"suite_ref"`
	SuiteVersion int64          `json:"suite_version"`
	CaseKey      string         `json:"case_key"`
	Input        string         `json:"input"`
	Expected     string         `json:"expected,omitempty"`
	Weight       float64        `json:"weight"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

func toCaseDTO(rec model.Record) caseDTO {
	return caseDTO{
		ID: rec.String(model.ColID), SuiteRef: rec.String(colSuiteRef), SuiteVersion: rec.Int(colSuiteVer),
		CaseKey: rec.String(colCaseKey), Input: rec.String(colInput), Expected: rec.String(colExpected),
		Weight: rec.Float(colWeight), Metadata: decodeJSONMap(rec.String(colCaseMeta)),
	}
}

type addCaseRequest struct {
	CaseKey  string         `json:"case_key"`
	Input    string         `json:"input"`
	Expected string         `json:"expected,omitempty"`
	Weight   float64        `json:"weight,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// handleAddCase appends a golden case to a suite (append-only — a fix is a new
// suite_version). The fixture text is clamped before Create. Write-tier + audited.
func (m *Module) handleAddCase(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	suiteID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid suite_id"))
		return
	}
	var req addCaseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	caseKey := clamp(strings.TrimSpace(req.CaseKey), maxNameLen)
	if caseKey == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("case_key is required"))
		return
	}
	weight := req.Weight
	if weight <= 0 {
		weight = 1.0
	}
	var out caseDTO
	notFound, conflict := false, false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		suiteRepo, err := sc.Ext(suiteKind)
		if err != nil {
			return err
		}
		suiteRec, err := suiteRepo.Get(r.Context(), suiteID)
		if err != nil {
			if isNotFound(err) {
				notFound = true
				return nil
			}
			return err
		}
		caseRepo, err := sc.Ext(caseKind)
		if err != nil {
			return err
		}
		created, err := caseRepo.Create(r.Context(), model.Record{
			colSuiteRef: suiteID.String(), colSuiteVer: suiteRec.Int(colSuiteVer), colCaseKey: caseKey,
			colInput: clamp(req.Input, maxFixtureLen), colExpected: clamp(req.Expected, maxFixtureLen),
			colWeight: weight, colCaseMeta: encodeJSONMap(req.Metadata),
		})
		if err != nil {
			if isConflict(err) {
				conflict = true
				return nil
			}
			return err
		}
		out = toCaseDTO(created)
		return auditEvent(r.Context(), sc, mc, "evals.case.add", caseKind, model.ID(created.String(model.ColID)),
			map[string]any{"suite_ref": suiteID.String(), "case_key": caseKey})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFound {
		writeJSON(w, http.StatusNotFound, errorBody("suite not found"))
		return
	}
	if conflict {
		writeJSON(w, http.StatusConflict, errorBody("a case with this case_key already exists in the suite"))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleListCases lists a suite's cases (ordered by case_key for a stable view).
func (m *Module) handleListCases(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	suiteID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid suite_id"))
		return
	}
	out := listResponse[caseDTO]{Items: []caseDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(caseKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colSuiteRef, suiteID.String()))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toCaseDTO(rec))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].CaseKey < out.Items[j].CaseKey })
	writeJSON(w, http.StatusOK, out)
}
