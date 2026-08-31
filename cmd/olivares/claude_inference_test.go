// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/evals"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLoadClaudeInference_RefusesNonDirectGatewayWithoutBaseURL(t *testing.T) {
	// Vertex selected but no ANTHROPIC_VERTEX_BASE_URL: wiring would default to the
	// DIRECT endpoint while tagging the call vertex — a lie. The judge must stay unwired.
	env := map[string]string{"ANTHROPIC_API_KEY": "k", "CLAUDE_CODE_USE_VERTEX": "1"}
	ci := loadClaudeInference(func(k string) string { return env[k] }, nil, discardLog())
	if ci.judge != nil {
		t.Error("non-direct gateway without base URL must NOT wire the judge")
	}
	// With the base URL present, it wires.
	env["ANTHROPIC_VERTEX_BASE_URL"] = "https://vertex.example.com"
	ci2 := loadClaudeInference(func(k string) string { return env[k] }, nil, discardLog())
	if ci2.judge == nil {
		t.Error("non-direct gateway WITH base URL must wire the judge")
	}
	// Direct (default) with just a key wires fine.
	ci3 := loadClaudeInference(func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "k"}[k] }, nil, discardLog())
	if ci3.judge == nil {
		t.Error("direct gateway with a key must wire the judge")
	}
}

// cannedDoer returns one canned HTTP body for every request (no live network).
type cannedDoer struct {
	body   string
	status int
}

func (d cannedDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	st := d.status
	if st == 0 {
		st = http.StatusOK
	}
	return &http.Response{StatusCode: st, Body: io.NopCloser(strings.NewReader(d.body)), Header: make(http.Header)}, nil
}

// TestClaudeJudgeAdapter_RunsViaMockedHTTP proves the Claude-backed llm_judge RUNS
// (and is no longer SKIPPED) end-to-end through the exact composition-root adapter,
// with the inference client mocked at the HTTP layer — never a real production call.
func TestClaudeJudgeAdapter_RunsViaMockedHTTP(t *testing.T) {
	doer := cannedDoer{body: `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8",
		"content":[{"type":"text","text":"{\"score\":0.8,\"passed\":true,\"reason\":\"ok\"}"}],
		"usage":{"input_tokens":10,"output_tokens":5}}`}
	adapter := &claudeJudgeAdapter{inf: claudeapi.NewInference(claudeapi.InferenceConfig{APIKey: "k", DefaultModel: "claude-opus-4-8", Doer: doer})}

	// It satisfies the evals.Judge seam the composition root injects via WithJudge.
	var _ evals.Judge = adapter

	v, err := adapter.Judge(context.Background(), model.TenantID("t1"), evals.JudgeRequest{
		Output: "the answer", Criterion: "is it correct",
	})
	if err != nil {
		t.Fatalf("judge errored (would be outcome=error, not a pass): %v", err)
	}
	if !v.Passed || v.Score != 0.8 || v.Reason != "ok" {
		t.Errorf("verdict = %+v", v)
	}
}

// TestClaudeJudgeAdapter_HTTPFaultIsError proves a wired-but-failing judge surfaces
// as an error (scorer outcome=error), never the offline sentinel (which would be a
// silent SKIPPED).
func TestClaudeJudgeAdapter_HTTPFaultIsError(t *testing.T) {
	adapter := &claudeJudgeAdapter{inf: claudeapi.NewInference(claudeapi.InferenceConfig{APIKey: "k", DefaultModel: "claude-opus-4-8", Doer: cannedDoer{status: 500, body: "upstream down"}})}
	_, err := adapter.Judge(context.Background(), model.TenantID("t1"), evals.JudgeRequest{Output: "x", Criterion: "y"})
	if err == nil {
		t.Fatal("want a real error on HTTP fault (scorer records outcome=error, not a silent SKIPPED)")
	}
}

// TestClaudeEmbedderAdapter_AllowsEgress proves the model-backed embedder declares
// egress, so the knowledge module's local_only red-line gate refuses it (docs/SECURITY-HARDENING.md).
func TestClaudeEmbedderAdapter_AllowsEgress(t *testing.T) {
	a := &claudeEmbedderAdapter{modelRef: "voyage-3", dim: 1024, egress: true}
	if !a.AllowsEgress() {
		t.Fatal("a hosted/model-backed embedder MUST report AllowsEgress()=true or it silently defeats the local_only gate")
	}
	if a.ModelRef() != "voyage-3" || a.Dim() != 1024 {
		t.Errorf("modelRef/dim = %q/%d", a.ModelRef(), a.Dim())
	}
}

func TestResolveEmbeddingsProviderOrderAndSelfHosted(t *testing.T) {
	env := map[string]string{
		"OLIVARES_EMBEDDINGS_PROVIDER":             "self-hosted",
		"OLIVARES_EMBEDDINGS_VOYAGE_BASE_URL":      "https://voyage.example",
		"OLIVARES_EMBEDDINGS_VOYAGE_KEY":           "voyage-key",
		"OLIVARES_EMBEDDINGS_VOYAGE_MODEL":         "voyage-3",
		"OLIVARES_EMBEDDINGS_VOYAGE_DIM":           "1024",
		"OLIVARES_EMBEDDINGS_SELF_HOSTED_BASE_URL": "http://tei.internal",
		"OLIVARES_EMBEDDINGS_SELF_HOSTED_KEY":      "local-key",
		"OLIVARES_EMBEDDINGS_SELF_HOSTED_MODEL":    "bge-large-en",
		"OLIVARES_EMBEDDINGS_SELF_HOSTED_DIM":      "384",
	}
	cfg, ok, reason := resolveEmbeddingsProvider(func(k string) string { return env[k] })
	if !ok {
		t.Fatalf("resolveEmbeddingsProvider ok=false reason=%s", reason)
	}
	if cfg.Provider != "self_hosted" || cfg.Model != "bge-large-en" || cfg.Dim != 384 || cfg.Egress {
		t.Fatalf("self-hosted provider = %+v, want self_hosted model bge-large-en dim 384 egress=false", cfg)
	}

	delete(env, "OLIVARES_EMBEDDINGS_PROVIDER")
	cfg, ok, _ = resolveEmbeddingsProvider(func(k string) string { return env[k] })
	if !ok || cfg.Provider != "voyage" || !cfg.Egress {
		t.Fatalf("default provider order = %+v ok=%v, want voyage egress=true", cfg, ok)
	}

	env["OLIVARES_EMBEDDINGS_BASE_URL"] = "https://override.example"
	env["OLIVARES_EMBEDDINGS_KEY"] = "override-key"
	env["OLIVARES_EMBEDDINGS_MODEL"] = "override-model"
	env["OLIVARES_EMBEDDINGS_DIM"] = "256"
	cfg, ok, _ = resolveEmbeddingsProvider(func(k string) string { return env[k] })
	if !ok || cfg.Provider != "openai_compat" || cfg.Model != "override-model" || cfg.Dim != 256 {
		t.Fatalf("unscoped override = %+v ok=%v", cfg, ok)
	}
}

// The composition root is where "nobody asked for a provider" is told apart from
// "somebody asked and it is unusable". Everything downstream — the /status word,
// the console banner, the `olivares status` exit code — hangs off this reason.
func TestResolveEmbeddingsProviderSeparatesMissingFromIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		wantOK  bool
		wantWhy string
	}{
		{"nothing configured at all", map[string]string{}, false, reasonEmbeddingsMissing},
		{"unrelated env only", map[string]string{"ANTHROPIC_API_KEY": "k"}, false, reasonEmbeddingsMissing},
		{"unscoped block half written", map[string]string{
			"OLIVARES_EMBEDDINGS_BASE_URL": "https://emb.example",
		}, false, reasonEmbeddingsIncomplete},
		{"provider block missing its dim", map[string]string{
			"OLIVARES_EMBEDDINGS_VOYAGE_BASE_URL": "https://voyage.example",
			"OLIVARES_EMBEDDINGS_VOYAGE_KEY":      "k",
			"OLIVARES_EMBEDDINGS_VOYAGE_MODEL":    "voyage-3",
		}, false, reasonEmbeddingsIncomplete},
		// Declared intent with nothing behind it is NOT the pristine default: the
		// operator named a provider and the install is not honoring it.
		{"provider pinned with no credentials", map[string]string{
			"OLIVARES_EMBEDDINGS_PROVIDER": "voyage",
		}, false, reasonEmbeddingsIncomplete},
		{"unknown provider name alone", map[string]string{
			"OLIVARES_EMBEDDINGS_PROVIDER": "not-a-provider",
		}, false, reasonEmbeddingsMissing},
		{"fully configured", map[string]string{
			"OLIVARES_EMBEDDINGS_BASE_URL": "https://emb.example",
			"OLIVARES_EMBEDDINGS_KEY":      "k",
			"OLIVARES_EMBEDDINGS_MODEL":    "m",
			"OLIVARES_EMBEDDINGS_DIM":      "8",
		}, true, reasonEmbeddingsConfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, reason := resolveEmbeddingsProvider(func(k string) string { return tc.env[k] })
			if ok != tc.wantOK || reason != tc.wantWhy {
				t.Fatalf("resolveEmbeddingsProvider ok=%v reason=%q, want ok=%v reason=%q", ok, reason, tc.wantOK, tc.wantWhy)
			}
			// And the reason must classify to the posture the status page reads.
			wantPosture := api.PostureImpaired
			switch reason {
			case reasonEmbeddingsMissing:
				wantPosture = api.PostureNotConfigured
			case reasonEmbeddingsConfigured:
				wantPosture = api.PostureReady
			}
			if got := knowledgePostureFor(reason); got != wantPosture {
				t.Fatalf("posture for %q = %q, want %q", reason, got, wantPosture)
			}
		})
	}
}

func TestLoadClaudeInference_NoEmbeddingsWarnsAndStatusIsLocalHash(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ci := loadClaudeInference(func(string) string { return "" }, nil, log)
	if ci.embedder != nil {
		t.Fatal("no embeddings provider must leave semantic embedder unwired")
	}
	st := ci.knowledgeStatus.KnowledgeStatus(context.Background())
	if st.EmbedderKind != "local-hash" || st.RetrievalSemantic || st.Reason != "embeddings_provider_missing" {
		t.Fatalf("knowledge status = %+v, want local-hash semantic=false provider_missing", st)
	}
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "retrieval is lexical, NOT semantic") {
		t.Fatalf("missing persistent WARN for local-hash fallback; log=%q", buf.String())
	}
}

func TestGatewayFromEnv(t *testing.T) {
	cases := []struct {
		env     map[string]string
		wantGW  sdkmodel.Gateway
		wantURL string
	}{
		{map[string]string{}, sdkmodel.GatewayDirect, ""},
		{map[string]string{"ANTHROPIC_BASE_URL": "https://proxy:4000"}, sdkmodel.GatewayDirect, "https://proxy:4000"},
		{map[string]string{"CLAUDE_CODE_USE_MANTLE": "1", "ANTHROPIC_BEDROCK_MANTLE_BASE_URL": "https://gw"}, sdkmodel.GatewayBedrockMantle, "https://gw"},
		{map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1"}, sdkmodel.GatewayBedrockLegacy, ""},
		{map[string]string{"CLAUDE_CODE_USE_VERTEX": "true"}, sdkmodel.GatewayVertex, ""},
		{map[string]string{"CLAUDE_CODE_USE_FOUNDRY": "1"}, sdkmodel.GatewayFoundry, ""},
		{map[string]string{"CLAUDE_CODE_USE_ANTHROPIC_AWS": "1"}, sdkmodel.GatewayClaudePlatformAWS, ""},
		{map[string]string{"CLAUDE_CODE_USE_ANTHROPIC_AWS": "1", "ANTHROPIC_AWS_BASE_URL": "https://aws-proxy.example"}, sdkmodel.GatewayClaudePlatformAWS, "https://aws-proxy.example"},
		{map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1", "CLAUDE_CODE_USE_ANTHROPIC_AWS": "1", "ANTHROPIC_AWS_BASE_URL": "https://aws-proxy.example"}, sdkmodel.GatewayBedrockLegacy, ""},
		{map[string]string{"CLAUDE_CODE_USE_FOUNDRY": "1", "CLAUDE_CODE_USE_ANTHROPIC_AWS": "1", "ANTHROPIC_AWS_BASE_URL": "https://aws-proxy.example"}, sdkmodel.GatewayFoundry, ""},
	}
	for i, tc := range cases {
		getenv := func(k string) string { return tc.env[k] }
		gw, url := gatewayFromEnv(getenv)
		if gw != tc.wantGW || url != tc.wantURL {
			t.Errorf("[%d] gateway=%q url=%q, want %q/%q", i, gw, url, tc.wantGW, tc.wantURL)
		}
	}
}

func TestResolveInferenceAndEmbedder(t *testing.T) {
	// No credential => judge unwired (fail-closed).
	if _, ok := resolveInference(func(string) string { return "" }); ok {
		t.Error("no credential must not wire a judge")
	}
	// ANTHROPIC_API_KEY => wired, with the pinned default model on a third-party gateway.
	env := map[string]string{"ANTHROPIC_API_KEY": "k", "CLAUDE_CODE_USE_VERTEX": "1", "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-4-8"}
	cfg, ok := resolveInference(func(k string) string { return env[k] })
	if !ok || cfg.Gateway != sdkmodel.GatewayVertex || cfg.DefaultModel != "claude-opus-4-8" {
		t.Errorf("resolveInference = %+v ok=%v", cfg, ok)
	}
	// Embedder requires all four; a partial config stays unwired.
	if _, ok := resolveEmbedder(func(k string) string { return map[string]string{"OLIVARES_EMBEDDINGS_BASE_URL": "https://x"}[k] }); ok {
		t.Error("partial embeddings config must not wire an embedder")
	}
	embEnv := map[string]string{"OLIVARES_EMBEDDINGS_BASE_URL": "https://x", "OLIVARES_EMBEDDINGS_KEY": "k", "OLIVARES_EMBEDDINGS_MODEL": "voyage-3", "OLIVARES_EMBEDDINGS_DIM": "1024"}
	emb, ok := resolveEmbedder(func(k string) string { return embEnv[k] })
	if !ok || emb.ModelRef() != "voyage-3" || emb.Dim() != 1024 || !emb.AllowsEgress() {
		t.Errorf("resolveEmbedder = %+v ok=%v", emb, ok)
	}
}
