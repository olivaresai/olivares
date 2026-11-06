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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestFakeOllamaProcess is not a test: it is the fake `ollama serve` the module starts
// in these tests (the test binary, through a one-line wrapper). It speaks the three
// endpoints the product uses, on OLLAMA_HOST, and keeps pulled models in OLLAMA_MODELS.
func TestFakeOllamaProcess(t *testing.T) {
	if os.Getenv("OLIVARES_FAKE_OLLAMA") != "serve" {
		t.Skip("only runs as the fake Ollama server")
	}
	dir := os.Getenv("OLLAMA_MODELS")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.35.0"}`))
	})
	// Test-only: what the module put in this server's environment.
	mux.HandleFunc("/fake/no-cloud", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(os.Getenv("OLLAMA_NO_CLOUD")))
	})
	// Test-only: end the fake process on request, so a test can drive a service that
	// stops on its own.
	mux.HandleFunc("/fake/exit", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		go func() { // let the response leave before the process goes away
			time.Sleep(50 * time.Millisecond)
			os.Exit(0)
		}()
	})
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := os.Stat(filepath.Join(dir, "..", "tags-failure")); err == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "tags unavailable"})
			return
		}
		entries, _ := os.ReadDir(dir)
		var models []map[string]any
		for _, e := range entries {
			name, _ := url.QueryUnescape(e.Name())
			info, _ := e.Info()
			models = append(models, map[string]any{"name": name, "size": info.Size()})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	})
	mux.HandleFunc("/api/delete", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Model == "busy:1" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "model is in use"})
			return
		}
		if r.Method != http.MethodDelete || os.Remove(filepath.Join(dir, url.QueryEscape(in.Model))) != nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "model '" + in.Model + "' not found"})
		}
	})
	mux.HandleFunc("/api/pull", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		enc := json.NewEncoder(w)
		_ = enc.Encode(map[string]any{"status": "pulling manifest"})
		w.(http.Flusher).Flush()
		switch in.Model {
		case "hang:1": // never finishes; notes that the client went away
			<-r.Context().Done()
			_ = os.WriteFile(filepath.Join(dir, "..", "client-went-away"), nil, 0o600)
			return
		case "dies:1": // the stream ends without success
			_ = enc.Encode(map[string]any{"status": "pulling 7c2f", "total": 100, "completed": 10})
			return
		case "broken:1": // Ollama's last line is not JSON: keep it for the error
			_ = enc.Encode(map[string]any{"status": "pulling 7c2f"})
			_, _ = w.Write([]byte("not-json\n"))
			return
		case "bad:1":
			_ = enc.Encode(map[string]any{"error": "pull model manifest: file does not exist"})
			return
		}
		for _, done := range []int{40, 100} {
			_ = enc.Encode(map[string]any{"status": "pulling 7c2f", "digest": "sha256:7c2f", "total": 100, "completed": done})
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
		if err := os.WriteFile(filepath.Join(dir, url.QueryEscape(in.Model)), make([]byte, 100), 0o600); err != nil {
			_ = enc.Encode(map[string]any{"error": "fixture model could not be stored"})
			return
		}
		_ = enc.Encode(map[string]any{"status": "success"})
	})
	_ = http.ListenAndServe(os.Getenv("OLLAMA_HOST"), mux)
	os.Exit(0)
}

type ollamaFixture struct {
	addr         string
	models       string
	bin          string
	cfg          OllamaConfig
	mu           sync.Mutex
	endpoint     string
	tenant       model.TenantID
	registers    int
	registerErr  error
	store        store.Store
	mod          *Module
	gate         chan struct{} // when set, Register waits for it to close
	entered      int
	containerCLI string
	containerLog string
}

func (f *ollamaFixture) registered() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.endpoint
}

func newOllamaServer(t *testing.T) (call func(string, string, any) (int, map[string]any), f *ollamaFixture) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f = &ollamaFixture{addr: l.Addr().String(), models: filepath.Join(t.TempDir(), "models")}
	_ = l.Close()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	call, _, f.store = newSignInServerWithStore(t, func(m *Module, bin string) {
		wrapper := "#!/bin/sh\n[ \"$1\" = serve ] || exit 2\nOLIVARES_FAKE_OLLAMA=serve exec " + self + " -test.run='^TestFakeOllamaProcess$'\n"
		if err := os.WriteFile(filepath.Join(bin, "ollama"), []byte(wrapper), 0o755); err != nil {
			t.Fatal(err)
		}
		f.mod, f.bin = m, bin
		f.cfg = OllamaConfig{
			Addr: f.addr, ModelsDir: f.models, HomeDir: filepath.Join(t.TempDir(), "home"),
			StateFile: filepath.Join(t.TempDir(), "started.json"),
			Register: func(_ context.Context, _ auth.Principal, tenant model.TenantID, endpoint string) error {
				f.mu.Lock()
				f.entered++
				gate := f.gate
				f.mu.Unlock()
				if gate != nil {
					<-gate
				}
				f.mu.Lock()
				f.endpoint, f.tenant = endpoint, tenant
				f.registers++
				err := f.registerErr
				f.mu.Unlock()
				return err
			},
		}
		m.UseOllama(f.cfg)
	})
	return call, f
}

func waitOllama(t *testing.T, call func(string, string, any) (int, map[string]any), want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		code, st := call("GET", "/v1/m/agenttools/ollama", nil)
		if code == 200 && st["state"] == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("Ollama never reached %q: %d %v", want, code, st)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func listening(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// HU-R17 (refresh 06): the console installed Ollama and the person started
// `ollama serve` by hand. Start runs it as the engine's own child, the endpoint is
// registered once it answers, and the engine's shutdown stops it.
func TestOllamaStartsAnswersIsRegisteredAndStopsWithTheEngine(t *testing.T) {
	call, f := newOllamaServer(t)
	if code, st := call("GET", "/v1/m/agenttools/ollama", nil); code != 200 || st["installed"] != true || st["state"] != "stopped" {
		t.Fatalf("before start = %d %v", code, st)
	}
	org := seedOrg(t, f.store, "Second", "second")
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]string{"tenant_id": org.String()}); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	st := waitOllama(t, call, "running")
	if st["endpoint"] != "http://"+f.addr {
		t.Fatalf("endpoint = %v, want http://%s", st["endpoint"], f.addr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.registered() == "" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if f.registered() != "http://"+f.addr {
		t.Fatalf("registered endpoint = %q", f.registered())
	}
	// Registered in the tenant the start named (the console's active tenant), only.
	f.mu.Lock()
	tenant := f.tenant
	f.mu.Unlock()
	if tenant.String() != org.String() {
		t.Fatalf("registered for tenant %q, want the named %v", tenant, org.String())
	}
	if _, err := os.Stat(f.models); err != nil {
		t.Fatalf("the models directory was not made: %v", err)
	}
	f.mod.Close() // the engine stops
	if listening(f.addr) {
		t.Fatal("Ollama outlived the engine")
	}
}

func TestOllamaDownloadsAModelWithItsProgress(t *testing.T) {
	call, _ := newOllamaServer(t)
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	code, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"})
	if code != 202 || p["state"] != "running" || p["model"] != "qwen2.5:0.5b" {
		t.Fatalf("pull = %d %v", code, p)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, p = call("GET", "/v1/m/agenttools/ollama/pulls/"+p["id"].(string), nil)
		if code == 200 && p["state"] == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the download never finished: %d %v", code, p)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if p["completed"] != float64(100) || p["total"] != float64(100) {
		t.Fatalf("progress = %v", p)
	}
	_, st := call("GET", "/v1/m/agenttools/ollama", nil)
	if models, _ := st["models"].([]any); len(models) != 1 || models[0] != "qwen2.5:0.5b" {
		t.Fatalf("models = %v", st["models"])
	}
}

// pullUntil polls one download until it leaves "running" (or the wanted state is read).
func pullUntil(t *testing.T, call func(string, string, any) (int, map[string]any), id, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, p := call("GET", "/v1/m/agenttools/ollama/pulls/"+id, nil)
		if code == 200 && p["state"] == want {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("the download never reached %q: %d %v", want, code, p)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The installed list carries what each model takes on disk, from Ollama's own tags.
func TestOllamaStatusReportsEachModelsSize(t *testing.T) {
	call, _ := newOllamaServer(t)
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	_, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"})
	pullUntil(t, call, p["id"].(string), "succeeded")
	_, st := call("GET", "/v1/m/agenttools/ollama", nil)
	sizes, _ := st["model_sizes"].(map[string]any)
	if len(sizes) != 1 || sizes["qwen2.5:0.5b"] != float64(100) {
		t.Fatalf("model_sizes = %v, want qwen2.5:0.5b=100 (status %v)", st["model_sizes"], st)
	}
}

// Cancel ends the download, tells Ollama (the connection closes, so it stops pulling),
// frees the slot for another download, and is safe to repeat.
func TestOllamaCancelsADownloadAndTellsOllama(t *testing.T) {
	call, f := newOllamaServer(t)
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	_, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "hang:1"})
	id := p["id"].(string)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, p = call("GET", "/v1/m/agenttools/ollama/pulls/"+id, nil)
		if p["status"] == "pulling manifest" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the download never started: %v", p)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The answer is the end state itself, not "running" until the worker notices.
	if code, p := call("DELETE", "/v1/m/agenttools/ollama/pulls/"+id, nil); code != 200 || p["state"] != "failed" || p["error"] != "Download cancelled." {
		t.Fatalf("cancel = %d %v, want failed with %q", code, p, "Download cancelled.")
	}
	p = pullUntil(t, call, id, "failed")
	if p["error"] != "Download cancelled." {
		t.Fatalf("cancelled download error = %v, want %q", p["error"], "Download cancelled.")
	}
	marker := filepath.Join(f.models, "..", "client-went-away")
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Ollama never saw the download end: cancel did not close its stream")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, again := call("DELETE", "/v1/m/agenttools/ollama/pulls/"+id, nil); code != 200 || again["error"] != "Download cancelled." {
		t.Fatalf("second cancel = %d %v, want the same cancelled download", code, again)
	}
	if code, _ := call("DELETE", "/v1/m/agenttools/ollama/pulls/no-such-download", nil); code != 404 {
		t.Fatalf("cancel of an unknown download = %d, want 404", code)
	}
	if code, q := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"}); code != 202 {
		t.Fatalf("a download after a cancel = %d %v", code, q)
	}
}

// Ollama has already downloaded the model when its success line arrives. Cancelling the
// remaining provider synchronization cannot turn that installed model into a failed download.
func TestOllamaCancelAfterDownloadSuccessDoesNotMarkItFailed(t *testing.T) {
	call, f := newOllamaServer(t)
	org := seedOrg(t, f.store, "Second", "second")
	call("POST", "/v1/m/agenttools/ollama/start", map[string]string{"tenant_id": org.String()})
	waitOllama(t, call, "running")

	gate := make(chan struct{})
	f.mu.Lock()
	f.gate = gate
	f.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	_, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"})
	id := p["id"].(string)
	deadline := time.Now().Add(20 * time.Second)
	for {
		f.mu.Lock()
		entered := f.entered
		f.mu.Unlock()
		if entered >= 2 { // the success line is in; registration has not returned
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the download never reached registration after its success line")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, cancelled := call("DELETE", "/v1/m/agenttools/ollama/pulls/"+id, nil); code != 200 || cancelled["state"] != "running" {
		t.Fatalf("cancel after Ollama finished = %d %v, want synchronization to finish", code, cancelled)
	}
	release()
	p = pullUntil(t, call, id, "succeeded")
	if p["error"] != nil {
		t.Fatalf("a completed download was marked failed: %v", p)
	}
}

// Cancelling a download that already finished changes nothing: it stays succeeded.
func TestOllamaCancelOfAFinishedDownloadKeepsItSucceeded(t *testing.T) {
	call, _ := newOllamaServer(t)
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	_, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"})
	id := p["id"].(string)
	pullUntil(t, call, id, "succeeded")
	if code, p := call("DELETE", "/v1/m/agenttools/ollama/pulls/"+id, nil); code != 200 || p["state"] != "succeeded" {
		t.Fatalf("cancel of a finished download = %d %v, want it to stay succeeded", code, p)
	}
}

// A failed download says what Ollama last said, never only a generic line.
func TestOllamaFailedDownloadKeepsOllamasLastLine(t *testing.T) {
	call, _ := newOllamaServer(t)
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	for model, want := range map[string]string{
		"bad:1":    "Ollama: pull model manifest: file does not exist",
		"dies:1":   "pulling 7c2f",
		"broken:1": "not-json",
	} {
		_, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": model})
		p = pullUntil(t, call, p["id"].(string), "failed")
		if got, _ := p["error"].(string); !strings.Contains(got, want) {
			t.Errorf("%s: error = %q, want it to carry %q", model, got, want)
		}
	}
}

// Remove deletes the model from the Ollama the engine runs (its own models directory,
// which a terminal's `ollama rm` would not reach), records who did it, and the list and
// sizes follow.
func TestOllamaRemovesAnInstalledModelAndRecordsIt(t *testing.T) {
	call, _ := newOllamaServer(t)
	_, caller := call("GET", "/v1/auth/whoami", nil)
	actor, _ := caller["actor"].(string)
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	_, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"})
	pullUntil(t, call, p["id"].(string), "succeeded")
	code, st := call("DELETE", "/v1/m/agenttools/ollama/models", map[string]string{"model": "qwen2.5:0.5b"})
	if models, _ := st["models"].([]any); code != 200 || len(models) != 0 || st["model_sizes"] != nil {
		t.Fatalf("remove = %d %v, want 200 and no model left", code, st)
	}
	_, st = call("GET", "/v1/m/agenttools/ollama", nil)
	if models, _ := st["models"].([]any); len(models) != 0 {
		t.Fatalf("the model is still listed after remove: %v", st)
	}
	code, ledger := call("GET", "/v1/audit/system?action=agenttools.ollama.model_remove", nil)
	items, _ := ledger["items"].([]any)
	if code != 200 || len(items) != 1 {
		t.Fatalf("audit = %d %v, want one model_remove event", code, ledger)
	}
	if ev, _ := items[0].(map[string]any); ev["actor"] != actor || ev["actor_kind"] != "user" {
		t.Fatalf("model_remove event = %v, want actor %q", ev, actor)
	}
}

func TestOllamaReadOnlyControlPlaneDoesNotRemoveModels(t *testing.T) {
	call, f := newOllamaServer(t)
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	_, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"})
	pullUntil(t, call, p["id"].(string), "succeeded")
	f.mod.readOnly = true
	code, st := call("DELETE", "/v1/m/agenttools/ollama/models", map[string]string{"model": "qwen2.5:0.5b"})
	if code != 409 || st["error"].(map[string]any)["code"] != "read_only" {
		t.Fatalf("read-only remove = %d %v, want read_only", code, st)
	}
	_, st = call("GET", "/v1/m/agenttools/ollama", nil)
	if models, _ := st["models"].([]any); len(models) != 1 {
		t.Fatalf("read-only remove deleted the model: %v", st)
	}
}

func TestOllamaRemoveKeepsOllamasFailureAndTheInstalledModel(t *testing.T) {
	call, _ := newOllamaServer(t)
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	_, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "busy:1"})
	pullUntil(t, call, p["id"].(string), "succeeded")
	code, st := call("DELETE", "/v1/m/agenttools/ollama/models", map[string]string{"model": "busy:1"})
	message, _ := st["error"].(map[string]any)["message"].(string)
	if code != 502 || !strings.Contains(message, "Ollama: model is in use") {
		t.Fatalf("failed remove = %d %v, want Ollama's own error", code, st)
	}
	_, st = call("GET", "/v1/m/agenttools/ollama", nil)
	if models, _ := st["models"].([]any); len(models) != 1 || models[0] != "busy:1" {
		t.Fatalf("failed remove lost the installed model: %v", st)
	}
}

func TestOllamaStatusSaysWhenTheModelListCannotBeRead(t *testing.T) {
	call, f := newOllamaServer(t)
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	if err := os.WriteFile(filepath.Join(f.models, "..", "tags-failure"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, st := call("GET", "/v1/m/agenttools/ollama", nil)
	message, _ := st["models_error"].(string)
	if code != 200 || !strings.Contains(message, "tags unavailable") {
		t.Fatalf("status with a failed tags read = %d %v, want models_error", code, st)
	}
	if models, _ := st["models"].([]any); len(models) != 0 {
		t.Fatalf("failed tags read returned models: %v", models)
	}
}

func TestOllamaRemoveRefusesWhatItCannotDo(t *testing.T) {
	call, _ := newOllamaServer(t)
	if code, _ := call("DELETE", "/v1/m/agenttools/ollama/models", map[string]string{"model": "qwen2.5:0.5b"}); code != 409 {
		t.Fatalf("remove while stopped = %d, want 409", code)
	}
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	for _, bad := range []string{"", "-rm", "a b", "../../x"} {
		if code, _ := call("DELETE", "/v1/m/agenttools/ollama/models", map[string]string{"model": bad}); code != 400 {
			t.Errorf("remove %q = %d, want 400", bad, code)
		}
	}
	code, st := call("DELETE", "/v1/m/agenttools/ollama/models", map[string]string{"model": "never-installed:1"})
	if code != 404 {
		t.Fatalf("remove of a model that is not installed = %d %v, want 404", code, st)
	}
}

// A download and removal change Ollama even when the tenant's provider record cannot
// follow. Report that partial result honestly instead of discarding the probe failure.
func TestOllamaReportsProviderSyncFailuresWithoutLosingModelFacts(t *testing.T) {
	call, f := newOllamaServer(t)
	org := seedOrg(t, f.store, "Second", "second")
	call("POST", "/v1/m/agenttools/ollama/start", map[string]string{"tenant_id": org.String()})
	waitOllama(t, call, "running")
	f.mu.Lock()
	f.registerErr = errors.New("provider probe unavailable")
	f.mu.Unlock()
	_, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"})
	p = pullUntil(t, call, p["id"].(string), "succeeded")
	syncError, _ := p["error"].(string)
	if !strings.Contains(syncError, "provider probe unavailable") {
		t.Fatalf("succeeded pull hid the provider sync failure: %v", p)
	}
	_, st := call("GET", "/v1/m/agenttools/ollama", nil)
	if models, _ := st["models"].([]any); len(models) != 1 {
		t.Fatalf("the model Ollama installed is missing: %v", st)
	}
	code, st := call("DELETE", "/v1/m/agenttools/ollama/models", map[string]string{"model": "qwen2.5:0.5b"})
	message, _ := st["error"].(map[string]any)["message"].(string)
	if code != 502 || !strings.Contains(message, "provider probe unavailable") {
		t.Fatalf("remove hid the provider sync failure: %d %v", code, st)
	}
	_, st = call("GET", "/v1/m/agenttools/ollama", nil)
	if models, _ := st["models"].([]any); len(models) != 0 {
		t.Fatalf("the model was not removed from Ollama: %v", st)
	}
}

func TestOllamaStopEndsTheServiceAndKeepsTheModels(t *testing.T) {
	call, f := newOllamaServer(t)
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	if code, st := call("POST", "/v1/m/agenttools/ollama/stop", nil); code != 200 || st["state"] != "stopped" {
		t.Fatalf("stop = %d %v", code, st)
	}
	if listening(f.addr) {
		t.Fatal("Ollama still answers after Stop")
	}
	if _, err := os.Stat(f.models); err != nil {
		t.Fatalf("the models directory went with the service: %v", err)
	}
}

func TestOllamaRefusesWhatItCannotDo(t *testing.T) {
	call, f := newOllamaServer(t)
	if code, _ := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"}); code != 409 {
		t.Fatalf("pull while stopped = %d, want 409", code)
	}
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	for _, bad := range []string{"", "-rm", "a b", "../../x", strings.Repeat("m", 200)} {
		if code, _ := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": bad}); code != 400 {
			t.Errorf("pull %q = %d, want 400", bad, code)
		}
	}
	f.mod.SetProgramResolver(func(string) string { return "" })
	call("POST", "/v1/m/agenttools/ollama/stop", nil)
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", nil); code != 409 || !strings.Contains(st["error"].(map[string]any)["message"].(string), "Install Ollama") {
		t.Fatalf("start without Ollama = %d %v", code, st)
	}
}

// Root on first-hour check: a start that names no tenant registers the endpoint nowhere, and the
// row says where it answers so a tenant adds it in Providers.
func TestOllamaStartedWithoutATenantRegistersNothing(t *testing.T) {
	call, f := newOllamaServer(t)
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]string{"tenant_id": "not-a-tenant"}); code != 400 {
		t.Fatalf("a malformed tenant id = %d %v, want 400", code, st)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", nil); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		code, st := call("GET", "/v1/m/agenttools/ollama", nil)
		if msg, _ := st["message"].(string); code == 200 && st["state"] == "running" && strings.Contains(msg, "in Providers") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no Providers sentence on the row: %d %v", code, st)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if f.registered() != "" {
		t.Fatalf("registered %q with no tenant named", f.registered())
	}
}

// first-hour check: an OpenCode session on the local Ollama needs the record's model list, and
// the record is registered before any model is downloaded. A download that succeeds
// registers again, for the tenant that started the service, so its list is read.
func TestOllamaDownloadRegistersAgainForTheStartingTenant(t *testing.T) {
	call, f := newOllamaServer(t)
	org := seedOrg(t, f.store, "Second", "second")
	call("POST", "/v1/m/agenttools/ollama/start", map[string]string{"tenant_id": org.String()})
	waitOllama(t, call, "running")
	registers := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.registers
	}
	deadline := time.Now().Add(5 * time.Second)
	for registers() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if registers() != 1 {
		t.Fatalf("registered %d times at start, want 1", registers())
	}
	code, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"})
	if code != 202 {
		t.Fatalf("pull = %d %v", code, p)
	}
	deadline = time.Now().Add(10 * time.Second)
	for registers() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	f.mu.Lock()
	n, tenant, endpoint := f.registers, f.tenant, f.endpoint
	f.mu.Unlock()
	if n != 2 || tenant.String() != org.String() || endpoint != "http://"+f.addr {
		t.Fatalf("after the download: %d registrations, tenant %q, endpoint %q", n, tenant, endpoint)
	}
}

// The product-started Ollama runs with its cloud features off (OLLAMA_NO_CLOUD): real use saw it
// reach ollama.com on start, which no local session needs.
func TestOllamaStartsWithItsCloudFeaturesOff(t *testing.T) {
	call, f := newOllamaServer(t)
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", nil); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	waitOllama(t, call, "running")
	resp, err := http.Get("http://" + f.addr + "/fake/no-cloud")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "1" {
		t.Fatalf("OLLAMA_NO_CLOUD = %q in the started Ollama, want 1", got)
	}
}

// A console rereads Providers when Ollama reports running or a download succeeded, and
// gets no later signal. Both states are reported only once the endpoint is registered
// for the tenant, so that reread finds it (and finds the new model).
func TestOllamaReportsRunningAndADoneDownloadOnlyOnceItsEndpointIsRegistered(t *testing.T) {
	call, f := newOllamaServer(t)
	hold := func() func() {
		gate := make(chan struct{})
		f.mu.Lock()
		f.gate = gate
		f.mu.Unlock()
		var once sync.Once
		release := func() { once.Do(func() { close(gate) }) }
		t.Cleanup(release) // a failed check must not leave Register waiting at shutdown
		return release
	}
	waitEntered := func(n int) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			f.mu.Lock()
			entered := f.entered
			f.mu.Unlock()
			if entered >= n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("Register was entered %d times, want %d", entered, n)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	org := seedOrg(t, f.store, "Second", "second")
	release := hold()
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]string{"tenant_id": org.String()}); code != 202 {
		t.Fatalf("start = %d %v", code, st)
	}
	waitEntered(1)
	if _, st := call("GET", "/v1/m/agenttools/ollama", nil); st["state"] != "starting" {
		t.Fatalf("while its endpoint is being registered Ollama reports %v, want starting", st["state"])
	}
	release()
	waitOllama(t, call, "running")
	if f.registered() != "http://"+f.addr {
		t.Fatalf("running was reported before the endpoint was registered (%q)", f.registered())
	}
	release = hold()
	code, p := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": "qwen2.5:0.5b"})
	if code != 202 {
		t.Fatalf("pull = %d %v", code, p)
	}
	waitEntered(2)
	if _, got := call("GET", "/v1/m/agenttools/ollama/pulls/"+p["id"].(string), nil); got["state"] == "succeeded" {
		t.Fatalf("the download was reported succeeded before the record listed the model: %v", got)
	}
	release()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, got := call("GET", "/v1/m/agenttools/ollama/pulls/"+p["id"].(string), nil); got["state"] == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the download never finished")
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.mu.Lock()
	registers := f.registers
	f.mu.Unlock()
	if registers != 2 {
		t.Fatalf("Register completed %d times, want 2 (start and download)", registers)
	}
}
