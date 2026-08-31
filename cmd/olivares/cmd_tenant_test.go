// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

func TestResolveTenant(t *testing.T) {
	t.Run("flag wins", func(t *testing.T) {
		t.Setenv("OLIVARES_TENANT", "primary-tenant")
		t.Setenv("OLIVARES_HOOK_PEP_TENANT", "alias-tenant")

		got, err := resolveTenant("flag-tenant")
		if err != nil || got != "flag-tenant" {
			t.Fatalf("resolveTenant(flag) = %q, %v; want flag-tenant, nil", got, err)
		}
	})

	t.Run("OLIVARES_TENANT fallback", func(t *testing.T) {
		t.Setenv("OLIVARES_TENANT", "primary-tenant")
		t.Setenv("OLIVARES_HOOK_PEP_TENANT", "alias-tenant")

		got, err := resolveTenant("")
		if err != nil || got != "primary-tenant" {
			t.Fatalf("resolveTenant(empty flag) = %q, %v; want primary-tenant, nil", got, err)
		}
	})

	t.Run("OLIVARES_HOOK_PEP_TENANT alias fallback", func(t *testing.T) {
		t.Setenv("OLIVARES_TENANT", "")
		t.Setenv("OLIVARES_HOOK_PEP_TENANT", "alias-tenant")

		got, err := resolveTenant("")
		if err != nil || got != "alias-tenant" {
			t.Fatalf("resolveTenant(empty primary) = %q, %v; want alias-tenant, nil", got, err)
		}
	})

	t.Run("empty returns a clear error", func(t *testing.T) {
		t.Setenv("OLIVARES_TENANT", "")
		t.Setenv("OLIVARES_HOOK_PEP_TENANT", "")

		got, err := resolveTenant("")
		if got != "" {
			t.Fatalf("resolveTenant(empty) tenant = %q, want empty", got)
		}
		const want = "tenant required: pass --tenant or set $OLIVARES_TENANT"
		if err == nil || err.Error() != want {
			t.Fatalf("resolveTenant(empty) error = %v, want %q", err, want)
		}
	})
}
