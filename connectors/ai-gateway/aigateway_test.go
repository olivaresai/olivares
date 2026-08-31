// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aigateway_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	aigateway "github.com/olivaresai/olivares/connectors/ai-gateway"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// capturingSink records the CostSamples a Gather emits (mirrors awskms's sink).
type capturingSink struct{ costs []model.CostSample }

func (c *capturingSink) Emit(_ context.Context, obs model.Observation) error {
	if cs, ok := obs.(model.CostSample); ok {
		c.costs = append(c.costs, cs)
	}
	return nil
}

func open(t *testing.T, path string) *aigateway.Source {
	t.Helper()
	s := aigateway.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": path}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gather(t *testing.T, s *aigateway.Source) *capturingSink {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

func TestOpenRequiresPath(t *testing.T) {
	s := aigateway.New()
	if err := s.Open(context.Background(), sdk.Config{}); err == nil {
		t.Fatal("Open with no path should error")
	}
}

func TestGatherCostSamples(t *testing.T) {
	s := open(t, "testdata/usage.jsonl")
	sink := gather(t, s)

	// 6 records: Anthropic(est) + Anthropic(billed) + Anthropic(cache) + OpenAI = 4
	// emitted; the zero-token Anthropic line and the /healthz proxy line are skipped.
	if len(sink.costs) != 4 {
		t.Fatalf("got %d cost samples, want 4: %+v", len(sink.costs), sink.costs)
	}

	for _, c := range sink.costs {
		if c.Gateway != aigateway.GatewayEnvoyAI {
			t.Errorf("gateway = %q, want %q", c.Gateway, aigateway.GatewayEnvoyAI)
		}
		if c.Gateway != model.Gateway("envoy-ai-gateway") {
			t.Errorf("gateway literal mismatch: %q", c.Gateway)
		}
		if c.ModelRef == "" || c.ProviderRef == "" {
			t.Errorf("sample missing model/provider: %+v", c)
		}
		if c.OccurredAt.IsZero() {
			t.Errorf("sample has zero OccurredAt: %+v", c)
		}
	}

	// Sample 1: Anthropic GenAI form, estimated (gateway reports tokens, not money).
	est := find(t, sink.costs, "claude-opus-4-20250514", model.ProvenanceEstimated, 1200)
	if est.ProviderRef != "anthropic" {
		t.Errorf("provider = %q, want anthropic", est.ProviderRef)
	}
	if est.InputTokens != 1200 || est.OutputTokens != 350 {
		t.Errorf("tokens = %d/%d, want 1200/350", est.InputTokens, est.OutputTokens)
	}
	if est.CostMicroUSD != 0 {
		t.Errorf("estimated sample must not carry a cost: %d", est.CostMicroUSD)
	}
	wantTime, _ := time.Parse(time.RFC3339, "2026-06-01T10:00:00Z")
	if !est.OccurredAt.Equal(wantTime) {
		t.Errorf("OccurredAt = %v, want %v", est.OccurredAt, wantTime)
	}

	// Sample 2: metadata form, authoritative cost -> billed.
	billed := find(t, sink.costs, "claude-3-5-sonnet-20241022", model.ProvenanceBilled, 800)
	if billed.ProviderRef != "anthropic" {
		t.Errorf("billed provider = %q, want anthropic (from backend_name)", billed.ProviderRef)
	}
	if billed.CostMicroUSD != 18450 {
		t.Errorf("billed cost = %d, want 18450", billed.CostMicroUSD)
	}
	if billed.InputTokens != 800 || billed.OutputTokens != 210 {
		t.Errorf("billed tokens = %d/%d, want 800/210", billed.InputTokens, billed.OutputTokens)
	}

	// Sample 3: cache split present -> mapped to CacheRead + CacheCreation5m only.
	cache := find(t, sink.costs, "claude-opus-4-20250514", model.ProvenanceEstimated, 5000)
	if cache.CacheReadTokens != 4200 {
		t.Errorf("cache read = %d, want 4200", cache.CacheReadTokens)
	}
	if cache.CacheCreation5mTokens != 600 {
		t.Errorf("cache creation 5m = %d, want 600", cache.CacheCreation5mTokens)
	}
	if cache.CacheCreation1hTokens != 0 {
		t.Errorf("cache creation 1h must be 0 (gateway has no TTL split): %d", cache.CacheCreation1hTokens)
	}

	// Sample 4: provider taken verbatim (not relabeled to anthropic).
	oai := find(t, sink.costs, "gpt-4o-mini", model.ProvenanceEstimated, 300)
	if oai.ProviderRef != "openai" {
		t.Errorf("openai provider = %q, want openai (verbatim)", oai.ProviderRef)
	}
}

// TestNoPromptLeaks is the minimal-data negative test (docs/SECURITY-HARDENING.md): the fixture
// records embed a prompt, a completion, a response body, AND recognizable secret
// canaries (an AWS access key and an Anthropic API key). None may appear in any
// emitted CostSample — only tokens/cost/model/provider/timestamp travel.
func TestNoPromptLeaks(t *testing.T) {
	s := open(t, "testdata/usage.jsonl")
	sink := gather(t, s)
	if len(sink.costs) == 0 {
		t.Fatal("expected cost samples")
	}
	blob, err := json.Marshal(sink.costs)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{
		"AKIAIOSFODNN7EXAMPLE",          // AWS access key in a prompt
		"sk-ant-api03-SECRETLEAKCANARY", // Anthropic key in a response body
		"hunter2",                       // password in a prompt
		"123-45-6789",                   // PII (SSN) in a prompt
		"summarize this PII",            // prompt text
		"the user asked for",            // completion text
		"do not leak me",                // response_body text
	} {
		if strings.Contains(string(blob), canary) {
			t.Fatalf("leak: %q appeared in emitted cost samples: %s", canary, blob)
		}
	}
}

// TestGatherDirectoryAndGzip checks the directory + .gz batch path: the same records
// in a gzipped file under a directory yield the same sample count.
func TestGatherDirectoryAndGzip(t *testing.T) {
	dir := t.TempDir()
	writeGz(t, dir+"/usage.jsonl.gz", "testdata/usage.jsonl")
	s := open(t, dir)
	sink := gather(t, s)
	if len(sink.costs) != 4 {
		t.Fatalf("gzip/dir: got %d samples, want 4", len(sink.costs))
	}
}

// writeGz reads src and writes it gzip-compressed to dst.
func writeGz(t *testing.T, dst, src string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	if _, err := gz.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

// find returns the single sample matching model+provenance+input tokens, failing if
// absent. (Input tokens disambiguate the two claude-opus rows.)
func find(t *testing.T, costs []model.CostSample, modelRef string, prov model.CostProvenance, inTokens int64) model.CostSample {
	t.Helper()
	for _, c := range costs {
		if c.ModelRef == modelRef && c.Provenance == prov && c.InputTokens == inTokens {
			return c
		}
	}
	t.Fatalf("no sample model=%q provenance=%q input=%d in %+v", modelRef, prov, inTokens, costs)
	return model.CostSample{}
}
