// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

// A REMEDY IS ONLY ACTIONABLE IF IT RUNS AS WRITTEN (F8 from the external
// contrast).
//
// The 501 cross_tenant_admin_pool_not_configured body quotes a command an operator
// is meant to paste (core/api/errors.go honestSeamMessage). Its first draft quoted
// two remedies and NEITHER worked: deploy/postgres/01-app-role.sql has its
// admin-role block commented out end to end, and a bare `--admin-role` exits with
// "flag needs an argument". The API tests could not see it — they assert substrings
// of a message, which says nothing about whether the thing named exists.
//
// This is the half that lives where the flags do. If one of these is renamed or
// dropped, the sentence an operator pastes stops working, and this fails in the
// package that made it stop working rather than silently in production.
func TestDBInitCarriesTheFlagsTheRefusalMessageQuotes(t *testing.T) {
	cmd := newDBCmd()
	init, _, err := cmd.Find([]string{"init"})
	if err != nil {
		t.Fatalf("db init subcommand not found: %v", err)
	}
	// Exactly the flags quoted in honestSeamMessage["cross_tenant_admin_pool_not_configured"].
	for _, name := range []string{"superuser-dsn", "admin-role", "admin-password-file"} {
		if init.Flags().Lookup(name) == nil {
			t.Errorf("`olivares db init` has no --%s, but the 501 refusal message tells operators to pass it", name)
		}
	}
	// --admin-role takes a VALUE. The broken draft quoted it bare, which is why the
	// type matters here and not just the name.
	if f := init.Flags().Lookup("admin-role"); f != nil && f.Value.Type() != "string" {
		t.Errorf("--admin-role is %s, not string: the quoted command passes it a role name", f.Value.Type())
	}
}
