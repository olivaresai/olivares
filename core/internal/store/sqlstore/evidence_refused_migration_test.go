// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// evidence_refused_migration_test.go - core v11 on real SQLite and on the owned
// PostgreSQL 16 fixture: the closed supported inputs, exact-object negative
// controls, the streamed row witness, injected rollback, supported-version
// preflight injection (not an old binary), tracked-stale refusal and the
// refused-state reader compatibility of this binary.

var errV11Injected = errors.New("v11 injected failure")

func v11Check(words []model.EvidenceOperationState) []string {
	return []string{evidenceOpStateCheckExpr(words)}
}

type v11Row struct {
	id, tenant, state, claim  string
	outcome, result, dispatch any
}

// v11Rows covers every state of one vocabulary in two tenants, with NULL and
// empty-string optional references so the witness distinguishes them.
func v11Rows(words []model.EvidenceOperationState) []v11Row {
	var rows []v11Row
	for i, w := range words {
		for k, tenant := range []string{"tenant-a", "tenant-b"} {
			r := v11Row{id: fmt.Sprintf("row-%d-%d", i, k), tenant: tenant, state: string(w), claim: "ref-claim"}
			if w != model.EvidenceOpClaimed {
				r.outcome = "ref-outcome"
				if k == 0 {
					r.result = ""
				} else {
					r.result, r.dispatch = "digest-result", "dispatch-1"
				}
			}
			rows = append(rows, r)
		}
	}
	return rows
}

const v11InsertSQL = `INSERT INTO evidence_operations
 (id, tenant_id, created_at, updated_at, version, operation_id, effect_digest, surface, action, state,
  claim_evidence_ref, outcome_evidence_ref, result_digest, dispatch_ref, leader_epoch)
 VALUES (?,?,'2026-09-12T00:00:00.000000000Z','2026-09-12T00:00:00.000000000Z',1,?,'digest-v11','mcp.gateway','mcp.tool.call',?,?,?,?,?,1)`

type v11Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func v11Insert(ctx context.Context, q v11Execer, dia dialect.Dialect, r v11Row) error {
	_, err := q.ExecContext(ctx, dia.Rebind(v11InsertSQL), r.id, r.tenant, "op-"+r.id, r.state, r.claim, r.outcome, r.result, r.dispatch)
	return err
}

func v11Witness(t *testing.T, q evidenceCatalogQuerier, dia dialect.Dialect) (int64, [32]byte) {
	t.Helper()
	n, sum, err := evidenceRowWitness(context.Background(), q, dia, evidenceOpDescriptor.Table)
	if err != nil {
		t.Fatalf("witness: %v", err)
	}
	return n, sum
}

// ---------------------------------------------------------------------------
// SQLite

func sqliteV11Dialect(t *testing.T) dialect.Dialect {
	t.Helper()
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("sqlite dialect")
	}
	return dia
}

// newV11SQLite builds an isolated database holding the journal exactly as the
// dialect generates it for the given CHECK list, plus the tenant pin table the
// generated scope triggers read. extra statements then mutate it.
func newV11SQLite(t *testing.T, checks []string, extra ...string) (*sql.DB, dialect.Dialect) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	dia := sqliteV11Dialect(t)
	desc := evidenceOpDescriptor
	desc.Checks = checks
	stmts := append(append(dia.TenancyStmts(), dia.CreateTableStmts(desc)...), extra...)
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("build input %q: %v", s, err)
		}
	}
	return db, dia
}

func sqliteV11Apply(t *testing.T, db *sql.DB, dia dialect.Dialect, fault func(string) error) (evidenceRefusedOutcome, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	out, aerr := applyEvidenceRefusedTransition(ctx, tx, dia, evidenceStateCalibration{}, fault)
	if aerr != nil {
		if rerr := tx.Rollback(); rerr != nil {
			t.Fatalf("rollback: %v", rerr)
		}
		return out, aerr
	}
	return out, tx.Commit()
}

func sqliteV11Schema(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT type, name, COALESCE(sql, '<null>') FROM sqlite_master WHERE name <> '_scope_tenant' ORDER BY type, name`)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, text string
		if err := rows.Scan(&typ, &name, &text); err != nil {
			t.Fatalf("schema: %v", err)
		}
		fmt.Fprintf(&b, "%s|%s|%s\n", typ, name, text)
	}
	return b.String()
}

func TestEvidenceV11SQLiteSupportedInputs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input, action string
		words         []model.EvidenceOperationState
	}{
		{"S5", "migrated", evidenceOpStateWords5},
		{"S6", "migrated", evidenceOpStateWords6},
		{"S7", "verified", evidenceOpStateWords7},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			db, dia := newV11SQLite(t, v11Check(tc.words))
			for _, r := range v11Rows(tc.words) {
				if err := v11Insert(ctx, db, dia, r); err != nil {
					t.Fatalf("insert %s: %v", r.state, err)
				}
			}
			schemaBefore := sqliteV11Schema(t, db)
			n0, sum0 := v11Witness(t, db, dia)
			out, err := sqliteV11Apply(t, db, dia, nil)
			if err != nil {
				t.Fatalf("transition: %v", err)
			}
			if out.Input != tc.input || out.Action != tc.action {
				t.Fatalf("outcome = %+v, want %s/%s", out, tc.input, tc.action)
			}
			if tc.action == "verified" && sqliteV11Schema(t, db) != schemaBefore {
				t.Fatal("verify-only transition changed the schema")
			}
			if got, err := classifySQLiteEvidence(ctx, db, dia); err != nil || got != "S7" {
				t.Fatalf("postcondition = %q, %v", got, err)
			}
			n1, sum1 := v11Witness(t, db, dia)
			if n1 != n0 || sum1 != sum0 || n0 != int64(2*len(tc.words)) {
				t.Fatalf("witness rows %d->%d, equal=%t", n0, n1, sum0 == sum1)
			}
			if err := verifyEvidenceRefusedPerBoot(ctx, db, dia, evidenceStateCalibration{}); err != nil {
				t.Fatalf("per-boot verifier after transition: %v", err)
			}
			refused := v11Row{id: "refused", tenant: "tenant-a", state: string(model.EvidenceOpRefused)}
			if err := v11Insert(ctx, db, dia, refused); err != nil {
				t.Fatalf("refused insert after v11: %v", err)
			}
			withheld := v11Row{id: "withheld", tenant: "tenant-a", state: string(model.EvidenceOpWithheld), claim: "c", outcome: "o"}
			if err := v11Insert(ctx, db, dia, withheld); err != nil {
				t.Fatalf("withheld insert after v11: %v", err)
			}
			if err := v11Insert(ctx, db, dia, v11Row{id: "bogus", tenant: "tenant-a", state: "bogus", claim: "c"}); err == nil {
				t.Fatal("seven-word CHECK accepted an unknown state")
			}
			t.Logf("V11_SQLITE_INPUT|input=%s|action=%s|rows=%d|witness_equal=%t|witness_prefix=%x", out.Input, out.Action, n1, sum0 == sum1, sum1[:4])
		})
	}
}

func TestEvidenceV11SQLiteRefusesUnsupportedInventory(t *testing.T) {
	t.Parallel()
	dia := sqliteV11Dialect(t)
	six := evidenceOpStateCheckExpr(evidenceOpStateWords6)
	generatedTable := dia.CreateTableStmts(evidenceDescriptorWith(evidenceOpStateWords6, ""))[0]
	for _, tc := range []struct {
		name   string
		checks []string
		extra  []string
		table  string // replaces the generated CREATE TABLE when set
	}{
		{name: "no state CHECK predecessor", checks: nil},
		{name: "lax predicate", checks: []string{six + " OR state <> ''"}},
		{name: "extra CHECK", checks: []string{six, "length(operation_id) > 0"}},
		{name: "extra index", checks: v11Check(evidenceOpStateWords6),
			extra: []string{"CREATE INDEX evidence_operations_surface_idx ON evidence_operations(tenant_id, surface)"}},
		{name: "missing scope trigger", checks: v11Check(evidenceOpStateWords6),
			extra: []string{"DROP TRIGGER evidence_operations_scope_del"}},
		{name: "altered scope trigger body", checks: v11Check(evidenceOpStateWords6),
			extra: []string{"DROP TRIGGER evidence_operations_scope_ins",
				"CREATE TRIGGER evidence_operations_scope_ins BEFORE INSERT ON evidence_operations\nBEGIN\n  SELECT 1;\nEND"}},
		{name: "extra trigger", checks: v11Check(evidenceOpStateWords6),
			extra: []string{"CREATE TRIGGER evidence_operations_audit BEFORE INSERT ON evidence_operations\nBEGIN\n  SELECT 1;\nEND"}},
		{name: "rebuild table left behind", checks: v11Check(evidenceOpStateWords6),
			extra: []string{"CREATE TABLE evidence_operations_v11 (id TEXT)"}},
		{name: "comment inside table", table: strings.Replace(generatedTable, "(\n", "( -- note\n", 1)},
		{name: "extra table constraint", table: strings.TrimSuffix(generatedTable, "\n)") + ",\n  UNIQUE(effect_digest)\n)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			var db *sql.DB
			if tc.table != "" {
				stmts := dia.CreateTableStmts(evidenceDescriptorWith(evidenceOpStateWords6, ""))
				db, _ = newV11SQLite(t, v11Check(evidenceOpStateWords6))
				if _, err := db.ExecContext(ctx, "DROP TABLE evidence_operations"); err != nil {
					t.Fatal(err)
				}
				for _, s := range append([]string{tc.table}, stmts[1:]...) {
					if _, err := db.ExecContext(ctx, s); err != nil {
						t.Fatalf("build %q: %v", s, err)
					}
				}
			} else {
				db, _ = newV11SQLite(t, tc.checks, tc.extra...)
			}
			if err := v11Insert(ctx, db, dia, v11Row{id: "r1", tenant: "tenant-a", state: "blocked", claim: "c", outcome: "o"}); err != nil {
				t.Fatalf("insert: %v", err)
			}
			schemaBefore := sqliteV11Schema(t, db)
			n0, sum0 := v11Witness(t, db, dia)
			_, err := sqliteV11Apply(t, db, dia, nil)
			if !errors.Is(err, ErrEvidenceRefusedTransitionInventory) {
				t.Fatalf("transition error = %v, want inventory refusal", err)
			}
			n1, sum1 := v11Witness(t, db, dia)
			if sqliteV11Schema(t, db) != schemaBefore || n1 != n0 || sum1 != sum0 {
				t.Fatal("a refused transition changed the database")
			}
			t.Logf("V11_SQLITE_NEGATIVE|case=%s|refused=true|unchanged=true", tc.name)
		})
	}
}

func TestEvidenceV11SQLiteRollsBackInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []string{"sqlite.after_copy", "sqlite.after_rename"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			db, dia := newV11SQLite(t, v11Check(evidenceOpStateWords6))
			for _, r := range v11Rows(evidenceOpStateWords6) {
				if err := v11Insert(ctx, db, dia, r); err != nil {
					t.Fatal(err)
				}
			}
			schemaBefore := sqliteV11Schema(t, db)
			n0, sum0 := v11Witness(t, db, dia)
			reached := false
			_, err := sqliteV11Apply(t, db, dia, func(s string) error {
				if s == step {
					reached = true
					return errV11Injected
				}
				return nil
			})
			if !reached || !errors.Is(err, errV11Injected) {
				t.Fatalf("injected step reached=%t err=%v", reached, err)
			}
			n1, sum1 := v11Witness(t, db, dia)
			if sqliteV11Schema(t, db) != schemaBefore || n1 != n0 || sum1 != sum0 {
				t.Fatal("rolled-back transition left a change")
			}
			if got, err := classifySQLiteEvidence(ctx, db, dia); err != nil || got != "S6" {
				t.Fatalf("after rollback = %q, %v", got, err)
			}
			if err := v11Insert(ctx, db, dia, v11Row{id: "refused", tenant: "tenant-a", state: "refused"}); err == nil {
				t.Fatal("rolled-back S6 accepted refused")
			}
			t.Logf("V11_SQLITE_ROLLBACK|step=%s|rows=%d|unchanged=true", step, n1)
		})
	}
}

func TestEvidenceV11SQLiteTrackedStaleAndPreV11Refusal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, dia := newV11SQLite(t, v11Check(evidenceOpStateWords6))
	if err := v11Insert(ctx, db, dia, v11Row{id: "early", tenant: "tenant-a", state: "refused"}); err == nil {
		t.Fatal("a six-word journal accepted refused before v11")
	}
	if err := verifyEvidenceRefusedPerBoot(ctx, db, dia, evidenceStateCalibration{}); !errors.Is(err, ErrEvidenceRefusedTrackedStale) {
		t.Fatalf("per-boot verifier on S6 = %v", err)
	}
}

// TestEvidenceV11SQLiteOpenJourney drives the transition through the real Open:
// fresh v11, supported-version preflight injection, an S5 upgrade with rows,
// tracked-stale refusal and an untracked seven-word verify-only boot.
func TestEvidenceV11SQLiteOpenJourney(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dia := sqliteV11Dialect(t)
	path := filepath.Join(t.TempDir(), "store.db")
	open := func() error {
		st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: path, MaxConns: 1}, nil)
		if err != nil {
			return err
		}
		return st.Close()
	}
	withRaw := func(fn func(db *sql.DB)) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		defer db.Close()
		fn(db)
	}
	trackedName := func(db *sql.DB) string {
		var name string
		err := db.QueryRowContext(ctx, "SELECT name FROM "+coreTrackingTable+" WHERE version = ?", coreEvidenceRefusedMigrationVersion).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return name
	}
	rebuild := func(db *sql.DB, words []model.EvidenceOperationState, rows bool) {
		stmts := append([]string{"DELETE FROM " + dialect.ScopeTenantTable, "DROP TABLE evidence_operations"},
			dia.CreateTableStmts(evidenceDescriptorWith(words, ""))...)
		for _, s := range stmts {
			if _, err := db.ExecContext(ctx, s); err != nil {
				t.Fatalf("rebuild %q: %v", s, err)
			}
		}
		if rows {
			for _, r := range v11Rows(words) {
				if err := v11Insert(ctx, db, dia, r); err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	if err := open(); err != nil {
		t.Fatalf("fresh open: %v", err)
	}
	withRaw(func(db *sql.DB) {
		if trackedName(db) != coreEvidenceRefusedMigrationName {
			t.Fatal("fresh open did not record v11")
		}
		if got, err := classifySQLiteEvidence(ctx, db, dia); err != nil || got != "S7" {
			t.Fatalf("fresh journal = %q, %v", got, err)
		}
		// Supported-version preflight injection: the preflight configured for v10
		// refuses this database. This is not an execution of an old binary.
		if err := preflightCoreMigrationVersion(ctx, db, dia, coreUserAuthorityMigrationVersion); !errors.Is(err, ErrCoreSchemaVersionAhead) {
			t.Fatalf("v10-configured preflight = %v", err)
		}
		rebuild(db, evidenceOpStateWords5, true)
		// Rewind core v13 (login capability) together with the v11 tracking row. Deleting
		// only v11 while v13 stays tracked leaves the history [1..10,13], which is NOT a
		// contiguous prefix of the compiled 1..11,13 plan (v12 is reserved and
		// unregistered) and is correctly refused by the boot classifier. A real pre-v11
		// database has no v13 either, so the legal on-disk state to model here is [1..10]
		// with v13's relation absent — the same newest-first rewind the shared historical
		// fixtures use. The next Open re-applies v11 (S5->S7) and re-creates v13.
		if err := rewindLoginCapabilityForHistoricalFixture(ctx, db, dia); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "DELETE FROM "+coreTrackingTable+" WHERE version = ?", coreEvidenceRefusedMigrationVersion); err != nil {
			t.Fatal(err)
		}
	})
	var n0 int64
	var sum0 [32]byte
	withRaw(func(db *sql.DB) { n0, sum0 = v11Witness(t, db, dia) })
	if err := open(); err != nil {
		t.Fatalf("open S5 upgrade: %v", err)
	}
	withRaw(func(db *sql.DB) {
		n1, sum1 := v11Witness(t, db, dia)
		if trackedName(db) != coreEvidenceRefusedMigrationName || n1 != n0 || sum1 != sum0 || n0 != int64(2*len(evidenceOpStateWords5)) {
			t.Fatalf("S5 upgrade: tracked=%q rows %d->%d equal=%t", trackedName(db), n0, n1, sum0 == sum1)
		}
		if got, err := classifySQLiteEvidence(ctx, db, dia); err != nil || got != "S7" {
			t.Fatalf("upgraded journal = %q, %v", got, err)
		}
		rebuild(db, evidenceOpStateWords6, false)
	})
	if err := open(); !errors.Is(err, ErrEvidenceRefusedTrackedStale) {
		t.Fatalf("open with tracked v11 and S6 journal = %v", err)
	}
	var schemaBefore string
	withRaw(func(db *sql.DB) {
		rebuild(db, evidenceOpStateWords7, true)
		// Capture the target schema while login_capability_observation is still present: it
		// is the schema a successful verify-only v11 boot must reproduce. The v13 rewind
		// below drops that relation, and Open re-creates it from the identical dialect DDL,
		// so the comparison after Open still proves the boot left the evidence journal
		// itself untouched (a byte-identical DROP+CREATE round-trips through sqlite_master).
		schemaBefore = sqliteV11Schema(t, db)
		// Rewind v13 together with the v11 tracking row so the on-disk history is the legal
		// contiguous prefix [1..10]; deleting only v11 would leave the refused [1..10,13].
		if err := rewindLoginCapabilityForHistoricalFixture(ctx, db, dia); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "DELETE FROM "+coreTrackingTable+" WHERE version = ?", coreEvidenceRefusedMigrationVersion); err != nil {
			t.Fatal(err)
		}
	})
	if err := open(); err != nil {
		t.Fatalf("open untracked S7: %v", err)
	}
	withRaw(func(db *sql.DB) {
		if trackedName(db) != coreEvidenceRefusedMigrationName {
			t.Fatal("verify-only boot did not record v11")
		}
		if got := sqliteV11Schema(t, db); got != schemaBefore {
			t.Fatal("verify-only boot changed the evidence journal schema")
		}
	})
	t.Logf("V11_SQLITE_OPEN|fresh=S7|preflight_v10=ahead|s5_upgrade_rows=%d|tracked_stale=refused|untracked_s7=verified", n0)
}

// TestEvidenceRefusedStateReaderCompatibility is the same-binary reader half:
// the model, the codec, generic Settle and generic Claim on a real store.
func TestEvidenceRefusedStateReaderCompatibility(t *testing.T) {
	t.Parallel()
	if !model.EvidenceOpRefused.Valid() || model.EvidenceOpRefused.Terminal() || model.EvidenceOpClaimed.Terminal() {
		t.Fatal("refused must be valid and not terminal; claimed stays non-terminal")
	}
	terminal := 0
	for _, s := range evidenceOpStateWords7 {
		if s.Terminal() {
			terminal++
		}
	}
	if terminal != 5 {
		t.Fatalf("terminal states = %d, want exactly five", terminal)
	}
	for _, forged := range []model.EvidenceOperation{
		{OperationID: "f1", State: model.EvidenceOpRefused, ClaimEvidenceRef: "x"},
		{OperationID: "f2", State: model.EvidenceOpRefused, OutcomeEvidenceRef: "x"},
		{OperationID: "f3", State: model.EvidenceOpRefused, ResultDigest: "x"},
		{OperationID: "f4", State: model.EvidenceOpRefused, DispatchRef: "x"},
	} {
		if err := validateEvidenceOpRow(forged); !errors.Is(err, store.ErrEvidenceIntegrity) {
			t.Fatalf("forged refused row %s decoded: %v", forged.OperationID, err)
		}
	}
	if err := validateEvidenceOpRow(model.EvidenceOperation{OperationID: "ok", State: model.EvidenceOpRefused}); err != nil {
		t.Fatalf("blank refused row: %v", err)
	}
	if err := store.ValidateEvidenceSettlement(store.EvidenceSettlement{
		OperationID: "op", EffectDigest: "d", State: model.EvidenceOpRefused, Actor: "a", ActorKind: model.ActorUser,
	}); !errors.Is(err, store.ErrEvidenceInvalid) {
		t.Fatalf("a refused settlement request validated: %v", err)
	}

	ctx := context.Background()
	st, path := openInitializedSQLiteTestCopy(t, initializedSQLiteCore)
	tenant := provisionTenant(t, st, "ev-refused")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	dia := sqliteV11Dialect(t)
	insertRaw := func(id, claimRef string) {
		tx, err := raw.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+dialect.ScopeTenantTable); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, dia.Rebind(v11InsertSQL), model.NewID().String(), string(tenant), id, "refused", claimRef, nil, nil, nil); err != nil {
			t.Fatalf("insert refused row: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	insertRaw("op-refused", "")

	settled, settleErr := store.SettleEvidenceOperation(ctx, st, tenant, store.EvidenceSettlement{
		OperationID: "op-refused", EffectDigest: "digest-v11", State: model.EvidenceOpCompleted,
		Actor: "user:test", ActorKind: model.ActorUser,
	})
	if !errors.Is(settleErr, store.ErrEvidenceIntegrity) || settled.Fresh {
		t.Fatalf("settle of a refused row = %+v, %v; want ErrEvidenceIntegrity and no settlement", settled, settleErr)
	}
	settleIntegrity := errors.Is(settleErr, store.ErrEvidenceIntegrity)
	row, err := getEvidenceOp(t, st, tenant, "op-refused")
	if err != nil || row.State != model.EvidenceOpRefused || row.OutcomeEvidenceRef != "" || row.ClaimEvidenceRef != "" {
		t.Fatalf("refused row after settle attempt = %+v, %v", row, err)
	}
	claimed, claimErr := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-refused", "digest-v11"))
	if claimErr != nil {
		t.Fatalf("same-digest claim replay of a refused row returned an error: %v", claimErr)
	}
	if claimed.Fresh || claimed.Op.OperationID != "op-refused" || claimed.Op.State != model.EvidenceOpRefused ||
		claimed.Op.ID != row.ID || claimed.Op.Version != row.Version || claimed.Op.ClaimEvidenceRef != "" ||
		claimed.Receipt.EvidenceRef != "" || claimed.Receipt.Fault != sdk.EvidenceFaultWriteError ||
		!claimed.Receipt.MustRefuse(claimed.Binding) {
		t.Fatalf("claim replay of a refused row = %+v; want the unchanged refused row, Fresh=false, blank anchor and a refusing write_error receipt", claimed)
	}
	after, err := getEvidenceOp(t, st, tenant, "op-refused")
	if err != nil || after != row {
		t.Fatalf("refused row changed across the claim replay: %+v -> %+v, %v", row, after, err)
	}
	insertRaw("op-forged", "forged-anchor")
	if _, err := getEvidenceOp(t, st, tenant, "op-forged"); !errors.Is(err, store.ErrEvidenceIntegrity) {
		t.Fatalf("forged refused row decoded: %v", err)
	}
	t.Logf("V11_READER|settle_refused=%t|settle_integrity=%t|settle_fault=%v|claim_replay_fresh=%t|claim_replay_fault=%v",
		settleErr != nil || settled.Receipt.Fault != sdk.EvidenceFaultNone, settleIntegrity, settled.Receipt.Fault,
		claimed.Fresh, claimed.Receipt.Fault)
}

// ---------------------------------------------------------------------------
// PostgreSQL 16

type v11PG struct {
	owner, super, app *sql.DB
	dia               dialect.Dialect
	cal               evidenceStateCalibration
}

func newV11Postgres(t *testing.T, split bool) v11PG {
	t.Helper()
	ctx := context.Background()
	pg := isolatedPG(t)
	if split {
		pg = isolatedPGSplit(t)
	}
	openDB := func(dsn string) *sql.DB {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	p := v11PG{owner: openDB(pg.Owner), super: openDB(pg.Superuser), app: openDB(pg.App), dia: pgDialect(t)}
	cal, err := calibrateEvidenceStates(ctx, p.owner, p.dia)
	if err != nil {
		t.Fatalf("calibrate: %v", err)
	}
	p.cal = cal
	return p
}

func (p v11PG) build(t *testing.T, checks []string, extra ...string) {
	t.Helper()
	desc := evidenceOpDescriptor
	desc.Checks = checks
	for _, s := range append(p.dia.CreateTableStmts(desc), extra...) {
		if _, err := p.owner.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("build %q: %v", s, err)
		}
	}
}

func (p v11PG) insert(t *testing.T, r v11Row) error {
	t.Helper()
	ctx := context.Background()
	tx, err := p.owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT pg_catalog.set_config('app.tenant_id', $1, true)", r.tenant); err != nil {
		t.Fatal(err)
	}
	if err := v11Insert(ctx, tx, p.dia, r); err != nil {
		return err
	}
	return tx.Commit()
}

func (p v11PG) apply(t *testing.T, db *sql.DB, fault func(string) error) (evidenceRefusedOutcome, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, aerr := applyEvidenceRefusedTransition(ctx, tx, p.dia, p.cal, fault)
	if aerr != nil {
		_ = tx.Rollback()
		return out, aerr
	}
	return out, tx.Commit()
}

// snapshot is an independent raw catalog reading, taken as superuser, so a
// refused or rolled-back transition can be shown to have changed nothing.
func (p v11PG) snapshot(t *testing.T) string {
	t.Helper()
	var s string
	if err := p.super.QueryRowContext(context.Background(), `SELECT pg_catalog.concat_ws(' | ',
	  (SELECT pg_catalog.string_agg(conname || ':' || pg_catalog.pg_get_constraintdef(oid) || ':' || convalidated::text, ',' ORDER BY conname)
	     FROM pg_catalog.pg_constraint WHERE conrelid = 'evidence_operations'::regclass),
	  (SELECT pg_catalog.string_agg(pg_catalog.pg_get_indexdef(indexrelid), ',' ORDER BY indexrelid::regclass::text)
	     FROM pg_catalog.pg_index WHERE indrelid = 'evidence_operations'::regclass),
	  (SELECT pg_catalog.string_agg(polname || ':' || COALESCE(pg_catalog.pg_get_expr(polqual, polrelid), ''), ',' ORDER BY polname)
	     FROM pg_catalog.pg_policy WHERE polrelid = 'evidence_operations'::regclass),
	  (SELECT COALESCE(pg_catalog.string_agg(tgname, ',' ORDER BY tgname), '-')
	     FROM pg_catalog.pg_trigger WHERE tgrelid = 'evidence_operations'::regclass),
	  (SELECT relrowsecurity::text || '/' || relforcerowsecurity::text || '/' || COALESCE(relacl::text, '-') || '/' || relowner::regrole::text
	     FROM pg_catalog.pg_class WHERE oid = 'evidence_operations'::regclass))`).Scan(&s); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return s
}

func TestEvidenceV11PostgresSupportedInputs(t *testing.T) {
	t.Parallel()
	six := evidenceOpStateCheckExpr(evidenceOpStateWords6)
	seven := evidenceOpStateCheckExpr(evidenceOpStateWords7)
	for _, tc := range []struct {
		input, action string
		checks        []string
		extra         []string
		words         []model.EvidenceOperationState
	}{
		{"P5", "migrated", v11Check(evidenceOpStateWords5), nil, evidenceOpStateWords5},
		{"P6-fresh", "migrated", v11Check(evidenceOpStateWords6), []string{"GRANT SELECT ON evidence_operations TO PUBLIC"}, evidenceOpStateWords6},
		{"P6-widened", "migrated", nil, []string{"ALTER TABLE evidence_operations ADD CONSTRAINT evidence_operations_state_vocab CHECK (" + six + ")"}, evidenceOpStateWords6},
		{"P7-state_check", "verified", v11Check(evidenceOpStateWords7), nil, evidenceOpStateWords7},
		{"P7-state_vocab", "verified", nil, []string{"ALTER TABLE evidence_operations ADD CONSTRAINT evidence_operations_state_vocab CHECK (" + seven + ")"}, evidenceOpStateWords7},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			p := newV11Postgres(t, false)
			p.build(t, tc.checks, tc.extra...)
			for _, r := range v11Rows(tc.words) {
				if err := p.insert(t, r); err != nil {
					t.Fatalf("insert %s: %v", r.state, err)
				}
			}
			before := p.snapshot(t)
			n0, sum0 := v11Witness(t, p.super, p.dia)
			var acl0 string
			if err := p.super.QueryRowContext(ctx, "SELECT COALESCE(relacl::text, '-') FROM pg_catalog.pg_class WHERE oid = 'evidence_operations'::regclass").Scan(&acl0); err != nil {
				t.Fatal(err)
			}
			out, err := p.apply(t, p.owner, nil)
			if err != nil {
				t.Fatalf("transition: %v", err)
			}
			if out.Input != tc.input || out.Action != tc.action {
				t.Fatalf("outcome = %+v", out)
			}
			after := p.snapshot(t)
			if tc.action == "verified" && after != before {
				t.Fatalf("verify-only changed the relation:\n%s\n%s", before, after)
			}
			inv, err := readPostgresEvidenceInventory(ctx, p.owner, p.dia, p.cal)
			if err != nil {
				t.Fatalf("postcondition inventory: %v", err)
			}
			got, err := inv.classify(p.cal)
			want := tc.input
			if tc.action == "migrated" {
				want = "P7-state_vocab"
			}
			if err != nil || got != want {
				t.Fatalf("postcondition = %q, %v; want %s", got, err, want)
			}
			var acl1 string
			if err := p.super.QueryRowContext(ctx, "SELECT COALESCE(relacl::text, '-') FROM pg_catalog.pg_class WHERE oid = 'evidence_operations'::regclass").Scan(&acl1); err != nil {
				t.Fatal(err)
			}
			n1, sum1 := v11Witness(t, p.super, p.dia)
			if acl1 != acl0 || n1 != n0 || sum1 != sum0 || n0 != int64(2*len(tc.words)) {
				t.Fatalf("acl %q->%q rows %d->%d witness equal=%t", acl0, acl1, n0, n1, sum0 == sum1)
			}
			if tc.input == "P6-fresh" && !strings.Contains(acl1, "=r/") {
				t.Fatalf("non-default PUBLIC grant not preserved: %q", acl1)
			}
			if err := verifyEvidenceRefusedPerBoot(ctx, p.owner, p.dia, p.cal); err != nil {
				t.Fatalf("per-boot verifier: %v", err)
			}
			if err := p.insert(t, v11Row{id: "refused", tenant: "tenant-a", state: "refused"}); err != nil {
				t.Fatalf("refused insert after v11: %v", err)
			}
			if err := p.insert(t, v11Row{id: "bogus", tenant: "tenant-a", state: "bogus", claim: "c"}); err == nil {
				t.Fatal("unknown state accepted")
			}
			t.Logf("V11_PG_INPUT|input=%s|action=%s|rows=%d|witness_equal=%t|acl_equal=%t|nondefault_acl=%t", out.Input, out.Action, n1, sum0 == sum1, acl0 == acl1, acl1 != "-")
		})
	}
}

func TestEvidenceV11PostgresRefusesUnsupportedInventory(t *testing.T) {
	t.Parallel()
	five := evidenceOpStateCheckExpr(evidenceOpStateWords5)
	six := evidenceOpStateCheckExpr(evidenceOpStateWords6)
	const drop = "ALTER TABLE evidence_operations DROP CONSTRAINT evidence_operations_state_check"
	for _, tc := range []struct {
		name  string
		extra []string
	}{
		{"NOT VALID residue", []string{drop, "ALTER TABLE evidence_operations ADD CONSTRAINT evidence_operations_state_check CHECK (" + six + ") NOT VALID"}},
		{"unknown constraint name", []string{drop, "ALTER TABLE evidence_operations ADD CONSTRAINT evidence_operations_states CHECK (" + six + ")"}},
		{"five words under vocab name", []string{drop, "ALTER TABLE evidence_operations ADD CONSTRAINT evidence_operations_state_vocab CHECK (" + five + ")"}},
		{"lax predicate", []string{drop, "ALTER TABLE evidence_operations ADD CONSTRAINT evidence_operations_state_check CHECK (" + six + " OR state <> '')"}},
		{"second state CHECK", []string{"ALTER TABLE evidence_operations ADD CONSTRAINT evidence_operations_state_vocab CHECK (" + six + ")"}},
		{"extra multicolumn CHECK", []string{"ALTER TABLE evidence_operations ADD CONSTRAINT evidence_operations_epoch_check CHECK (version > 0 AND leader_epoch >= 0)"}},
		{"extra unique constraint", []string{"ALTER TABLE evidence_operations ADD CONSTRAINT evidence_operations_digest_key UNIQUE (effect_digest)"}},
		{"extra index", []string{"CREATE INDEX evidence_operations_surface_idx ON evidence_operations(tenant_id, surface)"}},
		{"partial index in place of generated", []string{"DROP INDEX evidence_operations_state_idx",
			"CREATE INDEX evidence_operations_state_idx ON evidence_operations(tenant_id, state) WHERE state <> 'claimed'"}},
		{"extra trigger", []string{"CREATE FUNCTION ev90_v11_noop() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$",
			"CREATE TRIGGER evidence_operations_noop BEFORE INSERT ON evidence_operations FOR EACH ROW EXECUTE FUNCTION ev90_v11_noop()"}},
		{"altered tenant policy", []string{"DROP POLICY tenant_isolation ON evidence_operations",
			"CREATE POLICY tenant_isolation ON evidence_operations USING (true) WITH CHECK (true)"}},
		{"row security not forced", []string{"ALTER TABLE evidence_operations NO FORCE ROW LEVEL SECURITY"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newV11Postgres(t, false)
			p.build(t, v11Check(evidenceOpStateWords6), tc.extra...)
			if err := p.insert(t, v11Row{id: "r1", tenant: "tenant-a", state: "blocked", claim: "c", outcome: "o"}); err != nil {
				t.Fatalf("insert: %v", err)
			}
			before := p.snapshot(t)
			n0, sum0 := v11Witness(t, p.super, p.dia)
			_, err := p.apply(t, p.owner, nil)
			if !errors.Is(err, ErrEvidenceRefusedTransitionInventory) {
				t.Fatalf("transition error = %v, want inventory refusal", err)
			}
			n1, sum1 := v11Witness(t, p.super, p.dia)
			if p.snapshot(t) != before || n1 != n0 || sum1 != sum0 {
				t.Fatal("a refused transition changed the relation")
			}
			t.Logf("V11_PG_NEGATIVE|case=%s|refused=true|unchanged=true", tc.name)
		})
	}
}

func TestEvidenceV11PostgresRollbackNonOwnerAndTrackedStale(t *testing.T) {
	t.Parallel()
	t.Run("injected failure after DROP CONSTRAINT", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		p := newV11Postgres(t, false)
		p.build(t, v11Check(evidenceOpStateWords6))
		for _, r := range v11Rows(evidenceOpStateWords6) {
			if err := p.insert(t, r); err != nil {
				t.Fatal(err)
			}
		}
		before := p.snapshot(t)
		n0, sum0 := v11Witness(t, p.super, p.dia)
		reached := false
		_, err := p.apply(t, p.owner, func(step string) error {
			if step == "postgres.after_drop_constraint" {
				reached = true
				return errV11Injected
			}
			return nil
		})
		if !reached || !errors.Is(err, errV11Injected) {
			t.Fatalf("reached=%t err=%v", reached, err)
		}
		n1, sum1 := v11Witness(t, p.super, p.dia)
		if p.snapshot(t) != before || n1 != n0 || sum1 != sum0 {
			t.Fatal("rolled-back transition left a change")
		}
		if err := verifyEvidenceRefusedPerBoot(ctx, p.owner, p.dia, p.cal); !errors.Is(err, ErrEvidenceRefusedTrackedStale) {
			t.Fatalf("per-boot verifier on P6 = %v", err)
		}
		if err := p.insert(t, v11Row{id: "refused", tenant: "tenant-a", state: "refused"}); err == nil {
			t.Fatal("P6 accepted refused before v11")
		}
		t.Logf("V11_PG_ROLLBACK|step=postgres.after_drop_constraint|rows=%d|unchanged=true|tracked_stale=refused", n1)
	})
	t.Run("non-owner role", func(t *testing.T) {
		t.Parallel()
		p := newV11Postgres(t, true)
		p.build(t, v11Check(evidenceOpStateWords6))
		before := p.snapshot(t)
		_, err := p.apply(t, p.app, nil)
		if !errors.Is(err, ErrEvidenceRefusedNotOwner) {
			t.Fatalf("non-owner transition = %v", err)
		}
		if p.snapshot(t) != before {
			t.Fatal("non-owner refusal changed the relation")
		}
		t.Log("V11_PG_NON_OWNER|refused=true|unchanged=true")
	})
}

// TestEvidenceV11PostgresOpenJourney drives v11 through the real Open on a split
// owner topology: fresh migrate, supported-version preflight injection, a
// P6-fresh upgrade with rows, and tracked-stale refusal.
func TestEvidenceV11PostgresOpenJourney(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pg := isolatedPGSplit(t)
	p := newV11PostgresFrom(t, pg.Owner, pg.Superuser, pg.App)
	open := func() error {
		st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 4}, nil)
		if err != nil {
			return err
		}
		return st.Close()
	}
	exec := func(stmts ...string) {
		for _, s := range stmts {
			if _, err := p.owner.ExecContext(ctx, s); err != nil {
				t.Fatalf("%q: %v", s, err)
			}
		}
	}
	classify := func() string {
		inv, err := readPostgresEvidenceInventory(ctx, p.owner, p.dia, p.cal)
		if err != nil {
			return "ERR " + err.Error()
		}
		got, err := inv.classify(p.cal)
		if err != nil {
			return "ERR " + err.Error()
		}
		return got
	}
	six := evidenceOpStateCheckExpr(evidenceOpStateWords6)
	untrack := fmt.Sprintf("DELETE FROM %s WHERE version = %d", coreTrackingTable, coreEvidenceRefusedMigrationVersion)

	if err := open(); err != nil {
		t.Fatalf("fresh open: %v", err)
	}
	var name string
	if err := p.owner.QueryRowContext(ctx, "SELECT name FROM "+coreTrackingTable+" WHERE version = $1", coreEvidenceRefusedMigrationVersion).Scan(&name); err != nil || name != coreEvidenceRefusedMigrationName {
		t.Fatalf("fresh v11 tracking = %q, %v", name, err)
	}
	// v2 renders the historical six-word journal, so a fresh database reaches
	// seven words through the v11 migrate action.
	if got := classify(); got != "P7-state_vocab" {
		t.Fatalf("fresh journal = %s", got)
	}
	if err := preflightCoreMigrationVersion(ctx, p.owner, p.dia, coreUserAuthorityMigrationVersion); !errors.Is(err, ErrCoreSchemaVersionAhead) {
		t.Fatalf("v10-configured preflight = %v", err)
	}

	// Rewind core v13 (login capability) together with the v11 tracking row. As on SQLite,
	// deleting only v11 while v13 stays tracked leaves the history [1..10,13], which is NOT a
	// contiguous prefix of the compiled 1..11,13 plan (v12 reserved and unregistered) and is
	// correctly refused by the boot classifier. A real pre-v11 database has no v13 either, so
	// the legal on-disk state to model is [1..10] with v13's relation absent; the next Open
	// re-applies v11 (P6->P7) and re-creates v13.
	if err := rewindLoginCapabilityForHistoricalFixture(ctx, p.owner, p.dia); err != nil {
		t.Fatal(err)
	}
	exec(untrack, "ALTER TABLE evidence_operations DROP CONSTRAINT evidence_operations_state_vocab",
		"ALTER TABLE evidence_operations ADD CONSTRAINT evidence_operations_state_check CHECK ("+six+")")
	for _, r := range v11Rows(evidenceOpStateWords6) {
		if err := p.insert(t, r); err != nil {
			t.Fatal(err)
		}
	}
	if got := classify(); got != "P6-fresh" {
		t.Fatalf("prepared journal = %s", got)
	}
	n0, sum0 := v11Witness(t, p.super, p.dia)
	if err := open(); err != nil {
		t.Fatalf("open P6 upgrade: %v", err)
	}
	n1, sum1 := v11Witness(t, p.super, p.dia)
	if got := classify(); got != "P7-state_vocab" || n1 != n0 || sum1 != sum0 {
		t.Fatalf("upgrade = %s rows %d->%d equal=%t", got, n0, n1, sum0 == sum1)
	}

	exec("ALTER TABLE evidence_operations DROP CONSTRAINT evidence_operations_state_vocab",
		"ALTER TABLE evidence_operations ADD CONSTRAINT evidence_operations_state_vocab CHECK ("+six+")")
	if err := open(); !errors.Is(err, ErrEvidenceRefusedTrackedStale) {
		t.Fatalf("open with tracked v11 and P6 journal = %v", err)
	}
	t.Logf("V11_PG_OPEN|fresh=P7-state_vocab|preflight_v10=ahead|p6_upgrade_rows=%d|witness_equal=true|tracked_stale=refused", n1)
}

func newV11PostgresFrom(t *testing.T, ownerDSN, superDSN, appDSN string) v11PG {
	t.Helper()
	openDB := func(dsn string) *sql.DB {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	p := v11PG{owner: openDB(ownerDSN), super: openDB(superDSN), app: openDB(appDSN), dia: pgDialect(t)}
	cal, err := calibrateEvidenceStates(context.Background(), p.owner, p.dia)
	if err != nil {
		t.Fatalf("calibrate: %v", err)
	}
	p.cal = cal
	return p
}
