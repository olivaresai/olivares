// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables (longest, orchestration_schedule,
// is 22 chars — within the 40-char module-table cap).
const (
	relationKind  model.Kind = "orchestration.relation"
	relationTable            = "orchestration_relation"
	scheduleKind  model.Kind = "orchestration.schedule"
	scheduleTable            = "orchestration_schedule"
	decisionKind  model.Kind = "orchestration.decision"
	decisionTable            = "orchestration_decision"
	// operation is the durable, SINGLE-USE identity of one governed effect (D-05/D-06, sdk/evidence.go contract). It is the mutable claim the append-
	// only decision ledger cannot be: an acting path reserves the operation (its
	// OperationID) and its evidence anchor + outbox in ONE transaction BEFORE the
	// effect leaves, so the gate's pure-read Status can never authorize a second
	// dispatch of the same approval, and a retry after a lost outcome replays the
	// recorded result instead of re-actuating. UNIQUE(tenant, operation_id) is the
	// idempotency identity; UNIQUE(tenant, approval_ref) makes the direct-fire
	// reservation atomic under concurrency (the loser's insert collides as
	// store.ErrConflict and re-reads).
	operationKind  model.Kind = "orchestration.operation"
	operationTable            = "orchestration_operation"
	// outbox is the durable dispatch intent: the effect is emitted ONLY by
	// draining an outbox row whose reconstructed receipt AnchoredFor its binding.
	// A CAS ready→dispatch_started makes a duplicate drain a no-op; the SAME
	// OperationID propagates downstream for receiver-side dedup.
	outboxKind  model.Kind = "orchestration.outbox"
	outboxTable            = "orchestration_outbox"
	// runTargetBinding freezes, immutably per run+step, the HMAC fingerprint of
	// the effect-bearing target a human approved (D-06). Execution recomputes it
	// against the CURRENT config and BLOCKS on any change — a re-pointed schedule/
	// route or a rotated secret voids the approval rather than acting on it.
	runTargetBindingKind  model.Kind = "orchestration.run_target_binding"
	runTargetBindingTable            = "orchestration_run_target_binding"
)

// relation columns — one derived communication/delegation edge (the comm graph).
// MUTABLE: counts/timing accumulate via idempotent upsert. NO payload column.
const (
	colSupervisorRef = "supervisor_ref" // origin: the session/agent that delegates or talks
	colWorkerRef     = "worker_ref"     // target: the subagent/agent/mcp endpoint
	colLinkKind      = "link_kind"      // "delegation" | "mcp_server" | "mcp_tool"
	colToolRef       = "tool_ref"       // the tool/verb (e.g. "Task", an MCP tool); "" when none
	colMode          = "mode"           // R/RW class carried by the underlying edge ("unknown" for delegation)
	colSignalSource  = "signal_source"  // the observing signal (e.g. "otel", "mcp_annotation")
	colConfidence    = "confidence"     // "attributed" | "approximate" — shown, never faked
	colDelegationCnt = "delegation_count"
	colFirstSeenAt   = "first_seen_at"
	colLastSeenAt    = "last_seen_at"
)

// schedule columns — a governed desired-state declaration for a scheduled/
// autonomous agent. MUTABLE lifecycle. NO action-payload/credential/code column.
const (
	colSchedName   = "name"         // logical schedule name (unique per tenant)
	colSubjectKind = "subject_kind" // "agent" | "swarm"
	colSubjectRef  = "subject_ref"  // the agent/swarm external ref this schedule governs
	colTriggerKind = "trigger_kind" // "cron" | "event" | "manual"
	colCadenceSpec = "cadence_spec" // OPAQUE cron expr OR event-type ref — never parsed-to-fire here
	colExpectedIvl = "expected_interval_seconds"
	colGraceFactor = "grace_factor"
	colDesiredStat = "desired_status" // "active" | "paused" | "retired"
	colOwnerActor  = "owner_actor"    // declaring principal — the accountable actor for autonomous fires
	colOwnerActorK = "owner_actor_kind"
	colLastFiredAt = "last_fired_at"
	colMissedAt    = "missed_at" // sticky cadence-miss marker; set/cleared by the read-time cadence scan
	// Review MF1: the cadence RESERVATION, stamped BEFORE a fire leaves.
	// last_fired_at only advances after the dispatch settles, so on its own the
	// floor is a check-then-act: two approved fires read the same old stamp and
	// both dispatch inside the prohibited interval. This is committed under the
	// admission fence before the effect, so the second caller sees it. A fire
	// that then fails leaves the reservation standing — delaying the next
	// attempt by the floor is the safe direction.
	colFireReservedAt = "fire_reserved_at"
	// the routine's own GOVERNANCE SCOPE, frozen at declaration. The
	// Routine policy scopes to tenant | workspace | user, so enforcing it
	// on a later patch/restore/fire requires the OWNER's axes, not the current
	// caller's: an admin (or a token) may act on a schedule they did not
	// declare, and resolving user/workspace policy from the live principal
	// would let them step outside the owner's policy. owner_actor above is an
	// audit string ("user:<id>"/"token:<id>"), not a scope key — these are.
	colOwnerUserRef = "owner_user_ref" // declaring principal's user id ("" when none)
	colWorkspaceRef = "workspace_ref"  // declaring principal's confined workspace ("" when unconfined)
)

// admission fence — the per-tenant serialization point for every change
// to the ACTIVE routine population. Counting active schedules inside Mutate is
// NOT sufficient on its own: PostgreSQL's default Read Committed lets two
// transactions both read N-1 and both insert, so the max_active_routines
// cap would be exceeded by concurrent creates (the same phantom the existing
// workflow-count and user-seat caps accept). Every admitting transaction first
// version-CASes this single row, so the loser conflicts, retries, and re-counts
// against the winner's committed row.
const (
	admissionFenceKind  model.Kind = "orchestration.admission_fence"
	admissionFenceTable            = "orchestration_admission_fence"

	colFenceKey = "fence_key" // constant per fence family; "routine" today
	// activation claim (review M1) — the SINGLE-USE identity of one
	// consumed activation approval. ApprovalGate.Status is a pure READ: it
	// reports "approved" for as long as the row says so, so without a claim the
	// same approval re-activates a routine an operator paused, forever. The
	// fire path already learned this and reserves a durable operation keyed on
	// UNIQUE(tenant, approval_ref); this is the same guard for the declaration
	// side, minus the evidence anchoring a non-actuating write does not need.
	activationClaimKind  model.Kind = "orchestration.activation_claim"
	activationClaimTable            = "orchestration_activation_claim"

	colAcApprovalRef = "approval_ref" // the spent approval (unique per tenant)
	colAcScheduleRef = "schedule_ref" // what it activated (evidence)
	colAcPlanHash    = "plan_hash"    // the shape it was bound to (evidence)
	// fenceKeyRoutine is the single routine-admission fence per tenant.
	// Deliberately COARSE: routine declarations are low-volume, and one lock
	// order is far easier to prove correct than per-scope fences.
	fenceKeyRoutine = "routine"
)

// decision columns — the APPEND-ONLY fire/miss governance-evidence ledger (the
// deploy_operation shape: docs/SECURITY-HARDENING.md/compliance consumes op_status).
const (
	colDecSubjectKind = "subject_kind"
	colDecSubjectRef  = "subject_ref"
	colScheduleRef    = "schedule_ref"
	colOp             = "op"           // "fire_request" | "fire" | "cadence_miss" | "disable"
	colPlanHash       = "plan_hash"    // hash of the exact schedule+cadence the approval is bound to (anti-TOCTOU)
	colApprovalRef    = "approval_ref" // governance approval id (when gated)
	colGateStatus     = "gate_status"  // effective gate decision: approved/pending/rejected/expired/no_gate/not_required
	colOpStatus       = "op_status"    // requested | blocked | dispatched | declared_not_fired | failed
	colDispatchRef    = "dispatch_ref"
	colActor          = "actor" // REAL principal — never the system actor
	colActorKind      = "actor_kind"
	colDetailHash     = "detail_hash"
	colResult         = "result" // short, non-sensitive outcome summary
	colOccurredAt     = "occurred_at"
)

// operation columns — the durable single-use identity of one governed effect
// (sdk/evidence.go). MUTABLE lifecycle: claimed → (outbox drained) →
// dispatched|declared|failed|unknown. Minimal data: refs and opaque digests
// only, never a payload/command/secret.
const (
	colOpApprovalRef  = "approval_ref"           // the single-use approval this operation consumes (unique)
	colOpOperationID  = "operation_id"           // the server-minted sdk.OperationID (unique idempotency identity)
	colOpEffectDigest = "effect_digest"          // sdk.EffectDigest binding (retry vs FailureReplay guard)
	colOpSurface      = "surface"                // the acting PEP surface (e.g. schedule-fire, workflow-step)
	colOpAction       = "action"                 // the governed action (orchestration.schedule.fire, …)
	colOpPlanHash     = "plan_hash"              // the approved plan hash the operation is bound to
	colOpBindProfile  = "target_binding_profile" // versioned target-binding profile id
	colOpTargetFp     = "target_fingerprint"     // the approved target fingerprint (opaque HMAC)
	colOpEvidenceRef  = "evidence_ref"           // the ledger anchor of the claim (sdk EvidenceReceipt.EvidenceRef)
	colOpState        = "state"                  // internal lifecycle (NOT an EvidenceFault): claimed|dispatched|declared|failed|unknown
	colOpDispatchRef  = "dispatch_ref"           // the settled dispatcher ref
	colOpOutcome      = "outcome"                // short, non-sensitive terminal summary
	colOpScheduleRef  = "schedule_ref"           // the fired schedule (correlation, direct fire)
)

// outbox columns — the durable dispatch intent for one operation. MUTABLE:
// ready → dispatch_started (CAS) → dispatched|failed|unknown.
const (
	colObOperationID  = "operation_id"        // the operation this outbox drains (unique)
	colObEffectDigest = "effect_digest"       // the binding digest (receipt reconstruction)
	colObTargetFp     = "target_fingerprint"  // the approved target fingerprint
	colObState        = "state"               // ready|dispatch_started|dispatched|failed|unknown
	colObStartedAt    = "dispatch_started_at" // when the CAS claimed the dispatch (crash window marker)
	colObDispatchRef  = "dispatch_ref"        // the settled dispatcher ref
	colObOutcome      = "outcome"             // short, non-sensitive terminal summary
)

// run_target_binding columns — the immutable approved-target fingerprint per
// run+step (D-06). NEVER an executable copy of a URL/command/header/secret —
// only an opaque HMAC and non-sensitive labels.
const (
	colRtbRunRef      = "run_ref"
	colRtbStepRef     = "step_ref"
	colRtbProfile     = "binding_profile"    // versioned binding profile id
	colRtbMacKeyID    = "mac_key_id"         // the target-binding HMAC key id (custody, rotation)
	colRtbFingerprint = "target_fingerprint" // the approved opaque HMAC of the effect-bearing target
	colRtbGeneration  = "config_generation"  // the dispatcher config generation the approval saw
)

// pdeclLeaves classifies every listed leaf path of a Nested type with decl.
func pdeclLeaves(decl *model.ColumnDecl, paths ...string) []model.LeafDecl {
	out := make([]model.LeafDecl, 0, len(paths))
	for _, p := range paths {
		out = append(out, model.Leaf(p, decl))
	}
	return out
}

// What the columns of this module say about principals. A None reason cites the
// reader or writer lines that show its value names no account.
var (
	// pdeclActorEvidence is the caller who made a change or a decision, as its
	// actor ref "user:<id>" or "token:<id>", or the system actor of a detected
	// cadence miss (revisions.go:79, schedules.go:300, workflow.go:139).
	pdeclActorEvidence = model.Ref(model.EncodeUserRef, model.ClassEvidence)
	// pdeclObservedOrigin is the origin of an observed edge: a connector's natural
	// reference for an agent, a session or an identity
	// (sdk/model/observation.go:55-59, ingest.go:96-114). Graph history only.
	pdeclObservedOrigin = model.Scan(model.ClassEvidence)
	// pdeclScheduleOwnerActor and pdeclScheduleOwnerUser are a schedule's owner,
	// as its actor ref and as its bare account id. The writer fences what they
	// name and the retirement step reads them (fence.go, retirement.go).
	pdeclScheduleOwnerActor = model.Ref(model.EncodeUserRef, model.ClassObligation)
	pdeclScheduleOwnerUser  = model.Ref(model.EncodeUserID, model.ClassObligation)

	pdeclNoneActorKind      = model.None("the caller's actor kind, recorded beside its actor ref and rendered only: revisions.go:80, dto.go:284, workflow.go:321")
	pdeclNoneName           = model.None("a schedule or workflow name, rendered, searched and hashed into a plan only: dto.go:236, search.go:40, workflow.go:46-55")
	pdeclNoneScheduleEnum   = model.None("a closed subject kind, trigger kind or desired status the schedule validator checks: schedules.go:67-69, schedules.go:383")
	pdeclNoneSubjectRef     = model.None("the agent, swarm or workflow a schedule or decision governs; subject kinds are agent, swarm or workflow only: schedules.go:67, schedules.go:383, workflow_run.go:1680")
	pdeclNoneCadence        = model.None("a cron expression or event-type reference, hashed into the fire plan, checked against the cron allowlist and bound into the target: schedules.go:128-131, schedules.go:1090-1091, targetbind.go:94-95")
	pdeclNoneWorkspaceRef   = model.None("the declaring caller's confined core workspace id, a routine-policy scope key: routinepolicy.go:149-150, routinepolicy.go:550")
	pdeclNoneRevisionOf     = model.None("the id of the schedule or workflow a revision belongs to, compared before a restore: revisions.go:269, workflow.go:651")
	pdeclNoneRevisionOp     = model.None("create, update or restore: revisions.go:42-44, revisions.go:328")
	pdeclNoneObservedTarget = model.None("the observed worker, tool or MCP endpoint and the edge's kind, mode, signal and confidence labels: ingest.go:65-87, ingest.go:145-147, dto.go:51-65")
	pdeclNoneApprovalClaim  = model.None("a spent approval id, the schedule it activated or the plan hash it was bound to: routinepolicy.go:684-685")
	pdeclNoneFenceKey       = model.None("the constant fence family key: routinepolicy.go:492-499")
	pdeclNoneDecision       = model.None("an op, schedule id, plan hash, approval id, gate or op status, dispatch ref, detail digest or short result of the decision ledger, rendered only: schedules.go:158-172, dto.go:271-288")
	pdeclNoneOperation      = model.None("a ref, digest, label or state of the module's own governed-effect claim and outbox, compared for replay only: operation.go:189-196, operation.go:207-210, operation.go:276-282, operation.go:297-303")
	pdeclNoneTargetBinding  = model.None("the run, step, profile, key id, fingerprint or generation of an approved target binding: workflow_run.go:393-397")
	pdeclNoneRunState       = model.None("a workflow or work-item id, status, plan hash, approval id or pause reason of a run: workflow_run.go:834-836, workflow_run.go:1668-1670, workflow_run.go:1721")
	pdeclNoneRunCaller      = model.None("the initiator's actor kind, agent identity, session identity or runtime generation, replayed into the step actor beside its account id: workflow_run.go:837-842, workflow_work_run.go:18-28")
	pdeclNoneWorkflowText   = model.None("operator prose, bounded and rendered only: workflow.go:51, workflow.go:323")
	pdeclNoneSnapshotField  = model.None("a field of the schedule as it stood; restore re-validates and re-applies only its status, subject, cadence, interval and grace: revisions.go:274-292, dto.go:229-251")

	// pdeclScheduleSnapshot is a schedule revision's post-state snapshot. Its
	// owner_actor is the declaring caller's actor ref, which a restore never
	// re-applies (revisions.go:284-292).
	pdeclScheduleSnapshot = model.Nested(scheduleDTO{}, model.ClassEvidence, append(
		pdeclLeaves(pdeclNoneSnapshotField, "id", "name", "subject_kind", "subject_ref", "trigger_kind",
			"cadence_spec", "desired_status", "last_fired_at", "last_observed_at", "missed_at", "health", "created_at"),
		model.Leaf("owner_actor", model.Ref(model.EncodeUserRef, "")))...)
)

// RegisterSchema declares the module's three owned entities. The engine creates
// the tables, injects the base columns and attaches the tenant/append-only guards
// (S02 §7 /); a module cannot opt out of isolation. Every UNIQUE index
// leads model.ColTenantID so it can neither couple tenants nor leak existence.
//
// Minimal data (docs/SECURITY-HARDENING.md): no column can hold a message payload, prompt, tool
// argument or secret. The decision ledger is APPEND-ONLY so the fire/miss evidence
// cannot be silently rewritten (docs/SECURITY-HARDENING.md). None is descriptor-Audited: the
// privileged mutations each append a SEMANTIC self-audit attributed to the real
// principal in their own transaction (helpers.go auditEvent); the high-frequency
// relation upserts are automated ingestion gated by RBAC at read time.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	// the append-only schedule revision ledger (change history + restore).
	if err := reg.Register(schedRevisionDescriptor()); err != nil {
		return err
	}
	// the DAG-workflow entities (workflow + revision ledger + run state).
	if err := registerWorkflowSchema(reg); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:  relationKind,
		Table: relationTable,
		Fields: []model.FieldSpec{
			{Name: colSupervisorRef, Kind: model.KindText, Indexed: true, Principal: pdeclObservedOrigin},
			{Name: colWorkerRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneObservedTarget},
			{Name: colLinkKind, Kind: model.KindText, Principal: pdeclNoneObservedTarget},
			// tool_ref is NOT nullable and defaults to "" so it is a stable part of
			// the unique key (a NULL would make every link distinct and break the
			// idempotent upsert dedup).
			{Name: colToolRef, Kind: model.KindText, Principal: pdeclNoneObservedTarget},
			{Name: colMode, Kind: model.KindText, Principal: pdeclNoneObservedTarget},
			{Name: colSignalSource, Kind: model.KindText, Principal: pdeclNoneObservedTarget},
			{Name: colConfidence, Kind: model.KindText, Principal: pdeclNoneObservedTarget},
			{Name: colDelegationCnt, Kind: model.KindInt},
			{Name: colFirstSeenAt, Kind: model.KindTimestamp},
			{Name: colLastSeenAt, Kind: model.KindTimestamp, Indexed: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "orchestration_relation_uniq",
			Columns: []string{model.ColTenantID, colSupervisorRef, colWorkerRef, colLinkKind, colToolRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  scheduleKind,
		Table: scheduleTable,
		Fields: []model.FieldSpec{
			{Name: colSchedName, Kind: model.KindText, Indexed: true, Principal: pdeclNoneName},
			{Name: colSubjectKind, Kind: model.KindText, Principal: pdeclNoneScheduleEnum},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectRef},
			{Name: colTriggerKind, Kind: model.KindText, Principal: pdeclNoneScheduleEnum},
			{Name: colCadenceSpec, Kind: model.KindText, Nullable: true, Principal: pdeclNoneCadence},
			{Name: colExpectedIvl, Kind: model.KindInt},
			{Name: colGraceFactor, Kind: model.KindInt},
			{Name: colDesiredStat, Kind: model.KindText, Indexed: true, Principal: pdeclNoneScheduleEnum},
			// The declaring caller, "user:<id>" or "token:<id>": the accountable
			// party of every autonomous fire, and the owner a routine policy is
			// resolved for when owner_user_ref is absent (routinepolicy.go:160-165).
			{Name: colOwnerActor, Kind: model.KindText, Principal: pdeclScheduleOwnerActor},
			{Name: colOwnerActorK, Kind: model.KindText, Principal: pdeclNoneActorKind},
			{Name: colLastFiredAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colMissedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colFireReservedAt, Kind: model.KindTimestamp, Nullable: true},
			// Nullable so an existing deployment reconciles additively
			// (reconcileColumns refuses a new NON-null column). A pre row
			// has neither, so the enforcement path recovers the owner from
			// owner_actor ("user:<id>") and resolves an absent workspace to the
			// tenant's DEFAULT workspace — the engine's own rule for an entity
			// with an unset WorkspaceID. Without that recovery a single
			// user- or workspace-scoped policy would refuse every patch,
			// restore and fire of every routine that predates this session.
			//
			// owner_user_ref is the declaring caller's bare account id, the user
			// axis every later patch, restore and fire resolves the routine policy
			// for (routinepolicy.go:147-154).
			{Name: colOwnerUserRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclScheduleOwnerUser},
			{Name: colWorkspaceRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneWorkspaceRef},
		},
		Indexes: []model.IndexSpec{{
			Name:    "orchestration_schedule_uniq",
			Columns: []string{model.ColTenantID, colSchedName},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// Review M1: the single-use activation-approval claim.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  activationClaimKind,
		Table: activationClaimTable,
		Fields: []model.FieldSpec{
			{Name: colAcApprovalRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneApprovalClaim},
			{Name: colAcScheduleRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneApprovalClaim},
			{Name: colAcPlanHash, Kind: model.KindText, Principal: pdeclNoneApprovalClaim},
		},
		Indexes: []model.IndexSpec{{
			Name:    "orchestration_activation_claim_uniq",
			Columns: []string{model.ColTenantID, colAcApprovalRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the routine admission fence (one row per tenant).
	if err := reg.Register(model.EntityDescriptor{
		Kind:  admissionFenceKind,
		Table: admissionFenceTable,
		Fields: []model.FieldSpec{
			{Name: colFenceKey, Kind: model.KindText, Indexed: true, Principal: pdeclNoneFenceKey},
		},
		Indexes: []model.IndexSpec{{
			Name:    "orchestration_admission_fence_uniq",
			Columns: []string{model.ColTenantID, colFenceKey},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       decisionKind,
		Table:      decisionTable,
		AppendOnly: true, // immutable fire/miss governance evidence (docs/SECURITY-HARDENING.md)
		Fields: []model.FieldSpec{
			{Name: colDecSubjectKind, Kind: model.KindText, Principal: pdeclNoneSubjectRef},
			{Name: colDecSubjectRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectRef},
			{Name: colScheduleRef, Kind: model.KindUUID, Nullable: true, Indexed: true, Principal: pdeclNoneDecision},
			{Name: colOp, Kind: model.KindText, Indexed: true, Principal: pdeclNoneDecision},
			{Name: colPlanHash, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneDecision},
			{Name: colApprovalRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDecision},
			{Name: colGateStatus, Kind: model.KindText, Principal: pdeclNoneDecision},
			{Name: colOpStatus, Kind: model.KindText, Indexed: true, Principal: pdeclNoneDecision},
			{Name: colDispatchRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDecision},
			{Name: colActor, Kind: model.KindText, Principal: pdeclActorEvidence},
			{Name: colActorKind, Kind: model.KindText, Principal: pdeclNoneActorKind},
			{Name: colDetailHash, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDecision},
			{Name: colResult, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDecision},
			{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
		},
	}); err != nil {
		return err
	}

	// the durable operation identity. UNIQUE(tenant, operation_id) is the
	// idempotency identity; UNIQUE(tenant, approval_ref) makes a direct-fire
	// reservation atomic so two concurrent phase-2 fires cannot both win and a
	// re-POST of a spent approval finds the row instead of re-dispatching.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  operationKind,
		Table: operationTable,
		Fields: []model.FieldSpec{
			{Name: colOpApprovalRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneOperation},
			{Name: colOpOperationID, Kind: model.KindText, Indexed: true, Principal: pdeclNoneOperation},
			{Name: colOpEffectDigest, Kind: model.KindText, Principal: pdeclNoneOperation},
			{Name: colOpSurface, Kind: model.KindText, Principal: pdeclNoneOperation},
			{Name: colOpAction, Kind: model.KindText, Principal: pdeclNoneOperation},
			{Name: colOpPlanHash, Kind: model.KindText, Principal: pdeclNoneOperation},
			{Name: colOpBindProfile, Kind: model.KindText, Principal: pdeclNoneOperation},
			{Name: colOpTargetFp, Kind: model.KindText, Principal: pdeclNoneOperation},
			{Name: colOpEvidenceRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneOperation},
			{Name: colOpState, Kind: model.KindText, Indexed: true, Principal: pdeclNoneOperation},
			{Name: colOpDispatchRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneOperation},
			{Name: colOpOutcome, Kind: model.KindText, Nullable: true, Principal: pdeclNoneOperation},
			{Name: colOpScheduleRef, Kind: model.KindUUID, Nullable: true, Indexed: true, Principal: pdeclNoneOperation},
		},
		Indexes: []model.IndexSpec{
			{Name: "orchestration_operation_id_uniq", Columns: []string{model.ColTenantID, colOpOperationID}, Unique: true},
			{Name: "orchestration_operation_appr_uniq", Columns: []string{model.ColTenantID, colOpApprovalRef}, Unique: true},
		},
	}); err != nil {
		return err
	}

	// the durable dispatch intent. UNIQUE(tenant, operation_id) so an
	// operation has at most one outbox row; the CAS ready→dispatch_started makes
	// a duplicate drain a no-op.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  outboxKind,
		Table: outboxTable,
		Fields: []model.FieldSpec{
			{Name: colObOperationID, Kind: model.KindText, Indexed: true, Principal: pdeclNoneOperation},
			{Name: colObEffectDigest, Kind: model.KindText, Principal: pdeclNoneOperation},
			{Name: colObTargetFp, Kind: model.KindText, Principal: pdeclNoneOperation},
			{Name: colObState, Kind: model.KindText, Indexed: true, Principal: pdeclNoneOperation},
			{Name: colObStartedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colObDispatchRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneOperation},
			{Name: colObOutcome, Kind: model.KindText, Nullable: true, Principal: pdeclNoneOperation},
		},
		Indexes: []model.IndexSpec{{
			Name: "orchestration_outbox_op_uniq", Columns: []string{model.ColTenantID, colObOperationID}, Unique: true,
		}},
	}); err != nil {
		return err
	}

	// D-06: the immutable approved-target binding per run+step.
	return reg.Register(model.EntityDescriptor{
		Kind:  runTargetBindingKind,
		Table: runTargetBindingTable,
		Fields: []model.FieldSpec{
			{Name: colRtbRunRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneTargetBinding},
			{Name: colRtbStepRef, Kind: model.KindText, Principal: pdeclNoneTargetBinding},
			{Name: colRtbProfile, Kind: model.KindText, Principal: pdeclNoneTargetBinding},
			{Name: colRtbMacKeyID, Kind: model.KindText, Principal: pdeclNoneTargetBinding},
			{Name: colRtbFingerprint, Kind: model.KindText, Principal: pdeclNoneTargetBinding},
			{Name: colRtbGeneration, Kind: model.KindText, Principal: pdeclNoneTargetBinding},
		},
		Indexes: []model.IndexSpec{{
			Name: "orchestration_rtb_uniq", Columns: []string{model.ColTenantID, colRtbRunRef, colRtbStepRef}, Unique: true,
		}},
	})

	// FOLLOW-UP (D-05, deferred — see sessions/ report): the
	// (tenant, approval_ref) UNIQUE index on the EXISTING orchestration_workflow_run
	// table plus its duplicate-quarantine preflight migration. It is boot-brick-
	// risky on a populated table and NOT exercisable in the fresh in-memory
	// harness (no pre-existing duplicates), so it is documented for the upgrade/
	// enterprise migration harness rather than shipped unverified here. The
	// direct-fire single-use defect the RED tests exercise is already closed by
	// the orchestration_operation UNIQUE(tenant, approval_ref) claim above.
}
