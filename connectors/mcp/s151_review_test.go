// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

// s151_review_test.go pins the fixes that came out of the adversarial review:
// every test here encodes a CONFIRMED finding so the regression cannot return.

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
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

// TestRFC9068ExpClaimRequired: go-jose only enforces expiry when the claim is
// PRESENT, so without an explicit presence check a trusted-issuer token with no exp
// would be a never-expiring bearer. RFC 9068 §4 makes the expiry check mandatory —
// which requires the claim (review finding, medium).
func TestRFC9068ExpClaimRequired(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", "k-noexp"))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	std := jwt.Claims{
		Issuer:   rsIssuer,
		Subject:  "agent:claude",
		Audience: jwt.Audience{rsResource},
		IssuedAt: jwt.NewNumericDate(rsClock().Add(-time.Minute)),
		// NO Expiry — the never-expiring bearer.
	}
	raw, err := jwt.Signed(signer).Claims(std).Claims(map[string]any{"scope": "tools:read"}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: "k-noexp", Algorithm: "ES256", Use: "sig"}}}
	jwks, _ := json.Marshal(ks)

	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, &fakeUpstream{})
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(raw, "search", `{}`))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an exp-less access token must be 401 invalid_token, got %d", w.Code)
	}
}

// TestEmptyJWKSAnchorRejectedAtConstruction: an inline JWKS that parses but holds no
// keys (`{}`, `{"keys":[]}`, `null`) must NOT satisfy the trust-anchor presence
// check — it would construct a permanently-dead issuer that the fail-closed
// constructor exists to refuse (review finding, low).
func TestEmptyJWKSAnchorRejectedAtConstruction(t *testing.T) {
	ts, _ := NewToolset(nil)
	for _, raw := range []string{`{}`, `{"keys":[]}`, `null`} {
		_, err := NewResourceServer(ResourceServerConfig{
			Resource: rsResource, AuthorizationServers: []string{rsIssuer}, Toolset: ts,
			Issuers: []IssuerTrust{{Issuer: rsIssuer, JWKS: []byte(raw)}},
		})
		if err == nil {
			t.Errorf("issuer_jwks %q: a zero-key set must not construct", raw)
		}
	}
}

// TestLegacyNullJWKSDoesNotActivateFold: a JSON `null` in the legacy issuer_jwks
// RawMessage is not a configured anchor — it must not activate the legacy fold and
// demand a legacy issuer when only issuers[] is intended (review finding, low).
func TestLegacyNullJWKSDoesNotActivateFold(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	ts, _ := NewToolset(nil)
	_, err := NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer}, Toolset: ts,
		IssuerJWKS: []byte("null"), // a json.RawMessage artifact, not a config
		Issuers:    []IssuerTrust{{Issuer: rsIssuer, JWKS: jwks}},
	})
	if err != nil {
		t.Fatalf("issuers[] config with a stray legacy issuer_jwks null must construct: %v", err)
	}
}

// TestRFC9728PRMResourceMismatchRejected: the PRM's resource value MUST identify
// the protected resource the client is addressing (RFC 9728 §3.3) — a PRM answering
// for a DIFFERENT resource must not steer this client's authorization (review
// finding, low).
func TestRFC9728PRMResourceMismatchRejected(t *testing.T) {
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource" {
			writeJSON(w, fmt.Sprintf(`{"resource":"https://other.example/mcp","authorization_servers":[%q]}`, base))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	base = srv.URL
	defer srv.Close()

	c, err := newOAuthClient(base+"/mcp", &serverAuth{ClientID: "cid", ClientSecret: "csec"}, nil)
	if err != nil {
		t.Fatalf("newOAuthClient: %v", err)
	}
	_, err = c.discoverAS(context.Background(), `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource"`)
	if err == nil || !strings.Contains(err.Error(), "RFC 9728") {
		t.Fatalf("a PRM declaring a foreign resource must be rejected with the RFC 9728 §3.3 message, got: %v", err)
	}
}

// TestSEP2350StepUpAvailablePerRequest: the step-up must be once PER REQUEST, not
// once per transport lifetime — a later request on the same transport challenged
// for a DIFFERENT scope gets its own accumulated step-up (review finding, medium:
// the per-transport flag made SEP-2350 accumulation dead after the first challenge).
func TestSEP2350StepUpAvailablePerRequest(t *testing.T) {
	var (
		mu          sync.Mutex
		tokenScopes []string
		granted     = map[string]string{}
		adminPhase  bool // after the first successful call, "admin" is also required
	)
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource":
			writeJSON(w, fmt.Sprintf(`{"resource":%q,"authorization_servers":[%q]}`, base+"/mcp", base))
		case "/.well-known/oauth-authorization-server":
			writeJSON(w, fmt.Sprintf(`{"issuer":%q,"token_endpoint":%q}`, base, base+"/token"))
		case "/token":
			_ = r.ParseForm()
			mu.Lock()
			scope := r.PostForm.Get("scope")
			tok := fmt.Sprintf("tok-%d", len(tokenScopes)+1)
			tokenScopes = append(tokenScopes, scope)
			granted[tok] = scope
			mu.Unlock()
			writeJSON(w, fmt.Sprintf(`{"access_token":%q,"token_type":"Bearer"}`, tok))
		case "/mcp":
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			mu.Lock()
			scope, authed := granted[tok]
			admin := adminPhase
			mu.Unlock()
			if !authed {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			held := strings.Fields(scope)
			challenge := func(missing string) {
				w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+missing+`", resource_metadata="`+base+`/.well-known/oauth-protected-resource"`)
				w.WriteHeader(http.StatusForbidden)
			}
			switch {
			case !containsScope(held, "write"):
				challenge("write")
				return
			case admin && !containsScope(held, "admin"):
				challenge("admin")
				return
			}
			mu.Lock()
			adminPhase = true
			mu.Unlock()
			mcpHTTPHandler(false)(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	base = srv.URL
	defer srv.Close()

	tr, err := newHTTPTransport(serverSpec{
		Name: "s", Transport: transportHTTP, URL: base + "/mcp",
		Auth: &serverAuth{ClientID: "cid", ClientSecret: "csec", Scopes: []string{"read"}},
	})
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}
	if _, err := tr.roundTrip(context.Background(), rpcRequest{Method: "tools/list"}); err != nil {
		t.Fatalf("first request (step-up to write) must succeed: %v", err)
	}
	if _, err := tr.roundTrip(context.Background(), rpcRequest{Method: "tools/list"}); err != nil {
		t.Fatalf("second request (step-up to admin on the SAME transport) must succeed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"read", "read write", "read write admin"}
	if len(tokenScopes) != len(want) {
		t.Fatalf("token requests = %v, want %v", tokenScopes, want)
	}
	for i := range want {
		if tokenScopes[i] != want[i] {
			t.Errorf("token request #%d scope = %q, want %q (accumulation must survive across requests)", i+1, tokenScopes[i], want[i])
		}
	}
}
