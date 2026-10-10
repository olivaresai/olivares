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

	"github.com/olivaresai/olivares/core/driverfacts"
)

// The official Gemini CLI driver (CLI 0.62.0), over ACP on the owned child's
// stdio. The wire, the permission request and the turn are the shared ACP
// session's (acp_session.go, acp_conversation.go, acp_permission.go); this file
// keeps what is Gemini's own. Recorded behavior: GEMINI-ACP.md.
//
// OWNERSHIP. The spawn is always `gemini --acp` as OUR child, and
// GEMINI_CLI_NO_RELAUNCH keeps it one process: without it the CLI starts a second
// copy of itself, ignores SIGTERM in the first, and the child this run owns is
// only the wrapper.
//
// AUTHENTICATION. initialize advertises oauth-personal, gemini-api-key,
// vertex-ai and gateway. A bound Gemini key selects gemini-api-key before
// session/new. Account and legacy adapter sessions keep the CLI's own saved
// authentication: oauth-personal opens a browser, and their credential or login
// already belongs to the child's profile. Readiness starts `unknown`; only the
// CLI's own -32000 answer moves
// it to `required`. A home path is not readiness.
//
// APPROVAL MODE. Every launch sets the mode explicitly, because the CLI's
// settings can default to a mode that approves without asking. read-only runs the
// CLI's `plan` mode; every other preset runs `default`, which asks for each tool
// it does not consider safe, and the session's live policy answers each ask.

const providerDriverGemini = "gemini-cli"

// envGeminiNoRelaunch keeps the owned child a single process.
const envGeminiNoRelaunch = "GEMINI_CLI_NO_RELAUNCH"

// The ACP methods only Gemini's settings use, as the pinned 0.62.0 names them.
const (
	geminiMethodSetModel = "session/set_model"
	geminiMethodSetMode  = "session/set_mode"
)

// Gemini's approval modes. Only these two are ever selected: autoEdit and yolo
// approve without asking, which is the live policy's decision and never a driver's.
const (
	geminiModeDefault = "default"
	geminiModePlan    = "plan"
)

type geminiDriver struct{}

// NewGeminiDriver returns the official Gemini CLI driver.
func NewGeminiDriver() ProviderDriver { return geminiDriver{} }

func (geminiDriver) Key() string { return providerDriverGemini }
func (geminiDriver) ConfigHomeEnv() string {
	facts, _ := driverfacts.Lookup(providerDriverGemini)
	return facts.ConfigHomeEnv
}
func (geminiDriver) DefaultProgram() string {
	facts, _ := driverfacts.Lookup(providerDriverGemini)
	return facts.Program
}

// LaunchArgs is the ONLY operate form. The model and the approval mode travel
// on the protocol after session/new, never on argv.
func (geminiDriver) LaunchArgs(DriverLaunch) []string { return []string{"--acp"} }

func (geminiDriver) TransportProfile() DriverTransportProfile {
	return DriverTransportProfile{Protocol: protocolGeminiACP, IO: ioBidirectional, Input: inputText}
}

// LaunchTerms declares what a Gemini launch hands its child. The model rides on
// session/set_model as an exact value the CLI offered; the permission preset
// rides on session/set_mode. The CLI has no effort control. The models are the
// CLI's own offered choices, so provider model discovery does not select them.
func (geminiDriver) LaunchTerms() DriverLaunchTerms {
	return DriverLaunchTerms{
		Model:          TermCarried,
		Effort:         TermNotCarried,
		PermissionMode: TermCarried,
		ModelDiscovery: ModelDiscoveryNone,
	}
}

func (geminiDriver) LaunchEnv(l DriverLaunch) []EnvVar {
	env := []EnvVar{{Name: envGeminiNoRelaunch, Value: "true"}}
	if l.BoundProvider.Kind != "" {
		env = append(env, EnvVar{Name: "GEMINI_TELEMETRY_ENABLED", Value: "false"}, EnvVar{Name: "GEMINI_TELEMETRY_LOG_PROMPTS", Value: "false"})
	}
	return env
}

func (geminiDriver) OpenSession(cfg DriverSessionConfig) DriverSession {
	if cfg.sessionMCP == nil {
		cfg.sessionMCP = []json.RawMessage{}
	}
	s := &geminiSession{acpSession: newACPSession(cfg, providerDriverGemini)}
	s.conn.onRequest = s.onServerRequest
	s.conn.onNotify = s.onNotification
	return s
}

type geminiSession struct {
	acpSession
	caps acpAgentCapabilities
}

// geminiSessionResult is what session/new and session/load answer: the
// conversation (new only) and the modes and models this CLI offers.
type geminiSessionResult struct {
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

type geminiSetModelParams struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

type geminiSetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

func (s *geminiSession) Handshake(ctx context.Context) (DriverHandshake, error) {
	init, err := s.initialize(ctx)
	if err != nil {
		return DriverHandshake{}, err
	}
	s.mu.Lock()
	s.caps = init.AgentCapabilities
	s.mu.Unlock()

	// A bound key must choose the native API-key method explicitly: cached
	// OAuth or a different vendor method cannot decide the profile's identity.
	if s.cfg.AuthSource == AuthSourceManagedInjection && s.cfg.BoundProvider.Kind != "" {
		if s.cfg.BoundProvider.Kind != ProviderKindGemini {
			return DriverHandshake{}, &runErr{http.StatusConflict, "Gemini CLI requires a bound Gemini API key"}
		}
		offered := false
		for _, method := range init.AuthMethods {
			offered = offered || method.ID == "gemini-api-key"
		}
		if !offered {
			return DriverHandshake{}, &runErr{http.StatusUnprocessableEntity, "Gemini CLI did not offer API-key authentication; the session did not start"}
		}
		raw, err := s.conn.call(ctx, "authenticate", map[string]string{"methodId": "gemini-api-key"}, s.cfg.CallTimeout)
		if err != nil {
			return DriverHandshake{}, s.conversationErr(err)
		}
		if err := acpRequireResultObject(raw); err != nil {
			return DriverHandshake{}, err
		}
	}
	// Account sessions use the selected home's native login; no interactive
	// authentication is ever started by a session handshake.
	s.setAuthState(AuthStateUnknown)

	if resume := strings.TrimSpace(s.cfg.ResumeConversationID); resume != "" {
		return s.resumeConversation(ctx, resume)
	}
	return s.newConversation(ctx)
}

func (s *geminiSession) newConversation(ctx context.Context) (DriverHandshake, error) {
	raw, err := s.conn.call(ctx, acpMethodSessionNew, acpNewSessionParams{
		Cwd: s.workDir(), MCPServers: s.cfg.sessionMCP,
	}, s.cfg.CallTimeout)
	if err != nil {
		return DriverHandshake{}, s.conversationErr(err)
	}
	if err := acpRequireResultObject(raw); err != nil {
		return DriverHandshake{}, err
	}
	var resp geminiSessionResult
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
	if err := s.honorSettings(ctx, resp); err != nil {
		return DriverHandshake{}, err
	}
	return DriverHandshake{ConversationID: id, AuthState: s.AuthState()}, nil
}

// resumeConversation continues the stored conversation with session/load. A
// conversation the CLI cannot load is a refusal, never a different conversation.
func (s *geminiSession) resumeConversation(ctx context.Context, resume string) (DriverHandshake, error) {
	s.mu.Lock()
	loadable := s.caps.LoadSession
	s.replayFor = resume
	s.mu.Unlock()
	if !loadable {
		s.endReplay()
		return DriverHandshake{}, &runErr{
			http.StatusConflict,
			"the provider does not advertise session load; refusing to start a different conversation",
		}
	}
	raw, err := s.resumeCall(ctx, acpMethodSessionLoad, resume)
	if err != nil {
		return DriverHandshake{}, err
	}
	var resp geminiSessionResult
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
	if err := s.honorSettings(ctx, resp); err != nil {
		return DriverHandshake{}, err
	}
	return DriverHandshake{ConversationID: resume, AuthState: s.AuthState()}, nil
}

// honorSettings applies the approval mode, then the model when the launch names
// one. Each is accepted only as a value the CLI offered, and one it did not
// offer fails the handshake rather than falling back to a default.
func (s *geminiSession) honorSettings(ctx context.Context, offered geminiSessionResult) error {
	mode := geminiModeFor(s.cfg.Preset)
	var modes []string
	for _, m := range offered.Modes.AvailableModes {
		modes = append(modes, m.ID)
	}
	if err := geminiRequireOffered("approval mode", mode, modes); err != nil {
		return err
	}
	if err := s.setting(ctx, geminiMethodSetMode, geminiSetModeParams{SessionID: s.ConversationID(), ModeID: mode}, "approval mode"); err != nil {
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
	if err := geminiRequireOffered("model", model, models); err != nil {
		return err
	}
	return s.setting(ctx, geminiMethodSetModel, geminiSetModelParams{SessionID: s.ConversationID(), ModelID: model}, "model")
}

func (s *geminiSession) setting(ctx context.Context, method string, params any, what string) error {
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

// geminiModeFor is the approval mode a launch preset runs. Only read-only
// differs: every other preset asks for what the CLI does not consider safe.
func geminiModeFor(preset string) string {
	if preset == PresetReadOnly {
		return geminiModePlan
	}
	return geminiModeDefault
}

func geminiRequireOffered(what, want string, offered []string) error {
	for _, v := range offered {
		if v == want {
			return nil
		}
	}
	if len(offered) == 0 {
		return &runErr{
			http.StatusUnprocessableEntity,
			"Gemini CLI offered no " + what + " to choose from; refusing the handshake rather than substituting a default",
		}
	}
	return &requestNotOffered{&runErr{
		http.StatusUnprocessableEntity,
		"Gemini CLI does not offer the " + what + " " + strconv.Quote(clipCause(want)) + "; it offers: " +
			acpOfferedList(offered) + " (the session did not start rather than use a default)",
	}}
}

func (s *geminiSession) Shutdown(ctx context.Context) {
	// The CLI advertises no session/close, so a stop is a cancel and the child's end.
	s.shutdown(ctx, false)
}
