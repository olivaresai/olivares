// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func svidClock() time.Time { return time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC) }

// signedSVID mints a JWT-SVID signed with a fresh EC key and returns the token plus
// the matching public JWKS JSON. alg/kid/claims are overridable for negative tests.
func signedSVID(t *testing.T, alg jose.SignatureAlgorithm, kid string, claims jwt.Claims) (token string, jwks string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: key.Public(), KeyID: kid, Algorithm: string(alg), Use: "sig"},
	}}
	blob, err := json.Marshal(ks)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return raw, string(blob)
}

func validClaims() jwt.Claims {
	now := svidClock()
	return jwt.Claims{
		Subject:  "spiffe://corp.example/workload/api",
		Audience: jwt.Audience{"anthropic-wif"},
		IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(now.Add(time.Hour)),
	}
}

func newVerifier(t *testing.T, jwks string) *Verifier {
	t.Helper()
	v, err := NewVerifier(VerifierConfig{
		TrustDomain: "corp.example", Audience: "anthropic-wif", JWKS: jwks,
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	v.now = svidClock
	return v
}

func TestVerifyValidSVID(t *testing.T) {
	token, jwks := signedSVID(t, jose.ES256, "k1", validClaims())
	v := newVerifier(t, jwks)

	got, err := v.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.SpiffeID != "spiffe://corp.example/workload/api" {
		t.Errorf("spiffe id = %q", got.SpiffeID)
	}
	if got.Assertion() != token {
		t.Error("assertion must be the raw token to forward to the WIF exchange")
	}
	if got.RosterRef() != got.SpiffeID {
		t.Error("roster ref must equal the spiffe id (external_id convergence)")
	}
	if !got.ExpiresAt.Equal(svidClock().Add(time.Hour)) {
		t.Errorf("expiry = %v", got.ExpiresAt)
	}
}

func TestVerifyRejects(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		c := validClaims()
		c.Expiry = jwt.NewNumericDate(svidClock().Add(-time.Hour))
		token, jwks := signedSVID(t, jose.ES256, "k1", c)
		if _, err := newVerifier(t, jwks).Verify(context.Background(), token); err == nil {
			t.Fatal("expired token must be rejected")
		}
	})
	t.Run("wrong audience", func(t *testing.T) {
		c := validClaims()
		c.Audience = jwt.Audience{"someone-else"}
		token, jwks := signedSVID(t, jose.ES256, "k1", c)
		if _, err := newVerifier(t, jwks).Verify(context.Background(), token); err == nil {
			t.Fatal("wrong audience must be rejected")
		}
	})
	t.Run("wrong trust domain", func(t *testing.T) {
		c := validClaims()
		c.Subject = "spiffe://evil.example/workload/api"
		token, jwks := signedSVID(t, jose.ES256, "k1", c)
		if _, err := newVerifier(t, jwks).Verify(context.Background(), token); err == nil {
			t.Fatal("subject outside the trust domain must be rejected")
		}
	})
	t.Run("non-spiffe subject", func(t *testing.T) {
		c := validClaims()
		c.Subject = "not-a-spiffe-id"
		token, jwks := signedSVID(t, jose.ES256, "k1", c)
		if _, err := newVerifier(t, jwks).Verify(context.Background(), token); err == nil {
			t.Fatal("non-SPIFFE subject must be rejected")
		}
	})
	t.Run("unknown signing key", func(t *testing.T) {
		token, _ := signedSVID(t, jose.ES256, "k1", validClaims())
		_, otherJWKS := signedSVID(t, jose.ES256, "k1", validClaims()) // different key, same kid
		if _, err := newVerifier(t, otherJWKS).Verify(context.Background(), token); err == nil {
			t.Fatal("a token not signed by the trust-bundle key must be rejected")
		}
	})
	t.Run("hmac algorithm rejected", func(t *testing.T) {
		// A token signed with a symmetric alg must not even parse against the
		// asymmetric allow-list (algorithm-confusion defense).
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
		if err != nil {
			t.Fatalf("hmac signer: %v", err)
		}
		raw, err := jwt.Signed(signer).Claims(validClaims()).Serialize()
		if err != nil {
			t.Fatalf("hmac sign: %v", err)
		}
		_, jwks := signedSVID(t, jose.ES256, "k1", validClaims())
		if _, err := newVerifier(t, jwks).Verify(context.Background(), raw); err == nil {
			t.Fatal("HMAC-signed token must be rejected")
		}
	})
}

func TestVerifierConfigErrors(t *testing.T) {
	if _, err := NewVerifier(VerifierConfig{}, nil); err == nil {
		t.Fatal("verifier with no key source must error")
	}
	if _, err := NewVerifier(VerifierConfig{JWKS: "{not json"}, nil); err == nil {
		t.Fatal("malformed inline jwks must error")
	}
}
