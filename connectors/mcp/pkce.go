// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// pkceMethodS256 is the only code_challenge_method this client uses. Per OAuth 2.1
// §7.5.2 (referenced by the MCP 2025-06-18 authorization spec) S256 is mandatory to
// implement and a capable client MUST use it; "plain" is never sent.
const pkceMethodS256 = "S256"

// pkce is a PKCE (RFC 7636) verifier/challenge pair. The verifier is a high-entropy
// secret kept by the client; the challenge = BASE64URL(SHA256(verifier)) is sent in
// the authorization request and the verifier in the token request.
type pkce struct {
	verifier  string
	challenge string
}

// newPKCE generates a fresh S256 verifier/challenge pair (32 bytes of entropy →
// 43-char base64url verifier, within the RFC 7636 43–128 range).
func newPKCE() (pkce, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return pkce{}, fmt.Errorf("mcp: pkce: entropy: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return pkce{
		verifier:  verifier,
		challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}
