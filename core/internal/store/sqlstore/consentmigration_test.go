// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"testing"
)

// TestFreshStoreGainsConsentCustodyRelationsAndColumns: a fresh store holds the
// relations and columns core v14 records, whichever migration or reconcile step
// creates them. The historical render keeps v2's statements as they were before
// v14 (TestUserAuthorityPreservesHistoricalRender), so a fresh store must reach
// them the way an upgraded one does.
func TestFreshStoreGainsConsentCustodyRelationsAndColumns(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	s := st.(*sqlStore)
	for table, want := range map[string][]string{
		"tenant_exclusions": {"retirement_state", "retired_epoch", "module_results"},
		"account_offers":    {"selector", "authority_version", "voided_at"},
		"users":             {"credential_custody", "custody_tenant_id"},
		"auth_sessions":     {"tenant_scope"},
	} {
		cols, err := s.dia.TableColumns(ctx, s.db, table)
		if err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		for _, column := range want {
			if !cols[column] {
				t.Errorf("a fresh store's %s has no %s column", table, column)
			}
		}
	}
}
