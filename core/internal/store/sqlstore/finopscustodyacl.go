// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

func verifyFinOpsCustodyControlOwnerACL(ctx context.Context, q directoryWriterACLQuerier, dia dialect.Dialect, roles guardRoles) error {
	if dia.Name() == store.EngineSQLite {
		return nil
	}
	if err := finOpsCustodyRequireTopology([]guardRoles{roles}); err != nil {
		return err
	}
	placeholders, params := tableParams([]any{dialect.EngineSchema}, dialect.FinOpsCustodyControlTables())
	rows, err := q.QueryContext(ctx, `SELECT c.relname,
 EXISTS (SELECT 1 FROM pg_catalog.aclexplode(COALESCE(c.relacl, pg_catalog.acldefault('r', c.relowner))) x WHERE x.grantee = 0),
 EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a CROSS JOIN LATERAL pg_catalog.aclexplode(a.attacl) x
 WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped AND x.grantee = 0)
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind = 'r' AND c.relname IN (`+placeholders+`) ORDER BY c.relname`, params...)
	if err != nil {
		return fmt.Errorf("sqlstore: custody PUBLIC ACL: %w", err)
	}
	count := 0
	for rows.Next() {
		var table string
		var tableGrant, columnGrant bool
		if err := rows.Scan(&table, &tableGrant, &columnGrant); err != nil {
			_ = rows.Close()
			return err
		}
		count++
		if tableGrant || columnGrant {
			_ = rows.Close()
			return fmt.Errorf("sqlstore: custody %s has a PUBLIC grant: %w", table, store.ErrAppendOnlyACLOpen)
		}
	}
	if err := errorsJoinRows(rows); err != nil {
		return err
	}
	if count != len(dialect.FinOpsCustodyControlTables()) {
		return fmt.Errorf("sqlstore: custody PUBLIC ACL sees %d relations: %w", count, store.ErrAppendOnlyACLUnverifiable)
	}
	if guardMetadataTopologyOf(roles) == guardTopologySingleRole {
		return nil
	}
	app := roles.App.bindable()
	if err := verifyFinOpsCustodyReadOnlyRole(ctx, q, app); err != nil {
		return err
	}
	major, err := postgresMajorVia(ctx, q)
	if err != nil {
		return err
	}
	// A function owner can drop the guard with CASCADE even with SELECT-only
	// table privileges. Close that administration path for the app and every
	// role it can assume, inherit or grant itself through the same closure.
	var functionOwner string
	var administersFunction bool
	if err := q.QueryRowContext(ctx, guardReachableCTE(major)+`SELECT pg_catalog.pg_get_userbyid(p.proowner), EXISTS (
 SELECT 1 FROM pg_catalog.pg_roles r WHERE r.oid = p.proowner AND (r.rolname = $2 OR `+guardRoleReachability(major)+`))
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $3 AND p.pronargs = 0`, dialect.EngineSchema, app, dialect.PostgresCustodyGuardFunction).Scan(&functionOwner, &administersFunction); err != nil {
		return fmt.Errorf("sqlstore: custody guard-function ownership: %w", err)
	}
	if administersFunction {
		return fmt.Errorf("sqlstore: custody guard function is owned by the app or a reachable role %q: %w", functionOwner, store.ErrAppendOnlyACLOpen)
	}
	placeholders, params = tableParams([]any{dialect.EngineSchema, app}, dialect.FinOpsCustodyControlTables())
	rows, err = q.QueryContext(ctx, guardReachableCTE(major)+`SELECT r.rolname, r.rolsuper, r.rolcreaterole, c.relname,
 c.relowner = r.oid,
 pg_catalog.has_table_privilege(r.oid, c.oid, 'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER'),
 pg_catalog.has_table_privilege(r.oid, c.oid, 'SELECT WITH GRANT OPTION'),
 pg_catalog.has_any_column_privilege(r.oid, c.oid, 'INSERT,UPDATE,REFERENCES'),
 pg_catalog.has_any_column_privilege(r.oid, c.oid, 'SELECT WITH GRANT OPTION,INSERT WITH GRANT OPTION,UPDATE WITH GRANT OPTION,REFERENCES WITH GRANT OPTION')
FROM pg_catalog.pg_roles r CROSS JOIN pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind = 'r' AND c.relname IN (`+placeholders+`)
 AND r.rolname <> $2 AND `+guardRoleReachability(major)+` ORDER BY r.rolname, c.relname`, params...)
	if err != nil {
		return fmt.Errorf("sqlstore: custody reachable-role ACL: %w", err)
	}
	for rows.Next() {
		var role, table string
		var super, createRole, owner, dml, grantOption, columnDML, columnGrantOption bool
		if err := rows.Scan(&role, &super, &createRole, &table, &owner, &dml, &grantOption, &columnDML, &columnGrantOption); err != nil {
			_ = rows.Close()
			return err
		}
		if super || createRole && major < 16 || owner || dml || grantOption || columnDML || columnGrantOption {
			_ = rows.Close()
			return fmt.Errorf("sqlstore: custody %s is writable or grantable through reachable role %q: %w", table, role, store.ErrAppendOnlyACLOpen)
		}
	}
	if err := errorsJoinRows(rows); err != nil {
		return err
	}
	if major < 16 {
		createRole, err := guardRoleHasCreateRole(ctx, q, app)
		if err != nil {
			return err
		}
		if createRole {
			return fmt.Errorf("sqlstore: custody application role holds CREATEROLE: %w", store.ErrAppendOnlyACLOpen)
		}
	}
	if major >= 17 {
		rows, err := q.QueryContext(ctx, guardReachableCTE(major)+`SELECT r.rolname, c.relname,
 pg_catalog.has_table_privilege(r.oid, c.oid, 'MAINTAIN')
FROM pg_catalog.pg_roles r CROSS JOIN pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind = 'r' AND c.relname IN (`+placeholders+`)
 AND (r.rolname = $2 OR `+guardRoleReachability(major)+`)`, params...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var role, table string
			var maintain bool
			if err := rows.Scan(&role, &table, &maintain); err != nil {
				_ = rows.Close()
				return err
			}
			if maintain {
				_ = rows.Close()
				return fmt.Errorf("sqlstore: custody %s has MAINTAIN through role %q: %w", table, role, store.ErrAppendOnlyACLOpen)
			}
		}
		return errorsJoinRows(rows)
	}
	return nil
}

// This same effective-privilege projection checks the owner-visible application
// identity and the actual current_user resolved from the application pool.
func verifyFinOpsCustodyReadOnlyRole(ctx context.Context, q directoryWriterACLQuerier, role string) error {
	placeholders, params := tableParams([]any{dialect.EngineSchema, role}, dialect.FinOpsCustodyControlTables())
	rows, err := q.QueryContext(ctx, `SELECT c.relname, c.relowner = a.oid,
 pg_catalog.has_table_privilege(a.oid, c.oid, 'SELECT'),
 pg_catalog.has_table_privilege(a.oid, c.oid, 'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER'),
 pg_catalog.has_table_privilege(a.oid, c.oid, 'SELECT WITH GRANT OPTION'),
 pg_catalog.has_any_column_privilege(a.oid, c.oid, 'INSERT,UPDATE,REFERENCES'),
 pg_catalog.has_any_column_privilege(a.oid, c.oid, 'SELECT WITH GRANT OPTION,INSERT WITH GRANT OPTION,UPDATE WITH GRANT OPTION,REFERENCES WITH GRANT OPTION')
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace CROSS JOIN pg_catalog.pg_roles a
WHERE n.nspname = $1 AND a.rolname = $2 AND c.relkind = 'r' AND c.relname IN (`+placeholders+`) ORDER BY c.relname`, params...)
	if err != nil {
		return fmt.Errorf("sqlstore: custody role ACL: %w", err)
	}
	count := 0
	for rows.Next() {
		var table string
		var owner, selectOK, dml, grantOption, columnDML, columnGrantOption bool
		if err := rows.Scan(&table, &owner, &selectOK, &dml, &grantOption, &columnDML, &columnGrantOption); err != nil {
			_ = rows.Close()
			return err
		}
		count++
		if owner || !selectOK || dml || grantOption || columnDML || columnGrantOption {
			_ = rows.Close()
			return fmt.Errorf("sqlstore: custody role %q is not SELECT-only on %s: %w", role, table, store.ErrAppendOnlyACLOpen)
		}
	}
	if err := errorsJoinRows(rows); err != nil {
		return err
	}
	if count != len(dialect.FinOpsCustodyControlTables()) {
		return fmt.Errorf("sqlstore: custody role ACL sees %d relations: %w", count, store.ErrAppendOnlyACLUnverifiable)
	}
	major, err := postgresMajorVia(ctx, q)
	if err != nil {
		return err
	}
	if major >= 17 {
		var maintain bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relkind = 'r' AND c.relname IN (`+placeholders+`) AND pg_catalog.has_table_privilege($2, c.oid, 'MAINTAIN'))`, params...).Scan(&maintain); err != nil {
			return err
		}
		if maintain {
			return fmt.Errorf("sqlstore: custody role %q holds MAINTAIN: %w", role, store.ErrAppendOnlyACLOpen)
		}
	}
	return nil
}

func verifyFinOpsCustodyControlAppPool(ctx context.Context, db *sql.DB, dia dialect.Dialect, hardened bool) error {
	if dia.Name() == store.EngineSQLite || !hardened {
		return nil
	}
	var role string
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_USER").Scan(&role); err != nil {
		return fmt.Errorf("sqlstore: custody application pool identity: %w", err)
	}
	return verifyFinOpsCustodyReadOnlyRole(ctx, db, role)
}
