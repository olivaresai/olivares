// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"os"
	"testing"
)

func writeTestFile(path string, b []byte) error { return os.WriteFile(path, b, 0o600) }

func TestValidVersionRejectsPathShapes(t *testing.T) {
	for _, ok := range []string{"2.1.261", "0.153.4", "1.0.13-beta.1", "1.2.3+build.5"} {
		if !ValidVersion(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "latest", "2.1", "../2.1.261", "2.1.261/", "2.1.261 ", "v2.1.261", "2.1.261..1", string(make([]byte, 70))} {
		if ValidVersion(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestParsePlatformAndVendorKeys(t *testing.T) {
	cases := map[string]Platform{
		"linux-x64":        {OS: "linux", Arch: "amd64", Libc: "glibc"},
		"linux-arm64-musl": {OS: "linux", Arch: "arm64", Libc: "musl"},
		"darwin-arm64":     {OS: "darwin", Arch: "arm64"},
		"win32-x64":        {OS: "windows", Arch: "amd64"},
	}
	for key, want := range cases {
		got, err := ParsePlatform(key)
		if err != nil || got != want {
			t.Errorf("ParsePlatform(%q) = %+v, %v; want %+v", key, got, err, want)
		}
	}
	for _, bad := range []string{"", "linux", "linux-x86", "darwin-x64-musl", "plan9-x64", "linux-x64-musl-extra"} {
		if _, err := ParsePlatform(bad); err == nil {
			t.Errorf("ParsePlatform(%q) accepted", bad)
		}
	}
	for key, want := range map[string]string{"linux-x64": "linux-x64", "linux-arm64-musl": "linux-arm64-musl"} {
		p, _ := ParsePlatform(key)
		got, err := claudePlatformKey(p)
		if err != nil || got != want {
			t.Errorf("claudePlatformKey(%s) = %q, %v", key, got, err)
		}
	}
	for _, key := range []string{"darwin-arm64", "win32-x64"} {
		p, _ := ParsePlatform(key)
		if _, err := claudePlatformKey(p); KindOf(err) != KindUnsupportedPlatform {
			t.Errorf("claudePlatformKey(%s) = %v, want unsupported_platform", key, err)
		}
	}
	host := HostPlatform()
	if host.OS == "linux" && host.Libc == "" {
		t.Error("host platform on linux must decide a libc")
	}
}

func TestPlanDigestBindsSelectionNotObservation(t *testing.T) {
	a := &Plan{Schema: PlanSchema, Driver: "claude", Version: "2.1.261", Artifact: Artifact{Name: "claude", SHA256: "aa", Size: 1}}
	b := *a
	b.Action, b.Existing, b.Observed = ActionNoop, &Observed{ReceiptPresent: true}, []string{"x"}
	if ComputeDigest(a) != ComputeDigest(&b) {
		t.Fatal("observation changed the digest")
	}
	c := *a
	c.Artifact.SHA256 = "bb"
	if ComputeDigest(a) == ComputeDigest(&c) {
		t.Fatal("a different artifact kept the digest")
	}
	d := *a
	d.Destination.Root = "/elsewhere"
	if ComputeDigest(a) == ComputeDigest(&d) {
		t.Fatal("a different destination kept the digest")
	}
	// Re-signing the same manifest is proof, not selection.
	e := *a
	e.Provenance.SignatureSHA256, e.Provenance.SigningKeyFingerprint, e.Provenance.Verifier = "ff", "sub", "openpgp/gpg (GnuPG) 9.9"
	if ComputeDigest(a) != ComputeDigest(&e) {
		t.Fatal("a fresh signature over the same manifest changed the digest")
	}
	g := *a
	g.Provenance.ManifestSHA256 = "different-manifest"
	h := *a
	h.Provenance.KeyFingerprint = "OTHERKEY"
	if ComputeDigest(a) == ComputeDigest(&g) || ComputeDigest(a) == ComputeDigest(&h) {
		t.Fatal("a different manifest or key kept the digest")
	}
	// The fixed Verified list is bound: an approval cannot claim more facts.
	v := *a
	v.Verified = []string{"manifest_signature", "everything"}
	if ComputeDigest(a) == ComputeDigest(&v) {
		t.Fatal("a different Verified list kept the digest")
	}
}
