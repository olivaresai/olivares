// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/release"
)

// appliedRelease is a signed release as `olivares upgrade` verified it: the manifest bytes, its
// detached signature, the artifact archive, and the binary inside.
type appliedRelease struct {
	manifest, sig, archive, bin []byte
	pub                         ed25519.PublicKey
}

func signRelease(t *testing.T, priv ed25519.PrivateKey, ver string, bin []byte) appliedRelease {
	t.Helper()
	rel := signArchive(t, priv, ver, tarGzBinary(t, bin), runtime.GOOS, runtime.GOARCH)
	rel.bin = bin
	return rel
}

// tarGzWithoutBinary is a real release archive that carries a LICENSE and no executable. Bytes
// that are not a tar would not do: extractBinary takes those for a bare executable.
func tarGzWithoutBinary(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	lic := []byte("AGPL")
	if err := tw.WriteHeader(&tar.Header{Name: "LICENSE", Mode: 0o644, Size: int64(len(lic)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(lic); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// signArchive signs a manifest that names archive as the goos/goarch artifact of ver.
func signArchive(t *testing.T, priv ed25519.PrivateKey, ver string, archive []byte, goos, goarch string) appliedRelease {
	t.Helper()
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       release.ChannelStable,
		Version:       ver,
		ReleasedAt:    time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
		Artifacts: []release.Artifact{{OS: goos, Arch: goarch,
			Filename: "olivares_" + ver + ".tar.gz", SHA256: hex.EncodeToString(sha256hash(archive)), Size: int64(len(archive))}},
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	sig := base64.StdEncoding.EncodeToString(release.SignManifest(mb, priv))
	return appliedRelease{manifest: mb, sig: []byte(sig), archive: archive, pub: priv.Public().(ed25519.PublicKey)}
}

func newSigningKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// handOverRun is what one hand-over did.
type handOverRun struct {
	execed         string
	notice, warned string
	err            error
}

// isolateTemp gives the test its own temporary directory, where hand-overs keep their stamps:
// the container's, which a recreate empties. It returns the directory.
func isolateTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func runHandOver(t *testing.T, dataDir, image, imageVer string, pub ed25519.PublicKey, execErr error) handOverRun {
	t.Helper()
	old := version
	version = imageVer
	t.Cleanup(func() { version = old })
	var r handOverRun
	var notice, warn bytes.Buffer
	r.err = handOverToApplied(dataDir, image, pub, &notice, &warn, func(path string) error {
		r.execed = path
		return execErr
	})
	r.notice, r.warned = notice.String(), warn.String()
	return r
}

// editEvidence rewrites release.json in dataDir after f changed its content.
func editEvidence(t *testing.T, dataDir string, f func(*appliedEvidence)) {
	t.Helper()
	p := filepath.Join(dataDir, "ota", "release.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var ev appliedEvidence
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	f(&ev)
	raw, err = json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHandOverToApplied(t *testing.T) {
	priv := newSigningKey(t)
	imageBin := []byte("image-binary-26.700")
	newer := signRelease(t, priv, "26.800", []byte("applied-binary-26.800"))

	setup := func(t *testing.T, rel appliedRelease) (dataDir, image string) {
		t.Helper()
		isolateTemp(t)
		dataDir = t.TempDir()
		image = filepath.Join(t.TempDir(), "olivares")
		if err := os.WriteFile(image, imageBin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := recordAppliedRelease(dataDir, rel.manifest, rel.sig, rel.archive, rel.bin); err != nil {
			t.Fatal(err)
		}
		return dataDir, image
	}

	t.Run("a newer applied release is verified, written to the volume and started", func(t *testing.T) {
		dataDir, image := setup(t, newer)
		r := runHandOver(t, dataDir, image, "26.700", newer.pub, nil)
		slot := appliedBinaryPath(dataDir)
		if r.err != nil || r.execed != slot {
			t.Fatalf("exec = %q err = %v warned = %q, want a hand-over to %s", r.execed, r.err, r.warned, slot)
		}
		if got, _ := os.ReadFile(slot); !bytes.Equal(got, newer.bin) {
			t.Fatalf("the volume binary is not the artifact's binary: %q", got)
		}
		if fi, _ := os.Stat(slot); fi.Mode().Perm()&0o111 == 0 {
			t.Fatalf("the applied binary is not executable: %v", fi.Mode())
		}
	})

	t.Run("a volume binary that is not the artifact's is replaced, never trusted", func(t *testing.T) {
		dataDir, image := setup(t, newer)
		slot := appliedBinaryPath(dataDir)
		if err := os.WriteFile(slot, []byte("planted"), 0o755); err != nil {
			t.Fatal(err)
		}
		r := runHandOver(t, dataDir, image, "26.700", newer.pub, nil)
		if got, _ := os.ReadFile(slot); r.execed != slot || !bytes.Equal(got, newer.bin) {
			t.Fatalf("exec = %q, volume binary = %q: the planted file must be replaced by the verified one", r.execed, got)
		}
	})

	t.Run("an image newer than the applied release keeps running", func(t *testing.T) {
		old := signRelease(t, priv, "26.600", []byte("applied-binary-26.600"))
		dataDir, image := setup(t, old)
		r := runHandOver(t, dataDir, image, "26.700", old.pub, nil)
		if r.execed != "" || !strings.Contains(r.notice, "26.600") {
			t.Fatalf("exec = %q notice = %q, want the image to run and say why", r.execed, r.notice)
		}
	})

	t.Run("the same version hands over only when the build differs", func(t *testing.T) {
		same := signRelease(t, priv, "26.700", imageBin)
		dataDir, image := setup(t, same)
		if r := runHandOver(t, dataDir, image, "26.700", same.pub, nil); r.execed != "" {
			t.Fatalf("handed over to a copy of the image's own build: %q", r.execed)
		}
		other := signRelease(t, priv, "26.700", []byte("business-build-26.700"))
		dataDir, image = setup(t, other)
		if r := runHandOver(t, dataDir, image, "26.700", other.pub, nil); r.execed != appliedBinaryPath(dataDir) {
			t.Fatalf("exec = %q (warned %q): a different build of the same version is the edition swap and must run", r.execed, r.warned)
		}
	})

	t.Run("nothing applied, or already running the applied binary, does nothing", func(t *testing.T) {
		dataDir := t.TempDir()
		image := filepath.Join(t.TempDir(), "olivares")
		if err := os.WriteFile(image, imageBin, 0o755); err != nil {
			t.Fatal(err)
		}
		if r := runHandOver(t, dataDir, image, "26.700", newer.pub, nil); r.execed != "" || r.warned != "" {
			t.Fatalf("empty volume: exec = %q warned = %q", r.execed, r.warned)
		}
		dataDir, _ = setup(t, newer)
		slot := appliedBinaryPath(dataDir)
		if err := os.WriteFile(slot, newer.bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if r := runHandOver(t, dataDir, slot, "26.800", newer.pub, nil); r.execed != "" || r.warned != "" {
			t.Fatalf("running as the applied binary: exec = %q warned = %q (an exec loop)", r.execed, r.warned)
		}
	})

	refusals := []struct {
		name   string
		mutate func(t *testing.T, dataDir string) ed25519.PublicKey
		want   string
	}{
		{"a tampered artifact", func(t *testing.T, d string) ed25519.PublicKey {
			names, _ := filepath.Glob(filepath.Join(d, "ota", "artifact-*"))
			if len(names) != 1 {
				t.Fatalf("artifacts = %v", names)
			}
			b, _ := os.ReadFile(names[0])
			b[len(b)/2] ^= 0xFF
			if err := os.WriteFile(names[0], b, 0o600); err != nil {
				t.Fatal(err)
			}
			return newer.pub
		}, "SHA-256"},
		{"a tampered manifest", func(t *testing.T, d string) ed25519.PublicKey {
			editEvidence(t, d, func(ev *appliedEvidence) {
				ev.Manifest = bytes.Replace(ev.Manifest, []byte("26.800"), []byte("26.900"), 1)
			})
			return newer.pub
		}, "signature"},
		{"a signature from another key", func(t *testing.T, d string) ed25519.PublicKey {
			return newSigningKey(t).Public().(ed25519.PublicKey)
		}, "signature"},
		{"a missing signature", func(t *testing.T, d string) ed25519.PublicKey {
			editEvidence(t, d, func(ev *appliedEvidence) { ev.Signature = nil })
			return newer.pub
		}, "signature"},
		{"an unreadable release.json", func(t *testing.T, d string) ed25519.PublicKey {
			if err := os.WriteFile(filepath.Join(d, "ota", "release.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			return newer.pub
		}, "release.json"},
		{"a missing artifact", func(t *testing.T, d string) ed25519.PublicKey {
			names, _ := filepath.Glob(filepath.Join(d, "ota", "artifact-*"))
			for _, n := range names {
				if err := os.Remove(n); err != nil {
					t.Fatal(err)
				}
			}
			return newer.pub
		}, "artifact-"},
		{"a build with no OTA key", func(t *testing.T, d string) ed25519.PublicKey { return nil }, "no OTA key"},
		{"evidence signed for another platform", func(t *testing.T, d string) ed25519.PublicKey {
			other := signArchive(t, priv, "26.800", newer.archive, "plan9", "arm")
			if err := recordAppliedRelease(d, other.manifest, other.sig, other.archive, newer.bin); err != nil {
				t.Fatal(err)
			}
			return other.pub
		}, "has no"},
		{"a signed archive that holds no binary", func(t *testing.T, d string) ed25519.PublicKey {
			other := signArchive(t, priv, "26.800", tarGzWithoutBinary(t), runtime.GOOS, runtime.GOARCH)
			if err := recordAppliedRelease(d, other.manifest, other.sig, other.archive, newer.bin); err != nil {
				t.Fatal(err)
			}
			return other.pub
		}, "ignoring the applied release"},
		{"a manifest version that is not a version", func(t *testing.T, d string) ed25519.PublicKey {
			other := signRelease(t, priv, "banana", []byte("applied-binary-banana"))
			if err := recordAppliedRelease(d, other.manifest, other.sig, other.archive, newer.bin); err != nil {
				t.Fatal(err)
			}
			return other.pub
		}, "ignoring the applied release"},
		{"a release.json that is a symlink", func(t *testing.T, d string) ed25519.PublicKey {
			p := filepath.Join(d, "ota", "release.json")
			elsewhere := filepath.Join(t.TempDir(), "release.json")
			if err := os.Rename(p, elsewhere); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, p); err != nil {
				t.Fatal(err)
			}
			return newer.pub
		}, "symbolic links"}, // the kernel's refusal of O_NOFOLLOW
	}
	for _, tc := range refusals {
		t.Run("refuses "+tc.name+" and runs the image", func(t *testing.T) {
			dataDir, image := setup(t, newer)
			pub := tc.mutate(t, dataDir)
			r := runHandOver(t, dataDir, image, "26.700", pub, nil)
			if r.execed != "" || r.err != nil || !strings.Contains(r.warned, tc.want) {
				t.Fatalf("exec = %q err = %v warned = %q, want the image to run with a warning naming %q", r.execed, r.err, r.warned, tc.want)
			}
			if _, err := os.Stat(appliedBinaryPath(dataDir)); err == nil {
				t.Fatal("an unverified release was written as the applied binary")
			}
		})
	}

	t.Run("a symlinked slot is replaced by the verified binary, never followed", func(t *testing.T) {
		dataDir, image := setup(t, newer)
		decoy := filepath.Join(t.TempDir(), "decoy")
		if err := os.WriteFile(decoy, newer.bin, 0o755); err != nil {
			t.Fatal(err)
		}
		slot := appliedBinaryPath(dataDir)
		if err := os.Symlink(decoy, slot); err != nil {
			t.Fatal(err)
		}
		r := runHandOver(t, dataDir, image, "26.700", newer.pub, nil)
		if fi, err := os.Lstat(slot); err != nil || !fi.Mode().IsRegular() || r.execed != slot || r.err != nil {
			t.Fatalf("slot = %v %v, exec = %q err = %v: the link must be replaced by a regular file", fi, err, r.execed, r.err)
		}
		if got, _ := os.ReadFile(decoy); !bytes.Equal(got, newer.bin) {
			t.Fatalf("the heal wrote through the link into %q", got)
		}
	})

	t.Run("a slot that lost its exec bit is rewritten before it is started", func(t *testing.T) {
		dataDir, image := setup(t, newer)
		slot := appliedBinaryPath(dataDir)
		if err := os.WriteFile(slot, newer.bin, 0o644); err != nil {
			t.Fatal(err)
		}
		r := runHandOver(t, dataDir, image, "26.700", newer.pub, nil)
		if fi, err := os.Stat(slot); err != nil || fi.Mode().Perm()&0o100 == 0 || r.execed != slot {
			t.Fatalf("slot = %v %v, exec = %q: a binary the owner cannot execute must be rewritten", fi, err, r.execed)
		}
	})

	t.Run("a slot the volume cannot take is an error, not a silent downgrade", func(t *testing.T) {
		dataDir, image := setup(t, newer)
		slot := appliedBinaryPath(dataDir)
		if err := os.MkdirAll(filepath.Join(slot, "x"), 0o700); err != nil {
			t.Fatal(err)
		}
		r := runHandOver(t, dataDir, image, "26.700", newer.pub, nil)
		if r.err == nil || r.execed != "" || !strings.Contains(r.err.Error(), "could not be written") {
			t.Fatalf("exec = %q err = %v, want the failure to prepare 26.800", r.execed, r.err)
		}
	})

	t.Run("a file inside ota/ (a probed candidate, a backup) never hands over", func(t *testing.T) {
		dataDir, _ := setup(t, newer)
		candidate := filepath.Join(dataDir, "ota", ".olivares-upgrade-new-1")
		if err := os.WriteFile(candidate, []byte("candidate"), 0o755); err != nil {
			t.Fatal(err)
		}
		if r := runHandOver(t, dataDir, candidate, "26.700", newer.pub, nil); r.execed != "" || r.warned != "" {
			t.Fatalf("exec = %q warned = %q: the candidate's self-check must run the candidate", r.execed, r.warned)
		}
	})

	t.Run("a second release replaces the first and its artifact", func(t *testing.T) {
		dataDir, _ := setup(t, newer)
		third := signRelease(t, priv, "26.900", []byte("applied-binary-26.900"))
		if err := recordAppliedRelease(dataDir, third.manifest, third.sig, third.archive, third.bin); err != nil {
			t.Fatal(err)
		}
		names, _ := filepath.Glob(filepath.Join(dataDir, "ota", "artifact-*"))
		if len(names) != 1 {
			t.Fatalf("artifacts = %v, want only the newest", names)
		}
		if _, bin, err := verifyAppliedRelease(dataDir, third.pub); err != nil || !bytes.Equal(bin, third.bin) {
			t.Fatalf("verify the second release: bin=%q err=%v", bin, err)
		}
	})

	t.Run("a failed write of release.json leaves the previous release verifiable", func(t *testing.T) {
		dataDir, _ := setup(t, newer)
		third := signRelease(t, priv, "26.900", []byte("applied-binary-26.900"))
		// A directory where release.json must be renamed to makes the second write fail.
		p := filepath.Join(dataDir, "ota", "release.json")
		prev, _ := os.ReadFile(p)
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(p, "x"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := recordAppliedRelease(dataDir, third.manifest, third.sig, third.archive, third.bin); err == nil {
			t.Fatal("recordAppliedRelease reported success though release.json could not be written")
		}
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, prev, 0o600); err != nil {
			t.Fatal(err)
		}
		// The previous artifact is still there: only a successful record prunes it.
		if _, bin, err := verifyAppliedRelease(dataDir, newer.pub); err != nil || !bytes.Equal(bin, newer.bin) {
			t.Fatalf("the previous release no longer verifies: bin=%q err=%v", bin, err)
		}
	})

	t.Run("an unstamped image has no order to compare and runs itself", func(t *testing.T) {
		dataDir, image := setup(t, newer)
		r := runHandOver(t, dataDir, image, "dev", newer.pub, nil)
		if r.execed != "" || !strings.Contains(r.warned, "no version") {
			t.Fatalf("exec = %q warned = %q", r.execed, r.warned)
		}
	})

	t.Run("a verified release that cannot start is an error, not a silent downgrade", func(t *testing.T) {
		dataDir, image := setup(t, newer)
		r := runHandOver(t, dataDir, image, "26.700", newer.pub, errors.New("exec format error"))
		if r.err == nil || !strings.Contains(r.err.Error(), "26.800") {
			t.Fatalf("err = %v, want the failure to start 26.800", r.err)
		}
	})
}

// TestAppliedProbeTarget pins which file an in-container upgrade decides its ordering guards
// about. The slot sits in a volume the engine can write, so it counts only when it is exactly the
// binary the stored evidence verifies to and that release is at least as new as the image.
func TestAppliedProbeTarget(t *testing.T) {
	priv := newSigningKey(t)
	rel := signRelease(t, priv, "26.800", []byte("applied-binary-26.800"))

	cases := []struct {
		name     string
		imageVer string
		pub      ed25519.PublicKey
		prepare  func(t *testing.T, dataDir string)
		wantSlot bool
	}{
		{"a verified slot at least as new as the image", "26.700", rel.pub, nil, true},
		{"a verified slot at the image's own version", "26.800", rel.pub, nil, true},
		{"a slot that is not the verified binary was planted", "26.700", rel.pub, func(t *testing.T, dataDir string) {
			if err := os.WriteFile(appliedBinaryPath(dataDir), []byte("planted"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a slot without the exec bit is not the verified binary", "26.700", rel.pub, func(t *testing.T, dataDir string) {
			if err := os.Chmod(appliedBinaryPath(dataDir), 0o644); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a symlink to the verified bytes is not the slot", "26.700", rel.pub, func(t *testing.T, dataDir string) {
			slot := appliedBinaryPath(dataDir)
			elsewhere := filepath.Join(t.TempDir(), "copy")
			if err := os.Rename(slot, elsewhere); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, slot); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a missing slot has nothing to probe", "26.700", rel.pub, func(t *testing.T, dataDir string) {
			if err := os.Remove(appliedBinaryPath(dataDir)); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a release older than the image is stale", "26.900", rel.pub, nil, false},
		{"an image with no version ignores the volume, as the hand-over does", "dev", rel.pub, nil, false},
		{"evidence another key signed is not trusted", "26.700", newSigningKey(t).Public().(ed25519.PublicKey), nil, false},
		{"no evidence at all", "26.700", rel.pub, func(t *testing.T, dataDir string) {
			if err := os.Remove(filepath.Join(dataDir, "ota", "release.json")); err != nil {
				t.Fatal(err)
			}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := version
			version = tc.imageVer
			t.Cleanup(func() { version = old })
			dataDir := t.TempDir()
			image := filepath.Join(t.TempDir(), "olivares")
			if err := os.WriteFile(image, []byte("image-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := recordAppliedRelease(dataDir, rel.manifest, rel.sig, rel.archive, rel.bin); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(appliedBinaryPath(dataDir), rel.bin, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.prepare != nil {
				tc.prepare(t, dataDir)
			}
			want := image
			if tc.wantSlot {
				want = appliedBinaryPath(dataDir)
			}
			if got := appliedProbeTarget(dataDir, tc.pub, image); got != want {
				t.Fatalf("appliedProbeTarget = %q, want %q", got, want)
			}
		})
	}
}

// A slot that is not the binary the artifact holds is rewritten by the hand-over, whatever the
// release.json beside it says.
func TestHandOverHealsAPlantedSlot(t *testing.T) {
	isolateTemp(t)
	priv := newSigningKey(t)
	rel := signRelease(t, priv, "26.800", []byte("applied-binary-26.800"))
	dataDir := t.TempDir()
	image := filepath.Join(t.TempDir(), "olivares")
	if err := os.WriteFile(image, []byte("image-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := recordAppliedRelease(dataDir, rel.manifest, rel.sig, rel.archive, rel.bin); err != nil {
		t.Fatal(err)
	}
	slot := appliedBinaryPath(dataDir)
	if err := os.WriteFile(slot, []byte("planted"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := runHandOver(t, dataDir, image, "26.700", rel.pub, nil)
	fi, _ := os.Stat(slot)
	if got, _ := os.ReadFile(slot); r.execed != slot || !bytes.Equal(got, rel.bin) || fi.Mode().Perm() != 0o755 {
		t.Fatalf("exec = %q, slot = %q %v: the planted file must be replaced by the verified binary, mode 0755", r.execed, got, fi.Mode())
	}
}

// release.json carries the digest of the binary as a hint. A hint that is not the artifact's binary
// is evidence that has been tampered with, whatever else verifies, and the release is ignored.
func TestHandOverRefusesADigestThatIsNotTheArtifacts(t *testing.T) {
	isolateTemp(t)
	priv := newSigningKey(t)
	rel := signRelease(t, priv, "26.800", []byte("applied-binary-26.800"))
	dataDir := t.TempDir()
	image := filepath.Join(t.TempDir(), "olivares")
	if err := os.WriteFile(image, []byte("image-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := recordAppliedRelease(dataDir, rel.manifest, rel.sig, rel.archive, rel.bin); err != nil {
		t.Fatal(err)
	}
	editEvidence(t, dataDir, func(ev *appliedEvidence) { ev.BinarySHA256 = sha256hex([]byte("another binary")) })
	r := runHandOver(t, dataDir, image, "26.700", rel.pub, nil)
	if r.execed != "" || !strings.Contains(r.warned, "recorded") {
		t.Fatalf("exec = %q warned = %q, want the release ignored for a digest that is not the artifact's", r.execed, r.warned)
	}
}

// Versions are MAJOR.MINOR numbers, not strings: 0.10 is newer than 0.9.
func TestHandOverOrdersVersionsNumerically(t *testing.T) {
	isolateTemp(t)
	priv := newSigningKey(t)
	for _, tc := range []struct {
		applied, image string
		wantHandOver   bool
	}{
		{"0.10", "0.9", true},
		{"0.9", "0.10", false},
		{"1.0", "0.10", true},
	} {
		t.Run(tc.applied+" over "+tc.image, func(t *testing.T) {
			rel := signRelease(t, priv, tc.applied, []byte("applied-binary-"+tc.applied))
			dataDir := t.TempDir()
			image := filepath.Join(t.TempDir(), "olivares")
			if err := os.WriteFile(image, []byte("image-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := recordAppliedRelease(dataDir, rel.manifest, rel.sig, rel.archive, rel.bin); err != nil {
				t.Fatal(err)
			}
			r := runHandOver(t, dataDir, image, tc.image, rel.pub, nil)
			if got := r.execed != ""; got != tc.wantHandOver {
				t.Fatalf("hand-over = %v (exec %q, warned %q), want %v", got, r.execed, r.warned, tc.wantHandOver)
			}
		})
	}
}

// An image newer than the applied release is decided from the version alone: whatever the volume
// holds beyond release.json is not read, so a probe pays nothing and a damaged artifact is no news.
func TestHandOverNewerImageReadsNoArtifact(t *testing.T) {
	isolateTemp(t)
	priv := newSigningKey(t)
	old := signRelease(t, priv, "26.600", []byte("applied-binary-26.600"))
	dataDir := t.TempDir()
	image := filepath.Join(t.TempDir(), "olivares")
	if err := os.WriteFile(image, []byte("image-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := recordAppliedRelease(dataDir, old.manifest, old.sig, old.archive, old.bin); err != nil {
		t.Fatal(err)
	}
	artifacts, _ := filepath.Glob(filepath.Join(dataDir, "ota", "artifact-*"))
	for _, a := range artifacts {
		if err := os.Remove(a); err != nil {
			t.Fatal(err)
		}
	}
	r := runHandOver(t, dataDir, image, "26.700", old.pub, nil)
	if r.execed != "" || r.warned != "" || !strings.Contains(r.notice, "26.600") {
		t.Fatalf("exec = %q warned = %q notice = %q, want the image to run with the newer-image notice only", r.execed, r.warned, r.notice)
	}
}

// handOver is what main does first: which command is the engine, what a failure ends the process
// with, and what is said where. The same fault ends `serve` and not `version`.
func TestHandOver(t *testing.T) {
	isolateTemp(t)
	priv := newSigningKey(t)
	rel := signRelease(t, priv, "26.800", []byte("applied-binary-26.800"))
	dataDir := t.TempDir()
	image := filepath.Join(t.TempDir(), "olivares")
	if err := os.WriteFile(image, []byte("image-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := recordAppliedRelease(dataDir, rel.manifest, rel.sig, rel.archive, rel.bin); err != nil {
		t.Fatal(err)
	}
	slot := appliedBinaryPath(dataDir)
	run := func(t *testing.T, imageVer, cmd string, execErr error) (code int, stderr, execed string) {
		t.Helper()
		old := version
		version = imageVer
		t.Cleanup(func() { version = old })
		var out bytes.Buffer
		code = handOver([]string{"olivares", cmd, "--data-dir", dataDir}, image, rel.pub, &out, func(p string) error {
			execed = p
			return execErr
		})
		return code, out.String(), execed
	}

	t.Run("the engine starts the applied release and says so", func(t *testing.T) {
		code, out, execed := run(t, "26.700", "serve", nil)
		if code != 0 || execed != slot || !strings.Contains(out, "starting the applied release 26.800") {
			t.Fatalf("code = %d exec = %q stderr = %q", code, execed, out)
		}
	})
	t.Run("a command hands over silently", func(t *testing.T) {
		code, out, execed := run(t, "26.700", "version", nil)
		if code != 0 || execed != slot || out != "" {
			t.Fatalf("code = %d exec = %q stderr = %q, want a silent hand-over", code, execed, out)
		}
	})
	t.Run("a release the engine cannot start stops the engine", func(t *testing.T) {
		code, out, _ := run(t, "26.700", "serve", errors.New("exec format error"))
		if code != 1 || !strings.Contains(out, "could not be started") {
			t.Fatalf("code = %d stderr = %q, want exit 1 naming the failure", code, out)
		}
	})
	t.Run("the same fault does not stop any other command", func(t *testing.T) {
		code, out, _ := run(t, "26.700", "version", errors.New("exec format error"))
		if code != 0 || !strings.Contains(out, "could not be started") {
			t.Fatalf("code = %d stderr = %q, want the command to run on with the failure reported", code, out)
		}
	})
	t.Run("a newer image says so to the engine only", func(t *testing.T) {
		if _, out, execed := run(t, "26.900", "serve", nil); execed != "" || !strings.Contains(out, "newer than the applied release") {
			t.Fatalf("serve: exec = %q stderr = %q", execed, out)
		}
		if _, out, execed := run(t, "26.900", "version", nil); execed != "" || out != "" {
			t.Fatalf("version: exec = %q stderr = %q, want silence", execed, out)
		}
	})
	t.Run("nothing applied is silent", func(t *testing.T) {
		var out bytes.Buffer
		code := handOver([]string{"olivares", "serve", "--data-dir", t.TempDir()}, image, rel.pub, &out, func(string) error {
			t.Fatal("exec with nothing applied")
			return nil
		})
		if code != 0 || out.Len() != 0 {
			t.Fatalf("code = %d stderr = %q", code, out.String())
		}
	})
}

// A relative --data-dir must not hide the applied binary from the loop guard: the slot, started
// from the volume, would otherwise verify and hand over to itself again and again.
func TestHandOverRelativeDataDirDoesNotLoop(t *testing.T) {
	isolateTemp(t)
	priv := newSigningKey(t)
	rel := signRelease(t, priv, "26.800", []byte("applied-binary-26.800"))
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	if err := recordAppliedRelease(dataDir, rel.manifest, rel.sig, rel.archive, rel.bin); err != nil {
		t.Fatal(err)
	}
	slot := appliedBinaryPath(dataDir)
	if err := os.WriteFile(slot, rel.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	old := version
	version = "26.700" // stamped older than the manifest, as a binary can be: only the guard stops it
	t.Cleanup(func() { version = old })
	var out bytes.Buffer
	code := handOver([]string{slot, "serve", "--data-dir", "data"}, slot, rel.pub, &out, func(p string) error {
		t.Fatalf("the applied binary handed over again to %s", p)
		return nil
	})
	if code != 0 || out.Len() != 0 {
		t.Fatalf("code = %d stderr = %q", code, out.String())
	}
}

// What a recreate does with the release just applied, in the words `olivares upgrade` prints.
func TestAppliedNote(t *testing.T) {
	embedded := newSigningKey(t).Public().(ed25519.PublicKey)
	other := newSigningKey(t).Public().(ed25519.PublicKey)
	for _, tc := range []struct {
		name                     string
		pub                      ed25519.PublicKey
		imageVer, appliedVer, in string
	}{
		{"a release verified with another key is not started", other, "26.700", "26.800", "not the key embedded"},
		{"an image with no version runs itself", embedded, "dev", "26.800", "carries no version"},
		{"a release older than the image is not started", embedded, "26.900", "26.800", "older than the image (26.900)"},
		{"ordered as numbers, 0.9 is older than 0.10", embedded, "0.10", "0.9", "older than the image (0.10)"},
		{"a release newer than the image is kept", embedded, "26.700", "26.800", "kept:"},
		{"the same version is kept", embedded, "26.800", "26.800", "kept:"},
		{"the applied binary cannot order itself against an image it cannot see", embedded, "", "26.100", "kept:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := appliedNote(tc.pub, embedded, tc.imageVer, tc.appliedVer, "/data/ota/olivares", "--pubkey")
			if !strings.Contains(got, tc.in) || !strings.HasSuffix(got, "\n") {
				t.Fatalf("note = %q, want it to contain %q", got, tc.in)
			}
		})
	}
}

// The data volume is written by the engine: a read of it neither follows a link, nor loads more
// than the file may hold.
func TestReadVolumeFile(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small")
	if err := os.WriteFile(small, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readVolumeFile(small, 10); err != nil || string(got) != "0123456789" {
		t.Fatalf("a file at the limit: %q %v", got, err)
	}
	if _, err := readVolumeFile(small, 9); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a file over the limit: err = %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(small, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readVolumeFile(link, 10); err == nil {
		t.Fatal("a symlink was followed")
	}
	if _, err := readVolumeFile(dir, 10); err == nil {
		t.Fatal("a directory was read as a file")
	}
}

// The caps hold where the hand-over reads, not only in the helper: a release.json or an artifact
// larger than it may be is refused before it is read, so a start cannot be made to load it.
func TestHandOverRefusesOversizedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(dataDir string) string
		size int64
	}{
		{"release.json", func(d string) string { return filepath.Join(d, "ota", "release.json") }, maxEvidenceBytes + 1},
		{"artifact", func(d string) string {
			names, _ := filepath.Glob(filepath.Join(d, "ota", "artifact-*"))
			if len(names) != 1 {
				t.Fatalf("artifacts = %v", names)
			}
			return names[0]
		}, maxArtifactBytes + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateTemp(t)
			priv := newSigningKey(t)
			rel := signRelease(t, priv, "26.800", []byte("applied-binary-26.800"))
			dataDir := t.TempDir()
			image := filepath.Join(t.TempDir(), "olivares")
			if err := os.WriteFile(image, []byte("image-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := recordAppliedRelease(dataDir, rel.manifest, rel.sig, rel.archive, rel.bin); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(tc.path(dataDir), tc.size); err != nil { // sparse: no disk or memory is spent
				t.Fatal(err)
			}
			r := runHandOver(t, dataDir, image, "26.700", rel.pub, nil)
			if r.execed != "" || !strings.Contains(r.warned, "larger than") {
				t.Fatalf("exec = %q warned = %q, want the oversized %s refused unread", r.execed, r.warned, tc.name)
			}
		})
	}
}

// A data directory reached through a symlink must not hide the applied binary from the loop guard:
// the process started from the slot reports its resolved path.
func TestHandOverSymlinkedDataDirDoesNotLoop(t *testing.T) {
	isolateTemp(t)
	priv := newSigningKey(t)
	rel := signRelease(t, priv, "26.800", []byte("applied-binary-26.800"))
	real := filepath.Join(t.TempDir(), "real")
	if err := recordAppliedRelease(real, rel.manifest, rel.sig, rel.archive, rel.bin); err != nil {
		t.Fatal(err)
	}
	slot := appliedBinaryPath(real)
	if err := os.WriteFile(slot, rel.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	old := version
	version = "26.700" // older than the manifest, as a binary can be: only the guard stops it
	t.Cleanup(func() { version = old })
	var out bytes.Buffer
	code := handOver([]string{slot, "serve", "--data-dir", link}, slot, rel.pub, &out, func(p string) error {
		t.Fatalf("the applied binary handed over again to %s", p)
		return nil
	})
	if code != 0 || out.Len() != 0 {
		t.Fatalf("code = %d stderr = %q", code, out.String())
	}
}
