// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"

	_ "modernc.org/sqlite" // database/sql driver "sqlite" (pure Go, no CGO)
)

// migrationUnlockTimeout bounds the release of the migration advisory lock. It
// is deliberately short and deliberately NOT derived from the caller's context:
// the release must still run when boot was canceled, but a release that hangs
// must not hold boot open. On expiry the session is retired rather than pooled,
// which releases the lock server-side anyway.
const migrationUnlockTimeout = 5 * time.Second

// systemRestoreTimeout bounds SQLite's compensating SYSTEM presentation after
// a failed callback. The same pinned *sql.Conn is retained across rollback and
// compensation, so no pool waiter can steal it; the timeout still prevents a
// damaged engine from hanging the caller forever.
const systemRestoreTimeout = 5 * time.Second

// sqlStore implements store.Store over database/sql for one backend.
type sqlStore struct {
	engine store.Engine
	db     *sql.DB
	// adminDB is a dedicated BYPASSRLS pool (Postgres + Config.AdminDSN) used ONLY
	// for genuinely cross-tenant System reads (ListOrgs / multi-tenant checkpoint
	// coverage). It equals db when no separate admin pool is configured (SQLite, or
	// Postgres without AdminDSN), in which case those reads run RLS-scoped on the
	// app pool. It is never handed to tenant-scoped code.
	adminDB *sql.DB
	dia     dialect.Dialect
	clock   model.Clock
	debug   bool
	reg     *registry
	// signEvent, when non-nil, signs every appended audit event after its chain
	// hash is computed (store.AuditEventSigner). Injected via Config.SignEvent.
	signEvent store.AuditEventSigner
	// spoolMaxBytes and spoolOnFull carry ADR-0024 Q2's logical audit budget and
	// exhaustion policy into each tenant audit writer. A zero budget is inert.
	spoolMaxBytes int64
	spoolOnFull   store.AuditSpoolMode
	// blindMeta is the resolved metadata-commitment WRITE rule (store.Config
	// AuditMetaBlinding). Reads never consult it: every row carries its own
	// discriminator, which is what lets a fleet upgrade before it flips and lets
	// nodes disagree mid-flip without breaking the chain.
	blindMeta bool
	// directoryStatus is the immutable B2 boot witness. It never enables K3:
	// later composition and activation cuts must earn that separately.
	directoryStatus store.DirectoryStatus
	// directoryGuardRoles is the non-secret role/topology fact attested during
	// Open. Activation reopens only the transient owner credential and compares
	// the live identities against this fact; the Store never retains OwnerDSN.
	directoryGuardRoles guardRoles
	// directoryAdminRole is the exact non-secret AdminDSN identity attested by
	// this Open. RetireUser pins a fresh admin transaction and must compare it to
	// this fact; same-database identity alone cannot detect a changed DSN or a
	// multi-host/pooler endpoint that authenticated as another role.
	directoryAdminRole guardRoleFact
	// elector decides whether this node is the active writer in an active-passive
	// HA cluster. Always non-nil: alwaysLeader for SQLite/single-node,
	// pgElector for Postgres. The write-gate consults elector.active(); the
	// composition root drives its lifecycle via Leader() (Run before serving,
	// Resign at shutdown). Until Run is called it reports active, so a store opened
	// directly (every test, the embedded mode) behaves as the historical single
	// writer.
	elector elector
}

// Open constructs a Store: it opens the pool, registers core and module
// entities, runs migrations, runs the boot conformance self-test (refusing to
// open if any tenant table lacks its isolation guard), and only then returns a
// ready store. register may be nil when no modules contribute entities.
//
// Open is intentionally the only constructor and it lives in this internal
// package: a module never holds a Store, only a Scope, so it cannot reach the
// privileged System path or open an unscoped connection.
//
// guardPreServeTestHook runs immediately before the final control-plane verification, and is
// nil in every build that is not running this package's tests.
//
// IT IS THE SEAM OF A WINDOW, not a convenience. The property the final verification defends is
// drift committed after the migration lock is released and before the store is handed out, and
// that instant is not otherwise reachable from a test: drift planted earlier is refused by the
// verification INSIDE the rollout, so a regression built that way stays green with the final
// call deleted — which is exactly the class of vacuous test this campaign keeps finding.
var guardPreServeTestHook func()

func Open(ctx context.Context, cfg store.Config, register func(store.ExtensionRegistry) error) (store.Store, error) {
	switch cfg.AuditSpoolOnFull {
	case "", store.AuditSpoolBlock, store.AuditSpoolDegrade:
	default:
		return nil, fmt.Errorf("sqlstore: invalid audit spool on_full mode %q", cfg.AuditSpoolOnFull)
	}
	dia, ok := dialect.New(cfg.Engine)
	if !ok {
		return nil, fmt.Errorf("sqlstore: unsupported engine %q", cfg.Engine)
	}
	clock := cfg.Clock
	if clock == nil {
		clock = model.SystemClock{}
	}

	// INTERIM fail-closed refusal: boot had TWO independent places that take a
	// pooled connection while another is already pinned, and with exactly one
	// pooled connection each becomes a silent boot HANG rather than an error.
	// Refusing MaxConns==1 up front (before any connection is even dialed) turns
	// the hang into a diagnosis.
	//
	// The FIRST half is closed: migration work now runs on the very connection
	// that holds the migration advisory lock (withMigrationLock), so it no longer
	// asks the pool for a second one.
	//
	// The SECOND half is still open, and this comment previously got it wrong by
	// promising that landing the first half would let this refusal be "replaced by
	// a functional MaxConns=1 regression". It cannot: recomputeAuditSpoolUsage
	// opens a transaction on db, holds a FOR UPDATE row lock on the counter, and
	// only THEN reads the cross-tenant sum from adminDB — which IS db whenever no
	// AdminDSN is configured. That is a second two-connection demand, outside the
	// migration lock entirely, reached whenever AuditSpoolMaxBytes > 0.
	//
	// So the refusal is NARROWED to exactly the combination that still self-blocks:
	// a one-connection pool, a spool budget that makes the recompute run at all, and
	// no separate admin pool to read the sum from. Outside that combination a
	// single-connection pool now boots, which is what makes the MaxConns=1 boot a
	// usable regression: it passes only if EVERY migration step really runs on the
	// lock-holding connection. A blanket refusal would have hidden that.
	//
	// PostgreSQL only — SQLite is a single-writer engine whose pool semantics differ,
	// and 0 means "unlimited", so only the literal 1 is considered.
	if cfg.Engine == store.EnginePostgres && cfg.MaxConns == 1 &&
		cfg.AuditSpoolMaxBytes > 0 && strings.TrimSpace(cfg.AdminDSN) == "" {
		return nil, fmt.Errorf(
			"sqlstore: refusing MaxConns=1 on postgres with an audit-spool budget and no AdminDSN: the spool recompute opens a transaction on the application pool, holds a FOR UPDATE row lock on the counter, and only then reads the cross-tenant sum — which without AdminDSN comes from that same pool, so a single-connection pool HANGS at boot instead of failing. Any of three fixes works: set MaxConns to at least 2 (or 0 for the default), configure AdminDSN so the sum is read on its own pool, or set AuditSpoolMaxBytes to 0 to disable the budget. Migration work is NOT the reason — it now runs on the connection that holds the migration advisory lock")
	}

	db, err := openDB(cfg)
	if err != nil {
		return nil, err
	}

	// RLS-bypass boot guard (docs/SECURITY-HARDENING.md): on Postgres, refuse to start when the
	// connecting role is a superuser or BYPASSRLS — such a role silently bypasses
	// FORCE row-level security, so the multi-tenant backstop would be inert and
	// only the application-layer tenant predicate would separate tenants. This
	// converts a silent cross-tenant-leak risk into a startup failure. SQLite
	// reports an RLS-safe posture (no roles). Operators on a deliberately
	// single-tenant/dev deployment opt out with AllowPrivilegedRole.
	// The posture is read UNCONDITIONALLY, not just when the RLS guard is armed:
	// besides the privilege attributes it carries the role's IDENTITY, and that is
	// what the append-only ACL must target (below). Reading it under
	// AllowPrivilegedRole too costs one catalog query and keeps the two concerns —
	// "may this role bypass RLS" and "which role is this" — from sharing a branch.
	posture, perr := dia.ConnRolePosture(ctx, db)
	appRoleKnown := perr == nil
	if perr != nil {
		// Reading it is now mandatory for the ACL, but it must not become a NEW way to
		// refuse a deployment that used to boot: pg_roles' PUBLIC grant is revocable,
		// and a catalog-hardened install running with the privileged-role opt-out never
		// touched it before. Under that flag, degrade; without it, the RLS guard needed
		// this read anyway, so refuse.
		if !cfg.AllowPrivilegedRole {
			return closeOnErr(db, fmt.Errorf(
				"sqlstore: role posture check: %w — the RLS guard needs this role's attributes and the append-only ACL needs its identity, both read from pg_roles; grant SELECT on pg_roles or set AllowPrivilegedRole to proceed without either", perr))
		}
		// THE DEGRADE ASKS THE SEPARABLE HALF BEFORE GIVING UP, and this is where an
		// unreadable CATALOG used to become an unknown IDENTITY — which the code then
		// replaced with the conventional default name, aiming every revoke at a role
		// that may not exist and reporting success. Only the ATTRIBUTES need pg_roles;
		// current_user needs no grant at all. MEASURED on 15.18/16.14/17.10/18.4 with
		// `REVOKE SELECT ON pg_catalog.pg_roles FROM PUBLIC`: the posture query returns
		// 42501, current_user still answers.
		//
		// So the flag now buys exactly what its help text promises — an unverified RLS
		// backstop — and no longer silently buys an ACL aimed at a stranger.
		if id, iderr := dia.ConnRoleIdentity(ctx, db); iderr == nil {
			posture.Role = id
			appRoleKnown = true
			slog.Warn("could not read the connecting role's ATTRIBUTES (pg_roles is unreadable), so the RLS-bypass boot guard is UNVERIFIED for this connection and proceeds only because AllowPrivilegedRole is set. The role's IDENTITY was resolved separately, so the append-only ACL still targets the role this pool authenticates as",
				"role", id, "err", perr)
		} else {
			// Neither half answered. Refuse here, preserving both causes. In
			// particular ConnRoleIdentity rejects session_user != current_user,
			// which is an authority disguise rather than an unreadable catalog and
			// must never proceed under AllowPrivilegedRole.
			return closeOnErr(db, fmt.Errorf(
				"sqlstore: connecting role posture: %v; identity fallback refused: %w",
				perr, iderr,
			))
		}
	}
	// AND session_replication_role, which the branch adds and main did not carry.
	//
	// 'replica' makes PostgreSQL skip every ORDINARY trigger, and every trigger-based guard
	// in this schema is ordinary: the append-only immutability triggers and the module
	// cutover triggers. The catalog still lists them, so a self-test that checks presence
	// sees nothing wrong while the guards quietly stop running. ('origin' and 'local' both
	// fire ordinary triggers, so both are accepted — see RolePosture.TriggersDisabled.) The
	// setting is NOT superuser-only: since PostgreSQL 15 it can be granted with GRANT SET ON
	// PARAMETER, and it can be pinned onto the role or the database (ALTER ROLE / ALTER
	// DATABASE ... SET), so an unprivileged application connection can inherit it or be
	// given the right to set it. There is no opt-out: unlike an inert RLS backstop on a
	// single-tenant deployment, this has no legitimate use for the application connection.
	//
	// Gated on perr == nil for the same reason main gates the RLS refusal: a posture that
	// could not be read is not a posture that says 'replica'.
	if perr == nil && posture.TriggersDisabled() {
		return closeOnErr(db, fmt.Errorf(
			"sqlstore: refusing to start: the connecting %s role %q has session_replication_role=%q, which makes PostgreSQL SKIP every ordinary trigger — the append-only immutability guards and the module cutover guards would all be inert while still present in the catalog. Reset it (ALTER ROLE %s RESET session_replication_role, and check the database-level setting) so the connection runs in 'origin' mode",
			dia.Name(), posture.Role, posture.ReplicationRole, posture.Role))
	}

	if perr == nil && !cfg.AllowPrivilegedRole && posture.RLSUnsafe() {
		return closeOnErr(db, fmt.Errorf(
			"sqlstore: refusing to start: the connecting %s role %q is %s and SILENTLY BYPASSES row-level security, so multi-tenant isolation is unenforced (docs/08 §4). Provision a NOSUPERUSER NOBYPASSRLS application role (see deploy/postgres/01-app-role.sql) and point the DSN at it; or set AllowPrivilegedRole / pass --allow-privileged-db-role to accept an inert RLS backstop on a single-tenant or throwaway deployment",
			dia.Name(), posture.Role, posture.Why()))
	}

	// Aim the append-only ACL at the role this pool ACTUALLY authenticates as. It
	// used to be the compile-time constant dialect.DefaultAppRole, which made the
	// revoke a silent no-op on every deployment provisioned with `db init
	// --app-role <other>`: the statement is gated on the role existing, so a wrong
	// name changed nothing and raised nothing.
	//
	// posture.Role comes from the APPLICATION pool, which is the correct target in
	// both topologies. It is emphatically NOT the DDL session's current_user: in the
	// owner/app split the DDL runs as the owner, and revoking there would strip the
	// owner of DML on its own tables while leaving the runtime role untouched.
	//
	// THE BINDING IS NO LONGER GATED ON perr == nil, and that gate was the second half of
	// the same defect. When the posture read failed the binding was SKIPPED, which left dia
	// as dialect.New built it — aimed at DefaultAppRole — so the degrade path did not merely
	// fail to learn the role, it actively substituted a different one. Now the binding runs
	// on every Postgres boot and fails closed when the role is genuinely unknown, because
	// NewForAppRole refuses an empty role instead of defaulting it.
	if cfg.Engine == store.EnginePostgres {
		bound, ok := dialect.NewForAppRole(cfg.Engine, guardRoleFact{Role: posture.Role, Known: appRoleKnown}.bindable())
		if !ok {
			// Fail closed. Falling through would silently leave the dialect bound to
			// the default role name — precisely the defect this binding removes, and
			// the silence is what made that defect survive so long.
			return closeOnErr(db, fmt.Errorf(
				"sqlstore: cannot bind the append-only ACL to the connecting role on engine %q: this boot could not resolve which role the application pool authenticates as, and the revoke this dialect renders is gated on its target existing — so a guessed name would be a statement that succeeds and protects nobody. Grant SELECT on pg_roles, or fix whatever makes `SELECT current_user` fail on this connection", cfg.Engine))
		}
		dia = bound
	}

	// THE SUPPORTED-MAJOR REFUSAL, here and not inside the migration lock.
	//
	// It used to live in the guard preflight, which runs AFTER classifyRolloutControls — and
	// that function creates two relations and a durable row. So a server genuinely outside the
	// supported range could be MUTATED and only then refused, which makes the phrase "refused
	// before any DDL" false. This is the last point at which nothing has been created yet.
	if cfg.Engine == store.EnginePostgres {
		major, merr := postgresServerMajor(ctx, db)
		if merr != nil {
			return closeOnErr(db, merr)
		}
		if !postgresMajorSupported(major) {
			return closeOnErr(db, fmt.Errorf(
				"%w: the server is PostgreSQL %d and the supported range is %d..%d (the guard manifest's catalog projection has been executed against %d)",
				ErrGuardUnsupportedPostgresMajor, major,
				supportedPostgresMajorMin, supportedPostgresMajorMax, verifiedPostgresMajor))
		}
	}

	// Owner pool for DDL/migrations (Postgres only, opt-in via OwnerDSN — the field
	// is otherwise inert). The application pool (db, from DSN) above serves runtime
	// traffic; when an owner DSN is configured a SEPARATE least-privilege role owns
	// the schema and runs every migration, so the app role can be a non-owner holding
	// only DML grants (the deploy/postgres/01-app-role.sql split). Empty OwnerDSN — or
	// one equal to DSN — ⇒ owner == app, the single-role path, unchanged. The owner
	// role is held to the SAME RLS-safe bar as the app role: FORCE row-level security
	// protects even the table owner, so a superuser/BYPASSRLS owner would defeat it.
	// It is a DDL-only pool: the runtime never touches it, so it is closed once Open
	// returns (success or failure).
	ownerDB := db
	// guardRoles carries what this boot LEARNED about the two roles — not two strings.
	// OwnerConfigured false means the single-role topology BY CONFIGURATION, which is an
	// answer; the Known flags separate "read it" from "asked and got nothing", which the
	// old empty-string encoding could not.
	guardRolesForBoot := guardRoles{App: guardRoleFact{Role: posture.Role, Known: appRoleKnown}}
	if ownerDSN := strings.TrimSpace(cfg.OwnerDSN); cfg.Engine == store.EnginePostgres && ownerDSN != "" && ownerDSN != strings.TrimSpace(cfg.DSN) {
		ownerDB, err = openOwnerPool(ctx, dia, cfg, ownerDSN)
		if err != nil {
			return closeOnErr(db, err)
		}
		defer ownerDB.Close() //nolint:errcheck // DDL-only pool; runtime uses the app pool
		// The owner's ROLE, not merely the fact that a DSN was configured.
		//
		// The guard control plane's hardened ACL posture depends on the two pools
		// authenticating as DIFFERENT roles, and an OwnerDSN can perfectly well point at the
		// same role with a different host or sslmode. Comparing the resolved roles is the only
		// reading that answers the question actually being asked; comparing the DSN strings
		// would call that deployment hardened when it is not.
		//
		// AN UNREAD OWNER ROLE IS RECORDED AS UNREAD, and it used to be recorded as the
		// empty string — which guardMetadataSplit read as "no separate owner", i.e. as the
		// exact opposite of the fact that an OwnerDSN was configured. The deployment then
		// ran unhardened while its log said single-role, and the three defenses the split
		// buys were off. Here the fact is kept as a fact; resolveGuardMetadataPosture is
		// what refuses on it.
		guardRolesForBoot.OwnerConfigured = true
		if op, operr := dia.ConnRolePosture(ctx, ownerDB); operr == nil {
			guardRolesForBoot.Owner = guardRoleFact{Role: op.Role, Known: true}
		} else if id, iderr := dia.ConnRoleIdentity(ctx, ownerDB); iderr == nil {
			// Same separation as the application pool above: the topology question is about
			// IDENTITY, and identity does not need pg_roles. A catalog-hardened install that
			// left the owner readable by name keeps its hardened posture instead of losing it
			// to an attribute query it never needed for this decision.
			guardRolesForBoot.Owner = guardRoleFact{Role: id, Known: true}
			slog.Warn("could not read the owner pool's ATTRIBUTES, but resolved its identity separately, so the guard control plane keeps the posture the configured roles imply",
				"owner_role", id, "err", operr)
		} else {
			slog.Warn("could not read the owner pool's role by either route, so the guard control plane's topology is UNDETERMINED; the boot will refuse rather than assume the single-role posture the operator did not configure",
				"posture_err", operr, "identity_err", iderr)
		}
	}

	// A dedicated BYPASSRLS admin pool for cross-tenant System reads (Postgres
	// only). The app-pool guard above is unchanged — pointing --dsn at a privileged
	// role still fails closed. This pool is the explicit, named exception: it is
	// SUPPOSED to bypass RLS, but is held to least privilege (BYPASSRLS, not
	// superuser) and is never used for tenant-scoped work.
	adminDB := db
	if cfg.Engine == store.EnginePostgres && strings.TrimSpace(cfg.AdminDSN) != "" {
		adminDB, err = openAdminPool(ctx, dia, cfg)
		if err != nil {
			return closeOnErr(db, err)
		}
	}
	// closeOnErr below closes only the app pool; close the admin pool too if a
	// later boot step fails. Disarmed once the store is successfully constructed.
	adminOpened := adminDB != db
	bootOK := false
	defer func() {
		if !bootOK && adminOpened {
			_ = adminDB.Close()
		}
	}()
	adminRoleForBoot := guardRoleFact{}
	if adminOpened {
		adminPosture, postureErr := dia.ConnRolePosture(ctx, adminDB)
		if postureErr != nil {
			return closeOnErr(db, fmt.Errorf(
				"sqlstore: resolve boot AdminDSN identity: %w", postureErr,
			))
		}
		adminRoleForBoot = guardRoleFact{Role: adminPosture.Role, Known: true}
	}

	reg := newRegistry()
	for _, d := range coreDescriptors() {
		if err := reg.registerCore(d); err != nil {
			return closeOnErr(db, fmt.Errorf("sqlstore: core registry: %w", err))
		}
	}
	if register != nil {
		if err := register(reg); err != nil {
			return closeOnErr(db, fmt.Errorf("sqlstore: module registration: %w", err))
		}
	}
	reg.closed = true
	// Checked at close rather than at declaration, so a module may declare a staged
	// control before it registers the table that witnesses it.
	if err := reg.validateRolloutControls(); err != nil {
		return closeOnErr(db, fmt.Errorf("sqlstore: rollout controls: %w", err))
	}
	if err := reg.validateWorkspaceInitializers(); err != nil {
		return closeOnErr(db, fmt.Errorf("sqlstore: workspace initializers: %w", err))
	}
	// Retention depends on declarations that modules may register after their
	// descriptors, so it is validated at the same closed-registry boundary.
	if err := reg.validateRetainedDescriptors(); err != nil {
		return closeOnErr(db, fmt.Errorf("sqlstore: retained entities: %w", err))
	}
	// Load and bind every active-engine module migration before entering schema
	// work. In particular, a trigger transition that names a missing or duplicate
	// migration version must fail while boot has performed catalog reads only; it
	// must not be discovered after an earlier module has already committed DDL.
	moduleMigrationsForBoot, err := prepareModuleFileMigrations(dia, cfg.Engine, reg)
	if err != nil {
		return closeOnErr(db, fmt.Errorf("sqlstore: prepare module file migrations: %w", err))
	}

	// Apply all schema under a cluster-wide migration advisory lock (Postgres): with
	// replicaCount>1, every node runs Open and would otherwise race the DDL (a fresh
	// ADD COLUMN / module migration is not idempotent under concurrency the way a
	// CREATE TABLE IF NOT EXISTS is). The lock serializes migrations ACROSS nodes —
	// the second node finds them applied and no-ops. On SQLite it is a no-op (single
	// writer). It is a SEPARATE key from the leader-election lock, so migrating never
	// blocks leadership and vice-versa.
	// Every DDL step runs on the OWNER pool (ownerDB == db unless an owner DSN split
	// is configured), so the owner role owns the schema and the migration advisory
	// lock is held by the role doing the migrating.
	// The guard manifest is built ONCE, before the lock, from the registry that has just
	// closed. It is this binary's declaration of which objects it manages, so it depends on
	// nothing in the database — which is what lets the v6 bootstrap and the coordinator
	// compute the same rollout identity from it at two different points in boot.
	guardManifestForBoot, err := buildGuardManifest(reg.appendOnlyTables())
	if err != nil {
		return closeOnErr(db, err)
	}
	if err := requireCompleteGuardCurrentEdition(guardManifestForBoot); err != nil {
		return closeOnErr(db, fmt.Errorf("sqlstore: guard manifest: %w", err))
	}

	// The reconcile-session provider, taken from the OWNER pool rather than from the
	// migration connection.
	//
	// The distinction is the whole reason ReconcileSession exists: reconciliation is reached
	// after a COMMIT whose acknowledgement never arrived, which in practice means a COMMIT
	// whose connection has just died. Reading the receipt back through that same connection
	// asks the corpse whether it survived — both projections fail, both fold to unreadable,
	// and the one question worth asking can never be answered with yes.
	guardReconcileSession := func(ctx context.Context) (rowQuerier, func(), error) {
		c, cerr := ownerDB.Conn(ctx)
		if cerr != nil {
			return nil, func() {}, fmt.Errorf("sqlstore: open a session to reconcile a guard unit: %w", cerr)
		}
		return c, func() { _ = c.Close() }, nil
	}

	directoryWriterHardened := false
	if err := withMigrationLock(ctx, ownerDB, dia, func(mdb dialect.Execer) error {
		// FIRST, before anything creates anything: an older binary must not mutate a
		// database whose core migration history is ahead of the schema it understands.
		// migrate.Apply cannot own this check because ensureTracking performs DDL first.
		if err := preflightCoreMigrationVersion(ctx, mdb, dia, coreSupportedMigrationVersion); err != nil {
			return err
		}
		// A tracked v7 must already have its complete directory contract before
		// any additive reconciler runs. Otherwise reconcileColumns could recreate
		// a dropped table/index and launder corruption as ordinary schema growth.
		// The verifier is repeated post-reconcile below; this first pass preserves
		// the attribution and never repairs a target object.
		directoryV7Tracked, derr := coreDirectoryMigrationIsTracked(ctx, mdb, dia)
		if derr != nil {
			return fmt.Errorf("sqlstore: read core directory tracking state: %w", derr)
		}
		if directoryV7Tracked {
			if err := verifyCoreDirectoryRelationsPerBoot(ctx, mdb, dia, coreDescriptors()); err != nil {
				return fmt.Errorf("sqlstore: preflight core directory relations: %w", err)
			}
			if err := verifyDirectoryWriterControlPerBoot(ctx, mdb, dia); err != nil {
				return err
			}
			_, herr := verifyGuardCompletedV7History(ctx, mdb, dia, guardManifestForBoot)
			if herr != nil {
				return fmt.Errorf("sqlstore: preflight core v7 guard witness: %w", herr)
			}
		}
		// SECOND, still before this boot creates the witness. A staged control is classified by
		// whether its witness table ALREADY existed, and applyModuleTables below is
		// what creates that table on a fresh database — so a classification that ran
		// after it would observe a table it had just created and call every fresh
		// install an upgrade. See classifyRolloutControls.
		if err := classifyRolloutControls(ctx, mdb, dia, reg.rolloutControls()); err != nil {
			return err
		}
		// The C4 preflight comes THIRD, after classification and before any migration. Its
		// all-or-none question is about the three guard control-plane relations ONLY: the two
		// relations classifyRolloutControls just created are permitted predecessors, not
		// evidence of a half-finished guard bootstrap. It also refuses an unsupported
		// PostgreSQL major here, before any DDL, rather than after.
		if _, err := preflightGuardControlPlane(ctx, mdb, dia); err != nil {
			return err
		}
		// And the legacy trigger function is judged before v6 consumes it. On an existing
		// database olivares_block_mutation is already there; an exact one is reused and a
		// lookalike is refused, because replacing a function every installed guard already
		// points at would change objects this rollout has not even adopted yet.
		if dia.Name() == store.EnginePostgres {
			if err := verifyBootstrapFunction(ctx, mdb); err != nil {
				return err
			}
		}
		if err := migrate.Apply(ctx, mdb, dia, coreTrackingTable,
			buildCoreMigrations(
				dia,
				coreDescriptors(),
				guardBootstrapExec(dia, guardManifestForBoot),
				guardEditionTwoMigrationExec(dia, guardManifestForBoot),
			)); err != nil {
			return fmt.Errorf("sqlstore: core migrations: %w", err)
		}
		// Additively reconcile any core column/index/table the descriptors gained
		// since their v2 CREATE TABLE — the forward path for an already-migrated
		// database, a no-op on a fresh one (see reconcileColumns).
		if err := reconcileColumns(ctx, mdb, dia, coreDescriptors()); err != nil {
			return fmt.Errorf("sqlstore: reconcile core columns: %w", err)
		}
		// v7 is version-tracked and therefore skipped on every later boot. Repeat
		// its exact, non-healing directory relation verification after additive
		// reconciliation so losing or replacing a tombstone immutability guard is
		// a refusal rather than an object the tracker hides forever.
		if err := verifyCoreDirectoryRelationsPerBoot(ctx, mdb, dia, coreDescriptors()); err != nil {
			return fmt.Errorf("sqlstore: verify core directory relations: %w", err)
		}
		// Source tables added after the frozen v2 migration may only have come into
		// existence in reconcileColumns immediately above. With all nine present,
		// install/verify their generation guards while the migration lock is still
		// held. Resolve the same real role topology the guard control plane uses;
		// single-role/SQLite remain explicitly capability postures.
		writerHardened, wherr := resolveGuardMetadataPosture(ctx, mdb, dia, guardRolesForBoot)
		if wherr != nil {
			return wherr
		}
		directoryWriterHardened = writerHardened
		if err := reconcileDirectoryWriterGuards(ctx, mdb, db, dia, writerHardened, guardRolesForBoot); err != nil {
			return err
		}
		// Idempotent DATA normalization for core tables whose column semantics changed
		// (U4 federation alias backfill), AFTER the columns exist. See reconcileCoreData.
		if err := reconcileCoreData(ctx, mdb, dia); err != nil {
			return fmt.Errorf("sqlstore: reconcile core data: %w", err)
		}
		// The evidence ledger is raw DDL, so the descriptor reconciler above cannot
		// see it; it needs the same additive forward path (see reconcileAuditLedger).
		if err := reconcileAuditLedger(ctx, mdb, dia); err != nil {
			return fmt.Errorf("sqlstore: reconcile audit ledger: %w", err)
		}
		// The evidence-operation lifecycle CHECK is frozen in each table's CREATE
		// TABLE; widen it to the current settlement vocabulary on databases created
		// before a word existed (see reconcileEvidenceOpStateCheck).
		if err := reconcileEvidenceOpStateCheck(ctx, mdb, dia); err != nil {
			return fmt.Errorf("sqlstore: reconcile evidence operation states: %w", err)
		}
		if err := applyModuleTables(ctx, mdb, dia, reg.moduleDescriptors()); err != nil {
			return fmt.Errorf("sqlstore: module tables: %w", err)
		}
		// Additively reconcile any column/index a MODULE descriptor gained since its
		// table's CREATE TABLE. applyModuleTables only CREATES missing module
		// tables — it never ALTERs an existing (already-tracked) one — so without this a
		// nullable column appended to a module descriptor would exist on a fresh DB but
		// never on an in-place upgrade, bricking every read/write of that table. This
		// runs AFTER applyModuleTables (the tables now exist) and is a no-op on a fresh
		// DB (just created from the current descriptor). Same additive rules as core.
		if err := reconcileColumns(ctx, mdb, dia, reg.moduleDescriptors()); err != nil {
			return fmt.Errorf("sqlstore: reconcile module columns: %w", err)
		}
		// SQLite keeps its tenant pin in a durable single-row table. Core data
		// reconciliation above deliberately binds its own partition and can leave
		// that pin selected. Authored module migrations are privileged boot work and
		// may backfill every tenant, so enter them with the cross-tenant scope clear.
		if err := clearMigrationTenantScope(ctx, mdb, dia); err != nil {
			return err
		}
		if err := applyModuleFileMigrations(ctx, mdb, dia, moduleMigrationsForBoot); err != nil {
			return fmt.Errorf("sqlstore: module file migrations: %w", err)
		}
		// THE CONTROL PLANE'S OWN ACL, BEFORE THE ROLLOUT READS OR OPENS ANYTHING.
		//
		// The order is a correctness requirement, not tidiness, and the version that had it the
		// other way round was wrong in a way no test noticed. On a split topology provisioned by
		// deploy/postgres/01-app-role.sql, ALTER DEFAULT PRIVILEGES grants the application role
		// INSERT on every FUTURE table the owner creates — so the three control-plane relations
		// were born insertable by it, and the ENTIRE rollout (open, attempts, receipts, fold,
		// close) ran before the revoke below took that away. Inside that window the application
		// pool could append a receipt, a gate event or an inventory activation beside the
		// engine's own, and every one of them would be inside the checkpoint the close attests.
		// A rollout that failed before reaching the revoke left the window open until the next
		// boot.
		//
		// The v6 DDL now revokes in its own transaction (see pgRevokeAllWritesUnlessOwner), which
		// covers databases created from this migration onward. This leg covers the rest: an
		// in-place upgrade whose relations predate that DDL, and ACL drift applied afterwards.
		// Both are re-asserted on every boot, because the DDL revoke runs once and never again.
		//
		// The posture is STRICTER than the append-only one — the application role is denied
		// INSERT too — because runtime traffic never writes this history.
		hardened, herr := resolveGuardMetadataPosture(ctx, mdb, dia, guardRolesForBoot)
		if herr != nil {
			return herr
		}
		if err := reconcileGuardMetadataACL(ctx, mdb, dia, hardened, guardRolesForBoot); err != nil {
			return err
		}
		// AND VERIFIED FROM THE APPLICATION POOL, here as well as before serving.
		//
		// The reconcile is a name-targeted REVOKE and cannot strip a privilege held through a
		// group role or through PUBLIC; only has_table_privilege asked AS the application role
		// sees those. Verifying once, at the end of Open, was too late for everything this
		// rollout is about to write — so the same check runs here, and the boot stops before a
		// single stream is read if the boundary is not in place.
		//
		// It runs on `db`, the application pool, while this callback holds the migration lock on
		// the owner pool. Those are different pools by construction in the split topology, and in
		// the single-role topology `hardened` is false and this returns immediately.
		if err := verifyGuardMetadataACL(ctx, db, dia, hardened); err != nil {
			return err
		}
		// THE APPEND-ONLY GUARD ROLLOUT. Here, and not earlier, because every relation it
		// targets has just been created or confirmed: a unit cannot lock a relation that does
		// not exist. And not later, because it must run inside this lock, on this connection,
		// which is the session that holds olivares.migrate.v1.
		//
		// It stays a SEPARATE leg from the append-only ACL reconcile below, on purpose: a row
		// trigger cannot observe TRUNCATE, so the receipt of a trigger unit does not and cannot
		// attest an ACL. Whatever this reports about guards says nothing about privileges.
		if _, err := runAppendOnlyGuardUnits(ctx, mdb, dia, reg.appendOnlyTables(),
			observerDSN(cfg), hardened, guardReconcileSession); err != nil {
			return fmt.Errorf("sqlstore: append-only guard units: %w", err)
		}
		// Re-assert the append-only ACL for the effective application role, on every
		// boot, inside this same lock. The revoke is otherwise emitted only by
		// table-creation DDL, and every creation path above is one-shot — so on an
		// already-migrated database (i.e. every existing deployment) aiming the
		// revoke at the right role would otherwise change nothing at all. It runs on
		// the OWNER pool because only a table's owner may administer its ACL, and it
		// runs LAST so the tables it covers already exist.
		// AND THE GUARDS ON THE RELATIONS THAT RECORD ONE-WAY DECISIONS, which the
		// creation DDL cannot reach on a database that already exists. It runs BEFORE the
		// ACL reconcile below so the tables it guards are discovered by the same catalog
		// sweep and admitted to the scope inventory in this same boot, rather than a boot
		// later.
		if err := reconcileRolloutEvidenceGuards(ctx, mdb, dia); err != nil {
			return err
		}
		if err := reconcileAppendOnlyACL(ctx, mdb, dia, reg.appendOnlyTables()); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return closeOnErr(db, err)
	}
	// Directory reconciliation is the first boot step that reads engine tables
	// through the long-lived runtime pools. Verify their schema prerequisite before
	// those reads so a missing USAGE grant keeps its typed, actionable attribution.
	// The readiness checks below repeat this at the serving boundary to catch drift
	// that occurs after reconciliation.
	if cfg.Engine == store.EnginePostgres {
		if err := checkSchemaAccess(ctx, db); err != nil {
			return closeOnErr(db, err)
		}
		if adminOpened {
			if err := checkSchemaAccess(ctx, adminDB); err != nil {
				return closeOnErr(db, fmt.Errorf("admin pool: %w", err))
			}
		}
	}
	// Authored module migrations above require SQLite's privileged empty pin.
	// Once every migration/guard reconciliation has finished, publish the normal
	// inter-transaction SYSTEM baseline separately. A failed epoch backfill then
	// rolls back to SYSTEM without constraining cross-tenant migration work.
	if err := restoreDirectorySystemBaseline(ctx, db, dia); err != nil {
		return closeOnErr(db, err)
	}

	// Epoch data is reconciled outside the owner/migration transaction on
	// purpose. The writer transaction must belong to the application role; only
	// PostgreSQL enumeration crosses to the read-only BYPASSRLS admin pool.
	directoryBoot, err := reconcileDirectoryEpochs(
		ctx, db, adminDB, dia, adminRoleForBoot,
	)
	if err != nil {
		return closeOnErr(db, err)
	}
	// Authorization generation has its own fact and backfill. It intentionally
	// contributes no readiness bit in this cut: without PostgreSQL admin
	// enumeration the returned coverage is incomplete, and no global generation
	// claim is surfaced. Per-tenant absence remains UNKNOWN at the reader.
	if _, err := reconcileAuthorizationEpochs(
		ctx, db, adminDB, dia, adminRoleForBoot,
	); err != nil {
		return closeOnErr(db, err)
	}
	directoryStatus := directoryStatusFromBoot(
		cfg.Engine, directoryWriterHardened, directoryBoot,
	)

	if cfg.AuditSpoolMaxBytes > 0 {
		// Recompute on every budgeted boot rather than backfilling in the migration:
		// config toggles, in-place upgrades and DR restores can all carry a zero or
		// stale bookkeeping row. The ledger remains the source of truth, so this
		// exact logical-byte sum makes the incremental counter drift-proof.
		//
		// The sum is a genuinely cross-tenant READ, so on Postgres it runs on the
		// BYPASSRLS admin pool: FORCE RLS binds audit_events to a per-transaction
		// tenant and an unbound session RAISES (pgTenantGuard uses current_setting
		// without missing_ok — never a silent zero). The WRITE stays on the app
		// pool: the admin role is deliberately read-only (01-app-role.sql grants
		// SELECT only), and audit_spool_usage carries no RLS. On SQLite both pools
		// are the same connection and the whole recompute is one statement.
		if err := recomputeAuditSpoolUsage(ctx, db, adminDB, dia); err != nil {
			if cfg.Engine == store.EnginePostgres && !adminOpened {
				err = fmt.Errorf("%w (the audit spool budget needs a cross-tenant read of audit_events, which FORCE RLS blocks on the app role: configure AdminDSN — see deploy/postgres/01-app-role.sql)", err)
			}
			return closeOnErr(db, fmt.Errorf("sqlstore: recompute audit spool usage: %w", err))
		}
	}

	if err := runSelfTest(ctx, db, dia, reg.tenantTables()); err != nil {
		return closeOnErr(db, err)
	}
	if err := runSchemaInvariantSelfTest(
		ctx,
		db,
		dia,
		posture,
		reg.schemaInvariants(cfg.Engine),
		reg.invariantBoundaryTables(cfg.Engine),
		ownerDB != db,
	); err != nil {
		return closeOnErr(db, err)
	}

	// Read the append-only ACL back from the APPLICATION pool and refuse to serve if
	// that role can still mutate or wipe evidence — or cannot append it. The reconcile
	// above just executed the revoke, so a privilege that is still present came from
	// somewhere else — a direct grant, a group role, or PUBLIC — and only
	// has_table_privilege sees those.
	//
	// Unlike the two existing privilege checks this one is NOT gated on the owner/app
	// split: single-role is the default topology, it is where the application role
	// owns the tables, and it is therefore exactly where an open TRUNCATE matters.
	//
	// The skip is keyed on the APP role being a SUPERUSER, not on AllowPrivilegedRole.
	// The two are different questions and conflating them would silence an enforceable
	// guard: that flag is often set to permit a privileged OWNER or ADMIN pool while
	// the application role stays least-privilege, and BYPASSRLS — despite the name —
	// confers no table privilege at all. A BYPASSRLS role's self-revoke takes effect
	// exactly like anyone else's (measured). Only a superuser is exempt from ACLs
	// altogether, which makes the question unanswerable rather than merely awkward.
	if cfg.Engine == store.EnginePostgres {
		// USAGE on the engine's schema is the prerequisite every table privilege
		// silently assumes; without it the checks below would attest a boundary the
		// role cannot even reach.
		if err := checkSchemaAccess(ctx, db); err != nil {
			return closeOnErr(db, err)
		}
		// The admin pool is a long-lived runtime connection that runs UNQUALIFIED
		// cross-tenant reads, so it needs the same prerequisite. Without this, boot
		// returned the "ready store" its contract promises and the first org listing
		// failed with `relation "orgs" does not exist`.
		if adminOpened {
			if err := checkSchemaAccess(ctx, adminDB); err != nil {
				return closeOnErr(db, fmt.Errorf("admin pool: %w", err))
			}
		}
		if posture.Superuser {
			slog.Warn("append-only ACL enforcement is INEFFECTIVE for this connection: the application role is a PostgreSQL superuser, which bypasses table ACLs entirely, so the engine can neither revoke nor verify the append-only boundary. Point --dsn at a NOSUPERUSER role to get it back",
				"role", posture.Role)
		} else if err := verifyAppendOnlyACL(ctx, db, dia, reg.appendOnlyTables()); err != nil {
			return closeOnErr(db, err)
		}
		// And the guard control plane's own posture, which is STRICTER and therefore cannot be
		// folded into the check above: those three relations must deny the application role
		// INSERT as well, because the history of which schema changes were authorized is not
		// something runtime traffic appends to. The check above demands INSERT of every
		// REGISTERED append-only table, so registering these would require exactly what this
		// forbids.
		if !posture.Superuser {
			// The posture is RE-DERIVED here rather than carried over from the reconcile inside
			// the migration lock, and the extra catalog read is the point: this is the call that
			// STATES the boundary holds, so the membership closure it states it over must be the
			// one that exists at the moment of the claim. A membership granted between the two
			// would otherwise be verified away by a value computed before it existed.
			hardened, herr := resolveGuardMetadataPosture(ctx, db, dia, guardRolesForBoot)
			if herr != nil {
				return closeOnErr(db, herr)
			}
			if err := verifyGuardMetadataACL(ctx, db, dia, hardened); err != nil {
				return closeOnErr(db, err)
			}
		}
	}

	// AND THE CONTROL PLANE'S OWN OBJECTS, RE-ASSERTED FROM THE APPLICATION POOL BEFORE THE
	// STORE IS HANDED OUT.
	//
	// The ACL check above states WHO may write those three relations. It states nothing about
	// whether their immutability guards still exist, whether their shape still holds the
	// uniquenesses the ledger's ordering depends on, or whether bootstrap receipts, inventory
	// and gate still form one edition state this binary can have written — and every reading of
	// this history assumes all three.
	// Verified inside the migration lock and never again, they were true at that instant and
	// merely assumed at the one that matters: the rollout releases the lock, and a DROP TRIGGER,
	// an ALTER TABLE ... DISABLE TRIGGER or a DROP INDEX committed between the release and this
	// return would be carried into service by a boot that reported success.
	//
	// The claim this makes is the narrow one it can support, and no wider: nothing outside a
	// held lock can PREVENT that drift, so this does not. What it does is refuse to serve a
	// store whose control plane had ALREADY drifted by the time boot finished — which is the
	// difference between a window and an unbounded one.
	//
	// It runs on the APPLICATION pool, which is the connection this process will actually use,
	// and on BOTH engines, because the manifest, the receipts and the shape exist on both.
	//
	// THE HOOK IS THE ONLY WAY TO TEST THE WINDOW, and it is nil in production. What this check
	// exists for is drift committed AFTER the rollout released the migration lock and BEFORE
	// this return — and a regression that plants the drift before boot proves nothing, because
	// the verification INSIDE the rollout would refuse it first and the test would stay green
	// with this call deleted. Reproducing the window without a seam means racing the lock
	// release, which is not a regression but a coin toss.
	if guardPreServeTestHook != nil {
		guardPreServeTestHook()
	}
	if _, err := verifyGuardEditionHistory(ctx, db, dia, guardManifestForBoot); err != nil {
		return closeOnErr(db, fmt.Errorf("sqlstore: the guard control plane no longer matches its own attribution at the end of boot; its receipts, inventory and gate do not form one supported edition history: %w", err))
	}

	// AND THE DENY-CLOSED FENCE, WHICH IS THE ONLY PIECE THAT ACTS BETWEEN TWO BOOTS.
	//
	// Everything above answers "is this right NOW". The fence is what makes the DDL that
	// would break it fail inside the session that attempts it, at 03:00, with nobody
	// watching. This engine cannot install it — CREATE EVENT TRIGGER is superuser-only and
	// every role here is NOSUPERUSER — so what boot can do, and does, is say which of four
	// things is true about it and refuse when it was installed and then changed.
	//
	// It runs on the APPLICATION pool for the same reason the checks above do: the
	// rewritability question is about THAT role, and asking it as anyone else would answer
	// about the wrong subject.
	if err := runGuardEventFenceCheck(ctx, db, dia, cfg.GuardEventFence, posture.Role); err != nil {
		return closeOnErr(db, err)
	}

	// In the owner/app split the application pool is a NON-owner role that relies on
	// DML granted by the owner (ALTER DEFAULT PRIVILEGES, set by `olivares db init`).
	// runSelfTest only reads catalog metadata, so it would not notice a missing grant
	// — the failure would surface at the first tenant query instead. Probe the app
	// role's privileges now and refuse to start with a precise message (docs/SECURITY-HARDENING.md:
	// never a silent gap). No-op in the single-role path (ownerDB == db).
	if ownerDB != db {
		if err := checkAppTablePrivileges(ctx, db, reg.mutableTenantTables()); err != nil {
			return closeOnErr(db, err)
		}
	}

	// The leadership elector: alwaysLeader for SQLite/single-node (nothing to elect),
	// the Postgres session-advisory-lock elector otherwise. Constructing it opens the
	// dedicated lock pool; close it on any later boot error.
	el, err := newElector(cfg, nil)
	if err != nil {
		return closeOnErr(db, err)
	}

	// Resolve the metadata-commitment WRITE rule before the store is handed out, so
	// every writer in this process agrees and the decision is stated once.
	blindMeta, err := resolveBlindingMode(ctx, db, dia, cfg.AuditMetaBlinding)
	if err != nil {
		return closeOnErr(db, err)
	}

	bootOK = true
	return &sqlStore{
		engine: cfg.Engine, db: db, adminDB: adminDB, dia: dia, clock: clock,
		debug: cfg.Debug, reg: reg, signEvent: cfg.SignEvent,
		spoolMaxBytes: cfg.AuditSpoolMaxBytes, spoolOnFull: cfg.AuditSpoolOnFull,
		blindMeta:           blindMeta,
		directoryStatus:     directoryStatus,
		directoryGuardRoles: guardRolesForBoot,
		directoryAdminRole:  adminRoleForBoot,
		elector:             el,
	}, nil
}

// recomputeAuditSpoolUsage replaces the mutable counter with the exact logical
// bytes represented by the values stored in audit_events. Text lengths are byte
// lengths on both engines; only the nullable payload_hash and sig columns need
// COALESCE, matching the dialect DDL.
//
// Pool split (Postgres): the cross-tenant SUM must run on the BYPASSRLS admin
// pool, but that role is deliberately read-only, so the counter UPDATE runs on
// the app pool — inside a transaction that FIRST locks the counter row. Every
// appender's budget guard takes the same row lock (lockSpoolUsage FOR UPDATE)
// before inserting, so while the baseline SUM runs no increment can commit:
// a rolling-restart recompute on a passive node can never overwrite an active
// node's in-flight increments with a stale snapshot. An appender that already
// passed its guard when we ask for the lock still holds the row lock, so we
// wait for it; once we hold the lock, uncommitted inserts are invisible to the
// SUM and their increments apply on top of the recomputed baseline — exact
// either way.
func recomputeAuditSpoolUsage(ctx context.Context, db, adminDB *sql.DB, dia dialect.Dialect) error {
	var byteExpr string
	switch dia.Name() {
	case store.EnginePostgres:
		byteExpr = `octet_length(id) + octet_length(tenant_id) + 8 + octet_length(occurred_at) +
octet_length(actor) + octet_length(actor_kind) + octet_length(action) +
octet_length(target_kind) + octet_length(target_id) + octet_length(meta) +
COALESCE(octet_length(meta_blind), 0) +
COALESCE(octet_length(payload_hash), 0) + octet_length(prev_hash) +
octet_length(hash) + COALESCE(octet_length(sig), 0)`
	case store.EngineSQLite:
		byteExpr = `length(CAST(id AS BLOB)) + length(CAST(tenant_id AS BLOB)) + 8 +
length(CAST(occurred_at AS BLOB)) + length(CAST(actor AS BLOB)) +
length(CAST(actor_kind AS BLOB)) + length(CAST(action AS BLOB)) +
length(CAST(target_kind AS BLOB)) + length(CAST(target_id AS BLOB)) +
length(CAST(meta AS BLOB)) + COALESCE(length(meta_blind), 0) +
COALESCE(length(payload_hash), 0) +
length(prev_hash) + length(hash) + COALESCE(length(sig), 0)`
	default:
		return fmt.Errorf("unsupported engine %q", dia.Name())
	}
	sumQuery := "SELECT COALESCE(SUM(" + byteExpr + "), 0) FROM " + auditTable

	if dia.Name() != store.EnginePostgres {
		// Single pool, single writer: one atomic statement.
		// #nosec G202 -- auditSpoolUsageTable and the summed byteExpr are internal dialect constants, no user input
		_, err := db.ExecContext(ctx, "UPDATE "+auditSpoolUsageTable+
			" SET bytes = ("+sumQuery+") WHERE id = 1")
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var locked int64
	if err := tx.QueryRowContext(ctx,
		"SELECT bytes FROM "+auditSpoolUsageTable+" WHERE id = 1 FOR UPDATE").Scan(&locked); err != nil {
		return err
	}
	var total int64
	if adminDB == db {
		// Same role, same pool: run the SUM inside the SAME transaction. Taking a
		// second pooled connection here was not only pointless — with MaxConns=1 it
		// deadlocked boot: the transaction held the pool's only connection while
		// this read waited forever for a free one. Inside the transaction the read
		// either succeeds or raises the FORCE-RLS error immediately, which Open
		// wraps with the configure-AdminDSN remediation.
		if err := tx.QueryRowContext(ctx, sumQuery).Scan(&total); err != nil {
			return err
		}
	} else if err := adminDB.QueryRowContext(ctx, sumQuery).Scan(&total); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE "+auditSpoolUsageTable+" SET bytes = $1 WHERE id = 1", total); err != nil {
		return err
	}
	return tx.Commit()
}

// AuditSpoolStatus implements store.AuditSpoolStatuser: the operator-visible
// state of the configured spool budget. UsedBytes is the exact incremental
// counter the writer's guard compares — no measurement, no hysteresis. The
// pending-drops aggregate is a genuinely cross-tenant read of the RLS-guarded
// audit_spool_gaps table, so on Postgres it runs on the admin pool; without a
// dedicated admin pool the read errors and the observability edge reports the
// status unavailable rather than a silent zero (its callers log and omit).
func (s *sqlStore) AuditSpoolStatus(ctx context.Context) (store.AuditSpoolStatus, bool, error) {
	if s.spoolMaxBytes <= 0 {
		return store.AuditSpoolStatus{}, false, nil
	}
	var used int64
	if err := s.db.QueryRowContext(ctx,
		"SELECT bytes FROM "+auditSpoolUsageTable+" WHERE id = 1").Scan(&used); err != nil {
		return store.AuditSpoolStatus{}, true, fmt.Errorf("read audit spool usage: %w", err)
	}
	var pendingTenants int
	var pendingDrops int64
	if err := s.adminDB.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(dropped), 0) FROM "+auditSpoolGapsTable,
	).Scan(&pendingTenants, &pendingDrops); err != nil {
		return store.AuditSpoolStatus{}, true, fmt.Errorf("read audit spool pending drops: %w", err)
	}
	// Report the EFFECTIVE mode: the guard treats anything but an explicit
	// degrade as block, so an unset mode must surface as block.
	mode := s.spoolOnFull
	if mode != store.AuditSpoolDegrade {
		mode = store.AuditSpoolBlock
	}
	return store.AuditSpoolStatus{
		MaxBytes:           s.spoolMaxBytes,
		OnFull:             mode,
		UsedBytes:          used,
		Engaged:            used >= s.spoolMaxBytes,
		PendingDropTenants: pendingTenants,
		PendingDrops:       pendingDrops,
	}, true, nil
}

// Compile-time proof the store exposes the observability capability the
// console health summary and the residency decorator forward.
var _ store.AuditSpoolStatuser = (*sqlStore)(nil)

// newElector builds the leadership elector for the configured engine: a Postgres
// session-advisory-lock elector that opens its own dedicated lock pool, or the
// no-op always-leader for SQLite/single-node. log may be nil (defaults applied).
// resolveBlindingMode turns the configured metadata-commitment write rule into the
// boolean every audit writer uses.
//
//	"on"    → blind, and RECORD the actuation if it is not already recorded
//	"off"   → do not blind, and record nothing
//	empty   → follow what this ledger has already been actuated to
//
// The empty value follows a DURABLE record rather than re-deriving anything from
// the ledger's contents, and both halves of that matter.
//
// Re-deriving would make a deploy able to change the rule a live chain is being
// written under — the one thing this setting exists to prevent. A node that asked
// "does the ledger hold a blinded row?" and got a stale or differently-scoped
// answer would silently switch rules mid-chain; the recorded decision cannot
// drift, because it is written once and never restated (see
// reconcileAuditBlindingState for the seed).
//
// Asking the ledger is also not merely inelegant, it is not available: the
// question is cross-tenant and audit_events carries FORCE row-level security
// whose policy calls current_setting('app.tenant_id') without missing_ok, so from
// the application pool with no tenant bound it RAISES rather than answering. That
// is not hypothetical — it is the same failure, with the same SQLSTATE 42704, that
// once broke every Postgres boot through reconcileCoreData (see the comment
// there). An admin pool is not a way out either: adminDB EQUALS db whenever no
// separate AdminDSN is configured, which is the ordinary single-DSN deployment.
//
// Turning it on is irreversible for the rows it seals: a node still running a
// binary without blinding support reports a blinded row as a hash mismatch — a
// legitimate history denounced as forged. Recording the act is what makes that
// door visible after the fact instead of being inferred from the data.
//
// The decision is per process and needs no consensus: rows carry their own rule,
// so nodes that disagree during a rollout still produce one verifiable chain.
func resolveBlindingMode(ctx context.Context, db *sql.DB, dia dialect.Dialect, mode store.AuditBlindingMode) (bool, error) {
	switch mode {
	case store.AuditBlindingOff:
		// Refuse rather than quietly weaken. Once a ledger has been actuated, "off"
		// asks it to go back to sealing metadata under a rule that a holder of one
		// exported line can confirm by guessing — reopening, for every new record,
		// the oracle the actuation closed. The resulting chain still verifies end to
		// end, so nothing downstream would look wrong; a config that drifted back to
		// "off" on one node would silently produce a stream of weaker records.
		//
		// Failing at BOOT is the right moment: it is loud, it is before any evidence
		// is written, and it does not touch the mid-rollout node running an older
		// binary, which never reads this setting at all.
		_, actuated, err := blindingState(ctx, db, dia)
		if err != nil {
			return false, err
		}
		if actuated {
			return false, fmt.Errorf("sqlstore: refusing to start: audit metadata blinding is set to %q, but this ledger has already been actuated onto the blinded rule. Sealing new events under the legacy rule again would make each new metadata commitment a deterministic function of that record's metadata alone, so a holder of one exported line could confirm a guessed value — the exact exposure the actuation closed, reopened for every record written from now on. Rows already sealed are unaffected and keep verifying. Remove the setting to follow the ledger, or set it to %q",
				store.AuditBlindingOff, store.AuditBlindingOn)
		}
		return false, nil
	case store.AuditBlindingOn:
		if err := recordBlindingActuation(ctx, db, dia); err != nil {
			return false, err
		}
		return true, nil
	case store.AuditBlindingAuto:
	default:
		return false, fmt.Errorf("sqlstore: unknown audit meta blinding mode %q (want %q, %q or empty)",
			mode, store.AuditBlindingOn, store.AuditBlindingOff)
	}
	defaultBlinded, _, err := blindingState(ctx, db, dia)
	if err != nil {
		return false, err
	}
	if defaultBlinded {
		// This ledger's rule IS the blinded one, so a process following the default is
		// about to write blinded rows — which is the act, and it is recorded now
		// rather than inferred later.
		if err := recordBlindingActuation(ctx, db, dia); err != nil {
			return false, err
		}
		return true, nil
	}
	slog.Warn("audit: metadata blinding is OFF for new events because this ledger has not been actuated onto the blinded rule: it already held events sealed under the previous one when the blind column was added. A node still running an older binary would report a blinded row as a hash mismatch, so upgrade every node first, then set OLIVARES_AUDIT_META_BLINDING=on — an act that is irreversible for the rows it seals. Reading always accepts both rules, and a chain that interleaves them verifies end to end.")
	return false, nil
}

// blindingState reads the ledger's two durable facts: which rule is this ledger's
// default, and whether any process has actually resolved to writing blinded rows.
// It reads global bookkeeping, never the RLS-guarded ledger.
func blindingState(ctx context.Context, db *sql.DB, dia dialect.Dialect) (defaultBlinded, actuated bool, err error) {
	var d, a int64
	q := dia.Rebind("SELECT default_blinded, actuated FROM " + dialect.AuditBlindingStateTable + " WHERE id = 1")
	if err := db.QueryRowContext(ctx, q).Scan(&d, &a); err != nil {
		return false, false, fmt.Errorf("sqlstore: resolve audit meta blinding: %w", err)
	}
	return d != 0, a != 0, nil
}

// recordBlindingActuation marks this ledger as actuated onto the blinded rule.
// It is idempotent and one-way: it never returns a ledger to the legacy rule,
// because "off" is a statement about what this PROCESS writes next, while the
// record is a statement about what this LEDGER has already been told to do — and
// rows already sealed with a blind cannot be unsealed. An operator who sets "off"
// after actuating still writes legacy rows, and reading keeps working, but the
// ledger does not forget that the door was opened.
func recordBlindingActuation(ctx context.Context, db *sql.DB, dia dialect.Dialect) error {
	// Both facts move together: actuating a ledger IS declaring that its rule is
	// the blinded one, which is what the default mode must follow from here on. If
	// only the actuation bit moved, a later boot on the default would read the old
	// rule and quietly go back to sealing unblinded records — the regression this
	// whole mechanism exists to make impossible.
	q := dia.Rebind("UPDATE " + dialect.AuditBlindingStateTable +
		" SET actuated = 1, default_blinded = 1 WHERE id = 1 AND actuated = 0")
	res, err := db.ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("sqlstore: record audit meta blinding actuation: %w", err)
	}
	if n, aerr := res.RowsAffected(); aerr == nil && n > 0 {
		slog.Warn("audit: metadata blinding ACTUATED on this ledger. Every event sealed from now on commits its metadata under the blinded rule, and that is irreversible for those rows: a node still running a binary without blinding support will report them as a hash mismatch. Reading accepts both rules, and a chain that interleaves them verifies end to end.")
	}
	return nil
}

func newElector(cfg store.Config, log *slog.Logger) (elector, error) {
	if cfg.Engine == store.EnginePostgres {
		return newPGElector(cfg, log)
	}
	return newAlwaysLeader(), nil
}

// withMigrationLock runs fn while holding a cluster-wide session-level advisory
// lock on Postgres, so concurrent node boots serialize their schema changes. The
// lock is held on a dedicated checked-out connection, and fn RUNS ON THAT SAME
// CONNECTION (residual R1): the schema work is then serialized behind the lock it
// is protected by, instead of asking the pool for a second connection it may not
// have. That closes a boot HANG on a one-connection pool and — the reason it
// matters beyond the hang — it makes session-scoped settings meaningful, because
// lock_timeout set here governs the very statements it was set for.
//
// On SQLite there is nothing to serialize across, so fn runs on the pool directly.
func withMigrationLock(ctx context.Context, db *sql.DB, dia dialect.Dialect, fn func(dialect.Execer) error) error {
	if dia.Name() != store.EnginePostgres {
		return fn(db)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sqlstore: migration lock conn: %w", err)
	}
	// acquireAttempted, not "locked": the flag governs only whether an explicit
	// unlock is worth attempting. It does NOT govern whether the session may be
	// pooled, because that question has the same answer on every path — no.
	//
	// In particular an acquisition that returned an ERROR may still hold the lock:
	// a canceled or timed-out pg_advisory_lock can be granted server-side before
	// the cancellation arrives, and the client then sees a failure for a lock it
	// actually owns. Returning that session to the pool hands the next user a lock
	// nobody believes exists.
	acquireAttempted := false
	defer func() {
		// The unlock is BOUNDED and its boolean result is CHECKED, and the session is
		// then ALWAYS retired rather than returned to the pool — on EVERY exit path,
		// including the acquisition error above.
		//
		// Bounded, because the release must still run when boot was canceled but a
		// release that hangs must not hold boot open. Checked, because
		// pg_advisory_unlock returns a BOOLEAN — false means "this session did not
		// hold that lock" — so discarding the result cannot tell a release from a
		// no-op.
		//
		// Always retired, because "one unlock returned true" does NOT prove the
		// session is clean: pg_advisory_lock is RE-ENTRANT per session, so if any
		// migration step took the same key again the count is 2, a single unlock
		// reports true, and the session would go back to the pool still holding it.
		// pgx's ResetSession does not clear advisory locks on reuse, so the next
		// user of that pooled session would inherit a stale lock count. After
		// arbitrary migration work the lock state is never KNOWN clean, and this
		// package's rule is that such a session is never pooled (see forceDiscard).
		// Retiring it is also the stronger remedy for the lock itself: ending the
		// session releases every session-level lock it held, server-side. The cost
		// is one re-dial, once per boot.
		if acquireAttempted {
			uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), migrationUnlockTimeout)
			defer cancel()
			var released bool
			if uerr := conn.QueryRowContext(uctx,
				"SELECT pg_catalog.pg_advisory_unlock(pg_catalog.hashtextextended('olivares.migrate.v1', 0))").
				Scan(&released); uerr != nil || !released {
				slog.Warn("the migration advisory lock could not be confirmed released; its session is being retired, which releases the lock server-side",
					"released", released, "err", uerr)
			}
		}
		_ = forceDiscard(conn) //nolint:errcheck // the session is retired on every path; the warning above carries any diagnosis
	}()
	acquireAttempted = true
	budget := newCoordinationBudget()
	attempts, err := acquireCoordinationLock(ctx, conn, budget, logLockAttempt)
	if err != nil {
		return err
	}
	if attempts > 1 {
		slog.Info("migration coordination lock acquired after waiting for another node",
			"attempts", attempts)
	}
	return fn(conn)
}

func clearMigrationTenantScope(ctx context.Context, db dialect.Execer, dia dialect.Dialect) error {
	if dia.Name() != store.EngineSQLite {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore: begin migration scope reset: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if err := dia.ClearTenant(ctx, tx); err != nil {
		return fmt.Errorf("sqlstore: clear migration tenant scope: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlstore: commit migration scope reset: %w", err)
	}
	return nil
}

// openDB opens the backend pool with engine-appropriate settings.
func openDB(cfg store.Config) (*sql.DB, error) {
	switch cfg.Engine {
	case store.EngineSQLite:
		return openSQLite(cfg.DSN)
	case store.EnginePostgres:
		return openPostgres(cfg)
	default:
		return nil, fmt.Errorf("sqlstore: unsupported engine %q", cfg.Engine)
	}
}

// openSQLite opens a pure-Go SQLite pool. It is pinned to a single connection:
// SQLite is single-writer, and the per-connection scope pin and the gap-free
// audit sequence both rely on serialized access.
// sqlitePragmas are passed as DSN parameters rather than executed after opening the
// pool. The driver applies them inside its own per-connection setup, so EVERY
// physical connection is configured — including a replacement dialed after
// database/sql discards one it considers broken. A post-open Exec configures only
// whichever connection happened to serve that single call, and a replacement would
// silently come up with SQLite's defaults (foreign_keys OFF, busy_timeout 0,
// journal_mode DELETE).
//
// recursive_triggers is a security guard, not a tuning knob. SQLite's REPLACE
// conflict resolution DELETES the conflicting row, and those deletes fire BEFORE
// DELETE triggers only when recursive_triggers is enabled. Every append-only table in
// this schema is enforced by exactly such a trigger, so with it off an INSERT OR
// REPLACE quietly overwrites an append-only row — a promoted evidence-policy fact,
// an audit event — and the immutability guard never runs. It bounds application
// writes; a caller with raw file access can always open its own connection, which is
// why immutability also relies on the guards themselves.
var sqlitePragmas = []string{
	"busy_timeout(5000)",
	"foreign_keys(1)",
	"journal_mode(WAL)",
	"recursive_triggers(1)",
}

func openSQLite(dsn string) (*sql.DB, error) {
	if dsn == "" {
		dsn = ":memory:"
	}
	var params strings.Builder
	sep := "?"
	if strings.ContainsRune(dsn, '?') {
		sep = "&"
	}
	for _, pragma := range sqlitePragmas {
		params.WriteString(sep)
		params.WriteString("_pragma=")
		params.WriteString(url.QueryEscape(pragma))
		sep = "&"
	}
	db, err := sql.Open("sqlite", dsn+params.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	// Force the first connection now so a bad pragma fails here, at Open, instead of
	// at the first query.
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite pragmas %v: %w", sqlitePragmas, err)
	}
	if err := restrictSQLiteFiles(dsn); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// sqliteFilePerm is the mode the store's own files are held at. Until the
// engine stated no mode at all: the database landed at whatever the SQLite
// driver's 0666 became under the process umask — 0644 on a default umask, so
// world-readable. The containing data directory is 0700 (secure.EnsureDir), which
// is what actually keeps other users out, but a governance store's own bytes
// should not depend on an inherited umask to be unreadable. WAL and SHM are
// included: they carry committed page images, so a lax mode there is the same
// exposure by another name.
const sqliteFilePerm = 0o600

// restrictSQLiteFiles narrows the database and its WAL/SHM sidecars to
// sqliteFilePerm. It is a no-op for in-memory databases and for any DSN this
// package cannot resolve to a plain path — an operator-supplied URI with its own
// VFS or query parameters is theirs, and guessing at a path there would be worse
// than leaving it alone. Files that do not exist are skipped rather than created.
func restrictSQLiteFiles(dsn string) error {
	path := sqliteFilePath(dsn)
	if path == "" {
		return nil
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if info, err := os.Stat(p); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := os.Chmod(p, sqliteFilePerm); err != nil {
			return fmt.Errorf("sqlstore: restrict %s to %#o: %w", p, sqliteFilePerm, err)
		}
	}
	return nil
}

// sqliteFilePath extracts a plain filesystem path from a SQLite DSN, or "" when
// the DSN is in-memory or carries anything this package will not interpret.
func sqliteFilePath(dsn string) string {
	if dsn == "" || dsn == ":memory:" || strings.Contains(dsn, "mode=memory") {
		return ""
	}
	path := strings.TrimPrefix(dsn, "file:")
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if path == "" || strings.HasPrefix(path, ":") {
		return ""
	}
	return path
}

// openPostgres opens the Postgres APPLICATION pool via the pgx stdlib driver. The
// DSN is the non-owner, non-superuser, NOBYPASSRLS application role; the dedicated
// cross-tenant admin pool is opened separately from AdminDSN (openAdminPool).
func openPostgres(cfg store.Config) (*sql.DB, error) {
	return openPGPinnedToEngineSchema(cfg.DSN, cfg.MaxConns)
}

// openPGPinnedToEngineSchema opens a Postgres pool whose every physical connection
// starts with search_path pinned to the engine's schema, and returns a function that
// releases the driver registration when the pool is closed.
//
// This is a correctness requirement, not tidiness. The engine writes and reads its
// tables with UNQUALIFIED names, while every guard it installs and every check it
// runs addresses dialect.EngineSchema explicitly. Left to the connection's inherited
// search_path those are not the same relation: a schema earlier in the path can
// SHADOW an engine table, and then the boot verifies a locked-down public.audit_events
// while the runtime happily writes — and truncates — an unguarded shadow of it. That
// was measured, end to end, before this pin existed.
//
// The pin is applied PER PHYSICAL CONNECTION with set_config, not shipped in the
// startup packet. pgx copies ConnConfig.RuntimeParams verbatim into the StartupMessage
// (pgconn/pgconn.go:402), and a startup packet is exactly where a connection pooler
// gets to have an opinion: PgBouncer accepts only the handful of parameters it can
// track and rejects anything else with `FATAL: unsupported startup parameter`, in
// session mode as well as transaction mode. Since the engine needs session pooling at
// most, a pooled deployment that booted fine before would stop dialing at all — and
// the obvious operator workaround, ignore_startup_parameters, makes the pooler DROP the
// parameter, which would leave the pin silently absent. This codebase already paid for
// that lesson once through the DSN (leader_pg.go, the FATAL 42704 note).
//
// set_config costs nothing by comparison: it applies to every connection the pool ever
// opens, including ones created long after boot; it overrides role- and database-level
// defaults just as a startup parameter would; and because it RETURNS the value it
// installed, the same round trip proves the pin took. That check is fail-closed on
// purpose — an unverifiable pin is worse than none, since every guard and every boot
// check addresses dialect.EngineSchema literally and would go on reporting a
// locked-down schema while the runtime wrote somewhere else.
//
// One property the startup form DOES have and this one does not, stated so nobody has
// to rediscover it: a session value is what RESET restores AWAY from, while a startup
// value is the reset target. Measured — after a reset, the session form yields
// `"$user", public` and the startup form `public`. It is not a live gap today: pgx
// v5.10.0's default ResetSession does not touch GUCs, this repository issues no
// `RESET search_path` or `DISCARD ALL`, and every new physical connection runs this
// hook before it is used. Anything that adds a reset hook, or moves to transaction
// pooling, must re-pin.
func openPGPinnedToEngineSchema(dsn string, maxConns int) (*sql.DB, error) {
	connCfg, err := pgEngineConnConfig(dsn)
	if err != nil {
		return nil, err
	}
	// stdlib.OpenDB rather than RegisterConnConfig + sql.Open: the register form
	// stashes the config — password included — in a process-global map that must be
	// released by name, and a pool that outlives its registration entry (or forgets
	// to unregister) leaks both memory and credentials. This form owns nothing global.
	// Pinned at the ValidateConnect stage as well as AfterConnect: a DSN carrying
	// target_session_attrs makes pgx run `select pg_is_in_recovery()` UNQUALIFIED before
	// AfterConnect ever fires, and the owner and admin pools authenticate as roles more
	// privileged than the application role that could define a shadow of it.
	db := stdlib.OpenDB(*pinBeforeValidate(connCfg, dialect.EngineSchema), stdlib.OptionAfterConnect(pinSearchPath))
	if maxConns > 0 {
		db.SetMaxOpenConns(maxConns)
	}
	return db, nil
}

// pgEngineConnConfig parses a DSN into the connection config every engine pool uses.
//
// It deliberately adds NOTHING to RuntimeParams. Those are startup-packet parameters,
// and a connection pooler is entitled to reject any it does not track — so a parameter
// added here would take out every pooled deployment at the dial, before a single one of
// this package's actionable errors could be produced. Session state the engine needs
// goes in pinSearchPath, which runs as an ordinary query on each new connection.
func pgEngineConnConfig(dsn string) (*pgx.ConnConfig, error) {
	connCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: parse postgres dsn: %w", err)
	}
	return connCfg, nil
}

// pinSearchPath sets search_path to the engine's schema on a freshly established
// connection and refuses the connection unless the server confirms the value it
// installed. The name is bound as a parameter, never interpolated.
//
// set_config is called SCHEMA-QUALIFIED, and that qualification is load-bearing rather
// than stylistic. This hook is the one statement that runs while the search_path is
// still whatever the connection inherited, so it is the only place in the engine where
// an unqualified name can be resolved by an attacker-controlled schema. A search_path
// that names a writable schema ahead of pg_catalog — legal, and settable per role —
// lets a same-signature `other.set_config(text, text, boolean)` return the expected
// value while changing nothing: the hook would report success and every later query
// would still resolve shadows. Measured: `actual_search_path="other, pg_catalog,
// public" current_schema="other"` with the pin reporting no error.
//
// Everything after this point is safe unqualified: once search_path is set to the
// engine schema alone, pg_catalog is implicitly searched first again, so the shadowing
// window closes with this statement.
//
// The read-back is defense in depth against something between this process and the
// server rewriting the statement; a conforming PostgreSQL always returns what it
// stored, so that branch is NOT exercised by the suite and is not claimed to be.
func pinSearchPath(ctx context.Context, conn *pgx.Conn) error {
	var got string
	if err := conn.QueryRow(ctx, "SELECT pg_catalog.set_config('search_path', $1, false)", dialect.EngineSchema).Scan(&got); err != nil {
		return fmt.Errorf("sqlstore: pin search_path to %q: %w", dialect.EngineSchema, err)
	}
	if got != dialect.EngineSchema {
		return fmt.Errorf("%w: search_path reads back as %q after pinning it to %q; something between this process and the server is rewriting it, so the engine's unqualified SQL and its schema-pinned guards would address different relations",
			store.ErrEngineSchemaUnusable, got, dialect.EngineSchema)
	}
	return nil
}

// openAdminPool opens the dedicated cross-tenant admin pool from cfg.AdminDSN
// (Postgres only) and validates its privilege. Unlike the application pool, this
// pool is SUPPOSED to bypass row-level security — that is the whole point: a
// genuinely cross-tenant System read (the org list) must see every tenant's rows,
// which FORCE RLS would otherwise filter to the cleared tenant GUC. So the boot
// guard is INVERTED here: the role MUST be able to bypass RLS (superuser or
// BYPASSRLS), and is held to least privilege (BYPASSRLS, NOT superuser) so it can
// read across tenants but not, say, alter the schema. A superuser is refused
// unless the operator opts into privileged roles. There is no fallback to
// OwnerDSN/DSN: under FORCE RLS those roles do not bypass, so they would silently
// return an empty list — exactly the failure this pool exists to remove.
func openAdminPool(ctx context.Context, dia dialect.Dialect, cfg store.Config) (*sql.DB, error) {
	// Pinned to the engine schema: this pool runs unqualified cross-tenant reads, so
	// an inherited search_path would let it read a shadow of the table the rest of
	// the engine guards.
	adb, err := openPGPinnedToEngineSchema(cfg.AdminDSN, cfg.MaxConns)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: open admin pool: %w", err)
	}
	posture, perr := dia.ConnRolePosture(ctx, adb)
	if perr != nil {
		_ = adb.Close()
		return nil, fmt.Errorf("sqlstore: admin pool role posture: %w", perr)
	}
	if !posture.RLSUnsafe() {
		_ = adb.Close()
		return nil, fmt.Errorf("sqlstore: --admin-dsn role %q is %s and cannot perform cross-tenant reads under FORCE row-level security; grant it BYPASSRLS (see deploy/postgres/01-app-role.sql)", posture.Role, posture.Why())
	}
	if posture.Superuser && !cfg.AllowPrivilegedRole {
		_ = adb.Close()
		return nil, fmt.Errorf("sqlstore: --admin-dsn role %q is a SUPERUSER (more privilege than the cross-tenant admin pool needs); provision a NOSUPERUSER BYPASSRLS role (deploy/postgres/01-app-role.sql) or set --allow-privileged-db-role to accept it", posture.Role)
	}
	return adb, nil
}

func closeOnErr(db *sql.DB, err error) (store.Store, error) {
	_ = db.Close()
	return nil, err
}

func (s *sqlStore) Engine() store.Engine { return s.engine }

func (s *sqlStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Leader returns this node's leadership elector. Never nil.
func (s *sqlStore) Leader() store.LeaderElector { return s.elector }

func (s *sqlStore) Close() error {
	// Resign leadership first (idempotent: the composition root already does this for
	// a fast handoff at graceful shutdown). This also closes the elector's dedicated
	// lock pool — releasing the advisory lock so a standby takes over immediately.
	var resignErr error
	if s.elector != nil {
		if err := s.elector.Resign(context.Background()); err != nil {
			// Round-5 audit P1: a confirmed termination failure surfaced by
			// Resign must not die here — the operator closing the store is
			// exactly who needs to know the advisory lock may outlive the
			// process.
			resignErr = fmt.Errorf("sqlstore: close: %w", err)
		}
	}
	if s.adminDB != nil && s.adminDB != s.db {
		_ = s.adminDB.Close()
	}
	return errors.Join(resignErr, s.db.Close())
}

// View runs fn in one tenant-pinned, repeatable-read, read-only snapshot. A
// multi-row reconstruction must never combine rows from commits on opposite
// sides of the callback; PostgreSQL's default READ COMMITTED would otherwise
// assign a new snapshot to each statement. SQLite already provides a stable
// transaction snapshot under its default transaction (whose tenant-binding
// implementation writes a connection-local scope table, so it must not be
// declared database-read-only). The transaction is always rolled back, so a
// View never persists; writes through the scope are also rejected early with
// ErrReadOnly.
func viewTxOptions(engine store.Engine) *sql.TxOptions {
	if engine != store.EnginePostgres {
		return nil
	}
	return &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
}

func (s *sqlStore) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if tenant.IsZero() {
		return store.ErrNoTenant
	}
	tx, err := s.db.BeginTx(ctx, viewTxOptions(s.engine))
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // View intentionally never commits
	if err := s.dia.BindTenant(ctx, tx, tenant); err != nil {
		return err
	}
	return fn(&tenantScope{s: s, tx: tx, tenant: tenant, readOnly: true})
}

// Mutate runs fn in a tenant-pinned read-write transaction, committing on
// success and rolling back on any error. Every SQL error return — BeginTx,
// BindTenant, the callback's pass-through, Commit — goes through
// wrapUnavailableErr (round-3 item 2): a connection-level failure is
// wrapped in store.ErrStoreUnavailable (cause-preserving multi-%w), everything
// else passes through untouched. Wrapping only; the transactional shape is
// unchanged.
func (s *sqlStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if tenant.IsZero() {
		return store.ErrNoTenant
	}
	// HA write-gate: a standby never writes. This is defense in depth behind
	// the /readyz load-balancer drain — an in-process background loop (the
	// checkpointer, a periodic sync) on a standby fails closed here rather than
	// forking the signed audit chain. Inert on a single-node store (the elector is
	// always active until Run arms it). AuthMutate routes through here too.
	if !s.elector.active() {
		return store.ErrNotLeader
	}
	tx, err := s.db.BeginTx(ctx, directoryWriterTxOptions(s.dia))
	if err != nil {
		return wrapUnavailableErr(err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful commit
	if err := bindDirectoryTenant(ctx, tx, s.dia, tenant); err != nil {
		return wrapUnavailableErr(err)
	}
	scope := &tenantScope{s: s, tx: tx, tenant: tenant}
	scope.directoryWriter = newDirectoryWriteTracker(tx, s.dia, tenant)
	if err := fn(scope); err != nil {
		return wrapUnavailableErr(err)
	}
	if scope.directoryWriter.poisoned != nil {
		return wrapUnavailableErr(fmt.Errorf(
			"tenant transaction poisoned by discarded directory writer error: %w",
			scope.directoryWriter.poisoned,
		))
	}
	if err := scope.directoryWriter.finish(ctx); err != nil {
		return wrapUnavailableErr(err)
	}
	return wrapUnavailableErr(tx.Commit())
}

// Custody runs fn in a tenant-pinned read-write transaction that exposes ONLY the
// tenant's evidence ledger (store.CustodyScope). Transactionally it is Mutate —
// same tenant bind, same HA write-gate (a standby must never append a checkpoint),
// same commit/rollback and the same ErrStoreUnavailable wrapping. What differs is
// the type handed to fn, and that is the point: the narrowing is structural, not a
// rule a future caller has to remember.
func (s *sqlStore) Custody(ctx context.Context, tenant model.TenantID, fn func(store.CustodyScope) error) error {
	return s.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(&custodyScope{sc: sc})
	})
}

// custodyScope narrows a tenant-pinned Scope to the evidence ledger. It holds the
// Scope rather than embedding it, so the other 30-odd repositories are not
// reachable through it even by type assertion back to store.Scope.
type custodyScope struct{ sc store.Scope }

func (c *custodyScope) Tenant() model.TenantID { return c.sc.Tenant() }

func (c *custodyScope) Org(ctx context.Context) (model.Org, error) { return c.sc.Org(ctx) }

func (c *custodyScope) Audit() store.AuditLog { return c.sc.Audit() }

// Export runs fn over the tenant's own exportable data. Transactionally it is
// Mutate — same tenant bind, same HA write-gate, same wrapping — because the
// suspension guard appends the export's audit event in this very transaction, so
// the record of the copy and the copy itself commit or roll back together.
func (s *sqlStore) Export(ctx context.Context, tenant model.TenantID, fn func(store.ExportScope) error) error {
	return s.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(&exportScope{sc: sc})
	})
}

// exportScope narrows a tenant-pinned Scope to the portability surface. It holds
// the Scope rather than embedding it, so the rest of the product is not reachable
// through it even by asserting back to store.Scope.
type exportScope struct{ sc store.Scope }

func (e *exportScope) Tenant() model.TenantID { return e.sc.Tenant() }

func (e *exportScope) Org(ctx context.Context) (model.Org, error) { return e.sc.Org(ctx) }

func (e *exportScope) Audit() store.AuditLog { return e.sc.Audit() }

func (e *exportScope) Ext(kind model.Kind) (store.GenericRepo, error) { return e.sc.Ext(kind) }

// System runs fn in a privileged cross-tenant transaction. The tenant pin is
// cleared so the System path is the deliberate, audited exception to isolation.
func (s *sqlStore) System(ctx context.Context, fn func(store.SystemScope) error) (retErr error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck // operation error is authoritative
	tx, err := conn.BeginTx(ctx, directoryWriterTxOptions(s.dia))
	if err != nil {
		return err
	}
	needsRestore := false
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackErr := tx.Rollback()
		if errors.Is(rollbackErr, sql.ErrTxDone) {
			rollbackErr = nil
		}
		var restoreErr error
		if needsRestore {
			// SQLite's scope pin is durable across transactions. A tenant Mutate
			// may be the immediately preceding commit, so rollback alone can
			// restore a real-tenant pin. Publish SYSTEM after rolling the callback
			// transaction back while retaining the same pinned pool connection.
			restoreCtx, cancel := context.WithTimeout(
				context.WithoutCancel(ctx), systemRestoreTimeout,
			)
			restoreErr = restoreDirectorySystemBaseline(restoreCtx, conn, s.dia)
			cancel()
		}
		retErr = errors.Join(retErr, rollbackErr, restoreErr)
	}()
	// SQLite must reserve its sole writer before the baseline SELECT establishes
	// a WAL snapshot; otherwise a concurrent backfill commit makes the later
	// scope-pin DELETE an unretryable read-to-write upgrade (BUSY_SNAPSHOT).
	if err := reserveSQLiteDirectoryWriter(ctx, tx, s.dia); err != nil {
		return err
	}
	if err := verifyDirectoryWriterPresentationBaseline(ctx, tx, s.dia); err != nil {
		return err
	}
	needsRestore = true
	if err := clearDirectoryTenant(ctx, tx, s.dia); err != nil {
		return err
	}
	scope := &systemScope{s: s, tx: tx}
	if err := fn(scope); err != nil {
		return err
	}
	if scope.poisoned != nil {
		return fmt.Errorf("system transaction poisoned by discarded lifecycle error: %w",
			scope.poisoned)
	}
	// System callbacks may bind any number of real tenants. The transaction
	// envelope, not each operation, owns the final restoration: persist the
	// reserved SYSTEM pin and clear the generation presentation before commit.
	// A callback error rolls the transaction back to the same pre-call baseline.
	if err := restoreSystemDirectoryBaseline(ctx, tx, s.dia); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
