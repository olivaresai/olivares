// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !release

package license

import "crypto/ed25519"

// This file is compiled into DEVELOPMENT, demo and test builds only (the default
// `go build`). A RELEASE build (`go build -tags release`) compiles
// embedded_release.go instead, which carries NO dev seed — so the dev keypair
// below is physically absent from a release binary.

// devSeed is a FIXED, NON-SECRET 32-byte seed for the development license
// keypair. The public key derived from it is the engine's default offline
// verification key in dev/demo/test builds, so `license sign`/`verify` and the
// demos work out of the box. It is deliberately public: it signs nothing of
// value, and because license verification gates NOTHING (attestation-only — see
// the package doc), a dev-signed or even forged license confers no technical
// capability. Production trust comes from the dedicated license key injected into
// a `-tags release` build (embedded.go: releasePublicKeyB64), whose private half is
// scoped to the online license-issuance Worker and never reused for OTA.
var devSeed = [32]byte{
	0x0a, 0x11, 0x7e, 0x53, 0x4f, 0x6c, 0x69, 0x76,
	0x61, 0x72, 0x65, 0x73, 0x2e, 0x41, 0x49, 0x20,
	0x64, 0x65, 0x76, 0x20, 0x6c, 0x69, 0x63, 0x65,
	0x6e, 0x73, 0x65, 0x20, 0x6b, 0x65, 0x79, 0x31,
}

func devKeys() (ed25519.PublicKey, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(devSeed[:])
	return priv.Public().(ed25519.PublicKey), priv
}

// HasDevKey reports whether this build ships the dev signing key. It is a
// build-time const (true here, false in release builds) so the compiler can
// eliminate the dev-key branches from a release binary.
const HasDevKey = true

// DefaultPublicKey is the engine's built-in offline verification key. In a
// development build it is ALWAYS the dev key — the build tag, not an env var or
// ldflag, selects the anchor, so any injected release key is ignored here (a
// release key belongs in a `-tags release` build, keeping KeyOrigin/KeyFingerprint
// consistent). It is resolved live (a function, not a frozen var) so `-ldflags -X`
// injection and tests observe the real value rather than a value captured at init.
func DefaultPublicKey() ed25519.PublicKey {
	pub, _ := devKeys()
	return pub
}

// DevPrivateKey returns the development signing key — for demos, `license sign`
// without --key, and the tests. Release builds do not ship it (the release
// variant returns nil); production signs with the off-machine key passed to
// `license sign --key`.
func DevPrivateKey() ed25519.PrivateKey {
	_, priv := devKeys()
	return priv
}
