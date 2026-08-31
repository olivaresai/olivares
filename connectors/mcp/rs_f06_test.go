// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// rsPost builds an authenticated JSON-RPC POST for the RS PEP.
func rsPost(token, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// TestF06NonToolMethodRequiresScope is the F-06 red repro (piece 1): the gateway forwarded
// EVERY non-tool method with only a bearer. A token scoped for tools must not be able to
// read arbitrary resources; resources/read now requires resources:read (default-deny
// matrix), and an unknown method is refused, never forwarded.
func TestF06NonToolMethodRequiresScope(t *testing.T) {
	readBody := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"file:///secret"}}`

	// (a) tools:read alone must NOT authorize resources/read → 403, upstream untouched.
	tok, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(`{"contents":[{"text":"sensitive"}]}`)}
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, up)
	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, rsPost(tok, readBody))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("F-06: resources/read with only tools:read must be 403, got %d", rec.Code)
	}
	if up.called {
		t.Error("F-06: a scope-denied resources/read must not reach the upstream")
	}

	// (b) with resources:read the read is admitted and forwarded.
	tok2, jwks2 := mintAccessToken(t, "k1", rsResource, "tools:read resources:read", validExp())
	up2 := &fakeUpstream{result: json.RawMessage(`{"contents":[]}`)}
	rs2 := newRS(t, jwks2, fakeToolGate{StatusApproved}, up2)
	rec2 := httptest.NewRecorder()
	rs2.ServeHTTP(rec2, rsPost(tok2, readBody))
	if rec2.Code != http.StatusOK || !up2.called {
		t.Fatalf("resources:read scope must admit the read: %d (upstream called=%v)", rec2.Code, up2.called)
	}

	// (c) an unknown method is default-denied and never forwarded.
	up3 := &fakeUpstream{result: json.RawMessage(`{}`)}
	rs3 := newRS(t, jwks2, fakeToolGate{StatusApproved}, up3)
	rec3 := httptest.NewRecorder()
	rs3.ServeHTTP(rec3, rsPost(tok2, `{"jsonrpc":"2.0","id":2,"method":"debug/execute","params":{}}`))
	if rec3.Code == http.StatusOK || up3.called {
		t.Fatalf("F-06: an unknown method must be default-denied, got %d (upstream called=%v)", rec3.Code, up3.called)
	}
}

// TestF06ListingRequiresFamilyScope is the H1 red repro: the resources/prompts LISTINGS
// (resources/list, resources/templates/list, prompts/list) were admitted with ANY valid
// token and forwarded UNFILTERED, so a low-scope token (even without resources:read) could
// enumerate every resource URI (file:///secret/…) and prompt name. The gateway now gates
// the listings by the READ family scope (deny-by-default) — stricter than tools/list, which
// lists-then-filters. A token lacking the family scope → 403 with a step-up challenge and
// the upstream is never touched (no enumeration); with the scope the listing is forwarded.
func TestF06ListingRequiresFamilyScope(t *testing.T) {
	cases := []struct {
		method      string
		familyScope string
	}{
		{"resources/list", scopeResourcesRead},
		{"resources/templates/list", scopeResourcesRead},
		{"prompts/list", scopePromptsRead},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"` + tc.method + `","params":{}}`

			// (a) a token with only tools:read must NOT enumerate: 403, upstream untouched.
			tok, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
			up := &fakeUpstream{result: json.RawMessage(`{"resources":[{"uri":"file:///secret/db.env"}],"resourceTemplates":[{"uriTemplate":"file:///secret/{x}"}],"prompts":[{"name":"exfiltrate"}]}`)}
			rs := newRS(t, jwks, fakeToolGate{StatusApproved}, up)
			rec := httptest.NewRecorder()
			rs.ServeHTTP(rec, rsPost(tok, body))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s with only tools:read must be 403 (deny-by-default), got %d", tc.method, rec.Code)
			}
			if up.called {
				t.Errorf("%s: a scope-denied listing must never reach the upstream (no URI/name enumeration)", tc.method)
			}
			if wa := rec.Header().Get("WWW-Authenticate"); !strings.Contains(wa, tc.familyScope) {
				t.Errorf("%s: the 403 must carry the %s step-up challenge, got %q", tc.method, tc.familyScope, wa)
			}

			// (b) with the read-family scope the listing is admitted and forwarded.
			tok2, jwks2 := mintAccessToken(t, "k1", rsResource, "tools:read "+tc.familyScope, validExp())
			up2 := &fakeUpstream{result: json.RawMessage(`{"resources":[],"resourceTemplates":[],"prompts":[]}`)}
			rs2 := newRS(t, jwks2, fakeToolGate{StatusApproved}, up2)
			rec2 := httptest.NewRecorder()
			rs2.ServeHTTP(rec2, rsPost(tok2, body))
			if rec2.Code != http.StatusOK || !up2.called {
				t.Fatalf("%s with %s must be admitted+forwarded: %d (upstream called=%v)", tc.method, tc.familyScope, rec2.Code, up2.called)
			}
		})
	}
}

// TestF06UnknownNotificationDefaultDenied is the H2 repro: the gateway matched
// `notifications/*` by PREFIX and forwarded ANY such method verbatim upstream. It now
// admits only the known MCP notification set; an unknown notifications/* is default-denied
// and never forwarded (no verbatim relay of an attacker-chosen notification).
func TestF06UnknownNotificationDefaultDenied(t *testing.T) {
	// (a) a KNOWN notification (no id) is admitted and relayed → 202 Accepted, forwarded.
	tok, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{}
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, up)
	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, rsPost(tok, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"p","progress":1}}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a known notification must be relayed (202 Accepted), got %d", rec.Code)
	}
	if !up.called {
		t.Error("a known notification must reach the upstream")
	}

	// (b) an UNKNOWN notifications/* is default-denied and NOT forwarded verbatim.
	up2 := &fakeUpstream{}
	rs2 := newRS(t, jwks, fakeToolGate{StatusApproved}, up2)
	rec2 := httptest.NewRecorder()
	rs2.ServeHTTP(rec2, rsPost(tok, `{"jsonrpc":"2.0","method":"notifications/rogue/exfiltrate","params":{"x":1}}`))
	if up2.called {
		t.Fatal("H2: an unknown notifications/* must NOT be forwarded verbatim to the upstream")
	}
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("H2: an unknown notification must be default-denied (403), got %d", rec2.Code)
	}
}

// mintSubjectlessToken signs an otherwise-valid at+jwt with an EMPTY sub claim.
func mintSubjectlessToken(t *testing.T) (token string, jwks []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", "sub0"))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	std := jwt.Claims{
		Issuer:   rsIssuer,
		Subject:  "", // the vulnerability: no subject
		Audience: jwt.Audience{rsResource},
		IssuedAt: jwt.NewNumericDate(rsClock().Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(rsClock().Add(5 * time.Minute)),
	}
	raw, err := jwt.Signed(signer).Claims(std).Claims(map[string]any{"scope": "tools:read"}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: "sub0", Algorithm: "ES256", Use: "sig"}}}
	blob, err := json.Marshal(ks)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return raw, blob
}

// TestF06EmptySubjectTokenRejected is the F-06 red repro (piece 2): a token with an empty
// sub is unattributable — it must be rejected (mirror idjag's `sub is REQUIRED`), not
// admitted as an anonymous principal that every downstream PEP decision keys off.
func TestF06EmptySubjectTokenRejected(t *testing.T) {
	tok, jwks := mintSubjectlessToken(t)
	up := &fakeUpstream{}
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, up)
	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, rsPost(tok, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("F-06: a subject-less token must be rejected (401), got %d", rec.Code)
	}
	if up.called {
		t.Error("F-06: a subject-less token must never reach the upstream")
	}
}
