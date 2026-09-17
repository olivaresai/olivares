// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bufio"
	"encoding/json"
	"fmt"
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
	UnsupportedRequest     string `json:"unsupported_request"`
	SpawnChild             bool   `json:"spawn_child"`
	GroupedModel           bool   `json:"grouped_model"`
	OfferEffort            bool   `json:"offer_effort"`
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
}

var openCodeFixtureEnvValues = []string{
	"HOME", envOpenCodeConfigDir, envOpenCodeConfig, envXDGConfigHome, envXDGDataHome,
	envXDGStateHome, envXDGCacheHome, envXDGRuntimeDir, envGrokHome, envCodexHome,
	envClaudeConfigDir, envOpenCodeDisableAutoUpdate, "FIXTURE_MARKER",
}

var openCodeFixturePresence = []string{
	"OPENCODE_SERVER_PASSWORD", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "XAI_API_KEY",
	"OLIVARES_WORK_TOKEN", "OLIVARES_COMMUNICATION_TOKEN", "DISABLE_AUTOUPDATER",
	envOpenCodeConfig, envOpenCodeConfigContent,
}

type openCodeFixturePeer struct {
	cfg        openCodeFixture
	out        *json.Encoder
	mu         sync.Mutex
	rec        openCodeFixtureRecord
	session    string
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
		if method == openCodeMethodSessionCancel {
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

	switch method {
	case openCodeMethodInitialize:
		if raw, ok := params["clientCapabilities"]; ok {
			p.rec.ClientCaps = append(json.RawMessage(nil), raw...)
		}
		p.flush()
		p.reply(id, p.initializeResult())
	case "authenticate":
		p.replyError(id, openCodeErrMethodNotSupported, "the OpenCode fixture must not be asked to authenticate")
	case openCodeMethodSessionNew:
		cwd := ""
		if raw, ok := params["cwd"]; ok {
			_ = json.Unmarshal(raw, &cwd)
		}
		p.rec.NewCwds = append(p.rec.NewCwds, cwd)
		p.flush()
		p.waitFor(p.cfg.HoldNewPath)
		if p.cfg.NewSessionAuthRequired {
			p.replyError(id, openCodeErrAuthRequired, "provider authentication required")
			return
		}
		p.session = p.cfg.SessionID
		p.reply(id, map[string]any{"sessionId": p.session, "configOptions": p.configOptions("opencode/big-pickle", "")})
	case openCodeMethodSessionResume, openCodeMethodSessionLoad:
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
			p.sendUpdate(p.session, "agent_message_chunk", fmt.Sprintf("history-%d", i))
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
	case openCodeMethodSessionPrompt:
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
		p.onPrompt(id)
	case openCodeMethodSessionClose:
		p.reply(id, map[string]any{})
	default:
		p.replyError(id, openCodeErrMethodNotSupported, "the fixture peer does not implement "+method)
	}
}

func (p *openCodeFixturePeer) onPrompt(id json.RawMessage) {
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
	if p.cfg.PermissionOnTurn != "" {
		p.send(map[string]any{
			"jsonrpc": "2.0", "id": "srv-1", "method": openCodeReqRequestPermission,
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
		p.replyError(id, openCodeErrAuthRequired, "provider authentication required")
	case "hold":
		p.mu.Lock()
		p.heldPrompt = append(json.RawMessage(nil), id...)
		p.mu.Unlock()
		go func() {
			p.waitFor(p.cfg.PromptReleasePath)
			p.releaseHeldPrompt("end_turn")
		}()
	default:
		p.reply(id, map[string]any{"stopReason": "end_turn"})
	}
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
		version = openCodeProtocolVersion
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
	once := map[string]any{"optionId": "once", "name": "Allow once", "kind": openCodePermissionAllowOnce}
	always := map[string]any{"optionId": "always", "name": "Always allow", "kind": openCodePermissionAllowAlways}
	reject := map[string]any{"optionId": "reject", "name": "Reject", "kind": openCodePermissionRejectOnce}
	switch name {
	case "always-only":
		return []any{always, reject}
	case "duplicate":
		return []any{once, map[string]any{"optionId": "once", "name": "Allow once again", "kind": openCodePermissionAllowOnce}}
	default:
		return []any{once, always, reject}
	}
}

func (p *openCodeFixturePeer) sendUpdate(session, kind, text string) {
	p.send(map[string]any{
		"jsonrpc": "2.0", "method": openCodeNotifySessionUpdate,
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
