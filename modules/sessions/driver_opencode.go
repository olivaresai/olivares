// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/olivaresai/olivares/core/driverfacts"
)

// The official OpenCode ACP driver (CLI v1.18.30), over ACP on the owned child's
// stdio. Construction: an internal design note (not shipped)
//
// WIRE. NDJSON over stdio, JSON-RPC 2.0 WITH the `"jsonrpc":"2.0"` member. The
// existing owned rpcConn is the transport; this file does not introduce a second
// RPC framework, an HTTP client, or a new schema.
//
// OWNERSHIP. The spawn is always `acp --hostname 127.0.0.1 --print-logs --log-level ERROR [--cwd <WorkDir>]` as
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
	// --print-logs --log-level ERROR is the tool's own switch that writes its errors to
	// stderr: a failed model call is said nowhere else (1.19.3 sends no ACP update and
	// never ends the prompt), and openCodeSession.Deliver reads it from there (#2937).
	args := []string{"acp", "--hostname", "127.0.0.1", "--print-logs", "--log-level", "ERROR"}
	if dir := strings.TrimSpace(l.WorkDir); dir != "" {
		args = append(args, "--cwd", dir)
	}
	if l.OwnNetwork {
		args = append(args, "--port", strconv.Itoa(openCodeACPPort))
	}
	return args
}

// openCodeACPPort is where OpenCode's ACP mode serves the API it calls itself (its
// own default, 1.19.4). Behind the session proxy it is pinned and is the one port
// the proxy environment exempts (egress.Policy.LoopbackPorts): Bun sends every
// other request, loopback included, to the proxy, and matches NO_PROXY by host and
// port (measured, #3082).
const openCodeACPPort = 4096

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
	env := []EnvVar{{Name: envOpenCodeDisableAutoUpdate, Value: "1"}, {Name: envOpenCodeDisableExternalSkills, Value: "1"}}
	cfg := map[string]any{}
	bound := true
	switch {
	case l.LocalModelEndpoint != "":
		_ = json.Unmarshal([]byte(openCodeLocalProviderConfig(openCodeOllama, l.LocalModelEndpoint, l.LocalModels, l.Model)), &cfg)
	case l.BoundProvider.Kind == ProviderKindOpenAICompatible:
		_ = json.Unmarshal([]byte(openCodeLocalProviderConfig(openCodeLocalServer, l.BoundProvider.Endpoint, l.LocalModels, l.Model)), &cfg)
	case l.BoundProvider.Kind != "":
		openCodeConfineToKey(cfg, l.BoundProvider, l.Model)
	default:
		// The tool's own login (#2888) has no record to pin. It stays on the provider
		// its model names and shares nothing. It keeps OpenCode's model catalog,
		// which is what its model picker lists.
		bound = false
		if provider := openCodeModelProvider(l.Model); provider != "" {
			cfg["enabled_providers"] = []string{provider}
		}
		cfg["share"] = "disabled"
	}
	if bound {
		// npm_config_offline reaches every command OpenCode's shell runs, the agent's
		// npm install included; the own login keeps npm online (review of #3053).
		env = append(env, EnvVar{Name: envOpenCodeDisableModelsFetch, Value: "1"},
			EnvVar{Name: envNPMConfigOffline, Value: "true"})
	}
	env = append(env,
		EnvVar{Name: envOpenCodeDisableLSPDownload, Value: "1"},
		EnvVar{Name: envOpenCodeDisableShare, Value: "1"})
	// The external scans are off (envOpenCodeDisableExternalSkills); the session
	// folder's own two skill folders are listed again, relative to that folder.
	cfg["skills"] = map[string]any{"paths": []string{".claude/skills", ".agents/skills"}}
	permission := ""
	switch l.Preset {
	case PresetReadOnly:
		permission = "deny"
	case PresetAsk, PresetEditsOnly, PresetEditsAndCommands, PresetFull:
		permission = "ask"
	}
	if permission != "" {
		// Keep edits and commands asking so live policy can still deny or ask.
		// ACP approval is answered at once when that policy permits it.
		rules := map[string]any{"*": permission, "bash": permission, "edit": permission,
			"read": map[string]string{"*": "ask", "*.env": "deny", "*.env.*": "deny", "*.env.example": "ask",
				"mcp:*": "allow", "mcp:*.env": "deny", "mcp:*.env.*": "deny"},
			"glob": "ask", "grep": "ask", "list": "ask",
			// It starts no subagent: see the global rules below.
			"task": "deny"}
		// Only the build agent, pinned below with its own rules (agent configuration
		// takes precedence over the global rules), may ask. Its read asks (#2890): live
		// policy reviews it as a read, which every preset allows, and audits it; the file
		// comes from the tool_call_update OpenCode sends just before the request
		// (acpSession.toolFacts). OpenCode's MCP resource reads ask "read" with
		// mcp:<server>:<uri> patterns and name no file: they stay allowed. Glob and grep
		// (and list, which 1.19.4 does not offer) ask too, decided and audited as reads of
		// the folder they search (#3058).
		// Every other agent, a subagent above all, has the global rules, which deny
		// everything (#3083): OpenCode's ACP agent drops an ask from a session it did not
		// create, so a subagent's ask is never answered and its turn waits forever, and
		// what it allows goes unaudited (measured on 1.19.4). With every tool denied,
		// OpenCode offers a subagent none and it ends at once.
		cfg["permission"] = map[string]any{"*": "deny"}
		build := rules
		cfg["default_agent"] = "build"
		cfg["agent"] = map[string]any{"build": map[string]any{"permission": build}}
	}
	if len(cfg) > 0 {
		body, _ := json.Marshal(cfg)
		env = append(env, EnvVar{Name: envOpenCodeConfigContent, Value: string(body)})
	}
	return env
}

// openCodeLocalProvider is how a local model endpoint appears in OpenCode's configuration:
// the provider id its models are qualified with, its title, and the variable that carries
// its key ("" when the endpoint takes none).
type openCodeLocalProvider struct{ id, title, keyEnv string }

// openCodeLocalProviderID names the Ollama provider in OpenCode's configuration.
const openCodeLocalProviderID = "olivares_ollama"

var (
	openCodeOllama = openCodeLocalProvider{id: openCodeLocalProviderID, title: "Olivares Ollama (local)"}
	// openCodeLocalServer is any other local OpenAI-compatible server (vLLM, llama.cpp), which
	// holds the key Olivares gave it.
	openCodeLocalServer = openCodeLocalProvider{id: "olivares_local", title: "Olivares local model server", keyEnv: "OPENAI_API_KEY"}
)

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
//     registry.npmjs.org into every configuration directory (it fails and is logged).
//     OpenCode's shell runs the agent's commands with the same environment, so their
//     npm is offline too; the own login, which has no record, does not set it;
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

// envOpenCodeDisableExternalSkills stops the .claude/skills and .agents/skills scans
// under HOME and up every ancestor of the session folder. Measured on 1.19.3: in a
// folder that is not a git repository the walk reaches /, and every skill folder it
// finds gets external_directory: allow (#2997). OpenCode's own .opencode folders are
// still scanned.
const envOpenCodeDisableExternalSkills = "OPENCODE_DISABLE_EXTERNAL_SKILLS"

// openCodeOwnLoginHosts are the hosts a session on the tool's own login needs, by
// the provider its model names; the first is the provider, the rest the hosts it
// also calls. Measured on 1.19.4 behind a refusing proxy with the own-login
// switches: Zen and Go reach opencode.ai; the ChatGPT login reaches chatgpt.com and
// refreshes an expired token at auth.openai.com; each reads OpenCode's model catalog
// at models.opencode.ai, which the own login keeps (LaunchEnv).
var openCodeOwnLoginHosts = map[string][]string{
	openCodeHostedProviderID: {"https://opencode.ai", "https://models.opencode.ai"},
	"opencode-go":            {"https://opencode.ai", "https://models.opencode.ai"},
	"openai":                 {"https://chatgpt.com", "https://auth.openai.com", "https://models.opencode.ai"},
}

// openCodeConfiguredModel is the model the account's OpenCode configuration sets in
// OPENCODE_CONFIG_DIR, which OpenCode uses when a launch names none: opencode.jsonc
// over opencode.json, as OpenCode loads them there (1.19.4). "" when none can be
// read: a file that is not a small regular file (a symlink included), or a .jsonc
// that is not plain JSON, reads as unknown, and the launch then names its model.
func openCodeConfiguredModel(configHome string) string {
	if !filepath.IsAbs(configHome) {
		return ""
	}
	root, err := os.OpenRoot(configHome)
	if err != nil {
		return ""
	}
	defer root.Close()
	for _, name := range []string{"opencode.jsonc", "opencode.json"} {
		b, err := readSmallRegularFile(root, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		var cfg struct {
			Model *string `json:"model"`
		}
		if err != nil || json.Unmarshal(b, &cfg) != nil {
			return ""
		}
		if cfg.Model == nil {
			continue
		}
		// A model id is short; a longer value is not one to echo in a refusal.
		if model := strings.TrimSpace(*cfg.Model); len(model) <= 256 {
			return model
		}
		return ""
	}
	return ""
}

// readSmallRegularFile reads a regular file of at most 1 MiB in root without
// following a symlink or blocking on a FIFO (openStateFile): the account's
// configuration folder is one a session may write.
func readSmallRegularFile(root *os.Root, name string) ([]byte, error) {
	f, err := openStateFile(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const limit = 1 << 20
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("not a small regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && len(b) > limit {
		return nil, errors.New("not a small regular file")
	}
	return b, err
}

// openCodeModelProvider is the provider a "provider/model" id names; "" for an id
// that names none.
func openCodeModelProvider(model string) string {
	provider, _, ok := strings.Cut(model, "/")
	if !ok {
		return ""
	}
	return provider
}

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
	switch b.Kind {
	case ProviderKindOllama:
		id = openCodeOllama.id
	case ProviderKindOpenAICompatible:
		id = openCodeLocalServer.id
	}
	return qualifyOpenCodeModel(id, model, endpointID)
}

func qualifyOpenCodeModel(id, model string, endpointID bool) string {
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
//
// A server that holds a key gets it by reference, {env:<variable>}: the variable is in the
// child's environment (providerRecordEnv) and the configuration, which other tools may
// log, never carries the value.
func openCodeLocalProviderConfig(p openCodeLocalProvider, endpoint string, models []string, model string) string {
	listed := map[string]any{}
	for _, name := range models {
		listed[name] = map[string]any{"name": name}
	}
	options := map[string]any{"baseURL": endpoint}
	if p.keyEnv != "" {
		options["apiKey"] = "{env:" + p.keyEnv + "}"
	}
	cfg := map[string]any{
		"provider": map[string]any{
			p.id: map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"name":    p.title,
				"options": options,
				"models":  listed,
			},
		},
	}
	openCodeConfineTo(cfg, p.id)
	endpointID := model == ""
	if endpointID && len(models) > 0 {
		model = models[0]
	}
	if model != "" {
		cfg["model"] = qualifyOpenCodeModel(p.id, model, endpointID)
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
		if env.Name == spec.SessionMCPTokenEnv && env.Value != "" {
			token = env.Value
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"type": "http", "name": sessionMCPServerName, "url": spec.SessionMCPURL,
		"headers": []map[string]string{{"name": "Authorization", "value": "Bearer " + token}},
	})
	return []json.RawMessage{raw}
}

func (openCodeDriver) OpenSession(cfg DriverSessionConfig) DriverSession {
	s := &openCodeSession{acpSession: newACPSession(cfg, providerDriverOpenCode)}
	s.conn.onRequest = s.onServerRequest
	s.conn.onNotify = s.onNotification
	return s
}

type openCodeSession struct {
	acpSession
	caps acpAgentCapabilities
	// failedCalls counts the failed model calls OpenCode logged: only the latest one's
	// wait may stop its turn, so a retry that fails again is waited for afresh.
	failedCalls uint64
}

// takeStderr takes OpenCode's own "stream error" log lines (--print-logs): a failed
// model call for this conversation stops the turn and says why, and a small
// background call's failure (the title) is the tool's own business. Every other line
// is published as before, so a launch failure's reason still reaches the run.
func (s *openCodeSession) takeStderr(line []byte) bool {
	fields := openCodeLogFields(string(line))
	if fields["level"] != "ERROR" || fields["message"] != "stream error" {
		return false
	}
	if reason, ok := openCodeFailedCall(fields, s.ConversationID()); ok {
		s.observeFailedCall(reason)
	} else if fields["small"] != "true" {
		s.warn("sessions: OpenCode logged a failed model call that is not this conversation's", "run_ref", s.cfg.RunRef)
	}
	return true
}

// openCodeLogLine is the shape of OpenCode's log lines (1.19.3: "timestamp=... level=...").
var openCodeLogLine = regexp.MustCompile(`^timestamp=\S+ level=[A-Z]+ `)

// openCodeFailedCallGrace is how long a turn whose model call failed may still
// produce output (OpenCode retrying) before it is stopped.
var openCodeFailedCallGrace = 15 * time.Second

// observeFailedCall cancels the turn in flight when its model call failed and the tool
// shows nothing more for it: OpenCode 1.19.3 never ends that prompt (measured with
// "Free usage limit reached ..."). A turn that goes on (the tool retried) is left alone
// until a later failure; a turn already being ended (an interrupt, a stop) is left to
// that. The session is told why, and whether the cancel went out.
func (s *openCodeSession) observeFailedCall(reason string) {
	s.mu.Lock()
	turn, live := s.promptID, s.updates.live
	s.failedCalls++
	call := s.failedCalls
	s.mu.Unlock()
	if turn == "" {
		s.warn("sessions: OpenCode logged a failed model call outside a turn", "run_ref", s.cfg.RunRef)
		return
	}
	time.AfterFunc(openCodeFailedCallGrace, func() {
		s.mu.Lock()
		stalled := call == s.failedCalls && s.promptID == turn && s.updates.live == live &&
			s.cancelledTurn != turn && !s.closed
		s.mu.Unlock()
		if !stalled {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.callTimeout())
		defer cancel()
		err := s.interruptTurn(ctx, turn)
		if err != nil {
			s.warn("sessions: a turn whose model call failed could not be cancelled", "run_ref", s.cfg.RunRef, "err", err)
		}
		if s.cfg.OnTurnFailed != nil {
			s.cfg.OnTurnFailed(reason, err == nil)
		}
	})
}

// openCodeLogField is one key=value pair of OpenCode's log line, the value quoted or bare.
var openCodeLogField = regexp.MustCompile(`([A-Za-z0-9_.]+)=("(?:[^"\\]|\\.)*"|\S+)`)

// openCodeErrorClass is the error class OpenCode puts before its message.
var openCodeErrorClass = regexp.MustCompile(`^[A-Za-z_]*Error: `)

// openCodeLogFields reads one OpenCode log line as its key=value pairs (values
// unquoted); the first of a repeated key wins. A line of another shape has none.
func openCodeLogFields(line string) map[string]string {
	if !openCodeLogLine.MatchString(line) {
		return nil
	}
	fields := map[string]string{}
	for _, m := range openCodeLogField.FindAllStringSubmatch(line, -1) {
		if _, seen := fields[m[1]]; seen {
			continue
		}
		v := m[2]
		if u, err := strconv.Unquote(v); err == nil {
			v = u
		}
		fields[m[1]] = v
	}
	return fields
}

// openCodeFailedCall is the reason of OpenCode's own "stream error" for this
// conversation's main call. A small background call (the title) failing is not the
// turn failing.
func openCodeFailedCall(fields map[string]string, conversation string) (string, bool) {
	if conversation == "" || fields["level"] != "ERROR" || fields["message"] != "stream error" ||
		fields["session.id"] != conversation || fields["small"] == "true" {
		return "", false
	}
	// The JavaScript error class ("AI_APICallError: ") says nothing to a person.
	reason := clipCause(openCodeErrorClass.ReplaceAllString(fields["error.error"], ""))
	if reason == "" {
		reason = "no reason given"
	}
	return reason, true
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
	if err := acpRequireSessionMCP("OpenCode", s.cfg.sessionMCP, init.AgentCapabilities.MCP.HTTP); err != nil {
		return DriverHandshake{}, err
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
	s.beginReplay(resume)
	s.mu.Unlock()

	method := ""
	switch {
	case acpCapabilityPresent(caps.SessionCapabilities.Resume):
		method = acpMethodSessionResume
	case caps.LoadSession:
		method = acpMethodSessionLoad
	default:
		s.answerReplay() // nothing was asked, so nothing can replay
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

// Input refuses a /command whose text has a backtick, $ or @. OpenCode expands
// the arguments into the command's template and then runs each !`shell` and
// reads each @file, before and despite the session's bash, read and
// external_directory rules (measured over ACP on 1.19.3). A single character is
// enough: a typed backtick closes an opener the template leaves open, arguments
// are joined after the split, and OpenCode reads $ in them as replacement
// patterns. It finds the command after trimming any leading Unicode space or BOM.
// A placeholder next to !, ` or @ in a delivered template is refused at delivery
// (openCodeComponent). A plain prompt is not expanded, so it is sent as typed.
// openCodeSubtaskCommand is OpenCode's one built-in command that runs as a subagent.
const openCodeSubtaskCommand = "/review"

func (s *openCodeSession) Input(ctx context.Context, text string) (bool, error) {
	command := strings.TrimLeftFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
	if strings.HasPrefix(command, "/") && strings.ContainsAny(command, "`$@") {
		return false, badRequest("OpenCode expands a command's arguments into shell interpolation (!`...`) and file references (@path) outside this session's approvals; remove backticks, $ and @ from them, or send the text without the leading /command")
	}
	// OpenCode's built-in /review is a subtask command (1.19.4): it runs as a subagent
	// whatever the task rule says, and OpenCode never passes that subagent's approval
	// requests to Olivares, so its turn would wait forever (#3083).
	if name := strings.FieldsFunc(command, unicode.IsSpace); len(name) > 0 && name[0] == openCodeSubtaskCommand {
		return false, badRequest("OpenCode runs /review as a subagent whose approval requests never reach Olivares; ask for the review in your own words, without the leading /review")
	}
	return s.acpSession.Input(ctx, text)
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

// prepareOpenCodeSearch puts the installed ripgrep's folder first on an OpenCode
// child's PATH and lets a confined child run it (#3173). Without it, glob and grep
// fail in a confined session: OpenCode would download rg from github.com. A node
// with no ripgrep installed launches as before. An rg that is no longer the pinned
// program (something wrote over it) refuses the launch with the fix that works:
// installing OpenCode again replaces it (#3202).
func (m *Module) prepareOpenCodeSearch(p CreateRunParams, spec *LaunchSpec) error {
	dir := m.rt.openCodeSearchDir
	if launchDriverKey(p) != providerDriverOpenCode || dir == "" {
		return nil
	}
	rg := filepath.Join(dir, "rg")
	info, err := os.Lstat(rg)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	damaged := err != nil || !info.Mode().IsRegular()
	if !damaged {
		sum, herr := fileSHA256(rg)
		damaged = herr != nil || sum != m.rt.openCodeRipgrepSHA256
	}
	if damaged {
		// The reply names the fix, not the server's path; the log names the file.
		m.warnf("sessions: the installed ripgrep is not the pinned program; OpenCode launches are refused until `olivares tool install opencode` replaces it", "path", rg)
		return conflictErr("the ripgrep OpenCode's glob and grep run on this server is not the pinned program; run `olivares tool install opencode` to replace it")
	}
	path := os.Getenv("PATH")
	for _, e := range spec.Env {
		if e.Name == "PATH" {
			path = e.Value
		}
	}
	if path != "" {
		path = string(os.PathListSeparator) + path
	}
	setLaunchEnv(spec, "PATH", dir+path)
	spec.AllowRead(dir)
	return nil
}

// fileSHA256 is the hex sha256 of a file's content.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
