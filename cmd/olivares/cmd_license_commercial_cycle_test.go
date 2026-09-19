// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/release"
)

// The engine half of the commercial cycle. The Worker journey (test/commercial-cycle-e2e.test.ts)
// issues a v3 blob and, when bin/olivares exists, runs these same commands against it.
// This file pins the CLI without the Worker: install and verify of a live-term license, and
// rejection only when an OTA-signed CRL names the serial.
//
// A named gap (core/license/license.go:46-50, cmd_license.go:939-945): a commercial
// refund does not populate this CRL. Without --manifest, verify reports valid/expired/grace
// and says the CRL is unavailable. That is the product rule (commercial_crl_scope: forbidden),
// not a missing if-statement.

func TestLicenseInstallAndVerifyLiveTermThenCRLRevokes(t *testing.T) {
	if !license.HasDevKey {
		t.Skip("this build ships no dev signing key")
	}
	dir := t.TempDir()
	expires := time.Now().UTC().Add(30 * 24 * time.Hour)
	blob := signTestLicence(t, "Cycle Buyer", expires)

	out, err := installLicence(t, blob, "--data-dir", dir)
	if err != nil {
		t.Fatalf("install of a live-term license: %v\n%s", err, out)
	}
	var installed map[string]any
	if jerr := json.Unmarshal([]byte(firstJSONObject(out)), &installed); jerr != nil {
		t.Fatalf("install output is not JSON: %v\n%s", jerr, out)
	}
	if installed["status"] != "valid" {
		t.Fatalf("install status = %v, want valid", installed["status"])
	}

	verified, verr := runLicenseVerify(t, blob)
	if verr != nil {
		t.Fatalf("verify: %v\n%s", verr, verified)
	}
	var got map[string]any
	if jerr := json.Unmarshal([]byte(firstJSONObject(verified)), &got); jerr != nil {
		t.Fatalf("verify output is not JSON: %v\n%s", jerr, verified)
	}
	if got["status"] != "valid" {
		t.Fatalf("verify status = %v, want valid", got["status"])
	}
	if crl, ok := got["crl"].(string); !ok || !strings.Contains(crl, "unavailable") {
		t.Fatalf("without a manifest the CRL must be honestly unavailable, got %v", got["crl"])
	}

	// The same gap, from the other side: a commercial ending is not inferred, so the
	// same blob stays valid.
	verified2, verr2 := runLicenseVerify(t, blob)
	if verr2 != nil {
		t.Fatalf("verify after a hypothetical refund still has to succeed: %v\n%s", verr2, verified2)
	}
	got = map[string]any{}
	if jerr := json.Unmarshal([]byte(firstJSONObject(verified2)), &got); jerr != nil {
		t.Fatal(jerr)
	}
	if got["status"] == "revoked" {
		t.Fatal("verify must not report revoked unless a caller-supplied OTA CRL names the serial")
	}

	otaPub, otaPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := license.Verify(blob, license.DefaultPublicKey())
	if err != nil {
		t.Fatalf("parse the blob we just signed: %v", err)
	}
	if claims.Serial == "" {
		// signTestLicence currently omits Serial. Pin the CRL path with an explicit serial.
		blob, err = license.Sign(license.Claims{
			Licensee: "Cycle Buyer", Plan: "business", Serial: "cycle-ser-1",
			HolderID: "sub_d13", IssuedAt: time.Now().UTC().Add(-time.Hour), ExpiresAt: expires,
		}, license.DevPrivateKey())
		if err != nil {
			t.Fatal(err)
		}
		claims, err = license.Verify(blob, license.DefaultPublicKey())
		if err != nil {
			t.Fatal(err)
		}
	}
	manifest := writeCRLManifest(t, dir, &release.RevokedSet{Serials: []string{claims.Serial}}, otaPriv)
	out, err = runLicenseVerify(t, blob, "--manifest", manifest, "--ota-pubkey",
		base64.StdEncoding.EncodeToString(otaPub))
	if err != nil {
		t.Fatalf("verify with CRL: %v\n%s", err, out)
	}
	got = map[string]any{}
	if jerr := json.Unmarshal([]byte(firstJSONObject(out)), &got); jerr != nil {
		t.Fatal(jerr)
	}
	if got["status"] != "revoked" {
		t.Fatalf("a CRL-listed serial must report revoked, got %v", got["status"])
	}
}

func TestLicenseVerifyV3RevokedOnlyWhenSerialIsOnOTAManifest(t *testing.T) {
	pub := license.DefaultPublicKey()
	if len(pub) == 0 {
		t.Skip("this build embeds no verification key")
	}
	blob := signTestCredentialV3(t)
	lic, err := license.VerifyEnvelope(blob, pub)
	if err != nil {
		t.Fatalf("VerifyEnvelope: %v", err)
	}
	now := insideTheBaseTerm
	if lic.Status(now) != license.StatusValid {
		t.Fatalf("status inside the vector term = %q, want valid", lic.Status(now))
	}
	if lic.StatusWithRevocation(now, license.Revocation{}) == license.StatusRevoked {
		t.Fatal("an empty revocation snapshot must not revoke a paying credential")
	}
	if lic.StatusWithRevocation(now, license.Revocation{Serials: []string{lic.Serial()}}) != license.StatusRevoked {
		t.Fatalf("a snapshot that names this serial must report revoked, serial=%q", lic.Serial())
	}
}

func TestLicenseInstallRefusesABlobThisBuildDoesNotTrust(t *testing.T) {
	dir := t.TempDir()
	otherPub, otherPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := license.Sign(license.Claims{
		Licensee: "Stranger", IssuedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}, otherPriv)
	if err != nil {
		t.Fatal(err)
	}
	out, ierr := installLicence(t, blob, "--data-dir", dir)
	if ierr == nil {
		t.Fatalf("install must refuse a blob signed by a key this build does not trust\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, licenseFileName)); err == nil {
		t.Fatal("a refused install must not leave license.key")
	}
	_ = otherPub
}

// firstJSONObject returns the first top-level JSON object in s. `license install`
// writes JSON to stdout and a reload hint to stderr; tests that share one buffer
// must not treat the hint as part of the document.
func firstJSONObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return s
	}
	return s[start : end+1]
}
