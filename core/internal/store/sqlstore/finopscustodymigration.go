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
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

// Register the existing custody relation/guard foundation after the deployed v18
// prefix. Reserved ordinals 12 and 16 remain absent from migration history.
const coreFinOpsCustodyControlMigrationVersion = 19

const coreFinOpsCustodyControlMigrationName = "finops_custody_control_v1"

// Unresolved split roles retain the existing ACL-unverifiable refusal.
var errFinOpsCustodyTopology = fmt.Errorf("sqlstore: finops custody control cannot establish its ACL without both resolved roles: %w", store.ErrAppendOnlyACLUnverifiable)

// PostgreSQL requires exactly one resolved topology. Build its statements before
// execution so verification can compare the complete plan.
func coreFinOpsCustodyControlMigration(dia dialect.Dialect, roles ...guardRoles) migrate.Migration {
	m := migrate.Migration{
		Version: coreFinOpsCustodyControlMigrationVersion,
		Name:    coreFinOpsCustodyControlMigrationName,
		Phase:   migrate.Expand,
		Stmts:   dia.FinOpsCustodyControlStmts(),
	}
	m.After = func(ctx context.Context, tx *sql.Tx) error {
		var resolved guardRoles
		if len(roles) == 1 {
			resolved = roles[0]
		}
		return verifyFinOpsCustodyControlMigrationAfter(ctx, tx, dia, resolved)
	}
	if dia.Name() == store.EngineSQLite {
		return m
	}
	m.Before = func(ctx context.Context, tx *sql.Tx) error {
		return finOpsCustodyRequireTopology(roles)
	}
	// Default grants may include DML: revoke them before granting SELECT.
	if len(roles) == 1 && guardMetadataTopologyOf(roles[0]) == guardTopologySplit {
		m.Stmts = append(m.Stmts, finOpsCustodySplitACLStmts(roles[0].App.bindable())...)
	}
	return m
}

// An unresolved split topology must not fall back to a single role.
func finOpsCustodyRequireTopology(roles []guardRoles) error {
	if len(roles) != 1 {
		return fmt.Errorf("sqlstore: finops custody control requires exactly one guard roles value, got %d: %w",
			len(roles), store.ErrAppendOnlyACLUnverifiable)
	}
	switch guardMetadataTopologyOf(roles[0]) {
	case guardTopologySingleRole, guardTopologySplit:
		return nil
	default:
		return fmt.Errorf("%w: could not resolve %s", errFinOpsCustodyTopology, describeUnresolvedGuardRoles(roles[0]))
	}
}

func finOpsCustodySplitACLStmts(app string) []string {
	quoted := quoteIdent(app)
	targets := strings.Join(dialect.FinOpsCustodyControlTables(), ", ")
	return []string{
		"REVOKE ALL ON TABLE " + targets + " FROM " + quoted,
		"GRANT SELECT ON TABLE " + targets + " TO " + quoted,
	}
}
