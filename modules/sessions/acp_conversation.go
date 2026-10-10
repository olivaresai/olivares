// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// The conversation mechanics every ACP driver shares: the initialize exchange's
// shape, a turn's prompt, resuming the stored conversation, ending it, and the
// provider's own authentication-required answer. A driver keeps what is its own:
// the handshake order, its settings, and what it advertises.

type acpClientCapabilities struct {
	FS       acpFSCapabilities `json:"fs"`
	Terminal bool              `json:"terminal"`
}

type acpInitializeParams struct {
	ProtocolVersion    int                   `json:"protocolVersion"`
	ClientInfo         acpClientInfo         `json:"clientInfo"`
	ClientCapabilities acpClientCapabilities `json:"clientCapabilities"`
}

type acpAgentCapabilities struct {
	MCP struct {
		HTTP bool `json:"http"`
	} `json:"mcpCapabilities"`
	LoadSession         bool                   `json:"loadSession"`
	SessionCapabilities acpSessionCapabilities `json:"sessionCapabilities"`
}

type acpInitializeResponse struct {
	AuthMethods []struct {
		ID string `json:"id"`
	} `json:"authMethods"`
	ProtocolVersion   json.RawMessage      `json:"protocolVersion"`
	AgentCapabilities acpAgentCapabilities `json:"agentCapabilities"`
}

type acpNewSessionParams struct {
	Cwd        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

type acpPromptParams struct {
	SessionID string            `json:"sessionId"`
	Prompt    []acpContentBlock `json:"prompt"`
}

// initialize sends the one initialize this client speaks. It advertises no file
// system and no terminal, because this client implements neither handler.
func (s *acpSession) initialize(ctx context.Context) (acpInitializeResponse, error) {
	raw, err := s.conn.call(ctx, acpMethodInitialize, acpInitializeParams{
		ProtocolVersion: acpProtocolVersion,
		ClientInfo:      acpClientInfo{Name: s.cfg.ClientName, Version: s.cfg.ClientVersion},
		ClientCapabilities: acpClientCapabilities{
			FS: acpFSCapabilities{ReadTextFile: false, WriteTextFile: false}, Terminal: false,
		},
	}, s.cfg.CallTimeout)
	if err != nil {
		return acpInitializeResponse{}, acpHandshakeErr("initialize", err)
	}
	if err := acpRequireResultObject(raw); err != nil {
		return acpInitializeResponse{}, err
	}
	var init acpInitializeResponse
	if err := json.Unmarshal(raw, &init); err != nil {
		return acpInitializeResponse{}, &runErr{http.StatusBadGateway, "the provider's initialize response could not be read"}
	}
	if !acpProtocolVersionMatches(init.ProtocolVersion) {
		return acpInitializeResponse{}, &runErr{
			http.StatusBadGateway,
			"the provider answered an agent protocol version this client does not implement",
		}
	}
	return init, nil
}

// resumeCall asks the agent to continue the stored conversation with method
// (session/resume or session/load) and returns its result object. A failed
// resume is a refusal: a driver never starts a different conversation instead.
func (s *acpSession) resumeCall(ctx context.Context, method, resume string) (json.RawMessage, error) {
	raw, err := s.conn.callInOrder(ctx, method, acpResumeSessionParams{
		SessionID: resume, Cwd: s.workDir(), MCPServers: s.cfg.sessionMCP,
	}, s.cfg.CallTimeout, func(json.RawMessage) {
		s.endReplay()
	})
	if err != nil {
		s.endReplay()
		if auth := s.recognizeAuthRequired(err); auth != nil {
			return nil, auth
		}
		s.warn("sessions: the provider could not resume the stored conversation",
			"run_ref", s.cfg.RunRef, "method", method, "cause", errorCause(err, "unavailable"))
		return nil, &runErr{
			http.StatusConflict,
			"the provider could not resume the stored conversation for this session; refusing to start a different one",
		}
	}
	if err := acpRequireResultObject(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// Input starts one turn with the operator's text. The prompt is dispatched, not
// awaited: the turn ends when its own correlated response says so.
func (s *acpSession) Input(ctx context.Context, text string) (bool, error) {
	if strings.TrimSpace(text) == "" {
		return false, badRequest("input text is required for a provider-driven session")
	}
	s.mu.Lock()
	session, prompt := s.sessionID, s.promptID
	s.mu.Unlock()
	if session == "" {
		return false, conflictErr("the provider conversation is not bound yet")
	}
	if prompt != "" {
		return false, conflictErr("a provider turn is already in flight on this conversation; interrupt it before sending another")
	}
	dctx, cancel := context.WithTimeout(ctx, s.callTimeout())
	defer cancel()
	key, err := s.conn.dispatch(dctx, acpMethodSessionPrompt, acpPromptParams{
		SessionID: session, Prompt: []acpContentBlock{{Type: "text", Text: text}},
	}, func(id string) func(json.RawMessage, error) {
		s.beginTurn(id)
		return func(result json.RawMessage, err error) { s.completeTurn(id, result, err) }
	})
	if err != nil {
		s.clearTurn(key)
		if key == "" {
			return false, acpTurnErr("start", err)
		}
		return true, acpTurnErr("start", err)
	}
	return true, nil
}

// shutdown cancels the turn in flight, answers what is pending, and closes the
// conversation when the agent advertised that it can.
func (s *acpSession) shutdown(ctx context.Context, canClose bool) {
	session, prompt := s.markTurnCancelled()
	if session == "" {
		return
	}
	s.cancelPendingApprovals(ctx)
	if prompt != "" {
		if err := s.conn.notify(ctx, acpMethodSessionCancel, acpSessionParams{SessionID: session}); err != nil {
			s.warn("sessions: the provider turn could not be cancelled at shutdown",
				"run_ref", s.cfg.RunRef, "cause", errorCause(err, "unavailable"))
		}
	}
	if canClose {
		if _, err := s.conn.call(ctx, acpMethodSessionClose, acpSessionParams{SessionID: session}, s.callTimeout()); err != nil {
			s.warn("sessions: the provider conversation could not be closed at shutdown",
				"run_ref", s.cfg.RunRef, "cause", errorCause(err, "unavailable"))
		}
	}
}

func acpCapabilityPresent(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

func acpRequireResultObject(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return &runErr{http.StatusBadGateway, "the provider answered without a result"}
	}
	if trimmed[0] != '{' {
		return &runErr{http.StatusBadGateway, "the provider's result was not an object"}
	}
	return nil
}

func (s *acpSession) conversationErr(err error) error {
	if auth := s.recognizeAuthRequired(err); auth != nil {
		return auth
	}
	return acpHandshakeErr(acpMethodSessionNew, err)
}

// recognizeAuthRequired publishes required and returns the bounded public
// category when the provider's own JSON-RPC code is auth-required. It does not
// invent a ready transition. A nil return means this error is some other failure.
func (s *acpSession) recognizeAuthRequired(err error) error {
	var re *rpcError
	if errors.As(err, &re) && re.Code == acpErrAuthRequired {
		s.setAuthState(AuthStateRequired)
		return &runErr{
			http.StatusUnprocessableEntity,
			"the provider refused to open a conversation because this profile is not authenticated (auth_required)",
		}
	}
	return nil
}

// acpClipList joins values the provider chose, each bounded, for an error sentence.
func acpClipList(values []string) string {
	clipped := make([]string, len(values))
	for i, v := range values {
		clipped[i] = clipCause(v)
	}
	return strings.Join(clipped, ", ")
}

// acpOfferedList names up to ten offered values and how many more there are.
func acpOfferedList(offered []string) string {
	const shown = 10
	if len(offered) <= shown {
		return acpClipList(offered)
	}
	return acpClipList(offered[:shown]) + " and " + strconv.Itoa(len(offered)-shown) + " more"
}
