// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables.
const (
	configKind  model.Kind = "inferenceproxy.config"
	configTable            = "inferenceproxy_config"

	dlpRuleKind  model.Kind = "inferenceproxy.dlp_rule"
	dlpRuleTable            = "inferenceproxy_dlp_rule"

	deviceGrantKind  model.Kind = "inferenceproxy.device_grant"
	deviceGrantTable            = "inferenceproxy_device_grant"
)

// config columns. The config row is a per-tenant SINGLETON (the unique index leads
// with — and is only — tenant_id). Every gate flag defaults to ENABLED so the
// safe behavior is "all gates on"; a tenant relaxes a specific gate explicitly. The
// gate flags are stored only to let a tenant TURN OFF a gate. DLP starts with tunable
// secret/unscanned deny rules; other underlying gates retain their native activation
// semantics (model-access until the first grant, residency only when region-pinned,
// budget until an enforcing budget). fail_open is the proxy-DOWN posture (per-tenant knob,
// default fail-CLOSED) — a security proxy that cannot decide must not forward.
const (
	colFailOpen        = "fail_open"           // proxy decision-plane unavailable ⇒ allow (default false = deny)
	colResponseDLPMode = "response_dlp_mode"   // off | flag | buffer (default buffer)
	colRecordMandatory = "record_mandatory"    // privileged: no ledger evidence ⇒ deny the forward
	colGateModelAccess = "gate_model_access"   // run EvaluateModelAccess (default true)
	colGateBudget      = "gate_budget"         // run CheckBudget (default true)
	colGateResidency   = "gate_residency"      // run InferenceGeoCompatible (default true)
	colGateContextWin  = "gate_context_window" // run CheckContextWindowForSurface (default true)
	colGateDLPRequest  = "gate_dlp_request"    // scan the prompt before forward (default true)
	colGateDLPResponse = "gate_dlp_response"   // scan the response (default true; mode per colResponseDLPMode)
	colUpdatedBy       = "updated_by"

	colCeilingsEnforce         = "ceilings_enforce"
	colCeilingMaxTokens        = "ceiling_max_tokens"
	colCeilingMaxToolUses      = "ceiling_max_tool_uses"
	colCeilingTaskBudgetTokens = "ceiling_task_budget_tokens"

	// colRecordMandatoryChosen answers the question colRecordMandatory cannot: did the
	// operator CHOOSE an evidence posture, or is the value just the default?
	//
	// The precedence rule — a default must not override an explicit operator choice —
	// was first implemented over `Configured`, which only means "this tenant has a
	// config row". A tenant that set the DLP mode and nothing else has it true, so the
	// rule refused to yield for somebody who had never chosen anything: the rule was
	// right and its signal was wrong.
	//
	// NULL is the whole point and it is why this is a new column rather than a widened
	// one: NULL means nobody has decided, and no default may fabricate a decision.
	// Appended LAST and nullable, which is this schema's expand-only growth vehicle
	// (see the note above RegisterSchema) — the existing columns are untouched.
	colRecordMandatoryChosen = "record_mandatory_chosen"
)

// dlp_rule columns (the inference-egress DLP policy algebra). class is a
// sensitivity class id (or the reserved "*" / "unscanned"); action is allow | deny.
const (
	colClass     = "class"
	colAction    = "action"
	colNote      = "note"
	colCreatedBy = "created_by"
)

// device_grant columns. The row carries no bearer material: device_code is an
// opaque polling handle, user_code is the short human approval code, and tenant is
// filled only when an authenticated approver binds the grant to a tenant.
const (
	colDeviceCode   = "device_code"
	colUserCode     = "user_code"
	colGrantState   = "state"
	colGrantExpires = "expires_at"
	colApprovedBy   = "approved_by"
	colGrantTenant  = "tenant"
	colLastPollAt   = "last_poll_at"
)

// RegisterSchema declares the module's owned entities. It satisfies the engine-side
// runtime.SchemaProvider seam (structural — no runtime import) and is called once, at
// store construction, before any Scope exists (S02 §7 /). The engine creates
// the tables, injects the base columns and attaches the tenant guards.
//
// Both tables are MINIMAL-DATA by construction: no column can hold a prompt, response,
// secret or matched PII value — only toggles, a class id and an action (docs/SECURITY-HARDENING.md).
// New columns are appended last and nullable when added after release, per the expand-only
// growth vehicle (the boot reconcile, reconcileColumns in sqlstore, ALTER TABLE ADD
// COLUMNs missing nullable fields on an already-migrated DB — for module tables as well as
// core ones). Nullable bool/text/int fields are reconcile-safe; non-nullable additions need
// a hand-authored migration or a safe read-time default.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  configKind,
		Table: configTable,
		Fields: []model.FieldSpec{
			{Name: colFailOpen, Kind: model.KindBool},
			{Name: colResponseDLPMode, Kind: model.KindText},
			{Name: colRecordMandatory, Kind: model.KindBool},
			{Name: colGateModelAccess, Kind: model.KindBool},
			{Name: colGateBudget, Kind: model.KindBool},
			{Name: colGateResidency, Kind: model.KindBool},
			{Name: colGateContextWin, Kind: model.KindBool},
			{Name: colGateDLPRequest, Kind: model.KindBool},
			{Name: colGateDLPResponse, Kind: model.KindBool},
			{Name: colUpdatedBy, Kind: model.KindText},
			{Name: colCeilingsEnforce, Kind: model.KindBool, Nullable: true},
			{Name: colCeilingMaxTokens, Kind: model.KindInt, Nullable: true},
			{Name: colCeilingMaxToolUses, Kind: model.KindInt, Nullable: true},
			{Name: colCeilingTaskBudgetTokens, Kind: model.KindInt, Nullable: true},
			{Name: colRecordMandatoryChosen, Kind: model.KindBool, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One config row per tenant (a singleton). The unique index is tenant_id
			// alone — exactly one row per tenant — so an upsert reads/updates the same row.
			Name:    "inferenceproxy_config_uniq",
			Columns: []string{model.ColTenantID},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:  dlpRuleKind,
		Table: dlpRuleTable,
		Fields: []model.FieldSpec{
			{Name: colClass, Kind: model.KindText, Indexed: true},
			{Name: colAction, Kind: model.KindText},
			{Name: colNote, Kind: model.KindText, Nullable: true},
			{Name: colCreatedBy, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			// One rule per (tenant, class). Leads with tenant_id.
			Name:    "inferenceproxy_dlp_rule_uniq",
			Columns: []string{model.ColTenantID, colClass},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind:  deviceGrantKind,
		Table: deviceGrantTable,
		Fields: []model.FieldSpec{
			{Name: colDeviceCode, Kind: model.KindText, Indexed: true},
			{Name: colUserCode, Kind: model.KindText, Indexed: true},
			{Name: colGrantState, Kind: model.KindText, Indexed: true},
			{Name: colGrantExpires, Kind: model.KindTimestamp},
			{Name: colApprovedBy, Kind: model.KindText, Nullable: true},
			{Name: colGrantTenant, Kind: model.KindText, Nullable: true},
			{Name: colLastPollAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{
			{
				Name:    "inferenceproxy_device_grant_device_uniq",
				Columns: []string{model.ColTenantID, colDeviceCode},
				Unique:  true,
			},
			{
				Name:    "inferenceproxy_device_grant_user_uniq",
				Columns: []string{model.ColTenantID, colUserCode},
				Unique:  true,
			},
		},
	})
}
