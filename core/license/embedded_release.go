// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build release

package license

import "crypto/ed25519"

// This file is the RELEASE variant (`go build -tags release`). It carries NO dev
// seed: the development keypair is physically absent from a release binary. The
// verification anchor is the link-time-injected release public key
// (embedded.go: releasePublicKeyB64). A release build with none injected has a nil
// DefaultPublicKey and verifies nothing — safe, because licensing gates nothing
// (attestation-only). The build-time validator (`task build:release`) rejects an
// empty or malformed key before a release artifact is produced.

// HasDevKey is false in a release build — no dev signing key is compiled in.
const HasDevKey = false

// DefaultPublicKey is the injected release verification key, resolved live. It is
// nil when no key was injected ("none") or the injected value failed to decode
// ("misconfigured" — see KeyOrigin); a nil key makes license status "none" and
// never blocks boot.
func DefaultPublicKey() ed25519.PublicKey {
	k, _ := releaseKey()
	return k
}

// DevPrivateKey is unavailable in a release build: production signing uses the
// dedicated license private key via `license sign --key`. It returns nil so a missing
// --key fails clearly (the CLI checks HasDevKey) instead of signing with a dev key
// that this binary does not contain.
func DevPrivateKey() ed25519.PrivateKey { return nil }
