// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

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

// attMint is the set of knobs negative tests twist when minting an ABCA pair.
type attMint struct {
	attTyp, popTyp string
	cnfKey         jose.JSONWebKey
	popKey         any
	popAud         string
}

// mintAttestationPair mints a draft -09 ABCA pair: a Client Attestation JWT signed
// by the ATTESTER (binding the runtime instance public key via cnf/jwk) plus a PoP
// JWT signed by the INSTANCE key, audience-bound to the webhook. Returns both
// tokens and the attester's public JWKS (the operator trust anchor).
func mintAttestationPair(t *testing.T, jti string, mutate func(*attMint)) (attJWT, popJWT string, attesterJWKS []byte) {
	t.Helper()
	attesterKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("attester key: %v", err)
	}
	instanceKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("instance key: %v", err)
	}
	m := &attMint{
		attTyp: attestationTyp,
		popTyp: attestationPoPTyp,
		cnfKey: jose.JSONWebKey{Key: instanceKey.Public(), Algorithm: "ES256", Use: "sig"},
		popKey: instanceKey,
		popAud: pushAud,
	}
	if mutate != nil {
		mutate(m)
	}

	now := pushClock()
	attSigner, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: attesterKey},
		(&jose.SignerOptions{}).WithType(jose.ContentType(m.attTyp)).WithHeader("kid", "att-1"))
	if err != nil {
		t.Fatalf("att signer: %v", err)
	}
	attJWT = signMapJWT(t, attSigner, map[string]any{
		"iss": "https://attester.example",
		"sub": "runtime-client-1",
		"exp": now.Add(10 * time.Minute).Unix(),
		"iat": now.Add(-time.Minute).Unix(),
		"cnf": map[string]any{"jwk": m.cnfKey},
	})

	popSigner, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: m.popKey},
		(&jose.SignerOptions{}).WithType(jose.ContentType(m.popTyp)))
	if err != nil {
		t.Fatalf("pop signer: %v", err)
	}
	popJWT = signMapJWT(t, popSigner, map[string]any{
		"aud": m.popAud,
		"jti": jti,
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	})

	ks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: attesterKey.Public(), KeyID: "att-1", Algorithm: "ES256", Use: "sig"}}}
	blob, err := json.Marshal(ks)
	if err != nil {
		t.Fatalf("marshal attester jwks: %v", err)
	}
	return attJWT, popJWT, blob
}

func signMapJWT(t *testing.T, signer jose.Signer, claims map[string]any) string {
	t.Helper()
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return raw
}

// attestSetup builds an attestation-required receiver plus the full header set of a
// valid request (push bearer + attestation pair), with the pair optionally mutated.
func attestSetup(t *testing.T, mutate func(*attMint)) (*PushReceiver, http.Header) {
	t.Helper()
	pushToken, issuerJWKS := mintPushJWT(t, jose.ES256, "k1", validPushClaims("jti-att-"+t.Name()))
	attJWT, popJWT, attesterJWKS := mintAttestationPair(t, "pop-"+t.Name(), mutate)
	r, err := NewPushReceiver(PushReceiverConfig{
		Audience:                 pushAud,
		IssuerJWKS:               issuerJWKS,
		AllowedIssuers:           []string{pushIss},
		Clock:                    pushClock,
		RequireClientAttestation: true,
		AttesterJWKS:             attesterJWKS,
	})
	if err != nil {
		t.Fatalf("new receiver: %v", err)
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+pushToken)
	h.Set(attestationHeader, attJWT)
	h.Set(attestationPoPHeader, popJWT)
	return r, h
}

// doAttestPush posts the v1.0 push body with the given headers.
func doAttestPush(rec *PushReceiver, headers http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, pushAud, strings.NewReader(validPushBodyV1))
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)
	return w
}

// TestAttestRequiredValidPairAdmitted: valid attestation + PoP + push token → 204.
func TestAttestRequiredValidPairAdmitted(t *testing.T) {
	rec, h := attestSetup(t, nil)
	if w := doAttestPush(rec, h); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", w.Code, w.Body.String())
	}
}

// TestAttestRequiredMissingHeadersDenied: with the policy on, the push token alone
// is no longer enough — deny-closed admission.
func TestAttestRequiredMissingHeadersDenied(t *testing.T) {
	rec, h := attestSetup(t, nil)
	h.Del(attestationHeader)
	if w := doAttestPush(rec, h); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing attestation header status = %d, want 401", w.Code)
	}
}

// TestAttestPoPWrongKeyDenied: a PoP signed with a key OTHER than the attested
// instance key (cnf) is rejected — possession is the whole point.
func TestAttestPoPWrongKeyDenied(t *testing.T) {
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rec, h := attestSetup(t, func(m *attMint) { m.popKey = otherKey })
	if w := doAttestPush(rec, h); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-key PoP status = %d, want 401", w.Code)
	}
}

// TestAttestPoPWrongAudienceDenied: a PoP minted for another receiver is rejected
// (audience binding — the confused-deputy defense at the admission layer).
func TestAttestPoPWrongAudienceDenied(t *testing.T) {
	rec, h := attestSetup(t, func(m *attMint) { m.popAud = "https://other.example/hook" })
	if w := doAttestPush(rec, h); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-audience PoP status = %d, want 401", w.Code)
	}
}

// TestAttestPoPReplayDenied: re-presenting the same PoP jti is rejected.
func TestAttestPoPReplayDenied(t *testing.T) {
	rec, h := attestSetup(t, nil)
	if w := doAttestPush(rec, h); w.Code != http.StatusNoContent {
		t.Fatalf("first push = %d, want 204", w.Code)
	}
	if w := doAttestPush(rec, h); w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed PoP = %d, want 401", w.Code)
	}
}

// TestAttestPrivateCnfKeyDenied: an attestation whose cnf carries a PRIVATE key is
// rejected (draft: the cnf key must not be a private key).
func TestAttestPrivateCnfKeyDenied(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rec, h := attestSetup(t, func(m *attMint) {
		m.cnfKey = jose.JSONWebKey{Key: priv, Algorithm: "ES256", Use: "sig"} // private!
		m.popKey = priv
	})
	if w := doAttestPush(rec, h); w.Code != http.StatusUnauthorized {
		t.Fatalf("private cnf key status = %d, want 401", w.Code)
	}
}

// TestAttestWrongTypDenied: the draft's mandatory typ values are enforced.
func TestAttestWrongTypDenied(t *testing.T) {
	rec, h := attestSetup(t, func(m *attMint) { m.attTyp = "JWT" })
	if w := doAttestPush(rec, h); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong attestation typ status = %d, want 401", w.Code)
	}
}

// TestAttestDuplicateHeaderDenied: more than one attestation header is ambiguous →
// rejected (the draft requires precisely one of each).
func TestAttestDuplicateHeaderDenied(t *testing.T) {
	rec, h := attestSetup(t, nil)
	h.Add(attestationHeader, h.Get(attestationHeader))
	if w := doAttestPush(rec, h); w.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate attestation header status = %d, want 401", w.Code)
	}
}

// TestAttestUntrustedAttesterDenied: an attestation signed by an attester whose key
// is NOT in the operator anchor is rejected (a self-vouching runtime is worthless).
func TestAttestUntrustedAttesterDenied(t *testing.T) {
	rec, h := attestSetup(t, nil)
	// Mint a second, self-consistent pair from a DIFFERENT (untrusted) attester and
	// swap it in: the receiver's anchor only holds the first attester's key.
	attJWT, popJWT, _ := mintAttestationPair(t, "pop-untrusted", nil)
	h.Set(attestationHeader, attJWT)
	h.Set(attestationPoPHeader, popJWT)
	if w := doAttestPush(rec, h); w.Code != http.StatusUnauthorized {
		t.Fatalf("untrusted attester status = %d, want 401", w.Code)
	}
}

// TestAttestEnabledRequiresAnchor: enabling the gate without an attester anchor must
// fail construction (never a silently-open gate).
func TestAttestEnabledRequiresAnchor(t *testing.T) {
	_, issuerJWKS := mintPushJWT(t, jose.ES256, "k1", validPushClaims("x"))
	_, err := NewPushReceiver(PushReceiverConfig{
		Audience: pushAud, IssuerJWKS: issuerJWKS, AllowedIssuers: []string{pushIss},
		RequireClientAttestation: true, // no AttesterJWKS
	})
	if err == nil {
		t.Fatal("an attestation-required receiver without an attester anchor must not be constructed")
	}
}

// TestAttestDisabledIgnoresHeaders: with the policy OFF (default), the existing
// push verification governs alone — bogus attestation headers neither help nor harm.
func TestAttestDisabledIgnoresHeaders(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims("jti-noatt"))
	rec := newPushReceiver(t, jwks, nil)
	req := pushRequest(token, validPushBody)
	req.Header.Set(attestationHeader, "garbage")
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("attestation headers must be ignored when the policy is off, got %d", w.Code)
	}
}
