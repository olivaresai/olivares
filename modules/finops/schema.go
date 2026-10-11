// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The module's owned entities. Budgets reuse the core Policy entity (Kind="budget"
// — ARCHITECTURE.md names budget as a Policy kind), so the only things core does not
// model are the cost read-model (dedup + by-name analytics substrate) and the
// budget-alert history, registered here.
const (
	costSampleKind  model.Kind = "finops.cost_sample"
	budgetAlertKind model.Kind = "finops.budget_alert"
	// spendLimitAuditKind is the administrator mutation trail for apps-gateway
	// spend limits. The limit itself remains a core Policy (Kind="spend_limit").
	spendLimitAuditKind model.Kind = "finops.spend_limit_audit"
	// seatCountKind is the per-provider/day seat denominator for the
	// per-seat utilization view: assigned (and premium) seat counts an operator/
	// automation posts (the Enterprise Analytics summary carries them; the
	// connector cannot reach module HTTP — license boundary — so this ingest is
	// the sanctioned bridge).
	seatCountKind model.Kind = "finops.seat_count"
	// outcomeKind is the value-attribution substrate: a graded outcome of a
	// task/session/agent (a CMA outcome verdict, an operator-declared business
	// result, …), posted via the SAME operator/automation bridge as seats (the
	// producing connector cannot reach module HTTP, and the $-value of an outcome is
	// a business input the plane must never fabricate). Joined to the cost stream by
	// the resolved subject ref it yields cost-per-outcome and the cancellation-risk
	// signal (burn without successful outcomes).
	outcomeKind model.Kind = "finops.outcome"
	// costCenterKind is the first-class cost center entity: an accounting
	// code used by finance to attribute AI spend to internal business units. Flat
	// list (no hierarchy). Each cost center has an admin-assigned code, name, owner
	// and status (active/archived).
	costCenterKind model.Kind = "finops.cost_center"
	// costCenterMappingKind maps attribution dimensions (team, workspace,
	// project, agent, provider, identity) to cost centers. Resolution at ingestion
	// time stamps cost_center_ref on each cost_sample row — the same denormalized
	// pattern as team/project/workspace_ref.
	costCenterMappingKind model.Kind = "finops.cost_center_mapping"
	// modelRateKind is the admin-configured price sheet: per-model token
	// rates (input/output/cache-read/cache-creation) in micro-USD per 1M tokens.
	// Used by the model cost comparison (retrospective re-pricing + prospective
	// projection) and chargeback statement generation.
	modelRateKind model.Kind = "finops.model_rate"
	// chargebackStatementKind is the periodic chargeback snapshot: a
	// per-cost-center, per-period (monthly/weekly) statement with line items by
	// model/provider/agent — the artifact finance consumes for internal billing.
	chargebackStatementKind model.Kind = "finops.chargeback_statement"
	// statementLineKind is a line item within a chargeback statement:
	// one row per (model, provider, agent) combination with aggregated token
	// counts and cost.
	statementLineKind model.Kind = "finops.statement_line"
	// budgetReservationKind is the DYNAMIC per-request reserve-ledger that
	// closes the TOCTOU race between the pre-flight check (CheckBudget /
	// CheckSpendLimit) and the moment the actual spend is recorded. A pre-flight
	// RESERVES its estimated cost against a policy's remaining headroom; the ceiling
	// is evaluated on spend + static ReservedMicroUSD + SUM(active, unexpired
	// reservations), so N concurrent requests under the limit can no longer all pass
	// and collectively exceed it. Distinct from the STATIC budgetSpec.ReservedMicroUSD
	// (a Priority-Tier capacity commitment): this is a short-lived, per-request row
	// committed/released as the actuation completes. Atomicity is cross-store (SQLite
	// single-writer + Postgres READ COMMITTED) via a monotonic per-(policy, period)
	// seq under a UNIQUE index — concurrent reservers collide on the seq and one
	// retries, so the reserve→check→insert is serialized WITHOUT a process lock.
	budgetReservationKind model.Kind = "finops.budget_reservation"
	// admissionIdempotencyKind is the admission row: one per (tenant, idempotency
	// key). From the claim until the money of a request is settled, the row names the
	// hold identity every ledger row of that money carries. It is not a second money
	// ledger: the reservation rows remain the authority for headroom.
	admissionIdempotencyKind model.Kind = "finops.admission_idempotency"
)

const (
	costSampleTable           = "finops_cost_sample"
	budgetAlertTable          = "finops_budget_alert"
	spendLimitAuditTable      = "finops_spend_limit_audit"
	seatCountTable            = "finops_seat_count"
	outcomeTable              = "finops_outcome"
	costCenterTable           = "finops_cost_center"
	costCenterMappingTable    = "finops_cost_center_mapping"
	modelRateTable            = "finops_model_rate"
	chargebackStatementTable  = "finops_chargeback_statement"
	statementLineTable        = "finops_statement_line"
	budgetReservationTable    = "finops_budget_reservation"
	admissionIdempotencyTable = "finops_admission_idempotency"
)

// Provenance values stored in colProvenance (mirroring sdk/model.CostProvenance).
const (
	provenanceEstimated = "estimated"
	provenanceBilled    = "billed"
)

// finops.spend_limit_audit columns. Before/after are canonical wire objects
// encoded as JSON so the audit view remains an exact mutation snapshot.
const (
	colSpendAuditActor   = "admin_actor"
	colSpendAuditAction  = "action"
	colSpendAuditLimitID = "spend_limit_id"
	colSpendAuditBefore  = "before_state"
	colSpendAuditAfter   = "after_state"
)

// finops.cost_sample columns — the denormalized, by-name FinOps read-model and
// the ingestion dedup guard.
const (
	colSampleKey    = "sample_key"
	colProviderRef  = "provider_ref"
	colModelRef     = "model_ref"
	colAgentRef     = "agent_ref"
	colSessionRef   = "session_ref"
	colTeam         = "team"
	colProject      = "project"
	colInputTokens  = "input_tokens"
	colOutputTokens = "output_tokens"
	colCostMicroUSD = "cost_micro_usd"
	colOccurredAt   = "occurred_at"

	// Provenance discriminator: "estimated" (derived from list pricing) or
	// "billed" (provider cost API). Default aggregations exclude billed rows so the
	// two streams never double-count; reconciliation reads billed explicitly.
	colProvenance = "provenance"
	// Attribution dimensions — the dimensions finance allocates on.
	colWorkspaceRef  = "workspace_ref"
	colAPIKeyRef     = "api_key_ref"
	colActor         = "actor"
	colServiceTier   = "service_tier"
	colContextWindow = "context_window"
	colInferenceGeo  = "inference_geo"
	colGateway       = "gateway"
	colCostType      = "cost_type"
	// Firm identity — the resolved roster Identity (core model.Identity, module
	// VI) the spend is attributed to, distinct from the free-text actor/workspace/
	// api_key refs. identity_ref is the roster Identity.ExternalID (a SPIFFE id, an
	// Anthropic svac_/apikey_, a Vault entity, …) — the FIRM key a per-identity dollar
	// budget scopes on; identity_kind/source carry its classification + provenance so a
	// panel can tell a SPIFFE workload from an Anthropic service account. Empty = the
	// sample did not resolve to a roster identity (honest, never fabricated).
	colIdentityRef    = "identity_ref"
	colIdentityKind   = "identity_kind"
	colIdentitySource = "identity_source"
	// Cache breakdown — the dominant Claude cost lever, now measurable.
	colCacheReadTokens       = "cache_read_tokens"
	colCacheCreation1hTokens = "cache_creation_1h_tokens"
	colCacheCreation5mTokens = "cache_creation_5m_tokens"
	// colCostRecordID links the read-model row to its canonical CostRecord ledger
	// entry, so a re-pulled bucket (whose value grew/re-settled) updates BOTH the
	// read-model and the ledger instead of inserting a duplicate (the dedup key
	// is the natural key, not a content hash, so a changed re-delivery is an upsert).
	colCostRecordID = "cost_record_id"
	// colCostCenterRef is the resolved cost center code, denormalized at
	// ingestion time from the cost_center_mapping rules. Nullable: NULL = no mapping
	// matched (unmapped traffic — a useful finding in itself). Indexed: a per-CC
	// budget aggregates on cost_center_ref, and chargeback statements filter on it.
	colCostCenterRef = "cost_center_ref"
)

// finops.cost_center columns — the accounting code entity.
const (
	colCCCode        = "code"
	colCCName        = "cc_name"
	colCCDescription = "description"
	colCCOwner       = "owner"
	colCCStatus      = "status"
	colCCMetadata    = "metadata"
)

// finops.cost_center_mapping columns — dimension-to-CC binding rules.
const (
	colCCMappingCostCenterID = "cost_center_id"
	colCCMappingDimension    = "source_dimension"
	colCCMappingKey          = "source_key"
	colCCMappingPriority     = "priority"
)

// validMappingDimensions are the attribution dimensions a cost center mapping
// rule can bind on. These are the dimensions whose value at ingestion time is
// used to resolve the cost center.
var validMappingDimensions = map[string]bool{
	"team": true, "workspace": true, "project": true,
	"agent": true, "provider": true, "identity": true,
}

// finops.model_rate columns — admin-configured per-model token rates.
const (
	colRateProvider              = "rate_provider"
	colRateModel                 = "rate_model"
	colRateInputMicroUSD         = "input_rate_micro_usd"
	colRateOutputMicroUSD        = "output_rate_micro_usd"
	colRateCacheReadMicroUSD     = "cache_read_rate_micro_usd"
	colRateCacheCreationMicroUSD = "cache_creation_rate_micro_usd"
	colRateEffectiveFrom         = "effective_from"
	colRateEffectiveUntil        = "effective_until"
	colRateNotes                 = "notes"
)

// finops.chargeback_statement columns — periodic statement snapshots.
const (
	colStmtKey            = "statement_key"
	colStmtCostCenterID   = "cost_center_id"
	colStmtCostCenterCode = "cost_center_code"
	colStmtCostCenterName = "cost_center_name"
	colStmtPeriod         = "stmt_period"
	colStmtPeriodStart    = "period_start"
	colStmtPeriodEnd      = "period_end"
	colStmtTotalMicroUSD  = "total_micro_usd"
	colStmtLineCount      = "line_count"
	colStmtPriorTotal     = "prior_period_total_micro_usd"
	colStmtDeltaPct       = "delta_pct"
	colStmtStatus         = "stmt_status"
	colStmtGeneratedAt    = "generated_at"
)

// finops.statement_line columns — line items within a chargeback statement.
const (
	colLineStatementID  = "statement_id"
	colLineModelRef     = "line_model_ref"
	colLineProviderRef  = "line_provider_ref"
	colLineAgentRef     = "line_agent_ref"
	colLineInputTokens  = "line_input_tokens"
	colLineOutputTokens = "line_output_tokens"
	colLineCostMicroUSD = "line_cost_micro_usd"
	colLineSampleCount  = "line_sample_count"
)

// finops.seat_count columns — the seat denominators per provider/day.
// Premium seats = the Claude-Code-enabled tier on Claude Enterprise; 0 = not
// reported (never inferred from assigned).
const (
	colSeatProvider   = "provider"
	colSeatDay        = "day" // UTC day, YYYY-MM-DD
	colAssignedSeats  = "assigned_seats"
	colPremiumSeats   = "premium_seats"
	colPendingInvites = "pending_invites"
)

// finops.outcome columns — the value-attribution read-model. subject_kind ∈
// {session, agent, identity}; verdict is the grader vocabulary (satisfied|failed|
// max_iterations_reached|interrupted|…). value_micro_usd is the OPERATOR-supplied
// business value (0 = not reported — never inferred). agent_ref/identity_ref/
// session_ref are the resolved join keys (stamped at ingest), so the cost↔outcome
// join is a column match, not a per-query re-resolution.
const (
	colOutcomeKey         = "outcome_key" // unique dedup key
	colOutcomeSubjectKind = "subject_kind"
	colOutcomeSubjectRef  = "subject_ref"
	colOutcomeRef         = "outcome_ref" // the outcome/task id (e.g. outc_…); optional
	colOutcomeVerdict     = "verdict"
	colOutcomeValue       = "value_micro_usd"
	colOutcomeSource      = "source" // cma|operator|eval|…
)

// finops.budget_alert columns — the alert history and per-period crossing dedup.
const (
	colBudgetID     = "budget_id"
	colPeriod       = "period"
	colPeriodStart  = "period_start"
	colThresholdPct = "threshold_pct"
	colDimension    = "dimension"
	colDimKey       = "dim_key"
	colAlertSpend   = "spend_micro_usd"
	colAlertLimit   = "limit_micro_usd"
	colSeverity     = "severity"
	colTriggeredAt  = "triggered_at"
	// A4.2 durable financial evidence of the crossing. Both are NULLABLE and have no
	// default: a historical row keeps NULL and is read as legacy_unversioned, never
	// back-filled into a certainty it never had.
	colAlertEvidence     = "amount_evidence"
	colAlertEvidenceHash = "evidence_hash"
)

// finops.budget_reservation columns — the dynamic per-request reserve
// ledger. policy_ref is the governing Policy id (a budget OR a spend_limit — the
// mechanism is shared). period_start buckets the reservation to the policy's
// period. seq is the monotonic per-(policy, period) serialization key under the
// UNIQUE index. amount is the reserved estimate; actual is stamped at commit.
// state ∈ {active, committed, released, expired}; only active + unexpired rows
// count toward the ceiling, so expiry (expires_at) drops a reservation from the
// sum WITHOUT any decrement bookkeeping — there is no counter to double-count.
const (
	colResvPolicyRef  = "policy_ref"
	colResvPolicyKind = "policy_kind" // "budget" | "spend_limit" (observability)
	colResvDimension  = "dimension"
	// colResvScopeKey is the per-scope discriminator so ONE policy can cap many
	// subjects independently: for a budget it is the budget's key (dimension value,
	// "" for global — one scope per policy); for a per-seat spend limit it is the
	// ACTOR, so an org/group cap reserves per-actor headroom rather than a single
	// shared pool. Stored as "" (never NULL) so it is a real component of the seq
	// UNIQUE index (NULLs would compare distinct and defeat the serialization).
	colResvScopeKey    = "dim_key"
	colResvPeriod      = "period"
	colResvPeriodStart = "period_start"
	colResvSeq         = "seq"
	colResvAmount      = "amount_micro_usd"
	colResvActual      = "actual_micro_usd"
	colResvState       = "state"
	colResvHandle      = "handle" // groups the rows of one Reserve* call
	colResvExpiresAt   = "expires_at"
	colResvSettledAt   = "settled_at" // when it left the active state
)

// finops.admission_idempotency columns. One row per (tenant, key). The first eight
// are the row as an earlier build wrote it and keep their meaning; owed_handles is
// the one column this build adds.
const (
	colAdmKey         = "idempotency_key"
	colAdmPayloadHash = "payload_hash"
	colAdmHandle      = "handle"
	colAdmSpendHandle = "spend_handle"
	colAdmScope       = "scope"
	colAdmEstimate    = "estimate_micro_usd"
	colAdmState       = "state"
	// colAdmStateAt is when the row entered the state it is in, by the MODULE's
	// clock: it bounds a replay and a claim, and the store's updated_at is the
	// store's own observation, never the injected application clock. Nullable: a row
	// written before the column existed has none, and is undated.
	colAdmStateAt = "state_at"
	// colAdmOwedHandles lists the hold identities of superseded generations whose
	// money this row still owes back: NULL, or a JSON array of one to sixteen
	// distinct identities. Nullable, so the reconciler adds it to a populated table
	// in place and every existing row reads as owing nothing.
	colAdmOwedHandles = "owed_handles"
)

// reservation lifecycle states.
const (
	resvStateActive    = "active"
	resvStateCommitted = "committed"
	resvStateReleased  = "released"
	resvStateExpired   = "expired"
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
// reader or validator lines that show its value names no account.
var (
	// pdeclSpendActor is who incurred a cost as its source reports it: an actor
	// ref, an account id or a developer email (sdk/model/observation.go:183-186).
	// It is spend history: the spend-limit and seat readers only sum and count it
	// (spendlimits.go:875, seats.go:253).
	pdeclSpendActor = model.Scan(model.ClassEvidence)
	// pdeclAPIKeyRef is a key or service-account reference, matched against the
	// identity roster's external ids when a sample is ingested (ingest.go:538-549).
	pdeclAPIKeyRef = model.Scan(model.ClassEvidence)
	// pdeclIdentityRef is the external id of the roster identity a sample or an
	// outcome resolved to (ingest.go:555-558, value.go:226-227, value.go:269).
	pdeclIdentityRef = model.Ref(model.EncodeExternalID, model.ClassEvidence)
	// pdeclBudgetKeyEvidence is a recorded budget key or scope value. Under the
	// actor or identity dimension it names who a budget scoped
	// (budgets.go:212-213, budgets.go:226-227); the row is history of a crossing.
	pdeclBudgetKeyEvidence = model.Scan(model.ClassEvidence)
	// pdeclBudgetKeyRestrict is a live reservation's scope key: a budget key, or
	// the actor of a per-seat spend limit. Active rows only reduce the headroom
	// left under that key (reservation.go:1152-1167).
	pdeclBudgetKeyRestrict = model.Scan(model.ClassRestrict)
	// pdeclCostCenterOwner is the owner an operator typed for a cost center; it
	// is rendered only (costcenter.go:48).
	pdeclCostCenterOwner = model.Scan(model.ClassEvidence)
	// pdeclMappingKey is the dimension value a mapping rule matches. Under the
	// identity dimension it is a roster identity's external id
	// (costcenter.go:432-433, costcenter.go:459-462).
	pdeclMappingKey = model.Scan(model.ClassEvidence)
	// pdeclAdmissionKey is an admission's idempotency key. The engine's gates build
	// it from a prefix and a fresh id or a run reference
	// (cmd/olivares/budgetgate.go:87, cmd/olivares/sessiongov.go:307-318), but the
	// reserve route stores the key its caller chose (admission_api.go:64), checked
	// only for presence and length (admission.go:173-179). Readers only look a row
	// up by it (admission_row.go:161-163).
	pdeclAdmissionKey = model.Scan(model.ClassEvidence)

	pdeclNoneNaturalKey        = model.None("a SHA-256 of the row's natural key, used only to deduplicate: ingest.go:687-696, value.go:209-216")
	pdeclNoneCostLabel         = model.None("a provider, model, team, project, provenance, tier, window, region, gateway or cost-type label of a sample, grouped and filtered on only: ingest.go:645-676, analytics.go:30-62, analytics.go:73-75")
	pdeclNoneAgentRef          = model.None("an agent's external id or name, never an account: value.go:239-245, value.go:259-262")
	pdeclNoneSessionRef        = model.None("a session's external id, resolved only against sessions: value.go:229-231")
	pdeclNoneProviderWorkspace = model.None("the provider's own billing workspace, neither a core workspace nor an account: core/model/descriptor.go:143-146, sdk/model/observation.go:179-181")
	pdeclNoneIdentityLabel     = model.None("the kind and provider of the resolved roster identity, copied at ingest and rendered only: ingest.go:555-558")
	pdeclNoneCostRecordID      = model.None("the id of the core cost-ledger entry a sample mirrors: ingest.go:249, ingest.go:648")
	pdeclNoneCostCenterCode    = model.None("a cost-center accounting code, copied onto samples from an active cost center and filtered on: costcenter.go:495-498, statements.go:184")
	pdeclNoneSeat              = model.None("the provider and UTC day of a seat-count snapshot: seats.go:110, seats.go:216-227")
	pdeclNoneOutcomeLabel      = model.None("an outcome's subject kind, outcome id, verdict or source, rendered only: value.go:50-52, value.go:111, dto.go:312-324")
	pdeclNoneBudgetID          = model.None("the id of the budget policy an alert belongs to: budgets.go:542, alert_record_evidence.go:428-429")
	pdeclNoneAlertLabel        = model.None("a budget period, dimension or severity, compared with the digest-bound envelope and rendered: alert_record_evidence.go:1081-1084, dto.go:265-272")
	pdeclNoneAlertHash         = model.None("a SHA-256 of the alert evidence envelope, recomputed and compared: alert_record_evidence.go:409, alert_record_evidence.go:434-435")
	pdeclNoneAlertEnvelope     = model.None("a field of the digest-bound alert envelope, recomputed, checked and rendered only: alert_record_evidence.go:190-258, alert_record_evidence.go:407-451")
	pdeclNoneSpendAudit        = model.None("the audit action or spend-limit wire id the mutation records: spendlimits.go:293, spendlimits.go:320, spendlimits.go:370")
	pdeclNoneSpendLimitField   = model.None("a spend limit's wire id, type, timestamp, scope type, group id, amount, currency or period, built by spendLimitFromPolicy: spendlimits.go:220-238")
	pdeclNoneCostCenterText    = model.None("operator display text or tags of a cost center, rendered only: costcenter.go:42-56")
	pdeclNoneCostCenterStatus  = model.None("active or archived; attribution requires active: costcenter.go:84, costcenter.go:495")
	pdeclNoneCostCenterID      = model.None("the id of the cost center a mapping rule or statement points at: costcenter.go:475, costcenter.go:488, statements.go:70")
	pdeclNoneMappingDimension  = model.None("a closed mapping dimension: costcenter.go:116, costcenter.go:450")
	pdeclNoneRate              = model.None("a provider, model or note of the admin price sheet: ratecatalog.go:45-56, ratecatalog.go:309-310")
	pdeclNoneStatement         = model.None("a statement key, cost-center code or name, period or status, written by the generator and rendered: statements.go:67-85, statements.go:213-243")
	pdeclNoneStatementLine     = model.None("the statement id or the model, provider or agent reference a line aggregates: statements.go:87-97, statements.go:266-270")
	pdeclNoneReservation       = model.None("a policy id, policy kind, dimension, period, state or handle of a reservation, filtered on to sum held headroom: reservation.go:884-897, reservation.go:1154-1167")
	pdeclNoneAttemptRef        = model.None("an attempt reference of 32 lowercase hex characters: attempt_types.go:319-337, attempt_reconcile.go:1026-1033")
	pdeclNoneAdmissionHash     = model.None("an unkeyed SHA-256 of an admission request, compared only with a retry's: admission.go:186-211, admission_replay.go:77")
	pdeclNoneAdmissionHold     = model.None("a hold identity, or a JSON list of them, each a canonical UUID the module minted and decoded strictly: admission_hold.go:46, admission_hold.go:53-62, admission_hold.go:79-106")
	pdeclNoneAdmissionState    = model.None("a closed admission scope or row state, or the module clock's instant the row entered its state: admission.go:159-165, admission_row.go:18-30, admission_row.go:110-120")

	// pdeclSpendLimitState is a spend limit's wire object as the audit trail
	// recorded it. A user scope's user_id is "user:<id>" or "token:<id>"
	// (spendlimits.go:152-155, spendlimits.go:223-225).
	pdeclSpendLimitState = model.Nested(SpendLimit{}, model.ClassEvidence, append(
		pdeclLeaves(pdeclNoneSpendLimitField, "type", "id", "created_at", "updated_at", "scope.type",
			"scope.rbac_group_id", "amount", "currency", "period"),
		model.Leaf("scope.user_id", model.Ref(model.EncodeUserRef, "")))...)
	// pdeclAlertEvidence is the budget-alert evidence envelope. Its policy key and
	// scope value are the budget's key, which names a principal under the actor
	// or identity dimension (alert_record_evidence.go:198-199,
	// alert_record_evidence.go:232-237).
	pdeclAlertEvidence = model.Nested(alertEvidenceEnvelope{}, model.ClassEvidence, append(
		pdeclLeaves(pdeclNoneAlertEnvelope, "alert_id", "tenant_id", "budget_id",
			"policy.id", "policy.name", "policy.dimension", "policy.period", "policy.currency", "policy.action",
			"policy.limit_micro_usd", "policy.reserved_micro_usd", "policy.config_fault",
			"amount.class", "amount.value_micro_usd", "amount.currency", "amount.causes[]",
			"decision.result", "decision.threshold", "decision.target_micro_usd", "decision.target_numerator",
			"decision.target_denominator", "decision.causes[]",
			"context.window_start", "context.window_end", "context.window_bounds", "context.provenance_filter",
			"context.scope_column", "context.evaluated_at", "context.sample_occurred_at", "context.read_consistency",
			"legacy.value_kind", "legacy.note"),
		model.Leaf("policy.key", pdeclBudgetKeyEvidence),
		model.Leaf("context.scope_value", pdeclBudgetKeyEvidence),
		model.Leaf("context.scope_values[]", pdeclBudgetKeyEvidence),
		model.TypeLeaves(alertEvidenceComponent{}, pdeclLeaves(pdeclNoneAlertEnvelope, "state", "value_micro_usd", "causes[]")...))...)
)

// RegisterSchema declares the module's owned entities.
//
// The cost_sample table is deliberately NOT audited: cost ingestion is
// high-frequency automated ingestion (like inventory's catalog and the
// AccessEdge upsert), not a security-sensitive human mutation, and its reads are
// RBAC-gated at the API. The budget_alert table is likewise written only by the
// automated evaluator (no human actor in context), so it is not audited either;
// the durable, tamper-evident record of an alert is the FindingReport the module
// emits to the bus, which an output connector forwards to an external SIEM/WORM
// store (docs/SECURITY-HARDENING.md).
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  costSampleKind,
		Table: costSampleTable,
		Fields: []model.FieldSpec{
			{Name: colSampleKey, Kind: model.KindText, Principal: pdeclNoneNaturalKey},
			{Name: colProviderRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneCostLabel},
			{Name: colModelRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneCostLabel},
			{Name: colAgentRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneAgentRef},
			{Name: colSessionRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSessionRef},
			{Name: colTeam, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneCostLabel},
			{Name: colProject, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneCostLabel},
			{Name: colInputTokens, Kind: model.KindInt},
			{Name: colOutputTokens, Kind: model.KindInt},
			{Name: colCostMicroUSD, Kind: model.KindInt},
			{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
			// Additive dimensions. All nullable/zero-default so existing rows and
			// connectors that do not report a dimension stay valid.
			{Name: colProvenance, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneCostLabel},
			{Name: colWorkspaceRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneProviderWorkspace},
			{Name: colAPIKeyRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclAPIKeyRef},
			{Name: colActor, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclSpendActor},
			{Name: colServiceTier, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneCostLabel},
			{Name: colContextWindow, Kind: model.KindText, Nullable: true, Principal: pdeclNoneCostLabel},
			{Name: colInferenceGeo, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneCostLabel},
			{Name: colGateway, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneCostLabel},
			{Name: colCostType, Kind: model.KindText, Nullable: true, Principal: pdeclNoneCostLabel},
			// Firm identity — resolved at ingest from agent.IdentityID, else
			// api_key/actor matched to a roster Identity.ExternalID. Indexed: a
			// per-identity budget aggregates on identity_ref.
			{Name: colIdentityRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclIdentityRef},
			{Name: colIdentityKind, Kind: model.KindText, Nullable: true, Principal: pdeclNoneIdentityLabel},
			{Name: colIdentitySource, Kind: model.KindText, Nullable: true, Principal: pdeclNoneIdentityLabel},
			{Name: colCacheReadTokens, Kind: model.KindInt},
			{Name: colCacheCreation1hTokens, Kind: model.KindInt},
			{Name: colCacheCreation5mTokens, Kind: model.KindInt},
			{Name: colCostRecordID, Kind: model.KindText, Nullable: true, Principal: pdeclNoneCostRecordID},
			// cost center resolved at ingestion from mapping rules.
			{Name: colCostCenterRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneCostCenterCode},
		},
		Indexes: []model.IndexSpec{{
			// One read-model row per NATURAL key (provider/model/dims/instant, NOT the
			// value): the ingestion dedup/upsert guard, so a re-pulled bucket replaces
			// its row instead of double-counting. Leads with tenant_id so it
			// never couples tenants.
			Name:    "finops_cost_sample_uniq",
			Columns: []string{model.ColTenantID, colSampleKey},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  seatCountKind,
		Table: seatCountTable,
		Fields: []model.FieldSpec{
			{Name: colSeatProvider, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSeat},
			{Name: colSeatDay, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSeat},
			{Name: colAssignedSeats, Kind: model.KindInt},
			{Name: colPremiumSeats, Kind: model.KindInt},
			{Name: colPendingInvites, Kind: model.KindInt},
		},
		Indexes: []model.IndexSpec{{
			// One row per (provider, day): a re-posted day REPLACES its values
			// (the same upsert spirit as the cost natural key) — seat counts are
			// a state snapshot, never additive.
			Name:    "finops_seat_count_uniq",
			Columns: []string{model.ColTenantID, colSeatProvider, colSeatDay},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  outcomeKind,
		Table: outcomeTable,
		Fields: []model.FieldSpec{
			{Name: colOutcomeKey, Kind: model.KindText, Principal: pdeclNoneNaturalKey},
			{Name: colOutcomeSubjectKind, Kind: model.KindText, Indexed: true, Principal: pdeclNoneOutcomeLabel},
			// The graded subject as posted: for the identity kind it is a roster
			// identity's reference (value.go:226-227). History of a result only.
			{Name: colOutcomeSubjectRef, Kind: model.KindText, Indexed: true, Principal: model.Scan(model.ClassEvidence)},
			{Name: colOutcomeRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneOutcomeLabel},
			{Name: colOutcomeVerdict, Kind: model.KindText, Indexed: true, Principal: pdeclNoneOutcomeLabel},
			{Name: colOutcomeValue, Kind: model.KindInt},
			{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colOutcomeSource, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneOutcomeLabel},
			// Resolved join keys (stamped at ingest) — all nullable: an outcome may
			// name a subject that does not resolve to an agent/identity (honest).
			{Name: colAgentRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneAgentRef},
			{Name: colIdentityRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclIdentityRef},
			{Name: colSessionRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneSessionRef},
		},
		Indexes: []model.IndexSpec{{
			// One row per outcome natural key (source/subject/outcome_ref/instant): a
			// re-posted outcome REPLACES its row instead of double-counting, the same
			// upsert spirit as the cost natural key. Leads with tenant_id.
			Name:    "finops_outcome_uniq",
			Columns: []string{model.ColTenantID, colOutcomeKey},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  budgetAlertKind,
		Table: budgetAlertTable,
		Fields: []model.FieldSpec{
			{Name: colBudgetID, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneBudgetID},
			{Name: colPeriod, Kind: model.KindText, Principal: pdeclNoneAlertLabel},
			{Name: colPeriodStart, Kind: model.KindTimestamp},
			{Name: colThresholdPct, Kind: model.KindInt},
			{Name: colDimension, Kind: model.KindText, Principal: pdeclNoneAlertLabel},
			{Name: colDimKey, Kind: model.KindText, Nullable: true, Principal: pdeclBudgetKeyEvidence},
			{Name: colAlertSpend, Kind: model.KindInt},
			{Name: colAlertLimit, Kind: model.KindInt},
			{Name: colSeverity, Kind: model.KindText, Principal: pdeclNoneAlertLabel},
			{Name: colTriggeredAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colAlertEvidence, Kind: model.KindJSON, Nullable: true, Principal: pdeclAlertEvidence},
			{Name: colAlertEvidenceHash, Kind: model.KindText, Nullable: true, Principal: pdeclNoneAlertHash},
		},
		Indexes: []model.IndexSpec{{
			Name:    "finops_budget_alert_uniq",
			Columns: []string{model.ColTenantID, colBudgetID, colPeriodStart, colThresholdPct},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  spendLimitAuditKind,
		Table: spendLimitAuditTable,
		Fields: []model.FieldSpec{
			// The administrator who changed a limit, as the caller's actor ref
			// "user:<id>" or "token:<id>" (spendlimits.go:320, spendlimits.go:370).
			{Name: colSpendAuditActor, Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
			{Name: colSpendAuditAction, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSpendAudit},
			{Name: colSpendAuditLimitID, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSpendAudit},
			{Name: colSpendAuditBefore, Kind: model.KindJSON, Nullable: true, Principal: pdeclSpendLimitState},
			{Name: colSpendAuditAfter, Kind: model.KindJSON, Nullable: true, Principal: pdeclSpendLimitState},
		},
	}); err != nil {
		return err
	}

	// cost center entity — the accounting code.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  costCenterKind,
		Table: costCenterTable,
		Fields: []model.FieldSpec{
			{Name: colCCCode, Kind: model.KindText, Principal: pdeclNoneCostCenterCode},
			{Name: colCCName, Kind: model.KindText, Principal: pdeclNoneCostCenterText},
			{Name: colCCDescription, Kind: model.KindText, Nullable: true, Principal: pdeclNoneCostCenterText},
			{Name: colCCOwner, Kind: model.KindText, Nullable: true, Principal: pdeclCostCenterOwner},
			{Name: colCCStatus, Kind: model.KindText, Indexed: true, Principal: pdeclNoneCostCenterStatus},
			{Name: colCCMetadata, Kind: model.KindText, Nullable: true, Principal: pdeclNoneCostCenterText},
		},
		Indexes: []model.IndexSpec{{
			Name:    "finops_cost_center_code_uniq",
			Columns: []string{model.ColTenantID, colCCCode},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// cost center mapping rules.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  costCenterMappingKind,
		Table: costCenterMappingTable,
		Fields: []model.FieldSpec{
			{Name: colCCMappingCostCenterID, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneCostCenterID},
			{Name: colCCMappingDimension, Kind: model.KindText, Indexed: true, Principal: pdeclNoneMappingDimension},
			{Name: colCCMappingKey, Kind: model.KindText, Principal: pdeclMappingKey},
			{Name: colCCMappingPriority, Kind: model.KindInt},
		},
		Indexes: []model.IndexSpec{{
			Name:    "finops_cc_mapping_dim_key_uniq",
			Columns: []string{model.ColTenantID, colCCMappingDimension, colCCMappingKey},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// model rate catalog — admin-configured per-model token rates.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  modelRateKind,
		Table: modelRateTable,
		Fields: []model.FieldSpec{
			{Name: colRateProvider, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRate},
			{Name: colRateModel, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRate},
			{Name: colRateInputMicroUSD, Kind: model.KindInt},
			{Name: colRateOutputMicroUSD, Kind: model.KindInt},
			{Name: colRateCacheReadMicroUSD, Kind: model.KindInt},
			{Name: colRateCacheCreationMicroUSD, Kind: model.KindInt},
			{Name: colRateEffectiveFrom, Kind: model.KindTimestamp, Indexed: true},
			{Name: colRateEffectiveUntil, Kind: model.KindTimestamp, Nullable: true},
			{Name: colRateNotes, Kind: model.KindText, Nullable: true, Principal: pdeclNoneRate},
		},
		Indexes: []model.IndexSpec{{
			Name:    "finops_model_rate_uniq",
			Columns: []string{model.ColTenantID, colRateProvider, colRateModel, colRateEffectiveFrom},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// chargeback statement — periodic per-CC snapshot.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  chargebackStatementKind,
		Table: chargebackStatementTable,
		Fields: []model.FieldSpec{
			{Name: colStmtKey, Kind: model.KindText, Principal: pdeclNoneStatement},
			{Name: colStmtCostCenterID, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneCostCenterID},
			{Name: colStmtCostCenterCode, Kind: model.KindText, Indexed: true, Principal: pdeclNoneStatement},
			{Name: colStmtCostCenterName, Kind: model.KindText, Principal: pdeclNoneStatement},
			{Name: colStmtPeriod, Kind: model.KindText, Principal: pdeclNoneStatement},
			{Name: colStmtPeriodStart, Kind: model.KindTimestamp, Indexed: true},
			{Name: colStmtPeriodEnd, Kind: model.KindTimestamp},
			{Name: colStmtTotalMicroUSD, Kind: model.KindInt},
			{Name: colStmtLineCount, Kind: model.KindInt},
			{Name: colStmtPriorTotal, Kind: model.KindInt},
			{Name: colStmtDeltaPct, Kind: model.KindInt},
			{Name: colStmtStatus, Kind: model.KindText, Indexed: true, Principal: pdeclNoneStatement},
			{Name: colStmtGeneratedAt, Kind: model.KindTimestamp},
		},
		Indexes: []model.IndexSpec{{
			Name:    "finops_chargeback_stmt_uniq",
			Columns: []string{model.ColTenantID, colStmtKey},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// statement line items.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  statementLineKind,
		Table: statementLineTable,
		Fields: []model.FieldSpec{
			{Name: colLineStatementID, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneStatementLine},
			{Name: colLineModelRef, Kind: model.KindText, Principal: pdeclNoneStatementLine},
			{Name: colLineProviderRef, Kind: model.KindText, Principal: pdeclNoneStatementLine},
			{Name: colLineAgentRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneStatementLine},
			{Name: colLineInputTokens, Kind: model.KindInt},
			{Name: colLineOutputTokens, Kind: model.KindInt},
			{Name: colLineCostMicroUSD, Kind: model.KindInt},
			{Name: colLineSampleCount, Kind: model.KindInt},
		},
	}); err != nil {
		return err
	}

	// The attempt-lifecycle parent and the per-tenant activation frontier.
	// Registered BEFORE the reservation ledger below because the reservation's new
	// nullable linkage columns only mean something once the parent they point at
	// exists as a declared entity.
	if err := registerAttemptSchema(reg); err != nil {
		return err
	}

	// The admission row. A database without the table gets it whole
	// (applyModuleTables); one that has the earlier eight columns gets owed_handles
	// added in place (reconcileColumns). Additive and nullable only: no column is
	// altered or dropped, and no index is added to an existing table.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  admissionIdempotencyKind,
		Table: admissionIdempotencyTable,
		Fields: []model.FieldSpec{
			{Name: colAdmKey, Kind: model.KindText, Indexed: true, Principal: pdeclAdmissionKey},
			{Name: colAdmPayloadHash, Kind: model.KindText, Principal: pdeclNoneAdmissionHash},
			{Name: colAdmHandle, Kind: model.KindText, Indexed: true, Principal: pdeclNoneAdmissionHold},
			{Name: colAdmSpendHandle, Kind: model.KindText, Nullable: true, Principal: pdeclNoneAdmissionHold},
			{Name: colAdmScope, Kind: model.KindText, Principal: pdeclNoneAdmissionState},
			{Name: colAdmEstimate, Kind: model.KindInt},
			{Name: colAdmState, Kind: model.KindText, Indexed: true, Principal: pdeclNoneAdmissionState},
			{Name: colAdmStateAt, Kind: model.KindText, Nullable: true, Principal: pdeclNoneAdmissionState},
			// Text: the codec in admission_hold.go, not the store, decides what a
			// valid list is, and an undecodable one is kept byte for byte.
			{Name: colAdmOwedHandles, Kind: model.KindText, Nullable: true, Principal: pdeclNoneAdmissionHold},
		},
		Indexes: []model.IndexSpec{{
			Name:    "finops_admission_idempotency_key_uniq",
			Columns: []string{model.ColTenantID, colAdmKey},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the dynamic reserve ledger (TOCTOU fix). A fresh table, added additively
	// — applyModuleTables creates it on both a fresh DB and an in-place upgrade (it is
	// a missing module table), so this is the "new migration" in descriptor form; no
	// existing descriptor is modified.
	return reg.Register(model.EntityDescriptor{
		Kind:  budgetReservationKind,
		Table: budgetReservationTable,
		Fields: []model.FieldSpec{
			{Name: colResvPolicyRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneReservation},
			{Name: colResvPolicyKind, Kind: model.KindText, Principal: pdeclNoneReservation},
			{Name: colResvDimension, Kind: model.KindText, Nullable: true, Principal: pdeclNoneReservation},
			{Name: colResvScopeKey, Kind: model.KindText, Principal: pdeclBudgetKeyRestrict},
			{Name: colResvPeriod, Kind: model.KindText, Principal: pdeclNoneReservation},
			{Name: colResvPeriodStart, Kind: model.KindTimestamp, Indexed: true},
			{Name: colResvSeq, Kind: model.KindInt},
			{Name: colResvAmount, Kind: model.KindInt},
			{Name: colResvActual, Kind: model.KindInt},
			{Name: colResvState, Kind: model.KindText, Indexed: true, Principal: pdeclNoneReservation},
			{Name: colResvHandle, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneReservation},
			{Name: colResvExpiresAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colResvSettledAt, Kind: model.KindTimestamp, Nullable: true},
			// Attempt-lifecycle linkage. BOTH are nullable and BOTH stay NULL on every
			// row this module writes today: the engine's additive reconciliation adds
			// them to an existing populated table on an in-place upgrade, and a NULL
			// pair is exactly what the legacy read branch selects on.
			//
			// "Legacy" is the pair being NULL, not either column being empty: an empty
			// attempt_ref, an unknown lifecycle version or one column set without the
			// other is a MALFORMED row, and reading it as legacy would hand a v1
			// obligation to a branch that can expire it on a TTL.
			{Name: colResvAttemptRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneAttemptRef},
			{Name: colResvLifecycleVersion, Kind: model.KindInt, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// The serialization constraint: a monotonic seq per
			// (policy, period, scope_key). Two concurrent reservers on the same scope
			// compute the SAME next seq and both INSERT; the UNIQUE index lets exactly
			// one commit and maps the other to store.ErrConflict (mapWriteErr) — the
			// reserve loop retries and re-reads the now-committed reservation, so
			// read-check-insert is atomic without a process lock. scope_key keeps a
			// per-seat spend limit's actors on independent seq lines. Leads with
			// tenant_id.
			Name:    "finops_budget_reservation_seq_uniq",
			Columns: []string{model.ColTenantID, colResvPolicyRef, colResvPeriodStart, colResvScopeKey, colResvSeq},
			Unique:  true,
		}, {
			// One v1 child per (attempt, policy, scope, period). NULL attempt_ref
			// rows do not collide on either engine — SQLite and PostgreSQL both treat
			// NULLs as distinct in a unique index — so every legacy row stays outside
			// this constraint while a v1 group cannot acquire two children for one
			// target.
			Name:    "finops_reservation_attempt_target_uniq",
			Columns: []string{model.ColTenantID, colResvAttemptRef, colResvPolicyRef, colResvScopeKey, colResvPeriodStart},
			Unique:  true,
		}, {
			// The lookup a v1 hold read issues: a parent's complete child set.
			Name:    "finops_reservation_attempt_idx",
			Columns: []string{model.ColTenantID, colResvAttemptRef},
		}},
	})
}
