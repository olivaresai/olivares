// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// ONE UPGRADE AGENT PER BINARY, NOT N (C03-23).
//
// The install path had no mutual exclusion of any kind — the decision's own probe,
// `grep -rnE "flock|LOCK_EX" cmd/olivares/cmd_upgrade*.go core/release/*.go`, returned
// zero, and it still does for every file but this one. What made that expensive is the
// LENGTH of the unguarded window: runUpgrade reads the installed version once, then
// builds a source, fetches and verifies a manifest, evaluates anti-rollback and
// min_version against that reading, downloads an artifact over the network, extracts it,
// and only then swaps. On a slow link the guards are minutes old by the time the swap
// happens, and nothing rechecks them.
//
// Two runs inside that window corrupt each other's rollback rather than merely racing:
//
//   - The backup path was `<target>.bak-<unix seconds>`. Two runs in the SAME SECOND
//     computed the same path, so the second overwrote the first's backup. Run A saves V0
//     and swaps in Va; run B saves Va OVER that same file and swaps in Vb; A's post-swap
//     probe fails and A "rolls back" — to Va, the other channel's binary, and reports
//     success. That is the failure the retired "one systemd timer per channel" design
//     made routine: `RandomizedDelaySec=30m` and `Persistent=true` on two units are a
//     collision generator.
//   - copyFilePreserve staged the backup at the FIXED path `<dst>.tmp`, so two runs
//     interleaved bytes into one file and then published it with os.Rename. A torn
//     backup is worse than a missing one: it looks like a rollback that exists.
//
// Both are fixed at their source (os.CreateTemp for each), and this lock makes the whole
// prepare→download→swap sequence single-agent so a future third collision has nowhere to
// happen.
//
// WHY flock AND NOT AN O_EXCL LOCK FILE. A lock is only as good as its release. flock is
// held on the open file DESCRIPTION and the kernel drops it when the process dies for any
// reason, including SIGKILL and a power loss on the next boot — no staleness, no liveness
// heuristic, no owner check. An O_EXCL sentinel would need us to decide whether a lock
// whose PID is gone is stale, which is exactly the unresolved problem the push hook's
// mutex carries as deferred work D-1 (`.githooks/pre-push` LIMITACIONES N-4). We are not
// building a second one.
//
// WHY THE LOCK FILE IS NEVER REMOVED, and this is load-bearing rather than untidiness:
// unlinking it would BREAK the exclusion it provides. flock is a property of the inode.
// If run A holds the lock and removes the file on release, run B — which opened the same
// path a moment earlier and is waiting — is waiting on an inode that no longer has a
// name, while run C creates a NEW inode at that path and locks it successfully. B and C
// then both believe they are the only agent. The file is a few bytes and permanent on
// purpose.
const upgradeLockSuffix = ".upgrade-lock"

// lockUpgradeTarget takes the exclusive single-agent lock for target and returns the
// release function. It NEVER blocks: a second run is told to try later rather than queued
// behind a download that may take minutes, because the caller is as likely to be an
// unattended timer as a person, and a queue of timers is the thing being removed.
func lockUpgradeTarget(target string) (func(), error) {
	path := target + upgradeLockSuffix
	// 0o600: the lock carries no secret, but it sits next to a binary in a system
	// directory and nothing else needs to read it.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("could not open the upgrade lock %s: %w\nthis path must be writable to serialize upgrades of %s; a read-only directory cannot be upgraded in place", path, err, target)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, exitcode.New(exitcode.Conflict, fmt.Errorf(
				"another olivares upgrade is already installing to %s (it holds %s)\nREFUSING to run a second one: two concurrent installs overwrite each other's rollback backup, and the loser's automatic rollback would restore the winner's binary and report success\nway out: wait for it to finish and re-run; if you are certain no upgrade is running, the holder died and the kernel has already released the lock, so re-run now",
				target, path))
		}
		return nil, fmt.Errorf("could not take the upgrade lock %s: %w", path, err)
	}
	// Record who holds it. This is DIAGNOSTIC ONLY and no code reads it back to make a
	// decision: the kernel owns the answer to "is it held", and a PID in a file is
	// exactly the unreliable second opinion this design refuses to depend on.
	_ = f.Truncate(0)
	if _, werr := f.WriteAt([]byte(fmt.Sprintf("pid %d\n", os.Getpid())), 0); werr != nil {
		// A lock we hold but could not annotate is still a lock we hold.
		_ = werr
	}
	return func() {
		// Closing the descriptor releases the flock. The file itself stays — see the
		// comment above on why removing it would break exclusion.
		_ = f.Close()
	}, nil
}
