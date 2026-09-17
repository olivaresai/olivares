// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
)

func reconcileLineageACL(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, hardened bool, roles guardRoles) error {
	tables := []string{lineageWriterTable, lineageTouchedTable, lineageControlTable, lineageSeededTable}
	for _, r := range lineageRelations {
		tables = append(tables, r.descriptor().Table)
	}
	owned := append([]string(nil), tables...)
	for _, r := range lineageRelations {
		owned = append(owned, r.table)
	}
	if err := requireTableOwnership(ctx, tx, owned); err != nil {
		return err
	}
	app := quoteIdent(roles.App.Role)
	for _, table := range tables {
		target := "public." + quoteIdent(table)
		if _, err := tx.ExecContext(ctx, "REVOKE ALL PRIVILEGES ON TABLE "+target+" FROM PUBLIC"); err != nil {
			return err
		}
		if !hardened {
			continue
		}
		if _, err := tx.ExecContext(ctx, "REVOKE ALL PRIVILEGES ON TABLE "+target+" FROM "+app); err != nil {
			return err
		}
		if table != lineageWriterTable && table != lineageTouchedTable {
			// #nosec G202 -- ACL DDL: neither a table nor a role can be a bind parameter. target is "public."+quoteIdent(table) over this function's closed list (four package constants plus the compiled lineageRelations epoch tables), and app is quoteIdent(roles.App.Role), which is either a validIdent-checked provisioning name ([a-z_][a-z0-9_]*, ≤63 chars) or the server's own session_user; requireTableOwnership above has already refused the reconcile if this connection cannot administer any of those tables.
			if _, err := tx.ExecContext(ctx, "GRANT SELECT ON TABLE "+target+" TO "+app); err != nil {
				return err
			}
		}
		for _, priv := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE", "TRIGGER", "REFERENCES"} {
			var has bool
			if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.has_table_privilege($1,$2,$3)", roles.App.Role, "public."+table, priv).Scan(&has); err != nil {
				return err
			}
			if has {
				return fmt.Errorf("lineage metadata %s privilege %s remains writable by app", table, priv)
			}
		}
	}
	for _, object := range lineageGuardObjects(dia) {
		if object.body == "" {
			continue
		}
		argTypes := ""
		if object.arguments != "" {
			argTypes = "text"
		}
		function := "public." + quoteIdent(object.name) + "(" + argTypes + ")"
		if _, err := tx.ExecContext(ctx, "REVOKE ALL PRIVILEGES ON FUNCTION "+function+" FROM PUBLIC"); err != nil {
			return err
		}
		if !hardened {
			continue
		}
		if _, err := tx.ExecContext(ctx, "REVOKE ALL PRIVILEGES ON FUNCTION "+function+" FROM "+app); err != nil {
			return err
		}
		if object.exposed {
			if _, err := tx.ExecContext(ctx, "GRANT EXECUTE ON FUNCTION "+function+" TO "+app); err != nil {
				return err
			}
		}
		var has bool
		if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.has_function_privilege($1,$2,'EXECUTE')", roles.App.Role, function).Scan(&has); err != nil {
			return err
		}
		if has != object.exposed {
			return fmt.Errorf("lineage routine execute ACL drift")
		}
	}
	if hardened {
		sources := make([]string, 0, len(lineageRelations))
		for _, r := range lineageRelations {
			sources = append(sources, "public."+quoteIdent(r.table))
		}
		for _, role := range []string{"PUBLIC", app} {
			if _, err := tx.ExecContext(ctx, "REVOKE TRIGGER,TRUNCATE ON TABLE "+strings.Join(sources, ",")+" FROM "+role); err != nil {
				return err
			}
		}

		for _, r := range lineageRelations {
			for _, privilege := range []string{"TRIGGER", "TRUNCATE"} {
				var has bool
				if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.has_table_privilege($1,$2,$3)", roles.App.Role, "public."+r.table, privilege).Scan(&has); err != nil {
					return err
				}
				if has {
					return fmt.Errorf("lineage source %s retains app %s", r.table, privilege)
				}
			}
		}
		if err := verifyPostgresDirectoryWriterRoleClosure(ctx, tx, roles.App.Role); err != nil {
			return err
		}
	}
	return nil
}
