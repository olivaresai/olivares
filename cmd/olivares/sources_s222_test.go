// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

// TestRosterSettingsProviderDefault pins the fix that a directory roster registered
// under an intuitive kind (pingone/forgerock) cannot silently run the default
// keycloak backend: rosterSettings seeds `provider` from the kind alias when absent,
// never overriding an explicit provider, and leaves non-alias kinds untouched.
func TestRosterSettingsProviderDefault(t *testing.T) {
	// kind alias, no provider set -> provider is seeded from the kind.
	for kind, want := range map[string]string{"pingone": "pingone", "ping": "pingone", "forgerock": "forgerock", "keycloak": "keycloak"} {
		got := rosterSettings(kind, map[string]string{"base_url": "x"})
		if got["provider"] != want {
			t.Errorf("rosterSettings(%q) provider = %q, want %q", kind, got["provider"], want)
		}
	}
	// An explicit provider is never overridden.
	got := rosterSettings("pingone", map[string]string{"provider": "keycloak"})
	if got["provider"] != "keycloak" {
		t.Errorf("explicit provider overridden: got %q, want keycloak", got["provider"])
	}
	// A non-directory kind is left untouched (no provider injected, original map returned).
	in := map[string]string{"base_url": "x"}
	if out := rosterSettings("ldap", in); out["provider"] != "" {
		t.Errorf("rosterSettings(ldap) injected a provider: %q", out["provider"])
	}
}
