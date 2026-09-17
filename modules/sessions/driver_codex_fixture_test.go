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

// The FIXTURE EXECUTABLE PEER.
//
// It is a real program, not an in-process double, and that is the whole point of
// it. The rows this file supports — the child gets these homes and NOT that
// token, a completed turn leaves the PROCESS alive, an interrupt leaves it
// usable, a stop reaps its process GROUP including a grandchild — are claims
// about an operating-system process. A fake Process satisfies all of them by
// construction and proves none of them.
//
// It speaks exactly the Codex app-server 0.153.4 frames this driver implements,
// and nothing else: NDJSON, no `jsonrpc` member, correlated responses. It never
// contacts a network, never reads a real account home, and answers no method the
// driver does not send.
//
// It is selected by argv, because that is what the driver actually spawns: the
// test binary is registered as the `codex` program, and TestMain hands a process
// invoked as `app-server …` to the peer instead of to the test runner.

// The peer reads its configuration from a file inside the CONFIGURATION HOME the
// profile gave it, and writes its record beside it.
//
// It is a file and not an environment variable on purpose: `env_allow` is a
// property of a CREATE, and a resume rebuilds its parameters from the run row,
// which does not carry one. A fixture that depended on the environment would
// therefore silently reconfigure itself on resume — and the resume rows are
// exactly the ones this suite must be able to trust.
const (
	codexFixtureConfigFile = "olivares-fixture.json"
	codexFixtureRecordFile = "olivares-fixture-record.json"
)

// codexFixtureHomeEnv is the variable the peer reads its home from. It is the
// driver's own configuration-home variable, which is the one thing the runtime
// guarantees a codex child receives.
const codexFixtureHomeEnv = envCodexHome

// codexFixture configures one peer run.
type codexFixture struct {
	// ThreadID is the conversation the peer nominates on thread/start.
	ThreadID string `json:"thread_id"`
	// ResumeThreadID overrides what thread/resume answers ("" ⇒ echo the request).
	ResumeThreadID string `json:"resume_thread_id"`
	// Account is "", "apikey" or "chatgpt"; RequiresAuth is requiresOpenaiAuth.
	Account      string `json:"account"`
	RequiresAuth bool   `json:"requires_auth"`
	// FailResume answers thread/resume with a protocol error.
	FailResume bool `json:"fail_resume"`
	// RecordPath is where the peer writes what it saw. Never a secret VALUE.
	RecordPath string `json:"record_path"`
	// HoldStartPath makes the peer wait for that file before answering
	// thread/start, so a test can observe the launch WHILE the handshake is open.
	HoldStartPath string `json:"hold_start_path"`
	// CompleteTurn emits turn/completed right after answering turn/start.
	CompleteTurn bool `json:"complete_turn"`
	// ApprovalOnTurn asks for that approval method after answering turn/start.
	ApprovalOnTurn string `json:"approval_on_turn"`
	// SpawnChild forks a grandchild that inherits stdout, so a stop has something
	// to reap that the direct SIGTERM would not reach.
	SpawnChild bool `json:"spawn_child"`
	// AuthRecovery, when set, emits the provider's own authentication-recovery
	// notification after answering turn/start.
	AuthRecovery string `json:"auth_recovery"`
}

// codexFixtureRecord is what the peer observed. It records credential-bearing
// names as PRESENCE ONLY: an evidence file that contained a token would be the
// exact leak the runtime spends its effort preventing.
type codexFixtureRecord struct {
	PID      int               `json:"pid"`
	ChildPID int               `json:"child_pid"`
	Cwd      string            `json:"cwd"`
	Env      map[string]string `json:"env"`
	Present  map[string]bool   `json:"present"`
	Methods  []string          `json:"methods"`
	Replies  []json.RawMessage `json:"replies"`
	// Efforts is the `effort` carried by each turn/start, so a test can prove a
	// provider-advertised value reached the provider instead of being refused by
	// somebody else's enum on the way.
	Efforts []string `json:"efforts"`
	// Inputs is the `text` of every input block that arrived on a turn/start or a
	// turn/steer, byte for byte, so a row can compare the string the child received
	// rather than the number of turns.
	Inputs []string `json:"inputs"`
}

// codexFixtureEnvValues are the NON-SECRET names whose values the peer records:
// the homes it was given (test fixture directories) and the test's own markers.
var codexFixtureEnvValues = []string{"HOME", envCodexHome, envClaudeConfigDir, envGrokHome, "FIXTURE_MARKER"}

// codexFixturePresence are the names recorded as present/absent and NEVER by
// value. They are the credential families a launch may or may not inject.
var codexFixturePresence = []string{
	"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "OPENAI_API_KEY", "CODEX_API_KEY",
	"OLIVARES_WORK_TOKEN", "OLIVARES_COMMUNICATION_TOKEN", "DISABLE_AUTOUPDATER",
}

// TestMain routes a process the driver spawned to the fixture peer. Every other
// invocation is an ordinary test run.
//
// There is ONE TestMain per package, so it is also where the Grok fixture peer is
// selected. The two are told apart by the FIRST ARGUMENT OF THE REAL ARGV — Codex
// spawns `app-server …`, Grok spawns `agent …` — which means a driver that
// changed its argv would stop reaching its own peer, and every row of its suite
// would say so.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "app-server":
			os.Exit(runCodexFixturePeer())
		case "agent":
			os.Exit(runGrokFixturePeer())
		case "acp":
			os.Exit(runOpenCodeFixturePeer())
		case "fixture-grandchild":
			// A grandchild that holds the inherited stdout open. Only a PROCESS GROUP
			// teardown ends it; a signal to the direct child alone leaves it running
			// and the pipe open, which is the failure Setpgid exists to prevent.
			time.Sleep(10 * time.Minute)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

type codexFixturePeer struct {
	cfg    codexFixture
	out    *json.Encoder
	mu     sync.Mutex
	rec    codexFixtureRecord
	thread string
	turn   int
}

func runCodexFixturePeer() int {
	var cfg codexFixture
	home := os.Getenv(codexFixtureHomeEnv)
	if home == "" {
		fmt.Fprintln(os.Stderr, "fixture peer: no configuration home was selected")
		return 2
	}
	if raw, err := os.ReadFile(filepath.Join(home, codexFixtureConfigFile)); err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			fmt.Fprintln(os.Stderr, "fixture peer: bad configuration")
			return 2
		}
	}
	if cfg.ThreadID == "" {
		cfg.ThreadID = "fixture-thread"
	}
	if cfg.RecordPath == "" {
		cfg.RecordPath = filepath.Join(home, codexFixtureRecordFile)
	}
	p := &codexFixturePeer{cfg: cfg, out: json.NewEncoder(os.Stdout)}
	cwd, _ := os.Getwd()
	p.rec = codexFixtureRecord{
		PID: os.Getpid(), Cwd: cwd,
		Env: map[string]string{}, Present: map[string]bool{},
	}
	for _, name := range codexFixtureEnvValues {
		if v, ok := os.LookupEnv(name); ok {
			p.rec.Env[name] = v
		}
	}
	for _, name := range codexFixturePresence {
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

func (p *codexFixturePeer) flush() {
	if p.cfg.RecordPath == "" {
		return
	}
	raw, err := json.MarshalIndent(p.rec, "", " ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p.cfg.RecordPath, raw, 0o600)
}

func (p *codexFixturePeer) send(v any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.out.Encode(v)
}

func (p *codexFixturePeer) handle(frame map[string]json.RawMessage) {
	var method string
	if raw, ok := frame["method"]; ok {
		_ = json.Unmarshal(raw, &method)
	}
	id, hasID := frame["id"]
	p.rec.Methods = append(p.rec.Methods, method)
	if !hasID {
		// A client notification (`initialized`) or an answer we did not ask for.
		p.flush()
		return
	}
	if method == "" {
		// A RESPONSE to one of our server requests: record it verbatim so a test
		// can assert the exact codec bytes the driver produced.
		if raw, ok := frame["result"]; ok {
			p.rec.Replies = append(p.rec.Replies, append(json.RawMessage(nil), raw...))
		} else if raw, ok := frame["error"]; ok {
			p.rec.Replies = append(p.rec.Replies, append(json.RawMessage(nil), raw...))
		}
		p.flush()
		return
	}
	p.flush()

	var params map[string]json.RawMessage
	if raw, ok := frame["params"]; ok {
		_ = json.Unmarshal(raw, &params)
	}
	switch method {
	case codexMethodInitialize:
		p.reply(id, map[string]any{
			"userAgent": "olivares_session_fixture/0.153.4", "codexHome": os.Getenv(envCodexHome),
			"platformFamily": "unix", "platformOs": "linux",
		})
	case codexMethodAccountRead:
		var account any
		switch p.cfg.Account {
		case "apikey":
			account = map[string]any{"type": "apiKey"}
		case "chatgpt":
			account = map[string]any{"type": "chatgpt", "email": nil, "planType": "pro"}
		}
		p.reply(id, map[string]any{"account": account, "requiresOpenaiAuth": p.cfg.RequiresAuth})
	case codexMethodThreadStart:
		p.waitForRelease()
		p.thread = p.cfg.ThreadID
		p.reply(id, p.threadResult(p.thread))
	case codexMethodThreadResume:
		if p.cfg.FailResume {
			p.replyError(id, -32000, "conversation unavailable")
			return
		}
		requested := ""
		_ = json.Unmarshal(params["threadId"], &requested)
		p.thread = requested
		if p.cfg.ResumeThreadID != "" {
			p.thread = p.cfg.ResumeThreadID
		}
		p.reply(id, p.threadResult(p.thread))
	case codexMethodTurnStart:
		p.turn++
		turnID := fmt.Sprintf("turn-%d", p.turn)
		effort := ""
		if raw, ok := params["effort"]; ok {
			_ = json.Unmarshal(raw, &effort)
		}
		p.rec.Efforts = append(p.rec.Efforts, effort)
		p.recordInput(params)
		p.flush()
		p.reply(id, map[string]any{
			"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}},
		})
		if p.cfg.ApprovalOnTurn != "" {
			p.requestApproval(p.cfg.ApprovalOnTurn, turnID)
		}
		if p.cfg.AuthRecovery != "" {
			p.send(map[string]any{
				"method": p.cfg.AuthRecovery,
				"params": map[string]any{
					"message":  "the model provider rejected the stored credential",
					"provider": "openai", "threadId": p.thread, "turnId": turnID,
				},
			})
		}
		if p.cfg.CompleteTurn {
			p.send(map[string]any{
				"method": codexNotifyTurnCompleted,
				"params": map[string]any{
					"threadId": p.thread,
					"turn":     map[string]any{"id": turnID, "status": "completed", "items": []any{}},
				},
			})
		}
	case codexMethodTurnSteer:
		p.recordInput(params)
		p.flush()
		p.reply(id, map[string]any{"turnId": fmt.Sprintf("turn-%d", p.turn)})
	case codexMethodTurnInterrupt:
		p.reply(id, map[string]any{})
	case codexMethodThreadUnsubscribe:
		p.reply(id, map[string]any{"status": "unsubscribed"})
	default:
		p.replyError(id, -32601, "the fixture peer does not implement "+method)
	}
}

// recordInput keeps the text of every input block as it arrived. It decodes with the
// driver's own codec type so the record cannot disagree with the wire the driver writes.
func (p *codexFixturePeer) recordInput(params map[string]json.RawMessage) {
	raw, ok := params["input"]
	if !ok {
		return
	}
	var blocks []codexUserInputText
	if json.Unmarshal(raw, &blocks) != nil {
		return
	}
	for _, block := range blocks {
		p.rec.Inputs = append(p.rec.Inputs, block.Text)
	}
}

func (p *codexFixturePeer) threadResult(id string) map[string]any {
	return map[string]any{
		"thread": map[string]any{
			"id": id, "parentThreadId": nil, "agentRole": nil, "model": "gpt-5.6-sol",
			"cliVersion": "0.153.4", "modelProvider": "openai",
		},
		"model": "gpt-5.6-sol", "modelProvider": "openai",
		"cwd": p.rec.Cwd, "approvalPolicy": "untrusted",
		"sandbox": map[string]any{"mode": "read-only"}, "approvalsReviewer": "user",
	}
}

func (p *codexFixturePeer) requestApproval(method, turnID string) {
	params := map[string]any{
		"itemId": "item-1", "startedAtMs": time.Now().UnixMilli(),
		"threadId": p.thread, "turnId": turnID,
	}
	switch method {
	case codexReqLegacyExecApproval:
		params = map[string]any{
			"callId": "call-1", "command": []string{"ls"}, "conversationId": p.thread,
			"cwd": p.rec.Cwd, "parsedCmd": []any{},
		}
	case codexReqPermissionsApproval:
		params["cwd"] = p.rec.Cwd
		params["permissions"] = map[string]any{"network": map[string]any{"enabled": true}}
	}
	p.send(map[string]any{"id": "srv-1", "method": method, "params": params})
}

// waitForRelease blocks thread/start until the test creates the release file, so
// a test can inspect the plane WHILE the launch's handshake is still open.
func (p *codexFixturePeer) waitForRelease() {
	if p.cfg.HoldStartPath == "" {
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(p.cfg.HoldStartPath); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (p *codexFixturePeer) reply(id json.RawMessage, result any) {
	p.send(map[string]any{"id": json.RawMessage(id), "result": result})
}

func (p *codexFixturePeer) replyError(id json.RawMessage, code int, message string) {
	p.send(map[string]any{
		"id": json.RawMessage(id), "error": map[string]any{"code": code, "message": message},
	})
}
