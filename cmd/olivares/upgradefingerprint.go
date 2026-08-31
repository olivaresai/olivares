// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// THE GUARDS MUST BE ABOUT THE FILE THAT IS STILL THERE (C03-23).
//
// runUpgrade decides up-to-date, min_version and anti-rollback against one reading of the
// installed version, taken before a manifest fetch and an artifact download. The upgrade
// lock makes a second `olivares upgrade` impossible during that window; it says nothing
// about a package manager, an image rollout, a config-management run or a person with
// `cp`, none of which take our lock.
//
// WHY NOT SIMPLY RE-READ THE VERSION, which is the obvious move and is what the decision's
// prose asks for. Because on the two commonest invocations re-reading it CANNOT FAIL, and a
// check that cannot fail is not a check:
//
//   - the default target is the running executable, and currentInstalledVersion answers
//     for it from this build's own `main.version` stamp — a value in the running image,
//     not a fact about the bytes on disk. Replace the file and the "re-read" is unchanged.
//   - with --current-version the function returns the operator's declaration, which is
//     equally immune to anything happening on disk.
//
// So the second reading is of the BYTES. It is not a stronger version check, it is a
// different and answerable question: are these the bytes the plan was made about?
type targetFingerprint struct {
	Size    int64
	ModTime time.Time
	SHA256  string
	// Err records why no fingerprint could be taken. It is carried rather than returned
	// so the failure surfaces at the point where it MATTERS — just before the swap —
	// instead of aborting a --check that was never going to install anything.
	Err error
}

// fingerprintTarget reads target and identifies its exact contents.
//
// The hash is deliberate rather than a stat comparison. Size and mtime are what a
// stat-only version would compare, and a replacement that preserves both is ordinary
// rather than exotic: `install -p`, `cp -p`, rsync with --times, and any restore from an
// archive that carries timestamps all do it. Hashing a binary costs a fraction of the
// download it is protecting.
func fingerprintTarget(target string) targetFingerprint {
	if target == "" {
		return targetFingerprint{Err: fmt.Errorf("no target path to fingerprint")}
	}
	f, err := os.Open(target)
	if err != nil {
		return targetFingerprint{Err: fmt.Errorf("open %s: %w", target, err)}
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return targetFingerprint{Err: fmt.Errorf("stat %s: %w", target, err)}
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return targetFingerprint{Err: fmt.Errorf("read %s: %w", target, err)}
	}
	return targetFingerprint{
		Size:    fi.Size(),
		ModTime: fi.ModTime(),
		SHA256:  hex.EncodeToString(h.Sum(nil)),
	}
}

// refuseIfTargetMoved re-fingerprints target and refuses when it is not the file `before`
// described.
//
// FAIL CLOSED IN BOTH DIRECTIONS. If the first fingerprint could not be taken, there is
// nothing to compare and the answer is a refusal, not a pass: "I could not look" is not
// "nothing changed". Same if the second one fails. The alternative — treating an
// unreadable target as unchanged — would turn every failure of this check into a silent
// approval, which is the precise shape of defect it exists to remove.
func refuseIfTargetMoved(target string, before targetFingerprint) error {
	if before.Err != nil {
		return fmt.Errorf("REFUSING to swap %s: the binary the upgrade plan was made about could not be fingerprinted (%v), so there is no way to confirm it is still the file being replaced\nthis is a refusal to proceed on an unverifiable premise, not a failure of the download", target, before.Err)
	}
	now := fingerprintTarget(target)
	if now.Err != nil {
		return fmt.Errorf("REFUSING to swap %s: could not re-read the binary just before replacing it (%v)\nthe anti-rollback and minimum-version verdicts above were computed against the earlier reading and cannot be reconfirmed", target, now.Err)
	}
	if now.SHA256 == before.SHA256 && now.Size == before.Size {
		return nil
	}
	return fmt.Errorf("REFUSING to swap %s: it CHANGED while this upgrade was downloading\n"+
		"  planned against: %d bytes, sha256 %s…, modified %s\n"+
		"  found now:       %d bytes, sha256 %s…, modified %s\n"+
		"Every ordering guard above (up-to-date, minimum-version, anti-rollback) was decided about the first file, so none of those verdicts describes what is installed now. Something else replaced it — a package manager, an image rollout, or another tool that does not take the upgrade lock.\n"+
		"way out: re-run the upgrade; it will re-read the target and re-evaluate the guards against what is actually there",
		target,
		before.Size, shortHash(before.SHA256), before.ModTime.UTC().Format(time.RFC3339Nano),
		now.Size, shortHash(now.SHA256), now.ModTime.UTC().Format(time.RFC3339Nano))
}

func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// uniqueBackupPath reserves a free path beginning with prefix and returns it.
//
// It CREATES the file to reserve the name, then removes it, and the small window between
// those two is deliberate rather than overlooked: the caller immediately writes the backup
// there through copyFilePreserve, which stages to its own O_EXCL temp file and publishes
// with os.Rename, so the final publish is atomic no matter who else appeared in between.
// What this removes is the collision that mattered — two runs computing the SAME name from
// a one-second clock and silently overwriting one another's rollback copy.
//
// os.CreateTemp is the reservation because its pattern semantics (a single trailing "*"
// replaced by a random string, O_CREATE|O_EXCL, retried on collision) are exactly what is
// wanted, and reimplementing them by hand is how off-by-one retry bugs are born.
func uniqueBackupPath(prefix string) (string, error) {
	dir := filepath.Dir(prefix)
	f, err := os.CreateTemp(dir, filepath.Base(prefix)+"*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := os.Remove(name); err != nil {
		return "", err
	}
	return name, nil
}
