// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Registered entity kinds (the module's owned schema).
const (
	liveKind     model.Kind = "sessions.live"
	timelineKind model.Kind = "sessions.timeline"
	templateKind model.Kind = "sessions.template"
)

// Physical tables for the registered entities.
const (
	liveTable     = "sessions_live"
	timelineTable = "sessions_timeline"
	templateTable = "sessions_template"
)

// sessions.live columns: the live operational state of one session, keyed by its
// external reference.
const (
	colSessionRef   = "session_ref"
	colAgentRef     = "agent_ref"
	colCurrentTool  = "current_action"
	colCurrentRes   = "current_resource"
	colCurrentMode  = "current_mode"
	colModelRef     = "model_ref"
	colInputTokens  = "input_tokens"
	colOutputTokens = "output_tokens"
	colCostMicroUSD = "cost_micro_usd"
	colEventCount   = "event_count"
	colToolCalls    = "tool_call_count"
	colFirstEventAt = "first_event_at"
	colLastEventAt  = "last_event_at"
	colEvasionAt    = "evasion_at"
	colGoal         = "goal"
	colSummary      = "summary"
	// colUnclaimedAt (SG-02) is sticky: activity was seen from a session holding
	// no live claim. NULLABLE on purpose — the engine's strictly-additive
	// reconcile adds a nullable column to an existing table but REFUSES a NOT
	// NULL one (sqlstore/schema.go:646-651), and a populated sessions_live must
	// keep working across the upgrade.
	colUnclaimedAt = "unclaimed_at"
	// colEngine and colPosture (SG-01) name WHICH engine drives a session and how
	// firmly it is governed. They exist because the live view previously had no way
	// to tell a Claude session from a Codex one — the provider is on the wire and was
	// dropped at the fold — and with Codex able to be enforced on some events and only
	// observed on others, painting both classes identically asserts a control that in
	// one case does not exist. NULLABLE for the same additive-reconcile reason as
	// colUnclaimedAt, and because "unknown" is an honest state: a session that has not
	// yet made a tool call has told us nothing about its engine.
	colEngine  = "engine"
	colPosture = "posture"
	// B1 provider-instance identity of a live row (all nullable — additive
	// reconcile, and NULL is the honest legacy answer). observation_scope is
	// computed by the SERVER from the stamped source registration or the owning
	// bridge, never from a payload label:
	//   NULL                                  legacy (read as "legacy")
	//   observed:<profile_id>                 cooperative observation attributed to a profile
	//   source:<source_id>:<rev>:<env>:<drv>  known registration without a verifiable profile
	//   managed:<canonical_sid>               fed by the plane's own bridge for a launched run
	// session_ref keeps the external id for compatibility; sessions_live.id is the
	// opaque public live_ref new readers navigate by. canonical_sid is written ONLY
	// by the owning bridge and is what a run may be joined on — never a
	// retrospective external-id join.
	colObservationScope = "observation_scope"
	colLiveProfileID    = "provider_profile_id"
	colLiveProvider     = "provider"
	colLiveCanonicalSID = "canonical_sid"
	colLiveEnvRef       = "environment_ref"
	colLiveBindingRef   = "source_binding_ref"
	// colLiveRunRef is the run a MANAGED row belongs to, written by the bridge in
	// the transaction that proved the run owns the announced id. NULL elsewhere.
	colLiveRunRef = "run_ref"
)

// sessions.timeline columns: one replayable event in a session's history.
const (
	colTLSessionRef = "session_ref"
	colTLAt         = "at"
	colTLKind       = "kind"
	colTLToolRef    = "tool_ref"
	colTLResource   = "resource_ref"
	colTLMode       = "mode"
	colTLSource     = "source"
	colTLTitle      = "title"
	// B1: the EXACT live row this event was folded into, written in the same
	// mutation as the fold, plus the binding it was attributed under. Legacy rows
	// keep only session_ref; nothing assigns them a live_ref after the fact.
	colTLLiveRef    = "live_ref"
	colTLBindingRef = "source_binding_ref"
)

// sessions.template columns: the workspace template definition. The "version"
// base column is auto-managed by the store (optimistic concurrency counter), so
// it is NOT declared here.
const (
	colTplName        = "name"
	colTplDescription = "description"
	colTplAuthor      = "author"
	colTplBuiltin     = "builtin"
	colTplArchivedAt  = "archived_at"
	colTplBody        = "body"
)

// Claude Code state (derived at read time from activity recency / evasion). The
// session has no stored lifecycle column: the cooperative observation stream
// carries no session-end or failure signal, so a stored "state" could only ever
// be "running" — cc_state is the honest, derived liveness signal instead.
const (
	ccActive  = "active"
	ccIdle    = "idle"
	ccEnded   = "ended"
	ccEvasion = "silent_evasion"
)

// Timeline event kinds.
const (
	tlTool    = "tool"
	tlMCP     = "mcp"
	tlCost    = "cost"
	tlFinding = "finding"
)

// RegisterSchema declares the module's two owned entities. The engine creates
// the tables, injects the base columns and attaches the tenant guards (S02 §7).
// Neither is audited: live updates are high-frequency automated ingestion, and
// reads of the live operation are gated by RBAC at the API.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  liveKind,
		Table: liveTable,
		Fields: []model.FieldSpec{
			{Name: colSessionRef, Kind: model.KindText},
			{Name: colAgentRef, Kind: model.KindText, Nullable: true},
			{Name: colCurrentTool, Kind: model.KindText, Nullable: true},
			{Name: colCurrentRes, Kind: model.KindText, Nullable: true},
			{Name: colCurrentMode, Kind: model.KindText, Nullable: true},
			{Name: colModelRef, Kind: model.KindText, Nullable: true},
			{Name: colInputTokens, Kind: model.KindInt},
			{Name: colOutputTokens, Kind: model.KindInt},
			{Name: colCostMicroUSD, Kind: model.KindInt},
			{Name: colEventCount, Kind: model.KindInt},
			{Name: colToolCalls, Kind: model.KindInt},
			{Name: colFirstEventAt, Kind: model.KindTimestamp},
			{Name: colLastEventAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colEvasionAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colGoal, Kind: model.KindText, Nullable: true},
			{Name: colSummary, Kind: model.KindText, Nullable: true},
			{Name: colUnclaimedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colEngine, Kind: model.KindText, Nullable: true},
			{Name: colPosture, Kind: model.KindText, Nullable: true},
			{Name: colObservationScope, Kind: model.KindText, Nullable: true},
			{Name: colLiveProfileID, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colLiveProvider, Kind: model.KindText, Nullable: true},
			{Name: colLiveCanonicalSID, Kind: model.KindText, Nullable: true},
			{Name: colLiveEnvRef, Kind: model.KindText, Nullable: true},
			{Name: colLiveBindingRef, Kind: model.KindText, Nullable: true},
			{Name: colLiveRunRef, Kind: model.KindText, Nullable: true},
		},
		// B1: the historical UNIQUE (tenant_id, session_ref) index is deliberately
		// NOT declared any more. Its successor is UNIQUE (tenant_id,
		// COALESCE(observation_scope,'legacy'), session_ref) — an expression index the
		// descriptor cannot express (IndexSpec.Columns are column names) — created by
		// the module migrations, which also drop the old one. Declaring the old one
		// here would make reconcileColumns recreate it on every boot and refuse the
		// scoped rows the new index exists to admit. NOTE for operators: an OLDER
		// binary booting this database would recreate it from its own descriptor;
		// that is why a downgrade across this change is not a supported rolling path.
		Indexes: []model.IndexSpec{{
			Name:    "sessions_live_profile_scope_idx",
			Columns: []string{model.ColTenantID, colObservationScope},
		}},
	}); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:  timelineKind,
		Table: timelineTable,
		Fields: []model.FieldSpec{
			{Name: colTLSessionRef, Kind: model.KindText, Indexed: true},
			{Name: colTLAt, Kind: model.KindTimestamp},
			{Name: colTLKind, Kind: model.KindText},
			{Name: colTLToolRef, Kind: model.KindText, Nullable: true},
			{Name: colTLResource, Kind: model.KindText, Nullable: true},
			{Name: colTLMode, Kind: model.KindText, Nullable: true},
			{Name: colTLSource, Kind: model.KindText, Nullable: true},
			{Name: colTLTitle, Kind: model.KindText, Nullable: true},
			{Name: colTLLiveRef, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colTLBindingRef, Kind: model.KindText, Nullable: true},
		},
	}); err != nil {
		return err
	}
	// workspace templates.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  templateKind,
		Table: templateTable,
		Fields: []model.FieldSpec{
			{Name: colTplName, Kind: model.KindText},
			{Name: colTplDescription, Kind: model.KindText},
			{Name: colTplAuthor, Kind: model.KindText},
			{Name: colTplBuiltin, Kind: model.KindBool},
			{Name: colTplArchivedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colTplBody, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			Name:    "sessions_template_name_uniq",
			Columns: []string{model.ColTenantID, colTplName},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}
	// the OPERATE entities (managed run + lifecycle ledger).
	if err := m.registerRuntimeSchema(reg); err != nil {
		return err
	}
	// SG-00: the canonical session identity and its provider aliases.
	if err := m.registerIdentitySchema(reg); err != nil {
		return err
	}
	// SG-02: the admission plane (claim + lease + fencing).
	if err := m.registerClaimSchema(reg); err != nil {
		return err
	}
	// B1: provider profiles, source→profile bindings and profile-scoped aliases.
	if err := m.registerProviderProfileSchema(reg); err != nil {
		return err
	}
	// the WORKSPACE registry (host filesystem root bound to sessions).
	if err := m.registerWorkspaceSchema(reg); err != nil {
		return err
	}
	// K1: durable work across session lifetimes. Keep this last so its single
	// migration/invariant registration remains easy to extend in later cuts.
	return m.registerWorkSchema(reg)
}
