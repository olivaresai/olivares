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

// Principal declarations of the columns above. A declaration shared by several
// columns cites lines that hold for every column using it.
var (
	// pdeclActorRef is the caller principal's audit actor string ("user:<id>" or
	// "token:<id>", core/auth/principal.go:219-227) or the engine's "system"
	// (calls.go:383); it records who acted and is only rendered (dto.go:58,
	// dto.go:118, dto.go:172).
	pdeclActorRef        = model.Ref(model.EncodeUserRef, model.ClassEvidence)
	pdeclNoneActorKind   = model.None("the caller principal's actor kind or the engine's system kind, a closed set: core/auth/principal.go:243-251, calls.go:190")
	pdeclNoneSessionRef  = model.None("the voice session's own key, a telemetry session id or a provider call id: sessions.go:163, policies.go:541, calls.go:374")
	pdeclNoneAgentRef    = model.None("an agent reference (or a policy's \"*\" wildcard), compared between policy and request and gated as an agent subject: policies.go:111, policies.go:322")
	pdeclNoneModelRef    = model.None("a model reference (or a policy's \"*\" wildcard), compared between policy and request only: policies.go:116-118, calls.go:381")
	pdeclNoneProviderRef = model.None("a provider reference (or a policy's \"*\" wildcard), compared between policy and request only: policies.go:117-118, calls.go:382")
	pdeclNonePolicyRef   = model.None("the id of the matched voice policy row: policies.go:290, calls.go:229")
	pdeclNoneSIPMasked   = model.None("an external caller's or callee's SIP address reduced to scheme, host and last four digits, only rendered: connectors/voice/openai_calls.go:316-318, calls.go:98-99, dto.go:75-76")
	pdeclNoneCallPattern = model.None("a phone-number pattern matched digit-wise against an inbound call's SIP To/From: calls.go:222-225, calls.go:244")
	// pdeclCallPolicy is the call-policy block the policy writer marshals from
	// callPolicyDTO (policies.go:221-236); no leaf names an account.
	pdeclCallPolicy = model.Nested(callPolicyDTO{}, model.ClassEvidence,
		model.Leaf("to_patterns[]", pdeclNoneCallPattern),
		model.Leaf("from_patterns[]", pdeclNoneCallPattern),
		model.Leaf("model", model.None("a provider model name passed to the call accept: calls.go:231, calls.go:127")),
		model.Leaf("guardrail_instructions", model.None("prose instructions forwarded to the provider on accept: calls.go:232, calls.go:128")),
	)
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
			{Name: colSessionRef, Kind: model.KindText, Principal: pdeclNoneSessionRef},
			{Name: colAgentRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneAgentRef},
			{Name: colModelRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneModelRef},
			{Name: colProviderRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneProviderRef},
			{Name: colPrincipalRef, Kind: model.KindText, Nullable: true, Principal: pdeclActorRef},
			{Name: colPolicyRef, Kind: model.KindText, Nullable: true, Principal: pdeclNonePolicyRef},
			{Name: colLanguageCode, Kind: model.KindText, Nullable: true, Principal: model.None("a BCP-47 language tag reported by telemetry, only rendered: events.go:33, sessions.go:126, dto.go:60")},
			{Name: colUserTurns, Kind: model.KindInt},
			{Name: colAgentTurns, Kind: model.KindInt},
			{Name: colDurationMS, Kind: model.KindInt},
			{Name: colLatencyCount, Kind: model.KindInt},
			{Name: colLatencySumMS, Kind: model.KindInt},
			{Name: colLatencyMaxMS, Kind: model.KindInt},
			{Name: colGoverned, Kind: model.KindBool},
			{Name: colFirstEventAt, Kind: model.KindTimestamp},
			{Name: colLastEventAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colClosedReason, Kind: model.KindText, Nullable: true, Principal: model.None("a close-reason label reported by telemetry, only rendered: sessions.go:127, dto.go:71")},
			{Name: colTranscriptRef, Kind: model.KindText, Nullable: true, Principal: model.None("a SHA-256 digest of an external transcript locator: sessions.go:129, helpers.go:182")},
			{Name: colTransport, Kind: model.KindText, Nullable: true, Indexed: true, Principal: model.None("a transport label, only ever the constant \"sip\": calls.go:23, calls.go:386")},
			{Name: colCallRef, Kind: model.KindText, Nullable: true, Indexed: true, Principal: model.None("a provider call id, only rendered: calls.go:387, dto.go:74")},
			{Name: colFromRedacted, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSIPMasked},
			{Name: colToRedacted, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSIPMasked},
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
			{Name: colPolAgentRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneAgentRef},
			{Name: colAllowedModel, Kind: model.KindText, Principal: pdeclNoneModelRef},
			{Name: colAllowedProvi, Kind: model.KindText, Principal: pdeclNoneProviderRef},
			{Name: colMaxSessionMin, Kind: model.KindInt, Nullable: true},
			{Name: colMaxLatencyMS, Kind: model.KindInt, Nullable: true},
			{Name: colCallsJSON, Kind: model.KindText, Nullable: true, Principal: pdeclCallPolicy},
			{Name: colPolicySetBy, Kind: model.KindText, Principal: pdeclActorRef},
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
			{Name: colDecSessionRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSessionRef},
			{Name: colDecAgentRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneAgentRef},
			{Name: colReqModelRef, Kind: model.KindText, Principal: pdeclNoneModelRef},
			{Name: colReqProviRef, Kind: model.KindText, Principal: pdeclNoneProviderRef},
			{Name: colDecPolicyRef, Kind: model.KindText, Nullable: true, Principal: pdeclNonePolicyRef},
			{Name: colOp, Kind: model.KindText, Indexed: true, Principal: model.None("a ledger operation, a closed set: policies.go:22-26")},
			{Name: colPolicyVerdict, Kind: model.KindText, Principal: model.None("a policy verdict, a closed set: policies.go:43-46")},
			{Name: colPlanHash, Kind: model.KindText, Nullable: true, Indexed: true, Principal: model.None("a SHA-256 plan digest: policies.go:310, helpers.go:182")},
			{Name: colApprovalRef, Kind: model.KindText, Nullable: true, Principal: model.None("an approval id issued by the approval gate and echoed back to it: policies.go:334, policies.go:354")},
			{Name: colGateStatus, Kind: model.KindText, Principal: model.None("an approval-gate status, a closed set: ports.go:26-34")},
			{Name: colOpStatus, Kind: model.KindText, Indexed: true, Principal: model.None("an operation status, a closed set: policies.go:28-40")},
			{Name: colDispatchRef, Kind: model.KindText, Nullable: true, Principal: model.None("the dispatcher's handle for the opened session or the provider call id: policies.go:394, calls.go:353")},
			{Name: colActor, Kind: model.KindText, Principal: pdeclActorRef},
			{Name: colActorKind, Kind: model.KindText, Principal: pdeclNoneActorKind},
			{Name: colDetailHash, Kind: model.KindText, Nullable: true, Principal: model.None("a SHA-256 digest of the outcome detail: policies.go:578, helpers.go:182")},
			{Name: colResult, Kind: model.KindText, Nullable: true, Principal: model.None("a short outcome summary written by the open and call paths, only rendered: policies.go:581, dto.go:174")},
			{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
		},
	})
}
