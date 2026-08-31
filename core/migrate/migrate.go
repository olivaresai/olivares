// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// nowText is the apply timestamp recorded in the tracking table. It is
// infrastructure bookkeeping (never hashed into the audit chain), so the wall
// clock is read directly here.
func nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Phase classifies a migration in the expand-contract (parallel-change) model
// that makes a schema-change upgrade ONLINE — no maintenance window (OPS-3).
type Phase int

const (
	// Expand is an ADDITIVE, online-safe change: a new NULLABLE column, a new
	// table, a new index, a backfill. It is the zero value, so every migration
	// authored before (all additive) is an expand by default. Because old and
	// new code both work against an expanded schema, an expand ships and rolls out
	// with no downtime and no coordination.
	Expand Phase = iota
	// Contract is the DESTRUCTIVE cleanup that finishes a parallel-change: drop a
	// column, SET NOT NULL, drop a table, rename. It is online-safe ONLY when it
	// ships in a LATER release than the expand it completes — by which time no
	// running node still depends on the thing it removes. The CI online-safety
	// linter (scripts/check-migrations.sh) enforces this separation; the cluster-wide
	// migration advisory lock (sqlstore) ensures it never races across nodes.
	Contract
)

// String renders the phase as the text stored in the tracking table.
func (p Phase) String() string {
	if p == Contract {
		return "contract"
	}
	return "expand"
}

// Migration is one ordered, atomically-applied schema change: a set of SQL
// statements identified by a monotonically increasing version.
type Migration struct {
	// Version orders migrations within a tracking table; must be unique and > 0.
	Version int
	// Name is a short human label recorded alongside the version.
	Name string
	// Stmts are the statements applied, in order, inside one transaction (unless
	// NonTransactional).
	Stmts []string
	// Phase is Expand (default) or Contract. Recorded for observability and checked
	// by the CI online-safety linter; it does not change WHEN the migration applies
	// (the release cadence does — a contract ships a release after its expand).
	Phase Phase
	// DownStmts optionally reverses this migration, run by Revert in the order given.
	// Leave empty for a forward-only migration — the honest default for anything that
	// touches the RLS/audit guards (you cannot un-FORCE row-level security without a
	// tenant-leak window) or drops data. Reversibility is opt-in and only for safely
	// reversible expands (drop the added column/index).
	DownStmts []string
	// NonTransactional runs Stmts OUTSIDE a transaction, for DDL that Postgres
	// forbids inside one — chiefly CREATE INDEX CONCURRENTLY, the way to add an index
	// to a populated table WITHOUT holding a write lock (the online-index build). The
	// tracking row is still recorded atomically afterward. A failure mid-way can
	// leave partial state (an INVALID index) the operator drops and retries — standard
	// online-index practice, and the cost of not blocking writes.
	NonTransactional bool
	// Before runs BEFORE Stmts, on the SAME transaction as Stmts, Exec, After and
	// the tracking row. It is for fail-closed preconditions that must be observed
	// before a schema object is replaced while holding the locks that keep that
	// observation stable until commit.
	//
	// Like Exec and After, it is refused on a NonTransactional migration: there is
	// no transaction in that mode in which the observation and schema change can
	// be made atomic.
	Before func(ctx context.Context, tx *sql.Tx) error
	// Exec runs AFTER Stmts and BEFORE the tracking row, on the SAME transaction.
	//
	// It exists for the one shape Stmts cannot express: a migration whose rows carry
	// values that must not be rendered into a SQL string. The guard control plane's
	// bootstrap is exactly that — chained SHA-256 digests, an epoch, ordered unit
	// identities and a server timestamp. Rendering those as literals would mean either a
	// hand-rolled escaper on the hot path of schema creation, or hashes computed in SQL
	// where the Go side cannot verify them.
	//
	// Atomicity is the reason it lives HERE rather than after Apply returns. The tables,
	// their guards, the first rollout, the bootstrap receipts and the tracking row must
	// commit together or not at all: a crash between "the table exists" and "the gate has
	// an opening event" would leave a database whose tracking says the bootstrap happened
	// and whose gate has no history, which the preflight is obliged to refuse — a
	// deployment bricked by a window that need not exist.
	//
	// It is refused on a NonTransactional migration, because there the promise cannot be
	// kept: the statements run outside any transaction, so there is no transaction for
	// Exec to share and nothing to roll back with it.
	Exec func(ctx context.Context, tx *sql.Tx) error
	// After runs AFTER Stmts and Exec, but BEFORE the tracking row, on the SAME
	// transaction. It verifies postconditions before a migration can become
	// durable or be recorded as applied. Returning an error rolls back the schema
	// statements, every callback write and the absent tracking row together.
	//
	// It is refused on a NonTransactional migration for the same reason as Before
	// and Exec.
	After func(ctx context.Context, tx *sql.Tx) error
}

// Apply runs every migration in migs that is not yet recorded in trackingTable.
// Migrations are applied in ascending Version order, each (by default) in its own
// transaction together with its tracking-row insert, so a partially applied
// migration never leaves the tracking table inconsistent. Apply is idempotent:
// already-applied versions are skipped. Expand and contract migrations both apply
// here — the expand-contract guarantee comes from authoring the contract in a
// later release than its expand (the CI linter enforces the additive-only rule for
// expands), not from a separate runtime path.
func Apply(ctx context.Context, db dialect.Execer, dia dialect.Dialect, trackingTable string, migs []Migration) error {
	// Validate transaction-shape promises and every transactional SQL boundary
	// before ensureTracking performs even its idempotent DDL. A callback cannot be
	// made atomic in NonTransactional mode, and an already-recorded version must not
	// turn an invalid plan into one that appears valid merely because applyOne would
	// be skipped.
	for _, m := range migs {
		if err := validateMigrationPlan(m, dia.Name()); err != nil {
			return err
		}
	}
	if err := ensureTracking(ctx, db, dia, trackingTable); err != nil {
		return err
	}
	applied, err := appliedVersions(ctx, db, dia, trackingTable)
	if err != nil {
		return err
	}

	ordered := append([]Migration(nil), migs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Version < ordered[j].Version })

	for _, m := range ordered {
		if m.Version <= 0 {
			return fmt.Errorf("migrate: %s has non-positive version %d", m.Name, m.Version)
		}
		if applied[m.Version] {
			continue
		}
		if err := applyOne(ctx, db, dia, trackingTable, m); err != nil {
			return fmt.Errorf("migrate %s (v%d): %w", m.Name, m.Version, err)
		}
	}
	return nil
}

// Revert runs a migration's DownStmts and marks it reverted in the tracking table
// (it does NOT delete the row, so the history is preserved). It is the rollback
// path for a reversible expand during an incident; it refuses a forward-only
// migration (one with no DownStmts) loudly rather than pretending to reverse it.
// Revert is transactional: the down statements and the reverted_at stamp commit
// together.
func Revert(ctx context.Context, db dialect.Execer, dia dialect.Dialect, trackingTable string, m Migration) error {
	if len(m.DownStmts) == 0 {
		return fmt.Errorf("migrate: %s (v%d) is forward-only (no DownStmts); reverse it with a new contract migration, not Revert", m.Name, m.Version)
	}
	if err := validateTransactionalStatements(
		m.Name, m.Version, "down statement", m.DownStmts, dia.Name(),
	); err != nil {
		return err
	}
	if err := ensureTracking(ctx, db, dia, trackingTable); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	for i, stmt := range m.DownStmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate revert %s (v%d) down statement %d: %w", m.Name, m.Version, i+1, err)
		}
	}
	upd := dia.Rebind(fmt.Sprintf(
		"UPDATE %s SET reverted_at = ? WHERE version = ?",
		trackingTableRef(dia, trackingTable),
	))
	result, err := tx.ExecContext(ctx, upd, nowText(), m.Version)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("migrate: confirm reverted tracking row: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf(
			"migrate: %s (v%d) reverted tracking rows = %d, want exactly 1",
			m.Name, m.Version, affected,
		)
	}
	return tx.Commit()
}

// ensureTracking creates the version-tracking table if absent and additively
// reconciles the phase/reverted_at columns onto a table created before them,
// so an upgrade never needs a destructive change to the bookkeeping itself. Its
// DDL is portable across both engines.
func ensureTracking(ctx context.Context, db dialect.Execer, dia dialect.Dialect, table string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: begin tracking reconciliation: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	qualified := trackingTableRef(dia, table)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL, phase TEXT NOT NULL DEFAULT 'expand', reverted_at TEXT)",
		qualified)); err != nil {
		return err
	}
	cols, err := dia.TableColumns(ctx, tx, table)
	if err != nil {
		return err
	}
	if !cols["phase"] {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN phase TEXT NOT NULL DEFAULT 'expand'", qualified)); err != nil {
			return fmt.Errorf("migrate: add phase column: %w", err)
		}
	}
	if !cols["reverted_at"] {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN reverted_at TEXT", qualified)); err != nil {
			return fmt.Errorf("migrate: add reverted_at column: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate: commit tracking reconciliation: %w", err)
	}
	return nil
}

// appliedVersions returns the set of versions already recorded (regardless of
// revert state: a reverted version keeps its row and is not re-applied — reverse a
// schema with a new migration, never by silently re-running an old one).
func appliedVersions(ctx context.Context, db dialect.Execer, dia dialect.Dialect, table string) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT version FROM "+trackingTableRef(dia, table)) // #nosec G202 -- safely quoted internal migrations-table identity
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		set[v] = true
	}
	return set, rows.Err()
}

// applyOne runs one migration's statements and records it. A transactional
// migration runs the statements and the tracking insert in a single transaction;
// a NonTransactional one runs the statements outside any transaction (for CREATE
// INDEX CONCURRENTLY and the like) and then records the tracking row atomically.
func applyOne(ctx context.Context, db dialect.Execer, dia dialect.Dialect, table string, m Migration) error {
	if err := validateMigrationPlan(m, dia.Name()); err != nil {
		return err
	}
	if m.NonTransactional {
		return applyOneNonTx(ctx, db, dia, table, m)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if m.Before != nil {
		if err := m.Before(ctx, tx); err != nil {
			return fmt.Errorf("before: %w", err)
		}
	}
	for i, stmt := range m.Stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %d: %w", i+1, err)
		}
	}
	// After the statements, before the tracking row, inside the same transaction: the
	// rows Exec writes are only meaningful against the objects the statements just
	// created, and the tracking row must not become durable without them.
	if m.Exec != nil {
		if err := m.Exec(ctx, tx); err != nil {
			return fmt.Errorf("bootstrap: %w", err)
		}
	}
	if m.After != nil {
		if err := m.After(ctx, tx); err != nil {
			return fmt.Errorf("after: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, trackingInsert(dia, table), m.Version, m.Name, nowText(), m.Phase.String()); err != nil {
		return err
	}
	return tx.Commit()
}

func validateMigrationPlan(m Migration, engine store.Engine) error {
	if err := validateTransactionShape(m); err != nil {
		return err
	}
	if m.NonTransactional {
		return nil
	}
	return validateTransactionalStatements(
		m.Name, m.Version, "statement", m.Stmts, engine,
	)
}

func validateTransactionShape(m Migration) error {
	if !m.NonTransactional || (m.Before == nil && m.Exec == nil && m.After == nil) {
		return nil
	}
	// Refused rather than run outside a transaction. Exec's entire contract is
	// that it commits with the statements and the tracking row; Before and After
	// make the same promise for their observations. Honoring any callback here
	// would keep the call and silently drop the guarantee, which is worse than not
	// offering it.
	return fmt.Errorf(
		"migrate: %s (v%d) is non-transactional and also declares a transactional callback (Before, Exec or After); the two cannot both hold",
		m.Name, m.Version,
	)
}

// validateTransactionalStatements protects Apply's and Revert's advertised outer
// transaction boundary. PostgreSQL's simple-query protocol and SQLite both accept
// multiple statements in one ExecContext call, including COMMIT. If a migration
// string closes the transaction, later failure can neither roll back the durable
// schema change nor make an absent tracking row meaningful.
//
// Every transactional statement list is lexed before ensureTracking performs any
// write. The lexer recognizes statement boundaries while ignoring comments,
// quoted data, PostgreSQL dollar bodies and SQLite CREATE TRIGGER bodies; it never
// searches raw text for keywords. NonTransactional migrations are deliberately
// outside this contract and validated separately for callback incompatibility.
func validateTransactionalStatements(
	name string,
	version int,
	label string,
	statements []string,
	engine store.Engine,
) error {
	for i, stmt := range statements {
		control, err := callbackSQLTransactionControl(stmt, engine)
		if err != nil {
			return fmt.Errorf(
				"migrate: %s (v%d) %s %d cannot be safely framed as one outer transaction: %w",
				name, version, label, i+1, err,
			)
		}
		if control != "" {
			return fmt.Errorf(
				"migrate: %s (v%d) %s %d contains transaction control %s; migrate owns the transaction boundary",
				name, version, label, i+1, control,
			)
		}
	}
	return nil
}

type callbackSQLToken struct {
	word      string
	semicolon bool
}

type callbackSQLLexer struct {
	source string
	engine store.Engine
	offset int
}

func callbackSQLTransactionControl(statement string, engine store.Engine) (string, error) {
	lexer := callbackSQLLexer{source: statement, engine: engine}
	atStatementStart := true
	sqliteCreateTrigger := false
	sqliteTriggerBody := false
	sqliteTriggerEnd := false
	sqliteCaseDepth := 0
	prefix := make([]string, 0, 3)

	resetStatement := func() {
		atStatementStart = true
		sqliteCreateTrigger = false
		sqliteTriggerBody = false
		sqliteTriggerEnd = false
		sqliteCaseDepth = 0
		prefix = prefix[:0]
	}

	for {
		token, ok, err := lexer.next()
		if err != nil {
			return "", err
		}
		if !ok {
			return "", nil
		}
		if token.semicolon {
			if !sqliteTriggerBody || sqliteTriggerEnd {
				resetStatement()
			}
			continue
		}

		if atStatementStart {
			if isTransactionControlCommand(token.word) {
				return token.word, nil
			}
			atStatementStart = false
		}
		if engine != store.EngineSQLite {
			continue
		}

		if !sqliteTriggerBody {
			if len(prefix) < 3 {
				prefix = append(prefix, token.word)
			}
			sqliteCreateTrigger = sqliteCreateTriggerPrefix(prefix)
			if sqliteCreateTrigger && token.word == "BEGIN" {
				sqliteTriggerBody = true
			}
			continue
		}

		switch token.word {
		case "CASE":
			sqliteCaseDepth++
		case "END":
			if sqliteCaseDepth > 0 {
				sqliteCaseDepth--
			} else {
				sqliteTriggerEnd = true
			}
		}
	}
}

func isTransactionControlCommand(word string) bool {
	switch word {
	case "ABORT", "BEGIN", "COMMIT", "END", "PREPARE", "RELEASE", "ROLLBACK", "SAVEPOINT", "START":
		return true
	default:
		return false
	}
}

func sqliteCreateTriggerPrefix(prefix []string) bool {
	if len(prefix) < 2 || prefix[0] != "CREATE" {
		return false
	}
	if prefix[1] == "TRIGGER" {
		return true
	}
	return len(prefix) >= 3 && (prefix[1] == "TEMP" || prefix[1] == "TEMPORARY") &&
		prefix[2] == "TRIGGER"
}

func (l *callbackSQLLexer) next() (callbackSQLToken, bool, error) {
	for l.offset < len(l.source) {
		start := l.offset
		switch {
		case isSQLSpace(l.source[l.offset]):
			l.offset++
		case strings.HasPrefix(l.source[l.offset:], "--"):
			l.offset += 2
			for l.offset < len(l.source) && l.source[l.offset] != '\n' && l.source[l.offset] != '\r' {
				l.offset++
			}
		case strings.HasPrefix(l.source[l.offset:], "/*"):
			if err := l.skipBlockComment(); err != nil {
				return callbackSQLToken{}, false, err
			}
		case l.source[l.offset] == '\'':
			if err := l.skipSingleQuoted(); err != nil {
				return callbackSQLToken{}, false, err
			}
		case l.source[l.offset] == '"':
			if err := l.skipDoubleQuoted(); err != nil {
				return callbackSQLToken{}, false, err
			}
		case l.engine == store.EnginePostgres && l.source[l.offset] == '$':
			if delimiter, ok := postgresDollarDelimiter(l.source[l.offset:]); ok {
				l.offset += len(delimiter)
				end := strings.Index(l.source[l.offset:], delimiter)
				if end < 0 {
					return callbackSQLToken{}, false, fmt.Errorf("unterminated PostgreSQL dollar-quoted body at byte %d", start)
				}
				l.offset += end + len(delimiter)
			} else {
				l.offset++
			}
		case l.source[l.offset] == ';':
			l.offset++
			return callbackSQLToken{semicolon: true}, true, nil
		case isSQLWordStart(l.source[l.offset]):
			l.offset++
			for l.offset < len(l.source) && isSQLWordPart(l.source[l.offset]) {
				l.offset++
			}
			return callbackSQLToken{
				word: strings.ToUpper(l.source[start:l.offset]),
			}, true, nil
		default:
			l.offset++
		}
	}
	return callbackSQLToken{}, false, nil
}

func (l *callbackSQLLexer) skipBlockComment() error {
	start := l.offset
	l.offset += 2
	if l.engine != store.EnginePostgres {
		end := strings.Index(l.source[l.offset:], "*/")
		if end < 0 {
			return fmt.Errorf("unterminated SQL block comment at byte %d", start)
		}
		l.offset += end + 2
		return nil
	}
	depth := 1
	for l.offset < len(l.source) {
		switch {
		case strings.HasPrefix(l.source[l.offset:], "/*"):
			depth++
			l.offset += 2
		case strings.HasPrefix(l.source[l.offset:], "*/"):
			depth--
			l.offset += 2
			if depth == 0 {
				return nil
			}
		default:
			l.offset++
		}
	}
	return fmt.Errorf("unterminated SQL block comment at byte %d", start)
}

func (l *callbackSQLLexer) skipSingleQuoted() error {
	start := l.offset
	escapeString := l.postgresEscapeString(start)
	l.offset++
	for l.offset < len(l.source) {
		if l.source[l.offset] == '\\' && l.engine == store.EnginePostgres {
			if escapeString {
				if l.offset+1 >= len(l.source) {
					return fmt.Errorf("unterminated PostgreSQL escape string at byte %d", start)
				}
				l.offset += 2
				continue
			}
			return fmt.Errorf(
				"backslash-bearing PostgreSQL single-quoted literal at byte %d is ambiguous across standard_conforming_strings modes; use E or dollar quoting",
				start,
			)
		}
		if l.source[l.offset] != '\'' {
			l.offset++
			continue
		}
		if l.offset+1 < len(l.source) && l.source[l.offset+1] == '\'' {
			l.offset += 2
			continue
		}
		l.offset++
		return nil
	}
	return fmt.Errorf("unterminated SQL single-quoted literal at byte %d", start)
}

func (l *callbackSQLLexer) postgresEscapeString(quoteOffset int) bool {
	if l.engine != store.EnginePostgres || quoteOffset == 0 {
		return false
	}
	prefix := l.source[quoteOffset-1]
	if prefix != 'E' && prefix != 'e' {
		return false
	}
	return quoteOffset == 1 || !isSQLWordPart(l.source[quoteOffset-2])
}

func (l *callbackSQLLexer) skipDoubleQuoted() error {
	start := l.offset
	l.offset++
	for l.offset < len(l.source) {
		if l.source[l.offset] != '"' {
			l.offset++
			continue
		}
		if l.offset+1 < len(l.source) && l.source[l.offset+1] == '"' {
			l.offset += 2
			continue
		}
		l.offset++
		return nil
	}
	return fmt.Errorf("unterminated SQL quoted identifier at byte %d", start)
}

func postgresDollarDelimiter(source string) (string, bool) {
	if len(source) < 2 || source[0] != '$' {
		return "", false
	}
	if source[1] == '$' {
		return "$$", true
	}
	if !isSQLWordStart(source[1]) {
		return "", false
	}
	for i := 2; i < len(source); i++ {
		if source[i] == '$' {
			return source[:i+1], true
		}
		if !isSQLWordPart(source[i]) {
			return "", false
		}
	}
	return "", false
}

func isSQLSpace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func isSQLWordStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isSQLWordPart(value byte) bool {
	return isSQLWordStart(value) || value >= '0' && value <= '9' || value == '$'
}

// applyOneNonTx runs a migration's statements OUTSIDE a transaction (required for
// CREATE INDEX CONCURRENTLY, which errors inside a transaction block), then records
// the tracking row in its own small transaction. If a statement fails, the
// tracking row is NOT written, so a re-run retries from the failed statement —
// which for a CONCURRENTLY index means the operator first drops the leftover
// INVALID index (documented online-index practice).
func applyOneNonTx(ctx context.Context, db dialect.Execer, dia dialect.Dialect, table string, m Migration) error {
	for i, stmt := range m.Stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %d (non-transactional): %w", i+1, err)
		}
	}
	if _, err := db.ExecContext(ctx, trackingInsert(dia, table), m.Version, m.Name, nowText(), m.Phase.String()); err != nil {
		return err
	}
	return nil
}

// trackingInsert is the portable INSERT that records an applied migration with its
// phase.
func trackingInsert(dia dialect.Dialect, table string) string {
	return dia.Rebind(fmt.Sprintf(
		"INSERT INTO %s(version, name, applied_at, phase) VALUES(?, ?, ?, ?)",
		trackingTableRef(dia, table),
	))
}

// trackingTableRef binds migration bookkeeping to the engine's durable schema.
// Both PostgreSQL and SQLite search temporary schemas before ordinary unqualified
// relations; without this qualification a migration can commit its schema change
// while the apparent ledger row lands in a session-local shadow table.
func trackingTableRef(dia dialect.Dialect, table string) string {
	schema := "main"
	if dia.Name() == store.EnginePostgres {
		schema = dialect.EngineSchema
	}
	return quoteSQLIdentifier(schema) + "." + quoteSQLIdentifier(table)
}

func quoteSQLIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
