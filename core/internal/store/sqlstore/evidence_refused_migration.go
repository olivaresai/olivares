// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Core v11 admits the evidence-operation state 'refused' at the database on both
// engines. It is one atomic migration: the journal DDL, the row-preservation
// witness and the tracking row commit together or not at all.
//
// The supported inputs are a closed inventory generated from this binary's own
// descriptor and dialect, never a pattern:
//
//	SQLite      S5, S6 -> rebuilt to S7; S7 -> verified only.
//	PostgreSQL  P5 (evidence_operations_state_check), P6-fresh (_state_check),
//	            P6-widened (_state_vocab) -> constraint replaced by the seven-word
//	            _state_vocab; P7 under either name -> verified only.
//
// Anything else, including an absent relation, a SQLite table without a state
// CHECK, a lax or extra CHECK, a NOT VALID residue, or any extra or altered
// index, trigger, policy or column, refuses unchanged.
const (
	coreEvidenceRefusedMigrationVersion = 11
	coreEvidenceRefusedMigrationName    = "evidence_operation_refused_state"

	evidenceOpStateCheckFreshName = "evidence_operations_state_check"
	evidenceRefusedRebuildTable   = "evidence_operations_v11"
)

var (
	// ErrEvidenceRefusedTransitionInventory: the journal relation is not one of
	// the closed supported inputs. Nothing was changed.
	ErrEvidenceRefusedTransitionInventory = errors.New("sqlstore: evidence_operations is not a supported core v11 transition input")
	// ErrEvidenceRefusedWitness: the row witness differed across the transition.
	ErrEvidenceRefusedWitness = errors.New("sqlstore: evidence_operations rows changed across the core v11 transition")
	// ErrEvidenceRefusedNotOwner: PostgreSQL v11 runs only as the relation owner.
	ErrEvidenceRefusedNotOwner = errors.New("sqlstore: core v11 must run as the evidence_operations owner")
	// ErrEvidenceRefusedTrackedStale: v11 is recorded but the relation is not the
	// exact seven-word journal.
	ErrEvidenceRefusedTrackedStale = errors.New("sqlstore: core v11 is recorded but evidence_operations is not the verified v11 journal")
)

// evidenceStateCalibration carries the server-deparsed expected expressions for
// PostgreSQL. It is computed once per boot by calibrateEvidenceStates on the
// migration-lock connection, BEFORE any migration transaction opens, and is
// never mutated afterward. SQLite needs none.
type evidenceStateCalibration struct {
	ready       bool
	check5      string
	check6      string
	check7      string
	policyUsing string
	policyCheck string
	// columnCollations maps each generated journal column to the collation
	// identity ("schema.name", or "" for a non-collatable type) the server
	// assigns to the dialect's own column definition.
	columnCollations map[string]string
}

// calibrateEvidenceStates runs the probe helpers, each of which owns and rolls
// back its own transaction. It must not be called inside a migration transaction.
func calibrateEvidenceStates(ctx context.Context, db dialect.Execer, dia dialect.Dialect) (evidenceStateCalibration, error) {
	if dia.Name() != store.EnginePostgres {
		return evidenceStateCalibration{}, nil
	}
	var cal evidenceStateCalibration
	var err error
	if cal.check5, err = pgCanonicalStateCheckDef(ctx, db, evidenceOpStateCheckExpr(evidenceOpStateWords5)); err != nil {
		return evidenceStateCalibration{}, fmt.Errorf("sqlstore: core v11 calibration: %w", err)
	}
	if cal.check6, err = pgCanonicalStateCheckDef(ctx, db, evidenceOpStateCheckExpr(evidenceOpStateWords6)); err != nil {
		return evidenceStateCalibration{}, fmt.Errorf("sqlstore: core v11 calibration: %w", err)
	}
	if cal.check7, err = pgCanonicalStateCheckDef(ctx, db, evidenceOpStateCheckExpr(evidenceOpStateWords7)); err != nil {
		return evidenceStateCalibration{}, fmt.Errorf("sqlstore: core v11 calibration: %w", err)
	}
	if cal.policyUsing, cal.policyCheck, cal.columnCollations, err = pgCanonicalTenantPolicyExprs(ctx, db, dia); err != nil {
		return evidenceStateCalibration{}, fmt.Errorf("sqlstore: core v11 calibration: %w", err)
	}
	cal.ready = true
	return cal, nil
}

// pgCanonicalTenantPolicyExprs returns the USING and WITH CHECK expressions the
// server deparses for the dialect's own tenant_isolation policy, and the
// collation identity it assigns to every generated journal column. Both come
// from a TEMP probe built from the dialect's own table and policy statements,
// in a transaction that is always rolled back.
func pgCanonicalTenantPolicyExprs(ctx context.Context, db dialect.Execer, dia dialect.Dialect) (string, string, map[string]string, error) {
	const probe = "evidence_policy_calibration"
	probeDesc := evidenceOpDescriptor
	probeDesc.Table = probe
	stmts := dia.CreateTableStmts(probeDesc)
	var policy string
	for _, stmt := range stmts {
		if strings.HasPrefix(stmt, "CREATE POLICY ") {
			policy = stmt
		}
	}
	if policy == "" || !strings.HasPrefix(stmts[0], "CREATE TABLE "+probe+" ") {
		return "", "", nil, errors.New("tenant policy calibration: the dialect did not generate the expected table and policy")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", nil, fmt.Errorf("tenant policy calibration: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE "+strings.TrimPrefix(stmts[0], "CREATE TABLE ")); err != nil {
		return "", "", nil, fmt.Errorf("tenant policy calibration: create probe: %w", err)
	}
	if _, err := tx.ExecContext(ctx, policy); err != nil {
		return "", "", nil, fmt.Errorf("tenant policy calibration: create policy: %w", err)
	}
	var using, check string
	if err := tx.QueryRowContext(ctx,
		`SELECT pg_catalog.pg_get_expr(p.polqual, p.polrelid), pg_catalog.pg_get_expr(p.polwithcheck, p.polrelid)
		 FROM pg_catalog.pg_policy p WHERE p.polrelid = 'pg_temp.`+probe+`'::regclass`).Scan(&using, &check); err != nil {
		return "", "", nil, fmt.Errorf("tenant policy calibration: read probe: %w", err)
	}
	// Built only from compile-time constants: the fixed probe name and the fixed
	// pg_attribute collation identity expression.
	rows, err := tx.QueryContext(ctx, `SELECT a.attname, `+pgAttributeCollationIdentitySQL+`
		 FROM pg_catalog.pg_attribute a
		 WHERE a.attrelid = 'pg_temp.`+probe+`'::regclass AND a.attnum > 0 AND NOT a.attisdropped`)
	if err != nil {
		return "", "", nil, fmt.Errorf("tenant policy calibration: read column collations: %w", err)
	}
	collations := map[string]string{}
	for rows.Next() {
		var name, coll string
		if err := rows.Scan(&name, &coll); err != nil {
			rows.Close()
			return "", "", nil, fmt.Errorf("tenant policy calibration: read column collations: %w", err)
		}
		collations[name] = coll
	}
	if err := closeEvidenceInventoryRows(rows); err != nil {
		return "", "", nil, err
	}
	if len(collations) != len(evidenceWitnessColumns()) {
		return "", "", nil, fmt.Errorf("tenant policy calibration: probe has %d columns, want %d", len(collations), len(evidenceWitnessColumns()))
	}
	return using, check, collations, nil
}

// The collation identity expression is fixed catalog SQL around one OID expression.
// Its parts are constants so a query whose OID expression is also fixed can be built
// entirely at compile time.
const (
	pgCollationIdentityPrefix = `COALESCE((SELECT cn.nspname || '.' || co.collname FROM pg_catalog.pg_collation co
		JOIN pg_catalog.pg_namespace cn ON cn.oid = co.collnamespace WHERE co.oid = `
	pgCollationIdentitySuffix = `), '')`
	// pgAttributeCollationIdentitySQL is pgCollationIdentitySQL("a.attcollation") as a
	// compile-time constant: byte-identical text, with no runtime string assembly.
	pgAttributeCollationIdentitySQL = pgCollationIdentityPrefix + "a.attcollation" + pgCollationIdentitySuffix
)

// pgCollationIdentitySQL renders the qualified collation name for one collation
// OID expression, or ” for 0 (a non-collatable type).
func pgCollationIdentitySQL(oidExpr string) string {
	return pgCollationIdentityPrefix + oidExpr + pgCollationIdentitySuffix
}

// expectedKeyCollations returns the collation identities the generator's bare
// index keys inherit from their calibrated columns, in key order.
func expectedKeyCollations(cal evidenceStateCalibration, columns string) string {
	keys := strings.Split(columns, ",")
	out := make([]string, len(keys))
	for i, key := range keys {
		coll, ok := cal.columnCollations[key]
		if !ok {
			return "\x00unknown-column:" + key
		}
		out[i] = coll
	}
	return strings.Join(out, ",")
}

func coreEvidenceRefusedMigration(dia dialect.Dialect, cal evidenceStateCalibration) migrate.Migration {
	return migrate.Migration{
		Version: coreEvidenceRefusedMigrationVersion,
		Name:    coreEvidenceRefusedMigrationName,
		Exec: func(ctx context.Context, tx *sql.Tx) error {
			tracked, err := coreVersionIsTracked(ctx, tx, dia, coreUserAuthorityMigrationVersion)
			if err != nil {
				return err
			}
			if !tracked {
				return fmt.Errorf("%w: core v%d must be recorded before v%d opens its transaction",
					ErrGuardManifestNoEdge, coreUserAuthorityMigrationVersion, coreEvidenceRefusedMigrationVersion)
			}
			_, err = applyEvidenceRefusedTransition(ctx, tx, dia, cal, nil)
			return err
		},
	}
}

// evidenceRefusedOutcome reports what one transition observed and did.
type evidenceRefusedOutcome struct {
	Input  string // S5, S6, S7, P5, P6-fresh, P6-widened, P7-state_check, P7-state_vocab
	Action string // "migrated" or "verified"
	Rows   int64  // witnessed rows (migrated only)
}

// evidenceCatalogQuerier is satisfied by *sql.Tx and the migration-lock Execer.
type evidenceCatalogQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// applyEvidenceRefusedTransition classifies the journal and performs the selected
// action inside tx. fault is nil in production; tests use it to fail at a named
// step and prove the whole transaction rolls back.
func applyEvidenceRefusedTransition(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	cal evidenceStateCalibration,
	fault func(step string) error,
) (evidenceRefusedOutcome, error) {
	if fault == nil {
		fault = func(string) error { return nil }
	}
	if dia.Name() == store.EnginePostgres {
		return applyPostgresEvidenceRefused(ctx, tx, dia, cal, fault)
	}
	return applySQLiteEvidenceRefused(ctx, tx, dia, fault)
}

// verifyEvidenceRefusedPerBoot is the non-healing per-boot check once v11 is
// recorded. It runs before any reconciler can touch the relation.
func verifyEvidenceRefusedPerBoot(ctx context.Context, q evidenceCatalogQuerier, dia dialect.Dialect, cal evidenceStateCalibration) error {
	var (
		input string
		err   error
	)
	if dia.Name() == store.EnginePostgres {
		var inv pgEvidenceInventory
		inv, err = readPostgresEvidenceInventory(ctx, q, dia, cal)
		if err == nil {
			input, err = inv.classify(cal)
		}
	} else {
		input, err = classifySQLiteEvidence(ctx, q, dia)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEvidenceRefusedTrackedStale, err)
	}
	if input != "S7" && input != "P7-state_check" && input != "P7-state_vocab" {
		return fmt.Errorf("%w: found %s", ErrEvidenceRefusedTrackedStale, input)
	}
	return nil
}

func inventoryRefusal(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrEvidenceRefusedTransitionInventory}, args...)...)
}

// evidenceDescriptorWith returns the journal descriptor with one exact vocabulary
// and, optionally, another table name.
func evidenceDescriptorWith(words []model.EvidenceOperationState, table string) model.EntityDescriptor {
	d := evidenceOpDescriptor
	d.Checks = []string{evidenceOpStateCheckExpr(words)}
	if table != "" {
		d.Table = table
	}
	return d
}

// evidenceWitnessColumns is the fixed column order the row witness streams.
func evidenceWitnessColumns() []string {
	cols := []string{model.ColID, model.ColTenantID, model.ColCreatedAt, model.ColUpdatedAt, model.ColVersion}
	for _, f := range evidenceOpDescriptor.Fields {
		cols = append(cols, f.Name)
	}
	return cols
}

// evidenceRowWitness streams every row of table in canonical tenant/id order into
// SHA-256 with a fixed-field, length-prefixed encoding in which NULL (0x00) and
// the empty string (0x01, length 0) differ. It holds one row at a time.
func evidenceRowWitness(ctx context.Context, q evidenceCatalogQuerier, dia dialect.Dialect, table string) (int64, [32]byte, error) {
	cols := evidenceWitnessColumns()
	order := "tenant_id, id"
	if dia.Name() == store.EnginePostgres {
		order = `tenant_id COLLATE "C", id COLLATE "C"`
	}
	rows, err := q.QueryContext(ctx, "SELECT "+strings.Join(cols, ", ")+" FROM "+table+" ORDER BY "+order) // #nosec G202 -- internal constants
	if err != nil {
		return 0, [32]byte{}, fmt.Errorf("core v11 witness %s: %w", table, err)
	}
	defer rows.Close()
	h := sha256.New()
	h.Write([]byte("olivares.sqlstore.evidence-operations-v11-witness.v1\x00"))
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	var n int64
	var lenBuf [4]byte
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return 0, [32]byte{}, fmt.Errorf("core v11 witness %s: %w", table, err)
		}
		for _, v := range vals {
			if !v.Valid {
				h.Write([]byte{0x00})
				continue
			}
			binary.BigEndian.PutUint32(lenBuf[:], uint32(len(v.String))) // #nosec G115 -- journal fields are bounded refs/digests
			h.Write([]byte{0x01})
			h.Write(lenBuf[:])
			h.Write([]byte(v.String))
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, [32]byte{}, fmt.Errorf("core v11 witness %s: %w", table, err)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return n, sum, nil
}

// ---------------------------------------------------------------------------
// SQLite

func applySQLiteEvidenceRefused(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, fault func(string) error) (evidenceRefusedOutcome, error) {
	input, err := classifySQLiteEvidence(ctx, tx, dia)
	if err != nil {
		return evidenceRefusedOutcome{}, err
	}
	if input == "S7" {
		return evidenceRefusedOutcome{Input: input, Action: "verified"}, nil
	}
	table := evidenceOpDescriptor.Table
	beforeRows, beforeSum, err := evidenceRowWitness(ctx, tx, dia, table)
	if err != nil {
		return evidenceRefusedOutcome{}, err
	}
	rebuilt := dia.CreateTableStmts(evidenceDescriptorWith(evidenceOpStateWords7, evidenceRefusedRebuildTable))
	if _, err := tx.ExecContext(ctx, rebuilt[0]); err != nil {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 create %s: %w", evidenceRefusedRebuildTable, err)
	}
	cols := strings.Join(evidenceWitnessColumns(), ", ")
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+evidenceRefusedRebuildTable+" ("+cols+") SELECT "+cols+" FROM "+table); err != nil { // #nosec G202 -- internal constants
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 copy journal rows: %w", err)
	}
	copiedRows, copiedSum, err := evidenceRowWitness(ctx, tx, dia, evidenceRefusedRebuildTable)
	if err != nil {
		return evidenceRefusedOutcome{}, err
	}
	if copiedRows != beforeRows || copiedSum != beforeSum {
		return evidenceRefusedOutcome{}, fmt.Errorf("%w: copy rows=%d want %d", ErrEvidenceRefusedWitness, copiedRows, beforeRows)
	}
	if err := fault("sqlite.after_copy"); err != nil {
		return evidenceRefusedOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE "+table); err != nil {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 drop the verified predecessor: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE "+evidenceRefusedRebuildTable+" RENAME TO "+table); err != nil {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 rename the rebuilt journal: %w", err)
	}
	if err := fault("sqlite.after_rename"); err != nil {
		return evidenceRefusedOutcome{}, err
	}
	for _, stmt := range dia.CreateTableStmts(evidenceDescriptorWith(evidenceOpStateWords7, ""))[1:] {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return evidenceRefusedOutcome{}, fmt.Errorf("core v11 recreate journal object: %w", err)
		}
	}
	after, err := classifySQLiteEvidence(ctx, tx, dia)
	if err != nil {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 postcondition: %w", err)
	}
	if after != "S7" {
		return evidenceRefusedOutcome{}, inventoryRefusal("postcondition classified %s, want S7", after)
	}
	afterRows, afterSum, err := evidenceRowWitness(ctx, tx, dia, table)
	if err != nil {
		return evidenceRefusedOutcome{}, err
	}
	if afterRows != beforeRows || afterSum != beforeSum {
		return evidenceRefusedOutcome{}, fmt.Errorf("%w: rows=%d want %d", ErrEvidenceRefusedWitness, afterRows, beforeRows)
	}
	var fkRows int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_foreign_key_check('"+table+"')").Scan(&fkRows); err != nil {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 foreign key check: %w", err)
	}
	if fkRows != 0 {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 foreign key check reported %d rows", fkRows)
	}
	var quick string
	if err := tx.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quick); err != nil {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 quick_check: %w", err)
	}
	if quick != "ok" {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 quick_check reported %q", quick)
	}
	return evidenceRefusedOutcome{Input: input, Action: "migrated", Rows: afterRows}, nil
}

type sqliteSchemaObject struct {
	typ, name, sql string
	hasSQL         bool
}

// classifySQLiteEvidence returns S5, S6 or S7, or an inventory refusal.
func classifySQLiteEvidence(ctx context.Context, q evidenceCatalogQuerier, dia dialect.Dialect) (string, error) {
	table := evidenceOpDescriptor.Table
	rows, err := q.QueryContext(ctx,
		`SELECT type, name, sql FROM sqlite_master WHERE tbl_name = ? OR name = ? OR tbl_name = ? ORDER BY type, name`,
		table, evidenceRefusedRebuildTable, evidenceRefusedRebuildTable)
	if err != nil {
		return "", fmt.Errorf("core v11 inventory: %w", err)
	}
	var objects []sqliteSchemaObject
	for rows.Next() {
		var o sqliteSchemaObject
		var text sql.NullString
		if err := rows.Scan(&o.typ, &o.name, &text); err != nil {
			rows.Close()
			return "", fmt.Errorf("core v11 inventory: %w", err)
		}
		o.sql, o.hasSQL = text.String, text.Valid
		objects = append(objects, o)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("core v11 inventory: %w", err)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("core v11 inventory: %w", err)
	}

	candidates := []struct {
		input string
		words []model.EvidenceOperationState
	}{{"S5", evidenceOpStateWords5}, {"S6", evidenceOpStateWords6}, {"S7", evidenceOpStateWords7}}
	var tableObj *sqliteSchemaObject
	byName := map[string]sqliteSchemaObject{}
	for i := range objects {
		o := objects[i]
		if _, dup := byName[o.name]; dup {
			return "", inventoryRefusal("duplicate object %s", o.name)
		}
		byName[o.name] = o
		if o.typ == "table" && o.name == table {
			tableObj = &objects[i]
		}
	}
	if tableObj == nil || !tableObj.hasSQL {
		return "", inventoryRefusal("relation %s is absent", table)
	}
	tableTokens, err := tokenizeSQLiteDDL(tableObj.sql)
	if err != nil {
		return "", inventoryRefusal("table definition: %v", err)
	}
	input := ""
	var generated []string
	for _, c := range candidates {
		stmts := dia.CreateTableStmts(evidenceDescriptorWith(c.words, ""))
		want, terr := tokenizeSQLiteDDL(stmts[0])
		if terr != nil {
			return "", fmt.Errorf("core v11 tokenize generated table: %w", terr)
		}
		if sqlTokensEqual(tableTokens, want) {
			input, generated = c.input, stmts
			break
		}
	}
	if input == "" {
		return "", inventoryRefusal("table definition is not the generated five-, six- or seven-word journal")
	}

	// Every other object must be exactly one generated index or trigger, plus the
	// primary-key autoindex, and nothing else.
	expected := map[string][]sqlToken{}
	for _, stmt := range generated[1:] {
		tokens, terr := tokenizeSQLiteDDL(stmt)
		if terr != nil {
			return "", fmt.Errorf("core v11 tokenize generated object: %w", terr)
		}
		name := sqliteDDLObjectName(tokens)
		if name == "" {
			return "", fmt.Errorf("core v11 generated object without a name: %q", stmt)
		}
		expected[name] = tokens
	}
	autoindex := "sqlite_autoindex_" + table + "_1"
	for _, o := range objects {
		switch {
		case o.name == table:
			continue
		case o.name == autoindex && o.typ == "index" && !o.hasSQL:
			continue
		}
		want, ok := expected[o.name]
		if !ok || !o.hasSQL || (o.typ != "index" && o.typ != "trigger") {
			return "", inventoryRefusal("unexpected object %s %s", o.typ, o.name)
		}
		got, terr := tokenizeSQLiteDDL(o.sql)
		if terr != nil {
			return "", inventoryRefusal("object %s: %v", o.name, terr)
		}
		if !sqlTokensEqual(got, want) {
			return "", inventoryRefusal("object %s differs from its generated definition", o.name)
		}
		delete(expected, o.name)
	}
	if _, ok := byName[autoindex]; !ok {
		return "", inventoryRefusal("primary-key index %s is absent", autoindex)
	}
	for name := range expected {
		return "", inventoryRefusal("generated object %s is absent", name)
	}
	if err := verifySQLiteEvidenceColumns(ctx, q, dia); err != nil {
		return "", err
	}
	if err := verifySQLiteEvidenceIndexes(ctx, q); err != nil {
		return "", err
	}
	return input, nil
}

func verifySQLiteEvidenceColumns(ctx context.Context, q evidenceCatalogQuerier, dia dialect.Dialect) error {
	type col struct {
		name, typ string
		notNull   bool
		pk        int
	}
	want := []col{
		{model.ColID, "TEXT", false, 1}, {model.ColTenantID, "TEXT", true, 0},
		{model.ColCreatedAt, "TEXT", true, 0}, {model.ColUpdatedAt, "TEXT", true, 0},
		{model.ColVersion, "INTEGER", true, 0},
	}
	for _, f := range evidenceOpDescriptor.Fields {
		want = append(want, col{f.Name, strings.TrimSuffix(dia.ColumnType(f.Kind, true), " NOT NULL"), !f.Nullable, 0})
	}
	rows, err := q.QueryContext(ctx,
		`SELECT name, type, "notnull", dflt_value IS NULL, pk, hidden FROM pragma_table_xinfo('`+evidenceOpDescriptor.Table+`') ORDER BY cid`)
	if err != nil {
		return fmt.Errorf("core v11 column witness: %w", err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var c col
		var notNull, hidden int
		var noDefault bool
		if err := rows.Scan(&c.name, &c.typ, &notNull, &noDefault, &c.pk, &hidden); err != nil {
			return fmt.Errorf("core v11 column witness: %w", err)
		}
		c.notNull = notNull != 0
		if i >= len(want) || c != want[i] || !noDefault || hidden != 0 {
			return inventoryRefusal("column %d (%s) differs from the descriptor", i, c.name)
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("core v11 column witness: %w", err)
	}
	if i != len(want) {
		return inventoryRefusal("column count %d, want %d", i, len(want))
	}
	return nil
}

// verifySQLiteEvidenceIndexes checks index uniqueness, origin, partiality and
// key columns as catalog facts beside the exact DDL comparison.
func verifySQLiteEvidenceIndexes(ctx context.Context, q evidenceCatalogQuerier) error {
	table := evidenceOpDescriptor.Table
	type ix struct {
		unique  bool
		origin  string
		columns string
	}
	want := map[string]ix{
		"sqlite_autoindex_" + table + "_1": {true, "pk", model.ColID},
		table + "_tenant_id_idx":           {false, "c", model.ColTenantID + "," + model.ColID},
	}
	for _, f := range evidenceOpDescriptor.Fields {
		if f.Indexed {
			want[table+"_"+f.Name+"_idx"] = ix{false, "c", model.ColTenantID + "," + f.Name}
		}
	}
	for _, spec := range evidenceOpDescriptor.Indexes {
		want[spec.Name] = ix{spec.Unique, "c", strings.Join(spec.Columns, ",")}
	}
	rows, err := q.QueryContext(ctx,
		`SELECT l.name, l."unique", l.origin, l.partial,
		        (SELECT group_concat(name, ',') FROM (SELECT i.name FROM pragma_index_info(l.name) i ORDER BY i.seqno))
		 FROM pragma_index_list('`+table+`') l ORDER BY l.name`)
	if err != nil {
		return fmt.Errorf("core v11 index witness: %w", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name, origin string
		var unique, partial int
		var columns sql.NullString
		if err := rows.Scan(&name, &unique, &origin, &partial, &columns); err != nil {
			return fmt.Errorf("core v11 index witness: %w", err)
		}
		w, ok := want[name]
		if !ok || w.unique != (unique != 0) || w.origin != origin || partial != 0 || w.columns != columns.String {
			return inventoryRefusal("index %s catalog facts differ", name)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("core v11 index witness: %w", err)
	}
	if seen != len(want) {
		return inventoryRefusal("index count %d, want %d", seen, len(want))
	}
	return nil
}

// sqlToken is one lexical token of a SQLite DDL statement.
type sqlToken struct {
	kind byte // 'i' identifier or keyword, 's' string literal, 'n' number, 'p' punctuation
	text string
}

// tokenizeSQLiteDDL is a deliberately small lexer for the generator's DDL. It
// folds identifier and keyword case and treats a double-quoted identifier as its
// bare spelling; it preserves string literal contents, numbers, operators,
// parentheses and order exactly. Comments and any character outside this
// grammar are refused rather than erased into equality.
func tokenizeSQLiteDDL(s string) ([]sqlToken, error) {
	var out []sqlToken
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < len(s) && s[i+1] == '-', c == '/' && i+1 < len(s) && s[i+1] == '*':
			return nil, errors.New("comments are not part of a generated definition")
		case isSQLIdentStart(c):
			j := i + 1
			for j < len(s) && isSQLIdentPart(s[j]) {
				j++
			}
			out = append(out, sqlToken{'i', strings.ToLower(s[i:j])})
			i = j
		case c == '"':
			var b strings.Builder
			j := i + 1
			for {
				if j >= len(s) {
					return nil, errors.New("unterminated quoted identifier")
				}
				if s[j] == '"' {
					if j+1 < len(s) && s[j+1] == '"' {
						b.WriteByte('"')
						j += 2
						continue
					}
					break
				}
				b.WriteByte(s[j])
				j++
			}
			name := b.String()
			if name == "" || !isSQLIdentStart(name[0]) {
				return nil, errors.New("quoted identifier is not a plain identifier")
			}
			for k := 1; k < len(name); k++ {
				if !isSQLIdentPart(name[k]) {
					return nil, errors.New("quoted identifier is not a plain identifier")
				}
			}
			out = append(out, sqlToken{'i', strings.ToLower(name)})
			i = j + 1
		case c == '\'':
			j := i + 1
			for {
				if j >= len(s) {
					return nil, errors.New("unterminated string literal")
				}
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			out = append(out, sqlToken{'s', s[i : j+1]})
			i = j + 1
		case c >= '0' && c <= '9':
			j := i + 1
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			out = append(out, sqlToken{'n', s[i:j]})
			i = j
		default:
			if i+1 < len(s) {
				switch s[i : i+2] {
				case "<>", "<=", ">=", "!=", "==", "||":
					out = append(out, sqlToken{'p', s[i : i+2]})
					i += 2
					continue
				}
			}
			if strings.IndexByte("(),;=<>+*/.-", c) < 0 {
				return nil, fmt.Errorf("unsupported character %q", c)
			}
			out = append(out, sqlToken{'p', string(c)})
			i++
		}
	}
	return out, nil
}

func isSQLIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isSQLIdentPart(c byte) bool {
	return isSQLIdentStart(c) || (c >= '0' && c <= '9')
}

func sqlTokensEqual(a, b []sqlToken) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sqliteDDLObjectName returns the object name of CREATE [UNIQUE] INDEX name or
// CREATE TRIGGER name.
func sqliteDDLObjectName(tokens []sqlToken) string {
	if len(tokens) < 3 || tokens[0].text != "create" {
		return ""
	}
	i := 1
	if tokens[i].text == "unique" {
		i++
	}
	if i+1 >= len(tokens) || (tokens[i].text != "index" && tokens[i].text != "trigger") || tokens[i+1].kind != 'i' {
		return ""
	}
	return tokens[i+1].text
}

// ---------------------------------------------------------------------------
// PostgreSQL

type pgEvidenceConstraint struct {
	name, typ, def, columns string
	validated               bool
}

// pgEvidenceInventory is every catalog fact v11 compares. constraints is kept
// apart from the rest because the migrate action changes exactly that set.
type pgEvidenceInventory struct {
	oid         int64
	owned       bool
	owner       string
	facts       string // columns, indexes, policies, triggers, RLS, rules, children, ACL
	constraints []pgEvidenceConstraint
}

func readPostgresEvidenceInventory(ctx context.Context, q evidenceCatalogQuerier, dia dialect.Dialect, cal evidenceStateCalibration) (pgEvidenceInventory, error) {
	if !cal.ready {
		return pgEvidenceInventory{}, errors.New("core v11: PostgreSQL calibration is unavailable")
	}
	table := evidenceOpDescriptor.Table
	var inv pgEvidenceInventory
	var relRLS, relForce, relRules, relSubclass bool
	var relACL string
	err := q.QueryRowContext(ctx,
		`SELECT c.oid::bigint, pg_catalog.pg_get_userbyid(c.relowner) = CURRENT_USER, pg_catalog.pg_get_userbyid(c.relowner),
		        c.relrowsecurity, c.relforcerowsecurity, c.relhasrules, c.relhassubclass, COALESCE(c.relacl::text, '')
		 FROM pg_catalog.pg_class c
		 WHERE c.relname = $1 AND c.relkind = 'r' AND pg_catalog.pg_table_is_visible(c.oid)`, table).
		Scan(&inv.oid, &inv.owned, &inv.owner, &relRLS, &relForce, &relRules, &relSubclass, &relACL)
	if errors.Is(err, sql.ErrNoRows) {
		return pgEvidenceInventory{}, inventoryRefusal("relation %s is absent", table)
	}
	if err != nil {
		return pgEvidenceInventory{}, fmt.Errorf("core v11 inventory relation: %w", err)
	}
	var facts strings.Builder
	fmt.Fprintf(&facts, "owner=%s rls=%t force=%t rules=%t children=%t acl=%s\n", inv.owner, relRLS, relForce, relRules, relSubclass, relACL)

	// Columns.
	type pgCol struct {
		name, typ         string
		notNull, def      bool
		identity, generat string
	}
	wantCols := []pgCol{
		{model.ColID, "text", true, false, "", ""}, {model.ColTenantID, "text", true, false, "", ""},
		{model.ColCreatedAt, "text", true, false, "", ""}, {model.ColUpdatedAt, "text", true, false, "", ""},
		{model.ColVersion, "bigint", true, false, "", ""},
	}
	for _, f := range evidenceOpDescriptor.Fields {
		wantCols = append(wantCols, pgCol{f.Name, strings.ToLower(strings.TrimSuffix(dia.ColumnType(f.Kind, true), " NOT NULL")), !f.Nullable, false, "", ""})
	}
	rows, err := q.QueryContext(ctx,
		`SELECT a.attname, pg_catalog.format_type(a.atttypid, a.atttypmod), a.attnotnull, a.atthasdef,
		        a.attidentity::text, a.attgenerated::text, `+pgCollationIdentitySQL("a.attcollation")+`
		 FROM pg_catalog.pg_attribute a WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, inv.oid)
	if err != nil {
		return pgEvidenceInventory{}, fmt.Errorf("core v11 inventory columns: %w", err)
	}
	i := 0
	for rows.Next() {
		var c pgCol
		var coll string
		if err := rows.Scan(&c.name, &c.typ, &c.notNull, &c.def, &c.identity, &c.generat, &coll); err != nil {
			rows.Close()
			return pgEvidenceInventory{}, fmt.Errorf("core v11 inventory columns: %w", err)
		}
		if want, ok := cal.columnCollations[c.name]; !ok || coll != want {
			rows.Close()
			return pgEvidenceInventory{}, inventoryRefusal("column %s collation %q differs from the generated %q", c.name, coll, want)
		}
		if i >= len(wantCols) || c != wantCols[i] {
			rows.Close()
			return pgEvidenceInventory{}, inventoryRefusal("column %d (%s) differs from the descriptor", i, c.name)
		}
		i++
	}
	if err := closeEvidenceInventoryRows(rows); err != nil {
		return pgEvidenceInventory{}, err
	}
	if i != len(wantCols) {
		return pgEvidenceInventory{}, inventoryRefusal("column count %d, want %d", i, len(wantCols))
	}

	// Constraints: every one on the relation.
	rows, err = q.QueryContext(ctx,
		`SELECT c.conname, c.contype::text, c.convalidated, pg_catalog.pg_get_constraintdef(c.oid),
		        COALESCE((SELECT pg_catalog.string_agg(a.attname, ',' ORDER BY k.ord)
		                  FROM pg_catalog.unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		                  JOIN pg_catalog.pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum), '')
		 FROM pg_catalog.pg_constraint c WHERE c.conrelid = $1 ORDER BY c.conname`, inv.oid)
	if err != nil {
		return pgEvidenceInventory{}, fmt.Errorf("core v11 inventory constraints: %w", err)
	}
	for rows.Next() {
		var c pgEvidenceConstraint
		if err := rows.Scan(&c.name, &c.typ, &c.validated, &c.def, &c.columns); err != nil {
			rows.Close()
			return pgEvidenceInventory{}, fmt.Errorf("core v11 inventory constraints: %w", err)
		}
		inv.constraints = append(inv.constraints, c)
	}
	if err := closeEvidenceInventoryRows(rows); err != nil {
		return pgEvidenceInventory{}, err
	}

	// Indexes: uniqueness, primary, validity, access method, keys, expressions,
	// predicates, sort options and default operator classes.
	rows, err = q.QueryContext(ctx,
		`SELECT i.relname, x.indisunique, x.indisprimary, x.indisvalid, x.indisready, am.amname,
		        x.indexprs IS NULL, x.indpred IS NULL, x.indnkeyatts, x.indnatts, x.indoption::text,
		        COALESCE((SELECT pg_catalog.string_agg(a.attname, ',' ORDER BY k.ord)
		                  FROM pg_catalog.unnest(x.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
		                  JOIN pg_catalog.pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = k.attnum), ''),
		        NOT EXISTS (SELECT 1 FROM pg_catalog.unnest(x.indclass::oid[]) AS oc(o)
		                    JOIN pg_catalog.pg_opclass op ON op.oid = oc.o WHERE NOT op.opcdefault),
		        COALESCE((SELECT pg_catalog.string_agg(`+pgCollationIdentitySQL("kc.coll")+`, ',' ORDER BY kc.ord)
		                  FROM pg_catalog.unnest(x.indcollation::oid[]) WITH ORDINALITY AS kc(coll, ord)), '')
		 FROM pg_catalog.pg_index x
		 JOIN pg_catalog.pg_class i ON i.oid = x.indexrelid
		 JOIN pg_catalog.pg_am am ON am.oid = i.relam
		 WHERE x.indrelid = $1 ORDER BY i.relname`, inv.oid)
	if err != nil {
		return pgEvidenceInventory{}, fmt.Errorf("core v11 inventory indexes: %w", err)
	}
	type pgIndex struct {
		unique, primary bool
		columns         string
	}
	wantIdx := map[string]pgIndex{
		table + "_pkey":          {true, true, model.ColID},
		table + "_tenant_id_idx": {false, false, model.ColTenantID + "," + model.ColID},
	}
	for _, f := range evidenceOpDescriptor.Fields {
		if f.Indexed {
			wantIdx[table+"_"+f.Name+"_idx"] = pgIndex{false, false, model.ColTenantID + "," + f.Name}
		}
	}
	for _, spec := range evidenceOpDescriptor.Indexes {
		wantIdx[spec.Name] = pgIndex{spec.Unique, false, strings.Join(spec.Columns, ",")}
	}
	seen := 0
	for rows.Next() {
		var name, am, option, columns, collations string
		var unique, primary, valid, ready, noExprs, noPred, defaultOpclass bool
		var nKey, nAtts int
		if err := rows.Scan(&name, &unique, &primary, &valid, &ready, &am, &noExprs, &noPred, &nKey, &nAtts, &option, &columns, &defaultOpclass, &collations); err != nil {
			rows.Close()
			return pgEvidenceInventory{}, fmt.Errorf("core v11 inventory indexes: %w", err)
		}
		w, ok := wantIdx[name]
		keys := strings.Count(w.columns, ",") + 1
		if !ok || w.unique != unique || w.primary != primary || !valid || !ready || am != "btree" ||
			!noExprs || !noPred || nKey != keys || nAtts != keys || option != strings.TrimSpace(strings.Repeat("0 ", keys)) ||
			columns != w.columns || !defaultOpclass || collations != expectedKeyCollations(cal, w.columns) {
			rows.Close()
			return pgEvidenceInventory{}, inventoryRefusal("index %s catalog facts differ", name)
		}
		fmt.Fprintf(&facts, "index=%s\n", name)
		seen++
	}
	if err := closeEvidenceInventoryRows(rows); err != nil {
		return pgEvidenceInventory{}, err
	}
	if seen != len(wantIdx) {
		return pgEvidenceInventory{}, inventoryRefusal("index count %d, want %d", seen, len(wantIdx))
	}

	// Policies: exactly the dialect's tenant_isolation.
	rows, err = q.QueryContext(ctx,
		`SELECT p.polname, p.polcmd::text, p.polpermissive, p.polroles::text,
		        COALESCE(pg_catalog.pg_get_expr(p.polqual, p.polrelid), ''),
		        COALESCE(pg_catalog.pg_get_expr(p.polwithcheck, p.polrelid), '')
		 FROM pg_catalog.pg_policy p WHERE p.polrelid = $1 ORDER BY p.polname`, inv.oid)
	if err != nil {
		return pgEvidenceInventory{}, fmt.Errorf("core v11 inventory policies: %w", err)
	}
	policies := 0
	for rows.Next() {
		var name, cmd, roles, using, check string
		var permissive bool
		if err := rows.Scan(&name, &cmd, &permissive, &roles, &using, &check); err != nil {
			rows.Close()
			return pgEvidenceInventory{}, fmt.Errorf("core v11 inventory policies: %w", err)
		}
		if name != "tenant_isolation" || cmd != "*" || !permissive || roles != "{0}" ||
			using != cal.policyUsing || check != cal.policyCheck {
			rows.Close()
			return pgEvidenceInventory{}, inventoryRefusal("policy %s differs from the generated tenant policy", name)
		}
		policies++
	}
	if err := closeEvidenceInventoryRows(rows); err != nil {
		return pgEvidenceInventory{}, err
	}
	if policies != 1 || !relRLS || !relForce {
		return pgEvidenceInventory{}, inventoryRefusal("row-level security is not exactly ENABLE+FORCE with tenant_isolation")
	}

	var triggers int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_catalog.pg_trigger WHERE tgrelid = $1`, inv.oid).Scan(&triggers); err != nil {
		return pgEvidenceInventory{}, fmt.Errorf("core v11 inventory triggers: %w", err)
	}
	if triggers != 0 || relRules || relSubclass {
		return pgEvidenceInventory{}, inventoryRefusal("relation carries triggers, rules or children the descriptor does not declare")
	}
	inv.facts = facts.String()
	return inv, nil
}

func closeEvidenceInventoryRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("core v11 inventory: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("core v11 inventory: %w", err)
	}
	return nil
}

// classify returns P5, P6-fresh, P6-widened, P7-state_check or P7-state_vocab.
func (inv pgEvidenceInventory) classify(cal evidenceStateCalibration) (string, error) {
	table := evidenceOpDescriptor.Table
	var state *pgEvidenceConstraint
	pkeys := 0
	notNull := map[string]bool{}
	for i := range inv.constraints {
		c := inv.constraints[i]
		switch {
		case c.typ == "p" && c.name == table+"_pkey" && c.columns == model.ColID && c.validated:
			pkeys++
		case c.typ == "c" && c.columns == "state":
			if state != nil {
				return "", inventoryRefusal("more than one state CHECK")
			}
			state = &inv.constraints[i]
		case c.typ == "n" && c.name == table+"_"+c.columns+"_not_null" && c.validated:
			// PostgreSQL 18 catalogs NOT NULL as constraints; only the exact
			// generated NOT NULL columns are accepted (not exercised on PG16).
			notNull[c.columns] = true
		default:
			return "", inventoryRefusal("unexpected constraint %s (%s)", c.name, c.typ)
		}
	}
	if pkeys != 1 {
		return "", inventoryRefusal("primary key is not exactly %s_pkey (id)", table)
	}
	if len(notNull) != 0 {
		want := map[string]bool{model.ColID: true, model.ColTenantID: true, model.ColCreatedAt: true, model.ColUpdatedAt: true, model.ColVersion: true}
		for _, f := range evidenceOpDescriptor.Fields {
			if !f.Nullable {
				want[f.Name] = true
			}
		}
		if len(want) != len(notNull) {
			return "", inventoryRefusal("NOT NULL constraints differ from the descriptor")
		}
		for name := range want {
			if !notNull[name] {
				return "", inventoryRefusal("NOT NULL constraints differ from the descriptor")
			}
		}
	}
	if state == nil || !state.validated {
		return "", inventoryRefusal("no validated state CHECK")
	}
	switch {
	case state.name == evidenceOpStateCheckFreshName && state.def == cal.check5:
		return "P5", nil
	case state.name == evidenceOpStateCheckFreshName && state.def == cal.check6:
		return "P6-fresh", nil
	case state.name == evidenceOpStateVocabConstraint && state.def == cal.check6:
		return "P6-widened", nil
	case state.name == evidenceOpStateCheckFreshName && state.def == cal.check7:
		return "P7-state_check", nil
	case state.name == evidenceOpStateVocabConstraint && state.def == cal.check7:
		return "P7-state_vocab", nil
	}
	return "", inventoryRefusal("state CHECK %s is not a supported name and vocabulary pair", state.name)
}

func applyPostgresEvidenceRefused(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	cal evidenceStateCalibration,
	fault func(string) error,
) (evidenceRefusedOutcome, error) {
	before, err := readPostgresEvidenceInventory(ctx, tx, dia, cal)
	if err != nil {
		return evidenceRefusedOutcome{}, err
	}
	input, err := before.classify(cal)
	if err != nil {
		return evidenceRefusedOutcome{}, err
	}
	if input == "P7-state_check" || input == "P7-state_vocab" {
		return evidenceRefusedOutcome{Input: input, Action: "verified"}, nil
	}
	if !before.owned {
		return evidenceRefusedOutcome{}, ErrEvidenceRefusedNotOwner
	}
	var stateName string
	for _, c := range before.constraints {
		if c.typ == "c" {
			stateName = c.name
		}
	}
	table := evidenceOpDescriptor.Table
	// FORCE row-level security applies to the owner, whose session carries no
	// tenant binding here. The force flag is lifted only inside this transaction
	// so the witness can read every tenant's rows, and restored before the
	// postcondition compares it.
	if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" NO FORCE ROW LEVEL SECURITY"); err != nil {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 witness access: %w", err)
	}
	beforeRows, beforeSum, err := evidenceRowWitness(ctx, tx, dia, table)
	if err != nil {
		return evidenceRefusedOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" DROP CONSTRAINT "+quoteIdent(stateName)); err != nil {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 drop state CHECK: %w", err)
	}
	if err := fault("postgres.after_drop_constraint"); err != nil {
		return evidenceRefusedOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD CONSTRAINT "+evidenceOpStateVocabConstraint+
		" CHECK ("+evidenceOpStateCheckExpr(evidenceOpStateWords7)+")"); err != nil {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 add seven-word state CHECK: %w", err)
	}
	afterRows, afterSum, err := evidenceRowWitness(ctx, tx, dia, table)
	if err != nil {
		return evidenceRefusedOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" FORCE ROW LEVEL SECURITY"); err != nil {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 restore row-level security: %w", err)
	}
	if afterRows != beforeRows || afterSum != beforeSum {
		return evidenceRefusedOutcome{}, fmt.Errorf("%w: rows=%d want %d", ErrEvidenceRefusedWitness, afterRows, beforeRows)
	}
	after, err := readPostgresEvidenceInventory(ctx, tx, dia, cal)
	if err != nil {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 postcondition: %w", err)
	}
	afterInput, err := after.classify(cal)
	if err != nil {
		return evidenceRefusedOutcome{}, fmt.Errorf("core v11 postcondition: %w", err)
	}
	if afterInput != "P7-state_vocab" || after.oid != before.oid || after.facts != before.facts {
		return evidenceRefusedOutcome{}, inventoryRefusal("postcondition %s does not preserve the relation", afterInput)
	}
	return evidenceRefusedOutcome{Input: input, Action: "migrated", Rows: afterRows}, nil
}
