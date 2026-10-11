// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// CheckPostgresInventoryRestoreAuthority checks the explicit DBA connection before import.
func CheckPostgresInventoryRestoreAuthority(ctx context.Context, cfg store.Config, dsn string) error {
	return withInventoryRestoreAuthority(ctx, cfg, dsn, nil)
}

// RestorePostgresDirectoryInventory restores only an exact compiled inventory function
// imported under the schema owner. Ordinary boot and installation never repair drift.
func RestorePostgresDirectoryInventory(ctx context.Context, cfg store.Config, dsn string) error {
	return withInventoryRestoreAuthority(ctx, cfg, dsn, restoreDirectoryInventoryTx)
}

func withInventoryRestoreAuthority(ctx context.Context, cfg store.Config, dsn string, restore func(context.Context, *sql.Tx, guardRoles) error) error {
	if cfg.Engine != store.EnginePostgres || dsn == "" {
		return fmt.Errorf("inventory restore requires PostgreSQL and an explicit DBA connection")
	}
	dia, _ := dialect.New(store.EnginePostgres)
	appDB, err := openPGPinnedToEngineSchema(cfg.DSN, 1)
	if err != nil {
		return fmt.Errorf("inventory restore: invalid application connection")
	}
	defer appDB.Close()
	ownerDSN := cfg.OwnerDSN
	if ownerDSN == "" {
		ownerDSN = cfg.DSN
	}
	ownerDB, err := openPGPinnedToEngineSchema(ownerDSN, 1)
	if err != nil {
		return fmt.Errorf("inventory restore: invalid owner connection")
	}
	defer ownerDB.Close()
	dbaDB, err := openPGPinnedToEngineSchema(dsn, 1)
	if err != nil {
		return fmt.Errorf("inventory restore: invalid DBA connection")
	}
	defer dbaDB.Close()
	return withMigrationLock(ctx, dbaDB, dia, func(db dialect.Execer) (result error) {
		tx, err := db.BeginTx(ctx, directoryWriterTxOptions(dia))
		if err != nil {
			return fmt.Errorf("inventory restore: DBA connection unavailable")
		}
		commitAttempted := false
		defer func() {
			if !commitAttempted {
				if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
					result = errors.Join(result, fmt.Errorf("inventory restore rollback: %w", e))
				}
			}
		}()
		var super bool
		if err := tx.QueryRowContext(ctx, "SELECT rolsuper AND current_user=session_user AND pg_catalog.current_setting('session_replication_role')='origin' AND NOT pg_catalog.current_setting('transaction_read_only')::boolean AND NOT pg_catalog.pg_is_in_recovery() FROM pg_catalog.pg_roles WHERE rolname=current_user").Scan(&super); err != nil || !super {
			return fmt.Errorf("inventory restore requires a direct superuser session with triggers enabled")
		}
		appTx, err := appDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return fmt.Errorf("inventory restore: application connection unavailable")
		}
		defer appTx.Rollback()
		ownerTx, err := ownerDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return fmt.Errorf("inventory restore: owner connection unavailable")
		}
		defer ownerTx.Rollback()
		app, err := dia.ConnRolePosture(ctx, appTx)
		if err != nil {
			return err
		}
		owner, err := dia.ConnRolePosture(ctx, ownerTx)
		if err != nil {
			return err
		}
		roles := guardRoles{App: guardRoleFact{Role: app.Role, Known: true}, Owner: guardRoleFact{Role: owner.Role, Known: true}, OwnerConfigured: cfg.OwnerDSN != ""}
		if err := verifyDirectoryActivationWriterPosture("application", app, roles.App); err != nil {
			return err
		}
		if err := verifyDirectoryActivationWriterPosture("owner", owner, roles.Owner); err != nil {
			return err
		}
		for _, witness := range []*sql.Tx{appTx, ownerTx} {
			if err := verifyDirectoryActivationDatabaseIdentity(ctx, tx, directoryActivationWitnesses{app: witness}); err != nil {
				return err
			}
		}
		if restore == nil {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1,0))", directoryWriterLockKey); err != nil {
			return err
		}
		if err := restore(ctx, tx, roles); err != nil {
			return err
		}
		commitAttempted = true
		return tx.Commit()
	})
}

func restoreDirectoryInventoryTx(ctx context.Context, tx *sql.Tx, roles guardRoles) error {
	// An already closed routine is idempotent; every other prestate must be the
	// exact compiled function with the no-privileges import's stripped ACL.
	if present, err := verifyPostgresDirectoryInventory(ctx, tx, roles); err == nil && present {
		return nil
	}
	owner, app := userAuthorityOwnerRole(roles), roles.App.bindable()
	present, err := verifyPostgresAuthorityFunctionDefinition(ctx, tx, directoryInventoryFunction, owner, true)
	if err != nil {
		return err
	}
	if !present {
		return directoryUnavailable("restored inventory function is absent", nil)
	}
	var owns bool
	if err := tx.QueryRowContext(ctx, `SELECT count(*)=2 AND bool_and(pg_catalog.pg_get_userbyid(c.relowner)=$1) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname IN ('orgs','core_directory_epoch')`, owner).Scan(&owns); err != nil || !owns {
		return directoryUnavailable("restored inventory relation ownership differs from schema owner", err)
	}
	acl, err := readPostgresAuthorityFunctionACL(ctx, tx, directoryInventoryFunction, owner, app)
	if err != nil {
		return err
	}
	if !acl.null {
		return directoryUnavailable("restored inventory ACL is not stripped", nil)
	}
	// The existing installer creates only this isolated role, and verifies any
	// existing role's complete posture before its transaction can commit.
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1)", directoryInventoryOwner).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, "CREATE ROLE "+directoryInventoryOwner+" NOLOGIN NOINHERIT NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION"); err != nil {
			return err
		}
	}
	// A no-privileges import also reopens PUBLIC EXECUTE on lineage definers.
	// Reuse their compiled ACLs under the schema owner before attesting the
	// inventory role, which must not be able to call any other definer.
	if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quoteIdent(owner)); err != nil {
		return err
	}
	dia, _ := dialect.New(store.EnginePostgres)
	guards, err := lineageGuardObjectsRecorded(ctx, tx, dia)
	if err != nil {
		return err
	}
	for _, object := range guards {
		if object.body == "" {
			continue
		}
		present, err := verifyLineageGuard(ctx, tx, dia, object)
		if err != nil || !present {
			return lineageUnavailable("restored lineage routine is absent or changed", err)
		}
	}
	if err := reconcileLineageRoutineACL(ctx, tx, dia, guardMetadataTopologyOf(roles) == guardTopologySplit, roles); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "RESET ROLE"); err != nil {
		return err
	}
	for _, stmt := range directoryInventoryAuthorityStmts(app) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	_, err = verifyPostgresDirectoryInventory(ctx, tx, roles)
	return err
}
