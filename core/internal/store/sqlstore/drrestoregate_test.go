// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sqlstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/store"
)

// sqliteFenceHelperEnv turns this test binary into a SECOND PROCESS that holds a
// destination's control exclusively.
//
// A goroutine cannot stand in for it. This package's own nesting rule GRANTS a
// shared request under an exclusive lease the same process already holds — that is
// deliberate, and it is what keeps a restore from deadlocking against the store it
// is about to open — so an in-process holder would prove the opposite of the
// property under test. Only another process measures the kernel.
const sqliteFenceHelperEnv = "OLIVARES_SQLSTORE_FENCE_HELPER"

// TestSQLiteRestoreFenceHelperProcess is a helper ENTRY POINT, not a control. In an
// ordinary run it does nothing; it is re-executed by name, with the environment
// variable set, by the case below.
func TestSQLiteRestoreFenceHelperProcess(t *testing.T) {
	path := os.Getenv(sqliteFenceHelperEnv)
	if path == "" {
		t.Skip("helper process entry point: it runs only when this binary is re-executed by TestSQLiteOpenIsFencedOutByAnotherProcess")
	}
	anchor, present, err := opgate.AnchorForStoreFile(path)
	if err != nil || !present {
		fmt.Fprintf(os.Stderr, "fence helper: anchor %s: %v (present=%t)\n", path, err, present)
		os.Exit(1)
	}
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil || !ok {
		fmt.Fprintf(os.Stderr, "fence helper: acquire: %v (ok=%t)\n", err, ok)
		os.Exit(1)
	}
	// Report readiness, then hold until the parent closes our stdin.
	fmt.Println("held")
	_, _ = os.Stdin.Read(make([]byte, 1))
	_ = lease.Release()
	os.Exit(0)
}

// holdDestinationInAnotherProcess starts the helper and returns a stop function.
func holdDestinationInAnotherProcess(t *testing.T, path string) func() {
	t.Helper()
	cmd := osexec.Command(os.Args[0], "-test.run=^TestSQLiteRestoreFenceHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(), sqliteFenceHelperEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the holder process: %v", err)
	}
	ready := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		var seen strings.Builder
		for {
			n, rerr := stdout.Read(buf)
			if n > 0 {
				seen.Write(buf[:n])
				if strings.Contains(seen.String(), "held") {
					close(ready)
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		t.Fatal("the holder process never reported that it held the destination")
	}
	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		_ = stdin.Close()
		_ = cmd.Wait()
	}
}

// sqliteDestination is a disposable SQLite destination and its control anchor.
func sqliteDestination(t *testing.T) (string, opgate.Anchor) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "olivares.db")
	anchor, present, err := opgate.AnchorForStoreFile(path)
	if err != nil || !present {
		t.Fatalf("anchor for %s: %v (present=%t)", path, err, present)
	}
	return path, anchor
}

// writeLocalControl installs a durable control at anchor, through the product's
// own exclusive lease and commit path rather than by writing bytes: a fixture that
// hand-rolled the file would not be testing the record this build writes.
func writeLocalControl(t *testing.T, anchor opgate.Anchor, rec opgate.Record) {
	t.Helper()
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil || !ok {
		t.Fatalf("take the anchor to install a control: %v (ok=%t)", err, ok)
	}
	if err := lease.Commit(anchor, rec); err != nil {
		_ = lease.Release()
		t.Fatalf("commit the control: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release after installing the control: %v", err)
	}
}

func localControl(anchor opgate.Anchor, state string) opgate.Record {
	rec := opgate.Record{
		Format:      opgate.Format,
		Revision:    3,
		OpID:        "00112233445566778899aabbccddeeff",
		State:       state,
		Enrolled:    true,
		PlanSHA256:  strings.Repeat("1", 64),
		Destination: opgate.Destination{Engine: "sqlite", CanonicalPath: anchor.Canonical(), SQLiteFile: anchor.Canonical()},
		ObservedAt:  "2026-09-07T12:00:00Z",
	}
	if state == opgate.StateComplete {
		rec.Keyset = drFactualKeyset()
	}
	return rec
}

// sqliteArtefactCensus lists the destination and every sidecar the SQLite driver could
// have created at path. A refusal that happened BEFORE openDB must leave all of them
// absent (F3-IR-9): openSQLite forces its first connection to apply the query pragmas, so
// a fence placed after it has already created the file it was asked to refuse.
func sqliteArtefactCensus(t *testing.T, path string) []string {
	t.Helper()
	var found []string
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); err == nil {
			found = append(found, filepath.Base(path+suffix))
		}
	}
	return found
}

func openSQLiteDestination(t *testing.T, path string) (store.Store, error) {
	t.Helper()
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: path}, nil)
	if st != nil {
		t.Cleanup(func() { _ = st.Close() })
	}
	return st, err
}

// THE ORDINARY CASE, and the one every installation that exists today is in: no
// control, no witness, and a boot that proceeds with every guard it already had.
//
// It is first because it is the control for all the refusals below: without it, a
// fence that refused unconditionally would pass every one of them.
func TestSQLiteOpenProceedsWithNoRestoreControl(t *testing.T) {
	path, anchor := sqliteDestination(t)
	if _, err := os.Stat(anchor.RecordPath()); !os.IsNotExist(err) {
		t.Fatalf("the destination already carries a control before the case starts: %v", err)
	}
	st, err := openSQLiteDestination(t, path)
	if err != nil {
		t.Fatalf("an ordinary SQLite destination with no restore control was refused: %v", err)
	}
	if st == nil {
		t.Fatal("Open returned no store and no error")
	}
	// The fence is released by the time the store is handed back: these locks
	// coordinate construction and are not held by a serving process.
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil || !ok {
		t.Fatalf("after Open returned, the destination was still fenced: %v (ok=%t)", err, ok)
	}
	_ = lease.Release()
}

func TestSQLiteOpenRefusesAControlThatBlocksPublication(t *testing.T) {
	for _, state := range []string{opgate.StatePending, opgate.StateIndeterminate, opgate.StateQuarantined} {
		t.Run(state, func(t *testing.T) {
			path, anchor := sqliteDestination(t)
			writeLocalControl(t, anchor, localControl(anchor, state))
			_, err := openSQLiteDestination(t, path)
			if err == nil {
				t.Fatalf("Open PUBLISHED a store for a destination whose control is %q", state)
			}
			if !errors.Is(err, ErrRestorePublicationFenced) {
				t.Fatalf("Open refused a %q control with the wrong classification: %v", state, err)
			}
			if !strings.Contains(err.Error(), "00112233445566778899aabbccddeeff") {
				t.Fatalf("the refusal does not name the operation holding the destination: %v", err)
			}
		})
	}
}

// A COMPLETE control is the one state that permits publication — and under IR5-C2 it
// permits it only to a caller that can show the custody that operation published.
//
// ⚠ THE PREMISE OF THIS CASE CHANGED, and the change is the contract, not a concession.
// It used to assert that an ordinary Open PUBLISHES on a COMPLETE control. That is
// exactly what the serving constructor must no longer do: a direct Open supplies no
// observation of the signers it loaded, so the one comparison a completed control exists
// for cannot be made. So the case now measures BOTH halves of the live contract — the
// admission that reads the control before any key is loaded publishes, and the direct
// constructor refuses — which is strictly more than the old single assertion.
//
// Without the positive half, every refusal above would be satisfied by a fence that
// refuses everything; without the refusal half, the custody requirement would be
// decoration.
func TestSQLiteCompletedControlPublishesUnderItsAdmissionAndCustody(t *testing.T) {
	path, anchor := sqliteDestination(t)
	keyset := drFactualKeyset()
	rec := localControl(anchor, opgate.StateComplete)
	if rec.Keyset.SHA256 != keyset.SHA256 {
		t.Fatalf("the fixture record does not carry the keyset this case observes: %s vs %s", rec.Keyset.SHA256, keyset.SHA256)
	}
	writeLocalControl(t, anchor, rec)

	// THE REFUSAL HALF, taken first so it cannot create the destination the positive
	// half then opens.
	_, derr := openSQLiteDestination(t, path)
	if derr == nil {
		t.Fatal("an ordinary Open PUBLISHED a completed destination with no observation of the custody it loaded")
	}
	if !errors.Is(derr, ErrRestorePublicationFenced) {
		t.Fatalf("the direct refusal carries the wrong classification: %v", derr)
	}
	if !strings.Contains(derr.Error(), "no observation of the custody it actually loaded") {
		t.Fatalf("the direct refusal does not name the missing measurement: %v", derr)
	}
	if made := sqliteArtefactCensus(t, path); len(made) != 0 {
		t.Fatalf("the refused Open created the destination or its sidecars: %v", made)
	}

	// THE POSITIVE HALF, through the admission that reads this control BEFORE a key
	// could be loaded, with the measurement of the keyset the control authorized.
	ctx := context.Background()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: path}
	adm, aerr := BeginLocalAdmission(ctx, cfg, t.TempDir())
	if aerr != nil {
		t.Fatalf("the admission refused a valid completed control: %v", aerr)
	}
	defer adm.Close()
	req := adm.CustodyRequirement()
	if !req.CustodyRequired() {
		t.Fatal("a COMPLETE local control did not demand custody, so the loaders would have minted for a restored destination")
	}
	if req.KeysetSHA256() != keyset.SHA256 {
		t.Fatalf("the frozen requirement names %s and the control authorized %s", req.KeysetSHA256(), keyset.SHA256)
	}
	if req.OperationID() != rec.OpID || req.PlanSHA256() != rec.PlanSHA256 {
		t.Fatalf("the requirement is not bound to this operation and plan: %+v", req)
	}
	if got := len(req.ExpectedKeys()); got != 3 {
		t.Fatalf("local completed evidence supplied %d expected per-purpose keys, want 3", got)
	}
	st, oerr := adm.Open(ctx, nil, drObservationFor(t, keyset), nil)
	if oerr != nil {
		t.Fatalf("the admission refused to publish under the exact custody its control authorized: %v", oerr)
	}
	if st == nil {
		t.Fatal("the admission returned no store and no error")
	}
	if err := st.Close(); err != nil {
		t.Fatalf("the published store did not close: %v", err)
	}
	// THE FENCE IS THE ADMISSION'S, so giving the admission back is what releases it.
	//
	// Asserting this while the admission is still held would measure the opposite of the
	// property: this package GRANTS a nested request under a lease the same process
	// already owns, so the attempt returns ErrSelfHeld — measured, and it is the rule that
	// keeps a restore from deadlocking against the store it is about to open. Kernel
	// enforcement against a real competitor is proved by the second-process case below.
	adm.Close()
	lease, ok, lerr := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if lerr != nil || !ok {
		t.Fatalf("the destination was still fenced after the admission published and was given back: %v (ok=%t)", lerr, ok)
	}
	_ = lease.Release()
}

// A control that exists and cannot be read is a REFUSAL. Folding it into "absent"
// is how a corrupt or truncated control becomes permission.
func TestSQLiteOpenRefusesAnUnreadableRestoreControl(t *testing.T) {
	path, anchor := sqliteDestination(t)
	writeLocalControl(t, anchor, localControl(anchor, opgate.StateComplete))
	if err := os.WriteFile(anchor.RecordPath(), []byte(`{"format":1,"revision":`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := openSQLiteDestination(t, path)
	if err == nil {
		t.Fatal("Open PUBLISHED a store for a destination whose control could not be read")
	}
	if !errors.Is(err, ErrRestorePublicationFenced) {
		t.Fatalf("an unreadable control produced the wrong classification: %v", err)
	}
	if !strings.Contains(err.Error(), "not an absence") {
		t.Fatalf("the refusal does not distinguish an unreadable control from an absent one: %v", err)
	}
}

// A control bound to ANOTHER destination is refused where it lies, so a control file
// copied along with a data directory cannot speak for the copy.
//
// ⚠ THE FIXTURE CHANGED, and the old one could not prove this. It hand-wrote a
// `"format":1` document with a single-purpose keyset — an obsolete format this build no
// longer writes and a keyset the codec rejects — so the refusal it observed could have
// been a parse failure at the first field, which says nothing about destination binding.
// A fixture whose bytes are invalid for ANY target cannot demonstrate that a VALID
// control is refused for the wrong one.
//
// So the foreign control is now produced by the production codec for a genuinely
// different destination, PROVED valid for its own target first, and only then copied
// byte-for-byte onto the target under test. The assertion stays specific: the refusal
// must name the binding mismatch, not any early failure.
func TestSQLiteOpenRefusesAValidControlCopiedFromAnotherDestination(t *testing.T) {
	victim, victimAnchor := sqliteDestination(t)
	foreign, foreignAnchor := sqliteDestination(t)
	if victimAnchor.Canonical() == foreignAnchor.Canonical() {
		t.Fatal("the fixture produced one destination twice, so nothing foreign is under test")
	}

	// A VALID completed control for the FOREIGN destination, through the same lease and
	// commit path the product uses. Commit refuses a foreign binding itself, which is the
	// first of the two defences; writing it here for its OWN target is what makes these
	// bytes valid.
	keyset := drFactualKeyset()
	writeLocalControl(t, foreignAnchor, localControl(foreignAnchor, opgate.StateComplete))

	// PROOF THE BYTES ARE VALID, not merely well-intentioned: the foreign destination
	// admits them and publishes under the custody they authorize. Without this leg, a
	// refusal at the victim could still be "these bytes are broken everywhere".
	fctx := context.Background()
	fadm, ferr := BeginLocalAdmission(fctx, store.Config{Engine: store.EngineSQLite, DSN: foreign}, t.TempDir())
	if ferr != nil {
		t.Fatalf("the foreign control is not valid for its own destination: %v", ferr)
	}
	if !fadm.CustodyRequirement().CustodyRequired() {
		fadm.Close()
		t.Fatal("the foreign control did not demand custody for its own destination, so it is not the completed control this case needs")
	}
	fst, foerr := fadm.Open(fctx, nil, drObservationFor(t, keyset), nil)
	if foerr != nil {
		fadm.Close()
		t.Fatalf("the foreign destination refused the control written for it: %v", foerr)
	}
	_ = fst.Close()
	fadm.Close()

	// THE COPY: the exact valid bytes, at the victim's control path. This is what a
	// control file that travelled with a copied data directory looks like.
	payload, rerr := os.ReadFile(foreignAnchor.RecordPath())
	if rerr != nil {
		t.Fatalf("read the valid foreign control: %v", rerr)
	}
	if err := os.WriteFile(victimAnchor.RecordPath(), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if again, err := os.ReadFile(victimAnchor.RecordPath()); err != nil || !bytes.Equal(again, payload) {
		t.Fatalf("the copy is not byte-for-byte the valid control: %v", err)
	}

	_, err := openSQLiteDestination(t, victim)
	if err == nil {
		t.Fatal("Open accepted a control bound to another destination")
	}
	if !strings.Contains(err.Error(), "belongs to another destination") {
		t.Fatalf("the refusal does not name the binding mismatch, so it may be any earlier failure: %v", err)
	}
	if made := sqliteArtefactCensus(t, victim); len(made) != 0 {
		t.Fatalf("the refused Open created the destination or its sidecars before refusing: %v", made)
	}
}

// ONE TRY, AGAINST A REAL SECOND PROCESS: an ordinary Open is refused while a
// restore holds the destination, and it does not wait for it.
func TestSQLiteOpenIsFencedOutByAnotherProcess(t *testing.T) {
	path, _ := sqliteDestination(t)
	stop := holdDestinationInAnotherProcess(t, path)
	defer stop()

	start := time.Now()
	_, err := openSQLiteDestination(t, path)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Open PUBLISHED a store for a destination another process holds exclusively")
	}
	if !errors.Is(err, ErrRestorePublicationBusy) {
		t.Fatalf("a fenced destination produced the wrong classification: %v", err)
	}
	// The contract is one try: no polling, no waiting for the holder to finish. The
	// bound is generous — this is a refusal that must not queue, not a latency budget.
	if elapsed > 5*time.Second {
		t.Fatalf("Open took %s to report a fenced destination, which is a wait rather than one try", elapsed)
	}
	// And it recovers the moment the holder lets go: the fence is not sticky.
	stop()
	if _, err := openSQLiteDestination(t, path); err != nil {
		t.Fatalf("after the holder released the destination, Open still refused: %v", err)
	}
}

// An in-memory destination has nothing to fence. This is the shape the console's
// scratch verification and the DR drill use, and a fence that demanded an anchor
// would have broken both.
func TestOpenIsUnaffectedWhenThereIsNoDestinationToFence(t *testing.T) {
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("an in-memory store was refused by the publication fence: %v", err)
	}
	_ = st.Close()
}

// A scratch destination in a throwaway directory — the shape verifyBundleScratch
// and `dr drill` restore into — carries no control and no witness, so it takes the
// legacy_or_lost_unknown branch and opens normally.
func TestAScratchDestinationOpensWithoutAControl(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "scratch-verify.db")
	if _, err := openSQLiteDestination(t, scratch); err != nil {
		t.Fatalf("a throwaway scratch destination was refused: %v", err)
	}
}
