// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// THE ISSUING ROUTE'S OWN CREDENTIAL, SIGNED, THROUGH THE ENTRY POINT A CUSTOMER REACHES.
//
// ============ WHY THIS EXISTS WHEN TWO CONFORMANCE TESTS WERE ALREADY GREEN =================
// credential_v3_route_conformance_test.go hands `ParseCredentialV3` a payload with NO SIGNATURE
// and proves the structural parser accepts what this route issues. That is a real property and
// it leaves a hole in the direction that matters: A GO SIDE THAT NEVER CHECKED A SIGNATURE AT
// ALL WOULD PASS IT EXACTLY AS WELL, because it is never given one. Its tamper control blanks a
// FIELD, which exercises the parser's validation and says nothing about the envelope.
//
// So this file starts from a blob the TypeScript issuer SIGNED and runs it through
// `VerifyEnvelope` — the function `cmd/olivares` and `core/api` actually call — and asserts the
// two halves that had never been exercised together: the signature opens, AND the container
// routes to v3 instead of the legacy flat claim set.
//
// It was a THROWAWAY PROBE FIRST. On 2026-08-11 the issuing route was reported as blocked
// because production `Verify` answered `license: malformed` for every v3 payload and
// `ParseCredentialV3` had zero production callers. After that was closed, the check that the
// route's credential now survives the real path was run in a scratch worktree and thrown away.
// A measurement nobody can re-run is a story; this is the same measurement, committed.
//
// ============ THE TWO NEGATIVE HALVES, AND WHY NEITHER ALONE IS ENOUGH ======================
//   flipped signature bit   proves the signature is CHECKED. Without it, a VerifyEnvelope that
//                           decoded and never verified would pass the positive case.
//   legacy Verify refuses   proves the containers are SEPARATED. Without it, a flat parser that
//                           had grown lenient about `licensee` would read as success — the
//                           gap would be closed on paper and open in fact.
//
// ============ WHERE THE ARTIFACT COMES FROM, STATED SO THE GREEN IS NOT READ AS MORE ========
// The blob's CONTENT is the route's: the entity id the provider gave, the deployment id derived
// because the purchase creates deployment #1, the purpose, the refund-window phase and its two
// deadlines all come from `issuanceFor`, not from a hand-written context. Its CLOCK is pinned,
// because a vector built from the wall clock changes every run and pins nothing — the wall-clock
// path is covered on the TypeScript side, which drives the real router and asserts its output
// differs from this artifact only in the instants a clock decides.
//
// Regenerate deliberately, never automatically:
//
//	cd commercial/license-worker && OLIVARES_WRITE_V3_ROUTE_VECTOR=1 node --test test/dodo-issue.test.ts
//
// which leaves THIS test red until it is re-run. That order is the point: a vector that can
// bless itself agrees with any change, including a wrong one.

package license

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	v3RouteSignedBlobPath = "testdata/credential_v3_route_signed_blob.txt"
	v3RouteWireFixture    = "testdata/wireformat_vectors.json"
)

// routeBlobAndKey reads the committed signed blob and the public key of the fixture keypair the
// issuer signs test artifacts with.
func routeBlobAndKey(t *testing.T) (string, ed25519.PublicKey) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(v3RouteSignedBlobPath))
	if err != nil {
		t.Fatalf("the route's signed blob is missing (%v).\n"+
			"Regenerate it from the issuer:\n"+
			"  cd commercial/license-worker && OLIVARES_WRITE_V3_ROUTE_VECTOR=1 node --test test/dodo-issue.test.ts", err)
	}
	fixture, err := os.ReadFile(filepath.Clean(v3RouteWireFixture))
	if err != nil {
		t.Fatalf("wire fixture: %v", err)
	}
	var fx struct {
		PublicKeyB64 string `json:"public_key_b64"`
	}
	if err := json.Unmarshal(fixture, &fx); err != nil {
		t.Fatalf("wire fixture: %v", err)
	}
	pub, err := base64.StdEncoding.DecodeString(fx.PublicKeyB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("wire fixture public key is unusable: %v (%d bytes)", err, len(pub))
	}
	return strings.TrimSpace(string(raw)), ed25519.PublicKey(pub)
}

func TestRouteSignedCredentialOpensThroughVerifyEnvelope(t *testing.T) {
	blob, pub := routeBlobAndKey(t)

	v, err := VerifyEnvelope(blob, pub)
	if err != nil {
		t.Fatalf("VerifyEnvelope REFUSES the signed credential this route issues: %v\n"+
			"A paying customer would receive a license their engine cannot open.", err)
	}
	if !v.IsCredentialV3() {
		t.Fatalf("the envelope routed to %q, not to the v3 container", v.Container)
	}
	if len(v.Grants()) < 2 {
		t.Errorf("grants = %d, want the base and at least one add-on: the aggregate is the whole "+
			"reason v3 exists", len(v.Grants()))
	}
	if v.Licensee() == "" || v.Serial() == "" {
		t.Errorf("licensee %q / serial %q: both are read back by support and must survive the trip",
			v.Licensee(), v.Serial())
	}
}

// THE CREDENTIAL NAMES THE KEY THAT SIGNED IT, and this is the half no hand-written vector can
// carry. `key_id` is DERIVED by the issuer from its own public key rather than configured, so a
// verifier holding the key can recompute it — a configured id is a string that can name a key
// nobody ever checked it against, which is how a rotation that updates the secret and forgets
// the label mints credentials pointing at the previous key.
//
// Recomputed here from the key that actually OPENED the signature, so the assertion is about the
// two agreeing rather than about the issuer agreeing with itself.
func TestRouteSignedCredentialNamesTheKeyThatSignedIt(t *testing.T) {
	blob, pub := routeBlobAndKey(t)

	payload, _, ok := strings.Cut(blob, ".")
	if !ok {
		t.Fatal("the committed blob is not payload.signature")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	var w struct {
		KeyID string `json:"key_id"`
	}
	if err := json.Unmarshal(decoded, &w); err != nil {
		t.Fatalf("payload: %v", err)
	}

	digest := sha256.Sum256(pub)
	want := "sha256:" + hex.EncodeToString(digest[:])
	if w.KeyID != want {
		t.Errorf("key_id = %q, want %q — the credential does not name the key that signed it", w.KeyID, want)
	}
}

// NEGATIVE 1: the signature is CHECKED. Without this, a VerifyEnvelope that decoded the payload
// and never verified would pass the positive case above and nothing would notice.
func TestRouteSignedCredentialRejectsATamperedSignature(t *testing.T) {
	blob, pub := routeBlobAndKey(t)

	payloadB64, sigB64, ok := strings.Cut(blob, ".")
	if !ok {
		t.Fatal("the committed blob is not payload.signature")
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("signature: %v", err)
	}
	// ONE BIT, in the last byte. A wholesale replacement would also be caught by a length check
	// or a decode error, which is a weaker statement than "the Ed25519 verification runs".
	sig[len(sig)-1] ^= 0x01
	tampered := payloadB64 + "." + base64.RawURLEncoding.EncodeToString(sig)
	if tampered == blob {
		t.Fatal("the mutation did not apply; this control proves nothing")
	}

	if _, err := VerifyEnvelope(tampered, pub); err == nil {
		t.Fatal("a credential with ONE flipped signature bit was accepted")
	} else if !errors.Is(err, ErrBadSignature) {
		// Refused either way, but the REASON matters: a malformed-shape refusal would mean the
		// bit flip broke the encoding rather than failing verification, and the control would be
		// measuring the base64 decoder.
		t.Errorf("refused with %v, want ErrBadSignature — the control must fail at VERIFICATION", err)
	}
}

// NEGATIVE 2: the containers are SEPARATED. The legacy flat parser must still refuse a v3
// payload. If it ever accepts one, the dispatch is not what makes this work — a lenient flat
// parser is, and the same green would be reported for a broken engine.
func TestLegacyVerifyStillRefusesTheRouteCredential(t *testing.T) {
	blob, pub := routeBlobAndKey(t)

	_, err := Verify(blob, pub)
	if err == nil {
		t.Fatal("the LEGACY flat Verify accepted a v3 credential; the two containers are not separated")
	}
	// The NAMED refusal, not merely "an error". `Verify` is now a thin wrapper that opens the
	// envelope and refuses a non-flat container with ErrCredentialV3, so a generic error here
	// would mean something else broke — a bad key, an unreadable blob — and this control would be
	// green while measuring nothing about the separation it exists to pin.
	if !errors.Is(err, ErrCredentialV3) {
		t.Errorf("refused with %v, want ErrCredentialV3 — the refusal must be the CONTAINER check", err)
	}
}
