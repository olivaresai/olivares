// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr/opgate"
)

func bootGateTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// installDataDirControl writes a durable control on a data directory through the
// product's own exclusive lease and commit path.
// installGenuineCompletedCustody writes the three real signing keys a completed
// operation published into the data directory and returns their canonical keyset.
func installGenuineCompletedCustody(t *testing.T, dir string) opgate.Keyset {
	t.Helper()
	var selected []opgate.SelectedKey
	for i, p := range []opgate.KeyPurpose{opgate.KeyAudit, opgate.KeyCatalog, opgate.KeyPolicy} {
		priv := writeGenuineLocalKey(t, dir, []string{"audit-signing.key", "catalog-signing.key", "policy-signing.key"}[i])
		f, err := opgate.FingerprintPublicKey(priv.Public().(ed25519.PublicKey))
		if err != nil {
			t.Fatal(err)
		}
		selected = append(selected, opgate.SelectedKey{Purpose: p, Source: opgate.CustodyLocal, PublicSHA256: f})
	}
	wire, err := opgate.NewKeyset(selected)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

// resolvedDefaultSQLiteTarget is the frozen target of the DSN boot defaults to for a
// data directory, through the one resolver the fence and the driver both use.
func resolvedDefaultSQLiteTarget(t *testing.T, dir string) string {
	t.Helper()
	target, err := opgate.ResolveSQLiteTarget(filepath.Join(dir, "olivares.db"))
	if err != nil {
		t.Fatalf("resolve the default sqlite target: %v", err)
	}
	return target.CanonicalPath()
}

func installDataDirControl(t *testing.T, dir, state string) opgate.Anchor {
	t.Helper()
	anchor, present, err := opgate.AnchorForDataDir(dir)
	if err != nil || !present {
		t.Fatalf("anchor for %s: %v (present=%t)", dir, err, present)
	}
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil || !ok {
		t.Fatalf("take the data dir anchor: %v (ok=%t)", err, ok)
	}
	rec := opgate.Record{
		Format:     opgate.Format,
		Revision:   2,
		OpID:       "cafebabecafebabecafebabecafebabe",
		State:      state,
		Enrolled:   true,
		PlanSHA256: strings.Repeat("5", 64),
		// ⛔ THE FROZEN SQLITE TARGET, WITHOUT WHICH THIS FIXTURE COMMITTED NOTHING.
		//
		// A sqlite record that names no file target is refused by opgate.Record's own
		// validation, so every case below was failing inside this helper — at
		// lease.Commit, before boot() was ever called — and the guard position it exists
		// to measure was not being exercised at all. Measured at baseline
		// 07729a2ed02b812b0079cf93f94e647c77725e9d: both cases fail there with
		// "opgate: SQLite record requires its frozen absolute file target".
		//
		// The target is the one the AUTHORITATIVE resolver produces for the DSN boot
		// defaults to, not a second spelling of the same path: fencing the canonical
		// path and naming an alias in the record is F3-IR-2 from the fixture's side.
		Destination: opgate.Destination{
			Engine: "sqlite", CanonicalPath: anchor.Canonical(),
			SQLiteFile: resolvedDefaultSQLiteTarget(t, dir),
		},
		ObservedAt: "2026-09-07T12:00:00Z",
	}
	if state == opgate.StateComplete {
		// ⛔ A COMPLETE CONTROL NOW NAMES CUSTODY THAT ACTUALLY EXISTS.
		//
		// This used to be one hand-typed fingerprint with no format field, and it could
		// not be committed at all — so nothing downstream of it ran. It is now the
		// canonical three-key set of three GENUINE keys installed in this very data
		// directory, which is what a completed operation publishes and what the boot
		// must load and prove under F3-IR-5.
		rec.Keyset = installGenuineCompletedCustody(t, dir)
	}
	if err := lease.Commit(anchor, rec); err != nil {
		_ = lease.Release()
		t.Fatalf("commit the control: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	return anchor
}

func dataDirKeys(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*-signing.key"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// THE PROPERTY THE GUARD'S POSITION EXISTS FOR.
//
// The three signing-key loaders MINT on a data directory that has none. So a boot
// that read the control, released it and only then loaded keys could create a fresh
// audit key inside the window a restore was replacing that very custody — and the
// ledger the restore then verified would be signed by a key nobody chose.
//
// The assertion is therefore not merely "boot refused": it is that the data
// directory is byte-for-byte as untouched as it was, with no key and no store.
func TestBootRefusesAFencedDataDirBeforeAnyKeyIsMinted(t *testing.T) {
	for _, state := range []string{opgate.StatePending, opgate.StateIndeterminate, opgate.StateQuarantined} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			installDataDirControl(t, dir, state)

			eng, err := boot(context.Background(), bootConfig{
				DataDir: dir, Engine: "sqlite", Version: "test", Logger: bootGateTestLogger(),
			})
			if err == nil {
				_ = eng.Close()
				t.Fatalf("boot opened an installation whose restore control is %q", state)
			}
			if !strings.Contains(err.Error(), "disaster-recovery operation") {
				t.Fatalf("the refusal does not name the operation that holds the installation: %v", err)
			}
			if !strings.Contains(err.Error(), "cafebabecafebabecafebabecafebabe") {
				t.Fatalf("the refusal does not name the operation identity: %v", err)
			}
			if keys := dataDirKeys(t, dir); len(keys) != 0 {
				t.Fatalf("the refused boot MINTED signing keys into a fenced data directory: %v", keys)
			}
			if _, err := os.Stat(filepath.Join(dir, "olivares.db")); !os.IsNotExist(err) {
				t.Fatalf("the refused boot created a store in a fenced data directory: %v", err)
			}
			// AND NOTHING ELSE WAS CREATED EITHER, with two named exceptions that are
			// not custody and not product state:
			//
			//   - the coordination files themselves, which is what a fence IS; and
			//   - .gitignore, which secure.EnsureDataDir writes at cmd/olivares/boot.go:717,
			//     BEFORE this guard is reached. It is the VCS exclusion that keeps key
			//     material out of a repository, it carries no estate, and moving the guard
			//     in front of it would leave the marker missing on the very directory a
			//     restore is about to fill with keys.
			after, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			allowed := map[string]bool{
				".gitignore":                  true,
				".dr-control":                 true,
				".dr-control.lock":            true,
				"olivares.db.dr-control":      true,
				"olivares.db.dr-control.lock": true,
			}
			for _, e := range after {
				if !allowed[e.Name()] {
					t.Fatalf("the refused boot created %q in a fenced data directory", e.Name())
				}
			}
		})
	}
}

// The control for the case above. Without it a boot that refused unconditionally
// would satisfy every refusal assertion.
func TestBootProceedsAndReleasesItsGuardWhenNothingHoldsTheInstallation(t *testing.T) {
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: "test", Logger: bootGateTestLogger(),
	})
	if err != nil {
		t.Fatalf("an ordinary installation with no restore control was refused: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if keys := dataDirKeys(t, dir); len(keys) == 0 {
		t.Fatal("the boot succeeded without minting the signing keys it is supposed to mint, so this case is not measuring the ordinary path")
	}
	// The guard is released once the store's publication decision is made: it is a
	// construction lock, not a claim over an engine that is already serving.
	anchor, present, err := opgate.AnchorForDataDir(dir)
	if err != nil || !present {
		t.Fatalf("anchor: %v (present=%t)", err, present)
	}
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil {
		t.Fatalf("after boot returned, the installation could not be taken: %v", err)
	}
	if !ok {
		t.Fatal("the boot is still holding its local guard after returning a running engine: a construction lock held by a serving process claims an exclusion the product does not promise")
	}
	_ = lease.Release()
}

// A COMPLETE control does not fence the installation the operation published — but
// under F3-IR-5 it no longer waves it through either.
//
// ⚠ THE PREMISE OF THIS CASE CHANGED, and that change is the sublot. It used to say
// "a COMPLETE control does not fence anything" and passed with a fixture whose keyset
// could not even be committed, on a data directory with no keys at all — which is the
// defect exactly: a completed control was permission to boot, whatever custody the
// node happened to have or mint. It now says what the ratified contract says: the
// operation published a specific custody, and the boot proceeds only by LOADING it.
func TestBootProceedsUnderACompletedRestoreControlByLoadingItsPublishedCustody(t *testing.T) {
	dir := t.TempDir()
	installDataDirControl(t, dir, opgate.StateComplete)
	before := keyBytesCensus(t, dir)

	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: "test", Logger: bootGateTestLogger(),
	})
	if err != nil {
		t.Fatalf("boot refused an installation carrying exactly the custody its completed control published: %v", err)
	}
	_ = eng.Close()
	// The published custody was LOADED, not reminted: a successful boot leaves the
	// three keys byte-identical.
	assertNoNewKeyBytes(t, dir, before)
}

// The same completed control with its published custody REMOVED refuses, and the
// refusal does not mint the key it is missing. This is the negative the case above
// could not have, because its fixture named custody that never existed.
func TestBootRefusesACompletedControlWhosePublishedCustodyIsGone(t *testing.T) {
	dir := t.TempDir()
	installDataDirControl(t, dir, opgate.StateComplete)
	if err := os.Remove(filepath.Join(dir, "audit-signing.key")); err != nil {
		t.Fatal(err)
	}
	before := keyBytesCensus(t, dir)

	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: "test", Logger: bootGateTestLogger(),
	})
	if err == nil {
		_ = eng.Close()
		t.Fatal("boot published a restored destination after its authorized audit key had been removed")
	}
	if !strings.Contains(err.Error(), "COMPLETED restore control") {
		t.Fatalf("the refusal does not name the custody cause: %v", err)
	}
	assertNoNewKeyBytes(t, dir, before)
	if _, serr := os.Stat(filepath.Join(dir, "audit-signing.key")); !os.IsNotExist(serr) {
		t.Fatal("the refusal MINTED the audit key the completed operation published")
	}
}

// A control that exists and cannot be read is a refusal, and it refuses BEFORE the
// keys are loaded like every other blocking verdict.
func TestBootRefusesAnUnreadableControlWithoutMinting(t *testing.T) {
	dir := t.TempDir()
	// The state of the fixture is irrelevant — the record is overwritten with a
	// truncated one below — but it must not be COMPLETE, because that fixture now
	// installs the genuine custody its control names and this case's assertion is
	// precisely that the data directory holds NO key when the boot is refused.
	anchor := installDataDirControl(t, dir, opgate.StatePending)
	if err := os.WriteFile(anchor.RecordPath(), []byte(`{"format":1,`), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: "test", Logger: bootGateTestLogger(),
	})
	if err == nil {
		_ = eng.Close()
		t.Fatal("boot opened an installation whose restore control could not be read")
	}
	if !strings.Contains(err.Error(), "not an absence") {
		t.Fatalf("the refusal does not distinguish an unreadable control from an absent one: %v", err)
	}
	if keys := dataDirKeys(t, dir); len(keys) != 0 {
		t.Fatalf("the refused boot minted signing keys: %v", keys)
	}
}

// The guard SPANS key loading and the store's publication decision, observed while
// a boot is in flight.
//
// The observation is IN-PROCESS, and its evidence is opgate.ErrSelfHeld: this
// process holds the anchor, so a second descriptor would conflict and the package
// says so rather than opening one. That is exactly the fact under test — the lease
// exists for the whole span — and it is the honest limit of what one process can
// measure. Kernel enforcement against a real competitor is proven separately, by
// the second-process cases in core/dr/opgate and core/internal/store/sqlstore.
func TestTheBootGuardSpansKeyLoadingAndPublication(t *testing.T) {
	dir := t.TempDir()
	anchor, present, err := opgate.AnchorForDataDir(dir)
	if err != nil || !present {
		t.Fatalf("anchor: %v (present=%t)", err, present)
	}
	var (
		mu       sync.Mutex
		sawHeld  bool
		sawFree  bool
		observed error
	)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			lease, ok, aerr := opgate.TryAcquire(opgate.ModeExclusive, anchor)
			mu.Lock()
			switch {
			case errors.Is(aerr, opgate.ErrSelfHeld):
				sawHeld = true
			case aerr != nil:
				observed = aerr
			case ok:
				sawFree = true
				_ = lease.Release()
			}
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
		}
	}()
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: "test", Logger: bootGateTestLogger(),
	})
	close(stop)
	<-done
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	mu.Lock()
	defer mu.Unlock()
	if observed != nil {
		t.Fatalf("the observer failed for a reason other than the guard: %v", observed)
	}
	if !sawHeld {
		t.Fatal("no observation during the boot found the installation held, so the guard is not spanning key loading and the publication decision")
	}
	if !sawFree {
		t.Fatal("the observer never found the installation free, so this case never established that the samples are meaningful")
	}
	// And it is free once the boot has returned.
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil || !ok {
		t.Fatalf("after boot returned, the installation is still held: %v (ok=%t)", err, ok)
	}
	_ = lease.Release()
}
