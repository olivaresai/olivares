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
	modelIDs   []string
	statusExit int
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
			if p.peer.modelIDs != nil {
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
	proc := &catalogPeerProcess{fakeProc: &fakeProc{out: make(chan OutputFrame, 16), stopped: make(chan struct{})}, peer: p}
	p.procs = append(p.procs, proc)
	if len(spec.Args) > 0 && spec.Args[0] == "auth" {
		raw, _ := json.Marshal(map[string]bool{"loggedIn": p.signed})
		proc.out <- OutputFrame{Stream: streamStdout, Data: raw}
		proc.finish(p.statusExit)
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
