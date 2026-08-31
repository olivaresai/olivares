// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newS335HTTPRS(t *testing.T, jwks []byte, up Upstream, aud GateAuditor, mutate func(*ResourceServerConfig)) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	cfg := ResourceServerConfig{
		Resource:                   rsResource,
		AuthorizationServers:       []string{rsIssuer},
		ScopesSupported:            []string{"tools:read"},
		Issuer:                     rsIssuer,
		IssuerJWKS:                 jwks,
		Toolset:                    ts,
		Gate:                       fakeToolGate{StatusApproved},
		Upstream:                   up,
		DurableTaskStore:           newMemoryDurableTaskStore(),
		Auditor:                    aud,
		Clock:                      rsClock,
		DisableNextRevisionHeaders: true,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	rs, err := NewResourceServer(cfg)
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

func TestS335BoundTokenAsPlainBearerRejected(t *testing.T) {
	as := newS335AccessSigner(t)
	pk := newS335ProofKey(t)
	token := as.mint(t, map[string]any{"jkt": pk.jkt})
	up := &fakeUpstream{}
	rs := newS335HTTPRS(t, as.jwks, up, nil, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", "{}"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bound bearer without proof status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("challenge = %q, want invalid_token", w.Header().Get("WWW-Authenticate"))
	}
	if up.called {
		t.Fatal("bound token without proof must not reach upstream")
	}
}

func TestS335UnboundBearerDefaultAuditsBearer(t *testing.T) {
	as := newS335AccessSigner(t)
	token := as.mint(t, nil)
	up := &fakeUpstream{}
	aud := &capturingAuditor{}
	rs := newS335HTTPRS(t, as.jwks, up, aud, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", "{}"))
	if w.Code != http.StatusOK {
		t.Fatalf("unbound bearer status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !up.called {
		t.Fatal("authorized unbound bearer must reach upstream")
	}
	if len(aud.decisions) != 1 {
		t.Fatalf("audited decisions = %d, want 1", len(aud.decisions))
	}
	if aud.decisions[0].TokenBinding != tokenBindingBearer {
		t.Fatalf("audit token binding = %q, want bearer", aud.decisions[0].TokenBinding)
	}
}

func TestS335RequireDPoP(t *testing.T) {
	as := newS335AccessSigner(t)
	pk := newS335ProofKey(t)
	unbound := as.mint(t, nil)
	bound := as.mint(t, map[string]any{"jkt": pk.jkt})
	up := &fakeUpstream{}
	aud := &capturingAuditor{}
	rs := newS335HTTPRS(t, as.jwks, up, aud, func(cfg *ResourceServerConfig) {
		cfg.RequireDPoP = true
	})

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(unbound, "search", "{}"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("require_dpop unbound status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Header().Get("WWW-Authenticate"), "DPoP ") ||
		!strings.Contains(w.Header().Get("WWW-Authenticate"), `algs="`) {
		t.Fatalf("require_dpop challenge = %q, want DPoP algs", w.Header().Get("WWW-Authenticate"))
	}

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "DPoP "+bound)
	req.Header.Set("DPoP", mintS335DPoPProof(t, pk, dpopProofSpec{
		accessToken: bound,
		htu:         dpopHTU(rs.resource, req.URL.Path),
		jti:         "require-dpop",
	}))
	w2 := httptest.NewRecorder()
	rs.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("bound dpop status = %d, want 200; body=%s", w2.Code, w2.Body.String())
	}
	if len(aud.decisions) != 1 {
		t.Fatalf("audited decisions = %d, want 1", len(aud.decisions))
	}
	if aud.decisions[0].TokenBinding != tokenBindingDPoP {
		t.Fatalf("audit token binding = %q, want dpop", aud.decisions[0].TokenBinding)
	}
}

func TestS335MTLSBoundTokens(t *testing.T) {
	as := newS335AccessSigner(t)
	cert := s335Certificate(t, "client")
	thumb := s335CertThumbprint(cert)
	token := as.mint(t, map[string]any{"x5t#S256": thumb})

	aud := &capturingAuditor{}
	rs := newS335HTTPRS(t, as.jwks, &fakeUpstream{}, aud, func(cfg *ResourceServerConfig) {
		cfg.AcceptMTLSBoundTokens = true
	})
	req := toolsCallReq(token, "search", "{}")
	state := tlsConnectionState(cert)
	req.TLS = &state
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("matching mtls status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if len(aud.decisions) != 1 || aud.decisions[0].TokenBinding != tokenBindingMTLS {
		t.Fatalf("audit binding = %#v, want mtls", aud.decisions)
	}

	mismatch := toolsCallReq(token, "search", "{}")
	mismatchState := tlsConnectionState(s335Certificate(t, "other"))
	mismatch.TLS = &mismatchState
	w2 := httptest.NewRecorder()
	rs.ServeHTTP(w2, mismatch)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("mismatching mtls status = %d, want 401", w2.Code)
	}

	absent := toolsCallReq(token, "search", "{}")
	w3 := httptest.NewRecorder()
	rs.ServeHTTP(w3, absent)
	if w3.Code != http.StatusUnauthorized {
		t.Fatalf("absent tls status = %d, want 401", w3.Code)
	}

	flagOff := newS335HTTPRS(t, as.jwks, &fakeUpstream{}, nil, nil)
	reqOff := toolsCallReq(token, "search", "{}")
	offState := tlsConnectionState(cert)
	reqOff.TLS = &offState
	w4 := httptest.NewRecorder()
	flagOff.ServeHTTP(w4, reqOff)
	if w4.Code != http.StatusUnauthorized {
		t.Fatalf("flag off mtls status = %d, want 401", w4.Code)
	}
}

func TestS335ProtectedResourceMetadataBindingFields(t *testing.T) {
	as := newS335AccessSigner(t)
	cases := []struct {
		name     string
		mutate   func(*ResourceServerConfig)
		wantDPoP bool
		wantMTLS bool
	}{
		{name: "default"},
		{name: "require dpop", mutate: func(cfg *ResourceServerConfig) { cfg.RequireDPoP = true }, wantDPoP: true},
		{name: "accept mtls", mutate: func(cfg *ResourceServerConfig) { cfg.AcceptMTLSBoundTokens = true }, wantMTLS: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := newS335HTTPRS(t, as.jwks, nil, nil, c.mutate)
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, httptest.NewRequest(http.MethodGet, wellKnownProtectedResource, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("metadata status = %d, want 200", w.Code)
			}
			var doc map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
				t.Fatalf("decode metadata: %v", err)
			}
			algs, ok := doc["dpop_signing_alg_values_supported"].([]any)
			if !ok || len(algs) != len(mcpAllowedAlgs) {
				t.Fatalf("dpop alg metadata = %#v, want %d algs", doc["dpop_signing_alg_values_supported"], len(mcpAllowedAlgs))
			}
			if _, ok := doc["dpop_bound_access_tokens_required"]; ok != c.wantDPoP {
				t.Fatalf("dpop required flag present = %v, want %v", ok, c.wantDPoP)
			}
			if _, ok := doc["tls_client_certificate_bound_access_tokens"]; ok != c.wantMTLS {
				t.Fatalf("mtls flag present = %v, want %v", ok, c.wantMTLS)
			}
		})
	}
}

// TestS335UnverifiableCnfMethodRejected proves a token whose cnf carries a
// confirmation method this RS cannot check (e.g. RFC 7800 jwe/kid) is refused
// fail-closed — accepting it as a plain bearer would defeat the constraint.
func TestS335UnverifiableCnfMethodRejected(t *testing.T) {
	as := newS335AccessSigner(t)
	token := as.mint(t, map[string]any{"kid": "some-key-id"}) // neither jkt nor x5t#S256
	up := &fakeUpstream{}
	rs := newS335HTTPRS(t, as.jwks, up, nil, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", "{}"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unverifiable cnf method status = %d, want 401", w.Code)
	}
	if up.called {
		t.Fatal("a token with an unverifiable cnf confirmation method must not reach upstream")
	}
}

func s335Certificate(t *testing.T, cn string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen cert key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    rsClock().Add(-time.Hour),
		NotAfter:     rsClock().Add(time.Hour),
	}
	raw, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

func s335CertThumbprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func tlsConnectionState(cert *x509.Certificate) tls.ConnectionState {
	return tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
}
