// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
)

// The shared MCP server reaches every ACP tool the same way: ConfigureSessionMCP
// puts the endpoint on the launch, and the driver carries it in session/new and
// in the resume call. Gemini CLI and Grok Build used to receive none.

const (
	sessionMCPTestEndpoint = "http://127.0.0.1:19710/session/mcp"
	sessionMCPTestToken    = "fixture-session-mcp-token"
	sessionMCPTestEnv      = "OLIVARES_HOOK_PEP_TOKEN"
)

// sessionMCPFor runs the real launch step for driver and returns the server list
// the runtime hands to the driver (runtime_driver.go builds it from the same spec).
func sessionMCPFor(t *testing.T, driver string) []json.RawMessage {
	t.Helper()
	spec := LaunchSpec{Args: []string{"acp"}, Env: []EnvVar{{Name: sessionMCPTestEnv, Value: sessionMCPTestToken}}}
	cleanup, err := ConfigureSessionMCP(&spec, driver, t.TempDir(), "run-one", sessionMCPTestEndpoint, sessionMCPTestEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	return openCodeSessionMCP(spec)
}

// requireOlivaresServer fails unless params carry exactly the one Olivares HTTP
// server, authenticated with the launch's bearer.
func requireOlivaresServer(t *testing.T, what string, params map[string]any) {
	t.Helper()
	servers, ok := params["mcpServers"].([]any)
	if !ok || len(servers) != 1 {
		t.Fatalf("%s mcpServers = %v, want the one Olivares server", what, params["mcpServers"])
	}
	server, _ := servers[0].(map[string]any)
	headers, _ := server["headers"].([]any)
	if server["type"] != "http" || server["name"] != "olivares" || server["url"] != sessionMCPTestEndpoint || len(headers) != 1 {
		t.Fatalf("%s carried the wrong server: %v", what, server)
	}
	header, _ := headers[0].(map[string]any)
	if header["name"] != "Authorization" || header["value"] != "Bearer "+sessionMCPTestToken {
		t.Fatalf("%s server is not authenticated with the launch bearer: %v", what, header)
	}
}

// requireEmptyServerList fails unless params carry the empty array ACP requires.
func requireEmptyServerList(t *testing.T, what string, params map[string]any) {
	t.Helper()
	if servers, ok := params["mcpServers"].([]any); !ok || len(servers) != 0 {
		t.Fatalf("%s mcpServers = %v, want []", what, params["mcpServers"])
	}
}

// requireMCPRefusal fails unless err is the 409 the launch is refused with when a
// tool cannot carry the session MCP server, naming that tool.
func requireMCPRefusal(t *testing.T, err error, tool string) {
	t.Helper()
	var re *runErr
	want := tool + " does not support the HTTP MCP connection required for this session"
	if !errors.As(err, &re) || re.HTTPStatusCode() != http.StatusConflict || re.Error() != want {
		t.Fatalf("refusal = %v, want 409 %q", err, want)
	}
}

func TestSessionMCPLaunchCarriesTheEndpointOverACPForGeminiAndGrok(t *testing.T) {
	for _, driver := range []string{providerDriverGemini, providerDriverGrok} {
		t.Run(driver, func(t *testing.T) {
			dataDir := t.TempDir()
			spec := LaunchSpec{Args: []string{"--acp"}, Env: []EnvVar{{Name: sessionMCPTestEnv, Value: sessionMCPTestToken}}}
			cleanup, err := ConfigureSessionMCP(&spec, driver, dataDir, "run-one", sessionMCPTestEndpoint, sessionMCPTestEnv)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if spec.SessionMCPURL != sessionMCPTestEndpoint || spec.SessionMCPTokenEnv != sessionMCPTestEnv {
				t.Fatal("the launch did not carry the session MCP connection for ACP")
			}
			if entries, _ := os.ReadDir(dataDir); len(entries) != 0 || strings.Join(spec.Args, " ") != "--acp" {
				t.Fatal("ACP carries the server: no file and no argv change")
			}
		})
	}
}

// A launch without its bearer is refused, so the ACP request never carries an
// empty `Bearer `.
func TestSessionMCPLaunchRefusesAMissingBearerForGeminiAndGrok(t *testing.T) {
	for _, driver := range []string{providerDriverGemini, providerDriverGrok} {
		t.Run(driver, func(t *testing.T) {
			for name, env := range map[string][]EnvVar{
				"absent": nil,
				"empty":  {{Name: sessionMCPTestEnv, Value: ""}},
			} {
				spec := LaunchSpec{Args: []string{"--acp"}, Env: env}
				if _, err := ConfigureSessionMCP(&spec, driver, t.TempDir(), "run-one", sessionMCPTestEndpoint, sessionMCPTestEnv); err == nil || spec.SessionMCPURL != "" {
					t.Fatalf("%s bearer: err=%v url=%q, want a refusal and no endpoint", name, err, spec.SessionMCPURL)
				}
			}
		})
	}
}

// The bearer lookup agrees with the launch validator, which accepts any non-empty
// entry: an empty duplicate after the real one must not become `Bearer `.
func TestSessionMCPBearerIgnoresAnEmptyDuplicateEntry(t *testing.T) {
	spec := LaunchSpec{
		SessionMCPURL: sessionMCPTestEndpoint, SessionMCPTokenEnv: sessionMCPTestEnv,
		Env: []EnvVar{{Name: sessionMCPTestEnv, Value: sessionMCPTestToken}, {Name: sessionMCPTestEnv, Value: ""}},
	}
	servers := openCodeSessionMCP(spec)
	if len(servers) != 1 || !strings.Contains(string(servers[0]), `"value":"Bearer `+sessionMCPTestToken+`"`) {
		t.Fatalf("server list = %s, want the real bearer", servers)
	}
}

func TestGeminiSessionNewAndLoadCarrySessionMCP(t *testing.T) {
	for _, resume := range []string{"", "stored-gemini-1"} {
		method := acpMethodSessionNew
		if resume != "" {
			method = acpMethodSessionLoad
		}
		t.Run(method, func(t *testing.T) {
			peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
				cfg.sessionMCP = sessionMCPFor(t, providerDriverGemini)
				cfg.ResumeConversationID = resume
			})
			result := geminiSessionResultFor("g-1")
			if resume != "" {
				delete(result, "sessionId") // session/load answers the modes and models only
			}
			_, asked, err := peer.answerGemini(geminiInitializeResult(), result, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, req := range asked {
				if req.method == method {
					requireOlivaresServer(t, "gemini "+method, req.params)
					return
				}
			}
			t.Fatalf("no %s request in %v", method, geminiMethods(asked))
		})
	}
}

func TestGeminiRefusesSessionMCPWithoutHTTPCapability(t *testing.T) {
	peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.sessionMCP = sessionMCPFor(t, providerDriverGemini) })
	init := geminiInitializeResult()
	init["agentCapabilities"] = map[string]any{"loadSession": true}
	_, asked, err := peer.answerGemini(init, geminiSessionResultFor("g-1"), nil)
	requireMCPRefusal(t, err, "Gemini CLI")
	if got := geminiMethods(asked); len(got) != 1 || got[0] != acpMethodInitialize {
		t.Fatalf("the session was opened without its MCP transport: %v", got)
	}
}

// Without a session MCP endpoint the call carries the empty array ACP requires,
// never null, and a tool that does not advertise HTTP MCP is not refused.
func TestGeminiWithoutSessionMCPSendsAnEmptyServerListAndIsNotRefused(t *testing.T) {
	for _, resume := range []string{"", "stored-gemini-1"} {
		method := acpMethodSessionNew
		if resume != "" {
			method = acpMethodSessionLoad
		}
		t.Run(method, func(t *testing.T) {
			peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = resume })
			init := geminiInitializeResult()
			init["agentCapabilities"] = map[string]any{"loadSession": true}
			result := geminiSessionResultFor("g-1")
			if resume != "" {
				delete(result, "sessionId")
			}
			_, asked, err := peer.answerGemini(init, result, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, req := range asked {
				if req.method == method {
					requireEmptyServerList(t, "gemini "+method, req.params)
					return
				}
			}
			t.Fatalf("no %s request in %v", method, geminiMethods(asked))
		})
	}
}

// grokHandshakeParams plays a Grok handshake with init and returns the params of
// the first request named want (the conversation call), answering it with result.
func grokHandshakeParams(t *testing.T, peer *grokPeer, want string, init, result map[string]any) map[string]any {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, method, _ := peer.nextRequest()
	if method != acpMethodInitialize {
		t.Fatalf("first request = %q", method)
	}
	peer.reply(id, init)
	for {
		id, method, params := peer.nextRequest()
		switch method {
		case grokMethodAuthenticate:
			peer.reply(id, map[string]any{})
		case want:
			peer.reply(id, result)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return params
		default:
			t.Fatalf("unexpected handshake request %q", method)
		}
	}
}

// grokConversationCases are the three conversation calls the driver makes: a new
// conversation, a resume the agent advertises, and the load it falls back to.
var grokConversationCases = []struct {
	name, resume, method string
	noResumeCapability   bool
	result               map[string]any
}{
	{"new", "", acpMethodSessionNew, false, map[string]any{"sessionId": "grok-conv-1"}},
	{"resume", "stored-grok-1", acpMethodSessionResume, false, map[string]any{}},
	{"load", "stored-grok-1", acpMethodSessionLoad, true, map[string]any{}},
}

func grokInitFor(noResumeCapability bool) map[string]any {
	init := grokInitializeResult(grokAllAuthMethods())
	if noResumeCapability {
		delete(init["agentCapabilities"].(map[string]any), "sessionCapabilities")
	}
	return init
}

func TestGrokConversationCallsCarrySessionMCP(t *testing.T) {
	for _, tc := range grokConversationCases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newGrokPeer(t, func(cfg *DriverSessionConfig) {
				cfg.sessionMCP = sessionMCPFor(t, providerDriverGrok)
				cfg.ResumeConversationID = tc.resume
			})
			params := grokHandshakeParams(t, peer, tc.method, grokInitFor(tc.noResumeCapability), tc.result)
			requireOlivaresServer(t, "grok "+tc.method, params)
		})
	}
}

func TestGrokWithoutSessionMCPSendsAnEmptyServerListAndIsNotRefused(t *testing.T) {
	for _, tc := range grokConversationCases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newGrokPeer(t, func(cfg *DriverSessionConfig) { cfg.ResumeConversationID = tc.resume })
			init := grokInitFor(tc.noResumeCapability)
			delete(init["agentCapabilities"].(map[string]any), "mcpCapabilities")
			params := grokHandshakeParams(t, peer, tc.method, init, tc.result)
			requireEmptyServerList(t, "grok "+tc.method, params)
		})
	}
}

func TestGrokRefusesSessionMCPWithoutHTTPCapability(t *testing.T) {
	peer := newGrokPeer(t, func(cfg *DriverSessionConfig) { cfg.sessionMCP = sessionMCPFor(t, providerDriverGrok) })
	init := grokInitializeResult(grokAllAuthMethods())
	delete(init["agentCapabilities"].(map[string]any), "mcpCapabilities")
	done := make(chan error, 1)
	go func() {
		_, err := peer.session.Handshake(context.Background())
		done <- err
	}()
	id, _, _ := peer.nextRequest()
	peer.reply(id, init)
	requireMCPRefusal(t, <-done, "Grok Build")
	for _, line := range peer.sentLines() {
		var frame map[string]any
		_ = json.Unmarshal(line, &frame)
		if frame["method"] == acpMethodSessionNew || frame["method"] == grokMethodAuthenticate {
			t.Fatalf("the session was opened without its MCP transport: %s", line)
		}
	}
}
