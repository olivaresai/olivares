// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// ONE UPGRADE AGENT PER BINARY (C03-23) — the witnesses.
//
// Every test here is written so that reverting the change it guards turns it RED, and each
// one also asserts the NOT-FIRING direction, because "refuses a second agent" is satisfied
// just as well by a lock that refuses everybody.

// TestUpgradeLockExcludesASecondAgent is the witness for the lock itself. Delete the
// syscall.Flock call and this fails: the second acquisition succeeds.
func TestUpgradeLockExcludesASecondAgent(t *testing.T) {
	target := filepath.Join(t.TempDir(), "olivares")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	release1, err := lockUpgradeTarget(target)
	if err != nil {
		t.Fatalf("the FIRST agent must be able to take the lock: %v", err)
	}

	// THE FIRING DIRECTION: a second agent on the same target is refused.
	if _, err := lockUpgradeTarget(target); err == nil {
		t.Fatal("a SECOND concurrent upgrade took the lock on the same target; two installs would then overwrite each other's rollback backup")
	} else {
		if got := exitcode.From(err); got != exitcode.Conflict {
			t.Errorf("the refusal must be classified Conflict (%d) so a script can tell 'busy' from 'broken'; got %d", exitcode.Conflict, got)
		}
		// The message has to name the target and the lock path, or an operator cannot
		// act on it.
		if !strings.Contains(err.Error(), target) {
			t.Errorf("the refusal must name the target it is about; got: %v", err)
		}
		if !strings.Contains(err.Error(), upgradeLockSuffix) {
			t.Errorf("the refusal must name the lock file so the operator can inspect it; got: %v", err)
		}
	}

	// THE NOT-FIRING DIRECTION, in two parts. Without these, a lock that refuses
	// unconditionally passes the assertion above.
	other := filepath.Join(t.TempDir(), "olivares")
	if err := os.WriteFile(other, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	releaseOther, err := lockUpgradeTarget(other)
	if err != nil {
		t.Fatalf("the lock is PER TARGET: upgrading a different binary must not be blocked: %v", err)
	}
	releaseOther()

	release1()
	release2, err := lockUpgradeTarget(target)
	if err != nil {
		t.Fatalf("after the holder released it, the same target must be lockable again: %v", err)
	}
	release2()
}

// TestUpgradeLockFileSurvivesRelease guards the reason the release function closes the
// descriptor instead of removing the file. Unlinking it would break the exclusion: a
// waiter holds the old inode while a newcomer creates and locks a fresh one at the same
// path, and both then believe they are alone.
func TestUpgradeLockFileSurvivesRelease(t *testing.T) {
	target := filepath.Join(t.TempDir(), "olivares")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel, err := lockUpgradeTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if _, err := os.Stat(target + upgradeLockSuffix); err != nil {
		t.Fatalf("the lock file must OUTLIVE the lock (removing it breaks mutual exclusion): %v", err)
	}
}

// TestBackupPathsDoNotCollideWithinOneSecond is the witness for the backup-name defect.
// Restore `fmt.Sprintf("%s.bak-%d", target, time.Now().Unix())` and this fails: both
// callers get the identical path.
func TestBackupPathsDoNotCollideWithinOneSecond(t *testing.T) {
	dir := t.TempDir()
	// One fixed second for every caller, which is the condition the old code could not
	// survive. No sleeping, so the test cannot be flaky in the other direction either.
	prefix := filepath.Join(dir, "olivares") + ".bak-1786952743-"

	const n = 16
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := uniqueBackupPath(prefix)
			if err != nil {
				t.Errorf("uniqueBackupPath: %v", err)
				return
			}
			mu.Lock()
			seen[p]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("%d concurrent reservations in the SAME second produced %d distinct paths, so at least two runs would have overwritten one another's rollback copy: %v", n, len(seen), seen)
	}
	// The timestamp must survive in the name: operators read it to find the previous
	// binary, and the existing e2e tests glob `<target>.bak-*`.
	for p := range seen {
		if !strings.Contains(filepath.Base(p), ".bak-1786952743-") {
			t.Errorf("the reserved path lost its timestamp, which is how the backup is found: %s", p)
		}
	}
}

// TestCopyFilePreserveStagesUniquelyAndPreservesMode is the witness for the second
// collision: staging at the fixed path `<dst>.tmp`. Two callers writing one dst used to
// interleave into a single staging file and then each publish it, so the published backup
// could be a torn mixture of two binaries. Restore the fixed `.tmp` and the content
// assertion below fails.
func TestCopyFilePreserveStagesUniquelyAndPreservesMode(t *testing.T) {
	dir := t.TempDir()
	// Two sources of clearly different length, so a torn result is detectable rather
	// than merely suspected.
	srcA := filepath.Join(dir, "a")
	srcB := filepath.Join(dir, "b")
	contentA := bytes.Repeat([]byte("A"), 512*1024)
	contentB := bytes.Repeat([]byte("B"), 256*1024)
	if err := os.WriteFile(srcA, contentA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcB, contentB, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")

	var wg sync.WaitGroup
	for _, src := range []string{srcA, srcB, srcA, srcB} {
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			if err := copyFilePreserve(s, dst); err != nil {
				t.Errorf("copyFilePreserve(%s): %v", s, err)
			}
		}(src)
	}
	wg.Wait()

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	// Whoever won, the published file must be EXACTLY one of the inputs. A byte from
	// each is the corruption this fixes.
	if !bytes.Equal(got, contentA) && !bytes.Equal(got, contentB) {
		t.Fatalf("the published copy is neither source: %d bytes, %d 'A' and %d 'B' — a torn backup looks like a rollback that exists",
			len(got), bytes.Count(got, []byte("A")), bytes.Count(got, []byte("B")))
	}
	// Mode must be preserved, or a restored backup is not runnable. CreateTemp makes
	// 0o600, so this is what the explicit Chmod is for.
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("copyFilePreserve must preserve the source mode (a backup that cannot be executed cannot be rolled back to); got %v", fi.Mode().Perm())
	}
	// No staging leftovers.
	leftovers, _ := filepath.Glob(dst + ".tmp-*")
	if len(leftovers) != 0 {
		t.Errorf("staging files were left behind next to the binary: %v", leftovers)
	}
}

// TestAtomicSwapKeepsBothBackupsWithinOneSecond exists because the tests above prove
// uniqueBackupPath is CORRECT and prove nothing about whether atomicSwap CALLS it. A
// component that is right and unreachable is the failure mode this repository keeps
// finding, so the witness has to go through the swap itself: two swaps inside one second,
// two surviving backups. Restore the seconds-only name in atomicSwap and this fails with
// one backup where two are expected.
func TestAtomicSwapKeepsBothBackupsWithinOneSecond(t *testing.T) {
	v1 := buildStub(t, "26.7.0")
	v2 := buildStub(t, "26.8.0")
	v3 := buildStub(t, "26.9.0")
	target := writeTarget(t, v1)

	b1, _, err := atomicSwap(target, v2)
	if err != nil {
		t.Fatalf("first swap: %v", err)
	}
	b2, _, err := atomicSwap(target, v3)
	if err != nil {
		t.Fatalf("second swap: %v", err)
	}
	if b1 == b2 {
		t.Fatalf("both swaps reported the SAME backup path (%s): the second overwrote the first, so the first upgrade's rollback now restores the second's binary", b1)
	}
	all, _ := filepath.Glob(target + ".bak-*")
	if len(all) != 2 {
		t.Fatalf("two swaps must leave two backups; got %d: %v", len(all), all)
	}
	// And each backup must be the binary it replaced — the point of keeping them.
	if got := runsVersionMode(t, b1); !strings.Contains(got, "26.7.0") {
		t.Errorf("the first backup should hold the binary the first swap replaced; it reports %q", got)
	}
	if got := runsVersionMode(t, b2); !strings.Contains(got, "26.8.0") {
		t.Errorf("the second backup should hold the binary the second swap replaced; it reports %q", got)
	}
}

// TestUpgradeRefusesWhileAnotherAgentHoldsTheLock is the wiring witness for the lock: the
// unit test above proves lockUpgradeTarget excludes a second caller, and would stay green
// if runUpgrade never called it. Here the lock is held from outside and the REAL command
// is run.
func TestUpgradeRefusesWhileAnotherAgentHoldsTheLock(t *testing.T) {
	oldBin := buildStub(t, "26.7.0")
	newBin := buildStub(t, "26.8.0")
	f := newUpdFixture(t, "26.8.0", "", newBin)
	target := writeTarget(t, oldBin)

	rel, err := lockUpgradeTarget(target)
	if err != nil {
		t.Fatalf("could not take the lock to simulate the other agent: %v", err)
	}
	defer rel()

	out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
		"--target", target, "--yes", "--data-dir", t.TempDir())
	if err == nil {
		t.Fatal("the command installed while another agent held the upgrade lock, so the lock is not wired into the install path")
	}
	if got := exitcode.From(err); got != exitcode.Conflict {
		t.Errorf("a busy target must exit Conflict (%d) so an unattended caller can tell it apart from a real failure; got %d (%v)", exitcode.Conflict, got, err)
	}
	// Nothing was installed and nothing was backed up.
	if got := runsVersion(t, target); !strings.Contains(got, "26.7.0") {
		t.Errorf("the target must be untouched; it reports %q\noutput:\n%s", got, out)
	}
	if b, _ := filepath.Glob(target + ".bak-*"); len(b) != 0 {
		t.Errorf("a refusal before the swap must leave no backup: %v", b)
	}
}

// TestRefuseIfTargetMovedBothDirections is the witness for the pre-swap re-read, in both
// directions and including the two ways of not knowing.
func TestRefuseIfTargetMovedBothDirections(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "olivares")
	if err := os.WriteFile(target, []byte("version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTarget(target)
	if before.Err != nil {
		t.Fatalf("fingerprinting an existing file must work: %v", before.Err)
	}

	// NOT FIRING: an untouched target passes. Without this the whole check could be a
	// constant refusal and the firing case below would still be green.
	if err := refuseIfTargetMoved(target, before); err != nil {
		t.Fatalf("an UNCHANGED target must not be refused, or no upgrade could ever install: %v", err)
	}

	// FIRING: the target is replaced, as a package manager or an image rollout would.
	if err := os.WriteFile(target, []byte("version two, put here by something else"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := refuseIfTargetMoved(target, before)
	if err == nil {
		t.Fatal("the target changed between the plan and the swap and the swap was allowed; the anti-rollback and min_version verdicts were about a file that is no longer there")
	}
	if !strings.Contains(err.Error(), "CHANGED") || !strings.Contains(err.Error(), target) {
		t.Errorf("the refusal must say WHAT changed and name it; got: %v", err)
	}

	// FIRING, and this is the one an over-eager fix would get wrong: a change that keeps
	// the SIZE identical. A stat-only comparison of size would pass it.
	if err := os.WriteFile(target, []byte("version ONE"), 0o755); err != nil {
		t.Fatal(err)
	}
	sameSize := fingerprintTarget(target)
	if sameSize.Size != before.Size {
		t.Fatalf("test setup: the two contents must be the same length to exercise this (got %d vs %d)", sameSize.Size, before.Size)
	}
	if err := refuseIfTargetMoved(target, before); err == nil {
		t.Fatal("a same-size replacement was accepted: the comparison is not reading the bytes")
	}

	// FAIL CLOSED, direction one: nothing to compare against.
	//
	// AND IT MUST REFUSE FOR THE RIGHT REASON. Asserting only that the error is non-nil
	// measures the wrong guard: with the fail-closed branch disabled, execution falls
	// through to the byte comparison, where an empty first hash differs from any real one,
	// so the call still refuses — with the message "it CHANGED while this upgrade was
	// downloading", which is a false account of what happened and points the operator at a
	// nonexistent intruder. A mutation run caught exactly that: the assertion passed with
	// the guard removed. So the reason is what is asserted.
	blind := refuseIfTargetMoved(target, targetFingerprint{Err: fmt.Errorf("could not look")})
	if blind == nil {
		t.Fatal("a missing first fingerprint must REFUSE: 'I could not look' is not 'nothing changed'")
	}
	if !strings.Contains(blind.Error(), "could not be fingerprinted") || !strings.Contains(blind.Error(), "could not look") {
		t.Errorf("the refusal must name the unverifiable premise and carry the underlying cause, not report a change nobody made; got: %v", blind)
	}
	if strings.Contains(blind.Error(), "CHANGED") {
		t.Errorf("refusing an unfingerprintable plan must not be reported as the target having changed; got: %v", blind)
	}

	// FAIL CLOSED, direction two: the target is gone at swap time. Same discipline —
	// the message must be about not being able to re-read it.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	gone := refuseIfTargetMoved(target, before)
	if gone == nil {
		t.Fatal("a target that cannot be re-read at swap time must REFUSE, not be assumed unchanged")
	}
	if !strings.Contains(gone.Error(), "could not re-read") {
		t.Errorf("the refusal must say the re-read failed; got: %v", gone)
	}
}

// TestUpgradeRefusesWhenTheTargetIsReplacedMidDownload is the end-to-end witness: the real
// command, the real signed manifest, a real artifact fetch — and the binary swapped out
// from under it while the download is in flight. It proves the check is WIRED, not merely
// present, which is the difference the house rule is about.
func TestUpgradeRefusesWhenTheTargetIsReplacedMidDownload(t *testing.T) {
	oldBin := buildStub(t, "26.7.0")
	newBin := buildStub(t, "26.8.0")
	intruder := buildStub(t, "26.6.0")

	f := newUpdFixture(t, "26.8.0", "", newBin)
	target := writeTarget(t, oldBin)

	// The interposition: something else replaces the binary between the reading the
	// guards were decided against and the swap. Exactly once, so the test asserts one
	// event rather than a storm.
	var once sync.Once
	f.onArtifact = func() {
		once.Do(func() {
			if err := os.WriteFile(target, intruder, 0o755); err != nil {
				t.Errorf("interposition write: %v", err)
			}
		})
	}

	out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
		"--target", target, "--yes", "--data-dir", t.TempDir())
	if err == nil {
		t.Fatal("the upgrade swapped a binary that had been replaced while it downloaded; every ordering guard it printed was about the old file")
	}
	if !strings.Contains(err.Error(), "CHANGED") {
		t.Errorf("the refusal must say the target changed; got: %v\noutput:\n%s", err, out)
	}
	// AND IT MUST NOT HAVE SWAPPED. A refusal that still installed is worse than no
	// refusal, because the message says the opposite of what happened.
	if got := runsVersion(t, target); !strings.Contains(got, "26.6.0") {
		t.Errorf("the intruder's binary must still be in place, untouched by the refusal; target reports %q", got)
	}
	// No backup was taken either: the swap never started.
	if b, _ := filepath.Glob(target + ".bak-*"); len(b) != 0 {
		t.Errorf("a refusal before the swap must leave no backup behind: %v", b)
	}
}

// TestUpgradeStillInstallsWhenNothingTouchesTheTarget is the not-firing direction of the
// end-to-end case: the same command, the same fixture, no interposition. Without it, a
// pre-swap check that refused every upgrade would pass the test above.
func TestUpgradeStillInstallsWhenNothingTouchesTheTarget(t *testing.T) {
	oldBin := buildStub(t, "26.7.0")
	newBin := buildStub(t, "26.8.0")

	f := newUpdFixture(t, "26.8.0", "", newBin)
	target := writeTarget(t, oldBin)

	out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
		"--target", target, "--yes", "--data-dir", t.TempDir())
	if err != nil {
		t.Fatalf("an undisturbed upgrade must still install: %v\noutput:\n%s", err, out)
	}
	if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
		t.Fatalf("the new binary should be installed; target reports %q", got)
	}
	// Exactly one backup, and it must be the previous binary — the property the unique
	// name exists to keep true.
	b, _ := filepath.Glob(target + ".bak-*")
	if len(b) != 1 {
		t.Fatalf("expected exactly one backup, got %v", b)
	}
	if got := runsVersion(t, b[0]); !strings.Contains(got, "26.7.0") {
		t.Errorf("the backup must be the binary that was replaced; it reports %q", got)
	}
	// The lock was released, so a subsequent run is not locked out by our own leftover.
	rel, lerr := lockUpgradeTarget(target)
	if lerr != nil {
		t.Fatalf("the upgrade did not release its lock, so the next run would be refused: %v", lerr)
	}
	rel()
}
