// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrock

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// A model-invocation-log record carries the raw prompt/completion in the body fields.
// This sentinel lives in every fixture body so a test can prove it NEVER reaches an
// emitted observation (minimal data — bodies are not even deserialized).
const bodySentinel = "TOP-SECRET-PAYLOAD"

// invocationLogJSON builds one COMPACT (single-line) model-invocation-log record JSON
// with the bodies present (so the minimal-data guarantee is actually exercised, and the
// record is valid both inside a JSON array and as one NDJSON line).
func invocationLogJSON(modelID, arn string, in, out int64) string {
	rec := map[string]any{
		"schemaType": "ModelInvocationLog", "schemaVersion": "1.0",
		"timestamp": "2026-06-05T10:00:00Z", "accountId": "123456789012", "region": "us-east-1",
		"requestId": "req", "operation": "Converse", "modelId": modelID,
		"identity": map[string]any{"arn": arn},
		"input": map[string]any{
			"inputContentType": "application/json",
			"inputBodyJson":    map[string]any{"prompt": bodySentinel},
			"inputTokenCount":  in,
		},
		"output": map[string]any{
			"outputContentType": "application/json",
			"outputBodyJson":    map[string]any{"text": bodySentinel},
			"outputTokenCount":  out,
		},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeGzFile(t *testing.T, path, content string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(content)); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newUsageFilesSource(t *testing.T, path string) *Source {
	t.Helper()
	s := New()
	// Path-only: no credentials required (local I/O).
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgUsageLogPath: path,
		cfgRegion:       "us-east-1",
		cfgAccountID:    "123456789012",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func costByModel(cs []model.CostSample) map[string]model.CostSample {
	m := make(map[string]model.CostSample, len(cs))
	for _, c := range cs {
		m[c.ModelRef] = c
	}
	return m
}

// TestUsageFiles_S3_NonClaude proves the connector parses S3-delivered model-invocation
// logs (gzipped JSON array AND plain NDJSON) for NON-Claude vendors and emits a usage
// CostSample per record with the right tokens/gateway/provenance — and never leaks the
// model input/output bodies (minimal data).
func TestUsageFiles_S3_NonClaude(t *testing.T) {
	dir := t.TempDir()

	// File 1: gzipped JSON ARRAY — Titan (bare → mantle) + Llama via CRIS (us. → legacy)
	// + a no-modelId record that the ARRAY path must drop (it has no usable() pre-filter,
	// so usageSample must reject it).
	arr := "[" +
		invocationLogJSON("amazon.titan-text-v1", "arn:aws:sts::123456789012:assumed-role/AppRole/sess-titan", 120, 45) + "," +
		invocationLogJSON("us.meta.llama3-70b-instruct-v1:0", "arn:aws:sts::123456789012:assumed-role/AppRole/sess-llama", 200, 80) + "," +
		invocationLogJSON("", "arn:aws:sts::123456789012:assumed-role/AppRole/sess-arr-nomodel", 9, 9) +
		"]"
	writeGzFile(t, filepath.Join(dir, "a-batch.json.gz"), arr)

	openAIRecord, err := os.ReadFile(filepath.Join("testdata", "openai_gpt55_usage.ndjson"))
	if err != nil {
		t.Fatalf("read openai_gpt55_usage.ndjson: %v", err)
	}

	// File 2: plain NDJSON — Mistral (mantle) + Claude (mantle) + OpenAI-on-Bedrock
	// (mantle) + a zero-token record (skipped) + a record with no modelId (skipped).
	ndjson := strings.Join([]string{
		invocationLogJSON("mistral.mistral-large-2402-v1:0", "arn:aws:sts::123456789012:assumed-role/AppRole/sess-mistral", 50, 10),
		invocationLogJSON("anthropic.claude-opus-4-8", "arn:aws:sts::123456789012:assumed-role/AppRole/sess-claude", 1000, 500),
		strings.TrimSpace(string(openAIRecord)),
		invocationLogJSON("amazon.titan-embed-text-v2:0", "arn:aws:sts::123456789012:assumed-role/AppRole/sess-zero", 0, 0),
		invocationLogJSON("", "arn:aws:sts::123456789012:assumed-role/AppRole/sess-nomodel", 5, 5),
	}, "\n")
	writeFile(t, filepath.Join(dir, "b-stream.json"), ndjson)

	s := newUsageFilesSource(t, dir)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	cs := sink.costs()
	if len(cs) != 5 {
		t.Fatalf("emitted %d usage samples, want 5 (titan, llama, mistral, claude, openai; zero-token + no-model skipped): %+v", len(cs), cs)
	}

	byModel := costByModel(cs)

	// Titan: bare vendor.model → mantle; tokens + estimated provenance + zero cost.
	titan, ok := byModel["amazon.titan-text-v1"]
	if !ok {
		t.Fatal("missing titan sample")
	}
	if titan.Gateway != model.GatewayBedrockMantle {
		t.Errorf("titan gateway = %q, want bedrock-mantle", titan.Gateway)
	}
	if titan.InputTokens != 120 || titan.OutputTokens != 45 {
		t.Errorf("titan tokens = %d/%d, want 120/45", titan.InputTokens, titan.OutputTokens)
	}
	if titan.CostMicroUSD != 0 {
		t.Errorf("titan cost = %d, want 0 (no billed cost in invocation logs)", titan.CostMicroUSD)
	}
	if titan.Provenance != model.ProvenanceEstimated {
		t.Errorf("titan provenance = %q, want estimated", titan.Provenance)
	}
	if titan.ProviderRef != ProviderBedrock {
		t.Errorf("titan provider = %q, want bedrock", titan.ProviderRef)
	}
	if titan.Actor == "" || !strings.Contains(titan.Actor, "AppRole") {
		t.Errorf("titan actor = %q, want the caller principal arn", titan.Actor)
	}
	// OccurredAt comes from the record's own timestamp (2026-06-05T10:00:00Z), not the pass time.
	if titan.OccurredAt.Format(time.RFC3339) != "2026-06-05T10:00:00Z" {
		t.Errorf("titan OccurredAt = %v, want the record timestamp 2026-06-05T10:00:00Z", titan.OccurredAt)
	}

	// Llama via CRIS profile → legacy surface.
	llama, ok := byModel["us.meta.llama3-70b-instruct-v1:0"]
	if !ok || llama.Gateway != model.GatewayBedrockLegacy {
		t.Errorf("llama sample = %+v ok=%v, want bedrock-legacy", llama, ok)
	}

	openai, ok := byModel["openai.gpt-5.5"]
	if !ok {
		t.Fatal("missing OpenAI-on-Bedrock sample")
	}
	if openai.ModelRef != "openai.gpt-5.5" {
		t.Errorf("openai ModelRef = %q, want openai.gpt-5.5", openai.ModelRef)
	}
	if openai.Gateway != model.GatewayBedrockMantle {
		t.Errorf("openai gateway = %q, want bedrock-mantle", openai.Gateway)
	}

	// Minimal data: the raw bodies must NEVER appear in any emitted field.
	for _, c := range cs {
		blob, _ := json.Marshal(c)
		if strings.Contains(string(blob), bodySentinel) {
			t.Fatalf("model input/output body leaked into a CostSample: %s", blob)
		}
	}
}

// --- CloudWatch Logs (FilterLogEvents) ------------------------------------------

// cwFixture serves FilterLogEvents pages: pages[i] is the JSON returned on the i-th call.
// When loop is true it ALWAYS returns an EMPTY events page WITH a nextToken (to exercise
// the consecutive-empty-page bound).
type cwFixture struct {
	pages   []string
	loop    bool
	calls   int
	methods []string
	targets []string
}

func (f *cwFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.methods = append(f.methods, r.Method)
	f.targets = append(f.targets, r.Header.Get("X-Amz-Target"))
	i := f.calls
	f.calls++
	w.Header().Set("Content-Type", contentTypeAWSJSON)
	if f.loop {
		_, _ = w.Write([]byte(`{"events":[],"nextToken":"always-more"}`))
		return
	}
	if i < len(f.pages) {
		_, _ = w.Write([]byte(f.pages[i]))
		return
	}
	_, _ = w.Write([]byte(`{"events":[]}`))
}

func cwEvent(modelID string, in, out int64) string {
	msg := invocationLogJSON(modelID, "arn:aws:sts::123456789012:assumed-role/AppRole/sess", in, out)
	b, _ := json.Marshal(msg) // embed the record as a JSON string in `message`
	return `{"timestamp":1717581600000,"message":` + string(b) + `}`
}

func newCloudWatchSource(t *testing.T, srvURL string, maxEvents string) *Source {
	t.Helper()
	s := New()
	settings := map[string]string{
		cfgUsageLogGroup:  "/aws/bedrock/modelinvocations",
		cfgCWLogsEndpoint: srvURL,
		cfgRegion:         "us-east-1",
		cfgAccountID:      "123456789012",
	}
	if maxEvents != "" {
		settings[cfgMaxEvents] = maxEvents
	}
	for k, v := range testCreds {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// TestUsageCloudWatch_Pagination proves the CloudWatch source parses each event's
// message into a usage sample and paginates by nextToken — including an EMPTY page that
// still carries a nextToken (pagination ends only on an absent token).
func TestUsageCloudWatch_Pagination(t *testing.T) {
	fx := &cwFixture{pages: []string{
		// page 1: one event + a nextToken.
		`{"events":[` + cwEvent("amazon.nova-pro-v1:0", 300, 100) + `],"nextToken":"t1"}`,
		// page 2: EMPTY events but STILL a nextToken — must keep going, not stop here.
		`{"events":[],"nextToken":"t2"}`,
		// page 3: one event, no nextToken — pagination done.
		`{"events":[` + cwEvent("cohere.command-r-v1:0", 40, 20) + `]}`,
	}}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newCloudWatchSource(t, srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	cs := sink.costs()
	if len(cs) != 2 {
		t.Fatalf("emitted %d samples, want 2 (nova + cohere across 3 pages): %+v", len(cs), cs)
	}
	if fx.calls != 3 {
		t.Fatalf("made %d FilterLogEvents calls, want 3 (stopped only on absent nextToken)", fx.calls)
	}
	for _, m := range fx.methods {
		if m != http.MethodPost {
			t.Fatalf("non-POST FilterLogEvents request: %s", m)
		}
	}
	if fx.targets[0] != cwLogsTarget {
		t.Fatalf("X-Amz-Target = %q, want %q", fx.targets[0], cwLogsTarget)
	}
	byModel := costByModel(cs)
	if nova, ok := byModel["amazon.nova-pro-v1:0"]; !ok || nova.InputTokens != 300 || nova.Gateway != model.GatewayBedrockMantle {
		t.Errorf("nova sample = %+v ok=%v", nova, ok)
	}
}

// TestUsageCloudWatch_MaxEventsPartial proves that stopping at the max_events bound with
// a cursor still pending emits an honest Low partial-coverage finding (no silent caps).
func TestUsageCloudWatch_MaxEventsPartial(t *testing.T) {
	fx := &cwFixture{pages: []string{
		`{"events":[` + cwEvent("amazon.titan-text-v1", 10, 5) + `],"nextToken":"more"}`,
	}}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newCloudWatchSource(t, srv.URL, "1") // cap at 1 event; cursor still pending
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	partial := false
	for _, f := range postureFindings(sink.findings()) {
		if f.Severity == model.SeverityLow && strings.Contains(f.Title, "PARTIAL") {
			partial = true
		}
	}
	if !partial {
		t.Fatalf("expected a Low partial-coverage finding when max_events is hit with a pending cursor, got %+v", sink.findings())
	}
}

// TestUsageCloudWatch_PersistentEmptyPages proves a run of empty events pages that each
// still carry a nextToken (a documented FilterLogEvents behavior) does NOT loop forever:
// the connector stops at the consecutive-empty-page bound and emits an honest Low
// partial-coverage finding (no silent caps).
func TestUsageCloudWatch_PersistentEmptyPages(t *testing.T) {
	fx := &cwFixture{loop: true} // every page is empty + has a nextToken
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newCloudWatchSource(t, srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if fx.calls != cloudWatchMaxEmptyPages {
		t.Fatalf("made %d calls, want cloudWatchMaxEmptyPages=%d (loop is bounded)", fx.calls, cloudWatchMaxEmptyPages)
	}
	partial := false
	for _, f := range postureFindings(sink.findings()) {
		if f.Severity == model.SeverityLow && f.SubjectKind == subjectUsage && strings.Contains(f.Title, "PARTIAL") {
			partial = true
		}
	}
	if !partial {
		t.Fatalf("expected a Low partial-coverage usage finding at the empty-page bound, got %+v", sink.findings())
	}
}

// TestUsageCloudWatch_EventTimeFallback proves that when a record carries NO timestamp,
// OccurredAt falls back to the CloudWatch event timestamp (epoch ms), not the pass time.
func TestUsageCloudWatch_EventTimeFallback(t *testing.T) {
	rec := map[string]any{
		"schemaType": "ModelInvocationLog", "modelId": "amazon.titan-text-v1",
		"identity": map[string]any{"arn": "arn:aws:sts::123456789012:assumed-role/AppRole/sess"},
		"input":    map[string]any{"inputTokenCount": 10},
		"output":   map[string]any{"outputTokenCount": 5},
	} // deliberately no "timestamp"
	rb, _ := json.Marshal(rec)
	msg, _ := json.Marshal(string(rb))
	const evtMS = int64(1717581600000) // 2024-06-05T10:00:00Z
	page := `{"events":[{"timestamp":1717581600000,"message":` + string(msg) + `}]}`
	fx := &cwFixture{pages: []string{page}}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newCloudWatchSource(t, srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	cs := sink.costs()
	if len(cs) != 1 {
		t.Fatalf("want 1 sample, got %d", len(cs))
	}
	if !cs[0].OccurredAt.Equal(time.UnixMilli(evtMS).UTC()) {
		t.Fatalf("OccurredAt = %v, want event-time fallback %v", cs[0].OccurredAt, time.UnixMilli(evtMS).UTC())
	}
}

// TestParseInvocationLogs proves all three documented shapes parse, including the
// multi-line pretty-printed single object (the fallback branch).
func TestParseInvocationLogs(t *testing.T) {
	rec := invocationLogJSON("amazon.titan-text-v1", "arn:aws:sts::1:assumed-role/r/s", 10, 5)

	if got := parseInvocationLogs([]byte("[" + rec + "," + rec + "]")); len(got) != 2 {
		t.Errorf("array: got %d records, want 2", len(got))
	}
	if got := parseInvocationLogs([]byte(rec + "\n" + rec)); len(got) != 2 {
		t.Errorf("NDJSON: got %d records, want 2", len(got))
	}
	if got := parseInvocationLogs([]byte(rec)); len(got) != 1 {
		t.Errorf("compact single: got %d records, want 1", len(got))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(rec), &m); err != nil {
		t.Fatal(err)
	}
	pretty, _ := json.MarshalIndent(m, "", "  ") // multi-line single object → fallback branch
	got := parseInvocationLogs(pretty)
	if len(got) != 1 || got[0].ModelID != "amazon.titan-text-v1" {
		t.Errorf("pretty single object: got %+v, want exactly one titan record", got)
	}
	if parseInvocationLogs([]byte("not json")) != nil {
		t.Error("garbage input must yield nil")
	}
}

// TestUsageCloudWatch_ReadFailure proves a CloudWatch read failure yields one health
// finding (hashed detail), never a fabricated usage sample.
func TestUsageCloudWatch_ReadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	s := newCloudWatchSource(t, srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.costs()) != 0 {
		t.Fatal("a read failure must not fabricate a usage sample")
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].Kind != "health" || fs[0].SubjectKind != subjectUsage {
		t.Fatalf("expected one usage health finding, got %+v", fs)
	}
}
