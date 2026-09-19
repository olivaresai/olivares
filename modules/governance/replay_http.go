// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// reconstructBody is the POST /decisions/replay document. Unknown fields are
// refused by decodeJSON so a caller cannot smuggle a live-policy flag.
type reconstructBody struct {
	At               string `json:"at"`
	Principal        string `json:"principal"`
	Resource         string `json:"resource"`
	ResourceKind     string `json:"resource_kind"`
	SourceInstance   string `json:"source_instance"`
	Action           string `json:"action"`
	ActionVocabulary string `json:"action_vocabulary"`
	DecisionID       string `json:"decision_id"`
}

// handleReplayDecision reconstructs a past authorization from the access-evidence
// ledger, never from the live policy. The body names either a stored decision or a
// principal, action and instant. When a required fact is missing the answer names
// that fact.
func (m *Module) handleReplayDecision(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body reconstructBody
	if !decodeJSON(w, r, &body) {
		return
	}
	req, errMsg := replayRequestFromBody(body)
	if errMsg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(errMsg))
		return
	}
	out, err := m.Reconstruct(r.Context(), mc.Tenant, req)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleReconstructDecision reconstructs one stored authorization decision from the
// access-evidence ledger, never from the live policy. The path id selects the
// recorded row. When a required fact is missing the answer names that fact.
func (m *Module) handleReconstructDecision(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil || id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("decision id is required"))
		return
	}
	out, rerr := m.Reconstruct(r.Context(), mc.Tenant, ReconstructRequest{DecisionID: id})
	if rerr != nil {
		writeStoreError(w, rerr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func replayRequestFromBody(body reconstructBody) (ReconstructRequest, string) {
	req := ReconstructRequest{
		Principal:        strings.TrimSpace(body.Principal),
		Resource:         strings.TrimSpace(body.Resource),
		ResourceKind:     strings.TrimSpace(body.ResourceKind),
		SourceInstance:   strings.TrimSpace(body.SourceInstance),
		Action:           strings.TrimSpace(body.Action),
		ActionVocabulary: strings.TrimSpace(body.ActionVocabulary),
	}
	if id := strings.TrimSpace(body.DecisionID); id != "" {
		parsed, err := model.ParseID(id)
		if err != nil || parsed.IsZero() {
			return ReconstructRequest{}, "decision_id is not a record reference"
		}
		req.DecisionID = parsed
	}
	if at := strings.TrimSpace(body.At); at != "" {
		parsed, err := parseReplayTime(at)
		if err != nil {
			return ReconstructRequest{}, "at must be RFC3339 or the evidence timestamp layout"
		}
		req.At = parsed
	}
	if req.DecisionID.IsZero() {
		if req.At.IsZero() {
			return ReconstructRequest{}, "at is required when decision_id is omitted"
		}
		if req.Principal == "" || req.Action == "" {
			return ReconstructRequest{}, "principal and action are required when decision_id is omitted"
		}
	}
	return req, ""
}

func parseReplayTime(s string) (time.Time, error) {
	if t, err := sdk.ParseEvidenceTime(s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
	}
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
