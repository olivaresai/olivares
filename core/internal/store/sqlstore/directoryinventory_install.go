// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

func directoryInventoryInstallRoles(spec store.PgProvisionSpec) (guardRoles, error) {
	for label, value := range map[string]string{"database": spec.Database, "app role": spec.App.Name} {
		if _, err := validIdent(label, value); err != nil {
			return guardRoles{}, err
		}
	}
	roles := guardRoles{App: guardRoleFact{Role: spec.App.Name, Known: true}}
	if spec.HasSplitOwner() {
		if _, err := validIdent("owner role", spec.Owner.Name); err != nil {
			return roles, err
		}
		roles.Owner = guardRoleFact{Role: spec.Owner.Name, Known: true}
		roles.OwnerConfigured = true
	}
	if roles.App.Role == directoryInventoryOwner || userAuthorityOwnerRole(roles) == directoryInventoryOwner {
		return roles, fmt.Errorf("inventory role must be distinct from app and schema owner")
	}
	return roles, nil
}

func renderDirectoryInventoryInstall(spec store.PgProvisionSpec) ([]store.PgProvisionStep, error) {
	roles, err := directoryInventoryInstallRoles(spec)
	if err != nil {
		return nil, err
	}
	return []store.PgProvisionStep{{Label: "closed directory inventory: post-migration DBA installation; existing objects are verified and drift is refused by the command", SQL: strings.Join([]string{
		"\\connect " + spec.Database,
		"BEGIN;",
		"CREATE ROLE " + directoryInventoryOwner + " NOLOGIN NOINHERIT NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION;",
		"GRANT USAGE ON SCHEMA public TO " + directoryInventoryOwner + ";",
		"GRANT SELECT(id,tenant_id) ON public.orgs TO " + directoryInventoryOwner + ";",
		"GRANT SELECT(id,tenant_id,version) ON public.core_directory_epoch TO " + directoryInventoryOwner + ";",
		postgresDirectoryInventoryDDL + ";",
		"ALTER FUNCTION public.olivares_directory_inventory_v1() OWNER TO " + directoryInventoryOwner + ";",
		"REVOKE ALL ON FUNCTION public.olivares_directory_inventory_v1() FROM PUBLIC;",
		"GRANT EXECUTE ON FUNCTION public.olivares_directory_inventory_v1() TO " + quoteIdent(roles.App.Role) + ";",
		"COMMIT;",
	}, "\n")}}, nil
}

func provisionDirectoryInventory(ctx context.Context, superuserDSN string, spec store.PgProvisionSpec, execute bool) (store.PgProvisionResult, error) {
	steps, err := renderDirectoryInventoryInstall(spec)
	out := store.PgProvisionResult{Steps: steps, Executed: execute}
	if err != nil || !execute {
		return out, err
	}
	roles, err := directoryInventoryInstallRoles(spec)
	if err != nil {
		return out, err
	}
	parsed, err := pgx.ParseConfig(superuserDSN)
	if err != nil {
		return out, fmt.Errorf("inventory install: invalid maintenance DSN")
	}
	db, closeDB, err := openOnDatabase(parsed, spec.Database)
	if err != nil {
		return out, err
	}
	defer closeDB()
	dia, _ := dialect.New(store.EnginePostgres)
	err = withMigrationLock(ctx, db, dia, func(mdb dialect.Execer) error {
		tx, err := mdb.BeginTx(ctx, directoryWriterTxOptions(dia))
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1,0))", provisionRolesLockKey); err != nil {
			return err
		}
		if _, err := acquireDirectoryWriter(ctx, tx, dia); err != nil {
			return fmt.Errorf("inventory install requires completed core v10 migrations: %w", err)
		}
		var valid bool
		err = tx.QueryRowContext(ctx, `SELECT NOT a.rolsuper AND NOT a.rolbypassrls AND NOT o.rolsuper AND NOT o.rolbypassrls
 AND org.relowner=o.oid AND epoch.relowner=o.oid
 FROM pg_catalog.pg_roles a CROSS JOIN pg_catalog.pg_roles o
 CROSS JOIN pg_catalog.pg_class org JOIN pg_catalog.pg_namespace ons ON ons.oid=org.relnamespace
 CROSS JOIN pg_catalog.pg_class epoch JOIN pg_catalog.pg_namespace ens ON ens.oid=epoch.relnamespace
 WHERE a.rolname=$1 AND o.rolname=$2 AND ons.nspname='public' AND org.relname='orgs'
 AND ens.nspname='public' AND epoch.relname='core_directory_epoch'`, roles.App.Role, userAuthorityOwnerRole(roles)).Scan(&valid)
		if err != nil || !valid {
			return directoryUnavailable("inventory install app/owner or relation authority is not exact", err)
		}
		if err := installDirectoryInventoryTx(ctx, tx, roles); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return out, err
	}
	// Reattest from a new session before claiming installation. This proof is
	// object authority only, not serve readiness or an activation result.
	present, err := verifyPostgresDirectoryInventory(ctx, db, roles)
	if err != nil || !present {
		return out, directoryUnavailable("fresh inventory installation readback failed", err)
	}
	out.DirectoryInventoryInstalled = true
	return out, nil
}
