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

// The FIXTURE ACP PEER — a real program, not an in-process double.
//
// It exists for the same reason its Codex counterpart does: the rows it supports
// are claims about an OPERATING-SYSTEM PROCESS. The child gets these homes and
// not that credential; a completed turn leaves the process alive; an interrupt
// leaves it usable; a stop reaps its process GROUP including a grandchild holding
// the inherited stdout. A fake Process satisfies all of them by construction and
// proves none of them.
//
// It speaks the ACP frames this driver implements and nothing else: NDJSON,
// `"jsonrpc":"2.0"` present, correlated responses. It never contacts a network,
// never reads a real account home, and answers no method the driver does not
// send. Every credential-bearing name it sees is recorded as PRESENT or ABSENT,
// never by value.
//
// ⛔ IT IS SELECTED BY ARGV, and that is the point: the test binary is registered
// as the `grok` program, and TestMain (driver_codex_fixture_test.go, one per
// package) hands a process invoked as `agent …` to this peer. The driver's real
// argv is therefore what routes it, so an argv regression breaks every row here.

const (
	grokFixtureConfigFile = "olivares-grok-fixture.json"
	grokFixtureRecordFile = "olivares-grok-fixture-record.json"
)

// grokFixtureHomeEnv is the variable the peer reads its home from: the driver's
// own configuration-home variable, which is the one thing the runtime guarantees
// a grok child receives on BOTH a create and a resume. A resume rebuilds the
// child's environment from the run row, which carries no env_allow — a fixture
// configured through the environment would silently reconfigure itself there, and
// the resume rows are exactly the ones this suite must be able to trust.
const grokFixtureHomeEnv = envGrokHome

// grokFixture configures one peer run.
type grokFixture struct {
	// SessionID is the conversation the peer nominates on session/new.
	SessionID string `json:"session_id"`
	// ResumeSessionID overrides what session/resume answers ("" ⇒ answer no id at
	// all, which is the protocol's own legal success).
	ResumeSessionID string `json:"resume_session_id"`
	// ResumeEmptyObject answers session/resume with `{}` instead of a null result.
	ResumeEmptyObject bool `json:"resume_empty_object"`
	// FailResume answers session/resume (or session/load) with a protocol error.
	FailResume bool `json:"fail_resume"`
	// AuthMethods is what initialize advertises ("" ⇒ the two noninteractive ids
	// plus the browser one; "browser" ⇒ only grok.com; "none" ⇒ an empty list).
	AuthMethods string `json:"auth_methods"`
	// FailAuthenticate answers authenticate with a protocol error.
	FailAuthenticate bool `json:"fail_authenticate"`
	// NoResumeCapability drops `resume` from the advertised session capabilities.
	NoResumeCapability bool `json:"no_resume_capability"`
	// NoLoadSession drops `loadSession` from the advertised agent capabilities.
	NoLoadSession bool `json:"no_load_session"`
	// ProtocolVersion overrides the answered protocol version (0 ⇒ 1).
	ProtocolVersion int `json:"protocol_version"`
	// NewSessionAuthRequired answers session/new with -32000, the way an
	// unauthenticated agent does.
	NewSessionAuthRequired bool `json:"new_session_auth_required"`
	// RecordPath is where the peer writes what it saw. Never a secret VALUE.
	RecordPath string `json:"record_path"`
	// HoldNewPath makes the peer wait for that file before answering session/new,
	// so a test can observe the launch WHILE the handshake is open.
	HoldNewPath string `json:"hold_new_path"`
	// PromptMode decides what a session/prompt does:
	//   ""         answer end_turn immediately;
	//   "hold"     answer only after the release file appears, or after a cancel;
	//   "never"    never answer at all (the delayed-result row);
	//   "auth"     answer with the -32000 authentication error.
	PromptMode string `json:"prompt_mode"`
	// PromptReleasePath releases a held prompt.
	PromptReleasePath string `json:"prompt_release_path"`
	// PermissionOnTurn asks for a tool permission after receiving a prompt, with
	// the named option catalogue (see grokFixturePermissionOptions).
	PermissionOnTurn string `json:"permission_on_turn"`
	// ForeignUpdate emits a session/update naming ANOTHER conversation after the
	// prompt arrives.
	ForeignUpdate bool `json:"foreign_update"`
	// ReplayUpdates is how many session/update frames the peer replays BEFORE
	// answering session/resume.
	ReplayUpdates int `json:"replay_updates"`
	// UnsupportedRequest asks the client for a capability it never advertised.
	UnsupportedRequest string `json:"unsupported_request"`
	// SpawnChild forks a grandchild that inherits stdout, so a stop has something
	// to reap that a direct SIGTERM would not reach.
	SpawnChild bool `json:"spawn_child"`
}

// grokFixtureRecord is what the peer observed.
type grokFixtureRecord struct {
	PID      int               `json:"pid"`
	ChildPID int               `json:"child_pid"`
	Argv     []string          `json:"argv"`
	Cwd      string            `json:"cwd"`
	Env      map[string]string `json:"env"`
	Present  map[string]bool   `json:"present"`
	Methods  []string          `json:"methods"`
	// Envelopes records whether each inbound frame carried `"jsonrpc":"2.0"`.
	Envelopes []string `json:"envelopes"`
	// Replies is what the driver answered to the peer's OWN requests, verbatim, so
	// a test can assert the exact codec bytes.
	Replies []json.RawMessage `json:"replies"`
	// NewCwds / PromptTexts / AuthMethodIDs / ResumeIDs are the request fields a
	// test needs to prove reached the provider.
	NewCwds       []string `json:"new_cwds"`
	PromptTexts   []string `json:"prompt_texts"`
	AuthMethodIDs []string `json:"auth_method_ids"`
	ResumeIDs     []string `json:"resume_ids"`
	// ClientCaps is the capabilities object the client advertised on initialize.
	ClientCaps json.RawMessage `json:"client_caps"`
}

// grokFixtureEnvValues are the NON-SECRET names whose values the peer records.
var grokFixtureEnvValues = []string{
	"HOME", envGrokHome, envCodexHome, envClaudeConfigDir, envGrokDisableAutoUpdate, "FIXTURE_MARKER",
}

// grokFixturePresence are the names recorded as present/absent and NEVER by
// value: the credential families a launch may or may not inject.
var grokFixturePresence = []string{
	"XAI_API_KEY", "GROK_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY",
	"OLIVARES_WORK_TOKEN", "OLIVARES_COMMUNICATION_TOKEN", "DISABLE_AUTOUPDATER",
}

type grokFixturePeer struct {
	cfg     grokFixture
	out     *json.Encoder
	mu      sync.Mutex
	rec     grokFixtureRecord
	session string
	// heldPrompt is the id of a prompt the fixture is holding open.
	heldPrompt json.RawMessage
	cancelled  bool
}

func runGrokFixturePeer() int {
	var cfg grokFixture
	home := os.Getenv(grokFixtureHomeEnv)
	if home == "" {
		fmt.Fprintln(os.Stderr, "grok fixture peer: no configuration home was selected")
		return 2
	}
	if raw, err := os.ReadFile(filepath.Join(home, grokFixtureConfigFile)); err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			fmt.Fprintln(os.Stderr, "grok fixture peer: bad configuration")
			return 2
		}
	}
	if cfg.SessionID == "" {
		cfg.SessionID = "fixture-session"
	}
	if cfg.RecordPath == "" {
		cfg.RecordPath = filepath.Join(home, grokFixtureRecordFile)
	}
	p := &grokFixturePeer{cfg: cfg, out: json.NewEncoder(os.Stdout)}
	cwd, _ := os.Getwd()
	p.rec = grokFixtureRecord{
		PID: os.Getpid(), Argv: append([]string(nil), os.Args[1:]...), Cwd: cwd,
		Env: map[string]string{}, Present: map[string]bool{},
	}
	for _, name := range grokFixtureEnvValues {
		if v, ok := os.LookupEnv(name); ok {
			p.rec.Env[name] = v
		}
	}
	for _, name := range grokFixturePresence {
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

func (p *grokFixturePeer) flush() {
	if p.cfg.RecordPath == "" {
		return
	}
	raw, err := json.MarshalIndent(p.rec, "", " ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p.cfg.RecordPath, raw, 0o600)
}

func (p *grokFixturePeer) send(v any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.out.Encode(v)
}

func (p *grokFixturePeer) handle(frame map[string]json.RawMessage) {
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
		if method == grokMethodSessionCancel {
			p.releaseHeldPrompt("cancelled")
		}
		p.flush()
		return
	}
	if method == "" {
		// A RESPONSE to one of the peer's own requests: record it verbatim.
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
	case grokMethodInitialize:
		if raw, ok := params["clientCapabilities"]; ok {
			p.rec.ClientCaps = append(json.RawMessage(nil), raw...)
		}
		p.flush()
		p.reply(id, p.initializeResult())
	case grokMethodAuthenticate:
		methodID := ""
		if raw, ok := params["methodId"]; ok {
			_ = json.Unmarshal(raw, &methodID)
		}
		p.rec.AuthMethodIDs = append(p.rec.AuthMethodIDs, methodID)
		p.flush()
		if p.cfg.FailAuthenticate {
			p.replyError(id, -32000, "authentication refused")
			return
		}
		p.reply(id, map[string]any{})
	case grokMethodSessionNew:
		cwd := ""
		if raw, ok := params["cwd"]; ok {
			_ = json.Unmarshal(raw, &cwd)
		}
		p.rec.NewCwds = append(p.rec.NewCwds, cwd)
		p.flush()
		p.waitFor(p.cfg.HoldNewPath)
		if p.cfg.NewSessionAuthRequired {
			p.replyError(id, grokErrAuthRequired, "Authentication required")
			return
		}
		p.session = p.cfg.SessionID
		p.reply(id, map[string]any{"sessionId": p.session})
	case grokMethodSessionResume, grokMethodSessionLoad:
		requested := ""
		if raw, ok := params["sessionId"]; ok {
			_ = json.Unmarshal(raw, &requested)
		}
		p.rec.ResumeIDs = append(p.rec.ResumeIDs, requested)
		p.flush()
		if p.cfg.FailResume {
			p.replyError(id, -32000, "conversation unavailable")
			return
		}
		p.session = requested
		for i := 0; i < p.cfg.ReplayUpdates; i++ {
			// Replayed history, sent BEFORE the correlated response, exactly as the
			// protocol describes a load.
			p.sendUpdate(p.session, "agent_message_chunk", fmt.Sprintf("history-%d", i))
		}
		switch {
		case p.cfg.ResumeSessionID != "":
			p.session = p.cfg.ResumeSessionID
			p.reply(id, map[string]any{"sessionId": p.cfg.ResumeSessionID})
		case p.cfg.ResumeEmptyObject:
			p.reply(id, map[string]any{})
		default:
			// A null result: the protocol's own legal success for a load.
			p.replyNull(id)
		}
	case grokMethodSessionPrompt:
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
	case grokMethodSessionClose:
		p.reply(id, map[string]any{})
	default:
		p.replyError(id, grokErrMethodNotSupported, "the fixture peer does not implement "+method)
	}
}

// onPrompt implements the four prompt behaviours. The live output, the
// permission request and the foreign update all happen BEFORE any answer,
// because that is the order a real turn produces them in.
func (p *grokFixturePeer) onPrompt(id json.RawMessage) {
	p.sendUpdate(p.session, "agent_message_chunk", "live output")
	if p.cfg.ForeignUpdate {
		// Another conversation entirely: it must bind nothing and end nothing.
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
			"jsonrpc": "2.0", "id": "srv-1", "method": grokReqRequestPermission,
			"params": map[string]any{
				"sessionId": p.session,
				"toolCall": map[string]any{
					"toolCallId": "call-1", "title": "run a command",
					"kind": "execute", "status": "pending",
				},
				"options": grokFixturePermissionOptions(p.cfg.PermissionOnTurn),
			},
		})
	}
	switch p.cfg.PromptMode {
	case "never":
		// The delayed result: the turn stays in flight until the process ends.
		return
	case "auth":
		p.replyError(id, grokErrAuthRequired, "Authentication required")
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

// releaseHeldPrompt answers a held prompt exactly once. A `session/cancel`
// notification releases it with the protocol's own cancelled stop reason, which
// is what actually ends an interrupted turn.
func (p *grokFixturePeer) releaseHeldPrompt(reason string) {
	p.mu.Lock()
	id := p.heldPrompt
	p.heldPrompt = nil
	if reason == "cancelled" {
		p.cancelled = true
	}
	p.mu.Unlock()
	if id == nil {
		return
	}
	p.reply(id, map[string]any{"stopReason": reason})
}

func (p *grokFixturePeer) initializeResult() map[string]any {
	version := p.cfg.ProtocolVersion
	if version == 0 {
		version = grokProtocolVersion
	}
	sessionCaps := map[string]any{"list": map[string]any{}, "close": map[string]any{}}
	if !p.cfg.NoResumeCapability {
		sessionCaps["resume"] = map[string]any{}
	}
	var methods []any
	switch p.cfg.AuthMethods {
	case "none":
		methods = []any{}
	case "browser":
		methods = []any{map[string]any{"id": grokAuthMethodBrowser, "name": "Grok"}}
	default:
		methods = []any{
			map[string]any{"id": grokAuthMethodBrowser, "name": "Grok"},
			map[string]any{"id": grokAuthMethodCachedToken, "name": "Cached token"},
			map[string]any{"id": grokAuthMethodAPIKey, "name": "xAI API key"},
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"agentCapabilities": map[string]any{
			"loadSession": !p.cfg.NoLoadSession,
			"promptCapabilities": map[string]any{
				"image": false, "audio": false, "embeddedContext": true,
			},
			"sessionCapabilities": sessionCaps,
		},
		"authMethods":         methods,
		"agentVersion":        "1.0.13-fixture",
		"defaultAuthMethodId": nil,
		"currentModelId":      "grok-4.6",
	}
}

// grokFixturePermissionOptions is the named option catalogue a row asks for. The
// shapes are the protocol's; which of them a real authenticated Grok offers for a
// given tool call is NOT observed, which is exactly why the driver decides over
// what it is sent rather than over a table.
func grokFixturePermissionOptions(name string) []any {
	allowOnce := map[string]any{"optionId": "allow-once", "name": "Allow once", "kind": grokPermissionAllowOnce}
	allowAlways := map[string]any{"optionId": "allow-always", "name": "Always allow", "kind": grokPermissionAllowAlways}
	rejectOnce := map[string]any{"optionId": "reject-once", "name": "Reject", "kind": grokPermissionRejectOnce}
	rejectAlways := map[string]any{"optionId": "reject-always", "name": "Never allow", "kind": grokPermissionRejectAlways}
	switch name {
	case "allow-only":
		return []any{allowOnce, allowAlways}
	case "persistent-only":
		return []any{allowAlways, rejectAlways}
	case "duplicate":
		return []any{allowOnce, map[string]any{
			"optionId": "allow-once", "name": "Allow once again", "kind": grokPermissionRejectOnce,
		}}
	case "unknown-kinds":
		return []any{map[string]any{"optionId": "mystery", "name": "?", "kind": "teleport"}}
	case "empty":
		return []any{}
	default:
		return []any{allowOnce, allowAlways, rejectOnce, rejectAlways}
	}
}

func (p *grokFixturePeer) sendUpdate(session, kind, text string) {
	p.send(map[string]any{
		"jsonrpc": "2.0", "method": grokNotifySessionUpdate,
		"params": map[string]any{
			"sessionId": session,
			"update": map[string]any{
				"sessionUpdate": kind,
				"content":       map[string]any{"type": "text", "text": text},
			},
		},
	})
}

// waitFor blocks until the named file appears (or the deadline passes). An empty
// path does not wait at all.
func (p *grokFixturePeer) waitFor(path string) {
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

func (p *grokFixturePeer) reply(id json.RawMessage, result any) {
	p.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func (p *grokFixturePeer) replyNull(id json.RawMessage) {
	p.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": nil})
}

func (p *grokFixturePeer) replyError(id json.RawMessage, code int, message string) {
	p.send(map[string]any{
		"jsonrpc": "2.0", "id": json.RawMessage(id),
		"error": map[string]any{"code": code, "message": message},
	})
}

// setGrokFixture writes the peer's configuration into the profile's own
// configuration home and returns the path its record will appear at.
func setGrokFixture(t *testing.T, prof ProviderProfile, cfg grokFixture) string {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("fixture config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prof.ConfigHome, grokFixtureConfigFile), raw, 0o600); err != nil {
		t.Fatalf("write the fixture configuration: %v", err)
	}
	if cfg.RecordPath != "" {
		return cfg.RecordPath
	}
	return filepath.Join(prof.ConfigHome, grokFixtureRecordFile)
}

func readGrokFixtureRecord(t *testing.T, path string) grokFixtureRecord {
	t.Helper()
	var rec grokFixtureRecord
	waitFor(t, "the grok fixture peer wrote its record", func() bool {
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 {
			return false
		}
		return json.Unmarshal(raw, &rec) == nil
	})
	return rec
}

// tryReadGrokFixtureRecord reads the record WITHOUT waiting, for the rows that
// must prove no child was spawned at all.
func tryReadGrokFixtureRecord(path string) (grokFixtureRecord, bool) {
	var rec grokFixtureRecord
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return rec, false
	}
	return rec, json.Unmarshal(raw, &rec) == nil
}
