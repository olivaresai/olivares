// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"encoding/json"

	"github.com/olivaresai/olivares/core/model"
)

// sessionDTO projects a voice session's METADATA with its read-time-derived state.
// There is no audio/transcript-text field by construction; transcript_ref_hash is a
// one-way hash of an external locator that proves a transcript exists without holding it.
type sessionDTO struct {
	ID                string `json:"id"`
	SessionRef        string `json:"session_ref"`
	AgentRef          string `json:"agent_ref"`
	ModelRef          string `json:"model_ref,omitempty"`
	ProviderRef       string `json:"provider_ref,omitempty"`
	PrincipalRef      string `json:"principal_ref,omitempty"`
	PolicyRef         string `json:"policy_ref,omitempty"`
	LanguageCode      string `json:"language_code,omitempty"`
	State             string `json:"state"` // derived: live | idle | ended
	UserTurns         int64  `json:"user_turns"`
	AgentTurns        int64  `json:"agent_turns"`
	TurnCount         int64  `json:"turn_count"`
	DurationMS        int64  `json:"duration_ms"`
	LatencyAvgMS      int64  `json:"latency_avg_ms"`
	LatencyMaxMS      int64  `json:"latency_max_ms"`
	Governed          bool   `json:"governed"`
	FirstEventAt      string `json:"first_event_at,omitempty"`
	LastEventAt       string `json:"last_event_at,omitempty"`
	ClosedReason      string `json:"closed_reason,omitempty"`
	TranscriptRefHash string `json:"transcript_ref_hash,omitempty"`
	Transport         string `json:"transport,omitempty"`
	CallRef           string `json:"call_ref,omitempty"`
	FromRedacted      string `json:"from_redacted,omitempty"`
	ToRedacted        string `json:"to_redacted,omitempty"`
	CreatedAt         string `json:"created_at"`
}

// toSessionDTO projects a session record, deriving live/idle/ended from last-event
// recency and the mean latency from the running sum/count.
func (m *Module) toSessionDTO(rec model.Record) sessionDTO {
	user, agent := rec.Int(colUserTurns), rec.Int(colAgentTurns)
	avg := int64(0)
	if c := rec.Int(colLatencyCount); c > 0 {
		avg = rec.Int(colLatencySumMS) / c
	}
	return sessionDTO{
		ID:                rec.String(model.ColID),
		SessionRef:        rec.String(colSessionRef),
		AgentRef:          rec.String(colAgentRef),
		ModelRef:          rec.String(colModelRef),
		ProviderRef:       rec.String(colProviderRef),
		PrincipalRef:      rec.String(colPrincipalRef),
		PolicyRef:         rec.String(colPolicyRef),
		LanguageCode:      rec.String(colLanguageCode),
		State:             deriveState(rec.String(colLastEventAt), m.clock, m.activeWindow, m.idleWindow),
		UserTurns:         user,
		AgentTurns:        agent,
		TurnCount:         user + agent,
		DurationMS:        rec.Int(colDurationMS),
		LatencyAvgMS:      avg,
		LatencyMaxMS:      rec.Int(colLatencyMaxMS),
		Governed:          rec.Bool(colGoverned),
		FirstEventAt:      rec.String(colFirstEventAt),
		LastEventAt:       rec.String(colLastEventAt),
		ClosedReason:      rec.String(colClosedReason),
		TranscriptRefHash: rec.String(colTranscriptRef),
		Transport:         rec.String(colTransport),
		CallRef:           rec.String(colCallRef),
		FromRedacted:      rec.String(colFromRedacted),
		ToRedacted:        rec.String(colToRedacted),
		CreatedAt:         rec.String(model.ColCreatedAt),
	}
}

type callRecordingPolicyDTO struct {
	Active      bool `json:"active"`
	DTMFMasking bool `json:"dtmf_masking"`
	PauseResume bool `json:"pause_resume"`
}

type callPolicyDTO struct {
	Enabled               bool                   `json:"enabled"`
	ToPatterns            []string               `json:"to_patterns,omitempty"`
	FromPatterns          []string               `json:"from_patterns,omitempty"`
	Model                 string                 `json:"model,omitempty"`
	GuardrailInstructions string                 `json:"guardrail_instructions,omitempty"`
	Recording             callRecordingPolicyDTO `json:"recording,omitempty"`
}

// policyDTO projects a voice-open governance policy.
type policyDTO struct {
	ID                 string         `json:"id"`
	AgentRef           string         `json:"agent_ref"`
	AllowedModelRef    string         `json:"allowed_model_ref"`
	AllowedProviderRef string         `json:"allowed_provider_ref"`
	MaxSessionMinutes  int64          `json:"max_session_minutes,omitempty"`
	MaxLatencyMS       int64          `json:"max_latency_ms,omitempty"`
	Calls              *callPolicyDTO `json:"calls,omitempty"`
	SetBy              string         `json:"set_by"`
	UpdatedAt          string         `json:"updated_at"`
}

func toPolicyDTO(rec model.Record) policyDTO {
	return policyDTO{
		ID:                 rec.String(model.ColID),
		AgentRef:           rec.String(colPolAgentRef),
		AllowedModelRef:    rec.String(colAllowedModel),
		AllowedProviderRef: rec.String(colAllowedProvi),
		MaxSessionMinutes:  rec.Int(colMaxSessionMin),
		MaxLatencyMS:       rec.Int(colMaxLatencyMS),
		Calls:              parseCallPolicyDTO(rec.String(colCallsJSON)),
		SetBy:              rec.String(colPolicySetBy),
		UpdatedAt:          rec.String(model.ColUpdatedAt),
	}
}

func parseCallPolicyDTO(raw string) *callPolicyDTO {
	if raw == "" {
		return nil
	}
	var out callPolicyDTO
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	out.ToPatterns = append([]string(nil), out.ToPatterns...)
	out.FromPatterns = append([]string(nil), out.FromPatterns...)
	return &out
}

// decisionDTO projects one append-only open/close governance-evidence row.
type decisionDTO struct {
	ID                   string `json:"id"`
	SessionRef           string `json:"session_ref"`
	AgentRef             string `json:"agent_ref"`
	RequestedModelRef    string `json:"requested_model_ref"`
	RequestedProviderRef string `json:"requested_provider_ref"`
	PolicyRef            string `json:"policy_ref,omitempty"`
	Op                   string `json:"op"`
	PolicyVerdict        string `json:"policy_verdict"`
	PlanHash             string `json:"plan_hash,omitempty"`
	ApprovalRef          string `json:"approval_ref,omitempty"`
	GateStatus           string `json:"gate_status"`
	OpStatus             string `json:"op_status"`
	DispatchRef          string `json:"dispatch_ref,omitempty"`
	Actor                string `json:"actor"`
	ActorKind            string `json:"actor_kind"`
	Result               string `json:"result,omitempty"`
	OccurredAt           string `json:"occurred_at"`
}

func toDecisionDTO(rec model.Record) decisionDTO {
	return decisionDTO{
		ID:                   rec.String(model.ColID),
		SessionRef:           rec.String(colDecSessionRef),
		AgentRef:             rec.String(colDecAgentRef),
		RequestedModelRef:    rec.String(colReqModelRef),
		RequestedProviderRef: rec.String(colReqProviRef),
		PolicyRef:            rec.String(colDecPolicyRef),
		Op:                   rec.String(colOp),
		PolicyVerdict:        rec.String(colPolicyVerdict),
		PlanHash:             rec.String(colPlanHash),
		ApprovalRef:          rec.String(colApprovalRef),
		GateStatus:           rec.String(colGateStatus),
		OpStatus:             rec.String(colOpStatus),
		DispatchRef:          rec.String(colDispatchRef),
		Actor:                rec.String(colActor),
		ActorKind:            rec.String(colActorKind),
		Result:               rec.String(colResult),
		OccurredAt:           rec.String(colOccurredAt),
	}
}
