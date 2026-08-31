// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// C4-08: THE HALF OF THE POPULATION THE EXISTING GUARD CANNOT REACH.
//
// TestALostStateRowIsNotAFreshEncounter already covers a control whose state row was lost
// AFTER somebody transitioned it. It plants that history by calling SetRolloutMode first —
// which is not incidental: the guard it exercises reads control_rollout_transitions, so the
// test has to FABRICATE the very evidence the guard depends on before the guard can fire.
//
// This is the same loss on a control that was never transitioned, which is the state every
// control of every fresh install is in. There is no history, so that guard does not fire —
// and by the time the row goes missing the witness table exists, because an earlier boot
// created it, so the re-classification observes "present:tracked" and lands on the LEGACY
// disposition with the enforcement commitment cleared. The row it writes is self-consistent,
// so validateRolloutRow accepts it and every later boot is green.
//
// What that means for the egress control specifically: RolloutEnforced denies when no
// policy is in force, legacy_compat PERMITS, and ensureSeed then draws the grandfathering
// line from the CURRENT subscription set. Every destination an operator had deliberately
// retired starts working again, with no transition, no record and a green boot.
//
// MEASURED before the fix, on this exact shape: BOOT1 classified=enforced witness=absent:virgin,
// history rows = 0, DELETE of the single state row, BOOT2 classified=legacy_compat
// witness=present:tracked and GREEN, BOOT3 stable.
func TestALostStateRowWithNoHistoryIsNotAFreshEncounter(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "lost-nohistory.db")

	// The pre-control binary creates the witness table, exactly as an upgrade would.
	st1, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidget)
	if err != nil {
		t.Fatalf("open as the pre-control binary: %v", err)
	}
	_ = st1.Close()

	// The control-carrying binary classifies. NO SetRolloutMode: this control keeps the
	// disposition it was classified into, which is the whole point.
	st2, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("open as the control-carrying binary: %v", err)
	}
	before := rolloutStateOf(t, st2, testControlKey)
	_ = st2.Close()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	// The precondition that makes this test the one the other is not: no transitions.
	var hist int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM "+dialect.ControlRolloutTransitionTable+" WHERE control_key = ?",
		testControlKey).Scan(&hist); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if hist != 0 {
		t.Fatalf("this test is only meaningful with an EMPTY decision history, found %d rows; "+
			"with history the pre-existing guard fires and nothing here is being measured", hist)
	}
	// Lose ONLY the state row. Witness, tracker and (empty) history all survive.
	if _, err := db.ExecContext(ctx,
		"DELETE FROM "+dialect.ControlRolloutStateTable+" WHERE control_key = ?", testControlKey); err != nil {
		t.Fatalf("delete state row: %v", err)
	}
	_ = db.Close()

	_, err = Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged)
	if err == nil {
		t.Fatalf("a control classified %q, never transitioned, and whose state row was then lost was "+
			"RE-CLASSIFIED on a green boot. The witness table exists because an earlier boot created it, "+
			"so the re-derivation lands on the legacy disposition with the commitment cleared — the "+
			"grandfathered set is redrawn from today's estate and every deliberately retired destination "+
			"starts working again", before.ClassifiedMode)
	}
	// The refusal has to name the RECEIPT, not the history: an operator whose transition log
	// is intact and whose state row is gone must be sent to the relation that actually
	// recorded the classification.
	if !strings.Contains(err.Error(), dialect.ControlRolloutClassificationTable) {
		t.Fatalf("the refusal must name %s, the evidence it rests on; got %v",
			dialect.ControlRolloutClassificationTable, err)
	}
}

// TestTheClassificationReceiptIsBackfilledForAnExistingDatabase covers the leg without which
// the guard above would be armed only where the risk is lowest.
//
// Every deployment provisioned before the receipt relation existed has a state row and no
// receipt. If the receipt were only written by the classification path, those databases —
// the ones with a long enough history to have lost a row — would never acquire one, and the
// new refusal would protect installs from this edition onward and nothing else.
func TestTheClassificationReceiptIsBackfilledForAnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "backfill.db")

	st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	// Simulate the pre-receipt database: the state row is there, the receipt is not. The
	// guard has to be removed first, because the receipt relation is append-only — which is
	// itself worth asserting, since a DELETE that succeeded would mean it is not.
	if _, err := db.ExecContext(ctx,
		"DELETE FROM "+dialect.ControlRolloutClassificationTable); err == nil {
		t.Fatal("the classification receipt accepted a DELETE: its append-only guard is not installed")
	}
	for _, drop := range []string{
		"DROP TRIGGER IF EXISTS " + dialect.ControlRolloutClassificationTable + "_no_update",
		"DROP TRIGGER IF EXISTS " + dialect.ControlRolloutClassificationTable + "_no_delete",
	} {
		if _, err := db.ExecContext(ctx, drop); err != nil {
			t.Fatalf("remove the guard to simulate a pre-receipt database: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM "+dialect.ControlRolloutClassificationTable); err != nil {
		t.Fatalf("clear the receipts: %v", err)
	}
	_ = db.Close()

	// A boot on that database must restore the receipt from what the surviving state row
	// already attests, and must NOT refuse: nothing is wrong with this deployment.
	st2, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("a database with a state row and no receipt must boot and backfill, not refuse: %v", err)
	}
	_ = st2.Close()

	db2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	defer func() { _ = db2.Close() }()
	var mode string
	if err := db2.QueryRowContext(ctx,
		"SELECT classified_mode FROM "+dialect.ControlRolloutClassificationTable+" WHERE control_key = ?",
		testControlKey).Scan(&mode); err != nil {
		t.Fatalf("the receipt was not backfilled: %v", err)
	}
	// It must carry what the state row says, not a value re-derived from today's schema —
	// re-deriving is the failure this whole record prevents.
	var stateMode string
	if err := db2.QueryRowContext(ctx,
		"SELECT classified_mode FROM "+dialect.ControlRolloutStateTable+" WHERE control_key = ?",
		testControlKey).Scan(&stateMode); err != nil {
		t.Fatalf("read state row: %v", err)
	}
	if mode != stateMode {
		t.Fatalf("the backfilled receipt says %q and the state row it was copied from says %q", mode, stateMode)
	}
}
