// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// workflow_schema.go declares the DAG-workflow entities: a governed,
// revisioned workflow declaration (its step graph rides as ONE bounded JSON
// document — the graph is edited and approved as a whole, never step-by-step),
// its append-only revision ledger (the schedule-revision pattern), and the
// run state row (per-step execution state as a JSON document; the IMMUTABLE
// evidence of every actuating step lives in the shared decision ledger, not
// here — state is mutable and compact, evidence is append-only).
//
// Minimal data (docs/SECURITY-HARDENING.md): a step config holds REFERENCES and bounded labels
// only — a schedule id, a route id, a wait duration, an approval reason. There
// is no step kind that carries a command line, an HTTP target, a payload or a
// secret; arbitrary side-effect steps are explicitly out of scope.
const (
	workflowKind  model.Kind = "orchestration.workflow"
	workflowTable            = "orchestration_workflow"

	wfRevisionKind  model.Kind = "orchestration.workflow_revision"
	wfRevisionTable            = "orchestration_workflow_revision"

	wfRunKind  model.Kind = "orchestration.workflow_run"
	wfRunTable            = "orchestration_workflow_run"
)

// workflow columns — the governed DAG declaration. MUTABLE (name immutable
// after create: it is the tenant-unique identity, the schedule-name rule).
const (
	colWfName    = "name"        // logical workflow name (unique per tenant)
	colWfDesc    = "description" // operator prose, bounded
	colWfEnabled = "enabled"     // disabled workflows accept no new run (running runs finish)
	colWfSteps   = "steps"       // the FULL step graph as canonical JSON ([]stepDTO)
	colWfOwnerA  = "owner_actor" // declaring principal — accountable for autonomous runs
	colWfOwnerK  = "owner_actor_kind"
	// The user-facing config version is the engine's own optimistic-concurrency
	// counter (model.ColVersion): the workflow row is written ONLY by config
	// mutations, so the base column IS the config revision number — no shadow
	// column (the updated_at lesson: base columns are reserved).
)

// workflow_revision columns — the append-only config history (pattern:
// full post-state snapshot in the SAME transaction as the mutation).
const (
	colWfRevSubject  = "subject_id" // the workflow this revision belongs to
	colWfRevOp       = "op"         // create | update | restore
	colWfRevSnapshot = "snapshot"   // workflowDetailDTO JSON
	colWfRevActor    = "actor"
	colWfRevActorK   = "actor_kind"
)

// workflow_run columns — one governed execution. The per-step state rides as a
// JSON document (bounded by the step cap); every actuating step ALSO appends an
// immutable row to the shared decision ledger.
const (
	colWrWorkflow          = "workflow_ref"      // the workflow this run executes
	colWrRootWork          = "root_work_item_id" // nullable until work-create materializes the root
	colWrStatus            = "status"            // running | completed | failed
	colWrPlanHash          = "plan_hash"         // the exact graph the approval was bound to (anti-TOCTOU)
	colWrApproval          = "approval_ref"      // the phase-1 approval consumed to start the run
	colWrPaused            = "paused_reason"     // "" | "kill_switch" — set/cleared by the runner, visible state
	colWrSteps             = "steps"             // []runStepState JSON — per-step execution state
	colWrActor             = "actor"             // the phase-2 initiator — the accountable principal for every step
	colWrActorKind         = "actor_kind"
	colWrActorAdmin        = "actor_work_admin"
	colWrUserIdentity      = "actor_user_identity"
	colWrAgentIdentity     = "actor_agent_identity"
	colWrSessionIdentity   = "actor_session_identity"
	colWrSessionRunRef     = "actor_session_run_ref"
	colWrSessionFence      = "actor_session_fence"
	colWrPurposeRestricted = "actor_purpose_restricted"
	colWrCredentialBinding = "actor_credential_binding" // opaque core/auth handle; never projected
	colWrStartedAt         = "started_at"
	colWrFinished          = "finished_at"
)

// Run statuses (colWrStatus).
const (
	runStatusRunning   = "running"
	runStatusCompleted = "completed"
	runStatusFailed    = "failed"
)

// A run's initiator, as its actor ref and as its bare account id. The run
// creation fences what they name and the retirement step reads them (fence.go,
// retirement.go).
var (
	pdeclRunActor        = model.Ref(model.EncodeUserRef, model.ClassAuthority)
	pdeclRunUserIdentity = model.Ref(model.EncodeUserID, model.ClassAuthority)
)

// registerWorkflowSchema declares the three entities; called from
// RegisterSchema so the engine creates the tables with tenant isolation and the
// append-only guard on the revision ledger.
func registerWorkflowSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  workflowKind,
		Table: workflowTable,
		Fields: []model.FieldSpec{
			{Name: colWfName, Kind: model.KindText, Indexed: true, Principal: pdeclNoneName},
			{Name: colWfDesc, Kind: model.KindText, Nullable: true, Principal: pdeclNoneWorkflowText},
			{Name: colWfEnabled, Kind: model.KindBool, Indexed: true},
			{Name: colWfSteps, Kind: model.KindJSON, Principal: workflowStepsDecl},
			// The declaring caller's actor ref (workflow.go:320); a run acts for its
			// own initiator, never for this owner.
			{Name: colWfOwnerA, Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
			{Name: colWfOwnerK, Kind: model.KindText, Principal: pdeclNoneActorKind},
		},
		Indexes: []model.IndexSpec{{
			Name:    "orchestration_workflow_uniq",
			Columns: []string{model.ColTenantID, colWfName},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       wfRevisionKind,
		Table:      wfRevisionTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colWfRevSubject, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRevisionOf},
			{Name: colWfRevOp, Kind: model.KindText, Principal: pdeclNoneRevisionOp},
			// A past workflow detail, steps included. History: it acts again only
			// through the restore, which re-validates the graph (workflow.go:655-678).
			{Name: colWfRevSnapshot, Kind: model.KindText, Principal: model.Scan(model.ClassEvidence)},
			{Name: colWfRevActor, Kind: model.KindText, Principal: pdeclActorEvidence},
			{Name: colWfRevActorK, Kind: model.KindText, Principal: pdeclNoneActorKind},
		},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:  wfRunKind,
		Table: wfRunTable,
		Fields: []model.FieldSpec{
			{Name: colWrWorkflow, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneRunState},
			{Name: colWrRootWork, Kind: model.KindUUID, Nullable: true, Indexed: true, Principal: pdeclNoneRunState},
			{Name: colWrStatus, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRunState},
			{Name: colWrPlanHash, Kind: model.KindText, Principal: pdeclNoneRunState},
			{Name: colWrApproval, Kind: model.KindText, Nullable: true, Principal: pdeclNoneRunState},
			{Name: colWrPaused, Kind: model.KindText, Nullable: true, Principal: pdeclNoneRunState},
			{Name: colWrSteps, Kind: model.KindJSON, Principal: runStepsDecl},
			// The initiator every step of the run acts for: its actor ref and its
			// account id are replayed into each step's actor (workflow_work_run.go:18-28).
			{Name: colWrActor, Kind: model.KindText, Principal: pdeclRunActor},
			{Name: colWrActorKind, Kind: model.KindText, Principal: pdeclNoneRunCaller},
			{Name: colWrActorAdmin, Kind: model.KindBool, Nullable: true},
			{Name: colWrUserIdentity, Kind: model.KindUUID, Nullable: true, Principal: pdeclRunUserIdentity},
			{Name: colWrAgentIdentity, Kind: model.KindText, Nullable: true, Principal: pdeclNoneRunCaller},
			{Name: colWrSessionIdentity, Kind: model.KindText, Nullable: true, Principal: pdeclNoneRunCaller},
			{Name: colWrSessionRunRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneRunCaller},
			{Name: colWrSessionFence, Kind: model.KindInt, Nullable: true},
			{Name: colWrPurposeRestricted, Kind: model.KindBool, Nullable: true},
			// The opaque handle of the run's credential binding. Only core/auth
			// resolves it, and only for this run's own subject; it names no account.
			{Name: colWrCredentialBinding, Kind: model.KindText, Nullable: true, Principal: model.None(
				"an opaque credential-binding handle, parsed as such: modules/orchestration/workflow_work_run.go:20")},
			{Name: colWrStartedAt, Kind: model.KindTimestamp},
			{Name: colWrFinished, Kind: model.KindTimestamp, Nullable: true},
		},
	})
}
