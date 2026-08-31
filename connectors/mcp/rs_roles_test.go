// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
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

// mintRoledToken signs an at+jwt with a scope AND a roles claim of an arbitrary shape
// (array, space/comma string) under the given claim name, returning the token + JWKS.
func mintRoledToken(t *testing.T, kid, aud, scope, roleClaim string, roles any, exp time.Time) (string, []byte) {
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
	extra := map[string]any{"scope": scope}
	if roles != nil {
		extra[roleClaim] = roles
	}
	raw, err := jwt.Signed(signer).Claims(std).Claims(extra).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: kid, Algorithm: "ES256", Use: "sig"}}}
	blob, _ := json.Marshal(ks)
	return raw, blob
}

// newRoleRS builds an RS whose "deploy" tool is restricted to role "sre" and "search" is
// open (no role gate). roleClaim selects the token claim roles are read from.
func newRoleRS(t *testing.T, jwks []byte, roleClaim string, up Upstream) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "deploy", RequiredScope: "tools:read", AllowedRoles: []string{"sre", "platform"}},
	})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:             rsResource,
		AuthorizationServers: []string{rsIssuer},
		Issuer:               rsIssuer,
		IssuerJWKS:           jwks,
		Toolset:              ts,
		AllowedOrigins:       []string{rsOrigin},
		Upstream:             up,
		DurableTaskStore:     newMemoryDurableTaskStore(),
		Auditor:              &fakeEvidenceJournal{}, // granting journal (enforcement pinned by evidence_test.go)
		Clock:                rsClock,
		RoleClaim:            roleClaim,
		// Tests in this file send 2025-11-25 style requests; opt out of the
		// 2026-07-28 header gate so they can focus on role-based access.
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

// TestRSRoleAllowsMatchingRole: a caller holding the required role may call the
// role-restricted tool.
func TestRSRoleAllowsMatchingRole(t *testing.T) {
	tok, jwks := mintRoledToken(t, "k1", rsResource, "tools:read", "roles", []string{"sre"}, validExp())
	up := &fakeUpstream{}
	rs := newRoleRS(t, jwks, "roles", up)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(tok, "deploy", "{}"))
	if w.Code != http.StatusOK {
		t.Fatalf("matching role should be allowed, got %d: %s", w.Code, w.Body.String())
	}
	if !up.called {
		t.Error("upstream should have been called for an authorized role")
	}
}

// TestRSRoleDeniesMissingRole: a caller WITHOUT the required role is denied 403 and the
// upstream is never reached (deny-closed).
func TestRSRoleDeniesMissingRole(t *testing.T) {
	tok, jwks := mintRoledToken(t, "k1", rsResource, "tools:read", "roles", []string{"reader"}, validExp())
	up := &fakeUpstream{}
	rs := newRoleRS(t, jwks, "roles", up)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(tok, "deploy", "{}"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing role should be 403, got %d", w.Code)
	}
	if up.called {
		t.Error("upstream must NOT be called when the role is denied (deny-closed)")
	}
	// A 403 with NO scope challenge — a role is not a scope the client can step up to.
	if strings.Contains(w.Header().Get("WWW-Authenticate"), "insufficient_scope") {
		t.Error("role denial must not be a scope step-up challenge")
	}
}

// TestRSRoleNoRolesClaimDeniesRestricted: a token with NO roles claim is denied any
// role-restricted tool but may still call an unrestricted one.
func TestRSRoleNoRolesClaimDeniesRestricted(t *testing.T) {
	tok, jwks := mintRoledToken(t, "k1", rsResource, "tools:read", "roles", nil, validExp())
	up := &fakeUpstream{}
	rs := newRoleRS(t, jwks, "roles", up)
	// Restricted tool → denied.
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(tok, "deploy", "{}"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("no-roles token must be denied the restricted tool, got %d", w.Code)
	}
	// Unrestricted tool → allowed.
	up2 := &fakeUpstream{}
	rs2 := newRoleRS(t, jwks, "roles", up2)
	w2 := httptest.NewRecorder()
	rs2.ServeHTTP(w2, toolsCallReq(tok, "search", "{}"))
	if w2.Code != http.StatusOK {
		t.Fatalf("unrestricted tool must still be callable, got %d", w2.Code)
	}
}

// TestRSRoleConfigurableClaim: roles can be read from a non-default claim name (e.g.
// "https://acme/roles") and a space-delimited string shape.
func TestRSRoleConfigurableClaim(t *testing.T) {
	claim := "https://acme.example/roles"
	tok, jwks := mintRoledToken(t, "k1", rsResource, "tools:read", claim, "platform on-call", validExp())
	up := &fakeUpstream{}
	rs := newRoleRS(t, jwks, claim, up)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(tok, "deploy", "{}"))
	if w.Code != http.StatusOK {
		t.Fatalf("configurable role claim (space-string) should admit 'platform', got %d", w.Code)
	}
}

// TestRSToolsListRoleFiltered: tools/list hides a role-restricted tool from a caller
// whose role does not satisfy it, while keeping the unrestricted tool.
func TestRSToolsListRoleFiltered(t *testing.T) {
	upstreamList := json.RawMessage(`{"tools":[{"name":"search"},{"name":"deploy"},{"name":"ghost"}]}`)
	tok, jwks := mintRoledToken(t, "k1", rsResource, "tools:read", "roles", []string{"reader"}, validExp())
	up := &fakeUpstream{result: upstreamList}
	rs := newRoleRS(t, jwks, "roles", up)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d", w.Code)
	}
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range resp.Result.Tools {
		names[tl.Name] = true
	}
	if !names["search"] {
		t.Error("unrestricted 'search' should be listed")
	}
	if names["deploy"] {
		t.Error("role-restricted 'deploy' must be hidden from a caller lacking the role")
	}
	if names["ghost"] {
		t.Error("'ghost' is not in the server toolset and must be filtered (deny-by-default)")
	}
}

// spyGate records whether Authorize was ever consulted.
type spyGate struct{ called bool }

func (g *spyGate) Authorize(_ context.Context, req ToolApprovalRequest) (GateDecision, error) {
	g.called = true
	return GateDecision{ApprovalRef: "appr", Status: StatusApproved, PlanHash: req.PlanHash}, nil
}

// TestRSRoleDeniedBeforeHITL: a forbidden role on a DESTRUCTIVE tool is rejected by the
// role gate BEFORE the HITL approval gate is consulted — so a denied caller never causes
// an approval request to be raised (the role check short-circuits the destructive path).
func TestRSRoleDeniedBeforeHITL(t *testing.T) {
	ts, err := NewToolset([]ToolPolicy{
		{Name: "wipe", RequiredScope: "tools:read", Destructive: true, AllowedRoles: []string{"sre"}},
	})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	gate := &spyGate{}
	up := &fakeUpstream{}
	tok, jwks := mintRoledToken(t, "k1", rsResource, "tools:read", "roles", []string{"reader"}, validExp())
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer}, Issuer: rsIssuer,
		IssuerJWKS: jwks, Toolset: ts, AllowedOrigins: []string{rsOrigin},
		Gate: gate, Upstream: up, Clock: rsClock,
		DurableTaskStore:           newMemoryDurableTaskStore(),
		Auditor:                    &fakeEvidenceJournal{}, // granting journal (enforcement pinned by evidence_test.go)
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(tok, "wipe", "{}"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("forbidden role on destructive tool should be 403, got %d", w.Code)
	}
	if gate.called {
		t.Error("the HITL approval gate must NOT be consulted when the role is denied")
	}
	if up.called {
		t.Error("upstream must not be reached for a role-denied destructive call")
	}
}

// TestRolesFromClaim covers the three accepted role-claim shapes + empty.
func TestRolesFromClaim(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{`["a","b"]`, []string{"a", "b"}},
		{`"a b c"`, []string{"a", "b", "c"}},
		{`"a, b ,c"`, []string{"a", "b", "c"}},
		{`"solo"`, []string{"solo"}},
		{``, nil},
		{`null`, nil},
	}
	for _, tc := range cases {
		got := rolesFromClaim(json.RawMessage(tc.raw))
		if len(got) != len(tc.want) {
			t.Errorf("rolesFromClaim(%s) size = %d, want %d (%v)", tc.raw, len(got), len(tc.want), got)
			continue
		}
		for _, w := range tc.want {
			if _, ok := got[w]; !ok {
				t.Errorf("rolesFromClaim(%s) missing %q", tc.raw, w)
			}
		}
	}
}

// TestRoleAllowedUnit covers the per-tool role gate directly.
func TestRoleAllowedUnit(t *testing.T) {
	roles := func(rs ...string) map[string]struct{} {
		m := map[string]struct{}{}
		for _, r := range rs {
			m[r] = struct{}{}
		}
		return m
	}
	if !roleAllowed(ToolPolicy{Name: "x"}, roles()) {
		t.Error("no AllowedRoles ⇒ no role restriction (must allow)")
	}
	if roleAllowed(ToolPolicy{Name: "x", AllowedRoles: []string{"sre"}}, roles()) {
		t.Error("role-restricted tool must deny a caller with no roles")
	}
	if roleAllowed(ToolPolicy{Name: "x", AllowedRoles: []string{"sre"}}, roles("reader")) {
		t.Error("role-restricted tool must deny a non-matching role")
	}
	if !roleAllowed(ToolPolicy{Name: "x", AllowedRoles: []string{"sre", "platform"}}, roles("platform")) {
		t.Error("a caller holding ONE allowed role must be admitted")
	}
}
