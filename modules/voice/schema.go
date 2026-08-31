// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables (all within the 40-char cap).
const (
	sessionKind   model.Kind = "voice.session"
	sessionTable             = "voice_session"
	policyKind    model.Kind = "voice.policy"
	policyTable              = "voice_policy"
	decisionKind  model.Kind = "voice.decision"
	decisionTable            = "voice_decision"
)

// session columns — one voice/realtime session's METADATA. MUTABLE upsert. There is
// NO content column (audio/transcript text) and NO stored "state" column: state is
// derived at read time from last_event_at recency.
const (
	colSessionRef    = "session_ref"
	colAgentRef      = "agent_ref"
	colModelRef      = "model_ref"
	colProviderRef   = "provider_ref"
	colPrincipalRef  = "principal_ref" // the real opener (from the governed open)
	colPolicyRef     = "policy_ref"
	colLanguageCode  = "language_code" // BCP-47
	colUserTurns     = "user_turns"
	colAgentTurns    = "agent_turns"
	colDurationMS    = "duration_ms"
	colLatencyCount  = "latency_count"
	colLatencySumMS  = "latency_sum_ms"
	colLatencyMaxMS  = "latency_max_ms"
	colGoverned      = "governed" // was the open
	colFirstEventAt  = "first_event_at"
	colLastEventAt   = "last_event_at"
	colClosedReason  = "closed_reason"
	colTranscriptRef = "transcript_ref_hash" // hashHex of an EXTERNAL locator — NEVER transcript text
	colTransport     = "transport"           // "sip" for OpenAI Realtime calls
	colCallRef       = "call_ref"            // provider call id, never a SIP address
	colFromRedacted  = "from_redacted"       // RedactSIPAddress(raw From)
	colToRedacted    = "to_redacted"         // RedactSIPAddress(raw To)
)

// policy columns — the governance declaration: WHO may open WITH WHICH model/provider.
// Default (no matching row) = DENY.
const (
	colPolAgentRef   = "agent_ref"            // "*" wildcard or a specific agent
	colAllowedModel  = "allowed_model_ref"    // id or "*"
	colAllowedProvi  = "allowed_provider_ref" // id or "*"
	colMaxSessionMin = "max_session_minutes"  // optional governance bound
	colMaxLatencyMS  = "max_latency_ms"       // optional SLA bound for the latency-degraded finding
	colCallsJSON     = "calls_json"           // optional call-policy block, JSON; no SIP addresses/secrets
	colPolicySetBy   = "set_by"               // audit-actor string (provenance)
)

// decision columns — the APPEND-ONLY open/close governance-evidence ledger
// (deploy_operation shape).
const (
	colDecSessionRef = "session_ref"
	colDecAgentRef   = "agent_ref"
	colReqModelRef   = "requested_model_ref"
	colReqProviRef   = "requested_provider_ref"
	colDecPolicyRef  = "policy_ref"
	colOp            = "op"             // "open_request" | "open" | "close"
	colPolicyVerdict = "policy_verdict" // "allowed" | "denied" | "no_policy"
	colPlanHash      = "plan_hash"
	colApprovalRef   = "approval_ref"
	colGateStatus    = "gate_status"
	colOpStatus      = "op_status" // requested | blocked | dispatched | declared_not_opened | failed
	colDispatchRef   = "dispatch_ref"
	colActor         = "actor" // REAL principal — never the system actor
	colActorKind     = "actor_kind"
	colDetailHash    = "detail_hash"
	colResult        = "result"
	colOccurredAt    = "occurred_at"
)

// RegisterSchema declares the module's three owned entities. Every UNIQUE index
// leads model.ColTenantID. The decision ledger is APPEND-ONLY (docs/SECURITY-HARDENING.md). No
// column can hold audio, transcript text, prompt/response content or a secret
// (docs/SECURITY-HARDENING.md) — the hard minimal-data line of a voice plane.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  sessionKind,
		Table: sessionTable,
		Fields: []model.FieldSpec{
			{Name: colSessionRef, Kind: model.KindText},
			{Name: colAgentRef, Kind: model.KindText, Indexed: true},
			{Name: colModelRef, Kind: model.KindText, Nullable: true},
			{Name: colProviderRef, Kind: model.KindText, Nullable: true},
			{Name: colPrincipalRef, Kind: model.KindText, Nullable: true},
			{Name: colPolicyRef, Kind: model.KindText, Nullable: true},
			{Name: colLanguageCode, Kind: model.KindText, Nullable: true},
			{Name: colUserTurns, Kind: model.KindInt},
			{Name: colAgentTurns, Kind: model.KindInt},
			{Name: colDurationMS, Kind: model.KindInt},
			{Name: colLatencyCount, Kind: model.KindInt},
			{Name: colLatencySumMS, Kind: model.KindInt},
			{Name: colLatencyMaxMS, Kind: model.KindInt},
			{Name: colGoverned, Kind: model.KindBool},
			{Name: colFirstEventAt, Kind: model.KindTimestamp},
			{Name: colLastEventAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colClosedReason, Kind: model.KindText, Nullable: true},
			{Name: colTranscriptRef, Kind: model.KindText, Nullable: true},
			{Name: colTransport, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colCallRef, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colFromRedacted, Kind: model.KindText, Nullable: true},
			{Name: colToRedacted, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "voice_session_uniq",
			Columns: []string{model.ColTenantID, colSessionRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  policyKind,
		Table: policyTable,
		Fields: []model.FieldSpec{
			{Name: colPolAgentRef, Kind: model.KindText, Indexed: true},
			{Name: colAllowedModel, Kind: model.KindText},
			{Name: colAllowedProvi, Kind: model.KindText},
			{Name: colMaxSessionMin, Kind: model.KindInt, Nullable: true},
			{Name: colMaxLatencyMS, Kind: model.KindInt, Nullable: true},
			{Name: colCallsJSON, Kind: model.KindText, Nullable: true},
			{Name: colPolicySetBy, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			Name:    "voice_policy_uniq",
			Columns: []string{model.ColTenantID, colPolAgentRef, colAllowedModel, colAllowedProvi},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:       decisionKind,
		Table:      decisionTable,
		AppendOnly: true, // immutable open/close governance evidence (docs/SECURITY-HARDENING.md)
		Fields: []model.FieldSpec{
			{Name: colDecSessionRef, Kind: model.KindText, Indexed: true},
			{Name: colDecAgentRef, Kind: model.KindText, Indexed: true},
			{Name: colReqModelRef, Kind: model.KindText},
			{Name: colReqProviRef, Kind: model.KindText},
			{Name: colDecPolicyRef, Kind: model.KindText, Nullable: true},
			{Name: colOp, Kind: model.KindText, Indexed: true},
			{Name: colPolicyVerdict, Kind: model.KindText},
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
	})
}
