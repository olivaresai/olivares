// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func clearEmbeddingEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OLIVARES_EMBEDDINGS_BASE_URL",
		"OLIVARES_EMBEDDINGS_KEY",
		"OLIVARES_EMBEDDINGS_MODEL",
		"OLIVARES_EMBEDDINGS_DIM",
		"OLIVARES_EMBEDDINGS_GEO",
		"OLIVARES_EMBEDDINGS_PROVIDER",
		"OLIVARES_EMBEDDINGS_REQUIRE",
		"OLIVARES_EMBEDDINGS_VOYAGE_BASE_URL",
		"OLIVARES_EMBEDDINGS_VOYAGE_KEY",
		"OLIVARES_EMBEDDINGS_VOYAGE_MODEL",
		"OLIVARES_EMBEDDINGS_VOYAGE_DIM",
		"OLIVARES_EMBEDDINGS_VOYAGE_GEO",
		"OLIVARES_EMBEDDINGS_OPENAI_BASE_URL",
		"OLIVARES_EMBEDDINGS_OPENAI_KEY",
		"OLIVARES_EMBEDDINGS_OPENAI_MODEL",
		"OLIVARES_EMBEDDINGS_OPENAI_DIM",
		"OLIVARES_EMBEDDINGS_OPENAI_GEO",
		"OLIVARES_EMBEDDINGS_OPENAI_COMPAT_BASE_URL",
		"OLIVARES_EMBEDDINGS_OPENAI_COMPAT_KEY",
		"OLIVARES_EMBEDDINGS_OPENAI_COMPAT_MODEL",
		"OLIVARES_EMBEDDINGS_OPENAI_COMPAT_DIM",
		"OLIVARES_EMBEDDINGS_OPENAI_COMPAT_GEO",
		"OLIVARES_EMBEDDINGS_SELF_HOSTED_BASE_URL",
		"OLIVARES_EMBEDDINGS_SELF_HOSTED_KEY",
		"OLIVARES_EMBEDDINGS_SELF_HOSTED_MODEL",
		"OLIVARES_EMBEDDINGS_SELF_HOSTED_DIM",
		"OLIVARES_EMBEDDINGS_SELF_HOSTED_GEO",
	} {
		t.Setenv(k, "")
	}
}

func TestKnowledgeSemanticEmbedderStatusAndKBModel(t *testing.T) {
	clearEmbeddingEnv(t)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode embeddings request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Model != "fixture-embed-3" {
			t.Errorf("embedding model = %q, want fixture-embed-3", req.Model)
		}
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			data[i] = map[string]any{"index": i, "embedding": []float64{float64(i) + 0.1, 0.2, 0.3}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": req.Model, "data": data})
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OLIVARES_EMBEDDINGS_BASE_URL", srv.URL)
	t.Setenv("OLIVARES_EMBEDDINGS_KEY", "fixture-key")
	t.Setenv("OLIVARES_EMBEDDINGS_MODEL", "fixture-embed-3")
	t.Setenv("OLIVARES_EMBEDDINGS_DIM", "3")
	t.Setenv("OLIVARES_EMBEDDINGS_GEO", "global")

	h := newHarness(t)
	kbID := createKnowledgeKB(t, h, "semantic-kb")
	kb := h.getJSON(h.adminToken, h.tenantA, "/v1/m/knowledge/kbs/"+kbID)
	if kb["embed_model"] != "fixture-embed-3" {
		t.Fatalf("KB embed_model = %v, want fixture-embed-3", kb["embed_model"])
	}
	if code, raw := h.req("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", h.adminToken, h.tenantA, map[string]any{
		"documents": []map[string]any{{
			"source_doc_id": "doc-1",
			"title":         "Fixture",
			"body":          "semantic embeddings should go through the fixture provider",
		}},
	}); code != http.StatusOK {
		t.Fatalf("ingest with semantic embedder = %d %s", code, raw)
	}
	if calls.Load() == 0 {
		t.Fatal("embedding fixture was not called")
	}
	status := h.getJSON("", "", "/status")
	if status["embedder_kind"] != "semantic" || status["retrieval_semantic"] != true {
		t.Fatalf("/status knowledge fields = kind %v semantic %v", status["embedder_kind"], status["retrieval_semantic"])
	}
	knowledge, err := statusComponentDetail(status, "knowledge")
	if err != nil {
		t.Fatal(err)
	}
	if knowledge["status"] != "operational" {
		t.Fatalf("knowledge component status = %v, want operational", knowledge["status"])
	}
}

// A correct install with no embeddings provider — the product's deliberate
// default — must report itself as INCOMPLETE, never as broken: the whole engine
// answers not_configured, the knowledge component says so by name, and the
// reason and lexical posture stay on the wire. Reporting a pristine install as
// `degraded` is what made a fresh install look like an outage.
func TestKnowledgeLocalHashDefaultStatusAndKBModel(t *testing.T) {
	clearEmbeddingEnv(t)
	h := newHarness(t)
	kbID := createKnowledgeKB(t, h, "local-hash-kb")
	kb := h.getJSON(h.adminToken, h.tenantA, "/v1/m/knowledge/kbs/"+kbID)
	if kb["embed_model"] != "local-hash" {
		t.Fatalf("KB embed_model = %v, want local-hash", kb["embed_model"])
	}
	status := h.getJSON("", "", "/status")
	if status["embedder_kind"] != "local-hash" || status["retrieval_semantic"] != false {
		t.Fatalf("/status knowledge fields = kind %v semantic %v", status["embedder_kind"], status["retrieval_semantic"])
	}
	if status["knowledge_status_reason"] != "embeddings_provider_missing" {
		t.Fatalf("/status knowledge_status_reason = %v", status["knowledge_status_reason"])
	}
	if status["status"] != "not_configured" {
		t.Fatalf("/status overall = %v, want not_configured on a clean install with no optional provider", status["status"])
	}
	knowledge, err := statusComponentDetail(status, "knowledge")
	if err != nil {
		t.Fatal(err)
	}
	if knowledge["status"] != "not_configured" {
		t.Fatalf("knowledge component status = %v, want not_configured", knowledge["status"])
	}
}

// The other direction, measured on a REAL boot: an embeddings block the operator
// started and left unusable (base URL only — no key, model or dim) is a fault,
// not the pristine default. It must keep the degraded verdict that
// `olivares status` exits 7 on.
func TestKnowledgeIncompleteEmbeddingsConfigStaysDegraded(t *testing.T) {
	clearEmbeddingEnv(t)
	t.Setenv("OLIVARES_EMBEDDINGS_BASE_URL", "https://embeddings.invalid")

	h := newHarness(t)
	status := h.getJSON("", "", "/status")
	if status["knowledge_status_reason"] != "embeddings_config_incomplete" {
		t.Fatalf("/status knowledge_status_reason = %v, want embeddings_config_incomplete", status["knowledge_status_reason"])
	}
	if status["status"] != "degraded" {
		t.Fatalf("/status overall = %v, want degraded (a half-written provider block is a fault)", status["status"])
	}
	knowledge, err := statusComponentDetail(status, "knowledge")
	if err != nil {
		t.Fatal(err)
	}
	if knowledge["status"] != "degraded" {
		t.Fatalf("knowledge component status = %v, want degraded", knowledge["status"])
	}
}

func createKnowledgeKB(t *testing.T, h *harness, name string) string {
	t.Helper()
	var out struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/knowledge/kbs", h.adminToken, h.tenantA, map[string]any{"name": name}, &out); code != http.StatusCreated {
		t.Fatalf("create KB %q = %d", name, code)
	}
	if out.ID == "" {
		t.Fatalf("create KB %q returned empty id", name)
	}
	return out.ID
}

func statusComponentDetail(status map[string]any, name string) (map[string]any, error) {
	raw, _ := status["components"].([]any)
	for _, item := range raw {
		m, _ := item.(map[string]any)
		if m["name"] == name {
			return m, nil
		}
	}
	return nil, fmt.Errorf("component %q not found", name)
}
