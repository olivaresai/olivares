// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperschema

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

// SpoolKey is a helper's own secret, kept in its spool and readable by its account alone. It
// makes the nonces the helper issues verifiable without a list of them.
type SpoolKey [32]byte

// SpoolKind is the closed set of files a helper keeps in its spool.
type SpoolKind string

// The kinds of spooled file.
const (
	// SpoolBundle is a support bundle the support-bundle helper produced.
	SpoolBundle SpoolKind = "bundle.json"
	// SpoolCertificate is a certificate the Appliance Console spooled for the certificate helper to
	// install, under a nonce that helper issued.
	SpoolCertificate SpoolKind = "pem"
)

// nonceBytes is the random half of a nonce; the other half is its tag.
const nonceBytes = 16

const nonceDomain = "olivares-helper-spool-nonce/v1\n"

// Nonce is a spool name a helper issued: 16 random bytes and a 16-byte tag of them under the
// helper's key, in lowercase hexadecimal.
type Nonce string

// IssueNonce issues a nonce under key from random.
func IssueNonce(key SpoolKey, random io.Reader) (Nonce, error) {
	var raw [nonceBytes]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", errors.New("no random bytes for a nonce")
	}
	return Nonce(hex.EncodeToString(raw[:]) + hex.EncodeToString(tag(key, raw[:]))), nil
}

// SpoolName derives the file name of kind for a nonce this helper issued under key: the nonce
// and the kind, with no directory. It refuses anything that is not a nonce of this key, so a
// caller supplies a nonce, never a name, and cannot name a file the helper did not issue.
func (key SpoolKey) SpoolName(nonce string, kind SpoolKind) (string, error) {
	if kind != SpoolBundle && kind != SpoolCertificate {
		return "", refuse("$.kind", "not a kind of file this spool keeps")
	}
	if !nonceShape(nonce) {
		return "", refuse("$.nonce", "not a nonce: 64 lowercase hexadecimal digits")
	}
	raw, _ := hex.DecodeString(nonce)
	if !hmac.Equal(raw[nonceBytes:], tag(key, raw[:nonceBytes])) {
		return "", refuse("$.nonce", "not a nonce this helper issued")
	}
	return nonce + "." + string(kind), nil
}

func tag(key SpoolKey, random []byte) []byte {
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(nonceDomain))
	mac.Write(random)
	return mac.Sum(nil)[:nonceBytes]
}

// nonceShape reports whether s has a nonce's shape: 64 lowercase hexadecimal digits.
func nonceShape(s string) bool { return lowerHex(s, 4*nonceBytes) }

// lowerHex reports whether s is exactly n lowercase hexadecimal digits.
func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
