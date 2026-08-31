// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package claudeadoption

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/store"
)

// listCap / maxScanPages bound a single aggregation scan (mirroring the FinOps caps): a
// window with more than listCap*maxScanPages rows reports Truncated rather than scanning
// unboundedly. Day-bucketed rows keep real windows far under this.
const (
	listCap      = 1000
	maxScanPages = 1000
)

// boundaryDTO is the honest, non-optional Claude-API-only scope, returned with every
// response so the UI can render the boundary note from machine-readable truth.
type boundaryDTO struct {
	ClaudeAPIOnly bool     `json:"claude_api_only"`
	Excludes      []string `json:"excludes"`
}

func boundary() boundaryDTO {
	return boundaryDTO{
		ClaudeAPIOnly: true,
		Excludes:      []string{"claude-platform-aws", "microsoft-foundry", "amazon-bedrock", "vertex-ai"},
	}
}

// adoptionTotals is the headline productivity roll-up over a window for ONE lens.
type adoptionTotals struct {
	Sessions       int64    `json:"sessions"`
	LinesAdded     int64    `json:"lines_added"`
	LinesRemoved   int64    `json:"lines_removed"`
	LinesNet       int64    `json:"lines_net"`
	Commits        int64    `json:"commits"`
	PullRequests   int64    `json:"pull_requests"`
	ActiveTimeMs   int64    `json:"active_time_ms"`
	ToolsAccepted  int64    `json:"tools_accepted"`
	ToolsRejected  int64    `json:"tools_rejected"`
	AcceptanceRate *float64 `json:"acceptance_rate"` // accepted/(accepted+rejected); null when no decisions
	InputTokens    int64    `json:"input_tokens"`
	OutputTokens   int64    `json:"output_tokens"`
	Tokens         int64    `json:"tokens"`
}

type modelMixDTO struct {
	Model  string `json:"model"`
	Tokens int64  `json:"tokens"`
}

type toolBreakdownDTO struct {
	Tool           string   `json:"tool"`
	Accepted       int64    `json:"accepted"`
	Rejected       int64    `json:"rejected"`
	AcceptanceRate *float64 `json:"acceptance_rate"`
}

// lensDTO is one telemetry vantage point. The two lenses describe the SAME Claude Code
// activity from different planes and are NEVER summed: `analytics` is the admin Analytics
// feed (authoritative per-developer/day), `telemetry` is the OTLP plane (per-session,
// real-time, carries active_time + operator team labels).
type lensDTO struct {
	Totals  adoptionTotals     `json:"totals"`
	ByModel []modelMixDTO      `json:"by_model"`
	ByTool  []toolBreakdownDTO `json:"by_tool"`
}

type summaryResponse struct {
	Since      string      `json:"since,omitempty"`
	Until      string      `json:"until,omitempty"`
	Analytics  lensDTO     `json:"analytics"`
	Telemetry  lensDTO     `json:"telemetry"`
	Developers int         `json:"developers"` // distinct developers (analytics lens)
	Teams      int         `json:"teams"`      // distinct teams (telemetry lens)
	Boundary   boundaryDTO `json:"boundary"`
	Truncated  bool        `json:"truncated,omitempty"`
}

type trendDay struct {
	Day    string         `json:"day"`
	Totals adoptionTotals `json:"totals"`
}

type trendResponse struct {
	Lens      string      `json:"lens"`
	Days      []trendDay  `json:"days"`
	Boundary  boundaryDTO `json:"boundary"`
	Truncated bool        `json:"truncated,omitempty"`
}

type teamRow struct {
	Team   string         `json:"team"` // "" rendered as "(unassigned)" by the UI
	Totals adoptionTotals `json:"totals"`
}

type teamsResponse struct {
	Teams     []teamRow   `json:"teams"`
	Boundary  boundaryDTO `json:"boundary"`
	Truncated bool        `json:"truncated,omitempty"`
}

type developerRow struct {
	Developer string         `json:"developer"`
	Totals    adoptionTotals `json:"totals"`
}

type developersResponse struct {
	Developers []developerRow `json:"developers"`
	Boundary   boundaryDTO    `json:"boundary"`
	Truncated  bool           `json:"truncated,omitempty"`
}

type discrepancyMetric struct {
	Name      string  `json:"name"`
	Analytics int64   `json:"analytics"`
	Telemetry int64   `json:"telemetry"`
	Ratio     float64 `json:"ratio"`
	Direction string  `json:"direction"`
	Material  bool    `json:"material"`
}

type discrepancyDay struct {
	Day      string              `json:"day"`
	Metrics  []discrepancyMetric `json:"metrics"`
	Material bool                `json:"material"`
}

type discrepancyThresholds struct {
	Ratio  float64          `json:"ratio"`
	Floors map[string]int64 `json:"floors"`
}

type discrepancyResponse struct {
	Since      string                `json:"since,omitempty"`
	Until      string                `json:"until,omitempty"`
	Days       []discrepancyDay      `json:"days"`
	Thresholds discrepancyThresholds `json:"thresholds"`
	Boundary   boundaryDTO           `json:"boundary"`
	Truncated  bool                  `json:"truncated,omitempty"`
}

// --- http helpers (the module-local subset of the FinOps DTO helpers) --------

func timeParam(r *http.Request, key string) (t time.Time, ok bool, bad bool) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return time.Time{}, false, false
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false, true
	}
	return t, true, false
}

func timeWindow(r *http.Request) (since time.Time, hasSince bool, until time.Time, hasUntil bool, bad bool) {
	since, hasSince, badSince := timeParam(r, "since")
	until, hasUntil, badUntil := timeParam(r, "until")
	return since, hasSince, until, hasUntil, badSince || badUntil
}

func errorBody(msg string) map[string]any {
	return map[string]any{"error": map[string]string{"message": msg}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// writeStoreError maps a store error to an HTTP status. Everything except this
// module's own unknown-entity answer is api.StoreErrorStatus
// (core/api/moduleerrors.go), the ONE mapping the whole product shares.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, nil)
	case errors.Is(err, store.ErrUnknownEntity):
		// KEPT LOCAL, and it is the sharpest reading of this sentinel in the tree.
		// This module never sorts, so the only way it sees ErrUnknownEntity is
		// sc.Ext() on a kind the deployment has not registered — the adoption read
		// model is not there yet. That is the deployment's state, not the caller's
		// mistake, so 503 and not the shared 400 "invalid query". The three-way
		// divergence this belongs to is named in core/api/moduleerrors.go.
		writeJSON(w, http.StatusServiceUnavailable, errorBody("adoption store not ready"))
	default:
		status, msg, _ := api.StoreErrorStatus(err)
		writeJSON(w, status, errorBody(msg))
	}
}

// limitParam reads an optional positive "limit" (top-N) query param, defaulting to def.
func limitParam(r *http.Request, def int) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
