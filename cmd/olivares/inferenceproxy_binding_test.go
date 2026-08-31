// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// inferenceproxy_binding_test.go pins the F3 decision↔bytes binding at the DECIDER
// level: the governed decision FREEZES the effective request, so ProxyDecision.Prepared holds
// exactly the normalized + gate-rewritten bytes the ledger digest commits to, and a
// model-less request (no operator default) is a hard 400 rather than a silent forward.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/sdk"
)

// TestAuthorizeFreezesEffectiveBytes proves an allow carries a Prepared artifact whose bytes
// equal what the gates governed, and whose digest is echoed on the session (effectiveDigest).
func TestAuthorizeFreezesEffectiveBytes(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if !dec.Allow {
		t.Fatalf("expected allow; got status=%d reason=%q", dec.Status, dec.Reason)
	}
	if dec.Prepared.IsZero() {
		t.Fatal("an allow must carry a frozen Prepared artifact")
	}
	sess, ok := dec.Session.(*proxySession)
	if !ok || sess == nil {
		t.Fatal("allow must carry a *proxySession")
	}
	effDigest := dec.Prepared.Digest()
	if !bytes.Equal(sess.effectiveDigest, effDigest[:]) {
		t.Fatal("session effectiveDigest != Prepared.Digest()")
	}
	if len(sess.inputDigest) != sha256.Size {
		t.Fatalf("session inputDigest length = %d, want %d", len(sess.inputDigest), sha256.Size)
	}
	// The frozen body is the governed request serialized once.
	var sent map[string]any
	if err := json.Unmarshal(dec.Prepared.Body(), &sent); err != nil {
		t.Fatalf("frozen body not valid JSON: %v", err)
	}
	if sent["model"] != "claude-opus-4-8" {
		t.Fatalf("frozen model = %v, want claude-opus-4-8", sent["model"])
	}
}

// TestAuthorizeFreezesNormalization proves the freeze captures the NORMALIZED request: a
// sampling param that Opus 4.8 rejects is withheld in the frozen bytes (it was NOT before S3,
// because preflight ran AFTER governance and re-mutated the forwarded request).
func TestAuthorizeFreezesNormalization(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	temp := 0.7
	req := userReq("hi", false)
	req.Temperature = &temp // Opus 4.8 rejects sampling → normalization withholds it
	dec := d.Authorize(context.Background(), req, "bearer")
	if !dec.Allow {
		t.Fatalf("expected allow; got status=%d", dec.Status)
	}
	var sent map[string]any
	_ = json.Unmarshal(dec.Prepared.Body(), &sent)
	if _, present := sent["temperature"]; present {
		t.Fatalf("frozen bytes still carry temperature; normalization not frozen: %s", dec.Prepared.Body())
	}
}

// TestAuthorizeFreezesGateRewrite proves a GATE rewrite (a ceiling clamp on a tool's max_uses)
// is reflected in the frozen bytes — the decision and the forwarded octets cohere.
func TestAuthorizeFreezesGateRewrite(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.Ceilings = inferenceproxy.RequestCeilings{Enforce: true, MaxToolUses: 2}
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	req := userReq("hi", false)
	req.Tools = []any{map[string]any{"type": "web_search_20250305", "max_uses": 9}}
	dec := d.Authorize(context.Background(), req, "bearer")
	if !dec.Allow {
		t.Fatalf("expected allow (tool ceiling clamps, never hard-deny); got status=%d", dec.Status)
	}
	var sent struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(dec.Prepared.Body(), &sent); err != nil {
		t.Fatalf("frozen body: %v", err)
	}
	if len(sent.Tools) != 1 {
		t.Fatalf("frozen tools = %d, want 1", len(sent.Tools))
	}
	if got := sent.Tools[0]["max_uses"]; got != float64(2) {
		t.Fatalf("frozen max_uses = %v, want clamped 2", got)
	}
}

// TestAuthorizeModellessRequestIs400 proves a request with no model, on a proxy that pins no
// operator default, is a hard 400 malformed-request deny (never a silent forward to a hidden
// default) — the effective model must be explicit to be governed and frozen.
func TestAuthorizeModellessRequestIs400(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol) // defaultModel unset ("")
	req := userReq("hi", false)
	req.Model = ""
	dec := d.Authorize(context.Background(), req, "bearer")
	if dec.Allow || dec.Status != http.StatusBadRequest || dec.ErrorType != "invalid_request_error" {
		t.Fatalf("a model-less request must 400 invalid_request; got allow=%v status=%d type=%q", dec.Allow, dec.Status, dec.ErrorType)
	}
	if mg.calls != 0 {
		t.Errorf("model-access must not run on a malformed request; calls=%d", mg.calls)
	}
}

// TestAuthorizeModellessDenySemantics pins the PEP-neutral class of the malformed-request
// deny (FailureProtocolError, not a policy refusal) for the mapping.
func TestAuthorizeModellessDenySemantics(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	req := userReq("hi", false)
	req.Model = ""
	_, _, deny, ok := d.authorizeChain(context.Background(), req, "bearer")
	if ok || deny.code != gateCodeRequestMalformed || deny.class != sdk.FailureProtocolError {
		t.Fatalf("got ok=%v code=%q class=%q", ok, deny.code, deny.class)
	}
}

// TestIntentHashBindsBothDigests pins S3 fix #1: the pre-forward (RecordMandatory) intent
// anchor binds BOTH content digests, so two inputs that normalize to the SAME effective
// artifact still produce distinct intent evidence — a crash after forward, before outcome,
// leaves an anchor that proves which input was decided.
func TestIntentHashBindsBothDigests(t *testing.T) {
	eff := sha256.Sum256([]byte("effective"))
	inA := sha256.Sum256([]byte("input-A"))
	inB := sha256.Sum256([]byte("input-B"))
	hA := proxyIntentHash("ref", "direct", "acme", "claude-opus-4-8", "user:u1", inA[:], eff[:])
	hB := proxyIntentHash("ref", "direct", "acme", "claude-opus-4-8", "user:u1", inB[:], eff[:])
	if bytes.Equal(hA, hB) {
		t.Fatal("intent hash must differ when the input digest differs (same effective digest)")
	}
	// And the effective digest is bound too (different effective → different hash).
	eff2 := sha256.Sum256([]byte("effective-2"))
	hC := proxyIntentHash("ref", "direct", "acme", "claude-opus-4-8", "user:u1", inA[:], eff2[:])
	if bytes.Equal(hA, hC) {
		t.Fatal("intent hash must differ when the effective digest differs")
	}
}

// TestAuthorizeBatchFreezesEnvelope proves the batch path freezes the whole governed envelope
// into a Prepared artifact, with its digest on the session.
func TestAuthorizeBatchFreezesEnvelope(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.AuthorizeBatch(context.Background(), batchReqs("claude-opus-4-8", "claude-opus-4-8"), "bearer")
	if !dec.Allow {
		t.Fatalf("expected allow; got status=%d reason=%q", dec.Status, dec.Reason)
	}
	if dec.Prepared.IsZero() {
		t.Fatal("an allowed batch must carry a frozen Prepared envelope")
	}
	sess := dec.Session.(*proxySession)
	effDigest := dec.Prepared.Digest()
	if !bytes.Equal(sess.effectiveDigest, effDigest[:]) {
		t.Fatal("batch session effectiveDigest != Prepared.Digest()")
	}
	// The frozen envelope is {"requests":[...]} with both governed entries.
	var env struct {
		Requests []claudeapi.BatchRequest `json:"requests"`
	}
	if err := json.Unmarshal(dec.Prepared.Body(), &env); err != nil {
		t.Fatalf("frozen envelope: %v", err)
	}
	if len(env.Requests) != 2 {
		t.Fatalf("frozen envelope entries = %d, want 2", len(env.Requests))
	}
}
