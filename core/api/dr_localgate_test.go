// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package api

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr/opgate"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

func consoleGateConfig(t *testing.T) DRConfig {
	t.Helper()
	dir := t.TempDir()
	return DRConfig{DataDir: dir, EngineKind: "sqlite", BackupDir: filepath.Join(dir, "backups")}
}

func writeConsoleControl(t *testing.T, dir, state string) opgate.Anchor {
	t.Helper()
	anchor, present, err := opgate.AnchorForDataDir(dir)
	if err != nil || !present {
		t.Fatalf("anchor: %v (present=%t)", err, present)
	}
	// The console restore promotes into this installation's default SQLite
	// file. Destination.validate requires the frozen absolute target from
	// the same resolver the fence and driver share.
	target, err := opgate.ResolveSQLiteTarget(filepath.Join(dir, "olivares.db"))
	if err != nil {
		t.Fatalf("resolve the console SQLite destination: %v", err)
	}
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil || !ok {
		t.Fatalf("take the anchor: %v (ok=%t)", err, ok)
	}
	rec := opgate.Record{
		Format:     opgate.Format,
		Revision:   1,
		OpID:       "0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f",
		State:      state,
		Enrolled:   true,
		PlanSHA256: strings.Repeat("9", 64),
		Destination: opgate.Destination{
			Engine:        "sqlite",
			CanonicalPath: anchor.Canonical(),
			SQLiteFile:    target.CanonicalPath(),
		},
		ObservedAt: "2026-09-07T12:00:00Z",
	}
	if state == opgate.StateComplete {
		rec.Keyset = completeConsoleKeyset(t)
	}
	if err := lease.Commit(anchor, rec); err != nil {
		_ = lease.Release()
		t.Fatalf("commit: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	return anchor
}

// completeConsoleKeyset is the codec-valid three-key generation a completed
// operation publishes. A hand-typed single fingerprint is not a record this
// build can commit, so a complete-control case would still fail inside the
// fixture rather than reaching the guard.
func completeConsoleKeyset(t *testing.T) opgate.Keyset {
	t.Helper()
	var selected []opgate.SelectedKey
	for _, p := range []opgate.KeyPurpose{opgate.KeyAudit, opgate.KeyCatalog, opgate.KeyPolicy} {
		public, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		f, err := opgate.FingerprintPublicKey(public)
		if err != nil {
			t.Fatal(err)
		}
		selected = append(selected, opgate.SelectedKey{
			Purpose: p, Source: opgate.CustodyLocal, PublicSHA256: f,
		})
	}
	wire, err := opgate.NewKeyset(selected)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

// The console restore takes the installation EXCLUSIVELY, and holds it: its
// critical section overwrites the signing keys one file at a time and only then
// replaces the store, so an interleaved boot or a second restore inside that span
// would leave custody and data from different generations.
func TestTheConsoleRestoreGuardIsExclusiveAndCoversBothAnchors(t *testing.T) {
	cfg := consoleGateConfig(t)
	guard, err := acquireConsoleRestoreGuard(cfg)
	if err != nil {
		t.Fatalf("acquire the console restore guard on a clean installation: %v", err)
	}
	defer guard.release()

	dirAnchor, _, err := opgate.AnchorForDataDir(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := opgate.TryAcquire(opgate.ModeExclusive, dirAnchor); !errors.Is(err, opgate.ErrSelfHeld) {
		t.Fatalf("the data directory is not held exclusively by the guard: %v", err)
	}
	fileAnchor, _, err := opgate.AnchorForStoreFile(filepath.Join(cfg.DataDir, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := opgate.TryAcquire(opgate.ModeExclusive, fileAnchor); !errors.Is(err, opgate.ErrSelfHeld) {
		t.Fatalf("the store file this restore promotes into is not held by the guard: %v", err)
	}
	// A SECOND restore is refused rather than queued behind the first.
	if _, err := acquireConsoleRestoreGuard(cfg); err == nil {
		t.Fatal("a second console restore took the installation while the first held it")
	}
	guard.release()
	// And released, so an ordinary boot can proceed afterwards.
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, dirAnchor)
	if err != nil || !ok {
		t.Fatalf("after release the installation is still held: %v (ok=%t)", err, ok)
	}
	_ = lease.Release()
}

// An unfinished operation's control blocks a console restore: overwriting custody
// on top of it would destroy the evidence that operation is waiting on.
func TestTheConsoleRestoreGuardRefusesAnUnfinishedOperation(t *testing.T) {
	for _, state := range []string{opgate.StatePending, opgate.StateIndeterminate, opgate.StateQuarantined} {
		t.Run(state, func(t *testing.T) {
			cfg := consoleGateConfig(t)
			writeConsoleControl(t, cfg.DataDir, state)
			guard, err := acquireConsoleRestoreGuard(cfg)
			if err == nil {
				guard.release()
				t.Fatalf("the console restore proceeded over an installation whose control is %q", state)
			}
			if !strings.Contains(err.Error(), "0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f") {
				t.Fatalf("the refusal does not name the unfinished operation: %v", err)
			}
		})
	}
}

// A COMPLETE control does not block: the operation that installed it finished.
func TestTheConsoleRestoreGuardProceedsUnderACompletedControl(t *testing.T) {
	cfg := consoleGateConfig(t)
	writeConsoleControl(t, cfg.DataDir, opgate.StateComplete)
	guard, err := acquireConsoleRestoreGuard(cfg)
	if err != nil {
		t.Fatalf("the console restore was refused under a completed control: %v", err)
	}
	guard.release()
}

// A control that exists and cannot be read is a refusal, and the guard is given
// back rather than leaked on that path.
func TestTheConsoleRestoreGuardRefusesAnUnreadableControlAndReleases(t *testing.T) {
	cfg := consoleGateConfig(t)
	anchor := writeConsoleControl(t, cfg.DataDir, opgate.StateComplete)
	if err := os.WriteFile(anchor.RecordPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireConsoleRestoreGuard(cfg); err == nil {
		t.Fatal("the console restore proceeded over an unreadable control")
	} else if !strings.Contains(err.Error(), "not an absence") {
		t.Fatalf("the refusal does not distinguish an unreadable control from an absent one: %v", err)
	}
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil || !ok {
		t.Fatalf("the refused guard leaked its lease: %v (ok=%t)", err, ok)
	}
	_ = lease.Release()
}

// ⛔ F3-IR-1, THROUGH THE REAL CONSOLE GUARD AND THE REAL PUBLIC CONSTRUCTOR.
//
// The independent review took this exact guard — the one runRestore holds across its
// critical section — and then called public coreengine.Open, from an unrelated
// goroutine, against the very SQLite target the guard was protecting. Open returned a
// Store while both console anchors were still held EXCLUSIVELY. Nothing in the
// request named a parent lease, an operation, an owner or a capability; the lock
// package granted it because this PROCESS held something stronger.
//
// The correction is that an unrelated request is judged exactly as a request from
// another process would be. The console's own exclusive critical section is
// unchanged, and the case below proves both halves: the unrelated Open refuses while
// the guard holds, and succeeds once it lets go.
func TestConsoleExclusiveGuardRefusesUnrelatedEngineOpen(t *testing.T) {
	cfg := consoleGateConfig(t)
	target := filepath.Join(cfg.DataDir, "olivares.db")
	guard, err := acquireConsoleRestoreGuard(cfg)
	if err != nil {
		t.Fatalf("acquire the console restore guard: %v", err)
	}
	defer guard.release()

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		st, oerr := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: target}, nil)
		if st != nil {
			_ = st.Close()
		}
		done <- oerr
	}()
	select {
	case oerr := <-done:
		if oerr == nil {
			t.Fatal("an UNRELATED engine.Open published a store while the real console restore guard held both anchors exclusively")
		}
		if !errors.Is(oerr, coreengine.ErrRestorePublicationBusy) {
			t.Fatalf("the unrelated Open was refused with the wrong classification: %v", oerr)
		}
	case <-ctx.Done():
		t.Fatal("the ordinary Open WAITED behind the restore instead of refusing in one try")
	}
	// AND IT REFUSED BEFORE ANY EFFECT: no database, no WAL, no SHM beside the keys
	// the restore is about to replace.
	for _, p := range []string{target, target + "-wal", target + "-shm"} {
		if _, serr := os.Stat(p); !os.IsNotExist(serr) {
			t.Fatalf("the refused Open created %s inside the console restore's critical section: %v", p, serr)
		}
	}

	// THE CONTROL. Without it a fence that refused everything would pass above.
	guard.release()
	st, err := coreengine.Open(t.Context(), store.Config{Engine: store.EngineSQLite, DSN: target}, nil)
	if err != nil {
		t.Fatalf("after the console restore released, an ordinary Open of the same target was still refused: %v", err)
	}
	_ = st.Close()
}
