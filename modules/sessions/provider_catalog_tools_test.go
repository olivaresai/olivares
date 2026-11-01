// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

type catalogToolPeer struct {
	t          *testing.T
	signed     bool
	methods    []string
	specs      []LaunchSpec
	procs      []*catalogPeerProcess
	grokOutput string
	grokExit   int
	launchErr  error
	// openCodeOutput is what `opencode models --pure` prints (one provider/model per line).
	openCodeOutput string
	openCodeExit   int
	modelIDs       []string
	statusExit     int
	claudeModels   []map[string]any
	codexModels    []map[string]any
}
type catalogPeerProcess struct {
	*fakeProc
	peer *catalogToolPeer
}

func (p *catalogPeerProcess) Send(_ context.Context, line []byte) error {
	var request map[string]any
	if err := json.Unmarshal(line, &request); err != nil {
		return err
	}
	method, _ := request["method"].(string)
	var response any
	if method == "" {
		p.peer.methods = append(p.peer.methods, "claude initialize")
		inner, _ := request["request"].(map[string]any)
		if request["type"] != "control_request" || inner["subtype"] != "initialize" {
			p.peer.t.Error("not a read-only SDK initialize")
		}
		ids := p.peer.modelIDs
		if ids == nil {
			ids = []string{"opus", "sonnet"}
		}
		models := []map[string]string{}
		for _, id := range ids {
			models = append(models, map[string]string{"value": id})
		}
		if p.peer.claudeModels != nil {
			response = map[string]any{"type": "control_response", "response": map[string]any{"request_id": request["request_id"], "subtype": "success", "response": map[string]any{"models": p.peer.claudeModels}}}
			raw, _ := json.Marshal(response)
			p.out <- OutputFrame{Stream: streamStdout, Data: raw}
			return nil
		}
		response = map[string]any{"type": "control_response", "response": map[string]any{"request_id": request["request_id"], "subtype": "success", "response": map[string]any{"models": models}}}
	} else {
		p.peer.methods = append(p.peer.methods, method)
		var result any
		switch method {
		case "initialize":
			result = map[string]any{}
		case "initialized":
			return nil
		case "account/read":
			var account any
			if p.peer.signed {
				account = map[string]string{"type": "chatgpt"}
			}
			result = map[string]any{"account": account, "requiresOpenaiAuth": true}
		case "model/list":
			params := request["params"].(map[string]any)
			if params["includeHidden"] != false {
				p.peer.t.Error("hidden models requested")
			}
			if p.peer.codexModels != nil {
				result = map[string]any{"data": p.peer.codexModels, "nextCursor": nil}
			} else if p.peer.modelIDs != nil {
				models := []map[string]any{}
				for _, id := range p.peer.modelIDs {
					models = append(models, map[string]any{"model": id, "hidden": false})
				}
				result = map[string]any{"data": models, "nextCursor": nil}
			} else if params["cursor"] == "page2" {
				result = map[string]any{"data": []map[string]any{{"model": "gpt-two", "hidden": false}}, "nextCursor": nil}
			} else {
				result = map[string]any{"data": []map[string]any{{"model": "gpt-one", "hidden": false}, {"model": "hidden", "hidden": true}}, "nextCursor": "page2"}
			}
		default:
			p.peer.t.Errorf("discovery sent a non-catalog method: %s", method)
			result = map[string]any{}
		}
		response = map[string]any{"id": request["id"], "result": result}
	}
	raw, _ := json.Marshal(response)
	p.out <- OutputFrame{Stream: streamStdout, Data: raw}
	return nil
}
func (p *catalogToolPeer) Launch(_ context.Context, spec LaunchSpec) (Process, error) {
	p.specs = append(p.specs, spec)
	if p.launchErr != nil {
		return nil, p.launchErr
	}
	proc := &catalogPeerProcess{fakeProc: &fakeProc{out: make(chan OutputFrame, 16), stopped: make(chan struct{})}, peer: p}
	p.procs = append(p.procs, proc)
	if len(spec.Args) > 0 && spec.Args[0] == "auth" {
		raw, _ := json.Marshal(map[string]bool{"loggedIn": p.signed})
		proc.out <- OutputFrame{Stream: streamStdout, Data: raw}
		proc.finish(p.statusExit)
	}
	if reflect.DeepEqual(spec.Args, []string{"models", "--pure"}) {
		raw := p.openCodeOutput
		if raw == "" {
			raw = openCodeCatalogFixture
		}
		proc.out = make(chan OutputFrame, len(strings.Split(raw, "\n")))
		for _, line := range strings.Split(raw, "\n") {
			proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(line)}
		}
		proc.finish(p.openCodeExit)
	}
	if reflect.DeepEqual(spec.Args, []string{"models"}) {
		raw := p.grokOutput
		if raw == "" {
			banner := "You are not authenticated."
			if p.signed {
				banner = "You are logged in with grok.com."
			}
			raw = banner + "\n\nDefault model: grok-4.6\n\nAvailable models:\n  * grok-4.6 (default)\n  - grok-4.5\n"
		}
		proc.out = make(chan OutputFrame, len(strings.Split(raw, "\n")))
		for _, line := range strings.Split(raw, "\n") {
			proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(line)}
		}
		proc.finish(p.grokExit)
	}
	return proc, nil
}

func TestAutomaticToolCatalogRejectsMalformedModelIDs(t *testing.T) {
	for _, driver := range []string{"claude", "codex"} {
		t.Run(driver, func(t *testing.T) {
			for _, id := range []string{"", "has space", "has\x01control", strings.Repeat("x", 201)} {
				t.Run(id, func(t *testing.T) {
					peer := &catalogToolPeer{t: t, signed: true, modelIDs: []string{id}}
					m := New(WithRunner(peer), WithProviderDriver(NewCodexDriver()))
					m.UseToolLoginsRoot(t.TempDir())
					tenant := model.TenantID(model.NewID())
					_, configHome, _ := m.OwnToolLoginHomes(tenant, driver)
					if err := os.MkdirAll(configHome, 0700); err != nil {
						t.Fatal(err)
					}
					if ids, err := m.ReadOwnToolLoginModels(context.Background(), tenant, driver); err == nil || len(ids) != 0 {
						t.Fatalf("malformed catalog succeeded: %v %v", ids, err)
					}
					for _, proc := range peer.procs {
						select {
						case <-proc.stopped:
						default:
							t.Fatal("catalog child left running")
						}
					}
				})
			}
		})
	}
}

func TestAutomaticToolCatalogDoesNotGrantEngineUserHome(t *testing.T) {
	userHome := t.TempDir()
	saved := engineUserHome
	engineUserHome = func() string { return userHome }
	t.Cleanup(func() { engineUserHome = saved })
	for _, driver := range []string{"claude", "codex"} {
		t.Run(driver, func(t *testing.T) {
			peer := &catalogToolPeer{t: t, signed: true}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(peer), WithProviderDriver(NewCodexDriver()), WithConfinement([]string{t.TempDir()}, true))
			m.UseExecutionEnvironmentRef(testEnvRef)
			configHome := t.TempDir()
			profile, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{Driver: driver, UserHome: userHome, ConfigHome: configHome, AuthSource: AuthSourceAccountHome})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.ReadProfileModels(context.Background(), tenant, profile.Ref); err != nil {
				t.Fatal(err)
			}
			if len(peer.specs) == 0 {
				t.Fatal("catalog never launched")
			}
			for _, spec := range peer.specs {
				if spec.Confinement == nil || !reflect.DeepEqual(spec.Confinement.ReadWrite, []string{configHome}) {
					t.Fatalf("catalog granted the engine user home: %+v", spec.Confinement)
				}
			}
		})
	}
}

func TestAutomaticClaudeCatalogRefusesFailedSignInStatus(t *testing.T) {
	peer := &catalogToolPeer{t: t, signed: true, statusExit: 1}
	m := New(WithRunner(peer))
	m.UseToolLoginsRoot(t.TempDir())
	tenant := model.TenantID(model.NewID())
	_, configHome, _ := m.OwnToolLoginHomes(tenant, "claude")
	if err := os.MkdirAll(configHome, 0700); err != nil {
		t.Fatal(err)
	}
	if ids, err := m.ReadOwnToolLoginModels(context.Background(), tenant, "claude"); err == nil || len(ids) != 0 {
		t.Fatalf("failed sign-in command advertised models: %v %v", ids, err)
	}
	if len(peer.specs) != 1 || len(peer.methods) != 0 {
		t.Fatal("model discovery continued after sign-in status failed")
	}
}

// Reading a model list must not execute user automation: the Claude catalog child
// is a session with the user's hooks, MCP servers and settings sources off, the same
// switches the provider snapshot's usage read passes. Without this pin, dropping one
// of them is caught only by the live-tool test.
func TestAutomaticClaudeCatalogRunsNoUserAutomation(t *testing.T) {
	peer := &catalogToolPeer{t: t, signed: true}
	m := New(WithRunner(peer))
	m.UseToolLoginsRoot(t.TempDir())
	tenant := model.TenantID(model.NewID())
	_, configHome, _ := m.OwnToolLoginHomes(tenant, "claude")
	if err := os.MkdirAll(configHome, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadOwnToolLoginModels(context.Background(), tenant, "claude"); err != nil {
		t.Fatal(err)
	}
	var session []string
	for _, spec := range peer.specs {
		if len(spec.Args) > 0 && spec.Args[0] != "auth" {
			session = spec.Args
		}
	}
	if session == nil {
		t.Fatal("the Claude catalog never opened its initialize session")
	}
	joined := strings.Join(session, "\x00")
	for _, want := range []string{
		"--strict-mcp-config\x00--mcp-config\x00" + `{"mcpServers":{}}`,
		"--setting-sources\x00\x00",
		"--settings\x00" + `{"disableAllHooks":true}`,
		"--no-session-persistence",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the Claude catalog session lacks %q: %q", strings.ReplaceAll(want, "\x00", " "), session)
		}
	}
}

func TestAutomaticGrokCatalogRejectsUnconfirmedAndMalformedOutput(t *testing.T) {
	const signed = "You are logged in with grok.com.\n\n"
	const list = "Available models:\n  * grok-4.6 (default)\n"
	for _, tc := range []struct {
		name   string
		output string
		exit   int
	}{
		{"signed out", "You are not authenticated.\n" + list, 0},
		{"no auth status", list, 0},
		{"empty auth host", "You are logged in with .\n" + list, 0},
		{"invalid auth host", "You are logged in with   .\n" + list, 0},
		{"other auth source", "You are using XAI_API_KEY.\n" + list, 0},
		{"incomplete catalog", signed, 0},
		{"malformed entry", signed + "Available models:\n  - invalid model\n", 0},
		{"failed command", signed + list, 1},
		{"response bound", signed + strings.Repeat("x", (1<<20)+1), 0},
		{"model bound", signed + "Available models:\n" + strings.Repeat("  - grok-4.6\n", 1001), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := &catalogToolPeer{t: t, signed: true, grokOutput: tc.output, grokExit: tc.exit}
			m := New(WithRunner(peer), WithProviderDriver(NewGrokDriver()))
			m.UseToolLoginsRoot(t.TempDir())
			tenant := model.TenantID(model.NewID())
			_, configHome, _ := m.OwnToolLoginHomes(tenant, "grok")
			if err := os.MkdirAll(configHome, 0700); err != nil {
				t.Fatal(err)
			}
			if ids, err := m.ReadOwnToolLoginModels(context.Background(), tenant, "grok"); err == nil || len(ids) != 0 {
				t.Fatalf("unconfirmed catalog advertised: %v %v", ids, err)
			}
			if len(peer.procs) != 1 {
				t.Fatal("the native catalog response was not examined")
			}
			for _, proc := range peer.procs {
				if proc.sentCount() != 0 {
					t.Fatal("discovery sent input to the tool")
				}
				select {
				case <-proc.stopped:
				default:
					t.Fatal("catalog child left running")
				}
			}
		})
	}
}
func TestAutomaticToolCatalogUsesAccountHomesWithoutTurns(t *testing.T) {
	for _, driver := range []string{"claude", "codex", "grok"} {
		t.Run(driver, func(t *testing.T) {
			peer := &catalogToolPeer{t: t, signed: true}
			m := New(WithRunner(peer), WithProviderDriver(NewCodexDriver()), WithProviderDriver(NewGrokDriver()))
			m.UseToolLoginsRoot(t.TempDir())
			tenant := model.TenantID(model.NewID())
			userHome, configHome, ok := m.OwnToolLoginHomes(tenant, driver)
			if !ok {
				t.Fatal("no own login home")
			}
			if err := os.MkdirAll(configHome, 0700); err != nil {
				t.Fatal(err)
			}
			ids, err := m.ReadOwnToolLoginModels(context.Background(), tenant, driver)
			expected := []string{"gpt-one", "gpt-two"}
			if driver == "claude" {
				expected = []string{"opus", "sonnet"}
			}
			if driver == "grok" {
				expected = []string{"grok-4.6", "grok-4.5"}
			}
			if err != nil || !reflect.DeepEqual(ids, expected) {
				t.Fatalf("models: %v %v", ids, err)
			}
			if driver == "grok" {
				if len(peer.specs) != 1 || !reflect.DeepEqual(peer.specs[0].Args, []string{"models"}) {
					t.Fatal("Grok discovery did not use the native read-only command")
				}
			}
			for _, spec := range peer.specs {
				values := map[string]string{}
				for _, value := range spec.Env {
					values[value.Name] = value.Value
				}
				configEnv := map[string]string{"claude": "CLAUDE_CONFIG_DIR", "codex": "CODEX_HOME", "grok": "GROK_HOME"}[driver]
				if values["HOME"] != userHome || values[configEnv] != configHome {
					t.Fatalf("wrong account home: %+v", spec)
				}
			}
			for _, proc := range peer.procs {
				select {
				case <-proc.stopped:
				default:
					t.Fatal("catalog child left running")
				}
			}
			peer.signed = false
			peer.methods = nil
			if _, err := m.ReadOwnToolLoginModels(context.Background(), tenant, driver); err == nil {
				t.Fatal("signed-out account advertised models")
			}
			for _, method := range peer.methods {
				if method == "model/list" {
					t.Fatal("models requested before sign-in")
				}
			}
		})
	}
}

// The `* ... (default)` line is the only default marker `grok models` prints.
func TestParseGrokModelCatalogDefaultMarker(t *testing.T) {
	out, err := parseGrokModelCatalog("You are logged in with grok.com.\n\nDefault model: grok-4.6\n\nAvailable models:\n  * grok-4.6 (default)\n  - grok-4.5\n")
	if err != nil || len(out) != 2 {
		t.Fatalf("parse: %+v %v", out, err)
	}
	if out[0].ID != "grok-4.6" || out[0].IsDefault == nil || !*out[0].IsDefault || out[1].ID != "grok-4.5" || out[1].IsDefault == nil || *out[1].IsDefault {
		t.Fatalf("default marker: %+v", out)
	}
}

// openCodeCatalogFixture is the shape `opencode models --pure` prints on v1.18.34 for an
// OpenCode console key: one provider/model per line, the free models among them.
const openCodeCatalogFixture = "opencode/claude-haiku-5-5\nopencode/deepseek-v4.1-flash-free\nopencode/glm-5.3-flash-free\n" +
	"opencode/longcat-2.5-preview-free\nopencode/mimo-v2.6-flash-free\nopencode-go/kimi-k3\n"

// #2567: OpenCode's catalog is the tool's own `models` list, read in the account's home
// with `--pure` (v1.18.34: "run without external plugins", so a configured plugin does
// not run), with no input sent. The free models are listed exactly as the tool prints
// them, which is the provider/model form a session's model option takes.
func TestOpenCodeCatalogListsTheToolsOwnModels(t *testing.T) {
	peer := &catalogToolPeer{t: t, signed: true}
	m := New(WithRunner(peer), WithProviderDriver(NewOpenCodeDriver()))
	m.UseToolLoginsRoot(t.TempDir())
	tenant := model.TenantID(model.NewID())
	userHome, configHome, ok := m.OwnToolLoginHomes(tenant, "opencode")
	if !ok {
		t.Fatal("no own login home")
	}
	if err := os.MkdirAll(configHome, 0o700); err != nil {
		t.Fatal(err)
	}
	ids, err := m.ReadOwnToolLoginModels(context.Background(), tenant, "opencode")
	want := []string{"opencode/claude-haiku-5-5", "opencode/deepseek-v4.1-flash-free", "opencode/glm-5.3-flash-free",
		"opencode/longcat-2.5-preview-free", "opencode/mimo-v2.6-flash-free", "opencode-go/kimi-k3"}
	if err != nil || !reflect.DeepEqual(ids, want) {
		t.Fatalf("models = %v %v, want the tool's own list", ids, err)
	}
	if len(peer.specs) != 1 || !reflect.DeepEqual(peer.specs[0].Args, []string{"models", "--pure"}) {
		t.Fatalf("OpenCode discovery did not run the plugin-free native list: %+v", peer.specs)
	}
	values := map[string]string{}
	for _, value := range peer.specs[0].Env {
		values[value.Name] = value.Value
	}
	if values["HOME"] != userHome || values["OPENCODE_CONFIG_DIR"] != configHome || values["OPENCODE_DISABLE_AUTOUPDATE"] != "1" {
		t.Fatalf("wrong account home: %+v", peer.specs[0].Env)
	}
	for _, proc := range peer.procs {
		if proc.sentCount() != 0 {
			t.Fatal("discovery sent input to the tool")
		}
		select {
		case <-proc.stopped:
		default:
			t.Fatal("catalog child left running")
		}
	}
}

// A list the engine cannot read whole is no list: nothing is advertised.
func TestOpenCodeCatalogRejectsFailedAndMalformedOutput(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		exit         int
	}{
		{"failed command", openCodeCatalogFixture, 1},
		{"no provider", "glm-5.3-flash-free\n", 0},
		{"empty provider", "/glm-5.3-flash-free\n", 0},
		{"empty model", "opencode/\n", 0},
		{"space in id", "opencode/glm 5.3\n", 0},
		{"a sentence", "Error: something went wrong\n", 0},
		{"response bound", strings.Repeat("x", (1<<20)+1), 0},
		{"model bound", strings.Repeat("opencode/m\n", 1001), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := &catalogToolPeer{t: t, signed: true, openCodeOutput: tc.output, openCodeExit: tc.exit}
			m := New(WithRunner(peer), WithProviderDriver(NewOpenCodeDriver()))
			m.UseToolLoginsRoot(t.TempDir())
			tenant := model.TenantID(model.NewID())
			_, configHome, _ := m.OwnToolLoginHomes(tenant, "opencode")
			if err := os.MkdirAll(configHome, 0o700); err != nil {
				t.Fatal(err)
			}
			if ids, err := m.ReadOwnToolLoginModels(context.Background(), tenant, "opencode"); err == nil || len(ids) != 0 {
				t.Fatalf("unreadable catalog advertised: %v %v", ids, err)
			}
			if len(peer.procs) != 1 {
				t.Fatal("the native catalog response was not examined")
			}
		})
	}
}

// A catalog id is what a session's model option takes: on the tool's own login
// (no bound record) the free model keeps the provider/model spelling the tool listed.
func TestOpenCodeCatalogIDReachesTheModelOptionUnchanged(t *testing.T) {
	for _, id := range []string{"opencode/glm-5.3-flash-free", "opencode/deepseek-v4.1-flash-free"} {
		if got := openCodeModelValue(BoundProvider{}, id, false); got != id {
			t.Fatalf("native model %q became %q", id, got)
		}
	}
}
