// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// countingDecider keeps every Finalize outcome and counts the batch admissions and batch
// settlements, so a test can assert how many times one request reached each step.
type countingDecider struct {
	fakeDecider
	finalizes      []ProxyForwardResult
	authorizeBatch int
	finalizeBatch  int
}

func (d *countingDecider) Finalize(ctx context.Context, sess any, out ProxyForwardResult) ProxyResponseVerdict {
	d.finalizes = append(d.finalizes, out)
	return d.fakeDecider.Finalize(ctx, sess, out)
}

func (d *countingDecider) AuthorizeBatch(ctx context.Context, requests []BatchRequest, bearer string) ProxyBatchDecision {
	d.authorizeBatch++
	return d.fakeDecider.AuthorizeBatch(ctx, requests, bearer)
}

func (d *countingDecider) FinalizeBatch(ctx context.Context, sess any, out ProxyBatchForwardResult) {
	d.finalizeBatch++
	d.fakeDecider.FinalizeBatch(ctx, sess, out)
}

// decodeProxyError decodes an Anthropic-style error body written by the proxy.
func decodeProxyError(t *testing.T, body []byte) (errType, message string) {
	t.Helper()
	var got struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &got); err != nil || got.Type != "error" {
		t.Fatalf("body is not an Anthropic error (%v): %s", err, body)
	}
	return got.Error.Type, got.Error.Message
}

// TestProxyBufferCeilingSettlesTheForwardedCall pins that a buffered stream refused at the
// inspection buffer ceiling reaches Finalize once: the upstream call ran, so the decider must
// settle it now rather than let its hold lapse. The upstream answered 200 and streamed until the
// proxy abandoned the stream, so the outcome is a call that ran and was withheld (no UpstreamErr,
// status 200, the partial response), which commits the hold at the cost measured by the
// message_start usage. It carries no response fingerprint: the caller never received the bytes
// held before the ceiling. The caller's refusal, and the audit's count of those bytes, are
// unchanged.
func TestProxyBufferCeilingSettlesTheForwardedCall(t *testing.T) {
	const ceiling int64 = 256 // holds message_start (217 bytes), then overflows on the next frame
	doer := &proxyStubDoer{status: 200, body: okStreamSSE, contentType: "text/event-stream"}
	aud := &capAuditor{}
	dec := &countingDecider{fakeDecider: fakeDecider{decision: ProxyDecision{
		Allow: true, BufferResponse: true, MaxResponseBufferBytes: ceiling,
	}}}
	p := newProxy(t, doer, dec, aud)

	w := postMessages(t, p, inboundStreamJSON, "k")

	if doer.calls != 1 {
		t.Fatalf("upstream calls = %d, want 1 (the forward ran)", doer.calls)
	}
	if len(dec.finalizes) != 1 {
		t.Fatalf("Finalize ran %d times after a forward that ran, want 1: the call must be settled, not left to lapse", len(dec.finalizes))
	}
	out := dec.finalizes[0]
	if out.UpstreamErr || out.UpstreamStatus != http.StatusOK || !out.Streamed {
		t.Errorf("outcome UpstreamErr=%v UpstreamStatus=%d Streamed=%v, want a call that ran and was withheld (false, 200, true), so the hold is committed", out.UpstreamErr, out.UpstreamStatus, out.Streamed)
	}
	if out.Response.ID != "msg_1" || out.Response.Model != "claude-opus-4-8" || out.Response.Usage.InputTokens != 5 {
		t.Errorf("outcome Response = %+v, want the partial response whose message_start (msg_1, claude-opus-4-8, 5 in) measures the call's cost", out.Response)
	}
	wantReq := sha256.Sum256([]byte(inboundStreamJSON))
	if !bytes.Equal(out.ReqSHA, wantReq[:]) || out.ReqBytes != int64(len(inboundStreamJSON)) {
		t.Errorf("outcome ReqSHA=%x ReqBytes=%d, want the inbound body's %x/%d", out.ReqSHA, out.ReqBytes, wantReq, len(inboundStreamJSON))
	}
	if len(out.RespSHA) != 0 || out.RespBytes != 0 {
		t.Errorf("outcome RespSHA=%x RespBytes=%d, want none: the caller never received the bytes held before the ceiling", out.RespSHA, out.RespBytes)
	}

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (refusal unchanged); body=%s", w.Code, w.Body.String())
	}
	if errType, msg := decodeProxyError(t, w.Body.Bytes()); errType != "permission_error" || msg != responseBufferCeilingReason {
		t.Errorf("refusal = %s/%q, want permission_error/%q", errType, msg, responseBufferCeilingReason)
	}
	held := okStreamSSE[:strings.Index(okStreamSSE, "\n\n")+2]
	if len(aud.events) != 1 || aud.events[0].Decision != "blocked-response" || aud.events[0].RespBytes != int64(len(held)) {
		t.Errorf("audit = %+v, want one blocked-response counting the %d bytes held before the ceiling", aud.events, len(held))
	}
}

// TestProxyEncodeFailureSettlesTheForwardedCall pins that a blocking response the proxy cannot
// encode reaches Finalize once, with the outcome of the call that ran: the upstream answered
// 200 with a full response, which the decider needs to commit the hold at the call's measured
// cost. Nothing was relayed, so the outcome carries no response fingerprint. The caller's
// refusal is unchanged.
func TestProxyEncodeFailureSettlesTheForwardedCall(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: okMessageJSON, contentType: "application/json"}
	dec := &countingDecider{fakeDecider: fakeDecider{decision: ProxyDecision{Allow: true}}}
	p := newProxy(t, doer, dec, nil)
	p.encodeResponse = func(any) ([]byte, error) { return nil, errors.New("encode failed") }

	w := postMessages(t, p, inboundBlockingJSON, "k")

	if doer.calls != 1 {
		t.Fatalf("upstream calls = %d, want 1 (the forward ran)", doer.calls)
	}
	if len(dec.finalizes) != 1 {
		t.Fatalf("Finalize ran %d times after a forward that ran, want 1: the call must be settled, not left to lapse", len(dec.finalizes))
	}
	out := dec.finalizes[0]
	if out.UpstreamErr || out.UpstreamStatus != http.StatusOK || out.Streamed {
		t.Errorf("outcome UpstreamErr=%v UpstreamStatus=%d Streamed=%v, want the completed call (false, 200, false)", out.UpstreamErr, out.UpstreamStatus, out.Streamed)
	}
	if out.Response.ID != "msg_1" || out.Response.Usage.InputTokens != 5 || out.Response.Usage.OutputTokens != 2 {
		t.Errorf("outcome Response = %+v, want the upstream response (msg_1, 5 in, 2 out) that measures the call's cost", out.Response)
	}
	wantReq := sha256.Sum256([]byte(inboundBlockingJSON))
	if !bytes.Equal(out.ReqSHA, wantReq[:]) || out.ReqBytes != int64(len(inboundBlockingJSON)) {
		t.Errorf("outcome ReqSHA=%x ReqBytes=%d, want the inbound body's %x/%d", out.ReqSHA, out.ReqBytes, wantReq, len(inboundBlockingJSON))
	}
	if len(out.RespSHA) != 0 || out.RespBytes != 0 {
		t.Errorf("outcome RespSHA=%x RespBytes=%d, want none: nothing was relayed", out.RespSHA, out.RespBytes)
	}

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (refusal unchanged); body=%s", w.Code, w.Body.String())
	}
	if errType, msg := decodeProxyError(t, w.Body.Bytes()); errType != "api_error" || msg != "could not encode upstream response" {
		t.Errorf("refusal = %s/%q, want api_error/%q", errType, msg, "could not encode upstream response")
	}
	if strings.Contains(w.Body.String(), "hello world") {
		t.Errorf("refusal leaked the model output: %s", w.Body.String())
	}
}
