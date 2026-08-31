// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
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

// cmd_license_crl_test.go exercises the CRL surface of `license verify`:
// profile/grace rendering, the OTA-signed manifest as the ONLY accepted CRL
// source, revoked precedence, and the honest "unavailable" path.

func writeCRLManifest(t *testing.T, dir string, rs *release.RevokedSet, otaPriv ed25519.PrivateKey) (manifestPath string) {
	t.Helper()
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       release.ChannelStable,
		Version:       "26.8.0",
		ReleasedAt:    time.Now().UTC().Add(-time.Hour),
		Rollout:       release.Rollout{},
		Artifacts: []release.Artifact{{
			OS: "linux", Arch: "amd64",
			Filename: "olivares_26.8.0_linux_amd64.tar.gz",
			SHA256:   strings.Repeat("a", 64),
		}},
		Revoked: rs,
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath = filepath.Join(dir, "stable-manifest.json")
	if err := os.WriteFile(manifestPath, mb, 0o644); err != nil {
		t.Fatal(err)
	}
	sig := release.SignManifest(mb, otaPriv)
	if err := os.WriteFile(manifestPath+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return manifestPath
}

func runLicenseVerify(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := licenseVerifyCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestLicenseVerifyCRLStates(t *testing.T) {
	dir := t.TempDir()
	licPub, licPriv, _ := ed25519.GenerateKey(nil)
	otaPub, otaPriv, _ := ed25519.GenerateKey(nil)

	blob, err := license.Sign(license.Claims{
		Licensee: "Acme", HolderID: "org-acme", Serial: "ser-1", Profile: "online",
		MaxUsers: 10, IssuedAt: time.Now().UTC().Add(-time.Hour),
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}, licPriv)
	if err != nil {
		t.Fatal(err)
	}
	licPubB64 := base64.StdEncoding.EncodeToString(licPub)
	otaPubB64 := base64.StdEncoding.EncodeToString(otaPub)

	// 1) No manifest: valid, CRL honestly unavailable.
	out, err := runLicenseVerify(t, blob, "--pubkey", licPubB64)
	if err != nil {
		t.Fatalf("verify without manifest: %v\n%s", err, out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if got["status"] != "valid" || got["profile"] != "online" {
		t.Fatalf("want valid/online, got %v/%v", got["status"], got["profile"])
	}
	if crl, ok := got["crl"].(string); !ok || !strings.Contains(crl, "unavailable") {
		t.Fatalf("without a manifest the CRL must be honestly unavailable, got %v", got["crl"])
	}

	// 2) OTA-signed manifest revoking the serial: revoked wins.
	manifest := writeCRLManifest(t, dir, &release.RevokedSet{Serials: []string{"ser-1"}}, otaPriv)
	out, err = runLicenseVerify(t, blob, "--pubkey", licPubB64, "--manifest", manifest, "--ota-pubkey", otaPubB64)
	if err != nil {
		t.Fatalf("verify with CRL: %v\n%s", err, out)
	}
	got = map[string]any{}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "revoked" {
		t.Fatalf("a CRL-listed serial must report revoked, got %v", got["status"])
	}

	// 3) A tampered manifest must be REFUSED as a CRL source, not degraded.
	mb, _ := os.ReadFile(manifest)
	if err := os.WriteFile(manifest, bytes.Replace(mb, []byte("ser-1"), []byte("ser-2"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = runLicenseVerify(t, blob, "--pubkey", licPubB64, "--manifest", manifest, "--ota-pubkey", otaPubB64); err == nil {
		t.Fatal("a manifest whose signature does not verify must be refused as a CRL source")
	}
}

// CHANGED BY and renamed for what it actually pins now.
//
// It used to say "expired 10 days ago: online (30d grace) => grace; trial (0) => expired",
// deriving the window from the PROFILE. The v8 canon signs one window, G = T+168h, granted
// by the issuer; the profile is a display label. So the axis is now whether the issuance
// attested a window — and, deliberately, the same profile appears on both sides of the
// table to prove the profile no longer decides anything. The clock offsets moved from
// 10 days to hours because 168h IS the window: an expiry 10 days old is past every legal
// grace, which is the case the last row keeps covering.
func TestLicenseVerifyGraceByAttestation(t *testing.T) {
	licPub, licPriv, _ := ed25519.GenerateKey(nil)
	licPubB64 := base64.StdEncoding.EncodeToString(licPub)

	for _, tc := range []struct {
		name          string
		profile, want string
		expiredAgo    time.Duration
		attestGrace   time.Duration // zero = the issuance attested none
	}{
		{name: "online inside an attested window", profile: "online", want: "grace", expiredAgo: 24 * time.Hour, attestGrace: license.MaxGracePeriod},
		{name: "airgapped inside an attested window", profile: "airgapped", want: "grace", expiredAgo: 24 * time.Hour, attestGrace: license.MaxGracePeriod},
		{name: "online with nothing attested", profile: "online", want: "expired", expiredAgo: 24 * time.Hour},
		{name: "trial with nothing attested", profile: "trial", want: "expired", expiredAgo: 24 * time.Hour},
		{name: "past even an attested window", profile: "online", want: "expired", expiredAgo: 10 * 24 * time.Hour, attestGrace: license.MaxGracePeriod},
	} {
		expires := time.Now().UTC().Add(-tc.expiredAgo)
		claims := license.Claims{
			Licensee: "Acme", Serial: "s", Profile: tc.profile,
			IssuedAt:  time.Now().UTC().Add(-100 * 24 * time.Hour),
			ExpiresAt: expires,
		}
		if tc.attestGrace > 0 {
			claims.GraceUntil = expires.Add(tc.attestGrace)
		}
		blob, err := license.Sign(claims, licPriv)
		if err != nil {
			t.Fatal(err)
		}
		out, err := runLicenseVerify(t, blob, "--pubkey", licPubB64)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if got["status"] != tc.want {
			t.Fatalf("%s: want %s, got %v", tc.name, tc.want, got["status"])
		}
		if tc.want == "grace" {
			if _, ok := got["grace_remaining"]; !ok {
				t.Fatalf("%s: a license in grace must report grace_remaining", tc.name)
			}
		}
	}
}
