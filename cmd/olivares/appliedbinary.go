// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/olivaresai/olivares/core/release"
)

// appliedbinary.go keeps an OTA-applied release across a container recreate.
//
// A container's image is immutable and its data volume is not, so `olivares upgrade` run in a
// container installs into the volume (<data-dir>/ota/) and every start of the image binary hands
// over to that release when it is newer. The volume is writable by the engine, so nothing in it
// is trusted on its own: a hand-over verifies the signed manifest, the artifact digest and the
// binary it is about to execute against the OTA key embedded in the image binary.
//
// Proving that the applied binary is the one the artifact holds means unpacking the artifact, which
// costs a second of CPU and a gigabyte of memory for a 170 MB binary, and a health probe runs
// `olivares readyz` every few seconds. So a proof is remembered, in the container's temporary
// directory (appliedStamp): not in the volume, which its writer can edit, and emptied by a recreate.
// A command skips the unpack only while the stamp names the manifest and the applied binary's
// identity (device, inode, size, mtime, ctime) exactly as they are now; otherwise it unpacks.
//
// ponytail: the evidence is checked against the image's key and the image's version, not against a
// floor kept where the volume's writer cannot reach, and only the signature and the version are
// decided from the manifest. A writer of the volume can therefore replace the release with any
// other validly signed one at the image's version or above (an older release, an expired one, the
// other edition's build). The check and the exec are two steps on a path that writer can also
// rename, and what the applied binary starts for itself (the confinement helper, a self-restart)
// runs that path unchecked. It can also make every command unpack the artifact, by keeping the
// proof from forming. The data directory is read from the arguments, as the commands read it, not
// from the parsed command. Code running in the container as the engine's uid can also write the
// stamp, and is run by commands until the container is recreated; another user's stamp is not
// read, and the identity trusts the filesystem's attributes. Upgrade trigger: a store the
// engine's own uid cannot write (a read-only mount, or the key held outside the volume).
//
// <data-dir>/ota/olivares              the applied binary (derived from the artifact; healed on start)
// <data-dir>/ota/release.json          the signed manifest the artifact was verified against, its
//                                      detached signature and the binary's digest, in ONE file so
//                                      the evidence changes atomically
// <data-dir>/ota/artifact-<sha256>     the artifact archive whose SHA-256 the manifest signs; the name
//                                      carries the digest so a new one never overwrites the one the
//                                      current release.json points to

const (
	appliedDirName     = "ota"
	appliedReleaseName = "release.json"
	appliedArtifactPfx = "artifact-"
	maxEvidenceBytes   = 1 << 20 // release.json holds a manifest, a signature and a digest
	maxStampBytes      = 4 << 10
	// probeArg0 is the name `olivares upgrade` runs its version probes under (argv[0], nothing else
	// reads it): a binary it is about to decide about must answer for itself and never hand over to
	// the applied release.
	probeArg0 = "olivares-upgrade-probe"
)

// appliedEvidence is release.json: the verified manifest bytes, their detached signature and the
// SHA-256 of the binary the artifact held when it was unpacked. The digest is a hint, never a
// reason to run a file: it is only trusted to say "the image is this very build", where being
// wrong runs the image, and an unpack must find the same digest in the artifact before it counts.
type appliedEvidence struct {
	Manifest     []byte `json:"manifest"`
	Signature    []byte `json:"signature"`
	BinarySHA256 string `json:"binary_sha256"`
}

// appliedStamp is what a full verification leaves in the container's temporary directory.
type appliedStamp struct {
	Manifest string `json:"manifest"` // SHA-256 of the signed manifest bytes the binary was derived for
	Slot     string `json:"slot"`     // slotIdentity of the applied binary when it was proved
	SHA256   string `json:"sha256"`   // digest of that binary
}

// inContainer reports a Docker or Podman container, where the image is read-only and the data
// volume is the one place an upgrade can persist. A Kubernetes pod is declarative: its image is
// rolled by the operator and `olivares upgrade` is not run in it (docs: upgrade-and-rollback).
// A variable so a test can stand either answer in.
var inContainer = func() bool {
	return runningInContainer() && os.Getenv("KUBERNETES_SERVICE_HOST") == ""
}

func appliedBinaryPath(dataDir string) string {
	return filepath.Join(dataDir, appliedDirName, binaryName)
}

// readVolumeFile reads a file of the data volume that is at most max bytes. The volume is writable
// by the engine, so a read must neither follow a link out of it, nor block on a pipe, nor load a
// file of any size: it opens without following, then asks the descriptor what it is.
func readVolumeFile(path string, max int64) ([]byte, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("%s is larger than the %d bytes it may hold", path, max)
	}
	buf := make([]byte, fi.Size())
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// afterSlotHash runs between the hash of the applied binary and the second look at it. A seam for
// the test that swaps the file there, which a volume's writer can do at any moment.
var afterSlotHash = func() {}

// slotProof reports whether the slot is a regular, owner-executable file whose SHA-256 is want, and
// which file that was: one open descriptor is looked at, hashed and looked at again, and the
// identity returned is that descriptor's, never whatever the path names a moment later. A slot that
// fails any of it is rewritten from the verified artifact, never run as it is. The binary is
// streamed through the hash, not loaded.
func slotProof(slot, want string) (identity string, ok bool) {
	f, err := openRegular(slot)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o100 == 0 || before.Size() > maxArtifactBytes {
		return "", false
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false
	}
	afterSlotHash()
	after, err := f.Stat()
	// The descriptor keeps naming the file it opened after a rename has replaced the path, so the path
	// must still be that file: the inode catches a swap whatever the timestamps of the old one say.
	named, nerr := os.Lstat(slot)
	if err != nil || nerr != nil || !os.SameFile(before, named) || fileIdentity(before) != fileIdentity(after) || hex.EncodeToString(h.Sum(nil)) != want {
		return "", false
	}
	return fileIdentity(after), true
}

// appliedProbeTarget is the file a container upgrade decides its ordering guards about: the
// applied binary when its evidence verifies, the file is exactly the verified binary and the
// release is at least as new as the image's, else the image's own file. The slot is never run
// before that: it sits in a volume the engine can write, so only a verified file is executed.
func appliedProbeTarget(dataDir string, pub ed25519.PublicKey, image string) string {
	m, bin, err := verifyAppliedRelease(dataDir, pub)
	if err != nil {
		return image
	}
	slot := appliedBinaryPath(dataDir)
	if _, ok := slotProof(slot, sha256hex(bin)); !ok {
		return image
	}
	// The hand-over ignores the volume in an image that carries no version; so does this.
	applied, err := release.ParseVersion(m.Version)
	if err != nil || release.IsUnstamped(version) {
		return image
	}
	self, err := release.ParseVersion(version)
	if err != nil || release.Compare(applied, self) < 0 {
		return image
	}
	return slot
}

// seedAppliedSlot makes the applied binary a copy of the image's, so the one upgrade flow
// (probe, back up, swap, roll back) runs on a file of the volume.
func seedAppliedSlot(image, slot string) error {
	if err := ensureAppliedDir(filepath.Dir(slot)); err != nil {
		return err
	}
	return copyFilePreserve(image, slot)
}

// ensureAppliedDir makes the directory of the applied release and insists that it is one: a link
// there would send what the upgrade writes and removes somewhere else.
func ensureAppliedDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the applied-release directory in the data volume: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return nil
}

// recordAppliedRelease stores the evidence the next start re-verifies. The artifact goes first,
// under a name that carries its digest, and release.json is replaced in one rename, so a crash
// or a full disk leaves the previous release intact. It runs after the swap: a failure leaves
// the volume without evidence for the new binary, which the next start ignores, so the caller
// must report it rather than call the upgrade done.
func recordAppliedRelease(dataDir string, manifest, sig, artifact, binary []byte) error {
	dir := filepath.Join(dataDir, appliedDirName)
	if err := ensureAppliedDir(dir); err != nil {
		return err
	}
	keep := appliedArtifactPfx + sha256hex(artifact)
	if err := writeAtomicMode(filepath.Join(dir, keep), artifact, 0o600); err != nil {
		return err
	}
	raw, err := json.Marshal(appliedEvidence{Manifest: manifest, Signature: sig, BinarySHA256: sha256hex(binary)})
	if err != nil {
		return err
	}
	if err := writeAtomicMode(filepath.Join(dir, appliedReleaseName), raw, 0o600); err != nil {
		return err
	}
	// The superseded artifacts are dead weight now; a failed removal is only wasted space.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if n := e.Name(); strings.HasPrefix(n, appliedArtifactPfx) && n != keep {
				_ = os.Remove(filepath.Join(dir, n))
			}
		}
	}
	return nil
}

// pruneAppliedBackups removes the backups the swap leaves beside the applied binary: every upgrade
// would otherwise leave one more full binary in the volume.
func pruneAppliedBackups(slot string) {
	entries, err := os.ReadDir(filepath.Dir(slot))
	if err != nil {
		return
	}
	prefix := filepath.Base(slot) + ".bak-"
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			_ = os.Remove(filepath.Join(filepath.Dir(slot), e.Name()))
		}
	}
}

// appliedNote is the line `olivares upgrade` prints about what a recreate does with the release it
// just applied. embedded is the key the image binary carries; pub is the one the release was
// verified with. imageVersion is "" when the upgrade runs as the applied binary itself, which
// cannot tell the image's version: it then says nothing about the order.
func appliedNote(pub, embedded ed25519.PublicKey, imageVersion, appliedVersion, target, keySrc string) string {
	if !bytes.Equal(pub, embedded) {
		return fmt.Sprintf("note: this release was verified with %s, not the key embedded in this image, so recreating the container will not start it\n", keySrc)
	}
	if imageVersion != "" {
		image, err := release.ParseVersion(imageVersion)
		if err != nil || release.IsUnstamped(imageVersion) {
			return "note: this image carries no version to order the release against, so recreating the container runs the image\n"
		}
		if applied, _ := release.ParseVersion(appliedVersion); release.Compare(applied, image) < 0 {
			return fmt.Sprintf("note: this release is older than the image (%s), so recreating the container runs the image\n", imageVersion)
		}
	}
	return fmt.Sprintf("kept: %s is in the data volume, so recreating the container starts it\n", target)
}

// handOverIfApplied is main's first act in a container: start the release the data volume
// carries when it is newer than this image's binary. Anything it cannot verify is ignored, with
// a message, and the image binary runs.
func handOverIfApplied() {
	if !canReexec || filepath.Base(os.Args[0]) == probeArg0 || !inContainer() {
		return
	}
	self, err := runningBinary()
	if err != nil {
		return
	}
	if code := handOver(os.Args, self, release.EmbeddedKey(), os.Stderr, execApplied); code != 0 {
		os.Exit(code)
	}
}

// handOver is handOverIfApplied with its inputs given. It returns the exit status the process
// must end with: 0 to carry on as the image binary (which also covers every ignored release).
// The engine must not start as something else than the release the operator applied, so a verified
// release that cannot be started stops `serve` (1); any other command still runs, so `upgrade` or
// removing ota/ can repair the volume.
func handOver(args []string, self string, pub ed25519.PublicKey, stderr io.Writer, exec func(path string) error) int {
	if len(args) == 0 {
		return 0
	}
	dataDir := dataDirFromArgs(args[1:])
	if dataDir == "" {
		return 0
	}
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	serve := len(args) > 1 && args[1] == "serve"
	notice := io.Discard
	if serve {
		notice = stderr
	}
	if err := handOverToApplied(dataDir, self, pub, notice, stderr, exec); err != nil {
		fmt.Fprintf(stderr, "olivares: %v\n", err)
		if serve {
			fmt.Fprintf(stderr, "olivares: to run the image's binary instead, remove %s\n", filepath.Join(dataDir, appliedDirName))
			return 1
		}
	}
	return 0
}

// dataDirFromArgs reads --data-dir the way the commands do (the last one wins, none after `--`),
// else the default data directory.
func dataDirFromArgs(args []string) string {
	dir := ""
scan:
	for i, a := range args {
		switch {
		case a == "--":
			break scan
		case a == "--data-dir" && i+1 < len(args):
			dir = args[i+1]
		case strings.HasPrefix(a, "--data-dir="):
			dir = strings.TrimPrefix(a, "--data-dir=")
		}
	}
	if dir != "" {
		return dir
	}
	if d, err := defaultDataDir(); err == nil {
		return d
	}
	return ""
}

// readAppliedEvidence reads release.json and verifies the manifest's signature against pub. It does
// not touch the artifact. fs.ErrNotExist (wrapped) means nothing is applied.
func readAppliedEvidence(dataDir string, pub ed25519.PublicKey) (release.Manifest, appliedEvidence, error) {
	var ev appliedEvidence
	raw, err := readVolumeFile(filepath.Join(dataDir, appliedDirName, appliedReleaseName), maxEvidenceBytes)
	if err != nil {
		return release.Manifest{}, ev, err
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return release.Manifest{}, ev, fmt.Errorf("%s is not readable: %w", appliedReleaseName, err)
	}
	if pub == nil {
		return release.Manifest{}, ev, errors.New("this build embeds no OTA key to verify it with")
	}
	m, err := release.VerifyManifest(ev.Manifest, ev.Signature, pub)
	return m, ev, err
}

// readArtifact loads the artifact file of the volume only once a stream through the hash has shown it
// to be the one the manifest signs: a file of any size that merely carries the right name must not
// cost the memory of its size.
func readArtifact(path, wantSHA256 string) ([]byte, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Size() > maxArtifactBytes {
		return nil, fmt.Errorf("%s is larger than the %d bytes it may hold", path, int64(maxArtifactBytes))
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, fi.Size())); err != nil {
		return nil, err
	}
	if hex.EncodeToString(h.Sum(nil)) != wantSHA256 {
		return nil, errors.New("the artifact's SHA-256 is not the one the signed manifest names")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, fi.Size())
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// verifyAppliedArtifact unpacks the artifact the verified manifest signs: its digest is checked
// against the manifest, then the binary inside is extracted. The digest recorded in the evidence
// must be that binary's, or the evidence is not what the upgrade stored.
func verifyAppliedArtifact(dataDir string, m release.Manifest, ev appliedEvidence) ([]byte, error) {
	art, ok := m.ArtifactFor(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return nil, fmt.Errorf("its manifest has no %s/%s artifact", runtime.GOOS, runtime.GOARCH)
	}
	archive, err := readArtifact(filepath.Join(dataDir, appliedDirName, appliedArtifactPfx+art.SHA256), art.SHA256)
	if err != nil {
		return nil, err
	}
	if err := release.VerifyArtifactSHA256(archive, art.SHA256); err != nil {
		return nil, err
	}
	bin, err := extractBinary(archive, binaryName)
	if err != nil {
		return nil, err
	}
	if sha256hex(bin) != ev.BinarySHA256 {
		return nil, fmt.Errorf("the binary digest recorded in %s is not the artifact's", appliedReleaseName)
	}
	return bin, nil
}

// verifyAppliedRelease re-verifies all of the evidence in dataDir/ota: the manifest signature, the
// artifact digest the manifest signs, and the binary inside the artifact. It returns the verified
// manifest and binary; fs.ErrNotExist (wrapped) means nothing is applied.
func verifyAppliedRelease(dataDir string, pub ed25519.PublicKey) (release.Manifest, []byte, error) {
	m, ev, err := readAppliedEvidence(dataDir, pub)
	if err != nil {
		return release.Manifest{}, nil, err
	}
	bin, err := verifyAppliedArtifact(dataDir, m, ev)
	if err != nil {
		return release.Manifest{}, nil, err
	}
	return m, bin, nil
}

// handOverToApplied verifies the applied release in dataDir and, when it should run instead of
// self, replaces this process with it through exec. It returns nil whenever self should run:
// nothing applied, the applied binary is this one, the image is newer, or the evidence does not
// verify (warn says why). The only error is a verified release that could not be started: the
// operator asked for that release, so running something else silently would be the wrong answer.
//
// The version is compared first, so an image that is newer than the release costs no reads but
// release.json. Whether the applied binary is the one the artifact holds is taken from the stamp
// of an earlier proof when the stamp still fits, and proved by unpacking the artifact when not.
func handOverToApplied(dataDir, self string, pub ed25519.PublicKey, notice, warn io.Writer, exec func(path string) error) error {
	slot := appliedBinaryPath(dataDir)
	if insideDir(self, filepath.Dir(slot)) {
		// The applied binary asking itself, or a file the upgrade is probing or has backed up
		// there: none of them hands over, or a candidate's self-check would run the old release.
		return nil
	}
	ignore := func(format string, a ...any) error {
		fmt.Fprintf(warn, "olivares: ignoring the applied release in %s: %s\n", filepath.Join(dataDir, appliedDirName), fmt.Sprintf(format, a...))
		return nil
	}
	m, ev, err := readAppliedEvidence(dataDir, pub)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil // nothing applied
	}
	if err != nil {
		return ignore("%v", err)
	}
	selfVer, err := release.ParseVersion(version)
	if err != nil || release.IsUnstamped(version) {
		return ignore("this binary carries no version to order it against")
	}
	appliedVer, err := release.ParseVersion(m.Version)
	if err != nil {
		return ignore("its manifest version %q: %v", m.Version, err)
	}
	c := release.Compare(appliedVer, selfVer)
	if c < 0 {
		fmt.Fprintf(notice, "olivares: this image (%s) is newer than the applied release %s; running the image. Run `olivares upgrade` to apply a newer one.\n", version, m.Version)
		return nil
	}
	selfSHA := ""
	isImage := func(digest string) bool { // the same version, and the very build this image holds
		if c != 0 || digest == "" {
			return false
		}
		if selfSHA == "" {
			selfSHA = fingerprintTarget(self).SHA256
		}
		return digest == selfSHA
	}
	if isImage(ev.BinarySHA256) {
		return nil // only ever a reason to run the image: no read of the artifact
	}
	binSHA, healthy := rememberedBinary(dataDir, ev.Manifest, slot)
	var bin []byte
	proved := "" // the identity of the applied binary, when this call proved it
	if !healthy {
		if bin, err = verifyAppliedArtifact(dataDir, m, ev); err != nil {
			return ignore("%v", err)
		}
		binSHA = sha256hex(bin)
		proved, healthy = slotProof(slot, binSHA)
	}
	if isImage(binSHA) {
		return nil
	}
	// The volume copy is derived from the verified artifact, never trusted: heal it. The release
	// is verified and newer, so failing to prepare it is an error like failing to start it.
	if !healthy {
		if err := writeAtomicMode(slot, bin, 0o755); err != nil {
			return fmt.Errorf("the applied release %s verified but its binary could not be written to %s: %w", m.Version, slot, err)
		}
		// The file just written is proved as the file it is: another can replace it at once.
		if proved, healthy = slotProof(slot, binSHA); !healthy {
			return ignore("the applied binary changed while it was being verified")
		}
	}
	if bin != nil {
		if err := rememberApplied(dataDir, ev.Manifest, binSHA, proved); err != nil {
			fmt.Fprintf(warn, "olivares: cannot remember this verification, so every command verifies in full: %v\n", err)
		}
	}
	fmt.Fprintf(notice, "olivares: starting the applied release %s from the data volume (%s)\n", m.Version, slot)
	if err := exec(slot); err != nil {
		return fmt.Errorf("the applied release %s verified but could not be started: %w", m.Version, err)
	}
	return nil
}

// stampPath is where the proof for dataDir's applied binary is kept: in the temporary directory,
// which must be absolute and must not be the data directory or inside it, for that is the volume
// again. ok is false when no proof can be kept.
func stampPath(dataDir string) (path string, ok bool) {
	dir := os.TempDir()
	if !filepath.IsAbs(dir) {
		return "", false
	}
	rel, err := filepath.Rel(resolvePath(dataDir), resolvePath(dir))
	if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", false // the data directory itself, or inside it
	}
	sum := sha256.Sum256([]byte(resolvePath(appliedBinaryPath(dataDir))))
	return filepath.Join(dir, "olivares-applied-"+hex.EncodeToString(sum[:8])+".json"), true
}

// rememberedBinary returns the digest of the applied binary when a stamp proves it: the stamp must
// be this user's alone, and name this very manifest and the applied binary as the filesystem knows
// it now.
func rememberedBinary(dataDir string, manifest []byte, slot string) (string, bool) {
	path, ok := stampPath(dataDir)
	if !ok {
		return "", false
	}
	f, err := openRegular(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxStampBytes || !ownedPrivately(fi) {
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxStampBytes))
	if err != nil {
		return "", false
	}
	var st appliedStamp
	if json.Unmarshal(raw, &st) != nil {
		return "", false
	}
	cur, err := os.Lstat(slot)
	if err != nil || !cur.Mode().IsRegular() {
		return "", false
	}
	if id := fileIdentity(cur); id == "" || st.Slot != id || st.Manifest != sha256hex(manifest) || st.SHA256 == "" {
		return "", false
	}
	return st.SHA256, true
}

// rememberApplied records that the applied binary was just proved to be the artifact's. identity is
// the one slotProof returned for the file whose hash was checked.
func rememberApplied(dataDir string, manifest []byte, binSHA, identity string) error {
	path, ok := stampPath(dataDir)
	if !ok {
		return fmt.Errorf("the temporary directory %s is relative or is the data directory or inside it", os.TempDir())
	}
	if identity == "" {
		return errors.New("this platform cannot identify the applied binary")
	}
	raw, err := json.Marshal(appliedStamp{Manifest: sha256hex(manifest), Slot: identity, SHA256: binSHA})
	if err != nil {
		return err
	}
	return writeAtomicMode(path, raw, 0o600)
}

// insideDir reports whether file lies inside dir, both made absolute and symlinks resolved, so a
// relative --data-dir cannot hide the applied binary from the check.
func insideDir(file, dir string) bool {
	rel, err := filepath.Rel(resolvePath(dir), resolvePath(file))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolvePath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return p
}
