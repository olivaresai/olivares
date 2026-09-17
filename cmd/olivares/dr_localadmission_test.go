// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr/opgate"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// ⛔ F3-IR-1 AT THE BOOT, MEASURED THROUGH THE REAL boot().
//
// The independent review held a data-directory anchor EXCLUSIVELY and then ran the
// real boot in an unrelated goroutine. The boot minted three signing-key files and
// published a store: it had borrowed an exclusive lease nobody handed it, in the one
// window where a restore is replacing that very custody.
//
// This is that case in this correction's own evidence. The assertion is not merely
// "boot refused": it is that the data directory is as untouched as a fenced
// directory must be.
func TestBootCannotBorrowAnUnrelatedExclusiveLease(t *testing.T) {
	dir := t.TempDir()
	anchor, present, err := opgate.AnchorForDataDir(dir)
	if err != nil || !present {
		t.Fatalf("anchor: %v (present=%t)", err, present)
	}
	held, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil || !ok {
		t.Fatalf("hold the installation exclusively: %v (ok=%t)", err, ok)
	}
	defer held.Release() //nolint:errcheck // teardown

	eng, err := boot(t.Context(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: "test", Logger: bootGateTestLogger(),
	})
	if eng != nil {
		_ = eng.Close()
	}
	if err == nil {
		t.Fatalf("an UNRELATED boot published under a held exclusive local lease; minted key files=%v", dataDirKeys(t, dir))
	}
	if !strings.Contains(err.Error(), "disaster-recovery operation holds this installation") {
		t.Fatalf("the refusal does not name the operation holding the installation: %v", err)
	}
	if keys := dataDirKeys(t, dir); len(keys) != 0 {
		t.Fatalf("the refused boot MINTED signing keys under an exclusive restore: %v", keys)
	}
	if _, serr := os.Stat(filepath.Join(dir, "olivares.db")); !os.IsNotExist(serr) {
		t.Fatalf("the refused boot created a store under an exclusive restore: %v", serr)
	}

	// THE CONTROL: once the operation lets go, the same boot proceeds and does mint.
	if err := held.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	eng, err = boot(t.Context(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: "test", Logger: bootGateTestLogger(),
	})
	if err != nil {
		t.Fatalf("after the operation released, an ordinary boot was still refused: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if keys := dataDirKeys(t, dir); len(keys) == 0 {
		t.Fatal("the control boot minted no keys, so this case never measured the ordinary path")
	}
}

// The boot's admission carries its ownership INTO the store, rather than letting the
// store take a second, unrelated lease. This case proves the seam exists by using it
// the way boot() does, and proves the destination is BOUND once acquired.
//
// ⚠ THE BINDING IS NOW STRUCTURAL, and that is why this case changed shape. It used
// to hand Open a second store.Config naming a different destination and assert the
// refusal. Open no longer accepts a Config at all: the destination is frozen when the
// admission is taken, and the only things the publication call supplies are the audit
// signer and the custody measurement. A re-pointed Open is not refused here, it is
// unspellable — which is the stronger property, so the weaker assertion is not kept
// alongside it. ErrAdmissionDestination survives for the two mismatches that CAN still
// occur: a local control naming another engine or frozen target, and a store resolution
// that disagrees with the fenced anchor.
func TestTheBootAdmissionCarriesOwnershipAndBindsItsDestination(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "olivares.db")
	cfg := store.Config{Engine: store.EngineSQLite, DSN: dsn}

	pub, err := acquireBootPublication(t.Context(), cfg, dir)
	if err != nil {
		t.Fatalf("begin the boot publication: %v", err)
	}
	defer pub.Close()

	// An unenrolled destination demands no custody, which is what lets the ordinary
	// first boot mint its keys.
	if req := pub.CustodyRequirement(); req.CustodyRequired() {
		t.Fatalf("a destination with no restore control demanded completed custody: %+v", req)
	}

	// While the admission is held, an ordinary EXCLUSIVE request is refused: this
	// process holds the anchors, which is what the span exists for.
	anchor, _, err := opgate.AnchorForDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, aerr := opgate.TryAcquire(opgate.ModeExclusive, anchor); !errors.Is(aerr, opgate.ErrSelfHeld) {
		t.Fatalf("the admission is not holding the data directory: %v", aerr)
	}

	// THE BOUND DESTINATION OPENS, and it is the one the admission fenced.
	st, err := pub.Open(t.Context(), nil, coreengine.CustodyObservation{}, nil)
	if err != nil {
		t.Fatalf("the admission refused its OWN destination: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, serr := os.Stat(dsn); serr != nil {
		t.Fatalf("the admission's Open did not create its destination: %v", serr)
	}

	// ONE publication decision per acquisition.
	second, err := pub.Open(t.Context(), nil, coreengine.CustodyObservation{}, nil)
	if second != nil {
		_ = second.Close()
	}
	if err == nil {
		t.Fatal("one admission made two publication decisions")
	}

	// And a CLOSED admission opens nothing at all.
	pub.Close()
	third, err := pub.Open(t.Context(), nil, coreengine.CustodyObservation{}, nil)
	if third != nil {
		_ = third.Close()
	}
	if err == nil {
		t.Fatal("a closed admission still published a store")
	}
}

// A boot whose destination cannot be resolved to one file refuses, rather than
// opening it with no coordination at all. The empty anchor the old stripper returned
// for anything it could not parse WAS an unfenced destination.
//
// The DSN here is a PLAIN name rather than a `file:` URI on purpose: at this layer
// `--dsn=file:<path>` is a SECRET REFERENCE resolved before the store ever sees it
// (cmd_serve.go), so a URI would be read as a path to a file containing a DSN and
// would never reach the resolver. The URI grammar is exercised where it is actually
// consumed, in the store's own cases.
func TestBootRefusesAnUnprovableSQLiteDestination(t *testing.T) {
	dir := t.TempDir()
	eng, err := boot(t.Context(), bootConfig{
		DataDir: dir, Engine: "sqlite", DSN: filepath.Join(dir, "x.db") + "?vfs=hostile",
		Version: "test", Logger: bootGateTestLogger(),
	})
	if eng != nil {
		_ = eng.Close()
	}
	if err == nil {
		t.Fatal("boot opened a destination reached through an unimplemented VFS")
	}
	if !errors.Is(err, coreengine.ErrRestoreCoordinationUnknown) {
		t.Fatalf("wrong classification: %v", err)
	}
}
