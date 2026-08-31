// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package license

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// releasePublicKeyB64 is the LICENSE verification key, injected at LINK TIME. The
// historical symbol name is retained so existing direct `-ldflags -X` build recipes
// remain compatible; release tooling maps OLIVARES_LICENSE_PUBKEY to it.
// is a plain, UNINITIALIZED string var precisely so `go tool link -X` can patch
// it: the linker only rewrites string vars declared uninitialized (or set to a
// constant string). The engine's verification key is an ed25519.PublicKey (a
// []byte computed at init) which `-X` cannot patch at all — hence this separate
// string seam, decoded into the key below.
//
// A release build injects it:
//
//	go build -tags release -ldflags \
//	  "-X github.com/olivaresai/olivares/core/license.releasePublicKeyB64=<base64-std public key>"
//
// `olivares license keygen` prints the value to inject here. Its matching PRIVATE
// key is scoped to the online license Worker (never the repo, binary, build host, or
// OTA ceremony). It is empty in every non-release build. There is NO runtime key
// override — the key compiled into a `-tags release` build IS that build's license
// verification anchor.
var releasePublicKeyB64 string

// releaseKey decodes the injected release public key. It returns:
//
//	(nil, nil) — no key injected (every non-release build; a release build that
//	             forgot to inject one). License status degrades to "none".
//	(nil, err) — a non-empty value that does not decode to a 32-byte Ed25519
//	             public key (a broken release pipeline).
//
// It NEVER panics. A malformed injection is a build-pipeline error surfaced
// loudly but NON-fatally (KeyOrigin reports "misconfigured"), so a typo can never
// brick `version`/`license keygen`/`--help` or block boot — licensing is
// attestation-only and gates nothing (see the package doc). The build-time
// validator (`task build:release`) rejects a malformed key BEFORE a release
// artifact is ever produced, so this path is the runtime backstop, not the gate.
func releaseKey() (ed25519.PublicKey, error) {
	s := strings.TrimSpace(releasePublicKeyB64)
	if s == "" {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("license: releasePublicKeyB64 is set but is not valid base64: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("license: injected release public key is %d bytes, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// KeyOrigin reports which verification anchor this binary was BUILT with, for
// `olivares version`:
//
//	dev           — the public, non-secret dev seed (development/demo/test builds)
//	release       — a release key was injected at build time (-tags release)
//	none          — a release build with NO key injected (verifies nothing)
//	misconfigured — a release build whose injected key failed to decode
//
// It is a build-provenance AID, not an attestation: a binary self-reports its own
// compiled-in key, so a malicious or misconfigured pipeline can embed any key and
// still print "release". Trust in the key comes from the signed release pipeline
// (cosign/SLSA — docs/RELEASE-VERIFICATION.md), never from this string.
func KeyOrigin() string {
	if HasDevKey {
		return "dev"
	}
	k, err := releaseKey()
	switch {
	case err != nil:
		return "misconfigured"
	case k == nil:
		return "none"
	default:
		return "release"
	}
}

// KeyFingerprint is the first 8 hex chars of SHA-256 over the embedded
// verification public key — a human-eyeball identifier so an operator can confirm
// which key a binary trusts (and that two artifacts, e.g. olivares and
// olivares-fips, share it). It returns "" when no key is embedded. The full
// public key is itself public, so this truncation leaks nothing; for programmatic
// equality compare the full key, not the fingerprint.
func KeyFingerprint() string {
	k := DefaultPublicKey()
	if len(k) != ed25519.PublicKeySize {
		return ""
	}
	sum := sha256.Sum256(k)
	return hex.EncodeToString(sum[:4])
}
