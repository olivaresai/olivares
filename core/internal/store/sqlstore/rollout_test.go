// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The durable rollout state (unit G). What these tests pin is not that the code
// runs but that the CLASSIFICATION is right in both directions and never restated,
// because a wrong classification is silent: an upgrade misread as fresh breaks an
// estate's existing destinations, and a fresh install misread as an upgrade leaves it
// ungoverned with a record claiming it is being compatible about a history it does not
// have.

const testControlKey = "rrw.staged.v1"

var testRolloutControl = store.RolloutControl{
	Key:          testControlKey,
	WitnessTable: widgetDescriptor.Table,
	LegacyMode:   store.RolloutLegacyCompat,
	FreshMode:    store.RolloutEnforced,
}

// registerWidgetStaged registers the widget table AND a staged control witnessed by
// it — a binary that carries the control.
func registerWidgetStaged(reg store.ExtensionRegistry) error {
	if err := reg.Register(widgetDescriptor); err != nil {
		return err
	}
	return reg.RolloutControl(testRolloutControl)
}

func rolloutStateOf(t *testing.T, st store.Store, key string) store.RolloutState {
	t.Helper()
	rs, ok := st.(store.RolloutStater)
	if !ok {
		t.Fatal("store does not expose store.RolloutStater")
	}
	got, err := rs.RolloutState(context.Background(), key)
	if err != nil {
		t.Fatalf("read rollout state %q: %v", key, err)
	}
	return got
}

// TestFreshDatabaseClassifiesStagedControlEnforced is the whole point of the unit: a
// database that has never held the witness table starts in the control's FRESH mode,
// because it has nothing to grandfather.
func TestFreshDatabaseClassifiesStagedControlEnforced(t *testing.T) {
	st := openSQLiteTest(t, registerWidgetStaged)
	got := rolloutStateOf(t, st, testControlKey)
	if got.CurrentMode != store.RolloutEnforced || got.ClassifiedMode != store.RolloutEnforced {
		t.Fatalf("fresh database classified %q/%q, want %q for both", got.ClassifiedMode, got.CurrentMode, store.RolloutEnforced)
	}
	if got.EnforcementCommitted {
		t.Fatal("a fresh classification must NOT be marked committed: nobody decided it, it was inherited")
	}
	if got.Generation != 1 {
		t.Fatalf("generation %d, want 1 for the seed", got.Generation)
	}
	if got.WitnessKind != "module_table_presence.v1" {
		t.Fatalf("witness kind %q, want the classifier that produced it recorded", got.WitnessKind)
	}
	if got.WitnessDetail != "table:"+widgetDescriptor.Table+":absent:virgin" {
		t.Fatalf("witness detail %q does not record the observation that produced the verdict", got.WitnessDetail)
	}
	if got.ClassifiedAt.IsZero() {
		t.Fatal("classified_at is zero, so the decision cannot be audited after the fact")
	}
}

// TestUpgradeClassifiesStagedControlLegacy walks the REAL upgrade path: a database
// first opened by a binary that did not carry the control (the witness table is
// created, no rollout row exists), then reopened by one that does.
func TestUpgradeClassifiesStagedControlLegacy(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "upgrade.db")

	// The OLD binary: the module's table, no staged control.
	st1, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidget)
	if err != nil {
		t.Fatalf("open as the pre-control binary: %v", err)
	}
	if _, ok := st1.(store.RolloutStater); ok {
		if _, err := st1.(store.RolloutStater).RolloutState(ctx, testControlKey); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a binary that declares no control must classify nothing; got err=%v", err)
		}
	}
	_ = st1.Close()

	// The NEW binary on the same database.
	st2, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("open as the control-carrying binary: %v", err)
	}
	defer st2.Close()

	got := rolloutStateOf(t, st2, testControlKey)
	if got.CurrentMode != store.RolloutLegacyCompat || got.ClassifiedMode != store.RolloutLegacyCompat {
		t.Fatalf("upgrade classified %q/%q, want %q — an estate whose destinations predate the control must not have them switched off by a deploy", got.ClassifiedMode, got.CurrentMode, store.RolloutLegacyCompat)
	}
	if got.WitnessDetail != "table:"+widgetDescriptor.Table+":present:tracked" {
		t.Fatalf("witness detail %q, want the table recorded as present and corroborated", got.WitnessDetail)
	}
}

// TestClassificationIsNeverRestated is the property that makes the record trustworthy.
// The second boot's witness table EXISTS — because the first boot created it — so a
// classification that re-derived would flip a fresh install to legacy on its second
// start, and every subsequent boot would disagree with the first.
func TestClassificationIsNeverRestated(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "restate.db")

	st1, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	first := rolloutStateOf(t, st1, testControlKey)
	_ = st1.Close()

	st2, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer st2.Close()
	second := rolloutStateOf(t, st2, testControlKey)

	if first.CurrentMode != store.RolloutEnforced || second.CurrentMode != first.CurrentMode {
		t.Fatalf("mode moved across a reboot: %q then %q", first.CurrentMode, second.CurrentMode)
	}
	if second.WitnessDetail != first.WitnessDetail {
		t.Fatalf("witness was restated: %q then %q", first.WitnessDetail, second.WitnessDetail)
	}
	if !second.ClassifiedAt.Equal(first.ClassifiedAt) {
		t.Fatalf("classified_at moved: %v then %v", first.ClassifiedAt, second.ClassifiedAt)
	}
	if second.Generation != first.Generation {
		t.Fatalf("generation moved without a transition: %d then %d", first.Generation, second.Generation)
	}
}

// TestRolloutTransitionStateMachine pins the corrected machine: compatibility is a
// CLASSIFICATION and never a target, policy_optional is unreachable once compatibility
// is committed, and a decision without an owner or a reason is refused.
func TestRolloutTransitionStateMachine(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "oneway.db")
	st1, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidget)
	if err != nil {
		t.Fatalf("open as the pre-control binary: %v", err)
	}
	_ = st1.Close()
	st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("open as the control-carrying binary: %v", err)
	}
	defer st.Close()
	rs := st.(store.RolloutStater)

	cur := rolloutStateOf(t, st, testControlKey)
	if cur.CurrentMode != store.RolloutLegacyCompat {
		t.Fatalf("precondition: want %q, got %q", store.RolloutLegacyCompat, cur.CurrentMode)
	}

	// Compatibility is never a TARGET, even before anything is retired: it honors every
	// destination the deployment already had, so entering it deliberately would grant all
	// of them at once.
	if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: testControlKey, Mode: store.RolloutLegacyCompat, Actor: "op", Reason: "why not",
		ExpectGeneration: cur.Generation,
	}); err == nil {
		t.Fatal("compatibility mode was accepted as a transition target")
	}
	// A decision with no recorded reason, or no actor, is refused.
	if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: testControlKey, Mode: store.RolloutEnforced, Actor: "op", ExpectGeneration: cur.Generation,
	}); err == nil {
		t.Fatal("a transition with no reason was accepted")
	}
	if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: testControlKey, Mode: store.RolloutEnforced, Reason: "CHG-1", ExpectGeneration: cur.Generation,
	}); err == nil {
		t.Fatal("a transition with no actor was accepted")
	}
	// A stale generation loses, so two operators approving the same diff cannot both win.
	if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: testControlKey, Mode: store.RolloutEnforced, Actor: "op", Reason: "CHG-1",
		ExpectGeneration: cur.Generation + 7,
	}); err == nil {
		t.Fatal("a stale generation was accepted")
	}

	next, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: testControlKey, Mode: store.RolloutEnforced, Actor: "op", Reason: "CHG-1",
		Evidence: "sha256:deadbeef", ExpectGeneration: cur.Generation,
	})
	if err != nil {
		t.Fatalf("enforce: %v", err)
	}
	if next.CurrentMode != store.RolloutEnforced || !next.EnforcementCommitted {
		t.Fatalf("after enforcing: mode %q committed %v", next.CurrentMode, next.EnforcementCommitted)
	}
	if next.ClassifiedMode != store.RolloutLegacyCompat {
		t.Fatalf("the classification was rewritten to %q; it must stay %q so \"how was this deployment classified?\" remains answerable", next.ClassifiedMode, store.RolloutLegacyCompat)
	}
	if next.Generation != cur.Generation+1 {
		t.Fatalf("generation %d, want %d", next.Generation, cur.Generation+1)
	}
	if next.DecidedBy != "op" || next.DecidedReason != "CHG-1" || next.DecidedAt.IsZero() {
		t.Fatalf("the decision was not recorded: by=%q reason=%q at=%v", next.DecidedBy, next.DecidedReason, next.DecidedAt)
	}

	// Retirement is one-way in the direction that matters: neither compatibility nor the
	// policy-optional relaxation is reachable afterwards.
	for _, mode := range []store.RolloutMode{store.RolloutLegacyCompat, store.RolloutPolicyOptional} {
		if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
			Key: testControlKey, Mode: mode, Actor: "op", Reason: "rollback",
			ExpectGeneration: next.Generation,
		}); err == nil {
			t.Fatalf("a control committed to enforcement was allowed into %q", mode)
		}
	}

	// The history is append-only and complete: the state row carries only the latest
	// decision, so a single mutable row would have erased this.
	hist, err := rs.RolloutHistory(ctx, testControlKey)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("history has %d entries, want 1", len(hist))
	}
	if hist[0].FromMode != store.RolloutLegacyCompat || hist[0].ToMode != store.RolloutEnforced ||
		!hist[0].Committed || hist[0].Actor != "op" || hist[0].Evidence != "sha256:deadbeef" {
		t.Fatalf("history entry does not describe the decision: %+v", hist[0])
	}
}

// TestPolicyOptionalIsReachableBeforeRetirement is the other half of the machine: a
// deployment that has NOT committed to the control can still record that it does not
// want to author one. Refusing that would make the honest posture for a laboratory
// deployment unreachable, which is what pushes operators into working around a product.
func TestPolicyOptionalIsReachableBeforeRetirement(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerWidgetStaged)
	rs := st.(store.RolloutStater)
	cur := rolloutStateOf(t, st, testControlKey)
	if cur.CurrentMode != store.RolloutEnforced || cur.EnforcementCommitted {
		t.Fatalf("precondition: mode %q committed %v", cur.CurrentMode, cur.EnforcementCommitted)
	}
	got, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: testControlKey, Mode: store.RolloutPolicyOptional, Actor: "op", Reason: "single-box lab",
		ExpectGeneration: cur.Generation,
	})
	if err != nil {
		t.Fatalf("fresh install → policy_optional: %v", err)
	}
	if got.CurrentMode != store.RolloutPolicyOptional {
		t.Fatalf("mode %q, want %q", got.CurrentMode, store.RolloutPolicyOptional)
	}
	if got.ClassifiedMode != store.RolloutEnforced {
		t.Fatalf("the classification moved to %q", got.ClassifiedMode)
	}
	// Choosing this posture must NOT commit to enforcement: committing is what forbids
	// this very mode, so setting it here would make the decision undo itself.
	if got.EnforcementCommitted {
		t.Fatal("choosing the policy-optional posture committed the control to enforcement, which forbids that posture")
	}
	if got.DecidedAt.IsZero() || got.DecidedBy != "op" {
		t.Fatalf("the decision was not recorded: at=%v by=%q", got.DecidedAt, got.DecidedBy)
	}
	// And it stays reachable: nothing about choosing it locks it.
	if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: testControlKey, Mode: store.RolloutEnforced, Actor: "op2", Reason: "lab promoted to staging",
		ExpectGeneration: got.Generation,
	}); err != nil {
		t.Fatalf("policy_optional → enforced: %v", err)
	}
}

// TestContradictoryWitnessEvidenceFailsBoot pins the refusal that replaced a guess.
// A database whose module tracker claims the witness is applied while the table is
// gone is a partial restore, and classifying it "fresh" would silently retire every
// entitlement the deployment had — with no record that anything was decided.
func TestContradictoryWitnessEvidenceFailsBoot(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "contradiction.db")
	st1, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidget)
	if err != nil {
		t.Fatalf("open as the pre-control binary: %v", err)
	}
	// Drop the witness table but leave its tracker row: exactly the shape a dump that
	// omitted one table produces.
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	_ = st1.Close()
	if _, err := db.ExecContext(ctx, "DROP TABLE "+widgetDescriptor.Table); err != nil {
		t.Fatalf("drop witness: %v", err)
	}
	_ = db.Close()

	if _, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged); err == nil {
		t.Fatal("a database whose tracker and schema contradict each other was classified anyway")
	}
}

// TestRolloutControlWitnessMustBeRegistered pins the validation that catches a typo.
// A witness table nobody registered would never be found, so every upgrade would
// classify as fresh — the direction that switches an estate's destinations off.
func TestRolloutControlWitnessMustBeRegistered(t *testing.T) {
	ctx := context.Background()
	_, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, func(reg store.ExtensionRegistry) error {
		if err := reg.Register(widgetDescriptor); err != nil {
			return err
		}
		return reg.RolloutControl(store.RolloutControl{
			Key: testControlKey, WitnessTable: "rrw_wigdet", // transposed on purpose
			LegacyMode: store.RolloutLegacyCompat, FreshMode: store.RolloutEnforced,
		})
	})
	if err == nil {
		t.Fatal("a control witnessed by an unregistered table was accepted")
	}
}

// TestRolloutControlWitnessMustBelongToTheOwningModule keeps a control from being
// classified on another module's history: the two can be enabled independently, so a
// borrowed witness would answer a question about the wrong deployment.
func TestRolloutControlWitnessMustBelongToTheOwningModule(t *testing.T) {
	ctx := context.Background()
	other := model.EntityDescriptor{Kind: "zzz.thing", Table: "zzz_thing", Fields: []model.FieldSpec{{Name: "n", Kind: model.KindInt}}}
	_, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, func(reg store.ExtensionRegistry) error {
		if err := reg.Register(other); err != nil {
			return err
		}
		return reg.RolloutControl(store.RolloutControl{
			Key: testControlKey, WitnessTable: other.Table,
			LegacyMode: store.RolloutLegacyCompat, FreshMode: store.RolloutEnforced,
		})
	})
	if err == nil {
		t.Fatal("a control witnessed by another module's table was accepted")
	}
}

// TestRolloutStateMissingRowIsNotFound proves the reading a caller must treat as
// UNAVAILABLE is distinguishable. A control this binary knows but the database was
// never classified for must not read as "no control in force".
func TestRolloutStateMissingRowIsNotFound(t *testing.T) {
	st := openSQLiteTest(t, registerWidgetStaged)
	rs := st.(store.RolloutStater)
	if _, err := rs.RolloutState(context.Background(), "rrw.other.v1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unclassified control returned %v, want store.ErrNotFound", err)
	}
}

// TestStagedControlClassificationOnPostgres runs the same two classifications against
// the real engine, because the witness probe goes through per-dialect introspection
// and information_schema is scoped to the current schema — a difference SQLite cannot
// expose.
func TestStagedControlClassificationOnPostgres(t *testing.T) {
	ctx := context.Background()
	fresh := isolatedPG(t).App
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: fresh, MaxConns: 4}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("open fresh postgres: %v", err)
	}
	got := rolloutStateOf(t, st, testControlKey)
	_ = st.Close()
	if got.CurrentMode != store.RolloutEnforced {
		t.Fatalf("fresh postgres classified %q, want %q", got.CurrentMode, store.RolloutEnforced)
	}

	upgrade := isolatedPG(t).App
	st1, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: upgrade, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open postgres as the pre-control binary: %v", err)
	}
	_ = st1.Close()
	st2, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: upgrade, MaxConns: 4}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("reopen postgres as the control-carrying binary: %v", err)
	}
	defer st2.Close()
	if got := rolloutStateOf(t, st2, testControlKey); got.CurrentMode != store.RolloutLegacyCompat {
		t.Fatalf("postgres upgrade classified %q, want %q", got.CurrentMode, store.RolloutLegacyCompat)
	}
}

// TestALostStateRowIsNotAFreshEncounter is the CRITICAL hole an adversarial review of this unit
// found. After any successful boot the witness exists and is tracked, so a database that lost only
// its rollout state row would be re-classified into compatibility at generation 1 with the
// commitment cleared — and every grandfathered destination an operator had deliberately retired
// would start working again, with no transition and a green boot.
func TestALostStateRowIsNotAFreshEncounter(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "lost.db")
	st1, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidget)
	if err != nil {
		t.Fatalf("open as the pre-control binary: %v", err)
	}
	_ = st1.Close()
	st2, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("open as the control-carrying binary: %v", err)
	}
	cur := rolloutStateOf(t, st2, testControlKey)
	if _, err := st2.(store.RolloutStater).SetRolloutMode(ctx, store.RolloutTransition{
		Key: testControlKey, Mode: store.RolloutEnforced, Actor: "op", Reason: "CHG-9",
		ExpectGeneration: cur.Generation,
	}); err != nil {
		t.Fatalf("enforce: %v", err)
	}
	_ = st2.Close()

	// Lose ONLY the state row. The witness, the tracker and the decision history all survive.
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM control_rollout_state WHERE control_key = ?", testControlKey); err != nil {
		t.Fatalf("delete state row: %v", err)
	}
	_ = db.Close()

	if _, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged); err == nil {
		t.Fatal("a control whose state row was lost but whose decision history survived was re-classified as new: a one-way decision would have been silently undone")
	}
}

// TestATruncatedHistoryIsRefused is the other direction: the state says decisions happened and the
// history cannot account for them.
//
// THE PREMISE OF THIS TEST CHANGED, and the sentence that used to be here — "since the history has
// no database-level immutability, this cross-check is what makes append-only mean anything at
// all" — is no longer true. control_rollout_transitions now carries the same immutability guard
// every other append-only relation does, emitted idempotently at boot, so the plain DELETE this
// test used to issue is REFUSED by the database: measured, `constraint failed:
// control_rollout_transitions is append-only`. That refusal is the fix working, not a broken test.
//
// The cross-check still matters and is still what this asserts, because the guard is not the only
// way a history goes missing: a partial restore, or a drop-and-recreate, takes the guard with the
// rows. So the drift is now planted the way such an actor would produce it — by removing the guard
// first — and the property under test is unchanged: a state row claiming decisions the history
// cannot account for must fail the boot.

func TestATruncatedHistoryIsRefused(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "truncated.db")
	st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cur := rolloutStateOf(t, st, testControlKey)
	if _, err := st.(store.RolloutStater).SetRolloutMode(ctx, store.RolloutTransition{
		Key: testControlKey, Mode: store.RolloutEnforced, Actor: "op", Reason: "CHG-9",
		ExpectGeneration: cur.Generation,
	}); err != nil {
		t.Fatalf("enforce: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	// The guard is real, so it has to be removed before the rows can be. Asserted rather than
	// assumed: if the DELETE ever starts succeeding without this step, the guard has silently
	// stopped being emitted and this test would otherwise keep passing for the wrong reason.
	if _, err := db.ExecContext(ctx, "DELETE FROM control_rollout_transitions WHERE control_key = ?", testControlKey); err == nil {
		t.Fatal("the decision history accepted a DELETE: its append-only guard is not installed")
	}
	for _, drop := range []string{
		"DROP TRIGGER IF EXISTS control_rollout_transitions_no_update",
		"DROP TRIGGER IF EXISTS control_rollout_transitions_no_delete",
	} {
		if _, err := db.ExecContext(ctx, drop); err != nil {
			t.Fatalf("remove the guard to plant the drift: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM control_rollout_transitions WHERE control_key = ?", testControlKey); err != nil {
		t.Fatalf("truncate history: %v", err)
	}
	_ = db.Close()

	if _, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged); err == nil {
		t.Fatal("a control whose decision history was truncated was accepted; its decisions can no longer be accounted for")
	}
}

// TestAnImpossibleStateRowIsRefused pins the combinations the state machine cannot produce.
// Compatibility is only ever ARRIVED AT by classification, so a row sitting in it at a later
// generation, or one classified enforced, is claiming a history this engine cannot have written —
// and accepting it would serve compatibility, with its whole grandfathered set, to a deployment
// that was never classified into it.
func TestAnImpossibleStateRowIsRefused(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]string{
		"classified enforced but sitting in compatibility": "UPDATE control_rollout_state SET current_mode = 'legacy_compat' WHERE control_key = ?",
		"compatibility at a later generation":              "UPDATE control_rollout_state SET classified_mode = 'legacy_compat', current_mode = 'legacy_compat', generation = 4 WHERE control_key = ?",
		"policy_optional with nobody having decided":       "UPDATE control_rollout_state SET current_mode = 'policy_optional' WHERE control_key = ?",
	} {
		dsn := filepath.Join(t.TempDir(), "impossible.db")
		st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged)
		if err != nil {
			t.Fatalf("%s: open: %v", name, err)
		}
		_ = st.Close()
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("%s: reopen raw: %v", name, err)
		}
		if _, err := db.ExecContext(ctx, mutate, testControlKey); err != nil {
			t.Fatalf("%s: mutate: %v", name, err)
		}
		_ = db.Close()
		if _, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, registerWidgetStaged); err == nil {
			t.Errorf("%s: the row was accepted", name)
		}
	}
}

// TestASiblingTableContradictsAMissingWitness covers the no-tracker branch, which previously
// classified on the witness alone and admitted in a comment that it could not tell "predates the
// tracker" from "brand new". A module's tables are created together, so a sibling without the
// witness is a restore that dropped one.
func TestASiblingTableContradictsAMissingWitness(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "sibling.db")
	sibling := model.EntityDescriptor{
		Kind: "rrw.event", Table: "rrw_event",
		Fields: []model.FieldSpec{{Name: "n", Kind: model.KindInt}},
	}
	// A database that ran the module (its sibling exists) but whose witness is absent, with no
	// module tracker — the pre-tracker restore shape.
	st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, func(reg store.ExtensionRegistry) error {
		return reg.Register(sibling)
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = st.Close()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE "+moduleTablesTracking); err != nil {
		t.Fatalf("drop tracker: %v", err)
	}
	_ = db.Close()

	if _, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, func(reg store.ExtensionRegistry) error {
		if err := reg.Register(sibling); err != nil {
			return err
		}
		if err := reg.Register(widgetDescriptor); err != nil {
			return err
		}
		return reg.RolloutControl(testRolloutControl)
	}); err == nil {
		t.Fatal("a database holding the witness's sibling but not the witness was classified anyway")
	}
}
