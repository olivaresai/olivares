// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/driverfacts"
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

// openCodeMethodSetConfigOption is the ACP method only OpenCode's settings use;
// the shared methods are in acp_session.go.
const openCodeMethodSetConfigOption = "session/set_config_option"

const (
	openCodeConfigIDModel  = "model"
	openCodeConfigIDEffort = "effort"
)

type openCodeDriver struct{}

// NewOpenCodeDriver returns the official OpenCode ACP driver.
func NewOpenCodeDriver() ProviderDriver { return openCodeDriver{} }

func (openCodeDriver) Key() string { return providerDriverOpenCode }
func (openCodeDriver) ConfigHomeEnv() string {
	facts, _ := driverfacts.Lookup(providerDriverOpenCode)
	return facts.ConfigHomeEnv
}
func (openCodeDriver) DefaultProgram() string {
	facts, _ := driverfacts.Lookup(providerDriverOpenCode)
	return facts.Program
}

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

// LaunchTerms declares what an OpenCode launch hands its child. The model and
// the effort travel on session/set_config_option, each only as an exact value the
// agent offered, and a value it did not offer fails the handshake rather than
// falling back to a default. The permission preset reaches the native inline
// configuration, with stronger live policy still deciding each edit or command.
// The models can be discovered by probing
// the credential a profile binds; the driver lists none.
func (openCodeDriver) LaunchTerms() DriverLaunchTerms {
	return DriverLaunchTerms{
		Model:          TermCarried,
		Effort:         TermCarried,
		PermissionMode: TermCarried,
		ModelDiscovery: ModelDiscoveryBoundCredentialProbe,
	}
}

func (openCodeDriver) LaunchEnv(l DriverLaunch) []EnvVar {
	env := []EnvVar{{Name: envOpenCodeDisableAutoUpdate, Value: "1"}}
	cfg := map[string]any{}
	bound := true
	switch {
	case l.LocalModelEndpoint != "":
		_ = json.Unmarshal([]byte(openCodeLocalProviderConfig(l.LocalModelEndpoint, l.LocalModels, l.Model)), &cfg)
	case l.BoundProvider.Kind != "":
		openCodeConfineToKey(cfg, l.BoundProvider, l.Model)
	default:
		bound = false
	}
	if bound {
		env = append(env,
			EnvVar{Name: envOpenCodeDisableModelsFetch, Value: "1"},
			EnvVar{Name: envOpenCodeDisableLSPDownload, Value: "1"},
			EnvVar{Name: envNPMConfigOffline, Value: "true"},
			EnvVar{Name: envOpenCodeDisableShare, Value: "1"})
	}
	permission := ""
	switch l.Preset {
	case PresetReadOnly:
		permission = "deny"
	case PresetAsk, PresetEditsOnly, PresetEditsAndCommands, PresetFull:
		permission = "ask"
	}
	if permission != "" {
		rules := map[string]any{"*": permission, "bash": permission, "edit": permission,
			"read": map[string]string{"*": "allow", "*.env": "deny", "*.env.*": "deny", "*.env.example": "allow"},
			"glob": "allow", "grep": "allow", "list": "allow"}
		// Keep edits and commands asking so live policy can still deny or ask.
		// ACP approval is answered at once when that policy permits it.
		cfg["permission"] = rules
		// Agent-specific configuration takes precedence over global permission
		// rules. Pin the native built-in agent and its rules for this launch too.
		cfg["default_agent"] = "build"
		cfg["agent"] = map[string]any{"build": map[string]any{"permission": rules}}
	}
	if len(cfg) > 0 {
		body, _ := json.Marshal(cfg)
		env = append(env, EnvVar{Name: envOpenCodeConfigContent, Value: string(body)})
	}
	return env
}

// openCodeLocalProviderID names the local provider in OpenCode's configuration.
const openCodeLocalProviderID = "olivares_ollama"

// A SESSION BOUND TO A PROVIDER RECORD REACHES ONLY THAT PROVIDER.
//
// OpenCode keeps its own hosted provider (OpenCode Zen, "opencode") beside any key it is
// given, and with no model chosen it answers there: an Anthropic-key session answered on
// opencode/big-pickle. So every record-bound launch allows exactly the bound provider,
// disables the hosted one by name and turns sharing off. The title, small-model and
// compaction requests resolve their model among the enabled providers, so they stay on it
// too. Measured on OpenCode 1.18.34 over a whole session with an egress probe:
// Anthropic, OpenAI and xAI keys reached only their own API host, a local model only
// loopback.
//
// Three more requests went to other hosts, each closed by OpenCode's own switch:
//   - OPENCODE_DISABLE_MODELS_FETCH: the model catalogue from models.opencode.ai at start;
//   - npm_config_offline: the background install of @opencode-ai/plugin from
//     registry.npmjs.org into every configuration directory (it fails and is logged);
//   - OPENCODE_DISABLE_LSP_DOWNLOAD: language servers downloaded when a file is edited.
//
// And two held against what a profile or a project may already hold:
//   - the bound provider's address is pinned in the same configuration, so a baseURL saved
//     for that provider elsewhere cannot carry the key and the prompt to another host;
//   - OPENCODE_DISABLE_SHARE: share=disabled stops new shares, but a session shared before
//     keeps syncing its messages to the share service on resume unless this is set.
const (
	envOpenCodeDisableModelsFetch = "OPENCODE_DISABLE_MODELS_FETCH"
	envOpenCodeDisableLSPDownload = "OPENCODE_DISABLE_LSP_DOWNLOAD"
	envNPMConfigOffline           = "npm_config_offline"
	envOpenCodeDisableShare       = "OPENCODE_DISABLE_SHARE"
)

// openCodeHostedProviderID is OpenCode's own hosted provider.
const openCodeHostedProviderID = "opencode"

// openCodeKeyProvider is OpenCode's provider for a key record's kind and that provider's
// own API in its SDK's spelling (the carrier's vendor endpoint, providerVendorEndpoints,
// as the SDK takes it). Each reads its key from the variable the record injects.
type openCodeKeyProvider struct{ id, baseURL string }

var openCodeKeyProviders = map[string]openCodeKeyProvider{
	ProviderKindAnthropic: {"anthropic", "https://api.anthropic.com/v1"},
	ProviderKindOpenAI:    {"openai", "https://api.openai.com/v1"},
	ProviderKindXAI:       {"xai", "https://api.x.ai/v1"},
}

// openCodeUnconfinedProviderID is allowed when a launch names a kind OpenCode has no
// provider for. It names no provider, so nothing answers: the mint refuses such a record
// first (recordServesDriver), and this keeps the driver closed if a caller did not.
const openCodeUnconfinedProviderID = "olivares_no_provider"

// openCodeConfineToKey allows only the bound key's provider, at the carrier's endpoint. A
// carrier naming a kind OpenCode has no provider for, or any address but the vendor's own,
// enables no provider at all.
func openCodeConfineToKey(cfg map[string]any, b BoundProvider, model string) {
	p, ok := openCodeKeyProviders[b.Kind]
	if !ok || b.Endpoint != providerVendorEndpoints[b.Kind] {
		openCodeConfineTo(cfg, openCodeUnconfinedProviderID)
		return
	}
	cfg["provider"] = map[string]any{p.id: map[string]any{"options": map[string]any{"baseURL": p.baseURL}}}
	openCodeConfineTo(cfg, p.id)
	if model != "" {
		cfg["model"] = openCodeModelValue(b, model, false)
		cfg["small_model"] = cfg["model"]
	}
}

// A record stores the endpoint's model ID. OpenCode's config and ACP select use
// provider/model. Endpoint IDs always acquire the prefix; an explicit native
// choice already qualified on that same provider keeps its published spelling.
func openCodeModelValue(b BoundProvider, model string, endpointID bool) string {
	id := openCodeKeyProviders[b.Kind].id
	if b.Kind == ProviderKindOllama {
		id = openCodeLocalProviderID
	}
	if id == "" || model == "" || !endpointID && strings.HasPrefix(model, id+"/") {
		return model
	}
	return id + "/" + model
}

// openCodeManagedConfigDir is where OpenCode 1.18.34 reads its host-managed configuration on
// Linux (packages/opencode/src/config/managed.ts). A variable only so tests can move it.
var openCodeManagedConfigDir = "/etc/opencode"

// openCodeManagedConfigPresent names the host's managed OpenCode configuration file when one
// exists with any content, or cannot be read. OpenCode merges it after the launch's own
// configuration, and it can name the provider, its address and the models in more ways than a
// reader here can follow (decoded escapes, {env:...} and {file:...} substitution, its own
// comment rules), so a session on a provider from Providers does not start on top of it at all.
// An empty file holds nothing.
func openCodeManagedConfigPresent() (string, bool) {
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		path := filepath.Join(openCodeManagedConfigDir, name)
		raw, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || len(bytes.TrimSpace(raw)) > 0 {
			return path, true
		}
	}
	return "", false
}

// openCodeConfineTo allows exactly one provider, disables the hosted one and sharing.
func openCodeConfineTo(cfg map[string]any, providerID string) {
	cfg["enabled_providers"] = []string{providerID}
	cfg["disabled_providers"] = []string{openCodeHostedProviderID}
	cfg["share"] = "disabled"
}

// openCodeLocalProviderConfig hands the child its local provider the way Codex
// gets it (driver_codex.go LaunchArgs): direct launch-time configuration that
// modifies no home. OpenCode's documented inline-config source is
// OPENCODE_CONFIG_CONTENT (opencode.ai/docs/config — merged over the global and
// project files for the keys it sets); the block is the OpenAI-compatible
// adapter pointed at <endpoint>/v1 with no key, the record's own contract.
//
// A session bound to the local endpoint never reaches a hosted model (FH 085,
// measured on OpenCode 1.18.34): OpenCode offers only the models its config lists,
// and its own default is a hosted OpenCode Zen model. So the endpoint's models are
// listed, the first is the default, and enabled_providers allows no other
// provider. A launch with no listed model is refused before this is built
// (runtime_provider_auth.go).
func openCodeLocalProviderConfig(endpoint string, models []string, model string) string {
	listed := map[string]any{}
	for _, name := range models {
		listed[name] = map[string]any{"name": name}
	}
	cfg := map[string]any{
		"provider": map[string]any{
			openCodeLocalProviderID: map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"name":    "Olivares Ollama (local)",
				"options": map[string]any{"baseURL": endpoint},
				"models":  listed,
			},
		},
	}
	openCodeConfineTo(cfg, openCodeLocalProviderID)
	endpointID := model == ""
	if endpointID && len(models) > 0 {
		model = models[0]
	}
	if model != "" {
		cfg["model"] = openCodeModelValue(BoundProvider{Kind: ProviderKindOllama}, model, endpointID)
		cfg["small_model"] = cfg["model"]
	}
	body, _ := json.Marshal(cfg)
	return string(body)
}

// openCodeSessionMCP encodes the existing session edge for ACP v1. Only the
// owned stdio request carries the bearer; argv and configuration files do not.
func openCodeSessionMCP(spec LaunchSpec) []json.RawMessage {
	if spec.SessionMCPURL == "" {
		return nil
	}
	token := ""
	for _, env := range spec.Env {
		if env.Name == spec.SessionMCPTokenEnv {
			token = env.Value
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"type": "http", "name": "olivares", "url": spec.SessionMCPURL,
		"headers": []map[string]string{{"name": "Authorization", "value": "Bearer " + token}},
	})
	return []json.RawMessage{raw}
}

func (openCodeDriver) OpenSession(cfg DriverSessionConfig) DriverSession {
	if cfg.sessionMCP == nil {
		cfg.sessionMCP = []json.RawMessage{}
	}
	s := &openCodeSession{acpSession: newACPSession(cfg, providerDriverOpenCode)}
	s.conn.onRequest = s.onServerRequest
	s.conn.onNotify = s.onNotification
	return s
}

type openCodeSession struct {
	acpSession
	caps acpAgentCapabilities
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

func (s *openCodeSession) Handshake(ctx context.Context) (DriverHandshake, error) {
	init, err := s.initialize(ctx)
	if err != nil {
		return DriverHandshake{}, err
	}
	if len(s.cfg.sessionMCP) > 0 && !init.AgentCapabilities.MCP.HTTP {
		return DriverHandshake{}, &runErr{http.StatusConflict, "OpenCode does not support the HTTP MCP connection required for this session"}
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
	raw, err := s.conn.call(ctx, acpMethodSessionNew, acpNewSessionParams{
		Cwd: s.workDir(), MCPServers: s.cfg.sessionMCP,
	}, s.cfg.CallTimeout)
	if err != nil {
		return DriverHandshake{}, s.conversationErr(err)
	}
	if err := acpRequireResultObject(raw); err != nil {
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
	case acpCapabilityPresent(caps.SessionCapabilities.Resume):
		method = acpMethodSessionResume
	case caps.LoadSession:
		method = acpMethodSessionLoad
	default:
		s.endReplay()
		return DriverHandshake{}, &runErr{
			http.StatusConflict,
			"the provider advertises neither session resume nor session load; refusing to start a different conversation",
		}
	}
	raw, err := s.resumeCall(ctx, method, resume)
	if err != nil {
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

func (s *openCodeSession) honorSettings(ctx context.Context, options []openCodeConfigOption) error {
	model := openCodeModelValue(s.cfg.BoundProvider, strings.TrimSpace(s.cfg.Model), false)
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
		// HU-R34: the refusal names what OpenCode does offer, so the next try can use it.
		return nil, &requestNotOffered{&runErr{
			http.StatusUnprocessableEntity,
			"OpenCode does not offer the " + configID + " " + strconv.Quote(clipCause(value)) + "; it offers: " +
				acpOfferedList(offered) + " (the session did not start rather than use a default)",
		}}
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
	if err := acpRequireResultObject(raw); err != nil {
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

func (s *openCodeSession) Shutdown(ctx context.Context) {
	s.mu.Lock()
	caps := s.caps
	s.mu.Unlock()
	s.shutdown(ctx, acpCapabilityPresent(caps.SessionCapabilities.Close))
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
