// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
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
	SandboxExit   int  `json:"sandbox_exit"`
	SandboxSignal bool `json:"sandbox_signal"`
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
	// HoldSecondTurnPath makes the peer wait for that file before answering the second
	// turn/start, so a test can observe a run whose input the provider has received
	// but not yet named a turn for.
	HoldSecondTurnPath string `json:"hold_second_turn_path"`
	// CompleteTurn emits turn/completed right after answering turn/start.
	CompleteTurn bool `json:"complete_turn"`
	// ApprovalOnTurn asks for that approval method after answering turn/start.
	ApprovalOnTurn       string `json:"approval_on_turn"`
	CompleteApprovalTurn bool   `json:"complete_approval_turn"`
	// SpawnChild forks a grandchild that inherits stdout, so a stop has something
	// to reap that the direct SIGTERM would not reach.
	SpawnChild bool `json:"spawn_child"`
	// AuthRecovery, when set, emits the provider's own authentication-recovery
	// notification after answering turn/start.
	AuthRecovery string `json:"auth_recovery"`
	// ApprovalPolicy and SandboxType override what the thread answer says the
	// thread runs under ("" ⇒ "untrusted" and "readOnly"), so a test can tell the
	// mode Codex reports from the one the launch asked for.
	ApprovalPolicy json.RawMessage `json:"approval_policy,omitempty"`
	SandboxType    string          `json:"sandbox_type"`
	// TurnUsage is the running thread total the peer reports after answering each
	// turn/start, one thread/tokenUsage/updated per entry, for the turn it started.
	TurnUsage []codexFixtureTokens `json:"turn_usage"`
	// ResumeUsage is the total a resumed thread reports at once, before any turn,
	// for its previous turn: what codex-cli 0.160.1 does (measured 2026-10-07).
	ResumeUsage *codexFixtureTokens `json:"resume_usage"`
	// ResumeUsageAfterPath holds that report until the test creates this file, so it
	// arrives after the handshake has bound the thread.
	ResumeUsageAfterPath string `json:"resume_usage_after_path"`
	// UsageBeforeTurnReply sends a turn's usage BEFORE the turn/start answer, so a
	// test proves crediting does not depend on which of the two is read first.
	UsageBeforeTurnReply bool `json:"usage_before_turn_reply"`
	// ForeignUsage is a total the peer reports for ANOTHER thread (a subagent's)
	// after each turn/start; it must never reach this run.
	ForeignUsage *codexFixtureTokens `json:"foreign_usage"`
	// StoredImageBytes gives the thread two earlier turns, the first with an image of
	// this many base64 bytes in its user message (what codex-cli 0.162.1 reports for a
	// message that was sent a screenshot). thread/resume lists them unless the client
	// sends excludeTurns; thread/read lists them when it asks for turns.
	StoredImageBytes int `json:"stored_image_bytes"`
	// FirstTurnReplay is a file of notifications recorded from the real tool, one JSON
	// object per line, that the peer sends after answering the FIRST turn/start, with
	// the recorded thread and turn ids replaced by the peer's own. Later turns follow
	// CompleteTurn.
	FirstTurnReplay string `json:"first_turn_replay"`
	// Model overrides the model the thread answer names ("" ⇒ "the model").
	Model         string           `json:"model"`
	CatalogModels []map[string]any `json:"catalog_models,omitempty"`
	// SkillsPadBytes pads the description of the skill that one skills/list answer
	// carries, so its reply line can pass the one-line limit the way a large skill
	// set does. SkillsPadRequest is the 1-based request it pads (0 means the first);
	// the other answers are small. SkillsPadForeignID answers the padded request
	// under an id nobody issued.
	SkillsPadBytes     int  `json:"skills_pad_bytes"`
	SkillsPadRequest   int  `json:"skills_pad_request"`
	SkillsPadForeignID bool `json:"skills_pad_foreign_id"`
}

// codexFixtureTokens is one TokenUsageBreakdown, in the wire's own field names.
type codexFixtureTokens struct {
	InputTokens           int64 `json:"inputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
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
	Efforts      []string `json:"efforts"`
	ServiceTiers []string `json:"service_tiers"`
	// Inputs is the `text` of every input block that arrived on a turn/start or a
	// turn/steer, byte for byte, so a row can compare the string the child received
	// rather than the number of turns.
	Inputs []string `json:"inputs"`
	// Images is the `url` of every image block that arrived beside the text, as sent.
	Images []string `json:"images"`
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
	args := os.Args[1:]
	for len(args) >= 2 && args[0] == "-c" {
		args = args[2:]
	}
	if len(args) > 0 {
		switch args[0] {
		case "sandbox":
			var cfg codexFixture
			raw, _ := os.ReadFile(filepath.Join(os.Getenv(envCodexHome), codexFixtureConfigFile))
			_ = json.Unmarshal(raw, &cfg)
			if cfg.SandboxSignal {
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			}
			if cfg.SandboxExit != 0 {
				os.Exit(cfg.SandboxExit)
			}
			os.Exit(runCodexFixtureSandboxCommand(args[1:]))
		case "app-server":
			os.Exit(runCodexFixturePeer())
		case "agent":
			os.Exit(runGrokFixturePeer())
		case "acp":
			os.Exit(runOpenCodeFixturePeer())
		case confine.HelperArg:
			// The confinement helper: procRunner re-executes this test binary.
			os.Exit(RunConfinementHelper(args[1:]))
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

// runCodexFixtureSandboxCommand runs what `codex sandbox [COMMAND]...` runs once
// the native sandbox is up. The official parser (0.145.0 to 0.160.1) takes a
// leading `--` as the separator and every later word as the command, so
// `sandbox linux -- /bin/true` executes `linux` and fails.
func runCodexFixtureSandboxCommand(command []string) int {
	if len(command) > 0 && command[0] == "--" {
		command = command[1:]
	}
	if len(command) == 0 {
		fmt.Fprintln(os.Stderr, "fixture sandbox: no command")
		return 2
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "fixture sandbox: %v\n", err)
		return 1
	}
	return 0
}

type codexFixturePeer struct {
	cfg    codexFixture
	out    *json.Encoder
	mu     sync.Mutex
	rec    codexFixtureRecord
	thread string
	turn   int
	skills int // skills/list requests answered
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
		if p.cfg.CompleteApprovalTurn {
			p.send(map[string]any{"method": codexNotifyTurnCompleted, "params": map[string]any{"threadId": p.thread, "turn": map[string]any{"id": fmt.Sprintf("turn-%d", p.turn), "status": "interrupted"}}})
		}
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
	case "model/list":
		models := p.cfg.CatalogModels
		if models == nil {
			models = []map[string]any{}
		}
		p.reply(id, map[string]any{"data": models, "nextCursor": nil})
	case codexMethodThreadStart:
		var tier string
		_ = json.Unmarshal(params["serviceTier"], &tier)
		p.rec.ServiceTiers = append(p.rec.ServiceTiers, tier)
		p.flush()
		p.waitForRelease()
		p.thread = p.cfg.ThreadID
		p.reply(id, p.threadResult(p.thread))
	case codexMethodThreadResume:
		var tier string
		_ = json.Unmarshal(params["serviceTier"], &tier)
		p.rec.ServiceTiers = append(p.rec.ServiceTiers, tier)
		p.flush()
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
		result := p.threadResult(p.thread)
		var excludeTurns bool
		_ = json.Unmarshal(params["excludeTurns"], &excludeTurns)
		if p.cfg.StoredImageBytes > 0 && !excludeTurns {
			result["thread"].(map[string]any)["turns"] = p.storedTurns()
		}
		p.reply(id, result)
		if p.cfg.ResumeUsage != nil && p.cfg.ResumeUsageAfterPath != "" {
			go func(thread string, total codexFixtureTokens) {
				if waitForFile(p.cfg.ResumeUsageAfterPath) { // never released: no replay, and the test's wait fails
					p.tokenUsageFor(thread, "turn-of-an-earlier-process", total)
				}
			}(p.thread, *p.cfg.ResumeUsage)
		} else if p.cfg.ResumeUsage != nil {
			p.tokenUsage("turn-of-an-earlier-process", *p.cfg.ResumeUsage)
		}
	case "thread/read":
		var threadID string
		_ = json.Unmarshal(params["threadId"], &threadID)
		thread := map[string]any{"id": threadID, "parentThreadId": nil, "agentRole": nil}
		var includeTurns bool
		_ = json.Unmarshal(params["includeTurns"], &includeTurns)
		if includeTurns {
			thread["turns"] = p.storedTurns()
		}
		p.reply(id, map[string]any{"thread": thread})
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
		if p.cfg.HoldSecondTurnPath != "" && p.turn == 2 {
			waitForFile(p.cfg.HoldSecondTurnPath)
		}
		if p.cfg.UsageBeforeTurnReply {
			for _, total := range p.cfg.TurnUsage {
				p.tokenUsage(turnID, total)
			}
		}
		p.reply(id, map[string]any{
			"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}},
		})
		if !p.cfg.UsageBeforeTurnReply {
			for _, total := range p.cfg.TurnUsage {
				p.tokenUsage(turnID, total)
			}
		}
		if p.cfg.ForeignUsage != nil {
			p.tokenUsageFor("thread-of-a-subagent", "turn-of-a-subagent", *p.cfg.ForeignUsage)
		}
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
		if p.cfg.FirstTurnReplay != "" && p.turn == 1 {
			p.replayRecorded(p.cfg.FirstTurnReplay, turnID)
		} else if p.cfg.CompleteTurn {
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
	case "skills/list":
		p.skills++
		description := "Fixture skill"
		replyID := id
		if p.cfg.SkillsPadBytes > 0 && p.skills == max(p.cfg.SkillsPadRequest, 1) {
			description = strings.Repeat("x", p.cfg.SkillsPadBytes)
			if p.cfg.SkillsPadForeignID {
				n, _ := strconv.Atoi(string(id))
				replyID = json.RawMessage(strconv.Itoa(n + 1000))
			}
		}
		var cwds []string
		_ = json.Unmarshal(params["cwds"], &cwds)
		cwd := ""
		if len(cwds) == 1 {
			cwd = cwds[0]
		}
		p.reply(replyID, map[string]any{"data": []any{map[string]any{
			"cwd": cwd, "errors": []any{},
			"skills": []any{map[string]any{"name": "fixture-skill", "description": description, "enabled": true, "scope": "repo"}},
		}}})
	case codexMethodThreadUnsubscribe:
		p.reply(id, map[string]any{"status": "unsubscribed"})
	default:
		p.replyError(id, -32601, "the fixture peer does not implement "+method)
	}
}

// The ids inside the notifications recorded from the real tool (codex-cli 0.162.1).
const (
	recordedCodexThreadID = "01a13a73-2fe6-7593-a275-596356a9eacf"
	recordedCodexTurnID   = "01a13a73-30ab-7e02-8f7a-3a0f8f930986"
)

// replayRecorded sends each line of a recording as it was recorded, under this
// peer's thread and turn.
func (p *codexFixturePeer) replayRecorded(path, turnID string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("the fixture peer cannot read its recording: %v", err))
	}
	replacer := strings.NewReplacer(recordedCodexThreadID, p.thread, recordedCodexTurnID, turnID)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var frame map[string]any
		if err := json.Unmarshal([]byte(replacer.Replace(line)), &frame); err != nil {
			panic(fmt.Sprintf("the fixture peer cannot read a recorded frame: %v", err))
		}
		p.send(frame)
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
		if block.Type == "image" {
			p.rec.Images = append(p.rec.Images, block.URL)
			continue
		}
		p.rec.Inputs = append(p.rec.Inputs, block.Text)
	}
}

// storedTurns is the thread's earlier history: a user message that carried an image
// and the answer to it, in the shape thread/read lists them (itemsView "full").
func (p *codexFixturePeer) storedTurns() []map[string]any {
	image := "data:image/png;base64," + strings.Repeat("A", p.cfg.StoredImageBytes)
	return []map[string]any{
		{"id": "turn-a", "status": "completed", "itemsView": "full", "items": []map[string]any{
			{"type": "userMessage", "id": "item-1", "content": []map[string]any{
				{"type": "text", "text": "what is in this picture", "text_elements": []any{}},
				{"type": "image", "url": image},
			}},
		}},
		{"id": "turn-b", "status": "completed", "itemsView": "full", "items": []map[string]any{
			{"type": "agentMessage", "id": "item-2", "text": "a cat on a sofa"},
		}},
	}
}

func (p *codexFixturePeer) threadResult(id string) map[string]any {
	var serviceTier any
	if n := len(p.rec.ServiceTiers); n > 0 && p.rec.ServiceTiers[n-1] != "" {
		serviceTier = p.rec.ServiceTiers[n-1]
	}
	var approval any = "untrusted"
	if len(p.cfg.ApprovalPolicy) > 0 {
		approval = p.cfg.ApprovalPolicy
	}
	sandbox := "readOnly"
	if p.cfg.SandboxType != "" {
		sandbox = p.cfg.SandboxType
	}
	model := "gpt-5.6-sol"
	if p.cfg.Model != "" {
		model = p.cfg.Model
	}
	return map[string]any{
		"thread": map[string]any{
			"id": id, "parentThreadId": nil, "agentRole": nil, "model": "gpt-5.6-sol",
			"cliVersion": "0.153.4", "modelProvider": "openai",
		},
		"model": model, "modelProvider": "openai", "serviceTier": serviceTier,
		"cwd": p.rec.Cwd, "approvalPolicy": approval,
		// The shape codex-cli 0.160.1 answers (measured 2026-10-07): a tagged object.
		"sandbox":           map[string]any{"type": sandbox, "networkAccess": false},
		"approvalsReviewer": "user",
	}
}

// tokenUsage sends thread/tokenUsage/updated with total as the thread's running
// total, in the measured shape. `last` (the last response alone) is a different
// figure on purpose, so reading the wrong one cannot pass.
func (p *codexFixturePeer) tokenUsage(turnID string, total codexFixtureTokens) {
	p.tokenUsageFor(p.thread, turnID, total)
}

func (p *codexFixturePeer) tokenUsageFor(threadID, turnID string, total codexFixtureTokens) {
	breakdown := func(t codexFixtureTokens) map[string]any {
		return map[string]any{
			"inputTokens": t.InputTokens, "cachedInputTokens": t.CachedInputTokens,
			"cacheWriteInputTokens": t.CacheWriteInputTokens, "outputTokens": t.OutputTokens,
			"reasoningOutputTokens": 0, "totalTokens": t.InputTokens + t.OutputTokens,
		}
	}
	last := codexFixtureTokens{InputTokens: 1, OutputTokens: 1}
	p.send(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{
		"threadId": threadID, "turnId": turnID,
		"tokenUsage": map[string]any{"total": breakdown(total), "last": breakdown(last), "modelContextWindow": 400000},
	}})
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
	if p.cfg.HoldStartPath != "" {
		waitForFile(p.cfg.HoldStartPath)
	}
}

// waitForFile reports whether path came to exist within 30 seconds.
func waitForFile(path string) bool {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func (p *codexFixturePeer) reply(id json.RawMessage, result any) {
	p.send(map[string]any{"id": json.RawMessage(id), "result": result})
}

func (p *codexFixturePeer) replyError(id json.RawMessage, code int, message string) {
	p.send(map[string]any{
		"id": json.RawMessage(id), "error": map[string]any{"code": code, "message": message},
	})
}

// This journey exercises the real HTTP server, SQLite and OS process runner.
// The protocol peer's upstream model file changes without changing Olivares.
func TestProfileModelCatalogExecutableJourney(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := New(WithRunner(NewProcRunner()), WithProviderDriver(NewCodexDriver()), WithDriverProgram("codex", exe))
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "catalog-process")
	home := t.TempDir()
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", UserHome: t.TempDir(), ConfigHome: home, AuthSource: AuthSourceAccountHome})
	writeCatalog := func(id string) {
		cfg := codexFixture{Account: "chatgpt", RequiresAuth: true, RecordPath: filepath.Join(home, codexFixtureRecordFile), CatalogModels: []map[string]any{{"model": id, "displayName": "Upstream model", "isDefault": true, "supportedReasoningEfforts": []map[string]any{{"reasoningEffort": "future-effort"}}, "defaultReasoningEffort": "future-effort"}}}
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(home, codexFixtureConfigFile), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := "/v1/m/sessions/provider-profiles/" + profile.Ref + "/models"
	for _, id := range []string{"vendor-today", "vendor-just-added"} {
		writeCatalog(id)
		r := h.do("GET", path+"?refresh=true", admin, tenantHdr(tenant))
		if r.code != 200 || r.body["state"] != "fresh" || r.body["default_model"] != id || !strings.Contains(r.raw, "future-effort") {
			t.Fatalf("catalog journey: %d %s", r.code, r.raw)
		}
		raw, err := os.ReadFile(filepath.Join(home, codexFixtureRecordFile))
		if err != nil {
			t.Fatal(err)
		}
		var record codexFixtureRecord
		if err = json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		for _, method := range record.Methods {
			if method != "initialize" && method != "initialized" && method != "account/read" && method != "model/list" {
				t.Fatalf("catalog started inference: %s", method)
			}
		}
		t.Logf("real HTTP/SQLite/OS process journey: %s and future-effort discovered without product change", id)
	}
}
