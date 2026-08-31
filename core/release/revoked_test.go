// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// revoked_test.go covers the license CRL the channel manifest carries (D4=D, design §5.2): structural bounds, policy levers, and — the part that
// matters most — that the `revoked` block lives INSIDE the OTA-signed bytes, so
// neither a serial flip nor a whole-block strip survives verification, and the
// license signing key cannot mint a CRL manifest at all.

func crlManifest() Manifest {
	m := goodManifest()
	m.Revoked = &RevokedSet{
		Serials:         []string{"OL-2026-000123", "OL-2026-000124"},
		HolderIDs:       []string{"org-acme"},
		LicenseKeyEpoch: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
	}
	return m
}

func TestManifestWithoutRevokedStaysValid(t *testing.T) {
	// Compat: a manifest published with no CRL (every pre manifest, and
	// every channel with nothing revoked) parses and reports an empty set.
	mb, err := json.Marshal(goodManifest())
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(mb)
	if err != nil {
		t.Fatalf("a manifest without revoked must stay valid: %v", err)
	}
	if !m.Revoked.Empty() {
		t.Fatalf("absent revoked must read as the empty set")
	}
}

func TestManifestRevokedRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mb, sig := signManifestBytes(t, crlManifest(), priv)
	m, err := VerifyManifest(mb, sig, pub)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if m.Revoked.Empty() || len(m.Revoked.Serials) != 2 || len(m.Revoked.HolderIDs) != 1 {
		t.Fatalf("CRL did not survive the round trip: %+v", m.Revoked)
	}
	if m.Revoked.LicenseKeyEpoch != time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("epoch did not survive the round trip: %d", m.Revoked.LicenseKeyEpoch)
	}
}

func TestParseManifestRejectsBadRevoked(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Manifest)
		want string
	}{
		{"empty serial", func(m *Manifest) { m.Revoked = &RevokedSet{Serials: []string{""}} }, "is empty"},
		{"padded serial", func(m *Manifest) { m.Revoked = &RevokedSet{Serials: []string{" OL-1 "}} }, "whitespace"},
		{"control chars", func(m *Manifest) { m.Revoked = &RevokedSet{HolderIDs: []string{"org\x1b[2Jacme"}} }, "control characters"},
		{"duplicate", func(m *Manifest) { m.Revoked = &RevokedSet{Serials: []string{"OL-1", "OL-1"}} }, "listed twice"},
		{"negative epoch", func(m *Manifest) { m.Revoked = &RevokedSet{LicenseKeyEpoch: -1} }, "negative"},
		{"over the ceiling", func(m *Manifest) {
			s := make([]string, maxRevokedEntries+1)
			for i := range s {
				s[i] = "OL-" + strings.Repeat("0", 3) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))
			}
			m.Revoked = &RevokedSet{Serials: s}
		}, "re-evaluate the design"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := goodManifest()
			tc.mut(&m)
			mb, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, perr := ParseManifest(mb); perr == nil || !strings.Contains(perr.Error(), tc.want) {
				t.Fatalf("want refusal containing %q, got %v", tc.want, perr)
			}
		})
	}
}

func TestCheckPolicyRevokedLevers(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	base := func() Manifest {
		m := crlManifest()
		m.ReleasedAt = now
		exp := now.Add(2160 * time.Hour)
		m.Expires = &exp
		return m
	}

	// A past epoch is legitimate (the compromise fence) but must be ON THE RECORD.
	m := base()
	warnings, err := m.CheckPolicy(now, DefaultPolicyBounds())
	if err != nil {
		t.Fatalf("a past epoch is a legitimate rotation response: %v", err)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "license_key_epoch") || !strings.Contains(joined, "revokes 3 license(s)/holder(s)") {
		t.Fatalf("revocation levers must surface as warnings, got:\n%s", joined)
	}

	// A FUTURE epoch would kill licenses the worker is legitimately issuing now.
	m = base()
	m.Revoked.LicenseKeyEpoch = now.Add(48 * time.Hour).Unix()
	if _, err := m.CheckPolicy(now, DefaultPolicyBounds()); err == nil ||
		!strings.Contains(err.Error(), "license_key_epoch") {
		t.Fatalf("a future epoch must be refused, got %v", err)
	}

	// No CRL → no revocation warnings.
	m = base()
	m.Revoked = nil
	warnings, err = m.CheckPolicy(now, DefaultPolicyBounds())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(warnings, "\n"), "revoke") {
		t.Fatalf("a manifest with no CRL must not warn about revocations")
	}
}

func TestPolicySummaryFlagsRevoked(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	got := map[string]PolicyField{}
	for _, f := range crlManifest().PolicySummary(now) {
		got[f.Name] = f
	}
	fld, ok := got["revoked"]
	if !ok || !fld.Alert {
		t.Fatalf("a CRL-carrying manifest must flag `revoked` for the custodian, got %+v", fld)
	}
	if !strings.Contains(fld.Value, "2 serial(s), 1 holder(s)") || !strings.Contains(fld.Value, "license_key_epoch") {
		t.Fatalf("the custodian must see counts and epoch, got %q", fld.Value)
	}
	for _, f := range goodManifest().PolicySummary(now) {
		if f.Name == "revoked" && (f.Alert || f.Value != "none") {
			t.Fatalf("an absent CRL must render as unalarmed \"none\", got %+v", f)
		}
	}
}

// TestRevokedIsSignatureBound is the CRL-tamper regression: a serial flip and a
// whole-block strip must both invalidate the OTA signature, because the
// signature covers the verbatim manifest bytes and the CRL lives inside them.
func TestRevokedIsSignatureBound(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mb, sig := signManifestBytes(t, crlManifest(), priv)

	flipped := bytes.Replace(mb, []byte("OL-2026-000123"), []byte("OL-2026-999999"), 1)
	if !bytes.Contains(mb, []byte("OL-2026-000123")) || bytes.Equal(flipped, mb) {
		t.Fatal("test setup: serial not present to flip")
	}
	if _, err := VerifyManifest(flipped, sig, pub); err == nil {
		t.Fatal("a flipped serial must invalidate the signature")
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "revoked")
	stripped, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyManifest(stripped, sig, pub); err == nil {
		t.Fatal("stripping the whole CRL must invalidate the signature — un-revoking by omission is the attack")
	}
}

// TestLicenseKeyCannotMintCRLManifest closes the pack's adversarial vector
// head-on: a compromised LICENSE key signing a hostile CRL manifest (e.g. one
// that un-revokes itself, or revokes competitors) must never verify as an OTA
// manifest. License signatures cover RAW payload bytes (license.Sign has no
// domain tag); the OTA manifest verifies under an independent keypair AND a
// domain-tagged signing input (key-domain split) — both walls must hold.
func TestLicenseKeyCannotMintCRLManifest(t *testing.T) {
	otaPub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, licPriv, _ := ed25519.GenerateKey(rand.Reader)

	mb, err := json.Marshal(crlManifest())
	if err != nil {
		t.Fatal(err)
	}
	// The attacker signs the manifest bytes the way the license key signs
	// anything at all: raw, no domain tag (license.Sign's shape).
	hostileRaw := ed25519.Sign(licPriv, mb)
	if _, err := VerifyManifest(mb, hostileRaw, otaPub); err == nil {
		t.Fatal("a raw license-key signature must never verify a CRL manifest")
	}
	// Even constructing the CORRECT domain-tagged input with the WRONG
	// (license) key fails: the anchors are independent keypairs.
	hostileTagged := ed25519.Sign(licPriv, ManifestSigningInput(mb))
	if _, err := VerifyManifest(mb, hostileTagged, otaPub); err == nil {
		t.Fatal("the license keypair must not verify under the OTA anchor")
	}
}
