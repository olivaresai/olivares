// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
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
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		entries, _ := os.ReadDir(dir)
		var models []map[string]string
		for _, e := range entries {
			models = append(models, map[string]string{"name": strings.ReplaceAll(e.Name(), "__", ":")})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	})
	mux.HandleFunc("/api/pull", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		enc := json.NewEncoder(w)
		_ = enc.Encode(map[string]any{"status": "pulling manifest"})
		for _, done := range []int{40, 100} {
			_ = enc.Encode(map[string]any{"status": "pulling 7c2f", "digest": "sha256:7c2f", "total": 100, "completed": done})
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
		_ = os.WriteFile(filepath.Join(dir, strings.ReplaceAll(in.Model, ":", "__")), nil, 0o600)
		_ = enc.Encode(map[string]any{"status": "success"})
	})
	_ = http.ListenAndServe(os.Getenv("OLLAMA_HOST"), mux)
	os.Exit(0)
}

type ollamaFixture struct {
	addr      string
	models    string
	bin       string
	cfg       OllamaConfig
	mu        sync.Mutex
	endpoint  string
	tenant    model.TenantID
	registers int
	mod       *Module
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
	call, _ = newSignInServer(t, func(m *Module, bin string) {
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
				f.endpoint, f.tenant = endpoint, tenant
				f.registers++
				f.mu.Unlock()
				return nil
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
	code, org := call("POST", "/v1/system/orgs", map[string]string{"name": "Second", "slug": "second"})
	if code != 201 {
		t.Fatalf("org %d %v", code, org)
	}
	if code, st := call("POST", "/v1/m/agenttools/ollama/start", map[string]string{"tenant_id": org["tenant_id"].(string)}); code != 202 {
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
	if tenant.String() != org["tenant_id"] {
		t.Fatalf("registered for tenant %q, want the named %v", tenant, org["tenant_id"])
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

// Root on FH 033: a start that names no tenant registers the endpoint nowhere, and the
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

// FH 087: an OpenCode session on the local Ollama needs the record's model list, and
// the record is registered before any model is downloaded. A download that succeeds
// registers again, for the tenant that started the service, so its list is read.
func TestOllamaDownloadRegistersAgainForTheStartingTenant(t *testing.T) {
	call, f := newOllamaServer(t)
	code, org := call("POST", "/v1/system/orgs", map[string]string{"name": "Second", "slug": "second"})
	if code != 201 {
		t.Fatalf("org %d %v", code, org)
	}
	call("POST", "/v1/m/agenttools/ollama/start", map[string]string{"tenant_id": org["tenant_id"].(string)})
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
	if n != 2 || tenant.String() != org["tenant_id"] || endpoint != "http://"+f.addr {
		t.Fatalf("after the download: %d registrations, tenant %q, endpoint %q", n, tenant, endpoint)
	}
}

// The product-started Ollama runs with its cloud features off (OLLAMA_NO_CLOUD): HU2 saw it
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
