// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// TestSETVerificationFailsClosed: the checks every SET receiver shares refuse a SET
// that lacks what RFC 8417 requires (iat, jti, typ secevent+jwt), one whose exp has
// passed, one checked against a publisher that binds no issuer or audience, and an
// ES signature made on a curve its algorithm does not name.
func TestSETVerificationFailsClosed(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	pub := SETPublisher{Enabled: true, Issuer: "https://idp.example.test", Audiences: []string{"https://rp.example.test"}}
	good := SETEnvelope{
		Typ: "secevent+jwt", Issuer: pub.Issuer, Audience: pub.Audiences,
		JTI: "j1", IssuedAt: now.Unix(),
	}
	if err := checkIssuerAudience(pub, good, model.NewTimestamp(now)); err != nil {
		t.Fatalf("a complete SET = %v, want accepted", err)
	}
	if err := checkIssuerAudience(pub, func() SETEnvelope { e := good; e.Typ = "application/SECEVENT+JWT"; return e }(), model.NewTimestamp(now)); err != nil {
		t.Fatalf("typ with the application/ prefix = %v, want accepted (RFC 7515 4.1.9)", err)
	}
	for _, tc := range []struct {
		name string
		pub  func(*SETPublisher)
		env  func(*SETEnvelope)
		want error
	}{
		{name: "no iat", env: func(e *SETEnvelope) { e.IssuedAt = 0 }, want: ErrSETClaims},
		{name: "no jti", env: func(e *SETEnvelope) { e.JTI = "" }, want: ErrSETClaims},
		{name: "no typ", env: func(e *SETEnvelope) { e.Typ = "" }, want: ErrSETClaims},
		{name: "another JWT type", env: func(e *SETEnvelope) { e.Typ = "JWT" }, want: ErrSETClaims},
		{name: "exp passed", env: func(e *SETEnvelope) { e.ExpiresAt = now.Add(-time.Hour).Unix() }, want: ErrSETClaims},
		{name: "publisher binds no issuer", pub: func(p *SETPublisher) { p.Issuer = "" }, env: func(e *SETEnvelope) { e.Issuer = "https://other.example.test" }, want: ErrSCIMSetIssuer},
		{name: "publisher binds no audience", pub: func(p *SETPublisher) { p.Audiences = nil }, env: func(e *SETEnvelope) { e.Audience = []string{"https://other.example.test"} }, want: ErrSCIMSetIssuer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, e := pub, good
			if tc.pub != nil {
				tc.pub(&p)
			}
			tc.env(&e)
			if err := checkIssuerAudience(p, e, model.NewTimestamp(now)); !errors.Is(err, tc.want) {
				t.Fatalf("checkIssuerAudience = %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("ES256 over a P-384 key", func(t *testing.T) {
		priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		input := []byte("header.payload")
		sum := sha256.Sum256(input)
		r, s, err := ecdsa.Sign(rand.Reader, priv, sum[:])
		if err != nil {
			t.Fatal(err)
		}
		sig := make([]byte, 96)
		r.FillBytes(sig[:48])
		s.FillBytes(sig[48:])
		key := SETVerificationKey{Alg: "ES256", PEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}
		env := SETEnvelope{Alg: "ES256", SigningInput: input, Signature: sig}
		if err := verifySETSignature(SETPublisher{Keys: []SETVerificationKey{key}}, env); !errors.Is(err, ErrSCIMSetSignature) {
			t.Fatalf("verifySETSignature = %v, want ErrSCIMSetSignature", err)
		}
	})
}
