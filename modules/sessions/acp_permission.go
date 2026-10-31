// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"strings"
)

// session/request_permission, shared by the ACP drivers. Observed OpenCode v1.18.30 options:
//
//	once / allow_once, always / allow_always, reject / reject_once.
//
// allow_always maps to directory-scoped permission.reply persistence, which is
// not proved no broader than Olivares SessionScope. Never select it, including
// when SessionScope is true.

type acpToolCall struct {
	ToolCallID string `json:"toolCallId"`
	Kind       string `json:"kind"`
	facts      codexApprovalFacts
	// searchesSessionFolder is set for OpenCode's glob or grep given no folder, which
	// searches the session folder (openCodeFolderSearch, #3058).
	searchesSessionFolder bool
}

type acpRequestPermissionParams struct {
	SessionID string                `json:"sessionId"`
	ToolCall  acpToolCall           `json:"toolCall"`
	Options   []acpPermissionOption `json:"options"`
}

func acpKnownPermissionKind(kind string) bool {
	switch kind {
	case acpPermissionAllowOnce, acpPermissionAllowAlways, acpPermissionRejectOnce:
		return true
	default:
		return false
	}
}

func acpSelectGrant(options []acpPermissionOption, dec ProviderApprovalDecision) (string, bool) {
	granted := make(map[string]bool, len(dec.Granted))
	for _, id := range dec.Granted {
		granted[id] = true
	}
	for _, opt := range options {
		if !granted[opt.OptionID] {
			continue
		}
		if opt.Kind == acpPermissionAllowOnce {
			return opt.OptionID, true
		}
	}
	return "", false
}

func (s *acpSession) onServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	if method == acpReqWriteTextFile && s.cfg.files != nil {
		go s.writeTextFile(id, params)
		return
	}
	if method != acpReqRequestPermission {
		go func() {
			_ = s.conn.respondError(context.Background(), id, acpErrMethodNotSupported,
				"this client does not implement "+method)
		}()
		return
	}
	req := &acpServerRequest{id: id, method: method}
	key := string(id)
	// OpenCode names a call's file only in its tool_call_update (observeToolCall).
	// Take that update's facts here, in the pump's order, before a later update or
	// turn can replace them.
	var call struct {
		ToolCall struct {
			ToolCallID string `json:"toolCallId"`
		} `json:"toolCall"`
	}
	if s.driver == providerDriverOpenCode {
		_ = json.Unmarshal(params, &call) // an unreadable request is refused below
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.pending[key] = req
	session, turn := s.sessionID, s.promptID
	cancellingAtArrival := turn != "" && s.cancelledTurn == turn
	recorded, hasRecorded := s.toolFacts[call.ToolCall.ToolCallID]
	s.mu.Unlock()
	go s.resolveServerRequest(req, key, params, session, turn, cancellingAtArrival, recorded, hasRecorded)
}

func (s *acpSession) resolveServerRequest(req *acpServerRequest, key string, raw json.RawMessage, session, turn string, cancellingAtArrival bool, recorded acpToolCall, hasRecorded bool) {
	defer s.forgetServerRequest(key)

	var params acpRequestPermissionParams
	if err := json.Unmarshal(raw, &params); err != nil {
		s.warn("sessions: a provider permission request could not be read and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, acpCancelledOutcome())
		return
	}
	if why, ok := acpValidPermissionRequest(params.SessionID, params.Options, acpKnownPermissionKind); !ok {
		s.warn("sessions: a provider permission request was malformed and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method, "why", why)
		s.answer(req, acpCancelledOutcome())
		return
	}
	if session == "" || params.SessionID != session {
		s.warn("sessions: a provider permission request named a foreign conversation and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, acpCancelledOutcome())
		return
	}
	if turn == "" {
		s.warn("sessions: a provider permission request arrived with no active turn and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, acpCancelledOutcome())
		return
	}
	if cancellingAtArrival {
		s.warn("sessions: a provider permission arrived on a turn that was already being cancelled; refusing it without consulting the authority",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.refuse(req, params.Options)
		return
	}
	if s.driver == providerDriverOpenCode && !params.ToolCall.facts.Complete &&
		strings.TrimSpace(params.ToolCall.facts.CommandLine) == "" && len(params.ToolCall.facts.FilePaths) == 0 {
		// Only from an update of the same kind of tool call.
		if hasRecorded && recorded.ToolCallID == params.ToolCall.ToolCallID && recorded.Kind == params.ToolCall.Kind {
			params.ToolCall.facts = recorded.facts
		}
		// A glob or grep given no path searches the session folder (measured on 1.19.4):
		// that is the folder it reads (#3058).
		if params.ToolCall.searchesSessionFolder && len(params.ToolCall.facts.FilePaths) == 0 &&
			strings.TrimSpace(params.ToolCall.facts.CommandLine) == "" {
			params.ToolCall.facts = codexApprovalFacts{FilePaths: []string{s.workDir()}, Complete: true}
		}
	}
	if acpRefusesWithoutFacts(s.driver, params.ToolCall, s.cfg.ownMCPOnly) {
		s.warn("sessions: a provider permission carried no command or path for the policy to review and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method, "tool_kind", params.ToolCall.Kind,
			"tool", clipCause(strings.SplitN(params.ToolCall.ToolCallID, "__", 2)[0]), "session_mcp_configured", len(s.cfg.sessionMCP) > 0, "own_mcp_only", s.cfg.ownMCPOnly)
		s.refuse(req, params.Options)
		return
	}
	if req.answered.Load() {
		s.warn("sessions: a provider permission was already answered by a cancellation; not consulting the authority for it",
			"run_ref", s.cfg.RunRef, "method", req.method)
		return
	}
	deadline := s.cfg.ApprovalDeadline
	if deadline <= 0 {
		deadline = defaultDriverApprovalDeadline
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	offered := acpOfferedOptionIDs(params.Options)
	dec := ProviderApprovalDecision{}
	if s.cfg.Approve != nil {
		got, err := s.cfg.Approve(ctx, ProviderApprovalRequest{
			Driver: s.driver, RunRef: s.cfg.RunRef, ProfileRef: s.cfg.ProfileRef,
			ConversationID: session, TurnID: turn,
			Method: req.method, Kind: acpKindToolCallPermission, ToolKind: clipToolKind(params.ToolCall.Kind),
			Requested:   offered,
			CommandLine: params.ToolCall.facts.CommandLine, FilePaths: params.ToolCall.facts.FilePaths,
			FactsComplete:        params.ToolCall.facts.Complete,
			EffectiveCommandLine: params.ToolCall.facts.EffectiveCommandLine,
			EffectiveFilePaths:   params.ToolCall.facts.EffectiveFilePaths,
		})
		if err != nil {
			s.warn("sessions: the provider approval authority failed; refusing deny-closed",
				"run_ref", s.cfg.RunRef, "method", req.method, "cause", errorCause(err, "unavailable"))
			s.refuse(req, params.Options)
			return
		}
		dec = got
	}
	if ctx.Err() != nil {
		s.refuse(req, params.Options)
		return
	}
	if !dec.Allow {
		s.refuse(req, params.Options)
		return
	}
	if s.turnCancelled(turn) {
		s.warn("sessions: a provider permission's turn was cancelled while the authority decided; refusing it",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.refuse(req, params.Options)
		return
	}
	if turn != s.ActiveTurn() {
		s.warn("sessions: a provider permission was authorized after its turn ended; refusing it",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, acpCancelledOutcome())
		return
	}
	if s.cfg.AuthorityCheck != nil {
		if err := s.cfg.AuthorityCheck(ctx); err != nil {
			s.warn("sessions: a provider permission lost its launch authority before the grant; refusing it",
				"run_ref", s.cfg.RunRef, "method", req.method)
			s.answer(req, acpCancelledOutcome())
			return
		}
	}
	optionID, ok := acpSelectGrant(params.Options, dec)
	if !ok {
		s.warn("sessions: an authorized provider permission named no selectable offered option; refusing it",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.refuse(req, params.Options)
		return
	}
	s.answer(req, acpSelectedOutcome(optionID))
}

// acpRefusesWithoutFacts reports whether a driver's permission request must be
// refused for lack of facts. Cursor CLI is held to the same rule: its permission request was
// never measured, so a request that names nothing the policy can review is refused. Gemini CLI
// names a command only in a title and a
// file only in its locations, and a title is a description, not a fact. A request
// that is not a plain read, search or thought, and gave the live policy nothing
// to review, would be allowed blind by a preset that allows, and shown to a person
// as option ids only.
//
// One request is the exception, and only when the launch proved the session MCP
// server is the only MCP server the CLI can start (DriverSessionConfig.ownMCPOnly):
// a call to a tool of that server. It carries no command or path either, but the
// CLI registers the tool as mcp_<server>_<tool> and builds the call id from that
// name, so with one server the id's prefix names it. The request's title is not
// read: for an MCP tool the CLI builds it from the call's own arguments, which the
// model controls. Such a call goes to the authority like any other tool request,
// and the call itself reaches the session MCP edge under the session's policy.
func acpRefusesWithoutFacts(driver string, call acpToolCall, ownMCPOnly bool) bool {
	if (driver != providerDriverGemini && driver != providerDriverCursor) || call.facts.Complete {
		return false
	}
	if driver == providerDriverCursor {
		// Cursor's request shape was never measured: not even a plain read, search or thought
		// is allowed without something the policy can review.
		return true
	}
	switch call.Kind {
	case "read", "search", "think":
		return false
	}
	return !(driver == providerDriverGemini && ownMCPOnly && geminiSessionMCPCall(call))
}

// geminiSessionMCPCall reports whether call is a call to a tool of the server named
// olivares. Gemini builds every MCP tool as kind "other". Its name is qualified
// with the server's, and a built-in tool's never starts so. The prefix proves the
// server only when no other server exists (see launchOwnMCPOnly): the CLI turns
// every character outside [a-zA-Z0-9_.:-] in a server name into "_", so a server
// named olivares/x registers as olivares_x and a text test cannot tell it from ours.
func geminiSessionMCPCall(call acpToolCall) bool {
	return call.Kind == "other" && strings.HasPrefix(call.ToolCallID, "mcp_"+sessionMCPServerName+"_")
}

// clipToolKind bounds the agent's tool kind; ACP's kinds are short words.
func clipToolKind(kind string) string {
	if len(kind) > 32 {
		return "other"
	}
	return kind
}
