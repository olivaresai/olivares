// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

// finopscustodymigration_test.go exercises the v12 custody foundation on a REAL SQLite
// engine, through the real migrate.Apply.
//
// WHAT THESE TESTS ARE AND WHAT THEY ARE NOT, said first because the distinction is the
// whole honesty of the slice. They are a V12-ISOLATED FIXTURE: an empty database, one
// migration plan containing v12 alone, applied by the real applier. They are NOT a
// v10 → v12 production upgrade. No test here opens a store, runs the core plan, or observes
// v12 arriving after v11 — the plan change is separately owned and v12 is not in the plan
// yet. A green run proves the statements parse, the constraints bind and the guards fire on
// the engine; it proves nothing about ordering against the existing plan.
//
// THE CONTROL POSITIVE EVERY PREDICATE LEG CARRIES: each refusal case is paired with the
// legal row that differs only in the field under test, and the legal row is required to be
// ACCEPTED. A refusal test with no accepted twin passes identically against a table that
// rejects everything.

// finOpsCustodyPlan is the v12-only plan. Its tracking table is distinct from
// coreTrackingTable so that nothing here can be mistaken for, or collide with, the core
// plan's history.
const finOpsCustodyTestTrackingTable = "fc90_migration_history"

// openCustodySQLite returns a raw handle to a fresh file database with v12 applied, and the
// dialect.
//
// A FILE rather than :memory:, because each :memory: connection is its own database and the
// guards have to read rows a previous statement on another connection committed.
func openCustodySQLite(t *testing.T, roles ...guardRoles) (*sql.DB, dialect.Dialect) {
	t.Helper()
	db, dia := emptyCustodySQLite(t)
	if err := migrate.Apply(context.Background(), db, dia, finOpsCustodyTestTrackingTable,
		[]migrate.Migration{coreFinOpsCustodyControlMigration(dia, roles...)}); err != nil {
		t.Fatalf("apply the v12 custody foundation: %v", err)
	}
	return db, dia
}

// emptyCustodySQLite returns a raw handle to a fresh file database with NOTHING applied.
func emptyCustodySQLite(t *testing.T) (*sql.DB, dialect.Dialect) {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/custody.db")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no SQLite dialect")
	}
	return db, dia
}

// The fixture identities. The instance id is a canonical lowercase UUIDv4: version nibble 4
// at position 15, variant class [89ab] at position 20.
const (
	custodyInstanceA = "7f3a1c2e-4b5d-4e6f-8a9b-0c1d2e3f4a5b"
	custodyInstanceB = "1a2b3c4d-5e6f-4a7b-9c8d-0e1f2a3b4c5d"
	custodyKeyRefG1  = "kr1_abcdefghijklmnopqrstuvwxyz"
	custodyKeyRefG2  = "kr1_234567abcdefghijklmnopqrs"
	custodyStamp     = "2026-09-12T10:00:00.000000000Z"
)

// insertTransition writes one C3 row. Every value is BOUND, never rendered, so a test
// cannot accidentally prove that its own SQL escaping works.
func insertTransition(ctx context.Context, db *sql.DB, dia dialect.Dialect, row map[string]any) error {
	cols := []string{"custody_domain", "custody_instance_id", "to_revision", "transition",
		"from_state", "from_highest", "from_confirmed", "from_active",
		"to_state", "to_highest", "to_confirmed", "to_active",
		"subject_generation", "writers_upgraded", "writers_drained",
		"core_version", "guard_epoch", "actor", "reason", "recorded_at"}
	args := make([]any, 0, len(cols))
	for _, c := range cols {
		args = append(args, row[c])
	}
	q := "INSERT INTO " + directoryWriterRelation(dia, dialect.ControlCustodyTransitionTable) +
		" (" + strings.Join(cols, ", ") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + ")"
	_, err := db.ExecContext(ctx, dia.Rebind(q), args...)
	return err
}

// beginEnrollmentRow is the legal revision-1 journal row: no before-image at all.
func beginEnrollmentRow(instance string) map[string]any {
	return map[string]any{
		"custody_domain": dialect.FinOpsCustodyDomain, "custody_instance_id": instance,
		"to_revision": 1, "transition": "begin_enrollment",
		"from_state": nil, "from_highest": nil, "from_confirmed": nil, "from_active": nil,
		"to_state": "enrolling", "to_highest": 1, "to_confirmed": 0, "to_active": 0,
		"subject_generation": 1, "writers_upgraded": 0, "writers_drained": 1,
		"core_version": 12, "guard_epoch": 9,
		"actor": "ceremony", "reason": "open custody enrollment", "recorded_at": custodyStamp,
	}
}

// confirmEnrollmentRow is the legal revision-2 journal row: a complete before-image equal to
// the head it is about to replace.
func confirmEnrollmentRow(instance string) map[string]any {
	return map[string]any{
		"custody_domain": dialect.FinOpsCustodyDomain, "custody_instance_id": instance,
		"to_revision": 2, "transition": "confirm_enrollment",
		"from_state": "enrolling", "from_highest": 1, "from_confirmed": 0, "from_active": 0,
		"to_state": "enrolled", "to_highest": 1, "to_confirmed": 1, "to_active": 0,
		"subject_generation": 1, "writers_upgraded": 0, "writers_drained": 1,
		"core_version": 12, "guard_epoch": 9,
		"actor": "ceremony", "reason": "confirm custody enrollment", "recorded_at": custodyStamp,
	}
}

// activateRow is the legal revision-3 journal row.
func activateRow(instance string) map[string]any {
	return map[string]any{
		"custody_domain": dialect.FinOpsCustodyDomain, "custody_instance_id": instance,
		"to_revision": 3, "transition": "activate",
		"from_state": "enrolled", "from_highest": 1, "from_confirmed": 1, "from_active": 0,
		"to_state": "active", "to_highest": 1, "to_confirmed": 1, "to_active": 1,
		"subject_generation": 1, "writers_upgraded": 1, "writers_drained": 1,
		"core_version": 12, "guard_epoch": 9,
		"actor": "ceremony", "reason": "activate custody generation", "recorded_at": custodyStamp,
	}
}

// insertProof writes one C2 row.
func insertProof(ctx context.Context, db *sql.DB, dia dialect.Dialect, instance string, generation, revision int, keyRef, purpose string) error {
	q := "INSERT INTO " + directoryWriterRelation(dia, dialect.ControlCustodyProofTable) +
		" (custody_domain, custody_instance_id, generation, custody_key_ref, proof_format, proof_purpose, proof_nonce, proof_ciphertext, recorded_revision, recorded_at)" +
		" VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?)"
	_, err := db.ExecContext(ctx, dia.Rebind(q), dialect.FinOpsCustodyDomain, instance, generation,
		keyRef, purpose, make([]byte, 12), make([]byte, 48), revision, custodyStamp)
	return err
}

// insertHead writes the C1 row at revision 1.
func insertHead(ctx context.Context, db *sql.DB, dia dialect.Dialect, instance string) error {
	q := "INSERT INTO " + directoryWriterRelation(dia, dialect.ControlCustodyEnrollmentTable) +
		" (custody_domain, custody_instance_id, keyring_format, state, revision, highest_generation, confirmed_generation, active_generation, created_at, updated_at)" +
		" VALUES (?, ?, 1, 'enrolling', 1, 1, 0, 0, ?, ?)"
	_, err := db.ExecContext(ctx, dia.Rebind(q), dialect.FinOpsCustodyDomain, instance, custodyStamp, custodyStamp)
	return err
}

// advanceHead performs the C1 UPDATE for one revision.
func advanceHead(ctx context.Context, db *sql.DB, dia dialect.Dialect, instance, state string, revision, highest, confirmed, active int) error {
	q := "UPDATE " + directoryWriterRelation(dia, dialect.ControlCustodyEnrollmentTable) +
		" SET state = ?, revision = ?, highest_generation = ?, confirmed_generation = ?, active_generation = ?, updated_at = ?" +
		" WHERE custody_domain = ? AND custody_instance_id = ?"
	_, err := db.ExecContext(ctx, dia.Rebind(q), state, revision, highest, confirmed, active,
		custodyStamp, dialect.FinOpsCustodyDomain, instance)
	return err
}

// enrollThrough runs the legal opening sequence up to and including activation.
//
// THE ORDER IS THE CONTRACT, not a convenience: journal row, then its proof, then the head.
// The guards make the reverse impossible — each reads rows its predecessor wrote — so a
// helper that got it wrong would fail rather than silently test a different order.
func enrollThrough(t *testing.T, ctx context.Context, db *sql.DB, dia dialect.Dialect, instance string) {
	t.Helper()
	if err := insertTransition(ctx, db, dia, beginEnrollmentRow(instance)); err != nil {
		t.Fatalf("rev 1 begin_enrollment: %v", err)
	}
	if err := insertProof(ctx, db, dia, instance, 1, 1, custodyKeyRefG1, "enrollment"); err != nil {
		t.Fatalf("generation 1 proof: %v", err)
	}
	if err := insertHead(ctx, db, dia, instance); err != nil {
		t.Fatalf("head at revision 1: %v", err)
	}
	if err := insertTransition(ctx, db, dia, confirmEnrollmentRow(instance)); err != nil {
		t.Fatalf("rev 2 confirm_enrollment: %v", err)
	}
	if err := advanceHead(ctx, db, dia, instance, "enrolled", 2, 1, 1, 0); err != nil {
		t.Fatalf("head to revision 2: %v", err)
	}
	if err := insertTransition(ctx, db, dia, activateRow(instance)); err != nil {
		t.Fatalf("rev 3 activate: %v", err)
	}
	if err := advanceHead(ctx, db, dia, instance, "active", 3, 1, 1, 1); err != nil {
		t.Fatalf("head to revision 3: %v", err)
	}
}

// TestSQLiteCustodyLegalEnrollmentSequence is the accepted path, and it is the control
// positive every refusal leg below depends on.
func TestSQLiteCustodyLegalEnrollmentSequence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, dia := openCustodySQLite(t)
	enrollThrough(t, ctx, db, dia, custodyInstanceA)

	var state string
	var rev, high, conf, act int
	if err := db.QueryRowContext(ctx, dia.Rebind("SELECT state, revision, highest_generation, confirmed_generation, active_generation FROM "+
		directoryWriterRelation(dia, dialect.ControlCustodyEnrollmentTable)+" WHERE custody_instance_id = ?"), custodyInstanceA).
		Scan(&state, &rev, &high, &conf, &act); err != nil {
		t.Fatalf("read the head: %v", err)
	}
	if state != "active" || rev != 3 || high != 1 || conf != 1 || act != 1 {
		t.Fatalf("head after activation is (%s, %d, %d, %d, %d), want (active, 3, 1, 1, 1)", state, rev, high, conf, act)
	}

	var journal int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+
		directoryWriterRelation(dia, dialect.ControlCustodyTransitionTable)).Scan(&journal); err != nil {
		t.Fatalf("count the journal: %v", err)
	}
	if journal != 3 {
		t.Fatalf("journal holds %d rows, want 3", journal)
	}
}

// TestSQLiteCustodyRevisionOneImageIsNull proves the required NULL before-image is ACCEPTED
// at revision 1 and REFUSED everywhere else, and vice versa.
//
// This is the correction-1 defect the contract's decision 5 exists to fix: a universal
// `typeof(col) = 'integer'` rule rejected the NULL revision-1 image outright, so the legal
// opening row could not be written at all.
func TestSQLiteCustodyRevisionOneImageIsNull(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, dia := openCustodySQLite(t)

	// Accepted: NULL before-image at revision 1.
	if err := insertTransition(ctx, db, dia, beginEnrollmentRow(custodyInstanceA)); err != nil {
		t.Fatalf("the NULL revision-1 image must be accepted: %v", err)
	}

	// Refused: a revision-1 row carrying a before-image.
	row := beginEnrollmentRow(custodyInstanceB)
	row["from_state"], row["from_highest"], row["from_confirmed"], row["from_active"] = "enrolling", 1, 0, 0
	if err := insertTransition(ctx, db, dia, row); err == nil {
		t.Fatal("a revision-1 row carrying a before-image must be refused")
	}

	// Refused: the four columns NULL apart. A partially-NULL image is the shape four
	// independent nullability CHECKs would have admitted.
	row = beginEnrollmentRow(custodyInstanceB)
	row["from_state"] = "enrolling"
	if err := insertTransition(ctx, db, dia, row); err == nil {
		t.Fatal("a partially populated before-image must be refused")
	}

	// Refused, AND THIS IS THE CASE ONLY THE EQUIVALENCE CAN REFUSE — it was added
	// after a control positive showed the three cases above surviving the removal of the
	// revision-1 image CHECK. They survive because the `begin_enrollment` row predicate
	// already pins from_state, so dropping the equivalence changed nothing for them. The
	// row predicate names from_state and NOT the other three, so a revision-1 row with a
	// NULL state and a populated generation image satisfies every other CHECK in the
	// relation: the row predicate, the nullable range CHECK (5 is inside 1..32) and the
	// before-image order CHECK (short-circuited by `from_state IS NULL`).
	row = beginEnrollmentRow(custodyInstanceB)
	row["from_highest"] = 5
	if err := insertTransition(ctx, db, dia, row); err == nil {
		t.Fatal("a revision-1 row with a NULL state and a populated generation image must be refused")
	}

	// Refused: revision 2 with a NULL before-image.
	row = confirmEnrollmentRow(custodyInstanceA)
	row["from_state"], row["from_highest"], row["from_confirmed"], row["from_active"] = nil, nil, nil, nil
	if err := insertTransition(ctx, db, dia, row); err == nil {
		t.Fatal("a revision >= 2 row with no before-image must be refused")
	}
}

// TestSQLiteCustodyRejectsMalformedScalars covers the NULL, storage-class and UUID legs on
// the engine whose dynamic typing makes them necessary.
func TestSQLiteCustodyRejectsMalformedScalars(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, dia := openCustodySQLite(t)

	for _, tc := range []struct {
		name  string
		patch map[string]any
	}{
		// NOT NULL, on a column a range CHECK would have passed: a CHECK whose value
		// is NULL evaluates to NULL and therefore does not refuse.
		{"null to_highest", map[string]any{"to_highest": nil}},
		{"null transition", map[string]any{"transition": nil}},
		{"null actor", map[string]any{"actor": nil}},
		// Storage class. SQLite stores '1' in an INTEGER column verbatim, and
		// `'1' BETWEEN 1 AND 32` is FALSE there, so a bare range CHECK would have
		// refused for the wrong reason; the text 'abc' is the case that exposes it.
		{"text in an integer column", map[string]any{"to_highest": "abc"}},
		{"real in an integer column", map[string]any{"to_confirmed": 0.5}},
		{"blob in a text column", map[string]any{"actor": []byte{0x01, 0x02}}},
		{"integer in a text column", map[string]any{"to_state": 7}},
		// Canonical UUID. Each of these differs from the accepted id in exactly one
		// respect the §2.3 predicate names.
		{"uppercase uuid", map[string]any{"custody_instance_id": "7F3A1C2E-4B5D-4E6F-8A9B-0C1D2E3F4A5B"}},
		{"version nibble not 4", map[string]any{"custody_instance_id": "7f3a1c2e-4b5d-1e6f-8a9b-0c1d2e3f4a5b"}},
		{"variant class out of range", map[string]any{"custody_instance_id": "7f3a1c2e-4b5d-4e6f-7a9b-0c1d2e3f4a5b"}},
		{"hyphen misplaced", map[string]any{"custody_instance_id": "7f3a1c2e4-b5d-4e6f-8a9b-0c1d2e3f4a5b"}},
		{"too short", map[string]any{"custody_instance_id": "7f3a1c2e-4b5d-4e6f-8a9b-0c1d2e3f4a5"}},
		{"non-hex character", map[string]any{"custody_instance_id": "7f3a1c2g-4b5d-4e6f-8a9b-0c1d2e3f4a5b"}},
		// Closed domain and closed enumerations.
		{"foreign domain", map[string]any{"custody_domain": "finops.policy_recovery.v2"}},
		{"unknown transition", map[string]any{"transition": "rotate_custody"}},
		{"unknown to_state", map[string]any{"to_state": "retired"}},
		{"core_version below 12", map[string]any{"core_version": 11}},
		{"guard_epoch outside the closed set", map[string]any{"guard_epoch": 7}},
		{"generation above the cap", map[string]any{"to_highest": 33, "to_confirmed": 33, "to_active": 0}},
		{"revision above the cap", map[string]any{"to_revision": 129}},
		{"empty actor", map[string]any{"actor": ""}},
	} {
		row := beginEnrollmentRow(custodyInstanceA)
		for k, v := range tc.patch {
			row[k] = v
		}
		if err := insertTransition(ctx, db, dia, row); err == nil {
			t.Errorf("%s: must be refused, was accepted", tc.name)
		}
	}

	// The control positive, AFTER the refusals: the unpatched row is still accepted, so
	// the refusals above are about the patches and not about a table that rejects
	// everything.
	if err := insertTransition(ctx, db, dia, beginEnrollmentRow(custodyInstanceA)); err != nil {
		t.Fatalf("the unpatched legal row must be accepted: %v", err)
	}
}

// TestSQLiteCustodyRejectsMalformedKeyRef covers the C2 key reference and proof sizes.
func TestSQLiteCustodyRejectsMalformedKeyRef(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, dia := openCustodySQLite(t)
	if err := insertTransition(ctx, db, dia, beginEnrollmentRow(custodyInstanceA)); err != nil {
		t.Fatalf("rev 1: %v", err)
	}

	for _, tc := range []struct{ name, keyRef string }{
		{"wrong prefix", "kr2_abcdefghijklmnopqrstuvwxyz"},
		{"base32 alphabet violated: 0", "kr1_0bcdefghijklmnopqrstuvwxyz"},
		{"base32 alphabet violated: 1", "kr1_1bcdefghijklmnopqrstuvwxyz"},
		{"uppercase", "kr1_ABCDEFGHIJKLMNOPQRSTUVWXYZ"},
		{"too short", "kr1_abcdefghijklmnopqrstuvwxy"},
		{"too long", "kr1_abcdefghijklmnopqrstuvwxyza"},
	} {
		if err := insertProof(ctx, db, dia, custodyInstanceA, 1, 1, tc.keyRef, "enrollment"); err == nil {
			t.Errorf("%s: key ref %q must be refused", tc.name, tc.keyRef)
		}
	}

	// Purpose is an EQUIVALENCE with generation 1, not an implication.
	if err := insertProof(ctx, db, dia, custodyInstanceA, 1, 1, custodyKeyRefG1, "generation_add"); err == nil {
		t.Error("generation 1 with purpose generation_add must be refused")
	}

	// Control positive.
	if err := insertProof(ctx, db, dia, custodyInstanceA, 1, 1, custodyKeyRefG1, "enrollment"); err != nil {
		t.Fatalf("the legal generation-1 proof must be accepted: %v", err)
	}
}

// TestSQLiteCustodyRefusesIllegalOrderingAndAfterImage is the statement-order guard leg.
func TestSQLiteCustodyRefusesIllegalOrderingAndAfterImage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("head before its journal row", func(t *testing.T) {
		t.Parallel()
		db, dia := openCustodySQLite(t)
		if err := insertHead(ctx, db, dia, custodyInstanceA); err == nil {
			t.Fatal("a head with no recorded begin_enrollment must be refused")
		}
	})

	t.Run("head before its proof", func(t *testing.T) {
		t.Parallel()
		db, dia := openCustodySQLite(t)
		if err := insertTransition(ctx, db, dia, beginEnrollmentRow(custodyInstanceA)); err != nil {
			t.Fatalf("rev 1: %v", err)
		}
		if err := insertHead(ctx, db, dia, custodyInstanceA); err == nil {
			t.Fatal("a head with no generation-1 proof must be refused")
		}
	})

	t.Run("journal skips a revision", func(t *testing.T) {
		t.Parallel()
		db, dia := openCustodySQLite(t)
		enrollThrough(t, ctx, db, dia, custodyInstanceA)
		row := activateRow(custodyInstanceA)
		row["to_revision"] = 5
		if err := insertTransition(ctx, db, dia, row); err == nil {
			t.Fatal("a journal row that skips its predecessor must be refused")
		}
	})

	t.Run("journal cites a stale before-image", func(t *testing.T) {
		t.Parallel()
		db, dia := openCustodySQLite(t)
		if err := insertTransition(ctx, db, dia, beginEnrollmentRow(custodyInstanceA)); err != nil {
			t.Fatalf("rev 1: %v", err)
		}
		if err := insertProof(ctx, db, dia, custodyInstanceA, 1, 1, custodyKeyRefG1, "enrollment"); err != nil {
			t.Fatalf("proof: %v", err)
		}
		if err := insertHead(ctx, db, dia, custodyInstanceA); err != nil {
			t.Fatalf("head: %v", err)
		}
		if err := insertTransition(ctx, db, dia, confirmEnrollmentRow(custodyInstanceA)); err != nil {
			t.Fatalf("rev 2: %v", err)
		}
		// rev 2 is recorded but the head still stands at revision 1. A rev-3 row whose
		// before-image describes the JOURNAL rather than the HEAD must be refused: the
		// guard compares against the head, which is the thing about to be replaced.
		if err := insertTransition(ctx, db, dia, activateRow(custodyInstanceA)); err == nil {
			t.Fatal("a journal row citing a head that has not advanced must be refused")
		}
	})

	t.Run("head advances to an unrecorded after-image", func(t *testing.T) {
		t.Parallel()
		db, dia := openCustodySQLite(t)
		enrollThrough(t, ctx, db, dia, custodyInstanceA)
		// revision 4 is not journaled at all.
		if err := advanceHead(ctx, db, dia, custodyInstanceA, "active", 4, 1, 1, 1); err == nil {
			t.Fatal("a head advance with no journal row must be refused")
		}
	})

	t.Run("head skips a revision", func(t *testing.T) {
		t.Parallel()
		db, dia := openCustodySQLite(t)
		if err := insertTransition(ctx, db, dia, beginEnrollmentRow(custodyInstanceA)); err != nil {
			t.Fatalf("rev 1: %v", err)
		}
		if err := insertProof(ctx, db, dia, custodyInstanceA, 1, 1, custodyKeyRefG1, "enrollment"); err != nil {
			t.Fatalf("proof: %v", err)
		}
		if err := insertHead(ctx, db, dia, custodyInstanceA); err != nil {
			t.Fatalf("head: %v", err)
		}
		if err := insertTransition(ctx, db, dia, confirmEnrollmentRow(custodyInstanceA)); err != nil {
			t.Fatalf("rev 2: %v", err)
		}
		if err := advanceHead(ctx, db, dia, custodyInstanceA, "enrolled", 3, 1, 1, 0); err == nil {
			t.Fatal("a head advance of more than one revision must be refused")
		}
	})

	t.Run("head identity is immutable", func(t *testing.T) {
		t.Parallel()
		db, dia := openCustodySQLite(t)
		enrollThrough(t, ctx, db, dia, custodyInstanceA)
		_, err := db.ExecContext(ctx, dia.Rebind("UPDATE "+directoryWriterRelation(dia, dialect.ControlCustodyEnrollmentTable)+
			" SET custody_instance_id = ? WHERE custody_instance_id = ?"), custodyInstanceB, custodyInstanceA)
		if err == nil {
			t.Fatal("re-identifying a head must be refused")
		}
	})

	t.Run("proof without its minting transition", func(t *testing.T) {
		t.Parallel()
		db, dia := openCustodySQLite(t)
		if err := insertProof(ctx, db, dia, custodyInstanceA, 1, 1, custodyKeyRefG1, "enrollment"); err == nil {
			t.Fatal("a proof with no minting transition must be refused")
		}
	})
}

// TestSQLiteCustodyRowsAreImmutableAndRetained is the append-only and retention leg.
//
// Every probe runs against a SEEDED table, because a row trigger fires per ROW: an UPDATE or
// DELETE against an empty relation succeeds, affects nothing and proves nothing.
func TestSQLiteCustodyRowsAreImmutableAndRetained(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, dia := openCustodySQLite(t)
	enrollThrough(t, ctx, db, dia, custodyInstanceA)

	for _, table := range []string{dialect.ControlCustodyProofTable, dialect.ControlCustodyTransitionTable} {
		rel := directoryWriterRelation(dia, table)
		var seeded int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+rel).Scan(&seeded); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if seeded == 0 {
			t.Fatalf("%s must be seeded before the immutability probe", table)
		}
		if _, err := db.ExecContext(ctx, dia.Rebind("UPDATE "+rel+" SET recorded_at = ?"), custodyStamp); err == nil {
			t.Errorf("UPDATE on %s must be refused", table)
		}
		if _, err := db.ExecContext(ctx, "DELETE FROM "+rel); err == nil {
			t.Errorf("DELETE on %s must be refused", table)
		}
		var after int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+rel).Scan(&after); err != nil {
			t.Fatalf("recount %s: %v", table, err)
		}
		if after != seeded {
			t.Errorf("%s holds %d rows after the refused probes, want the retained %d", table, after, seeded)
		}
	}

	// The head is not deletable either, which is what makes retention a property of the
	// instance and not only of its history.
	if _, err := db.ExecContext(ctx, "DELETE FROM "+
		directoryWriterRelation(dia, dialect.ControlCustodyEnrollmentTable)); err == nil {
		t.Error("DELETE on the head must be refused")
	}
}

// TestSQLiteCustodyRefusesDuplicateLiveEnrollment is the one-live-instance leg.
//
// It asserts BOTH mechanisms, because they fail differently: the order guard refuses a second
// opening journal row while a live head exists, and the partial unique index refuses a second
// live head on every path no guard sees.
func TestSQLiteCustodyRefusesDuplicateLiveEnrollment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, dia := openCustodySQLite(t)
	enrollThrough(t, ctx, db, dia, custodyInstanceA)

	if err := insertTransition(ctx, db, dia, beginEnrollmentRow(custodyInstanceB)); err == nil {
		t.Fatal("opening a second instance while one is live must be refused")
	}

	// The index, probed directly: the same statement, with the guards of the head
	// dropped, must still be refused. Dropping the guard is how the index is shown to be
	// an independent backstop rather than a restatement of the trigger.
	if _, err := db.ExecContext(ctx, "DROP TRIGGER "+dialect.ControlCustodyEnrollmentTable+"_insert"); err != nil {
		t.Fatalf("drop the head insert guard: %v", err)
	}
	if err := insertHead(ctx, db, dia, custodyInstanceB); err == nil {
		t.Fatal("a second live head must be refused by the partial unique index")
	} else if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("the refusal must come from the unique index, got %v", err)
	}
}

// TestCustodyMigrationRollsBackWhenItFails proves the migration is atomic: a failing v12
// leaves NO relation, NO guard and NO tracking row.
//
// The failure is injected the way the real one would arrive — a relation the statements
// cannot create because the name is already taken — rather than by a test-only hook. That is
// also the exact shape of the real hazard: an untracked custody relation already present on
// the database.
func TestCustodyMigrationRollsBackWhenItFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, dia := emptyCustodySQLite(t)

	// The collision is on the LAST of the three relations, so the first two and several
	// guards have already been created inside the transaction when the failure arrives.
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+dialect.ControlCustodyTransitionTable+" (squatter INTEGER)"); err != nil {
		t.Fatalf("seed the colliding relation: %v", err)
	}

	err := migrate.Apply(ctx, db, dia, finOpsCustodyTestTrackingTable,
		[]migrate.Migration{coreFinOpsCustodyControlMigration(dia)})
	if err == nil {
		t.Fatal("the migration must fail when a custody relation already exists")
	}

	// The two relations created before the failure are gone.
	for _, table := range []string{dialect.ControlCustodyEnrollmentTable, dialect.ControlCustodyProofTable} {
		cols, cErr := dia.TableColumns(ctx, db, table)
		if cErr != nil {
			t.Fatalf("introspect %s: %v", table, cErr)
		}
		if len(cols) != 0 {
			t.Errorf("%s survived the rolled-back migration with %d columns", table, len(cols))
		}
	}
	// Every guard is gone too.
	var guards int
	if qErr := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'control_custody%'").Scan(&guards); qErr != nil {
		t.Fatalf("count the guards: %v", qErr)
	}
	if guards != 0 {
		t.Errorf("%d custody guards survived the rolled-back migration", guards)
	}
	// And the tracking row was never written.
	var tracked int
	if qErr := db.QueryRowContext(ctx, dia.Rebind("SELECT COUNT(*) FROM "+finOpsCustodyTestTrackingTable+" WHERE version = ?"),
		coreFinOpsCustodyControlMigrationVersion).Scan(&tracked); qErr != nil {
		t.Fatalf("count the tracking rows: %v", qErr)
	}
	if tracked != 0 {
		t.Errorf("v12 is tracked %d times after a failed apply, want 0", tracked)
	}
	// The squatter is untouched: the rollback removed what the migration made, and
	// nothing else.
	if cols, cErr := dia.TableColumns(ctx, db, dialect.ControlCustodyTransitionTable); cErr != nil || !cols["squatter"] {
		t.Errorf("the pre-existing relation must survive unchanged, got %v (err %v)", cols, cErr)
	}
}

// TestCustodyMigrationRefusesUnsupportedTopology is the constructor's Before precondition.
//
// It runs on PostgreSQL's dialect with NO server, which is the point: Before is reached
// before any statement, so the refusal is observable without an engine, and a deployment
// whose owner role could not be resolved is refused rather than silently created with no
// application-role revoke.
func TestCustodyMigrationRefusesUnsupportedTopology(t *testing.T) {
	t.Parallel()
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}

	unresolved := guardRoles{
		App:             guardRoleFact{Known: true, Role: "olivares_app"},
		Owner:           guardRoleFact{Known: false},
		OwnerConfigured: true,
	}

	for _, tc := range []struct {
		name  string
		roles []guardRoles
	}{
		{"no roles supplied", nil},
		{"two roles supplied", []guardRoles{unresolved, unresolved}},
		{"owner configured but unresolved", []guardRoles{unresolved}},
		{"application role unresolved", []guardRoles{{
			App:             guardRoleFact{Known: false},
			Owner:           guardRoleFact{Known: true, Role: "olivares_owner"},
			OwnerConfigured: true,
		}}},
	} {
		m := coreFinOpsCustodyControlMigration(dia, tc.roles...)
		if m.Before == nil {
			t.Fatalf("%s: the PostgreSQL migration must carry a Before precondition", tc.name)
		}
		err := m.Before(context.Background(), nil)
		if err == nil {
			t.Errorf("%s: must be refused", tc.name)
			continue
		}
		if !errors.Is(err, store.ErrAppendOnlyACLUnverifiable) {
			t.Errorf("%s: refusal must be ErrAppendOnlyACLUnverifiable, got %v", tc.name, err)
		}
	}

	// Both supported topologies pass, and they differ in exactly one observable way: the
	// split one appends the application-role establishment pair and the single-role one
	// does not. An unconditional pair would revoke the engine's own writer.
	single := []guardRoles{{App: guardRoleFact{Known: true, Role: "olivares"}, OwnerConfigured: false}}
	split := []guardRoles{{
		App:             guardRoleFact{Known: true, Role: "olivares_app"},
		Owner:           guardRoleFact{Known: true, Role: "olivares_owner"},
		OwnerConfigured: true,
	}}
	for _, tc := range []struct {
		name  string
		roles []guardRoles
		extra int
	}{
		{"single role", single, 0},
		{"split owner/app", split, 2},
	} {
		m := coreFinOpsCustodyControlMigration(dia, tc.roles...)
		if err := m.Before(context.Background(), nil); err != nil {
			t.Fatalf("%s: must be accepted, got %v", tc.name, err)
		}
		want := len(dia.FinOpsCustodyControlStmts()) + tc.extra
		if len(m.Stmts) != want {
			t.Errorf("%s: %d statements, want %d", tc.name, len(m.Stmts), want)
		}
	}

	// SQLite carries no Before at all: it has no role model, so there is no topology to
	// classify and refusing it would be a refusal about an unaskable question.
	lite, _ := dialect.New(store.EngineSQLite)
	if m := coreFinOpsCustodyControlMigration(lite); m.Before != nil {
		t.Error("the SQLite migration must carry no topology precondition")
	}
}

// TestCustodyControlTablesIsClosedAndSorted pins the closed target list §4.3.1 requires every
// custody ACL decision to take its targets from.
func TestCustodyControlTablesIsClosedAndSorted(t *testing.T) {
	t.Parallel()
	got := dialect.FinOpsCustodyControlTables()
	want := []string{"control_custody_enrollment", "control_custody_proof", "control_custody_transition"}
	if len(got) != len(want) {
		t.Fatalf("the custody target list holds %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("target %d is %q, want %q", i, got[i], want[i])
		}
	}
	// It is a DIFFERENT list from the C4 guard control plane's, which §4.3.1 forbids
	// substituting for it. Asserting non-overlap is what makes a future copy-paste of the
	// wrong accessor fail here instead of silently revoking the wrong three relations.
	for _, custody := range got {
		for _, guard := range dialect.GuardControlPlaneTables() {
			if custody == guard {
				t.Errorf("%q appears in both the custody and the guard-plane target lists", custody)
			}
		}
	}
	// The caller gets a copy, not the package's slice: a caller that sorts or truncates
	// the result must not be able to change what the next caller sees.
	got[0] = "mutated"
	if dialect.FinOpsCustodyControlTables()[0] != want[0] {
		t.Error("FinOpsCustodyControlTables must not expose shared backing state")
	}
}
