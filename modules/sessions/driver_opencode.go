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
	"os"
	"strings"
	"sync"
	"time"
)

// The official OpenCode ACP driver (CLI v1.18.30), over ACP on the owned child's
// stdio. Construction: an internal design note (not shipped)
//
// WIRE. NDJSON over stdio, JSON-RPC 2.0 WITH the `"jsonrpc":"2.0"` member. The
// existing owned rpcConn is the transport; this file does not introduce a second
// RPC framework, an HTTP client, or a new schema.
//
// OWNERSHIP. The spawn is always `acp --hostname 127.0.0.1 [--cwd <WorkDir>]` as
// OUR child. serve, web, attach, mDNS and an external listener are not operate
// forms. The counterpart itself opens loopback HTTP for its internal ACP service;
// that is observed counterpart behavior, not an Olivares server-adoption interface.
//
// AUTHENTICATION. OpenCode advertises interactive `opencode-login`. Calling it
// returns an empty object and is not a headless login. This driver never selects
// it. Initial readiness is `unknown`. Only the provider's own auth-required error
// (-32000) moves the plane to `required`. A home path is not readiness.

const providerDriverOpenCode = "opencode"

const envOpenCodeDisableAutoUpdate = "OPENCODE_DISABLE_AUTOUPDATE"

const (
	openCodeMethodInitialize      = "initialize"
	openCodeMethodSessionNew      = "session/new"
	openCodeMethodSessionResume   = "session/resume"
	openCodeMethodSessionLoad     = "session/load"
	openCodeMethodSessionPrompt   = "session/prompt"
	openCodeMethodSessionCancel   = "session/cancel"
	openCodeMethodSessionClose    = "session/close"
	openCodeMethodSetConfigOption = "session/set_config_option"
	openCodeNotifySessionUpdate   = "session/update"
)

const openCodeProtocolVersion = 1

// openCodeErrAuthRequired is RequestError.authRequired from the ACP SDK 0.21.0
// pinned by OpenCode v1.18.30. Match the CODE, not localized message text.
const openCodeErrAuthRequired = -32000

const openCodeErrMethodNotSupported = -32601

const (
	openCodeConfigIDModel  = "model"
	openCodeConfigIDEffort = "effort"
)

type openCodeDriver struct{}

// NewOpenCodeDriver returns the official OpenCode ACP driver.
func NewOpenCodeDriver() ProviderDriver { return openCodeDriver{} }

func (openCodeDriver) Key() string            { return providerDriverOpenCode }
func (openCodeDriver) ConfigHomeEnv() string  { return envOpenCodeConfigDir }
func (openCodeDriver) DefaultProgram() string { return "opencode" }

func (openCodeDriver) LaunchArgs(l DriverLaunch) []string {
	args := []string{"acp", "--hostname", "127.0.0.1"}
	if dir := strings.TrimSpace(l.WorkDir); dir != "" {
		args = append(args, "--cwd", dir)
	}
	return args
}

func (openCodeDriver) TransportProfile() DriverTransportProfile {
	return DriverTransportProfile{
		Protocol: protocolOpenCodeACP, IO: ioBidirectional, Input: inputText,
	}
}

func (openCodeDriver) LaunchEnv(DriverLaunch) []EnvVar {
	return []EnvVar{{Name: envOpenCodeDisableAutoUpdate, Value: "1"}}
}

func (openCodeDriver) OpenSession(cfg DriverSessionConfig) DriverSession {
	s := &openCodeSession{cfg: cfg, pending: map[string]*openCodeServerRequest{}}
	s.conn = newRPCConn(cfg.Send, true)
	s.conn.onRequest = s.onServerRequest
	s.conn.onNotify = s.onNotification
	return s
}

type openCodeSession struct {
	cfg  DriverSessionConfig
	conn *rpcConn

	mu            sync.Mutex
	sessionID     string
	promptID      string
	cancelledTurn string
	authState     string
	replayFor     string
	caps          openCodeAgentCapabilities
	updates       openCodeUpdateCounts
	pending       map[string]*openCodeServerRequest
	closed        bool
}

type openCodeUpdateCounts struct {
	live    int
	history int
	foreign int
}

type openCodeClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type openCodeFSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type openCodeClientCapabilities struct {
	FS       openCodeFSCapabilities `json:"fs"`
	Terminal bool                   `json:"terminal"`
}

type openCodeInitializeParams struct {
	ProtocolVersion    int                        `json:"protocolVersion"`
	ClientInfo         openCodeClientInfo         `json:"clientInfo"`
	ClientCapabilities openCodeClientCapabilities `json:"clientCapabilities"`
}

type openCodeSessionCapabilities struct {
	List   json.RawMessage `json:"list"`
	Resume json.RawMessage `json:"resume"`
	Close  json.RawMessage `json:"close"`
}

type openCodeAgentCapabilities struct {
	LoadSession         bool                        `json:"loadSession"`
	SessionCapabilities openCodeSessionCapabilities `json:"sessionCapabilities"`
}

type openCodeInitializeResponse struct {
	ProtocolVersion   json.RawMessage           `json:"protocolVersion"`
	AgentCapabilities openCodeAgentCapabilities `json:"agentCapabilities"`
}

type openCodeNewSessionParams struct {
	Cwd        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

type openCodeResumeSessionParams struct {
	SessionID  string            `json:"sessionId"`
	Cwd        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

type openCodeSessionResult struct {
	SessionID     string                 `json:"sessionId"`
	ConfigOptions []openCodeConfigOption `json:"configOptions"`
}

type openCodeConfigOption struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Type         string                 `json:"type"`
	Category     string                 `json:"category"`
	Value        string                 `json:"value"`
	CurrentValue json.RawMessage        `json:"currentValue"`
	Options      []openCodeConfigOption `json:"options"`
}

type openCodeSetConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

type openCodeSetConfigOptionResult struct {
	ConfigOptions []openCodeConfigOption `json:"configOptions"`
}

type openCodeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openCodePromptParams struct {
	SessionID string                 `json:"sessionId"`
	Prompt    []openCodeContentBlock `json:"prompt"`
}

type openCodePromptResponse struct {
	StopReason string `json:"stopReason"`
}

type openCodeSessionParams struct {
	SessionID string `json:"sessionId"`
}

type openCodeSessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

func (s *openCodeSession) Deliver(frame OutputFrame) {
	if frame.Stream != streamStdout {
		return
	}
	s.conn.deliver(frame.Data)
}

func (s *openCodeSession) Handshake(ctx context.Context) (DriverHandshake, error) {
	raw, err := s.conn.call(ctx, openCodeMethodInitialize, openCodeInitializeParams{
		ProtocolVersion: openCodeProtocolVersion,
		ClientInfo:      openCodeClientInfo{Name: s.cfg.ClientName, Version: s.cfg.ClientVersion},
		ClientCapabilities: openCodeClientCapabilities{
			FS: openCodeFSCapabilities{ReadTextFile: false, WriteTextFile: false}, Terminal: false,
		},
	}, s.cfg.CallTimeout)
	if err != nil {
		return DriverHandshake{}, openCodeHandshakeErr("initialize", err)
	}
	if err := openCodeRequireResultObject(raw); err != nil {
		return DriverHandshake{}, err
	}
	var init openCodeInitializeResponse
	if err := json.Unmarshal(raw, &init); err != nil {
		return DriverHandshake{}, &runErr{http.StatusBadGateway, "the provider's initialize response could not be read"}
	}
	if !openCodeProtocolVersionMatches(init.ProtocolVersion) {
		return DriverHandshake{}, &runErr{
			http.StatusBadGateway,
			"the provider answered an agent protocol version this client does not implement",
		}
	}
	s.mu.Lock()
	s.caps = init.AgentCapabilities
	s.mu.Unlock()

	// Do not call authenticate(opencode-login). Publish the initial unknown.
	s.setAuthState(AuthStateUnknown)

	if resume := strings.TrimSpace(s.cfg.ResumeConversationID); resume != "" {
		return s.resumeConversation(ctx, resume)
	}
	return s.newConversation(ctx)
}

func (s *openCodeSession) newConversation(ctx context.Context) (DriverHandshake, error) {
	raw, err := s.conn.call(ctx, openCodeMethodSessionNew, openCodeNewSessionParams{
		Cwd: s.workDir(), MCPServers: []json.RawMessage{},
	}, s.cfg.CallTimeout)
	if err != nil {
		return DriverHandshake{}, s.conversationErr(err)
	}
	if err := openCodeRequireResultObject(raw); err != nil {
		return DriverHandshake{}, err
	}
	var resp openCodeSessionResult
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
	if err := s.honorSettings(ctx, resp.ConfigOptions); err != nil {
		return DriverHandshake{}, err
	}
	return DriverHandshake{ConversationID: id, AuthState: s.AuthState()}, nil
}

func (s *openCodeSession) resumeConversation(ctx context.Context, resume string) (DriverHandshake, error) {
	s.mu.Lock()
	caps := s.caps
	s.replayFor = resume
	s.mu.Unlock()

	method := ""
	switch {
	case openCodeCapabilityPresent(caps.SessionCapabilities.Resume):
		method = openCodeMethodSessionResume
	case caps.LoadSession:
		method = openCodeMethodSessionLoad
	default:
		s.endReplay()
		return DriverHandshake{}, &runErr{
			http.StatusConflict,
			"the provider advertises neither session resume nor session load; refusing to start a different conversation",
		}
	}
	raw, err := s.conn.callInOrder(ctx, method, openCodeResumeSessionParams{
		SessionID: resume, Cwd: s.workDir(), MCPServers: []json.RawMessage{},
	}, s.cfg.CallTimeout, func(json.RawMessage) {
		s.endReplay()
	})
	if err != nil {
		s.endReplay()
		if auth := s.recognizeAuthRequired(err); auth != nil {
			return DriverHandshake{}, auth
		}
		return DriverHandshake{}, &runErr{
			http.StatusConflict,
			"the provider could not resume the stored conversation for this session; refusing to start a different one",
		}
	}
	if err := openCodeRequireResultObject(raw); err != nil {
		return DriverHandshake{}, err
	}
	var resp openCodeSessionResult
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
	if err := s.honorSettings(ctx, resp.ConfigOptions); err != nil {
		return DriverHandshake{}, err
	}
	return DriverHandshake{ConversationID: resume, AuthState: s.AuthState()}, nil
}

func (s *openCodeSession) endReplay() {
	s.mu.Lock()
	s.replayFor = ""
	s.mu.Unlock()
}

func (s *openCodeSession) honorSettings(ctx context.Context, options []openCodeConfigOption) error {
	model := strings.TrimSpace(s.cfg.Model)
	effort := strings.TrimSpace(s.cfg.Effort)
	if model == "" && effort == "" {
		return nil
	}
	current := options
	if model != "" {
		next, err := s.setConfigOption(ctx, openCodeConfigIDModel, model, current)
		if err != nil {
			return err
		}
		current = next
	}
	if effort != "" {
		_, err := s.setConfigOption(ctx, openCodeConfigIDEffort, effort, current)
		return err
	}
	return nil
}

func (s *openCodeSession) setConfigOption(ctx context.Context, configID, value string, current []openCodeConfigOption) ([]openCodeConfigOption, error) {
	opt, err := openCodeFindConfigOption(current, configID)
	if err != nil {
		return nil, err
	}
	offered, err := openCodeOfferedSelectValues(opt)
	if err != nil {
		return nil, err
	}
	if !openCodeExactOfferedValue(offered, value) {
		return nil, &runErr{
			http.StatusUnprocessableEntity,
			"the requested " + configID + " is not an exact offered value; refusing the handshake rather than substituting a default",
		}
	}
	raw, err := s.conn.call(ctx, openCodeMethodSetConfigOption, openCodeSetConfigOptionParams{
		SessionID: s.ConversationID(), ConfigID: configID, Value: value,
	}, s.cfg.CallTimeout)
	if err != nil {
		if auth := s.recognizeAuthRequired(err); auth != nil {
			return nil, auth
		}
		return nil, &runErr{
			http.StatusUnprocessableEntity,
			"the provider rejected the requested " + configID + "; refusing the handshake rather than keeping a default",
		}
	}
	if err := openCodeRequireResultObject(raw); err != nil {
		return nil, err
	}
	var resp openCodeSetConfigOptionResult
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, &runErr{http.StatusBadGateway, "the provider's configuration response could not be read"}
	}
	confirmed, err := openCodeFindConfigOption(resp.ConfigOptions, configID)
	if err != nil {
		return nil, err
	}
	if openCodeCurrentValue(confirmed.CurrentValue) != value {
		return nil, &runErr{
			http.StatusBadGateway,
			"the provider reported a different " + configID + " than the one this launch requested; refusing it",
		}
	}
	return resp.ConfigOptions, nil
}

func (s *openCodeSession) Input(ctx context.Context, text string) (bool, error) {
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
	key, err := s.conn.dispatch(dctx, openCodeMethodSessionPrompt, openCodePromptParams{
		SessionID: session, Prompt: []openCodeContentBlock{{Type: "text", Text: text}},
	}, func(id string) func(json.RawMessage, error) {
		s.beginTurn(id)
		return func(result json.RawMessage, err error) { s.completeTurn(id, result, err) }
	})
	if err != nil {
		s.clearTurn(key)
		if key == "" {
			return false, openCodeTurnErr("start", err)
		}
		return true, openCodeTurnErr("start", err)
	}
	return true, nil
}

func (s *openCodeSession) Interrupt(ctx context.Context) (bool, error) {
	session, prompt := s.markTurnCancelled()
	if session == "" {
		return false, conflictErr("the provider conversation is not bound yet")
	}
	if prompt == "" {
		return false, conflictErr("there is no active provider turn to interrupt")
	}
	s.cancelPendingApprovals(ctx)
	if err := s.conn.notify(ctx, openCodeMethodSessionCancel, openCodeSessionParams{SessionID: session}); err != nil {
		return true, openCodeTurnErr("interrupt", err)
	}
	return true, nil
}

func (s *openCodeSession) Shutdown(ctx context.Context) {
	session, prompt := s.markTurnCancelled()
	if session == "" {
		return
	}
	s.mu.Lock()
	caps := s.caps
	s.mu.Unlock()
	s.cancelPendingApprovals(ctx)
	if prompt != "" {
		_ = s.conn.notify(ctx, openCodeMethodSessionCancel, openCodeSessionParams{SessionID: session})
	}
	if openCodeCapabilityPresent(caps.SessionCapabilities.Close) {
		_, _ = s.conn.call(ctx, openCodeMethodSessionClose, openCodeSessionParams{SessionID: session}, s.callTimeout())
	}
}

func (s *openCodeSession) AuthState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authState == "" {
		return AuthStateUnknown
	}
	return s.authState
}

func (s *openCodeSession) ActiveTurn() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.promptID
}

func (s *openCodeSession) ConversationID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

func (s *openCodeSession) Close(err error) {
	s.mu.Lock()
	s.closed = true
	s.pending = map[string]*openCodeServerRequest{}
	s.mu.Unlock()
	s.conn.close(err)
}

func (s *openCodeSession) beginTurn(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || id == "" {
		return
	}
	s.promptID = id
}

func (s *openCodeSession) clearTurn(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		return
	}
	if s.promptID == id {
		s.promptID = ""
	}
	if s.cancelledTurn == id {
		s.cancelledTurn = ""
	}
}

func (s *openCodeSession) markTurnCancelled() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID != "" && s.promptID != "" {
		s.cancelledTurn = s.promptID
	}
	return s.sessionID, s.promptID
}

func (s *openCodeSession) turnCancelled(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return id != "" && s.cancelledTurn == id
}

func (s *openCodeSession) completeTurn(id string, result json.RawMessage, err error) {
	s.clearTurn(id)
	if err != nil {
		var re *rpcError
		if errors.As(err, &re) && re.Code == openCodeErrAuthRequired {
			s.setAuthState(AuthStateRequired)
		}
		return
	}
	var resp openCodePromptResponse
	if json.Unmarshal(result, &resp) != nil {
		return
	}
	s.warnStopReason(resp.StopReason)
}

func (s *openCodeSession) warnStopReason(reason string) {
	if reason == "" {
		s.warn("sessions: the provider's turn response carried no stop reason", "run_ref", s.cfg.RunRef)
	}
}

func (s *openCodeSession) onNotification(method string, params json.RawMessage) {
	if method != openCodeNotifySessionUpdate {
		return
	}
	var n openCodeSessionNotification
	if json.Unmarshal(params, &n) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.replayFor != "" && n.SessionID == s.replayFor:
		s.updates.history++
	case s.sessionID == "" || n.SessionID != s.sessionID:
		s.updates.foreign++
	default:
		s.updates.live++
	}
}

func (s *openCodeSession) updateCounts() openCodeUpdateCounts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updates
}

func (s *openCodeSession) workDir() string {
	if dir := strings.TrimSpace(s.cfg.WorkDir); dir != "" {
		return dir
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "/"
}

func (s *openCodeSession) callTimeout() time.Duration {
	if s.cfg.CallTimeout > 0 {
		return s.cfg.CallTimeout
	}
	return defaultDriverCallTimeout
}

func (s *openCodeSession) setAuthState(state string) {
	s.mu.Lock()
	changed := s.authState != state
	s.authState = state
	s.mu.Unlock()
	if changed && s.cfg.OnAuthState != nil {
		s.cfg.OnAuthState(state)
	}
}

func (s *openCodeSession) warn(msg string, args ...any) {
	if s.cfg.Warn != nil {
		s.cfg.Warn(msg, args...)
	}
}

func openCodeProtocolVersionMatches(raw json.RawMessage) bool {
	var version int
	if err := json.Unmarshal(raw, &version); err != nil {
		return false
	}
	return version == openCodeProtocolVersion
}

func openCodeCapabilityPresent(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

func openCodeRequireResultObject(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return &runErr{http.StatusBadGateway, "the provider answered without a result"}
	}
	if trimmed[0] != '{' {
		return &runErr{http.StatusBadGateway, "the provider's result was not an object"}
	}
	return nil
}

func openCodeFindConfigOption(options []openCodeConfigOption, id string) (openCodeConfigOption, error) {
	var found []openCodeConfigOption
	for _, opt := range options {
		if opt.ID == id {
			found = append(found, opt)
		}
	}
	switch len(found) {
	case 0:
		return openCodeConfigOption{}, &runErr{
			http.StatusUnprocessableEntity,
			"this OpenCode launch has no " + id + " selector; refusing the requested " + id + " rather than substituting a default",
		}
	case 1:
		return found[0], nil
	default:
		return openCodeConfigOption{}, &runErr{
			http.StatusBadGateway,
			"the provider advertised more than one " + id + " selector; refusing the handshake rather than guessing",
		}
	}
}

func openCodeOfferedSelectValues(opt openCodeConfigOption) ([]string, error) {
	var out []string
	var walk func(openCodeConfigOption)
	walk = func(o openCodeConfigOption) {
		if v := strings.TrimSpace(o.Value); v != "" {
			out = append(out, v)
		}
		for _, child := range o.Options {
			walk(child)
		}
	}
	walk(opt)
	if len(out) == 0 {
		return nil, &runErr{
			http.StatusUnprocessableEntity,
			"the advertised " + opt.ID + " selector offered no selectable values; refusing the handshake rather than substituting a default",
		}
	}
	return out, nil
}

func openCodeExactOfferedValue(offered []string, want string) bool {
	for _, v := range offered {
		if v == want {
			return true
		}
	}
	return false
}

func openCodeCurrentValue(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(trimmed, &s) == nil {
		return s
	}
	return ""
}

func openCodeHandshakeErr(step string, err error) error {
	var re *runErr
	if errors.As(err, &re) {
		return re
	}
	if errors.Is(err, errRPCClosed) {
		return &runErr{http.StatusBadGateway, "the owned provider process ended during " + step}
	}
	return &runErr{http.StatusBadGateway, "the provider refused " + step + " for this launch"}
}

func (s *openCodeSession) conversationErr(err error) error {
	if auth := s.recognizeAuthRequired(err); auth != nil {
		return auth
	}
	return openCodeHandshakeErr(openCodeMethodSessionNew, err)
}

// recognizeAuthRequired publishes required and returns the bounded public
// category when the provider's own JSON-RPC code is auth-required. It does not
// invent a ready transition. A nil return means this error is some other failure.
func (s *openCodeSession) recognizeAuthRequired(err error) error {
	var re *rpcError
	if errors.As(err, &re) && re.Code == openCodeErrAuthRequired {
		s.setAuthState(AuthStateRequired)
		return &runErr{
			http.StatusUnprocessableEntity,
			"the provider refused to open a conversation because this profile is not authenticated (auth_required)",
		}
	}
	return nil
}

func openCodeTurnErr(step string, err error) error {
	var re *runErr
	if errors.As(err, &re) {
		return re
	}
	if errors.Is(err, errRPCClosed) {
		return &runErr{http.StatusConflict, "the owned provider process is no longer accepting input"}
	}
	return conflictErr("the provider refused to " + step + " a turn on this conversation")
}
