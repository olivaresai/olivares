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

type s335AccessSigner struct {
	key  *ecdsa.PrivateKey
	jwks []byte
}

func newS335AccessSigner(t *testing.T) *s335AccessSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen access key: %v", err)
	}
	ks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: key.Public(), KeyID: "s335-at", Algorithm: "ES256", Use: "sig",
	}}}
	jwks, err := json.Marshal(ks)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return &s335AccessSigner{key: key, jwks: jwks}
}

func (s *s335AccessSigner) mint(t *testing.T, cnf map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: s.key},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", "s335-at"))
	if err != nil {
		t.Fatalf("access signer: %v", err)
	}
	std := jwt.Claims{
		Issuer:   rsIssuer,
		Subject:  "agent:claude",
		Audience: jwt.Audience{rsResource},
		IssuedAt: jwt.NewNumericDate(rsClock().Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(validExp()),
	}
	ext := map[string]any{"scope": "tools:read"}
	if cnf != nil {
		ext["cnf"] = cnf
	}
	raw, err := jwt.Signed(signer).Claims(std).Claims(ext).Serialize()
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}
	return raw
}

type s335ProofKey struct {
	key *ecdsa.PrivateKey
	jwk jose.JSONWebKey
	jkt string
}

func newS335ProofKey(t *testing.T) *s335ProofKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen proof key: %v", err)
	}
	jwk := jose.JSONWebKey{Key: key.Public(), Algorithm: "ES256", Use: "sig"}
	jkt, err := jwkThumbprint(&jwk)
	if err != nil {
		t.Fatalf("jwk thumbprint: %v", err)
	}
	return &s335ProofKey{key: key, jwk: jwk, jkt: jkt}
}

type dpopProofSpec struct {
	accessToken string
	method      string
	htu         string
	jti         string
	iat         time.Time
	ath         *string
	nonce       string
	typ         string
	signingKey  *ecdsa.PrivateKey
	headerJWK   jose.JSONWebKey
}

func mintS335DPoPProof(t *testing.T, pk *s335ProofKey, spec dpopProofSpec) string {
	t.Helper()
	method := spec.method
	if method == "" {
		method = http.MethodPost
	}
	htu := spec.htu
	if htu == "" {
		htu = rsResource
	}
	jti := spec.jti
	if jti == "" {
		jti = "jti-" + time.Now().Format("150405.000000000")
	}
	iat := spec.iat
	if iat.IsZero() {
		iat = rsClock()
	}
	ath := accessTokenHash(spec.accessToken)
	if spec.ath != nil {
		ath = *spec.ath
	}
	typ := spec.typ
	if typ == "" {
		typ = "dpop+jwt"
	}
	signingKey := spec.signingKey
	if signingKey == nil {
		signingKey = pk.key
	}
	headerJWK := spec.headerJWK
	if headerJWK.Key == nil {
		headerJWK = pk.jwk
	}
	opts := (&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("jwk", headerJWK)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: signingKey}, opts)
	if err != nil {
		t.Fatalf("proof signer: %v", err)
	}
	claims := map[string]any{
		"jti": jti,
		"htm": method,
		"htu": htu,
		"iat": jwt.NewNumericDate(iat),
	}
	if ath != "" {
		claims["ath"] = ath
	}
	if spec.nonce != "" {
		claims["nonce"] = spec.nonce
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign proof: %v", err)
	}
	return raw
}

func newS335UnitRS(t *testing.T, jwks []byte, mutate func(*ResourceServerConfig)) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	cfg := ResourceServerConfig{
		Resource:                   rsResource,
		AuthorizationServers:       []string{rsIssuer},
		Issuer:                     rsIssuer,
		IssuerJWKS:                 jwks,
		Toolset:                    ts,
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

func enforceS335DPoP(t *testing.T, rs *ResourceServer, accessToken, proof, scheme string) (validatedToken, error) {
	t.Helper()
	tok, err := rs.validator.validate(context.Background(), accessToken)
	if err != nil {
		t.Fatalf("validate access token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, rsResource+"?ignored=1", strings.NewReader(`{}`))
	if scheme == "" {
		scheme = "DPoP"
	}
	req.Header.Set("Authorization", scheme+" "+accessToken)
	if proof != "" {
		req.Header.Set("DPoP", proof)
	}
	return rs.enforceTokenBinding(req, accessToken, strings.ToLower(scheme), tok)
}

func TestDPoPBoundAccessTokenHappyPath(t *testing.T) {
	as := newS335AccessSigner(t)
	pk := newS335ProofKey(t)
	accessToken := as.mint(t, map[string]any{"jkt": pk.jkt})
	rs := newS335UnitRS(t, as.jwks, nil)
	proof := mintS335DPoPProof(t, pk, dpopProofSpec{accessToken: accessToken, htu: dpopHTU(rs.resource, "/gw"), jti: "happy"})

	tok, err := enforceS335DPoP(t, rs, accessToken, proof, "DPoP")
	if err != nil {
		t.Fatalf("dpop binding failed: %v", err)
	}
	if tok.Binding != tokenBindingDPoP {
		t.Fatalf("binding = %q, want %q", tok.Binding, tokenBindingDPoP)
	}
}

func TestDPoPProofRejections(t *testing.T) {
	as := newS335AccessSigner(t)
	pk := newS335ProofKey(t)
	accessToken := as.mint(t, map[string]any{"jkt": pk.jkt})
	goodHTU := dpopHTU(rsResource, "/gw")
	badATH := "wrong"
	otherPK := newS335ProofKey(t)

	cases := []struct {
		name      string
		access    string
		spec      dpopProofSpec
		beforeRun func(t *testing.T, rs *ResourceServer)
	}{
		{name: "wrong typ", spec: dpopProofSpec{typ: "jwt", jti: "wrong-typ"}},
		{name: "jwk with private material", spec: dpopProofSpec{headerJWK: jose.JSONWebKey{Key: pk.key}, jti: "private-jwk"}},
		{name: "bad signature", spec: dpopProofSpec{signingKey: otherPK.key, headerJWK: pk.jwk, jti: "bad-sig"}},
		{name: "wrong htm", spec: dpopProofSpec{method: http.MethodGet, jti: "wrong-htm"}},
		{name: "wrong htu includes query", spec: dpopProofSpec{htu: rsResource + "?ignored=1", jti: "wrong-htu"}},
		{name: "iat too old", spec: dpopProofSpec{iat: rsClock().Add(-dpopIATWindow - time.Second), jti: "old-iat"}},
		{name: "iat too new", spec: dpopProofSpec{iat: rsClock().Add(dpopIATWindow + time.Second), jti: "new-iat"}},
		{name: "missing jti", spec: dpopProofSpec{jti: " "}},
		{name: "missing ath", spec: dpopProofSpec{ath: new(string), jti: "missing-ath"}},
		{name: "wrong ath", spec: dpopProofSpec{ath: &badATH, jti: "wrong-ath"}},
		{
			name:   "jkt mismatch",
			access: as.mint(t, map[string]any{"jkt": otherPK.jkt}),
			spec:   dpopProofSpec{jti: "jkt-mismatch"},
		},
		{
			name: "duplicate jti replay",
			spec: dpopProofSpec{jti: "replayed"},
			beforeRun: func(t *testing.T, rs *ResourceServer) {
				t.Helper()
				proof := mintS335DPoPProof(t, pk, dpopProofSpec{
					accessToken: accessToken, htu: goodHTU, jti: "replayed",
				})
				if _, err := enforceS335DPoP(t, rs, accessToken, proof, "DPoP"); err != nil {
					t.Fatalf("prime replay cache: %v", err)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := newS335UnitRS(t, as.jwks, nil)
			if c.beforeRun != nil {
				c.beforeRun(t, rs)
			}
			access := c.access
			if access == "" {
				access = accessToken
			}
			spec := c.spec
			spec.accessToken = access
			if spec.htu == "" {
				spec.htu = goodHTU
			}
			proof := mintS335DPoPProof(t, pk, spec)
			if _, err := enforceS335DPoP(t, rs, access, proof, "DPoP"); err == nil {
				t.Fatal("proof unexpectedly accepted")
			}
		})
	}

	t.Run("query stripped htu passes", func(t *testing.T) {
		rs := newS335UnitRS(t, as.jwks, nil)
		proof := mintS335DPoPProof(t, pk, dpopProofSpec{
			accessToken: accessToken,
			htu:         goodHTU,
			jti:         "queryless-htu",
		})
		if _, err := enforceS335DPoP(t, rs, accessToken, proof, "DPoP"); err != nil {
			t.Fatalf("queryless htu should pass even when request has query: %v", err)
		}
	})
}

func TestDPoPNonceRequiredRetry(t *testing.T) {
	as := newS335AccessSigner(t)
	pk := newS335ProofKey(t)
	accessToken := as.mint(t, map[string]any{"jkt": pk.jkt})
	rs := newS335UnitRS(t, as.jwks, func(cfg *ResourceServerConfig) {
		cfg.RequireDPoPNonce = true
	})
	htu := dpopHTU(rs.resource, "/gw")

	absentNonce := mintS335DPoPProof(t, pk, dpopProofSpec{accessToken: accessToken, htu: htu, jti: "nonce-absent"})
	if _, err := enforceS335DPoP(t, rs, accessToken, absentNonce, "DPoP"); !errors.Is(err, errDPoPUseNonce) {
		t.Fatalf("absent nonce error = %v, want use_dpop_nonce", err)
	}

	current := rs.dpopNonces.fresh(rs.clock())
	withCurrent := mintS335DPoPProof(t, pk, dpopProofSpec{
		accessToken: accessToken, htu: htu, nonce: current, jti: "nonce-current",
	})
	if tok, err := enforceS335DPoP(t, rs, accessToken, withCurrent, "DPoP"); err != nil || tok.Binding != tokenBindingDPoP {
		t.Fatalf("current nonce binding = %q err=%v, want dpop", tok.Binding, err)
	}

	previous := rs.dpopNonces.nonceAt(rs.clock().Add(-dpopNonceBucket))
	withPrevious := mintS335DPoPProof(t, pk, dpopProofSpec{
		accessToken: accessToken, htu: htu, nonce: previous, jti: "nonce-previous",
	})
	if _, err := enforceS335DPoP(t, rs, accessToken, withPrevious, "DPoP"); err != nil {
		t.Fatalf("previous bucket nonce should pass: %v", err)
	}

	stale := rs.dpopNonces.nonceAt(rs.clock().Add(-2 * dpopNonceBucket))
	withStale := mintS335DPoPProof(t, pk, dpopProofSpec{
		accessToken: accessToken, htu: htu, nonce: stale, jti: "nonce-stale",
	})
	if _, err := enforceS335DPoP(t, rs, accessToken, withStale, "DPoP"); !errors.Is(err, errDPoPUseNonce) {
		t.Fatalf("stale nonce error = %v, want use_dpop_nonce", err)
	}
}

func TestDPoPReplayCache(t *testing.T) {
	as := newS335AccessSigner(t)
	pk := newS335ProofKey(t)
	accessToken := as.mint(t, map[string]any{"jkt": pk.jkt})
	rs := newS335UnitRS(t, as.jwks, nil)
	rs.dpopReplay = newDPoPReplayCache(4)
	htu := dpopHTU(rs.resource, "/gw")

	proof := mintS335DPoPProof(t, pk, dpopProofSpec{accessToken: accessToken, htu: htu, jti: "same-jti"})
	if _, err := enforceS335DPoP(t, rs, accessToken, proof, "DPoP"); err != nil {
		t.Fatalf("first proof: %v", err)
	}
	if _, err := enforceS335DPoP(t, rs, accessToken, proof, "DPoP"); err == nil {
		t.Fatal("replayed jti unexpectedly accepted")
	}

	rs = newS335UnitRS(t, as.jwks, nil)
	rs.dpopReplay = newDPoPReplayCache(4)
	for i := 0; i < 4; i++ {
		proof := mintS335DPoPProof(t, pk, dpopProofSpec{
			accessToken: accessToken,
			htu:         htu,
			jti:         "distinct-" + string(rune('a'+i)),
		})
		if _, err := enforceS335DPoP(t, rs, accessToken, proof, "DPoP"); err != nil {
			t.Fatalf("distinct jti %d should pass under cap: %v", i, err)
		}
	}
}
