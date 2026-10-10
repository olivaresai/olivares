// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise || !enterprise_negotiated

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommunityBundleInstallRefusesBeforeReadingLicenseOrBundle(t *testing.T) {
	for _, enterprise := range []bool{false, true} {
		t.Run(map[bool]string{false: "bundle", true: "enterprise_bundle"}[enterprise], func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "olivares")
			original := []byte("unchanged target")
			if err := os.WriteFile(target, original, 0700); err != nil {
				t.Fatal(err)
			}
			args := []string{"--bundle", filepath.Join(dir, "missing-bundle"), "--license", dir, "--data-dir", dir, "--target", target, "--yes"}
			if enterprise {
				args = append(args, "--enterprise")
			}
			_, err := runUpgradeCmd(t, args...)
			if err == nil || !strings.Contains(err.Error(), "Enterprise") || !strings.Contains(err.Error(), "--bundle --check") {
				t.Fatalf("Community must name the edition and verification carve-out before reading the license or bundle: %v", err)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != string(original) {
				t.Fatalf("refusal changed target: %q, %v", got, err)
			}
			if matches, err := filepath.Glob(target + ".bak-*"); err != nil || len(matches) != 0 {
				t.Fatalf("refusal created backup: %v, %v", matches, err)
			}
		})
	}
}

func TestCommunityBundleCheckVerifiesWithoutLicense(t *testing.T) {
	v1 := buildStub(t, "1.1")
	f := newUpdFixture(t, "1.2", "1.0", buildStub(t, "1.2"))
	bundle := f.writeBundleDeclaring(t, "community", true)
	target := writeTarget(t, v1)
	// A directory cannot be read as a license. --check must not even try.
	out, err := runUpgradeCmd(t, "--bundle", bundle, "--check", "--license", t.TempDir(), "--pubkey", f.pubB64, "--data-dir", t.TempDir(), "--target", target, "--os", "linux", "--arch", "amd64")
	if err != nil || !strings.Contains(out, "upgrade available") {
		t.Fatalf("Community verification: %v\n%s", err, out)
	}
	if strings.Contains(out, "Re-run without --check to install") || !strings.Contains(out, "Enterprise") {
		t.Fatalf("verification suggests an unavailable install: %s", out)
	}
	if got := runsVersion(t, target); !strings.Contains(got, "1.1") {
		t.Fatalf("--check changed target: %s", got)
	}
	if matches, err := filepath.Glob(target + ".bak-*"); err != nil || len(matches) != 0 {
		t.Fatalf("--check created backup: %v, %v", matches, err)
	}
}

func TestCommunitySignedBundleStillRefusesInstall(t *testing.T) {
	old := buildStub(t, "1.1")
	f := newUpdFixture(t, "1.2", "1.0", buildStub(t, "1.2"))
	bundle := f.writeBundleDeclaring(t, "community", true)
	for _, liveLicense := range []bool{false, true} {
		t.Run(map[bool]string{false: "without_license", true: "live_license"}[liveLicense], func(t *testing.T) {
			data := t.TempDir()
			if liveLicense {
				installDevLicense(t, data)
			}
			target := writeTarget(t, old)
			_, err := runUpgradeCmd(t, "--bundle", bundle, "--pubkey", f.pubB64, "--data-dir", data, "--target", target, "--yes", "--os", "linux", "--arch", "amd64")
			if err == nil || !strings.Contains(err.Error(), "Enterprise") {
				t.Fatalf("signed Community bundle installed: %v", err)
			}
			if got := runsVersion(t, target); !strings.Contains(got, "1.1") {
				t.Fatalf("refusal changed target: %s", got)
			}
			if matches, err := filepath.Glob(target + ".bak-*"); err != nil || len(matches) != 0 {
				t.Fatalf("refusal created backup: %v, %v", matches, err)
			}
		})
	}
}

func TestCommunityBundleCheckRejectsUnsignedEditionClaim(t *testing.T) {
	f := newUpdFixture(t, "1.2", "1.0", buildStub(t, "1.2"))
	target := writeTarget(t, buildStub(t, "1.1"))
	_, err := runUpgradeCmd(t, "--bundle", f.writeBundleDeclaring(t, "community", false), "--check", "--license", t.TempDir(), "--pubkey", f.pubB64, "--data-dir", t.TempDir(), "--target", target, "--os", "linux", "--arch", "amd64")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "signature") {
		t.Fatalf("--check accepted unsigned manifest: %v", err)
	}
	if got := runsVersion(t, target); !strings.Contains(got, "1.1") {
		t.Fatalf("--check changed target: %s", got)
	}
}

func TestCommunityOfflineMirrorHasEditionRefusal(t *testing.T) {
	c := newReleaseExportMirrorCmd()
	out := filepath.Join(t.TempDir(), "mirror")
	c.SetArgs([]string{"--endpoint", "http://127.0.0.1:1", "--token", "fixture-not-a-secret", "--set", "biz", "--out", out})
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "Enterprise") {
		t.Fatalf("mirror did not refuse edition: %v", err)
	}
	if !c.Hidden {
		t.Fatal("Community help exposes offline mirror")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("mirror created output: %v", err)
	}
}
