// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

const huggingFaceFixtureModel = "hf.co/bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M"

func TestOllamaPullsHuggingFaceGGUFWithAudit(t *testing.T) {
	call, _ := newOllamaServer(t)
	code, caller := call("GET", "/v1/auth/whoami", nil)
	actor, ok := caller["actor"].(string)
	if code != 200 || caller["kind"] != "user" || !ok || actor == "" {
		t.Fatalf("caller = %d %v", code, caller)
	}
	call("POST", "/v1/m/agenttools/ollama/start", nil)
	waitOllama(t, call, "running")
	code, pull := call("POST", "/v1/m/agenttools/ollama/pulls", map[string]string{"model": huggingFaceFixtureModel})
	if code != 202 || pull["model"] != huggingFaceFixtureModel {
		t.Fatalf("pull = %d %v", code, pull)
	}
	id := pull["id"].(string)
	deadline := time.Now().Add(10 * time.Second)
	for pull["state"] == "running" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		code, pull = call("GET", "/v1/m/agenttools/ollama/pulls/"+id, nil)
		if code != 200 {
			t.Fatalf("progress = %d %v", code, pull)
		}
	}
	if pull["state"] != "succeeded" || pull["completed"] != float64(100) {
		t.Fatalf("not downloaded: %v", pull)
	}
	_, status := call("GET", "/v1/m/agenttools/ollama", nil)
	models, _ := status["models"].([]any)
	if len(models) != 1 || models[0] != huggingFaceFixtureModel {
		t.Fatalf("Ollama did not retain the HF reference: %v", status)
	}
	code, ledger := call("GET", "/v1/audit/system", nil)
	if code != 200 {
		t.Fatalf("audit = %d %v", code, ledger)
	}
	// The public audit DTO omits metadata; correlate the download by its ID.
	items, _ := ledger["items"].([]any)
	for _, item := range items {
		event, _ := item.(map[string]any)
		if event["action"] == "agenttools.ollama.pull" && event["target_id"] == id && event["actor_kind"] == "user" && event["actor"] == actor {
			return
		}
	}
	t.Fatalf("missing audit event for model download %s: %v", id, ledger)
}

func TestOllamaHuggingFacePullRequiresAuditBeforeDownload(t *testing.T) {
	var requests atomic.Int32
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/pull" {
			t.Errorf("unexpected Ollama request: %s %s", r.Method, r.URL.Path)
		}
		requests.Add(1)
		_, _ = w.Write([]byte("{\"status\":\"success\"}\n"))
	}))
	defer ollama.Close()
	m := &Module{ctx: t.Context(), ollama: ollamaService{
		cfg: OllamaConfig{Addr: strings.TrimPrefix(ollama.URL, "http://")}, state: ollamaRunning,
	}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/ollama/pulls", strings.NewReader(`{"model":"`+huggingFaceFixtureModel+`"}`))
	m.handleOllamaPull(w, r, api.ModuleContext{Principal: auth.Principal{Kind: auth.KindUser, Superadmin: true, AAL: auth.AAL3}})
	if w.Code != 503 {
		t.Fatalf("missing audit store = %d %s", w.Code, w.Body.String())
	}
	// Join any worker a regression launched before returning the audit refusal.
	m.wg.Wait()
	if requests.Load() != 0 {
		t.Fatalf("Ollama received %d unaudited download requests", requests.Load())
	}
}
