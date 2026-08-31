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
			{Name: colSupervisorRef, Kind: model.KindText, Indexed: true},
			{Name: colWorkerRef, Kind: model.KindText, Indexed: true},
			{Name: colLinkKind, Kind: model.KindText},
			// tool_ref is NOT nullable and defaults to "" so it is a stable part of
			// the unique key (a NULL would make every link distinct and break the
			// idempotent upsert dedup).
			{Name: colToolRef, Kind: model.KindText},
			{Name: colMode, Kind: model.KindText},
			{Name: colSignalSource, Kind: model.KindText},
			{Name: colConfidence, Kind: model.KindText},
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
			{Name: colSchedName, Kind: model.KindText, Indexed: true},
			{Name: colSubjectKind, Kind: model.KindText},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colTriggerKind, Kind: model.KindText},
			{Name: colCadenceSpec, Kind: model.KindText, Nullable: true},
			{Name: colExpectedIvl, Kind: model.KindInt},
			{Name: colGraceFactor, Kind: model.KindInt},
			{Name: colDesiredStat, Kind: model.KindText, Indexed: true},
			{Name: colOwnerActor, Kind: model.KindText},
			{Name: colOwnerActorK, Kind: model.KindText},
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
			{Name: colOwnerUserRef, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colWorkspaceRef, Kind: model.KindText, Nullable: true, Indexed: true},
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
			{Name: colAcApprovalRef, Kind: model.KindText, Indexed: true},
			{Name: colAcScheduleRef, Kind: model.KindText, Nullable: true},
			{Name: colAcPlanHash, Kind: model.KindText},
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
			{Name: colFenceKey, Kind: model.KindText, Indexed: true},
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
			{Name: colDecSubjectKind, Kind: model.KindText},
			{Name: colDecSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colScheduleRef, Kind: model.KindUUID, Nullable: true, Indexed: true},
			{Name: colOp, Kind: model.KindText, Indexed: true},
			{Name: colPlanHash, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colApprovalRef, Kind: model.KindText, Nullable: true},
			{Name: colGateStatus, Kind: model.KindText},
			{Name: colOpStatus, Kind: model.KindText, Indexed: true},
			{Name: colDispatchRef, Kind: model.KindText, Nullable: true},
			{Name: colActor, Kind: model.KindText},
			{Name: colActorKind, Kind: model.KindText},
			{Name: colDetailHash, Kind: model.KindText, Nullable: true},
			{Name: colResult, Kind: model.KindText, Nullable: true},
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
			{Name: colOpApprovalRef, Kind: model.KindText, Indexed: true},
			{Name: colOpOperationID, Kind: model.KindText, Indexed: true},
			{Name: colOpEffectDigest, Kind: model.KindText},
			{Name: colOpSurface, Kind: model.KindText},
			{Name: colOpAction, Kind: model.KindText},
			{Name: colOpPlanHash, Kind: model.KindText},
			{Name: colOpBindProfile, Kind: model.KindText},
			{Name: colOpTargetFp, Kind: model.KindText},
			{Name: colOpEvidenceRef, Kind: model.KindText, Nullable: true},
			{Name: colOpState, Kind: model.KindText, Indexed: true},
			{Name: colOpDispatchRef, Kind: model.KindText, Nullable: true},
			{Name: colOpOutcome, Kind: model.KindText, Nullable: true},
			{Name: colOpScheduleRef, Kind: model.KindUUID, Nullable: true, Indexed: true},
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
			{Name: colObOperationID, Kind: model.KindText, Indexed: true},
			{Name: colObEffectDigest, Kind: model.KindText},
			{Name: colObTargetFp, Kind: model.KindText},
			{Name: colObState, Kind: model.KindText, Indexed: true},
			{Name: colObStartedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colObDispatchRef, Kind: model.KindText, Nullable: true},
			{Name: colObOutcome, Kind: model.KindText, Nullable: true},
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
			{Name: colRtbRunRef, Kind: model.KindUUID, Indexed: true},
			{Name: colRtbStepRef, Kind: model.KindText},
			{Name: colRtbProfile, Kind: model.KindText},
			{Name: colRtbMacKeyID, Kind: model.KindText},
			{Name: colRtbFingerprint, Kind: model.KindText},
			{Name: colRtbGeneration, Kind: model.KindText},
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
