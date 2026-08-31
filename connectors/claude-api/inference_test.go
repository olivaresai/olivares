// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// routeDoer returns a canned response per (method, path-prefix) so the inference
// client is exercised against recorded Messages/Batches/Files shapes with no live
// network call. It also records the last request body for assertions.
type routeDoer struct {
	routes   map[string]string // "METHOD path" -> JSON/text body
	status   map[string]int
	lastBody string
	lastURL  string
	lastBeta string
}

func (d *routeDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		d.lastBody = string(b)
	}
	d.lastURL = req.URL.String()
	d.lastBeta = req.Header.Get("anthropic-beta")
	key := req.Method + " " + req.URL.Path
	body := ""
	bestLen := -1
	for k, v := range d.routes {
		if strings.HasPrefix(key, k) && len(k) > bestLen {
			body, bestLen = v, len(k)
		}
	}
	st := http.StatusOK
	if d.status != nil {
		if s, ok := d.status[key]; ok {
			st = s
		}
	}
	return &http.Response{StatusCode: st, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

func newInf(d *routeDoer, gw model.Gateway) *Inference {
	return NewInference(InferenceConfig{APIKey: "k-inference", Gateway: gw, DefaultModel: "claude-opus-4-8", Doer: d})
}

func TestCreateMessage_ParsesUsageAndCacheTokens(t *testing.T) {
	d := &routeDoer{routes: map[string]string{
		"POST /v1/messages": `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8",
			"stop_reason":"end_turn","content":[{"type":"text","text":"hello world"}],
			"usage":{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":248,
			"cache_read_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":148,"ephemeral_1h_input_tokens":100},
			"service_tier":"standard"}}`,
	}}
	inf := newInf(d, model.GatewayBedrockMantle)
	resp, err := inf.CreateMessage(context.Background(), MessageRequest{
		MaxTokens: 64,
		System:    []ContentBlock{CachedTextBlock("stable system", "")},
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if resp.Text() != "hello world" {
		t.Errorf("Text() = %q", resp.Text())
	}
	// Request defaulted the model and carried the cache_control breakpoint.
	if !strings.Contains(d.lastBody, `"claude-opus-4-8"`) {
		t.Errorf("model not defaulted into body: %s", d.lastBody)
	}
	if !strings.Contains(d.lastBody, `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("cache_control not sent on system block: %s", d.lastBody)
	}

	// Usage mapping: uncached input verbatim, per-TTL cache split, read tokens, gateway stamp.
	u := inf.UsageFor(resp, "sess-1", time.Unix(0, 0).UTC())
	if u.InputTokens != 100 || u.OutputTokens != 20 {
		t.Errorf("tokens = in %d out %d", u.InputTokens, u.OutputTokens)
	}
	if u.CacheCreation5mTokens != 148 || u.CacheCreation1hTokens != 100 || u.CacheReadTokens != 50 {
		t.Errorf("cache split wrong: 5m=%d 1h=%d read=%d", u.CacheCreation5mTokens, u.CacheCreation1hTokens, u.CacheReadTokens)
	}
	if u.Gateway != model.GatewayBedrockMantle {
		t.Errorf("gateway = %q, want bedrock-mantle", u.Gateway)
	}
	if u.Provenance != model.ProvenanceEstimated {
		t.Errorf("provenance = %q, want estimated", u.Provenance)
	}
	if u.ProviderRef == "" || u.ModelRef != "claude-opus-4-8" {
		t.Errorf("provider/model ref wrong: %q/%q", u.ProviderRef, u.ModelRef)
	}
}

func TestJudge_ParsesVerdict(t *testing.T) {
	d := &routeDoer{routes: map[string]string{
		"POST /v1/messages": `{"id":"msg_2","type":"message","role":"assistant","model":"claude-opus-4-8",
			"content":[{"type":"text","text":"Here is my verdict: {\"score\":0.9,\"passed\":true,\"reason\":\"meets criterion\"}"}],
			"usage":{"input_tokens":50,"output_tokens":10}}`,
	}}
	inf := newInf(d, model.GatewayDirect)
	res, err := inf.Judge(context.Background(), JudgeInput{Output: "the answer", Criterion: "is it correct"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if !res.Passed || res.Score != 0.9 || res.Reason != "meets criterion" {
		t.Errorf("verdict = %+v", res)
	}
	// The criterion + output are sent; the stable rubric is the cached system.
	if !strings.Contains(d.lastBody, "is it correct") || !strings.Contains(d.lastBody, "the answer") {
		t.Errorf("judge prompt missing criterion/output: %s", d.lastBody)
	}
	if strings.Contains(d.lastBody, `"temperature"`) {
		t.Errorf("temperature must not be sent (Opus 4.7+ rejects it): %s", d.lastBody)
	}
}

func TestParseJudgeVerdict_ClampAndErrors(t *testing.T) {
	// Out-of-range score clamps to [0,1].
	v, err := parseJudgeVerdict(`prose {"score":1.7,"passed":false,"reason":"x"} trailing`)
	if err != nil || v.Score != 1 {
		t.Errorf("clamp failed: %+v err=%v", v, err)
	}
	v, err = parseJudgeVerdict(`{"score":-3,"passed":true,"reason":""}`)
	if err != nil || v.Score != 0 {
		t.Errorf("clamp-low failed: %+v err=%v", v, err)
	}
	// No JSON object => error (the scorer records outcome=error, never a silent pass).
	if _, err := parseJudgeVerdict("no json here"); err == nil {
		t.Errorf("want error on missing verdict")
	}
}

func TestBatches_SubmitPollResults(t *testing.T) {
	d := &routeDoer{routes: map[string]string{
		"POST /v1/messages/batches":   `{"id":"batch_1","type":"message_batch","processing_status":"in_progress"}`,
		"GET /v1/messages/batches/ba": `{"id":"batch_1","processing_status":"ended","results_url":"https://api.example.com/v1/messages/batches/batch_1/results"}`,
	}}
	inf := newInf(d, model.GatewayDirect)
	b, err := inf.CreateBatch(context.Background(), []BatchRequest{{CustomID: "c1", Params: MessageRequest{MaxTokens: 8, Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}}}}})
	if err != nil || b.ID != "batch_1" || b.ProcessingStatus != BatchInProgress {
		t.Fatalf("CreateBatch: %+v err=%v", b, err)
	}
	if !strings.Contains(d.lastBody, `"custom_id":"c1"`) || !strings.Contains(d.lastBody, `"claude-opus-4-8"`) {
		t.Errorf("batch body missing custom_id/defaulted model: %s", d.lastBody)
	}
	got, err := inf.GetBatch(context.Background(), "batch_1")
	if err != nil || got.ProcessingStatus != BatchEnded || got.ResultsURL == "" {
		t.Fatalf("GetBatch: %+v err=%v", got, err)
	}
	// Results JSONL.
	d.routes["GET /v1/messages/batches/batch_1/results"] = `{"custom_id":"c1","result":{"type":"succeeded","message":{"id":"msg_x"}}}
{"custom_id":"c2","result":{"type":"errored","error":{"type":"invalid_request"}}}`
	entries, err := inf.BatchResults(context.Background(), got)
	if err != nil {
		t.Fatalf("BatchResults: %v", err)
	}
	if len(entries) != 2 || entries[0].CustomID != "c1" || entries[0].Result.Type != "succeeded" || entries[1].Result.Type != "errored" {
		t.Errorf("results parse: %+v", entries)
	}
}

func TestUploadFile_MultipartAndBetaHeader(t *testing.T) {
	d := &routeDoer{routes: map[string]string{
		"POST /v1/files": `{"id":"file_abc","type":"file","filename":"d.txt","size_bytes":5}`,
	}}
	inf := newInf(d, model.GatewayDirect)
	fm, err := inf.UploadFile(context.Background(), "d.txt", []byte("hello"))
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if fm.ID != "file_abc" {
		t.Errorf("id = %q", fm.ID)
	}
	if d.lastBeta != filesBetaHeader {
		t.Errorf("beta header = %q, want %q", d.lastBeta, filesBetaHeader)
	}
	if !strings.Contains(d.lastBody, "hello") {
		t.Errorf("file content not in multipart body")
	}
}

// pagingFilesDoer serves GET /v1/files in two pages keyed on the after_id cursor, so the
// paginated reader is exercised across a real has_more boundary (routeDoer keys on path
// only and cannot distinguish the two pages).
type pagingFilesDoer struct {
	calls    int
	gotAfter []string
	gotBeta  string
}

func (d *pagingFilesDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	d.gotBeta = req.Header.Get("anthropic-beta")
	after := req.URL.Query().Get("after_id")
	d.gotAfter = append(d.gotAfter, after)
	body := ""
	switch after {
	case "":
		body = `{"data":[{"id":"file_1","type":"file","filename":"a.pdf","mime_type":"application/pdf","size_bytes":10,"created_at":"2026-01-01T00:00:00Z","downloadable":false},
			{"id":"file_2","type":"file","filename":"b.txt","mime_type":"text/plain","size_bytes":20,"created_at":"2026-01-02T00:00:00Z","scope":{"id":"sess_9","type":"session"}}],
			"first_id":"file_1","last_id":"file_2","has_more":true}`
	case "file_2":
		body = `{"data":[{"id":"file_3","type":"file","filename":"c.bin","mime_type":"application/octet-stream","size_bytes":30,"created_at":"2026-01-03T00:00:00Z"}],
			"first_id":"file_3","last_id":"file_3","has_more":false}`
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

func TestListAllFiles_FollowsCursorAndParsesFields(t *testing.T) {
	d := &pagingFilesDoer{}
	inf := NewInference(InferenceConfig{APIKey: "k-inference", Gateway: model.GatewayDirect, Doer: d})
	files, err := inf.ListAllFiles(context.Background(), "")
	if err != nil {
		t.Fatalf("ListAllFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("got %d files across pages, want 3 (pagination not followed)", len(files))
	}
	if d.calls != 2 {
		t.Errorf("upstream calls = %d, want 2 pages", d.calls)
	}
	// The second page MUST be requested with the prior page's last_id as the cursor.
	if len(d.gotAfter) != 2 || d.gotAfter[0] != "" || d.gotAfter[1] != "file_2" {
		t.Errorf("after_id cursors = %v, want [\"\", \"file_2\"]", d.gotAfter)
	}
	if d.gotBeta != filesBetaHeader {
		t.Errorf("beta header = %q, want %q", d.gotBeta, filesBetaHeader)
	}
	// Optional fields round-trip: downloadable=false on file_1, session scope on file_2.
	if files[0].Downloadable == nil || *files[0].Downloadable {
		t.Errorf("file_1 downloadable = %v, want non-nil false", files[0].Downloadable)
	}
	if files[1].Scope == nil || files[1].Scope.ID != "sess_9" || files[1].Scope.Type != "session" {
		t.Errorf("file_2 scope = %+v, want {sess_9 session}", files[1].Scope)
	}
}

func TestGetFile_ParsesMetadata(t *testing.T) {
	d := &routeDoer{routes: map[string]string{
		"GET /v1/files/file_abc": `{"id":"file_abc","type":"file","filename":"d.txt","mime_type":"text/plain","size_bytes":5,"created_at":"2026-01-01T00:00:00Z","downloadable":true}`,
	}}
	inf := newInf(d, model.GatewayDirect)
	fm, err := inf.GetFile(context.Background(), "file_abc")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if fm.ID != "file_abc" || fm.MimeType != "text/plain" || fm.SizeBytes != 5 {
		t.Errorf("metadata = %+v", fm)
	}
	if fm.Downloadable == nil || !*fm.Downloadable {
		t.Errorf("downloadable = %v, want non-nil true", fm.Downloadable)
	}
	if d.lastBeta != filesBetaHeader {
		t.Errorf("beta header = %q, want %q", d.lastBeta, filesBetaHeader)
	}
}

func TestDeleteFile_ConfirmationShapeAndBeta(t *testing.T) {
	d := &routeDoer{routes: map[string]string{
		"DELETE /v1/files/file_zzz": `{"id":"file_zzz","type":"file_deleted"}`,
	}}
	inf := newInf(d, model.GatewayDirect)
	del, err := inf.DeleteFile(context.Background(), "file_zzz")
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if del.ID != "file_zzz" || del.Type != "file_deleted" {
		t.Errorf("delete confirmation = %+v, want {file_zzz file_deleted}", del)
	}
	if !strings.HasSuffix(d.lastURL, "/v1/files/file_zzz") {
		t.Errorf("delete URL = %q, want .../v1/files/file_zzz", d.lastURL)
	}
	if d.lastBeta != filesBetaHeader {
		t.Errorf("beta header = %q, want %q", d.lastBeta, filesBetaHeader)
	}
}

func TestFiles_NotConfiguredFailsClosed(t *testing.T) {
	inf := &Inference{}
	if _, err := inf.ListFiles(context.Background(), ListFilesParams{}); err != ErrNotConfigured {
		t.Errorf("ListFiles want ErrNotConfigured, got %v", err)
	}
	if _, err := inf.GetFile(context.Background(), "file_1"); err != ErrNotConfigured {
		t.Errorf("GetFile want ErrNotConfigured, got %v", err)
	}
	if _, err := inf.DeleteFile(context.Background(), "file_1"); err != ErrNotConfigured {
		t.Errorf("DeleteFile want ErrNotConfigured, got %v", err)
	}
}

func TestInference_NotConfiguredFailsClosed(t *testing.T) {
	// No Doer/key path still constructs, but a default client backs it; verify the
	// explicit not-configured guard when the underlying client is nil.
	inf := &Inference{}
	if _, err := inf.CreateMessage(context.Background(), MessageRequest{Model: "m", MaxTokens: 1}); err != ErrNotConfigured {
		t.Errorf("want ErrNotConfigured, got %v", err)
	}
	if _, err := inf.Judge(context.Background(), JudgeInput{}); err != ErrNotConfigured {
		t.Errorf("judge want ErrNotConfigured, got %v", err)
	}
}
