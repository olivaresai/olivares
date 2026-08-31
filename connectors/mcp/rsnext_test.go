// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// capturingAuditor records ToolDecisions so the tests can assert the SEP-414
// trace correlation on PEP decisions. It embeds the granting in-memory journal
//: enforcement legs behave contract-faithfully while decisions are captured.
type capturingAuditor struct {
	fakeEvidenceJournal
	decisions []ToolDecision
}

func (a *capturingAuditor) Record(ctx context.Context, d ToolDecision, binding sdk.EvidenceBinding) GateRecord {
	a.decisions = append(a.decisions, d) // tests using this fake are sequential
	return a.fakeEvidenceJournal.Record(ctx, d, binding)
}

// newRSNext builds the PEP with the RC strict header gate ON (and a capturing
// auditor), mirroring the stable newRS toolset.
func newRSNext(t *testing.T, jwks []byte, up Upstream, aud GateAuditor) *ResourceServer {
	t.Helper()
	return newRSTestMode(t, jwks, up, aud, revisionModeRCStrict)
}

func newRSDual(t *testing.T, jwks []byte, up Upstream, aud GateAuditor) *ResourceServer {
	t.Helper()
	return newRSTestMode(t, jwks, up, aud, "")
}

func newRSTestMode(t *testing.T, jwks []byte, up Upstream, aud GateAuditor, revisionMode string) *ResourceServer {
	t.Helper()
	return newRSTestModeTools(t, jwks, up, aud, revisionMode)
}

func newRSTestModeTools(t *testing.T, jwks []byte, up Upstream, aud GateAuditor, revisionMode string, extra ...ToolPolicy) *ResourceServer {
	t.Helper()
	ts, err := NewToolset(append([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "delete_db", RequiredScope: "tools:admin", Destructive: true},
	}, extra...))
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:             rsResource,
		AuthorizationServers: []string{rsIssuer},
		Issuer:               rsIssuer,
		IssuerJWKS:           jwks,
		Toolset:              ts,
		Gate:                 fakeToolGate{StatusApproved},
		DurableTaskStore:     newMemoryDurableTaskStore(),
		Upstream:             up,
		Auditor:              aud,
		Clock:                rsClock,
		RevisionMode:         revisionMode,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

// nextReq builds a POST with the RC routing headers and the given body.
// withRCMeta injects the per-request protocol fields a conforming 2026-07-28
// client MUST send (`params._meta.protocolVersion` + `.clientCapabilities`),
// unless the body already carries a `_meta`. The fixtures used to omit them
// entirely, so every RC test modeled a NON-conforming client and the gateway's
// acceptance of those requests went unnoticed — which is how the header/body
// version split-brain stayed invisible.
func withRCMeta(body string) string {
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &envelope) != nil {
		return body // not an object: leave malformed-body fixtures untouched
	}
	var params map[string]json.RawMessage
	if raw, ok := envelope["params"]; !ok || json.Unmarshal(raw, &params) != nil {
		params = map[string]json.RawMessage{}
	}
	// MERGE rather than replace or skip: a fixture that sets its own `_meta` (a
	// traceparent, say) still needs the required protocol fields, and must keep
	// what it deliberately stated.
	meta := map[string]json.RawMessage{}
	if raw, ok := params["_meta"]; ok {
		_ = json.Unmarshal(raw, &meta)
	}
	if _, ok := meta[metaProtocolVersion]; !ok {
		meta[metaProtocolVersion] = json.RawMessage(`"` + revision20260728 + `"`)
	}
	if _, ok := meta[metaClientCapabilities]; !ok {
		meta[metaClientCapabilities] = json.RawMessage(`{}`)
	}
	m, err := json.Marshal(meta)
	if err != nil {
		return body
	}
	params["_meta"] = m
	p, err := json.Marshal(params)
	if err != nil {
		return body
	}
	envelope["params"] = p
	out, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return string(out)
}

func nextReq(token, method, name, body string) *http.Request {
	return nextReqRaw(token, method, name, withRCMeta(body))
}

// nextReqRaw is nextReq WITHOUT the conformance injection, for tests that must
// send exactly the body they wrote — including a deliberately non-conforming one.
func nextReqRaw(token, method, name, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(headerMCPProtocolVersion, revision20260728)
	req.Header.Set(headerMcpMethod, method)
	if name != "" {
		req.Header.Set(headerMcpName, name)
	}
	return req
}

func rpcErrorCode(t *testing.T, body string) int {
	t.Helper()
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode rpc error: %v; body=%s", err, body)
	}
	return resp.Error.Code
}

// TestRSNextRequiredHeaders: flag ON — missing/expected-absent headers are
// refused with HTTP 400 + -32020 HeaderMismatch (spec MUST), before the body.
func TestRSNextRequiredHeaders(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{}
	rs := newRSNext(t, jwks, up, &capturingAuditor{})

	cases := []struct {
		name string
		mod  func(*http.Request)
		// wantCode distinguishes the two refusals the spec assigns different
		// codes to: an ABSENT header is HeaderMismatch, while a NAMED version
		// this server does not implement MUST be UnsupportedProtocolVersion
		// carrying data.supported — including a KNOWN version the server has
		// chosen not to support (streamable-http.mdx §Protocol Version Header).
		wantCode int
	}{
		{"missing protocol version", func(r *http.Request) { r.Header.Del(headerMCPProtocolVersion) }, rpcHeaderMismatch},
		{"pre-RC version (downgrade guard)", func(r *http.Request) { r.Header.Set(headerMCPProtocolVersion, revision20251125) }, rpcUnsupportedProtocolVersion},
		{"missing Mcp-Method", func(r *http.Request) { r.Header.Del(headerMcpMethod) }, rpcHeaderMismatch},
		{"Mcp-Name on a method that must omit it", func(r *http.Request) {
			r.Header.Set(headerMcpMethod, "tools/list")
			r.Header.Set(headerMcpName, "sneaky")
		}, rpcHeaderMismatch},
		{"tools/call without Mcp-Name", func(r *http.Request) { r.Header.Del(headerMcpName) }, rpcHeaderMismatch},
	}
	for _, c := range cases {
		req := nextReq(token, "tools/call", "search", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search"}}`)
		c.mod(req)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", c.name, w.Code)
			continue
		}
		var resp struct {
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		// A NAMED version this server does not implement is
		// UnsupportedProtocolVersion (-32022) carrying data.supported; only an
		// ABSENT/blank header is HeaderMismatch (-32020). streamable-http.mdx
		// §Protocol Version Header is explicit that a "known version the server
		// has chosen not to support" takes -32022 too, so the downgrade guard
		// answers with the list the client may retry with instead of a bare
		// mismatch. This assertion used to demand -32020 for both.
		if resp.Error.Code != c.wantCode {
			t.Errorf("%s: error code = %d, want %d", c.name, resp.Error.Code, c.wantCode)
		}
		if up.called {
			t.Errorf("%s: a header-refused request must never reach the upstream", c.name)
		}
	}
}

// TestRSNextRemovedMethodAtL7: a method the RC deleted is answered 404 +
// -32601 from the headers alone (the unreadable body proves no parse happened).
func TestRSNextRemovedMethodAtL7(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{}
	rs := newRSNext(t, jwks, up, &capturingAuditor{})
	req := nextReq(token, "tasks/list", "", `this is not even JSON`)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("removed-method status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "-32601") {
		t.Errorf("removed method must answer -32601, got %s", w.Body.String())
	}
	if up.called {
		t.Error("a removed method must never reach the upstream")
	}
}

// TestRSNextPreBodyDeny: the tools/call deny decision is made from Mcp-Name
// BEFORE the body is read — an unparseable body still gets the policy denial
// (deny-by-default and scope step-up), and the L7 decision is audited.
func TestRSNextPreBodyDeny(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{}
	aud := &capturingAuditor{}
	rs := newRSNext(t, jwks, up, aud)

	// Unknown tool: deny-by-default at L7. The garbage body proves the decision
	// needed no body parse.
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, nextReq(token, "tools/call", "unknown_tool", `garbage`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("L7 deny-by-default status = %d, want 403", w.Code)
	}
	// Insufficient scope: step-up challenge from headers alone.
	w2 := httptest.NewRecorder()
	rs.ServeHTTP(w2, nextReq(token, "tools/call", "delete_db", `garbage`))
	if w2.Code != http.StatusForbidden {
		t.Fatalf("L7 scope-deny status = %d, want 403", w2.Code)
	}
	if !strings.Contains(w2.Header().Get("WWW-Authenticate"), `scope="tools:admin"`) {
		t.Errorf("L7 scope denial must carry the step-up challenge, got %q", w2.Header().Get("WWW-Authenticate"))
	}
	if up.called {
		t.Error("an L7-denied call must never reach the upstream")
	}
	if len(aud.decisions) != 2 {
		t.Fatalf("audited decisions = %d, want 2", len(aud.decisions))
	}
	for _, d := range aud.decisions {
		if d.Allowed || !strings.Contains(d.Reason, "L7") {
			t.Errorf("L7 denial must be audited as such: %+v", d)
		}
	}
}

// TestRSNextAllowedFlowAndTrace: a fully-consistent RC request is forwarded;
// the `_meta` traceparent (SEP-414) wins over the HTTP header on BOTH the
// upstream propagation and the audited decision.
func TestRSNextAllowedFlowAndTrace(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{}
	aud := &capturingAuditor{}
	rs := newRSNext(t, jwks, up, aud)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{},"_meta":{"traceparent":"00-meta-trace-01"}}}`
	req := nextReq(token, "tools/call", "search", body)
	req.Header.Set("traceparent", "00-header-trace-02")
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("allowed RC tools/call status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !up.called {
		t.Fatal("the consistent RC call must reach the upstream")
	}
	if up.gotReq.TraceParent != "00-meta-trace-01" {
		t.Errorf("upstream traceparent = %q, want the `_meta` value (preferred over the HTTP header)", up.gotReq.TraceParent)
	}
	last := aud.decisions[len(aud.decisions)-1]
	if !last.Allowed || last.TraceParent != "00-meta-trace-01" {
		t.Errorf("the allow decision must be audited with the `_meta` trace: %+v", last)
	}
}

// TestRSHeaderBodyMismatchBothModes: a volunteered mirror that contradicts the
// body is refused in BOTH modes (smuggling defense) — flag OFF included.
func TestRSHeaderBodyMismatchBothModes(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	// Flag OFF (stable RS): Mcp-Method says tools/list, body says tools/call.
	upOff := &fakeUpstream{}
	rsOff := newRS(t, jwks, fakeToolGate{StatusApproved}, upOff)
	req := toolsCallReq(token, "search", "{}")
	req.Header.Set(headerMcpMethod, "tools/list")
	w := httptest.NewRecorder()
	rsOff.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("flag-OFF mismatch status = %d, want 400 (-32020)", w.Code)
	}
	if upOff.called {
		t.Error("a mismatched request must never reach the upstream")
	}

	// Flag ON: Mcp-Name says search, body names another tool.
	upOn := &fakeUpstream{}
	rsOn := newRSNext(t, jwks, upOn, &capturingAuditor{})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_db","arguments":{}}}`
	w2 := httptest.NewRecorder()
	rsOn.ServeHTTP(w2, nextReq(token, "tools/call", "search", body))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("flag-ON name mismatch status = %d, want 400", w2.Code)
	}
	if upOn.called {
		t.Error("a name-mismatched request must never reach the upstream")
	}
}

// TestRSLegacyRequestsUnchanged: with the flag OFF, a 2025-11-25 request that
// volunteers no RC headers behaves exactly as before (announced compat) — and
// every response now carries deny-closed cache directives.
func TestRSLegacyRequestsUnchanged(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{}
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, up)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", "{}"))
	if w.Code != http.StatusOK {
		t.Fatalf("legacy tools/call status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("a tools/call result must be no-store, got %q", got)
	}
	if got := w.Header().Get("Vary"); got != "Authorization" {
		t.Errorf("responses must vary on Authorization, got %q", got)
	}
}

func TestRSDualModeRevisionSelection(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	t.Run("RC headers take strict path", func(t *testing.T) {
		up := &fakeUpstream{}
		aud := &capturingAuditor{}
		rs := newRSDual(t, jwks, up, aud)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, nextReq(token, "tools/call", "unknown_tool", `garbage`))
		if w.Code != http.StatusForbidden {
			t.Fatalf("dual RC L7 denial status = %d, want 403", w.Code)
		}
		if up.called {
			t.Fatal("dual RC L7 denial must not reach upstream")
		}
		if len(aud.decisions) != 1 || !strings.Contains(aud.decisions[0].Reason, "L7") {
			t.Fatalf("dual RC path did not audit an L7 decision: %+v", aud.decisions)
		}
	})

	t.Run("legacy revision with no mirrors passes", func(t *testing.T) {
		up := &fakeUpstream{}
		rs := newRSDual(t, jwks, up, &capturingAuditor{})
		req := toolsCallReq(token, "search", "{}")
		req.Header.Set(headerMCPProtocolVersion, revision20251125)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("legacy-in-dual status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		if !up.called {
			t.Fatal("legacy-in-dual request must reach upstream")
		}
	})

	t.Run("legacy volunteered mismatch is refused", func(t *testing.T) {
		up := &fakeUpstream{}
		rs := newRSDual(t, jwks, up, &capturingAuditor{})
		req := toolsCallReq(token, "search", "{}")
		req.Header.Set(headerMCPProtocolVersion, revision20251125)
		req.Header.Set(headerMcpMethod, "tools/list")
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || rpcErrorCode(t, w.Body.String()) != rpcHeaderMismatch {
			t.Fatalf("legacy mirror mismatch = status %d body %s, want 400/%d", w.Code, w.Body.String(), rpcHeaderMismatch)
		}
		if up.called {
			t.Fatal("legacy mirror mismatch must not reach upstream")
		}
	})

	t.Run("absent protocol version refused", func(t *testing.T) {
		rs := newRSDual(t, jwks, &fakeUpstream{}, &capturingAuditor{})
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, toolsCallReq(token, "search", "{}"))
		if w.Code != http.StatusBadRequest || rpcErrorCode(t, w.Body.String()) != rpcHeaderMismatch {
			t.Fatalf("absent version = status %d body %s, want 400/%d", w.Code, w.Body.String(), rpcHeaderMismatch)
		}
		if !strings.Contains(w.Body.String(), headerMCPProtocolVersion) {
			t.Fatalf("absent-version message must name %s: %s", headerMCPProtocolVersion, w.Body.String())
		}
	})

	t.Run("unknown protocol version refused", func(t *testing.T) {
		rs := newRSDual(t, jwks, &fakeUpstream{}, &capturingAuditor{})
		req := toolsCallReq(token, "search", "{}")
		req.Header.Set(headerMCPProtocolVersion, "not-a-date")
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, req)
		// A named-but-unknown version is -32022 with data.supported, not a bare
		// header mismatch: the client needs the list to retry with.
		if w.Code != http.StatusBadRequest || rpcErrorCode(t, w.Body.String()) != rpcUnsupportedProtocolVersion {
			t.Fatalf("unknown version = status %d body %s, want 400/%d", w.Code, w.Body.String(), rpcUnsupportedProtocolVersion)
		}
	})
}

func TestRSNextRemovedNotificationsAndListenDenyClosed(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	up := &fakeUpstream{}
	rs := newRSDual(t, jwks, up, &capturingAuditor{})
	req := nextReq(token, "notifications/roots/list_changed", "", `this body is not read`)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || rpcErrorCode(t, w.Body.String()) != -32601 {
		t.Fatalf("removed notification = status %d body %s, want 404/-32601", w.Code, w.Body.String())
	}
	if up.called {
		t.Fatal("removed RC notification must not reach upstream")
	}

	aud := &capturingAuditor{}
	upListen := &fakeUpstream{}
	rsListen := newRSDual(t, jwks, upListen, aud)
	body := `{"jsonrpc":"2.0","id":7,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true}}}`
	w2 := httptest.NewRecorder()
	rsListen.ServeHTTP(w2, nextReq(token, methodSubscriptionsListen, "", body))
	if w2.Code != http.StatusServiceUnavailable || rpcErrorCode(t, w2.Body.String()) != rpcEvidenceUnavailable {
		t.Fatalf("unwired listen = status %d body %s, want 503/%d", w2.Code, w2.Body.String(), rpcEvidenceUnavailable)
	}
	if !strings.Contains(w2.Body.String(), "durable subscriptions/listen relay unavailable") {
		t.Fatalf("unwired listen refusal must name the missing durable relay: %s", w2.Body.String())
	}
	if upListen.called {
		t.Fatal("subscriptions/listen must not reach request/response upstream")
	}
	if len(aud.decisions) != 1 || aud.decisions[0].Allowed || aud.decisions[0].Tool != methodSubscriptionsListen {
		t.Fatalf("listen refusal must be audited: %+v", aud.decisions)
	}
}

func TestRSNextMcpParamValidation(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	cases := []struct {
		name       string
		args       string
		headerName string
		headerVal  string
		wantStatus int
		wantCalled bool
	}{
		{"matching string", `{"query":"hello"}`, "query", "hello", http.StatusOK, true},
		{"mismatch", `{"query":"hello"}`, "query", "goodbye", http.StatusBadRequest, false},
		{"base64 sentinel", `{"query":"caf\u00e9"}`, "query", "=?base64?Y2Fmw6k=?=", http.StatusOK, true},
		{"absent argument", `{"query":"hello"}`, "missing", "hello", http.StatusBadRequest, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			up := &fakeUpstream{}
			rs := newRSDual(t, jwks, up, &capturingAuditor{})
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":` + c.args + `}}`
			req := nextReq(token, "tools/call", "search", body)
			req.Header.Set("Mcp-Param-"+c.headerName, c.headerVal)
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, req)
			if w.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, c.wantStatus, w.Body.String())
			}
			if c.wantStatus == http.StatusBadRequest && rpcErrorCode(t, w.Body.String()) != rpcHeaderMismatch {
				t.Fatalf("bad Mcp-Param must be HeaderMismatch: %s", w.Body.String())
			}
			if up.called != c.wantCalled {
				t.Fatalf("upstream called = %v, want %v", up.called, c.wantCalled)
			}
		})
	}
}

func TestRSRevisionModeValidation(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	ts, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	_, err = NewResourceServer(ResourceServerConfig{
		Resource:             rsResource,
		AuthorizationServers: []string{rsIssuer},
		Issuer:               rsIssuer,
		IssuerJWKS:           jwks,
		Toolset:              ts,
		RevisionMode:         "surprise",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown revision_mode") {
		t.Fatalf("unknown revision mode must fail closed, got %v", err)
	}
}

// TestRSCacheScopeEnforcement: SEP-2549 — the relayed metadata becomes
// Cache-Control; the per-principal filtered tools/list is ALWAYS downgraded to
// private (body + HTTP agree), other reads honor the upstream scope, absent
// metadata is no-store.
func TestRSCacheScopeEnforcement(t *testing.T) {
	// resources:read is required for the resources/read leg (F-06 method matrix); tools:read
	// alone covers the tools/list leg.
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read resources:read", validExp())

	listReq := func() *http.Request {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
		r := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}

	// tools/list with upstream cacheScope=public: filtered per principal ⇒ the
	// RS downgrades to private in BOTH the HTTP directive and the body, and
	// preserves the RC sibling fields it does not own (resultType, ttlMs).
	upList := &fakeUpstream{result: json.RawMessage(`{"resultType":"complete","tools":[{"name":"search"},{"name":"secret_tool"}],"ttlMs":300000,"cacheScope":"public"}`)}
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, upList)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, listReq())
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "private, max-age=300" {
		t.Errorf("filtered tools/list Cache-Control = %q, want private, max-age=300 (public downgraded)", got)
	}
	var resp struct {
		Result struct {
			ResultType string          `json:"resultType"`
			Tools      json.RawMessage `json:"tools"`
			TTLMs      *int64          `json:"ttlMs"`
			CacheScope string          `json:"cacheScope"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result.CacheScope != cacheScopePrivate {
		t.Errorf("body cacheScope = %q, want private (must agree with the HTTP directive)", resp.Result.CacheScope)
	}
	if resp.Result.ResultType != "complete" || resp.Result.TTLMs == nil || *resp.Result.TTLMs != 300000 {
		t.Errorf("filtering must preserve the RC sibling fields: %+v", resp.Result)
	}
	if strings.Contains(string(resp.Result.Tools), "secret_tool") {
		t.Error("filtering itself must still apply")
	}

	// A non-filtered cacheable read honors the upstream public scope.
	upRead := &fakeUpstream{result: json.RawMessage(`{"resultType":"complete","contents":[],"ttlMs":60000,"cacheScope":"public"}`)}
	rs2 := newRS(t, jwks, fakeToolGate{StatusApproved}, upRead)
	body := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"file:///a"}}`
	r2 := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	r2.Header.Set("Authorization", "Bearer "+token)
	w2 := httptest.NewRecorder()
	rs2.ServeHTTP(w2, r2)
	if got := w2.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("public resources/read Cache-Control = %q, want public, max-age=60", got)
	}

	// No cache metadata (a 2025-11-25 upstream) ⇒ deny-closed no-store.
	upLegacy := &fakeUpstream{result: json.RawMessage(`{"tools":[{"name":"search"}]}`)}
	rs3 := newRS(t, jwks, fakeToolGate{StatusApproved}, upLegacy)
	w3 := httptest.NewRecorder()
	rs3.ServeHTTP(w3, listReq())
	if got := w3.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("metadata-less result Cache-Control = %q, want no-store", got)
	}
}
