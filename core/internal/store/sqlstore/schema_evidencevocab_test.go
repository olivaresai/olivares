// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// schema_evidencevocab_test.go — reconcileEvidenceOpStateCheck (stage-7 B-bis):
// the evidence_operations lifecycle CHECK is frozen at CREATE TABLE, so a
// database created before 'withheld' existed refuses the new settlement word at
// the constraint. Postgres is widened in place — failure-atomically (round 2,
// F-3), with OID/column-precise probes (F-4) and no acceptance of a NOT VALID
// residue (F-2); SQLite cannot be and must stay deny-closed. Fresh databases
// need no reconcile at all — TestEvidenceSettleTerminalStates settles
// 'withheld' through a store opened from the current descriptors.

// preWithheldEvidenceOpsDDL is the evidence_operations CREATE TABLE exactly as a
// pre-stage-7 deployment holds it: the five-word CHECK, 'withheld' absent.
const preWithheldEvidenceOpsDDL = `CREATE TABLE evidence_operations (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  version BIGINT NOT NULL,
  operation_id TEXT NOT NULL,
  effect_digest TEXT NOT NULL,
  surface TEXT NOT NULL,
  action TEXT NOT NULL,
  state TEXT NOT NULL,
  claim_evidence_ref TEXT NOT NULL,
  outcome_evidence_ref TEXT,
  result_digest TEXT,
  dispatch_ref TEXT,
  leader_epoch BIGINT NOT NULL,
  CHECK (state IN ('claimed','completed','not_sent','unknown','blocked'))
)`

// insertEvidenceOpState inserts a minimal row with the given state, returning
// the driver's verdict — the CHECK constraint is the thing under test.
func insertEvidenceOpState(ctx context.Context, db *sql.DB, id, state string) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO evidence_operations
		 (id, tenant_id, created_at, updated_at, version, operation_id, effect_digest,
		  surface, action, state, claim_evidence_ref, leader_epoch)
		 VALUES ('%s','t1','2026-07-30T00:00:00Z','2026-07-30T00:00:00Z',1,'op-%s','d1',
		  'mcp.gateway','mcp.tool.call','%s','ref1',1)`, id, id, state))
	return err
}

// evidenceVocabConstraints returns (name → convalidated, def) of every CHECK on
// the target table, resolved by regclass (the visible relation) like the
// reconciler itself — never by relname alone.
func evidenceVocabConstraints(t *testing.T, ctx context.Context, db *sql.DB) map[string]struct {
	validated bool
	def       string
} {
	t.Helper()
	rows, err := db.QueryContext(ctx,
		`SELECT c.conname, c.convalidated, pg_catalog.pg_get_constraintdef(c.oid)
		 FROM pg_catalog.pg_constraint c
		 WHERE c.conrelid = 'evidence_operations'::regclass AND c.contype = 'c'`)
	if err != nil {
		t.Fatalf("list constraints: %v", err)
	}
	defer rows.Close()
	out := map[string]struct {
		validated bool
		def       string
	}{}
	for rows.Next() {
		var name, def string
		var validated bool
		if err := rows.Scan(&name, &validated, &def); err != nil {
			t.Fatalf("scan constraint: %v", err)
		}
		out[name] = struct {
			validated bool
			def       string
		}{validated, def}
	}
	return out
}

func pgDialect(t *testing.T) dialect.Dialect {
	t.Helper()
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("postgres dialect")
	}
	return dia
}

// TestEvidenceOpStateCheckSQLitePreWithheldStaysDenyClosed: SQLite cannot ALTER
// a CHECK, so the reconcile must NOT fail the boot of a pre-withheld database
// (it announces the gap instead), and the stale constraint must keep refusing
// 'withheld' — the deny-closed shape the settlement path is documented to
// surface as a refused settlement (response withheld, operation stays claimed).
func TestEvidenceOpStateCheckSQLitePreWithheldStaysDenyClosed(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, preWithheldEvidenceOpsDDL); err != nil {
		t.Fatalf("create pre-withheld table: %v", err)
	}
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("sqlite dialect")
	}
	// The reconcile is a no-op-with-warning: an in-place upgrade must still boot.
	if err := reconcileEvidenceOpStateCheck(ctx, db, dia); err != nil {
		t.Fatalf("reconcile on a pre-withheld SQLite table must not fail the boot: %v", err)
	}
	// The old vocabulary still writes...
	if err := insertEvidenceOpState(ctx, db, "old", "blocked"); err != nil {
		t.Fatalf("legacy-vocabulary insert refused: %v", err)
	}
	// ...and the new word is refused AT THE DATABASE: fail-closed, never a
	// silently accepted row the decode-time validator would then have to trust.
	if err := insertEvidenceOpState(ctx, db, "new", "withheld"); err == nil {
		t.Fatal("a pre-withheld SQLite CHECK accepted 'withheld'; the deny-closed contract of the skip is broken")
	}
}

// TestSQLiteEvidenceStateVocabProbePrecision pins the SQLite probe against the
// F-4 class: the diagnostic must read the `state IN (...)` segment itself, so a
// 'withheld' appearing in an UNRELATED constraint can neither silence the stale
// warning nor fake a current one.
//
// MUTATION VERIFIED (round-2 method): reverting the probe to
// strings.Contains(ddl, "withheld") over the whole DDL turns the decoy case red
// — the decoy constraint would silence the only operator diagnostic.
func TestSQLiteEvidenceStateVocabProbePrecision(t *testing.T) {
	const staleWithDecoy = `CREATE TABLE evidence_operations (
  state TEXT NOT NULL,
  surface TEXT NOT NULL,
  CHECK (state IN ('claimed','completed','not_sent','unknown','blocked')),
  CHECK (surface <> 'withheld')
)`
	if stale, found := sqliteEvidenceStateVocabStale(staleWithDecoy); !found || !stale {
		t.Fatalf("stale-with-decoy: (stale=%t, found=%t), want (true, true): a 'withheld' outside the state CHECK must not silence the stale diagnostic", stale, found)
	}
	current := strings.Replace(staleWithDecoy, "'blocked')", "'blocked','withheld')", 1)
	if stale, found := sqliteEvidenceStateVocabStale(current); !found || stale {
		t.Fatalf("current DDL: (stale=%t, found=%t), want (false, true)", stale, found)
	}
	if stale, found := sqliteEvidenceStateVocabStale("CREATE TABLE evidence_operations (state TEXT, note TEXT CHECK (note <> 'withheld'))"); found || stale {
		t.Fatalf("no state CHECK: (stale=%t, found=%t), want (false, false) — absence is its own diagnostic, not staleness", stale, found)
	}
}

// TestEvidenceOpStateCheckPGWidenedInPlace: an existing Postgres journal gains
// the current vocabulary in one transaction — stale vocabulary CHECK dropped,
// the named constraint added and VALIDATED at ADD time — idempotently, while
// still refusing words outside the vocabulary.
func TestEvidenceOpStateCheckPGWidenedInPlace(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, preWithheldEvidenceOpsDDL); err != nil {
		t.Fatalf("create pre-withheld table: %v", err)
	}
	// A historical row under the OLD vocabulary: the in-transaction validation
	// must scan past it.
	if err := insertEvidenceOpState(ctx, db, "hist", "blocked"); err != nil {
		t.Fatalf("seed historical row: %v", err)
	}
	if err := reconcileEvidenceOpStateCheck(ctx, db, pgDialect(t)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if err := insertEvidenceOpState(ctx, db, "new", "withheld"); err != nil {
		t.Fatalf("'withheld' still refused after the widening: %v", err)
	}
	if err := insertEvidenceOpState(ctx, db, "bad", "bogus"); err == nil {
		t.Fatal("the widened CHECK accepted an out-of-vocabulary state; the backstop is gone")
	}
	// Idempotent: a second boot probes the catalog and changes nothing.
	if err := reconcileEvidenceOpStateCheck(ctx, db, pgDialect(t)); err != nil {
		t.Fatalf("re-reconcile must be a no-op: %v", err)
	}
	cons := evidenceVocabConstraints(t, ctx, db)
	got, ok := cons[evidenceOpStateVocabConstraint]
	if len(cons) != 1 || !ok {
		t.Fatalf("constraints = %v, want exactly the named vocabulary CHECK (stale dropped, no duplicates on re-run)", cons)
	}
	if !got.validated || !evidenceVocabCheckCurrent(got.def) {
		t.Fatalf("vocabulary CHECK = %+v, want validated and current", got)
	}
}

// TestEvidenceOpStateCheckPGShadowSchemaHomonymDoesNotSatisfy is the F-4
// reproduction from the round-2 audit: a homonymous table in ANOTHER schema
// whose CHECK mentions the whole current vocabulary must not make the visible
// target look up to date — the probes resolve the ONE visible relation by OID.
//
// MUTATION VERIFIED (round-2 method): reverting the up-to-date probe to the
// round-1 relname/substring form turns this red — the reconcile returns nil,
// the target CHECK stays stale, and the 'withheld' insert below fails.
func TestEvidenceOpStateCheckPGShadowSchemaHomonymDoesNotSatisfy(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, preWithheldEvidenceOpsDDL); err != nil {
		t.Fatalf("create pre-withheld target: %v", err)
	}
	for _, stmt := range []string{
		"CREATE SCHEMA codex_shadow",
		`CREATE TABLE codex_shadow.evidence_operations (
		   state TEXT NOT NULL,
		   CHECK (state IN ('claimed','completed','not_sent','unknown','blocked','withheld'))
		 )`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("create shadow homonym: %v", err)
		}
	}
	if err := reconcileEvidenceOpStateCheck(ctx, db, pgDialect(t)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if err := insertEvidenceOpState(ctx, db, "new", "withheld"); err != nil {
		t.Fatalf("the visible target was not widened — a shadow-schema homonym satisfied the probe: %v", err)
	}
}

// TestEvidenceOpStateCheckPGPreservesForeignConstraints pins the other F-4 leg:
// the stale-selection must pick vocabulary CHECKs on the state COLUMN — never
// "any CHECK whose text contains the substring state". A CHECK on another
// column mentioning 'state', and a non-vocabulary CHECK on the state column,
// must both survive the widening.
//
// MUTATION VERIFIED (round-2 method): reverting the stale listing to the
// round-1 `pg_get_constraintdef LIKE '%state%'` form turns this red — both
// foreign constraints are dropped by the widening transaction.
func TestEvidenceOpStateCheckPGPreservesForeignConstraints(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, preWithheldEvidenceOpsDDL); err != nil {
		t.Fatalf("create pre-withheld table: %v", err)
	}
	for _, stmt := range []string{
		"ALTER TABLE evidence_operations ADD CONSTRAINT surface_guard CHECK (surface <> 'state')",
		"ALTER TABLE evidence_operations ADD CONSTRAINT state_nonempty CHECK (state <> '')",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("add foreign constraint: %v", err)
		}
	}
	if err := reconcileEvidenceOpStateCheck(ctx, db, pgDialect(t)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	cons := evidenceVocabConstraints(t, ctx, db)
	if _, ok := cons["surface_guard"]; !ok {
		t.Fatal("the widening dropped a CHECK on another column whose text mentions 'state'")
	}
	if _, ok := cons["state_nonempty"]; !ok {
		t.Fatal("the widening dropped a non-vocabulary CHECK on the state column")
	}
	if got, ok := cons[evidenceOpStateVocabConstraint]; !ok || !got.validated {
		t.Fatalf("vocabulary CHECK missing or not validated after the widening: %v", cons)
	}
	if err := insertEvidenceOpState(ctx, db, "new", "withheld"); err != nil {
		t.Fatalf("'withheld' refused after the widening: %v", err)
	}
}

// TestEvidenceOpStateCheckPGFailedWideningIsAtomic is the F-3 property: a
// widening whose in-transaction validation fails (a row outside even the
// CURRENT vocabulary — out-of-band corruption) must roll the WHOLE transition
// back: named boot failure, previous backstop still installed, nothing left
// half-committed — and the next boot retries instead of succeeding.
//
// MUTATION VERIFIED (round-2 method): reverting the transition to round-1's
// autocommitted DROP → ADD → VALIDATE turns this red — after the failed
// reconcile the previous CHECK is gone (the drop committed alone), so the
// backstop-survival assertion fails.
func TestEvidenceOpStateCheckPGFailedWideningIsAtomic(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// A corrupt legacy vocabulary CHECK (out-of-band history): it admits 'bogus',
	// which the CURRENT vocabulary refuses — so the widening's validation fails.
	ddl := strings.Replace(preWithheldEvidenceOpsDDL, "'blocked')", "'blocked','bogus')", 1)
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("create corrupt-legacy table: %v", err)
	}
	if err := insertEvidenceOpState(ctx, db, "bad", "bogus"); err != nil {
		t.Fatalf("seed out-of-vocabulary row: %v", err)
	}
	err = reconcileEvidenceOpStateCheck(ctx, db, pgDialect(t))
	if err == nil {
		t.Fatal("a widening over an out-of-vocabulary row must fail the boot, not succeed")
	}
	if !strings.Contains(err.Error(), evidenceOpStateVocabConstraint) {
		t.Fatalf("the failure must name the vocabulary constraint: %v", err)
	}
	// Failure-atomic: the transition rolled back WHOLE — the previous (corrupt
	// but present) backstop still exists and still refuses states outside ITS
	// list; nothing named evidence_operations_state_vocab was left behind.
	cons := evidenceVocabConstraints(t, ctx, db)
	if _, ok := cons[evidenceOpStateVocabConstraint]; ok {
		t.Fatalf("a failed widening left the new constraint behind (not atomic): %v", cons)
	}
	if len(cons) != 1 {
		t.Fatalf("constraints after the failed widening = %v, want exactly the original CHECK (backstop preserved)", cons)
	}
	if err := insertEvidenceOpState(ctx, db, "worse", "never-a-state"); err == nil {
		t.Fatal("the failed widening removed the previous backstop: an arbitrary state was accepted")
	}
	// A second boot retries the same refusal — it never converts into success
	// while the row stands.
	if err := reconcileEvidenceOpStateCheck(ctx, db, pgDialect(t)); err == nil {
		t.Fatal("the second boot accepted a table the first boot refused, with the corrupt row still present")
	}
	// Repair the row: the same reconcile now completes and the new word writes.
	if _, err := db.ExecContext(ctx, "DELETE FROM evidence_operations WHERE state = 'bogus'"); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if err := reconcileEvidenceOpStateCheck(ctx, db, pgDialect(t)); err != nil {
		t.Fatalf("reconcile after repair: %v", err)
	}
	if err := insertEvidenceOpState(ctx, db, "new", "withheld"); err != nil {
		t.Fatalf("'withheld' refused after repair+widening: %v", err)
	}
}

// TestEvidenceOpStateCheckPGNotValidResidueRevalidated is the F-2 reproduction
// from the round-2 audit: a round-1 upgrade interrupted after a failed VALIDATE
// left the current CHECK installed NOT VALID. The next boot must NOT accept it
// as done — it re-validates, keeps failing by name while the violating row
// stands, and records convalidated=true once the row is repaired.
//
// MUTATION VERIFIED (round-2 method): making the probe accept the current
// constraint without checking convalidated (dropping the re-VALIDATE branch)
// turns this red — the first reconcile returns nil over an unvalidated CHECK.
func TestEvidenceOpStateCheckPGNotValidResidueRevalidated(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The round-1 crash shape: no vocabulary CHECK from CREATE TABLE (the stale
	// one was dropped by the autocommitted round-1 sequence), a violating row,
	// and the current CHECK left NOT VALID by the failed VALIDATE.
	ddl := strings.Replace(preWithheldEvidenceOpsDDL,
		",\n  CHECK (state IN ('claimed','completed','not_sent','unknown','blocked'))", "", 1)
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("create residue table: %v", err)
	}
	if err := insertEvidenceOpState(ctx, db, "bad", "bogus"); err != nil {
		t.Fatalf("seed violating row: %v", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"ALTER TABLE evidence_operations ADD CONSTRAINT %s CHECK (%s) NOT VALID",
		evidenceOpStateVocabConstraint, evidenceOpDescriptor.Checks[0])); err != nil {
		t.Fatalf("install NOT VALID residue: %v", err)
	}
	err = reconcileEvidenceOpStateCheck(ctx, db, pgDialect(t))
	if err == nil {
		t.Fatal("the boot accepted a NOT VALID vocabulary CHECK as done (F-2): it must keep failing until the row is repaired")
	}
	if !strings.Contains(err.Error(), "NOT VALID") {
		t.Fatalf("the failure must name the NOT VALID residue: %v", err)
	}
	// Repair the row: the same boot path validates the residue in place.
	if _, err := db.ExecContext(ctx, "DELETE FROM evidence_operations WHERE state = 'bogus'"); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if err := reconcileEvidenceOpStateCheck(ctx, db, pgDialect(t)); err != nil {
		t.Fatalf("reconcile after repair: %v", err)
	}
	got, ok := evidenceVocabConstraints(t, ctx, db)[evidenceOpStateVocabConstraint]
	if !ok || !got.validated {
		t.Fatalf("the residue was not validated in place: %+v (present=%t)", got, ok)
	}
	if err := insertEvidenceOpState(ctx, db, "new", "withheld"); err != nil {
		t.Fatalf("'withheld' refused after validation: %v", err)
	}
}

// TestEvidenceOpStateCheckPGSameColumnLaxDecoyIsNotCurrent is the round-3
// reproduction of the F-4 residual (Codex r2): a VALIDATED CHECK on the same
// relation and the same conkey whose text contains all six quoted words but
// whose PREDICATE does not restrict the column to the vocabulary (an OR escape
// hatch) must not classify as current. Round 2 recognized "current" by a bag of
// literals, so the decoy satisfied the probe, the REAL stale backstop was
// dropped as stale, no canonical CHECK was added, and an out-of-vocabulary
// state inserted cleanly — the backstop the descriptor promises against even
// raw out-of-band writes was gone.
//
// MUTATION VERIFIED (round-3 method): reverting the current-classification to
// the literal-bag evidenceVocabCheckCurrent (no canonical-definition equality)
// turns this red on the out-of-vocabulary assertion.
func TestEvidenceOpStateCheckPGSameColumnLaxDecoyIsNotCurrent(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, preWithheldEvidenceOpsDDL); err != nil {
		t.Fatalf("create pre-withheld table: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE evidence_operations ADD CONSTRAINT lax_decoy CHECK (state <> '' OR state IN
		 ('claimed','completed','not_sent','unknown','blocked','withheld'))`); err != nil {
		t.Fatalf("install lax decoy: %v", err)
	}
	if err := reconcileEvidenceOpStateCheck(ctx, db, pgDialect(t)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if err := insertEvidenceOpState(ctx, db, "bad", "bogus"); err == nil {
		t.Fatal("same-column lax decoy satisfied the current-vocabulary probe: the real backstop was dropped and an unknown state was accepted")
	}
	if err := insertEvidenceOpState(ctx, db, "new", "withheld"); err != nil {
		t.Fatalf("'withheld' refused after the widening: %v", err)
	}
	if got, ok := evidenceVocabConstraints(t, ctx, db)[evidenceOpStateVocabConstraint]; !ok || !got.validated {
		t.Fatalf("the canonical vocabulary CHECK is missing or not validated: %+v (present=%t)", got, ok)
	}
}
