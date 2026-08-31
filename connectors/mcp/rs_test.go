// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

func rsClock() time.Time { return time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC) }

const (
	rsResource = "https://mcp.olivares.example/gw"
	rsIssuer   = "https://auth.olivares.example"
	rsOrigin   = "https://app.olivares.example"
)

// mintAccessToken signs an at+jwt access token with a fresh EC key and returns the
// token + matching public JWKS. aud/scope/exp are overridable for negative tests.
func mintAccessToken(t *testing.T, kid, aud, scope string, exp time.Time) (token string, jwks []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", kid))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	std := jwt.Claims{
		Issuer:   rsIssuer,
		Subject:  "agent:claude",
		Audience: jwt.Audience{aud},
		IssuedAt: jwt.NewNumericDate(rsClock().Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(exp),
	}
	raw, err := jwt.Signed(signer).Claims(std).Claims(map[string]any{"scope": scope}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: kid, Algorithm: "ES256", Use: "sig"}}}
	blob, err := json.Marshal(ks)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return raw, blob
}

// --- test doubles ---------------------------------------------------------------

type fakeUpstream struct {
	called bool
	gotReq UpstreamRequest
	result json.RawMessage
	err    error
}

func (u *fakeUpstream) Forward(_ context.Context, req UpstreamRequest) (UpstreamResult, error) {
	u.called = true
	u.gotReq = req
	if u.err != nil {
		// A local fake never handed anything to a transport: honest not_sent.
		return UpstreamResult{State: DispatchNotSent}, u.err
	}
	if u.result == nil {
		return UpstreamResult{Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), State: DispatchCompleted}, nil
	}
	return UpstreamResult{Result: u.result, State: DispatchCompleted}, nil
}

type fakeToolGate struct{ status GateStatus }

func (g fakeToolGate) Authorize(_ context.Context, req ToolApprovalRequest) (GateDecision, error) {
	return GateDecision{ApprovalRef: "appr-1", Status: g.status, PlanHash: req.PlanHash}, nil
}

func newRS(t *testing.T, jwks []byte, gate ApprovalGate, up Upstream) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "delete_db", RequiredScope: "tools:admin", Destructive: true},
	})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:             rsResource,
		AuthorizationServers: []string{rsIssuer},
		ScopesSupported:      []string{"tools:read", "tools:admin"},
		Issuer:               rsIssuer,
		IssuerJWKS:           jwks,
		Toolset:              ts,
		AllowedOrigins:       []string{rsOrigin},
		Gate:                 gate,
		Upstream:             up,
		DurableTaskStore:     newMemoryDurableTaskStore(),
		// tools/call is evidence-mandatory; the granting in-memory journal
		// keeps these tests focused on their own concerns (the deny-closed nop
		// default is pinned by the evidence exploit suite).
		Auditor: &fakeEvidenceJournal{},
		Clock:   rsClock,
		// Tests in this file send 2025-11-25 style requests; opt out of the
		// 2026-07-28 header gate so they can focus on their own concerns.
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

// toolsCallReq builds a tools/call request that DECLARES the Tasks extension in
// its per-request clientCapabilities. Design adjudication (§2/§6): the pin
// requires clientCapabilities on every request (schema.ts:92-98) and the
// task-handle response contract exists only for a request that declared the
// tasks extension — the fixtures across the task suites exercise durable task
// handles through this helper, so the shared request is the CONFORMING,
// capability-declared one. Tests that need the UNDECLARED shape build their
// params via customToolsCallReq.
func toolsCallReq(token, name, args string) *http.Request {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args +
		`,"_meta":{"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func validExp() time.Time { return rsClock().Add(time.Hour) }

// --- tests ----------------------------------------------------------------------

// TestRSMetadataServed: the RFC 9728 PRM document is served unauthenticated.
func TestRSMetadataServed(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, &fakeUpstream{})
	req := httptest.NewRequest(http.MethodGet, wellKnownProtectedResource, nil)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, want 200", w.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if doc["resource"] != rsResource {
		t.Errorf("metadata resource = %v, want %s", doc["resource"], rsResource)
	}
	if _, ok := doc["authorization_servers"]; !ok {
		t.Error("metadata must list authorization_servers (RFC 9728)")
	}
}

// TestRSMissingBearer: no token → 401 with a resource_metadata challenge.
func TestRSMissingBearer(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, &fakeUpstream{})
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq("", "search", "{}"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata=") {
		t.Errorf("401 must carry a resource_metadata challenge, got %q", w.Header().Get("WWW-Authenticate"))
	}
}

// TestRSCrossAudienceRejected: a token minted for ANOTHER audience but signed by a
// TRUSTED key is the confused-deputy case → 401 (the rejection is on the AUDIENCE, not
// the signature), upstream never called.
func TestRSCrossAudienceRejected(t *testing.T) {
	sg := newSigner(t)
	up := &fakeUpstream{}
	rs := newRS(t, sg.jwks, fakeToolGate{StatusApproved}, up)
	// Same trusted signing key; foreign audience. Signature verifies; audience fails.
	foreign := sg.mint(t, "https://other-server.example/x", "tools:read", validExp())
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(foreign, "search", "{}"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cross-audience status = %d, want 401", w.Code)
	}
	if up.called {
		t.Error("a cross-audience token must NEVER reach the upstream")
	}
	// Sanity: the SAME key minting the CORRECT audience is accepted (so the 401 above
	// was the audience, not a signature/key problem).
	good := sg.mint(t, rsResource, "tools:read", validExp())
	w2 := httptest.NewRecorder()
	rs.ServeHTTP(w2, toolsCallReq(good, "search", "{}"))
	if w2.Code != http.StatusOK {
		t.Fatalf("correct-audience token from the same key must be accepted, got %d", w2.Code)
	}
}

// TestRSExpiredRejected: an expired token → 401.
func TestRSExpiredRejected(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", rsClock().Add(-time.Hour))
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, &fakeUpstream{})
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", "{}"))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expired status = %d, want 401", w.Code)
	}
}

// TestRSToolsCallAllowed: valid aud + sufficient scope on a non-destructive tool →
// forwarded; the upstream sees the subject but NOT the raw bearer (no passthrough).
func TestRSToolsCallAllowed(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(`{"content":[{"type":"text","text":"hits"}]}`)}
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, up)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{"q":"x"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("allowed tools/call status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !up.called {
		t.Fatal("an authorized tools/call must reach the upstream")
	}
	if up.gotReq.Subject != "agent:claude" {
		t.Errorf("upstream subject = %q, want agent:claude", up.gotReq.Subject)
	}
	// No-token-passthrough: the inbound bearer must be unreachable from the upstream
	// request. The UpstreamRequest has no token field; assert it structurally too.
	blob, _ := json.Marshal(up.gotReq)
	if strings.Contains(string(blob), token) {
		t.Error("CRITICAL: the inbound token must NOT be reachable from the upstream request (no passthrough)")
	}
}

func TestRSToolsCallIgnoresCallerDeclaredAgentRefForIdentity(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{}
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, up)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{"agent_ref":"agent:victim","q":"x"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !up.called {
		t.Fatal("authorized tools/call did not reach the upstream")
	}
	if up.gotReq.Subject != "agent:claude" {
		t.Fatalf("caller-declared agent_ref changed authenticated identity to %q; want token subject agent:claude", up.gotReq.Subject)
	}
}

// TestRSInsufficientScope: a tool whose required scope the token lacks → 403 + a scope
// challenge (step-up SEP-835); upstream not called.
func TestRSInsufficientScope(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp()) // lacks tools:admin
	up := &fakeUpstream{}
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, up)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "delete_db", "{}"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("insufficient-scope status = %d, want 403", w.Code)
	}
	wa := w.Header().Get("WWW-Authenticate")
	if !strings.Contains(wa, "insufficient_scope") || !strings.Contains(wa, `scope="tools:admin"`) {
		t.Errorf("403 must carry a step-up scope challenge for tools:admin, got %q", wa)
	}
	if up.called {
		t.Error("an insufficient-scope call must not reach the upstream")
	}
}

// TestRSDestructiveRequiresApproval: a destructive tool with sufficient scope but no
// approval → 403; with approval → forwarded.
func TestRSDestructiveRequiresApproval(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:admin", validExp())

	// No approval (gate pending) → denied, upstream not called.
	upDenied := &fakeUpstream{}
	rsDenied := newRS(t, jwks, fakeToolGate{StatusPending}, upDenied)
	w := httptest.NewRecorder()
	rsDenied.ServeHTTP(w, toolsCallReq(token, "delete_db", `{"name":"prod"}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("destructive-no-approval status = %d, want 403", w.Code)
	}
	if upDenied.called {
		t.Error("a destructive tool without approval must NOT reach the upstream")
	}

	// With approval → forwarded.
	upOK := &fakeUpstream{}
	rsOK := newRS(t, jwks, fakeToolGate{StatusApproved}, upOK)
	w2 := httptest.NewRecorder()
	rsOK.ServeHTTP(w2, toolsCallReq(token, "delete_db", `{"name":"prod"}`))
	if w2.Code != http.StatusOK {
		t.Fatalf("destructive-approved status = %d, want 200; body=%s", w2.Code, w2.Body.String())
	}
	if !upOK.called {
		t.Error("an approved destructive tool must reach the upstream")
	}
}

// rpcErrorMessage returns the JSON-RPC error.message of a refusal so a test can pin WHY a
// call was denied instead of merely that it was. On this endpoint the HTTP status alone
// cannot carry that: insufficient scope, a tool outside the server toolset, a caller-role
// refusal, a gate ERROR and the deny-closed default all answer 403.
func rpcErrorMessage(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("refusal body is not a JSON-RPC error: %v (body=%s)", err, w.Body.String())
	}
	return resp.Error.Message
}

// TestRSDestructiveDenyClosedDefaultGate: no gate wired → destructive tool denied.
//
// It pins the REASON, not just the 403. gate.go returns StatusNoGate explicitly and rs.go
// carries it into the JSON-RPC message, so only the no-gate path can turn this green —
// a 403 produced by a bad token, an unresolved route or an expired JWKS cannot.
func TestRSDestructiveDenyClosedDefaultGate(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:admin", validExp())
	up := &fakeUpstream{}
	// Gate nil => denyApprovalGate.
	ts, _ := NewToolset([]ToolPolicy{{Name: "delete_db", RequiredScope: "tools:admin", Destructive: true}})
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer}, Issuer: rsIssuer,
		IssuerJWKS: jwks, Toolset: ts, Upstream: up, Clock: rsClock,
		DurableTaskStore:           newMemoryDurableTaskStore(),
		Auditor:                    &fakeEvidenceJournal{}, // granting journal (enforcement pinned by evidence_test.go)
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "delete_db", "{}"))
	if w.Code != http.StatusForbidden {
		t.Errorf("deny-closed gate status = %d, want 403", w.Code)
	}
	// Matched EXACTLY, not by containment: "no_gate" is a substring of any status ending in
	// it, and a gate returning GateStatus("gate_no_gate") took a different path and still
	// turned this test green. GateStatus is an open string type whose only allow is
	// "approved" (gate.go), so the oracle has to be the whole message.
	wantMsg := "destructive tool requires human approval (" + string(StatusNoGate) + ")"
	if msg := rpcErrorMessage(t, w); msg != wantMsg {
		t.Errorf("the refusal must read exactly %q so a different status cannot pass for it; got %q",
			wantMsg, msg)
	}
	// ...and from the deny-closed DEFAULT: a wired gate reporting the same status would
	// otherwise certify a default that was never installed.
	if _, isDefault := rs.gate.(denyApprovalGate); !isDefault {
		t.Errorf("this test certifies the deny-closed default; the server's gate is %T", rs.gate)
	}
	if up.called {
		t.Error("deny-closed gate must not reach the upstream")
	}
}

// TestRSDenyByDefault: a tool absent from the server toolset → 403, upstream not called.
func TestRSDenyByDefault(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read tools:admin", validExp())
	up := &fakeUpstream{}
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, up)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "unknown_tool", "{}"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unknown-tool status = %d, want 403 (deny-by-default)", w.Code)
	}
	if up.called {
		t.Error("a tool not in the server toolset must not reach the upstream")
	}
}

// TestRSInvalidOrigin: a browser request with an Origin not on the allowlist → 403,
// before any token work.
func TestRSInvalidOrigin(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, &fakeUpstream{})
	req := toolsCallReq(token, "search", "{}")
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("invalid-origin status = %d, want 403 (PR#1439)", w.Code)
	}
	// An allowed Origin passes the check (reaches auth → 200).
	req2 := toolsCallReq(token, "search", "{}")
	req2.Header.Set("Origin", rsOrigin)
	w2 := httptest.NewRecorder()
	rs.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("allowed-origin status = %d, want 200", w2.Code)
	}
}

// TestRSInputErrorIsToolError: a tools/call with no name is a SEP-1303 input failure →
// HTTP 200 with a result carrying isError:true (NOT a protocol error).
func TestRSInputErrorIsToolError(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, &fakeUpstream{})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("input-error status = %d, want 200 (SEP-1303 isError result)", w.Code)
	}
	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Result.IsError || resp.Error != nil {
		t.Errorf("input failure must be a result with isError:true, not a protocol error: %s", w.Body.String())
	}
}

// TestRSToolsListFiltered: tools/list is filtered to the server-owned toolset (deny-
// by-default at discovery): a tool the server does not allow is never advertised.
func TestRSToolsListFiltered(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(`{"tools":[{"name":"search"},{"name":"secret_tool"}]}`)}
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, up)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, want 200", w.Code)
	}
	if strings.Contains(w.Body.String(), "secret_tool") {
		t.Error("tools/list must NOT advertise a tool absent from the server toolset")
	}
	if !strings.Contains(w.Body.String(), "search") {
		t.Error("tools/list must advertise an allowed tool")
	}
}

// TestRSNoTrustAnchorRejected: construction fails without a trust anchor (never a
// silently-open RS).
func TestRSNoTrustAnchorRejected(t *testing.T) {
	ts, _ := NewToolset(nil)
	_, err := NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer}, Toolset: ts,
	})
	if err == nil {
		t.Error("an RS with no token trust anchor must not be constructed")
	}
}

// signer mints multiple access tokens with ONE EC key (so tests can isolate the
// audience check from the signature check: a foreign-audience token signed by a
// trusted key fails on audience, not signature).
type signer struct {
	js   jose.Signer
	jwks []byte
}

func newSigner(t *testing.T) *signer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	js, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	ks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: "k1", Algorithm: "ES256", Use: "sig"}}}
	blob, err := json.Marshal(ks)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return &signer{js: js, jwks: blob}
}

func (s *signer) mint(t *testing.T, aud, scope string, exp time.Time) string {
	t.Helper()
	std := jwt.Claims{
		Issuer:   rsIssuer,
		Subject:  "agent:claude",
		Audience: jwt.Audience{aud},
		IssuedAt: jwt.NewNumericDate(rsClock().Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(exp),
	}
	raw, err := jwt.Signed(s.js).Claims(std).Claims(map[string]any{"scope": scope}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

// --- pin verifier integration tests -----------------------------------------

// fakePinVerifier is a test double for ToolPinVerifier.
type fakePinVerifier struct {
	allowed bool
	reason  string
	err     error
	// recorded tracks RecordPin calls.
	recorded []string
}

func (f *fakePinVerifier) Verify(_ context.Context, _, _, _ string) (bool, string, error) {
	return f.allowed, f.reason, f.err
}

func (f *fakePinVerifier) RecordPin(_ context.Context, server, toolName, fp string) error {
	f.recorded = append(f.recorded, server+"/"+toolName+"="+fp)
	return nil
}

// newRSWithPin builds an RS with a PinVerifier wired for pin-gate tests.
func newRSWithPin(t *testing.T, jwks []byte, pv ToolPinVerifier) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
	})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:                   rsResource,
		AuthorizationServers:       []string{rsIssuer},
		ScopesSupported:            []string{"tools:read"},
		Issuer:                     rsIssuer,
		IssuerJWKS:                 jwks,
		Toolset:                    ts,
		AllowedOrigins:             []string{rsOrigin},
		Gate:                       fakeToolGate{StatusApproved},
		Upstream:                   &fakeUpstream{},
		DurableTaskStore:           newMemoryDurableTaskStore(),
		Auditor:                    &fakeEvidenceJournal{}, // granting journal (enforcement pinned by evidence_test.go)
		Clock:                      rsClock,
		PinVerifier:                pv,
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs with pin: %v", err)
	}
	return rs
}

// TestRSPinMismatchDenied: a PinVerifier that reports mismatch blocks the
// tools/call with 403 (MCP04 deny-closed on rug-pull).
func TestRSPinMismatchDenied(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	pv := &fakePinVerifier{allowed: false, reason: "definition changed"}
	rs := newRSWithPin(t, jwks, pv)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{"q":"test"}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("pin mismatch status = %d, want 403 (deny-closed rug-pull)", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "rug-pull") {
		t.Errorf("403 body must mention rug-pull, got: %s", body)
	}
}

// TestRSPinErrorDenied: a PinVerifier that returns an error is treated as
// deny-closed (fail-closed on infrastructure error, MCP04).
func TestRSPinErrorDenied(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	pv := &fakePinVerifier{err: errors.New("store unavailable")}
	rs := newRSWithPin(t, jwks, pv)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{"q":"test"}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("pin error status = %d, want 403 (fail-closed)", w.Code)
	}
}

// TestRSPinMatchAllows: a PinVerifier that reports match (allowed=true) lets
// the tools/call proceed normally.
func TestRSPinMatchAllows(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	pv := &fakePinVerifier{allowed: true}
	rs := newRSWithPin(t, jwks, pv)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{"q":"test"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("pin match status = %d, want 200 (allowed)", w.Code)
	}
}

// TestRSNilPinVerifierAllows: nil PinVerifier (community build) must not block
// any tools/call — the gate is purely additive.
func TestRSNilPinVerifierAllows(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	// newRS uses nil PinVerifier (the default).
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, &fakeUpstream{})
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{"q":"test"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("nil-pin-verifier status = %d, want 200 (community build, additive)", w.Code)
	}
}
