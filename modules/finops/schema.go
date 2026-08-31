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
)

const (
	costSampleTable          = "finops_cost_sample"
	budgetAlertTable         = "finops_budget_alert"
	spendLimitAuditTable     = "finops_spend_limit_audit"
	seatCountTable           = "finops_seat_count"
	outcomeTable             = "finops_outcome"
	costCenterTable          = "finops_cost_center"
	costCenterMappingTable   = "finops_cost_center_mapping"
	modelRateTable           = "finops_model_rate"
	chargebackStatementTable = "finops_chargeback_statement"
	statementLineTable       = "finops_statement_line"
	budgetReservationTable   = "finops_budget_reservation"
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

// reservation lifecycle states.
const (
	resvStateActive    = "active"
	resvStateCommitted = "committed"
	resvStateReleased  = "released"
	resvStateExpired   = "expired"
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
			{Name: colSampleKey, Kind: model.KindText},
			{Name: colProviderRef, Kind: model.KindText, Indexed: true},
			{Name: colModelRef, Kind: model.KindText, Indexed: true},
			{Name: colAgentRef, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colSessionRef, Kind: model.KindText, Nullable: true},
			{Name: colTeam, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colProject, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colInputTokens, Kind: model.KindInt},
			{Name: colOutputTokens, Kind: model.KindInt},
			{Name: colCostMicroUSD, Kind: model.KindInt},
			{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
			// Additive dimensions. All nullable/zero-default so existing rows and
			// connectors that do not report a dimension stay valid.
			{Name: colProvenance, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colWorkspaceRef, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colAPIKeyRef, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colActor, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colServiceTier, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colContextWindow, Kind: model.KindText, Nullable: true},
			{Name: colInferenceGeo, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colGateway, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colCostType, Kind: model.KindText, Nullable: true},
			// Firm identity — resolved at ingest from agent.IdentityID, else
			// api_key/actor matched to a roster Identity.ExternalID. Indexed: a
			// per-identity budget aggregates on identity_ref.
			{Name: colIdentityRef, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colIdentityKind, Kind: model.KindText, Nullable: true},
			{Name: colIdentitySource, Kind: model.KindText, Nullable: true},
			{Name: colCacheReadTokens, Kind: model.KindInt},
			{Name: colCacheCreation1hTokens, Kind: model.KindInt},
			{Name: colCacheCreation5mTokens, Kind: model.KindInt},
			{Name: colCostRecordID, Kind: model.KindText, Nullable: true},
			// cost center resolved at ingestion from mapping rules.
			{Name: colCostCenterRef, Kind: model.KindText, Nullable: true, Indexed: true},
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
			{Name: colSeatProvider, Kind: model.KindText, Indexed: true},
			{Name: colSeatDay, Kind: model.KindText, Indexed: true},
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
			{Name: colOutcomeKey, Kind: model.KindText},
			{Name: colOutcomeSubjectKind, Kind: model.KindText, Indexed: true},
			{Name: colOutcomeSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colOutcomeRef, Kind: model.KindText, Nullable: true},
			{Name: colOutcomeVerdict, Kind: model.KindText, Indexed: true},
			{Name: colOutcomeValue, Kind: model.KindInt},
			{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colOutcomeSource, Kind: model.KindText, Nullable: true, Indexed: true},
			// Resolved join keys (stamped at ingest) — all nullable: an outcome may
			// name a subject that does not resolve to an agent/identity (honest).
			{Name: colAgentRef, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colIdentityRef, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colSessionRef, Kind: model.KindText, Nullable: true, Indexed: true},
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
			{Name: colBudgetID, Kind: model.KindUUID, Indexed: true},
			{Name: colPeriod, Kind: model.KindText},
			{Name: colPeriodStart, Kind: model.KindTimestamp},
			{Name: colThresholdPct, Kind: model.KindInt},
			{Name: colDimension, Kind: model.KindText},
			{Name: colDimKey, Kind: model.KindText, Nullable: true},
			{Name: colAlertSpend, Kind: model.KindInt},
			{Name: colAlertLimit, Kind: model.KindInt},
			{Name: colSeverity, Kind: model.KindText},
			{Name: colTriggeredAt, Kind: model.KindTimestamp, Indexed: true},
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
			{Name: colSpendAuditActor, Kind: model.KindText},
			{Name: colSpendAuditAction, Kind: model.KindText, Indexed: true},
			{Name: colSpendAuditLimitID, Kind: model.KindText, Indexed: true},
			{Name: colSpendAuditBefore, Kind: model.KindJSON, Nullable: true},
			{Name: colSpendAuditAfter, Kind: model.KindJSON, Nullable: true},
		},
	}); err != nil {
		return err
	}

	// cost center entity — the accounting code.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  costCenterKind,
		Table: costCenterTable,
		Fields: []model.FieldSpec{
			{Name: colCCCode, Kind: model.KindText},
			{Name: colCCName, Kind: model.KindText},
			{Name: colCCDescription, Kind: model.KindText, Nullable: true},
			{Name: colCCOwner, Kind: model.KindText, Nullable: true},
			{Name: colCCStatus, Kind: model.KindText, Indexed: true},
			{Name: colCCMetadata, Kind: model.KindText, Nullable: true},
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
			{Name: colCCMappingCostCenterID, Kind: model.KindUUID, Indexed: true},
			{Name: colCCMappingDimension, Kind: model.KindText, Indexed: true},
			{Name: colCCMappingKey, Kind: model.KindText},
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
			{Name: colRateProvider, Kind: model.KindText, Indexed: true},
			{Name: colRateModel, Kind: model.KindText, Indexed: true},
			{Name: colRateInputMicroUSD, Kind: model.KindInt},
			{Name: colRateOutputMicroUSD, Kind: model.KindInt},
			{Name: colRateCacheReadMicroUSD, Kind: model.KindInt},
			{Name: colRateCacheCreationMicroUSD, Kind: model.KindInt},
			{Name: colRateEffectiveFrom, Kind: model.KindTimestamp, Indexed: true},
			{Name: colRateEffectiveUntil, Kind: model.KindTimestamp, Nullable: true},
			{Name: colRateNotes, Kind: model.KindText, Nullable: true},
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
			{Name: colStmtKey, Kind: model.KindText},
			{Name: colStmtCostCenterID, Kind: model.KindUUID, Indexed: true},
			{Name: colStmtCostCenterCode, Kind: model.KindText, Indexed: true},
			{Name: colStmtCostCenterName, Kind: model.KindText},
			{Name: colStmtPeriod, Kind: model.KindText},
			{Name: colStmtPeriodStart, Kind: model.KindTimestamp, Indexed: true},
			{Name: colStmtPeriodEnd, Kind: model.KindTimestamp},
			{Name: colStmtTotalMicroUSD, Kind: model.KindInt},
			{Name: colStmtLineCount, Kind: model.KindInt},
			{Name: colStmtPriorTotal, Kind: model.KindInt},
			{Name: colStmtDeltaPct, Kind: model.KindInt},
			{Name: colStmtStatus, Kind: model.KindText, Indexed: true},
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
			{Name: colLineStatementID, Kind: model.KindUUID, Indexed: true},
			{Name: colLineModelRef, Kind: model.KindText},
			{Name: colLineProviderRef, Kind: model.KindText},
			{Name: colLineAgentRef, Kind: model.KindText, Nullable: true},
			{Name: colLineInputTokens, Kind: model.KindInt},
			{Name: colLineOutputTokens, Kind: model.KindInt},
			{Name: colLineCostMicroUSD, Kind: model.KindInt},
			{Name: colLineSampleCount, Kind: model.KindInt},
		},
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
			{Name: colResvPolicyRef, Kind: model.KindUUID, Indexed: true},
			{Name: colResvPolicyKind, Kind: model.KindText},
			{Name: colResvDimension, Kind: model.KindText, Nullable: true},
			{Name: colResvScopeKey, Kind: model.KindText},
			{Name: colResvPeriod, Kind: model.KindText},
			{Name: colResvPeriodStart, Kind: model.KindTimestamp, Indexed: true},
			{Name: colResvSeq, Kind: model.KindInt},
			{Name: colResvAmount, Kind: model.KindInt},
			{Name: colResvActual, Kind: model.KindInt},
			{Name: colResvState, Kind: model.KindText, Indexed: true},
			{Name: colResvHandle, Kind: model.KindUUID, Indexed: true},
			{Name: colResvExpiresAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colResvSettledAt, Kind: model.KindTimestamp, Nullable: true},
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
		}},
	})
}
