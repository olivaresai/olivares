// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// inferenceproxy_egress_test.go pins: the count_tokens PRE-FLIGHT is the only
// pre-forward upstream egress in the proxy, and it must never run before the local
// content gates (DLP, firewall). Before the fix, gate 4 (context window) POSTed
// /v1/messages/count_tokens — carrying system, messages, tools and MCP servers — BEFORE
// gate 5 (DLP), so a DLP-denied secret was exfiltrated to the provider through the
// token-count side channel while the caller saw a clean 403.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
)

// erroringDoer fails every upstream call — the sizing pre-flight must stay NON-blocking
// (a count_tokens outage never denies; it is a capability pre-flight, not a security gate).
type erroringDoer struct{ calls int }

func (d *erroringDoer) Do(*http.Request) (*http.Response, error) {
	d.calls++
	return nil, errors.New("upstream unreachable")
}

// TestProxyDLPDenyNeverReachesCountTokens is the regression: a stock-DLP-denied
// secret prompt must be denied WITHOUT any count_tokens round trip — zero upstream
// bytes before the content gates pass.
func TestProxyDLPDenyNeverReachesCountTokens(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "")
	inf, doer := proxyCountTokensInference(5)
	d := storeBackedDecider(st, tenant, ipx, "", nil, inf)

	dec := d.Authorize(context.Background(), userReq("use AKIAIOSFODNN7EXAMPLE for deploy", false), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("stock DLP must deny the secret: allow=%v status=%d reason=%q", dec.Allow, dec.Status, dec.Reason)
	}
	// The deny must be THE DLP deny — another early 403 (residency, model-access) would
	// make the zero-egress assertion below vacuous without ever running the classifier.
	if !strings.Contains(dec.Reason, "data-loss-prevention") {
		t.Fatalf("expected the DLP gate's deny, got reason=%q", dec.Reason)
	}
	if doer.calls != 0 {
		t.Fatalf("DLP-denied prompt egressed upstream via count_tokens: %d POST(s) before the deny", doer.calls)
	}
}

// TestProxyBatchDLPDenyNeverReachesCountTokens pins the batch BARRIER, not just the
// intra-entry order: the FIRST entry is clean (it would size under a naive per-entry
// chain), the SECOND carries the secret. A denied batch must produce ZERO count_tokens
// POSTs total — local gates must clear ALL entries before ANY entry egresses.
func TestProxyBatchDLPDenyNeverReachesCountTokens(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "")
	inf, doer := proxyCountTokensInference(5)
	d := storeBackedDecider(st, tenant, ipx, "", nil, inf)

	entries := []claudeapi.BatchRequest{
		{CustomID: "c0", Params: userReq("summarize the public changelog", false)},
		{CustomID: "c1", Params: userReq("use AKIAIOSFODNN7EXAMPLE for deploy", false)},
	}
	dec := d.AuthorizeBatch(context.Background(), entries, "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("stock DLP must deny the secret batch entry: allow=%v status=%d reason=%q", dec.Allow, dec.Status, dec.Reason)
	}
	if !strings.Contains(dec.Reason, "data-loss-prevention") {
		t.Fatalf("expected the DLP gate's deny, got reason=%q", dec.Reason)
	}
	if doer.calls != 0 {
		t.Fatalf("denied batch egressed upstream via count_tokens (clean entry sized before the barrier): %d POST(s)", doer.calls)
	}
}

// TestProxyCleanPromptStillSizes is the positive control that forbids "fixing" the
// egress by deleting the sizing: a clean prompt is still sized exactly once and allowed.
func TestProxyCleanPromptStillSizes(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "")
	inf, doer := proxyCountTokensInference(5)
	d := storeBackedDecider(st, tenant, ipx, "", nil, inf)

	dec := d.Authorize(context.Background(), userReq("summarize the public changelog", false), "bearer")
	if !dec.Allow {
		t.Fatalf("clean prompt must be allowed: status=%d reason=%q", dec.Status, dec.Reason)
	}
	if doer.calls != 1 {
		t.Fatalf("clean prompt must be sized exactly once via count_tokens; calls=%d", doer.calls)
	}
}

// TestProxyFirewallDenyNeverReachesCountTokens extends the zero-egress law to the
// content firewall: a firewall-denied (DLP-clean) prompt must never have been sized.
func TestProxyFirewallDenyNeverReachesCountTokens(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "")
	inf, doer := proxyCountTokensInference(5)
	d := storeBackedDecider(st, tenant, ipx, "", nil, inf)
	d.inspector = &fakeInspector{dec: claudeapi.ContentInspectionDecision{Forward: false, Status: http.StatusForbidden, Reason: "prompt injection detected"}}

	dec := d.Authorize(context.Background(), userReq("summarize the public changelog", false), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("firewall must deny: allow=%v status=%d reason=%q", dec.Allow, dec.Status, dec.Reason)
	}
	if doer.calls != 0 {
		t.Fatalf("firewall-denied prompt egressed upstream via count_tokens: %d POST(s)", doer.calls)
	}
}

// TestProxyHardCeilingDenyNeverReachesCountTokens extends the law to the request-ceiling
// gate: an enforce-mode hard violation (max_tokens over the cap) denies with zero egress.
func TestProxyHardCeilingDenyNeverReachesCountTokens(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.GateContextWindow = true
	base.Ceilings = inferenceproxy.RequestCeilings{Enforce: true, MaxTokens: 8} // userReq carries MaxTokens:16
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	inf, doer := proxyCountTokensInference(5)
	d.inf = inf

	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusPaymentRequired {
		t.Fatalf("hard ceiling violation must 402: allow=%v status=%d reason=%q", dec.Allow, dec.Status, dec.Reason)
	}
	if doer.calls != 0 {
		t.Fatalf("ceiling-denied request egressed upstream via count_tokens: %d POST(s)", doer.calls)
	}
}

// TestProxySizingFailureStaysNonBlocking pins that the reorder did NOT turn the sizing
// pre-flight fail-closed: a count_tokens outage still allows (capability pre-flight).
func TestProxySizingFailureStaysNonBlocking(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "")
	doer := &erroringDoer{}
	inf := claudeapi.NewInference(claudeapi.InferenceConfig{APIKey: "k", Gateway: "direct", DefaultModel: "claude-opus-4-8", Doer: doer})
	d := storeBackedDecider(st, tenant, ipx, "", nil, inf)

	dec := d.Authorize(context.Background(), userReq("summarize the public changelog", false), "bearer")
	if !dec.Allow {
		t.Fatalf("a sizing outage must not deny (non-blocking pre-flight): status=%d reason=%q", dec.Status, dec.Reason)
	}
	if doer.calls == 0 {
		t.Fatal("the sizing pre-flight was never attempted (vacuous outage)")
	}
}

// TestProxySizingMeasuresGovernedRequest pins that the sizing runs AFTER the gate
// rewrites: the count_tokens body must carry the GOVERNED tools (ceilings-clamped
// max_uses), not the inbound values — the measured object is the one that gets frozen.
func TestProxySizingMeasuresGovernedRequest(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.GateContextWindow = true
	base.Ceilings = inferenceproxy.RequestCeilings{Enforce: true, MaxToolUses: 2}
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	inf, doer := proxyCountTokensInference(5)
	d.inf = inf

	req := userReq("hi", false)
	req.Tools = []any{map[string]any{"type": "web_search_20250305", "max_uses": 9}}
	dec := d.Authorize(context.Background(), req, "bearer")
	if !dec.Allow {
		t.Fatalf("clamped request must be allowed: status=%d reason=%q", dec.Status, dec.Reason)
	}
	if doer.calls != 1 {
		t.Fatalf("expected exactly one sizing call; calls=%d", doer.calls)
	}
	var sent struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(doer.gotBody, &sent); err != nil {
		t.Fatalf("count_tokens body: %v", err)
	}
	if len(sent.Tools) != 1 || sent.Tools[0]["max_uses"] != float64(2) {
		t.Fatalf("sizing measured the UNGOVERNED request; tools=%v (want max_uses=2)", sent.Tools)
	}
}

// TestProxyWindowDenyStillEnforced is the second positive control: after the reorder the
// per-surface window check still denies an oversized (but DLP-clean) request with 400.
func TestProxyWindowDenyStillEnforced(t *testing.T) {
	ipx := inferenceproxy.New()
	st, tenant := provisionTenant(t, ipx, "")
	inf, doer := proxyCountTokensInference(10_000_000) // exceeds every surface window
	d := storeBackedDecider(st, tenant, ipx, "", nil, inf)

	dec := d.Authorize(context.Background(), userReq("summarize the public changelog", false), "bearer")
	if dec.Allow || dec.Status != http.StatusBadRequest || dec.ErrorType != "invalid_request_error" {
		t.Fatalf("oversized request must 400 on the surface window: allow=%v status=%d type=%q reason=%q",
			dec.Allow, dec.Status, dec.ErrorType, dec.Reason)
	}
	if doer.calls != 1 {
		t.Fatalf("window enforcement requires exactly one sizing call; calls=%d", doer.calls)
	}
}
