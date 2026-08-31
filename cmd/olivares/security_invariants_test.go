// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

// TestInvariant_D6_FailClosedEnterpriseDefault pins the ratified D6 posture
// (an internal design note (not shipped), D6=A): when a per-control availability dependency
// at the session launch gate cannot be READ, the ENTERPRISE edition defaults to
// fail-closed (deny), community preserves fail-open, and any invalid/typo value is
// fail-closed + loud. A regression that flips the enterprise default to fail-open —
// silently weakening the gate so an unreadable budget/context control lets a
// session through — must fail here. Anchor: resolveAvailabilityPosture (sessiongov.go).
func TestInvariant_D6_FailClosedEnterpriseDefault(t *testing.T) {
	cases := []struct {
		raw, edition string
		want         availabilityPosture
	}{
		{"", "enterprise", availabilityFailClosed},        // D6: enterprise default = fail-closed
		{"", "community", availabilityFailOpen},           // community preserves fail-open
		{"garbage", "enterprise", availabilityFailClosed}, // invalid → fail-closed + loud
		{"typo", "community", availabilityFailClosed},     // invalid → fail-closed even on community
		{"fail-open", "enterprise", availabilityFailOpen}, // explicit override wins
		{"fail-closed", "community", availabilityFailClosed},
	}
	for _, c := range cases {
		if got := resolveAvailabilityPosture(c.raw, c.edition, nil); got != c.want {
			t.Errorf("resolveAvailabilityPosture(%q, %q) = %v, want %v", c.raw, c.edition, got, c.want)
		}
	}
}
