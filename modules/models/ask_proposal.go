// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// A write Ask proposes waits on an ordinary approval (design D2). The approval names the
// action ask.<tool>, its subject is the digest of the exact action, and governance records
// Ask as its origin from the principal that opened it. Only a person decides it, through the
// approvals route; Ask cannot. Once approved, the person runs it here, exactly once.
const (
	askApprovalsPath = "/v1/m/governance/approvals"
	askSubjectKind   = "ask_action"
	askActionPrefix  = "ask."
)

type askRunRequest struct {
	Tool string                     `json:"tool"`
	Args map[string]json.RawMessage `json:"args"`
}

type askRunResponse struct {
	Status     int    `json:"status"`
	Command    string `json:"command"`
	ApprovalID string `json:"approval_id"`
	Output     string `json:"output"`
}

// handleAskRunProposal runs the action Ask proposed, once, after a person approved it. The
// tool and arguments are sent again and must be exactly the ones the approval is bound to;
// the approval is consumed once, so a second run, or any changed argument, is refused and
// nothing runs.
func (m *Module) handleAskRunProposal(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.ask.ready() {
		api.WriteJSON(w, http.StatusServiceUnavailable, api.ModuleErrorBody("Ask Olivares is not available on this engine"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in askRunRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	tool := askToolNamed(in.Tool)
	if tool == nil || tool.Kind != askKindWrite {
		writeJSON(w, http.StatusBadRequest, errorBody("tool must be one of Ask's write tools"))
		return
	}
	args, reason := askCheckArgs(tool, in.Args)
	if reason != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(reason))
		return
	}
	ctx := r.Context()
	body, command := tool.build(args)
	refuse := func(status int, reason string) {
		_ = m.askRecord(ctx, mc, "ask.tool.refused", id, "", map[string]any{"outcome": "refused", "tool": tool.Name, "reason": reason, "approval": id})
		writeJSON(w, status, errorBody(reason))
	}
	ceiling := []auth.Permission{askPermApprovalsRead, askPermApprovalsWrite, tool.Permission}
	approvalPath := askApprovalsPath + "/" + url.PathEscape(id)

	status, raw, err := m.askServe(r, mc, ceiling, http.MethodGet, approvalPath, nil)
	if err != nil || status != http.StatusOK {
		refuse(max(status, http.StatusForbidden), "the approval cannot be read")
		return
	}
	var approval struct {
		Status      string `json:"status"`
		Origin      string `json:"origin"`
		Action      string `json:"action"`
		SubjectKind string `json:"subject_kind"`
		SubjectRef  string `json:"subject_ref"`
		RequestedBy string `json:"requested_by"`
	}
	if json.Unmarshal(raw, &approval) != nil {
		refuse(http.StatusBadGateway, "the approval cannot be read")
		return
	}
	if approval.Origin != "ask" || approval.RequestedBy != mc.Principal.Actor() ||
		approval.Action != askActionPrefix+tool.Name || approval.SubjectKind != askSubjectKind ||
		approval.SubjectRef != askActionDigest(mc.Tenant, mc.Principal.UserID, tool.Name, args) {
		refuse(http.StatusConflict, "this is not the action that was approved")
		return
	}
	if approval.Status != "approved" {
		refuse(http.StatusConflict, "the approval is "+approval.Status+", not approved")
		return
	}
	// Ask turned off after the approval stops it here: the approval is not spent and nothing runs.
	if _, on := askOn(w, ctx, mc); !on {
		return
	}
	// Deny closed: a write that cannot be recorded does not run.
	if !m.askRecorded(w, ctx, mc, "ask.tool.call", id, "", map[string]any{
		"outcome": askOutcomeCall, "tool": tool.Name, "permission": string(tool.Permission), "approval": id,
	}) {
		return
	}
	consumer, _ := json.Marshal(map[string]string{"consumer_id": askConsumerID()})
	status, raw, err = m.askServe(r, mc, ceiling, http.MethodPost, approvalPath+"/consume", consumer)
	var spent struct {
		Granted bool `json:"granted"`
		Replay  bool `json:"replay"`
	}
	if err != nil || status != http.StatusOK || json.Unmarshal(raw, &spent) != nil || !spent.Granted || spent.Replay {
		refuse(http.StatusConflict, "the approval was already used or is no longer valid")
		return
	}
	status, raw, err = m.askServe(r, mc, []auth.Permission{tool.Permission}, tool.Method, tool.Path, body)
	if err != nil {
		writeJSON(w, http.StatusForbidden, errorBody("the action could not run as you"))
		return
	}
	writeJSON(w, http.StatusOK, askRunResponse{Status: status, Command: command, ApprovalID: id, Output: askResultText(status, raw, nil)})
}

// askConsumerID names one run attempt. Each attempt is a new consumer, so the approval's
// single-use spend refuses a second one.
func askConsumerID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "ask-" + hex.EncodeToString(b[:])
}
