// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

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

// stepDTO / mockDTO are the wire shapes of a scenario's synthetic fixture. They are
// clamped before persistence; no secrets (docs/SECURITY-HARDENING.md).
type stepDTO struct {
	Key   string `json:"key"`
	Input string `json:"input"`
}

type mockDTO struct {
	Resource string `json:"resource"`
	Response string `json:"response"`
}

// scenarioDTO is the wire shape of a scenario.
type scenarioDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	SubjectKind string    `json:"subject_kind,omitempty"`
	Steps       []stepDTO `json:"steps"`
	Mocks       []mockDTO `json:"mocks"`
	SpecHash    string    `json:"spec_hash,omitempty"`
	Status      string    `json:"status"`
}

func toScenarioDTO(rec model.Record) scenarioDTO {
	return scenarioDTO{
		ID: rec.String(model.ColID), Name: rec.String(colName), Description: rec.String(colDescription),
		SubjectKind: rec.String(colSubjectKind), Steps: decodeSteps(rec.String(colSteps)),
		Mocks: decodeMocks(rec.String(colMocks)), SpecHash: rec.String(colSpecHash),
		Status: rec.String(colScenStatus),
	}
}

type createScenarioRequest struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	SubjectKind string    `json:"subject_kind,omitempty"`
	Steps       []stepDTO `json:"steps,omitempty"`
	Mocks       []mockDTO `json:"mocks,omitempty"`
}

// handleCreateScenario records a synthetic, operator-authored scenario fixture. All
// caller-supplied text is clamped before Create; the spec_hash is derived from the
// canonical steps+mocks so an identical fixture is recognizable. Write-tier +
// self-audited.
func (m *Module) handleCreateScenario(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req createScenarioRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := clamp(strings.TrimSpace(req.Name), maxNameLen)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("name is required"))
		return
	}
	steps := clampSteps(req.Steps)
	mocks := clampMocks(req.Mocks)
	stepsJSON := encodeSteps(steps)
	mocksJSON := encodeMocks(mocks)
	specHash := hashHex(stepsJSON + "|" + mocksJSON)

	var out scenarioDTO
	conflict := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(scenarioKind)
		if err != nil {
			return err
		}
		created, err := repo.Create(r.Context(), model.Record{
			colName: name, colDescription: clamp(strings.TrimSpace(req.Description), maxRefLen),
			colSubjectKind: clamp(strings.TrimSpace(req.SubjectKind), maxNameLen),
			colSteps:       stepsJSON, colMocks: mocksJSON, colSpecHash: specHash,
			colScenStatus: "active",
		})
		if err != nil {
			if isConflict(err) {
				conflict = true
				return nil
			}
			return err
		}
		out = toScenarioDTO(created)
		return auditEvent(r.Context(), sc, mc, "sandbox.scenario.create", scenarioKind,
			model.ID(created.String(model.ColID)),
			map[string]any{"name": name, "steps": len(steps), "mocks": len(mocks), "spec_hash": specHash})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if conflict {
		writeJSON(w, http.StatusConflict, errorBody("a scenario with this name already exists"))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleListScenarios lists the tenant's scenarios (optionally filtered by status).
func (m *Module) handleListScenarios(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		q.Filters = append(q.Filters, eq(colScenStatus, v))
	}
	out := listResponse[scenarioDTO]{Items: []scenarioDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(scenarioKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toScenarioDTO(rec))
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

// handleGetScenario returns one scenario.
func (m *Module) handleGetScenario(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out scenarioDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(scenarioKind)
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
		found, out = true, toScenarioDTO(rec)
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

// handleArchiveScenario marks a scenario archived (admin-tier + self-audited). It is
// idempotent: archiving an archived scenario succeeds.
func (m *Module) handleArchiveScenario(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out scenarioDTO
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(scenarioKind)
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
		rec[colScenStatus] = "archived"
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		found, out = true, toScenarioDTO(updated)
		return auditEvent(r.Context(), sc, mc, "sandbox.scenario.archive", scenarioKind, id, nil)
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

// loadScenarioSpec loads a scenario and projects its persisted steps+mocks onto a
// RunSpec ready for the runner. It is the read half of a scenario run.
func loadScenarioSpec(ctx context.Context, sc store.Scope, id model.ID) (scenarioDTO, RunSpec, bool, error) {
	repo, err := sc.Ext(scenarioKind)
	if err != nil {
		return scenarioDTO{}, RunSpec{}, false, err
	}
	rec, err := repo.Get(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return scenarioDTO{}, RunSpec{}, false, nil
		}
		return scenarioDTO{}, RunSpec{}, false, err
	}
	dto := toScenarioDTO(rec)
	return dto, specOf(dto.Steps, dto.Mocks), true, nil
}

// specOf builds a RunSpec from DTO steps+mocks.
func specOf(steps []stepDTO, mocks []mockDTO) RunSpec {
	spec := RunSpec{Steps: make([]Step, len(steps)), Mocks: make([]Mock, len(mocks))}
	for i, s := range steps {
		spec.Steps[i] = Step(s)
	}
	for i, mk := range mocks {
		spec.Mocks[i] = Mock(mk)
	}
	return spec
}

// ---- step/mock clamp + (de)serialization -----------------------------------------

// clampSteps bounds each step's key/input and assigns a stable key when absent (the
// 1-based index) so every per-step output row has an identifiable step.
func clampSteps(in []stepDTO) []stepDTO {
	out := make([]stepDTO, 0, len(in))
	for i, s := range in {
		key := clamp(strings.TrimSpace(s.Key), maxNameLen)
		if key == "" {
			key = "step-" + strconv.Itoa(i+1)
		}
		out = append(out, stepDTO{Key: key, Input: clamp(s.Input, maxStepLen)})
	}
	return out
}

// clampMocks bounds each mock's resource/response.
func clampMocks(in []mockDTO) []mockDTO {
	out := make([]mockDTO, 0, len(in))
	for _, mk := range in {
		out = append(out, mockDTO{Resource: clamp(mk.Resource, maxStepLen), Response: clamp(mk.Response, maxOutputLen)})
	}
	return out
}
