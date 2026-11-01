// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// The Cursor CLI driver is held to the wire `agent acp` spoke when measured on
// 2026.10.22-27dcf5f with no login: initialize advertises loadSession, MCP over http and
// sse, and the one auth method cursor_login; session/new answers -32000 "Authentication
// required"; authenticate without a stored login answers -32602 with a browser link in its
// data. session/set_mode takes agent, plan or ask and session/set_model a modelId, as the
// vendor's server declares them. A signed-in session was not measured: no account.

func newCursorPeer(t *testing.T, opts ...func(*DriverSessionConfig)) *openCodePeer {
	t.Helper()
	p := &openCodePeer{t: t, frames: make(chan map[string]any, 64)}
	cfg := DriverSessionConfig{
		Send:             p.send,
		Warn:             func(string, ...any) {},
		ClientName:       "olivares",
		ClientVersion:    "test",
		WorkDir:          "/workspace/fixture",
		AuthSource:       AuthSourceAccountHome,
		Preset:           PresetAsk,
		CallTimeout:      2 * time.Second,
		ApprovalDeadline: 2 * time.Second,
		RunRef:           "run-test",
		ProfileRef:       "ppf_test",
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	p.session = cursorDriver{}.OpenSession(cfg)
	return p
}

func cursorInitializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": 1,
		"agentCapabilities": map[string]any{
			"loadSession":         true,
			"mcpCapabilities":     map[string]any{"http": true, "sse": true},
			"promptCapabilities":  map[string]any{"audio": false, "embeddedContext": false, "image": true},
			"sessionCapabilities": map[string]any{"list": map[string]any{}},
		},
		"authMethods": []any{map[string]any{"id": "cursor_login", "name": "Cursor Login"}},
	}
}

func cursorSessionResultFor(sessionID string) map[string]any {
	return map[string]any{
		"sessionId": sessionID,
		"modes": map[string]any{
			"currentModeId": "agent",
			"availableModes": []any{
				map[string]any{"id": "agent", "name": "Agent"}, map[string]any{"id": "plan", "name": "Plan"},
				map[string]any{"id": "ask", "name": "Ask"},
			},
		},
		"models": map[string]any{
			"currentModelId": "default",
			"availableModels": []any{
				map[string]any{"modelId": "default", "name": "Default"}, map[string]any{"modelId": "gpt-5", "name": "GPT-5"},
			},
		},
	}
}

func TestCursorLaunchIsOneOwnedACPProcessWithItsLoginInsideItsHome(t *testing.T) {
	t.Parallel()
	d := cursorDriver{}
	if got := d.LaunchArgs(DriverLaunch{WorkDir: "/w", Model: "gpt-5"}); !slices.Equal(got, []string{"acp"}) {
		t.Fatalf("argv = %q, want only acp: the model and mode travel on the protocol", got)
	}
	env := map[string]string{}
	for _, v := range d.LaunchEnv(DriverLaunch{}) {
		env[v.Name] = v.Value
	}
	if len(env) != 2 || env["AGENT_CLI_CREDENTIAL_STORE"] != "file" || env["NO_OPEN_BROWSER"] != "1" {
		t.Fatalf("env = %v, want the login in the profile's home (file store) and no browser on this host", env)
	}
	if d.Key() != "cursor" || d.DefaultProgram() != "agent" || d.ConfigHomeEnv() != "CURSOR_CONFIG_DIR" {
		t.Fatalf("identity = %q %q %q", d.Key(), d.DefaultProgram(), d.ConfigHomeEnv())
	}
	if got := normalizeTransportProfile(d.TransportProfile()); got.Protocol != "cursor_acp" || got.IO != ioBidirectional || got.Input != inputText {
		t.Fatalf("readiness transport = %+v, want cursor_acp, bidirectional, text", got)
	}
}

func TestCursorSessionsAreHeldToTheVendorServiceURLs(t *testing.T) {
	t.Parallel()
	m := New(WithConfinement([]string{"/srv/olivares"}, false), WithProviderDriver(NewCursorDriver()))
	home := &ProviderHomeSnapshot{Driver: providerDriverCursor, AuthSource: AuthSourceAccountHome, ConfigHome: t.TempDir(), UserHome: t.TempDir()}
	spec := m.childSpec(CreateRunParams{WorkspaceDir: "/srv/work", ProviderHome: home}, childDecision{})
	if spec.NetworkPolicy == nil {
		t.Fatal("a Cursor account session keeps the host network")
	}
	if !slices.Equal(spec.NetworkPolicy.Providers, []string{"https://api2.cursor.sh"}) ||
		!slices.Equal(spec.NetworkPolicy.PublicProviders, []string{"https://api3.cursor.sh", "https://cursor.com"}) ||
		!spec.NetworkPolicy.PublicOnly {
		t.Fatalf("network policy = %+v, want Cursor's production apiUrl, metricsUrl and websiteUrl, public only", spec.NetworkPolicy)
	}
}

func TestCursorHandshakeAuthenticatesWithItsLoginThenSetsTheMode(t *testing.T) {
	t.Parallel()
	var states []string
	peer := newCursorPeer(t, func(cfg *DriverSessionConfig) {
		cfg.OnAuthState = func(state string) { states = append(states, state) }
	})
	hs, asked, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_cur"), nil)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if hs.ConversationID != "ses_cur" || hs.AuthState != AuthStateUnknown {
		t.Fatalf("handshake = %+v, want the CLI's conversation and unknown readiness", hs)
	}
	if got := geminiMethods(asked); !slices.Equal(got, []string{acpMethodInitialize, "authenticate", acpMethodSessionNew, "session/set_mode"}) {
		t.Fatalf("requests = %q, want initialize, authenticate, session/new, then the explicit mode", got)
	}
	if asked[1].params["methodId"] != "cursor_login" || asked[3].params["modeId"] != "agent" || asked[3].params["sessionId"] != "ses_cur" {
		t.Fatalf("authenticate %v, set_mode %v", asked[1].params, asked[3].params)
	}
	if len(states) != 1 || states[0] != AuthStateUnknown {
		t.Fatalf("OnAuthState = %v, want the initial unknown: authenticate is the tool's, not a readiness claim", states)
	}
}

// Read-only is Cursor's ask mode; every other preset asks for each tool in agent mode, and
// nothing runs in a mode the tool did not offer.
func TestCursorPresetsRunTheirMode(t *testing.T) {
	t.Parallel()
	for preset, want := range map[string]string{PresetReadOnly: "ask", PresetAsk: "agent", PresetEditsAndCommands: "agent"} {
		peer := newCursorPeer(t, func(cfg *DriverSessionConfig) { cfg.Preset = preset })
		_, asked, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_p"), nil)
		if err != nil {
			t.Fatalf("%s: %v", preset, err)
		}
		if got := asked[len(asked)-1]; got.method != "session/set_mode" || got.params["modeId"] != want {
			t.Errorf("preset %s: last request %s %v, want set_mode %s", preset, got.method, got.params, want)
		}
	}
	peer := newCursorPeer(t, func(cfg *DriverSessionConfig) { cfg.Preset = PresetReadOnly })
	noAsk := cursorSessionResultFor("ses_noask")
	noAsk["modes"] = map[string]any{"currentModeId": "agent", "availableModes": []any{map[string]any{"id": "agent"}, map[string]any{"id": "plan"}}}
	if _, _, err := peer.answerGemini(cursorInitializeResult(), noAsk, nil); err == nil {
		t.Fatal("a read-only session started although the tool did not offer a read-only mode")
	}
}

func TestCursorModelIsAnExactOfferedValue(t *testing.T) {
	t.Parallel()
	peer := newCursorPeer(t, func(cfg *DriverSessionConfig) { cfg.Model = "gpt-5" })
	_, asked, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_m"), nil)
	if last := asked[len(asked)-1]; err != nil || last.method != "session/set_model" || last.params["modelId"] != "gpt-5" {
		t.Fatalf("model: %v, last request %v", err, last)
	}
	peer = newCursorPeer(t, func(cfg *DriverSessionConfig) { cfg.Model = "gpt-9" })
	if _, asked, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_m2"), nil); err == nil || slices.Contains(geminiMethods(asked), "session/set_model") {
		t.Fatalf("a model the tool did not offer: err=%v requests=%v", err, geminiMethods(asked))
	}
	peer = newCursorPeer(t)
	if _, asked, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_m3"), nil); err != nil || slices.Contains(geminiMethods(asked), "session/set_model") {
		t.Fatalf("no model named must leave the tool's own: err=%v requests=%v", err, geminiMethods(asked))
	}
}

// Without a stored login `authenticate` could only open a browser. That is "sign in first",
// said in the bounded category, and the link it carries (a one-time challenge) is never repeated.
func TestCursorAuthenticateWithoutALoginPublishesRequiredAndLeaksNoLink(t *testing.T) {
	t.Parallel()
	var states []string
	peer := newCursorPeer(t, func(cfg *DriverSessionConfig) {
		cfg.OnAuthState = func(state string) { states = append(states, state) }
	})
	_, asked, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_x"), map[string]func(int64){
		"authenticate": func(id int64) {
			peer.replyError(id, acpErrInvalidParams, "Failed to open browser for login. Please visit: https://cursor.com/loginDeepControl?challenge=SECRETCHALLENGE&uuid=u")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "auth_required") || !slices.Equal(states, []string{AuthStateUnknown, AuthStateRequired}) {
		t.Fatalf("err=%v states=%v, want the bounded refusal and the tool's own required", err, states)
	}
	if strings.Contains(err.Error(), "SECRETCHALLENGE") || strings.Contains(err.Error(), "cursor.com") {
		t.Fatalf("the error repeats the login link: %v", err)
	}
	if slices.Contains(geminiMethods(asked), acpMethodSessionNew) {
		t.Fatalf("a conversation was opened without a login: %v", geminiMethods(asked))
	}
}

func TestCursorAuthRequiredOnAnyConversationCallPublishesRequired(t *testing.T) {
	t.Parallel()
	for _, method := range []string{acpMethodSessionNew, "session/set_mode", "session/set_model", acpMethodSessionLoad} {
		var states []string
		peer := newCursorPeer(t, func(cfg *DriverSessionConfig) {
			cfg.OnAuthState = func(state string) { states = append(states, state) }
			cfg.Model = "gpt-5"
			if method == acpMethodSessionLoad {
				cfg.ResumeConversationID = "ses_old"
			}
		})
		_, _, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_auth"), map[string]func(int64){
			method: func(id int64) { peer.replyError(id, acpErrAuthRequired, "Authentication required") },
		})
		if err == nil || !strings.Contains(err.Error(), "auth_required") || !slices.Equal(states, []string{AuthStateUnknown, AuthStateRequired}) {
			t.Errorf("%s: err=%v states=%v, want the bounded refusal and the tool's own required", method, err, states)
		}
	}
}

// The login method is named by the tool: a tool that does not offer cursor_login is not asked
// to authenticate with a method nobody advertised.
func TestCursorRefusesAToolThatOffersNoCursorLogin(t *testing.T) {
	t.Parallel()
	peer := newCursorPeer(t)
	init := cursorInitializeResult()
	init["authMethods"] = []any{map[string]any{"id": "something_else"}}
	_, asked, err := peer.answerGemini(init, cursorSessionResultFor("ses_none"), nil)
	if err == nil || slices.Contains(geminiMethods(asked), "authenticate") || slices.Contains(geminiMethods(asked), acpMethodSessionNew) {
		t.Fatalf("err=%v requests=%v, want a refusal before any authenticate or conversation", err, geminiMethods(asked))
	}
}

// A Cursor session runs on the account's login; a provider record bound to it is never used.
func TestCursorRefusesABoundProviderRecord(t *testing.T) {
	t.Parallel()
	peer := newCursorPeer(t, func(cfg *DriverSessionConfig) {
		cfg.AuthSource = AuthSourceManagedInjection
		cfg.BoundProvider = BoundProvider{Kind: ProviderKindOpenAI}
	})
	_, asked, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_b"), nil)
	if err == nil || slices.Contains(geminiMethods(asked), acpMethodSessionNew) {
		t.Fatalf("err=%v requests=%v, want a refusal before any conversation", err, geminiMethods(asked))
	}
}

func TestCursorResumeLoadsTheStoredConversationOnly(t *testing.T) {
	t.Parallel()
	peer := newCursorPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "ses_old" })
	hs, asked, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_old"), nil)
	if err != nil || hs.ConversationID != "ses_old" {
		t.Fatalf("resume: %v %+v", err, hs)
	}
	if got := geminiMethods(asked); !slices.Equal(got, []string{acpMethodInitialize, "authenticate", acpMethodSessionLoad, "session/set_mode"}) {
		t.Fatalf("requests = %q, want authenticate, session/load and the mode, never session/new", got)
	}
	peer = newCursorPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "ses_old" })
	if _, _, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_other"), nil); err == nil {
		t.Fatal("the tool resumed a different conversation and the driver accepted it")
	}
	peer = newCursorPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "ses_old" })
	noLoad := cursorInitializeResult()
	noLoad["agentCapabilities"] = map[string]any{"loadSession": false, "mcpCapabilities": map[string]any{"http": true}}
	if _, asked, err := peer.answerGemini(noLoad, cursorSessionResultFor("ses_old"), nil); err == nil || slices.Contains(geminiMethods(asked), acpMethodSessionNew) {
		t.Fatalf("a tool that cannot load a session started a new one: err=%v requests=%v", err, geminiMethods(asked))
	}
}

func TestCursorSendsAnEmptyMCPServerListNotNull(t *testing.T) {
	t.Parallel()
	peer := newCursorPeer(t)
	_, asked, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_mcp"), nil)
	if err != nil {
		t.Fatal(err)
	}
	servers, ok := asked[2].params["mcpServers"].([]any)
	if asked[2].method != acpMethodSessionNew || !ok || len(servers) != 0 {
		t.Fatalf("session/new params = %v, want mcpServers as an empty list", asked[2].params)
	}
}

// Cursor's permission request was not measured: no account. A request that gives the live
// policy nothing to review (no command, no path) is refused without asking, whatever kind
// it claims: a plain read, search or thought is not exempt as it is for Gemini.
func TestCursorRequestsWithNoFactsForThePolicyAreRefusedWithoutAsking(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"execute", "edit", "delete", "move", "fetch", "other", "read", "search", "think", ""} {
		asked := 0
		peer := newCursorPeer(t, func(cfg *DriverSessionConfig) {
			cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
				asked++
				return ProviderApprovalDecision{Allow: true, Granted: []string{"proceed_once"}}, nil
			}
		})
		if _, _, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_nofacts"), nil); err != nil {
			t.Fatalf("handshake: %v", err)
		}
		go func() { _, _ = peer.session.Input(context.Background(), "do it") }()
		peer.nextRequest()
		peer.requestFromServer("srv-1", acpReqRequestPermission, geminiToolRequest("ses_nofacts", kind))
		if got := peer.permissionOutcome("srv-1"); got.Outcome != acpOutcomeSelected || got.OptionID != "cancel" {
			t.Errorf("kind %q: outcome = %+v, want the one-shot rejection", kind, got)
		}
		if asked != 0 {
			t.Errorf("kind %q reached the approval authority with nothing for it to review", kind)
		}
	}
}

func TestCursorGrantIsOnceOnlyAndNamesTheDriver(t *testing.T) {
	t.Parallel()
	var seen ProviderApprovalRequest
	peer := newCursorPeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(_ context.Context, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			seen = req
			return ProviderApprovalDecision{Allow: true, Granted: []string{"proceed_once"}}, nil
		}
	})
	if _, _, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_ok"), nil); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = peer.session.Input(context.Background(), "edit it") }()
	peer.nextRequest()
	peer.requestFromServer("srv-2", acpReqRequestPermission, geminiEditPermissionRequest("ses_ok"))
	if got := peer.permissionOutcome("srv-2"); got.Outcome != acpOutcomeSelected || got.OptionID != "proceed_once" {
		t.Fatalf("outcome = %+v, want the one-shot grant, never proceed_always", got)
	}
	if seen.Driver != "cursor" {
		t.Fatalf("the approval named the driver %q, want cursor", seen.Driver)
	}
}

// Only the CLI's own two answers are "sign in first". An invalid-params answer that is not about
// the browser, or any other error, is a failure of the handshake: telling the person to sign in
// again would hide the cause.
func TestCursorAuthenticateFailuresThatAreNotALoginAreNotReadiness(t *testing.T) {
	t.Parallel()
	for name, code := range map[string]int{"invalid params without the browser": acpErrInvalidParams, "internal error": -32603} {
		var states []string
		peer := newCursorPeer(t, func(cfg *DriverSessionConfig) {
			cfg.OnAuthState = func(state string) { states = append(states, state) }
		})
		_, _, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_e"), map[string]func(int64){
			"authenticate": func(id int64) { peer.replyError(id, code, "unsupported methodId") },
		})
		if err == nil || strings.Contains(err.Error(), "auth_required") || !slices.Equal(states, []string{AuthStateUnknown}) {
			t.Errorf("%s: err=%v states=%v, want a plain failure and readiness left unknown", name, err, states)
		}
	}
	var states []string
	peer := newCursorPeer(t, func(cfg *DriverSessionConfig) {
		cfg.OnAuthState = func(state string) { states = append(states, state) }
	})
	_, _, err := peer.answerGemini(cursorInitializeResult(), cursorSessionResultFor("ses_a"), map[string]func(int64){
		"authenticate": func(id int64) { peer.replyError(id, acpErrAuthRequired, "Authentication required") },
	})
	if err == nil || !strings.Contains(err.Error(), "auth_required") || !slices.Equal(states, []string{AuthStateUnknown, AuthStateRequired}) {
		t.Errorf("the CLI's own authentication code: err=%v states=%v", err, states)
	}
}

// A session MCP server the CLI cannot carry is refused before anything else is sent, as for the
// other ACP tools: starting anyway would give the session none of the Olivares tools.
func TestCursorRefusesSessionMCPItCannotCarry(t *testing.T) {
	t.Parallel()
	peer := newCursorPeer(t, func(cfg *DriverSessionConfig) {
		cfg.sessionMCP = []json.RawMessage{json.RawMessage(`{"type":"http","name":"olivares","url":"http://127.0.0.1:1/session/mcp","headers":[]}`)}
	})
	init := cursorInitializeResult()
	init["agentCapabilities"] = map[string]any{"loadSession": true, "mcpCapabilities": map[string]any{"http": false}}
	_, asked, err := peer.answerGemini(init, cursorSessionResultFor("ses_mcp2"), nil)
	if err == nil || slices.Contains(geminiMethods(asked), "authenticate") {
		t.Fatalf("err=%v requests=%v, want a refusal before authenticate", err, geminiMethods(asked))
	}
}

// The wiring around the driver: its prompt is added to the transcript (ACP agents do not echo
// it), template controls it cannot apply are refused under its name, and a launch is checked
// for a home that approves tools.
func TestCursorIsWiredLikeTheOtherACPTools(t *testing.T) {
	t.Parallel()
	if !acpEchoDriver(NewCursorDriver()) {
		t.Error("a Cursor prompt would be missing from the transcript")
	}
	if nativeACPToolName(providerDriverCursor) != "Cursor CLI" {
		t.Errorf("template controls are refused under the name %q", nativeACPToolName(providerDriverCursor))
	}
	if err := refuseACPUnsupportedControls(CreateRunParams{Instructions: "be brief"}, "Cursor CLI"); err == nil {
		t.Error("template instructions were discarded for Cursor")
	}
	if SessionMCPDriver(providerDriverCursor) {
		t.Error("Cursor receives the Olivares session MCP before it was measured with an account")
	}
	if !strings.Contains(mcpGovernanceWarning(providerDriverCursor), "not governed by Olivares") {
		t.Error("project MCP servers of Cursor are reported as governed")
	}
}

// Create and resume both mint the launch's authority, so a home the child could have written a
// pre-approval into is refused there, before any process starts.
func TestCursorLaunchAuthorityRefusesAPreApprovingHomeAndAcceptsAClean(t *testing.T) {
	t.Parallel()
	m := New(WithProviderDriver(NewCursorDriver()))
	for source, wantRefused := range map[string]bool{AuthSourceAccountHome: true} {
		for name, files := range map[string]map[string]string{
			"hooks":      {".cursor/hooks.json": `{"version":1,"hooks":{}}`},
			"allow list": {".cursor/cli-config.json": `{"permissions":{"allow":["Shell(*)"]}}`},
		} {
			home := cursorTestHome(t, files)
			home.Driver, home.AuthSource = providerDriverCursor, source
			_, _, err := m.mintLaunchAuthority(context.Background(), model.TenantID("tenant-test"), "", CreateRunParams{ProviderHome: home})
			var re *runErr
			if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(err.Error(), "approv") {
				t.Errorf("%s (%v): err = %v, want a 409 refusal", name, wantRefused, err)
			}
		}
	}
	clean := cursorTestHome(t, nil)
	clean.Driver, clean.AuthSource = providerDriverCursor, AuthSourceAccountHome
	if _, env, err := m.mintLaunchAuthority(context.Background(), model.TenantID("tenant-test"), "", CreateRunParams{ProviderHome: clean}); err != nil || len(env) != 0 {
		t.Fatalf("clean home: env=%v err=%v", env, err)
	}
}

func TestWithholdCursorLoginLink(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]string{
		"a json error": `{"jsonrpc":"2.0","id":3,"error":{"code":-32602,"message":"Invalid params","data":{"message":"Failed to open browser for login. Please visit: https://cursor.com/loginDeepControl?challenge=SECRETCHALLENGE&uuid=u&mode=login"}}}`,
		"plain text":   "Open a browser and navigate to this link: https://cursor.com/loginDeepControl?challenge=SECRETCHALLENGE&uuid=u",
	} {
		out := withholdCursorLoginLink([]byte(in))
		if strings.Contains(string(out), "SECRETCHALLENGE") || !strings.Contains(string(out), "[login link withheld]") {
			t.Errorf("%s: %s", name, out)
		}
		if strings.HasPrefix(in, "{") && !json.Valid(out) {
			t.Errorf("%s: the line is no longer JSON: %s", name, out)
		}
	}
	if got := withholdCursorLoginLink([]byte("see https://cursor.com/changelog")); string(got) != "see https://cursor.com/changelog" {
		t.Errorf("another link was changed: %s", got)
	}
}

// The link reaches neither the attach stream nor the recorder: the bridge withholds it before
// the ring, like a vault secret.
func TestCursorLoginLinkNeverReachesTheOutputRing(t *testing.T) {
	f := newProjectConfigFixture(t)
	r := f.wantStart(f.profile(providerDriverCursor), http.StatusCreated)
	runRef, _ := r.body["run_ref"].(string)
	lr, ok := f.h.m.rt.getLive(f.tenant, runRef)
	if !ok {
		t.Fatalf("expected a live run: %s", r.raw)
	}
	p := f.runner.lastProc()
	p.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"jsonrpc":"2.0","id":3,"error":{"code":-32602,"message":"Invalid params","data":{"message":"Failed to open browser for login. Please visit: https://cursor.com/loginDeepControl?challenge=SECRETCHALLENGE&uuid=u"}}}`)}
	p.out <- OutputFrame{Stream: streamStderr, Data: []byte("navigate to https://cursor.com/loginDeepControl?challenge=SECRETCHALLENGE")}
	waitFor(t, "both frames in the ring", func() bool { return len(lr.ring.readFrom(0).frames) >= 2 })
	for _, frame := range lr.ring.readFrom(0).frames {
		if strings.Contains(string(frame.Data), "SECRETCHALLENGE") {
			t.Fatalf("the output carries the one-time login link: %s", frame.Data)
		}
		if frame.Stream == streamStdout && !json.Valid(frame.Data) {
			t.Fatalf("withholding broke a JSON line: %s", frame.Data)
		}
	}
}
