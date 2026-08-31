// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// workflow_run.go — the governed execution engine. A run is the ONLY
// production-affecting path of a workflow and it mirrors the schedule fire
// posture exactly: two-phase HITL (request approval → consume decision with
// STRICT plan binding) kill-switch pre-flight that FAILS CLOSED, FIN-08
// budget pre-flight per fired schedule that fails open, append-only evidence
// per actuating step in the shared decision ledger, and deny-closed actuation
// seams (an unwired dispatcher/notifier declares, never pretends).
//
// Concurrency: the runner advances a run with CLAIM-THEN-ACT (the notify
// rule). Actuating I/O never happens inside a store transaction; a step is
// claimed (status executing, version-checked write) BEFORE its side effect and
// resolved after. Two advancers (the composition-root pump and the in-request
// advance) can never double-actuate: the claim write is optimistic-locked, so
// exactly one wins. A claim orphaned by a crash is failed after a timeout —
// at-most-once with an honest failure, never at-least-once double actuation.

// Ledger ops added by (the workflow verbs of the append-only evidence).
const (
	opRunRequest = "run_request" // phase 1: a run requested approval
	opRun        = "run"         // phase 2: a run was decided (blocked/started)
	opRunStep    = "run_step"    // one step's outcome (dispatched/failed/skipped/…)
	opRunEnd     = "run_end"     // the run reached a terminal status
)

// Ledger op_status values added by (additive to the vocabulary).
const (
	opStatusSkipped    = "skipped"     // a step never ran: an upstream step failed or was denied
	opStatusGatePassed = "gate_passed" // an approval-gate step was approved
	opStatusCompleted  = "completed"   // the run finished with every step OK
	// opStatusReconciled records what a side effect ACTUALLY did when its result
	// arrived after the run state had moved the step elsewhere (orphan timeout,
	// kill-switch freeze). The step's own state may read "outcome unknown"; this
	// row carries the truth so the ledger never lies by omission.
	opStatusReconciled = "reconciled"
)

// Per-step execution statuses (the run row's steps JSON — mutable STATE; the
// immutable evidence is the ledger).
const (
	stepStatusPending               = "pending"
	stepStatusExecuting             = "executing" // claimed by an advancer; side effect in flight
	stepStatusWaiting               = "waiting"   // a wait step pacing until not_before
	stepStatusWaitingGate           = "waiting_approval"
	stepStatusWaitingAck            = "waiting_ack"
	stepStatusDispatched            = "dispatched" // schedule-fire actuated
	stepStatusDeclared              = "declared"   // approved path with no actuator wired — honest no-op
	stepStatusEmitted               = "emitted"    // workflow.signal published
	stepStatusNotified              = "notified"   // notify test sent
	stepStatusDone                  = "done"       // wait elapsed
	stepStatusGatePassed            = "gate_passed"
	stepStatusWorkApplied           = "work_applied"
	stepStatusLaunched              = "launched"
	stepStatusMessageSent           = "message_sent"
	stepStatusHandoff               = "handoff_offered"
	stepStatusAcked                 = "acknowledged"
	stepStatusReconciled            = "reconciled"
	stepStatusRemotePlanned         = "remote_planned"
	stepStatusRemoteTested          = "remote_tested"
	stepStatusRemoteStarted         = "remote_started"
	stepStatusRemoteObserved        = "remote_observed"
	stepStatusRemoteCancelRequested = "remote_cancel_requested"
	stepStatusRemoteCanceled        = "remote_canceled"
	stepStatusBlocked               = "blocked" // gate denied (rejected/expired/no gate)
	stepStatusBudget                = "budget_blocked"
	stepStatusFailed                = "failed"
	stepStatusSkipped               = "skipped"
)

// executingTimeout bounds a claimed step whose advancer died mid-flight: after
// it, the step is FAILED (not retried) — the at-most-once posture.
const executingTimeout = 5 * time.Minute

// resolveTimeout bounds the detached resolve/evidence write of pass C. It is
// generous relative to a local store write and far below executingTimeout, so a
// resolve that is going to succeed always lands before the orphan sweep could
// contradict it.
const resolveTimeout = 30 * time.Second

// maxAdvancePasses bounds the drain loop. One pass claims the ready frontier
// and resolves it, so a chain of N steps needs about N passes; the bound is
// deliberately generous rather than exact, because it is a RUNAWAY GUARD, not a
// completeness guarantee. Whatever a drain leaves unfinished the pump picks up
// on its next tick — so the honest claim is "this loop always terminates",
// never "this loop always finishes the graph".
func maxAdvancePasses(steps int) int { return 2*steps + 2 }

// stepOK reports a success-terminal status (dependents may proceed).
func stepOK(s string) bool {
	switch s {
	case stepStatusDispatched, stepStatusDeclared, stepStatusEmitted,
		stepStatusNotified, stepStatusDone, stepStatusGatePassed,
		stepStatusWorkApplied, stepStatusLaunched, stepStatusMessageSent,
		stepStatusHandoff, stepStatusAcked, stepStatusReconciled,
		stepStatusRemotePlanned, stepStatusRemoteTested, stepStatusRemoteStarted,
		stepStatusRemoteObserved, stepStatusRemoteCancelRequested, stepStatusRemoteCanceled:
		return true
	}
	return false
}

// stepFailedTerminal reports a failure-terminal status (dependents skip).
func stepFailedTerminal(s string) bool {
	switch s {
	case stepStatusBlocked, stepStatusBudget, stepStatusFailed, stepStatusSkipped:
		return true
	}
	return false
}

func stepTerminal(s string) bool { return stepOK(s) || stepFailedTerminal(s) }

// runStepState is one step's execution state inside the run row. Config and
// DependsOn are SNAPSHOTTED from the approved graph at phase 2, so a workflow
// edited mid-run can never change what an in-flight run executes — the run
// executes exactly the plan a human approved (anti-TOCTOU beyond the hash).
type runStepState struct {
	Ref       string          `json:"ref"`
	Kind      string          `json:"kind"`
	Config    json.RawMessage `json:"config"`
	DependsOn []string        `json:"depends_on"`
	Status    string          `json:"status"`
	// K4 work lineage is part of the durable step snapshot. It is populated
	// before actuation when the graph carries a literal WorkItem and completed
	// by the executor when work-create produces the root dynamically.
	WorkItemID      string `json:"work_item_id,omitempty"`
	CommandID       string `json:"command_id,omitempty"`
	EventSeq        int64  `json:"event_seq,omitempty"`
	OutputKind      string `json:"output_kind,omitempty"`
	OutputID        string `json:"output_id,omitempty"`
	OwnerEpoch      int64  `json:"owner_epoch,omitempty"`
	LeaseFence      int64  `json:"lease_fence,omitempty"`
	AttemptSemantic string `json:"attempt_semantic,omitempty"`
	// K5 remote-work lineage is the bounded durable projection needed to
	// reconcile after restart. It stores identities, hashes and verdicts only;
	// protocol payloads remain outside the workflow snapshot.
	RemoteOutcome               string `json:"remote_outcome,omitempty"`
	RemoteCode                  string `json:"remote_code,omitempty"`
	RemoteObservedAt            string `json:"remote_observed_at,omitempty"`
	RemotePlanHash              string `json:"remote_plan_hash,omitempty"`
	RemoteApprovalRef           string `json:"remote_approval_ref,omitempty"`
	RemoteBindingID             string `json:"remote_binding_id,omitempty"`
	RemoteBindingSpecID         string `json:"remote_binding_spec_id,omitempty"`
	RemoteBindingSpecGeneration int64  `json:"remote_binding_spec_generation,omitempty"`
	RemoteAttemptID             string `json:"remote_attempt_id,omitempty"`
	RemoteGeneration            int64  `json:"remote_generation,omitempty"`
	RemoteSyntheticSID          string `json:"remote_synthetic_sid,omitempty"`
	RemoteResultKind            string `json:"remote_result_kind,omitempty"`
	RemoteTaskID                string `json:"remote_task_id,omitempty"`
	RemoteContextID             string `json:"remote_context_id,omitempty"`
	RemoteMessageID             string `json:"remote_message_id,omitempty"`
	RemoteState                 string `json:"remote_state,omitempty"`
	RemoteRevision              string `json:"remote_revision,omitempty"`
	RemoteTerminal              bool   `json:"remote_terminal,omitempty"`
	RemoteWireHash              string `json:"remote_wire_hash,omitempty"`
	RemoteDetailHash            string `json:"remote_detail_hash,omitempty"`
	RemoteCommandID             string `json:"remote_command_id,omitempty"`
	RemoteEventID               string `json:"remote_event_id,omitempty"`
	RemoteEventSeq              int64  `json:"remote_event_seq,omitempty"`
	RemoteWorkState             string `json:"remote_work_state,omitempty"`
	// A work-wait-ack step persists its exact resume cursor and target. The
	// worker can therefore reconstruct the wait after restart without an
	// in-memory timer or subscription.
	WaitingTargetKind    string `json:"waiting_target_kind,omitempty"`
	WaitingTargetID      string `json:"waiting_target_id,omitempty"`
	WaitingAfterEventSeq int64  `json:"waiting_after_event_seq,omitempty"`
	WaitingDeadline      string `json:"waiting_deadline,omitempty"`
	Detail               string `json:"detail,omitempty"`
	ApprovalRef          string `json:"approval_ref,omitempty"`
	DispatchRef          string `json:"dispatch_ref,omitempty"`
	NotBefore            string `json:"not_before,omitempty"`
	At                   string `json:"at,omitempty"` // last transition instant
	// D-06: the APPROVED target binding frozen at run creation. Execution
	// recomputes the fingerprint against the CURRENT config and BLOCKS on any
	// change (a re-pointed schedule/route, a rotated secret). An acting step with
	// an empty BindProfile could not be bound (no HMAC key) and is BLOCKED — a
	// target that cannot be verified is never actuated. Opaque only: no URL/
	// command/header/secret is ever stored here.
	BindProfile    string `json:"bind_profile,omitempty"`
	ApprovedTarget string `json:"approved_target,omitempty"`
	MacKeyID       string `json:"mac_key_id,omitempty"`
	Generation     string `json:"generation,omitempty"`
	// RouteFp is the notify route's OWN opaque fingerprint (pre-HMAC) frozen at
	// approval, handed to the atomic notify seam so it can refuse a re-pointed
	// route from a SINGLE read that also delivers (hole c1). Empty for
	// non-notify steps.
	RouteFp string `json:"route_fp,omitempty"`
}

// stepBinding is the approved target binding of one acting step.
type stepBinding struct {
	profile     string
	fingerprint string
	keyID       string
	generation  string
	routeFp     string // notify only: the route's own pre-HMAC fingerprint
}

// acting reports whether a step kind actuates an effect-bearing target (and so
// requires an approved target binding). eventing-emit publishes only the fixed
// workflow.signal; wait/approval-gate actuate nothing external.
func stepActuatesTarget(kind string) bool {
	return kind == stepScheduleFire || kind == stepNotifyTest
}

// bindResult is the three-way outcome of resolving a step's target binding.
type bindResult int

const (
	bindOK         bindResult = iota // fingerprint resolved
	bindBlock                        // cannot resolve (no key / unresolvable target / seam error) ⇒ BLOCK (deny-closed)
	bindNoActuator                   // the actuator is UNWIRED ⇒ nothing actuates, so the binding is moot (declare)
)

// resolveStepBinding computes the CURRENT target binding of one acting step — the
// HMAC fingerprint of its effect-bearing target under the target-binding key. It
// is the SINGLE derivation used both at run creation (freeze) and at execution
// (verify), so the two can never disagree on how the fingerprint is formed. The
// three-way result separates "no actuator wired" (safe: nothing actuates) from
// "cannot verify" (deny-closed: block) so an unwired seam still DECLARES rather
// than blocking.
func (m *Module) resolveStepBinding(ctx context.Context, mc api.ModuleContext, s runStepState) (stepBinding, string, bindResult) {
	switch s.Kind {
	case stepScheduleFire:
		var cfg scheduleFireConfig
		if json.Unmarshal(s.Config, &cfg) != nil {
			return stepBinding{}, "", bindBlock
		}
		var sched model.Record
		found := false
		if err := mc.Data.View(ctx, func(sc store.Scope) error {
			repo, e := sc.Ext(scheduleKind)
			if e != nil {
				return e
			}
			rec, e := repo.Get(ctx, model.ID(cfg.ScheduleID))
			if e != nil {
				if isNotFound(e) {
					return nil
				}
				return e
			}
			sched, found = rec, true
			return nil
		}); err != nil || !found {
			return stepBinding{}, "", bindBlock
		}
		canon := m.scheduleTargetString(sched)
		fp, keyID, ok := m.targetHMAC(canon)
		if !ok {
			return stepBinding{}, "", bindBlock // key unavailable ⇒ cannot verify ⇒ block
		}
		return stepBinding{profile: bindingProfileV1, fingerprint: fp, keyID: keyID,
			generation: m.dispatchGen.Generation(sched.String(colSubjectKind), sched.String(colSubjectRef))}, canon, bindOK
	case stepNotifyTest:
		var cfg notifyTestConfig
		if json.Unmarshal(s.Config, &cfg) != nil {
			return stepBinding{}, "", bindBlock
		}
		routeFp, ok, err := m.notifyTest.RouteFingerprint(ctx, mc.Tenant, cfg.RouteID)
		if errors.Is(err, errNoNotifyTester) {
			return stepBinding{}, "", bindNoActuator // unwired notifier ⇒ declare, not block
		}
		if err != nil || !ok {
			return stepBinding{}, "", bindBlock
		}
		canon := routeTargetString(routeFp)
		fp, keyID, ok := m.targetHMAC(canon)
		if !ok {
			return stepBinding{}, "", bindBlock
		}
		return stepBinding{profile: bindingProfileV1, fingerprint: fp, keyID: keyID, routeFp: routeFp}, canon, bindOK
	}
	return stepBinding{}, "", bindBlock
}

// checkStepTarget re-resolves an acting step's CURRENT target binding and
// decides whether it may proceed:
//   - proceed          ⇒ the current fingerprint matches the approval; actuate.
//   - declared         ⇒ no actuator is wired; the step DECLARES (honest no-op).
//   - neither          ⇒ BLOCK (detail says why): the target CHANGED since the
//     human approved (the D-06 defect), was unbound at approval but is now
//     actuable, or cannot be verified (no key). A block never re-targets to the
//     new value nor acts on the old snapshot.
func (m *Module) checkStepTarget(ctx context.Context, mc api.ModuleContext, s runStepState) (proceed, declared bool, detail string) {
	bind, _, res := m.resolveStepBinding(ctx, mc, s)
	switch res {
	case bindNoActuator:
		return false, true, ""
	case bindOK:
		if s.BindProfile == "" {
			return false, false, "target unbound at approval but now actuable; re-approval required"
		}
		if bind.fingerprint != s.ApprovedTarget {
			return false, false, "target changed since approval; step blocked (re-approval required)"
		}
		return true, false, ""
	default: // bindBlock
		return false, false, "target could not be verified (unresolvable or no key); step blocked"
	}
}

// persistRunTargetBindings writes the immutable approved-target binding rows for
// a run's acting steps in the run-creation transaction (the authoritative record
// the run-step JSON mirrors). A step that could not be bound is recorded with an
// empty profile/fingerprint so the gap is durable and visible.
func (m *Module) persistRunTargetBindings(ctx context.Context, sc store.Scope, runID model.ID, steps []runStepState) error {
	repo, err := sc.Ext(runTargetBindingKind)
	if err != nil {
		return err
	}
	for _, s := range steps {
		if !stepActuatesTarget(s.Kind) {
			continue
		}
		if _, err := repo.Create(ctx, model.Record{
			colRtbRunRef: runID.String(), colRtbStepRef: s.Ref,
			colRtbProfile: s.BindProfile, colRtbMacKeyID: s.MacKeyID,
			colRtbFingerprint: s.ApprovedTarget, colRtbGeneration: s.Generation,
		}); err != nil {
			return err
		}
	}
	return nil
}

// runDTO projects one run with its per-step timeline.
type runDTO struct {
	ID             string       `json:"id"`
	WorkflowRef    string       `json:"workflow_ref"`
	RootWorkItemID string       `json:"root_work_item_id,omitempty"`
	Status         string       `json:"status"`
	PlanHash       string       `json:"plan_hash"`
	ApprovalRef    string       `json:"approval_ref,omitempty"`
	PausedReason   string       `json:"paused_reason,omitempty"`
	Actor          string       `json:"actor"`
	ActorKind      string       `json:"actor_kind"`
	StartedAt      string       `json:"started_at"`
	FinishedAt     string       `json:"finished_at,omitempty"`
	Steps          []runStepDTO `json:"steps"`
}

// runStepDTO is the read shape of one step's state (config omitted — the
// approved graph is on the workflow/revision; the timeline is about outcomes).
type runStepDTO struct {
	Ref                         string   `json:"ref"`
	Kind                        string   `json:"kind"`
	DependsOn                   []string `json:"depends_on"`
	Status                      string   `json:"status"`
	WorkItemID                  string   `json:"work_item_id,omitempty"`
	CommandID                   string   `json:"command_id,omitempty"`
	EventSeq                    int64    `json:"event_seq,omitempty"`
	OutputKind                  string   `json:"output_kind,omitempty"`
	OutputID                    string   `json:"output_id,omitempty"`
	OwnerEpoch                  int64    `json:"owner_epoch,omitempty"`
	LeaseFence                  int64    `json:"lease_fence,omitempty"`
	AttemptSemantic             string   `json:"attempt_semantic,omitempty"`
	RemoteOutcome               string   `json:"remote_outcome,omitempty"`
	RemoteCode                  string   `json:"remote_code,omitempty"`
	RemoteObservedAt            string   `json:"remote_observed_at,omitempty"`
	RemotePlanHash              string   `json:"remote_plan_hash,omitempty"`
	RemoteApprovalRef           string   `json:"remote_approval_ref,omitempty"`
	RemoteBindingID             string   `json:"remote_binding_id,omitempty"`
	RemoteBindingSpecID         string   `json:"remote_binding_spec_id,omitempty"`
	RemoteBindingSpecGeneration int64    `json:"remote_binding_spec_generation,omitempty"`
	RemoteAttemptID             string   `json:"remote_attempt_id,omitempty"`
	RemoteGeneration            int64    `json:"remote_generation,omitempty"`
	RemoteSyntheticSID          string   `json:"remote_synthetic_sid,omitempty"`
	RemoteResultKind            string   `json:"remote_result_kind,omitempty"`
	RemoteTaskID                string   `json:"remote_task_id,omitempty"`
	RemoteContextID             string   `json:"remote_context_id,omitempty"`
	RemoteMessageID             string   `json:"remote_message_id,omitempty"`
	RemoteState                 string   `json:"remote_state,omitempty"`
	RemoteRevision              string   `json:"remote_revision,omitempty"`
	RemoteTerminal              bool     `json:"remote_terminal,omitempty"`
	RemoteWireHash              string   `json:"remote_wire_hash,omitempty"`
	RemoteDetailHash            string   `json:"remote_detail_hash,omitempty"`
	RemoteCommandID             string   `json:"remote_command_id,omitempty"`
	RemoteEventID               string   `json:"remote_event_id,omitempty"`
	RemoteEventSeq              int64    `json:"remote_event_seq,omitempty"`
	RemoteWorkState             string   `json:"remote_work_state,omitempty"`
	WaitingTargetKind           string   `json:"waiting_target_kind,omitempty"`
	WaitingTargetID             string   `json:"waiting_target_id,omitempty"`
	WaitingAfterEventSeq        int64    `json:"waiting_after_event_seq,omitempty"`
	WaitingDeadline             string   `json:"waiting_deadline,omitempty"`
	Detail                      string   `json:"detail,omitempty"`
	ApprovalRef                 string   `json:"approval_ref,omitempty"`
	DispatchRef                 string   `json:"dispatch_ref,omitempty"`
	NotBefore                   string   `json:"not_before,omitempty"`
	At                          string   `json:"at,omitempty"`
}

func toRunDTO(rec model.Record, steps []runStepState) runDTO {
	out := runDTO{
		ID:             rec.String(model.ColID),
		WorkflowRef:    rec.String(colWrWorkflow),
		RootWorkItemID: rec.String(colWrRootWork),
		Status:         rec.String(colWrStatus),
		PlanHash:       rec.String(colWrPlanHash),
		ApprovalRef:    rec.String(colWrApproval),
		PausedReason:   rec.String(colWrPaused),
		Actor:          rec.String(colWrActor),
		ActorKind:      rec.String(colWrActorKind),
		StartedAt:      rec.String(colWrStartedAt),
		FinishedAt:     rec.String(colWrFinished),
		Steps:          make([]runStepDTO, 0, len(steps)),
	}
	for _, s := range steps {
		out.Steps = append(out.Steps, runStepDTO{
			Ref: s.Ref, Kind: s.Kind, DependsOn: s.DependsOn, Status: s.Status, Detail: s.Detail,
			WorkItemID: s.WorkItemID, CommandID: s.CommandID, EventSeq: s.EventSeq,
			OutputKind: s.OutputKind, OutputID: s.OutputID, OwnerEpoch: s.OwnerEpoch,
			LeaseFence:      s.LeaseFence,
			AttemptSemantic: s.AttemptSemantic, WaitingTargetKind: s.WaitingTargetKind,
			RemoteOutcome: s.RemoteOutcome, RemoteCode: s.RemoteCode,
			RemoteObservedAt: s.RemoteObservedAt, RemotePlanHash: s.RemotePlanHash,
			RemoteApprovalRef: s.RemoteApprovalRef,
			RemoteBindingID:   s.RemoteBindingID, RemoteBindingSpecID: s.RemoteBindingSpecID,
			RemoteBindingSpecGeneration: s.RemoteBindingSpecGeneration,
			RemoteAttemptID:             s.RemoteAttemptID, RemoteGeneration: s.RemoteGeneration,
			RemoteSyntheticSID: s.RemoteSyntheticSID, RemoteResultKind: s.RemoteResultKind,
			RemoteTaskID: s.RemoteTaskID, RemoteContextID: s.RemoteContextID,
			RemoteMessageID: s.RemoteMessageID, RemoteState: s.RemoteState,
			RemoteRevision: s.RemoteRevision, RemoteTerminal: s.RemoteTerminal,
			RemoteWireHash: s.RemoteWireHash, RemoteDetailHash: s.RemoteDetailHash,
			RemoteCommandID: s.RemoteCommandID, RemoteEventID: s.RemoteEventID,
			RemoteEventSeq: s.RemoteEventSeq, RemoteWorkState: s.RemoteWorkState,
			WaitingTargetID: s.WaitingTargetID, WaitingAfterEventSeq: s.WaitingAfterEventSeq,
			WaitingDeadline: s.WaitingDeadline,
			ApprovalRef:     s.ApprovalRef, DispatchRef: s.DispatchRef, NotBefore: s.NotBefore, At: s.At,
		})
	}
	return out
}

// decodeRunSteps parses a run row's steps document.
func decodeRunSteps(raw string) ([]runStepState, error) {
	var steps []runStepState
	if err := json.Unmarshal([]byte(raw), &steps); err != nil {
		return nil, fmt.Errorf("orchestration: stored run steps are malformed: %w", err)
	}
	return steps, nil
}

func encodeRunSteps(steps []runStepState) string { return string(mustJSON(steps)) }

func nullableRunRef(ref string) any {
	if ref == "" {
		return nil
	}
	return ref
}

func nullableRunID(id model.ID) any {
	if id.IsZero() {
		return nil
	}
	return id.String()
}

func nullableRunInt(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableRunBool(value bool) any {
	if !value {
		return nil
	}
	return value
}

func workflowWorkAdmin(principal auth.Principal, tenant model.TenantID) bool {
	role, _ := principal.RoleIn(tenant)
	return principal.Superadmin || auth.RoleRank(role) >= auth.RoleRank(auth.RoleAdmin)
}

// runRequestBody is the two-phase run body (the fireRequest shape).
type runRequestBody struct {
	ApprovalRef string `json:"approval_ref"`
}

// runPhaseResponse reports a phase-1/phase-2 verdict (the fireResponse shape);
// phase 2 additionally carries the created run.
type runPhaseResponse struct {
	Op               string     `json:"op"`
	OpStatus         string     `json:"op_status"`
	PlanHash         string     `json:"plan_hash"`
	ApprovalRef      string     `json:"approval_ref,omitempty"`
	GateStatus       GateStatus `json:"gate_status"`
	RequiresApproval bool       `json:"requires_approval,omitempty"`
	Detail           string     `json:"detail,omitempty"`
	Run              *runDTO    `json:"run,omitempty"`
}

// handleRunWorkflow is the two-phase governed run (admin-tier + HITL). Phase 1
// (no approval_ref) opens an approval bound to the graph's plan_hash. Phase 2
// consumes the decision under strict plan binding, snapshots the approved graph
// into a run row and advances it synchronously to its first quiescent point.
func (m *Module) handleRunWorkflow(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("workflow id required"))
		return
	}
	var in runRequestBody
	if !decodeOptionalJSON(w, r, &in) {
		return
	}

	var wf model.Record
	var steps []stepDTO
	var targets map[string]string
	found := false
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		rec, st, ok, err := getWorkflow(r.Context(), sc, id)
		if err != nil || !ok {
			return err
		}
		tg, terr := m.resolveTargets(r.Context(), sc, mc.Tenant, st)
		if terr != nil {
			return terr
		}
		wf, steps, targets, found = rec, st, tg, true
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if !wf.Bool(colWfEnabled) {
		writeJSON(w, http.StatusConflict, errorBody("workflow is disabled"))
		return
	}
	if len(steps) == 0 {
		writeJSON(w, http.StatusConflict, errorBody("workflow has no steps"))
		return
	}
	// The hash binds the graph AND what its actuating steps point at, so a
	// re-target between the two phases voids the approval (planHashOfWorkflow).
	planHash := planHashOfWorkflow(wf.String(model.ColID), wf.String(colWfName), steps, targets)

	// Estate kill switch FIRST, failing CLOSED — a stop outranks approval.
	if m.stopBlocksRun(w, r, mc, id, planHash, in.ApprovalRef) {
		return
	}

	if in.ApprovalRef == "" {
		m.runPhaseRequest(w, r, mc, id, planHash)
		return
	}
	m.runPhaseDecide(w, r, mc, id, planHash, in.ApprovalRef, steps, targets)
}

// stopBlocksRun is stopBlocksFire for workflow runs: estate-scoped check (the
// per-agent dims apply per schedule-fire step at execution). Returns true when
// the run was DENIED (response written). Fails CLOSED on a gate error.
func (m *Module) stopBlocksRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, id model.ID, planHash, approvalRef string) bool {
	op := opRunRequest
	if approvalRef != "" {
		op = opRun
	}
	verdict, err := m.stopGate.Check(r.Context(), mc.Tenant, StopDims{})
	if err != nil {
		m.errorf("orchestration: kill-switch gate error; failing CLOSED (workflow run denied)", "workflow", id.String(), "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("kill-switch state unreadable; run denied (deny-closed)"))
		return true
	}
	if !verdict.Stopped {
		return false
	}
	detail := "denied: emergency stop active (" + verdict.Scope + " kill switch " + verdict.StopRef + "); re-enable requires dual-control"
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := m.recordDecision(r.Context(), sc, decisionRow{
			subjectKind: "workflow", subjectRef: id.String(), op: op,
			planHash: planHash, approvalRef: approvalRef, opStatus: opStatusBlocked,
			actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: detail,
		}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.workflow.run.killswitch_denied", workflowKind, id,
			map[string]any{"plan_hash": planHash, "stop_ref": verdict.StopRef, "stop_scope": verdict.Scope})
	}); err != nil {
		m.errorf("orchestration: failed to record kill-switch-denied run evidence", "workflow", id.String(), "err", err)
	}
	writeJSON(w, http.StatusLocked, runPhaseResponse{
		Op: op, OpStatus: opStatusBlocked, PlanHash: planHash, ApprovalRef: approvalRef, Detail: detail,
	})
	return true
}

// runPhaseRequest opens the HITL approval for a run (phase 1).
func (m *Module) runPhaseRequest(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, id model.ID, planHash string) {
	decision, gerr := m.gate.Request(r.Context(), ApprovalRequest{
		Tenant: mc.Tenant, Action: "orchestration.workflow.run", SubjectKind: "workflow",
		SubjectRef: id.String(), PlanHash: planHash, RequestedBy: mc.Principal.Actor(),
	})
	if gerr != nil {
		m.errorf("orchestration: approval gate request failed", "workflow", id.String(), "err", gerr)
		writeJSON(w, http.StatusBadGateway, errorBody("approval gate unavailable"))
		return
	}
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := m.recordDecision(r.Context(), sc, decisionRow{
			subjectKind: "workflow", subjectRef: id.String(), op: opRunRequest,
			planHash: planHash, approvalRef: decision.ApprovalRef, gateStatus: decision.Status,
			opStatus: opStatusRequested, actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(),
			result: "approval requested",
		}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.workflow.run.request", workflowKind, id,
			map[string]any{"plan_hash": planHash, "approval_ref": decision.ApprovalRef, "gate_status": string(decision.Status)})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if decision.Status == StatusNoGate {
		m.reportUngovernedRun(r.Context(), mc, id.String())
	}
	writeJSON(w, http.StatusAccepted, runPhaseResponse{
		Op: opRunRequest, OpStatus: opStatusRequested, PlanHash: planHash, ApprovalRef: decision.ApprovalRef,
		GateStatus: decision.Status, RequiresApproval: true, Detail: "approval requested; re-POST with approval_ref to run",
	})
}

// runPhaseDecide consumes the decision (phase 2): strict plan binding, then the
// run row is created with the approved graph SNAPSHOTTED in and advanced.
func (m *Module) runPhaseDecide(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, id model.ID, planHash, approvalRef string, steps []stepDTO, targets map[string]string) {
	decision, gerr := m.gate.Status(r.Context(), ApprovalCheck{
		Tenant: mc.Tenant, ApprovalRef: approvalRef, PlanHash: planHash,
		Action: "orchestration.workflow.run", SubjectKind: "workflow", SubjectRef: id.String(),
	})
	if gerr != nil {
		m.errorf("orchestration: approval gate status failed", "workflow", id.String(), "err", gerr)
		writeJSON(w, http.StatusBadGateway, errorBody("approval gate unavailable"))
		return
	}
	if !decision.Allowed() || decision.PlanHash != planHash {
		if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			if err := m.recordDecision(r.Context(), sc, decisionRow{
				subjectKind: "workflow", subjectRef: id.String(), op: opRun,
				planHash: planHash, approvalRef: approvalRef, gateStatus: decision.Status, opStatus: opStatusBlocked,
				actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: "denied: " + string(decision.Status),
			}); err != nil {
				return err
			}
			return auditEvent(r.Context(), sc, mc, "orchestration.workflow.run.blocked", workflowKind, id,
				map[string]any{"plan_hash": planHash, "approval_ref": approvalRef, "gate_status": string(decision.Status)})
		}); err != nil {
			m.errorf("orchestration: failed to record blocked-run evidence", "workflow", id.String(), "err", err)
		}
		if decision.Status == StatusNoGate {
			m.reportUngovernedRun(r.Context(), mc, id.String())
		}
		writeJSON(w, http.StatusForbidden, runPhaseResponse{
			Op: opRun, OpStatus: opStatusBlocked, PlanHash: planHash, ApprovalRef: approvalRef,
			GateStatus: decision.Status, RequiresApproval: true, Detail: "run denied (deny-by-default)",
		})
		return
	}

	now := m.clock.Now().String()
	rootWorkID := rootWorkItemID(steps)
	runSteps := make([]runStepState, 0, len(steps))
	for _, s := range steps {
		rs := runStepState{
			Ref: s.Ref, Kind: s.Kind, Config: s.Config, DependsOn: s.DependsOn,
			Status: stepStatusPending, WorkItemID: stepWorkItemID(s),
		}
		if isWorkStepKind(s.Kind) {
			rs.AttemptSemantic = "primary"
		}
		if s.Kind == stepWorkWaitAck {
			var cfg workWaitAckConfig
			_ = json.Unmarshal(s.Config, &cfg)
			rs.WaitingTargetKind, rs.WaitingTargetID = cfg.TargetKind, cfg.TargetID
			rs.WaitingAfterEventSeq, rs.WaitingDeadline = cfg.AfterEventSeq, cfg.Deadline
		}
		// D-06: FREEZE the approved target binding of every acting step at run
		// creation (the phase-2 state a human approved). Execution recomputes it
		// and BLOCKS on any change. A step whose binding cannot be resolved (no
		// HMAC key, unresolvable target) is frozen with an empty profile and
		// blocks at execution — deny-closed, never actuated on an unverifiable
		// target.
		if stepActuatesTarget(s.Kind) {
			if bind, canon, res := m.resolveStepBinding(r.Context(), mc, rs); res == bindOK {
				// Item 4 window 1: the plan hash was validated against the target
				// A that resolveTargets read (:394). This re-read must still be A — a
				// re-target A→B between the plan validation and this freeze would
				// otherwise freeze B under A's approval. Refuse the run if it moved.
				if canon != targets[s.Ref] {
					writeJSON(w, http.StatusForbidden, runPhaseResponse{
						Op: opRun, OpStatus: opStatusBlocked, PlanHash: planHash, ApprovalRef: approvalRef,
						GateStatus: decision.Status, RequiresApproval: true,
						Detail: "target changed between approval and run start (step " + s.Ref + "); request a new approval",
					})
					return
				}
				rs.BindProfile, rs.ApprovedTarget = bind.profile, bind.fingerprint
				rs.MacKeyID, rs.Generation, rs.RouteFp = bind.keyID, bind.generation, bind.routeFp
			}
		}
		runSteps = append(runSteps, rs)
	}
	// D-05 item 3: an approval authorizes ONE run. The gate's Status is a
	// pure read, so the single-use guard is a durable run-level operation with a
	// UNIQUE(tenant, approval_ref) index, claimed in the SAME transaction as the
	// run row. Two concurrent phase-2 POSTs (a real hazard on Postgres's multi-
	// connection pool, masked only by SQLite's single writer) then cannot both
	// create a run: the loser's operation insert conflicts and the whole
	// transaction — run row included — rolls back.
	runSpec := operationSpec{
		tenant: mc.Tenant.String(), approvalRef: approvalRef, surface: surfaceWorkflowRun,
		action: surfaceWorkflowRun, planHash: planHash, policyVersion: string(decision.Status),
		bindProfile: bindingProfileV1, targetFp: planHash, auditTarget: id,
	}
	runDigest := m.effectDigest(runSpec)
	var runClaimTx operationClaim
	var runAppendDropped bool
	var runID model.ID
	replayed := false
	mutErr := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		var e error
		runClaimTx, runAppendDropped, _, e = m.claimOperationInTx(r.Context(), sc, mc, runSpec, runDigest)
		if e != nil {
			return e
		}
		if runAppendDropped {
			return nil // evidence degraded: no run (handled after commit)
		}
		if runClaimTx.replay {
			replayed = true
			return nil
		}
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		created, err := repo.Create(r.Context(), model.Record{
			colWrWorkflow: id.String(), colWrStatus: runStatusRunning, colWrPlanHash: planHash,
			colWrRootWork: nullableRunRef(rootWorkID),
			colWrApproval: approvalRef, colWrSteps: encodeRunSteps(runSteps),
			colWrActor: mc.Principal.Actor(), colWrActorKind: mc.Principal.ActorKind(),
			colWrActorAdmin:        workflowWorkAdmin(mc.Principal, mc.Tenant),
			colWrUserIdentity:      nullableRunID(mc.Principal.UserID),
			colWrAgentIdentity:     nullableRunRef(mc.Principal.AgentIdentity),
			colWrSessionIdentity:   nullableRunRef(mc.Principal.SessionIdentity),
			colWrSessionRunRef:     nullableRunRef(mc.Principal.SessionRunRef),
			colWrSessionFence:      nullableRunInt(mc.Principal.SessionFence),
			colWrPurposeRestricted: nullableRunBool(mc.Principal.IsPurposeRestricted()),
			colWrStartedAt:         now,
		})
		if err != nil {
			return err
		}
		runID = model.ID(created.String(model.ColID))
		// Settle the run operation to dispatched (the run was created) in this same
		// transaction — anchor + effect commit together (local mutation).
		if err := m.settleOperation(r.Context(), sc, &runClaimTx, opStateDispatched, obStateDispatched, "", "run "+runID.String()); err != nil {
			return err
		}
		// D-06: persist the immutable approved-target binding per acting step
		// (the authoritative record; the run-step JSON mirrors it for the fast-path
		// verify) in the SAME transaction as the run row.
		if err := m.persistRunTargetBindings(r.Context(), sc, runID, runSteps); err != nil {
			return err
		}
		if err := m.recordDecision(r.Context(), sc, decisionRow{
			subjectKind: "workflow", subjectRef: id.String(), op: opRun,
			planHash: planHash, approvalRef: approvalRef, gateStatus: decision.Status, opStatus: opStatusDispatched,
			actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: "run started: " + runID.String(),
		}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.workflow.run", workflowKind, id,
			map[string]any{"plan_hash": planHash, "approval_ref": approvalRef, "run": runID.String()})
	})
	switch {
	case errors.Is(mutErr, errOperationReplay):
		// Same approval bound to a DIFFERENT plan digest ⇒ sdk.FailureReplay.
		writeJSON(w, http.StatusConflict, runPhaseResponse{
			Op: opRun, OpStatus: opStatusBlocked, PlanHash: planHash, ApprovalRef: approvalRef,
			GateStatus: decision.Status, RequiresApproval: true,
			Detail: "approval already consumed for a different run (" + string(sdk.FailureReplay) + "); request a new approval",
		})
		return
	case mutErr != nil:
		writeStoreError(w, mutErr)
		return
	}
	if runAppendDropped {
		writeJSON(w, http.StatusServiceUnavailable, runPhaseResponse{
			Op: opRun, OpStatus: opStatusBlocked, PlanHash: planHash, ApprovalRef: approvalRef,
			Detail: "evidence unavailable (spool degraded); run refused",
		})
		return
	}
	if replayed {
		// The approval was already spent. Record the refusal — a replay attempt
		// is exactly what an auditor wants to see — and answer 409: the
		// reference is valid but no longer available, not forbidden.
		if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			return m.recordDecision(r.Context(), sc, decisionRow{
				subjectKind: "workflow", subjectRef: id.String(), op: opRun,
				planHash: planHash, approvalRef: approvalRef, gateStatus: decision.Status, opStatus: opStatusBlocked,
				actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(),
				result: "denied: approval already used for an earlier run (single-use)",
			})
		}); err != nil {
			m.errorf("orchestration: failed to record replayed-approval evidence", "workflow", id.String(), "err", err)
		}
		writeJSON(w, http.StatusConflict, runPhaseResponse{
			Op: opRun, OpStatus: opStatusBlocked, PlanHash: planHash, ApprovalRef: approvalRef,
			GateStatus: decision.Status, RequiresApproval: true,
			Detail: "this approval has already started a run; request a new approval to run again",
		})
		return
	}

	// Drain synchronously so a wait/gate-free graph completes in-request; the
	// pump owns everything the drain cannot finish (waits, gates, crashes).
	m.drainRun(r.Context(), mc, runID, len(runSteps))

	var out runDTO
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), runID)
		if err != nil {
			return err
		}
		st, err := decodeRunSteps(rec.String(colWrSteps))
		if err != nil {
			return err
		}
		out = toRunDTO(rec, st)
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runPhaseResponse{
		Op: opRun, OpStatus: opStatusDispatched, PlanHash: planHash, ApprovalRef: approvalRef,
		GateStatus: decision.Status, Detail: "run started", Run: &out,
	})
}

// reportUngovernedRun surfaces a run attempted with no approval gate wired —
// the same governance gap as an ungoverned fire, on the workflow subject.
func (m *Module) reportUngovernedRun(ctx context.Context, mc api.ModuleContext, workflowID string) {
	detail := fmt.Sprintf("workflow:%s run attempted with no approval gate wired", workflowID)
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		return m.persistFinding(ctx, sc, finding{
			kind: busUngovernedFire, severity: sdkmodel.SeverityMedium, subjectKind: "workflow", subjectRef: workflowID,
			title: "workflow run blocked: no approval gate wired", detail: detail,
			meta: map[string]any{"workflow": workflowID},
		})
	}); err != nil {
		m.errorf("orchestration: failed to persist ungoverned-run finding", "workflow", workflowID, "err", err)
	}
	m.emitFinding(ctx, mc.Tenant, busUngovernedFire, sdkmodel.SeverityMedium, "workflow", workflowID,
		"workflow run blocked: no approval gate wired", detail)
}

// handleListWorkflowRuns lists a workflow's runs, newest-first pagination by id.
func (m *Module) handleListWorkflowRuns(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("workflow id required"))
		return
	}
	q := listQuery(r)
	q.Filters = append(q.Filters, eq(colWrWorkflow, id.String()))
	out := listResponse[runDTO]{Items: []runDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			steps, derr := decodeRunSteps(rec.String(colWrSteps))
			if derr != nil {
				return derr
			}
			out.Items = append(out.Items, toRunDTO(rec, steps))
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

// handleGetWorkflowRun returns one run's timeline. The run must belong to the
// path's workflow — a run of ANOTHER workflow is not found, never confirmed.
func (m *Module) handleGetWorkflowRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
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
	var out runDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), runID)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		if rec.String(colWrWorkflow) != id.String() {
			return nil
		}
		steps, derr := decodeRunSteps(rec.String(colWrSteps))
		if derr != nil {
			return derr
		}
		out = toRunDTO(rec, steps)
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
	writeJSON(w, http.StatusOK, out)
}

// ----------------------------------------------------------------------------
// The runner.
// ----------------------------------------------------------------------------

// AdvanceWorkflowRuns advances every RUNNING run of mc's tenant to its next
// quiescent point. It is the exported seam for the composition root's
// leader-gated pump (the RunCadenceScan convention); phase 2 calls the same
// engine in-request. mc needs Tenant and Data only.
func (m *Module) AdvanceWorkflowRuns(ctx context.Context, mc api.ModuleContext) {
	var runIDs []model.ID
	var stepCounts []int
	if err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		recs, err := listAll(ctx, repo, eq(colWrStatus, runStatusRunning))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			steps, derr := decodeRunSteps(rec.String(colWrSteps))
			if derr != nil {
				return derr
			}
			runIDs = append(runIDs, model.ID(rec.String(model.ColID)))
			stepCounts = append(stepCounts, len(steps))
		}
		return nil
	}); err != nil {
		m.errorf("orchestration: workflow run enumeration failed", "err", err)
		return
	}
	for i, id := range runIDs {
		if ctx.Err() != nil {
			return
		}
		m.drainRun(ctx, mc, id, stepCounts[i])
	}
}

// drainRun advances one run pass-by-pass until a pass makes no progress.
func (m *Module) drainRun(ctx context.Context, mc api.ModuleContext, runID model.ID, stepCount int) {
	for i := 0; i < maxAdvancePasses(stepCount); i++ {
		progressed, err := m.advanceRunOnce(ctx, mc, runID)
		if err != nil {
			m.errorf("orchestration: workflow run advance failed", "run", runID.String(), "err", err)
			return
		}
		if !progressed {
			return
		}
	}
}

// runAction is one side effect pass B must perform for a claimed step.
type runAction struct {
	step runStepState
}

// runOutcome is pass C's resolution for one step.
type runOutcome struct {
	ref                         string
	fromStatus                  string // apply only if the step is still in this status (race tolerance)
	status                      string
	detail                      string
	approvalRef                 string
	dispatchRef                 string
	notBefore                   string
	rootWorkItemID              string
	workItemID                  string
	commandID                   string
	eventSeq                    int64
	outputKind                  string
	outputID                    string
	ownerEpoch                  int64
	leaseFence                  int64
	remoteOutcome               string
	remoteCode                  string
	remoteObservedAt            string
	remotePlanHash              string
	remoteApprovalRef           string
	remoteBindingID             string
	remoteBindingSpecID         string
	remoteBindingSpecGeneration int64
	remoteAttemptID             string
	remoteGeneration            int64
	remoteSyntheticSID          string
	remoteResultKind            string
	remoteTaskID                string
	remoteContextID             string
	remoteMessageID             string
	remoteState                 string
	remoteRevision              string
	remoteTerminal              bool
	remoteWireHash              string
	remoteDetailHash            string
	remoteCommandID             string
	remoteEventID               string
	remoteEventSeq              int64
	remoteWorkState             string
	waitingTargetKind           string
	waitingTargetID             string
	waitingAfterEventSeq        int64
	waitingDeadline             string
	gateStatus                  GateStatus
	ledger                      bool   // append an opRunStep evidence row
	ledgerOp                    string // op_status for the ledger row
}

// advanceRunOnce performs one claim → act → resolve cycle. progressed reports
// whether any state changed (claims count: a claimed step resolves this pass).
func (m *Module) advanceRunOnce(ctx context.Context, mc api.ModuleContext, runID model.ID) (bool, error) {
	// Pass A (claim, in-tx): skip-propagation, crash recovery, claim ready steps.
	var claimed []runAction
	var polls []runStepState // durable waits/gates — evaluated without claim
	var run model.Record
	progressed := false
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, runID)
		if err != nil {
			return err
		}
		if rec.String(colWrStatus) != runStatusRunning {
			return nil
		}
		steps, err := decodeRunSteps(rec.String(colWrSteps))
		if err != nil {
			return err
		}
		byRef := map[string]*runStepState{}
		for i := range steps {
			byRef[steps[i].Ref] = &steps[i]
		}
		now := m.clock.Now()
		dirty := false
		for i := range steps {
			s := &steps[i]
			switch s.Status {
			case stepStatusPending:
				// Scan EVERY dependency before deciding. Breaking on the first
				// not-yet-done one made the outcome depend on the lexical order
				// of the refs: a doomed step whose failed upstream happened to
				// sort after a still-running one stayed "pending" and held the
				// run open (for as long as a 24h wait, in the worst case)
				// instead of being skipped at once.
				failedDep, okDeps := "", true
				for _, d := range s.DependsOn {
					dep := byRef[d]
					if dep == nil || stepFailedTerminal(dep.Status) {
						failedDep = d
						break
					}
					if !stepOK(dep.Status) {
						okDeps = false
					}
				}
				if failedDep != "" {
					s.Status, s.Detail, s.At = stepStatusSkipped, "skipped: upstream step "+failedDep+" did not succeed", now.String()
					dirty, progressed = true, true
					if err := m.recordRunStep(ctx, sc, rec, *s, opStatusSkipped, GateStatus("")); err != nil {
						return err
					}
					continue
				}
				if okDeps {
					s.Status, s.At = stepStatusExecuting, now.String()
					claimed = append(claimed, runAction{step: *s})
					dirty = true
				}
			case stepStatusExecuting:
				// Crash recovery: a generic orphaned claim is failed after the
				// timeout. K4 commands are the narrow exception because their
				// neighbors expose durable semantic receipts/reservations.
				// The wording is deliberately UNCERTAIN: the claiming advancer may
				// have died before its side effect, after it, or still be alive and
				// slow. Recording "did not run" would be a fabricated fact; the
				// honest statement is that the outcome is unknown. Dependents skip
				// (failure-terminal) because cascading on an unknown is the unsafe
				// direction, and if the side effect DID land its true outcome is
				// reconciled into the ledger by pass C (recordLateOutcome).
				if t, perr := model.ParseTimestamp(s.At); perr == nil && now.Time().Sub(t.Time()) > executingTimeout {
					if retryableK4Step(s.Kind) {
						// K4 neighbors persist a receipt or dispatch reservation before
						// returning. Reusing the same attempt semantic therefore observes
						// the committed result (or the explicit ambiguous outcome) instead
						// of repeating the effect. This is recovery, not a blind retry.
						s.At, s.Detail = now.String(), "recovering exact durable K4 command"
						claimed = append(claimed, runAction{step: *s})
						dirty, progressed = true, true
						continue
					}
					s.Status, s.Detail, s.At = stepStatusFailed, "advance interrupted; outcome UNKNOWN, not retried (at-most-once)", now.String()
					dirty, progressed = true, true
					if err := m.recordRunStep(ctx, sc, rec, *s, opStatusFailed, GateStatus("")); err != nil {
						return err
					}
				}
			case stepStatusWaiting, stepStatusWaitingGate, stepStatusWaitingAck:
				polls = append(polls, *s)
			}
		}
		if dirty {
			rec[colWrSteps] = encodeRunSteps(steps)
			if rec, err = repo.Update(ctx, rec); err != nil {
				return err
			}
		}
		run = rec
		return nil
	})
	if err != nil || run == nil {
		return false, err
	}
	if len(claimed) == 0 && len(polls) == 0 {
		// Nothing in flight: settle the terminal status if every step is terminal.
		settled, serr := m.settleRun(ctx, mc, runID)
		return progressed || settled, serr
	}

	// Estate-wide kill switch: while stopped (or unreadable — fail CLOSED),
	// NOTHING advances: claims revert to pending, polls stay, the run shows why.
	verdict, verr := m.stopGate.Check(ctx, mc.Tenant, StopDims{})
	if verr != nil || verdict.Stopped {
		reason := "kill_switch"
		if verr != nil {
			m.errorf("orchestration: kill-switch gate error; freezing workflow run (deny-closed)", "run", runID.String(), "err", verr)
		}
		return progressed, m.freezeRun(ctx, mc, runID, claimed, reason)
	}

	// Pass B (act, OUTSIDE any tx): perform each claimed side effect and each poll.
	outcomes := make([]runOutcome, 0, len(claimed)+len(polls))
	for _, a := range claimed {
		outcomes = append(outcomes, m.executeStep(ctx, mc, run, a.step))
	}
	for _, p := range polls {
		if o, ok := m.pollStep(ctx, mc, run, p); ok {
			outcomes = append(outcomes, o)
		}
	}
	if len(outcomes) == 0 {
		return progressed || len(claimed) > 0, nil
	}

	// Pass C (resolve, in-tx): apply outcomes + evidence, settle terminal state.
	//
	// It runs on a context DETACHED from cancellation. Pass B has already
	// actuated; if the caller's request context dies in the window between the
	// side effect and this commit, the actuation is real but nothing records
	// it — and five minutes later the orphan sweep would append a row asserting
	// the step failed. The ledger would then positively state that something did
	// not happen when it did. Evidence for work already performed must not be
	// hostage to the client that happened to trigger it (the notify
	// finalizeDelivery rule); the bounded timeout keeps a wedged store from
	// pinning the advancer forever.
	resolveCtx, cancelResolve := context.WithTimeout(context.WithoutCancel(ctx), resolveTimeout)
	defer cancelResolve()
	ctx = resolveCtx
	err = mc.Data.Mutate(ctx, func(sc store.Scope) error {
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
		now := m.clock.Now().String()
		dirty := false
		for i := range steps {
			s := &steps[i]
			for _, o := range outcomes {
				if o.ref != s.Ref {
					continue
				}
				if s.Status != o.fromStatus {
					// A LATE outcome: the step left the status we claimed it in
					// while our side effect was in flight (the orphan timeout
					// fired, or a freeze reverted the claim). The run state has
					// moved on and must not be rewritten — but silently dropping
					// this would leave the ledger asserting something that did
					// not happen, which is the one thing evidence may never do.
					// Reconcile it instead: an immutable row stating what the
					// side effect ACTUALLY did.
					if err := m.recordLateOutcome(ctx, sc, rec, *s, o); err != nil {
						return err
					}
					continue
				}
				// A dynamically created root is fixed before the step resolves. The
				// next frontier can therefore never observe a successful create
				// without the root_work_item_id produced by that same effect.
				if o.rootWorkItemID != "" {
					current := rec.String(colWrRootWork)
					if current != "" && current != o.rootWorkItemID {
						return fmt.Errorf("orchestration: workflow root changed from %s to %s", current, o.rootWorkItemID)
					}
					if current == "" {
						rec[colWrRootWork] = o.rootWorkItemID
						dirty = true
					}
				}
				metadataChanged := false
				setString := func(dst *string, value string) {
					if value != "" && *dst != value {
						*dst, metadataChanged = value, true
					}
				}
				setInt := func(dst *int64, value int64) {
					if value > 0 && *dst != value {
						*dst, metadataChanged = value, true
					}
				}
				setString(&s.WorkItemID, o.workItemID)
				setString(&s.CommandID, o.commandID)
				setInt(&s.EventSeq, o.eventSeq)
				setString(&s.OutputKind, o.outputKind)
				setString(&s.OutputID, o.outputID)
				setInt(&s.OwnerEpoch, o.ownerEpoch)
				setInt(&s.LeaseFence, o.leaseFence)
				setString(&s.RemoteOutcome, o.remoteOutcome)
				setString(&s.RemoteCode, o.remoteCode)
				setString(&s.RemoteObservedAt, o.remoteObservedAt)
				setString(&s.RemotePlanHash, o.remotePlanHash)
				setString(&s.RemoteApprovalRef, o.remoteApprovalRef)
				setString(&s.RemoteBindingID, o.remoteBindingID)
				setString(&s.RemoteBindingSpecID, o.remoteBindingSpecID)
				setInt(&s.RemoteBindingSpecGeneration, o.remoteBindingSpecGeneration)
				setString(&s.RemoteAttemptID, o.remoteAttemptID)
				setInt(&s.RemoteGeneration, o.remoteGeneration)
				setString(&s.RemoteSyntheticSID, o.remoteSyntheticSID)
				setString(&s.RemoteResultKind, o.remoteResultKind)
				setString(&s.RemoteTaskID, o.remoteTaskID)
				setString(&s.RemoteContextID, o.remoteContextID)
				setString(&s.RemoteMessageID, o.remoteMessageID)
				setString(&s.RemoteState, o.remoteState)
				setString(&s.RemoteRevision, o.remoteRevision)
				setString(&s.RemoteWireHash, o.remoteWireHash)
				setString(&s.RemoteDetailHash, o.remoteDetailHash)
				setString(&s.RemoteCommandID, o.remoteCommandID)
				setString(&s.RemoteEventID, o.remoteEventID)
				setInt(&s.RemoteEventSeq, o.remoteEventSeq)
				setString(&s.RemoteWorkState, o.remoteWorkState)
				if o.remoteBindingID != "" && s.RemoteTerminal != o.remoteTerminal {
					s.RemoteTerminal, metadataChanged = o.remoteTerminal, true
				}
				setString(&s.WaitingTargetKind, o.waitingTargetKind)
				setString(&s.WaitingTargetID, o.waitingTargetID)
				setInt(&s.WaitingAfterEventSeq, o.waitingAfterEventSeq)
				setString(&s.WaitingDeadline, o.waitingDeadline)
				statusChanged := s.Status != o.status
				if !statusChanged && !metadataChanged {
					continue
				}
				if statusChanged {
					s.Status = o.status
				}
				s.At = now
				if o.detail != "" {
					s.Detail = clamp(o.detail, maxNameLen)
				}
				if o.approvalRef != "" {
					s.ApprovalRef = o.approvalRef
				}
				if o.dispatchRef != "" {
					s.DispatchRef = o.dispatchRef
				}
				if o.notBefore != "" {
					s.NotBefore = o.notBefore
				}
				dirty, progressed = true, true
				if statusChanged && o.ledger {
					if err := m.recordRunStep(ctx, sc, rec, *s, o.ledgerOp, o.gateStatus); err != nil {
						return err
					}
				}
			}
		}
		// Clear the pause marker only when THIS pass actually made progress: a
		// concurrent freeze may have stamped it after our gate check, and wiping
		// it unconditionally would hide a live kill switch behind a stale
		// outcome. A still-frozen run re-stamps on the next tick regardless.
		if progressed && rec.String(colWrPaused) != "" {
			rec[colWrPaused] = nil
			dirty = true
		}
		if dirty {
			rec[colWrSteps] = encodeRunSteps(steps)
			if _, err := repo.Update(ctx, rec); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return progressed, err
	}
	if _, err := m.settleRun(ctx, mc, runID); err != nil {
		return progressed, err
	}
	return progressed, nil
}

func retryableK4Step(kind string) bool {
	switch kind {
	case stepWorkCreate, stepWorkAssign, stepWorkClaim, stepSessionLaunch,
		stepWorkMessage, stepWorkWaitAck, stepWorkHandoff, stepWorkTransition,
		stepWorkCancel, stepWorkReconcile, stepRemotePlan, stepRemoteTest,
		stepRemoteStart, stepRemoteObserve, stepRemoteCancel:
		return true
	default:
		return false
	}
}

// freezeRun reverts claimed steps to pending and stamps the visible pause
// reason — an emergency stop freezes a run, it never fails it.
//
// It also appends a DURABLE ledger row the first time a run freezes. The pause
// marker is mutable run state that later passes clear, so without this the fact
// that an estate kill switch once halted a governed run would vanish the moment
// the stop lifted. The pre-flight denial (stopBlocksRun) already records the
// same policy decision; an in-flight freeze is no less worth keeping.
func (m *Module) freezeRun(ctx context.Context, mc api.ModuleContext, runID model.ID, claimed []runAction, reason string) error {
	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
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
		reverted := map[string]bool{}
		for _, a := range claimed {
			reverted[a.step.Ref] = true
		}
		for i := range steps {
			if reverted[steps[i].Ref] && steps[i].Status == stepStatusExecuting {
				steps[i].Status = stepStatusPending
			}
		}
		alreadyFrozen := rec.String(colWrPaused) == reason
		rec[colWrSteps] = encodeRunSteps(steps)
		rec[colWrPaused] = reason
		if _, err := repo.Update(ctx, rec); err != nil {
			return err
		}
		if alreadyFrozen {
			// Already recorded on the tick that froze it; a pump ticking every
			// few seconds must not flood the ledger with the same fact.
			return nil
		}
		return m.recordDecision(ctx, sc, decisionRow{
			subjectKind: "workflow", subjectRef: rec.String(colWrWorkflow), op: opRunStep,
			planHash: rec.String(colWrPlanHash), approvalRef: rec.String(colWrApproval),
			opStatus: opStatusBlocked, actor: rec.String(colWrActor), actorKind: rec.String(colWrActorKind),
			result: "run " + runID.String() + " frozen: " + reason,
		})
	})
}

// settleRun finalizes a run whose steps are all terminal: completed when every
// step succeeded, failed otherwise, with the terminal evidence row.
func (m *Module) settleRun(ctx context.Context, mc api.ModuleContext, runID model.ID) (bool, error) {
	settled := false
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, runID)
		if err != nil {
			return err
		}
		if rec.String(colWrStatus) != runStatusRunning {
			return nil
		}
		steps, err := decodeRunSteps(rec.String(colWrSteps))
		if err != nil {
			return err
		}
		allOK := true
		for _, s := range steps {
			if !stepTerminal(s.Status) {
				return nil
			}
			if !stepOK(s.Status) {
				allOK = false
			}
		}
		status, ledgerStatus := runStatusCompleted, opStatusCompleted
		if !allOK {
			status, ledgerStatus = runStatusFailed, opStatusFailed
		}
		rec[colWrStatus] = status
		rec[colWrFinished] = m.clock.Now().String()
		rec[colWrPaused] = nil
		if _, err := repo.Update(ctx, rec); err != nil {
			return err
		}
		settled = true
		return m.recordDecision(ctx, sc, decisionRow{
			subjectKind: "workflow", subjectRef: rec.String(colWrWorkflow), op: opRunEnd,
			planHash: rec.String(colWrPlanHash), approvalRef: rec.String(colWrApproval),
			opStatus: ledgerStatus, actor: rec.String(colWrActor), actorKind: rec.String(colWrActorKind),
			result: "run " + runID.String() + " " + status,
		})
	})
	return settled, err
}

// recordRunStep appends one step's immutable evidence row to the shared
// decision ledger, attributed to the run's ACCOUNTABLE principal (the phase-2
// initiator — the owner_actor rule for autonomous actuation).
func (m *Module) recordRunStep(ctx context.Context, sc store.Scope, run model.Record, s runStepState, opStatus string, gate GateStatus) error {
	// A step's own approval when it has one (a mid-graph gate), otherwise the
	// approval that authorized the RUN — so every step row joins back to the
	// human decision that permitted it, rather than leaving the authorizing
	// approval reachable only by re-reading the run row.
	approvalRef := s.ApprovalRef
	if approvalRef == "" {
		approvalRef = run.String(colWrApproval)
	}
	return m.recordDecision(ctx, sc, decisionRow{
		subjectKind: "workflow", subjectRef: run.String(colWrWorkflow), op: opRunStep,
		planHash: run.String(colWrPlanHash), approvalRef: approvalRef, gateStatus: gate,
		opStatus: opStatus, dispatchRef: s.DispatchRef,
		actor: run.String(colWrActor), actorKind: run.String(colWrActorKind),
		result: "run " + run.String(model.ColID) + " step " + s.Ref + ": " + s.Status,
		detail: s.Detail,
	})
}

// recordLateOutcome appends the reconciliation row for a side effect whose
// result arrived after the run state had already moved the step elsewhere (the
// orphan timeout failed the claim, or a kill-switch freeze reverted it). It
// exists so the append-only ledger never CLAIMS an actuation that did not
// happen and never HIDES one that did: the step's recorded state may say
// "outcome unknown", but the ledger carries the truth, with the dispatch
// reference the operator can correlate against the target system.
//
// A late outcome that actuated nothing (a pure pacing transition, or one whose
// own path already recorded a terminal denial) adds no information, so only
// outcomes that carry evidence are reconciled.
func (m *Module) recordLateOutcome(ctx context.Context, sc store.Scope, run model.Record, s runStepState, o runOutcome) error {
	if !o.ledger {
		return nil
	}
	// Rebuild the step shape the outcome describes — the ledger row must carry
	// the ACTUATION's own references, not the state the step was left in.
	actual := s
	actual.Status, actual.Detail = o.status, o.detail
	if o.dispatchRef != "" {
		actual.DispatchRef = o.dispatchRef
	}
	if o.approvalRef != "" {
		actual.ApprovalRef = o.approvalRef
	}
	return m.recordDecision(ctx, sc, decisionRow{
		subjectKind: "workflow", subjectRef: run.String(colWrWorkflow), op: opRunStep,
		planHash: run.String(colWrPlanHash), approvalRef: actual.ApprovalRef, gateStatus: o.gateStatus,
		opStatus: opStatusReconciled, dispatchRef: actual.DispatchRef,
		actor: run.String(colWrActor), actorKind: run.String(colWrActorKind),
		result: "run " + run.String(model.ColID) + " step " + s.Ref + ": late " + o.status + " reconciled (state was " + s.Status + ")",
		detail: o.detail,
	})
}

// executeStep performs ONE claimed step's side effect (outside any tx) and
// returns its outcome for pass C.
func (m *Module) executeStep(ctx context.Context, mc api.ModuleContext, run model.Record, s runStepState) runOutcome {
	out := runOutcome{ref: s.Ref, fromStatus: stepStatusExecuting, ledger: true}
	switch s.Kind {
	case stepScheduleFire:
		return m.executeScheduleFire(ctx, mc, run, s)
	case stepEventingEmit:
		var cfg eventingEmitConfig
		_ = json.Unmarshal(s.Config, &cfg)
		if m.host == nil {
			out.status, out.detail, out.ledgerOp = stepStatusFailed, "module has no bus host", opStatusFailed
			return out
		}
		if err := m.host.Publish(ctx, event.Event{
			Type: TypeWorkflowSignal, Tenant: mc.Tenant.String(), Source: Name, Time: m.clock.Now().Time(),
			Payload: WorkflowSignal{
				WorkflowRef: run.String(colWrWorkflow), RunRef: run.String(model.ColID),
				StepRef: s.Ref, Label: cfg.Label,
			},
		}); err != nil {
			out.status, out.detail, out.ledgerOp = stepStatusFailed, "publish failed", opStatusFailed
			return out
		}
		out.status, out.detail, out.ledgerOp = stepStatusEmitted, "workflow.signal published", opStatusDispatched
		return out
	case stepNotifyTest:
		return m.executeNotifyTest(ctx, mc, run, s)
	case stepWait:
		var cfg waitConfig
		_ = json.Unmarshal(s.Config, &cfg)
		out.status = stepStatusWaiting
		out.notBefore = model.NewTimestamp(m.clock.Now().Time().Add(time.Duration(cfg.Seconds) * time.Second)).String()
		out.detail = fmt.Sprintf("waiting %ds", cfg.Seconds)
		out.ledger = false // pure pacing: state, not governance evidence
		return out
	case stepApprovalGate:
		var cfg approvalGateConfig
		_ = json.Unmarshal(s.Config, &cfg)
		// Bound to a hash DERIVED from the run's, never the run's own: the two
		// approvals would otherwise be interchangeable, and a human's answer to
		// this in-run checkpoint could be replayed as the authorization to start
		// a whole new run (runPhaseDecide matches on the bound hash).
		decision, err := m.gate.Request(ctx, ApprovalRequest{
			Tenant: mc.Tenant, Action: "orchestration.workflow.gate", SubjectKind: "workflow_step",
			SubjectRef:  run.String(model.ColID) + "/" + s.Ref,
			PlanHash:    gatePlanHash(run.String(colWrPlanHash), s.Ref),
			RequestedBy: run.String(colWrActor),
		})
		if err != nil {
			out.status, out.detail, out.ledgerOp = stepStatusFailed, "approval gate unavailable", opStatusFailed
			return out
		}
		if decision.Status == StatusNoGate {
			// Deny-by-default: with no gate wired an approval-gate step can never
			// pass — block it now rather than waiting forever.
			out.status, out.detail, out.ledgerOp = stepStatusBlocked, "no approval gate wired (deny-by-default)", opStatusBlocked
			out.gateStatus = decision.Status
			return out
		}
		out.status, out.approvalRef = stepStatusWaitingGate, decision.ApprovalRef
		out.detail, out.gateStatus = "approval requested; run paused until resolved", decision.Status
		out.ledgerOp = opStatusRequested
		return out
	default:
		if isWorkStepKind(s.Kind) {
			return m.executeK4WorkStep(ctx, mc, run, s)
		}
		out.status, out.detail, out.ledgerOp = stepStatusFailed, "unknown step kind "+s.Kind, opStatusFailed
		return out
	}
}

// executeScheduleFire actuates a schedule-fire step: the schedule must still
// exist and not be retired, the FIN-08 budget gate must admit it (fail-open on
// gate error), and the dispatch leaves through the deny-closed seam. A real or
// declared fire advances the schedule's last_fired_at (a fire is activity).
func (m *Module) executeScheduleFire(ctx context.Context, mc api.ModuleContext, run model.Record, s runStepState) runOutcome {
	out := runOutcome{ref: s.Ref, fromStatus: stepStatusExecuting, ledger: true}
	var cfg scheduleFireConfig
	_ = json.Unmarshal(s.Config, &cfg)

	var sched model.Record
	found := false
	if err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(cfg.ScheduleID))
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		sched, found = rec, true
		return nil
	}); err != nil || !found {
		out.status, out.detail, out.ledgerOp = stepStatusFailed, "schedule "+cfg.ScheduleID+" not found", opStatusFailed
		return out
	}
	// A retired OR PAUSED routine must not fire: only "retired" was
	// rejected before, so a workflow could fire a schedule an operator had
	// deliberately paused.
	if st := sched.String(colDesiredStat); st != "active" {
		out.status, out.detail, out.ledgerOp = stepStatusFailed, "schedule "+cfg.ScheduleID+" is "+st+"; only an active schedule can fire", opStatusFailed
		return out
	}
	subjectKind, subjectRef := sched.String(colSubjectKind), sched.String(colSubjectRef)

	// Routine policy — the SAME admission as the direct fire path.
	// Without it, embedding a schedule in an approved workflow is a complete
	// bypass of the cadence floor and the blocked-environment control.
	stepReplay, srerr := m.fireAlreadyClaimed(ctx, mc, colOpOperationID, scheduleStepOperationID(run, s))
	if srerr != nil {
		out.status, out.detail, out.ledgerOp = stepStatusFailed, "could not read the step's operation claim", opStatusFailed
		return out
	}
	if d := m.admitScheduleFireUnlessReplay(ctx, mc, sched, stepReplay); d != nil {
		out.status, out.ledgerOp = stepStatusBlocked, opStatusBlocked
		out.detail = "denied by routine policy (" + d.code + "): " + d.message
		return out
	}

	// Agent-scoped kill switch (the estate scope was checked run-wide): a stop
	// on the FIRED agent blocks this step, failing CLOSED on a gate error.
	if subjectKind == nodeAgent {
		verdict, err := m.stopGate.Check(ctx, mc.Tenant, StopDims{AgentRef: subjectRef})
		if err != nil {
			m.errorf("orchestration: kill-switch gate error on workflow step; failing CLOSED", "run", run.String(model.ColID), "err", err)
			out.status, out.detail, out.ledgerOp = stepStatusBlocked, "kill-switch state unreadable; step denied (deny-closed)", opStatusBlocked
			return out
		}
		if verdict.Stopped {
			out.status, out.ledgerOp = stepStatusBlocked, opStatusBlocked
			out.detail = "denied: emergency stop active (" + verdict.Scope + " kill switch " + verdict.StopRef + ")"
			return out
		}
	}

	// FIN-08 budget pre-flight, fail-open (the budgetBlocksFire contract).
	// RoutineRef carries the schedule id exactly as the direct fire path does
	// — it was missing here, so a per-routine enforcing budget did
	// NOT scope a workflow-driven fire of the same routine.
	dims := BudgetDims{RoutineRef: sched.String(model.ColID)}
	if subjectKind == nodeAgent {
		dims.AgentRef = subjectRef
	}
	if verdict, err := m.budgetGate.Check(ctx, mc.Tenant, dims); err != nil {
		m.errorf("orchestration: budget gate error on workflow step; failing open", "run", run.String(model.ColID), "err", err)
	} else if !verdict.Allowed {
		reason := verdict.Reason
		if reason == "" {
			reason = "step denied: budget cap reached"
		}
		out.status, out.detail, out.ledgerOp = stepStatusBudget, reason, opStatusBudgetBlocked
		return out
	}

	// MF1: the same pre-dispatch cadence reservation the direct fire takes.
	if pol, pd := m.resolvePolicyTenant(ctx, mc.Tenant, routineScopeOfSchedule(sched)); pd != nil && !stepReplay {
		out.status, out.detail, out.ledgerOp = stepStatusBlocked, "denied by routine policy ("+pd.code+")", opStatusBlocked
		return out
	} else if !stepReplay && pol.MinIntervalSec > 0 {
		var rd *routineDenial
		if ferr := m.withAdmissionFence(ctx, mc, true, func(sc store.Scope) error {
			d, e := m.reserveFireSlot(ctx, sc, pol, sched)
			rd = d
			return e
		}); ferr != nil {
			out.status, out.detail, out.ledgerOp = stepStatusFailed, "could not reserve the routine cadence slot", opStatusFailed
			return out
		} else if rd != nil {
			out.status, out.ledgerOp = stepStatusBlocked, opStatusBlocked
			out.detail = "denied by routine policy (" + rd.code + "): " + rd.message
			return out
		}
	}

	// Item 6: with NO dispatcher wired nothing actuates, so DECLARE honestly
	// rather than blocking on a missing target-binding key — a missing key must
	// not masquerade as a missing dispatcher (and vice-versa). Only a WIRED
	// dispatcher (an actuation would really happen) makes an unverifiable target a
	// hard block.
	if !m.dispatcherWired() {
		out.status, out.detail, out.ledgerOp = stepStatusDeclared, "no dispatcher wired; declared, not fired", opStatusDeclaredNotFired
		return out
	}

	// D-06 + hole c2 (single-read TOCTOU): verify the schedule read ABOVE
	// still fingerprints to what the human approved, and dispatch the subject
	// from that SAME read. A re-pointed schedule (or a rotated secret) changes the
	// HMAC and BLOCKS — and because verify and dispatch consume ONE read, no
	// second read can slip a different target between them.
	if proceed, detail := m.verifyScheduleTarget(sched, s); !proceed {
		out.status, out.detail, out.ledgerOp = stepStatusBlocked, detail, opStatusBlocked
		return out
	}

	// D-05/D-06: reserve the step's single-use operation (its OperationID +
	// anchor + outbox) BEFORE the effect, so a re-executed (crash-recovered) step
	// replays the recorded outcome instead of re-actuating.
	claim, cerr := m.claimScheduleStepOperation(ctx, mc, run, s, cfg.ScheduleID)
	if errors.Is(cerr, errEvidenceGap) {
		out.status, out.detail, out.ledgerOp = stepStatusBlocked, "evidence unavailable (spool degraded); step refused", opStatusBlocked
		return out
	}
	if cerr != nil {
		m.errorf("orchestration: workflow step operation claim failed", "run", run.String(model.ColID), "err", cerr)
		out.status, out.detail, out.ledgerOp = stepStatusFailed, "operation claim failed", opStatusFailed
		return out
	}
	if claim.replay {
		return scheduleStepReplayOutcome(out, claim.rec)
	}
	// Frozen evidence law: no effect without an anchored receipt for THIS binding.
	if claim.receipt.MustRefuse(claim.binding) {
		out.status, out.detail, out.ledgerOp = stepStatusBlocked, "evidence not anchored; step refused ("+string(claim.receipt.FailureClass(claim.binding))+")", opStatusBlocked
		return out
	}

	result, derr := m.dispatch.Fire(ctx, FireRequest{
		Tenant: mc.Tenant, SubjectKind: subjectKind, SubjectRef: subjectRef,
		ScheduleRef: cfg.ScheduleID, PlanHash: run.String(colWrPlanHash),
		OperationID: string(claim.binding.OperationID),
	})
	opState, obState := opStateDispatched, obStateDispatched
	switch {
	case derr == nil:
		out.status, out.dispatchRef, out.detail, out.ledgerOp = stepStatusDispatched, result.Ref, "dispatched", opStatusDispatched
	case errors.Is(derr, errNoDispatcher):
		out.status, out.detail, out.ledgerOp = stepStatusDeclared, "approved; no dispatcher wired (declared, not fired)", opStatusDeclaredNotFired
		opState, obState = opStateDeclared, obStateReady
	case errors.Is(derr, ErrDispatchAmbiguous):
		// The effect MAY have actuated: record UNKNOWN (never re-dispatched), and
		// fail the step (the safe direction for dependents), not "declared".
		m.errorf("orchestration: workflow step dispatch ambiguous; recording unknown", "run", run.String(model.ColID), "err", derr)
		out.status, out.detail, out.ledgerOp = stepStatusFailed, "dispatch outcome uncertain; not re-actuated", opStatusFailed
		opState, obState = opStateUnknown, obStateUnknown
	default:
		m.errorf("orchestration: workflow step dispatcher failed", "run", run.String(model.ColID), "err", derr)
		out.status, out.detail, out.ledgerOp = stepStatusFailed, "dispatcher error", opStatusFailed
		opState, obState = opStateFailed, obStateFailed
	}
	// Settle the operation (+advanceFired) in one transaction. A settle FAILURE
	// must NOT let pass C record the step as dispatched while the operation stays
	// "claimed" (hole c4): mark the step failed and leave the operation uncertain,
	// so a re-execution replays the claim without re-firing (at-most-once).
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		if err := m.settleOperation(ctx, sc, claim, opState, obState, out.dispatchRef, out.detail); err != nil {
			return err
		}
		if out.status == stepStatusDispatched {
			return m.advanceFired(ctx, sc, model.ID(cfg.ScheduleID))
		}
		return nil
	}); err != nil {
		m.errorf("orchestration: failed to settle workflow-step fire operation; marking step uncertain", "schedule", cfg.ScheduleID, "err", err)
		out.status, out.dispatchRef, out.detail, out.ledgerOp = stepStatusFailed, "", "effect emitted but evidence settle failed; outcome uncertain", opStatusFailed
	}
	return out
}

// dispatcherWired reports whether a real actuation dispatcher is wired (i.e. the
// deny-closed default is NOT in place). Used to DECLARE rather than block a
// schedule-fire step when nothing could actuate anyway (item 6).
func (m *Module) dispatcherWired() bool {
	_, unwired := m.dispatch.(unwiredDispatcher)
	return !unwired
}

// verifyScheduleTarget checks an ALREADY-READ schedule record against the
// approved binding, from that SAME read — verify and dispatch cannot straddle a
// second read (hole c2). An empty BindProfile (unbound at approval) or an
// unavailable key BLOCKS deny-closed; a changed fingerprint BLOCKS.
func (m *Module) verifyScheduleTarget(sched model.Record, s runStepState) (proceed bool, detail string) {
	if s.BindProfile == "" {
		return false, "target binding unavailable at approval (no key); re-approval required"
	}
	fp, _, ok := m.targetHMAC(m.scheduleTargetString(sched))
	if !ok {
		return false, "target could not be verified (no key); step blocked"
	}
	if fp != s.ApprovedTarget {
		return false, "target changed since approval; step blocked (re-approval required)"
	}
	return true, ""
}

// scheduleStepOperationID is the DETERMINISTIC single-use identity of one
// schedule-fire step. It is a function, not an inline expression, because two
// call sites depend on it agreeing EXACTLY: the claim that reserves the
// operation, and the replay probe that decides whether to skip the routine-
// policy admission. If those two ever computed different ids the probe would
// silently never fire, and a crash-recovered step would be refused by the
// cadence floor instead of replaying the effect it already performed.
func scheduleStepOperationID(run model.Record, s runStepState) string {
	return canonicalHash("orchestration.workflow.step.v2", run.String(model.ColID), s.Ref, s.Generation)
}

// claimScheduleStepOperation reserves a schedule-fire step's single-use operation.
// Its OperationID is DETERMINISTIC (namespaced from run+step+generation) so a
// crash-recovered re-execution recomputes the same id and replays; approval_ref
// is namespaced per step (the run approval + step ref) so it never collides with
// the run-level operation's UNIQUE(approval_ref).
func (m *Module) claimScheduleStepOperation(ctx context.Context, mc api.ModuleContext, run model.Record, s runStepState, scheduleRef string) (*operationClaim, error) {
	runID := run.String(model.ColID)
	spec := operationSpec{
		tenant:      mc.Tenant.String(),
		approvalRef: run.String(colWrApproval) + ":" + s.Ref,
		operationID: scheduleStepOperationID(run, s),
		surface:     surfaceWorkflowStep, action: surfaceScheduleFire,
		planHash: run.String(colWrPlanHash), policyVersion: s.Generation, bindProfile: s.BindProfile,
		targetFp: s.ApprovedTarget, scheduleRef: scheduleRef, auditTarget: model.ID(runID),
	}
	claim, err := m.claimOperation(ctx, mc, spec)
	if errors.Is(err, errOperationRaced) {
		claim, err = m.claimOperation(ctx, mc, spec)
	}
	return claim, err
}

// scheduleStepReplayOutcome reflects an already-recorded step operation without
// re-actuating (crash-recovery idempotency).
func scheduleStepReplayOutcome(out runOutcome, op model.Record) runOutcome {
	switch op.String(colOpState) {
	case opStateDispatched:
		out.status, out.dispatchRef, out.detail, out.ledgerOp = stepStatusDispatched, op.String(colOpDispatchRef), "dispatched (replay)", opStatusDispatched
	case opStateDeclared:
		out.status, out.detail, out.ledgerOp = stepStatusDeclared, "approved; no dispatcher wired (replay)", opStatusDeclaredNotFired
	default:
		out.status, out.detail, out.ledgerOp = stepStatusFailed, "prior fire outcome uncertain; not re-actuated", opStatusFailed
	}
	return out
}

// executeNotifyTest actuates a notify-test step: it verifies the route still
// fingerprints to the approved binding (D-06 block-on-change), reserves the
// step's single-use operation (anchor+outbox before the effect, D-05), then
// sends the synthetic test through the deny-closed notify seam.
func (m *Module) executeNotifyTest(ctx context.Context, mc api.ModuleContext, run model.Record, s runStepState) runOutcome {
	out := runOutcome{ref: s.Ref, fromStatus: stepStatusExecuting, ledger: true}
	var cfg notifyTestConfig
	_ = json.Unmarshal(s.Config, &cfg)

	// D-06: a re-pointed route (destination/config changed since approval) BLOCKS.
	// An unwired notifier DECLARES (nothing actuates), preserving the honest no-op.
	proceed, declared, detail := m.checkStepTarget(ctx, mc, s)
	if declared {
		out.status, out.detail, out.ledgerOp = stepStatusDeclared, "no notify actuator wired; declared, not sent", opStatusDeclaredNotFired
		return out
	}
	if !proceed {
		out.status, out.detail, out.ledgerOp = stepStatusBlocked, detail, opStatusBlocked
		return out
	}

	runID := run.String(model.ColID)
	spec := operationSpec{
		tenant:      mc.Tenant.String(),
		approvalRef: run.String(colWrApproval) + ":" + s.Ref,
		operationID: canonicalHash("orchestration.workflow.step.v2", runID, s.Ref, s.Generation),
		surface:     surfaceWorkflowStep, action: "orchestration.notify.test",
		planHash: run.String(colWrPlanHash), policyVersion: s.Generation, bindProfile: s.BindProfile,
		targetFp: s.ApprovedTarget, auditTarget: model.ID(runID),
	}
	claim, cerr := m.claimOperation(ctx, mc, spec)
	if errors.Is(cerr, errOperationRaced) {
		claim, cerr = m.claimOperation(ctx, mc, spec)
	}
	if errors.Is(cerr, errEvidenceGap) {
		out.status, out.detail, out.ledgerOp = stepStatusBlocked, "evidence unavailable (spool degraded); step refused", opStatusBlocked
		return out
	}
	if cerr != nil {
		m.errorf("orchestration: notify-test operation claim failed", "run", runID, "err", cerr)
		out.status, out.detail, out.ledgerOp = stepStatusFailed, "operation claim failed", opStatusFailed
		return out
	}
	if claim.replay {
		switch claim.rec.String(colOpState) {
		case opStateDispatched:
			out.status, out.detail, out.ledgerOp = stepStatusNotified, "test notification (replay)", opStatusDispatched
		case opStateDeclared:
			out.status, out.detail, out.ledgerOp = stepStatusDeclared, "no notify actuator wired (replay)", opStatusDeclaredNotFired
		default:
			out.status, out.detail, out.ledgerOp = stepStatusFailed, "prior notify outcome uncertain; not re-sent", opStatusFailed
		}
		return out
	}
	// Frozen evidence law: no effect without an anchored receipt for THIS binding.
	if claim.receipt.MustRefuse(claim.binding) {
		out.status, out.detail, out.ledgerOp = stepStatusBlocked, "evidence not anchored; step refused ("+string(claim.receipt.FailureClass(claim.binding))+")", opStatusBlocked
		return out
	}

	// Hole c1 + deferral #6: deliver through the ATOMIC seam that re-reads the
	// route ONCE, refuses if its fingerprint no longer equals the approved one, and
	// delivers THAT verified read — so a route re-pointed between the check and the
	// send can never divert the delivery (RunRouteTest's independent re-read did).
	// The SAME OperationID rides along for receiver dedup.
	status, detail, err := m.notifyTest.TestBound(ctx, mc.Tenant, cfg.RouteID, s.RouteFp, string(claim.binding.OperationID))
	opState, obState := opStateDispatched, obStateDispatched
	switch {
	case errors.Is(err, errNoNotifyTester):
		out.status, out.detail, out.ledgerOp = stepStatusDeclared, "no notify actuator wired; declared, not sent", opStatusDeclaredNotFired
		opState, obState = opStateDeclared, obStateReady
	case errors.Is(err, ErrRouteBindingChanged):
		out.status, out.detail, out.ledgerOp = stepStatusBlocked, "route changed since approval; step blocked (re-approval required)", opStatusBlocked
		opState, obState = opStateFailed, obStateFailed
	case err != nil:
		out.status, out.detail, out.ledgerOp = stepStatusFailed, "notify test failed: "+clamp(err.Error(), 120), opStatusFailed
		opState, obState = opStateFailed, obStateFailed
	default:
		out.status, out.detail, out.ledgerOp = stepStatusNotified, "test notification "+status+": "+clamp(detail, 100), opStatusDispatched
	}
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		return m.settleOperation(ctx, sc, claim, opState, obState, "", out.detail)
	}); err != nil {
		m.errorf("orchestration: failed to settle notify-test operation; marking step uncertain", "run", runID, "err", err)
		out.status, out.detail, out.ledgerOp = stepStatusFailed, "effect emitted but evidence settle failed; outcome uncertain", opStatusFailed
	}
	return out
}

// pollStep evaluates a waiting/waiting_approval step without claiming it (the
// checks are read-only and idempotent; the version-checked resolve makes a
// double evaluation harmless). ok=false means no transition yet.
func (m *Module) pollStep(ctx context.Context, mc api.ModuleContext, run model.Record, s runStepState) (runOutcome, bool) {
	switch s.Status {
	case stepStatusWaiting:
		t, err := model.ParseTimestamp(s.NotBefore)
		if err != nil || m.clock.Now().Time().Before(t.Time()) {
			return runOutcome{}, false
		}
		return runOutcome{
			ref: s.Ref, fromStatus: stepStatusWaiting, status: stepStatusDone,
			detail: "wait elapsed", ledger: false,
		}, true
	case stepStatusWaitingGate:
		want := gatePlanHash(run.String(colWrPlanHash), s.Ref)
		decision, err := m.gate.Status(ctx, ApprovalCheck{
			Tenant: mc.Tenant, ApprovalRef: s.ApprovalRef, PlanHash: want,
			Action: "orchestration.workflow.gate", SubjectKind: "workflow_step",
			SubjectRef: run.String(model.ColID) + "/" + s.Ref,
		})
		if err != nil {
			m.errorf("orchestration: approval gate status failed on workflow step", "run", run.String(model.ColID), "err", err)
			return runOutcome{}, false
		}
		if decision.Status == StatusPending {
			return runOutcome{}, false
		}
		if decision.Allowed() && decision.PlanHash == want {
			return runOutcome{
				ref: s.Ref, fromStatus: stepStatusWaitingGate, status: stepStatusGatePassed,
				detail: "approved", gateStatus: decision.Status, ledger: true, ledgerOp: opStatusGatePassed,
			}, true
		}
		return runOutcome{
			ref: s.Ref, fromStatus: stepStatusWaitingGate, status: stepStatusBlocked,
			detail: "denied: " + string(decision.Status), gateStatus: decision.Status,
			ledger: true, ledgerOp: opStatusBlocked,
		}, true
	case stepStatusWaitingAck:
		return m.pollK4WorkAck(ctx, mc, run, s)
	}
	return runOutcome{}, false
}
