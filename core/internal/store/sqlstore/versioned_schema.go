// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	coreAuditSchemaMigrationVersion    = 23
	coreAuditTreeMigrationVersion      = 24
	coreWorkspaceTreeMigrationVersion  = 25
	coreAuditTreeGuardMigrationVersion = 26
	coreUserGroupWorkspaceVersion      = 27
)

// The deployed prefix keeps its guard-edition barrier. Versions appended here
// run after the per-boot checks that refuse damage to already-tracked relations.
func historicalCoreMigrations(plan []migrate.Migration) []migrate.Migration {
	for i, m := range plan {
		if m.Version > coreOSAccountMigrationVersion {
			return plan[:i]
		}
	}
	return plan
}

// These forward-only migrations adopt the schema previously maintained by
// unversioned boot reconciliation. Later changes require a new migration.
func coreDescriptorSchemaMigration(dia dialect.Dialect) migrate.Migration {
	return migrate.Migration{Version: 21, Name: "descriptor_schema", Exec: func(ctx context.Context, tx *sql.Tx) error {
		if err := reconcileColumnsTx(ctx, tx, dia, coreDescriptorsV21()); err != nil {
			return err
		}
		return reconcileOSAccountReservations(ctx, tx)
	}}
}

func coreFederationDataMigration(dia dialect.Dialect) migrate.Migration {
	return migrate.Migration{Version: 22, Name: "federation_alias_data", Exec: func(ctx context.Context, tx *sql.Tx) error {
		return reconcileCoreDataTx(ctx, tx, dia)
	}}
}

func coreAuditSchemaMigration(dia dialect.Dialect) migrate.Migration {
	return migrate.Migration{Version: coreAuditSchemaMigrationVersion, Name: "audit_blinding_schema", Exec: func(ctx context.Context, tx *sql.Tx) error {
		return reconcileAuditLedger(ctx, tx, dia)
	}}
}

// coreAuditTreeMigration adds the append-only table of RFC 6962 tree hashes beside
// the audit ledger. It creates no rows: a ledger sealed before v24 is completed by
// its tenant's next Append (auditLog.extendTree), under that tenant's own lock.
// It is a no-op where the table already exists, so a history rewound to an earlier
// version converges instead of failing on its own relation.
func coreAuditTreeMigration(dia dialect.Dialect) migrate.Migration {
	return migrate.Migration{Version: coreAuditTreeMigrationVersion, Name: "audit_merkle_tree", Exec: func(ctx context.Context, tx *sql.Tx) error {
		have, err := dia.TableColumns(ctx, tx, dialect.AuditTreeTable)
		if err != nil {
			return fmt.Errorf("introspect %q: %w", dialect.AuditTreeTable, err)
		}
		if len(have) > 0 {
			// Already there (a history rewound to an earlier version): accept only the
			// shape this binary writes, so a stub cannot be recorded as v24.
			for _, col := range []string{"tenant_id", "idx", "leaf", "seq", "hash"} {
				if !have[col] {
					return fmt.Errorf("%q exists without column %q: refusing to record v%d over a different shape", dialect.AuditTreeTable, col, coreAuditTreeMigrationVersion)
				}
			}
			return nil
		}
		for i, stmt := range dia.AuditTreeStmts() {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("statement %d: %w", i+1, err)
			}
		}
		return nil
	}}
}

// v24's guard is outside the historical registry census. Adding it to that
// census would rewrite every predecessor manifest, so v26 owns its O -> A edge.
// It never recreates a missing guard or replaces a noncanonical definition.
func coreAuditTreeGuardMigration(dia dialect.Dialect) migrate.Migration {
	return migrate.Migration{Version: coreAuditTreeGuardMigrationVersion, Name: "audit_tree_guard_always", Exec: func(ctx context.Context, tx *sql.Tx) error {
		if dia.Name() != store.EnginePostgres {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "LOCK TABLE ONLY public.audit_tree IN ROW EXCLUSIVE MODE"); err != nil {
			return fmt.Errorf("sqlstore: lock audit tree guard: %w", err)
		}
		fn := canonicalGuardDefinition().Function
		if _, err := tx.ExecContext(ctx, fenceSharedFunctionStatement(fn.Schema, fn.Name)); err != nil {
			return fmt.Errorf("sqlstore: fence audit tree guard function: %w", err)
		}
		if err := verifyAuditTreeGuard(ctx, tx, dia, true); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE ONLY public.audit_tree ENABLE ALWAYS TRIGGER audit_tree_immutable"); err != nil {
			return fmt.Errorf("sqlstore: enable audit tree guard ALWAYS: %w", err)
		}
		return verifyAuditTreeGuard(ctx, tx, dia, false)
	}}
}

func verifyAuditTreeGuard(ctx context.Context, q rowQuerier, dia dialect.Dialect, allowLegacyOrigin bool) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	key := guardKey{Schema: dialect.EngineSchema, Relation: dialect.AuditTreeTable, Trigger: dialect.AuditTreeTable + guardTriggerSuffix}
	row, err := projectGuardCatalogRow(ctx, q, key)
	if err != nil {
		return fmt.Errorf("sqlstore: read audit tree guard: %w", err)
	}
	if ok, diff := row.matchesCanonical(guardSpec{Key: key, Definition: canonicalGuardDefinition()}); !ok {
		return fmt.Errorf("sqlstore: audit tree guard is not canonical: %s", strings.Join(diff, "; "))
	}
	if row.EnableState != guardStateAlways && !(allowLegacyOrigin && row.EnableState == guardStateOrigin) {
		return fmt.Errorf("sqlstore: audit tree guard state is %q, want ALWAYS", row.EnableState)
	}
	return nil
}

// coreWorkspaceTreeMigration adds the workspace parent and materialized path:
// two nullable columns and their indexes. It rewrites no row; a workspace written
// before v25 is a root whose path is set when it first gains a child or moves
// (workspace_tree.go). It is a no-op where the columns already exist, and it
// refuses a missing workspace relation rather than create a stub of it.
func coreWorkspaceTreeMigration(dia dialect.Dialect) migrate.Migration {
	return migrate.Migration{Version: coreWorkspaceTreeMigrationVersion, Name: "workspace_tree", Exec: func(ctx context.Context, tx *sql.Tx) error {
		have, err := dia.TableColumns(ctx, tx, workspaceDescriptor.Table)
		if err != nil {
			return fmt.Errorf("introspect %q: %w", workspaceDescriptor.Table, err)
		}
		if !have["slug"] {
			return fmt.Errorf("%q is absent or lacks its slug: refusing to record v%d", workspaceDescriptor.Table, coreWorkspaceTreeMigrationVersion)
		}
		return reconcileColumnsTx(ctx, tx, dia, []model.EntityDescriptor{workspaceTreeV25()})
	}}
}

// workspaceTreeColumns are the workspace columns v25 owns. v2 and v21 render the
// relation without them, so their historical statements do not change.
var workspaceTreeColumns = []string{"parent_id", "path"}

func beforeWorkspaceTree(d model.EntityDescriptor) model.EntityDescriptor {
	if d.Kind != workspaceDescriptor.Kind {
		return d
	}
	d.Fields = slices.DeleteFunc(slices.Clone(d.Fields), func(f model.FieldSpec) bool {
		return slices.Contains(workspaceTreeColumns, f.Name)
	})
	return d
}

// workspaceTreeV25 is v25's frozen input: the workspace relation with only the
// columns v25 owns. A later workspace column needs a later migration.
func workspaceTreeV25() model.EntityDescriptor {
	d := workspaceDescriptor
	d.Fields = slices.DeleteFunc(slices.Clone(d.Fields), func(f model.FieldSpec) bool {
		return !slices.Contains(workspaceTreeColumns, f.Name)
	})
	d.Indexes = nil
	return d
}

// coreUserGroupWorkspaceMigration adds the optional workspace place of a user
// group: one nullable column and its index. It rewrites no row; every group
// written before v27 stays tenant-wide. It is a no-op where the column already
// exists, and it refuses a missing user_groups relation rather than create a
// stub of it.
func coreUserGroupWorkspaceMigration(dia dialect.Dialect) migrate.Migration {
	return migrate.Migration{Version: coreUserGroupWorkspaceVersion, Name: "user_group_workspace", Exec: func(ctx context.Context, tx *sql.Tx) error {
		have, err := dia.TableColumns(ctx, tx, userGroupDescriptor.Table)
		if err != nil {
			return fmt.Errorf("introspect %q: %w", userGroupDescriptor.Table, err)
		}
		if !have["display_name"] {
			return fmt.Errorf("%q is absent or lacks its display_name: refusing to record v%d", userGroupDescriptor.Table, coreUserGroupWorkspaceVersion)
		}
		return reconcileColumnsTx(ctx, tx, dia, []model.EntityDescriptor{userGroupWorkspaceV27()})
	}}
}

// userGroupWorkspaceColumns are the user_groups columns v27 owns. v2 and v21
// render the relation without them, so their historical statements do not change.
var userGroupWorkspaceColumns = []string{"workspace_id"}

func beforeUserGroupWorkspace(d model.EntityDescriptor) model.EntityDescriptor {
	if d.Kind != userGroupDescriptor.Kind {
		return d
	}
	d.Fields = slices.DeleteFunc(slices.Clone(d.Fields), func(f model.FieldSpec) bool {
		return slices.Contains(userGroupWorkspaceColumns, f.Name)
	})
	return d
}

// userGroupWorkspaceV27 is v27's frozen input: the user_groups relation with
// only the columns v27 owns. A later user_groups column needs a later migration.
func userGroupWorkspaceV27() model.EntityDescriptor {
	d := userGroupDescriptor
	d.Fields = slices.DeleteFunc(slices.Clone(d.Fields), func(f model.FieldSpec) bool {
		return !slices.Contains(userGroupWorkspaceColumns, f.Name)
	})
	d.Indexes = nil
	return d
}

func reconcileVersionedDirectoryGuards(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, state directoryWriterControlState) error {
	return migrate.ReconcileTx(ctx, tx, dia, "schema_migrations_directory_guards", []migrate.Migration{{
		Version: 1, Name: "directory_writer_guards", Exec: func(ctx context.Context, tx *sql.Tx) error {
			switch dia.Name() {
			case store.EngineSQLite:
				return reconcileSQLiteDirectoryWriterGuards(ctx, tx, dia, state)
			case store.EnginePostgres:
				return reconcilePostgresDirectoryWriterGuards(ctx, tx, state)
			default:
				return fmt.Errorf("sqlstore: directory writer reconcile: unsupported engine %q", dia.Name())
			}
		},
	}})
}

// Adoption remains frozen in v21. Reassert reservations on every boot so a
// missing index cannot admit another account with an already-reserved identity.
func reconcileVersionedOSAccountReservations(ctx context.Context, db dialect.Execer, dia dialect.Dialect) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // commit below owns success
	if err := migrate.ReconcileTx(ctx, tx, dia, "schema_migrations_os_account_guards", []migrate.Migration{{
		Version: 1, Name: "os_account_reservations", Stmts: osAccountReservationStmts(),
	}}); err != nil {
		return err
	}
	return tx.Commit()
}

// Keep v23's adoption immutable while restoring the published per-boot repair
// of PostgreSQL's audit blind length constraint. SQLite cannot add this CHECK
// to an existing ledger without rebuilding it, so its posture stays unchanged.
func reconcileVersionedAuditBlindGuards(ctx context.Context, db dialect.Execer, dia dialect.Dialect) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // commit below owns success
	if err := migrate.ReconcileTx(ctx, tx, dia, "schema_migrations_audit_blind_guards", []migrate.Migration{{
		Version: 1, Name: "audit_blind_length", Exec: func(ctx context.Context, tx *sql.Tx) error {
			return reconcileAuditBlindGuards(ctx, tx, dia)
		},
	}}); err != nil {
		return err
	}
	return tx.Commit()
}

// Freeze the adoption input independently of the current catalog. New tables
// and columns need a later migration; v21 must render the same deployed schema.
func coreDescriptorsV21() []model.EntityDescriptor {
	ds := []model.EntityDescriptor{
		orgDescriptor, agentDescriptor, sessionDescriptor, providerDescriptor,
		modelDescriptor, mcpServerDescriptor, skillDescriptor, toolDescriptor,
		resourceDescriptor, identityDescriptor, policyDescriptor, costDescriptor,
		evalDescriptor, findingDescriptor, healthDescriptor, deploymentDescriptor,
		accessEdgeDescriptor, evidenceOpDescriptor,
	}
	ds = append(ds, accessEvidenceDescriptors()...)
	ds = append(ds, directoryDescriptors()...)
	ds = append(ds, authorizationEpochDescriptor)
	ds = append(ds, lineageDescriptors()...)
	ds = append(ds, userAuthorityDescriptor)
	for _, d := range scopingDescriptors() {
		ds = append(ds, beforeWorkspaceTree(d))
	}
	for _, d := range authDescriptors() {
		ds = append(ds, beforeUserGroupWorkspace(d))
	}
	return ds
}
