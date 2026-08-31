// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import (
	"bytes"
	"testing"
)

// TestManifestSigningInputGolden pins the EXACT bytes an update-manifest signature
// covers. Refactored ManifestSigningInput to delegate to core/sigbundle; this
// test proves the output is byte-for-byte what the pre implementation produced —
// `"olivares.update-manifest.v1\n" || manifest`. If it ever changes, every already
// issued release signature stops verifying, so this test must never be "fixed" to
// match a new output: a red result means the refactor broke the wire, not the test.
func TestManifestSigningInputGolden(t *testing.T) {
	manifest := []byte(`{"schema_version":1,"channel":"stable","version":"26.7.0"}`)

	// The pre definition, inlined so this test is self-contained and does not
	// depend on the code under test to describe its own contract.
	wantTag := []byte("olivares.update-manifest.v1\n")
	want := append(append([]byte(nil), wantTag...), manifest...)

	got := ManifestSigningInput(manifest)
	if !bytes.Equal(got, want) {
		t.Fatalf("ManifestSigningInput changed:\n got: %q\nwant: %q", got, want)
	}

	// The signing input MUST begin with the domain tag and end with the verbatim
	// manifest, with nothing in between.
	if !bytes.HasPrefix(got, wantTag) {
		t.Fatalf("signing input does not begin with the domain tag")
	}
	if !bytes.Equal(got[len(wantTag):], manifest) {
		t.Fatalf("signing input tail is not the verbatim manifest")
	}

	// (conscious extension, not a blind re-pin): a manifest carrying the
	// license CRL signs the SAME way — tag || verbatim bytes — so the `revoked`
	// block is inside the signed message. Nothing may ever canonicalise,
	// re-order or strip it between signer and verifier: un-signing the CRL is
	// exactly the attack the OTA key domain exists to stop (design §5.2).
	withCRL := []byte(`{"schema_version":1,"channel":"stable","version":"26.7.0","revoked":{"serials":["OL-2026-000123"],"holder_ids":["org-acme"],"license_key_epoch":1767225600}}`)
	gotCRL := ManifestSigningInput(withCRL)
	wantCRL := append(append([]byte(nil), wantTag...), withCRL...)
	if !bytes.Equal(gotCRL, wantCRL) {
		t.Fatalf("ManifestSigningInput over a CRL-carrying manifest changed:\n got: %q\nwant: %q", gotCRL, wantCRL)
	}
	if !bytes.Contains(gotCRL, []byte(`"license_key_epoch":1767225600`)) {
		t.Fatalf("the revoked block is not inside the signed bytes")
	}
}
