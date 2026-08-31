// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// listResponse is the paginated envelope every list endpoint returns: the ONE
// engine-wide shape (items + opaque cursor + has_more), aliased rather than
// re-declared so an empty page can never serialize as `{"items":null}` here
// while it serializes as `{"items":[]}` next door (core/api/listresponse.go).
type listResponse[T any] = api.ListResponse[T]

// liveDTO is the live operation of one session: what it is doing now, its
// objective, its live tokens/cost and an activity summary. cc_state (the Claude
// Code state) and duration are derived at read time; goal/summary are modeled
// but only populated when a richer channel provides them (is minimal-data
// and does not carry them — never fabricated).
type liveDTO struct {
	SessionRef      string `json:"session_ref"`
	AgentRef        string `json:"agent_ref,omitempty"`
	CCState         string `json:"cc_state"`
	CurrentAction   string `json:"current_action,omitempty"`
	CurrentResource string `json:"current_resource,omitempty"`
	CurrentMode     string `json:"current_mode,omitempty"`
	ModelRef        string `json:"model_ref,omitempty"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	CostMicroUSD    int64  `json:"cost_micro_usd"`
	EventCount      int64  `json:"event_count"`
	ToolCallCount   int64  `json:"tool_call_count"`
	FirstEventAt    string `json:"first_event_at"`
	LastEventAt     string `json:"last_event_at"`
	DurationSeconds int64  `json:"duration_seconds"`
	Goal            string `json:"goal,omitempty"`
	Summary         string `json:"summary,omitempty"`
	// Unclaimed reports activity seen from a session holding no live claim
	// (SG-02). It is sticky and it is NOT folded into cc_state: silent_evasion
	// means the connector caught a discrepancy, and an unclaimed session is a
	// different fact. Collapsing them would make an operator unable to tell which
	// of the two they are looking at.
	Unclaimed   bool   `json:"unclaimed,omitempty"`
	UnclaimedAt string `json:"unclaimed_at,omitempty"`
	// Engine names the agent engine driving this session ("claude", "codex"), and
	// Posture how firmly it is governed ("enforced" | "observed"). Both are omitted
	// when unknown rather than defaulted: a console that renders a blank badge is
	// telling the truth, and one that renders "enforced" by default is not.
	Engine  string `json:"engine,omitempty"`
	Posture string `json:"posture,omitempty"`
}

// toLiveDTO projects a live record to its DTO, deriving the Claude Code state and
// duration from the record at read time.
func (m *Module) toLiveDTO(rec model.Record) liveDTO {
	return liveDTO{
		SessionRef:      rec.String(colSessionRef),
		AgentRef:        rec.String(colAgentRef),
		CCState:         m.deriveCC(rec),
		Engine:          rec.String(colEngine),
		Posture:         rec.String(colPosture),
		CurrentAction:   rec.String(colCurrentTool),
		CurrentResource: rec.String(colCurrentRes),
		CurrentMode:     rec.String(colCurrentMode),
		ModelRef:        rec.String(colModelRef),
		InputTokens:     rec.Int(colInputTokens),
		OutputTokens:    rec.Int(colOutputTokens),
		CostMicroUSD:    rec.Int(colCostMicroUSD),
		EventCount:      rec.Int(colEventCount),
		ToolCallCount:   rec.Int(colToolCalls),
		FirstEventAt:    rec.String(colFirstEventAt),
		LastEventAt:     rec.String(colLastEventAt),
		DurationSeconds: durationSeconds(rec.String(colFirstEventAt), rec.String(colLastEventAt)),
		Goal:            rec.String(colGoal),
		Summary:         rec.String(colSummary),
		Unclaimed:       rec.String(colUnclaimedAt) != "",
		UnclaimedAt:     rec.String(colUnclaimedAt),
	}
}

// liveSnapshot is a live DTO tagged with its tenant, so the broker delivers it
// only to subscribers authorized for that tenant.
type liveSnapshot struct {
	tenant model.TenantID
	dto    liveDTO
}

// snapshot builds a tenant-tagged snapshot for the stream broker.
func (m *Module) snapshot(rec model.Record, tenant model.TenantID) liveSnapshot {
	return liveSnapshot{tenant: tenant, dto: m.toLiveDTO(rec)}
}

// timelineDTO is one replayable event in a session's history.
type timelineDTO struct {
	At          string `json:"at"`
	Kind        string `json:"kind"`
	ToolRef     string `json:"tool_ref,omitempty"`
	ResourceRef string `json:"resource_ref,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Source      string `json:"source,omitempty"`
	Title       string `json:"title,omitempty"`
}

func toTimelineDTO(rec model.Record) timelineDTO {
	return timelineDTO{
		At:          rec.String(colTLAt),
		Kind:        rec.String(colTLKind),
		ToolRef:     rec.String(colTLToolRef),
		ResourceRef: rec.String(colTLResource),
		Mode:        rec.String(colTLMode),
		Source:      rec.String(colTLSource),
		Title:       rec.String(colTLTitle),
	}
}

// durationSeconds returns last-first in whole seconds (0 if unparseable).
func durationSeconds(first, last string) int64 {
	ft, err := model.ParseTimestamp(first)
	if err != nil {
		return 0
	}
	lt, err := model.ParseTimestamp(last)
	if err != nil {
		return 0
	}
	d := int64(lt.Time().Sub(ft.Time()).Seconds())
	if d < 0 {
		return 0
	}
	return d
}

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// errorBody is the small error envelope module endpoints return.
func errorBody(msg string) map[string]any {
	return map[string]any{"error": map[string]string{"message": msg}}
}
