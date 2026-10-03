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
			{Name: colSessionRef, Kind: model.KindText, Principal: pdeclNoneExternalSessionRef},
			{Name: colAgentRef, Kind: model.KindText, Nullable: true, Principal: model.None("an observed agent's opaque name from an identity-attribution edge, filtered and shown only, never an account: live.go:109-110, dto.go:81, export.go:108")},
			{Name: colCurrentTool, Kind: model.KindText, Nullable: true, Principal: model.None("the tool reference of the last observed tool call, shown only: live.go:78, dto.go:85")},
			{Name: colCurrentRes, Kind: model.KindText, Nullable: true, Principal: pdeclNoneObservedResource},
			{Name: colCurrentMode, Kind: model.KindText, Nullable: true, Principal: model.None("the access mode of the last observed tool call, shown only: live.go:80, dto.go:87")},
			{Name: colModelRef, Kind: model.KindText, Nullable: true, Principal: model.None("a model id reported by a cost observation, filtered and shown only: live.go:162, dto.go:88, export.go:111")},
			{Name: colInputTokens, Kind: model.KindInt},
			{Name: colOutputTokens, Kind: model.KindInt},
			{Name: colCostMicroUSD, Kind: model.KindInt},
			{Name: colEventCount, Kind: model.KindInt},
			{Name: colToolCalls, Kind: model.KindInt},
			{Name: colFirstEventAt, Kind: model.KindTimestamp},
			{Name: colLastEventAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colEvasionAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colGoal, Kind: model.KindText, Nullable: true, Principal: model.None("a session goal label no writer of this module sets, shown only: dto.go:97")},
			{Name: colSummary, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded finding title from a context-compaction finding, shown only: live.go:216-217, dto.go:98")},
			{Name: colUnclaimedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colEngine, Kind: model.KindText, Nullable: true, Principal: model.None("an engine label from the profile driver or the producing connector, shown only: live.go:95-98, dto.go:83")},
			{Name: colPosture, Kind: model.KindText, Nullable: true, Principal: model.None("an enforcement-posture label from the producing connector, shown only: live.go:100-103, dto.go:84")},
			{Name: colObservationScope, Kind: model.KindText, Nullable: true, Principal: model.None("a server-computed attribution scope over profile, source and session ids, read only as a fold key and an attribution label: live_scope.go:69-81, live_scope.go:122-124, dto.go:102")},
			{Name: colLiveProfileID, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneProfileRef},
			{Name: colLiveProvider, Kind: model.KindText, Nullable: true, Principal: model.None("the driver key of the attributed profile, shown only: live_scope.go:135, dto.go:104")},
			{Name: colLiveCanonicalSID, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSID},
			{Name: colLiveEnvRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneEnvRef},
			{Name: colLiveBindingRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneBindingRef},
			{Name: colLiveRunRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneRunRef},
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
			{Name: colTLSessionRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneExternalSessionRef},
			{Name: colTLAt, Kind: model.KindTimestamp},
			{Name: colTLKind, Kind: model.KindText, Principal: model.None("a timeline event kind, a closed set: schema.go:125-130, dto.go:138")},
			{Name: colTLToolRef, Kind: model.KindText, Nullable: true, Principal: model.None("the tool reference of an observed call, shown only: live.go:120, dto.go:139")},
			{Name: colTLResource, Kind: model.KindText, Nullable: true, Principal: pdeclNoneObservedResource},
			{Name: colTLMode, Kind: model.KindText, Nullable: true, Principal: model.None("the access mode of an observed call, shown only: live.go:120, dto.go:141")},
			{Name: colTLSource, Kind: model.KindText, Nullable: true, Principal: model.None("the producing source label or finding kind of an event, shown only: live.go:120, live.go:224, dto.go:142")},
			{Name: colTLTitle, Kind: model.KindText, Nullable: true, Principal: model.None("a display title of an observed event (a tool edge, a token count or a finding title), shown only: live.go:120, live.go:170, live.go:224, dto.go:143")},
			{Name: colTLLiveRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: model.None("the id of the live row the event was folded into: live_scope.go:264-266")},
			{Name: colTLBindingRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneBindingRef},
		},
	}); err != nil {
		return err
	}
	// workspace templates.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  templateKind,
		Table: templateTable,
		Fields: []model.FieldSpec{
			{Name: colTplName, Kind: model.KindText, Principal: model.None("an operator-chosen template name, the natural key, shown only: templates.go:85, templates.go:675")},
			{Name: colTplDescription, Kind: model.KindText, Principal: model.None("template prose, shown only: templates.go:86")},
			{Name: colTplAuthor, Kind: model.KindText, Principal: pdeclActorRef},
			{Name: colTplBuiltin, Kind: model.KindBool},
			{Name: colTplArchivedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colTplBody, Kind: model.KindText, Principal: pdeclTemplateBody},
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

// Principal declarations of the observe overlay and template descriptors, and
// the declarations several descriptors of this module share. A shared
// declaration cites lines that hold for every column that uses it.
var (
	// pdeclActorRef is the caller's audit actor string: "user:<account id>",
	// "token:<credential id>" or a system label (runtime_api.go:85,
	// templates.go:203). It records who acted and is kept as evidence.
	pdeclActorRef = model.Ref(model.EncodeUserRef, model.ClassEvidence)

	pdeclNoneSID               = model.None("a canonical session id, the osn_ prefix and a UUID minted by the identity plane, parsed as a session and never as an account: identity.go:187, work_lease.go:1029-1038")
	pdeclNoneRunRef            = model.None("the opaque run reference (a UUID) this module mints for an operated run, never an account: runtime.go:892, runtime_dto.go:118")
	pdeclNoneProfileRef        = model.None("a provider profile id, the ppf_ prefix and a UUID minted by this module, read back only to find that profile: provider_profile.go:387, provider_profile.go:453")
	pdeclNoneDriverKey         = model.None("a provider driver key of lowercase letters, digits and - _ . only, never an account: provider_profile.go:280-297")
	pdeclNoneEnvRef            = model.None("an execution-environment reference, printable with no whitespace or separator: provider_profile.go:301")
	pdeclNoneHomePath          = model.None("a canonical home directory path on the execution environment: provider_profile.go:319")
	pdeclNoneProviderRecordRef = model.None("a provider record id, the prv_ prefix and a UUID, a locator of a registered credential record and never an account: provider_record.go:361-370")
	pdeclNoneBindingRef        = model.None("a provider source binding id, the psb_ prefix and a UUID minted by this module: provider_source_binding.go:179, provider_source_binding.go:239")
	pdeclNoneAuthSource        = model.None("an authorized authentication source, a closed set: runtime_provider_auth.go:45-63")
	pdeclNonePermissionMode    = model.None("a permission mode, a closed set: runtime_ports.go:90-93")

	pdeclNoneExternalSessionRef = model.None("a provider-issued session id as observed, used only as the fold key and shown as-is, never resolved to an account: live_scope.go:122-124, live_scope.go:264-271, dto.go:80")
	pdeclNoneObservedResource   = model.None("the resource reference of an observed tool call, shown only and never resolved to an account: live.go:79, live.go:120, dto.go:86, dto.go:140")

	// pdeclTemplateBody is the template body, the tplBody the template writer
	// marshals (templates.go:205) and the launch reduces to its terms
	// (templateapply.go:141-157). No leaf names a principal.
	pdeclTemplateBody = model.Nested(tplBody{}, model.ClassEvidence,
		model.Leaf("hooks.pre_tool[].command", pdeclNoneTemplateHook),
		model.Leaf("hooks.post_tool[].command", pdeclNoneTemplateHook),
		model.Leaf("hooks.pre_session[].command", pdeclNoneTemplateHook),
		model.Leaf("hooks.post_session[].command", pdeclNoneTemplateHook),
		model.Leaf("settings.permission_mode", model.None("a permission mode, checked against a closed set: templateapply.go:145, templateapply.go:180")),
		model.Leaf("settings.effort", model.None("an effort level, checked against a closed set: templateapply.go:146, templateapply.go:183")),
		model.Leaf("settings.model", model.None("a model id a launch passes to the child: templateapply.go:147")),
		model.Leaf("settings.custom_instructions", model.None("instruction prose a launch passes to the child: templateapply.go:148")),
		model.Leaf("connectors[]", model.None("a connector name no launch consumes: templateapply.go:176-179")),
		model.Leaf("policies.dlp_mode", model.None("a DLP posture label: templateapply.go:158")),
		model.Leaf("policies.allowed_tools[]", model.None("a tool name: templateapply.go:151")),
		model.Leaf("peers_rule", model.None("a peer selection rule, checked as same-template or omission: templates.go:58, templateapply.go:284-286")),
		model.Leaf("settings.secret_env[].env", model.None("an environment variable NAME a launch sets on the child, checked against reserved and malformed names, never a value: templateapply.go:150, templateapply.go:391-407, session_secret_env.go:72")),
		model.Leaf("settings.secret_env[].secret", model.None("a vault secret NAME under env/, opened only at launch and never stored or returned as a value: templateapply.go:150, session_secret_env.go:72, session_secret_env.go:113")),
	)
	pdeclNoneTemplateHook = model.None("a hook command a launch never runs: templateapply.go:163-175")
)
