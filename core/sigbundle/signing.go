// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package sigbundle is the one signed-envelope primitive shared by every Olivares
// subsystem that publishes a document a disconnected/air-gapped consumer must verify
// OFFLINE: the OTA update manifest, the DDIL air-gap bundle, and the
// security advisories feed. Callers choose the dedicated key for their domain.
//
// # Trust model (identical to manifest)
//
// A producer signs the DOMAIN-SEPARATED, VERBATIM bytes of a payload — `tag ||
// payload` — with a detached Ed25519 signature. A verifier checks the signature
// BEFORE unmarshalling, so a tampered or wrong-protocol payload never reaches decision
// logic. There is no canonicalisation step to disagree on: producer and verifier sign
// and check the exact bytes served.
//
// # Domain separation is the whole point
//
// Keys may be shared by closely related offline message types or accidentally reused
// despite the custody design. Without a per-type tag, a signature minted for one type
// could verify as another. Every tag is registered here (Tags) and a test enforces that
// no tag is a prefix of another, so the message spaces remain provably disjoint even
// if a future configuration error reuses a key.
package sigbundle

import (
	"crypto/ed25519"
	"errors"
	"strings"
)

// Domain tags. Each MUST end in '\n' so a tag can never be a prefix of a longer
// tag that shares its text, and MUST be registered in Tags below. Adding a signed
// document type is: add a const here, add it to Tags, and the uniqueness test guards
// the rest.
const (
	// TagUpdateManifest is the OTA per-channel update manifest. Its exact bytes
	// are load-bearing: core/release.ManifestSigningInput must stay byte-identical so
	// every already-issued release signature keeps verifying.
	TagUpdateManifest = "olivares.update-manifest.v1\n"
	// TagDDILBundle is the air-gap bundle carrying policy + audit + evidence for
	// sneakernet across a disconnected gap.
	TagDDILBundle = "olivares.ddil-bundle.v1\n"
	// TagSecurityAdvisories is the machine-readable OSV advisories feed the
	// product self-checks its version + SBOM against.
	TagSecurityAdvisories = "olivares.security-advisories.v1\n"
	// TagMemoryPortability is the governed-memory portability manifest: the
	// signed sidecar of a per-caller-clearance memory export whose entries_sha256
	// binds the JSONL body. It is a DATA-portability artifact (anti-lock-in), never
	// a policy/update channel, so it gets its own domain so a portability signature
	// can never be replayed as an update-manifest or DDIL bundle.
	TagMemoryPortability = "olivares.memory-portability.v1\n"
)

// Tags is the registry of every domain tag in use. The uniqueness/no-prefix test in
// signing_test.go asserts this list stays collision-free; a new signed document type
// is not usable until it is listed here.
var Tags = []string{
	TagUpdateManifest,
	TagDDILBundle,
	TagSecurityAdvisories,
	TagMemoryPortability,
}

// Signing/verification errors. These are integrity signals surfaced to the operator,
// never authorization controls.
var (
	// ErrNoKey is returned when verification is attempted with no usable key. Fail
	// closed: a payload is NEVER trusted without a key to check it against.
	ErrNoKey = errors.New("sigbundle: no verification key (this build embeds none; supply a public key)")
	// ErrBadSignature is returned when the detached signature does not verify against
	// the key over the domain-separated payload — a tampered payload, a wrong key, or
	// a signature minted for a different domain tag.
	ErrBadSignature = errors.New("sigbundle: signature does not verify against the key for this domain")
	// ErrUnknownTag is returned when a caller passes a tag not in the registry. A tag
	// we do not recognize is a programming error, not a runtime input.
	ErrUnknownTag = errors.New("sigbundle: domain tag is not registered")
)

// validTag reports whether tag is a registered domain tag.
func validTag(tag string) bool {
	for _, t := range Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// SigningInput returns the exact bytes a signature covers: the domain tag followed by
// the verbatim payload. Producers sign this; Verify verifies over it. Keeping it in one
// place means a generator and a verifier can never disagree on what was signed.
//
// It PANICS on an unregistered tag: the tag is always a compile-time constant chosen by
// the caller, never runtime input, so an unknown tag is a bug that must fail loudly
// rather than silently sign under an unseparated domain.
func SigningInput(tag string, payload []byte) []byte {
	if !validTag(tag) {
		panic("sigbundle: SigningInput with unregistered domain tag " + tag)
	}
	out := make([]byte, 0, len(tag)+len(payload))
	out = append(out, tag...)
	return append(out, payload...)
}

// Sign returns a detached Ed25519 signature over the domain-separated payload.
func Sign(tag string, payload []byte, priv ed25519.PrivateKey) []byte {
	return ed25519.Sign(priv, SigningInput(tag, payload))
}

// Verify authenticates a payload OFFLINE: it checks the detached signature over the
// domain-separated payload against pub. A nil/short key fails closed with ErrNoKey; a
// signature that does not verify (including one minted under a different tag) returns
// ErrBadSignature. This is the single trusted entry point every consumer calls before
// it parses anything.
func Verify(tag string, payload, sig []byte, pub ed25519.PublicKey) error {
	if !validTag(tag) {
		return ErrUnknownTag
	}
	if len(pub) != ed25519.PublicKeySize {
		return ErrNoKey
	}
	if len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	if !ed25519.Verify(pub, SigningInput(tag, payload), sig) {
		return ErrBadSignature
	}
	return nil
}

// TagPrefixCollision reports the first pair of registered tags where one is a prefix of
// the other (including equality after de-duplication), or "", "" if the registry is
// collision-free. It is exported so both the internal test and any downstream registry
// extension can assert disjointness.
func TagPrefixCollision(tags []string) (string, string) {
	for i := 0; i < len(tags); i++ {
		for j := 0; j < len(tags); j++ {
			if i == j {
				continue
			}
			if strings.HasPrefix(tags[j], tags[i]) {
				return tags[i], tags[j]
			}
		}
	}
	return "", ""
}
