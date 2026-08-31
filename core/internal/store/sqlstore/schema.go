// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Tracking objects. Core migrations use an ordered version table; module entity
// tables are tracked by NAME (not by registration position), so adding,
// removing or reordering a module never re-maps a version to a different table.
const (
	coreTrackingTable    = "schema_migrations_core"
	moduleTablesTracking = "applied_module_tables"
)

// buildCoreMigrations assembles the engine's own schema from the dialect and the
// core descriptors: (1) tenancy prerequisites, (2) the entity tables generated
// from descriptors — so a core table can never drift from its descriptor —
// and (3) the append-only hash-chained audit ledger.
func buildCoreMigrations(
	dia dialect.Dialect,
	descs []model.EntityDescriptor,
	guardBootstrap func(context.Context, *sql.Tx) error,
	directoryAfter directoryMigrationAfter,
) []migrate.Migration {
	entity := make([]string, 0, len(descs)*4)
	for _, d := range descs {
		entity = append(entity, dia.CreateTableStmts(d)...)
	}
	migrations := []migrate.Migration{
		{Version: 1, Name: "tenancy", Stmts: dia.TenancyStmts()},
		{Version: 2, Name: "core_entities", Stmts: entity},
		{Version: 3, Name: "audit_chain", Stmts: dia.AuditTableStmts()},
		// v4 (U4): relax the federation_configs scope-unique index to per-(scope,
		// alias) so more than one IdP can coexist under a TargetTenantID. This DROP of the
		// old 2-column UNIQUE is the ONE destructive step the strictly-additive reconcile
		// cannot perform; the NEW 3-column index and the alias column are added by
		// reconcile (CREATE UNIQUE INDEX IF NOT EXISTS / ALTER ADD COLUMN), and the legacy
		// NULL→"default" backfill runs in reconcileCoreData right after. FORWARD-ONLY: once
		// >1 IdP per scope exists the old uniqueness cannot be truthfully restored, so there
		// is deliberately no DownStmts (migrate.Revert refuses a forward-only migration
		// loudly rather than pretend to reverse it). IDEMPOTENT: DROP INDEX IF EXISTS is a
		// no-op on a fresh DB (the old index name is gone from the descriptor so it is never
		// created) and on any re-apply; unqualified index names resolve the same way as the
		// reconcile CREATE INDEX statements on both engines.
		{Version: 4, Name: "federation_multi_idp", Stmts: []string{
			"DROP INDEX IF EXISTS federation_configs_scope_uniq",
		}},
		{Version: 5, Name: "audit_spool", Stmts: dia.AuditSpoolStmts()},
		// v6 (C4): the guard control plane — the three append-only logs the append-only
		// rollout keeps its own history in, their immutability guards in ALWAYS, and the ACL
		// posture that denies the application role even INSERT on them.
		//
		// EXPAND and FORWARD-ONLY, with no DownStmts, and that is the honest default rather
		// than an omission: reversing it would mean dropping an evidence log, and
		// migrate.Revert refuses a forward-only migration loudly instead of pretending.
		//
		// Exec carries the ROWS — the inventory activations and the bootstrap receipts —
		// because they hold chained digests that must not be rendered into a SQL string, and
		// because they must commit with the relations and the tracking row or not at all. A
		// crash between "the tables exist" and "the ledger has a history" would leave a
		// database the preflight is obliged to refuse.
		//
		// It does NOT open the rollout. At this point in Open the module tables these units
		// target do not exist yet (applyModuleTables runs later), so the expected unit set is
		// not derivable here; the coordinator opens it at the insertion point. See
		// guardunits.go.
		{
			Version: guardControlPlaneVersion,
			Name:    guardControlPlaneName,
			Stmts:   dia.GuardControlPlaneStmts(),
			Exec:    guardBootstrap,
		},
	}
	// Core v7 is an Exec rather than a static statement list. Fresh v2 already
	// emitted these current descriptors, while an upgrade has v2 recorded and
	// needs all three relations created atomically. The composable continuation
	// seam in coreDirectoryMigration is where the guard 1 -> 2 transition and
	// writer-control half attach before this migration is published.
	return append(migrations, coreDirectoryMigration(dia, descs, directoryAfter))
}

// reconcileCoreData runs idempotent, one-time DATA normalizations for core tables
// whose descriptor changed a column from an implicit to an explicit value — the
// data-shape analog of reconcileColumns (which reconciles the SCHEMA). It runs
// AFTER reconcileColumns (so the new columns exist) and under the same migration
// advisory lock. Every statement MUST be idempotent (a no-op once applied and on a
// fresh/empty table) because it runs on EVERY boot, not version-tracked — that is
// what lets it converge an upgraded database onto the shape a fresh one is created
// with, without a hand-authored, order-fragile migration.
//
// It runs BOUND to the auth partition, in ONE transaction. federation_configs is
// an auth-partition table: it is reachable only through
// store.AuthScope.FederationConfigs() (core/store/auth.go), and AuthView/AuthMutate
// pin model.SystemTenantID as an ordinary RLS scope (authscope.go) — the per-IdP
// scope is the target_tenant_id COLUMN, not the row's tenant. Every SUPPORTED write
// therefore stamps tenant_id = SystemTenantID, so binding it reaches exactly the
// rows this backfill is for. (It cannot reach a row some out-of-band path stamped
// with a foreign tenant; this function does not attempt to detect that.)
//
// Without the bind these statements ran on a raw pool with no tenant set, and on
// Postgres the FORCE-RLS policy calls pg_catalog.current_setting('app.tenant_id') WITHOUT
// missing_ok — deliberately, so a forgotten bind RAISES instead of silently
// matching zero rows (dialect/postgres.go). It duly raised, on every boot, for
// every Postgres deployment: `unrecognized configuration parameter
// "app.tenant_id"` (SQLSTATE 42704), and the store never opened. Binding is the
// fix that keeps the guard intact; binding the EMPTY tenant would also stop the
// crash, by matching zero rows — trading a loud failure for a silent one, since an
// un-backfilled NULL alias is DISTINCT in the (tenant_id, target_tenant_id, alias)
// unique index and escapes the very constraint it is meant to satisfy.
//
// The pin is deliberately NOT cleared before COMMIT. On Postgres it is
// transaction-local (set_config with is_local=true) and evaporates on its own. On
// SQLite it is a ROW that outlives the commit, and an EMPTY pin table is the
// PERMISSIVE state — the tripwire triggers only fire WHERE EXISTS(pin)
// (dialect/sqlite.go), which is how the System path deliberately opts out. Leaving
// the auth-partition pin means a later unbound raw write to another tenant's row
// aborts loudly instead of being silently allowed; the next scoped operation
// overwrites the pin, and System() clears it explicitly when it wants the
// cross-tenant exception (store.go).
func reconcileCoreData(ctx context.Context, db dialect.Execer, dia dialect.Dialect) error {
	// U4: the federation_configs.alias column is added nullable by reconcile, so every
	// pre-U4 row carries alias NULL. Converge them to the reserved "default" so the
	// (tenant_id, target_tenant_id, alias) UNIQUE index enforces one default IdP per scope
	// IDENTICALLY on an upgraded and a fresh database — a NULL alias is DISTINCT in a unique
	// index on both engines, so an un-backfilled NULL would silently escape the constraint.
	// The codec already reads NULL/empty as "default", so this converges STORAGE, not
	// behavior.
	//
	// The backfill is written so it can NEVER violate the unique index (and so brick boot),
	// even in the pathological mixed-version window where v4 has dropped the old scope-unique
	// index but pre-U4 replicas (whose codec never emits an alias) could still write a second
	// NULL row for one scope:
	//   (1) promote the lowest-id NULL/empty row of each scope to "default", but ONLY where
	//       the scope has no "default" row yet (so a concurrently-written new-codec 'default'
	//       is never duplicated);
	//   (2) any remaining NULL/empty rows are duplicates of an existing default — give each a
	//       unique alias ("dup-<id>") so nothing collides and no row is lost.
	//       KNOWN LIMITATION (pre-existing, not fixed here): an id is a 36-char UUIDv7
	//       (model/ids.go), so "dup-<id>" is 40 chars while validateFederationAlias caps an
	//       alias at 31 (core/auth/federation_config.go). The per-IdP admin routes are keyed
	//       by {alias} and 400 a malformed one BEFORE any store round-trip
	//       (core/api/handlers_sso_config.go), so such a row can be LISTED but not edited or
	//       deleted from the console. Shortening the alias is not a safe drive-by: a truncated
	//       id can collide, and a collision here violates the unique index and bricks boot —
	//       the exact failure this two-step shape exists to avoid. Tracked as a residual.
	// The common single-row-per-scope upgrade runs (1) once and (2) as a no-op; a second boot
	// updates zero rows. Portable across SQLite and Postgres (correlated subquery + ||).
	stmts := []string{
		`UPDATE federation_configs SET alias = 'default'
		   WHERE (alias IS NULL OR alias = '')
		     AND id = (SELECT MIN(f2.id) FROM federation_configs f2
		                WHERE f2.tenant_id = federation_configs.tenant_id
		                  AND f2.target_tenant_id = federation_configs.target_tenant_id
		                  AND (f2.alias IS NULL OR f2.alias = ''))
		     AND NOT EXISTS (SELECT 1 FROM federation_configs f3
		                WHERE f3.tenant_id = federation_configs.tenant_id
		                  AND f3.target_tenant_id = federation_configs.target_tenant_id
		                  AND f3.alias = 'default')`,
		`UPDATE federation_configs SET alias = 'dup-' || id WHERE alias IS NULL OR alias = ''`,
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if err := dia.BindTenant(ctx, tx, model.SystemTenantID); err != nil {
		return fmt.Errorf("bind auth partition: %w", err)
	}
	// One transaction, so the promote step and the deduplicate step cannot be
	// separated by a crash: they previously ran as two autocommit statements, which
	// left rows half-converged until the next boot.
	for i, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %d: %w", i+1, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// reconcileAuditLedger adds the ledger columns the audit table gained after its
// v3 CREATE TABLE. The ledger is raw DDL rather than a descriptor-generated table,
// so reconcileColumns cannot see it — but it needs the same forward path, and for
// the same reason: a database created before a column existed must converge onto
// the shape a fresh one is created with, without a version-tracked migration that
// would then have to be a no-op on every fresh database forever.
//
// ADD COLUMN is the whole of it, and it must stay that way. The table carries
// append-only triggers and a tenant guard; anything beyond adding a nullable column
// belongs in an authored migration where its online-safety can be reviewed.
//
// meta_blind is nullable BY CONTRACT, not for convenience: a row sealed before
// blinding existed has no blind, and the absence is what tells Verify to apply the
// unblinded rule that row was actually sealed under. Backfilling a blind here would
// be the one unforgivable operation on an evidence ledger — it would change the
// metadata commitment of already-sealed rows and break their chain hashes.
func reconcileAuditLedger(ctx context.Context, db dialect.Execer, dia dialect.Dialect) error {
	have, err := dia.TableColumns(ctx, db, dialect.AuditEventsTable)
	if err != nil {
		return fmt.Errorf("introspect %q: %w", dialect.AuditEventsTable, err)
	}
	if len(have) == 0 {
		// The v3 migration creates it before this runs; an empty introspection means
		// the engine reported no such table, which the boot self-test will surface
		// far more usefully than an ALTER against a missing table would.
		return nil
	}
	// Whether the column is being ADDED now is exactly the fact the write rule
	// needs, and this is the only moment it is knowable for free. A ledger that
	// lacks the column has been accumulating rows under the legacy rule, so it must
	// not start blinding on its own; a ledger that already has it was created by a
	// CREATE TABLE that includes it, so it has never held a row under any other
	// rule. Recording the answer here means no later boot has to ask the ledger,
	// which matters because asking it is not free and, on Postgres, not even
	// possible from the application pool: audit_events carries FORCE row-level
	// security whose policy RAISES outside a tenant-bound transaction.
	legacyLedger := !have["meta_blind"]
	// The ALTER and the seed it justifies commit TOGETHER, in one transaction, and
	// that is a correctness requirement rather than tidiness. Run as two autocommit
	// statements — which is how this shipped — a crash between them leaves a ledger
	// that HAS the column and NO state row, and the next boot reads the column as
	// proof that the ledger never held a row under the legacy rule. It then seeds
	// default_blinded=1 for a ledger full of legacy rows: a deployment silently
	// changing the write rule of a live chain, which is the single failure this whole
	// record exists to make impossible. The observation and the row it produces must
	// therefore be inseparable. Both engines run DDL transactionally, so this costs
	// nothing on either.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reconcile %q: begin: %w", dialect.AuditEventsTable, err)
	}
	defer func() { _ = tx.Rollback() }()
	if legacyLedger {
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN meta_blind %s",
			dialect.AuditEventsTable, dia.ColumnType(model.KindBytes, true))
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("reconcile %q add column meta_blind: %w", dialect.AuditEventsTable, err)
		}
	}
	if err := reconcileAuditBlindingState(ctx, tx, dia, legacyLedger); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reconcile %q: commit: %w", dialect.AuditEventsTable, err)
	}
	// Outside the transaction on purpose: the guard is defense in depth, it probes the
	// catalog and may legitimately skip itself on a non-owner role, and none of that
	// belongs in the atomic unit above.
	return reconcileAuditBlindGuards(ctx, db, dia)
}

// reconcileAuditBlindingState creates and seeds the global record of whether this
// ledger has been actuated onto the blinded metadata-commitment rule.
//
// It is a GLOBAL, un-guarded, single-row table on the exact model of
// audit_spool_usage (dialect/postgres.go): bookkeeping about the ledger, not
// tenant data and not evidence, so it carries neither row-level security nor an
// append-only trigger and can be read from the application pool with no tenant
// bound. That property is the point. The alternative — asking audit_events
// whether it holds a blinded row — is a cross-tenant question about an
// RLS-guarded table, which on Postgres does not return zero rows outside a
// tenant-bound transaction: it RAISES, failing every boot in the default mode.
//
// The seed is written ONCE, from the only moment the answer is free: a ledger
// that already carried the blind column has never held a row under another rule
// and starts actuated; one that is gaining the column now has been accumulating
// rows under the legacy rule and must not start blinding without an operator
// saying so. Once seeded the row is never re-derived, so a later boot cannot
// change its mind about a ledger whose history has moved on.
// It takes a Querier rather than an Execer because its caller now runs it inside a
// transaction, and a *sql.Tx cannot begin another one.
func reconcileAuditBlindingState(ctx context.Context, db dialect.Querier, dia dialect.Dialect, legacyLedger bool) error {
	create := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (id INTEGER PRIMARY KEY, default_blinded INTEGER NOT NULL, actuated INTEGER NOT NULL)",
		dialect.AuditBlindingStateTable)
	if _, err := db.ExecContext(ctx, create); err != nil {
		return fmt.Errorf("reconcile %q: %w", dialect.AuditBlindingStateTable, err)
	}
	defaultBlinded := 1
	if legacyLedger {
		defaultBlinded = 0
	}
	// The two facts are deliberately separate. default_blinded is what the DEFAULT
	// mode follows, and it is seeded here. actuated records that a process has
	// actually resolved to writing blinded rows, and it starts at 0 even on a fresh
	// ledger — because a brand-new deployment that deliberately starts with blinding
	// off (a node joining a fleet that still runs older binaries) has not actuated
	// anything, and must be able to start. Conflating the two would make "off"
	// unusable on exactly the installs where it is the legitimate choice.
	//
	// ON CONFLICT DO NOTHING keeps this a seed and never a restatement: the row is
	// the ledger's own history of the decision, and a boot that re-derived it would
	// be exactly the "deploy changes the rule" failure the setting exists to stop.
	seed := dia.Rebind(fmt.Sprintf(
		"INSERT INTO %s (id, default_blinded, actuated) VALUES (1, ?, 0) ON CONFLICT (id) DO NOTHING",
		dialect.AuditBlindingStateTable))
	if _, err := db.ExecContext(ctx, seed, defaultBlinded); err != nil {
		return fmt.Errorf("seed %q: %w", dialect.AuditBlindingStateTable, err)
	}
	return nil
}

// reconcileAuditBlindGuards adds the at-rest length CHECK on meta_blind where the
// engine can add one to a populated table AND this role is entitled to.
//
// The guard is defense in depth, not the guarantee: canon.ValidateBlind refuses an
// illegal blind in flight, and the writer only ever binds nil or BlindLen bytes,
// so the constraint defends against something reaching the column from outside the
// engine. That is worth having and worth being honest about, because it is not
// universal:
//
//   - A FRESHLY CREATED database of either engine has it, in the CREATE TABLE.
//   - An existing POSTGRES ledger gains it here, online (NOT VALID then VALIDATE
//     takes only SHARE UPDATE EXCLUSIVE), but ONLY if this role owns the table.
//   - An existing SQLITE ledger never gains it: SQLite cannot add a CHECK to an
//     existing table, and the only route is a rebuild-and-swap, which on an
//     append-only evidence ledger carrying triggers and a tenant guard is the one
//     operation that must never be automated.
//
// The ownership test is not a nicety. ALTER TABLE requires ownership, and a
// deployment that deliberately runs the application role as a NON-OWNER is a
// supported and privilege-conscious posture; failing its boot over a
// defense-in-depth constraint would punish the more careful operator. Skipping is
// announced rather than silent, so the operator can add it themselves.
func reconcileAuditBlindGuards(ctx context.Context, db dialect.Execer, dia dialect.Dialect) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	// Idempotent by catalog probe: ADD CONSTRAINT has no IF NOT EXISTS.
	const constraint = "audit_events_meta_blind_len"
	var exists bool
	if err := db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = $1)", constraint).Scan(&exists); err != nil {
		return fmt.Errorf("reconcile %q probe %s: %w", dialect.AuditEventsTable, constraint, err)
	}
	if exists {
		return nil
	}
	var owned bool
	if err := db.QueryRowContext(ctx,
		// CURRENT_USER is a reserved keyword, not a schema-qualifiable function, so it
		// is written bare; the search_path is pinned elsewhere and pg_class is
		// qualified, which is what the qualification discipline is actually for.
		"SELECT pg_catalog.pg_get_userbyid(relowner) = CURRENT_USER FROM pg_catalog.pg_class WHERE relname = $1",
		dialect.AuditEventsTable).Scan(&owned); err != nil {
		return fmt.Errorf("reconcile %q probe ownership: %w", dialect.AuditEventsTable, err)
	}
	if !owned {
		slog.Warn("audit: skipping the meta_blind length constraint because this role does not own the ledger table; the engine still refuses an illegal blind in flight (canon.ValidateBlind), but the database will not state the invariant about itself. Add it as the table owner to close that gap",
			"table", dialect.AuditEventsTable, "constraint", constraint)
		return nil
	}
	add := fmt.Sprintf(
		"ALTER TABLE %s ADD CONSTRAINT %s CHECK (meta_blind IS NULL OR octet_length(meta_blind) = 32) NOT VALID",
		dialect.AuditEventsTable, constraint)
	if _, err := db.ExecContext(ctx, add); err != nil {
		return fmt.Errorf("reconcile %q add %s: %w", dialect.AuditEventsTable, constraint, err)
	}
	// Existing rows are all NULL (there is no backfill, ever), so validation is a
	// scan that cannot fail; running it here means the constraint is not left in
	// the weaker NOT VALID state, where it would guard new writes but leave the
	// table unable to state the invariant about itself.
	val := fmt.Sprintf("ALTER TABLE %s VALIDATE CONSTRAINT %s", dialect.AuditEventsTable, constraint)
	if _, err := db.ExecContext(ctx, val); err != nil {
		return fmt.Errorf("reconcile %q validate %s: %w", dialect.AuditEventsTable, constraint, err)
	}
	return nil
}

// evidenceOpStateVocabWords is the CURRENT settlement vocabulary, from the model
// constants so no probe can drift from the Go vocabulary.
func evidenceOpStateVocabWords() []string {
	return []string{
		string(model.EvidenceOpClaimed), string(model.EvidenceOpCompleted),
		string(model.EvidenceOpNotSent), string(model.EvidenceOpUnknown),
		string(model.EvidenceOpBlocked), string(model.EvidenceOpWithheld),
	}
}

// evidenceVocabCheckCurrent reports whether one CHECK definition carries the
// FULL current vocabulary, each word quoted (both engines quote list members, so
// a word matched without quotes would be a column name, not a vocabulary value).
//
// It is a DIAGNOSTIC predicate — the SQLite warn probe and test assertions —
// never the Postgres reconciler's notion of "current": a bag of literals is not
// predicate equivalence (a lax `state <> ” OR state IN (...)` contains every
// word and restricts nothing — round-3, F-4 residual). The reconciler compares
// against the server-deparsed CANONICAL definition (pgCanonicalStateCheckDef).
func evidenceVocabCheckCurrent(def string) bool {
	for _, w := range evidenceOpStateVocabWords() {
		if !strings.Contains(def, "'"+w+"'") {
			return false
		}
	}
	return true
}

// sqliteEvidenceStateVocabStale reports whether a SQLite CREATE TABLE DDL
// carries a STALE state-vocabulary CHECK: the probe locates the `state IN (`
// segment itself — never a bare substring over the whole DDL, where a word like
// 'withheld' appearing in an unrelated constraint or comment would silence the
// diagnostic (stage-7 round 2, F-4) — and requires every current word inside
// that segment. found=false means no state-vocabulary CHECK was located at all.
func sqliteEvidenceStateVocabStale(ddl string) (stale, found bool) {
	i := strings.Index(ddl, "state IN (")
	if i < 0 {
		return false, false
	}
	seg := ddl[i:]
	if j := strings.IndexByte(seg, ')'); j >= 0 {
		seg = seg[:j]
	}
	return !evidenceVocabCheckCurrent(seg), true
}

// evidenceOpStateVocabConstraint names the Postgres CHECK this reconcile owns.
const evidenceOpStateVocabConstraint = "evidence_operations_state_vocab"

// pgCanonicalStateCheckDef returns the definition PostgreSQL itself deparses
// for the descriptor's state-vocabulary CHECK expression: a TEMP probe table
// carrying the exact expression is created inside a transaction that is rolled
// back (no residue, single pinned connection), and its pg_get_constraintdef is
// the calibrated ground truth. Self-calibration — rather than a hardcoded
// deparse format — keeps the equality exact across server versions: whatever
// this server would produce for OUR expression is what a current constraint
// must read back as.
func pgCanonicalStateCheckDef(ctx context.Context, db dialect.Execer, expr string) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("canonical state CHECK calibration: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		"CREATE TEMP TABLE evidence_vocab_calibration (state TEXT NOT NULL, CHECK ("+expr+")) ON COMMIT DROP"); err != nil {
		return "", fmt.Errorf("canonical state CHECK calibration: create probe: %w", err)
	}
	var def string
	if err := tx.QueryRowContext(ctx,
		`SELECT pg_catalog.pg_get_constraintdef(c.oid)
		 FROM pg_catalog.pg_constraint c
		 WHERE c.conrelid = 'pg_temp.evidence_vocab_calibration'::regclass AND c.contype = 'c'`).Scan(&def); err != nil {
		return "", fmt.Errorf("canonical state CHECK calibration: read probe: %w", err)
	}
	return def, nil
}

// reconcileEvidenceOpStateCheck widens the evidence_operations lifecycle CHECK to
// the CURRENT settlement vocabulary on a database created before a word existed
// (stage-7 B-bis added 'withheld'). The CHECK is emitted only at CREATE TABLE, so
// without this an in-place upgrade keeps the old list and every settlement using
// the new word is REFUSED at the database — deny-closed (the response is withheld
// and the operation stays claimed, never re-dispatched) but permanently stuck.
//
// Engine honesty, on the exact model of reconcileAuditBlindGuards:
//
//   - A FRESHLY CREATED database of either engine has the full list in its
//     CREATE TABLE (the descriptor is the single source of truth).
//   - An existing POSTGRES journal gains it here, in ONE transaction — drop the
//     stale vocabulary CHECK(s) and add the named current one, which VALIDATES
//     at ADD time (historical rows are a strict subset of the new vocabulary on
//     any table this engine wrote). Failure-atomic by construction (round 2,
//     F-3): a validation failure — a row outside even the CURRENT vocabulary,
//     i.e. out-of-band corruption — rolls the whole transition back, keeps the
//     previous backstop in place, and FAILS THE BOOT with a named error; it is
//     retried on every boot until the row is repaired. There is no intermediate
//     commit in which the table has no vocabulary CHECK. Ownership-gated; a
//     non-owner skip is announced, not silent.
//   - An existing SQLITE journal never gains it: SQLite cannot ALTER a CHECK,
//     and a rebuild-and-swap of a live journal is the one operation that must
//     never be automated. The skip is announced loudly with its consequence:
//     'withheld' settlements refuse (fail-closed) until the table is rebuilt by
//     hand.
//
// Probe discipline (round 2, F-4): the target relation is resolved to the ONE
// visible pg_class OID — the relation the store's unqualified DML actually hits
// — and every constraint question is asked of that OID via pg_constraint.conrelid
// and conkey (the CHECK constraining exactly the `state` column). A homonymous
// table in another schema, or a CHECK on another column whose text happens to
// contain "state" or "withheld", can neither satisfy nor be selected by the
// probes. "Current" is EXACT EQUALITY with the canonical definition this server
// deparses for the descriptor's own expression (round 3: a same-column CHECK
// whose text merely contains the six words behind a lax predicate is stale and
// replaced, never accepted). A residual NOT VALID constraint (a round-1 upgrade
// interrupted after a failed validation) is never accepted as done: it is
// re-VALIDATEd, and a persisting violation keeps failing the boot by name
// (round 2, F-2).
func reconcileEvidenceOpStateCheck(ctx context.Context, db dialect.Execer, dia dialect.Dialect) error {
	table := evidenceOpDescriptor.Table
	current := evidenceOpDescriptor.Checks[0]
	if dia.Name() != store.EnginePostgres {
		var ddl string
		err := db.QueryRowContext(ctx,
			"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&ddl)
		if err != nil {
			return fmt.Errorf("reconcile %q probe state CHECK: %w", table, err)
		}
		stale, found := sqliteEvidenceStateVocabStale(ddl)
		if !found {
			slog.Warn("evidence journal: no state-vocabulary CHECK found on this SQLite table; the database-level backstop for the settlement vocabulary is absent (decode-time validation still refuses unknown states)",
				"table", table, "check", current)
		} else if stale {
			slog.Warn("evidence journal: this SQLite database predates the 'withheld' settlement state and SQLite cannot widen a CHECK constraint in place. Settlements of fetched-then-withheld responses will REFUSE (deny-closed: the response is withheld anyway and the operation stays claimed) until the evidence_operations table is rebuilt with the current vocabulary",
				"table", table, "check", current)
		}
		return nil
	}
	// Postgres. Resolve the ONE visible target relation and its ownership.
	var relOID int64
	var owned bool
	if err := db.QueryRowContext(ctx,
		`SELECT c.oid::bigint, pg_catalog.pg_get_userbyid(c.relowner) = CURRENT_USER
		 FROM pg_catalog.pg_class c
		 WHERE c.relname = $1 AND c.relkind = 'r' AND pg_catalog.pg_table_is_visible(c.oid)`,
		table).Scan(&relOID, &owned); err != nil {
		return fmt.Errorf("reconcile %q resolve visible relation: %w", table, err)
	}
	var stateAttNum int16
	if err := db.QueryRowContext(ctx,
		`SELECT a.attnum FROM pg_catalog.pg_attribute a
		 WHERE a.attrelid = $1 AND a.attname = 'state' AND NOT a.attisdropped`,
		relOID).Scan(&stateAttNum); err != nil {
		return fmt.Errorf("reconcile %q resolve state column: %w", table, err)
	}
	// The CANONICAL definition this server deparses for the descriptor's
	// expression — the only thing "current" may mean. Bag-of-literals matching
	// is not predicate equivalence (round-3, F-4 residual): a lax CHECK on the
	// same column can contain every quoted word behind an OR escape hatch and
	// restrict nothing.
	canonicalDef, err := pgCanonicalStateCheckDef(ctx, db, current)
	if err != nil {
		return fmt.Errorf("reconcile %q: %w", table, err)
	}
	// Every CHECK constraining exactly the state column, classified. Only
	// VOCABULARY checks (they quote 'claimed', the word every vocabulary CHECK
	// has carried since) participate: an operator's unrelated state CHECK is
	// neither counted current nor dropped. A vocabulary CHECK is CURRENT only on
	// exact equality with the calibrated canonical definition; anything else —
	// old list, lax predicate, foreign rewrite — is stale and replaced.
	rows, err := db.QueryContext(ctx,
		`SELECT c.conname, c.convalidated, pg_catalog.pg_get_constraintdef(c.oid)
		 FROM pg_catalog.pg_constraint c
		 WHERE c.conrelid = $1 AND c.contype = 'c' AND c.conkey = ARRAY[$2::int2]`,
		relOID, stateAttNum)
	if err != nil {
		return fmt.Errorf("reconcile %q list state CHECKs: %w", table, err)
	}
	var stale []string
	currentName, currentValidated := "", false
	for rows.Next() {
		var name, def string
		var validated bool
		if err := rows.Scan(&name, &validated, &def); err != nil {
			rows.Close()
			return fmt.Errorf("reconcile %q scan state CHECK: %w", table, err)
		}
		if !strings.Contains(def, "'"+string(model.EvidenceOpClaimed)+"'") {
			continue // not a vocabulary CHECK; leave it alone
		}
		// pg_get_constraintdef suffixes an unvalidated constraint with
		// " NOT VALID"; validation state is carried by convalidated, so the
		// suffix is stripped before the equality — a NOT VALID residue of the
		// CANONICAL expression is current-but-unvalidated (re-VALIDATE below),
		// never stale.
		if strings.TrimSuffix(def, " NOT VALID") == canonicalDef {
			currentName, currentValidated = name, validated
		} else {
			stale = append(stale, name)
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("reconcile %q close state CHECKs: %w", table, err)
	}
	if currentName != "" && !currentValidated {
		// Round-1 residue (F-2): an interrupted upgrade left the current CHECK
		// NOT VALID. Never accepted as done — validate it now, and keep failing
		// the boot by name until the violating row is repaired. CHECK constraints
		// (unlike the state itself) apply conjunctively, so validation runs even
		// while stale siblings still exist; they are removed below.
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			"ALTER TABLE %s VALIDATE CONSTRAINT %s", table, currentName)); err != nil {
			return fmt.Errorf("reconcile %q: the settlement-vocabulary CHECK %s is NOT VALID and re-validation failed (repair the out-of-vocabulary rows; the boot will keep refusing): %w",
				table, currentName, err)
		}
		currentValidated = true
	}
	if currentName != "" && len(stale) == 0 {
		return nil // the one vocabulary CHECK is current and validated
	}
	if !owned {
		slog.Warn("evidence journal: skipping the settlement-vocabulary CHECK widening because this role does not own the table; 'withheld' settlements will REFUSE (deny-closed) until the table owner widens the constraint",
			"table", table, "check", current)
		return nil
	}
	// ONE transaction (F-3): drop the stale vocabulary CHECK(s) and, if no
	// current one exists, add the named current CHECK — WITHOUT `NOT VALID`, so
	// the historical scan runs inside this same transaction and a violation
	// rolls the entire transition back, previous backstop included.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reconcile %q: begin state-vocabulary transition: %w", table, err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, name := range stale {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", table, name)); err != nil {
			return fmt.Errorf("reconcile %q drop stale CHECK %s: %w", table, name, err)
		}
	}
	if currentName == "" {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			"ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s)", table, evidenceOpStateVocabConstraint, current)); err != nil {
			return fmt.Errorf("reconcile %q add %s (a row outside the current vocabulary blocks the widening; repair it — the transition rolled back whole and the boot will keep refusing): %w",
				table, evidenceOpStateVocabConstraint, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reconcile %q: commit state-vocabulary transition: %w", table, err)
	}
	return nil
}

// reconcileColumns brings each descriptor's table up to the descriptor: it CREATES
// a wholly missing table (with its base columns, isolation guards and indexes, the
// same DDL the v2 migration would have generated) and adds any NULLABLE field an
// existing table is missing, then ensures the descriptor's secondary indexes
// exist. It is the additive-schema-growth vehicle for BOTH core auth/entity tables
// AND module-owned tables — neither path otherwise ALTERs an existing
// table: a core table's CREATE TABLE is frozen in the v2 "core_entities" migration
// (migrate.Apply skips v2 on an already-migrated database), and a module table's
// CREATE TABLE runs in applyModuleTables ONLY when the table is not yet tracked, so
// once tracked it is never re-touched. In both cases a descriptor that gained a
// column would be created on a FRESH database (regenerated from the current
// descriptors) but never on an existing one — bricking CRUD that selects the new
// column. A naive CREATE/ALTER in a new migration would then fail on the fresh
// database (the object already exists). This reconcile resolves both: it
// introspects the live schema and adds ONLY what is missing, so it converges fresh
// and existing databases identically and is a no-op once applied. It is strictly
// additive — never ALTER/DROP/RENAME of an existing column (the no-destructive-
// migration rule) — and refuses to add a NOT NULL column to an existing table
// (which a populated table cannot accept without a default; that genuinely needs a
// hand-authored migration). It runs after the core migrations / module-table
// creation and before the self-test, so a created table is still covered by the
// isolation-guard self-check.
func reconcileColumns(ctx context.Context, db dialect.Execer, dia dialect.Dialect, descs []model.EntityDescriptor) error {
	for _, d := range descs {
		have, err := dia.TableColumns(ctx, db, d.Table)
		if err != nil {
			return fmt.Errorf("introspect %q: %w", d.Table, err)
		}
		if len(have) == 0 {
			// The table does not exist: a fresh DB creates it in the v2 migration
			// before this runs, so this is an existing database meeting a descriptor
			// added after its v2 — create the whole table (guards and indexes
			// included) exactly as v2 would have. In ONE transaction, like
			// createModuleTable: a mid-create failure must leave nothing behind, or
			// the next boot would find a guard-less table and the self-test would
			// refuse to open forever.
			if cerr := createTableTx(ctx, db, dia, d); cerr != nil {
				return fmt.Errorf("reconcile %q create table: %w", d.Table, cerr)
			}
			continue
		}
		for _, f := range d.Fields {
			if have[f.Name] {
				continue
			}
			if !f.Nullable {
				return fmt.Errorf(
					"reconcile %q: cannot add non-nullable column %q to an existing table without a default; author an explicit migration",
					d.Table, f.Name)
			}
			stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", d.Table, f.Name, dia.ColumnType(f.Kind, true))
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("reconcile %q add column %q: %w", d.Table, f.Name, err)
			}
		}
		for _, stmt := range reconcileIndexStmts(d) {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("reconcile %q index: %w", d.Table, err)
			}
		}
	}
	return nil
}

// createTableTx creates a descriptor's table, guards and indexes atomically.
func createTableTx(ctx context.Context, db dialect.Execer, dia dialect.Dialect, d model.EntityDescriptor) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	for _, stmt := range dia.CreateTableStmts(d) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// reconcileIndexStmts renders idempotent (IF NOT EXISTS) CREATE INDEX statements
// for a descriptor's indexed fields and explicit indexes, mirroring the dialect's
// table-creation index DDL. Both engines accept IF NOT EXISTS for indexes, so the
// same statements are safe on a fresh table (no-op) and an existing one (adds a
// newly-declared index, e.g. a field promoted to Indexed).
func reconcileIndexStmts(d model.EntityDescriptor) []string {
	var out []string
	for _, f := range d.Fields {
		if f.Indexed {
			out = append(out, fmt.Sprintf(
				"CREATE INDEX IF NOT EXISTS %s_%s_idx ON %s(tenant_id, %s)",
				d.Table, f.Name, d.Table, f.Name))
		}
	}
	for _, ix := range d.Indexes {
		unique := ""
		if ix.Unique {
			unique = "UNIQUE "
		}
		out = append(out, fmt.Sprintf("CREATE %sINDEX IF NOT EXISTS %s ON %s(%s)",
			unique, ix.Name, d.Table, strings.Join(ix.Columns, ", ")))
	}
	return out
}

// applyModuleTables creates each registered module table that does not yet exist,
// keyed by table name in a tracking table. Each table is generated from its
// validated descriptor (inheriting base columns and the tenant/append-only
// guards) and created atomically with its tracking row. Because tracking is by
// name, the set of modules can change between boots without corrupting the
// schema; a removed module simply leaves its (orphan) table in place.
func applyModuleTables(ctx context.Context, db dialect.Execer, dia dialect.Dialect, mods []model.EntityDescriptor) error {
	if len(mods) == 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE IF NOT EXISTS "+moduleTablesTracking+" (table_name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)"); err != nil {
		return err
	}
	applied, err := appliedModuleTables(ctx, db)
	if err != nil {
		return err
	}
	for _, d := range mods {
		if applied[d.Table] {
			continue
		}
		if err := createModuleTable(ctx, db, dia, d); err != nil {
			return fmt.Errorf("create module table %q: %w", d.Table, err)
		}
	}
	return nil
}

func appliedModuleTables(ctx context.Context, db dialect.Querier) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT table_name FROM "+moduleTablesTracking)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		set[name] = true
	}
	return set, rows.Err()
}

func createModuleTable(ctx context.Context, db dialect.Execer, dia dialect.Dialect, d model.EntityDescriptor) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	for _, stmt := range dia.CreateTableStmts(d) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	ins := dia.Rebind("INSERT INTO " + moduleTablesTracking + "(table_name, applied_at) VALUES(?, ?)")
	if _, err := tx.ExecContext(ctx, ins, d.Table, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}
