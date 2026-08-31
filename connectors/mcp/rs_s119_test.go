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
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

// mintTyped signs an access token with a configurable JOSE typ header (empty => no typ
// set), so the RFC 9068 strict-at+jwt path can be tested both ways.
func mintTyped(t *testing.T, kid, aud, scope, typ string, exp time.Time) (string, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	opts := (&jose.SignerOptions{}).WithHeader("kid", kid)
	if typ != "" {
		opts = opts.WithType(jose.ContentType(typ))
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, opts)
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
	blob, _ := json.Marshal(ks)
	return raw, blob
}

// TestRSMetadataComplete: the PRM document carries the complete RFC 9728 field set the
// RS actually honors — bearer_methods_supported=["header"], resource_name,
// resource_documentation — and advertises NO proof-of-possession flag (honest).
func TestRSMetadataComplete(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	ts, _ := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer}, Issuer: rsIssuer,
		IssuerJWKS: jwks, Toolset: ts, Clock: rsClock,
		ResourceName: "Olivares MCP Gateway", ResourceDocumentation: "https://docs.olivares.example/mcp",
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, httptest.NewRequest(http.MethodGet, wellKnownProtectedResource, nil))
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	methods, _ := doc["bearer_methods_supported"].([]any)
	if len(methods) != 1 || methods[0] != "header" {
		t.Errorf("bearer_methods_supported must be [\"header\"], got %v", doc["bearer_methods_supported"])
	}
	if doc["resource_name"] != "Olivares MCP Gateway" {
		t.Errorf("resource_name = %v", doc["resource_name"])
	}
	if doc["resource_documentation"] != "https://docs.olivares.example/mcp" {
		t.Errorf("resource_documentation = %v", doc["resource_documentation"])
	}
	if _, ok := doc["dpop_bound_access_tokens_required"]; ok {
		t.Error("must not advertise a PoP flag the validator does not enforce (honesty)")
	}
}

// TestRSScopesDerivedFromToolset: when the operator does not list scopes_supported, the
// PRM advertises exactly the scopes the toolset ENFORCES (advertised==enforced).
func TestRSScopesDerivedFromToolset(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	ts, _ := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "delete_db", RequiredScope: "tools:admin", Destructive: true},
		{Name: "noscope"}, // no required scope -> not advertised
		{Name: "killed", RequiredScope: "tools:secret", Deny: true}, // denied -> not advertised
	})
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer}, Issuer: rsIssuer,
		IssuerJWKS: jwks, Toolset: ts, Clock: rsClock, // ScopesSupported intentionally empty
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, httptest.NewRequest(http.MethodGet, wellKnownProtectedResource, nil))
	var doc struct {
		Scopes []string `json:"scopes_supported"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &doc)
	got := strings.Join(doc.Scopes, ",")
	// Review ROUND-5 R5-03 (reachability): the invariant under test is
	// advertised == ENFORCED, and this RS also enforces the operator reconciliation
	// scope on every tasks/reconcile/* request. Round-4 omitted it from the union,
	// so the recovery surface was undiscoverable even for an operator entitled to
	// it. The expectation is CORRECTED to the full enforced set, not relaxed:
	// advertising a scope grants nothing (the AS still has to mint it), and the
	// non-advertised cases below (`noscope`, the denied tool) are untouched.
	if got != "tasks:reconcile,tools:admin,tools:read" { // sorted union of enforced scopes
		t.Errorf("scopes_supported = %q, want the enforced toolset + reconciliation scopes (sorted)", got)
	}
}

// TestRSStrictATJWT: with RequireATJWT, a token without typ=at+jwt is rejected (401);
// a proper at+jwt token is accepted. With the default (off), a no-typ token is accepted.
func TestRSStrictATJWT(t *testing.T) {
	ts, _ := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
	build := func(strict bool, jwks []byte) *ResourceServer {
		rs, err := NewResourceServer(ResourceServerConfig{
			Resource: rsResource, AuthorizationServers: []string{rsIssuer}, Issuer: rsIssuer,
			IssuerJWKS: jwks, Toolset: ts, Upstream: &fakeUpstream{}, Gate: fakeToolGate{StatusApproved},
			DurableTaskStore: newMemoryDurableTaskStore(),
			Auditor:          &fakeEvidenceJournal{}, // granting journal (enforcement pinned by evidence_test.go)
			Clock:            rsClock, RequireATJWT: strict,
			// Test sends 2025-11-25 style requests; focus is on typ validation.
			DisableNextRevisionHeaders: true,
		})
		if err != nil {
			t.Fatalf("new rs: %v", err)
		}
		return rs
	}

	// Strict ON: a JWT with typ "JWT" (not at+jwt) is rejected.
	wrongTok, wrongJWKS := mintTyped(t, "k1", rsResource, "tools:read", "JWT", validExp())
	w := httptest.NewRecorder()
	build(true, wrongJWKS).ServeHTTP(w, toolsCallReq(wrongTok, "search", "{}"))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("strict at+jwt: a typ=JWT token must be 401, got %d", w.Code)
	}

	// Strict ON: a proper at+jwt token is accepted.
	okTok, okJWKS := mintTyped(t, "k1", rsResource, "tools:read", "at+jwt", validExp())
	w2 := httptest.NewRecorder()
	build(true, okJWKS).ServeHTTP(w2, toolsCallReq(okTok, "search", "{}"))
	if w2.Code != http.StatusOK {
		t.Errorf("strict at+jwt: a proper at+jwt token must be accepted, got %d (%s)", w2.Code, w2.Body.String())
	}

	// Strict OFF (default): a token with no typ is still accepted (back-compat).
	noTypeTok, noTypeJWKS := mintTyped(t, "k1", rsResource, "tools:read", "", validExp())
	w3 := httptest.NewRecorder()
	build(false, noTypeJWKS).ServeHTTP(w3, toolsCallReq(noTypeTok, "search", "{}"))
	if w3.Code != http.StatusOK {
		t.Errorf("non-strict: a no-typ token must be accepted, got %d (%s)", w3.Code, w3.Body.String())
	}
}
