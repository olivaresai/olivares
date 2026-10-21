// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stampFixture is a container with a release applied, and the temporary directory its proofs go to.
type stampFixture struct {
	dataDir, image, tmp string
	priv                ed25519.PrivateKey
	rel                 appliedRelease
}

func newStampFixture(t *testing.T) stampFixture {
	t.Helper()
	f := stampFixture{tmp: isolateTemp(t), dataDir: t.TempDir(), priv: newSigningKey(t)}
	f.rel = signRelease(t, f.priv, "26.800", []byte("applied-binary-26.800"))
	f.image = filepath.Join(t.TempDir(), "olivares")
	if err := os.WriteFile(f.image, []byte("image-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := recordAppliedRelease(f.dataDir, f.rel.manifest, f.rel.sig, f.rel.archive, f.rel.bin); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f stampFixture) run(t *testing.T) handOverRun {
	t.Helper()
	return runHandOver(t, f.dataDir, f.image, "26.700", f.rel.pub, nil)
}

func (f stampFixture) slot() string { return appliedBinaryPath(f.dataDir) }

func (f stampFixture) dropArtifacts(t *testing.T) {
	t.Helper()
	names, _ := filepath.Glob(filepath.Join(f.dataDir, "ota", "artifact-*"))
	for _, n := range names {
		if err := os.Remove(n); err != nil {
			t.Fatal(err)
		}
	}
}

// recreate is what `docker compose up -d --force-recreate` does to the temporary directory.
func (f stampFixture) recreate(t *testing.T) {
	t.Helper()
	names, _ := filepath.Glob(filepath.Join(f.tmp, "olivares-applied-*"))
	for _, n := range names {
		if err := os.Remove(n); err != nil {
			t.Fatal(err)
		}
	}
}

// The unpack that proves the applied binary is paid once; the commands after it (a health probe
// every few seconds) rely on a stamp in the container's temporary directory, and a recreate, which
// empties that directory, brings the proof back.
func TestHandOverRemembersAProofInTheTemporaryDirectory(t *testing.T) {
	f := newStampFixture(t)
	if r := f.run(t); r.execed != f.slot() {
		t.Fatalf("first hand-over: exec = %q warned = %q", r.execed, r.warned)
	}
	if names, _ := filepath.Glob(filepath.Join(f.tmp, "olivares-applied-*")); len(names) != 1 {
		t.Fatalf("stamps = %v, want the one proof", names)
	}
	f.dropArtifacts(t) // nothing left to unpack: only a stamp can vouch for the slot now
	if r := f.run(t); r.execed != f.slot() || r.warned != "" {
		t.Fatalf("with a proof: exec = %q warned = %q, want a hand-over that unpacked nothing", r.execed, r.warned)
	}
	f.recreate(t)
	if r := f.run(t); r.execed != "" || !strings.Contains(r.warned, "artifact-") {
		t.Fatalf("after a recreate: exec = %q warned = %q, want the release refused: it can no longer be proved", r.execed, r.warned)
	}
}

// The attack the stamp exists for. Everything on the volume is the writer's: a public signed
// manifest at the image's version or above, a binary of their own and the digest of it in
// release.json. With no artifact to prove the binary, no command may run it, however often it asks.
func TestHandOverRefusesAPlantedSlotWithForgedEvidence(t *testing.T) {
	f := newStampFixture(t)
	f.dropArtifacts(t)
	planted := []byte("#!/bin/sh\necho planted\n")
	if err := os.WriteFile(f.slot(), planted, 0o755); err != nil {
		t.Fatal(err)
	}
	editEvidence(t, f.dataDir, func(ev *appliedEvidence) { ev.BinarySHA256 = sha256hex(planted) })
	for i := 0; i < 2; i++ {
		if r := f.run(t); r.execed != "" || !strings.Contains(r.warned, "artifact-") {
			t.Fatalf("run %d: exec = %q warned = %q, want the planted binary refused", i+1, r.execed, r.warned)
		}
	}
	if names, _ := filepath.Glob(filepath.Join(f.tmp, "olivares-applied-*")); len(names) != 0 {
		t.Fatalf("a refused release left a proof: %v", names)
	}
}

// A proof is about one manifest and one file as the filesystem knows it. Rewriting the binary in
// place with its size and mtime put back (`cp -p` does that) changes its ctime, and a different
// signed manifest is a different proof.
func TestHandOverStampFollowsTheSlotAndTheManifest(t *testing.T) {
	f := newStampFixture(t)
	f.run(t)
	fi, err := os.Stat(f.slot())
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Repeat([]byte("x"), len(f.rel.bin))
	if err := os.WriteFile(f.slot(), tampered, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f.slot(), time.Now(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	f.dropArtifacts(t)
	recorded := f.rel
	// Without the artifact the rewritten slot cannot be proved, and the old stamp must not vouch for it.
	if r := f.run(t); r.execed != "" || !strings.Contains(r.warned, "artifact-") {
		t.Fatalf("a rewritten slot: exec = %q warned = %q, want it refused", r.execed, r.warned)
	}
	// Restore the evidence's artifact: the slot is healed from it, not run as it is.
	if err := recordAppliedRelease(f.dataDir, recorded.manifest, recorded.sig, recorded.archive, recorded.bin); err != nil {
		t.Fatal(err)
	}
	if r := f.run(t); r.execed != f.slot() {
		t.Fatalf("after restoring the artifact: exec = %q warned = %q", r.execed, r.warned)
	}
	if got, _ := os.ReadFile(f.slot()); !bytes.Equal(got, f.rel.bin) {
		t.Fatalf("the slot holds %q, want the artifact's binary", got)
	}

	// Another signed manifest, whose artifact is not there, is not covered by the old proof.
	other := signRelease(t, f.priv, "26.900", []byte("applied-binary-26.900"))
	if err := recordAppliedRelease(f.dataDir, other.manifest, other.sig, other.archive, other.bin); err != nil {
		t.Fatal(err)
	}
	f.dropArtifacts(t)
	if r := f.run(t); r.execed != "" || !strings.Contains(r.warned, "artifact-") {
		t.Fatalf("another manifest: exec = %q warned = %q, want the old proof not to vouch for it", r.execed, r.warned)
	}
}

// A temporary directory inside the data directory is the volume again: a proof kept there is the
// writer's to forge, and no hand-over trusts one.
func TestHandOverIgnoresAStampInTheDataDirectory(t *testing.T) {
	f := newStampFixture(t)
	inVolume := filepath.Join(f.dataDir, "tmp")
	if err := os.MkdirAll(inVolume, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", inVolume)
	first := f.run(t)
	if first.execed != f.slot() || !strings.Contains(first.warned, "inside it") {
		t.Fatalf("first: exec = %q warned = %q, want a hand-over that says why nothing is remembered", first.execed, first.warned)
	}
	f.dropArtifacts(t)
	if r := f.run(t); r.execed != "" || !strings.Contains(r.warned, "artifact-") {
		t.Fatalf("second: exec = %q warned = %q, want no proof taken from the volume", r.execed, r.warned)
	}
}

// When the release is this image's own build there is nothing to hand over to, and nothing to
// prove: the artifact is not read, so a missing or damaged one is no news.
func TestHandOverSameBuildNeedsNoUnpack(t *testing.T) {
	f := newStampFixture(t)
	same := signRelease(t, f.priv, "26.700", []byte("image-binary"))
	if err := recordAppliedRelease(f.dataDir, same.manifest, same.sig, same.archive, same.bin); err != nil {
		t.Fatal(err)
	}
	f.dropArtifacts(t)
	f.rel = same
	if r := f.run(t); r.execed != "" || r.warned != "" || r.notice != "" {
		t.Fatalf("exec = %q warned = %q notice = %q, want the image to run in silence", r.execed, r.warned, r.notice)
	}
}

// A proven slot does not excuse evidence that no longer verifies: the signature and the key are
// checked on every hand-over, stamp or not.
func TestHandOverRefusesBadEvidenceEvenWithAProvenSlot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, f stampFixture) ed25519.PublicKey
		want   string
	}{
		{"a tampered manifest", func(t *testing.T, f stampFixture) ed25519.PublicKey {
			editEvidence(t, f.dataDir, func(ev *appliedEvidence) {
				ev.Manifest = bytes.Replace(ev.Manifest, []byte("26.800"), []byte("26.900"), 1)
			})
			return f.rel.pub
		}, "signature"},
		{"a missing signature", func(t *testing.T, f stampFixture) ed25519.PublicKey {
			editEvidence(t, f.dataDir, func(ev *appliedEvidence) { ev.Signature = nil })
			return f.rel.pub
		}, "signature"},
		{"a key that did not sign it", func(t *testing.T, f stampFixture) ed25519.PublicKey {
			return newSigningKey(t).Public().(ed25519.PublicKey)
		}, "signature"},
		{"a build with no OTA key", func(t *testing.T, f stampFixture) ed25519.PublicKey { return nil }, "no OTA key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStampFixture(t)
			if r := f.run(t); r.execed != f.slot() {
				t.Fatalf("setup hand-over: exec = %q warned = %q", r.execed, r.warned)
			}
			pub := tc.mutate(t, f)
			r := runHandOver(t, f.dataDir, f.image, "26.700", pub, nil)
			if r.execed != "" || !strings.Contains(r.warned, tc.want) {
				t.Fatalf("exec = %q warned = %q, want the image to run with a warning naming %q", r.execed, r.warned, tc.want)
			}
		})
	}
}

// The version probes `olivares upgrade` runs on a binary it is about to decide about carry a name of
// their own, so that binary answers for itself instead of handing over to the applied release.
func TestExecProbeNamesItsProbe(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build a stub binary")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\nimport (\"fmt\"; \"os\")\nfunc main(){ fmt.Println(\"olivares argv0=\" + os.Args[0]) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	probed := filepath.Join(execCapableDir(t), "probed")
	if out, err := exec.Command("go", "build", "-o", probed, src).CombinedOutput(); err != nil {
		t.Fatalf("build the stub: %v\n%s", err, out)
	}
	line, err := execProbe(t.Context(), probed)
	if err != nil || line != "olivares argv0="+probeArg0 {
		t.Fatalf("probe line = %q err = %v, want the probe to run under the name %q", line, err, probeArg0)
	}
}

// A volume's writer can swap the applied binary at any moment, and an honest health probe can heal it
// during an upgrade. Either way the proof must be of the file that was hashed. Here the slot is
// replaced by a planted file just after the hash: the descriptor's identity changes under the hash,
// so nothing is vouched for, the plant is overwritten by the artifact's binary, and the stamp names
// the file that was written. A stamp taken from the path afterwards would name the plant, and the
// next command would run it with no artifact left to check it.
func TestHandOverStampsTheFileItProved(t *testing.T) {
	f := newStampFixture(t)
	if err := os.WriteFile(f.slot(), f.rel.bin, 0o755); err != nil { // a healthy slot, and no stamp yet
		t.Fatal(err)
	}
	planted := []byte("#!/bin/sh\necho planted\n")
	swapped := false
	old := afterSlotHash
	afterSlotHash = func() {
		if swapped {
			return
		}
		swapped = true
		stage := f.slot() + ".plant"
		if err := os.WriteFile(stage, planted, 0o755); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(stage, f.slot()); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterSlotHash = old })
	if r := f.run(t); r.execed != f.slot() {
		t.Fatalf("first: exec = %q warned = %q", r.execed, r.warned)
	}
	afterSlotHash = old
	if got, _ := os.ReadFile(f.slot()); !bytes.Equal(got, f.rel.bin) {
		t.Fatalf("the slot holds %q: the planted file survived the hand-over", got)
	}
	f.dropArtifacts(t) // only a stamp can vouch now, and it must be for the file that is there
	if r := f.run(t); r.execed != f.slot() || r.warned != "" {
		t.Fatalf("second: exec = %q warned = %q, want the stamp to match the written file", r.execed, r.warned)
	}
}

// A stamp is trusted only if it is this user's alone: one that others can read or write could have
// been made by anyone in the container.
func TestHandOverIgnoresAStampOthersCanWrite(t *testing.T) {
	f := newStampFixture(t)
	f.run(t)
	names, _ := filepath.Glob(filepath.Join(f.tmp, "olivares-applied-*"))
	if len(names) != 1 {
		t.Fatalf("stamps = %v", names)
	}
	if err := os.Chmod(names[0], 0o666); err != nil {
		t.Fatal(err)
	}
	f.dropArtifacts(t)
	if r := f.run(t); r.execed != "" || !strings.Contains(r.warned, "artifact-") {
		t.Fatalf("exec = %q warned = %q, want a world-writable stamp not to vouch", r.execed, r.warned)
	}
}

// Where no proof can be kept (a relative temporary directory, the data directory itself) every
// command proves in full, and says so.
func TestHandOverKeepsNoProofWhereItCannotBeTrusted(t *testing.T) {
	for _, tc := range []struct {
		name string
		tmp  func(f stampFixture) string
	}{
		{"a relative temporary directory", func(stampFixture) string { return "relative-tmp" }},
		{"the data directory itself", func(f stampFixture) string { return f.dataDir }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStampFixture(t)
			t.Chdir(t.TempDir())
			if err := os.Mkdir("relative-tmp", 0o700); err != nil { // it exists and could hold a stamp: only the rule refuses
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", tc.tmp(f))
			if r := f.run(t); r.execed != f.slot() || !strings.Contains(r.warned, "cannot remember") {
				t.Fatalf("first: exec = %q warned = %q, want a hand-over that says no proof is kept", r.execed, r.warned)
			}
			f.dropArtifacts(t)
			if r := f.run(t); r.execed != "" || !strings.Contains(r.warned, "artifact-") {
				t.Fatalf("second: exec = %q warned = %q, want no proof taken from there", r.execed, r.warned)
			}
		})
	}
}

// A data directory whose ota is a plain file (not a directory) has nothing applied, and a command
// has nothing to say about it.
func TestHandOverIsSilentWhenOtaIsNotADirectory(t *testing.T) {
	isolateTemp(t)
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "ota"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(t.TempDir(), "olivares")
	if err := os.WriteFile(image, []byte("image-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := runHandOver(t, dataDir, image, "26.700", newSigningKey(t).Public().(ed25519.PublicKey), nil)
	if r.execed != "" || r.warned != "" || r.notice != "" {
		t.Fatalf("exec = %q warned = %q notice = %q, want silence", r.execed, r.warned, r.notice)
	}
}

// What an upgrade writes and removes in ota/ must not be sent elsewhere by a link in its place.
func TestRecordRefusesALinkedAppliedDirectory(t *testing.T) {
	isolateTemp(t)
	rel := signRelease(t, newSigningKey(t), "26.800", []byte("applied-binary-26.800"))
	dataDir := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(dataDir, "ota")); err != nil {
		t.Fatal(err)
	}
	if err := recordAppliedRelease(dataDir, rel.manifest, rel.sig, rel.archive, rel.bin); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v, want a linked ota/ refused", err)
	}
	if names, _ := filepath.Glob(filepath.Join(elsewhere, "*")); len(names) != 0 {
		t.Fatalf("wrote through the link: %v", names)
	}
}

// A file that only carries the artifact's name must not cost the memory of its size: it is
// streamed through the hash and refused before anything is loaded.
func TestHandOverDoesNotLoadAnArtifactItHasNotHashed(t *testing.T) {
	f := newStampFixture(t)
	names, _ := filepath.Glob(filepath.Join(f.dataDir, "ota", "artifact-*"))
	if len(names) != 1 {
		t.Fatalf("artifacts = %v", names)
	}
	const size = 200 << 20 // sparse: costs no disk
	if err := os.Truncate(names[0], size); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	r := f.run(t)
	runtime.ReadMemStats(&after)
	if r.execed != "" || !strings.Contains(r.warned, "SHA-256") {
		t.Fatalf("exec = %q warned = %q, want the file refused by its digest", r.execed, r.warned)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > size/4 {
		t.Fatalf("a %d MiB file that is not the artifact cost %d MiB of allocation", size>>20, grown>>20)
	}
}
