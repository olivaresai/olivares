// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// workflow.go — CRUD + revision history for the governed DAG workflow.
// The engine-first rule of D24: this file and the run engine landed BEFORE any
// canvas; the editor is a client of these verbs, never the other way around.

// workflowDTO projects a workflow row WITHOUT its step graph (the list shape).
type workflowDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	Version     int64  `json:"version"`
	StepCount   int    `json:"step_count"`
	PlanHash    string `json:"plan_hash"`
	OwnerActor  string `json:"owner_actor"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// workflowDetailDTO adds the full canonical step graph (the get/snapshot shape).
type workflowDetailDTO struct {
	workflowDTO
	Steps []stepDTO `json:"steps"`
}

func toWorkflowDetailDTO(rec model.Record, steps []stepDTO, targets map[string]string) workflowDetailDTO {
	id := rec.String(model.ColID)
	name := rec.String(colWfName)
	return workflowDetailDTO{
		workflowDTO: workflowDTO{
			ID:          id,
			Name:        name,
			Description: rec.String(colWfDesc),
			Enabled:     rec.Bool(colWfEnabled),
			Version:     rec.Int(model.ColVersion),
			StepCount:   len(steps),
			PlanHash:    planHashOfWorkflow(id, name, steps, targets),
			OwnerActor:  rec.String(colWfOwnerA),
			CreatedAt:   rec.String(model.ColCreatedAt),
			UpdatedAt:   rec.String(model.ColUpdatedAt),
		},
		Steps: steps,
	}
}

// resolveTargets reads the CURRENT identity of everything the graph's actuating
// steps point at, so the plan hash binds the target and not merely its name (see
// planHashOfWorkflow). For a schedule-fire step that is the schedule's own
// subject and cadence; for a notify-test step it is the route's opaque
// fingerprint (hole c1 — the notify destination MUST enter the plan hash so
// a route re-pointed between the two phases voids the approval, exactly as a
// re-pointed schedule does). A missing or unreadable target yields an empty
// binding, which changes the hash and therefore voids any approval bound to the
// previous state: the safe direction. An UNWIRED notifier yields a stable empty
// binding on both phases (it actuates nothing), so authoring still works.
func (m *Module) resolveTargets(ctx context.Context, sc store.Scope, tenant model.TenantID, steps []stepDTO) (map[string]string, error) {
	targets := map[string]string{}
	var repo store.GenericRepo
	for _, s := range steps {
		switch s.Kind {
		case stepScheduleFire:
			var cfg scheduleFireConfig
			if err := json.Unmarshal(s.Config, &cfg); err != nil {
				continue // an unparseable config binds empty
			}
			if repo == nil {
				var err error
				if repo, err = sc.Ext(scheduleKind); err != nil {
					return nil, err
				}
			}
			rec, err := repo.Get(ctx, model.ID(cfg.ScheduleID))
			if err != nil {
				if isNotFound(err) {
					continue // gone: empty binding, hash changes, approval voids
				}
				return nil, err
			}
			// The plan hash binds the EXACT canonical target string the run-creation
			// freeze and the execution-time verify also use (single source), so the
			// three can never disagree — and the EFFECTIVE dispatcher generation is
			// folded in, so re-pointing the subject to an attacker image/URL/skill
			// (a config reload) voids the approval, not merely re-cadencing it.
			targets[s.Ref] = m.scheduleTargetString(rec)
		case stepNotifyTest:
			var cfg notifyTestConfig
			if err := json.Unmarshal(s.Config, &cfg); err != nil {
				continue
			}
			fp, ok, ferr := m.notifyTest.RouteFingerprint(ctx, tenant, cfg.RouteID)
			if ferr != nil {
				// Unwired/unreachable notifier: stable empty binding on both phases
				// (nothing actuates), so authoring is unaffected.
				continue
			}
			if !ok {
				continue // route gone: empty binding, hash changes, approval voids
			}
			targets[s.Ref] = routeTargetString(fp)
		}
	}
	return targets, nil
}

// appendWfRevision snapshots the full post-state detail DTO into the
// append-only revision ledger, in the caller's open transaction (atomic with
// the mutation), attributed to the REAL principal (the rule).
func appendWfRevision(ctx context.Context, sc store.Scope, mc api.ModuleContext, wfID model.ID, op string, dto workflowDetailDTO) error {
	repo, err := sc.Ext(wfRevisionKind)
	if err != nil {
		return err
	}
	snap, err := json.Marshal(dto)
	if err != nil {
		return err
	}
	_, err = repo.Create(ctx, model.Record{
		colWfRevSubject:  wfID.String(),
		colWfRevOp:       op,
		colWfRevSnapshot: string(snap),
		colWfRevActor:    mc.Principal.Actor(),
		colWfRevActorK:   mc.Principal.ActorKind(),
	})
	return err
}

// validateNotifyRefs enforces the existing-refs rule for notify-test steps, the
// counterpart of validateScheduleRefs. The route lives in another module, so it
// resolves through the composition-root seam; an unwired or unreachable notifier
// SKIPS the check rather than rejecting, because "I cannot ask" is not evidence
// that the operator's reference is wrong (the community build has no notifier
// and must still be able to author).
func (m *Module) validateNotifyRefs(ctx context.Context, tenant model.TenantID, steps []stepDTO) *graphError {
	for _, s := range steps {
		if s.Kind != stepNotifyTest {
			continue
		}
		var cfg notifyTestConfig
		if err := json.Unmarshal(s.Config, &cfg); err != nil {
			return &graphError{StepRef: s.Ref, Message: "invalid notify-test config"}
		}
		_, ok, err := m.notifyTest.LookupRoute(ctx, tenant, cfg.RouteID)
		if err != nil {
			continue // cannot ask — do not reject
		}
		if !ok {
			return &graphError{StepRef: s.Ref, Message: "alert route " + cfg.RouteID + " does not exist"}
		}
	}
	return nil
}

// validateScheduleRefs enforces the existing-refs rule for schedule-fire steps:
// every referenced schedule must EXIST in this tenant and not be retired. It
// runs inside the mutation transaction, so the graph a revision snapshots was
// valid at commit time (execution re-checks — a schedule retired later fails
// that step honestly at run time).
func validateScheduleRefs(ctx context.Context, sc store.Scope, steps []stepDTO) (*graphError, error) {
	repo, err := sc.Ext(scheduleKind)
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		if s.Kind != stepScheduleFire {
			continue
		}
		var cfg scheduleFireConfig
		if err := json.Unmarshal(s.Config, &cfg); err != nil {
			return &graphError{StepRef: s.Ref, Message: "invalid schedule-fire config"}, nil
		}
		rec, err := repo.Get(ctx, model.ID(cfg.ScheduleID))
		if err != nil {
			if isNotFound(err) {
				return &graphError{StepRef: s.Ref, Message: "schedule " + cfg.ScheduleID + " does not exist"}, nil
			}
			return nil, err
		}
		if rec.String(colDesiredStat) == "retired" {
			return &graphError{StepRef: s.Ref, Message: "schedule " + cfg.ScheduleID + " is retired"}, nil
		}
	}
	return nil, nil
}

// writeGraphError returns a structured 400 the editor can pin to a node.
func writeGraphError(w http.ResponseWriter, ge *graphError) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{"message": ge.Error(), "step_ref": ge.StepRef},
	})
}

// createWorkflowRequest declares a workflow, optionally with its initial graph.
type createWorkflowRequest struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Enabled     *bool     `json:"enabled"`
	Steps       []stepDTO `json:"steps"`
}

// patchWorkflowRequest updates description/enabled (pointer semantics — an
// omitted field is never clobbered; steps change through the dedicated verb).
type patchWorkflowRequest struct {
	Description *string `json:"description"`
	Enabled     *bool   `json:"enabled"`
}

// putStepsRequest replaces the WHOLE step graph atomically — the graph is
// edited, validated, hashed and approved as one unit, never step-by-step.
type putStepsRequest struct {
	Steps []stepDTO `json:"steps"`
}

// handleListWorkflows lists the tenant's workflows (list shape, no graphs).
func (m *Module) handleListWorkflows(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	out := listResponse[workflowDTO]{Items: []workflowDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(workflowKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			steps, derr := decodeSteps(rec.String(colWfSteps))
			if derr != nil {
				return derr
			}
			targets, terr := m.resolveTargets(r.Context(), sc, mc.Tenant, steps)
			if terr != nil {
				return terr
			}
			out.Items = append(out.Items, toWorkflowDetailDTO(rec, steps, targets).workflowDTO)
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

// handleCreateWorkflow declares a workflow (write-tier, self-audited,
// revisioned). The declaring principal is the accountable owner_actor.
func (m *Module) handleCreateWorkflow(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in createWorkflowRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("name is required"))
		return
	}
	steps, ge := validateGraph(in.Steps, m.maxWorkflowSteps)
	if ge != nil {
		writeGraphError(w, ge)
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	var dto workflowDetailDTO
	var refErr *graphError
	overCap := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(workflowKind)
		if err != nil {
			return err
		}
		existing, err := listAll(r.Context(), repo)
		if err != nil {
			return err
		}
		if len(existing) >= m.maxWorkflows {
			overCap = true
			return nil
		}
		if refErr, err = validateScheduleRefs(r.Context(), sc, steps); err != nil || refErr != nil {
			return err
		}
		if refErr = m.validateNotifyRefs(r.Context(), mc.Tenant, steps); refErr != nil {
			return nil
		}
		rec := model.Record{
			colWfName:    clamp(in.Name, maxNameLen),
			colWfEnabled: enabled,
			colWfSteps:   encodeSteps(steps),
			colWfOwnerA:  mc.Principal.Actor(),
			colWfOwnerK:  mc.Principal.ActorKind(),
		}
		setIf(rec, colWfDesc, clamp(in.Description, maxWfDescLen))
		created, err := repo.Create(r.Context(), rec)
		if err != nil {
			return err
		}
		targets, terr := m.resolveTargets(r.Context(), sc, mc.Tenant, steps)
		if terr != nil {
			return terr
		}
		dto = toWorkflowDetailDTO(created, steps, targets)
		id := model.ID(created.String(model.ColID))
		if err := appendWfRevision(r.Context(), sc, mc, id, revOpCreate, dto); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.workflow.create", workflowKind, id,
			map[string]any{"name": dto.Name, "steps": len(steps), "enabled": enabled})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if overCap {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("workflow cap reached for this tenant"))
		return
	}
	if refErr != nil {
		writeGraphError(w, refErr)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

// getWorkflow loads one workflow row + decoded steps inside sc.
func getWorkflow(ctx context.Context, sc store.Scope, id model.ID) (model.Record, []stepDTO, bool, error) {
	repo, err := sc.Ext(workflowKind)
	if err != nil {
		return nil, nil, false, err
	}
	rec, err := repo.Get(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	steps, err := decodeSteps(rec.String(colWfSteps))
	if err != nil {
		return nil, nil, false, err
	}
	return rec, steps, true, nil
}

// workflowDetail loads one workflow with its graph AND the resolved target
// bindings, so the returned plan hash is the one an approval would be bound to.
func (m *Module) workflowDetail(ctx context.Context, sc store.Scope, tenant model.TenantID, id model.ID) (workflowDetailDTO, bool, error) {
	rec, steps, ok, err := getWorkflow(ctx, sc, id)
	if err != nil || !ok {
		return workflowDetailDTO{}, ok, err
	}
	targets, err := m.resolveTargets(ctx, sc, tenant, steps)
	if err != nil {
		return workflowDetailDTO{}, false, err
	}
	return toWorkflowDetailDTO(rec, steps, targets), true, nil
}

// handleGetWorkflow returns one workflow with its full canonical graph.
func (m *Module) handleGetWorkflow(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("workflow id required"))
		return
	}
	var dto workflowDetailDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		rec, steps, ok, err := getWorkflow(r.Context(), sc, id)
		if err != nil || !ok {
			return err
		}
		targets, terr := m.resolveTargets(r.Context(), sc, mc.Tenant, steps)
		if terr != nil {
			return terr
		}
		dto = toWorkflowDetailDTO(rec, steps, targets)
		found = true
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
	writeJSON(w, http.StatusOK, dto)
}

// handlePatchWorkflow updates description/enabled (write-tier, revisioned).
// Disabling stops NEW runs; a running run finishes (documented behavior).
func (m *Module) handlePatchWorkflow(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("workflow id required"))
		return
	}
	var in patchWorkflowRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	var dto workflowDetailDTO
	found := false
	changed := map[string]any{}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		rec, steps, ok, err := getWorkflow(r.Context(), sc, id)
		if err != nil || !ok {
			return err
		}
		if in.Description != nil {
			// Explicit set, INCLUDING to empty — clear to NULL (the schedules rule).
			if *in.Description == "" {
				rec[colWfDesc] = nil
			} else {
				rec[colWfDesc] = clamp(*in.Description, maxWfDescLen)
			}
			changed["description"] = true
		}
		if in.Enabled != nil {
			rec[colWfEnabled] = *in.Enabled
			changed["enabled"] = *in.Enabled
		}
		repo, err := sc.Ext(workflowKind)
		if err != nil {
			return err
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		targets, terr := m.resolveTargets(r.Context(), sc, mc.Tenant, steps)
		if terr != nil {
			return terr
		}
		dto = toWorkflowDetailDTO(updated, steps, targets)
		found = true
		if err := appendWfRevision(r.Context(), sc, mc, id, revOpUpdate, dto); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.workflow.update", workflowKind, id, changed)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handlePutWorkflowSteps replaces the whole step graph atomically (write-tier,
// revisioned). The new graph re-validates in full; the plan_hash changes, so
// any stale run approval is inherently void (anti-TOCTOU).
func (m *Module) handlePutWorkflowSteps(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("workflow id required"))
		return
	}
	var in putStepsRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	steps, ge := validateGraph(in.Steps, m.maxWorkflowSteps)
	if ge != nil {
		writeGraphError(w, ge)
		return
	}
	var dto workflowDetailDTO
	var refErr *graphError
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		rec, _, ok, err := getWorkflow(r.Context(), sc, id)
		if err != nil || !ok {
			return err
		}
		if refErr, err = validateScheduleRefs(r.Context(), sc, steps); err != nil || refErr != nil {
			found = true
			return err
		}
		if refErr = m.validateNotifyRefs(r.Context(), mc.Tenant, steps); refErr != nil {
			found = true
			return nil
		}
		rec[colWfSteps] = encodeSteps(steps)
		repo, err := sc.Ext(workflowKind)
		if err != nil {
			return err
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		targets, terr := m.resolveTargets(r.Context(), sc, mc.Tenant, steps)
		if terr != nil {
			return terr
		}
		dto = toWorkflowDetailDTO(updated, steps, targets)
		found = true
		if err := appendWfRevision(r.Context(), sc, mc, id, revOpUpdate, dto); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.workflow.steps", workflowKind, id,
			map[string]any{"steps": len(steps), "plan_hash": dto.PlanHash})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if refErr != nil {
		writeGraphError(w, refErr)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleListWorkflowRevisions lists a workflow's append-only revision ledger.
func (m *Module) handleListWorkflowRevisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("workflow id required"))
		return
	}
	q := listQuery(r)
	q.Filters = append(q.Filters, eq(colWfRevSubject, id.String()))
	out := listResponse[revisionDTO]{Items: []revisionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(wfRevisionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		for _, rec := range recs {
			out.Items = append(out.Items, revisionDTO{
				ID:        rec.String(model.ColID),
				Op:        rec.String(colWfRevOp),
				Snapshot:  json.RawMessage(rec.String(colWfRevSnapshot)),
				Actor:     rec.String(colWfRevActor),
				ActorKind: rec.String(colWfRevActorK),
				At:        rec.String(model.ColCreatedAt),
			})
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRestoreWorkflow re-applies the MUTABLE shape of an earlier revision —
// description, enabled, steps — through the exact same validation as the live
// verbs (the restore rule). Identity (name) never changes; version keeps
// advancing; the plan_hash consequence voids stale approvals.
func (m *Module) handleRestoreWorkflow(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("workflow id required"))
		return
	}
	var in restoreRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.RevisionID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("revision_id is required"))
		return
	}
	var dto workflowDetailDTO
	var ge *graphError
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		rec, _, ok, err := getWorkflow(r.Context(), sc, id)
		if err != nil || !ok {
			return err
		}
		revRepo, err := sc.Ext(wfRevisionKind)
		if err != nil {
			return err
		}
		revRec, err := revRepo.Get(r.Context(), model.ID(in.RevisionID))
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		if revRec.String(colWfRevSubject) != id.String() {
			// A revision of ANOTHER workflow: not found — never confirm it exists.
			return nil
		}
		var snap workflowDetailDTO
		if err := json.Unmarshal([]byte(revRec.String(colWfRevSnapshot)), &snap); err != nil {
			return err
		}
		steps, verr := validateGraph(snap.Steps, m.maxWorkflowSteps)
		if verr != nil {
			ge, found = verr, true
			return nil
		}
		if ge, err = validateScheduleRefs(r.Context(), sc, steps); err != nil || ge != nil {
			found = true
			return err
		}
		if ge = m.validateNotifyRefs(r.Context(), mc.Tenant, steps); ge != nil {
			found = true
			return nil
		}
		if snap.Description == "" {
			rec[colWfDesc] = nil
		} else {
			rec[colWfDesc] = clamp(snap.Description, maxWfDescLen)
		}
		rec[colWfEnabled] = snap.Enabled
		rec[colWfSteps] = encodeSteps(steps)
		repo, err := sc.Ext(workflowKind)
		if err != nil {
			return err
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		targets, terr := m.resolveTargets(r.Context(), sc, mc.Tenant, steps)
		if terr != nil {
			return terr
		}
		dto = toWorkflowDetailDTO(updated, steps, targets)
		found = true
		if err := appendWfRevision(r.Context(), sc, mc, id, revOpRestore, dto); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.workflow.restore", workflowKind, id,
			map[string]any{"name": dto.Name, "revision": in.RevisionID, "steps": len(steps)})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if ge != nil {
		writeGraphError(w, ge)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}
