// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// The apps gateway asks the admission seam for finops:budget:admin before it writes a
// spend limit. The module must declare it, or no scoped grant or custom role can carry it,
// and the role tiers must keep it to admin and owner: an editor holds budget:write and
// must not gain spend-limit administration.
func TestBudgetAdminIsDeclaredAndAdminTier(t *testing.T) {
	declared := false
	for _, p := range New().Permissions() {
		declared = declared || p == PermBudgetAdmin
	}
	if !declared {
		t.Error("finops:budget:admin is not in Permissions(), so no scoped grant can carry it")
	}
	for role, want := range map[string]bool{
		auth.RoleViewer: false, auth.RoleEditor: false, auth.RoleAdmin: true, auth.RoleOwner: true,
	} {
		if got := auth.RoleGrants(role, PermBudgetAdmin); got != want {
			t.Errorf("RoleGrants(%s, finops:budget:admin) = %v, want %v", role, got, want)
		}
	}
}
