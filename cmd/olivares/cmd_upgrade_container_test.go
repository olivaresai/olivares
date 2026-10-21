// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// realInContainer is the production container test, kept before any test stands another in.
var realInContainer = inContainer

// containerUpgrade runs `olivares upgrade` as the image binary against f, into dataDir.
func containerUpgrade(t *testing.T, f *updFixture, dataDir string, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{"--endpoint", f.server.URL, "--pubkey", f.pubB64,
		"--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir}, extra...)
	return runUpgradeCmd(t, args...)
}

// startsApplied replays what a recreated container does: the image binary hands over, and the
// path it would exec is returned ("" when it would keep running).
func startsApplied(t *testing.T, f *updFixture, dataDir, image string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(f.pubB64)
	if err != nil {
		t.Fatal(err)
	}
	var execed string
	var warn strings.Builder
	if err := handOverToApplied(dataDir, image, ed25519.PublicKey(raw), io.Discard, &warn, func(p string) error {
		execed = p
		return nil
	}); err != nil {
		t.Fatalf("hand-over: %v", err)
	}
	if execed == "" {
		t.Logf("hand-over kept the image: %s", warn.String())
	}
	return execed
}

// asContainerImage makes this process look like an image binary at imagePath, stamped ver,
// running in a container whose data volume is dataDir.
func asContainerImage(t *testing.T, imagePath, ver string) {
	t.Helper()
	isolateTemp(t) // the container's temporary directory: where proofs of the applied binary are kept
	oldBin, oldIn, oldVer := runningBinary, inContainer, version
	runningBinary = func() (string, error) { return imagePath, nil }
	inContainer = func() bool { return true }
	version = ver
	t.Cleanup(func() { runningBinary, inContainer, version = oldBin, oldIn, oldVer })
}

// TestUpgradeInContainerLeavesTheImageAlone is the reproducer for the Business container
// that lost its OTA-applied binary on `docker compose up -d --force-recreate`: the upgrade
// replaced /usr/local/bin/olivares, a file of the image layer, and a recreate brings the
// image's file back. In a container the image is immutable and the data volume is not, so
// the upgrade must install into the volume and leave the image binary alone.
func TestUpgradeInContainerLeavesTheImageAlone(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1 := buildStub(t, "26.700")
	v2 := buildStub(t, "26.800")
	f := newUpdFixture(t, "26.800", "26.600", v2)
	image := writeTarget(t, v1)
	dataDir := execCapableDir(t)
	asContainerImage(t, image, "26.700")

	if _, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
		"--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir); err != nil {
		t.Fatalf("upgrade in a container: %v", err)
	}
	if got := runsVersion(t, image); !strings.Contains(got, "26.700") {
		t.Fatalf("the image binary was replaced (%q): a recreate brings the old one back and the upgrade is lost", got)
	}
	applied := filepath.Join(dataDir, "ota", "olivares")
	if got := runsVersion(t, applied); !strings.Contains(got, "26.800") {
		t.Fatalf("the data volume holds %q at %s, want the applied 26.800", got, applied)
	}
	// The write side and the read side meet here: the recreated container's image binary must
	// accept the evidence this very upgrade stored and start the applied release.
	if got := startsApplied(t, f, dataDir, image); got != applied {
		t.Fatalf("after a recreate the image binary would exec %q, want %q", got, applied)
	}
}

func TestUpgradeInContainerAppliesOverAnExistingRelease(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1, v2, v3 := buildStub(t, "26.700"), buildStub(t, "26.800"), buildStub(t, "26.900")
	image := writeTarget(t, v1)
	dataDir := execCapableDir(t)
	asContainerImage(t, image, "26.700")
	applied := filepath.Join(dataDir, "ota", "olivares")

	_, priv, _ := ed25519.GenerateKey(nil) // one deployment, one OTA key
	f2 := newUpdFixtureSignedBy(t, priv, "26.800", "26.600", v2)
	if _, err := containerUpgrade(t, f2, dataDir); err != nil {
		t.Fatalf("first upgrade: %v", err)
	}
	f3 := newUpdFixtureSignedBy(t, priv, "26.900", "26.600", v3)
	if _, err := containerUpgrade(t, f3, dataDir); err != nil {
		t.Fatalf("second upgrade: %v", err)
	}
	if got := runsVersion(t, image); !strings.Contains(got, "26.700") {
		t.Fatalf("the image binary changed: %q", got)
	}
	if got := runsVersion(t, applied); !strings.Contains(got, "26.900") {
		t.Fatalf("the volume runs %q, want 26.900", got)
	}
	if got := startsApplied(t, f3, dataDir, image); got != applied {
		t.Fatalf("a recreate would exec %q, want the second release at %s", got, applied)
	}
	// No backup stays: the next start rebuilds the slot from the evidence, so a backup is no
	// rollback, and each upgrade would otherwise leave one more full binary in the volume.
	if backups, _ := filepath.Glob(applied + ".bak-*"); len(backups) != 0 {
		t.Fatalf("backups = %v, want none", backups)
	}
	// What the volume holds is private to the engine's uid; only the binary is executable.
	artifacts, _ := filepath.Glob(filepath.Join(dataDir, "ota", "artifact-*"))
	want := map[string]os.FileMode{
		filepath.Join(dataDir, "ota"): 0o700,
		applied:                       0o755,
		filepath.Join(dataDir, "ota", "release.json"): 0o600,
	}
	for _, a := range artifacts {
		want[a] = 0o600
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifacts = %v, want only the current release's", artifacts)
	}
	for path, mode := range want {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != mode {
			t.Fatalf("%s: mode %v (%v), want %v", path, fi.Mode().Perm(), err, mode)
		}
	}
}

func TestUpgradeInContainerIgnoresAStaleAppliedBinary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v0, v1, v2 := buildStub(t, "26.600"), buildStub(t, "26.700"), buildStub(t, "26.800")
	image := writeTarget(t, v1)
	dataDir := execCapableDir(t)
	asContainerImage(t, image, "26.700")
	applied := filepath.Join(dataDir, "ota", "olivares")
	if err := os.MkdirAll(filepath.Dir(applied), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(applied, v0, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newUpdFixture(t, "26.800", "26.600", v2)
	out, err := containerUpgrade(t, f, dataDir)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if !strings.Contains(out, "current:   26.700") {
		t.Fatalf("the guards were decided about the stale applied binary, not the newer image:\n%s", out)
	}
	if got := runsVersion(t, applied); !strings.Contains(got, "26.800") {
		t.Fatalf("the slot runs %q, want the applied 26.800 over the stale 26.600", got)
	}
	if backups, _ := filepath.Glob(applied + ".bak-*"); len(backups) != 0 {
		t.Fatalf("backups = %v, want none (a copy of the image is no rollback)", backups)
	}
}

func TestUpgradeInContainerCheckAndTargetLeaveTheVolumeAlone(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1, v2 := buildStub(t, "26.700"), buildStub(t, "26.800")
	image := writeTarget(t, v1)
	dataDir := execCapableDir(t)
	asContainerImage(t, image, "26.700")
	f := newUpdFixture(t, "26.800", "26.600", v2)

	if _, err := containerUpgrade(t, f, dataDir, "--check"); err != nil {
		t.Fatalf("--check: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "ota")); err == nil {
		t.Fatal("--check wrote into the data volume")
	}
	explicit := writeTarget(t, v1)
	if _, err := containerUpgrade(t, f, dataDir, "--target", explicit); err != nil {
		t.Fatalf("--target: %v", err)
	}
	if got := runsVersion(t, explicit); !strings.Contains(got, "26.800") {
		t.Fatalf("an explicit --target must still be upgraded in place: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "ota")); err == nil {
		t.Fatal("an explicit --target used the data volume")
	}
}

func TestUpgradeInContainerReportsMissingEvidence(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1, v2 := buildStub(t, "26.700"), buildStub(t, "26.800")
	image := writeTarget(t, v1)
	dataDir := execCapableDir(t)
	asContainerImage(t, image, "26.700")
	// A directory where release.json belongs makes the evidence unwritable.
	if err := os.MkdirAll(filepath.Join(dataDir, "ota", "release.json", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	f := newUpdFixture(t, "26.800", "26.600", v2)
	_, err := containerUpgrade(t, f, dataDir)
	if err == nil || !strings.Contains(err.Error(), "the next start runs the release applied before it, or the image if there was none") {
		t.Fatalf("err = %v, want the upgrade to say which binary the next start runs", err)
	}
	if got := runsVersion(t, image); !strings.Contains(got, "26.700") {
		t.Fatalf("the image binary changed: %q", got)
	}
}

// A failed download must not leave a copy of the image in the volume: the slot is seeded only
// once the artifact is in hand.
func TestUpgradeInContainerLeavesNoCopyWhenTheDownloadFails(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1, v2 := buildStub(t, "26.700"), buildStub(t, "26.800")
	image := writeTarget(t, v1)
	dataDir := execCapableDir(t)
	asContainerImage(t, image, "26.700")
	f := newUpdFixture(t, "26.800", "26.600", v2)
	f.corrupt = true

	if _, err := containerUpgrade(t, f, dataDir); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("err = %v, want a byte-flipped artifact refused by its digest", err)
	}
	// The upgrade lock is a persistent file next to its target, here and beside any binary; the
	// copy of the image and the evidence of a release are what must not be left.
	all, _ := filepath.Glob(filepath.Join(dataDir, "ota", "*"))
	var left []string
	for _, p := range all {
		if !strings.HasSuffix(p, upgradeLockSuffix) {
			left = append(left, p)
		}
	}
	if len(left) != 0 {
		t.Fatalf("a failed upgrade left %v in the volume", left)
	}
}

// After a recreate `olivares upgrade` runs as the applied binary: the hand-over presents the slot
// as the running executable, stamped with its own version. The next upgrade must work from there,
// keep the image alone and refuse a release that is not newer than the slot.
func TestUpgradeInContainerRunsFromTheAppliedBinary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1, v2, v3 := buildStub(t, "26.700"), buildStub(t, "26.800"), buildStub(t, "26.900")
	image := writeTarget(t, v1)
	dataDir := execCapableDir(t)
	asContainerImage(t, image, "26.700")
	applied := filepath.Join(dataDir, "ota", "olivares")
	_, priv, _ := ed25519.GenerateKey(nil)

	f2 := newUpdFixtureSignedBy(t, priv, "26.800", "26.600", v2)
	if _, err := containerUpgrade(t, f2, dataDir); err != nil {
		t.Fatalf("first upgrade: %v", err)
	}
	// What a recreated container does: the image binary hands over, and the slot is what runs.
	runningBinary = func() (string, error) { return applied, nil }
	version = "26.800"

	f3 := newUpdFixtureSignedBy(t, priv, "26.900", "26.600", v3)
	if _, err := containerUpgrade(t, f3, dataDir); err != nil {
		t.Fatalf("upgrade run as the applied binary: %v", err)
	}
	if got := runsVersion(t, applied); !strings.Contains(got, "26.900") {
		t.Fatalf("the slot runs %q, want 26.900", got)
	}
	if got := runsVersion(t, image); !strings.Contains(got, "26.700") {
		t.Fatalf("the image binary changed: %q", got)
	}
	version = "26.700" // a recreate starts the image binary again
	if got := startsApplied(t, f3, dataDir, image); got != applied {
		t.Fatalf("a recreate would exec %q, want %q", got, applied)
	}
	version = "26.900" // and the hand-over runs the slot again
	// The slot is already at 26.900: the same release is not an upgrade, an older one is a downgrade.
	before, _ := os.ReadFile(applied)
	out, err := containerUpgrade(t, f3, dataDir)
	if err != nil || !strings.Contains(out, "up to date") {
		t.Fatalf("same release: err = %v out:\n%s", err, out)
	}
	if _, err := containerUpgrade(t, f2, dataDir); err == nil {
		t.Fatal("a downgrade of the applied binary was installed without --force-rollback")
	}
	if after, _ := os.ReadFile(applied); !bytes.Equal(before, after) {
		t.Fatal("a refused upgrade changed the applied binary")
	}
}

// The issue's own command: `olivares upgrade --enterprise` in a container keeps the release in
// the volume, so a recreate starts it again. The fixture models the licensed download route.
func TestUpgradeEnterpriseInContainerSurvivesARecreate(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1, v2 := buildStub(t, "26.700"), buildStub(t, "26.800")
	image := writeTarget(t, v1)
	dataDir := execCapableDir(t)
	installDevLicense(t, dataDir)
	asContainerImage(t, image, "26.700")
	f := newUpdFixture(t, "26.800", "26.600", v2)

	out, err := containerUpgrade(t, f, dataDir, "--enterprise", "--download-protocol", "legacy", "--token", "tkn")
	if err != nil {
		t.Fatalf("upgrade --enterprise in a container: %v\n%s", err, out)
	}
	applied := filepath.Join(dataDir, "ota", "olivares")
	if got := runsVersion(t, image); !strings.Contains(got, "26.700") {
		t.Fatalf("the image binary was replaced: %q", got)
	}
	if got := startsApplied(t, f, dataDir, image); got != applied {
		t.Fatalf("after a recreate the image binary would exec %q, want %q", got, applied)
	}
	// The fixture's key is not the one this test binary embeds, and the output must say so.
	if !strings.Contains(out, "not the key embedded in this image") {
		t.Fatalf("the output does not warn that a recreate will not start a release verified with another key:\n%s", out)
	}
}

func TestInContainerExcludesKubernetes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), ".dockerenv")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := containerMarkers
	containerMarkers = []string{marker}
	t.Cleanup(func() { containerMarkers = old })

	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if !inContainer() {
		t.Fatal("a Docker container (marker present) is not recognized")
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	if inContainer() {
		t.Fatal("a Kubernetes pod is declarative: `olivares upgrade` must not move its binary into the volume")
	}
	containerMarkers = []string{filepath.Join(t.TempDir(), "absent")}
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if inContainer() {
		t.Fatal("a host without a marker was taken for a container")
	}
}

func TestDataDirFromArgs(t *testing.T) {
	t.Setenv("OLIVARES_DATA_DIR", "/from/env")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"serve", "--data-dir", "/a"}, "/a"},
		{[]string{"serve", "--data-dir=/b", "--listen", ":1"}, "/b"},
		{[]string{"--data-dir", "/c", "upgrade"}, "/c"},
		{[]string{"serve", "--data-dir"}, "/from/env"},
		{[]string{"serve", "--data-dir", "/a", "--data-dir", "/z"}, "/z"},
		{[]string{"serve", "--data-dir=/a", "--data-dir=/z"}, "/z"},
		{[]string{"serve", "--data-dir="}, "/from/env"},
		{[]string{"version"}, "/from/env"},
		{[]string{"agent", "--data-dir", "/a", "--", "--data-dir", "/z"}, "/a"}, // after `--` it is somebody else's argument
		{[]string{"--", "--data-dir", "/z"}, "/from/env"},
		{[]string{"serve", "--data-dir", "relative/dir"}, "relative/dir"}, // made absolute by the caller
	} {
		if got := dataDirFromArgs(tc.args); got != tc.want {
			t.Errorf("dataDirFromArgs(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// Outside a container nothing changes: with no --target the upgrade replaces the binary it runs
// from, and no ota/ appears. A Kubernetes pod is declarative and counts as outside too. Without
// these two, dropping the container test from runUpgrade would send every host upgrade into a
// directory nothing ever executes from.
func TestUpgradeOutsideAContainerReplacesTheBinaryInPlace(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	marker := filepath.Join(t.TempDir(), ".dockerenv")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		set  func(t *testing.T)
	}{
		{"a host", func(t *testing.T) { inContainer = func() bool { return false } }},
		{"a Kubernetes pod", func(t *testing.T) {
			inContainer = realInContainer // the production test, on a host that shows a container marker
			old := containerMarkers
			containerMarkers = []string{marker}
			t.Cleanup(func() { containerMarkers = old })
			t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v1, v2 := buildStub(t, "26.700"), buildStub(t, "26.800")
			image := writeTarget(t, v1)
			dataDir := execCapableDir(t)
			asContainerImage(t, image, "26.700")
			tc.set(t) // after asContainerImage, which stands a container in
			f := newUpdFixture(t, "26.800", "26.600", v2)
			out, err := containerUpgrade(t, f, dataDir)
			if err != nil {
				t.Fatalf("upgrade: %v\n%s", err, out)
			}
			if got := runsVersion(t, image); !strings.Contains(got, "26.800") {
				t.Fatalf("the running binary was not replaced in place: %q", got)
			}
			if _, err := os.Stat(filepath.Join(dataDir, "ota")); err == nil {
				t.Fatal("an upgrade outside a container wrote into the data volume")
			}
			if backups, _ := filepath.Glob(image + ".bak-*"); len(backups) != 1 {
				t.Fatalf("backups = %v, want the one beside the binary that was replaced", backups)
			}
			if strings.Contains(out, "kept:") || strings.Contains(out, "data volume") {
				t.Fatalf("a container note outside a container:\n%s", out)
			}
		})
	}
}

// An upgrade whose candidate does not run after the swap is rolled back by the swap itself. In a
// container that must leave the previous release applied and verifiable, with nothing new stored.
func TestUpgradeInContainerRolledBackKeepsThePreviousRelease(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1, v2 := buildStub(t, "26.700"), buildStub(t, "26.800")
	flaky := buildFlakyStub(t, filepath.Join(t.TempDir(), "sentinel"))
	image := writeTarget(t, v1)
	dataDir := execCapableDir(t)
	asContainerImage(t, image, "26.700")
	applied := filepath.Join(dataDir, "ota", "olivares")
	_, priv, _ := ed25519.GenerateKey(nil)

	f2 := newUpdFixtureSignedBy(t, priv, "26.800", "26.600", v2)
	if _, err := containerUpgrade(t, f2, dataDir); err != nil {
		t.Fatalf("first upgrade: %v", err)
	}
	evidence, _ := os.ReadFile(filepath.Join(dataDir, "ota", "release.json"))

	f3 := newUpdFixtureSignedBy(t, priv, "26.900", "26.600", flaky)
	if _, err := containerUpgrade(t, f3, dataDir); err == nil {
		t.Fatal("a candidate that does not run after the swap was installed")
	}
	if got := runsVersion(t, applied); !strings.Contains(got, "26.800") {
		t.Fatalf("the slot runs %q after the rollback, want the previous 26.800", got)
	}
	if after, _ := os.ReadFile(filepath.Join(dataDir, "ota", "release.json")); !bytes.Equal(evidence, after) {
		t.Fatal("a rolled-back upgrade replaced the stored evidence")
	}
	if got := startsApplied(t, f2, dataDir, image); got != applied {
		t.Fatalf("a recreate would exec %q, want the previous release at %s", got, applied)
	}
}
