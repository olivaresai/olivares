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

type openCodePeer struct {
	t         *testing.T
	session   DriverSession
	frames    chan map[string]any
	mu        sync.Mutex
	lines     [][]byte
	blockSend bool
}

func newOpenCodePeer(t *testing.T, opts ...func(*DriverSessionConfig)) *openCodePeer {
	t.Helper()
	p := &openCodePeer{t: t, frames: make(chan map[string]any, 64)}
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
	p.session = openCodeDriver{}.OpenSession(cfg)
	return p
}

func (p *openCodePeer) send(_ context.Context, line []byte) error {
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

func (p *openCodePeer) next() map[string]any {
	p.t.Helper()
	select {
	case f := <-p.frames:
		return f
	case <-time.After(3 * time.Second):
		p.t.Fatal("timed out waiting for the driver to send a frame")
		return nil
	}
}

func (p *openCodePeer) nextRequest() (int64, string, map[string]any) {
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

func (p *openCodePeer) reply(id int64, result any) {
	p.t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		p.t.Fatalf("marshal reply: %v", err)
	}
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *openCodePeer) replyMissing(id int64) {
	p.t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id})
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *openCodePeer) replyNull(id int64) {
	p.t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": nil})
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *openCodePeer) replyError(id int64, code int, message string) {
	p.t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message},
	})
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *openCodePeer) requestFromServer(id, method string, params any) {
	p.t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *openCodePeer) notify(method string, params any) {
	p.t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	p.session.Deliver(OutputFrame{Stream: streamStdout, Data: raw})
}

func (p *openCodePeer) sentLines() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.lines))
	copy(out, p.lines)
	return out
}

func (p *openCodePeer) blockWrites() {
	p.mu.Lock()
	p.blockSend = true
	p.mu.Unlock()
}

func openCodeInitializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": 1,
		"agentCapabilities": map[string]any{
			"loadSession": true,
			"sessionCapabilities": map[string]any{
				"list": map[string]any{}, "resume": map[string]any{}, "close": map[string]any{},
			},
		},
		"authMethods": []any{
			map[string]any{"id": "opencode-login", "name": "Login with opencode"},
		},
		"agentInfo": map[string]any{"name": "OpenCode", "version": "1.18.30-fixture"},
	}
}

func openCodeGroupedConfigOptions(currentModel, currentEffort string) []any {
	model := map[string]any{
		"id": "model", "name": "Model", "type": "select", "category": "model",
		"currentValue": currentModel,
		"options": []any{
			map[string]any{"value": "opencode/big-pickle", "name": "Big Pickle"},
			map[string]any{
				"name": "Anthropic",
				"options": []any{
					map[string]any{"value": "anthropic/claude-sonnet-4", "name": "Sonnet 4"},
				},
			},
		},
	}
	out := []any{model, map[string]any{
		"id": "mode", "name": "Mode", "type": "select", "category": "mode",
		"currentValue": "build",
		"options":      []any{map[string]any{"value": "build", "name": "Build"}},
	}}
	if currentEffort != "" || currentModel == "anthropic/claude-sonnet-4" {
		effort := "medium"
		if currentEffort != "" {
			effort = currentEffort
		}
		out = append(out, map[string]any{
			"id": "effort", "name": "Effort", "type": "select", "category": "thought_level",
			"currentValue": effort,
			"options": []any{
				map[string]any{"value": "low", "name": "Low"},
				map[string]any{"value": "medium", "name": "Medium"},
				map[string]any{"value": "high", "name": "High"},
			},
		})
	}
	return out
}

func (p *openCodePeer) answerHandshake(sessionID string) (DriverHandshake, error) {
	p.t.Helper()
	return p.answerHandshakeWith(sessionID, openCodeInitializeResult(), map[string]any{
		"sessionId": sessionID, "configOptions": openCodeGroupedConfigOptions("opencode/big-pickle", ""),
	})
}

func (p *openCodePeer) answerHandshakeWith(sessionID string, init, newResult any) (DriverHandshake, error) {
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
	if method != openCodeMethodInitialize {
		p.t.Fatalf("first request = %q, want initialize", method)
	}
	p.reply(id, init)
	for {
		id, method, _ = p.nextRequest()
		switch method {
		case "authenticate":
			p.t.Fatal("OpenCode must not call authenticate")
		case openCodeMethodSessionNew:
			p.reply(id, newResult)
		case openCodeMethodSetConfigOption:
			p.t.Fatalf("unexpected set_config_option during a default handshake")
		default:
			p.t.Fatalf("unexpected handshake request %q", method)
		}
		break
	}
	res := <-done
	return res.hs, res.err
}

func TestOpenCodeLaunchArgsAreOwnedACPOnLoopback(t *testing.T) {
	t.Parallel()
	d := NewOpenCodeDriver()
	if d.Key() != providerDriverOpenCode || d.ConfigHomeEnv() != envOpenCodeConfigDir || d.DefaultProgram() != "opencode" {
		t.Fatalf("driver identity = %q %q %q", d.Key(), d.ConfigHomeEnv(), d.DefaultProgram())
	}
	if got := d.LaunchArgs(DriverLaunch{}); !reflect.DeepEqual(got, []string{"acp", "--hostname", "127.0.0.1"}) {
		t.Fatalf("empty workdir argv = %v", got)
	}
	got := d.LaunchArgs(DriverLaunch{WorkDir: "/workspace/fixture", Model: "ignored", Effort: "ignored"})
	want := []string{"acp", "--hostname", "127.0.0.1", "--cwd", "/workspace/fixture"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for _, arg := range got {
		switch arg {
		case "serve", "web", "attach", "--mdns", "--port":
			t.Fatalf("the driver produced a non-owned form: %v", got)
		}
	}
	env := d.(ProviderDriverLaunchEnv).LaunchEnv(DriverLaunch{})
	if len(env) != 1 || env[0].Name != envOpenCodeDisableAutoUpdate || env[0].Value != "1" {
		t.Fatalf("launch env = %v", env)
	}
	if providerHomeEnvName(env[0].Name) || openCodeReservedEnvName(env[0].Name) {
		t.Fatal("LaunchEnv must not name a profile-owned or reserved XDG variable")
	}
	tp := d.(ProviderDriverTransport).TransportProfile()
	if tp.Protocol != protocolOpenCodeACP || tp.IO != ioBidirectional || tp.Input != inputText {
		t.Fatalf("transport = %+v", tp)
	}
}

func TestOpenCodeWireCarriesJSONRPCAndAdvertisesNoUnimplementedCapability(t *testing.T) {
	peer := newOpenCodePeer(t)
	hs, err := peer.answerHandshake("ses_1")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if hs.ConversationID != "ses_1" || hs.AuthState != AuthStateUnknown {
		t.Fatalf("handshake = %+v", hs)
	}
	lines := peer.sentLines()
	if len(lines) < 2 {
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
		if frame["method"] == "authenticate" {
			t.Fatal("authenticate must not be sent")
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
	if init.Params.ProtocolVersion != openCodeProtocolVersion {
		t.Fatalf("initialize declared protocolVersion %d", init.Params.ProtocolVersion)
	}
	caps := init.Params.ClientCapabilities
	if caps.FS.ReadTextFile || caps.FS.WriteTextFile || caps.Terminal {
		t.Fatalf("this client implements no filesystem or terminal handler and must advertise none: %s", lines[0])
	}
}

func TestOpenCodePublishesInitialUnknownAndDoesNotCallAuthenticate(t *testing.T) {
	var states []string
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.OnAuthState = func(state string) { states = append(states, state) }
	})
	hs, err := peer.answerHandshake("ses_unknown")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if hs.AuthState != AuthStateUnknown {
		t.Fatalf("auth = %q", hs.AuthState)
	}
	if len(states) != 1 || states[0] != AuthStateUnknown {
		t.Fatalf("OnAuthState = %v, want the initial unknown", states)
	}
}

func TestOpenCodeMissingOrNullResultIsNotSuccess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		send func(*openCodePeer, int64)
	}{
		{"missing result", func(p *openCodePeer, id int64) { p.replyMissing(id) }},
		{"null result", func(p *openCodePeer, id int64) { p.replyNull(id) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newOpenCodePeer(t)
			done := make(chan error, 1)
			go func() {
				_, err := peer.session.Handshake(context.Background())
				done <- err
			}()
			id, method, _ := peer.nextRequest()
			if method != openCodeMethodInitialize {
				t.Fatalf("first = %q", method)
			}
			peer.reply(id, openCodeInitializeResult())
			id, method, _ = peer.nextRequest()
			if method != openCodeMethodSessionNew {
				t.Fatalf("second = %q", method)
			}
			tc.send(peer, id)
			err := <-done
			if err == nil {
				t.Fatal("a missing or null result must not nominate a conversation")
			}
			if peer.session.ConversationID() != "" {
				t.Fatal("a failed envelope must not bind a conversation")
			}
		})
	}
}

func TestOpenCodeNewRequiresASessionID(t *testing.T) {
	peer := newOpenCodePeer(t)
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, openCodeInitializeResult())
	id, _, _ = peer.nextRequest()
	peer.reply(id, map[string]any{"configOptions": []any{}})
	if err := <-done; err == nil {
		t.Fatal("session/new without a session id must fail")
	}
}

func TestOpenCodeResumeEmptyObjectConfirmsTheRequestedID(t *testing.T) {
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.ResumeConversationID = "ses_stored"
	})
	done := make(chan struct {
		hs  DriverHandshake
		err error
	}, 1)
	go func() {
		hs, err := peer.session.Handshake(context.Background())
		done <- struct {
			hs  DriverHandshake
			err error
		}{hs, err}
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, openCodeInitializeResult())
	id, method, params := peer.nextRequest()
	if method != openCodeMethodSessionResume {
		t.Fatalf("resume method = %q", method)
	}
	if params["sessionId"] != "ses_stored" {
		t.Fatalf("resume params = %v", params)
	}
	peer.reply(id, map[string]any{})
	res := <-done
	if res.err != nil {
		t.Fatalf("handshake: %v", res.err)
	}
	if res.hs.ConversationID != "ses_stored" {
		t.Fatalf("bound = %q", res.hs.ConversationID)
	}
}

func TestOpenCodeFailedResumeNeverStartsANewConversation(t *testing.T) {
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.ResumeConversationID = "ses_missing"
	})
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, openCodeInitializeResult())
	id, method, _ := peer.nextRequest()
	if method != openCodeMethodSessionResume {
		t.Fatalf("method = %q", method)
	}
	peer.replyError(id, -32001, "not found")
	if err := <-done; err == nil {
		t.Fatal("a failed resume must refuse")
	}
	for _, line := range peer.sentLines() {
		var frame map[string]any
		_ = json.Unmarshal(line, &frame)
		if frame["method"] == openCodeMethodSessionNew || frame["method"] == openCodeMethodSessionLoad {
			t.Fatalf("a failed resume fell back to %s", frame["method"])
		}
	}
}

func TestOpenCodeResumeMismatchIsRefused(t *testing.T) {
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.ResumeConversationID = "ses_stored"
	})
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, openCodeInitializeResult())
	id, _, _ = peer.nextRequest()
	peer.reply(id, map[string]any{"sessionId": "ses_other"})
	if err := <-done; err == nil {
		t.Fatal("a mismatched resume id must be refused")
	}
}

func TestOpenCodeLoadIsUsedOnlyWhenResumeIsAbsent(t *testing.T) {
	init := openCodeInitializeResult()
	caps := init["agentCapabilities"].(map[string]any)
	caps["sessionCapabilities"] = map[string]any{"list": map[string]any{}, "close": map[string]any{}}
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.ResumeConversationID = "ses_load"
	})
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, init)
	id, method, _ := peer.nextRequest()
	if method != openCodeMethodSessionLoad {
		t.Fatalf("method = %q, want session/load", method)
	}
	peer.reply(id, map[string]any{})
	if err := <-done; err != nil {
		t.Fatalf("load handshake: %v", err)
	}
}

func TestOpenCodeWrongAndForeignRepliesDoNotNominate(t *testing.T) {
	peer := newOpenCodePeer(t)
	done := make(chan struct {
		hs  DriverHandshake
		err error
	}, 1)
	go func() {
		hs, err := peer.session.Handshake(context.Background())
		done <- struct {
			hs  DriverHandshake
			err error
		}{hs, err}
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id+99, map[string]any{"sessionId": "ses_wrong"})
	peer.notify(openCodeNotifySessionUpdate, map[string]any{
		"sessionId": "ses_foreign", "update": map[string]any{"sessionUpdate": "agent_message_chunk"},
	})
	peer.reply(id, openCodeInitializeResult())
	id, _, _ = peer.nextRequest()
	peer.notify(openCodeNotifySessionUpdate, map[string]any{
		"sessionId": "ses_live_not_yet", "update": map[string]any{"sessionUpdate": "agent_message_chunk"},
	})
	peer.reply(id, map[string]any{"sessionId": "ses_real", "configOptions": []any{}})
	res := <-done
	if res.err != nil {
		t.Fatalf("handshake: %v", res.err)
	}
	if res.hs.ConversationID != "ses_real" {
		t.Fatalf("bound = %q", res.hs.ConversationID)
	}
	counts := peer.session.(*openCodeSession).updateCounts()
	if counts.foreign == 0 {
		t.Fatal("foreign session/update frames must be classified as foreign")
	}
	if peer.session.ConversationID() != "ses_real" {
		t.Fatal("a foreign notification must not rebind the conversation")
	}
}

func TestOpenCodeGroupedModelAndEffortAreHonoredExactly(t *testing.T) {
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.Model = "anthropic/claude-sonnet-4"
		cfg.Effort = "high"
	})
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, openCodeInitializeResult())
	id, method, _ := peer.nextRequest()
	if method != openCodeMethodSessionNew {
		t.Fatalf("method = %q", method)
	}
	peer.reply(id, map[string]any{
		"sessionId": "ses_model", "configOptions": openCodeGroupedConfigOptions("opencode/big-pickle", ""),
	})
	id, method, params := peer.nextRequest()
	if method != openCodeMethodSetConfigOption || params["configId"] != "model" || params["value"] != "anthropic/claude-sonnet-4" {
		t.Fatalf("model set = %s %v", method, params)
	}
	peer.reply(id, map[string]any{"configOptions": openCodeGroupedConfigOptions("anthropic/claude-sonnet-4", "medium")})
	id, method, params = peer.nextRequest()
	if method != openCodeMethodSetConfigOption || params["configId"] != "effort" || params["value"] != "high" {
		t.Fatalf("effort set = %s %v", method, params)
	}
	peer.reply(id, map[string]any{"configOptions": openCodeGroupedConfigOptions("anthropic/claude-sonnet-4", "high")})
	if err := <-done; err != nil {
		t.Fatalf("handshake: %v", err)
	}
}

func TestOpenCodeUnsupportedOrContradictorySettingsFailTheHandshake(t *testing.T) {
	t.Parallel()
	t.Run("model not offered", func(t *testing.T) {
		peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) { cfg.Model = "missing/model" })
		done := make(chan error, 1)
		go func() {
			_, err := peer.session.Handshake(context.Background())
			done <- err
		}()
		id, _, _ := peer.nextRequest()
		peer.reply(id, openCodeInitializeResult())
		id, _, _ = peer.nextRequest()
		peer.reply(id, map[string]any{
			"sessionId": "ses_x", "configOptions": openCodeGroupedConfigOptions("opencode/big-pickle", ""),
		})
		err := <-done
		if err == nil {
			t.Fatal("an unoffered model must fail the handshake")
		}
	})
	t.Run("effort with no selector", func(t *testing.T) {
		peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) { cfg.Effort = "high" })
		done := make(chan error, 1)
		go func() {
			_, err := peer.session.Handshake(context.Background())
			done <- err
		}()
		id, _, _ := peer.nextRequest()
		peer.reply(id, openCodeInitializeResult())
		id, _, _ = peer.nextRequest()
		peer.reply(id, map[string]any{
			"sessionId": "ses_x", "configOptions": openCodeGroupedConfigOptions("opencode/big-pickle", ""),
		})
		if err := <-done; err == nil {
			t.Fatal("requested effort without an effort selector must fail")
		}
	})
	t.Run("contradictory currentValue", func(t *testing.T) {
		peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) { cfg.Model = "opencode/big-pickle" })
		done := make(chan error, 1)
		go func() {
			_, err := peer.session.Handshake(context.Background())
			done <- err
		}()
		id, _, _ := peer.nextRequest()
		peer.reply(id, openCodeInitializeResult())
		id, _, _ = peer.nextRequest()
		peer.reply(id, map[string]any{
			"sessionId": "ses_x", "configOptions": openCodeGroupedConfigOptions("opencode/big-pickle", ""),
		})
		id, _, _ = peer.nextRequest()
		peer.reply(id, map[string]any{"configOptions": openCodeGroupedConfigOptions("anthropic/claude-sonnet-4", "")})
		if err := <-done; err == nil {
			t.Fatal("a contradictory currentValue must fail")
		}
	})
}

func TestOpenCodeAuthRequiredOnResumeLoadAndConfigPublishesRequired(t *testing.T) {
	t.Parallel()
	type outcome struct {
		hs  DriverHandshake
		err error
	}
	assertAuthRequired := func(t *testing.T, err error, states []string, session DriverSession) {
		t.Helper()
		if err == nil {
			t.Fatal("auth-required must fail the handshake")
		}
		var re *runErr
		if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity {
			t.Fatalf("err = %v, want the bounded auth_required category", err)
		}
		if !strings.Contains(err.Error(), "auth_required") {
			t.Fatalf("err = %v, want auth_required named", err)
		}
		if session.AuthState() != AuthStateRequired {
			t.Fatalf("auth = %q", session.AuthState())
		}
		if len(states) != 2 || states[0] != AuthStateUnknown || states[1] != AuthStateRequired {
			t.Fatalf("OnAuthState = %v", states)
		}
	}
	t.Run("resume", func(t *testing.T) {
		var states []string
		peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
			cfg.ResumeConversationID = "ses_stored"
			cfg.OnAuthState = func(state string) { states = append(states, state) }
		})
		done := make(chan outcome, 1)
		go func() {
			hs, err := peer.session.Handshake(context.Background())
			done <- outcome{hs, err}
		}()
		id, _, _ := peer.nextRequest()
		peer.reply(id, openCodeInitializeResult())
		id, method, _ := peer.nextRequest()
		if method != openCodeMethodSessionResume {
			t.Fatalf("method = %q", method)
		}
		peer.replyError(id, openCodeErrAuthRequired, "provider authentication required")
		res := <-done
		assertAuthRequired(t, res.err, states, peer.session)
		if res.hs.ConversationID != "" || peer.session.ConversationID() != "" {
			t.Fatal("auth-required resume must not bind a conversation")
		}
		for _, line := range peer.sentLines() {
			var frame map[string]any
			_ = json.Unmarshal(line, &frame)
			if frame["method"] == openCodeMethodSessionNew || frame["method"] == openCodeMethodSessionLoad {
				t.Fatalf("auth-required resume fell back to %s", frame["method"])
			}
		}
	})
	t.Run("load", func(t *testing.T) {
		var states []string
		init := openCodeInitializeResult()
		caps := init["agentCapabilities"].(map[string]any)
		caps["sessionCapabilities"] = map[string]any{"list": map[string]any{}, "close": map[string]any{}}
		peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
			cfg.ResumeConversationID = "ses_load"
			cfg.OnAuthState = func(state string) { states = append(states, state) }
		})
		done := make(chan outcome, 1)
		go func() {
			hs, err := peer.session.Handshake(context.Background())
			done <- outcome{hs, err}
		}()
		id, _, _ := peer.nextRequest()
		peer.reply(id, init)
		id, method, _ := peer.nextRequest()
		if method != openCodeMethodSessionLoad {
			t.Fatalf("method = %q, want session/load", method)
		}
		peer.replyError(id, openCodeErrAuthRequired, "provider authentication required")
		res := <-done
		assertAuthRequired(t, res.err, states, peer.session)
		for _, line := range peer.sentLines() {
			var frame map[string]any
			_ = json.Unmarshal(line, &frame)
			if frame["method"] == openCodeMethodSessionNew || frame["method"] == openCodeMethodSessionResume {
				t.Fatalf("auth-required load fell back to %s", frame["method"])
			}
		}
	})
	t.Run("set_config_option", func(t *testing.T) {
		var states []string
		peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
			cfg.Model = "opencode/big-pickle"
			cfg.OnAuthState = func(state string) { states = append(states, state) }
		})
		done := make(chan outcome, 1)
		go func() {
			hs, err := peer.session.Handshake(context.Background())
			done <- outcome{hs, err}
		}()
		id, _, _ := peer.nextRequest()
		peer.reply(id, openCodeInitializeResult())
		id, method, _ := peer.nextRequest()
		if method != openCodeMethodSessionNew {
			t.Fatalf("method = %q", method)
		}
		peer.reply(id, map[string]any{
			"sessionId": "ses_cfg", "configOptions": openCodeGroupedConfigOptions("opencode/big-pickle", ""),
		})
		id, method, params := peer.nextRequest()
		if method != openCodeMethodSetConfigOption || params["configId"] != "model" {
			t.Fatalf("config set = %s %v", method, params)
		}
		peer.replyError(id, openCodeErrAuthRequired, "provider authentication required")
		res := <-done
		assertAuthRequired(t, res.err, states, peer.session)
	})
}

func TestOpenCodePromptAuthRequiredPublishesRequired(t *testing.T) {
	var states []string
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.OnAuthState = func(state string) { states = append(states, state) }
	})
	if _, err := peer.answerHandshake("ses_prompt_auth"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if peer.session.AuthState() != AuthStateUnknown {
		t.Fatalf("after handshake auth = %q", peer.session.AuthState())
	}
	go func() { _, _ = peer.session.Input(context.Background(), "hello") }()
	id, method, _ := peer.nextRequest()
	if method != openCodeMethodSessionPrompt {
		t.Fatalf("method = %q", method)
	}
	peer.replyError(id, openCodeErrAuthRequired, "provider authentication required")
	waitFor(t, "auth-required ended the turn and published required", func() bool {
		return peer.session.ActiveTurn() == "" && peer.session.AuthState() == AuthStateRequired
	})
	if len(states) != 2 || states[0] != AuthStateUnknown || states[1] != AuthStateRequired {
		t.Fatalf("OnAuthState = %v", states)
	}
}

func TestOpenCodeAuthRequiredMovesUnknownToRequired(t *testing.T) {
	var states []string
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.OnAuthState = func(state string) { states = append(states, state) }
	})
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, openCodeInitializeResult())
	id, _, _ = peer.nextRequest()
	peer.replyError(id, openCodeErrAuthRequired, "provider authentication required")
	err := <-done
	if err == nil {
		t.Fatal("auth-required session/new must fail")
	}
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity {
		t.Fatalf("err = %v", err)
	}
	if peer.session.AuthState() != AuthStateRequired {
		t.Fatalf("auth = %q", peer.session.AuthState())
	}
	if len(states) != 2 || states[0] != AuthStateUnknown || states[1] != AuthStateRequired {
		t.Fatalf("OnAuthState = %v", states)
	}
}

func TestOpenCodeInputReturnsOnDispatchAndNotificationsDoNotEndTheTurn(t *testing.T) {
	peer := newOpenCodePeer(t)
	if _, err := peer.answerHandshake("ses_turn"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	done := make(chan struct {
		attempted bool
		err       error
	}, 1)
	go func() {
		attempted, err := peer.session.Input(context.Background(), "hello")
		done <- struct {
			attempted bool
			err       error
		}{attempted, err}
	}()
	id, method, params := peer.nextRequest()
	if method != openCodeMethodSessionPrompt {
		t.Fatalf("method = %q", method)
	}
	res := <-done
	if !res.attempted || res.err != nil {
		t.Fatalf("input = %v %v", res.attempted, res.err)
	}
	if peer.session.ActiveTurn() == "" {
		t.Fatal("the dispatched prompt is the active turn")
	}
	peer.notify(openCodeNotifySessionUpdate, map[string]any{
		"sessionId": "ses_turn", "update": map[string]any{"sessionUpdate": "agent_message_chunk"},
	})
	if peer.session.ActiveTurn() == "" {
		t.Fatal("a notification must not end the turn")
	}
	if _, err := peer.session.Input(context.Background(), "second"); err == nil {
		t.Fatal("a second concurrent input must be refused")
	}
	peer.reply(id, map[string]any{"stopReason": "end_turn"})
	waitFor(t, "the turn ended", func() bool { return peer.session.ActiveTurn() == "" })
	prompt, _ := params["prompt"].([]any)
	if len(prompt) == 0 {
		t.Fatalf("prompt params = %v", params)
	}
}

func TestOpenCodeCloseEndsTheDispatchedTurn(t *testing.T) {
	peer := newOpenCodePeer(t)
	if _, err := peer.answerHandshake("ses_close"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	go func() { _, _ = peer.session.Input(context.Background(), "hold") }()
	_, method, _ := peer.nextRequest()
	if method != openCodeMethodSessionPrompt {
		t.Fatalf("method = %q", method)
	}
	if peer.session.ActiveTurn() == "" {
		t.Fatal("turn was not recorded")
	}
	peer.session.Close(errRPCClosed)
	waitFor(t, "close ended the turn", func() bool { return peer.session.ActiveTurn() == "" })
}

func TestOpenCodePermissionGrantIsOnceOnlyAndNeverAlways(t *testing.T) {
	t.Parallel()
	options := []any{
		map[string]any{"optionId": "once", "kind": openCodePermissionAllowOnce, "name": "Allow once"},
		map[string]any{"optionId": "always", "kind": openCodePermissionAllowAlways, "name": "Always allow"},
		map[string]any{"optionId": "reject", "kind": openCodePermissionRejectOnce, "name": "Reject"},
	}
	if id, ok := openCodeSelectGrant([]openCodePermissionOption{
		{OptionID: "once", Kind: openCodePermissionAllowOnce},
		{OptionID: "always", Kind: openCodePermissionAllowAlways},
	}, ProviderApprovalDecision{Allow: true, Granted: []string{"once", "always"}, SessionScope: true}); !ok || id != "once" {
		t.Fatalf("grant = %q %v, want once even with SessionScope", id, ok)
	}
	if _, ok := openCodeSelectGrant([]openCodePermissionOption{
		{OptionID: "always", Kind: openCodePermissionAllowAlways},
	}, ProviderApprovalDecision{Allow: true, Granted: []string{"always"}, SessionScope: true}); ok {
		t.Fatal("allow_always must never be selected")
	}

	var seen ProviderApprovalRequest
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(_ context.Context, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			seen = req
			return ProviderApprovalDecision{Allow: true, Granted: []string{"once", "always"}, SessionScope: true}, nil
		}
	})
	if _, err := peer.answerHandshake("ses_perm"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	go func() { _, _ = peer.session.Input(context.Background(), "tool") }()
	id, _, _ := peer.nextRequest()
	peer.requestFromServer("srv-1", openCodeReqRequestPermission, map[string]any{
		"sessionId": "ses_perm",
		"toolCall":  map[string]any{"toolCallId": "c1", "kind": "edit"},
		"options":   options,
	})
	waitFor(t, "the grant was answered", func() bool {
		for _, line := range peer.sentLines() {
			var frame map[string]any
			if json.Unmarshal(line, &frame) != nil {
				continue
			}
			if frame["id"] == "srv-1" {
				return true
			}
		}
		return false
	})
	var reply openCodeRequestPermissionResponse
	for _, line := range peer.sentLines() {
		var frame map[string]any
		if json.Unmarshal(line, &frame) != nil {
			continue
		}
		if frame["id"] != "srv-1" {
			continue
		}
		raw, _ := json.Marshal(frame["result"])
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("reply: %v %s", err, line)
		}
	}
	if reply.Outcome.Outcome != openCodeOutcomeSelected || reply.Outcome.OptionID != "once" {
		t.Fatalf("outcome = %+v", reply.Outcome)
	}
	if seen.Driver != providerDriverOpenCode || strings.Join(seen.Requested, ",") != "once,always,reject" {
		t.Fatalf("authority saw %+v", seen)
	}
	peer.reply(id, map[string]any{"stopReason": "end_turn"})
}

func TestOpenCodeUnknownRequestsAnswerMethodNotFound(t *testing.T) {
	peer := newOpenCodePeer(t)
	if _, err := peer.answerHandshake("ses_unk"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	peer.requestFromServer("srv-fs", "fs/write_text_file", map[string]any{"path": "/tmp/x"})
	waitFor(t, "the protocol error was written", func() bool {
		for _, line := range peer.sentLines() {
			if strings.Contains(string(line), `"code":-32601`) || strings.Contains(string(line), `"code": -32601`) {
				return true
			}
		}
		return false
	})
}

func TestOpenCodePermissionRacesRefuseWithoutASecondGrant(t *testing.T) {
	var gateCalls atomic.Int32
	block := make(chan struct{})
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(ctx context.Context, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			gateCalls.Add(1)
			select {
			case <-block:
			case <-ctx.Done():
			}
			return ProviderApprovalDecision{Allow: true, Granted: []string{"once"}}, nil
		}
	})
	if _, err := peer.answerHandshake("ses_race"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	go func() { _, _ = peer.session.Input(context.Background(), "tool") }()
	_, _, _ = peer.nextRequest()
	peer.requestFromServer("srv-race", openCodeReqRequestPermission, map[string]any{
		"sessionId": "ses_race",
		"toolCall":  map[string]any{"toolCallId": "c1", "kind": "edit"},
		"options": []any{
			map[string]any{"optionId": "once", "kind": openCodePermissionAllowOnce, "name": "Allow once"},
			map[string]any{"optionId": "reject", "kind": openCodePermissionRejectOnce, "name": "Reject"},
		},
	})
	waitFor(t, "the authority was entered", func() bool { return gateCalls.Load() == 1 })
	if _, err := peer.session.Interrupt(context.Background()); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	close(block)
	waitFor(t, "the permission was answered once", func() bool {
		n := 0
		for _, line := range peer.sentLines() {
			if strings.Contains(string(line), `"id":"srv-race"`) {
				n++
			}
		}
		return n == 1
	})
	for _, line := range peer.sentLines() {
		if !strings.Contains(string(line), `"id":"srv-race"`) {
			continue
		}
		if strings.Contains(string(line), `"optionId":"once"`) {
			t.Fatalf("a late grant crossed after interrupt: %s", line)
		}
	}
}

func TestOpenCodeRefusesAProtocolVersionItDoesNotImplement(t *testing.T) {
	t.Parallel()
	for _, raw := range []any{2, "1", nil} {
		peer := newOpenCodePeer(t)
		done := make(chan error, 1)
		go func() {
			_, err := peer.session.Handshake(context.Background())
			done <- err
		}()
		id, _, _ := peer.nextRequest()
		init := openCodeInitializeResult()
		init["protocolVersion"] = raw
		peer.reply(id, init)
		if err := <-done; err == nil {
			t.Fatalf("version %#v was accepted", raw)
		}
	}
}

func TestOpenCodeHomeEnvNamesAreCrossProvider(t *testing.T) {
	t.Parallel()
	for _, name := range []string{envOpenCodeConfigDir, envOpenCodeConfig, envOpenCodeConfigContent, envOpenCodeTUIConfig} {
		if !providerHomeEnvName(name) {
			t.Fatalf("%s must be a cross-provider profile home", name)
		}
	}
	for _, name := range []string{envXDGConfigHome, envXDGDataHome, envXDGStateHome, envXDGCacheHome, envXDGRuntimeDir} {
		if providerHomeEnvName(name) {
			t.Fatalf("%s must not globally break existing drivers", name)
		}
		if !openCodeReservedEnvName(name) {
			t.Fatalf("%s must be reserved for OpenCode launches", name)
		}
	}
}
