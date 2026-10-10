// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/agenttoolsapi"
	"github.com/olivaresai/olivares/core/engine/enginetest"
)

func TestToolPullDownloadsHuggingFaceModelAndShowsProgress(t *testing.T) {
	const model = "hf.co/bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M"
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST " + agentToolsPath + "/ollama/pulls":
			var in struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Model != model {
				t.Errorf("pull did not preserve the HF reference: %+v %v", in, err)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "pull-1", "model": model, "state": "running", "status": "pulling manifest"})
		case "GET " + agentToolsPath + "/ollama/pulls/pull-1":
			count := polls.Add(1)
			state, completed := "running", 40
			if count > 1 {
				state, completed = "succeeded", 100
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "pull-1", "model": model, "state": state, "status": "pulling model", "completed": completed, "total": 100})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out, progress, err := execSessionCLI(t, nil, append([]string{"tool", "pull", model}, sessionCreds(server.URL)...)...)
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, progress)
	}
	if !strings.Contains(progress, "40%") || !strings.Contains(out, "Downloaded "+model+".") || polls.Load() != 2 {
		t.Fatalf("stdout=%q progress=%q polls=%d", out, progress, polls.Load())
	}
}

func TestToolPullReportsRefusalsAndDownloadFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
		code   int
	}{
		{"not running", 409, `{"error":{"code":"not_running","message":"Start Ollama first, then download a model."}}`, "Start Ollama first", exitcode.Conflict},
		{"forbidden", 403, `{"error":{"code":"forbidden","message":"A system administrator session is required."}}`, "A system administrator session is required", exitcode.Auth},
		{"invalid reference", 400, `{"error":{"code":"bad_request","message":"Name a model the way Ollama does."}}`, "Name a model", exitcode.Err},
		{"download failed", 202, `{"id":"p1","model":"qwen2.5:0.5b","state":"failed","error":"Ollama: model not found"}`, "qwen2.5:0.5b was not downloaded. Ollama: model not found", exitcode.Err},
		{"unknown state", 202, `{"id":"p1","state":"unexpected"}`, "unknown model download state", exitcode.Err},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != agentToolsPath+"/ollama/pulls" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			out, _, err := execSessionCLI(t, nil, append([]string{"tool", "pull", "qwen2.5:0.5b"}, sessionCreds(server.URL)...)...)
			if err == nil || exitcode.From(err) != tc.code || !strings.Contains(err.Error(), tc.want) || strings.Contains(out, "Downloaded") {
				t.Fatalf("stdout=%q err=%v code=%v", out, err, exitcode.From(err))
			}
		})
	}
}

func TestToolPullJSONAndPlainOllamaName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Model != "qwen2.5:0.5b" {
			t.Errorf("model = %+v %v", in, err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"id":"p1","model":"qwen2.5:0.5b","state":"succeeded","completed":100,"total":100}`))
	}))
	defer server.Close()
	out, _, err := execSessionCLI(t, nil, append([]string{"tool", "pull", "qwen2.5:0.5b", "-o", "json"}, sessionCreds(server.URL)...)...)
	var result map[string]any
	if err != nil || json.Unmarshal([]byte(out), &result) != nil || result["state"] != "succeeded" || result["model"] != "qwen2.5:0.5b" {
		t.Fatalf("JSON=%q err=%v", out, err)
	}
}

func TestToolPullPreservesTheEngineRefusalWhenDownloadDisappears(t *testing.T) {
	const refusal = "This download is no longer known. Start it again."
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "POST":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"expired-pull","model":"qwen2.5:0.5b","state":"running"}`))
		case "GET":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found", "message": refusal}})
		default:
			t.Errorf("unexpected request: %s", r.Method)
		}
	}))
	defer server.Close()
	out, _, err := execSessionCLI(t, nil, append([]string{"tool", "pull", "qwen2.5:0.5b"}, sessionCreds(server.URL)...)...)
	if err == nil || exitcode.From(err) != exitcode.NotFound || !strings.Contains(err.Error(), refusal) || strings.Contains(err.Error(), "Upgrade") || strings.Contains(out, "Downloaded") {
		t.Fatalf("stdout=%q err=%v", out, err)
	}
}

func TestToolPullCancellationStopsWaitingAndDoesNotCancelTheDownload(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "POST":
			posts.Add(1)
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"p1","model":"qwen2.5:0.5b","state":"running"}`))
		case "GET":
			cancel()
		case "DELETE":
			t.Error("the CLI must not claim to cancel a download without a cancel route")
		}
	}))
	defer server.Close()
	root := newRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"tool", "pull", "qwen2.5:0.5b"}, sessionCreds(server.URL)...))
	err := root.ExecuteContext(ctx)
	if err == nil || !strings.Contains(err.Error(), "The model download continues on the engine") || posts.Load() != 1 {
		t.Fatalf("err=%v POSTs=%d", err, posts.Load())
	}
}

// The CLI and the real engine use the same pull/audit path on SQLite and on the
// default PostgreSQL owner/app-role installation. Only native Ollama is a fixture.
func TestToolPullHuggingFaceOnSupportedStores(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	const model = "hf.co/bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M"
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dir, home, release := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
				t.Setenv(name, "")
			}
			cfg := bootConfig{DataDir: dir, Engine: "sqlite", Version: version, Logger: discardLogger(), ApplyModuleProfile: true}
			if backend == "postgres" {
				pg := enginetest.IsolatedPostgresSplitOwner(t)
				cfg.Engine, cfg.DSN, cfg.OwnerDSN = "postgres", pg.App, pg.Owner
			}
			eng, err := boot(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = eng.Close() })
			program := filepath.Join(release, "bin", "ollama")
			if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
				t.Fatal(err)
			}
			fixture := "#!" + python + "\n" + `import http.server, json, os, pathlib, time
models = pathlib.Path(os.environ['OLLAMA_MODELS']) / 'fixture-model'
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        if self.path == '/api/version': value = {'version': 'fixture'}
        elif self.path == '/api/tags': value = {'models': [{'name': models.read_text()}] if models.exists() else []}
        else: self.send_error(404); return
        self.send_response(200); self.end_headers(); self.wfile.write(json.dumps(value).encode())
    def do_POST(self):
        if self.path != '/api/pull': self.send_error(404); return
        model = json.loads(self.rfile.read(int(self.headers['Content-Length'])))['model']
        self.send_response(200); self.end_headers()
        self.wfile.write(b'{"status":"pulling GGUF","completed":40,"total":100}\n'); self.wfile.flush()
        time.sleep(.1)
        models.write_text(model)
        self.wfile.write(b'{"status":"success","completed":100,"total":100}\n')
host, port = os.environ['OLLAMA_HOST'].rsplit(':', 1)
http.server.HTTPServer((host, int(port)), Handler).serve_forever()
`
			if err := os.WriteFile(program, []byte(fixture), 0o700); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			_ = listener.Close()
			eng.agentTools.SetProgramResolver(func(driver string) string {
				if driver == "ollama" {
					return program
				}
				return ""
			})
			eng.agentTools.UseOllama(agenttoolsapi.OllamaConfig{Addr: addr,
				ModelsDir: filepath.Join(dir, "ollama", "models"), HomeDir: filepath.Join(dir, "ollama", "home"),
				Command: confinedOllamaCommand(dir), Register: registerLocalOllama(eng.sessionsMod, eng.store)})
			var token, tenant string
			call := func(method, path string, body any, want int) map[string]any {
				t.Helper()
				code, result, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, token, tenant, body)
				if code != want {
					t.Fatalf("%s %s = %d, want %d: %s", method, path, code, want, raw)
				}
				return result
			}
			setupToken, _, err := eng.setupTok.Ensure()
			if err != nil {
				t.Fatal(err)
			}
			setup := call("POST", "/v1/setup", map[string]any{"token": setupToken, "email": "hf@olivares.invalid", "password": "HF-fixture-password-2026!"}, 201)
			login := call("POST", "/v1/auth/login", map[string]any{"email": "hf@olivares.invalid", "password": "HF-fixture-password-2026!"}, 200)
			token = login["token"].(string)
			tenant = setup["organization"].(map[string]any)["tenant_id"].(string)
			caller := call("GET", "/v1/auth/whoami", nil, 200)
			actor, ok := caller["actor"].(string)
			if caller["kind"] != "user" || !ok || actor == "" {
				t.Fatalf("caller = %v", caller)
			}
			call("POST", agentToolsPath+"/ollama/start", map[string]string{"tenant_id": tenant}, 202)
			deadline := time.Now().Add(10 * time.Second)
			for call("GET", agentToolsPath+"/ollama", nil, 200)["state"] != "running" {
				if time.Now().After(deadline) {
					t.Fatal("the fixture Ollama did not start")
				}
				time.Sleep(50 * time.Millisecond)
			}
			server := httptest.NewServer(eng.api.Handler())
			defer server.Close()
			out, _, err := execSessionCLI(t, nil, "tool", "pull", model, "-o", "json", "--server", server.URL, "--token", token, "--tenant", tenant)
			var pull toolModelPull
			if err != nil || json.Unmarshal([]byte(out), &pull) != nil || pull.State != "succeeded" || pull.Model != model {
				t.Fatalf("CLI pull = %q, %v", out, err)
			}
			status := call("GET", agentToolsPath+"/ollama", nil, 200)
			models := status["models"].([]any)
			if len(models) != 1 || models[0] != model {
				t.Fatalf("model reference was not retained: %v", status)
			}
			ledger := call("GET", "/v1/audit/system", nil, 200)
			found := false
			for _, item := range ledger["items"].([]any) {
				event := item.(map[string]any)
				if event["action"] == "agenttools.ollama.pull" && event["target_id"] == pull.ID && event["actor_kind"] == "user" && event["actor"] == actor {
					found = true
				}
			}
			if !found {
				t.Fatal("the completed CLI download has no matching audit event")
			}
			call("POST", agentToolsPath+"/ollama/stop", nil, 200)
		})
	}
}
