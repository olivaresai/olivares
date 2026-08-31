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
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// proxyStubDoer is a fake upstream that records what credential the FORWARD carried and
// returns a canned response. It is the seam that proves no inbound-bearer passthrough.
type proxyStubDoer struct {
	status      int
	body        string
	contentType string
	calls       int
	gotAPIKey   string
	gotAuth     string
	gotBody     []byte
	gotAccept   string
}

func (s *proxyStubDoer) Do(req *http.Request) (*http.Response, error) {
	s.calls++
	s.gotAPIKey = req.Header.Get("x-api-key")
	s.gotAuth = req.Header.Get("Authorization")
	s.gotAccept = req.Header.Get("Accept")
	if req.Body != nil {
		s.gotBody, _ = io.ReadAll(req.Body)
	}
	h := http.Header{}
	if s.contentType != "" {
		h.Set("Content-Type", s.contentType)
	}
	return &http.Response{StatusCode: s.status, Body: io.NopCloser(strings.NewReader(s.body)), Header: h}, nil
}

type fakeDecider struct {
	decision    ProxyDecision
	verdict     ProxyResponseVerdict
	gotReq      MessageRequest
	gotBearer   string
	finalized   bool
	finalizeOut ProxyForwardResult

	// batch knobs: batchDecision is returned by AuthorizeBatch (Requests defaults to the
	// inbound entries when Allow); batchDenyAt, when >= 0, denies the entry at that index.
	batchDecision    ProxyBatchDecision
	batchDenyAt      int
	gotBatch         []BatchRequest
	batchFinalized   bool
	batchFinalizeOut ProxyBatchForwardResult
}

func (d *fakeDecider) Authorize(_ context.Context, req MessageRequest, bearer string) ProxyDecision {
	d.gotReq = req
	d.gotBearer = bearer
	dd := d.decision
	if dd.Allow {
		dd.Request = req // mirror the governed request (no rewrite in v1)
	}
	return dd
}

func (d *fakeDecider) Finalize(_ context.Context, _ any, out ProxyForwardResult) ProxyResponseVerdict {
	d.finalized = true
	d.finalizeOut = out
	return d.verdict
}

func (d *fakeDecider) AuthorizeBatch(_ context.Context, requests []BatchRequest, bearer string) ProxyBatchDecision {
	d.gotBatch = requests
	d.gotBearer = bearer
	// Per-entry deny-closed: deny the whole batch if any entry is the configured deny index.
	if d.batchDenyAt >= 0 {
		for i := range requests {
			if i == d.batchDenyAt {
				return ProxyBatchDecision{Allow: false, Status: http.StatusForbidden, ErrorType: "permission_error", Reason: "entry denied"}
			}
		}
	}
	dd := d.batchDecision
	if dd.Allow && dd.Requests == nil {
		dd.Requests = requests // mirror the governed entries (no rewrite in the fake)
	}
	return dd
}

func (d *fakeDecider) FinalizeBatch(_ context.Context, _ any, out ProxyBatchForwardResult) {
	d.batchFinalized = true
	d.batchFinalizeOut = out
}

type capAuditor struct{ events []ProxyAuditEvent }

func (c *capAuditor) Record(_ context.Context, ev ProxyAuditEvent) { c.events = append(c.events, ev) }

const okMessageJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8","stop_reason":"end_turn","content":[{"type":"text","text":"hello world"}],"usage":{"input_tokens":5,"output_tokens":2}}`

func newProxy(t *testing.T, doer *proxyStubDoer, dec ProxyDecider, aud ProxyAuditor) *MessagesProxy {
	t.Helper()
	inf := NewInference(InferenceConfig{
		BaseURL: "https://api.anthropic.com", APIKey: "OPERATOR-KEY",
		Gateway: model.GatewayDirect, Doer: doer,
	})
	return NewMessagesProxy(inf, dec, aud, nil)
}

func postMessages(t *testing.T, p *MessagesProxy, body, inboundKey string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	if inboundKey != "" {
		r.Header.Set("Authorization", "Bearer "+inboundKey)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

func postBatch(t *testing.T, p *MessagesProxy, body, inboundKey string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages/batches", strings.NewReader(body))
	if inboundKey != "" {
		r.Header.Set("Authorization", "Bearer "+inboundKey)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

// upstreamBatchJSON carries fields the connector's Batch struct does NOT model
// (request_counts, expires_at, cancel_initiated_at) so a relay test proves the proxy
// returns Anthropic's bytes VERBATIM, not a lossy re-marshal.
const upstreamBatchJSON = `{"id":"msgbatch_1","type":"message_batch","processing_status":"in_progress",` +
	`"request_counts":{"processing":2,"succeeded":0,"errored":0,"canceled":0,"expired":0},` +
	`"created_at":"2026-01-01T00:00:00Z","expires_at":"2026-01-02T00:00:00Z","cancel_initiated_at":null,"results_url":null}`

const twoEntryBatch = `{"requests":[` +
	`{"custom_id":"a","params":{"model":"claude-opus-4-8","max_tokens":8,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}},` +
	`{"custom_id":"b","params":{"model":"claude-opus-4-8","max_tokens":8,"messages":[{"role":"user","content":[{"type":"text","text":"yo"}]}]}}]}`

func TestBatchPathNowServedAndRelaysVerbatim(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: upstreamBatchJSON, contentType: "application/json"}
	dec := &fakeDecider{batchDecision: ProxyBatchDecision{Allow: true}, batchDenyAt: -1}
	p := newProxy(t, doer, dec, nil)

	w := postBatch(t, p, twoEntryBatch, "INBOUND-CALLER-KEY")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (batch path served, no longer 404); body=%s", w.Code, w.Body.String())
	}
	if doer.calls != 1 {
		t.Fatalf("upstream calls = %d, want 1 (batch forwarded)", doer.calls)
	}
	// Faithful relay: a field the connector's Batch struct drops MUST survive in the response.
	if !strings.Contains(w.Body.String(), `"request_counts"`) || !strings.Contains(w.Body.String(), `"expires_at"`) {
		t.Errorf("batch response was lossily re-marshaled (missing request_counts/expires_at): %s", w.Body.String())
	}
	// Credential separation holds on the batch path too.
	if doer.gotAPIKey != "OPERATOR-KEY" || strings.Contains(doer.gotAuth, "INBOUND") {
		t.Errorf("batch forward credential wrong: x-api-key=%q auth=%q", doer.gotAPIKey, doer.gotAuth)
	}
	if dec.gotBearer != "INBOUND-CALLER-KEY" {
		t.Errorf("decider bearer = %q, want INBOUND-CALLER-KEY", dec.gotBearer)
	}
	if !dec.batchFinalized || dec.batchFinalizeOut.Entries != 2 {
		t.Errorf("FinalizeBatch must run with Entries=2; finalized=%v entries=%d", dec.batchFinalized, dec.batchFinalizeOut.Entries)
	}
}

func TestBatchPerEntryDenyRejectsWholeBatchDenyClosed(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: upstreamBatchJSON, contentType: "application/json"}
	dec := &fakeDecider{batchDenyAt: 1} // second entry denied
	p := newProxy(t, doer, dec, nil)

	w := postBatch(t, p, twoEntryBatch, "k")

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (one denied entry denies the batch)", w.Code)
	}
	if doer.calls != 0 {
		t.Fatalf("upstream called %d times on a denied batch; must be 0 (nothing forwarded)", doer.calls)
	}
	if dec.batchFinalized {
		t.Error("FinalizeBatch must not run on a pre-forward batch deny")
	}
	if !strings.Contains(w.Body.String(), "\"type\":\"error\"") {
		t.Errorf("deny body is not an Anthropic error: %s", w.Body.String())
	}
}

func TestBatchEmptyRequestsRejected(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: upstreamBatchJSON}
	dec := &fakeDecider{batchDecision: ProxyBatchDecision{Allow: true}, batchDenyAt: -1}
	p := newProxy(t, doer, dec, nil)
	w := postBatch(t, p, `{"requests":[]}`, "k")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 on an empty batch", w.Code)
	}
	if doer.calls != 0 {
		t.Errorf("upstream called on an empty batch")
	}
}

func TestBatchNilDeciderDeniesClosed(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: upstreamBatchJSON}
	p := newProxy(t, doer, nil, nil)
	w := postBatch(t, p, twoEntryBatch, "k")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (no decider ⇒ deny-closed) on the batch path", w.Code)
	}
	if doer.calls != 0 {
		t.Errorf("upstream called with no decider wired on the batch path")
	}
}

func TestProxyUnknownPathIs404(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: okMessageJSON}
	dec := &fakeDecider{decision: ProxyDecision{Allow: true}, batchDenyAt: -1}
	p := newProxy(t, doer, dec, nil)
	r := httptest.NewRequest(http.MethodPost, "/v1/foo", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 on an unserved path", w.Code)
	}
}

// TestProxyForwardsPreparedBytesVerbatim is the F3 end-to-end proof at the proxy level:
// when the decider returns a frozen Prepared artifact, the exact frozen bytes reach the
// upstream — NOT a re-marshal of dec.Request. The frozen body deliberately differs from a
// naive marshal of dec.Request (extra field order / a governed value) so a re-marshal would
// be detected.
func TestProxyForwardsPreparedBytesVerbatim(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: okMessageJSON}
	gov := MessageRequest{
		Model: "claude-opus-4-8", MaxTokens: 16,
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}
	prep, err := MarshalPrepared(gov)
	if err != nil {
		t.Fatalf("marshal prepared: %v", err)
	}
	dec := &fakeDecider{decision: ProxyDecision{Allow: true, Request: gov, Prepared: prep}, batchDenyAt: -1}
	p := newProxy(t, doer, dec, nil)

	// The inbound body has different whitespace/ordering than the frozen artifact — proving
	// the forward uses the FROZEN bytes, not the inbound body nor a re-marshal.
	w := postMessages(t, p, "{\n  \"model\": \"claude-opus-4-8\",\n  \"max_tokens\": 16,\n  \"messages\": []\n}", "INBOUND-CALLER-KEY")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !bytes.Equal(doer.gotBody, prep.Body()) {
		t.Fatalf("upstream received bytes that are NOT the frozen artifact\n got:  %s\n want: %s", doer.gotBody, prep.Body())
	}
}

// TestProxyPreparedStreamFlagGovernsDispatch pins S3 fix #3: the FROZEN artifact's stream
// flag — not dec.Request.Stream — chooses the blocking vs SSE transport. A prepared
// stream:true artifact must go out on the SSE path (Accept: text/event-stream) even if
// dec.Request.Stream is stale/false, so the transport can never diverge from the frozen body.
func TestProxyPreparedStreamFlagGovernsDispatch(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", contentType: "text/event-stream"}
	streamReq := MessageRequest{
		Model: "claude-opus-4-8", MaxTokens: 16, Stream: true,
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}
	prep, err := MarshalPrepared(streamReq)
	if err != nil {
		t.Fatalf("marshal prepared: %v", err)
	}
	// dec.Request.Stream is deliberately FALSE — the frozen artifact is authoritative.
	dec := &fakeDecider{decision: ProxyDecision{Allow: true, Request: MessageRequest{Model: "claude-opus-4-8"}, Prepared: prep}, batchDenyAt: -1}
	p := newProxy(t, doer, dec, nil)
	w := postMessages(t, p, `{"model":"claude-opus-4-8","stream":true,"messages":[]}`, "K")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if doer.gotAccept != "text/event-stream" {
		t.Fatalf("upstream Accept = %q, want text/event-stream (dispatched on the frozen stream flag)", doer.gotAccept)
	}
}

// TestProxyUpstreamErrorBindsEffectiveDigest pins S3 fix #4: an upstream error still records
// the EffectiveSHA in the outcome — the body WAS forwarded (the upstream returned a status),
// so the evidence must bind what was sent.
func TestProxyUpstreamErrorBindsEffectiveDigest(t *testing.T) {
	doer := &proxyStubDoer{status: 429, body: `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, contentType: "application/json"}
	gov := MessageRequest{
		Model: "claude-opus-4-8", MaxTokens: 16,
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}
	prep, _ := MarshalPrepared(gov)
	dec := &fakeDecider{decision: ProxyDecision{Allow: true, Request: gov, Prepared: prep}, batchDenyAt: -1}
	p := newProxy(t, doer, dec, nil)
	w := postMessages(t, p, `{"model":"claude-opus-4-8","messages":[]}`, "K")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 relayed", w.Code)
	}
	if !dec.finalized {
		t.Fatal("Finalize must run on an upstream error (outcome evidence)")
	}
	wantDigest := prep.Digest()
	if !bytes.Equal(dec.finalizeOut.EffectiveSHA, wantDigest[:]) {
		t.Fatalf("upstream-error outcome EffectiveSHA = %x, want the frozen digest %x", dec.finalizeOut.EffectiveSHA, wantDigest[:])
	}
	if !dec.finalizeOut.UpstreamErr {
		t.Error("outcome must be flagged UpstreamErr")
	}
}

func TestProxyDenyDoesNotForward(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: okMessageJSON}
	dec := &fakeDecider{decision: ProxyDecision{Allow: false, Status: http.StatusForbidden, Reason: "model not granted"}}
	p := newProxy(t, doer, dec, nil)

	w := postMessages(t, p, `{"model":"claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, "INBOUND-CALLER-KEY")

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if doer.calls != 0 {
		t.Fatalf("upstream was called %d times on a DENY; must be 0 (no forward)", doer.calls)
	}
	if !strings.Contains(w.Body.String(), "\"type\":\"error\"") {
		t.Errorf("deny body is not an Anthropic error: %s", w.Body.String())
	}
	if dec.finalized {
		t.Error("Finalize must not run on a pre-forward deny")
	}
}

func TestProxyAllowForwardsWithOperatorCredentialNotInboundBearer(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: okMessageJSON}
	dec := &fakeDecider{decision: ProxyDecision{Allow: true}}
	p := newProxy(t, doer, dec, nil)

	w := postMessages(t, p, `{"model":"claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, "INBOUND-CALLER-KEY")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if doer.calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", doer.calls)
	}
	// Credential separation: the FORWARD carries the OPERATOR key, never the inbound bearer.
	if doer.gotAPIKey != "OPERATOR-KEY" {
		t.Errorf("forwarded x-api-key = %q, want OPERATOR-KEY", doer.gotAPIKey)
	}
	if strings.Contains(doer.gotAPIKey, "INBOUND") || strings.Contains(doer.gotAuth, "INBOUND") {
		t.Errorf("inbound caller credential LEAKED upstream: x-api-key=%q auth=%q", doer.gotAPIKey, doer.gotAuth)
	}
	// The bearer reached the decider (for identity resolution) but nowhere else.
	if dec.gotBearer != "INBOUND-CALLER-KEY" {
		t.Errorf("decider bearer = %q, want INBOUND-CALLER-KEY", dec.gotBearer)
	}
	if !dec.finalized || len(dec.finalizeOut.ReqSHA) == 0 || len(dec.finalizeOut.RespSHA) == 0 {
		t.Errorf("Finalize must run with req+resp fingerprints; finalized=%v reqSHA=%d respSHA=%d", dec.finalized, len(dec.finalizeOut.ReqSHA), len(dec.finalizeOut.RespSHA))
	}
}

func TestProxyResponseDLPBlockWithholdsBody(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: okMessageJSON}
	dec := &fakeDecider{
		decision: ProxyDecision{Allow: true},
		verdict:  ProxyResponseVerdict{Block: true, Status: http.StatusForbidden, Reason: "response carries a secret"},
	}
	p := newProxy(t, doer, dec, nil)

	w := postMessages(t, p, `{"model":"claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, "k")

	if doer.calls != 1 {
		t.Fatalf("upstream calls = %d, want 1 (the call ran)", doer.calls)
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (response blocked)", w.Code)
	}
	if strings.Contains(w.Body.String(), "hello world") {
		t.Errorf("blocked response leaked the model output: %s", w.Body.String())
	}
}

const okStreamSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi there"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

func TestProxyStreamPassthroughRelaysAndFinalizes(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: okStreamSSE, contentType: "text/event-stream"}
	dec := &fakeDecider{decision: ProxyDecision{Allow: true}} // passthrough (BufferResponse=false)
	p := newProxy(t, doer, dec, nil)

	w := postMessages(t, p, `{"model":"claude-opus-4-8","max_tokens":16,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, "k")

	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	body := w.Body.Bytes()
	if !strings.Contains(string(body), "event: content_block_delta") || !strings.Contains(string(body), "hi there") {
		t.Errorf("relayed stream missing the live delta: %s", string(body))
	}
	if !dec.finalized || !dec.finalizeOut.Streamed || len(dec.finalizeOut.RespSHA) == 0 {
		t.Errorf("Finalize must run for the stream with a response fingerprint")
	}
	// The recorded RespSHA MUST fingerprint exactly the bytes relayed to the caller (so an
	// auditor can reproduce the ledger anchor), and RespBytes must match their length.
	want := sha256.Sum256(body)
	if !bytes.Equal(dec.finalizeOut.RespSHA, want[:]) {
		t.Errorf("RespSHA does not fingerprint the relayed bytes:\n got %x\n want %x", dec.finalizeOut.RespSHA, want)
	}
	if dec.finalizeOut.RespBytes != int64(len(body)) {
		t.Errorf("RespBytes = %d, want %d (the relayed byte count)", dec.finalizeOut.RespBytes, len(body))
	}
}

func TestProxyStreamBufferBlockWithholdsEntireStream(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: okStreamSSE, contentType: "text/event-stream"}
	dec := &fakeDecider{
		decision: ProxyDecision{Allow: true, BufferResponse: true},
		verdict:  ProxyResponseVerdict{Block: true, Status: http.StatusForbidden, Reason: "dlp"},
	}
	p := newProxy(t, doer, dec, nil)

	w := postMessages(t, p, `{"model":"claude-opus-4-8","max_tokens":16,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, "k")

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (buffered response blocked before relay)", w.Code)
	}
	if strings.Contains(w.Body.String(), "hi there") {
		t.Errorf("buffer-mode block leaked stream content: %s", w.Body.String())
	}
}

func TestProxyStreamBufferCeilingWithholdsEntireStream(t *testing.T) {
	const ceiling int64 = 256 // accepts message_start, then overflows on a later frame
	doer := &proxyStubDoer{status: 200, body: okStreamSSE, contentType: "text/event-stream"}
	aud := &capAuditor{}
	dec := &fakeDecider{decision: ProxyDecision{
		Allow: true, BufferResponse: true, MaxResponseBufferBytes: ceiling,
	}}
	p := newProxy(t, doer, dec, aud)

	w := postMessages(t, p, `{"model":"claude-opus-4-8","max_tokens":16,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, "k")

	if doer.calls != 1 {
		t.Fatalf("upstream calls = %d, want 1 (response crossed the local buffer ceiling)", doer.calls)
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (buffer ceiling blocks before relay); body=%s", w.Code, w.Body.String())
	}
	for _, fragment := range []string{"event:", `"type":"message_start"`, "hi there"} {
		if strings.Contains(w.Body.String(), fragment) {
			t.Fatalf("buffer ceiling leaked upstream stream fragment %q: %s", fragment, w.Body.String())
		}
	}
	var got struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode blocked-response error: %v; body=%s", err, w.Body.String())
	}
	if got.Type != "error" || got.Error.Type != "permission_error" || got.Error.Message != responseBufferCeilingReason {
		t.Fatalf("blocked-response error = %+v, want permission_error/%q", got, responseBufferCeilingReason)
	}
	if len(aud.events) != 1 {
		t.Fatalf("audit events = %d, want 1 blocked-response outcome", len(aud.events))
	}
	ev := aud.events[0]
	if ev.Decision != "blocked-response" || ev.Reason != responseBufferCeilingReason || !ev.Streamed {
		t.Fatalf("audit outcome = %+v, want streamed blocked-response/%q", ev, responseBufferCeilingReason)
	}
	if ev.RespBytes <= 0 || ev.RespBytes > ceiling {
		t.Fatalf("audited buffered bytes = %d, want 1..%d", ev.RespBytes, ceiling)
	}
}

func TestProxyMalformedBodyDenies(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: okMessageJSON}
	dec := &fakeDecider{decision: ProxyDecision{Allow: true}}
	p := newProxy(t, doer, dec, nil)
	w := postMessages(t, p, `not json`, "k")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 on malformed body", w.Code)
	}
	if doer.calls != 0 {
		t.Errorf("upstream called on a malformed request")
	}
}

func TestProxyNilDeciderDeniesClosed(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: okMessageJSON}
	p := newProxy(t, doer, nil, nil)
	w := postMessages(t, p, `{"model":"claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, "k")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (no decider ⇒ deny-closed)", w.Code)
	}
	if doer.calls != 0 {
		t.Errorf("upstream called with no decider wired")
	}
}

// TestProxyNoPromptLeaks is the negative guard the OBSERVE connectors carry (docs/SECURITY-HARDENING.md
// §3): the prompt the proxy inspects in flight must never reach the SOC auditor — only
// the decision and byte counts. (The ledger anchor is the decider's job and is the
// fingerprint-only chain; this asserts the SHELL's contract.)
func TestProxyNoPromptLeaks(t *testing.T) {
	const (
		secret         = "sk-ant-SUPERSECRET-PROMPT-VALUE"
		responseCanary = "customer SSN 078-05-1120 RESPONSE-AUDIT-CANARY"
	)
	doer := &proxyStubDoer{status: 200, body: strings.Replace(okMessageJSON, "hello world", responseCanary, 1)}
	aud := &capAuditor{}
	dec := &fakeDecider{decision: ProxyDecision{Allow: true}}
	p := newProxy(t, doer, dec, aud)

	body := `{"model":"claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"please use ` + secret + `"}]}]}`
	_ = postMessages(t, p, body, secret) // even the inbound bearer carries the secret

	if len(aud.events) == 0 {
		t.Fatal("expected at least one audit event")
	}
	for _, ev := range aud.events {
		blob, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		for _, canary := range []string{secret, responseCanary} {
			if strings.Contains(string(blob), canary) {
				t.Fatalf("SOC audit event leaked raw inference content %q: %s", canary, blob)
			}
		}
	}
	// The decider's Finalize receives the RESPONSE + fingerprints, never the inbound prompt
	// body — assert the response struct handed over carries no inbound prompt text.
	if strings.Contains(dec.finalizeOut.Response.Model, secret) {
		t.Fatal("Finalize response leaked the prompt secret")
	}
}
