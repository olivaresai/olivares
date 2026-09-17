// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr/opgate"
)

// dataDirFingerprint is every entry of a data directory with its mode, size and
// content digest.
//
// The read-only cases below have to assert more than "boot returned nil": the whole
// point of the posture is that a remote-PostgreSQL installation with already-installed
// keys is READ, not rewritten. A boot that quietly re-minted a key or rewrote a
// record would satisfy a bare success assertion.
func dataDirFingerprint(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		info, ierr := os.Lstat(path)
		if ierr != nil {
			t.Fatal(ierr)
		}
		if !info.Mode().IsRegular() {
			out[e.Name()] = info.Mode().String()
			continue
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatal(rerr)
		}
		sum := sha256.Sum256(raw)
		out[e.Name()] = info.Mode().String() + " " + hex.EncodeToString(sum[:])
	}
	return out
}

func fingerprintNames(fp map[string]string) []string {
	names := make([]string, 0, len(fp))
	for n := range fp {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ⛔ F3-IR-7: A REMOTE-POSTGRES INSTALLATION WITH READ-ONLY LOCAL CUSTODY.
//
// The independent review installed keys through a real writable PostgreSQL boot,
// removed only the new coordination lock to model an installation that predates this
// control, set the directory to 0500 and ran a ReadOnly boot. It was refused, at lock
// CREATION, on a deployment that needs no local writes at all — a posture that worked
// before this control existed and that the old boot body still passes.
//
// Root RATIFIED the correction as an explicit rollout prerequisite rather than as a
// claim that nothing regressed. So this case measures the two halves of what was
// ratified, and it deliberately does NOT relabel the removed zero-step posture as
// solved:
//
//   - a PREPROVISIONED read-only installation boots, unchanged; and
//   - one that lacks the file is refused with a NAMED provisioning diagnosis that
//     carries the exact location, and the documented offline operation then makes the
//     same boot succeed.
//
// It is a permission-mode fixture on a local filesystem. It is NOT a read-only mount,
// and it is not Darwin or NFS acceptance.
func TestReadOnlyPostgresCustodyDirectory(t *testing.T) {
	t.Run("preprovisioned-lock-boots-unchanged", func(t *testing.T) {
		pg := newPGSplitFixture(t, "roprovisioned", false)
		dir := t.TempDir()
		cfg := bootConfig{DataDir: dir, Engine: "postgres", DSN: pg.appDSN, Version: "test", Logger: bootGateTestLogger()}

		// A real writable boot installs the keys AND, because the directory is
		// writable, provisions the coordination lock. That is the supported rollout:
		// the file exists before the directory becomes read-only.
		eng, err := boot(t.Context(), cfg)
		if err != nil {
			t.Fatalf("writable positive: %v", err)
		}
		_ = eng.Close()
		if keys := dataDirKeys(t, dir); len(keys) == 0 {
			t.Fatal("the writable boot installed no keys, so this case has no custody to read")
		}
		lock := filepath.Join(dir, ".dr-control.lock")
		info, err := os.Stat(lock)
		if err != nil {
			t.Fatalf("the writable boot did not provision the coordination lock: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("the provisioned lock is %#o, not 0600", info.Mode().Perm())
		}

		before := dataDirFingerprint(t, dir)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		cfg.ReadOnly = true
		eng, err = boot(t.Context(), cfg)
		if eng != nil {
			_ = eng.Close()
		}
		if err != nil {
			t.Fatalf("a preprovisioned read-only Postgres custody directory was rejected: %v", err)
		}
		after := dataDirFingerprint(t, dir)
		if len(before) != len(after) {
			t.Fatalf("the read-only boot changed the directory's contents: %v -> %v", fingerprintNames(before), fingerprintNames(after))
		}
		for name, want := range before {
			if got := after[name]; got != want {
				t.Fatalf("the read-only boot changed %s: %q -> %q", name, want, got)
			}
		}
	})

	t.Run("missing-lock-refuses-then-offline-provision-succeeds", func(t *testing.T) {
		if runWithoutDACOverride(t) {
			return
		}
		pg := newPGSplitFixture(t, "romissing", false)
		dir := t.TempDir()
		cfg := bootConfig{DataDir: dir, Engine: "postgres", DSN: pg.appDSN, Version: "test", Logger: bootGateTestLogger()}
		eng, err := boot(t.Context(), cfg)
		if err != nil {
			t.Fatalf("writable positive: %v", err)
		}
		_ = eng.Close()

		// Model an installation that predates this control: keys installed, no
		// coordination file.
		lock := filepath.Join(dir, ".dr-control.lock")
		if err := os.Remove(lock); err != nil {
			t.Fatal(err)
		}
		if keys := dataDirKeys(t, dir); len(keys) == 0 {
			t.Fatal("missing fixture keys")
		}
		before := dataDirFingerprint(t, dir)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		requireUnwritableDirectory(t, dir)

		cfg.ReadOnly = true
		eng, err = boot(t.Context(), cfg)
		if eng != nil {
			_ = eng.Close()
		}
		if err == nil {
			t.Fatal("a read-only installation with no coordination file booted anyway: the fence was skipped rather than refused")
		}
		// THE DIAGNOSIS IS THE PRODUCT OF THIS DECISION, so it is asserted rather than
		// assumed: a named sentinel, the exact location, and the offline action.
		if !errors.Is(err, opgate.ErrLockProvisioningRequired) {
			t.Fatalf("permission denied was not reported as a provisioning requirement: %v", err)
		}
		if !strings.Contains(err.Error(), lock) {
			t.Fatalf("the refusal does not name the exact file to provision: %v", err)
		}
		// ⛔ THIS ASSERTION USED TO REQUIRE `install `, AND THAT EXPECTATION WAS THE
		// DEFECT. install(1) opens the destination O_CREAT|O_TRUNC and falls back to
		// unlinking and re-creating it, so recommending it on a path that may already
		// hold a live lock invites replacing the very inode the exclusion lives on.
		// The refusal must carry a CREATE-ONLY action — the same one
		// docs/DR-RUNBOOK.md section 9.6 documents and opgate's tests execute.
		if !strings.Contains(err.Error(), "OFFLINE") || !strings.Contains(err.Error(), "set -C") {
			t.Fatalf("the refusal does not carry a create-only offline action: %v", err)
		}
		if strings.Contains(err.Error(), "install ") {
			t.Fatalf("the refusal recommends install(1), which can replace a live lock inode: %v", err)
		}
		// AND IT CHANGED NOTHING. A refusal that had fallen back to another lock
		// location, or re-minted a key, would show here.
		after := dataDirFingerprint(t, dir)
		if len(before) != len(after) {
			t.Fatalf("the refused read-only boot changed the directory: %v -> %v", fingerprintNames(before), fingerprintNames(after))
		}
		for name, want := range before {
			if got := after[name]; got != want {
				t.Fatalf("the refused read-only boot changed %s: %q -> %q", name, want, got)
			}
		}

		// THE IN-PROCESS PROVISIONING OPERATION — the one an ordinary writable boot
		// runs. It creates only the lock file, and the same boot then succeeds with
		// the custody untouched.
		//
		// The operator-facing SHELL recipe of docs/DR-RUNBOOK.md section 9.6 is a
		// different artifact and is executed, against the document itself, by
		// opgate's TestTheDocumentedProvisioningRecipeIsCreateOnlyAndAgreesWith-
		// ProvisionLock. Calling this helper and labeling it "the documented
		// operation" is what let the two drift apart.
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		anchor, present, aerr := opgate.AnchorForDataDir(dir)
		if aerr != nil || !present {
			t.Fatalf("anchor: %v (present=%t)", aerr, present)
		}
		if perr := opgate.ProvisionLock(anchor); perr != nil {
			t.Fatalf("the offline provisioning operation failed: %v", perr)
		}
		provisioned := dataDirFingerprint(t, dir)
		if len(provisioned) != len(before)+1 {
			t.Fatalf("provisioning created more than the lock file: %v -> %v", fingerprintNames(before), fingerprintNames(provisioned))
		}
		for name, want := range before {
			if got := provisioned[name]; got != want {
				t.Fatalf("provisioning changed %s: %q -> %q", name, want, got)
			}
		}
		// It is idempotent and never repairs what it finds.
		if perr := opgate.ProvisionLock(anchor); perr != nil {
			t.Fatalf("provisioning an already-provisioned lock failed: %v", perr)
		}
		if got := dataDirFingerprint(t, dir)[".dr-control.lock"]; got != provisioned[".dr-control.lock"] {
			t.Fatalf("a second provisioning rewrote the lock: %q -> %q", provisioned[".dr-control.lock"], got)
		}

		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		requireUnwritableDirectory(t, dir)
		eng, err = boot(t.Context(), cfg)
		if eng != nil {
			_ = eng.Close()
		}
		if err != nil {
			t.Fatalf("after the offline provisioning step, the same read-only boot was still refused: %v", err)
		}
		final := dataDirFingerprint(t, dir)
		for name, want := range provisioned {
			if got := final[name]; got != want {
				t.Fatalf("the read-only boot changed %s: %q -> %q", name, want, got)
			}
		}
	})
}
