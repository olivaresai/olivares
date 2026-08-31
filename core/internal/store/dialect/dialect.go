// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package dialect isolates the handful of real divergences between the two
// supported backends (SQLite via modernc, Postgres via pgx/stdlib) behind one
// small interface. There is no query builder and no ORM: SQL is authored once
// with '?' placeholders and portable syntax, and the dialect only covers what
// genuinely differs — placeholder form, column types, the per-transaction
// tenant binding, and the data-definition statements for module tables and
// their isolation guards (ARCHITECTURE.md).
package dialect

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Querier is the read/exec surface shared by *sql.DB and *sql.Tx, so dialect
// helpers can run against either.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Execer is Querier plus transaction control: the surface shared by *sql.DB and
// *sql.Conn (NOT *sql.Tx, which cannot nest). Schema work takes it rather than a
// *sql.DB so it can run on ONE named connection — specifically the connection
// that holds the cluster-wide migration advisory lock, instead of racing the pool
// for a different one (residual R1). Session-scoped settings such as lock_timeout
// then govern the statements they were set for, which is impossible when every
// step may land on a different pooled connection.
//
// *sql.DB still satisfies it, so a caller that legitimately wants pool semantics
// passes the pool and nothing changes for it.
type Execer interface {
	Querier
	// QueryRowContext is not in Querier because dialect helpers do not need it,
	// but schema reconcilers do — a single-row catalog probe is the commonest
	// shape in that path.
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// Audit spool bookkeeping table names. Usage is global mutable accounting;
// gaps is per-tenant mutable integrity state.
const (
	AuditSpoolUsageTable = "audit_spool_usage"
	AuditSpoolGapsTable  = "audit_spool_gaps"
	// AuditBlindingStateTable records ONCE whether this ledger has been actuated
	// onto the blinded metadata-commitment rule. Like AuditSpoolUsageTable it is
	// global bookkeeping about the ledger rather than tenant data or evidence, so it
	// deliberately carries neither row-level security nor an append-only trigger —
	// which is what lets the write rule be resolved at boot from the application
	// pool, with no tenant bound and without querying the RLS-guarded ledger.
	AuditBlindingStateTable = "audit_meta_blinding"
	// ControlRolloutStateTable records ONCE, per staged control, whether this
	// deployment predates the control — and therefore which disposition it starts
	// in. Same class as the two above and un-guarded for the same reason: it is
	// bookkeeping about the DEPLOYMENT, so it must be readable with no tenant bound,
	// which a row-level-security policy calling current_setting('app.tenant_id')
	// would make impossible. See core/store/rollout.go for why the fact has to be
	// durable rather than configured or re-derived.
	ControlRolloutStateTable = "control_rollout_state"
	// ControlRolloutTransitionTable is the APPEND-ONLY history of deliberate rollout
	// decisions. The state row above carries only the LATEST decision's metadata, and
	// a single mutable row is not a history: an operator auditing why a control is
	// enforced needs every transition, not the most recent overwrite.
	//
	// CORRECTED: this used to end "Same class as the state table and un-guarded for the
	// same reason", and that sentence fused two different properties. What the state
	// table's reason establishes is the absence of ROW-LEVEL SECURITY — it must be
	// readable with no tenant bound. It establishes nothing about the absence of an
	// IMMUTABILITY TRIGGER, and this relation was called append-only in six places while
	// having no database guarantee whatsoever: measured, an UPDATE rewriting decided_by,
	// decided_reason and evidence was accepted, and the next boot served the forged
	// values without a word. It now carries the same guard every other append-only
	// relation does, emitted idempotently at boot (see reconcileRolloutEvidenceGuards).
	// The state table still must NOT: it takes UPDATEs in production, by design.
	ControlRolloutTransitionTable = "control_rollout_transitions"
	// ControlRolloutClassificationTable is the durable RECEIPT that a control was
	// classified, and it exists because the state row alone cannot distinguish "never
	// classified" from "classified, and the row is gone".
	//
	// The guard that catches a lost state row reads the TRANSITION history, so it only
	// fires for a control somebody had already transitioned. A control still in its
	// original disposition — the default for every fresh install — has no history, so
	// losing its state row read as a first encounter, and the witness table now exists
	// (an earlier boot created it), so the re-derivation lands on the LEGACY disposition
	// with the commitment cleared and a green boot. Measured end to end.
	//
	// It is APPEND-ONLY and guarded as such: one row per control, written in the same
	// transaction as the state row it justifies, never updated and never deleted.
	ControlRolloutClassificationTable = "control_rollout_classifications"
	// ControlAppendOnlyScopeTable is the durable inventory of every relation that has
	// ever been in the append-only ACL scope.
	//
	// It exists because the scope used to be derived from the immutability trigger
	// itself, so a DROP TRIGGER removed the table from the set whose protection is
	// checked — destroying the guard destroyed the obligation to have one. The catalog
	// is now a source of ADMISSION only; removal has to be deliberate and visible. See
	// core/internal/store/sqlstore/appendonlyscope.go.
	ControlAppendOnlyScopeTable = "control_appendonly_scope"
)

// Dialect adapts the engine-neutral store to one backend.
type Dialect interface {
	// Name is the engine this dialect targets.
	Name() store.Engine

	// Rebind rewrites '?' placeholders into the engine's form (SQLite keeps
	// '?'; Postgres rewrites to $1,$2,… left to right), skipping '?' inside
	// single-quoted string literals.
	Rebind(query string) string

	// ColumnType renders the column type for a portable kind, including the
	// NOT NULL clause when nullable is false.
	ColumnType(k model.SQLKind, nullable bool) string

	// BindTenant pins tenant for the current transaction: Postgres sets the
	// app.tenant_id GUC (tx-local); SQLite writes the scope-pin row that its
	// tripwire triggers check.
	BindTenant(ctx context.Context, tx *sql.Tx, tenant model.TenantID) error

	// ClearTenant removes the pin for a privileged cross-tenant (System)
	// transaction: Postgres resets the GUC; SQLite empties the pin row.
	ClearTenant(ctx context.Context, tx *sql.Tx) error

	// TenancyStmts renders the one-time engine prerequisites for isolation:
	// SQLite creates the scope-pin table; Postgres creates the shared mutation-
	// blocking trigger function. Run before any entity table is created.
	TenancyStmts() []string

	// AuditTableStmts renders the append-only, hash-chained audit_events table
	// with its tenant guard, immutability guard and the unique(tenant_id, seq)
	// constraint that backstops the chain against duplicate sequence numbers.
	AuditTableStmts() []string

	// AuditSpoolStmts renders the global mutable usage counter and the
	// tenant-guarded mutable pending-gaps table used by ADR-0024 Q2. Neither is
	// append-only: they are bookkeeping, not evidence rows.
	AuditSpoolStmts() []string

	// CreateTableStmts renders the full DDL for a registered module table:
	// CREATE TABLE with the injected base columns and entity columns, the
	// secondary indexes, and the unconditional tenant isolation guard (plus the
	// append-only guard when the descriptor requests it). The engine, never the
	// module, emits these — so a module entity cannot escape isolation.
	CreateTableStmts(d model.EntityDescriptor) []string

	// GuardedTables returns the set of tenant tables that currently carry this
	// engine's isolation guard (Postgres: FORCE row-level security plus a
	// policy; SQLite: the tripwire triggers). The boot self-test compares this
	// to the set of tenant tables and refuses to start if any is unguarded.
	GuardedTables(ctx context.Context, q Querier) (map[string]bool, error)

	// SchemaTriggers returns live non-internal triggers in the dialect's operating
	// schema, keyed by their full identity and carrying the catalog's RAW firing
	// state (never a verdict — see TriggerEnableState). PostgreSQL also reports the
	// invoked function and whether the dialect's configured APPLICATION role can
	// EXECUTE it, even when q is an owner migration transaction. SQLite has no
	// function privilege layer and instead reports the catalog form of the trigger's
	// own definition.
	SchemaTriggers(ctx context.Context, q Querier) (map[TriggerKey]TriggerInfo, error)

	// SchemaName is the schema the dialect operates in ("public" on Postgres via
	// current_schema(), "main" on SQLite). It qualifies a module's declared
	// trigger, which names only a table and a trigger.
	SchemaName(ctx context.Context, q Querier) (string, error)

	// TableColumns returns the set of column names a table currently has, or an
	// empty set when the table does not exist. It powers the additive-column
	// reconcile (sqlstore): a core entity that gained a NULLABLE field since its
	// v2 CREATE TABLE has the column ADDed on an already-migrated database,
	// without a destructive migration and without diverging from a fresh DB
	// (whose v2 already includes it). It introspects the live schema, never the
	// descriptor.
	TableColumns(ctx context.Context, q Querier, table string) (map[string]bool, error)

	// AppendOnlyACLStmts renders the idempotent statements that re-assert the
	// append-only ACL — no UPDATE/DELETE/TRUNCATE for the application role — on
	// tables that already exist. Postgres returns one REVOKE per table, guarded on
	// the role existing; SQLite returns nil, having no role layer.
	//
	// It exists because the revoke is otherwise emitted ONLY inside table-creation
	// DDL, and every creation path is one-shot: migrate.Apply skips applied
	// versions, applyModuleTables skips tracked tables, and reconcileColumns only
	// creates a table that is wholly absent. On an already-migrated database — every
	// existing deployment — those statements never run again, so correcting the role
	// the revoke targets would change nothing there. Boot re-asserts it with these.
	AppendOnlyACLStmts(tables []string) []string

	// GuardControlPlaneStmts renders the DDL of the C4 guard control plane: the three
	// append-only logs the guard rollout keeps its own history in, their immutability
	// guards, and (on Postgres) the ACL posture that denies the application role even
	// INSERT on them.
	//
	// It is implemented on BOTH engines because buildCoreMigrations runs on both: a core
	// migration carrying PostgreSQL SQL would break every SQLite boot. The guard RUNNER
	// stays PostgreSQL-only; this migration exists so a SQLite deployment has a coherent
	// tracked version rather than a hole in its history.
	//
	// DDL only. The bootstrap ROWS carry digests and timestamps, so they are written by
	// the engine with BOUND parameters inside the same migration transaction — see
	// migrate.Migration.Exec.
	GuardControlPlaneStmts() []string

	// DirectoryWriterControlStmts renders core v7's raw directory-writer control.
	// It is deliberately not an entity descriptor: runtime code may read it only
	// through the engine-owned writer seam, and no public repository is generated.
	// PostgreSQL creates the singleton control row and closes its split-owner ACL
	// at birth; SQLite additionally creates the transaction marker at its durable
	// empty baseline.
	DirectoryWriterControlStmts() []string

	// GuardMetadataACLStmts re-asserts the control plane's ACL posture on every boot.
	//
	// It exists for exactly the reason AppendOnlyACLStmts does: the revoke inside the
	// creation DDL runs once and never again, so on an already-bootstrapped database — every
	// database after the first boot — nothing would re-assert it. The posture is STRICTER
	// than the append-only one: the application role is denied INSERT as well, because
	// runtime traffic never writes this history.
	//
	// Postgres returns one statement per relation; SQLite returns nil, having no role layer.
	GuardMetadataACLStmts() []string

	// AppendOnlyACLTables lists the tables that carry this engine's append-only
	// guard according to the LIVE schema. Postgres finds them by their immutability
	// trigger; SQLite returns nil, having no ACL layer to maintain.
	//
	// The registry is not a sufficient source for this. A module that is removed from
	// a build deliberately leaves its tables in place (they hold retained evidence),
	// so a set built only from currently-registered descriptors would stop protecting
	// exactly the rows nobody is writing any more and everybody still relies on.
	AppendOnlyACLTables(ctx context.Context, q Querier) ([]string, error)

	// AppendOnlyGuardStmts renders the immutability guard for ONE table in a form that
	// may be re-issued on every boot.
	//
	// The guards that already exist are emitted by table-CREATION DDL, which runs once
	// and never again — so a relation created before this edition, or one whose class
	// was reclassified as evidence later, has no path to acquiring one. That is not
	// hypothetical: control_rollout_transitions is documented append-only in six places
	// and carries no database guarantee at all, and a "one-shot DDL cannot converge"
	// argument was used to shelve the fix. This is the converging form, and it is
	// deliberately separate from CreateTableStmts so the creation path keeps its
	// non-idempotent statements, whose failure on a re-run is a signal worth keeping.
	//
	// Postgres uses CREATE OR REPLACE TRIGGER, available in every supported major
	// (15..18). SQLite uses CREATE TRIGGER IF NOT EXISTS. Both include the ACL half
	// where the engine has one: no row trigger can observe TRUNCATE.
	AppendOnlyGuardStmts(table string) []string

	// ConnRolePosture reports the privilege level of the role this connection
	// authenticates as. Postgres silently bypasses ALL row-level security for a
	// superuser or a BYPASSRLS role, so the FORCE-RLS guard is only a real
	// cross-tenant backstop when the connecting role has neither attribute; the
	// boot guard refuses to start otherwise (docs/SECURITY-HARDENING.md). SQLite has no roles —
	// its tripwire triggers apply unconditionally — so it reports a safe posture.
	ConnRolePosture(ctx context.Context, q Querier) (RolePosture, error)

	// ConnRoleIdentity reports ONLY which role this connection authenticates as,
	// and exists because identity is not privilege.
	//
	// ConnRolePosture answers two questions in one statement — "which role is this"
	// and "may it bypass RLS" — and only the second needs pg_roles, whose PUBLIC
	// grant an operator may revoke. MEASURED on PostgreSQL 15.18/16.14/17.10/18.4:
	// after `REVOKE SELECT ON pg_catalog.pg_roles FROM PUBLIC`, ConnRolePosture's
	// statement fails with 42501 while `SELECT current_user` still answers. Binding
	// them together is what let an unreadable CATALOG be recorded as an unknown
	// IDENTITY, and a caller then read that empty string as a role name it could
	// substitute a default for.
	//
	// It is the fallback, never the primary: a boot that can read the posture already
	// has the identity and does not ask twice.
	ConnRoleIdentity(ctx context.Context, q Querier) (string, error)
}

// SchemaTriggerFunctionKey is the structured identity of a zero-argument
// PostgreSQL trigger function. Schema and Name remain separate because parsing a
// regprocedure rendering is ambiguous for quoted identifiers containing dots or
// parentheses.
type SchemaTriggerFunctionKey struct {
	Schema string
	Name   string
}

// SchemaTriggerCallerInventory is the optional cross-schema catalog surface used
// only while guarding a declared trigger-function identity transition. A normal
// boot self-test asks SchemaTriggers about the engine schema; a transition must
// instead find every pre-existing caller of the old function tuples anywhere in
// the database so it can lock and retain them as byte-exact witnesses. It also
// proves that each freshly reserved next function has exactly the declared caller
// set, without deparsing unrelated trigger functions on every boot.
//
// PostgreSQL implements this interface. Other dialects deliberately do not: SQLite
// has no separately replaceable trigger function shared across schemas.
type SchemaTriggerCallerInventory interface {
	SchemaTriggerCallers(
		ctx context.Context,
		q Querier,
		functions []SchemaTriggerFunctionKey,
	) (map[TriggerKey]TriggerInfo, error)
}

// SchemaTriggerFunctionInfo is a directed PostgreSQL catalog projection for one
// zero-argument routine identity. OID is retained so a migration cannot drop and
// recreate an old shared function behind an identical pg_get_functiondef, which
// would also discard a concurrently added dependency. ACL is PostgreSQL's complete,
// effective aclitem[] rendering (including grantors and grant options), not merely
// an executable verdict: CREATE OR REPLACE must preserve the exact ACL installed by
// the reservation. ACLIsExact additionally proves that the only ACL entries are
// non-grantable EXECUTE for the owner and configured application role (one entry
// when those roles are the same). PublicCanExecute remains separate for precise
// diagnostics.
type SchemaTriggerFunctionInfo struct {
	OID                  int64
	Definition           string
	ACL                  string
	ACLIsExact           bool
	CanExecute           bool
	AppRoleDirectExecute bool
	PublicCanExecute     bool
}

// SchemaTriggerFunctionCatalog is the PostgreSQL-only catalog and reservation
// surface for a trigger's function-identity transition. Reserve creates a
// fail-closed trigger-function placeholder with a plain CREATE FUNCTION, revokes
// PUBLIC and grants EXECUTE to the dialect's configured application role. Because
// the placeholder is born in the owner migration transaction, no other session
// can resolve it before that transaction commits.
type SchemaTriggerFunctionCatalog interface {
	SchemaTriggerFunction(
		ctx context.Context,
		q Querier,
		function SchemaTriggerFunctionKey,
	) (SchemaTriggerFunctionInfo, bool, error)
	ReserveSchemaTriggerFunction(
		ctx context.Context,
		q Querier,
		function SchemaTriggerFunctionKey,
	) error
}

// TriggerKey is a trigger's full identity. A map keyed by NAME alone is unsound
// on PostgreSQL: a trigger name only has to be unique per table, so two triggers
// with the same name on different tables silently collapse into one entry — and
// whichever survives decides whether the self-test passes.
type TriggerKey struct {
	Schema string
	Table  string
	Name   string
}

// String renders the key for diagnostics.
func (k TriggerKey) String() string { return k.Schema + "." + k.Table + "." + k.Name }

// TriggerEnableState is the catalog's per-trigger firing mode, kept as the RAW
// catalog value rather than a pre-digested boolean. PostgreSQL stores it in
// pg_trigger.tgenabled as a single character; SQLite has no such column, so its
// dialect reports TriggerFiresAlways for every trigger present in the schema.
//
// Keeping the raw value has two payoffs the boolean did not. The decision that
// turns a state into "does this guard actually run" becomes a pure, unit-testable
// function instead of a predicate buried inside a SQL string that only a live
// server can exercise. And the boot error can NAME the state an operator has to
// undo, instead of reporting a bare "disabled".
type TriggerEnableState string

const (
	// TriggerFiresOrigin ('O') is the default. It fires when the session's
	// session_replication_role is 'origin' or 'local', and is SKIPPED in 'replica'.
	TriggerFiresOrigin TriggerEnableState = "O"
	// TriggerNeverFires ('D') is `ALTER TABLE … DISABLE TRIGGER`: the trigger stays
	// listed in the catalog and never runs, in any session.
	TriggerNeverFires TriggerEnableState = "D"
	// TriggerFiresReplicaOnly ('R') is `ALTER TABLE … ENABLE REPLICA TRIGGER`: it
	// runs ONLY in a session_replication_role='replica' session — which the boot
	// guard refuses outright — so for this engine it is indistinguishable from
	// disabled, while looking perfectly healthy to any check that reads only names.
	TriggerFiresReplicaOnly TriggerEnableState = "R"
	// TriggerFiresAlways ('A') is `ALTER TABLE … ENABLE ALWAYS TRIGGER`: it runs in
	// every session, including a replica one. It is a state an operator CHOSE.
	TriggerFiresAlways TriggerEnableState = "A"
	// TriggerNoEnableState is for engines that have NO per-trigger enable state at
	// all — SQLite, where a trigger present in the schema fires unconditionally on
	// every connection and there is nothing to enable or disable.
	//
	// It is deliberately NOT the same value as PostgreSQL's 'A'. Mapping SQLite onto
	// ENABLE ALWAYS looked harmless and was not: any POLICY about the ALWAYS state —
	// and there is one open, about logical replication — would silently apply to an
	// engine where that state cannot be chosen, rejecting every SQLite trigger the
	// moment the policy said "refuse ALWAYS". A state an operator chose and a state
	// the engine does not have are different facts and now have different values.
	TriggerNoEnableState TriggerEnableState = "none"
	// TriggerStateUnknown is the zero value: no state was read from the catalog.
	TriggerStateUnknown TriggerEnableState = ""
)

// Fires reports whether a trigger in this state runs for the sessions this engine
// permits to write.
//
// PRECONDITION — the session is NOT session_replication_role='replica'. That is
// not left to chance: the boot guard refuses such a session for the application
// pool (sqlstore/store.go:105) and for the owner pool that runs every migration
// (sqlstore/dbsetup.go:87), and the trigger self-test re-asserts the posture
// before it trusts this answer. The re-assertion earns its keep because in a
// replica session the mapping INVERTS — 'R' would fire and 'O' would not — so a
// silent precondition failure would flip every verdict here to its opposite.
//
// Unknown states are deny-closed: a value this build does not recognize (a future
// PostgreSQL firing mode, or a catalog read that returned nothing) reports NOT
// firing. A security guard is never credited with running on the strength of a
// value nobody has characterized.
func (s TriggerEnableState) Fires() bool {
	switch s {
	case TriggerFiresOrigin, TriggerFiresAlways, TriggerNoEnableState:
		return true
	default:
		return false
	}
}

// Describe renders the state as an actionable phrase for a boot error: what the
// trigger currently does, and the statement that undoes it.
func (s TriggerEnableState) Describe() string {
	switch s {
	case TriggerFiresOrigin:
		return `fires normally ("O")`
	case TriggerFiresAlways:
		return `fires always, replica sessions included ("A", ENABLE ALWAYS TRIGGER)`
	case TriggerNoEnableState:
		return "fires unconditionally — this engine has no per-trigger enable state"
	case TriggerNeverFires:
		return `DISABLED — listed in the catalog and never fires ("D", DISABLE TRIGGER); undo with ALTER TABLE … ENABLE TRIGGER`
	case TriggerFiresReplicaOnly:
		return `REPLICA-ONLY — fires only in a session_replication_role='replica' session, which this engine refuses to open, so it never fires here ("R", ENABLE REPLICA TRIGGER); undo with ALTER TABLE … ENABLE TRIGGER`
	case TriggerStateUnknown:
		return "no enable state was read from the catalog"
	default:
		return fmt.Sprintf(
			"unrecognized enable state %q — this build cannot prove the trigger fires, so it is treated as inert",
			string(s))
	}
}

// TriggerInfo is live trigger metadata used by the deny-closed boot self-test.
type TriggerInfo struct {
	// Function is the trigger function this trigger invokes (Postgres only).
	Function string
	// FunctionSchema and FunctionName are the same PostgreSQL function identity
	// carried structurally. Function's regprocedure rendering is for diagnostics;
	// it is not safe to parse because quoted identifiers may themselves contain
	// dots and parentheses. Schema migration fencing uses these fields instead.
	FunctionSchema string
	FunctionName   string
	// CanExecute reports whether the configured application role may EXECUTE that
	// function. On PostgreSQL this is deliberately not inferred from q's current_user:
	// schema migrations may project through a distinct owner transaction while the
	// application role is the one that must execute the trigger at runtime.
	CanExecute bool
	// EnableState is the catalog's firing mode for this trigger. PostgreSQL keeps a
	// DISABLED (or replica-only) trigger in the catalog, so presence alone proves
	// nothing: the object stays listed while it never runs.
	EnableState TriggerEnableState
	// Definition is the catalog's own canonical evidence for the complete trigger,
	// used to detect a same-name object whose behavior was replaced with a no-op —
	// which no amount of structural checking catches. SQLite stores the complete
	// statement in sqlite_schema.sql. PostgreSQL composes pg_get_triggerdef with
	// pg_get_functiondef using an injective length-prefixed frame, because its
	// executable body lives in the referenced function rather than the trigger row.
	Definition string
}

// RolePosture is the privilege level of the connecting database role, used by
// the RLS-bypass boot guard. RLS-safe means: not a superuser and not BYPASSRLS.
type RolePosture struct {
	// Role is the connecting role name (informational, for the error message).
	Role string
	// Superuser is true when the role is a Postgres superuser (bypasses RLS).
	Superuser bool
	// BypassRLS is true when the role has the BYPASSRLS attribute.
	BypassRLS bool
	// ReplicationRole is the connection's session_replication_role. In "replica"
	// PostgreSQL SKIPS every ordinary ("O") trigger — which is every security trigger
	// this schema installs: the append-only immutability guards and the module cutover
	// guards. The catalog still lists them, so nothing looks broken; the guards simply
	// stop running. The setting is NOT superuser-only: since PostgreSQL 15 it can be
	// granted with GRANT SET ON PARAMETER, and it can be PINNED onto the role or the
	// database (ALTER ROLE/DATABASE ... SET). Reading the value therefore proves what
	// THIS session does, not that the role is unable to change it later — revoking
	// that parameter privilege is separate, still-open work. SQLite has no such
	// setting and
	// reports "origin".
	ReplicationRole string
}

// TriggersDisabled reports whether this session would silently skip ordinary
// triggers, making every trigger-based guard inert.
//
// PostgreSQL fires a simply-enabled trigger when the replication role is 'origin'
// (the default) OR 'local' — only 'replica' suppresses it. Treating 'local' as unsafe
// would refuse to open a database whose guards fire perfectly well, so the two
// firing modes are both accepted. The value is a PostgreSQL enum of exactly
// {origin, replica, local}; anything else is unknown and refused deny-closed.
func (p RolePosture) TriggersDisabled() bool {
	switch p.ReplicationRole {
	case "", "origin", "local":
		return false
	default:
		return true
	}
}

// RLSUnsafe reports whether this role would silently bypass row-level security,
// making the FORCE-RLS tenant backstop inert.
func (p RolePosture) RLSUnsafe() bool { return p.Superuser || p.BypassRLS }

// Why renders the reason a role is RLS-unsafe, for the boot-guard error.
func (p RolePosture) Why() string {
	switch {
	case p.Superuser && p.BypassRLS:
		return "a SUPERUSER with BYPASSRLS"
	case p.Superuser:
		return "a SUPERUSER"
	case p.BypassRLS:
		return "a BYPASSRLS role"
	default:
		return "RLS-safe"
	}
}

// New returns the dialect for an engine, or false for an unsupported one. On
// Postgres the append-only ACL targets DefaultAppRole; a caller that knows the role
// the application actually connects as should use NewForAppRole instead, so the
// revoke lands on that role rather than on a name nobody is using.
func New(engine store.Engine) (Dialect, bool) {
	return NewForAppRole(engine, DefaultAppRole)
}

// NewForAppRole returns the dialect for an engine with the append-only ACL aimed at
// appRole — the role the APPLICATION pool authenticates as, which is the role that
// must not hold UPDATE/DELETE/TRUNCATE on an append-only table.
//
// It is deliberately NOT the role that runs the DDL. In the owner/app split the
// schema is created by a separate owner role, so revoking from the DDL session's
// current_user would strip the owner of DML on its own tables while leaving the
// runtime role — the one that actually faces traffic — untouched: a strict
// downgrade. The caller reads the application pool's posture and passes that role.
//
// AN EMPTY appRole IS REFUSED ON POSTGRES, and it used to fall back to
// DefaultAppRole. The fallback is what made an UNKNOWN role indistinguishable from
// the conventional one, and the difference is not cosmetic: every revoke this
// dialect renders is gated on the target role existing (pgRevokeMutations,
// pgRevokeAllWritesUnlessOwner), so a guessed name that nobody uses makes the
// statement a silent success that revokes nothing. MEASURED: the v6 control-plane
// revoke aimed at an absent `olivares_app` returns DO — success — while the real
// application role keeps INSERT on the guard ledger.
//
// "Nobody could read the role" and "the role is the conventional default" are
// different facts, and a constructor is the wrong place to convert the first into
// the second. The caller knows which it has; New passes DefaultAppRole
// DELIBERATELY, a boot that could not resolve its role passes nothing and is told
// so here instead of being handed a dialect aimed at a stranger.
func NewForAppRole(engine store.Engine, appRole string) (Dialect, bool) {
	switch engine {
	case store.EngineSQLite:
		// SQLite has no role layer at all: its append-only defense is the trigger
		// pair, which applies to every connection. The role is meaningless here.
		return sqliteDialect{}, true
	case store.EnginePostgres:
		if appRole == "" {
			return nil, false
		}
		return postgresDialect{appRole: appRole}, true
	default:
		return nil, false
	}
}

// columnDef renders "<name> <type[ NOT NULL]>" for a base or entity column using
// the given dialect's type mapping.
func columnDef(d Dialect, name string, k model.SQLKind, nullable bool) string {
	return name + " " + d.ColumnType(k, nullable)
}
