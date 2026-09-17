// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package connectv1

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// KID derives a PoP key id: "sha256:" and the lowercase hex SHA-256 of the raw 32-byte key
// (keys.ts popKid).
func KID(pub ed25519.PublicKey) (string, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", fmt.Errorf("connectv1: a PoP public key is 32 raw Ed25519 bytes")
	}
	sum := sha256.Sum256(pub)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Fingerprint is the human comparison form shown on the approval page: the first eight bytes of
// the key digest as colon-separated lowercase hex (keys.ts popFingerprint).
func Fingerprint(pub ed25519.PublicKey) (string, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", fmt.Errorf("connectv1: a PoP public key is 32 raw Ed25519 bytes")
	}
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:8])
	parts := make([]string, 0, 8)
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":"), nil
}

// EncodePublicKey is the unpadded base64url form the Worker expects in `public_key`.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(pub)
}

// DecodePublicKey parses the unpadded base64url form.
func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("connectv1: a PoP public key is 32 raw Ed25519 bytes in base64url")
	}
	return ed25519.PublicKey(raw), nil
}

// Sign returns the unpadded base64url Ed25519 signature over the canonical message.
func Sign(priv ed25519.PrivateKey, m PopMessage) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("connectv1: PoP private key has the wrong size")
	}
	msg, err := EncodePopMessage(m)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, msg)), nil
}

// Verify checks an unpadded base64url signature over the canonical message.
func Verify(pub ed25519.PublicKey, m PopMessage, sig string) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return false
	}
	msg, err := EncodePopMessage(m)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, msg, raw)
}

// BodyDigest is the lowercase hex SHA-256 of the exact request body bytes.
func BodyDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// NewIdempotencyKey draws a 32-byte random operation id (43 base64url characters, inside the
// Worker's 8..128 bound).
func NewIdempotencyKey(random io.Reader) (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(random, buf); err != nil {
		return "", fmt.Errorf("connectv1: draw an idempotency key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
