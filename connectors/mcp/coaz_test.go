// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- COAZ evaluator seam tests on the RS ------------------------------------

func TestRS_COAZEvaluator_Allow(t *testing.T) {
	eval := &stubCOAZEvaluator{decision: COAZDecision{Allow: true, Reason: "policy permits"}}
	tok, jwks := mintAccessToken(t, "coaz-1", rsResource, "tools:read", rsClock().Add(5*time.Minute))

	rs := newCOAZTestRS(t, jwks, eval)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(toolsCallJSON("search")))
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	rs.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestRS_COAZEvaluator_Deny(t *testing.T) {
	eval := &stubCOAZEvaluator{decision: COAZDecision{Allow: false, Reason: "enterprise policy forbids"}}
	tok, jwks := mintAccessToken(t, "coaz-2", rsResource, "tools:read", rsClock().Add(5*time.Minute))

	rs := newCOAZTestRS(t, jwks, eval)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(toolsCallJSON("search")))
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	rs.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	errObj, _ := resp["error"].(map[string]any)
	if errObj == nil {
		t.Fatal("expected JSON-RPC error in response")
	}
	msg, _ := errObj["message"].(string)
	if msg != "tool not permitted by authorization policy" {
		t.Errorf("error message = %q", msg)
	}
}

func TestRS_COAZEvaluator_Error_FailClosed(t *testing.T) {
	eval := &stubCOAZEvaluator{err: errors.New("pdp unreachable")}
	tok, jwks := mintAccessToken(t, "coaz-3", rsResource, "tools:read", rsClock().Add(5*time.Minute))

	rs := newCOAZTestRS(t, jwks, eval)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(toolsCallJSON("search")))
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	rs.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (fail-closed on PDP error); body: %s", w.Code, w.Body.String())
	}
}

func TestRS_COAZEvaluator_Nil_Skipped(t *testing.T) {
	tok, jwks := mintAccessToken(t, "coaz-4", rsResource, "tools:read", rsClock().Add(5*time.Minute))

	rs := newCOAZTestRS(t, jwks, nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(toolsCallJSON("search")))
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	rs.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (nil evaluator ⇒ COAZ skipped); body: %s", w.Code, w.Body.String())
	}
}

func TestRS_COAZEvaluator_RequestFields(t *testing.T) {
	eval := &stubCOAZEvaluator{decision: COAZDecision{Allow: true}}
	tok, jwks := mintAccessToken(t, "coaz-5", rsResource, "tools:read", rsClock().Add(5*time.Minute))

	rs := newCOAZTestRS(t, jwks, eval)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(toolsCallJSON("search")))
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	rs.ServeHTTP(w, r)

	if eval.lastReq.Tool != "search" {
		t.Errorf("COAZRequest.Tool = %q, want search", eval.lastReq.Tool)
	}
	if eval.lastReq.ServerURI != rsResource {
		t.Errorf("COAZRequest.ServerURI = %q, want %s", eval.lastReq.ServerURI, rsResource)
	}
	if eval.lastReq.Subject != "agent:claude" {
		t.Errorf("COAZRequest.Subject = %q, want agent:claude", eval.lastReq.Subject)
	}
	if eval.lastReq.Issuer != rsIssuer {
		t.Errorf("COAZRequest.Issuer = %q, want %s", eval.lastReq.Issuer, rsIssuer)
	}
	if _, ok := eval.lastReq.Scopes["tools:read"]; !ok {
		t.Errorf("COAZRequest.Scopes = %v, want to contain tools:read", eval.lastReq.Scopes)
	}
}

func TestRS_COAZEvaluator_AfterScopeCheck(t *testing.T) {
	eval := &stubCOAZEvaluator{decision: COAZDecision{Allow: true}}
	tok, jwks := mintAccessToken(t, "coaz-6", rsResource, "wrong:scope", rsClock().Add(5*time.Minute))

	rs := newCOAZTestRS(t, jwks, eval)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(toolsCallJSON("search")))
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	rs.ServeHTTP(w, r)

	// Scope check should reject BEFORE COAZ evaluator is called.
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (insufficient scope)", w.Code)
	}
	if eval.lastReq.Tool != "" {
		t.Error("COAZ evaluator was called despite scope failure — should be gated before")
	}
}

// --- helpers ----------------------------------------------------------------

type stubCOAZEvaluator struct {
	decision COAZDecision
	err      error
	lastReq  COAZRequest
}

func (s *stubCOAZEvaluator) EvaluateToolCall(_ context.Context, req COAZRequest) (COAZDecision, error) {
	s.lastReq = req
	return s.decision, s.err
}

func newCOAZTestRS(t *testing.T, jwks []byte, eval COAZEvaluator) *ResourceServer {
	t.Helper()
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:             rsResource,
		AuthorizationServers: []string{rsIssuer},
		ScopesSupported:      []string{"tools:read", "tools:admin"},
		Issuer:               rsIssuer,
		IssuerJWKS:           jwks,
		Toolset: func() *Toolset {
			ts, err := NewToolset([]ToolPolicy{
				{Name: "search", RequiredScope: "tools:read"},
				{Name: "delete_db", RequiredScope: "tools:admin", Destructive: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			return ts
		}(),
		AllowedOrigins:             []string{rsOrigin},
		Gate:                       fakeToolGate{status: StatusApproved},
		Upstream:                   &fakeUpstream{},
		Auditor:                    &fakeEvidenceJournal{}, // granting journal (nop default pinned by evidence_test.go)
		Clock:                      rsClock,
		COAZEvaluator:              eval,
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func toolsCallJSON(name string) string {
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": map[string]any{}},
	}
	raw, _ := json.Marshal(req)
	return string(raw)
}
