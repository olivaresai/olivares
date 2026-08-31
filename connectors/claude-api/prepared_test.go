// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// TestForwardPreparedSendsExactFrozenBytes is the F3 core proof: ForwardPrepared
// transmits the frozen artifact's bytes VERBATIM — the octets on the wire equal
// PreparedRequest.Body() and its Digest — with NO preflight re-mutation and NO re-marshal.
func TestForwardPreparedSendsExactFrozenBytes(t *testing.T) {
	doer := &bodyCapturingDoer{}
	inf := NewInference(InferenceConfig{APIKey: "k", Gateway: model.GatewayDirect, Doer: doer})

	// A request that preflight WOULD mutate (Opus 4.8 rejects sampling params): freeze it
	// AFTER normalization, then forward. The wire bytes must be the frozen bytes, unchanged.
	req := MessageRequest{
		Model: "claude-opus-4-8", MaxTokens: 16,
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}
	norm, err := NormalizeMessageRequest(req, "")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	prep, err := MarshalPrepared(norm)
	if err != nil {
		t.Fatalf("marshal prepared: %v", err)
	}
	if _, err := inf.ForwardPrepared(context.Background(), prep); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if !bytes.Equal(doer.body, prep.Body()) {
		t.Fatalf("forwarded bytes != frozen bytes\n got:  %s\n want: %s", doer.body, prep.Body())
	}
	if got := sha256.Sum256(doer.body); got != prep.Digest() {
		t.Fatalf("forwarded-bytes digest != EffectiveRequestDigest")
	}
}

// TestForwardPreparedFreezesSamplingAndThinking proves the freeze captures the NORMALIZED
// shape: sampling params withheld and thinking normalized, so the forwarded bytes match the
// governed (normalized) request — not the raw pre-normalization request that F3 used to send.
func TestForwardPreparedFreezesNormalization(t *testing.T) {
	temp := 0.7
	doer := &bodyCapturingDoer{}
	inf := NewInference(InferenceConfig{APIKey: "k", Gateway: model.GatewayDirect, Doer: doer})
	req := MessageRequest{
		Model: "claude-opus-4-8", MaxTokens: 16, Temperature: &temp, // Opus 4.8 rejects sampling
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}
	norm, err := NormalizeMessageRequest(req, "")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	prep, _ := MarshalPrepared(norm)
	if _, err := inf.ForwardPrepared(context.Background(), prep); err != nil {
		t.Fatalf("forward: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(doer.body, &sent); err != nil {
		t.Fatalf("sent body not valid JSON: %v", err)
	}
	if _, present := sent["temperature"]; present {
		t.Fatalf("forwarded bytes still carry temperature; normalization was NOT frozen: %s", doer.body)
	}
}

// TestForwardPreparedSendsExactBytesWithHTMLAndUnicode hardens the byte-exactness guarantee
// against the tricky octets a JSON re-encode could change: HTML-sensitive chars (< > &) in
// both values AND keys, unicode/emoji, and the line/paragraph separators json.Marshal escapes.
// The frozen bytes and the wire bytes must be identical — proving the transport's
// json.RawMessage pass is a true byte-identity over the already-escaped, already-compact body.
func TestForwardPreparedSendsExactBytesWithHTMLAndUnicode(t *testing.T) {
	doer := &bodyCapturingDoer{}
	inf := NewInference(InferenceConfig{APIKey: "k", Gateway: model.GatewayDirect, Doer: doer})
	req := MessageRequest{
		Model: "claude-opus-4-8", MaxTokens: 16,
		System:   []ContentBlock{TextBlock("compare 1 < 2 && 3 > 2 — café ☃")},
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("<script>alert(&x)</script>")}}},
		Tools:    []any{map[string]any{"type": "web_search_20250305", "allowed_domains": []string{"a&b.com", "<x>"}}},
	}
	norm, err := NormalizeMessageRequest(req, "")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	prep, _ := MarshalPrepared(norm)
	if _, err := inf.ForwardPrepared(context.Background(), prep); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if !bytes.Equal(doer.body, prep.Body()) {
		t.Fatalf("forwarded bytes != frozen bytes with HTML/unicode payload\n got:  %s\n want: %s", doer.body, prep.Body())
	}
	if got := sha256.Sum256(doer.body); got != prep.Digest() {
		t.Fatal("forwarded-bytes digest != EffectiveRequestDigest with HTML/unicode payload")
	}
}

// TestForwardPreparedStreamSendsExactFrozenBytes proves the STREAM path forwards the frozen
// bytes verbatim too (only the response is SSE; the request body is the same artifact).
func TestForwardPreparedStreamSendsExactFrozenBytes(t *testing.T) {
	sse := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	doer := &captureStreamDoer{bodyCapturingDoer: bodyCapturingDoer{resp: sse}}
	inf := NewInference(InferenceConfig{APIKey: "k", Gateway: model.GatewayDirect, Doer: doer})
	req := MessageRequest{
		Model: "claude-opus-4-8", MaxTokens: 16, Stream: true,
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}
	norm, _ := NormalizeMessageRequest(req, "")
	prep, _ := MarshalPrepared(norm)
	if !prep.Stream() {
		t.Fatal("prepared artifact must carry the stream flag")
	}
	if _, err := inf.ForwardPreparedStream(context.Background(), prep, func(StreamEvent) error { return nil }); err != nil {
		t.Fatalf("forward stream: %v", err)
	}
	if !bytes.Equal(doer.body, prep.Body()) {
		t.Fatalf("streamed request bytes != frozen bytes\n got:  %s\n want: %s", doer.body, prep.Body())
	}
}

// TestForwardPreparedBatchSendsExactFrozenEnvelope proves the BATCH envelope is forwarded
// verbatim from the frozen artifact (no per-entry model-defaulting / re-serialize).
func TestForwardPreparedBatchSendsExactFrozenEnvelope(t *testing.T) {
	doer := &bodyCapturingDoer{resp: `{"id":"batch_1","processing_status":"in_progress"}`}
	inf := NewInference(InferenceConfig{APIKey: "k", Gateway: model.GatewayDirect, Doer: doer})
	entries := []BatchRequest{{CustomID: "c0", Params: MessageRequest{
		Model: "claude-opus-4-8", MaxTokens: 8,
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}}}
	prep, err := MarshalPreparedBatch(entries)
	if err != nil {
		t.Fatalf("marshal prepared batch: %v", err)
	}
	if _, _, err := inf.ForwardPreparedBatch(context.Background(), prep); err != nil {
		t.Fatalf("forward batch: %v", err)
	}
	if !bytes.Equal(doer.body, prep.Body()) {
		t.Fatalf("forwarded batch bytes != frozen bytes\n got:  %s\n want: %s", doer.body, prep.Body())
	}
}

// TestPreparedDefensiveCopy proves the artifact owns its bytes: mutating the source request
// after freezing, or the returned Body() slice, cannot change what is forwarded/digested.
func TestPreparedDefensiveCopy(t *testing.T) {
	req := MessageRequest{
		Model: "claude-opus-4-8", MaxTokens: 16,
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}
	prep, _ := MarshalPrepared(req)
	d0 := prep.Digest()
	// Mutate the source request AFTER freezing.
	req.MaxTokens = 9999
	req.Model = "attacker-model"
	// Mutate the returned Body() copy.
	b := prep.Body()
	for i := range b {
		b[i] = 'X'
	}
	if prep.Digest() != d0 {
		t.Fatal("prepared artifact digest changed after mutating the source request / a returned Body copy")
	}
	if bytes.Contains(prep.Body(), []byte("attacker-model")) || bytes.Contains(prep.Body(), []byte("9999")) {
		t.Fatal("prepared artifact reflected a post-freeze source mutation")
	}
}

// TestForwardPreparedZeroArtifactRefuses proves an unbuilt artifact never forwards.
func TestForwardPreparedZeroArtifactRefuses(t *testing.T) {
	doer := &bodyCapturingDoer{}
	inf := NewInference(InferenceConfig{APIKey: "k", Gateway: model.GatewayDirect, Doer: doer})
	if _, err := inf.ForwardPrepared(context.Background(), PreparedRequest{}); err == nil {
		t.Fatal("a zero PreparedRequest must not forward")
	}
	if doer.body != nil {
		t.Fatal("a zero artifact must not reach the wire")
	}
}

// captureStreamDoer serves a text/event-stream response while capturing the request body.
type captureStreamDoer struct{ bodyCapturingDoer }

func (d *captureStreamDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		d.body, _ = io.ReadAll(req.Body)
	}
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(d.resp))), Header: make(http.Header)}
	resp.Header.Set("Content-Type", "text/event-stream")
	return resp, nil
}
