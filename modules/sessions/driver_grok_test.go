// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Protocol-level tests for the official Grok ACP driver.
//
// They are about the WIRE and the DECISIONS, because that is where a driver is
// wrong in ways nothing else notices: a version mismatch accepted as a zero, an
// authentication method chosen from the advertisement instead of from the
// authorization, a persistent permission selected because it was the only one on
// offer. The runtime-level acceptance — a real child, a real run row, a real
// alias, real HTTP — is in runtime_grok_test.go; these two files prove different
// things and neither substitutes for the other.

// --- in-process peer ---------------------------------------------------------

// grokPeer drives a grokSession from the other side of the pipe: it captures what
// the session SENDS and lets a test answer it.
type grokPeer struct {
	t       *testing.T
	session DriverSession
	frames  chan map[string]any
	mu      sync.Mutex
	lines   [][]byte
	// blockSend, when set, makes every write fail, so a test can prove what an
	// unwritable stdin does to the turn.
	blockSend bool
}

func newGrokPeer(t *testing.T, opts ...func(*DriverSessionConfig)) *grokPeer {
	t.Helper()
	p := &grokPeer{t: t, frames: make(chan map[string]any, 64)}
	cfg := DriverSessionConfig{
		Send:             p.send,
		Warn:             func(string, ...any) {},
		ClientName:       "olivares",
		ClientVersion:    "test",
		WorkDir:          "/workspace/fixture",
		AuthSource:       AuthSourceAccountHome,
		CallTimeout:      2 * time.Second,
		ApprovalDeadline: 2 * time.Second,
		RunRef:           "run-test",
		ProfileRef:       "ppf_test",
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	p.session = grokDriver{}.OpenSession(cfg)
	return p
}

func (p *grokPeer) send(_ context.Context, line []byte) error {
	p.mu.Lock()
	blocked := p.blockSend
	if !blocked {
		p.lines = append(p.lines, append([]byte(nil), line...))
	}
	p.mu.Unlock()
	if blocked {
		return errors.New("fixture: the child's stdin refused the write")
	}
	var frame map[string]any
	if err := json.Unmarshal(line, &frame); err != nil {
		p.t.Errorf("the driver wrote a line that is not JSON: %s", line)
		return nil
	}
	p.frames <- frame
	return nil
}

func (p *grokPeer) next() map[string]any {
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
func (p *grokPeer) nextRequest() (int64, string, map[string]any) {
	p.t.Helper()
	for {
		f := p.next()
		id, ok := f["id"]
		if !ok {
			continue
		}
		num, _ := id.(float64)
		params, _ := f["params"].(map[string]any)
		method, _ := f["method"].(string)
		return int64(num), method, params
	}
}

func (p *grokPeer) reply(id int64, result any) {
	p.t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		p.t.Fatalf("marshal reply: %v", err)
	}
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *grokPeer) replyError(id int64, code int, message string) {
	p.t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message},
	})
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *grokPeer) requestFromServer(id, method string, params any) {
	p.t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *grokPeer) notify(method string, params any) {
	p.t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *grokPeer) sentLines() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.lines))
	copy(out, p.lines)
	return out
}

func (p *grokPeer) blockWrites() {
	p.mu.Lock()
	p.blockSend = true
	p.mu.Unlock()
}

// grokInitializeResult is the peer's initialize answer, shaped like the recorded
// one from the pinned 1.0.13 binary.
func grokInitializeResult(methods []any) map[string]any {
	return map[string]any{
		"protocolVersion": 1,
		"agentCapabilities": map[string]any{
			"loadSession": true,
			"sessionCapabilities": map[string]any{
				"list": map[string]any{}, "resume": map[string]any{}, "close": map[string]any{},
			},
		},
		"authMethods":  methods,
		"agentVersion": "1.0.13-fixture",
	}
}

func grokAllAuthMethods() []any {
	return []any{
		map[string]any{"id": grokAuthMethodBrowser, "name": "Grok"},
		map[string]any{"id": grokAuthMethodCachedToken, "name": "Cached token"},
		map[string]any{"id": grokAuthMethodAPIKey, "name": "xAI API key"},
	}
}

// answerHandshake plays the agent side of a NEW-conversation handshake.
func (p *grokPeer) answerHandshake(sessionID string, methods []any) (DriverHandshake, error) {
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
	if method != grokMethodInitialize {
		p.t.Fatalf("first request = %q, want initialize", method)
	}
	p.reply(id, grokInitializeResult(methods))
	for {
		id, method, _ = p.nextRequest()
		switch method {
		case grokMethodAuthenticate:
			p.reply(id, map[string]any{})
			continue
		case grokMethodSessionNew:
			p.reply(id, map[string]any{"sessionId": sessionID})
		default:
			p.t.Fatalf("unexpected handshake request %q", method)
		}
		break
	}
	res := <-done
	return res.hs, res.err
}

// --- the wire ----------------------------------------------------------------

// ACP requires the `jsonrpc` member; the Codex app-server's schema forbids it.
// That single difference is why the envelope is a switch on the shared pump, and
// this is the row that holds the Grok side of it.
func TestGrokWireCarriesTheJSONRPCMemberAndAdvertisesNoUnimplementedCapability(t *testing.T) {
	peer := newGrokPeer(t)
	if _, err := peer.answerHandshake("sess-1", grokAllAuthMethods()); err != nil {
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
		if frame["jsonrpc"] != "2.0" {
			t.Fatalf("every ACP frame must carry jsonrpc 2.0: %s", line)
		}
	}
	var init struct {
		Params struct {
			ProtocolVersion    int `json:"protocolVersion"`
			ClientCapabilities struct {
				FS struct {
					ReadTextFile  bool `json:"readTextFile"`
					WriteTextFile bool `json:"writeTextFile"`
				} `json:"fs"`
				Terminal bool `json:"terminal"`
			} `json:"clientCapabilities"`
		} `json:"params"`
	}
	if err := json.Unmarshal(lines[0], &init); err != nil {
		t.Fatalf("initialize is not readable: %s", lines[0])
	}
	if init.Params.ProtocolVersion != grokProtocolVersion {
		t.Fatalf("initialize declared protocolVersion %d", init.Params.ProtocolVersion)
	}
	caps := init.Params.ClientCapabilities
	if caps.FS.ReadTextFile || caps.FS.WriteTextFile || caps.Terminal {
		t.Fatalf("this client implements no filesystem or terminal handler and must advertise none: %s", lines[0])
	}
}

// The owned operate argv, and the forms that are NOT it. A leader shares one
// backend between clients — adoption of a process this run did not create — and
// serve/headless are network servers.
func TestGrokLaunchArgsAreAnOwnedNonLeaderStdioAgent(t *testing.T) {
	t.Parallel()
	d := NewGrokDriver()
	if got, want := d.LaunchArgs(DriverLaunch{}), []string{"agent", "--no-leader", "stdio"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	// Model and effort are the provider's OWN open strings, and the option ORDER is
	// the installed CLI's: agent options go after `agent` and before the mode.
	got := d.LaunchArgs(DriverLaunch{Model: "grok-4.6", Effort: "xhigh"})
	want := []string{"agent", "--no-leader", "--model", "grok-4.6", "--reasoning-effort", "xhigh", "stdio"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	if got[len(got)-1] != "stdio" {
		t.Fatalf("the mode must be last: %v", got)
	}
	for _, arg := range got {
		switch arg {
		case "--leader", "leader", "serve", "headless", "-p", "--prompt-file":
			t.Fatalf("the driver produced a non-owned form: %v", got)
		}
	}
	// The version pin is the provider's own documented variable, and it is the ONLY
	// environment this driver asks for: it never names a credential or a home.
	env := d.(ProviderDriverLaunchEnv).LaunchEnv(DriverLaunch{})
	if len(env) != 1 || env[0].Name != envGrokDisableAutoUpdate || env[0].Value != "1" {
		t.Fatalf("launch env = %v", env)
	}
	if providerHomeEnvName(env[0].Name) {
		t.Fatal("a driver may not name a variable the provider profile owns")
	}
}

// A peer speaking another major version is not a peer this client can hold to the
// contract it is about to enforce. A missing, null or non-numeric field is the
// same answer, not a zero to be accepted.
func TestGrokRefusesAProtocolVersionItDoesNotImplement(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"the implemented version", `1`, true},
		{"a later major", `2`, false},
		{"a string", `"1"`, false},
		{"null", `null`, false},
		{"missing", ``, false},
		{"an object", `{"major":1}`, false},
	} {
		if got := grokProtocolVersionMatches(json.RawMessage(tc.raw)); got != tc.want {
			t.Fatalf("%s: match = %t, want %t", tc.name, got, tc.want)
		}
	}

	// And end to end: the handshake refuses and nominates nothing.
	peer := newGrokPeer(t)
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, method, _ := peer.nextRequest()
	if method != grokMethodInitialize {
		t.Fatalf("first request = %q", method)
	}
	peer.reply(id, map[string]any{"protocolVersion": 99, "authMethods": []any{}})
	err := <-done
	if err == nil {
		t.Fatal("a mismatched protocol version must fail the handshake")
	}
	if peer.session.ConversationID() != "" {
		t.Fatal("a refused handshake nominated a conversation")
	}
	if n := len(peer.sentLines()); n != 1 {
		t.Fatalf("the driver wrote %d lines after a version mismatch; nothing may follow initialize", n)
	}
}

// The authentication METHOD is chosen from the AUTHORIZED source, never from the
// advertisement. There is no fallback between the two sources and the interactive
// browser flow is never selected.
func TestGrokAuthenticationMethodMatchesTheAuthorizedSource(t *testing.T) {
	t.Parallel()
	all := []grokAuthMethod{
		{ID: grokAuthMethodBrowser}, {ID: grokAuthMethodCachedToken}, {ID: grokAuthMethodAPIKey},
	}
	for _, tc := range []struct {
		name       string
		source     string
		advertised []grokAuthMethod
		want       string
		ok         bool
	}{
		{"account home takes the cached login", AuthSourceAccountHome, all, grokAuthMethodCachedToken, true},
		{"managed injection takes the api key", AuthSourceManagedInjection, all, grokAuthMethodAPIKey, true},
		{"no authorized source selects nothing", "", all, "", false},
		{"an unknown source selects nothing", "guesswork", all, "", false},
		{
			"account home does not fall back to the injected key",
			AuthSourceAccountHome,
			[]grokAuthMethod{{ID: grokAuthMethodAPIKey}}, "", false,
		},
		{
			"managed injection does not fall back to the account home",
			AuthSourceManagedInjection,
			[]grokAuthMethod{{ID: grokAuthMethodCachedToken}}, "", false,
		},
		{
			"the interactive sign-in is never selected",
			AuthSourceAccountHome,
			[]grokAuthMethod{{ID: grokAuthMethodBrowser}}, "", false,
		},
		{"nothing advertised selects nothing", AuthSourceAccountHome, nil, "", false},
	} {
		got, ok := grokAuthMethodForSource(tc.source, tc.advertised)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%s: method = %q,%t want %q,%t", tc.name, got, ok, tc.want, tc.ok)
		}
	}
	if !grokOnlyInteractiveOffered([]grokAuthMethod{{ID: grokAuthMethodBrowser}}) {
		t.Fatal("a browser-only advertisement is the interactive-only case")
	}
	if grokOnlyInteractiveOffered(nil) || grokOnlyInteractiveOffered(all) {
		t.Fatal("an empty or mixed advertisement is not the interactive-only case")
	}
}

// Readiness is what the PROVIDER established, and it is never inferred from a
// home path. Three advertisements, three honest answers.
func TestGrokReadinessComesFromTheProviderNotFromAPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		source  string
		methods []any
		want    string
	}{
		{"a compatible method authenticates", AuthSourceAccountHome, grokAllAuthMethods(), AuthStateReady},
		{
			"only the interactive sign-in is offered",
			AuthSourceAccountHome,
			[]any{map[string]any{"id": grokAuthMethodBrowser, "name": "Grok"}},
			AuthStateRequired,
		},
		// An EMPTY advertisement offers no usable method, which the binding contract
		// calls auth_required. It is not "unknown": unknown is neither ready nor
		// required, and required is the only state the runtime refuses a turn on.
		{"the agent asked for nothing at all", AuthSourceAccountHome, []any{}, AuthStateRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.source
			peer := newGrokPeer(t, func(cfg *DriverSessionConfig) { cfg.AuthSource = source })
			hs, err := peer.answerHandshake("sess-auth", tc.methods)
			if err != nil {
				t.Fatalf("handshake: %v", err)
			}
			if hs.AuthState != tc.want {
				t.Fatalf("readiness = %q, want %q", hs.AuthState, tc.want)
			}
			// Whatever the readiness, the conversation was still nominated by the
			// correlated root response and by nothing else.
			if hs.ConversationID != "sess-auth" {
				t.Fatalf("conversation = %q", hs.ConversationID)
			}
			authenticated := false
			for _, line := range peer.sentLines() {
				var frame map[string]any
				_ = json.Unmarshal(line, &frame)
				if frame["method"] == grokMethodAuthenticate {
					authenticated = true
				}
			}
			if want := tc.want == AuthStateReady; authenticated != want {
				t.Fatalf("authenticate sent = %t, want %t", authenticated, want)
			}
		})
	}
}

// --- the turn ----------------------------------------------------------------

// ⛔ THE ROW THIS DRIVER EXISTS FOR. session/prompt's correlated response is the
// TURN's completion, so Input must return on the DISPATCH and never wait for it.
// A driver that copied the Codex shape would block here until the call timeout.
func TestGrokInputReturnsOnDispatchAndTheTurnEndsOnItsOwnResponse(t *testing.T) {
	peer := newGrokPeer(t)
	if _, err := peer.answerHandshake("sess-turn", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	// Nothing answers this prompt. Input must still return promptly.
	started := time.Now()
	attempted, err := peer.session.Input(context.Background(), "do the thing")
	if err != nil || !attempted {
		t.Fatalf("Input = %t, %v", attempted, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Input waited %s for the turn to finish; it must return on the dispatch", elapsed)
	}
	id, method, params := peer.nextRequest()
	if method != grokMethodSessionPrompt {
		t.Fatalf("request = %q, want session/prompt", method)
	}
	if params["sessionId"] != "sess-turn" {
		t.Fatalf("prompt named %v", params["sessionId"])
	}
	turn := peer.session.ActiveTurn()
	if turn == "" {
		t.Fatal("the dispatched prompt did not record an active turn")
	}
	// A SECOND prompt is refused BEFORE any byte: ACP has no steer, so it would be a
	// second concurrent turn.
	before := len(peer.sentLines())
	attempted, err = peer.session.Input(context.Background(), "and another")
	if attempted {
		t.Fatal("a second prompt must be refused before anything is attempted")
	}
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict {
		t.Fatalf("second prompt = %v, want a 409 refusal", err)
	}
	if got := len(peer.sentLines()); got != before {
		t.Fatalf("a refused second prompt wrote %d extra line(s)", got-before)
	}
	// Only the correlated response ends the turn.
	peer.reply(id, map[string]any{"stopReason": "end_turn"})
	waitFor(t, "the correlated prompt response ended the turn", func() bool {
		return peer.session.ActiveTurn() == ""
	})
	// And the conversation is usable again on the same session.
	if attempted, err := peer.session.Input(context.Background(), "next"); err != nil || !attempted {
		t.Fatalf("input after completion = %t, %v", attempted, err)
	}
}

// A notification is addressed to nobody in particular. It may not nominate a
// conversation and it may not end a turn — not even one that looks like this
// conversation's.
func TestGrokNotificationsNeitherNominateNorFinishATurn(t *testing.T) {
	peer := newGrokPeer(t)
	if _, err := peer.answerHandshake("sess-note", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := peer.session.Input(context.Background(), "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	promptID, _, _ := peer.nextRequest()
	turn := peer.session.ActiveTurn()

	session := peer.session.(*grokSession)
	peer.notify(grokNotifySessionUpdate, map[string]any{
		"sessionId": "sess-note",
		"update":    map[string]any{"sessionUpdate": "agent_message_chunk"},
	})
	peer.notify(grokNotifySessionUpdate, map[string]any{
		"sessionId": "someone-elses-conversation",
		"update":    map[string]any{"sessionUpdate": "agent_message_chunk"},
	})
	// An extension notification this client does not implement changes nothing.
	peer.notify("_x.ai/mcp/servers_updated", map[string]any{})
	waitFor(t, "the updates were classified", func() bool {
		c := session.updateCounts()
		return c.live == 1 && c.foreign == 1
	})
	if got := peer.session.ActiveTurn(); got != turn {
		t.Fatalf("a notification changed the active turn: %q -> %q", turn, got)
	}
	if got := peer.session.ConversationID(); got != "sess-note" {
		t.Fatalf("a notification renamed the conversation: %q", got)
	}
	peer.reply(promptID, map[string]any{"stopReason": "end_turn"})
	waitFor(t, "the turn ended", func() bool { return peer.session.ActiveTurn() == "" })
}

// Close is the child's death. Every in-flight waiter fails and the dispatched
// prompt — which has no waiter — is ended by its own terminal callback, or the
// session would refuse every later input on a turn that can never answer.
func TestGrokCloseEndsTheDispatchedTurnWithoutLeakingAWaiter(t *testing.T) {
	peer := newGrokPeer(t)
	if _, err := peer.answerHandshake("sess-close", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := peer.session.Input(context.Background(), "long task"); err != nil {
		t.Fatalf("input: %v", err)
	}
	if peer.session.ActiveTurn() == "" {
		t.Fatal("no turn was recorded")
	}
	peer.session.Close(errors.New("the owned provider process exited"))
	if got := peer.session.ActiveTurn(); got != "" {
		t.Fatalf("the turn survived the process: %q", got)
	}
	// And a later input is refused as a dead channel, not as a busy turn.
	attempted, err := peer.session.Input(context.Background(), "after")
	if attempted {
		t.Fatal("input after Close must attempt nothing")
	}
	if err == nil {
		t.Fatal("input after Close must refuse")
	}
}

// A write that never left is not a turn. The registration is withdrawn, so the
// conversation does not spend the rest of its life refusing input as busy.
func TestGrokAFailedDispatchLeavesNoPhantomTurn(t *testing.T) {
	peer := newGrokPeer(t)
	if _, err := peer.answerHandshake("sess-write", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	peer.blockWrites()
	attempted, err := peer.session.Input(context.Background(), "go")
	if err == nil {
		t.Fatal("a failed write must be reported")
	}
	if !attempted {
		// Over-reporting the attempt is the safe direction: a write that failed may
		// still have put bytes on the child's stdin.
		t.Fatal("a failed write is an ambiguity, not a clean refusal")
	}
	if got := peer.session.ActiveTurn(); got != "" {
		t.Fatalf("a turn that never started is active: %q", got)
	}
}

// --- resume ------------------------------------------------------------------

// A successful resume is the correlated answer to a request that NAMED the id.
// The protocol does not promise the id back, so requiring it would turn every
// success into a failure — and a contradicting id is refused outright.
func TestGrokResumeConfirmsTheStoredIDWithoutRequiringItBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result any
		wantOK bool
	}{
		{"a null result is a legal success", nil, true},
		{"an empty object is a legal success", map[string]any{}, true},
		{"the same id echoed back", map[string]any{"sessionId": "stored-1"}, true},
		{"a DIFFERENT id is refused", map[string]any{"sessionId": "somebody-elses"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newGrokPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "stored-1" })
			type outcome struct {
				hs  DriverHandshake
				err error
			}
			done := make(chan outcome, 1)
			go func() {
				hs, err := peer.session.Handshake(context.Background())
				done <- outcome{hs, err}
			}()
			id, method, _ := peer.nextRequest()
			if method != grokMethodInitialize {
				t.Fatalf("first request = %q", method)
			}
			peer.reply(id, grokInitializeResult(grokAllAuthMethods()))
			id, method, _ = peer.nextRequest()
			if method == grokMethodAuthenticate {
				peer.reply(id, map[string]any{})
				id, method, _ = peer.nextRequest()
			}
			if method != grokMethodSessionResume {
				t.Fatalf("resume used %q; the advertised resume capability is preferred", method)
			}
			peer.reply(id, tc.result)
			res := <-done
			if tc.wantOK {
				if res.err != nil {
					t.Fatalf("resume: %v", res.err)
				}
				if res.hs.ConversationID != "stored-1" {
					t.Fatalf("resume bound %q", res.hs.ConversationID)
				}
				return
			}
			if res.err == nil {
				t.Fatal("a conflicting id must be refused")
			}
			if peer.session.ConversationID() != "" {
				t.Fatalf("a refused resume bound %q", peer.session.ConversationID())
			}
		})
	}
}

// A resume the agent refuses is a REFUSAL. It is not retried as a load and never
// becomes a new conversation: those are different conversations under one name.
func TestGrokAFailedResumeNeverStartsANewConversation(t *testing.T) {
	peer := newGrokPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "stored-2" })
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, grokInitializeResult(grokAllAuthMethods()))
	id, method, _ := peer.nextRequest()
	if method == grokMethodAuthenticate {
		peer.reply(id, map[string]any{})
		id, method, _ = peer.nextRequest()
	}
	if method != grokMethodSessionResume {
		t.Fatalf("request = %q", method)
	}
	peer.replyError(id, -32000, "conversation unavailable")
	if err := <-done; err == nil {
		t.Fatal("a refused resume must fail the handshake")
	}
	for _, line := range peer.sentLines() {
		var frame map[string]any
		_ = json.Unmarshal(line, &frame)
		switch frame["method"] {
		case grokMethodSessionNew:
			t.Fatalf("a refused resume fell back to session/new: %s", line)
		case grokMethodSessionLoad:
			t.Fatalf("a refused resume fell back to session/load: %s", line)
		}
	}
}

// Load is used only when resume is NOT advertised, and a peer that advertises
// neither refuses rather than starting something else.
func TestGrokResumeSelectsTheAdvertisedCapabilityOnce(t *testing.T) {
	t.Run("load when resume is not advertised", func(t *testing.T) {
		peer := newGrokPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "stored-3" })
		done := make(chan error, 1)
		go func() {
			_, err := peer.session.Handshake(context.Background())
			done <- err
		}()
		id, _, _ := peer.nextRequest()
		result := grokInitializeResult(grokAllAuthMethods())
		caps := result["agentCapabilities"].(map[string]any)
		caps["sessionCapabilities"] = map[string]any{"list": map[string]any{}, "close": map[string]any{}}
		peer.reply(id, result)
		id, method, params := peer.nextRequest()
		if method == grokMethodAuthenticate {
			peer.reply(id, map[string]any{})
			id, method, params = peer.nextRequest()
		}
		if method != grokMethodSessionLoad {
			t.Fatalf("request = %q, want session/load", method)
		}
		if params["sessionId"] != "stored-3" {
			t.Fatalf("load named %v", params["sessionId"])
		}
		peer.reply(id, nil)
		if err := <-done; err != nil {
			t.Fatalf("load: %v", err)
		}
	})

	t.Run("neither advertised refuses", func(t *testing.T) {
		peer := newGrokPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "stored-4" })
		done := make(chan error, 1)
		go func() {
			_, err := peer.session.Handshake(context.Background())
			done <- err
		}()
		id, _, _ := peer.nextRequest()
		result := grokInitializeResult([]any{})
		caps := result["agentCapabilities"].(map[string]any)
		caps["loadSession"] = false
		caps["sessionCapabilities"] = map[string]any{"list": map[string]any{}}
		peer.reply(id, result)
		if err := <-done; err == nil {
			t.Fatal("a peer that can neither resume nor load must be refused")
		}
		for _, line := range peer.sentLines() {
			var frame map[string]any
			_ = json.Unmarshal(line, &frame)
			if frame["method"] == grokMethodSessionNew {
				t.Fatalf("a refused resume started a new conversation: %s", line)
			}
		}
	})
}

// --- permissions --------------------------------------------------------------

// The decoder's own discriminators. Each of these is a request that cannot be
// answered by SELECTION, and answering it by position would be a grant nobody
// authorized.
func TestGrokPermissionRequestValidation(t *testing.T) {
	t.Parallel()
	ok := []grokPermissionOption{{OptionID: "a", Kind: grokPermissionAllowOnce}}
	for _, tc := range []struct {
		name   string
		params grokRequestPermissionParams
		valid  bool
	}{
		{"a well-formed request", grokRequestPermissionParams{SessionID: "s", Options: ok}, true},
		{"no conversation", grokRequestPermissionParams{Options: ok}, false},
		{"no options", grokRequestPermissionParams{SessionID: "s"}, false},
		{
			"an option with no id",
			grokRequestPermissionParams{SessionID: "s", Options: []grokPermissionOption{{Kind: grokPermissionAllowOnce}}},
			false,
		},
		{
			"two options with one id",
			grokRequestPermissionParams{SessionID: "s", Options: []grokPermissionOption{
				{OptionID: "a", Kind: grokPermissionAllowOnce}, {OptionID: "a", Kind: grokPermissionRejectOnce},
			}},
			false,
		},
		{
			"no kind this client understands",
			grokRequestPermissionParams{SessionID: "s", Options: []grokPermissionOption{{OptionID: "a", Kind: "teleport"}}},
			false,
		},
	} {
		why, valid := grokValidPermissionRequest(tc.params)
		if valid != tc.valid {
			t.Fatalf("%s: valid = %t (%s), want %t", tc.name, valid, why, tc.valid)
		}
		if !valid && why == "" {
			t.Fatalf("%s: a refusal must name its reason", tc.name)
		}
	}
}

// Selection is an INTERSECTION, in both directions, and a persistent option needs
// a matching authority decision.
func TestGrokSelectionNeverWidensWhatWasOfferedOrAuthorized(t *testing.T) {
	t.Parallel()
	offered := []grokPermissionOption{
		{OptionID: "allow-once", Kind: grokPermissionAllowOnce},
		{OptionID: "allow-always", Kind: grokPermissionAllowAlways},
		{OptionID: "reject-once", Kind: grokPermissionRejectOnce},
		{OptionID: "reject-always", Kind: grokPermissionRejectAlways},
	}
	for _, tc := range []struct {
		name    string
		options []grokPermissionOption
		dec     ProviderApprovalDecision
		want    string
		ok      bool
	}{
		{
			"a one-shot grant the authority named",
			offered, ProviderApprovalDecision{Allow: true, Granted: []string{"allow-once"}},
			"allow-once", true,
		},
		{
			"an id nobody offered grants nothing",
			offered, ProviderApprovalDecision{Allow: true, Granted: []string{"allow-everything"}},
			"", false,
		},
		{
			"a persistent option under a turn-scoped decision grants nothing",
			offered, ProviderApprovalDecision{Allow: true, Granted: []string{"allow-always"}},
			"", false,
		},
		{
			"a persistent option WITH session scope is selectable",
			offered, ProviderApprovalDecision{Allow: true, Granted: []string{"allow-always"}, SessionScope: true},
			"allow-always", true,
		},
		{
			"an authority that named nothing grants nothing",
			offered, ProviderApprovalDecision{Allow: true},
			"", false,
		},
		{
			"a rejection option is never selected as a grant",
			offered, ProviderApprovalDecision{Allow: true, Granted: []string{"reject-once"}},
			"", false,
		},
		{
			"only persistent options are offered and the decision is turn-scoped",
			[]grokPermissionOption{{OptionID: "allow-always", Kind: grokPermissionAllowAlways}},
			ProviderApprovalDecision{Allow: true, Granted: []string{"allow-always"}},
			"", false,
		},
	} {
		got, ok := grokSelectGrant(tc.options, tc.dec)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%s: grant = %q,%t want %q,%t", tc.name, got, ok, tc.want, tc.ok)
		}
	}

	// And the refusal side: only the ONE-SHOT rejection is selected. A standing
	// "never allow" is a persistent decision this driver does not take on its own.
	if got, ok := grokSelectRefusal(offered); !ok || got != "reject-once" {
		t.Fatalf("refusal = %q,%t", got, ok)
	}
	onlyPersistent := []grokPermissionOption{{OptionID: "reject-always", Kind: grokPermissionRejectAlways}}
	if _, ok := grokSelectRefusal(onlyPersistent); ok {
		t.Fatal("a persistent rejection must not be selected for a turn-scoped refusal")
	}
}

// Deny-closed on the wire: with no authority wired, the agent's own one-shot
// rejection comes back — a refusal it understands, never a hang and never a
// grant.
func TestGrokUnwiredAuthorityRefusesWithAnOfferedRejection(t *testing.T) {
	peer := newGrokPeer(t)
	if _, err := peer.answerHandshake("sess-perm", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := peer.session.Input(context.Background(), "run something"); err != nil {
		t.Fatalf("input: %v", err)
	}
	promptID, _, _ := peer.nextRequest()
	peer.requestFromServer("srv-1", grokReqRequestPermission, map[string]any{
		"sessionId": "sess-perm",
		"toolCall":  map[string]any{"toolCallId": "call-1", "kind": "execute"},
		"options": []any{
			map[string]any{"optionId": "allow-once", "kind": grokPermissionAllowOnce},
			map[string]any{"optionId": "reject-once", "kind": grokPermissionRejectOnce},
		},
	})
	reply := peer.nextResponse()
	if got := reply.Outcome.Outcome; got != grokOutcomeSelected {
		t.Fatalf("outcome = %q, want a selected rejection", got)
	}
	if reply.Outcome.OptionID != "reject-once" {
		t.Fatalf("selected %q, want the one-shot rejection", reply.Outcome.OptionID)
	}
	peer.reply(promptID, map[string]any{"stopReason": "end_turn"})
}

// A request that arrives with NO turn in flight has nothing to belong to. The
// absence of a turn id on the ACP wire is not the absence of the requirement.
func TestGrokPermissionWithNoActiveTurnIsCancelled(t *testing.T) {
	peer := newGrokPeer(t)
	if _, err := peer.answerHandshake("sess-idle", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	peer.requestFromServer("srv-2", grokReqRequestPermission, map[string]any{
		"sessionId": "sess-idle",
		"toolCall":  map[string]any{"toolCallId": "call-1", "kind": "execute"},
		"options": []any{
			map[string]any{"optionId": "allow-once", "kind": grokPermissionAllowOnce},
			map[string]any{"optionId": "reject-once", "kind": grokPermissionRejectOnce},
		},
	})
	if got := peer.nextResponse().Outcome.Outcome; got != grokOutcomeCancelled {
		t.Fatalf("outcome = %q, want cancelled", got)
	}
}

// A request naming another conversation is not ours to answer with a grant, even
// when a turn of ours is live and an authority would have allowed it.
func TestGrokPermissionForAForeignConversationIsCancelled(t *testing.T) {
	peer := newGrokPeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			return ProviderApprovalDecision{Allow: true, Granted: []string{"allow-once"}}, nil
		}
	})
	if _, err := peer.answerHandshake("sess-mine", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := peer.session.Input(context.Background(), "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	promptID, _, _ := peer.nextRequest()
	peer.requestFromServer("srv-3", grokReqRequestPermission, map[string]any{
		"sessionId": "sess-somebody-else",
		"toolCall":  map[string]any{"toolCallId": "call-1", "kind": "execute"},
		"options":   []any{map[string]any{"optionId": "allow-once", "kind": grokPermissionAllowOnce}},
	})
	if got := peer.nextResponse().Outcome.Outcome; got != grokOutcomeCancelled {
		t.Fatalf("outcome = %q, want cancelled", got)
	}
	peer.reply(promptID, map[string]any{"stopReason": "end_turn"})
}

// ⛔ AN INTERRUPTED TURN IS A CANCELLING TURN, AND A CANCELLING TURN GRANTS
// NOTHING.
//
// `session/cancel` is a notification: it ends nothing, and the turn stays
// OCCUPIED until its own correlated result — that part is right and this row
// keeps it. What was missing is the state in between. Between the cancel and the
// result the turn is still the active turn, so an approval that arrived in that
// window found a live turn, asked the authority, and was GRANTED: a tool call
// authorized for a turn the operator had just stopped.
//
// The refusal uses the agent's OWN offered one-shot rejection, and it stays
// one-shot: an authority that had authorized the persistent option for the
// session does not make a cancelling turn a place to record a standing "always".
//
// And the requirement is not only that no grant is written: the authority is NOT
// CONSULTED AT ALL. Asking is itself a governed side effect — it can put an
// approval in front of an operator, wake a policy engine, or record a decision —
// and none of those may happen for work the operator has already stopped.
func TestGrokPermissionAfterAnInterruptCannotBeGranted(t *testing.T) {
	var consults atomic.Int64
	peer := newGrokPeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			consults.Add(1)
			// The widest decision an authority can take: both grants, session-scoped.
			return ProviderApprovalDecision{
				Allow: true, Granted: []string{"allow-once", "allow-always"}, SessionScope: true,
			}, nil
		}
	})
	if _, err := peer.answerHandshake("sess-cancelling", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := peer.session.Input(context.Background(), "start a turn"); err != nil {
		t.Fatalf("input: %v", err)
	}
	promptID, method, _ := peer.nextRequest()
	if method != grokMethodSessionPrompt {
		t.Fatalf("request = %q, want %q", method, grokMethodSessionPrompt)
	}
	if attempted, err := peer.session.Interrupt(context.Background()); err != nil || !attempted {
		t.Fatalf("interrupt = attempted %t, err %v", attempted, err)
	}
	// The cancel did NOT end the turn: it is still the one this conversation has,
	// and a second one is still refused before any byte.
	if peer.session.ActiveTurn() == "" {
		t.Fatal("the cancel ended the turn; only its own correlated result may")
	}
	if _, err := peer.session.Input(context.Background(), "a second turn"); err == nil {
		t.Fatal("a second concurrent turn opened while the first was winding down")
	}

	peer.requestFromServer("srv-after-cancel", grokReqRequestPermission, map[string]any{
		"sessionId": "sess-cancelling",
		"toolCall":  map[string]any{"toolCallId": "call-after-cancel", "kind": "execute"},
		"options": []any{
			map[string]any{"optionId": "allow-once", "kind": grokPermissionAllowOnce},
			map[string]any{"optionId": "allow-always", "kind": grokPermissionAllowAlways},
			map[string]any{"optionId": "reject-once", "kind": grokPermissionRejectOnce},
		},
	})
	reply := peer.nextResponse()
	if reply.Outcome.OptionID == "allow-once" || reply.Outcome.OptionID == "allow-always" {
		t.Fatalf("a cancelling turn granted a permission: %+v", reply.Outcome)
	}
	if reply.Outcome.Outcome != grokOutcomeSelected || reply.Outcome.OptionID != "reject-once" {
		t.Fatalf("outcome = %+v, want the offered one-shot rejection", reply.Outcome)
	}
	// The count is read AFTER the answer is on the wire, which is the point at
	// which the deciding goroutine is known to have reached its answer: a zero read
	// before that would prove only that the test got there first.
	if n := consults.Load(); n != 0 {
		t.Fatalf("a permission that arrived after the cancel consulted the authority %d time(s)", n)
	}

	// And the turn ends where it always did: on its own correlated result.
	peer.reply(promptID, map[string]any{"stopReason": "cancelled"})
	waitFor(t, "the correlated result ended the cancelled turn", func() bool {
		return peer.session.ActiveTurn() == ""
	})
}

// The same rule from the OTHER SIDE OF THE CLOCK, and it is a different ordering
// rather than a variation on the one above. Here the request arrives while the
// turn is still live, so it is admitted and legitimately reaches the authority —
// and the authority is ALLOWED to take its time, so its decision can be handed
// back after a cancel it knew nothing about. A grant that lands late is still a
// grant for a turn the operator stopped.
//
// Two distinct mechanisms have to hold, and the test separates them because
// neither implies the other: the interrupt answers the request it finds already
// pending, with the protocol's own cancellation; and the late `Allow` that
// follows writes NOTHING, because the request is answered exactly once. Neither
// of them is the admission refusal of the test above — this request was admitted,
// and admitting it was correct.
func TestGrokAnApprovalGateEnteredBeforeTheCancelAndReturningAfterItCannotGrant(t *testing.T) {
	release := make(chan struct{}, 1)
	entered := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case release <- struct{}{}:
		default:
		}
	})
	peer := newGrokPeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return ProviderApprovalDecision{Allow: true, Granted: []string{"allow-once"}}, nil
		}
	})
	if _, err := peer.answerHandshake("sess-held", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := peer.session.Input(context.Background(), "start a turn"); err != nil {
		t.Fatalf("input: %v", err)
	}
	promptID, _, _ := peer.nextRequest()
	// BEFORE the cancel, on a live turn.
	peer.requestFromServer("srv-held", grokReqRequestPermission, map[string]any{
		"sessionId": "sess-held",
		"toolCall":  map[string]any{"toolCallId": "call-held", "kind": "execute"},
		"options": []any{
			map[string]any{"optionId": "allow-once", "kind": grokPermissionAllowOnce},
			map[string]any{"optionId": "reject-once", "kind": grokPermissionRejectOnce},
		},
	})
	// Waiting for the gate to be ENTERED is what makes the ordering a fact rather
	// than a hope: the request is registered and the authority is occupied before
	// the interrupt below runs, so this is the pending-callback case and not the
	// admission case by accident of scheduling.
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("a permission that arrived on a live turn never reached the authority")
	}
	if attempted, err := peer.session.Interrupt(context.Background()); err != nil || !attempted {
		t.Fatalf("interrupt = attempted %t, err %v", attempted, err)
	}
	// The interrupt answered the request it found pending, and it answered it
	// while the authority had still said nothing at all.
	reply := peer.nextResponse()
	if reply.Outcome.Outcome != grokOutcomeCancelled {
		t.Fatalf("outcome = %+v, want the protocol's cancellation written by the interrupt", reply.Outcome)
	}

	// And NOW the authority allows, after the cancellation it never saw.
	release <- struct{}{}
	grokWaitRequestResolved(t, peer, "srv-held")
	// Exactly one answer reached the agent. A second one would be a grant for a
	// turn the operator stopped, written after a refusal that was already correct.
	if n := grokAnswersTo(peer, "srv-held"); n != 1 {
		t.Fatalf("the driver wrote %d answers to srv-held; a late grant must write none", n)
	}
	peer.reply(promptID, map[string]any{"stopReason": "cancelled"})
}

// Shutdown sends the SAME `session/cancel`, so it leaves the same state behind,
// and the window it opens is real: between the graceful half of a stop and the
// process teardown the conversation is still live and the pump is still reading.
// An approval that arrives in it belongs to a turn that is being cancelled, and
// granting one would authorize a tool call for a session that is going away.
func TestGrokPermissionDuringShutdownCannotBeGranted(t *testing.T) {
	var consults atomic.Int64
	peer := newGrokPeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			consults.Add(1)
			return ProviderApprovalDecision{Allow: true, Granted: []string{"allow-once"}}, nil
		}
	})
	if _, err := peer.answerHandshake("sess-shutdown", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := peer.session.Input(context.Background(), "start a turn"); err != nil {
		t.Fatalf("input: %v", err)
	}
	promptID, _, _ := peer.nextRequest()

	done := make(chan struct{})
	go func() {
		defer close(done)
		peer.session.Shutdown(context.Background())
	}()
	// Shutdown cancels the turn and then asks for the advertised session/close. It
	// is waiting for that answer, which is what holds the window open here.
	closeID, method, _ := peer.nextRequest()
	if method != grokMethodSessionClose {
		t.Fatalf("request = %q, want %q", method, grokMethodSessionClose)
	}
	peer.requestFromServer("srv-shutdown", grokReqRequestPermission, map[string]any{
		"sessionId": "sess-shutdown",
		"toolCall":  map[string]any{"toolCallId": "call-shutdown", "kind": "execute"},
		"options": []any{
			map[string]any{"optionId": "allow-once", "kind": grokPermissionAllowOnce},
			map[string]any{"optionId": "reject-once", "kind": grokPermissionRejectOnce},
		},
	})
	reply := peer.nextResponse()
	if reply.Outcome.OptionID == "allow-once" {
		t.Fatalf("a session being shut down granted a permission: %+v", reply.Outcome)
	}
	if reply.Outcome.Outcome != grokOutcomeSelected || reply.Outcome.OptionID != "reject-once" {
		t.Fatalf("outcome = %+v, want the offered one-shot rejection", reply.Outcome)
	}
	// Shutdown marks the same cancellation, so it admits the same way: a request
	// that turns up in this window never reaches the authority either.
	if n := consults.Load(); n != 0 {
		t.Fatalf("a permission that arrived during shutdown consulted the authority %d time(s)", n)
	}
	peer.reply(closeID, map[string]any{})
	<-done
	peer.reply(promptID, map[string]any{"stopReason": "cancelled"})
}

// The arrival fact must OUTLIVE the state it was read from, and this proves it
// instead of asserting it in a comment.
//
// `clearTurn` RELEASES `cancelledTurn` when the cancelled turn's own correlated
// result comes back — correctly, and the control below depends on it: that
// release is how the next turn becomes ordinary again. But it means the live
// state is a WASTING asset. A resolver that re-read "is this turn cancelling?"
// at the check instead of carrying the answer from arrival would, once that
// result had landed, be told "no" about a request that arrived while the answer
// was "yes" — and would consult the authority for work the operator stopped.
//
// That interleaving is real and it is NOT reachable from the wire on a schedule a
// test can force: it lives between the pump spawning the resolver and the
// resolver reaching its check. So the resolver is driven directly, with the state
// deliberately left in the losing configuration — mark released, turn ended, the
// request still carrying the fact it arrived with. If the fact ever stops being
// carried, this is the row that fails.
func TestGrokTheArrivalCancellationFactOutlivesTheTurnItNames(t *testing.T) {
	var consults atomic.Int64
	peer := newGrokPeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			consults.Add(1)
			return ProviderApprovalDecision{Allow: true, Granted: []string{"allow-once"}}, nil
		}
	})
	sess, ok := peer.session.(*grokSession)
	if !ok {
		t.Fatalf("peer session is %T, want *grokSession", peer.session)
	}
	if _, err := peer.answerHandshake("sess-outlives", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := peer.session.Input(context.Background(), "start a turn"); err != nil {
		t.Fatalf("input: %v", err)
	}
	promptID, _, _ := peer.nextRequest()
	turn := peer.session.ActiveTurn()
	if turn == "" {
		t.Fatal("the dispatched prompt opened no turn")
	}
	if attempted, err := peer.session.Interrupt(context.Background()); err != nil || !attempted {
		t.Fatalf("interrupt = attempted %t, err %v", attempted, err)
	}
	// The correlated result lands and takes the mark with it. From here the live
	// state says nothing at all is being cancelled.
	peer.reply(promptID, map[string]any{"stopReason": "cancelled"})
	waitFor(t, "the correlated result ended the cancelled turn", func() bool {
		return peer.session.ActiveTurn() == ""
	})
	if sess.turnCancelled(turn) {
		t.Fatal("the cancellation mark outlived its own turn; the live state is not the losing configuration this row needs")
	}

	// A request that ARRIVED during the cancellation, resolving only now. Nothing
	// about the session still remembers the cancel — only the request does.
	raw, err := json.Marshal(map[string]any{
		"sessionId": "sess-outlives",
		"toolCall":  map[string]any{"toolCallId": "call-outlives", "kind": "execute"},
		"options": []any{
			map[string]any{"optionId": "allow-once", "kind": grokPermissionAllowOnce},
			map[string]any{"optionId": "reject-once", "kind": grokPermissionRejectOnce},
		},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	id := json.RawMessage(`"srv-outlives"`)
	sess.resolveServerRequest(
		&grokServerRequest{id: id, method: grokReqRequestPermission},
		string(id), raw, "sess-outlives", turn, true,
	)

	reply := peer.nextResponse()
	if reply.Outcome.Outcome != grokOutcomeSelected || reply.Outcome.OptionID != "reject-once" {
		t.Fatalf("outcome = %+v, want the offered one-shot rejection", reply.Outcome)
	}
	if n := consults.Load(); n != 0 {
		t.Fatalf("the authority was consulted %d time(s) for a request whose arrival fact was dropped", n)
	}
}

// The OVER-CORRECTION control, and it is why the cancellation is recorded
// against the turn it was taken for rather than as a session-wide flag: once the
// cancelled turn's own correlated result has come back, the conversation is
// ordinary again. The next turn opens on the same session and a genuinely
// authorized approval is granted, exactly as it would have been before.
func TestGrokTheTurnAfterACancelledResultIsAuthorizedNormally(t *testing.T) {
	peer := newGrokPeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			return ProviderApprovalDecision{Allow: true, Granted: []string{"allow-once"}}, nil
		}
	})
	if _, err := peer.answerHandshake("sess-again", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := peer.session.Input(context.Background(), "first turn"); err != nil {
		t.Fatalf("input: %v", err)
	}
	first, _, _ := peer.nextRequest()
	if attempted, err := peer.session.Interrupt(context.Background()); err != nil || !attempted {
		t.Fatalf("interrupt = attempted %t, err %v", attempted, err)
	}
	peer.reply(first, map[string]any{"stopReason": "cancelled"})
	waitFor(t, "the correlated result ended the cancelled turn", func() bool {
		return peer.session.ActiveTurn() == ""
	})

	if _, err := peer.session.Input(context.Background(), "second turn"); err != nil {
		t.Fatalf("the conversation refused a turn after a cancelled one completed: %v", err)
	}
	second, method, _ := peer.nextRequest()
	if method != grokMethodSessionPrompt {
		t.Fatalf("request = %q, want %q", method, grokMethodSessionPrompt)
	}
	peer.requestFromServer("srv-again", grokReqRequestPermission, map[string]any{
		"sessionId": "sess-again",
		"toolCall":  map[string]any{"toolCallId": "call-again", "kind": "execute"},
		"options": []any{
			map[string]any{"optionId": "allow-once", "kind": grokPermissionAllowOnce},
			map[string]any{"optionId": "reject-once", "kind": grokPermissionRejectOnce},
		},
	})
	reply := peer.nextResponse()
	if reply.Outcome.Outcome != grokOutcomeSelected || reply.Outcome.OptionID != "allow-once" {
		t.Fatalf("outcome = %+v, want the authorized one-shot grant", reply.Outcome)
	}
	peer.reply(second, map[string]any{"stopReason": "end_turn"})
}

// --- an answer is also a reason not to ask ------------------------------------

// grokPermissionParams is one permission request offering BOTH grants and the
// one-shot rejection: the widest thing an agent can put on the wire, so a row
// that ends without a grant ended without one for a reason.
func grokPermissionParams(conversation, call string) map[string]any {
	return map[string]any{
		"sessionId": conversation,
		"toolCall":  map[string]any{"toolCallId": call, "kind": "execute"},
		"options": []any{
			map[string]any{"optionId": "allow-once", "kind": grokPermissionAllowOnce},
			map[string]any{"optionId": "allow-always", "kind": grokPermissionAllowAlways},
			map[string]any{"optionId": "reject-once", "kind": grokPermissionRejectOnce},
		},
	}
}

// grokOpenTurn handshakes and dispatches a prompt, returning the correlation id
// of the request that opened the turn.
func grokOpenTurn(t *testing.T, peer *grokPeer, conversation string) int64 {
	t.Helper()
	if _, err := peer.answerHandshake(conversation, grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := peer.session.Input(context.Background(), "start a turn"); err != nil {
		t.Fatalf("input: %v", err)
	}
	promptID, method, _ := peer.nextRequest()
	if method != grokMethodSessionPrompt {
		t.Fatalf("request = %q, want %q", method, grokMethodSessionPrompt)
	}
	if peer.session.ActiveTurn() == "" {
		t.Fatal("the dispatched prompt opened no turn")
	}
	return promptID
}

// grokSessionOf is the concrete session behind the peer, for the rows that have
// to drive an interleaving the wire cannot schedule.
func grokSessionOf(t *testing.T, peer *grokPeer) *grokSession {
	t.Helper()
	sess, ok := peer.session.(*grokSession)
	if !ok {
		t.Fatalf("peer session is %T, want *grokSession", peer.session)
	}
	return sess
}

// grokRequestRegistered reports whether a fixture request id is in `pending`
// RIGHT NOW, which is how a row tells the pending-callback ordering from the
// arrival one instead of assuming which of them it got.
func grokRequestRegistered(t *testing.T, peer *grokPeer, id string) bool {
	t.Helper()
	sess := grokSessionOf(t, peer)
	raw, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("marshal request id: %v", err)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	_, ok := sess.pending[string(raw)]
	return ok
}

// ⛔ A REQUEST THE CANCELLATION HAS ALREADY ANSWERED IS NOT ONE AN AUTHORITY IS
// ASKED ABOUT EITHER.
//
// This is the ordering NEXT TO the admission refusal, and it is a different one:
// the request ARRIVED on a live turn, so it carries no arrival fact and admitting
// it was correct. What happens afterwards is the whole row — the interrupt finds
// it in `pending` and answers it with the protocol's cancellation, and only THEN
// does its resolver get to run. The once-only reply already stopped it from
// granting; it did not stop it from ASKING, and asking is the governed side
// effect: an approval in front of an operator, a policy engine woken, a decision
// recorded, all for a request that is already answered and a turn that is already
// stopped.
//
// The interleaving is not reachable from the wire on a schedule a test can force
// — it lives between the pump registering the request and the resolver reaching
// its admission point — so the row builds it: the request is registered exactly
// as the pump registers one, proven NOT to have entered approval, answered by a
// real `Interrupt`, and only then released. The release is a direct call, so its
// RETURN is the completion of the whole resolution: a zero read after it is a
// fact about the resolution and not about when the test looked.
func TestGrokAPermissionAlreadyAnsweredByTheCancellationIsNeverAdmitted(t *testing.T) {
	var consults, authority atomic.Int64
	peer := newGrokPeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			consults.Add(1)
			// The widest decision an authority can take, so nothing here is refused for
			// want of an authorization.
			return ProviderApprovalDecision{
				Allow: true, Granted: []string{"allow-once", "allow-always"}, SessionScope: true,
			}, nil
		}
		cfg.AuthorityCheck = func(context.Context) error {
			authority.Add(1)
			return nil
		}
	})
	sess := grokSessionOf(t, peer)
	promptID := grokOpenTurn(t, peer, "sess-answered")
	turn := peer.session.ActiveTurn()

	// REGISTERED ON A LIVE TURN, under the same mutex and with the same state the
	// pump writes: in `pending`, so the interrupt will find it, and carrying no
	// arrival fact, because at this instant nothing is being cancelled.
	if sess.turnCancelled(turn) {
		t.Fatal("the turn was already cancelling; this row needs a live one")
	}
	id := json.RawMessage(`"srv-answered"`)
	req := &grokServerRequest{id: id, method: grokReqRequestPermission}
	sess.mu.Lock()
	sess.pending[string(id)] = req
	sess.mu.Unlock()
	// It has NOT entered approval, and nothing has yet run that could take it there.
	if n := consults.Load(); n != 0 {
		t.Fatalf("the authority was consulted %d time(s) before the request resolved at all", n)
	}

	// The operator stops the turn. The interrupt answers the request it found
	// pending, with the protocol's own cancellation.
	if attempted, err := peer.session.Interrupt(context.Background()); err != nil || !attempted {
		t.Fatalf("interrupt = attempted %t, err %v", attempted, err)
	}
	reply := peer.nextResponse()
	if reply.Outcome.Outcome != grokOutcomeCancelled {
		t.Fatalf("outcome = %+v, want the protocol's cancellation written by the interrupt", reply.Outcome)
	}

	// And NOW the resolver resumes, into a state where every other check would let
	// it through: the conversation is ours, the turn is non-empty, and it is still
	// the active one.
	raw, err := json.Marshal(grokPermissionParams("sess-answered", "call-answered"))
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	sess.resolveServerRequest(req, string(id), raw, "sess-answered", turn, false)

	if n := consults.Load(); n != 0 {
		t.Fatalf("a request already answered by the cancellation consulted the authority %d time(s)", n)
	}
	if n := authority.Load(); n != 0 {
		t.Fatalf("a request already answered by the cancellation re-proved the launch authority %d time(s)", n)
	}
	// One reply, and it is the interrupt's. A second write would be the late answer
	// the once exists to forbid.
	if n := grokAnswersTo(peer, "srv-answered"); n != 1 {
		t.Fatalf("the driver wrote %d answers to srv-answered, want exactly the cancellation", n)
	}
	// The turn still ends where it always did: on its own correlated result.
	peer.reply(promptID, map[string]any{"stopReason": "cancelled"})
	waitFor(t, "the correlated result ended the cancelled turn", func() bool {
		return peer.session.ActiveTurn() == ""
	})
}

// The SAME two events in the OTHER order, and it is the control that keeps the
// refusal above from becoming "a cancelled turn means the authority is dropped".
//
// Here the request is ADMITTED before the cancellation exists: the gate is
// entered, which is what makes the ordering a fact rather than a hope, and the
// interrupt lands while the authority is still thinking. An admitted request is
// allowed to FINISH — the authority may take its time and its callback may return
// long afterwards — and what it may not do is turn that late decision into a
// grant. Both halves are asserted, because a correction that refused this request
// too would pass the row above and still be wrong.
func TestGrokAPermissionAdmittedBeforeTheCancellationStillFinishesAndCannotGrant(t *testing.T) {
	var consults, authority atomic.Int64
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	peer := newGrokPeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			consults.Add(1)
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return ProviderApprovalDecision{
				Allow: true, Granted: []string{"allow-once", "allow-always"}, SessionScope: true,
			}, nil
		}
		cfg.AuthorityCheck = func(context.Context) error {
			authority.Add(1)
			return nil
		}
	})
	promptID := grokOpenTurn(t, peer, "sess-admitted")
	peer.requestFromServer("srv-admitted", grokReqRequestPermission,
		grokPermissionParams("sess-admitted", "call-admitted"))
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("a permission that arrived on a live turn never reached the authority")
	}

	if attempted, err := peer.session.Interrupt(context.Background()); err != nil || !attempted {
		t.Fatalf("interrupt = attempted %t, err %v", attempted, err)
	}
	reply := peer.nextResponse()
	if reply.Outcome.Outcome != grokOutcomeCancelled {
		t.Fatalf("outcome = %+v, want the protocol's cancellation written by the interrupt", reply.Outcome)
	}

	// The authority answers Allow, after a cancellation it never saw.
	close(release)
	grokWaitRequestResolved(t, peer, "srv-admitted")
	if n := consults.Load(); n != 1 {
		t.Fatalf("an admitted request consulted the authority %d time(s), want exactly once: it was admitted before the cancel and must finish", n)
	}
	// It finishes at the turn re-check, which is BEFORE the durable authority: a
	// cancelled turn is not a place to spend that check either.
	if n := authority.Load(); n != 0 {
		t.Fatalf("a decision handed back after the cancellation re-proved the launch authority %d time(s)", n)
	}
	if n := grokAnswersTo(peer, "srv-admitted"); n != 1 {
		t.Fatalf("the driver wrote %d answers to srv-admitted; a late grant must write none", n)
	}
	peer.reply(promptID, map[string]any{"stopReason": "cancelled"})
}

// The OVER-CORRECTION control for the refusal, and it is about SCOPE: the fact
// that stops the consult is recorded on the REQUEST, so it cannot survive into
// the conversation. Once the cancelled turn's own correlated result has come
// back, the next turn is ordinary and a genuinely authorized approval is granted,
// with the authority consulted exactly once — the once it was never consulted for
// the request that was already answered.
func TestGrokTheTurnAfterAnAnsweredRefusalIsAuthorizedNormally(t *testing.T) {
	var consults atomic.Int64
	peer := newGrokPeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			consults.Add(1)
			return ProviderApprovalDecision{Allow: true, Granted: []string{"allow-once"}}, nil
		}
	})
	sess := grokSessionOf(t, peer)
	first := grokOpenTurn(t, peer, "sess-after-answered")
	turn := peer.session.ActiveTurn()

	id := json.RawMessage(`"srv-first"`)
	req := &grokServerRequest{id: id, method: grokReqRequestPermission}
	sess.mu.Lock()
	sess.pending[string(id)] = req
	sess.mu.Unlock()
	if attempted, err := peer.session.Interrupt(context.Background()); err != nil || !attempted {
		t.Fatalf("interrupt = attempted %t, err %v", attempted, err)
	}
	if got := peer.nextResponse().Outcome.Outcome; got != grokOutcomeCancelled {
		t.Fatalf("outcome = %q, want the interrupt's cancellation", got)
	}
	raw, err := json.Marshal(grokPermissionParams("sess-after-answered", "call-first"))
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	sess.resolveServerRequest(req, string(id), raw, "sess-after-answered", turn, false)
	if n := consults.Load(); n != 0 {
		t.Fatalf("the already-answered request consulted the authority %d time(s)", n)
	}

	peer.reply(first, map[string]any{"stopReason": "cancelled"})
	waitFor(t, "the correlated result ended the cancelled turn", func() bool {
		return peer.session.ActiveTurn() == ""
	})
	if _, err := peer.session.Input(context.Background(), "second turn"); err != nil {
		t.Fatalf("the conversation refused a turn after a cancelled one completed: %v", err)
	}
	second, _, _ := peer.nextRequest()
	peer.requestFromServer("srv-second", grokReqRequestPermission,
		grokPermissionParams("sess-after-answered", "call-second"))
	reply := peer.nextResponse()
	if reply.Outcome.Outcome != grokOutcomeSelected || reply.Outcome.OptionID != "allow-once" {
		t.Fatalf("outcome = %+v, want the authorized one-shot grant on the next turn", reply.Outcome)
	}
	if n := consults.Load(); n != 1 {
		t.Fatalf("the authority was consulted %d time(s) across both turns, want exactly the second one", n)
	}
	peer.reply(second, map[string]any{"stopReason": "end_turn"})
}

// ⛔ AND THE CLAIM IS RECORDED BEFORE THE REPLY IS WRITTEN, WHICH IS THE WHOLE
// ORDERING GUARANTEE AND NOT AN IMPLEMENTATION DETAIL.
//
// A write to the child's stdin can BLOCK — backpressure is the normal state of a
// busy agent, not a fault — and if the request were only marked answered once
// those bytes had left, every resolver arriving during the write would be
// admitted and would consult the authority for a request already spoken for. The
// window would be as wide as the child is slow.
//
// So the row pins it: the cancellation's answer is held INSIDE the write, and the
// resolver is released while it is still held. The claim is the only thing that
// can be visible at that instant, and the refusal has to come from it.
func TestGrokTheCancellationsClaimIsRecordedBeforeItsReplyIsWritten(t *testing.T) {
	var consults atomic.Int64
	writing := make(chan struct{}, 1)
	releaseWrite := make(chan struct{})
	interrupted := make(chan struct{})
	// The write is released even when the row fails early, and the interrupting
	// goroutine is joined before the test ends: a goroutine still blocked on the
	// child's stdin would outlive the row that parked it there.
	releaseTheWrite := sync.OnceFunc(func() { close(releaseWrite) })
	t.Cleanup(func() {
		releaseTheWrite()
		select {
		case <-interrupted:
		case <-time.After(3 * time.Second):
		}
	})
	peer := newGrokPeer(t, func(cfg *DriverSessionConfig) {
		inner := cfg.Send
		cfg.Send = func(ctx context.Context, line []byte) error {
			if strings.Contains(string(line), `"id":"srv-blocked"`) {
				select {
				case writing <- struct{}{}:
				default:
				}
				<-releaseWrite
			}
			return inner(ctx, line)
		}
		cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			consults.Add(1)
			return ProviderApprovalDecision{Allow: true, Granted: []string{"allow-once"}}, nil
		}
	})
	sess := grokSessionOf(t, peer)
	promptID := grokOpenTurn(t, peer, "sess-blocked")
	turn := peer.session.ActiveTurn()

	id := json.RawMessage(`"srv-blocked"`)
	req := &grokServerRequest{id: id, method: grokReqRequestPermission}
	sess.mu.Lock()
	sess.pending[string(id)] = req
	sess.mu.Unlock()

	go func() {
		defer close(interrupted)
		if attempted, err := peer.session.Interrupt(context.Background()); err != nil || !attempted {
			t.Errorf("interrupt = attempted %t, err %v", attempted, err)
		}
	}()
	select {
	case <-writing:
	case <-time.After(3 * time.Second):
		t.Fatal("the interrupt never started writing the cancellation")
	}

	// The reply is still in the pipe. The resolver runs anyway.
	resolved := make(chan struct{})
	go func() {
		defer close(resolved)
		raw, err := json.Marshal(grokPermissionParams("sess-blocked", "call-blocked"))
		if err != nil {
			t.Errorf("marshal params: %v", err)
			return
		}
		sess.resolveServerRequest(req, string(id), raw, "sess-blocked", turn, false)
	}()
	// Either it decided without asking, or it asked. Both are observable, so the
	// row waits for the decision instead of for a duration.
	waitFor(t, "the resolver reached its decision", func() bool {
		select {
		case <-resolved:
			return true
		default:
		}
		return consults.Load() > 0
	})

	releaseTheWrite()
	select {
	case <-resolved:
	case <-time.After(3 * time.Second):
		t.Fatal("the resolver never finished after the write was released")
	}
	<-interrupted
	if n := consults.Load(); n != 0 {
		t.Fatalf("a resolver running during the cancellation's own write consulted the authority %d time(s)", n)
	}
	if n := grokAnswersTo(peer, "srv-blocked"); n != 1 {
		t.Fatalf("the driver wrote %d answers to srv-blocked, want exactly the cancellation", n)
	}
	if got := peer.nextResponse().Outcome.Outcome; got != grokOutcomeCancelled {
		t.Fatalf("outcome = %q, want the interrupt's cancellation", got)
	}
	peer.reply(promptID, map[string]any{"stopReason": "cancelled"})
}

// ⛔ THE SAME FAMILY ON THE REAL PATH, DRIVEN BY THE PUMP, AS A SOAK OVER
// INVARIANTS — AND NOT AS A CLAIM ABOUT WHEN A CALLBACK RAN.
//
// Origin, because it is not mine: this is observation probe **O1**, authored
// earlier and independently by the reviewer of `c336583d` (ClaudeD) in
// `assessments/.../final-independent-review/reviewer_final_probe_test.go`, where it
// MEASURED 20 iterations of 20 consulting the authority after the cancellation had
// already answered the request.
//
// ⛔ AND CORRECTED HERE, on finding **O1-T1** of the independent review of
// `73c3fa5d9d`. Carried in, it asserted that no callback may be observed after the
// cancellation's answer — which is NOT the contract. Admission is the successful
// `req.answered.Load()`, not the callback's entry: a resolver may be admitted, be
// descheduled between that load and `cfg.Approve`, and have its callback run after
// the cancellation answered the request. That is the ordering root ratified as
// preservation, and the reviewer drove it deliberately with an external barrier
// after the admission load: 20 of 20 iterations were rejected by a row measuring
// callback timing instead of admission. A permanent gate cannot reject a schedule
// the contract allows, so this row now asserts only what it can actually observe
// across BOTH orderings, and reports the timing as observation.
//
// The invariants, which hold whichever way the two goroutines interleave:
// exactly ONE answer per request — never two, which is a late grant behind an
// answer, and never zero, which is an agent waiting for ever; that answer is never
// a GRANT and is one of the two no-grant answers this driver may write; the
// authority is consulted AT MOST once; the resolution TERMINATES; and the turn is
// the one this row opened — a cancel does not end it, and its own correlated
// result does.
//
// What still owns the O1 property are the deterministic rows above, which order
// the two events instead of watching them.
func TestGrokConcurrentCancellationAnswersEachPermissionOnceAndGrantsNone(t *testing.T) {
	const iterations = 20
	registeredBefore, enteredBeforeTheInterrupt, enteredAfterTheAnswer, consultsTotal := 0, 0, 0, 0
	for i := range iterations {
		var consults atomic.Int64
		release := make(chan struct{})
		entered := make(chan struct{}, 1)
		peer := newGrokPeer(t, func(cfg *DriverSessionConfig) {
			cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
				consults.Add(1)
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
				return ProviderApprovalDecision{
					Allow: true, Granted: []string{"allow-once", "allow-always"}, SessionScope: true,
				}, nil
			}
		})
		promptID := grokOpenTurn(t, peer, "sess-probe")
		turn := peer.session.ActiveTurn()
		peer.requestFromServer("srv-probe", grokReqRequestPermission,
			grokPermissionParams("sess-probe", "call-probe"))

		// OBSERVATIONS, not classifications: which ordering this iteration happened to
		// take. Neither decides whether the iteration passes.
		registered := grokRequestRegistered(t, peer, "srv-probe")
		enteredBefore := false
		select {
		case <-entered:
			enteredBefore = true
		default:
		}
		if attempted, err := peer.session.Interrupt(context.Background()); err != nil || !attempted {
			t.Fatalf("iteration %d: interrupt = attempted %t, err %v", i, attempted, err)
		}
		// The cancel is a notification: it ends nothing, and the turn it named is
		// still the one this conversation has.
		if got := peer.session.ActiveTurn(); got != turn {
			t.Fatalf("iteration %d: active turn = %q after the interrupt, want the turn it cancelled %q", i, got, turn)
		}
		// The one answer, whichever path wrote it. It is never a grant, and it is one
		// of the two answers a stopped turn may receive.
		reply := peer.nextResponse()
		if reply.Outcome.Outcome == grokOutcomeSelected &&
			(reply.Outcome.OptionID == "allow-once" || reply.Outcome.OptionID == "allow-always") {
			t.Fatalf("iteration %d: a cancelled turn granted a permission: %+v", i, reply.Outcome)
		}
		cancelled := reply.Outcome.Outcome == grokOutcomeCancelled
		refusedOnce := reply.Outcome.Outcome == grokOutcomeSelected && reply.Outcome.OptionID == "reject-once"
		if !cancelled && !refusedOnce {
			t.Fatalf("iteration %d: outcome = %+v, want the protocol's cancellation or the offered one-shot rejection", i, reply.Outcome)
		}
		close(release)
		// TERMINATION: the resolving goroutine ran to completion, so what follows is a
		// fact about the whole resolution and not about when the test looked.
		grokWaitRequestResolved(t, peer, "srv-probe")
		if n := grokAnswersTo(peer, "srv-probe"); n != 1 {
			t.Fatalf("iteration %d: the driver wrote %d answers to srv-probe, want exactly one", i, n)
		}
		if n := consults.Load(); n > 1 {
			t.Fatalf("iteration %d: the authority was consulted %d times for one request", i, n)
		}
		enteredAfter := false
		select {
		case <-entered:
			enteredAfter = true
		default:
		}
		if registered {
			registeredBefore++
		}
		if enteredBefore {
			enteredBeforeTheInterrupt++
		}
		if enteredAfter {
			enteredAfterTheAnswer++
		}
		consultsTotal += int(consults.Load())

		// And the turn ends where it always did: on its own correlated result.
		peer.reply(promptID, map[string]any{"stopReason": "cancelled"})
		waitFor(t, "the correlated result ended the cancelled turn", func() bool {
			return peer.session.ActiveTurn() == ""
		})
	}
	// Observations. A schedule that lands entirely on one side is a legitimate
	// schedule, so these are reported and none of them fails the row.
	t.Logf("iterations=%d registered-before-the-interrupt=%d callback-entered-before-the-interrupt=%d callback-entered-after-the-answer=%d consults=%d",
		iterations, registeredBefore, enteredBeforeTheInterrupt, enteredAfterTheAnswer, consultsTotal)
}

// A method this client never advertised gets a protocol ERROR, not a synthesized
// answer that would make the client look capable of something it is not.
func TestGrokUnimplementedServerRequestsAnswerAProtocolError(t *testing.T) {
	peer := newGrokPeer(t)
	if _, err := peer.answerHandshake("sess-caps", grokAllAuthMethods()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	for _, method := range []string{"fs/read_text_file", "fs/write_text_file", "terminal/create"} {
		peer.requestFromServer("srv-cap", method, map[string]any{"sessionId": "sess-caps"})
		line := peer.nextResponseLine()
		var frame struct {
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatalf("answer to %s is not JSON: %s", method, line)
		}
		if frame.Error == nil || frame.Error.Code != grokErrMethodNotSupported {
			t.Fatalf("%s was not refused with a protocol error: %s", method, line)
		}
		if len(frame.Result) > 0 && string(frame.Result) != "null" {
			t.Fatalf("%s got a result: %s", method, line)
		}
	}
}

// nextResponse reads the next frame the driver wrote that answers a SERVER
// request, decoded as a permission outcome.
func (p *grokPeer) nextResponse() grokRequestPermissionResponse {
	p.t.Helper()
	var out struct {
		Result grokRequestPermissionResponse `json:"result"`
	}
	if err := json.Unmarshal(p.nextResponseLine(), &out); err != nil {
		p.t.Fatalf("the driver's answer is not a permission response: %v", err)
	}
	return out.Result
}

// nextResponseLine returns the next frame with a STRING id, which is how the
// fixture's own requests are correlated (the driver's requests use integers).
func (p *grokPeer) nextResponseLine() []byte {
	p.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case frame := <-p.frames:
			id, ok := frame["id"].(string)
			if !ok || !strings.HasPrefix(id, "srv-") {
				continue
			}
			raw, _ := json.Marshal(frame)
			return raw
		case <-deadline:
			p.t.Fatal("timed out waiting for the driver to answer a server request")
			return nil
		}
	}
}

// grokWaitRequestResolved blocks until the driver's resolving goroutine for a
// fixture request id has RUN TO COMPLETION. It is what turns "nothing further was
// written" from a race into a fact: the request leaves `pending` in that
// goroutine's own deferred cleanup, so its absence is the completion signal, and
// checking the wire before it would only prove the test got there first.
func grokWaitRequestResolved(t *testing.T, peer *grokPeer, id string) {
	t.Helper()
	sess, ok := peer.session.(*grokSession)
	if !ok {
		t.Fatalf("peer session is %T, want *grokSession", peer.session)
	}
	raw, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("marshal request id: %v", err)
	}
	key := string(raw)
	waitFor(t, "the driver finished resolving "+id, func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		_, pending := sess.pending[key]
		return !pending
	})
}

// grokAnswersTo counts the lines the driver WROTE answering a fixture request id.
// It reads the recorded wire rather than the frame channel, so it counts every
// answer instead of only the ones a test happened to consume.
func grokAnswersTo(peer *grokPeer, id string) int {
	needle := `"id":"` + id + `"`
	n := 0
	for _, line := range peer.sentLines() {
		if strings.Contains(string(line), needle) {
			n++
		}
	}
	return n
}
