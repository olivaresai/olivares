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
)

// The Gemini CLI driver is held to the wire the pinned CLI 0.62.0 was recorded
// speaking (GEMINI-ACP.md): the same fake peer as the OpenCode driver, answering
// with Gemini's own initialize, session/new, session/set_mode and
// session/request_permission shapes.

func newGeminiPeer(t *testing.T, opts ...func(*DriverSessionConfig)) *openCodePeer {
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
	p.session = geminiDriver{}.OpenSession(cfg)
	return p
}

func geminiInitializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": 1,
		"authMethods": []any{
			map[string]any{"id": "oauth-personal", "name": "Log in with Google"},
			map[string]any{"id": "gemini-api-key", "name": "Gemini API key"},
		},
		"agentInfo":         map[string]any{"name": "gemini-cli", "version": "0.62.0"},
		"agentCapabilities": map[string]any{"loadSession": true, "mcpCapabilities": map[string]any{"http": true}},
	}
}

func geminiSessionResultFor(sessionID string) map[string]any {
	return map[string]any{
		"sessionId": sessionID,
		"modes": map[string]any{
			"currentModeId": "default",
			"availableModes": []any{
				map[string]any{"id": "default"}, map[string]any{"id": "autoEdit"},
				map[string]any{"id": "yolo"}, map[string]any{"id": "plan"},
			},
		},
		"models": map[string]any{
			"currentModelId": "auto",
			"availableModels": []any{
				map[string]any{"modelId": "auto"}, map[string]any{"modelId": "gemini-2.5-pro"},
				map[string]any{"modelId": "gemini-3.8-flash"},
			},
		},
	}
}

// geminiRequest is one request the driver sent: its method and params.
type geminiRequest struct {
	method string
	params map[string]any
}

// answerGemini runs one handshake, answering each request with the recorded
// shape (or the override named for its method), and returns what the driver
// asked, in order.
func (p *openCodePeer) answerGemini(init, newOrLoad any, answers map[string]func(id int64)) (DriverHandshake, []geminiRequest, error) {
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
	var asked []geminiRequest
	for {
		select {
		case res := <-done:
			return res.hs, asked, res.err
		case f := <-p.frames:
			idf, ok := f["id"].(float64)
			if !ok {
				continue
			}
			method, _ := f["method"].(string)
			params, _ := f["params"].(map[string]any)
			asked = append(asked, geminiRequest{method, params})
			id := int64(idf)
			if answer, ok := answers[method]; ok {
				answer(id)
				continue
			}
			switch method {
			case acpMethodInitialize:
				p.reply(id, init)
			case acpMethodSessionNew, acpMethodSessionLoad:
				p.reply(id, newOrLoad)
			case geminiMethodSetMode, geminiMethodSetModel:
				p.reply(id, map[string]any{})
			case "authenticate":
				p.reply(id, map[string]any{})
			default:
				p.t.Fatalf("unexpected handshake request %q", method)
			}
		case <-time.After(3 * time.Second):
			p.t.Fatal("timed out waiting for the handshake")
		}
	}
}

func geminiMethods(asked []geminiRequest) []string {
	var out []string
	for _, r := range asked {
		out = append(out, r.method)
	}
	return out
}

func TestGeminiLaunchIsOneOwnedACPProcess(t *testing.T) {
	t.Parallel()
	d := geminiDriver{}
	if got := d.LaunchArgs(DriverLaunch{WorkDir: "/w", Model: "gemini-2.5-pro"}); !slices.Equal(got, []string{"--acp"}) {
		t.Fatalf("argv = %q, want only --acp: the model and mode travel on the protocol", got)
	}
	env := d.LaunchEnv(DriverLaunch{})
	if len(env) != 1 || env[0].Name != "GEMINI_CLI_NO_RELAUNCH" || env[0].Value != "true" {
		t.Fatalf("env = %+v, want GEMINI_CLI_NO_RELAUNCH=true so the owned child is not a wrapper", env)
	}
	if d.Key() != "gemini-cli" || d.DefaultProgram() != "gemini" || d.ConfigHomeEnv() != "GEMINI_CLI_HOME" {
		t.Fatalf("identity = %q %q %q", d.Key(), d.DefaultProgram(), d.ConfigHomeEnv())
	}
	if got := d.TransportProfile(); got != (DriverTransportProfile{Protocol: "gemini_acp", IO: ioBidirectional, Input: inputText}) {
		t.Fatalf("transport = %+v", got)
	}
}

func TestGeminiHandshakeSetsTheApprovalModeAndNeverAuthenticates(t *testing.T) {
	t.Parallel()
	var states []string
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
		cfg.OnAuthState = func(state string) { states = append(states, state) }
	})
	hs, asked, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_gem"), nil)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if hs.ConversationID != "ses_gem" || hs.AuthState != AuthStateUnknown {
		t.Fatalf("handshake = %+v, want the CLI's conversation and unknown readiness", hs)
	}
	if got := geminiMethods(asked); !slices.Equal(got, []string{acpMethodInitialize, acpMethodSessionNew, geminiMethodSetMode}) {
		t.Fatalf("requests = %q, want initialize, session/new, then the explicit mode and nothing else", got)
	}
	if asked[2].params["modeId"] != "default" || asked[2].params["sessionId"] != "ses_gem" {
		t.Fatalf("set_mode params = %v", asked[2].params)
	}
	if len(states) != 1 || states[0] != AuthStateUnknown {
		t.Fatalf("OnAuthState = %v, want the initial unknown", states)
	}
}

// Only read-only differs, and the default is set on purpose: a CLI whose
// settings default to an approving mode must not run an asking preset in it.
func TestGeminiPresetsRunTheirApprovalMode(t *testing.T) {
	t.Parallel()
	for preset, want := range map[string]string{
		PresetReadOnly:         "plan",
		PresetAsk:              "default",
		PresetEditsOnly:        "default",
		PresetEditsAndCommands: "default",
		PresetFull:             "default",
		"":                     "default",
	} {
		peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.Preset = preset })
		_, asked, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_mode"), nil)
		if err != nil {
			t.Fatalf("preset %q: %v", preset, err)
		}
		if got := asked[len(asked)-1]; got.method != geminiMethodSetMode || got.params["modeId"] != want {
			t.Errorf("preset %q sent %s %v, want modeId %q", preset, got.method, got.params, want)
		}
	}
}

func TestGeminiNeverSelectsAModeThatApprovesWithoutAsking(t *testing.T) {
	t.Parallel()
	for _, preset := range []string{PresetReadOnly, PresetAsk, PresetEditsOnly, PresetEditsAndCommands, PresetFull, "", "unknown"} {
		if mode := geminiModeFor(preset); mode != "plan" && mode != "default" {
			t.Errorf("preset %q selects %q: autoEdit and yolo approve without asking, which the live policy decides", preset, mode)
		}
	}
}

func TestGeminiMissingModeFailsTheHandshakeInsteadOfRunningWithoutIt(t *testing.T) {
	t.Parallel()
	result := geminiSessionResultFor("ses_nomode")
	result["modes"] = map[string]any{"availableModes": []any{map[string]any{"id": "default"}}}
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.Preset = PresetReadOnly })
	_, asked, err := peer.answerGemini(geminiInitializeResult(), result, nil)
	var rejected *requestNotOffered
	if !errors.As(err, &rejected) || !strings.Contains(err.Error(), "plan") {
		t.Fatalf("err = %v, want a refusal naming the plan mode the CLI did not offer", err)
	}
	if slices.Contains(geminiMethods(asked), geminiMethodSetMode) {
		t.Fatal("a read-only session must not fall back to another mode")
	}
}

func TestGeminiModelIsAnExactOfferedValue(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.Model = "gemini-2.5-pro" })
	_, asked, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_model"), nil)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	last := asked[len(asked)-1]
	if last.method != geminiMethodSetModel || last.params["modelId"] != "gemini-2.5-pro" || last.params["sessionId"] != "ses_model" {
		t.Fatalf("last request = %s %v, want session/set_model with the offered model", last.method, last.params)
	}

	for _, bad := range []string{"gemini-2.5", "GEMINI-2.5-PRO", "gemini-9-ultra"} {
		peer = newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.Model = bad })
		_, asked, err = peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_bad"), nil)
		var rejected *requestNotOffered
		if !errors.As(err, &rejected) || !strings.Contains(err.Error(), "gemini-2.5-pro") {
			t.Fatalf("model %q: err = %v, want a refusal that names what the CLI offers", bad, err)
		}
		if slices.Contains(geminiMethods(asked), geminiMethodSetModel) {
			t.Fatalf("model %q was sent although the CLI did not offer it", bad)
		}
	}
}

func TestGeminiNoModelLeavesTheCLIsOwn(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t)
	_, asked, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_own"), nil)
	if err != nil || slices.Contains(geminiMethods(asked), geminiMethodSetModel) {
		t.Fatalf("err=%v requests=%q: no model named, none may be chosen", err, geminiMethods(asked))
	}
}

func TestGeminiRejectedSettingFailsTheHandshake(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t)
	_, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_rej"), map[string]func(int64){
		geminiMethodSetMode: func(id int64) { peer.replyError(id, -32603, "Internal error") },
	})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity {
		t.Fatalf("err = %v, want a 422 refusal rather than a session on an unknown mode", err)
	}
}

func TestGeminiAuthRequiredOnNewPublishesRequired(t *testing.T) {
	t.Parallel()
	var states []string
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
		cfg.OnAuthState = func(state string) { states = append(states, state) }
	})
	_, asked, err := peer.answerGemini(geminiInitializeResult(), nil, map[string]func(int64){
		acpMethodSessionNew: func(id int64) {
			peer.replyError(id, acpErrAuthRequired, "Gemini API key is missing or not configured.")
		},
	})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity || !strings.Contains(err.Error(), "auth_required") {
		t.Fatalf("err = %v, want the bounded auth_required refusal", err)
	}
	if strings.Contains(err.Error(), "API key is missing") {
		t.Fatalf("err = %q repeats the provider's text", err)
	}
	if !slices.Equal(states, []string{AuthStateUnknown, AuthStateRequired}) {
		t.Fatalf("OnAuthState = %v, want unknown then the CLI's own required", states)
	}
	if slices.Contains(geminiMethods(asked), "authenticate") {
		t.Fatal("the driver must not authenticate to recover")
	}
}

func TestGeminiResumeLoadsTheStoredConversationOnly(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "ses_old" })
	load := geminiSessionResultFor("")
	delete(load, "sessionId")
	hs, asked, err := peer.answerGemini(geminiInitializeResult(), load, nil)
	if err != nil || hs.ConversationID != "ses_old" {
		t.Fatalf("resume = %+v %v, want the stored conversation", hs, err)
	}
	if got := geminiMethods(asked); !slices.Equal(got, []string{acpMethodInitialize, acpMethodSessionLoad, geminiMethodSetMode}) {
		t.Fatalf("requests = %q, want session/load and never session/new", got)
	}
	if asked[1].params["sessionId"] != "ses_old" || asked[1].params["cwd"] != "/workspace/fixture" {
		t.Fatalf("load params = %v", asked[1].params)
	}
}

func TestGeminiFailedResumeNeverStartsANewConversation(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "ses_gone" })
	_, asked, err := peer.answerGemini(geminiInitializeResult(), nil, map[string]func(int64){
		acpMethodSessionLoad: func(id int64) { peer.replyError(id, -32603, "No previous sessions found for this project.") },
	})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict {
		t.Fatalf("err = %v, want a 409 refusal", err)
	}
	if slices.Contains(geminiMethods(asked), acpMethodSessionNew) {
		t.Fatal("a failed resume must not start a different conversation")
	}
	if peer.session.ConversationID() != "" {
		t.Fatalf("conversation = %q, want none bound", peer.session.ConversationID())
	}

	// A CLI that does not advertise session load cannot resume at all.
	peer = newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "ses_gone" })
	init := geminiInitializeResult()
	init["agentCapabilities"] = map[string]any{}
	_, asked, err = peer.answerGemini(init, nil, nil)
	if !errors.As(err, &re) || re.status != http.StatusConflict || !slices.Equal(geminiMethods(asked), []string{acpMethodInitialize}) {
		t.Fatalf("err = %v requests = %q, want a refusal before any conversation request", err, geminiMethods(asked))
	}
}

func TestGeminiResumeOfADifferentConversationIsRefused(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = "ses_old" })
	_, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_other"), nil)
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict {
		t.Fatalf("err = %v, want a 409 refusal of another conversation", err)
	}
}

func TestGeminiRefusesAProtocolVersionItDoesNotImplement(t *testing.T) {
	t.Parallel()
	for _, raw := range []any{2, "1", nil} {
		peer := newGeminiPeer(t)
		init := geminiInitializeResult()
		init["protocolVersion"] = raw
		if _, _, err := peer.answerGemini(init, nil, nil); err == nil {
			t.Fatalf("version %#v was accepted", raw)
		}
	}
}

// The permission request Gemini sends offers allow_always, allow_once and
// reject_once (ids proceed_always, proceed_once, cancel), and carries no rawInput.
func geminiPermissionRequest(sessionID string) map[string]any {
	return map[string]any{
		"sessionId": sessionID,
		"options": []any{
			map[string]any{"optionId": "proceed_always", "name": "Allow for this session", "kind": acpPermissionAllowAlways},
			map[string]any{"optionId": "proceed_once", "name": "Allow", "kind": acpPermissionAllowOnce},
			map[string]any{"optionId": "cancel", "name": "Reject", "kind": acpPermissionRejectOnce},
		},
		"toolCall": map[string]any{
			"toolCallId": "run_shell_command__1", "status": "pending", "kind": "execute",
			"title": "touch marker.txt", "locations": []any{},
		},
	}
}

func (p *openCodePeer) permissionOutcome(id string) acpPermissionOutcome {
	p.t.Helper()
	var reply acpRequestPermissionResponse
	waitFor(p.t, "the permission was answered", func() bool {
		for _, line := range p.sentLines() {
			var frame map[string]any
			if json.Unmarshal(line, &frame) != nil || frame["id"] != id {
				continue
			}
			raw, _ := json.Marshal(frame["result"])
			return json.Unmarshal(raw, &reply) == nil
		}
		return false
	})
	return reply.Outcome
}

func TestGeminiPermissionGrantIsOnceOnlyAndNamesTheDriver(t *testing.T) {
	t.Parallel()
	var seen ProviderApprovalRequest
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(_ context.Context, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			seen = req
			return ProviderApprovalDecision{Allow: true, Granted: []string{"proceed_once", "proceed_always"}, SessionScope: true}, nil
		}
	})
	if _, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_perm"), nil); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	go func() { _, _ = peer.session.Input(context.Background(), "touch a marker") }()
	promptID, _, _ := peer.nextRequest()
	peer.requestFromServer("srv-1", acpReqRequestPermission, geminiEditPermissionRequest("ses_perm"))
	if got := peer.permissionOutcome("srv-1"); got.Outcome != acpOutcomeSelected || got.OptionID != "proceed_once" {
		t.Fatalf("outcome = %+v, want proceed_once and never proceed_always", got)
	}
	if seen.Driver != "gemini-cli" || seen.Method != acpReqRequestPermission || seen.Kind != acpKindToolCallPermission ||
		strings.Join(seen.Requested, ",") != "proceed_always,proceed_once,cancel" {
		t.Fatalf("authority saw %+v", seen)
	}
	// The path comes from locations; the title is a description and is never a fact.
	if seen.CommandLine != "" || !seen.FactsComplete || len(seen.FilePaths) != 1 || seen.FilePaths[0] != "/workspace/fixture/note.txt" {
		t.Fatalf("authority saw command %q paths %v complete=%v, want only the path from locations", seen.CommandLine, seen.FilePaths, seen.FactsComplete)
	}
	peer.reply(promptID, map[string]any{"stopReason": "end_turn"})
}

func TestGeminiDeniedPermissionAnswersTheOneShotRejection(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			return ProviderApprovalDecision{Allow: false}, nil
		}
	})
	if _, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_deny"), nil); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	go func() { _, _ = peer.session.Input(context.Background(), "touch a marker") }()
	promptID, _, _ := peer.nextRequest()
	peer.requestFromServer("srv-1", acpReqRequestPermission, geminiEditPermissionRequest("ses_deny"))
	if got := peer.permissionOutcome("srv-1"); got.Outcome != acpOutcomeSelected || got.OptionID != "cancel" {
		t.Fatalf("outcome = %+v, want the CLI's reject_once option", got)
	}
	peer.reply(promptID, map[string]any{"stopReason": "end_turn"})
}

func TestGeminiNoApprovalAuthorityRefusesEveryPermission(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t)
	if _, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_closed"), nil); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	go func() { _, _ = peer.session.Input(context.Background(), "touch a marker") }()
	peer.nextRequest()
	peer.requestFromServer("srv-1", acpReqRequestPermission, geminiEditPermissionRequest("ses_closed"))
	if got := peer.permissionOutcome("srv-1"); got.Outcome != acpOutcomeSelected || got.OptionID != "cancel" {
		t.Fatalf("outcome = %+v: with no authority wired the only answer is the CLI's reject_once option", got)
	}
}

// A grant that names only the persistent option, or names the once option for an
// option whose kind is allow_always, selects nothing.
func TestGeminiGrantsThatAreNotOnceAreRefused(t *testing.T) {
	t.Parallel()
	for name, granted := range map[string][]string{
		"only the persistent option": {"proceed_always"},
		"no option":                  nil,
		"an option nobody offered":   {"proceed_forever"},
	} {
		peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
			cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
				return ProviderApprovalDecision{Allow: true, Granted: granted, SessionScope: true}, nil
			}
		})
		if _, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_grant"), nil); err != nil {
			t.Fatalf("%s: handshake: %v", name, err)
		}
		go func() { _, _ = peer.session.Input(context.Background(), "edit a file") }()
		peer.nextRequest()
		peer.requestFromServer("srv-1", acpReqRequestPermission, geminiEditPermissionRequest("ses_grant"))
		if got := peer.permissionOutcome("srv-1"); got.Outcome != acpOutcomeSelected || got.OptionID != "cancel" {
			t.Errorf("%s: outcome = %+v, want the one-shot rejection", name, got)
		}
	}
}

// An edit names its absolute path in locations; a shell call names nothing but a
// title, and a title is a description, not a fact.
func geminiEditPermissionRequest(sessionID string) map[string]any {
	req := geminiPermissionRequest(sessionID)
	req["toolCall"] = map[string]any{
		"toolCallId": "write_file__1", "status": "pending", "kind": "edit",
		"title": "Writing to note.txt", "locations": []any{map[string]any{"path": "/workspace/fixture/note.txt"}},
	}
	return req
}

func geminiToolRequest(sessionID, kind string) map[string]any {
	req := geminiPermissionRequest(sessionID)
	req["toolCall"] = map[string]any{"toolCallId": kind + "__1", "status": "pending", "kind": kind, "title": "a " + kind, "locations": []any{}}
	return req
}

func TestGeminiRequestsWithNoFactsForThePolicyAreRefusedWithoutAsking(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"execute", "edit", "delete", "move", "fetch", "other", ""} {
		asked := 0
		peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
			cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
				asked++
				return ProviderApprovalDecision{Allow: true, Granted: []string{"proceed_once"}}, nil
			}
		})
		if _, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_nofacts"), nil); err != nil {
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

func TestGeminiRequestsWithFactsOrNoEffectReachThePolicy(t *testing.T) {
	t.Parallel()
	for name, req := range map[string]map[string]any{
		"an edit with its path": geminiEditPermissionRequest("ses_facts"),
		"a read":                geminiToolRequest("ses_facts", "read"),
		"a search":              geminiToolRequest("ses_facts", "search"),
		"a thought":             geminiToolRequest("ses_facts", "think"),
	} {
		var seen ProviderApprovalRequest
		peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
			cfg.Approve = func(_ context.Context, r ProviderApprovalRequest) (ProviderApprovalDecision, error) {
				seen = r
				return ProviderApprovalDecision{Allow: true, Granted: []string{"proceed_once"}}, nil
			}
		})
		if _, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_facts"), nil); err != nil {
			t.Fatalf("%s: handshake: %v", name, err)
		}
		go func() { _, _ = peer.session.Input(context.Background(), "do it") }()
		peer.nextRequest()
		peer.requestFromServer("srv-1", acpReqRequestPermission, req)
		if got := peer.permissionOutcome("srv-1"); got.OptionID != "proceed_once" {
			t.Errorf("%s: outcome = %+v, want the grant the authority gave", name, got)
		}
		if seen.Driver != "gemini-cli" {
			t.Errorf("%s: the authority was not asked: %+v", name, seen)
		}
		if name == "an edit with its path" && (!seen.FactsComplete || len(seen.FilePaths) != 1 || seen.FilePaths[0] != "/workspace/fixture/note.txt") {
			t.Errorf("edit facts = %+v, want the one path from locations", seen)
		}
	}
}

// A tool that runs a command and names none has nothing to review, whatever paths
// it also lists.
func TestAnExecuteToolWithoutACommandIsNotCompleteEvenWithPaths(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"no command, a path": `{"kind":"execute","locations":[{"path":"/workspace/x"}]}`,
		"no command at all":  `{"kind":"execute"}`,
	} {
		var call acpToolCall
		if err := json.Unmarshal([]byte(raw), &call); err != nil {
			t.Fatal(err)
		}
		if call.facts.Complete {
			t.Errorf("%s: facts = %+v, want incomplete", name, call.facts)
		}
	}
	var call acpToolCall
	if err := json.Unmarshal([]byte(`{"kind":"execute","rawInput":{"command":"git status"}}`), &call); err != nil || !call.facts.Complete || call.facts.CommandLine != "git status" {
		t.Fatalf("a command with its text must stay complete: %+v %v", call.facts, err)
	}
}

func TestGeminiAuthRequiredOnAnyConversationCallPublishesRequired(t *testing.T) {
	t.Parallel()
	for _, method := range []string{geminiMethodSetMode, geminiMethodSetModel, acpMethodSessionLoad} {
		var states []string
		peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
			cfg.OnAuthState = func(state string) { states = append(states, state) }
			cfg.Model = "gemini-2.5-pro"
			if method == acpMethodSessionLoad {
				cfg.ResumeConversationID = "ses_old"
			}
		})
		_, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_auth"), map[string]func(int64){
			method: func(id int64) { peer.replyError(id, acpErrAuthRequired, "login required") },
		})
		if err == nil || !strings.Contains(err.Error(), "auth_required") || !slices.Equal(states, []string{AuthStateUnknown, AuthStateRequired}) {
			t.Errorf("%s: err=%v states=%v, want the bounded refusal and the CLI's own required", method, err, states)
		}
	}
}

func TestGeminiRejectedModelAndEmptyModeListFailTheHandshake(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.Model = "gemini-2.5-pro" })
	_, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_m"), map[string]func(int64){
		geminiMethodSetModel: func(id int64) { peer.replyError(id, -32603, "Internal error") },
	})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity {
		t.Fatalf("rejected model: err = %v, want a 422 refusal", err)
	}

	result := geminiSessionResultFor("ses_nomodes")
	result["modes"] = map[string]any{"availableModes": []any{}}
	peer = newGeminiPeer(t)
	_, asked, err := peer.answerGemini(geminiInitializeResult(), result, nil)
	if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity || !strings.Contains(err.Error(), "offered no approval mode") {
		t.Fatalf("empty mode list: err = %v", err)
	}
	if slices.Contains(geminiMethods(asked), geminiMethodSetMode) {
		t.Fatal("no mode may be set when the CLI offered none")
	}
}

// The CLI rejects a null mcpServers; the driver connects no Olivares MCP edge.
func TestGeminiSendsAnEmptyMCPServerListNotNull(t *testing.T) {
	t.Parallel()
	peer := newGeminiPeer(t)
	_, asked, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_mcp"), nil)
	if err != nil {
		t.Fatal(err)
	}
	servers, ok := asked[1].params["mcpServers"].([]any)
	if !ok || len(servers) != 0 {
		t.Fatalf("mcpServers = %#v, want an empty list", asked[1].params["mcpServers"])
	}
}

func TestGeminiProtocolTokenReachesTheReadinessVocabulary(t *testing.T) {
	t.Parallel()
	got := normalizeTransportProfile(geminiDriver{}.TransportProfile())
	if got.Protocol != "gemini_acp" || got.IO != ioBidirectional || got.Input != inputText {
		t.Fatalf("readiness transport = %+v, want gemini_acp, bidirectional, text", got)
	}
}

func TestGeminiBoundKeySelectsOnlyAPIKeyAuthentication(t *testing.T) {
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
		cfg.AuthSource = AuthSourceManagedInjection
		cfg.BoundProvider = BoundProvider{Kind: ProviderKindGemini, Endpoint: "https://generativelanguage.googleapis.com"}
	})
	_, asked, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(geminiMethods(asked), []string{acpMethodInitialize, "authenticate", acpMethodSessionNew, geminiMethodSetMode}) || asked[1].params["methodId"] != "gemini-api-key" {
		t.Fatalf("requests = %+v", asked)
	}
	init := geminiInitializeResult()
	init["authMethods"] = []any{map[string]any{"id": "oauth-personal"}}
	peer = newGeminiPeer(t, func(cfg *DriverSessionConfig) {
		cfg.AuthSource = AuthSourceManagedInjection
		cfg.BoundProvider = BoundProvider{Kind: ProviderKindGemini}
	})
	_, asked, err = peer.answerGemini(init, nil, nil)
	if err == nil || !slices.Equal(geminiMethods(asked), []string{acpMethodInitialize}) {
		t.Fatalf("unoffered key method: err=%v requests=%+v", err, asked)
	}
}

// The deprecated governed adapter still supplies its own authentication. Its
// credential has no provider-record binding, so the CLI's original handshake stays.
func TestGeminiLegacyManagedAdapterKeepsItsOwnAuthentication(t *testing.T) {
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
		cfg.AuthSource = AuthSourceManagedInjection
		cfg.BoundProvider = BoundProvider{}
	})
	_, asked, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_legacy"), nil)
	if err != nil || !slices.Equal(geminiMethods(asked), []string{acpMethodInitialize, acpMethodSessionNew, geminiMethodSetMode}) {
		t.Fatalf("legacy adapter handshake: err=%v requests=%+v", err, asked)
	}
}
