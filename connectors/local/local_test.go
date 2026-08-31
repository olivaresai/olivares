// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type fixtureDoer struct {
	t    *testing.T
	reqs []*http.Request
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	var file string
	switch req.URL.Path {
	case "/api/tags":
		file = "ollama_tags.json"
	case "/v1/models":
		file = "vllm_models.json"
	case "/api/ps":
		file = "ollama_ps.json"
	case "/metrics":
		file = "vllm_metrics.txt"
	default:
		d.t.Fatalf("unexpected path %q", req.URL.Path)
	}
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		d.t.Fatalf("read fixture %s: %v", file, err)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
}

type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func open(t *testing.T, settings map[string]string) (*Source, *fixtureDoer) {
	doer := &fixtureDoer{t: t}
	s := New()
	s.doer = doer
	s.now = func() time.Time { return time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, doer
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource {
		t.Fatalf("descriptor = %+v", d)
	}
}

func TestSnapshot_BothProviders(t *testing.T) {
	s, _ := open(t, map[string]string{"vllm_url": "http://localhost:8000"})
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if cat.Provider.Kind != modelprovider.KindLocalInference {
		t.Fatalf("provider kind = %s", cat.Provider.Kind)
	}
	var ollama, vllm int
	for _, m := range cat.Models {
		switch m.ProviderRef {
		case modelprovider.ProviderOllama:
			ollama++
		case modelprovider.ProviderVLLM:
			vllm++
		default:
			t.Fatalf("unexpected provider ref %q", m.ProviderRef)
		}
		if m.Pricing != nil {
			t.Fatalf("local model %s priced without an operator rate", m.Ref)
		}
	}
	if ollama != 2 || vllm != 1 {
		t.Fatalf("models: ollama=%d vllm=%d, want 2/1", ollama, vllm)
	}
}

func TestSnapshot_OllamaDisabled(t *testing.T) {
	s, _ := open(t, map[string]string{"ollama_url": "", "vllm_url": "http://localhost:8000"})
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, m := range cat.Models {
		if m.ProviderRef == modelprovider.ProviderOllama {
			t.Fatal("ollama disabled but produced models")
		}
	}
}

func TestGather_VLLMMetrics_NoRateZeroCost(t *testing.T) {
	s, doer := open(t, map[string]string{"vllm_url": "http://localhost:8000"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// Gather also emits Ollama RESIDENCY posture (default ollama_url is enabled here), so
	// the subject of this test is the cost samples, not the total. Filtering keeps the
	// assertion exactly as strong — still "two models in /metrics, and no more".
	var samples []model.CostSample
	for _, o := range sink.obs {
		if cs, ok := o.(model.CostSample); ok {
			samples = append(samples, cs)
		}
	}
	if len(samples) != 2 {
		t.Fatalf("emitted %d cost sample(s), want 2 (two models in /metrics)", len(samples))
	}
	for _, cs := range samples {
		if cs.ProviderRef != modelprovider.ProviderVLLM {
			t.Fatalf("provider = %q", cs.ProviderRef)
		}
		if cs.CostMicroUSD != 0 {
			t.Fatalf("local cost without a rate = %d, want 0", cs.CostMicroUSD)
		}
		if cs.ModelRef == "meta-llama/Llama-3.1-8B-Instruct" {
			if cs.InputTokens != 12000 || cs.OutputTokens != 3400 {
				t.Fatalf("llama tokens = %d/%d, want 12000/3400", cs.InputTokens, cs.OutputTokens)
			}
		}
	}
	// Read-only: /metrics fetched with GET.
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET: %s", r.Method)
		}
	}
}

func TestGather_VLLMMetrics_WithRateDerivesCost(t *testing.T) {
	s, _ := open(t, map[string]string{"vllm_url": "http://localhost:8000", "cost_per_mtok_usd": "10"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var llama model.CostSample
	for _, o := range sink.obs {
		cs, ok := o.(model.CostSample)
		if !ok {
			continue // residency posture; this test is about the derived cost
		}
		if cs.ModelRef == "meta-llama/Llama-3.1-8B-Instruct" {
			llama = cs
		}
	}
	// (12000 input + 3400 output) at $10/MTok each = 120000 + 34000 = 154000 micro-USD.
	if llama.CostMicroUSD != 154000 {
		t.Fatalf("llama cost = %d, want 154000", llama.CostMicroUSD)
	}
}

func TestGather_NoVLLMEmitsNoCostSamples(t *testing.T) {
	s, _ := open(t, map[string]string{}) // only ollama (default) enabled, no vllm
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// This test used to assert Gather emitted NOTHING without vLLM. That premise was
	// true only while Gather did metering alone: Ollama publishes no aggregate token
	// metrics, so it contributed no cost. It now also reports RESIDENCY from /api/ps,
	// which is a posture and not a measurement of spend, so the invariant is restated
	// as what it always meant — Ollama contributes no METERING — instead of being
	// deleted, which would have stopped checking it.
	for _, o := range sink.obs {
		if cs, ok := o.(model.CostSample); ok {
			t.Errorf("cost sample emitted with no vLLM configured: %+v", cs)
		}
	}
	if len(sink.obs) == 0 {
		t.Error("expected the Ollama residency posture; got no observations at all")
	}
}

func TestParseVLLMTokens(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "vllm_metrics.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got := parseVLLMTokens(string(body))
	llama := got["meta-llama/Llama-3.1-8B-Instruct"]
	if llama == nil || llama.prompt != 12000 || llama.generation != 3400 {
		t.Fatalf("llama tokens = %+v", llama)
	}
	// The float-form counter 1.5e+03 must round to 1500.
	mistral := got["mistralai/Mistral-7B"]
	if mistral == nil || mistral.prompt != 5000 || mistral.generation != 1500 {
		t.Fatalf("mistral tokens = %+v", mistral)
	}
	if _, ok := got[""]; ok {
		t.Fatal("an unlabeled/other metric leaked into the result")
	}
}

func TestTimedGetJSON_MeasuresLatency(t *testing.T) {
	// An advancing clock makes the measured round-trip non-zero and assignable to
	// the router's latency policy.
	doer := &fixtureDoer{t: t}
	s := New()
	s.doer = doer
	var calls int
	s.now = func() time.Time {
		calls++
		return time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC).Add(time.Duration(calls) * 5 * time.Millisecond)
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"vllm_url": "http://localhost:8000"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var measured bool
	for _, m := range cat.Models {
		if m.ObservedLatencyMillis > 0 {
			measured = true
		}
	}
	if !measured {
		t.Fatal("no model carried a measured latency")
	}
}

// TestGather_OllamaResidency pins the /api/ps half of this connector: what is LOADED,
// which /api/tags cannot answer. The connectors reference used to have to declare that
// gap in prose — "reports what is INSTALLED, never which model is LOADED".
//
// The three fixtures are the three placements, and the severity is the placement rather
// than the model: a model fully in VRAM is information, while one on the CPU or SPLIT
// between CPU and GPU is the case an operator pays latency for without being told, so
// it is a warning.
func TestGather_OllamaResidency(t *testing.T) {
	s, _ := open(t, map[string]string{"ollama_url": "http://ollama.invalid"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	got := map[string]model.FindingReport{}
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			continue
		}
		if f.SubjectKind != subjectResidency {
			t.Errorf("unexpected subject kind %q", f.SubjectKind)
			continue
		}
		got[f.SubjectRef] = f
	}
	if len(got) != 3 {
		t.Fatalf("expected the three resident models, got %d: %v", len(got), got)
	}

	for _, tc := range []struct {
		ref      string
		severity model.Severity
		says     string
	}{
		{"llama3.1:8b", model.SeverityInfo, "resident on gpu"},
		{"qwen2.5:14b", model.SeverityMedium, "resident on split gpu/cpu"},
		{"phi3:mini", model.SeverityMedium, "resident on cpu"},
	} {
		f, ok := got[tc.ref]
		if !ok {
			t.Errorf("no residency observation for %q", tc.ref)
			continue
		}
		if f.Severity != tc.severity {
			t.Errorf("%s: severity = %v, want %v", tc.ref, f.Severity, tc.severity)
		}
		if !strings.Contains(f.Title, tc.says) {
			t.Errorf("%s: title %q does not say %q", tc.ref, f.Title, tc.says)
		}
	}
}

// TestGather_NoOllamaEmitsNoResidency is the non-firing direction. An absent server is
// not a posture: emitting a finding for it would make "not configured" and "configured
// and holding nothing" indistinguishable, and a connector that always emits something
// would satisfy the test above without observing anything.
func TestGather_NoOllamaEmitsNoResidency(t *testing.T) {
	// Omitting ollama_url does NOT disable Ollama — the field carries a default
	// (localhost), which is the right default for a local connector. Disabling is an
	// EXPLICIT empty value, exactly as the config field documents. Getting this wrong is
	// what this test caught on its first run, which is the point of writing it.
	s, doer := open(t, map[string]string{"ollama_url": "", "vllm_url": "http://vllm.invalid"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, o := range sink.obs {
		if f, ok := o.(model.FindingReport); ok && f.SubjectKind == subjectResidency {
			t.Errorf("residency observation emitted with no Ollama configured: %+v", f)
		}
	}
	for _, r := range doer.reqs {
		if r.URL.Path == "/api/ps" {
			t.Error("called /api/ps with no Ollama configured")
		}
	}
}
