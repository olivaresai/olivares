// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

// rs_s320_review_test.go — regression tests for the three review
// corrections layered on top of assignments A/B: subscriptions/listen refused in
// EVERY revision mode (not only the RC path), ambiguous Mcp-Param
// case-insensitive argument matches refused, and the COAZ centralized-policy
// gate re-run on tasks/update exactly as on tools/call.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSubscriptionsListenRefusedInLegacyMode: a request that DECLARES a legacy
// revision (or reaches a legacy-mode RS) and calls the RC-only
// subscriptions/listen must be refused 404/-32601, never forwarded — the
// request/response Upstream.Forward cannot consume a long-lived stream, so a
// forward would stall instead of failing honestly.
func TestSubscriptionsListenRefusedInLegacyMode(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	body := `{"jsonrpc":"2.0","id":3,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true}}}`

	t.Run("dual mode, legacy-declared request", func(t *testing.T) {
		up := &fakeUpstream{}
		aud := &capturingAuditor{}
		rs := newRSDual(t, jwks, up, aud)
		req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(headerMCPProtocolVersion, revision20251125)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound || rpcErrorCode(t, w.Body.String()) != -32601 {
			t.Fatalf("legacy-declared subscriptions/listen = status %d body %s, want 404/-32601", w.Code, w.Body.String())
		}
		if up.called {
			t.Fatal("subscriptions/listen must never reach the upstream forwarder")
		}
	})

	t.Run("legacy mode RS", func(t *testing.T) {
		up := &fakeUpstream{}
		rs := newRSTestMode(t, jwks, up, &capturingAuditor{}, revisionModeLegacy)
		req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound || rpcErrorCode(t, w.Body.String()) != -32601 {
			t.Fatalf("legacy-mode subscriptions/listen = status %d body %s, want 404/-32601", w.Code, w.Body.String())
		}
		if up.called {
			t.Fatal("subscriptions/listen must never reach the upstream forwarder")
		}
	})
}

// TestMcpParamAmbiguousArgumentRefused: two body arguments differing only by
// case make an Mcp-Param mirror ambiguous — the RS refuses (400/-32020) rather
// than matching one by nondeterministic map order.
func TestMcpParamAmbiguousArgumentRefused(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{}
	rs := newRSDual(t, jwks, up, &capturingAuditor{})

	body := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"search","arguments":{"path":"a","PATH":"a"},"_meta":{}}}`
	req := nextReq(token, "tools/call", "search", body)
	req.Header.Set("Mcp-Param-Path", "a")
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || rpcErrorCode(t, w.Body.String()) != rpcHeaderMismatch {
		t.Fatalf("ambiguous Mcp-Param = status %d body %s, want 400/%d", w.Code, w.Body.String(), rpcHeaderMismatch)
	}
	if up.called {
		t.Fatal("ambiguous Mcp-Param mirror must not reach upstream")
	}
}

// TestTasksUpdateCOAZGate: tasks/update is an actuation on the original tool,
// so the COAZ policy gate re-runs — deny and evaluator error both refuse
// deny-closed; allow proceeds to the upstream.
func TestTasksUpdateCOAZGate(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	newCOAZTaskRS := func(t *testing.T, eval COAZEvaluator, up Upstream) *ResourceServer {
		t.Helper()
		ts, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
		if err != nil {
			t.Fatalf("toolset: %v", err)
		}
		rs, err := NewResourceServer(ResourceServerConfig{
			Resource:                   rsResource,
			AuthorizationServers:       []string{rsIssuer},
			Issuer:                     rsIssuer,
			IssuerJWKS:                 jwks,
			Toolset:                    ts,
			Gate:                       fakeToolGate{StatusApproved},
			Upstream:                   up,
			DurableTaskStore:           newMemoryDurableTaskStore(),
			Auditor:                    &taskAuditor{},
			Clock:                      rsClock,
			COAZEvaluator:              eval,
			DisableNextRevisionHeaders: true,
		})
		if err != nil {
			t.Fatalf("new rs: %v", err)
		}
		return rs
	}

	seed := func(t *testing.T, rs *ResourceServer) {
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-coaz", Tool: "search", RequiredScope: "tools:read"})
	}
	update := func(rs *ResourceServer) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksUpdate, `{"taskId":"task-coaz","inputResponses":{}}`))
		return w
	}

	t.Run("deny refuses 403", func(t *testing.T) {
		up := &taskUpstream{}
		eval := &stubCOAZEvaluator{decision: COAZDecision{Allow: false, Reason: "policy"}}
		rs := newCOAZTaskRS(t, eval, up)
		seed(t, rs)
		if w := update(rs); w.Code != http.StatusForbidden {
			t.Fatalf("COAZ-denied tasks/update status = %d, want 403; body=%s", w.Code, w.Body.String())
		}
		if up.count(methodTasksUpdate) != 0 {
			t.Fatal("COAZ-denied tasks/update must not reach upstream")
		}
		if eval.lastReq.Tool != "search" {
			t.Fatalf("COAZ evaluated tool = %q, want the task's original tool", eval.lastReq.Tool)
		}
	})

	t.Run("evaluator error refuses fail-closed", func(t *testing.T) {
		up := &taskUpstream{}
		eval := &stubCOAZEvaluator{err: errors.New("pdp unreachable")}
		rs := newCOAZTaskRS(t, eval, up)
		seed(t, rs)
		if w := update(rs); w.Code != http.StatusForbidden {
			t.Fatalf("COAZ-error tasks/update status = %d, want 403; body=%s", w.Code, w.Body.String())
		}
		if up.count(methodTasksUpdate) != 0 {
			t.Fatal("COAZ-error tasks/update must not reach upstream (fail-closed)")
		}
	})

	t.Run("allow proceeds upstream", func(t *testing.T) {
		up := &taskUpstream{}
		eval := &stubCOAZEvaluator{decision: COAZDecision{Allow: true}}
		rs := newCOAZTaskRS(t, eval, up)
		seed(t, rs)
		if w := update(rs); w.Code != http.StatusOK {
			t.Fatalf("COAZ-allowed tasks/update status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		if up.count(methodTasksUpdate) != 1 {
			t.Fatal("COAZ-allowed tasks/update must reach upstream exactly once")
		}
	})
}
