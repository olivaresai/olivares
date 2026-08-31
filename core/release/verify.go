// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package release verifies a downloaded Olivares release artifact OFFLINE,
// against a dedicated OTA-signing public key compiled into this binary. It is the
// trust anchor for `olivares upgrade`: a self-serve in-place upgrade must
// never run an unverified binary (docs/SECURITY-HARDENING.md), and the check must work
// air-gapped — no network, no cosign, no Rekor, no external tool.
//
// The model mirrors the signed-release pipeline (scripts/verify-release.sh):
// the signer produces a `checksums.txt` manifest of `<sha256hex>  <filename>`
// lines and an Ed25519 detached signature over that file's exact bytes. The
// verifier here, in order:
//
//  1. verifies the detached signature over checksums.txt against the embedded
//     (or operator-supplied) release public key — this authenticates the whole
//     manifest with ONE signature check;
//  2. confirms the downloaded artifact's SHA-256 matches its checksums entry —
//     binding the authenticated manifest to the actual bytes on disk.
//
// BOTH must hold. Either failure aborts the upgrade with the running binary
// untouched. There is no "skip verification" path: a build with no embedded key
// and no --pubkey fails closed (VerifyChecksumsSignature errors on a nil key),
// so an unauthenticated artifact is never executed.
package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// artifactVerifyKeyB64 is the base64 (std) Ed25519 PUBLIC key the OTA release
// ceremony signs update manifests with. It is injected at link time by the signed
// release build independently from the license key:
//
//	go build -tags release -ldflags \
//	  "-X github.com/olivaresai/olivares/core/release.artifactVerifyKeyB64=<public_key>"
//
// (or via OLIVARES_OTA_PUBKEY for `task build:release` / goreleaser). It is EMPTY
// by default: a development build embeds no OTA key, so `upgrade` fail-closes
// unless the operator passes an explicit --pubkey. The PRIVATE half is generated
// and held off-box/HSM and signs only in the offline release ceremony; it never
// enters repo, CI, the build container, or the online license Worker.
var artifactVerifyKeyB64 string

// Verification errors. None is an authorization control — they are integrity
// signals surfaced to the operator by `upgrade`.
var (
	// ErrNoKey is returned when verification is attempted with no key at all
	// (build embeds none and the operator passed no --pubkey). Fail-closed: an
	// artifact is NEVER trusted without a key to check it against.
	ErrNoKey = errors.New("release: no OTA-verification key (this build embeds none; pass --pubkey <base64|file>)")
	// ErrBadSignature is returned when the detached signature over checksums.txt
	// does not verify against the key — a tampered or wrong-key manifest.
	ErrBadSignature = errors.New("release: signature does not verify against the OTA key")
	// ErrChecksumMismatch is returned when the artifact's SHA-256 does not match
	// its authenticated checksums entry — a tampered or truncated download.
	ErrChecksumMismatch = errors.New("release: artifact SHA-256 does not match the signed checksums")
	// ErrNotInManifest is returned when the artifact filename is absent from the
	// (authenticated) checksums manifest — nothing to bind the bytes to.
	ErrNotInManifest = errors.New("release: artifact filename is not listed in checksums.txt")
	// ErrDigestDisagreement is returned when an UPDATE MANIFEST claims a SHA-256
	// that disagrees with the same filename's entry in the out-of-band-authenticated
	// checksums.txt (cosign/SLSA in the public pipeline). It is the signal that the
	// manifest and the release it claims to describe have diverged — the exact
	// footprint of a manifest substituted between build and signing ceremony.
	ErrDigestDisagreement = errors.New("release: manifest digest disagrees with the signed checksums.txt")
)

// EmbeddedKey returns the compiled-in OTA-verification public key, or nil
// when this build embeds none (dev/community) or the injected value is malformed
// (treated as absent — KeyOrigin reports "misconfigured").
func EmbeddedKey() ed25519.PublicKey {
	if strings.TrimSpace(artifactVerifyKeyB64) == "" {
		return nil
	}
	pub, err := DecodePublicKey(artifactVerifyKeyB64)
	if err != nil {
		return nil
	}
	return pub
}

// KeyConfigured reports whether this build embeds a usable OTA key.
func KeyConfigured() bool { return EmbeddedKey() != nil }

// KeyOrigin classifies the embedded key for the `version` provenance string:
// "release" (a valid key is embedded), "none" (dev/community — no key), or
// "misconfigured" (a value was injected but does not decode).
func KeyOrigin() string {
	if strings.TrimSpace(artifactVerifyKeyB64) == "" {
		return "none"
	}
	if EmbeddedKey() == nil {
		return "misconfigured"
	}
	return "release"
}

// KeyFingerprint returns an 8-hex SHA-256 prefix of the embedded key for display,
// or "" when no valid key is embedded. It is a provenance aid, not an attestation.
func KeyFingerprint() string {
	pub := EmbeddedKey()
	if pub == nil {
		return ""
	}
	return Fingerprint(pub)
}

// Fingerprint returns an 8-hex SHA-256 prefix of a public key for display.
func Fingerprint(pub ed25519.PublicKey) string {
	if len(pub) == 0 {
		return ""
	}
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:4])
}

// DecodePublicKey parses a base64 Ed25519 public key. It accepts std base64
// first, then raw-URL base64, so a key copied from either encoding works.
func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("release: empty public key")
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		if raw, err = base64.RawURLEncoding.DecodeString(s); err != nil {
			return nil, fmt.Errorf("release: public key is not valid base64: %w", err)
		}
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("release: public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// decodeSignature parses a detached signature, accepting either raw 64 bytes or
// a base64 (std then raw-URL) encoding of them — so a `.sig` file written in
// either form verifies.
func decodeSignature(sig []byte) ([]byte, error) {
	if len(sig) == ed25519.SignatureSize {
		return sig, nil
	}
	s := strings.TrimSpace(string(sig))
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		if raw, err = base64.RawURLEncoding.DecodeString(s); err != nil {
			return nil, fmt.Errorf("release: signature is neither %d raw bytes nor base64", ed25519.SignatureSize)
		}
	}
	if len(raw) != ed25519.SignatureSize {
		return nil, fmt.Errorf("release: decoded signature is %d bytes, want %d", len(raw), ed25519.SignatureSize)
	}
	return raw, nil
}

// VerifySignature verifies a detached Ed25519 signature over the EXACT bytes of
// msg against pub. sig may be raw 64 bytes or base64 (std then raw-URL). A nil or
// short key fails closed with ErrNoKey — a caller must never treat "no key" as
// "verified".
//
// FOOTGUN — this is a RAW, UNTAGGED signature primitive. It verifies whatever
// bytes you hand it under whatever key you hand it; it carries NO domain tag of
// its own, so a caller that passes attacker-influenced bytes turns the OTA anchor
// into a general-purpose signature oracle across message types. Do NOT call it
// directly for a new signed message type. Callers MUST go through a wrapper that
// prepends a per-type tag from core/sigbundle — VerifyManifest
// (sigbundle.TagUpdateManifest) is the model, and today the only caller in this
// package. Adding a new signed OTA document means adding a new tag + wrapper,
// never a bare VerifySignature call.
func VerifySignature(msg, sig []byte, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return ErrNoKey
	}
	raw, err := decodeSignature(sig)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, msg, raw) {
		return ErrBadSignature
	}
	return nil
}

// VerifyChecksumsSignature verifies a detached Ed25519 signature over the EXACT
// bytes of checksums.txt against pub. A nil key fails closed with ErrNoKey — the
// caller must never treat "no key" as "verified".
//
// FOOTGUN — same untagged-primitive caveat as VerifySignature, and it has NO
// caller in the shipping update path: the public pipeline authenticates
// checksums.txt with cosign/Sigstore (keyless OIDC), NOT with the Ed25519 OTA
// anchor, and `upgrade` binds bytes through the update manifest instead (see
// Verify below). It exists for a key-based/air-gapped signer that chooses to sign
// checksums.txt with the OTA key. If you reach for it, remember the bytes it
// authenticates are untagged: never feed it a document that could also parse as
// another OTA message type.
func VerifyChecksumsSignature(checksums, sig []byte, pub ed25519.PublicKey) error {
	return VerifySignature(checksums, sig, pub)
}

// ParseChecksums parses a sha256sum-style manifest into filename→lowercase-hex.
// Each non-blank, non-comment line is `<64-hex><space><space-or-*><name>`
// (GNU coreutils text/binary forms). A malformed line is an error — a manifest
// we cannot fully parse is not one we will trust bytes against.
//
// A DUPLICATE filename is an error too. Last-wins would let an appended line silently
// redefine a digest that an earlier reader (a human eyeballing the file, another tool
// parsing first-wins) already resolved differently — one document, two answers about
// the same bytes. goreleaser never emits one, so a duplicate is either corruption or
// an attempt to make the file mean different things to different readers.
func ParseChecksums(b []byte) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Split into exactly the hex and the remainder on the first run of spaces.
		fields := strings.SplitN(trimmed, " ", 2)
		if len(fields) != 2 {
			return nil, fmt.Errorf("release: checksums line %d is malformed: %q", i+1, line)
		}
		sum := strings.ToLower(strings.TrimSpace(fields[0]))
		name := strings.TrimSpace(fields[1])
		name = strings.TrimPrefix(name, "*") // binary-mode marker
		name = strings.TrimSpace(name)
		if len(sum) != 2*sha256.Size || !isHex(sum) {
			return nil, fmt.Errorf("release: checksums line %d has a non-SHA-256 digest: %q", i+1, fields[0])
		}
		if name == "" {
			return nil, fmt.Errorf("release: checksums line %d names no file", i+1)
		}
		if prev, dup := out[name]; dup {
			return nil, fmt.Errorf("release: checksums line %d lists %q a second time (%s then %s) — one file, two digests", i+1, name, prev, sum)
		}
		out[name] = sum
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("release: checksums.txt lists no artifacts")
	}
	return out, nil
}

// isHex reports whether s is all lowercase hex digits.
func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return len(s) > 0
}

// VerifyArtifactSHA256 recomputes SHA-256 over data and constant-time compares it
// to wantHex (lowercase hex). A mismatch is ErrChecksumMismatch.
func VerifyArtifactSHA256(data []byte, wantHex string) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(strings.ToLower(strings.TrimSpace(wantHex)))) != 1 {
		return fmt.Errorf("%w: got %s, want %s", ErrChecksumMismatch, got, wantHex)
	}
	return nil
}

// Verify performs the full offline check for one artifact against an OTA-key-signed
// checksums.txt: authenticate the checksums manifest, then bind the artifact bytes
// to their authenticated digest. filename is the artifact's name as listed in
// checksums.txt.
//
// It returns nil only when the signature verifies against the OTA key AND the bytes
// match the signed digest.
//
// SCOPE — this is NOT the path `upgrade` takes. `olivares upgrade` verifies the
// per-channel UPDATE MANIFEST (VerifyManifest, domain-tagged) and then binds the
// downloaded bytes with VerifyArtifactSHA256 against that manifest's digest
// (cmd/olivares/cmd_upgrade.go). Verify is the checksums.txt-shaped variant, kept
// for a key-based/air-gapped signer that publishes a signed checksums.txt instead
// of a manifest; it has no caller in the shipping update path. In the public
// pipeline checksums.txt is authenticated by cosign, and the manifest digests are
// cross-checked against it by Manifest.CrossCheckChecksums, not here.
func Verify(artifact, checksums, sig []byte, filename string, pub ed25519.PublicKey) error {
	if err := VerifyChecksumsSignature(checksums, sig, pub); err != nil {
		return err
	}
	sums, err := ParseChecksums(checksums)
	if err != nil {
		return err
	}
	want, ok := sums[filename]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNotInManifest, filename)
	}
	return VerifyArtifactSHA256(artifact, want)
}
