// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"
)

// es256Signer holds a freshly generated P-256 key and renders its public half as
// the PEM the SET config stores.
type es256Signer struct {
	priv *ecdsa.PrivateKey
	pem  string
}

func newES256Signer(t *testing.T) es256Signer {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return es256Signer{priv: priv, pem: string(pubPEM)}
}

// signSET serializes and ES256-signs a SET with the given claims, returning the
// compact JWS the receiver consumes.
func (s es256Signer) signSET(t *testing.T, kid string, claims map[string]any) []byte {
	t.Helper()
	b64 := base64.RawURLEncoding
	hdr, _ := json.Marshal(map[string]any{"alg": "ES256", "kid": kid, "typ": "secevent+jwt"})
	payload, _ := json.Marshal(claims)
	signingInput := b64.EncodeToString(hdr) + "." + b64.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signingInput))
	r, ss, err := ecdsa.Sign(rand.Reader, s.priv, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	ss.FillBytes(sig[32:])
	return []byte(signingInput + "." + b64.EncodeToString(sig))
}
