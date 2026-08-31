// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardrollout_pg_test.go is the leg that decides whether this campaign produced a PRODUCT or
// a library.
//
// Everything else can pass with retryUnit never running: the manifest, the plan, the ledger and
// the fold are all testable without it. What these tests assert is the thing the previous
// branch could NOT claim — that the real Open reaches the real runner, executes real DDL, and
// leaves a receipt that a second boot recognizes.
//
// THE MUTATION THAT MUST TURN THIS RED, and it is the whole point of the file: delete the
// runAppendOnlyGuardUnits call from store.go. Nothing else changes — the control plane is still
// bootstrapped by v6, the ledger still verifies, every other test in the package still passes.
// Measured with the call removed:
//
//	after boot 2 of 5 guards are not ALWAYS: [audit_events=O set_seen_jtis=O]
//	GUARD_STATES_AFTER_BOOT|guards=5|always=3
//	GUARD_RECEIPTS|unit=0|adopt=0|transition=0
//
// AND ONE TEST HERE DOES NOT DISCRIMINATE IT, which is worth saying out loud rather than
// leaving for somebody to discover: TestPostgresSecondOpenIsVerifyOnly PASSED under that
// mutation. With no units, nothing changes between two boots either — so it pins idempotence
// and says nothing about whether the runner is wired. The test that carries that claim is
// TestPostgresOpenDrivesTheGuardRolloutToReady, on the receipts and the guard states. A test
// that asserts less than its name suggests is worse than one that is absent.

// guardPGProbe opens a raw connection to an isolated database for reading the catalog back.
func guardPGProbe(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open a probe connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// guardEnableStates reads tgenabled for every append-only guard in the schema.
func guardEnableStates(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
SELECT c.relname, t.tgenabled::text
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
WHERE n.nspname = $1 AND NOT t.tgisinternal AND p.proname = $2
ORDER BY 1`, dialect.EngineSchema, guardBlockMutationFn)
	if err != nil {
		t.Fatalf("read guard states: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var table, state string
		if err := rows.Scan(&table, &state); err != nil {
			t.Fatalf("scan guard state: %v", err)
		}
		out[table] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read guard states: %v", err)
	}
	return out
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

// TestPostgresOpenDrivesTheGuardRolloutToReady is the headline regression.
//
// It asserts the PRODUCT property first — after boot, every append-only guard is ALWAYS — and
// then the evidence that the rollout is what produced it. The order matters: 'A' is the state
// certified on 15.18 to make a publisher UPDATE fail on a logical-replication subscriber
// instead of applying with zero errors, so it is the thing an operator is buying.
func TestPostgresOpenDrivesTheGuardRolloutToReady(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	probe := guardPGProbe(t, dsns.App)

	// 1. THE PRODUCT PROPERTY. Every guard the engine installed is ALWAYS, including the three
	// the control plane installed on itself.
	states := guardEnableStates(t, probe)
	if len(states) == 0 {
		t.Fatal("no append-only guards found at all, so this test would pass vacuously")
	}
	var origin []string
	for table, state := range states {
		if state != guardStateAlways {
			origin = append(origin, table+"="+state)
		}
	}
	if len(origin) > 0 {
		t.Errorf("after boot %d of %d guards are not ALWAYS: %v — at 'O' a logical-replication apply mutates evidence in silence",
			len(origin), len(states), origin)
	}
	t.Logf("GUARD_STATES_AFTER_BOOT|guards=%d|always=%d", len(states), len(states)-len(origin))

	// 2. THE EVIDENCE. One receipt per edge, and the edges are the ones the plan derived: the
	// engine's DDL creates guards at ORIGIN, so every target needs adoption AND the transition.
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	unitReceipts := countRows(t, probe, "SELECT COUNT(*) FROM "+dialect.GuardReceiptsTable+" WHERE receipt_kind = 'unit'")
	adoptions := countRows(t, probe, "SELECT COUNT(*) FROM "+dialect.GuardReceiptsTable+" WHERE intent = $1", string(intentAdoptLegacy))
	transitions := countRows(t, probe, "SELECT COUNT(*) FROM "+dialect.GuardReceiptsTable+" WHERE intent = $1", string(intentTransitionLegacyOToA))
	t.Logf("GUARD_RECEIPTS|unit=%d|adopt=%d|transition=%d", unitReceipts, adoptions, transitions)
	if unitReceipts == 0 {
		t.Fatal("the boot left NO unit receipts: the runner has no production caller, or it was never reached")
	}
	if adoptions == 0 || transitions == 0 {
		t.Errorf("adoptions=%d transitions=%d; a guard created at ORIGIN needs both edges", adoptions, transitions)
	}
	if adoptions != transitions {
		t.Errorf("adoptions=%d but transitions=%d; every target created at ORIGIN takes both edges", adoptions, transitions)
	}
	if unitReceipts != adoptions+transitions {
		t.Errorf("unit receipts=%d but adopt+transition=%d", unitReceipts, adoptions+transitions)
	}

	// The transition's receipt records where it came FROM, which is the fact the catalog no
	// longer shows: the object reads 'A' now and 'O' survives only in the ledger.
	var fromStates []string
	rows, err := probe.QueryContext(ctx, "SELECT DISTINCT from_enable_state FROM "+dialect.GuardReceiptsTable+
		" WHERE intent = $1", string(intentTransitionLegacyOToA))
	if err != nil {
		t.Fatalf("read from-states: %v", err)
	}
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan from-state: %v", err)
		}
		fromStates = append(fromStates, s.String)
	}
	_ = rows.Close()
	if len(fromStates) != 1 || fromStates[0] != guardStateOrigin {
		t.Errorf("the transitions record from-states %v, want exactly [%q]", fromStates, guardStateOrigin)
	}

	// 3. THE GATE CLOSED, and it closed by an event rather than by a flag.
	m, err := buildGuardManifest(registryWithWidget(t).appendOnlyTables())
	if err != nil {
		t.Fatalf("build the manifest: %v", err)
	}
	empty, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	rolloutID, err := guardRolloutID(m.Format, m.CodeEpoch, m.CodeSHA256, 0, empty)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := foldGateEvents(ctx, probe, dia, rolloutID)
	if err != nil {
		t.Fatalf("fold the gate: %v", err)
	}
	if !gate.Found {
		t.Fatalf("no rollout %s in the gate; the coordinator computed a different identity than the test", rolloutID)
	}
	if gate.Phase != gatePhaseReady || gate.Condition != gateConditionVerified {
		t.Errorf("the gate is %s/%s after a clean boot, want ready/verified (%s: %s)",
			gate.Phase, gate.Condition, gate.FirstBlocking.Code, gate.FirstBlocking.Details)
	}
	if gate.mayMutate() {
		t.Error("a closed rollout authorizes mutation")
	}
	if len(gate.ExpectedUnits) != unitReceipts {
		t.Errorf("the gate enumerates %d units and the ledger holds %d unit receipts",
			len(gate.ExpectedUnits), unitReceipts)
	}
	// Every enumerated unit was judged, and its judged reading is reconstructible — which is
	// what a later boot reasons from once the catalog shows the poststate.
	for _, unitID := range gate.ExpectedUnits {
		fold, ok := gate.Units[unitID]
		if !ok {
			t.Errorf("unit %s is enumerated but absent from the fold", unitID)
			continue
		}
		if fold.State != unitGateJudged {
			t.Errorf("unit %s folded to %q, want %q", unitID, fold.State, unitGateJudged)
		}
		if !fold.JudgedReadingValid {
			t.Errorf("unit %s has no reconstructible judged reading", unitID)
		}
	}

	// 4. THE CHAINS VERIFY, all three of them.
	if _, _, err := verifyInventoryChain(ctx, probe, dia, m); err != nil {
		t.Errorf("the inventory chain does not verify: %v", err)
	}
	if _, err := guardRolloutReceipts(ctx, probe, dia, rolloutID); err != nil {
		t.Errorf("the receipt chain does not verify: %v", err)
	}
}

// TestPostgresSecondOpenIsVerifyOnly pins that a boot with nothing to do changes nothing.
//
// The measurement is the EVENT COUNT rather than an instrumented query counter, and that is
// deliberate: a unit that ran would append attempt-started and attempt-judged events, so an
// unchanged gate log IS the proof that zero units ran on the SECOND boot. A coordinator that
// re-executed and happened to be idempotent would still leave events behind.
//
// WHAT IT DOES NOT PROVE, measured rather than assumed: it passes when the production call is
// removed entirely, because with no units the two boots are trivially identical (`events=0->0`).
// So this pins idempotence and NOT the wiring. The wiring is
// TestPostgresOpenDrivesTheGuardRolloutToReady's claim.
func TestPostgresSecondOpenIsVerifyOnly(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	st1, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	probe := guardPGProbe(t, dsns.App)
	events1 := countRows(t, probe, "SELECT COUNT(*) FROM "+dialect.GuardGateEventsTable)
	receipts1 := countRows(t, probe, "SELECT COUNT(*) FROM "+dialect.GuardReceiptsTable)
	inventory1 := countRows(t, probe, "SELECT COUNT(*) FROM "+dialect.GuardInventoryEventsTable)
	if err := st1.Close(); err != nil {
		t.Fatalf("close the first store: %v", err)
	}

	st2, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	events2 := countRows(t, probe, "SELECT COUNT(*) FROM "+dialect.GuardGateEventsTable)
	receipts2 := countRows(t, probe, "SELECT COUNT(*) FROM "+dialect.GuardReceiptsTable)
	inventory2 := countRows(t, probe, "SELECT COUNT(*) FROM "+dialect.GuardInventoryEventsTable)

	t.Logf("GUARD_SECOND_OPEN_PG|events=%d->%d|receipts=%d->%d|inventory=%d->%d",
		events1, events2, receipts1, receipts2, inventory1, inventory2)
	if events2 != events1 {
		t.Errorf("the second boot appended %d gate events; a closed rollout is VERIFY-only, so a unit that ran would have left attempt events",
			events2-events1)
	}
	if receipts2 != receipts1 {
		t.Errorf("the second boot wrote %d receipts", receipts2-receipts1)
	}
	if inventory2 != inventory1 {
		t.Errorf("the second boot wrote %d inventory events", inventory2-inventory1)
	}
}

// TestPostgresBootRefusesWhenAGuardWasRemovedUnderReady pins that drift does NOT get laundered.
//
// After a closed rollout, dropping a guard is exactly the sabotage this machinery exists to
// catch. The wrong behavior is seductive: re-create it and carry on, which leaves a green boot
// and no record that anything was ever removed. The right behavior is to refuse — and to
// refuse for the RECORDED reason rather than a fresh guess.
func TestPostgresBootRefusesWhenAGuardWasRemovedUnderReady(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	st1, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close the first store: %v", err)
	}
	probe := guardPGProbe(t, dsns.App)

	// Drop the guard on the evidence ledger itself. The application role owns it in this
	// topology, which is the honest limit this test also documents: a single-role deployment
	// CAN do this, and what the engine gives is detection at the next boot.
	if _, err := probe.ExecContext(ctx,
		"DROP TRIGGER audit_events"+guardTriggerSuffix+" ON audit_events"); err != nil {
		t.Fatalf("drop the guard: %v", err)
	}

	_, err = Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err == nil {
		t.Fatal("the boot succeeded with a guard removed: the drift was laundered rather than reported")
	}
	t.Logf("GUARD_DRIFT_REFUSAL|err=%v", err)
	// The refusal must be about the GUARD, not an incidental failure somewhere else.
	if !strings.Contains(err.Error(), "audit_events") {
		t.Errorf("the refusal does not name the affected relation: %v", err)
	}

	// And it is recorded durably, so the next boot finds the same diagnosis rather than
	// re-deriving it.
	blocked := countRows(t, probe, "SELECT COUNT(*) FROM "+dialect.GuardGateEventsTable+
		" WHERE kind = $1", string(gateEventVerificationFailed))
	if blocked == 0 {
		t.Error("the refusal left no verification-failed event, so the next boot would have to re-derive it")
	}

	// AND THE DRIFT DOES NOT EXPIRE. Put the guard back exactly as it was — which is what a
	// well-meaning operator would do — and the boot must STILL refuse, on the strength of what
	// the ledger recorded rather than of what the object looks like now.
	//
	// This is the property that separates detection from laundering. The drift is the event; a
	// guard that looks correct again does not unmake it. Clearing it is a deliberate human
	// decision, which is why the durable diagnostic carries unblock_policy=operator.
	if _, err := probe.ExecContext(ctx,
		"CREATE TRIGGER audit_events"+guardTriggerSuffix+
			" BEFORE UPDATE OR DELETE ON audit_events FOR EACH ROW EXECUTE FUNCTION "+guardBlockMutationFn+"()"); err != nil {
		t.Fatalf("restore the guard: %v", err)
	}
	if _, err := probe.ExecContext(ctx,
		"ALTER TABLE ONLY audit_events ENABLE ALWAYS TRIGGER audit_events"+guardTriggerSuffix); err != nil {
		t.Fatalf("restore the guard's state: %v", err)
	}
	_, err = Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err == nil {
		t.Fatal("the boot succeeded once the guard was put back: a recorded drift expired on its own, which is laundering with extra steps")
	}
	t.Logf("GUARD_DRIFT_STICKY|err=%v", err)
	if !errors.Is(err, ErrGuardGateBlocked) {
		t.Errorf("the third boot refused with %v, which is not the named blocked error — so it refused by re-deriving the drift rather than by the recorded condition", err)
	}
}

// TestPostgresCheckpointCatchesARemovedReceipt is F-02's discriminating regression.
//
// A chain authenticates each row against its predecessor, so the LAST row of a stream has nothing
// behind it: remove it and what remains is a shorter, cryptographically perfect chain. Before the
// closing event carried a checkpoint, deleting the last receipt of a closed rollout was therefore
// undetectable — every digest still recomputed, and the boot carried on.
//
// The checkpoint gives that last row a successor in a different stream. This test deletes it and
// requires the next boot to refuse.
func TestPostgresCheckpointCatchesARemovedReceipt(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	st1, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	probe := guardPGProbe(t, dsns.App)

	// FIRST, THE PRODUCT PROPERTY THIS TEST DISCOVERED. The application role CANNOT delete a
	// receipt: the boot's append-only reconcile revokes UPDATE/DELETE/TRUNCATE on these relations
	// because the live catalog discovers them by their immutability trigger. Measured — the first
	// version of this test failed with SQLSTATE 42501 attempting exactly that.
	//
	// So the deletion below needs a role the deployment does not run as, and asserting the refusal
	// first is what keeps this test from silently becoming easier if that ever changes.
	before := countRows(t, probe, "SELECT COUNT(*) FROM "+dialect.GuardReceiptsTable)
	if _, derr := probe.ExecContext(ctx, "DELETE FROM "+dialect.GuardReceiptsTable); derr == nil {
		t.Fatal("the application role deleted a receipt; the append-only ACL on the control plane is open")
	} else if !strings.Contains(derr.Error(), "42501") && !strings.Contains(derr.Error(), "permission denied") {
		t.Errorf("the application role's DELETE was refused for the wrong reason: %v", derr)
	}

	// SECOND, THE OTHER LAYER, also discovered by running this. Even the SUPERUSER cannot delete a
	// receipt: the immutability trigger is in ALWAYS, and ALWAYS fires for every role including
	// one that bypasses ACLs entirely. Measured — the second version of this test failed with
	//
	//	ERROR: table is append-only (SQLSTATE P0001)
	//
	// That is precisely what the O -> A transition buys, and it means the checkpoint is the THIRD
	// layer rather than the first: to remove a receipt at all, an attacker must first disarm the
	// guard whose whole purpose is to stop them.
	super := guardPGProbe(t, dsns.Superuser)
	if _, derr := super.ExecContext(ctx, "DELETE FROM "+dialect.GuardReceiptsTable); derr == nil {
		t.Fatal("the superuser deleted a receipt without disarming the guard; ENABLE ALWAYS is not in force")
	} else if !strings.Contains(derr.Error(), "append-only") {
		t.Errorf("the superuser's DELETE was refused for the wrong reason: %v", derr)
	}

	// THIRD, the attacker who does the work: disarm the guard, delete, put it back. This is the
	// scenario the checkpoint exists for — every remaining digest recomputes, and without an
	// attestation of the head and count the shorter history is indistinguishable from the real one.
	for _, stmt := range []string{
		"ALTER TABLE " + dialect.GuardReceiptsTable + " DISABLE TRIGGER " + dialect.GuardReceiptsTable + guardTriggerSuffix,
		"DELETE FROM " + dialect.GuardReceiptsTable +
			" WHERE event_ordinal = (SELECT MAX(event_ordinal) FROM " + dialect.GuardReceiptsTable + ")",
		"ALTER TABLE ONLY " + dialect.GuardReceiptsTable + " ENABLE ALWAYS TRIGGER " + dialect.GuardReceiptsTable + guardTriggerSuffix,
	} {
		if _, err := super.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("as the superuser, %.60s...: %v", stmt, err)
		}
	}
	after := countRows(t, probe, "SELECT COUNT(*) FROM "+dialect.GuardReceiptsTable)
	if after != before-1 {
		t.Fatalf("the deletion did not happen: %d -> %d receipts", before, after)
	}

	_, err = Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err == nil {
		t.Fatal("the boot succeeded with the last receipt deleted: a removed tail was accepted as an intact history")
	}
	t.Logf("GUARD_CHECKPOINT_CATCHES_TAIL|receipts=%d->%d|err=%v", before, after, err)
	if !errors.Is(err, ErrGuardGateChainBroken) {
		t.Errorf("the refusal was %v, which is not the named chain error — so it refused for some other reason and this test would pass without a checkpoint", err)
	}
	if !strings.Contains(err.Error(), "attested") {
		t.Errorf("the refusal does not mention what was attested: %v", err)
	}
}

// TestPostgresGuardControlPlaneIsCanonicalAndAlways pins the control plane's own posture.
//
// The bootstrap receipt states what the migration created; this reads the catalog back. Without
// it the receipt would be self-certifying — a row asserting that the objects it created are
// correct, with nothing having looked.
func TestPostgresGuardControlPlaneIsCanonicalAndAlways(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	probe := guardPGProbe(t, dsns.App)
	conn, err := probe.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a connection: %v", err)
	}
	defer func() { _ = conn.Close() }()

	manifest, err := buildGuardManifest(newRegistryForTest(t).appendOnlyTables())
	if err != nil {
		t.Fatalf("build the manifest this boot ran with: %v", err)
	}
	if err := verifyGuardControlPlaneObjects(ctx, conn, dia, manifest); err != nil {
		t.Errorf("the control plane's own guards are not what its bootstrap receipt claims: %v", err)
	}

	// And the same reading through the shared projection, so the decoder itself is exercised
	// against a real catalog rather than only against a hand-built row.
	specs, err := guardMetadataSpecs(guardManifestFormat)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		row, perr := projectGuardCatalogRow(ctx, conn, spec.Key)
		if perr != nil {
			t.Fatalf("project %s: %v", spec.Key, perr)
		}
		canonical, diff := row.matchesCanonical(spec)
		if !canonical {
			t.Errorf("%s is not canonical against the manifest: %v", spec.Key, diff)
		}
		if row.EnableState != guardStateAlways {
			t.Errorf("%s is in state %q, want %q", spec.Key, row.EnableState, guardStateAlways)
		}
	}
}

// TestPostgresBulkProjectionReturnsOneRowPerTarget exercises the wide query directly.
//
// Three properties in one: the array binding works through pgx (a []string must reach
// PostgreSQL as text[]), the ordinal comes back dense and in order, and a target that does NOT
// exist still produces a row. The last one is the one a naive query gets wrong — and it would
// make "the relation is absent" indistinguishable from "the batch lost a row".
func TestPostgresBulkProjectionReturnsOneRowPerTarget(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	probe := guardPGProbe(t, dsns.App)
	conn, err := probe.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a connection: %v", err)
	}
	defer func() { _ = conn.Close() }()

	keys := []guardKey{
		{Schema: guardSchema, Relation: "audit_events", Trigger: "audit_events" + guardTriggerSuffix},
		{Schema: guardSchema, Relation: "no_such_relation", Trigger: "no_such_relation" + guardTriggerSuffix},
		{Schema: guardSchema, Relation: dialect.GuardReceiptsTable, Trigger: dialect.GuardReceiptsTable + guardTriggerSuffix},
	}
	got, err := projectGuardCatalogBatch(ctx, conn, keys)
	if err != nil {
		t.Fatalf("the bulk projection failed: %v", err)
	}
	if len(got) != len(keys) {
		t.Fatalf("the batch returned %d rows for %d targets", len(got), len(keys))
	}
	present := got[keys[0]]
	if !present.RelationExists || !present.GuardExists || !present.FunctionExists {
		t.Errorf("audit_events came back as relation=%v guard=%v function=%v",
			present.RelationExists, present.GuardExists, present.FunctionExists)
	}
	if present.EnableState != guardStateAlways {
		t.Errorf("audit_events came back in state %q", present.EnableState)
	}
	absent := got[keys[1]]
	if absent.RelationExists || absent.GuardExists {
		t.Errorf("a relation that does not exist came back as relation=%v guard=%v",
			absent.RelationExists, absent.GuardExists)
	}
	// The absent target still carries its KEY, which is what lets the plan match it up: a row
	// with no identity would be indistinguishable from a lost one.
	if absent.Key != keys[1] {
		t.Errorf("the absent target came back keyed %s, want %s", absent.Key, keys[1])
	}

	// A duplicate is refused rather than silently collapsed, because the plan's cardinality is
	// checked against the row count.
	if _, err := projectGuardCatalogBatch(ctx, conn, []guardKey{keys[0], keys[0]}); err == nil {
		t.Error("a duplicated target was accepted")
	}
}

// TestPostgresGuardPreflightIgnoresTheRolloutControlRelations pins the permitted-predecessor
// rule against a real database.
//
// The two relations #448 creates are present on EVERY database that reaches this preflight,
// including a completely fresh one — so a preflight that treated any pre-existing Olivares
// relation as evidence of a partial C4 bootstrap would refuse every boot. The property is
// asserted where it can actually be wrong: after a real boot, with those relations present and
// the control plane complete.
func TestPostgresGuardPreflightIgnoresTheRolloutControlRelations(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	probe := guardPGProbe(t, dsns.App)
	conn, err := probe.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a connection: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Both permitted predecessors really are there — otherwise this test would assert the rule
	// against a database where it cannot be exercised.
	for _, table := range guardPermittedPredecessors() {
		cols, cerr := dia.TableColumns(ctx, conn, table)
		if cerr != nil {
			t.Fatalf("probe %s: %v", table, cerr)
		}
		if len(cols) == 0 {
			t.Fatalf("%s is absent, so the permitted-predecessor rule is not being exercised", table)
		}
	}
	disposition, err := preflightGuardControlPlane(ctx, conn, dia)
	if err != nil {
		t.Fatalf("the preflight refused a complete control plane: %v", err)
	}
	if disposition != guardBootstrapComplete {
		t.Errorf("the preflight reports %q, want %q", disposition, guardBootstrapComplete)
	}

	// And the all-or-none refusal is real: drop ONE of the three and the preflight must refuse
	// rather than let v6 recreate it. (migrate.Apply would skip v6 forever, so a preflight that
	// allowed this would leave a database whose ledger silently does not exist.)
	if _, err := probe.ExecContext(ctx, "DROP TABLE "+dialect.GuardInventoryEventsTable); err != nil {
		t.Fatalf("drop one control-plane relation: %v", err)
	}
	if _, err := preflightGuardControlPlane(ctx, conn, dia); err == nil {
		t.Fatal("the preflight accepted a control plane missing one of its three relations")
	} else if !errors.Is(err, ErrGuardControlPlaneBootstrapInconsistent) {
		t.Errorf("refused with %v, which is not the named all-or-none error", err)
	}
}

// registryWithWidget mirrors the registry the PG tests' Open builds: core plus the widget the
// shared registerWidget helper adds.
//
// It exists so a test derives the census the SAME way production does rather than reading it
// back from the database under test.
func registryWithWidget(t *testing.T) *registry {
	t.Helper()
	reg := newRegistryForTest(t)
	reg.closed = false
	if err := registerWidget(reg); err != nil {
		t.Fatalf("register the widget descriptor: %v", err)
	}
	reg.closed = true
	return reg
}

// TestPostgresGuardPreflightRefusesARelationThatIsNotTheDeclaredOne is the F-08 preflight half,
// against a real catalog.
//
// The scenario it closes is exactly the one the old probe accepted: version 6 is RECORDED and
// all three relations are PRESENT, so the migration will be skipped forever — and one of them
// has quietly lost a property the ledger's whole argument rests on. Every case below is a
// single ALTER an operator, a restore tool or a schema-diff product could plausibly perform.
//
// Each case gets its own isolated database because the mutation is not reversible cheaply and
// because a shared one would let the first refusal mask the rest.
func TestPostgresGuardPreflightRefusesARelationThatIsNotTheDeclaredOne(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter string
		want  string
	}{
		{
			name:  "a lost uniqueness on the gate's ordinal",
			alter: "ALTER TABLE " + dialect.GuardGateEventsTable + " DROP CONSTRAINT " + dialect.GuardGateEventsTable + "_rollout_id_event_ordinal_key",
			want:  "uniquely-indexed tuple",
		},
		{
			name:  "a lost uniqueness on the inventory's ordinal",
			alter: "ALTER TABLE " + dialect.GuardInventoryEventsTable + " DROP CONSTRAINT " + dialect.GuardInventoryEventsTable + "_event_ordinal_key",
			want:  "uniquely-indexed tuple",
		},
		{
			name:  "a column nobody declared",
			alter: "ALTER TABLE " + dialect.GuardReceiptsTable + " ADD COLUMN somebody_elses_column TEXT",
			want:  "columns",
		},
		{
			name:  "an unlogged ledger",
			alter: "ALTER TABLE " + dialect.GuardGateEventsTable + " SET UNLOGGED",
			want:  "relpersistence",
		},
		{
			name:  "row-level security that could hide rows from the fold",
			alter: "ALTER TABLE " + dialect.GuardReceiptsTable + " ENABLE ROW LEVEL SECURITY",
			want:  "row-level security",
		},
		{
			name:  "a named index that is gone",
			alter: "DROP INDEX " + dialect.GuardReceiptsTable + "_target_idx",
			want:  "named index",
		},
		{
			name:  "a dropped CHECK",
			alter: "ALTER TABLE " + dialect.GuardGateEventsTable + " DROP CONSTRAINT " + dialect.GuardGateEventsTable + "_kind_check",
			want:  "CHECK constraint",
		},
		{
			name:  "a column that became nullable",
			alter: "ALTER TABLE " + dialect.GuardGateEventsTable + " ALTER COLUMN details DROP NOT NULL",
			want:  "column",
		},
		{
			name:  "a column the server can now fill",
			alter: "ALTER TABLE " + dialect.GuardGateEventsTable + " ALTER COLUMN details SET DEFAULT ''",
			want:  "DEFAULT",
		},
		{
			name: "a foreign key nobody declared",
			// Self-referencing on the nullable predecessor, so the existing rows satisfy it and
			// the ALTER succeeds: the point is a constraint class this binary never creates, not
			// a constraint that fails to be added.
			alter: "ALTER TABLE " + dialect.GuardReceiptsTable + " ADD CONSTRAINT somebody_elses_fk FOREIGN KEY (predecessor_receipt_id) REFERENCES " + dialect.GuardReceiptsTable + " (receipt_id)",
			want:  "never creates",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsns := isolatedPG(t)
			ctx := context.Background()
			st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
			if err != nil {
				t.Fatalf("open the store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })

			dia, ok := dialect.New(store.EnginePostgres)
			if !ok {
				t.Fatal("no PostgreSQL dialect")
			}
			probe := guardPGProbe(t, dsns.App)
			conn, err := probe.Conn(ctx)
			if err != nil {
				t.Fatalf("check out a connection: %v", err)
			}
			defer func() { _ = conn.Close() }()

			// The preflight accepts this database BEFORE the mutation. Without this the test
			// would pass even if the shape check refused every control plane ever built.
			if _, perr := preflightGuardControlPlane(ctx, conn, dia); perr != nil {
				t.Fatalf("the preflight refused an untouched control plane: %v", perr)
			}
			if _, aerr := probe.ExecContext(ctx, tc.alter); aerr != nil {
				t.Fatalf("apply %q: %v", tc.alter, aerr)
			}
			_, err = preflightGuardControlPlane(ctx, conn, dia)
			if err == nil {
				t.Fatal("the preflight accepted a relation that is not the declared one, and version 6 is recorded — so nothing would ever recreate it")
			}
			if !errors.Is(err, ErrGuardControlPlaneShapeDivergent) {
				t.Fatalf("the refusal was %v, which is not the named shape error — so it refused for some other reason and this test would pass without a shape check", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q: %v", tc.want, err)
			}
		})
	}
}

// certificationGap explains why a live server may not have its declaration compared, and returns
// "" when it may.
//
// IT IS PURE SO THAT THE REFUSAL IS EXERCISED. The branch can only fire when the certified set
// and the supported range come apart, which they do not today — and a refusal nothing ever
// evaluates is a skip with better prose, which is precisely the failure applyMatrixEnvVerdict was
// split out to end one file over. A table can drive this with a major no server here has.
func certificationGap(major int, certified []int, minMajor, maxMajor int) string {
	for _, m := range certified {
		if m == major {
			return ""
		}
	}
	return fmt.Sprintf("this server is PostgreSQL %d and the certified set is %v, while the supported range is %d..%d: reaching here means the two have come apart. Certify the major or narrow the range — do not skip, because a skip here cannot be told from a comparison that passed",
		major, certified, minMajor, maxMajor)
}

// TestTheCertificationGapRefusesAnUncertifiedServer is the CI spec's §5.3 point 3, exercised.
//
// The spec's condition is that the t.Skipf disappears: while it existed, "no skip" could not be
// true by construction, and worse, that skip only fired when a difference was FOUND — so an
// uncertified major that happened to render identically passed with the strongest layer never
// asserted. Three states now, and all three are driven here rather than inferred.
func TestTheCertificationGapRefusesAnUncertifiedServer(t *testing.T) {
	certified := certifiedPostgresMajors()

	// EVERY MAJOR IN THE SUPPORTED RANGE MUST BE CERTIFIED, or a supported server would reach a
	// live comparison and be refused by the very guard that is meant to let it through. This is
	// the coupling that makes the refusal safe to make unconditional.
	for m := supportedPostgresMajorMin; m <= supportedPostgresMajorMax; m++ {
		if gap := certificationGap(m, certified, supportedPostgresMajorMin, supportedPostgresMajorMax); gap != "" {
			t.Errorf("PostgreSQL %d is inside the supported range %d..%d and is not in the certified set %v, so a supported server would be refused rather than compared: %s",
				m, supportedPostgresMajorMin, supportedPostgresMajorMax, certified, gap)
		}
	}

	// AND AN UNCERTIFIED MAJOR IS REFUSED, with a message that says what to do about it. 99 is
	// chosen because no server this suite can reach is one, which is the whole point of a pure
	// seam: the branch is unreachable in practice and testable anyway.
	const uncertified = 99
	gap := certificationGap(uncertified, certified, supportedPostgresMajorMin, supportedPostgresMajorMax)
	if gap == "" {
		t.Fatalf("PostgreSQL %d is not in %v and was allowed through: the comparison would then run its strongest layer against a deparser nobody has measured", uncertified, certified)
	}
	for _, must := range []string{"99", "do not skip"} {
		if !strings.Contains(gap, must) {
			t.Errorf("the refusal does not say %q, so a reader cannot tell which server tripped it or what the alternative is: %s", must, gap)
		}
	}
}

// TestPostgresGuardShapeDeclarationMatchesTheLiveCatalog is what makes the declaration
// trustworthy rather than merely present.
//
// dialect.GuardControlPlaneShapePostgres was MEASURED against a server; this is the regression
// that keeps it measured. It also reports which comparison mode ran, because the CHECK
// predicate text is only compared on the verified major and a run that silently skipped it
// would look identical to one that did not.
func TestPostgresGuardShapeDeclarationMatchesTheLiveCatalog(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	probe := guardPGProbe(t, dsns.App)
	conn, err := probe.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a connection: %v", err)
	}
	defer func() { _ = conn.Close() }()

	major, err := postgresServerMajor(ctx, conn)
	if err != nil {
		t.Fatalf("read the server major: %v", err)
	}
	observed, err := projectGuardControlPlaneShape(ctx, conn)
	if err != nil {
		t.Fatalf("project the control plane's shape: %v", err)
	}
	// NO SKIP, AND THE REFUSAL COMES BEFORE THE COMPARISON. This used to skip when an
	// UNCERTIFIED major rendered the declaration differently, on the reasoning that such a
	// failure is information about the deparser rather than about this repository. Two things
	// were wrong with it, and the CI spec (§5.3 point 3) names the first:
	//
	//   - The certified set now covers the WHOLE supported range and store.Open refuses to boot
	//     outside it, so a live server that reaches this line uncertified means the set and the
	//     range have come apart. A skip reports that as a pass.
	//   - The skip only fired when a difference was FOUND. An uncertified major that happened to
	//     render identically went through as a green with the strongest layer never asserted —
	//     which is the shape of every false green this campaign has spent sixteen rounds on.
	//
	// Refusing up front answers all three states: certified and matching is a pass, certified and
	// diverging is an error naming the relation, and uncertified is neither.
	if gap := certificationGap(major, certifiedPostgresMajors(), supportedPostgresMajorMin, supportedPostgresMajorMax); gap != "" {
		t.Fatal(gap)
	}
	for _, want := range dialect.GuardControlPlaneShapePostgres() {
		got := observed[want.Relation]
		if !got.Found {
			t.Fatalf("%s does not exist after a successful boot", want.Relation)
		}
		if diff := guardShapeDifference(want, got, true); diff != "" {
			t.Errorf("the declared shape of %s does not match this server: %s", want.Relation, diff)
		}
	}
	t.Logf("GUARD_SHAPE_VERIFIED|major=%d|predicates_compared=%t|relations=%d",
		major, postgresMajorCertified(major), len(observed))
}

// currentRole reads which role a DSN actually authenticates as.
//
// The role names pgtest generates are per-test, and a test that GRANTS one role to another has
// to name them. Reading them from the server rather than parsing the DSN is what keeps this
// working if the provisioner ever changes how it renders a connection string.
func currentRole(t *testing.T, dsn string) string {
	t.Helper()
	db := guardPGProbe(t, dsn)
	var role string
	if err := db.QueryRowContext(context.Background(), "SELECT current_user").Scan(&role); err != nil {
		t.Fatalf("read current_user: %v", err)
	}
	return role
}

// TestPostgresRefusesASplitTopologyTheAppRoleCanEscalateOutOf is F-07.
//
// The hardened posture was decided by comparing two role NAMES. Two distinct names say what the
// operator configured; they say nothing about whether the application role can SET ROLE to the
// other one — and PostgreSQL separates automatic inheritance from the right to change role
// deliberately, so a membership can convey the second without the first.
//
// Both subtests therefore assert the SAME three things in order:
//
//  1. the boot succeeds before the membership exists, so the refusal is about the membership;
//  2. has_table_privilege still reports the application role holds NO write privilege — which
//     is exactly why the previous verification passed and why a closure was needed; and
//  3. the boot refuses once the membership exists.
func TestPostgresRefusesASplitTopologyTheAppRoleCanEscalateOutOf(t *testing.T) {
	for _, tc := range []struct {
		name   string
		grant  func(t *testing.T, super *sql.DB, app, owner string) string
		revoke func(t *testing.T, super *sql.DB, app, owner string)
	}{
		{
			// PostgreSQL 15's pg_auth_members is (roleid, member, grantor, admin_option) —
			// measured — so there is no per-membership INHERIT option to turn off. A DIRECT
			// membership that does not convey privileges therefore requires the MEMBER role to be
			// NOINHERIT, and the member here is the application role.
			//
			// THAT ROLE IS CLUSTER-GLOBAL (see pgtest.Isolate: the app role is deliberately not
			// per-test), so this ALTER is visible to every other isolated database on the server.
			// Leaving it NOINHERIT strips its implicit membership in pg_database_owner, which is
			// where a single-role database gets CREATE on schema public from — every
			// SingleRole test then fails with 42501 for a reason that has nothing to do with
			// itself. Measured the hard way. The restore is registered BEFORE the ALTER's effects
			// can matter and runs even when the test fails.
			name: "a direct membership the application role does not inherit",
			grant: func(t *testing.T, super *sql.DB, app, owner string) string {
				// The ALTER below writes the pg_authid tuple of a role every other package
				// provisions concurrently, so it takes the SAME lock Provision takes. Without
				// it this was serialized on one side only, and PostgreSQL answered the other
				// with `tuple concurrently updated` inside whichever package happened to be
				// provisioning — reddening a test that had nothing to do with it.
				//
				// Registered BEFORE the restore below because cleanups run LIFO: the lock must
				// outlive the restore, or the window reopens between putting the role back and
				// letting the next provisioner in.
				t.Cleanup(pgtest.LockSharedRole(t))
				t.Cleanup(func() { _, _ = super.Exec("ALTER ROLE " + app + " INHERIT") })
				exec(t, super, "ALTER ROLE "+app+" NOINHERIT")
				exec(t, super, "GRANT "+owner+" TO "+app)
				return owner
			},
			revoke: func(t *testing.T, super *sql.DB, app, owner string) {
				exec(t, super, "REVOKE "+owner+" FROM "+app)
			},
		},
		{
			// The indirect case needs no change to the shared role's ATTRIBUTES, and that is a
			// measured property rather than a convenience: inheritance stops at a NOINHERIT link
			// in the chain, so an INHERIT application role that is a member of a NOINHERIT middle
			// role does not acquire the owner's privileges — while the MEMBERSHIP, and therefore
			// the right to SET ROLE, remains transitive.
			//
			// It still takes the lock, and that is NOT belt-and-braces. Measured 2026-08-06 on
			// PostgreSQL 15.18: `GRANT m TO r` concurrent with `ALTER ROLE r` raises
			// `tuple concurrently updated` too — writing a pg_auth_members row that NAMES the
			// cluster-global role contends with a provisioner altering it. "Attributes untouched"
			// is not the same claim as "no catalog contention", and only the first one is true here.
			//
			//	m_app INHERIT, m_mid NOINHERIT, app -> mid -> owner
			//	pg_has_role('m_app','m_owner','USAGE')  = false
			//	pg_has_role('m_app','m_owner','MEMBER') = true
			name: "an indirect membership through a NOINHERIT middle role",
			grant: func(t *testing.T, super *sql.DB, app, owner string) string {
				// mid is derived from the cluster-global name, so it is cluster-global too;
				// the lock is what keeps both it and the membership below off a concurrent
				// provisioner. Registered first so it is released LAST (cleanups are LIFO).
				t.Cleanup(pgtest.LockSharedRole(t))
				mid := app + "_mid"
				t.Cleanup(func() { _, _ = super.Exec("DROP ROLE IF EXISTS " + mid) })
				exec(t, super, "CREATE ROLE "+mid+" NOINHERIT")
				exec(t, super, "GRANT "+owner+" TO "+mid)
				exec(t, super, "GRANT "+mid+" TO "+app)
				return owner
			},
			revoke: func(t *testing.T, super *sql.DB, app, _ string) {
				exec(t, super, "REVOKE "+app+"_mid FROM "+app)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsns := isolatedPGSplit(t)
			ctx := context.Background()
			cfg := store.Config{
				Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner,
				AdminDSN: dsns.Admin, MaxConns: 4,
			}

			// 1. THE BASELINE. Without this the test would pass even if split topologies were
			// refused unconditionally.
			st, err := Open(ctx, cfg, registerWidget)
			if err != nil {
				t.Fatalf("the split topology does not boot even before the membership exists: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			app, owner := currentRole(t, dsns.App), currentRole(t, dsns.Owner)
			if app == owner {
				t.Fatalf("the split harness returned one role (%s) for both DSNs, so this test cannot exercise the split posture", app)
			}
			super := guardPGProbe(t, dsns.Superuser)
			assumable := tc.grant(t, super, app, owner)

			// 2. THE OLD VERIFICATION STILL PASSES. Asked about the application role's own
			// privileges, the answer is unchanged — which is the whole reason two distinct names
			// were not enough.
			appProbe := guardPGProbe(t, dsns.App)
			for _, table := range dialect.GuardControlPlaneTables() {
				for _, priv := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE"} {
					var held bool
					if qerr := appProbe.QueryRowContext(ctx,
						"SELECT pg_catalog.has_table_privilege($1, $2)", table, priv).Scan(&held); qerr != nil {
						t.Fatalf("read %s on %s: %v", priv, table, qerr)
					}
					if held {
						t.Fatalf("the application role holds %s on %s directly, so this test is exercising the direct-privilege path rather than the escalation path",
							priv, table)
					}
				}
			}

			// 3. THE REFUSAL.
			st2, err := Open(ctx, cfg, registerWidget)
			if err == nil {
				_ = st2.Close()
				t.Fatal("the boot accepted a split topology whose application role can SET ROLE to the owner, and would have reported the control plane's boundary as verified")
			}
			if !errors.Is(err, store.ErrAppendOnlyACLOpen) {
				t.Fatalf("the refusal was %v, which is not the named ACL error — so it refused for some other reason and this test would pass without the closure", err)
			}
			if !strings.Contains(err.Error(), assumable) {
				t.Errorf("the refusal does not name %s, the role the application role can become: %v", assumable, err)
			}

			// 4. AND IT IS THE MEMBERSHIP, not something else this test did: breaking the chain
			// restores the boot. Only the link the application role holds is revoked, which is
			// the one that makes the closure reach the owner.
			tc.revoke(t, super, app, owner)
			st3, err := Open(ctx, cfg, registerWidget)
			if err != nil {
				t.Fatalf("after revoking the membership the split topology no longer boots: %v", err)
			}
			if err := st3.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
		})
	}
}

// exec runs one statement and fails the test with the statement in the message.
func exec(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), stmt); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}
