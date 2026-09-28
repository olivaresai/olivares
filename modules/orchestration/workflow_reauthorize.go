// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// POST /workflows/{id}/runs/{run}/reauthorize is the owning continuation of a
// run paused for reauthentication. It binds the caller's own fresh credential
// as the run's successor binding and reopens the run, and it is the only thing
// that clears the reauthentication gate.
//
// The auth succession and the run update live in two partitions, so they are
// never one transaction and are not presented as one. The operation is a
// conditional publication in three durable writes, each safe to stop after.
// Before any of them the continuation is verified by reading only: a
// credential of another account, one wider than the run's recorded ceiling,
// or any credential for a run never bound (whose original authority nothing
// proves) is refused without writing the run.
//
//   - W0 reserves: one run write, version-checked, that requires the run to be
//     gated, the plan hash to match and no step to hold a live claim. It moves
//     the run to a new version, the attempt's generation g.
//   - W1 succeeds: core/auth writes the one successor binding for generation g,
//     for the run's own account, and supersedes the current one.
//   - W2 publishes: one run write, version-checked on g and on the old handle,
//     that stores the successor, resumes the paused steps and clears the gate.
//
// Until W2 the run stays gated and names its old binding, which no longer
// resolves after W1, so no intermediate state authorizes an effect. A stopped
// attempt leaves an unpublished successor that the next attempt, reserving a
// newer generation, supersedes. Two attempts cannot both publish: only one W0
// wins a version, and only the W1 of that version can match W2's check.

type reauthorizeRequestBody struct {
	PlanHash string `json:"plan_hash"`
}

type reauthorizeResponse struct {
	Detail string  `json:"detail"`
	Run    *runDTO `json:"run,omitempty"`
}

// reauthorizeStage names the durable points of the operation for the
// test-only stage hook.
type reauthorizeStage string

const (
	reauthorizeReserved  reauthorizeStage = "reserved"
	reauthorizeSucceeded reauthorizeStage = "succeeded"
)

var (
	errReauthorizeNotFound     = errors.New("orchestration: run not found")
	errReauthorizeNotRunning   = errors.New("orchestration: run is not running")
	errReauthorizeNotGated     = errors.New("orchestration: run is not waiting for reauthentication")
	errReauthorizePlanMismatch = errors.New("orchestration: plan hash does not match the run")
	errReauthorizeNoInitiator  = errors.New("orchestration: run has no account initiator to continue")
	errReauthorizeMoved        = errors.New("orchestration: run changed during reauthorization")
)

// reservedReauthorization is what W0 fixed: the old handle, the generation
// and the run's own account.
type reservedReauthorization struct {
	old        auth.CredentialBinding
	generation int64
	user       model.ID
	steps      int
}

// handleReauthorizeWorkflowRun continues a run paused for reauthentication: it binds
// the caller's own fresh credential as the run's successor binding and resumes the
// paused steps. A credential of another account, one wider than the run's recorded
// ceiling, or a run that was never bound is refused without writing the run.
func (m *Module) handleReauthorizeWorkflowRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("workflow id required"))
		return
	}
	runID, ok := idParam(chi.URLParam(r, "run"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("run id required"))
		return
	}
	var in reauthorizeRequestBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.PlanHash == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("plan_hash required"))
		return
	}
	if m.credentialBinder == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("credential binding is unavailable; run not reauthorized"))
		return
	}
	ctx := r.Context()

	user, err := m.reauthorizationInitiator(ctx, mc, id, runID, in.PlanHash)
	if err != nil {
		writeReauthorizeError(w, err)
		return
	}
	subject := auth.CredentialBindingSubject{
		Tenant: mc.Tenant, Kind: auth.CredentialBindingWorkflowRun, Ref: runID, User: user,
	}
	if m.writeContinuationRefusal(w, runID, m.credentialBinder.VerifyCredentialContinuation(ctx, mc.Principal, subject)) {
		return
	}
	reserved, err := m.reserveReauthorization(ctx, mc, id, runID, in.PlanHash)
	if err == nil && reserved.user != user {
		err = errReauthorizeMoved
	}
	if err != nil {
		writeReauthorizeError(w, err)
		return
	}
	if err := m.reauthorizeStageReached(reauthorizeReserved); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("reauthorization interrupted; retry"))
		return
	}
	successor, err := m.credentialBinder.RebindCredential(ctx, reserved.old, reserved.generation, mc.Principal,
		subject, mc.Principal)
	if m.writeContinuationRefusal(w, runID, err) {
		return
	}
	if err := m.reauthorizeStageReached(reauthorizeSucceeded); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("reauthorization interrupted; retry"))
		return
	}
	if err := m.publishReauthorization(ctx, mc, id, runID, in.PlanHash, reserved, successor); err != nil {
		writeReauthorizeError(w, err)
		return
	}

	m.drainRun(ctx, mc, runID, reserved.steps)
	out, err := m.readRunDTO(ctx, mc, runID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reauthorizeResponse{Detail: "run reauthorized", Run: &out})
}

// reauthorizationInitiator reads the run without writing it, applies the
// checks W0 repeats and returns the account the run continues for.
func (m *Module) reauthorizationInitiator(
	ctx context.Context,
	mc api.ModuleContext,
	workflowID, runID model.ID,
	planHash string,
) (model.ID, error) {
	var user model.ID
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, _, err := reauthorizableRun(ctx, repo, workflowID, runID, planHash)
		if err != nil {
			return err
		}
		user, err = model.ParseID(rec.String(colWrUserIdentity))
		if err != nil || user.IsZero() {
			return errReauthorizeNoInitiator
		}
		return nil
	})
	return user, err
}

// writeContinuationRefusal answers a refused continuation or succession and
// reports whether it did. Only core/auth's domain refusals are 403; anything
// operational is 503 and never reads as a refusal of the credential.
func (m *Module) writeContinuationRefusal(w http.ResponseWriter, runID model.ID, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, auth.ErrCredentialBindingConflict):
		writeJSON(w, http.StatusConflict, errorBody("another reauthorization of this run is in progress; retry"))
	case errors.Is(err, auth.ErrCredentialBindingInvalid):
		writeJSON(w, http.StatusForbidden, errorBody(
			"this credential cannot continue the run: reauthorize with a current credential of the run's initiator"))
	case errors.Is(err, auth.ErrCredentialBindingCeiling):
		writeJSON(w, http.StatusForbidden, errorBody(
			"this credential exceeds the authority the run was started with; reauthorize with a credential no wider"))
	case errors.Is(err, auth.ErrCredentialBindingUnproven):
		writeJSON(w, http.StatusForbidden, errorBody(
			"the run was never bound to the credential that started it, so its original authority cannot be proved; start a new run"))
	default:
		m.errorf("orchestration: reauthorization evidence is unavailable", "run", runID.String(), "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("credential binding is unavailable; run not reauthorized, retry"))
	}
	return true
}

// reserveReauthorization is W0.
func (m *Module) reserveReauthorization(
	ctx context.Context,
	mc api.ModuleContext,
	workflowID, runID model.ID,
	planHash string,
) (reservedReauthorization, error) {
	var reserved reservedReauthorization
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, steps, err := reauthorizableRun(ctx, repo, workflowID, runID, planHash)
		if err != nil {
			return err
		}
		// No claim is consulted: a claim's age proves nothing about whether its
		// effect ended. The supersession in W1 moves the directory epoch every
		// effect's commit consumes, so an effect prepared under the old binding
		// either committed before W1 or is refused at its commit.
		user, err := model.ParseID(rec.String(colWrUserIdentity))
		if err != nil || user.IsZero() {
			return errReauthorizeNoInitiator
		}
		old, _ := auth.ParseCredentialBindingStorage(rec.String(colWrCredentialBinding))
		rec[colWrPaused] = pausedReauthRequired
		updated, err := repo.Update(ctx, rec)
		if err != nil {
			return err
		}
		reserved = reservedReauthorization{
			old: old, generation: updated.Int(model.ColVersion), user: user, steps: len(steps),
		}
		return nil
	})
	return reserved, err
}

// publishReauthorization is W2.
func (m *Module) publishReauthorization(
	ctx context.Context,
	mc api.ModuleContext,
	workflowID, runID model.ID,
	planHash string,
	reserved reservedReauthorization,
	successor auth.CredentialBinding,
) error {
	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, steps, err := reauthorizableRun(ctx, repo, workflowID, runID, planHash)
		if err != nil {
			return err
		}
		if rec.Int(model.ColVersion) != reserved.generation ||
			rec.String(colWrCredentialBinding) != reserved.old.StorageValue() {
			return errReauthorizeMoved
		}
		now := m.clock.Now().String()
		for i := range steps {
			s := &steps[i]
			if s.Status != stepStatusReauthRequired {
				continue
			}
			resume := s.ReauthResume
			if resume != stepStatusWaitingAck {
				resume = stepStatusPending
			}
			s.Status, s.ReauthResume, s.At = resume, "", now
			s.Detail = "reauthorized; resuming with its original semantic key"
		}
		rec[colWrSteps] = encodeRunSteps(steps)
		rec[colWrCredentialBinding] = successor.StorageValue()
		rec[colWrPaused] = nil
		if _, err := repo.Update(ctx, rec); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return errReauthorizeMoved
			}
			return err
		}
		if err := m.recordDecision(ctx, sc, decisionRow{
			subjectKind: "workflow", subjectRef: workflowID.String(), op: opRunStep,
			planHash: rec.String(colWrPlanHash), approvalRef: rec.String(colWrApproval),
			opStatus: opStatusGatePassed, actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(),
			result: "run " + runID.String() + " reauthorized",
		}); err != nil {
			return err
		}
		return auditEvent(ctx, sc, mc, "orchestration.workflow.run.reauthorized", workflowKind, workflowID,
			map[string]any{"run": runID.String(), "plan_hash": rec.String(colWrPlanHash), "generation": reserved.generation})
	})
}

// reauthorizableRun reads the run for W0 and W2 and applies the checks both
// share: it belongs to the path's workflow, runs, is gated and was approved
// for exactly planHash.
func reauthorizableRun(
	ctx context.Context,
	repo store.GenericRepo,
	workflowID, runID model.ID,
	planHash string,
) (model.Record, []runStepState, error) {
	rec, err := repo.Get(ctx, runID)
	if isNotFound(err) {
		return nil, nil, errReauthorizeNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if rec.String(colWrWorkflow) != workflowID.String() {
		return nil, nil, errReauthorizeNotFound
	}
	if rec.String(colWrStatus) != runStatusRunning {
		return nil, nil, errReauthorizeNotRunning
	}
	steps, err := decodeRunSteps(rec.String(colWrSteps))
	if err != nil {
		return nil, nil, err
	}
	if !runReauthenticationGated(rec, steps) {
		return nil, nil, errReauthorizeNotGated
	}
	if rec.String(colWrPlanHash) != planHash {
		return nil, nil, errReauthorizePlanMismatch
	}
	return rec, steps, nil
}

func (m *Module) readRunDTO(ctx context.Context, mc api.ModuleContext, runID model.ID) (runDTO, error) {
	var out runDTO
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, runID)
		if err != nil {
			return err
		}
		steps, err := decodeRunSteps(rec.String(colWrSteps))
		if err != nil {
			return err
		}
		out = toRunDTO(rec, steps)
		return nil
	})
	return out, err
}

func (m *Module) reauthorizeStageReached(stage reauthorizeStage) error {
	if m.reauthorizeHook == nil {
		return nil
	}
	return m.reauthorizeHook(stage)
}

func writeReauthorizeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errReauthorizeNotFound):
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
	case errors.Is(err, errReauthorizeNotRunning), errors.Is(err, errReauthorizeNotGated):
		writeJSON(w, http.StatusConflict, errorBody("the run is not waiting for reauthentication"))
	case errors.Is(err, errReauthorizePlanMismatch):
		writeJSON(w, http.StatusConflict, errorBody("plan_hash does not match the run's approved plan"))
	case errors.Is(err, errReauthorizeNoInitiator):
		writeJSON(w, http.StatusForbidden, errorBody("the run has no account initiator to continue"))
	case errors.Is(err, errReauthorizeMoved), errors.Is(err, store.ErrConflict):
		writeJSON(w, http.StatusConflict, errorBody("the run changed during reauthorization; retry"))
	default:
		writeStoreError(w, err)
	}
}
