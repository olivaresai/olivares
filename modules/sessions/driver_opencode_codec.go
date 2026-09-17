// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
)

// OpenCode session/request_permission. Observed v1.18.30 options:
//
//	once / allow_once, always / allow_always, reject / reject_once.
//
// allow_always maps to directory-scoped permission.reply persistence, which is
// not proved no broader than Olivares SessionScope. Never select it, including
// when SessionScope is true.

const (
	openCodePermissionAllowOnce   = "allow_once"
	openCodePermissionAllowAlways = "allow_always"
	openCodePermissionRejectOnce  = "reject_once"
)

const (
	openCodeOutcomeSelected  = "selected"
	openCodeOutcomeCancelled = "cancelled"
)

const openCodeReqRequestPermission = "session/request_permission"

const openCodeKindToolCallPermission = "tool_call_permission"

type openCodePermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type openCodeToolCall struct {
	ToolCallID string `json:"toolCallId"`
	Kind       string `json:"kind"`
}

type openCodeRequestPermissionParams struct {
	SessionID string                     `json:"sessionId"`
	ToolCall  openCodeToolCall           `json:"toolCall"`
	Options   []openCodePermissionOption `json:"options"`
}

type openCodePermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type openCodeRequestPermissionResponse struct {
	Outcome openCodePermissionOutcome `json:"outcome"`
}

func openCodeCancelledOutcome() any {
	return openCodeRequestPermissionResponse{Outcome: openCodePermissionOutcome{Outcome: openCodeOutcomeCancelled}}
}

func openCodeSelectedOutcome(optionID string) any {
	return openCodeRequestPermissionResponse{
		Outcome: openCodePermissionOutcome{Outcome: openCodeOutcomeSelected, OptionID: optionID},
	}
}

func openCodeValidPermissionRequest(p openCodeRequestPermissionParams) (string, bool) {
	if p.SessionID == "" {
		return "the request named no conversation", false
	}
	if len(p.Options) == 0 {
		return "the request offered no options to select", false
	}
	seen := make(map[string]bool, len(p.Options))
	known := false
	for _, opt := range p.Options {
		if opt.OptionID == "" {
			return "the request offered an option with no id", false
		}
		if seen[opt.OptionID] {
			return "the request offered two options with the same id", false
		}
		seen[opt.OptionID] = true
		known = known || openCodeKnownPermissionKind(opt.Kind)
	}
	if !known {
		return "the request offered no option of a kind this client understands", false
	}
	return "", true
}

func openCodeKnownPermissionKind(kind string) bool {
	switch kind {
	case openCodePermissionAllowOnce, openCodePermissionAllowAlways, openCodePermissionRejectOnce:
		return true
	default:
		return false
	}
}

func openCodeOfferedOptionIDs(options []openCodePermissionOption) []string {
	out := make([]string, 0, len(options))
	for _, opt := range options {
		out = append(out, opt.OptionID)
	}
	return out
}

func openCodeSelectGrant(options []openCodePermissionOption, dec ProviderApprovalDecision) (string, bool) {
	granted := make(map[string]bool, len(dec.Granted))
	for _, id := range dec.Granted {
		granted[id] = true
	}
	for _, opt := range options {
		if !granted[opt.OptionID] {
			continue
		}
		if opt.Kind == openCodePermissionAllowOnce {
			return opt.OptionID, true
		}
	}
	return "", false
}

func openCodeSelectRefusal(options []openCodePermissionOption) (string, bool) {
	for _, opt := range options {
		if opt.Kind == openCodePermissionRejectOnce {
			return opt.OptionID, true
		}
	}
	return "", false
}

type openCodeServerRequest struct {
	id       json.RawMessage
	method   string
	once     sync.Once
	answered atomic.Bool
}

func (s *openCodeSession) onServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	if method != openCodeReqRequestPermission {
		go func() {
			_ = s.conn.respondError(context.Background(), id, openCodeErrMethodNotSupported,
				"this client does not implement "+method)
		}()
		return
	}
	req := &openCodeServerRequest{id: id, method: method}
	key := string(id)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.pending[key] = req
	session, turn := s.sessionID, s.promptID
	cancellingAtArrival := turn != "" && s.cancelledTurn == turn
	s.mu.Unlock()
	go s.resolveServerRequest(req, key, params, session, turn, cancellingAtArrival)
}

func (s *openCodeSession) resolveServerRequest(req *openCodeServerRequest, key string, raw json.RawMessage, session, turn string, cancellingAtArrival bool) {
	defer s.forgetServerRequest(key)

	var params openCodeRequestPermissionParams
	if err := json.Unmarshal(raw, &params); err != nil {
		s.warn("sessions: a provider permission request could not be read and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, openCodeCancelledOutcome())
		return
	}
	if why, ok := openCodeValidPermissionRequest(params); !ok {
		s.warn("sessions: a provider permission request was malformed and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method, "why", why)
		s.answer(req, openCodeCancelledOutcome())
		return
	}
	if session == "" || params.SessionID != session {
		s.warn("sessions: a provider permission request named a foreign conversation and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, openCodeCancelledOutcome())
		return
	}
	if turn == "" {
		s.warn("sessions: a provider permission request arrived with no active turn and was refused",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.answer(req, openCodeCancelledOutcome())
		return
	}
	if cancellingAtArrival {
		s.warn("sessions: a provider permission arrived on a turn that was already being cancelled; refusing it without consulting the authority",
			"run_ref", s.cfg.RunRef, "method", req.method)
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

	offered := openCodeOfferedOptionIDs(params.Options)
	dec := ProviderApprovalDecision{}
	if s.cfg.Approve != nil {
		got, err := s.cfg.Approve(ctx, ProviderApprovalRequest{
			Driver: providerDriverOpenCode, RunRef: s.cfg.RunRef, ProfileRef: s.cfg.ProfileRef,
			ConversationID: session, TurnID: turn,
			Method: req.method, Kind: openCodeKindToolCallPermission,
			Requested: offered,
		})
		if err != nil {
			s.warn("sessions: the provider approval authority failed; refusing deny-closed",
				"run_ref", s.cfg.RunRef, "method", req.method)
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
		s.answer(req, openCodeCancelledOutcome())
		return
	}
	if s.cfg.AuthorityCheck != nil {
		if err := s.cfg.AuthorityCheck(ctx); err != nil {
			s.warn("sessions: a provider permission lost its launch authority before the grant; refusing it",
				"run_ref", s.cfg.RunRef, "method", req.method)
			s.answer(req, openCodeCancelledOutcome())
			return
		}
	}
	optionID, ok := openCodeSelectGrant(params.Options, dec)
	if !ok {
		s.warn("sessions: an authorized provider permission named no selectable offered option; refusing it",
			"run_ref", s.cfg.RunRef, "method", req.method)
		s.refuse(req, params.Options)
		return
	}
	s.answer(req, openCodeSelectedOutcome(optionID))
}

func (s *openCodeSession) refuse(req *openCodeServerRequest, options []openCodePermissionOption) {
	if optionID, ok := openCodeSelectRefusal(options); ok {
		s.answer(req, openCodeSelectedOutcome(optionID))
		return
	}
	s.answer(req, openCodeCancelledOutcome())
}

func (s *openCodeSession) answer(req *openCodeServerRequest, reply any) {
	req.once.Do(func() {
		req.answered.Store(true)
		if err := s.conn.respond(context.Background(), req.id, reply); err != nil &&
			!errors.Is(err, errRPCClosed) {
			s.warn("sessions: could not answer a provider permission request",
				"run_ref", s.cfg.RunRef, "method", req.method)
		}
	})
}

func (s *openCodeSession) forgetServerRequest(key string) {
	s.mu.Lock()
	delete(s.pending, key)
	s.mu.Unlock()
}

func (s *openCodeSession) cancelPendingApprovals(context.Context) {
	s.mu.Lock()
	pending := make([]*openCodeServerRequest, 0, len(s.pending))
	for _, req := range s.pending {
		pending = append(pending, req)
	}
	s.mu.Unlock()
	for _, req := range pending {
		s.answer(req, openCodeCancelledOutcome())
	}
}
