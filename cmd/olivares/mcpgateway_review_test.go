// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
)

// mcpgateway_review_test.go — Stage-3 Codex round-1 MUST-FIX proofs at the
// composition root: tenant canonicalization (P0 finding 2), the HTTP forwarder
// dispatch-classification matrix (P1 finding 4), and the full production
// interleaving (P2 finding 5, real store + barrier + >=32 identical requests).

const mcpReviewResource = "https://mcp.review.example/gw"

// mintReviewToken signs an at+jwt access token for the review MCP RS.
func mintReviewToken(t *testing.T, aud, scope string) (token string, jwks []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", "rk1"))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	now := time.Now()
	std := jwt.Claims{
		Issuer:   "https://auth.review.example",
		Subject:  "agent:review",
		Audience: jwt.Audience{aud},
		IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(now.Add(time.Hour)),
	}
	raw, err := jwt.Signed(signer).Claims(std).Claims(map[string]any{"scope": scope}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: "rk1", Algorithm: "ES256", Use: "sig"}}}
	blob, err := json.Marshal(ks)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return raw, blob
}

// countingUpstreamServer is an httptest MCP upstream that returns a valid
// correlated JSON-RPC result and counts the tools/call forwards it observes.
func countingUpstreamServer(t *testing.T, calls *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Method == "tools/call" {
			atomic.AddInt32(calls, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	}))
}

func buildReviewMCP(t *testing.T, eng *engine, jwks []byte, upstreamURL, tenantStr string) *mcpc.ResourceServer {
	t.Helper()
	cfg := &mcpGatewayConfig{
		Resource:             mcpReviewResource,
		AuthorizationServers: []string{"https://auth.review.example"},
		Issuer:               "https://auth.review.example",
		IssuerJWKS:           json.RawMessage(jwks),
		Tenant:               tenantStr,
		UpstreamURL:          upstreamURL,
		Tools:                []mcpc.ToolPolicy{{Name: "search", RequiredScope: "tools:read"}},
		NextRevisionHeaders:  false, // legacy body path for these tests
	}
	rs, _, err := buildMCPResourceServerWithDurableTaskStore(
		eng,
		cfg,
		discardLogger(),
		newMCPMemoryDurableTaskStore(),
	)
	if err != nil {
		t.Fatalf("build review MCP RS (tenant %q): %v", tenantStr, err)
	}
	return rs
}

// TestMCPTenantCanonicalizationPreventsDoubleEffect — finding 2 (P0): two RS
// reconstructions with equivalent-but-differently-represented tenant strings
// (uppercase vs lowercase UUID) sharing ONE store must, for the same op-keyed
// request, forward EXACTLY once. Pre-fix the raw cfg.Tenant fed the OperationID
// derivation, so the two representations minted two fresh operations → a double
// effect across a restart/config-normalization.
func TestMCPTenantCanonicalizationPreventsDoubleEffect(t *testing.T) {
	f := newMCPLedgerFixture(t)
	eng := &engine{store: f.store, log: discardLogger()}
	var calls int32
	up := countingUpstreamServer(t, &calls)
	defer up.Close()

	// ONE matched (token, jwks) pair; both RS trust the same key.
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")

	// The fixture tenant, in two equivalent representations.
	canonical := f.tenant.String()
	upper := strings.ToUpper(canonical)
	if upper == canonical {
		t.Skip("tenant has no case to vary")
	}

	rsA := buildReviewMCP(t, eng, jwks, up.URL, upper)     // raw uppercase
	rsB := buildReviewMCP(t, eng, jwks, up.URL, canonical) // canonical lower

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"q":"x"},"_meta":{"ai.olivares/operationId":"dedup-key-1"}}}`
	post := func(rs *mcpc.ResourceServer) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, mcpReviewResource, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, req)
		return w
	}
	w1 := post(rsA)
	if w1.Code != http.StatusOK {
		t.Fatalf("first (uppercase-tenant) call status = %d; body=%s", w1.Code, w1.Body.String())
	}
	w2 := post(rsB)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream forwards across two tenant representations = %d, want EXACTLY 1 (no double effect)", got)
	}
	if w2.Code != http.StatusConflict {
		t.Errorf("second (canonical-tenant) call status = %d, want 409 (exact replay); body=%s", w2.Code, w2.Body.String())
	}
}

// TestMCPUpstreamForwarderDispatchClassification — finding 4/5: the HTTP
// forwarder classifies each leg honestly. Pre-fix, non-2xx and malformed/
// uncorrelated 2xx bodies were laundered as completed.
func TestMCPUpstreamForwarderDispatchClassification(t *testing.T) {
	newForwarder := func(url string) *mcpUpstreamForwarder {
		return &mcpUpstreamForwarder{url: url, client: &http.Client{Timeout: 5 * time.Second}}
	}
	call := func(f *mcpUpstreamForwarder) (mcpc.UpstreamResult, error) {
		return f.Forward(context.Background(), mcpc.UpstreamRequest{Method: "tools/call", Params: []byte(`{"name":"search"}`)})
	}

	t.Run("valid result is completed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":1}}`))
		}))
		defer srv.Close()
		res, err := call(newForwarder(srv.URL))
		if err != nil || res.State != mcpc.DispatchCompleted || len(res.Result) == 0 {
			t.Fatalf("valid result = %+v err=%v, want completed with a result", res, err)
		}
	})

	t.Run("valid JSON-RPC error is completed round-trip", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"tool failed"}}`))
		}))
		defer srv.Close()
		res, err := call(newForwarder(srv.URL))
		if err == nil || res.State != mcpc.DispatchCompleted {
			t.Fatalf("json-rpc error = %+v err=%v, want completed + error", res, err)
		}
	})

	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"non-2xx", `{"jsonrpc":"2.0","id":1,"result":{}}`, http.StatusInternalServerError},
		{"malformed body", `not json`, http.StatusOK},
		{"wrong id", `{"jsonrpc":"2.0","id":99,"result":{}}`, http.StatusOK},
		{"missing result and error", `{"jsonrpc":"2.0","id":1}`, http.StatusOK},
		{"both result and error", `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-1,"message":"x"}}`, http.StatusOK},
		{"wrong jsonrpc version", `{"jsonrpc":"1.0","id":1,"result":{}}`, http.StatusOK},
	} {
		t.Run(tc.name+" is unknown", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			res, err := call(newForwarder(srv.URL))
			if err == nil || res.State != mcpc.DispatchUnknown {
				t.Fatalf("%s = state %q err=%v, want unknown + error (never laundered as completed)", tc.name, res.State, err)
			}
		})
	}

	t.Run("connection refused after Do is unknown", func(t *testing.T) {
		res, err := call(newForwarder("http://127.0.0.1:1/")) // nothing listening
		if err == nil || res.State != mcpc.DispatchUnknown {
			t.Fatalf("connection refused = state %q err=%v, want unknown", res.State, err)
		}
	})

	t.Run("credential-provider failure before Do is not_sent", func(t *testing.T) {
		f := newForwarder("http://127.0.0.1:1/")
		f.credProv = erroringCredProvider{}
		res, err := call(f)
		if err == nil || res.State != mcpc.DispatchNotSent {
			t.Fatalf("cred-provider failure = state %q err=%v, want not_sent (nothing dispatched)", res.State, err)
		}
	})
}

type erroringCredProvider struct{}

func (erroringCredProvider) Credential(context.Context, string) (string, error) {
	return "", fmt.Errorf("credential unavailable")
}

// countMCPToolCallEvents walks the tenant ledger and counts the enforced
// tools/call claim/settle events, returning the claim event's TargetID (the
// OperationID) when exactly one claim exists.
func countMCPToolCallEvents(t *testing.T, f *mcpLedgerFixture) (claims, settles int, opID string) {
	t.Helper()
	for _, ev := range mcpLedgerEventsFrom(t, f.store, f.tenant, 0) {
		switch ev.Action {
		case "mcp.tool.call.keyed.claim":
			claims++
			opID = string(ev.TargetID)
		case "mcp.tool.call.keyed.settle":
			settles++
		}
	}
	return claims, settles, opID
}

// TestMCPFullInterleavingSingleEffect — finding 5 (P2) + round-2 NEW-2: the REAL
// store + a barrier + 40 identical op-keyed requests through the full ServeHTTP
// (Record→BeforeEffect→Forward→Settle). The winning upstream call is HELD between
// claim and settle while the other 39 receive their replay-pending 409s — proving
// the claim (not the settlement) is what fences duplicates — then released. The
// ledger must carry EXACTLY one claim and one settle event; the journal row must
// settle completed; the forwarded request must carry the claimed OperationID.
func TestMCPFullInterleavingSingleEffect(t *testing.T) {
	f := newMCPLedgerFixture(t)
	eng := &engine{store: f.store, log: discardLogger()}
	release := make(chan struct{})
	var calls int32
	var forwardedOpID atomic.Value
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Method == "tools/call" {
			atomic.AddInt32(&calls, 1)
			forwardedOpID.Store(r.Header.Get("Olivares-Operation-Id"))
			<-release // hold the winner between its claim and its settle
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	}))
	defer up.Close()
	// Release the held winner exactly once, and ALWAYS on unwind: a t.Fatalf while
	// the winner is blocked on <-release would otherwise deadlock up.Close() (it
	// waits for the in-flight handler to return). Registered AFTER defer up.Close()
	// so LIFO runs it FIRST — the handler unblocks before Close waits.
	var releaseOnce sync.Once
	releaseWinner := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWinner()

	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	rs := buildReviewMCP(t, eng, jwks, up.URL, f.tenant.String())

	const n = 40
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"q":"x"},"_meta":{"ai.olivares/operationId":"interleave-key-1"}}}`
	var start sync.WaitGroup
	start.Add(1)
	results := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() {
			start.Wait() // barrier: release all goroutines together
			req := httptest.NewRequest(http.MethodPost, mcpReviewResource, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, req)
			results <- w.Code
		}()
	}
	start.Done()

	// While the winner is HELD between claim and settle, the other 39 must all
	// complete with replay-pending 409s (the CLAIM is the duplicate fence).
	deadline := time.After(60 * time.Second)
	for i := 0; i < n-1; i++ {
		select {
		case c := <-results:
			if c != http.StatusConflict {
				t.Fatalf("concurrent duplicate %d returned %d while the winner was held, want 409", i, c)
			}
		case <-deadline:
			t.Fatal("timed out waiting for the 39 replay-pending responses (winner held)")
		}
	}
	claims, settles, heldOpID := countMCPToolCallEvents(t, f)
	if claims != 1 || settles != 0 {
		t.Fatalf("ledger while winner held: claims=%d settles=%d, want 1/0", claims, settles)
	}
	// The claimed operation id was being read here and thrown away (ineffassign), and
	// the discarded value is the one thing this phase can establish that the final
	// phase cannot: that the id the 39 duplicates were rejected against is ALREADY on
	// the ledger while the winner is still in flight, and is the SAME id that later
	// settles. Without this, a change that re-claimed under a fresh id between the two
	// reads would leave every assertion below still passing.
	if heldOpID == "" {
		t.Fatal("the claim event carries no operation id while the winner is held; the 39 duplicates were rejected against nothing identifiable")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream forwards while winner held = %d, want EXACTLY 1", got)
	}

	releaseWinner() // let the winner settle and respond
	select {
	case c := <-results:
		if c != http.StatusOK {
			t.Fatalf("winner status = %d, want 200", c)
		}
	case <-deadline:
		t.Fatal("timed out waiting for the winner's response")
	}
	claims, settles, opID := countMCPToolCallEvents(t, f)
	if claims != 1 || settles != 1 {
		t.Fatalf("final ledger: claims=%d settles=%d, want exactly 1/1", claims, settles)
	}
	if opID != heldOpID {
		t.Fatalf("the settled operation id %q is not the one claimed while the winner was held (%q): the single effect was re-claimed under a new identity, so the duplicates were deduplicated against an id that no longer decides anything", opID, heldOpID)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("final upstream forwards = %d, want EXACTLY 1", got)
	}
	// Header correlation: the forwarded request carried the claimed OperationID.
	if hdr, _ := forwardedOpID.Load().(string); hdr == "" || hdr != opID {
		t.Fatalf("Olivares-Operation-Id header = %q, want the claimed operation id %q", hdr, opID)
	}
	// The journal row settled completed.
	row, found := mcpJournalRow(t, f, opID)
	if !found || row.State != model.EvidenceOpCompleted {
		t.Fatalf("journal row %s = %+v found=%t, want settled completed", opID, row, found)
	}
	verifyMCPLedger(t, f)
}

// --- Round-2 NEW-1 (P1): strict JSON-RPC response validation matrix -----------
//
// RED-first: pre-fix the forwarder unmarshaled into a struct (case-INSENSITIVE
// member matching, last-duplicate-wins) and truncated at 8MiB without overflow
// detection, so every case below was laundered as completed.
func TestMCPUpstreamForwarderStrictResponseValidation(t *testing.T) {
	newForwarder := func(url string) *mcpUpstreamForwarder {
		return &mcpUpstreamForwarder{url: url, client: &http.Client{Timeout: 5 * time.Second}}
	}
	call := func(f *mcpUpstreamForwarder) (mcpc.UpstreamResult, error) {
		return f.Forward(context.Background(), mcpc.UpstreamRequest{Method: "tools/call", Params: []byte(`{"name":"search"}`)})
	}
	serve := func(t *testing.T, body string) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
	}

	for _, tc := range []struct{ name, body string }{
		{"alternate member casing", `{"JSONRPC":"2.0","ID":1,"RESULT":{}}`},
		{"empty error object", `{"jsonrpc":"2.0","id":1,"error":{}}`},
		{"error code wrong type", `{"jsonrpc":"2.0","id":1,"error":{"code":"x","message":"m"}}`},
		{"error code non-integer", `{"jsonrpc":"2.0","id":1,"error":{"code":1.5,"message":"m"}}`},
		{"error message wrong type", `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":5}}`},
		{"duplicate result member", `{"jsonrpc":"2.0","id":1,"result":{"a":1},"result":{"a":2}}`},
		{"duplicate id member", `{"jsonrpc":"2.0","id":7,"id":1,"result":{}}`},
		{"trailing data", `{"jsonrpc":"2.0","id":1,"result":{}} {"x":1}`},
		{"string id", `{"jsonrpc":"2.0","id":"1","result":{}}`},
	} {
		t.Run(tc.name+" is unknown", func(t *testing.T) {
			srv := serve(t, tc.body)
			defer srv.Close()
			res, err := call(newForwarder(srv.URL))
			if err == nil || res.State != mcpc.DispatchUnknown {
				t.Fatalf("%s = state %q err=%v, want unknown + error (strict validation)", tc.name, res.State, err)
			}
		})
	}

	t.Run("valid prefix with overflow data is unknown", func(t *testing.T) {
		// A perfectly valid response followed by padding that pushes the body over
		// the read limit: truncation must be DETECTED, never validated as a prefix.
		srv := serve(t, `{"jsonrpc":"2.0","id":1,"result":{}}`+strings.Repeat(" ", 8<<20))
		defer srv.Close()
		res, err := call(newForwarder(srv.URL))
		if err == nil || res.State != mcpc.DispatchUnknown {
			t.Fatalf("overflow-prefix = state %q err=%v, want unknown (cannot validate a truncated body)", res.State, err)
		}
	})
}

// --- Round-2 finding 3 (P1): the upstream target descriptor binds the digest --
//
// RED-first: pre-fix the generation slot was literally empty, so two gateways
// pointed at DIFFERENT upstream backends derived the SAME EffectDigest — a keyed
// retry against a re-pointed backend replayed (-31012) instead of refusing the
// rebind (-31011).
func TestMCPUpstreamDescriptorBindsEffectDigest(t *testing.T) {
	f := newMCPLedgerFixture(t)
	eng := &engine{store: f.store, log: discardLogger()}
	var callsA, callsB int32
	upA := countingUpstreamServer(t, &callsA)
	defer upA.Close()
	upB := countingUpstreamServer(t, &callsB)
	defer upB.Close()

	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	rsA := buildReviewMCP(t, eng, jwks, upA.URL, f.tenant.String())
	rsB := buildReviewMCP(t, eng, jwks, upB.URL, f.tenant.String())

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"q":"x"},"_meta":{"ai.olivares/operationId":"backend-key-1"}}}`
	post := func(rs *mcpc.ResourceServer) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, mcpReviewResource, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, req)
		return w
	}
	if w := post(rsA); w.Code != http.StatusOK {
		t.Fatalf("first call (backend A) status = %d; body=%s", w.Code, w.Body.String())
	}
	// Same operation key, same params — but a DIFFERENT upstream backend: the
	// effect identity changed, so the single-use claim must refuse the REBIND.
	w2 := post(rsB)
	if w2.Code != http.StatusConflict {
		t.Fatalf("re-pointed backend status = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// -31011, not the retired -32011: stage 6 moved the PEP's own codes OUT of the
	// JSON-RPC reserved range (basic/index.mdx:153-155 instructs exactly that for
	// codes the spec does not define). The trailing digits are preserved so log
	// greps and runbooks still map.
	if resp.Error.Code != -31011 {
		t.Errorf("re-pointed backend code = %d, want -31011 (rebind: the upstream descriptor is part of the effect identity)", resp.Error.Code)
	}
	if got := atomic.LoadInt32(&callsB); got != 0 {
		t.Errorf("backend B forwards = %d, want 0", got)
	}
}
