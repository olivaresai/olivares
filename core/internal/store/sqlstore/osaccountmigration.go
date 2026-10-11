// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
)

// v20 records the additive os_account binding kind. Like v15/v18, nullable
// columns are reconciled after the version record on both fresh/upgraded stores.
const coreOSAccountMigrationVersion = 20

func coreOSAccountMigration() migrate.Migration {
	return migrate.Migration{Version: coreOSAccountMigrationVersion, Name: "os_account_bindings_v1", Phase: migrate.Expand}
}

// Generation-zero rows reserve the tuple permanently. Successors copy it;
// revocation/retirement never remove these rows or free their unique keys.
func reconcileOSAccountReservations(ctx context.Context, db dialect.Querier) error {
	for _, statement := range osAccountReservationStmts() {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sqlstore: OS account reservation: %w", err)
		}
	}
	return nil
}

func osAccountReservationStmts() []string {
	var statements []string
	for _, key := range []struct{ name, columns string }{
		{"uid", "tenant_id, os_uid"}, {"account", "tenant_id, os_account"},
		{"subject", "tenant_id, target_tenant_id, subject_user_id"},
	} {
		statements = append(statements, fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS core_credential_bindings_os_%s_uniq ON core_credential_bindings (%s) WHERE subject_kind = 'os_account' AND subject_generation = 0", key.name, key.columns))
	}
	return statements
}
