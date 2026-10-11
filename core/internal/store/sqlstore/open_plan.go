// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// storePreparation owns one Open's transient state. It never escapes into the
// serving store; in particular, owner credentials and publication fences end
// with preparation on success as well as on failure.
type storePreparation struct {
	cfg         store.Config
	register    func(store.ExtensionRegistry) error
	purpose     preparePurpose
	maintenance func(*sqlStore) error
	in          publicationInputs

	dia                  dialect.Dialect
	clock                model.Clock
	localFence           *restorePublicationFence
	db, ownerDB, adminDB *sql.DB
	adminOpened          bool
	posture              dialect.RolePosture

	pub, ownedPublication *publicationAdmission
	roles                 guardRoles
	adminRole             guardRoleFact

	reg                                         *registry
	custodyRelation                             custodialRelation
	custodyRelationErr                          error
	readiness                                   store.Readiness
	guardManifest                               guardManifest
	directoryHardened, evidenceRefusedSupported bool
	directoryStatus                             store.DirectoryStatus

	el       elector
	finished bool
	result   store.Store
}

// mayCommit describes durable database effects, not connections or transient
// coordination locks. A read-only step can still fail after an earlier commit;
// preparation never promises to roll back a completed migration or maintenance.
type storeOpenStep struct {
	name      string
	mayCommit bool
	run       func(context.Context) error
}

// bootPlan is the single order for both engines and every preparation purpose.
// Engine-specific work stays in the existing adapters. The two finish steps
// stop without publishing a Store for schema-only and maintenance callers.
func (b *storePreparation) bootPlan() []storeOpenStep {
	return []storeOpenStep{
		{"prepareSchema", true, func(ctx context.Context) error {
			return b.openPrepared(ctx, b.cfg, b.register, b.purpose, b.maintenance, b.in)
		}},
		{"finishSchemaOnly", false, b.finishSchemaOnly},
		{"reconcileRuntime", true, b.reconcileRuntime},
		{"verifyReadiness", false, b.verifyReadiness},
		{"runMaintenance", true, b.runMaintenance},
		{"publishStore", true, b.publishStore},
	}
}

func (b *storePreparation) runBootPlan(ctx context.Context, plan []storeOpenStep) (store.Store, error) {
	defer b.closeBootResources()
	for _, step := range plan {
		slog.DebugContext(ctx, "store preparation step", "step", step.name, "may_commit", step.mayCommit)
		if err := step.run(ctx); err != nil {
			return nil, err
		}
		if b.finished {
			break
		}
	}
	return b.result, nil
}

func (b *storePreparation) closeBootResources() {
	if b.result == nil {
		if b.el != nil {
			_ = b.el.Resign(context.Background())
		}
		if b.db != nil {
			_ = b.db.Close()
		}
		if b.adminOpened {
			_ = b.adminDB.Close()
		}
	}
	if b.ownerDB != nil && b.ownerDB != b.db {
		_ = b.ownerDB.Close()
	}
	if b.ownedPublication != nil {
		_ = b.ownedPublication.close()
	}
	if b.localFence != nil {
		b.localFence.release()
	}
}

func (b *storePreparation) finishSchemaOnly(ctx context.Context) error {
	// THE SCHEMA-ONLY BOUNDARY. Everything past this point is the runtime stretch:
	// readiness schema access, the SQLite SYSTEM baseline, epoch/authorization/lineage
	// reconciliation, the spool recompute, the self-tests, the pre-serve verifications,
	// the maintenance callback, the elector and the blinding-mode resolution.
	//
	// `migrate apply` returns HERE and returns NOTHING. Preparation cleanup closes
	// the owner and application pools; no admin pool was opened. This call leaves
	// no connection behind and cannot hand
	// back something a caller could mistake for a ready store.
	if b.purpose == prepareSchemaOnly {
		if err := decidePublication(ctx, b.in, b.pub); err != nil {
			// A REFUSAL HERE IS NOT A ROLLBACK, and the diagnostic says so.
			//
			// The migrations above this line have already committed. The final
			// decision withholds the publication of this preparation; it does not, and
			// cannot, undo schema that is on the destination. The ratified contract
			// states that limit, and an operator reading "refused" about a destination
			// that has in fact been migrated needs the same sentence. Nothing about the
			// decision itself changes: the error, its sentinels and the closing of
			// every unpublished resource are exactly as before.
			return fmt.Errorf("%w; the schema this call already applied remains on the destination — the refusal withholds publication and does not roll it back", err)
		}
		b.finished = true
		return nil
	}
	return nil
}

func (b *storePreparation) reconcileRuntime(ctx context.Context) error {
	// Directory reconciliation is the first boot step that reads engine tables
	// through the long-lived runtime pools. Verify their schema prerequisite before
	// those reads so a missing USAGE grant keeps its typed, actionable attribution.
	// The readiness checks below repeat this at the serving boundary to catch drift
	// that occurs after reconciliation.
	if b.cfg.Engine == store.EnginePostgres {
		if err := checkSchemaAccess(ctx, b.db); err != nil {
			return err
		}
		if b.adminOpened {
			if err := checkSchemaAccess(ctx, b.adminDB); err != nil {
				return fmt.Errorf("admin pool: %w", err)
			}
		}
	}
	// Authored module migrations above require SQLite's privileged empty pin.
	// Once every migration/guard reconciliation has finished, publish the normal
	// inter-transaction SYSTEM baseline separately. A failed epoch backfill then
	// rolls back to SYSTEM without constraining cross-tenant migration work.
	if err := restoreDirectorySystemBaseline(ctx, b.db, b.dia); err != nil {
		return err
	}

	// Epoch data is reconciled outside the owner/migration transaction on
	// purpose. The writer transaction must belong to the application role; only
	// PostgreSQL enumeration crosses to the read-only BYPASSRLS admin pool.
	directoryBoot, err := reconcileDirectoryEpochs(
		ctx, b.db, b.adminDB, b.dia, b.adminRole,
		directoryReconcileOptions{roles: b.roles, maintenance: b.maintenance != nil},
	)
	if err != nil {
		return err
	}
	// Authorization generation has its own fact and backfill. It intentionally
	// contributes no readiness bit in this cut: without PostgreSQL admin
	// enumeration the returned coverage is incomplete, and no global generation
	// claim is surfaced. Per-tenant absence remains UNKNOWN at the reader.
	if _, err := reconcileAuthorizationEpochs(
		ctx, b.db, b.adminDB, b.dia, b.adminRole,
	); err != nil {
		return err
	}
	if err := reconcileLineageEpochs(ctx, b.db, b.adminDB, b.dia, b.adminRole); err != nil {
		return err
	}
	b.directoryStatus = directoryStatusFromBoot(
		b.cfg.Engine, b.directoryHardened, directoryBoot,
	)

	if b.cfg.AuditSpoolMaxBytes > 0 {
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
		if err := recomputeAuditSpoolUsage(ctx, b.db, b.adminDB, b.dia); err != nil {
			if b.cfg.Engine == store.EnginePostgres && !b.adminOpened {
				err = fmt.Errorf("%w (the audit spool budget needs a cross-tenant read of audit_events, which FORCE RLS blocks on the app role: configure AdminDSN — see deploy/postgres/01-app-role.sql)", err)
			}
			return fmt.Errorf("sqlstore: recompute audit spool usage: %w", err)
		}
	}
	return nil
}

func (b *storePreparation) verifyReadiness(ctx context.Context) error {
	if err := runSelfTest(ctx, b.db, b.dia, b.reg.tenantTables()); err != nil {
		return err
	}
	if err := runSchemaInvariantSelfTest(
		ctx,
		b.db,
		b.dia,
		b.posture,
		b.reg.schemaInvariants(b.cfg.Engine),
		b.reg.invariantBoundaryTables(b.cfg.Engine),
		b.ownerDB != b.db,
	); err != nil {
		return err
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
	if b.cfg.Engine == store.EnginePostgres {
		// USAGE on the engine's schema is the prerequisite every table privilege
		// silently assumes; without it the checks below would attest a boundary the
		// role cannot even reach.
		if err := checkSchemaAccess(ctx, b.db); err != nil {
			return err
		}
		// The admin pool is a long-lived runtime connection that runs UNQUALIFIED
		// cross-tenant reads, so it needs the same prerequisite. Without this, boot
		// returned the "ready store" its contract promises and the first org listing
		// failed with `relation "orgs" does not exist`.
		if b.adminOpened {
			if err := checkSchemaAccess(ctx, b.adminDB); err != nil {
				return fmt.Errorf("admin pool: %w", err)
			}
		}
		if b.posture.Superuser {
			slog.Warn("append-only ACL enforcement is INEFFECTIVE for this connection: the application role is a PostgreSQL superuser, which bypasses table ACLs entirely, so the engine can neither revoke nor verify the append-only boundary. Point --dsn at a NOSUPERUSER role to get it back",
				"role", b.posture.Role)
		} else if err := verifyAppendOnlyACL(ctx, b.db, b.dia, append(b.reg.appendOnlyTables(), dialect.AuditTreeTable)); err != nil {
			return err
		}
		// And the guard control plane's own posture, which is STRICTER and therefore cannot be
		// folded into the check above: those three relations must deny the application role
		// INSERT as well, because the history of which schema changes were authorized is not
		// something runtime traffic appends to. The check above demands INSERT of every
		// REGISTERED append-only table, so registering these would require exactly what this
		// forbids.
		if !b.posture.Superuser {
			// The posture is RE-DERIVED here rather than carried over from the reconcile inside
			// the migration lock, and the extra catalog read is the point: this is the call that
			// STATES the boundary holds, so the membership closure it states it over must be the
			// one that exists at the moment of the claim. A membership granted between the two
			// would otherwise be verified away by a value computed before it existed.
			hardened, herr := resolveGuardMetadataPosture(ctx, b.db, b.dia, b.roles)
			if herr != nil {
				return herr
			}
			if err := verifyGuardMetadataACL(ctx, b.db, b.dia, hardened); err != nil {
				return err
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
	// The audit tree is outside the historical manifest, so verify its guard
	// separately on the application pool at the same serving boundary.
	if err := verifyAuditTreeGuard(ctx, b.db, b.dia, false); err != nil {
		return err
	}
	if _, err := verifyGuardEditionHistory(ctx, b.db, b.dia, b.guardManifest); err != nil {
		return fmt.Errorf("sqlstore: the guard control plane no longer matches its own attribution at the end of boot; its receipts, inventory and gate do not form one supported edition history: %w", err)
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
	if err := runGuardEventFenceCheck(ctx, b.db, b.dia, b.cfg.GuardEventFence, b.posture.Role); err != nil {
		return err
	}

	// In the owner/app split the application pool is a NON-owner role that relies on
	// DML granted by the owner (ALTER DEFAULT PRIVILEGES, set by `olivares db init`).
	// runSelfTest only reads catalog metadata, so it would not notice a missing grant
	// — the failure would surface at the first tenant query instead. Probe the app
	// role's privileges now and refuse to start with a precise message (docs/SECURITY-HARDENING.md:
	// never a silent gap). No-op in the single-role path (ownerDB == db).
	if b.ownerDB != b.db {
		if err := checkAppTablePrivileges(ctx, b.db, b.reg.mutableTenantTables()); err != nil {
			return err
		}
	}
	return nil
}

func (b *storePreparation) runMaintenance(ctx context.Context) error {
	if b.maintenance != nil {
		if err := b.pub.requireMutating(ctx); err != nil {
			return err
		}
		err := b.maintenance(&sqlStore{engine: b.cfg.Engine, db: b.db, adminDB: b.adminDB, dia: b.dia, clock: b.clock, reg: b.reg,
			directoryStatus: b.directoryStatus, directoryGuardRoles: b.roles, directoryAdminRole: b.adminRole})
		if err != nil {
			return err
		}
		if err := decidePublication(ctx, b.in, b.pub); err != nil {
			// Same limit as the schema-only path above: the callback's cutover has
			// already committed, and this refusal withholds the result rather than
			// reversing it.
			return fmt.Errorf("%w; the maintenance this call already completed remains on the destination — the refusal withholds its result and does not roll it back", err)
		}
		b.finished = true
		return nil
	}
	return nil
}

func (b *storePreparation) publishStore(ctx context.Context) error {
	// The leadership elector: alwaysLeader for SQLite/single-node (nothing to elect),
	// the Postgres session-advisory-lock elector otherwise. Constructing it opens the
	// dedicated lock pool; close it on any later boot error.
	var err error
	b.el, err = newElector(b.cfg, nil)
	if err != nil {
		return err
	}

	// Resolve the metadata-commitment WRITE rule before the store is handed out, so
	// every writer in this process agrees and the decision is stated once.
	blindMeta, err := resolveBlindingMode(ctx, b.db, b.dia, b.cfg.AuditMetaBlinding)
	if err != nil {
		return err
	}

	if err := decidePublication(ctx, b.in, b.pub); err != nil {
		return err
	}

	b.result = &sqlStore{
		engine: b.cfg.Engine, db: b.db, adminDB: b.adminDB, dia: b.dia, clock: b.clock,
		debug: b.cfg.Debug, reg: b.reg, signEvent: b.cfg.SignEvent,
		spoolMaxBytes: b.cfg.AuditSpoolMaxBytes, spoolOnFull: b.cfg.AuditSpoolOnFull,
		blindMeta:           blindMeta,
		directoryStatus:     b.directoryStatus,
		directoryGuardRoles: b.roles,
		directoryAdminRole:  b.adminRole,
		elector:             b.el,
		pgExecMode:          observePGExecMode(b.cfg),

		custodyRelation:          b.custodyRelation,
		custodyRelationErr:       b.custodyRelationErr,
		evidenceRefusedSupported: b.evidenceRefusedSupported,
		readiness:                b.readiness,
	}
	return nil
}
