// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Protocol-level tests for the official Codex driver.
//
// They are deliberately about the WIRE and the CODECS, because that is where a
// driver is wrong in ways nothing else notices: a generic "cancel" answered on a
// surface that has no cancel, a granular approval policy flattened into a string,
// an experimental request answered to look supported. The runtime-level
// acceptance (a real child, a real run row, a real alias) is in
// runtime_codex_test.go — these two files prove different things and neither
// substitutes for the other.

// --- in-process peer ---------------------------------------------------------

// codexPeer drives a codexSession from the other side of the pipe: it captures
// what the session SENDS and lets a test answer it.
type codexPeer struct {
	t       *testing.T
	session DriverSession
	frames  chan map[string]any
	mu      sync.Mutex
	lines   [][]byte
}

func newCodexPeer(t *testing.T, opts ...func(*DriverSessionConfig)) *codexPeer {
	t.Helper()
	p := &codexPeer{t: t, frames: make(chan map[string]any, 64)}
	cfg := DriverSessionConfig{
		Send:          p.send,
		Warn:          func(string, ...any) {},
		ClientName:    "olivares",
		ClientVersion: "test",
		CallTimeout:   2 * time.Second,
		RunRef:        "run-test",
		ProfileRef:    "ppf_test",
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	p.session = codexDriver{}.OpenSession(cfg)
	return p
}

func (p *codexPeer) send(_ context.Context, line []byte) error {
	p.mu.Lock()
	p.lines = append(p.lines, append([]byte(nil), line...))
	p.mu.Unlock()
	var frame map[string]any
	if err := json.Unmarshal(line, &frame); err != nil {
		p.t.Errorf("the driver wrote a line that is not JSON: %s", line)
		return nil
	}
	p.frames <- frame
	return nil
}

// next returns the next frame the driver sent, or fails the test.
func (p *codexPeer) next() map[string]any {
	p.t.Helper()
	select {
	case f := <-p.frames:
		return f
	case <-time.After(3 * time.Second):
		p.t.Fatal("timed out waiting for the driver to send a frame")
		return nil
	}
}

// nextRequest returns the next frame that is a REQUEST (skipping notifications).
func (p *codexPeer) nextRequest() (int64, string, map[string]any) {
	p.t.Helper()
	for {
		f := p.next()
		id, ok := f["id"]
		if !ok {
			continue
		}
		num, _ := id.(float64)
		params, _ := f["params"].(map[string]any)
		return int64(num), f["method"].(string), params
	}
}

func (p *codexPeer) reply(id int64, result any) {
	p.t.Helper()
	raw, err := json.Marshal(map[string]any{"id": id, "result": result})
	if err != nil {
		p.t.Fatalf("marshal reply: %v", err)
	}
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *codexPeer) replyError(id int64, code int, message string) {
	p.t.Helper()
	raw, _ := json.Marshal(map[string]any{"id": id, "error": map[string]any{"code": code, "message": message}})
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *codexPeer) requestFromServer(id string, method string, params any) {
	p.t.Helper()
	raw, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *codexPeer) notify(method string, params any) {
	p.t.Helper()
	raw, _ := json.Marshal(map[string]any{"method": method, "params": params})
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

// sentLines returns every line the driver wrote so far.
func (p *codexPeer) sentLines() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.lines))
	copy(out, p.lines)
	return out
}

// answerHandshake plays the server side of the handshake and returns its result.
func (p *codexPeer) answerHandshake(threadID string, account any, requiresAuth bool) (DriverHandshake, error) {
	p.t.Helper()
	type outcome struct {
		hs  DriverHandshake
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		hs, err := p.session.Handshake(context.Background())
		done <- outcome{hs, err}
	}()
	id, method, _ := p.nextRequest()
	if method != codexMethodInitialize {
		p.t.Fatalf("first request = %q, want initialize", method)
	}
	p.reply(id, map[string]any{
		"userAgent": "fixture", "codexHome": "/fixture", "platformFamily": "unix", "platformOs": "linux",
	})
	id, method, _ = p.nextRequest()
	if method != codexMethodAccountRead {
		p.t.Fatalf("second request = %q, want account/read", method)
	}
	p.reply(id, map[string]any{"account": account, "requiresOpenaiAuth": requiresAuth})
	id, method, _ = p.nextRequest()
	if method != codexMethodThreadStart {
		p.t.Fatalf("third request = %q, want thread/start", method)
	}
	p.reply(id, map[string]any{
		"thread": map[string]any{"id": threadID, "parentThreadId": nil, "agentRole": nil},
		"model":  "gpt-5.6-sol", "modelProvider": "openai",
	})
	res := <-done
	return res.hs, res.err
}

// --- wire --------------------------------------------------------------------

// The Codex app-server's own JSONRPCRequest requires exactly `id` and `method`
// and has NO `jsonrpc` member; the recorded standalone capture of 0.153.4 shows
// frames without one. Emitting it would be inventing a field the schema does not
// declare — and ACP, which does require it, is why the envelope is a switch on
// the shared pump instead of an assumption inside it.
func TestCodexWireCarriesNoJSONRPCMember(t *testing.T) {
	peer := newCodexPeer(t)
	if _, err := peer.answerHandshake("thread-1", nil, false); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	lines := peer.sentLines()
	if len(lines) < 3 {
		t.Fatalf("the handshake wrote %d lines", len(lines))
	}
	for _, line := range lines {
		var frame map[string]any
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatalf("line is not JSON: %s", line)
		}
		if _, present := frame["jsonrpc"]; present {
			t.Fatalf("the codex wire must carry no jsonrpc member: %s", line)
		}
	}
	// initialize declares the stable surface, and `initialized` follows it.
	var init map[string]any
	_ = json.Unmarshal(lines[0], &init)
	params, _ := init["params"].(map[string]any)
	caps, _ := params["capabilities"].(map[string]any)
	if caps["experimentalApi"] != false {
		t.Fatalf("initialize must declare experimentalApi false: %s", lines[0])
	}
	var second map[string]any
	_ = json.Unmarshal(lines[1], &second)
	if second["method"] != codexMethodInitialized {
		t.Fatalf("the handshake must send the initialized notification: %s", lines[1])
	}
	if _, hasID := second["id"]; hasID {
		t.Fatalf("initialized is a notification and carries no id: %s", lines[1])
	}
}

// Readiness is what the PROVIDER answered, never what a home path suggests.
func TestCodexAuthReadinessComesFromTheProvider(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		account      any
		requiresAuth bool
		want         string
	}{
		{"empty home", nil, true, AuthStateRequired},
		{"saved chatgpt login", map[string]any{"type": "chatgpt", "email": nil, "planType": "pro"}, true, AuthStateReady},
		{"api key account", map[string]any{"type": "apiKey"}, false, AuthStateReady},
		{"authorized custom provider", nil, false, AuthStateReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"account": tc.account, "requiresOpenaiAuth": tc.requiresAuth})
			if got := codexAuthStateFrom(raw); got != tc.want {
				t.Fatalf("readiness = %q, want %q", got, tc.want)
			}
		})
	}
	if got := codexAuthStateFrom(json.RawMessage(`not json`)); got != AuthStateUnknown {
		t.Fatalf("an unreadable answer must stay unknown, got %q", got)
	}
}

// --- AskForApproval ----------------------------------------------------------

func TestCodexApprovalPolicyKeepsTheTypedGranularObject(t *testing.T) {
	t.Parallel()
	granular := CodexApprovalPolicy{Granular: &CodexGranularApproval{
		MCPElicitations: true, Rules: false, SandboxApproval: true,
	}}
	raw, err := json.Marshal(granular)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"granular":{"mcp_elicitations":true,"rules":false,"sandbox_approval":true}}`
	if string(raw) != want {
		t.Fatalf("granular policy = %s, want %s", raw, want)
	}
	var back CodexApprovalPolicy
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Granular == nil || back.Mode != "" {
		t.Fatalf("a granular policy must NOT be coerced into a string: %+v", back)
	}
	if !reflect.DeepEqual(*back.Granular, *granular.Granular) {
		t.Fatalf("granular round trip = %+v", *back.Granular)
	}
	// The two optional booleans default to false and are omitted at that value, so
	// the wire says what the schema's default says rather than restating it.
	if strings.Contains(string(raw), "request_permissions") || strings.Contains(string(raw), "skill_approval") {
		t.Fatalf("defaulted granular switches must be omitted: %s", raw)
	}
	for _, mode := range []string{CodexApprovalUntrusted, CodexApprovalOnRequest, CodexApprovalNever} {
		raw, _ := json.Marshal(CodexApprovalPolicy{Mode: mode})
		if string(raw) != `"`+mode+`"` {
			t.Fatalf("mode %q = %s", mode, raw)
		}
	}
	// `on-failure` is obsolete and absent from this CLI's schema.
	if err := (CodexApprovalPolicy{Mode: "on-failure"}).validate(); err == nil {
		t.Fatal("on-failure must be refused: it is not in the 0.153.4 schema")
	}
	if _, err := NewCodexDriverWithPolicy(CodexPolicy{Sandbox: "everything"}); err == nil {
		t.Fatal("an unknown sandbox mode must be refused")
	}
}

// The deny-closed floor is the tightest pair the schema offers, and the approval
// policy and the sandbox are INDEPENDENT: `never` means "ask nothing", not
// "widen the sandbox".
func TestCodexThreadStartCarriesBothControlsExplicitly(t *testing.T) {
	peer := newCodexPeer(t)
	go func() { _, _ = peer.session.Handshake(context.Background()) }()
	id, _, _ := peer.nextRequest()
	peer.reply(id, map[string]any{"userAgent": "f", "codexHome": "/f", "platformFamily": "unix", "platformOs": "linux"})
	id, _, _ = peer.nextRequest()
	peer.reply(id, map[string]any{"account": nil, "requiresOpenaiAuth": true})
	_, method, params := peer.nextRequest()
	if method != codexMethodThreadStart {
		t.Fatalf("method = %q", method)
	}
	if params["approvalPolicy"] != CodexApprovalUntrusted {
		t.Fatalf("approvalPolicy = %v, want the deny-closed floor", params["approvalPolicy"])
	}
	if params["sandbox"] != CodexSandboxReadOnly {
		t.Fatalf("sandbox = %v, want the deny-closed floor", params["sandbox"])
	}
	// Both are SENT, not omitted: an omitted control lets a persisted permissive
	// CLI default decide the launch's effective policy, which §6 forbids.
	if _, ok := params["approvalPolicy"]; !ok {
		t.Fatal("the approval policy must be explicit on every thread/start")
	}
	if _, ok := params["sandbox"]; !ok {
		t.Fatal("the sandbox must be explicit on every thread/start")
	}
}

// --- nomination --------------------------------------------------------------

func TestCodexBindsOnlyTheCorrelatedRootResponse(t *testing.T) {
	peer := newCodexPeer(t)
	type outcome struct {
		hs  DriverHandshake
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		hs, err := peer.session.Handshake(context.Background())
		done <- outcome{hs, err}
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, map[string]any{"userAgent": "f", "codexHome": "/f", "platformFamily": "unix", "platformOs": "linux"})
	id, _, _ = peer.nextRequest()
	peer.reply(id, map[string]any{"account": nil, "requiresOpenaiAuth": true})
	id, _, _ = peer.nextRequest()
	// A notification announcing ANOTHER thread arrives first. It is addressed to
	// nobody and must nominate nothing.
	peer.notify("thread/started", map[string]any{"thread": map[string]any{"id": "not-ours"}})
	peer.reply(id, map[string]any{
		"thread": map[string]any{"id": "ours-1"}, "model": "m", "modelProvider": "openai",
	})
	res := <-done
	if res.err != nil {
		t.Fatalf("handshake: %v", res.err)
	}
	if res.hs.ConversationID != "ours-1" {
		t.Fatalf("conversation = %q; only the correlated root response may nominate", res.hs.ConversationID)
	}
}

func TestCodexRefusesASubagentNomination(t *testing.T) {
	peer := newCodexPeer(t)
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, map[string]any{"userAgent": "f", "codexHome": "/f", "platformFamily": "unix", "platformOs": "linux"})
	id, _, _ = peer.nextRequest()
	peer.reply(id, map[string]any{"account": nil, "requiresOpenaiAuth": true})
	id, _, _ = peer.nextRequest()
	parent := "parent-thread"
	peer.reply(id, map[string]any{
		"thread": map[string]any{"id": "sub-1", "parentThreadId": parent}, "model": "m", "modelProvider": "openai",
	})
	if err := <-done; err == nil {
		t.Fatal("a subagent thread must not be bound as this launch's conversation")
	}
}

func TestCodexResumeRefusesInsteadOfStartingANewThread(t *testing.T) {
	peer := newCodexPeer(t, func(c *DriverSessionConfig) { c.ResumeConversationID = "stored-1" })
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, map[string]any{"userAgent": "f", "codexHome": "/f", "platformFamily": "unix", "platformOs": "linux"})
	id, _, _ = peer.nextRequest()
	peer.reply(id, map[string]any{"account": nil, "requiresOpenaiAuth": true})
	id, method, params := peer.nextRequest()
	if method != codexMethodThreadResume {
		t.Fatalf("a resume must use thread/resume, got %q", method)
	}
	if params["threadId"] != "stored-1" {
		t.Fatalf("thread/resume threadId = %v, want the STORED id", params["threadId"])
	}
	peer.replyError(id, -32000, "conversation unavailable")
	if err := <-done; err == nil {
		t.Fatal("a failed resume must refuse")
	}
	// And nothing else was attempted: no fallback thread/start.
	for _, line := range peer.sentLines() {
		if strings.Contains(string(line), `"`+codexMethodThreadStart+`"`) {
			t.Fatalf("a failed resume fell back to thread/start: %s", line)
		}
	}
}

func TestCodexResumeRefusesADifferentConversation(t *testing.T) {
	peer := newCodexPeer(t, func(c *DriverSessionConfig) { c.ResumeConversationID = "stored-1" })
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, map[string]any{"userAgent": "f", "codexHome": "/f", "platformFamily": "unix", "platformOs": "linux"})
	id, _, _ = peer.nextRequest()
	peer.reply(id, map[string]any{"account": nil, "requiresOpenaiAuth": true})
	id, _, _ = peer.nextRequest()
	peer.reply(id, map[string]any{
		"thread": map[string]any{"id": "somebody-else"}, "model": "m", "modelProvider": "openai",
	})
	if err := <-done; err == nil {
		t.Fatal("a resume that returns another conversation must be refused")
	}
}

// --- turns -------------------------------------------------------------------

func TestCodexTurnStartThenSteerThenCompletedLeavesTheSessionUsable(t *testing.T) {
	peer := newCodexPeer(t)
	if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	// Idle conversation: the first input STARTS a turn.
	go func() { _, _ = peer.session.Input(context.Background(), "hello") }()
	id, method, params := peer.nextRequest()
	if method != codexMethodTurnStart {
		t.Fatalf("first input = %q, want turn/start", method)
	}
	input, _ := params["input"].([]any)
	first, _ := input[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "hello" {
		t.Fatalf("turn input = %v", params["input"])
	}
	peer.reply(id, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
	waitUntil(t, "the turn is active", func() bool { return peer.session.ActiveTurn() == "turn-1" })

	// A second input while the turn is in flight STEERS it; it never opens a
	// second concurrent turn.
	go func() { _, _ = peer.session.Input(context.Background(), "also this") }()
	id, method, params = peer.nextRequest()
	if method != codexMethodTurnSteer {
		t.Fatalf("second input = %q, want turn/steer", method)
	}
	if params["expectedTurnId"] != "turn-1" {
		t.Fatalf("steer expectedTurnId = %v", params["expectedTurnId"])
	}
	peer.reply(id, map[string]any{"turnId": "turn-1"})

	// turn/completed finishes the TURN, not the session: the same peer accepts
	// another input immediately, which is the whole point of the state split.
	peer.notify(codexNotifyTurnCompleted, map[string]any{
		"threadId": "thread-1",
		"turn":     map[string]any{"id": "turn-1", "status": "completed", "items": []any{}},
	})
	waitUntil(t, "the turn cleared", func() bool { return peer.session.ActiveTurn() == "" })
	go func() { _, _ = peer.session.Input(context.Background(), "next") }()
	_, method, _ = peer.nextRequest()
	if method != codexMethodTurnStart {
		t.Fatalf("input after completion = %q, want a NEW turn/start", method)
	}
}

func TestCodexIgnoresAForeignTurnCompletion(t *testing.T) {
	peer := newCodexPeer(t)
	if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	go func() { _, _ = peer.session.Input(context.Background(), "hello") }()
	id, _, _ := peer.nextRequest()
	peer.reply(id, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
	waitUntil(t, "the turn is active", func() bool { return peer.session.ActiveTurn() == "turn-1" })
	peer.notify(codexNotifyTurnCompleted, map[string]any{
		"threadId": "another-thread",
		"turn":     map[string]any{"id": "turn-1", "status": "completed", "items": []any{}},
	})
	time.Sleep(20 * time.Millisecond)
	if peer.session.ActiveTurn() != "turn-1" {
		t.Fatal("a completion of ANOTHER conversation must complete nothing of ours")
	}
}

func TestCodexInterruptNeedsAnActiveTurnAndKeepsTheSession(t *testing.T) {
	peer := newCodexPeer(t)
	if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	// ADAPTATION, recorded: Interrupt now reports the uncertainty boundary, and the
	// assertion GAINED a half rather than losing one. Refusing an idle conversation
	// must also say that nothing was attempted — a work-fenced caller files an
	// attempt as a durable ambiguity, so a refusal that claims one is a clean
	// answer turned into an unresolvable UNKNOWN.
	if attempted, err := peer.session.Interrupt(context.Background()); err == nil || attempted {
		t.Fatalf("interrupting an idle conversation = (attempted %t, %v); want a pre-effect refusal", attempted, err)
	}
	go func() { _, _ = peer.session.Input(context.Background(), "hello") }()
	id, _, _ := peer.nextRequest()
	peer.reply(id, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
	waitUntil(t, "the turn is active", func() bool { return peer.session.ActiveTurn() == "turn-1" })

	type interruptOutcome struct {
		attempted bool
		err       error
	}
	done := make(chan interruptOutcome, 1)
	go func() {
		attempted, err := peer.session.Interrupt(context.Background())
		done <- interruptOutcome{attempted, err}
	}()
	id, method, params := peer.nextRequest()
	if method != codexMethodTurnInterrupt {
		t.Fatalf("interrupt sent %q", method)
	}
	if params["threadId"] != "thread-1" || params["turnId"] != "turn-1" {
		t.Fatalf("turn/interrupt params = %v", params)
	}
	peer.reply(id, map[string]any{})
	outcome := <-done
	if outcome.err != nil {
		t.Fatalf("interrupt: %v", outcome.err)
	}
	if !outcome.attempted {
		t.Fatal("the frame this peer just answered crossed; the interrupt cannot report no attempt")
	}
	if peer.session.ActiveTurn() != "" {
		t.Fatal("an interrupted turn is no longer the active one")
	}
	// The CONVERSATION survives: the next input starts a new turn on the same one.
	go func() { _, _ = peer.session.Input(context.Background(), "again") }()
	_, method, params = peer.nextRequest()
	if method != codexMethodTurnStart || params["threadId"] != "thread-1" {
		t.Fatalf("after interrupt: %q %v", method, params)
	}
}

// --- server requests ---------------------------------------------------------

// decodeReply reads the driver's answer to a server request with the given id.
func (p *codexPeer) awaitReplyTo(id string) map[string]any {
	p.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case f := <-p.frames:
			if got, ok := f["id"].(string); ok && got == id {
				return f
			}
		case <-deadline:
			p.t.Fatalf("timed out waiting for the answer to server request %q", id)
			return nil
		}
	}
}

// Every surface is answered in ITS OWN vocabulary. A generic reply would be wrong
// on four of the six, which is exactly the correction this file exists to hold.
func TestCodexApprovalRefusalsAreMethodSpecific(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		params map[string]any
		want   map[string]any
	}{
		{
			name:   "command execution declines and the agent continues",
			method: codexReqCommandApproval,
			params: map[string]any{"itemId": "i1", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1"},
			want:   map[string]any{"decision": "decline"},
		},
		{
			name:   "file change declines",
			method: codexReqFileChangeApproval,
			params: map[string]any{"itemId": "i1", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1"},
			want:   map[string]any{"decision": "decline"},
		},
		{
			// No `decision` at all on this surface, and NO cancel: the safe reply is an
			// empty grant scoped to the turn.
			name:   "permissions answers an empty grant, never a decision",
			method: codexReqPermissionsApproval,
			params: map[string]any{
				"cwd": "/w", "itemId": "i1", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1",
				"permissions": map[string]any{"network": map[string]any{"enabled": true}},
			},
			want: map[string]any{"permissions": map[string]any{}, "scope": "turn"},
		},
		{
			// The legacy contract's ordinary denial is STRUCTURED, not a bare string.
			name:   "legacy exec denies with the structured rejection",
			method: codexReqLegacyExecApproval,
			params: map[string]any{"callId": "c1", "command": []any{"ls"}, "conversationId": "thread-1", "cwd": "/w", "parsedCmd": []any{}},
			want:   map[string]any{"decision": map[string]any{"denied": map[string]any{"rejection": codexRefusalReason}}},
		},
		{
			name:   "legacy patch denies with the structured rejection",
			method: codexReqLegacyPatchApproval,
			params: map[string]any{"callId": "c1", "conversationId": "thread-1", "fileChanges": map[string]any{}},
			want:   map[string]any{"decision": map[string]any{"denied": map[string]any{"rejection": codexRefusalReason}}},
		},
		{
			// `action`, not `decision`.
			name:   "mcp elicitation uses action",
			method: codexReqMcpElicitation,
			params: map[string]any{"serverName": "s", "threadId": "thread-1"},
			want:   map[string]any{"action": "decline"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newCodexPeer(t)
			if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			go func() { _, _ = peer.session.Input(context.Background(), "go") }()
			id, _, _ := peer.nextRequest()
			peer.reply(id, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
			waitUntil(t, "the turn is active", func() bool { return peer.session.ActiveTurn() == "turn-1" })

			peer.requestFromServer("srv-1", tc.method, tc.params)
			reply := peer.awaitReplyTo("srv-1")
			result, _ := reply["result"].(map[string]any)
			if !reflect.DeepEqual(result, tc.want) {
				t.Fatalf("reply = %#v, want %#v", result, tc.want)
			}
			if _, isError := reply["error"]; isError {
				t.Fatalf("a supported surface must be answered, not errored: %#v", reply)
			}
		})
	}
}

// An unsupported request is ANSWERED with a protocol error: never a hang (which
// stalls the provider's turn forever) and never an approval.
func TestCodexUnsupportedServerRequestsGetAProtocolError(t *testing.T) {
	for _, method := range []string{codexReqUserInput, "item/tool/call", "attestation/generate", "totally/unknown"} {
		t.Run(method, func(t *testing.T) {
			peer := newCodexPeer(t)
			if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			peer.requestFromServer("srv-x", method, map[string]any{"threadId": "thread-1"})
			reply := peer.awaitReplyTo("srv-x")
			errObj, ok := reply["error"].(map[string]any)
			if !ok {
				t.Fatalf("reply = %#v, want a protocol error", reply)
			}
			if int(errObj["code"].(float64)) != codexErrMethodNotSupported {
				t.Fatalf("error code = %v", errObj["code"])
			}
			if _, hasResult := reply["result"]; hasResult {
				t.Fatalf("an unsupported request must not receive a result: %#v", reply)
			}
		})
	}
	// And the experimental one says WHY, so nobody reads its refusal as a bug.
	if !strings.Contains(codexUnsupportedRequestReason(codexReqUserInput), "experimental") {
		t.Fatal("the experimental user-input refusal must name why it is refused")
	}
}

// A request naming another conversation, or a turn that is no longer the active
// one, is answered with the method's CANCELLATION and never with a grant: a stale
// reply must not be able to authorize anything in a new turn.
func TestCodexRefusesForeignConversationAndStaleTurnApprovals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{"foreign conversation", map[string]any{"itemId": "i", "startedAtMs": 1, "threadId": "somebody-else", "turnId": "turn-1"}},
		{"stale turn", map[string]any{"itemId": "i", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newCodexPeer(t, func(c *DriverSessionConfig) {
				// Even with an authority that would say yes to everything.
				c.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
					return ProviderApprovalDecision{Allow: true, SessionScope: true}, nil
				}
			})
			if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			go func() { _, _ = peer.session.Input(context.Background(), "go") }()
			id, _, _ := peer.nextRequest()
			peer.reply(id, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
			waitUntil(t, "the turn is active", func() bool { return peer.session.ActiveTurn() == "turn-1" })

			peer.requestFromServer("srv-2", codexReqCommandApproval, tc.params)
			reply := peer.awaitReplyTo("srv-2")
			result, _ := reply["result"].(map[string]any)
			if result["decision"] != "cancel" {
				t.Fatalf("reply = %#v, want the method's cancellation", result)
			}
		})
	}
}

// The arrival check is not enough on its own: an authority takes time, and the
// turn it was asked about can end while it thinks. A grant written after that
// would authorize a turn that no longer exists.
func TestCodexRefusesAnApprovalWhoseTurnEndedWhileTheAuthorityDecided(t *testing.T) {
	release := make(chan struct{})
	peer := newCodexPeer(t, func(c *DriverSessionConfig) {
		c.ApprovalDeadline = 5 * time.Second
		c.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			<-release // the turn ends while this authority is deciding
			return ProviderApprovalDecision{Allow: true, SessionScope: true}, nil
		}
	})
	if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	go func() { _, _ = peer.session.Input(context.Background(), "go") }()
	id, _, _ := peer.nextRequest()
	peer.reply(id, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
	waitUntil(t, "the turn is active", func() bool { return peer.session.ActiveTurn() == "turn-1" })

	peer.requestFromServer("srv-late", codexReqCommandApproval, map[string]any{
		"itemId": "i", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1",
	})
	peer.notify(codexNotifyTurnCompleted, map[string]any{
		"threadId": "thread-1",
		"turn":     map[string]any{"id": "turn-1", "status": "completed", "items": []any{}},
	})
	waitUntil(t, "the turn ended", func() bool { return peer.session.ActiveTurn() == "" })
	close(release)

	reply := peer.awaitReplyTo("srv-late")
	result, _ := reply["result"].(map[string]any)
	if result["decision"] != "cancel" {
		t.Fatalf("reply = %#v, want the method's cancellation for a turn that ended", result)
	}
}

// The bounded deadline is answered in each surface's own vocabulary too: the
// legacy contract HAS `timed_out`, the v2 ones only `decline`, and permissions
// says it with the safe empty grant.
func TestCodexApprovalDeadlineUsesTheMethodTimeoutCodec(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		params map[string]any
		want   map[string]any
	}{
		{"legacy exec times out", codexReqLegacyExecApproval,
			map[string]any{"callId": "c", "command": []any{"ls"}, "conversationId": "thread-1", "cwd": "/w", "parsedCmd": []any{}},
			map[string]any{"decision": "timed_out"}},
		{"command execution declines", codexReqCommandApproval,
			map[string]any{"itemId": "i", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1"},
			map[string]any{"decision": "decline"}},
		{"permissions grants nothing", codexReqPermissionsApproval,
			map[string]any{"cwd": "/w", "itemId": "i", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1",
				"permissions": map[string]any{"network": map[string]any{"enabled": true}}},
			map[string]any{"permissions": map[string]any{}, "scope": "turn"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newCodexPeer(t, func(c *DriverSessionConfig) {
				c.ApprovalDeadline = 20 * time.Millisecond
				c.Approve = func(ctx context.Context, _ ProviderApprovalRequest) (ProviderApprovalDecision, error) {
					<-ctx.Done() // an authority that never answers in time
					return ProviderApprovalDecision{Allow: true}, nil
				}
			})
			if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			go func() { _, _ = peer.session.Input(context.Background(), "go") }()
			id, _, _ := peer.nextRequest()
			peer.reply(id, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
			waitUntil(t, "the turn is active", func() bool { return peer.session.ActiveTurn() == "turn-1" })

			peer.requestFromServer("srv-3", tc.method, tc.params)
			reply := peer.awaitReplyTo("srv-3")
			result, _ := reply["result"].(map[string]any)
			if !reflect.DeepEqual(result, tc.want) {
				t.Fatalf("reply = %#v, want %#v", result, tc.want)
			}
		})
	}
}

// An authority may not WIDEN what the provider asked for, and a session-scoped
// grant needs the authority to have said session.
func TestCodexPermissionGrantIsIntersectedWithTheRequest(t *testing.T) {
	request := codexPermissionProfile{
		FileSystem: &codexFileSystemPermissions{Read: []string{"/a", "/b"}},
	}
	tokens := codexPermissionTokens(request)
	want := []string{"fs:read:/a", "fs:read:/b"}
	if !reflect.DeepEqual(tokens, want) {
		t.Fatalf("tokens = %v, want %v", tokens, want)
	}
	// The authority grants one requested path AND one it invented.
	granted := codexGrantSubset(request, []string{"fs:read:/a", "fs:write:/etc/shadow", "network:enabled"})
	if granted.Network != nil {
		t.Fatal("network was not requested and must not be granted")
	}
	if granted.FileSystem == nil || !reflect.DeepEqual(granted.FileSystem.Read, []string{"/a"}) {
		t.Fatalf("granted read = %#v, want exactly the requested subset", granted.FileSystem)
	}
	if len(granted.FileSystem.Write) != 0 {
		t.Fatalf("an unrequested write was granted: %#v", granted.FileSystem.Write)
	}
}

func TestCodexPermissionsGrantHonoursTheAuthorizedScope(t *testing.T) {
	peer := newCodexPeer(t, func(c *DriverSessionConfig) {
		c.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			return ProviderApprovalDecision{Allow: true, Granted: []string{"fs:read:/a"}}, nil
		}
	})
	if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	go func() { _, _ = peer.session.Input(context.Background(), "go") }()
	id, _, _ := peer.nextRequest()
	peer.reply(id, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
	waitUntil(t, "the turn is active", func() bool { return peer.session.ActiveTurn() == "turn-1" })

	peer.requestFromServer("srv-4", codexReqPermissionsApproval, map[string]any{
		"cwd": "/w", "itemId": "i", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1",
		"permissions": map[string]any{"fileSystem": map[string]any{"read": []any{"/a", "/b"}}},
	})
	reply := peer.awaitReplyTo("srv-4")
	result, _ := reply["result"].(map[string]any)
	if result["scope"] != "turn" {
		t.Fatalf("scope = %v; a session grant needs the authority to have said session", result["scope"])
	}
	fs, _ := result["permissions"].(map[string]any)["fileSystem"].(map[string]any)
	read, _ := fs["read"].([]any)
	if len(read) != 1 || read[0] != "/a" {
		t.Fatalf("granted read = %v, want exactly the authorized subset", read)
	}
}

// The registry is bounded and its content is pinned, so a surface cannot be added
// silently and a removal is visible.
func TestCodexApprovalRegistryIsBounded(t *testing.T) {
	t.Parallel()
	want := []string{
		codexReqLegacyPatchApproval,
		codexReqLegacyExecApproval,
		codexReqCommandApproval,
		codexReqFileChangeApproval,
		codexReqPermissionsApproval,
		codexReqMcpElicitation,
	}
	got := codexSupportedApprovalMethods()
	sorted := append([]string(nil), want...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	if !reflect.DeepEqual(got, sorted) {
		t.Fatalf("supported approval methods = %v, want %v", got, sorted)
	}
	if _, ok := codexApprovalCodecs[codexReqUserInput]; ok {
		t.Fatal("the experimental user-input surface must not be in the supported registry")
	}
}

// A dead child fails every waiter instead of hanging one.
func TestCodexPumpFailsWaitersWhenTheChildDies(t *testing.T) {
	peer := newCodexPeer(t)
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	peer.nextRequest()
	peer.session.Close(errors.New("the owned process exited"))
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a handshake on a dead child must fail")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the handshake hung after the child died")
	}
}

// A non-JSON stdout line is not protocol and must not kill the pump: an official
// CLI may print a plain log line, and a dead session would be a worse answer.
func TestCodexPumpIgnoresNonProtocolLines(t *testing.T) {
	peer := newCodexPeer(t)
	peer.session.Deliver(OutputFrame{Stream: streamStdout, Data: []byte("warning: bubblewrap not found")})
	if _, err := peer.answerHandshake("thread-1", nil, false); err != nil {
		t.Fatalf("handshake after a plain log line: %v", err)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// --- the active-turn requirement, for surfaces whose wire has no turn ---------
//
// These are the independent review's discriminators, kept as permanent
// regressions. Their shape is the reviewer's; the only adaptation is that they
// now live beside the suite they guard.

// An installed Codex 0.153.4 legacy approval has no turnId field at all. The
// absence of the field cannot make the ratified active-turn requirement
// disappear: the request is bound to the turn observed on arrival, that id is
// what the authority is asked about, and the same id is rechecked before any
// grant crosses to the child.
func TestCodexLegacyApprovalCannotCrossActiveTurn(t *testing.T) {
	entered := make(chan ProviderApprovalRequest, 1)
	release := make(chan struct{})
	peer := newCodexPeer(t, func(c *DriverSessionConfig) {
		c.ApprovalDeadline = 5 * time.Second
		c.Approve = func(_ context.Context, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			entered <- req
			<-release
			return ProviderApprovalDecision{Allow: true}, nil
		}
	})
	if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	session := peer.session.(*codexSession)
	session.setActiveTurn("turn-1", "inProgress")

	peer.requestFromServer("legacy-cross-turn", codexReqLegacyExecApproval, map[string]any{
		"callId": "call-1", "command": []string{"true"}, "conversationId": "thread-1",
		"cwd": "/fixture", "parsedCmd": []any{},
	})
	req := <-entered
	session.clearTurn("turn-1")
	session.setActiveTurn("turn-2", "inProgress")
	close(release)

	reply := peer.awaitReplyTo("legacy-cross-turn")
	result, _ := reply["result"].(map[string]any)
	if req.TurnID != "turn-1" {
		t.Errorf("authority request TurnID = %q, want the active turn-1 captured at arrival", req.TurnID)
	}
	if decision := result["decision"]; decision != "abort" {
		t.Errorf("reply decision = %#v with active turn %q, want the legacy cancellation abort", decision, session.ActiveTurn())
	}
}

// The MCP schema makes turnId OPTIONAL. An omitted optional field carries the
// same obligation as an absent one: capture the current active turn and refuse a
// delayed grant once that turn is over.
func TestCodexOptionalTurnElicitationCannotCrossActiveTurn(t *testing.T) {
	entered := make(chan ProviderApprovalRequest, 1)
	release := make(chan struct{})
	var once sync.Once
	peer := newCodexPeer(t, func(c *DriverSessionConfig) {
		c.ApprovalDeadline = 5 * time.Second
		c.Approve = func(_ context.Context, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			once.Do(func() { entered <- req })
			<-release
			return ProviderApprovalDecision{Allow: true}, nil
		}
	})
	if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	session := peer.session.(*codexSession)
	session.setActiveTurn("turn-1", "inProgress")

	peer.requestFromServer("mcp-cross-turn", codexReqMcpElicitation, map[string]any{
		"serverName": "fixture", "threadId": "thread-1", "message": "continue?",
	})
	req := <-entered
	session.clearTurn("turn-1")
	session.setActiveTurn("turn-2", "inProgress")
	close(release)

	reply := peer.awaitReplyTo("mcp-cross-turn")
	result, _ := reply["result"].(map[string]any)
	if req.TurnID != "turn-1" {
		t.Errorf("authority request TurnID = %q, want the active turn-1 captured at arrival", req.TurnID)
	}
	if action := result["action"]; action != "cancel" {
		t.Errorf("reply action = %#v with active turn %q, want the MCP cancellation cancel", action, session.ActiveTurn())
	}
}

// A request that arrives when NOTHING is in flight belongs to no turn, so there
// is nothing it can be authorised against. The authority is never asked, and the
// answer is the method's own cancellation.
func TestCodexApprovalWithNoActiveTurnIsRefusedWithoutAskingTheAuthority(t *testing.T) {
	var asked atomic.Int32
	peer := newCodexPeer(t, func(c *DriverSessionConfig) {
		c.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			asked.Add(1)
			return ProviderApprovalDecision{Allow: true}, nil
		}
	})
	if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	for _, tc := range []struct {
		name, method string
		params       map[string]any
		want         map[string]any
	}{
		{"legacy exec", codexReqLegacyExecApproval,
			map[string]any{"callId": "c", "command": []string{"true"}, "conversationId": "thread-1", "cwd": "/f", "parsedCmd": []any{}},
			map[string]any{"decision": "abort"}},
		{"mcp elicitation", codexReqMcpElicitation,
			map[string]any{"serverName": "s", "threadId": "thread-1"},
			map[string]any{"action": "cancel"}},
		{"v2 command naming a turn", codexReqCommandApproval,
			map[string]any{"itemId": "i", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-ghost"},
			map[string]any{"decision": "cancel"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "idle-" + tc.name
			peer.requestFromServer(id, tc.method, tc.params)
			reply := peer.awaitReplyTo(id)
			result, _ := reply["result"].(map[string]any)
			if !reflect.DeepEqual(result, tc.want) {
				t.Fatalf("reply = %#v, want %#v", result, tc.want)
			}
		})
	}
	if got := asked.Load(); got != 0 {
		t.Fatalf("the authority was consulted %d time(s) for requests belonging to no turn", got)
	}
}

// The turn a correlated turn/start response establishes must be published in
// STREAM ORDER, so the request the provider is entitled to send on the very next
// line is judged against it. This asserts the ordering directly, without the
// runtime: the response and the request are delivered back to back, and the
// authority must see the started turn.
func TestCodexTurnIsPublishedBeforeTheNextLineIsJudged(t *testing.T) {
	entered := make(chan ProviderApprovalRequest, 1)
	peer := newCodexPeer(t, func(c *DriverSessionConfig) {
		c.Approve = func(_ context.Context, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			entered <- req
			return ProviderApprovalDecision{Allow: true}, nil
		}
	})
	if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	go func() { _, _ = peer.session.Input(context.Background(), "go") }()
	id, method, _ := peer.nextRequest()
	if method != codexMethodTurnStart {
		t.Fatalf("method = %q", method)
	}
	// Back to back on the same stream, exactly as the peer is allowed to send them.
	peer.reply(id, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
	peer.requestFromServer("immediate", codexReqCommandApproval, map[string]any{
		"itemId": "i", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1",
	})
	select {
	case req := <-entered:
		if req.TurnID != "turn-1" {
			t.Fatalf("authority TurnID = %q, want turn-1", req.TurnID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the immediate same-turn request never reached the authority")
	}
	reply := peer.awaitReplyTo("immediate")
	result, _ := reply["result"].(map[string]any)
	if result["decision"] != "accept" {
		t.Fatalf("immediate same-turn approval = %#v, want accept", result)
	}
}

// A launch that has lost its durable authority cancels instead of granting, even
// when the turn it named is still the active one.
func TestCodexApprovalCancelsWhenTheLaunchAuthorityIsGone(t *testing.T) {
	peer := newCodexPeer(t, func(c *DriverSessionConfig) {
		c.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			return ProviderApprovalDecision{Allow: true}, nil
		}
		c.AuthorityCheck = func(context.Context) error {
			return forbiddenErr("the session authority is no longer valid")
		}
	})
	if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	peer.session.(*codexSession).setActiveTurn("turn-1", "inProgress")
	peer.requestFromServer("lost-authority", codexReqCommandApproval, map[string]any{
		"itemId": "i", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1",
	})
	reply := peer.awaitReplyTo("lost-authority")
	result, _ := reply["result"].(map[string]any)
	if result["decision"] != "cancel" {
		t.Fatalf("reply = %#v, want cancel once the launch authority is gone", result)
	}
}
