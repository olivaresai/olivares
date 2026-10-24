// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	openCodeFixtureConfigFile = "olivares-opencode-fixture.json"
	openCodeFixtureRecordFile = "olivares-opencode-fixture-record.json"
)

const openCodeFixtureHomeEnv = envOpenCodeConfigDir

type openCodeFixture struct {
	SessionID              string `json:"session_id"`
	ResumeSessionID        string `json:"resume_session_id"`
	ResumeEmptyObject      bool   `json:"resume_empty_object"`
	ResumeMissingResult    bool   `json:"resume_missing_result"`
	FailResume             bool   `json:"fail_resume"`
	NoResumeCapability     bool   `json:"no_resume_capability"`
	NoLoadSession          bool   `json:"no_load_session"`
	ProtocolVersion        int    `json:"protocol_version"`
	NewSessionAuthRequired bool   `json:"new_session_auth_required"`
	RecordPath             string `json:"record_path"`
	HoldNewPath            string `json:"hold_new_path"`
	PromptMode             string `json:"prompt_mode"`
	PromptReleasePath      string `json:"prompt_release_path"`
	PermissionOnTurn       string `json:"permission_on_turn"`
	ForeignUpdate          bool   `json:"foreign_update"`
	ReplayUpdates          int    `json:"replay_updates"`
	ReplayText             string `json:"replay_text"` // what each replayed update says ("" = "history-<n>")
	UnsupportedRequest     string `json:"unsupported_request"`
	SpawnChild             bool   `json:"spawn_child"`
	GroupedModel           bool   `json:"grouped_model"`
	OfferEffort            bool   `json:"offer_effort"`
	RequireMCPURL          string `json:"require_mcp_url"`
	ConnectMCP             bool   `json:"connect_mcp"`
	NoMCPHTTP              bool   `json:"no_mcp_http"`
	// EchoPrompt makes the peer send the prompt back as a user_message_chunk first,
	// as an agent that echoes the person's message would.
	EchoPrompt bool `json:"echo_prompt"`

	// PromptUsage is the usage object each prompt result carries (ACP PromptResponse.usage,
	// per turn), and UsageCost the cost each turn adds to the cumulative usage_update the
	// peer sends before that result, in UsageCurrency ("USD" when empty). OpenCode 1.19.3
	// sends both (measured on opencode/glm-5.3-flash-free: cost {"amount":0,"currency":"USD"}).
	PromptUsage   map[string]int64 `json:"prompt_usage"`
	UsageCost     *float64         `json:"usage_cost"`
	UsageCurrency string           `json:"usage_currency"`
	// HistoryCost is the cost a resumed conversation had already reached. As OpenCode
	// 1.19.3 does (acp/usage.ts totalSessionCost; resumeSession sends no usage_update),
	// the peer replays no cost on session/resume and counts it in every later total.
	HistoryCost float64 `json:"history_cost"`
	// UsageCostTotals, when set, are the cumulative totals the peer reports on its
	// turns in order (a total may go down, as for a reverted message).
	UsageCostTotals []float64 `json:"usage_cost_totals"`
	// FailCall makes the peer behave as OpenCode 1.19.3 does when its model call fails
	// upstream: it logs the error on stderr (with --print-logs, for the title helper
	// and for the conversation) and never answers the prompt until it is cancelled.
	FailCall string `json:"fail_call"`
	// FailCallRecovers makes the peer go on after logging the failure, as a retry would:
	// it sends output, then answers the turn after 2.5 s, or, with FailCallThen, logs that
	// second failure and waits like the first.
	FailCallRecovers bool   `json:"fail_call_recovers"`
	FailCallThen     string `json:"fail_call_then"`

	// WritePath makes the peer ask the client to write WriteContent to that path on
	// the turn, as OpenCode does after an approved edit; a relative path is joined to
	// the session folder the client named in session/new.
	WritePath    string `json:"write_path"`
	WriteContent string `json:"write_content"`
}

type openCodeFixtureRecord struct {
	PID          int               `json:"pid"`
	ChildPID     int               `json:"child_pid"`
	Argv         []string          `json:"argv"`
	Cwd          string            `json:"cwd"`
	Env          map[string]string `json:"env"`
	Present      map[string]bool   `json:"present"`
	Methods      []string          `json:"methods"`
	Envelopes    []string          `json:"envelopes"`
	Replies      []json.RawMessage `json:"replies"`
	NewCwds      []string          `json:"new_cwds"`
	PromptTexts  []string          `json:"prompt_texts"`
	ResumeIDs    []string          `json:"resume_ids"`
	ConfigIDs    []string          `json:"config_ids"`
	ConfigValues []string          `json:"config_values"`
	ClientCaps   json.RawMessage   `json:"client_caps"`
	MCPMethods   []string          `json:"mcp_methods"`
}

var openCodeFixtureEnvValues = []string{
	"HOME", envOpenCodeConfigDir, envOpenCodeConfig, envOpenCodeConfigContent, envXDGConfigHome, envXDGDataHome,
	envXDGStateHome, envXDGCacheHome, envXDGRuntimeDir, envGrokHome, envCodexHome,
	envClaudeConfigDir, envOpenCodeDisableAutoUpdate, "FIXTURE_MARKER",
	envOpenCodeDisableModelsFetch, envOpenCodeDisableLSPDownload, envNPMConfigOffline, envOpenCodeDisableShare,
}

var openCodeFixturePresence = []string{
	"OPENCODE_SERVER_PASSWORD", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "XAI_API_KEY",
	"OLIVARES_WORK_TOKEN", "OLIVARES_COMMUNICATION_TOKEN", "DISABLE_AUTOUPDATER",
	envOpenCodeConfig, envOpenCodeConfigContent,
}

type openCodeFixturePeer struct {
	cfg        openCodeFixture
	out        *json.Encoder
	mu         sync.Mutex
	rec        openCodeFixtureRecord
	session    string
	cwd        string
	heldPrompt json.RawMessage
}

func runOpenCodeFixturePeer() int {
	var cfg openCodeFixture
	home := os.Getenv(openCodeFixtureHomeEnv)
	if home == "" {
		fmt.Fprintln(os.Stderr, "opencode fixture peer: no configuration home was selected")
		return 2
	}
	if raw, err := os.ReadFile(filepath.Join(home, openCodeFixtureConfigFile)); err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			fmt.Fprintln(os.Stderr, "opencode fixture peer: bad configuration")
			return 2
		}
	}
	if cfg.SessionID == "" {
		cfg.SessionID = "fixture-session"
	}
	if cfg.RecordPath == "" {
		cfg.RecordPath = filepath.Join(home, openCodeFixtureRecordFile)
	}
	p := &openCodeFixturePeer{cfg: cfg, out: json.NewEncoder(os.Stdout)}
	cwd, _ := os.Getwd()
	p.rec = openCodeFixtureRecord{
		PID: os.Getpid(), Argv: append([]string(nil), os.Args[1:]...), Cwd: cwd,
		Env: map[string]string{}, Present: map[string]bool{},
	}
	for _, name := range openCodeFixtureEnvValues {
		if v, ok := os.LookupEnv(name); ok {
			p.rec.Env[name] = v
		}
	}
	for _, name := range openCodeFixturePresence {
		_, ok := os.LookupEnv(name)
		p.rec.Present[name] = ok
	}
	if cfg.SpawnChild {
		child := exec.Command(os.Args[0], "fixture-grandchild")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err == nil {
			p.rec.ChildPID = child.Process.Pid
		}
	}
	p.flush()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var frame map[string]json.RawMessage
		if err := json.Unmarshal(line, &frame); err != nil {
			continue
		}
		p.handle(frame)
	}
	return 0
}

func (p *openCodeFixturePeer) flush() {
	if p.cfg.RecordPath == "" {
		return
	}
	raw, err := json.MarshalIndent(p.rec, "", " ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p.cfg.RecordPath, raw, 0o600)
}

func (p *openCodeFixturePeer) send(v any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.out.Encode(v)
}

func (p *openCodeFixturePeer) handle(frame map[string]json.RawMessage) {
	var method string
	if raw, ok := frame["method"]; ok {
		_ = json.Unmarshal(raw, &method)
	}
	envelope := ""
	if raw, ok := frame["jsonrpc"]; ok {
		_ = json.Unmarshal(raw, &envelope)
	}
	id, hasID := frame["id"]
	p.rec.Methods = append(p.rec.Methods, method)
	p.rec.Envelopes = append(p.rec.Envelopes, envelope)
	if !hasID {
		if method == acpMethodSessionCancel {
			p.releaseHeldPrompt("cancelled")
		}
		p.flush()
		return
	}
	if method == "" {
		if raw, ok := frame["result"]; ok {
			p.rec.Replies = append(p.rec.Replies, append(json.RawMessage(nil), raw...))
		} else if raw, ok := frame["error"]; ok {
			p.rec.Replies = append(p.rec.Replies, append(json.RawMessage(nil), raw...))
		}
		p.flush()
		return
	}

	var params map[string]json.RawMessage
	if raw, ok := frame["params"]; ok {
		_ = json.Unmarshal(raw, &params)
	}
	p.flush()

	if p.cfg.RequireMCPURL != "" && (method == acpMethodSessionNew || method == acpMethodSessionResume || method == acpMethodSessionLoad) {
		var servers []struct {
			Type, Name, URL string
			Headers         []struct{ Name, Value string }
		}
		if json.Unmarshal(params["mcpServers"], &servers) != nil || len(servers) != 1 ||
			servers[0].Type != "http" || servers[0].Name != "olivares" || servers[0].URL != p.cfg.RequireMCPURL ||
			len(servers[0].Headers) != 1 || servers[0].Headers[0].Name != "Authorization" ||
			servers[0].Headers[0].Value != "Bearer "+os.Getenv("OLIVARES_HOOK_PEP_TOKEN") {
			p.replyError(id, -32602, "the session MCP connection is missing or unauthenticated")
			return
		}
		if p.cfg.ConnectMCP {
			req, _ := http.NewRequest(http.MethodPost, servers[0].URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", servers[0].Headers[0].Value)
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				p.replyError(id, -32603, "session MCP tools/list could not connect")
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				p.replyError(id, -32603, "session MCP tools/list was refused")
				return
			}
		}
		p.rec.MCPMethods = append(p.rec.MCPMethods, method)
		p.flush()
	}

	switch method {
	case acpMethodInitialize:
		if raw, ok := params["clientCapabilities"]; ok {
			p.rec.ClientCaps = append(json.RawMessage(nil), raw...)
		}
		p.flush()
		p.reply(id, p.initializeResult())
	case "authenticate":
		p.replyError(id, acpErrMethodNotSupported, "the OpenCode fixture must not be asked to authenticate")
	case acpMethodSessionNew:
		cwd := ""
		if raw, ok := params["cwd"]; ok {
			_ = json.Unmarshal(raw, &cwd)
		}
		p.rec.NewCwds = append(p.rec.NewCwds, cwd)
		p.cwd = cwd
		p.flush()
		p.waitFor(p.cfg.HoldNewPath)
		if p.cfg.NewSessionAuthRequired {
			p.replyError(id, acpErrAuthRequired, "provider authentication required")
			return
		}
		p.session = p.cfg.SessionID
		p.reply(id, map[string]any{"sessionId": p.session, "configOptions": p.configOptions("opencode/big-pickle", "")})
	case acpMethodSessionResume, acpMethodSessionLoad:
		requested := ""
		if raw, ok := params["sessionId"]; ok {
			_ = json.Unmarshal(raw, &requested)
		}
		p.rec.ResumeIDs = append(p.rec.ResumeIDs, requested)
		p.flush()
		if p.cfg.FailResume {
			p.replyError(id, -32001, "conversation unavailable")
			return
		}
		p.session = requested
		for i := 0; i < p.cfg.ReplayUpdates; i++ {
			text := fmt.Sprintf("history-%d", i)
			if p.cfg.ReplayText != "" {
				text = p.cfg.ReplayText
			}
			p.sendUpdate(p.session, "agent_message_chunk", text)
		}
		switch {
		case p.cfg.ResumeSessionID != "":
			p.session = p.cfg.ResumeSessionID
			p.reply(id, map[string]any{"sessionId": p.cfg.ResumeSessionID, "configOptions": p.configOptions("opencode/big-pickle", "")})
		case p.cfg.ResumeMissingResult:
			p.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id)})
		default:
			p.reply(id, map[string]any{})
		}
	case openCodeMethodSetConfigOption:
		configID, value := "", ""
		if raw, ok := params["configId"]; ok {
			_ = json.Unmarshal(raw, &configID)
		}
		if raw, ok := params["value"]; ok {
			_ = json.Unmarshal(raw, &value)
		}
		p.rec.ConfigIDs = append(p.rec.ConfigIDs, configID)
		p.rec.ConfigValues = append(p.rec.ConfigValues, value)
		p.flush()
		model, effort := "opencode/big-pickle", ""
		if configID == "model" {
			model = value
		}
		if configID == "effort" {
			effort = value
			model = "anthropic/claude-sonnet-4"
		}
		p.reply(id, map[string]any{"configOptions": p.configOptions(model, effort)})
	case acpMethodSessionPrompt:
		text := ""
		if raw, ok := params["prompt"]; ok {
			var blocks []struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(raw, &blocks) == nil && len(blocks) > 0 {
				text = blocks[0].Text
			}
		}
		p.rec.PromptTexts = append(p.rec.PromptTexts, text)
		p.flush()
		if p.cfg.EchoPrompt {
			p.sendUpdate(p.session, "user_message_chunk", text)
		}
		p.onPrompt(id)
	case acpMethodSessionClose:
		p.reply(id, map[string]any{})
	default:
		p.replyError(id, acpErrMethodNotSupported, "the fixture peer does not implement "+method)
	}
}

func (p *openCodeFixturePeer) onPrompt(id json.RawMessage) {
	if p.cfg.FailCall != "" {
		for _, line := range []string{
			`timestamp=2026-10-22T12:55:57.309Z level=ERROR run=8d38203a message="stream error" providerID=opencode modelID=gpt-5.4-nano session.id=` +
				p.session + ` small=true agent=title mode=primary error.error="AI_APICallError: Upstream request failed: Model access is disabled"`,
			`timestamp=2026-10-22T12:55:57.836Z level=ERROR run=8d38203a message="stream error" providerID=opencode modelID=glm-5.3-flash-free session.id=` +
				p.session + ` small=false agent=build mode=primary error.error="AI_APICallError: ` + p.cfg.FailCall + `"`,
		} {
			fmt.Fprintln(os.Stderr, line)
		}
		if p.cfg.FailCallRecovers {
			// The retry's output comes soon, and the turn runs on past the grace a test sets.
			time.Sleep(200 * time.Millisecond)
			p.sendUpdate(p.session, "agent_message_chunk", "after a retry")
			if p.cfg.FailCallThen == "" {
				time.Sleep(2500 * time.Millisecond)
				p.reply(id, map[string]any{"stopReason": "end_turn"})
				return
			}
			time.Sleep(300 * time.Millisecond) // the second failure comes after the retry's output
			fmt.Fprintln(os.Stderr, `timestamp=2026-10-22T12:55:59.000Z level=ERROR run=8d38203a message="stream error" providerID=opencode modelID=glm-5.3-flash-free session.id=`+
				p.session+` small=false agent=build mode=primary error.error="AI_APICallError: `+p.cfg.FailCallThen+`"`)
		}
		p.mu.Lock()
		p.heldPrompt = append(json.RawMessage(nil), id...)
		p.mu.Unlock()
		return
	}
	p.sendUpdate(p.session, "agent_message_chunk", "live output")
	if p.cfg.ForeignUpdate {
		p.sendUpdate("some-other-conversation", "agent_message_chunk", "not ours")
	}
	if p.cfg.UnsupportedRequest != "" {
		p.send(map[string]any{
			"jsonrpc": "2.0", "id": "srv-cap", "method": p.cfg.UnsupportedRequest,
			"params": map[string]any{"sessionId": p.session, "path": "/etc/hostname"},
		})
	}
	if p.cfg.WritePath != "" {
		path := p.cfg.WritePath
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.cwd, path)
		}
		p.send(map[string]any{
			"jsonrpc": "2.0", "id": "srv-write", "method": "fs/write_text_file",
			"params": map[string]any{"sessionId": p.session, "path": path, "content": p.cfg.WriteContent},
		})
	}
	if p.cfg.PermissionOnTurn != "" {
		p.send(map[string]any{
			"jsonrpc": "2.0", "id": "srv-1", "method": acpReqRequestPermission,
			"params": map[string]any{
				"sessionId": p.session,
				"toolCall":  map[string]any{"toolCallId": "call-1", "kind": "edit"},
				"options":   openCodeFixturePermissionOptions(p.cfg.PermissionOnTurn),
			},
		})
	}
	switch p.cfg.PromptMode {
	case "never":
		return
	case "auth":
		p.replyError(id, acpErrAuthRequired, "provider authentication required")
	case "hold":
		p.mu.Lock()
		p.heldPrompt = append(json.RawMessage(nil), id...)
		p.mu.Unlock()
		go func() {
			p.waitFor(p.cfg.PromptReleasePath)
			p.releaseHeldPrompt("end_turn")
		}()
	default:
		p.sendUsage()
		result := map[string]any{"stopReason": "end_turn"}
		if p.cfg.PromptUsage != nil {
			result["usage"] = p.cfg.PromptUsage
		}
		p.reply(id, result)
	}
}

// sendUsage sends the session's cumulative cost after this many prompts, as OpenCode's
// usage_update does before the turn's result.
func (p *openCodeFixturePeer) sendUsage() {
	p.mu.Lock()
	turns := len(p.rec.PromptTexts)
	p.mu.Unlock()
	switch {
	case len(p.cfg.UsageCostTotals) >= turns && turns > 0:
		p.sendCost(p.cfg.UsageCostTotals[turns-1])
	case p.cfg.UsageCost != nil:
		p.sendCost(p.cfg.HistoryCost + *p.cfg.UsageCost*float64(turns))
	}
}

func (p *openCodeFixturePeer) sendCost(amount float64) {
	currency := p.cfg.UsageCurrency
	if currency == "" {
		currency = "USD"
	}
	p.send(map[string]any{
		"jsonrpc": "2.0", "method": acpNotifySessionUpdate,
		"params": map[string]any{
			"sessionId": p.session,
			"update": map[string]any{
				"sessionUpdate": "usage_update", "used": 2994, "size": 200000,
				"cost": map[string]any{"amount": amount, "currency": currency},
			},
		},
	})
}

func (p *openCodeFixturePeer) releaseHeldPrompt(reason string) {
	p.mu.Lock()
	id := p.heldPrompt
	p.heldPrompt = nil
	p.mu.Unlock()
	if id == nil {
		return
	}
	p.reply(id, map[string]any{"stopReason": reason})
}

func (p *openCodeFixturePeer) initializeResult() map[string]any {
	version := p.cfg.ProtocolVersion
	if version == 0 {
		version = acpProtocolVersion
	}
	sessionCaps := map[string]any{"list": map[string]any{}, "close": map[string]any{}}
	if !p.cfg.NoResumeCapability {
		sessionCaps["resume"] = map[string]any{}
	}
	return map[string]any{
		"protocolVersion": version,
		"agentCapabilities": map[string]any{
			"loadSession":         !p.cfg.NoLoadSession,
			"sessionCapabilities": sessionCaps,
			"mcpCapabilities":     map[string]any{"http": !p.cfg.NoMCPHTTP},
		},
		"authMethods": []any{
			map[string]any{"id": "opencode-login", "name": "Login with opencode"},
		},
		"agentInfo": map[string]any{"name": "OpenCode", "version": "1.18.30-fixture"},
	}
}

func (p *openCodeFixturePeer) configOptions(model, effort string) []any {
	opts := []any{
		map[string]any{
			"id": "model", "name": "Model", "type": "select", "category": "model",
			"currentValue": model,
			"options": []any{
				map[string]any{"value": "opencode/big-pickle", "name": "Big Pickle"},
				map[string]any{
					"name": "Anthropic",
					"options": []any{
						map[string]any{"value": "anthropic/claude-sonnet-4", "name": "Sonnet 4"},
					},
				},
			},
		},
	}
	if p.cfg.OfferEffort || effort != "" || model == "anthropic/claude-sonnet-4" {
		if effort == "" {
			effort = "medium"
		}
		opts = append(opts, map[string]any{
			"id": "effort", "name": "Effort", "type": "select", "category": "thought_level",
			"currentValue": effort,
			"options": []any{
				map[string]any{"value": "low", "name": "Low"},
				map[string]any{"value": "medium", "name": "Medium"},
				map[string]any{"value": "high", "name": "High"},
			},
		})
	}
	return opts
}

func openCodeFixturePermissionOptions(name string) []any {
	once := map[string]any{"optionId": "once", "name": "Allow once", "kind": acpPermissionAllowOnce}
	always := map[string]any{"optionId": "always", "name": "Always allow", "kind": acpPermissionAllowAlways}
	reject := map[string]any{"optionId": "reject", "name": "Reject", "kind": acpPermissionRejectOnce}
	switch name {
	case "always-only":
		return []any{always, reject}
	case "duplicate":
		return []any{once, map[string]any{"optionId": "once", "name": "Allow once again", "kind": acpPermissionAllowOnce}}
	default:
		return []any{once, always, reject}
	}
}

func (p *openCodeFixturePeer) sendUpdate(session, kind, text string) {
	p.send(map[string]any{
		"jsonrpc": "2.0", "method": acpNotifySessionUpdate,
		"params": map[string]any{
			"sessionId": session,
			"update": map[string]any{
				"sessionUpdate": kind,
				"content":       map[string]any{"type": "text", "text": text},
			},
		},
	})
}

func (p *openCodeFixturePeer) waitFor(path string) {
	if path == "" {
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (p *openCodeFixturePeer) reply(id json.RawMessage, result any) {
	p.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func (p *openCodeFixturePeer) replyError(id json.RawMessage, code int, message string) {
	p.send(map[string]any{
		"jsonrpc": "2.0", "id": json.RawMessage(id),
		"error": map[string]any{"code": code, "message": message},
	})
}

func setOpenCodeFixture(t *testing.T, prof ProviderProfile, cfg openCodeFixture) string {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("fixture config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prof.ConfigHome, openCodeFixtureConfigFile), raw, 0o600); err != nil {
		t.Fatalf("write the fixture configuration: %v", err)
	}
	if cfg.RecordPath != "" {
		return cfg.RecordPath
	}
	return filepath.Join(prof.ConfigHome, openCodeFixtureRecordFile)
}

func readOpenCodeFixtureRecord(t *testing.T, path string) openCodeFixtureRecord {
	t.Helper()
	var rec openCodeFixtureRecord
	waitFor(t, "the opencode fixture peer wrote its record", func() bool {
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 {
			return false
		}
		return json.Unmarshal(raw, &rec) == nil
	})
	return rec
}

func tryReadOpenCodeFixtureRecord(path string) (openCodeFixtureRecord, bool) {
	var rec openCodeFixtureRecord
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return rec, false
	}
	return rec, json.Unmarshal(raw, &rec) == nil
}
