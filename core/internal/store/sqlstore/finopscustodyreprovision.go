// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
)

// Reprovisioning grants DML on existing tables. Take it back on custody's
// owner-written controls in that same transaction, before another boot sees it.
// Missing relations are left to their migrations on a fresh installation.
func finOpsCustodyReprovisionStmt(app string) string {
	role := "'" + strings.ReplaceAll(app, "'", "''") + "'"
	tables := make([]string, 0, len(dialect.FinOpsCustodyControlTables()))
	for _, table := range dialect.FinOpsCustodyControlTables() {
		tables = append(tables, "'"+dialect.EngineSchema+"."+table+"'")
	}
	return `DO $finops_custody_reprovision$
DECLARE
  relation_name pg_catalog.text;
  rel pg_catalog.regclass;
BEGIN
  FOREACH relation_name IN ARRAY ARRAY[` + strings.Join(tables, ", ") + `]
  LOOP
    rel := pg_catalog.to_regclass(relation_name);
    IF rel IS NOT NULL THEN
      EXECUTE pg_catalog.format('REVOKE ALL PRIVILEGES ON TABLE %s FROM %I', rel, ` + role + `);
      EXECUTE pg_catalog.format('GRANT SELECT ON TABLE %s TO %I', rel, ` + role + `);
    END IF;
  END LOOP;
END
$finops_custody_reprovision$`
}
