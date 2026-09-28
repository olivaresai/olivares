// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
)

// catalogDoer serves fixture files for the local catalog. Show responses are
// chosen from the POST body model name, which is the documented show request.
type catalogDoer struct {
	mu         sync.Mutex
	tags       []byte
	vllm       []byte
	show       map[string][]byte
	showStatus map[string]int
	shows      []showCall
}

type showCall struct {
	method  string
	model   string
	verbose *bool
}

func (d *catalogDoer) Do(req *http.Request) (*http.Response, error) {
	switch req.URL.Path {
	case "/api/tags":
		return textResponse(http.StatusOK, string(d.tags)), nil
	case "/v1/models":
		if d.vllm == nil {
			return textResponse(http.StatusNotFound, `{"error":"vllm disabled in this fixture"}`), nil
		}
		return textResponse(http.StatusOK, string(d.vllm)), nil
	case "/api/show":
		body, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		var got struct {
			Model   string `json:"model"`
			Verbose *bool  `json:"verbose"`
		}
		_ = json.Unmarshal(body, &got)
		d.mu.Lock()
		d.shows = append(d.shows, showCall{method: req.Method, model: got.Model, verbose: got.Verbose})
		d.mu.Unlock()
		if code, ok := d.showStatus[got.Model]; ok {
			return textResponse(code, `{"error":"unavailable"}`), nil
		}
		body, ok := d.show[got.Model]
		if !ok {
			return textResponse(http.StatusNotFound, `{"error":"unknown model"}`), nil
		}
		return textResponse(http.StatusOK, string(body)), nil
	default:
		return textResponse(http.StatusNotFound, `{"error":"unexpected path"}`), nil
	}
}

func (d *catalogDoer) showCalls() []showCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]showCall, len(d.shows))
	copy(out, d.shows)
	return out
}

func textResponse(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Status:     http.StatusText(code),
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Header:     make(http.Header),
	}
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func loadShow(t *testing.T, files map[string]string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte, len(files))
	for model, name := range files {
		out[model] = readTestdata(t, name)
	}
	return out
}

func openWith(t *testing.T, doer modelprovider.Doer, settings map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = doer
	s.now = func() time.Time { return time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func findModel(t *testing.T, cat modelprovider.Catalog, ref string) modelprovider.Model {
	t.Helper()
	for _, m := range cat.Models {
		if m.Ref == ref {
			return m
		}
	}
	t.Fatalf("model %q not in catalog (%d models)", ref, len(cat.Models))
	return modelprovider.Model{}
}

// TestSnapshot_CompletionOnlyOllamaIsNotToolCapable is the negative: discovery
// must not grant tool use. The model name contains "vision" and "tools"; those
// words are not evidence. The parameters text says num_ctx 512, which is not
// the documented model_info context length of 2048.
func TestSnapshot_CompletionOnlyOllamaIsNotToolCapable(t *testing.T) {
	doer := &catalogDoer{
		tags: readTestdata(t, "ollama_tags_completion.json"),
		show: loadShow(t, map[string]string{
			"vision-tools:latest": "ollama_show_completion.json",
		}),
	}
	s := openWith(t, doer, map[string]string{"ollama_url": "http://ollama.invalid"})
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	m := findModel(t, cat, "vision-tools:latest")
	if m.HasCapability(modelprovider.CapToolUse) {
		t.Fatalf("completion-only model reported tool use: %v", m.Capabilities)
	}
	if m.HasCapability(modelprovider.CapVision) {
		t.Fatalf("model name must not grant vision: %v", m.Capabilities)
	}
	if m.HasCapability(modelprovider.CapExtendedThinking) {
		t.Fatalf("completion metadata must not grant thinking: %v", m.Capabilities)
	}
	if m.CapabilitySource != "live" {
		t.Fatalf("source = %q, want live", m.CapabilitySource)
	}
	if !m.HasCapability(modelprovider.CapStreaming) {
		t.Fatalf("Ollama chat and generate stream for every model; caps = %v", m.Capabilities)
	}
	if m.ContextWindow != 2048 {
		t.Fatalf("context = %d, want 2048 from model_info, not parameters num_ctx", m.ContextWindow)
	}
	if len(m.Capabilities) != 1 {
		t.Fatalf("caps = %v, want only streaming", m.Capabilities)
	}
	calls := doer.showCalls()
	if len(calls) != 1 || calls[0].method != http.MethodPost || calls[0].model != "vision-tools:latest" {
		t.Fatalf("show calls = %+v, want one POST for vision-tools:latest", calls)
	}
	if calls[0].verbose != nil && *calls[0].verbose {
		t.Fatal("show requested verbose metadata")
	}
}

func TestSnapshot_OllamaShowMetadata(t *testing.T) {
	doer := &catalogDoer{
		tags: readTestdata(t, "ollama_tags_capabilities.json"),
		show: loadShow(t, map[string]string{
			"fixture-tools:1b":   "ollama_show_tools.json",
			"fixture-vision:1b":  "ollama_show_vision.json",
			"fixture-nothink:1b": "ollama_show_nothink.json",
			"fixture-embed:1b":   "ollama_show_embedding.json",
		}),
	}
	s := openWith(t, doer, map[string]string{"ollama_url": "http://ollama.invalid"})
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(cat.Models) != 4 {
		t.Fatalf("models = %d, want 4", len(cat.Models))
	}

	tools := findModel(t, cat, "fixture-tools:1b")
	if !tools.HasCapability(modelprovider.CapToolUse) {
		t.Fatalf("tools metadata did not grant tool use: %v", tools.Capabilities)
	}
	if tools.HasCapability(modelprovider.CapVision) {
		t.Fatalf("tools model granted vision: %v", tools.Capabilities)
	}
	if !tools.HasCapability(modelprovider.CapExtendedThinking) {
		t.Fatalf("thinking metadata did not grant extended thinking: %v", tools.Capabilities)
	}
	if !tools.HasCapability(modelprovider.CapStreaming) || tools.CapabilitySource != "live" {
		t.Fatalf("tools model = source %q caps %v", tools.CapabilitySource, tools.Capabilities)
	}
	if tools.ContextWindow != 32768 {
		t.Fatalf("tools context = %d, want 32768", tools.ContextWindow)
	}
	if tools.APICapabilities == nil {
		t.Fatal("thinking values were not recorded")
	}
	gotModes := tools.APICapabilities.ThinkingModes
	wantModes := []string{"low", "medium", "high"}
	if len(gotModes) != len(wantModes) {
		t.Fatalf("thinking modes = %v, want %v", gotModes, wantModes)
	}
	for i := range wantModes {
		if gotModes[i] != wantModes[i] {
			t.Fatalf("thinking modes = %v, want %v", gotModes, wantModes)
		}
	}
	if tools.APICapabilities.AsOf != "2026-06-02" {
		t.Fatalf("thinking AsOf = %q, want the connector clock date", tools.APICapabilities.AsOf)
	}

	vision := findModel(t, cat, "fixture-vision:1b")
	if !vision.HasCapability(modelprovider.CapVision) {
		t.Fatalf("vision metadata did not grant vision: %v", vision.Capabilities)
	}
	if vision.HasCapability(modelprovider.CapToolUse) {
		t.Fatalf("vision model granted tool use: %v", vision.Capabilities)
	}
	if vision.ContextWindow != 8192 || vision.CapabilitySource != "live" {
		t.Fatalf("vision model = source %q context %d caps %v", vision.CapabilitySource, vision.ContextWindow, vision.Capabilities)
	}

	nothink := findModel(t, cat, "fixture-nothink:1b")
	if nothink.HasCapability(modelprovider.CapExtendedThinking) || nothink.HasCapability(modelprovider.CapToolUse) {
		t.Fatalf("thinking values [false] must not grant thinking or tools: %v", nothink.Capabilities)
	}
	if nothink.APICapabilities != nil {
		t.Fatalf("unsupported thinking recorded modes: %+v", nothink.APICapabilities)
	}
	if nothink.ContextWindow != 1024 || nothink.CapabilitySource != "live" {
		t.Fatalf("nothink model = source %q context %d", nothink.CapabilitySource, nothink.ContextWindow)
	}

	embed := findModel(t, cat, "fixture-embed:1b")
	if embed.HasCapability(modelprovider.CapStreaming) || embed.HasCapability(modelprovider.CapToolUse) || embed.HasCapability(modelprovider.CapVision) {
		t.Fatalf("embedding model must not gain streaming, tool use, or vision: %v", embed.Capabilities)
	}
	if embed.CapabilitySource != "live" || embed.ContextWindow != 2048 {
		t.Fatalf("embed model = source %q context %d", embed.CapabilitySource, embed.ContextWindow)
	}
}

func TestSnapshot_OllamaShowFailureLeavesThatModelUnknown(t *testing.T) {
	doer := &catalogDoer{
		tags: readTestdata(t, "ollama_tags_show_failure.json"),
		show: loadShow(t, map[string]string{
			"fixture-absent:1b": "ollama_show_absent.json",
			"fixture-tools:1b":  "ollama_show_tools.json",
		}),
		showStatus: map[string]int{"fixture-down:1b": http.StatusInternalServerError},
	}
	s := openWith(t, doer, map[string]string{"ollama_url": "http://ollama.invalid"})
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(cat.Models) != 3 {
		t.Fatalf("models = %d, want 3 (a failed show must not drop the list)", len(cat.Models))
	}
	for _, ref := range []string{"fixture-down:1b", "fixture-absent:1b"} {
		m := findModel(t, cat, ref)
		if m.CapabilitySource != "unknown" {
			t.Fatalf("%s source = %q, want unknown", ref, m.CapabilitySource)
		}
		if m.Capabilities != nil {
			t.Fatalf("%s caps = %v, want unknown rather than an empty or invented set", ref, m.Capabilities)
		}
		if m.HasCapability(modelprovider.CapToolUse) || m.HasCapability(modelprovider.CapVision) || m.ContextWindow != 0 {
			t.Fatalf("%s must not become capable from a failed or empty show: %+v", ref, m)
		}
	}
	ok := findModel(t, cat, "fixture-tools:1b")
	if !ok.HasCapability(modelprovider.CapToolUse) || ok.CapabilitySource != "live" {
		t.Fatalf("sibling model lost its show evidence: source %q caps %v", ok.CapabilitySource, ok.Capabilities)
	}
}

func TestSnapshot_VLLMModelHasNoToolOrVisionClaim(t *testing.T) {
	doer := &catalogDoer{
		tags: readTestdata(t, "ollama_tags_completion.json"),
		vllm: readTestdata(t, "vllm_models.json"),
		show: loadShow(t, map[string]string{
			"vision-tools:latest": "ollama_show_completion.json",
		}),
	}
	s := openWith(t, doer, map[string]string{
		"ollama_url": "http://ollama.invalid",
		"vllm_url":   "http://vllm.invalid",
	})
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	m := findModel(t, cat, "meta-llama/Llama-3.1-8B-Instruct")
	if m.ProviderRef != modelprovider.ProviderVLLM {
		t.Fatalf("provider = %q", m.ProviderRef)
	}
	if m.HasCapability(modelprovider.CapToolUse) || m.HasCapability(modelprovider.CapVision) {
		t.Fatalf("vLLM list has no per-model capability metadata; caps = %v", m.Capabilities)
	}
	if m.HasCapability(modelprovider.CapStreaming) {
		t.Fatalf("vLLM streaming is not for every listed model; caps = %v", m.Capabilities)
	}
	if m.CapabilitySource != "unknown" || m.Capabilities != nil || m.ContextWindow != 0 {
		t.Fatalf("vLLM model = source %q context %d caps %v", m.CapabilitySource, m.ContextWindow, m.Capabilities)
	}
}

func TestSnapshot_StreamingClaimFollowsServerAPI(t *testing.T) {
	doer := &catalogDoer{
		tags: readTestdata(t, "ollama_tags_completion.json"),
		vllm: readTestdata(t, "vllm_models.json"),
		show: loadShow(t, map[string]string{
			"vision-tools:latest": "ollama_show_completion.json",
		}),
	}
	s := openWith(t, doer, map[string]string{
		"ollama_url": "http://ollama.invalid",
		"vllm_url":   "http://vllm.invalid",
	})
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	ollama := findModel(t, cat, "vision-tools:latest")
	if !ollama.HasCapability(modelprovider.CapStreaming) {
		t.Fatalf("Ollama generate and chat stream by default; caps = %v", ollama.Capabilities)
	}
	vllm := findModel(t, cat, "meta-llama/Llama-3.1-8B-Instruct")
	if vllm.HasCapability(modelprovider.CapStreaming) {
		t.Fatalf("vLLM must not get a streaming claim from the model list; caps = %v", vllm.Capabilities)
	}
}

// blockingShowDoer holds each show call until release is closed, so the test
// can see how many shows run at once.
type blockingShowDoer struct {
	tags     []byte
	show     []byte
	release  <-chan struct{}
	inflight atomic.Int32
	max      atomic.Int32
	posts    atomic.Int32
}

func (d *blockingShowDoer) Do(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/api/tags" {
		return textResponse(http.StatusOK, string(d.tags)), nil
	}
	if req.URL.Path != "/api/show" {
		return textResponse(http.StatusNotFound, `{}`), nil
	}
	_, _ = io.Copy(io.Discard, req.Body)
	cur := d.inflight.Add(1)
	for {
		m := d.max.Load()
		if cur <= m || d.max.CompareAndSwap(m, cur) {
			break
		}
	}
	defer d.inflight.Add(-1)
	select {
	case <-d.release:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	d.posts.Add(1)
	return textResponse(http.StatusOK, string(d.show)), nil
}

func TestSnapshot_OllamaShowRequestsStayBounded(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	doer := &blockingShowDoer{
		tags:    readTestdata(t, "ollama_tags_show_bound.json"),
		show:    readTestdata(t, "ollama_show_completion.json"),
		release: release,
	}
	go func() {
		defer unblock()
		deadline := time.After(time.Second)
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-deadline:
				return
			case <-tick.C:
				if doer.inflight.Load() >= 4 {
					time.Sleep(200 * time.Millisecond)
					return
				}
			}
		}
	}()

	s := openWith(t, doer, map[string]string{"ollama_url": "http://ollama.invalid"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cat, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(cat.Models) != 6 {
		t.Fatalf("models = %d, want 6", len(cat.Models))
	}
	for _, m := range cat.Models {
		if m.HasCapability(modelprovider.CapToolUse) {
			t.Fatalf("%s reported tool use from a completion-only show: %v", m.Ref, m.Capabilities)
		}
	}
	if got := doer.posts.Load(); got != 6 {
		t.Fatalf("show requests = %d, want one per model", got)
	}
	if got := doer.max.Load(); got > 4 {
		t.Fatalf("show requests in flight = %d, want at most 4", got)
	}
}
