// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package confine

import "testing"

func TestExecContext_EntersTheDomainOnlyFromTheEngineDomain(t *testing.T) {
	for _, tc := range []struct {
		current, want string
		ok            bool
	}{
		// The appliance's engine: only the type changes; user, role and level stay.
		{"system_u:system_r:olivares_t:s0", "system_u:system_r:olivares_gitpub_t:s0", true},
		{"system_u:system_r:olivares_t:s0\x00", "system_u:system_r:olivares_gitpub_t:s0", true},
		{"system_u:system_r:olivares_t:s0-s0:c0.c1023\n", "system_u:system_r:olivares_gitpub_t:s0-s0:c0.c1023", true},
		// Any other host or domain starts the command unchanged: no Olivares policy, nothing to enter.
		{"unconfined_u:unconfined_r:unconfined_t:s0-s0:c0.c1023", "", false},
		{"system_u:system_r:unconfined_service_t:s0", "", false},
		{"system_u:system_r:olivares_tool_t:s0", "", false},
		{"kernel", "", false},
		{"", "", false},
	} {
		got, ok := execContext(tc.current, "olivares_gitpub_t")
		if got != tc.want || ok != tc.ok {
			t.Errorf("execContext(%q) = %q, %v; want %q, %v", tc.current, got, ok, tc.want, tc.ok)
		}
	}
}
