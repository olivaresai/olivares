// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"os"
	"strings"
)

// setupTokenBytes is the entropy of a setup token (256 bits).
const setupTokenBytes = 32

// setupPrefix prefixes the setup token so it is recognizable in operator output.
const setupPrefix = "olst_"

var setupB32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// SetupToken manages the first-boot setup token at a fixed path. Only the SHA-256
// of the token is persisted (0600), so the file leaking does not reveal the
// token; the plaintext is returned exactly once, to be printed to stdout.
type SetupToken struct {
	path string
}

// NewSetupToken manages the setup token stored at path.
func NewSetupToken(path string) *SetupToken { return &SetupToken{path: path} }

// Exists reports whether a setup token is currently active.
func (s *SetupToken) Exists() bool { return fileExists(s.path) }

// Ensure mints a setup token if none exists, returning the plaintext to print
// once (created=true). If one already exists it returns created=false and no
// plaintext (the original was shown at mint time and cannot be recovered).
func (s *SetupToken) Ensure() (plaintext string, created bool, err error) {
	if s.Exists() {
		return "", false, nil
	}
	raw := make([]byte, setupTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", false, fmt.Errorf("secure: read entropy: %w", err)
	}
	token := setupPrefix + setupB32.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	if err := writeSecret(s.path, []byte(setupB32.EncodeToString(sum[:])+"\n")); err != nil {
		return "", false, err
	}
	return token, true, nil
}

// Verify reports whether presented matches the active setup token, in constant
// time. It is false if no token is active.
func (s *SetupToken) Verify(presented string) bool {
	if !s.Exists() {
		return false
	}
	b, err := readSecret(s.path)
	if err != nil {
		return false
	}
	want, err := setupB32.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return false
	}
	got := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(got[:], want) == 1
}

// Consume invalidates the setup token (single-use), called after the first
// superadmin is created. It is idempotent.
func (s *SetupToken) Consume() error {
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("secure: consume setup token: %w", err)
	}
	return nil
}
