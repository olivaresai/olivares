// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// The conversation settings of an ACP CLI that offers modes and models on
// session/new (Gemini CLI, Cursor CLI): the shapes, the two methods, and applying a mode
// and then a model. Each is accepted only as a value the CLI offered, and one it did not
// offer fails the handshake rather than falling back to a default.

const (
	acpMethodSetMode  = "session/set_mode"
	acpMethodSetModel = "session/set_model"
)

// acpSessionResult is what session/new and session/load answer: the conversation (new
// only) and the modes and models this CLI offers.
type acpSessionResult struct {
	SessionID string `json:"sessionId"`
	Modes     struct {
		AvailableModes []struct {
			ID string `json:"id"`
		} `json:"availableModes"`
	} `json:"modes"`
	Models struct {
		AvailableModels []struct {
			ModelID string `json:"modelId"`
		} `json:"availableModels"`
	} `json:"models"`
}

type acpSetModelParams struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

type acpSetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// applyModeAndModel sets the mode, then the model when the launch names one. tool names the
// CLI in a refusal.
func (s *acpSession) applyModeAndModel(ctx context.Context, tool, mode string, offered acpSessionResult) error {
	var modes []string
	for _, m := range offered.Modes.AvailableModes {
		modes = append(modes, m.ID)
	}
	if err := acpRequireOffered(tool, "approval mode", mode, modes); err != nil {
		return err
	}
	if err := s.setting(ctx, acpMethodSetMode, acpSetModeParams{SessionID: s.ConversationID(), ModeID: mode}, "approval mode"); err != nil {
		return err
	}
	model := strings.TrimSpace(s.cfg.Model)
	if model == "" {
		return nil
	}
	var models []string
	for _, m := range offered.Models.AvailableModels {
		models = append(models, m.ModelID)
	}
	if err := acpRequireOffered(tool, "model", model, models); err != nil {
		return err
	}
	return s.setting(ctx, acpMethodSetModel, acpSetModelParams{SessionID: s.ConversationID(), ModelID: model}, "model")
}

func (s *acpSession) setting(ctx context.Context, method string, params any, what string) error {
	raw, err := s.conn.call(ctx, method, params, s.cfg.CallTimeout)
	if err != nil {
		if auth := s.recognizeAuthRequired(err); auth != nil {
			return auth
		}
		s.warn("sessions: the provider rejected a setting", "run_ref", s.cfg.RunRef, "method", method,
			"cause", errorCause(err, "unavailable"))
		return &runErr{
			http.StatusUnprocessableEntity,
			"the provider rejected the requested " + what + "; refusing the handshake rather than keeping a default",
		}
	}
	return acpRequireResultObject(raw)
}

func acpRequireOffered(tool, what, want string, offered []string) error {
	for _, v := range offered {
		if v == want {
			return nil
		}
	}
	if len(offered) == 0 {
		return &runErr{
			http.StatusUnprocessableEntity,
			tool + " offered no " + what + " to choose from; refusing the handshake rather than substituting a default",
		}
	}
	return &requestNotOffered{&runErr{
		http.StatusUnprocessableEntity,
		tool + " does not offer the " + what + " " + strconv.Quote(clipCause(want)) + "; it offers: " +
			acpOfferedList(offered) + " (the session did not start rather than use a default)",
	}}
}

// newConversation opens a conversation with session/new and applies the launch settings.
func (s *acpSession) newConversation(ctx context.Context, honor func(context.Context, acpSessionResult) error) (DriverHandshake, error) {
	raw, err := s.conn.call(ctx, acpMethodSessionNew, acpNewSessionParams{
		Cwd: s.workDir(), MCPServers: s.cfg.sessionMCP,
	}, s.cfg.CallTimeout)
	if err != nil {
		return DriverHandshake{}, s.conversationErr(err)
	}
	if err := acpRequireResultObject(raw); err != nil {
		return DriverHandshake{}, err
	}
	var resp acpSessionResult
	if err := json.Unmarshal(raw, &resp); err != nil {
		return DriverHandshake{}, &runErr{http.StatusBadGateway, "the provider's conversation response could not be read"}
	}
	id := strings.TrimSpace(resp.SessionID)
	if id == "" {
		return DriverHandshake{}, &runErr{http.StatusBadGateway, "the provider's conversation response carried no session id"}
	}
	s.mu.Lock()
	s.sessionID = id
	s.mu.Unlock()
	if err := honor(ctx, resp); err != nil {
		return DriverHandshake{}, err
	}
	return DriverHandshake{ConversationID: id, AuthState: s.AuthState()}, nil
}

// resumeConversation continues the stored conversation with session/load. A
// conversation the CLI cannot load is a refusal, never a different conversation.
func (s *acpSession) resumeConversation(ctx context.Context, resume string, loadable bool, honor func(context.Context, acpSessionResult) error) (DriverHandshake, error) {
	s.mu.Lock()
	s.beginReplay(resume)
	s.mu.Unlock()
	if !loadable {
		s.answerReplay() // nothing was asked, so nothing can replay
		return DriverHandshake{}, &runErr{
			http.StatusConflict,
			"the provider does not advertise session load; refusing to start a different conversation",
		}
	}
	raw, err := s.resumeCall(ctx, acpMethodSessionLoad, resume)
	if err != nil {
		return DriverHandshake{}, err
	}
	var resp acpSessionResult
	if err := json.Unmarshal(raw, &resp); err != nil {
		return DriverHandshake{}, &runErr{http.StatusBadGateway, "the provider's conversation response could not be read"}
	}
	if id := strings.TrimSpace(resp.SessionID); id != "" && id != resume {
		return DriverHandshake{}, &runErr{
			http.StatusConflict,
			"the provider resumed a different conversation than the one stored for this session; refusing it",
		}
	}
	s.mu.Lock()
	s.sessionID = resume
	s.mu.Unlock()
	if err := honor(ctx, resp); err != nil {
		return DriverHandshake{}, err
	}
	return DriverHandshake{ConversationID: resume, AuthState: s.AuthState()}, nil
}
