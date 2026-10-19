// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestFakeModelServerProcess is not a test: it is the fake `llama-server` / `vllm serve` the
// module starts in these tests (the test binary, through a one-line wrapper). It answers
// /health, serves /v1/models only to the bearer key in its environment, and echoes what the
// module gave it so a test can see the arguments and the environment.
func TestFakeModelServerProcess(t *testing.T) {
	if os.Getenv("OLIVARES_FAKE_MODEL_SERVER") != "serve" {
		t.Skip("only runs as the fake model server")
	}
	if os.Getenv("OLIVARES_FAKE_EXIT") != "" {
		line := "error: the fixture model could not be loaded"
		if os.Getenv("OLIVARES_FAKE_EXIT_KEY") != "" {
			line += " (key " + os.Getenv("LLAMA_API_KEY") + ")" // a server that echoes its key
		}
		_, _ = os.Stderr.WriteString(line + "\n")
		os.Exit(3)
	}
	args := strings.Fields(os.Getenv("OLIVARES_FAKE_ARGS"))
	var host, port string
	for i, a := range args {
		switch {
		case a == "--host" && i+1 < len(args):
			host = args[i+1]
		case a == "--port" && i+1 < len(args):
			port = args[i+1]
		}
	}
	key := os.Getenv("LLAMA_API_KEY") + os.Getenv("VLLM_API_KEY")
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if os.Getenv("OLIVARES_FAKE_NO_HEALTH") != "" {
			http.Error(w, `{"status":"loading"}`, http.StatusServiceUnavailable) // never finishes loading
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if key == "" || r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "fixture-model"}}})
	})
	// A completion with an empty body: refused without the key, a complaint about the body with
	// it. OLIVARES_FAKE_OPEN is a build that ignores the key variable and serves anyone.
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("OLIVARES_FAKE_OPEN") == "" && (key == "" || r.Header.Get("Authorization") != "Bearer "+key) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		http.Error(w, `{"error":"empty request"}`, http.StatusBadRequest)
	})
	// Test-only: what the module put in this server's arguments and environment.
	mux.HandleFunc("/fake/args", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(os.Getenv("OLIVARES_FAKE_ARGS"))) })
	mux.HandleFunc("/fake/key", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(key)) })
	_ = http.ListenAndServe(net.JoinHostPort(host, port), mux)
	os.Exit(0)
}

type modelServerRegistration struct{ name, endpoint, key string }

type modelServerFixture struct {
	call      func(string, string, any) (int, map[string]any)
	mod       *Module
	bin       string
	dir       string
	modelFile string
	cfg       ModelServerConfig
	store     store.Store
	mu        sync.Mutex
	tenant    model.TenantID
	registers []modelServerRegistration
	failWith  error // Register answers this, when set
}

func (f *modelServerFixture) last() (modelServerRegistration, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.registers) == 0 {
		return modelServerRegistration{}, 0
	}
	return f.registers[len(f.registers)-1], len(f.registers)
}

// freeAddr is a loopback address nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func newModelServerFixture(t *testing.T, addrs map[string]string) *modelServerFixture {
	t.Helper()
	return newModelServerFixtureWith(t, addrs, "")
}

// newModelServerFixtureWith is the fixture whose fake server wrapper first runs the shell lines
// in setup (`export NAME=value` for the fake, or a marker the test reads): the module gives its
// child an explicit environment, so a test cannot set the fake's through its own.
func newModelServerFixtureWith(t *testing.T, addrs map[string]string, setup string) *modelServerFixture {
	t.Helper()
	f := &modelServerFixture{dir: t.TempDir(), modelFile: filepath.Join(t.TempDir(), "fixture-model.gguf")}
	if err := os.WriteFile(f.modelFile, []byte("GGUF"), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f.call, _, f.store = newSignInServerWithStore(t, func(m *Module, bin string) {
		f.mod, f.bin = m, bin
		wrapper := "#!/bin/sh\n" + setup + "\nOLIVARES_FAKE_ARGS=\"$*\" OLIVARES_FAKE_MODEL_SERVER=serve exec " + self + " -test.run='^TestFakeModelServerProcess$'\n"
		for _, name := range []string{"llama-server", "vllm"} {
			if err := os.WriteFile(filepath.Join(bin, name), []byte(wrapper), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		// llama-server and vllm are found on the engine's PATH, like any program the operator
		// installed with their package manager.
		m.lookPath = func(name string) (string, error) {
			p := filepath.Join(bin, name)
			_, err := os.Stat(p)
			return p, err
		}
		f.cfg = ModelServerConfig{
			Dir:   f.dir,
			Addrs: addrs,
			Register: func(_ context.Context, _ auth.Principal, tenant model.TenantID, name, endpoint, key string) error {
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.failWith != nil {
					return f.failWith
				}
				f.tenant = tenant
				f.registers = append(f.registers, modelServerRegistration{name, endpoint, key})
				return nil
			},
		}
		m.UseModelServers(f.cfg)
	})
	return f
}

// nextEngine is the engine after a restart: a new module over the same directory and
// programs, started the way boot starts it, auditing into st's system chain.
func (f *modelServerFixture) nextEngine(t *testing.T, audit func(context.Context, model.AuditDraft) error) *Module {
	t.Helper()
	return f.nextEngineLooking(t, audit, f.mod.lookPath)
}

func (f *modelServerFixture) nextEngineLooking(t *testing.T, audit func(context.Context, model.AuditDraft) error, look func(string) (string, error)) *Module {
	t.Helper()
	m, err := New(context.Background(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	m.lookPath = look
	cfg := f.cfg
	cfg.Audit = audit
	m.UseModelServers(cfg)
	m.RestartModelServers()
	return m
}

// body is a start request for a business tenant (any organization id parses; Register
// records whichever it is given).
func body(extra map[string]string) map[string]string {
	out := map[string]string{"tenant_id": model.NewID().String()}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func waitModelServer(t *testing.T, call func(string, string, any) (int, map[string]any), kind, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		code, out := call("GET", "/v1/m/agenttools/model-servers", nil)
		if code == 200 {
			servers, _ := out["servers"].([]any)
			for _, s := range servers {
				row, _ := s.(map[string]any)
				if row["kind"] == kind && row["state"] == want {
					return row
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never reached %q: %d %v", kind, want, code, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func fetch(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// apiError is the {code, message} of a refusal, which the API wraps in {"error": ...}.
func apiError(out map[string]any) (code, message string) {
	e, _ := out["error"].(map[string]any)
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	return code, message
}

func portOf(addr string) string { return addr[strings.LastIndex(addr, ":")+1:] }

// A LOCAL OPENAI-COMPATIBLE SERVER IS A SERVICE OF THE ENGINE (dev#2422). `llama-server` and
// `vllm serve` were started by hand; Start runs the installed one as the engine's own child on
// loopback, with a key the engine made for it, and registers it in Providers so OpenCode can
// be bound to it.

func TestModelServerStartsAnswersIsRegisteredWithItsOwnKeyAndStops(t *testing.T) {
	addr := freeAddr(t)
	f := newModelServerFixture(t, map[string]string{"llama-cpp": addr})
	code, st := f.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/start", body(map[string]string{"model": f.modelFile}))
	if code != 202 {
		t.Fatalf("start = %d %v, want 202", code, st)
	}
	row := waitModelServer(t, f.call, "llama-cpp", "running")
	if row["endpoint"] != "http://"+addr+"/v1" || row["model"] != f.modelFile {
		t.Fatalf("running row = %v, want the loopback endpoint (with /v1, the address the record holds) and the model", row)
	}
	reg, n := f.last()
	if n != 1 || reg.endpoint != "http://"+addr+"/v1" || reg.name != "llama.cpp on this server" || len(reg.key) < 32 {
		t.Fatalf("registered %+v (%d times), want one record at the endpoint with a generated key", reg, n)
	}
	// The server holds the very key that was registered, and that key is not in its arguments.
	if got := fetch(t, "http://"+addr+"/fake/key"); got != reg.key {
		t.Fatal("the server does not hold the registered key (LLAMA_API_KEY)")
	}
	args := fetch(t, "http://"+addr+"/fake/args")
	if strings.Contains(args, reg.key) {
		t.Fatal("the key is in the server's arguments")
	}
	for _, want := range []string{"-m " + f.modelFile, "--host 127.0.0.1", "--port " + portOf(addr), "--alias fixture-model"} {
		if !strings.Contains(args, want) {
			t.Errorf("arguments %q lack %q", args, want)
		}
	}
	if models, _ := row["models"].([]any); len(models) != 1 || models[0] != "fixture-model" {
		t.Fatalf("models = %v, want what the server lists with its key", row["models"])
	}
	// Stop ends the service; the model file stays.
	if code, _ := f.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/stop", nil); code != 200 {
		t.Fatalf("stop = %d", code)
	}
	waitModelServer(t, f.call, "llama-cpp", "stopped")
	if listening(addr) {
		t.Fatal("the server still listens after Stop")
	}
	if _, err := os.Stat(f.modelFile); err != nil {
		t.Fatal("Stop removed the model")
	}
}

// With no port pinned a start never fails on a port something else holds: the OS picks a free
// loopback one, the server is given that exact address, and the endpoint the row shows and the
// record gets is that address.
func TestModelServerPicksAFreeLoopbackPortWhenNoneIsPinned(t *testing.T) {
	f := newModelServerFixture(t, nil)
	if code, st := f.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/start", body(map[string]string{"model": f.modelFile})); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	row := waitModelServer(t, f.call, "llama-cpp", "running")
	endpoint, _ := row["endpoint"].(string)
	host, port, err := net.SplitHostPort(strings.TrimSuffix(strings.TrimPrefix(endpoint, "http://"), "/v1"))
	if err != nil || host != "127.0.0.1" || port == "0" || port == "" {
		t.Fatalf("endpoint %q, want a concrete loopback port", endpoint)
	}
	if reg, _ := f.last(); reg.endpoint != endpoint {
		t.Fatalf("registered %q, shown %q, want the same address", reg.endpoint, endpoint)
	}
	if !listening(host + ":" + port) {
		t.Fatal("nothing listens at the endpoint the row shows")
	}
}

func TestModelServerGetsANewKeyOnEveryStart(t *testing.T) {
	addr := freeAddr(t)
	f := newModelServerFixture(t, map[string]string{"llama-cpp": addr})
	req := body(map[string]string{"model": f.modelFile})
	var keys []string
	for i := 0; i < 2; i++ {
		if code, st := f.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/start", req); code != 202 {
			t.Fatalf("start %d = %d %v", i, code, st)
		}
		waitModelServer(t, f.call, "llama-cpp", "running")
		reg, _ := f.last()
		keys = append(keys, reg.key)
		f.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/stop", nil)
		waitModelServer(t, f.call, "llama-cpp", "stopped")
	}
	if keys[0] == keys[1] {
		t.Fatal("a restarted server kept the previous key")
	}
}

func TestModelServerRunsVLLMWithItsServeCommand(t *testing.T) {
	addr := freeAddr(t)
	f := newModelServerFixture(t, map[string]string{"vllm": addr})
	if code, st := f.call("POST", "/v1/m/agenttools/model-servers/vllm/start", body(map[string]string{"model": "Qwen/Qwen3-0.6B"})); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	waitModelServer(t, f.call, "vllm", "running")
	args := fetch(t, "http://"+addr+"/fake/args")
	if !strings.HasPrefix(args, "serve Qwen/Qwen3-0.6B ") || !strings.Contains(args, "--host 127.0.0.1") {
		t.Fatalf("arguments %q, want `serve <model> --host 127.0.0.1 --port N`", args)
	}
	reg, _ := f.last()
	if got := fetch(t, "http://"+addr+"/fake/key"); got != reg.key || reg.name != "vLLM on this server" {
		t.Fatalf("vLLM key/name = %q / %q, want the registered key (VLLM_API_KEY) and name", got, reg.name)
	}
}

func TestModelServerRefusesWhatItCannotDo(t *testing.T) {
	f := newModelServerFixture(t, map[string]string{"llama-cpp": freeAddr(t), "vllm": freeAddr(t)})
	notGGUF := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(notGGUF, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		kind string
		req  map[string]string
		want int
	}{
		"unknown server": {"ollama", body(map[string]string{"model": "x/y"}), 404},
		"no model":       {"llama-cpp", body(nil), 400},
		"no tenant":      {"llama-cpp", map[string]string{"model": f.modelFile}, 400},
		"relative path":  {"llama-cpp", body(map[string]string{"model": "models/a.gguf"}), 400},
		"missing file":   {"llama-cpp", body(map[string]string{"model": "/nonexistent/a.gguf"}), 400},
		"not a gguf":     {"llama-cpp", body(map[string]string{"model": notGGUF}), 400},
		// Resolves to the real model file, so only the clean-path rule can refuse it.
		"traversal":             {"llama-cpp", body(map[string]string{"model": filepath.Dir(f.modelFile) + "/../" + filepath.Base(filepath.Dir(f.modelFile)) + "/fixture-model.gguf"}), 400},
		"option as a model":     {"vllm", body(map[string]string{"model": "--api-key=x"}), 400},
		"control character":     {"vllm", body(map[string]string{"model": "a/b\nc"}), 400},
		"vllm needs a repo/dir": {"vllm", body(map[string]string{"model": "justaword"}), 400},
	} {
		t.Run(name, func(t *testing.T) {
			if code, out := f.call("POST", "/v1/m/agenttools/model-servers/"+tc.kind+"/start", tc.req); code != tc.want {
				t.Fatalf("start = %d %v, want %d", code, out, tc.want)
			}
		})
	}
	// Nothing started, nothing registered.
	if _, n := f.last(); n != 0 {
		t.Fatalf("a refused start registered %d records", n)
	}
	// A program that is not installed is said, with the step.
	f.mod.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	code, out := f.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/start", body(map[string]string{"model": f.modelFile}))
	if kind, msg := apiError(out); code != 409 || kind != "tool_not_installed" || !strings.Contains(msg, "llama-server") {
		t.Fatalf("not installed = %d %v, want 409 tool_not_installed naming llama-server", code, out)
	}
}

func TestModelServerRefusesAnAddressSomethingElseHolds(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	f := newModelServerFixture(t, map[string]string{"llama-cpp": l.Addr().String()})
	code, out := f.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/start", body(map[string]string{"model": f.modelFile}))
	if _, msg := apiError(out); code != 409 || !strings.Contains(msg, "already listens") {
		t.Fatalf("start = %d %v, want 409 naming the busy address", code, out)
	}
}

func TestModelServerRefusesAPublicAddress(t *testing.T) {
	f := newModelServerFixture(t, map[string]string{"llama-cpp": "0.0.0.0:18080"})
	code, out := f.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/start", body(map[string]string{"model": f.modelFile}))
	if kind, _ := apiError(out); code != 409 || kind != "not_started" {
		t.Fatalf("start on 0.0.0.0 = %d %v, want 409 not_started: the server is reached on loopback only", code, out)
	}
	if _, n := f.last(); n != 0 || listening("127.0.0.1:18080") {
		t.Fatalf("a refused public bind registered %d records / started a listener", n)
	}
}

func TestModelServerSaysWhyItStoppedOnItsOwn(t *testing.T) {
	f := newModelServerFixtureWith(t, map[string]string{"llama-cpp": freeAddr(t)}, "export OLIVARES_FAKE_EXIT=1")
	if code, out := f.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/start", body(map[string]string{"model": f.modelFile})); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	row := waitModelServer(t, f.call, "llama-cpp", "failed")
	// A server that crashed on its own is not one that was slow: the row says which.
	if msg, _ := row["message"].(string); !strings.Contains(msg, "stopped on its own") || strings.Contains(msg, "did not answer") ||
		!strings.Contains(msg, "the fixture model could not be loaded") {
		t.Fatalf("message = %q, want 'stopped on its own' and the server's last line", msg)
	}
	if _, n := f.last(); n != 0 {
		t.Fatal("a server that never answered was registered")
	}
}

func TestModelServerChildIsConfinedToItsDirectoriesAndTheModel(t *testing.T) {
	addr := freeAddr(t)
	var mu sync.Mutex
	var rw, ro []string
	f := newModelServerFixture(t, map[string]string{"llama-cpp": addr})
	f.mod.SetChildCommand(func(ctx context.Context, w, r []string, program string, args ...string) (*exec.Cmd, error) {
		mu.Lock()
		rw, ro = w, r
		mu.Unlock()
		return toolCommand(ctx, program, args...), nil
	})
	if code, out := f.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/start", body(map[string]string{"model": f.modelFile})); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	waitModelServer(t, f.call, "llama-cpp", "running")
	mu.Lock()
	defer mu.Unlock()
	if !containsDir(rw, filepath.Join(f.dir, "llama-cpp")) || containsDir(rw, filepath.Dir(f.modelFile)) {
		t.Fatalf("writable = %v, want the server's own directory and never the model's", rw)
	}
	if !containsDir(ro, filepath.Dir(f.modelFile)) {
		t.Fatalf("readable = %v, want the model's directory", ro)
	}
}

// After an engine restart the server a person had started stays "stopped" until someone
// presses Start, and the sessions bound to it fail meanwhile. The start is kept outside the
// server's directories (which the child writes); the next engine starts it again for the
// same tenant and model, with a new key, and writes one system audit row before it does.
// Stop forgets it. With no audit written, nothing is started.
func TestModelServerComesBackWithTheEngineUntilStopped(t *testing.T) {
	addr := freeAddr(t)
	f := newModelServerFixture(t, map[string]string{"llama-cpp": addr})
	req := body(map[string]string{"model": f.modelFile})
	if code, out := f.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/start", req); code != 202 {
		t.Fatalf("start = %d %v", code, out)
	}
	waitModelServer(t, f.call, "llama-cpp", "running")
	first, _ := f.last()
	f.mu.Lock()
	firstTenant := f.tenant
	f.mu.Unlock()
	f.mod.Close() // the engine stops
	if listening(addr) {
		t.Fatal("the server outlived the engine")
	}
	state := filepath.Join(f.dir, "llama-cpp.json")
	if rel, err := filepath.Rel(filepath.Join(f.dir, "llama-cpp"), state); err == nil && filepath.IsLocal(rel) {
		t.Fatalf("the state file %s is inside the directory the child writes", state)
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("the person's start was not kept: %v", err)
	}

	var drafts []model.AuditDraft
	next := f.nextEngine(t, func(_ context.Context, d model.AuditDraft) error { drafts = append(drafts, d); return nil })
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, n := f.last(); listening(addr) && n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			_, n := f.last()
			t.Fatalf("after the restart the server listens=%v, registered %d times, want it back and registered again", listening(addr), n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if again, _ := f.last(); again.key == first.key || again.endpoint != first.endpoint {
		t.Fatalf("restart registered %+v after %+v, want the same endpoint with a new key", again, first)
	}
	// the same organization and the same model, not just a server
	f.mu.Lock()
	againTenant := f.tenant
	f.mu.Unlock()
	if againTenant != firstTenant || firstTenant.IsZero() {
		t.Fatalf("restart registered in organization %v, want the one that started it (%v)", againTenant, firstTenant)
	}
	if args := fetch(t, "http://"+addr+"/fake/args"); !strings.Contains(args, f.modelFile) {
		t.Fatalf("the restarted server runs %q, want the model %s", args, f.modelFile)
	}
	if len(drafts) != 1 || drafts[0].Action != "agenttools.model_server.restart" || drafts[0].Meta["kind"] != "llama-cpp" ||
		!strings.HasPrefix(drafts[0].Meta["reason"].(string), "restarted with the engine; started by ") {
		t.Fatalf("restart audit = %+v, want one row naming the server and who started it", drafts)
	}
	for _, v := range drafts[0].Meta {
		if s, _ := v.(string); strings.Contains(s, first.key) {
			t.Fatal("a key reached the audit row")
		}
	}
	next.Close()

	// Stop is the person's choice: the next engine leaves it stopped.
	addr2 := freeAddr(t)
	f2 := newModelServerFixture(t, map[string]string{"llama-cpp": addr2})
	f2.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/start", body(map[string]string{"model": f2.modelFile}))
	waitModelServer(t, f2.call, "llama-cpp", "running")
	f2.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/stop", nil)
	f2.mod.Close()
	if _, err := os.Stat(filepath.Join(f2.dir, "llama-cpp.json")); !os.IsNotExist(err) {
		t.Fatalf("Stop left the start kept: %v", err)
	}
	var again []model.AuditDraft
	after := f2.nextEngine(t, func(_ context.Context, d model.AuditDraft) error { again = append(again, d); return nil })
	// RestartModelServers is synchronous: nothing was started, so there is nothing to wait for.
	if st := after.modelServerStatus(context.Background(), "llama-cpp"); st.State != "stopped" || len(again) != 0 || listening(addr2) {
		t.Fatalf("a server the person stopped came back with the engine: %+v, %d audit rows, listening=%v", st, len(again), listening(addr2))
	}
}

func TestModelServerWithNoAuditRowIsNotStartedByTheEngine(t *testing.T) {
	addr := freeAddr(t)
	f := newModelServerFixture(t, map[string]string{"llama-cpp": addr})
	f.call("POST", "/v1/m/agenttools/model-servers/llama-cpp/start", body(map[string]string{"model": f.modelFile}))
	waitModelServer(t, f.call, "llama-cpp", "running")
	f.mod.Close()
	next := f.nextEngine(t, func(context.Context, model.AuditDraft) error { return errors.New("audit store unavailable") })
	got := next.modelServerStatus(context.Background(), "llama-cpp")
	if listening(addr) || got.State != "failed" || !strings.Contains(got.Message, "audit record could not be written") {
		t.Fatalf("with no audit the restart gave %+v (listening=%v), want no start and the reason", got, listening(addr))
	}
}

func containsDir(list []string, under string) bool {
	for _, p := range list {
		if p == under || strings.HasPrefix(p, under+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
