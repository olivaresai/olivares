// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

// TestInvariant_D6_FailClosedDefault pins the launch gate's posture over a per-control
// availability dependency it cannot READ (budget ledger, context policy): unset is
// fail-closed in every build (Community defaulted to fail-open until 26.10.1), an explicit
// value wins, and any invalid/typo value is fail-closed + loud. A regression that makes
// the default fail-open again (silently weakening the gate so an unreadable
// budget/context control lets a session through) must fail here. Anchor:
// resolveAvailabilityPosture (sessiongov.go).
func TestInvariant_D6_FailClosedDefault(t *testing.T) {
	cases := []struct {
		raw  string
		want availabilityPosture
	}{
		{"", availabilityFailClosed},        // the default, whatever the edition is
		{"garbage", availabilityFailClosed}, // invalid → fail-closed + loud
		{"fail-open", availabilityFailOpen}, // explicit override wins
		{"fail-closed", availabilityFailClosed},
	}
	for _, c := range cases {
		if got := resolveAvailabilityPosture(c.raw, nil); got != c.want {
			t.Errorf("resolveAvailabilityPosture(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
	// A gate built without a posture must deny too.
	var zero availabilityPosture
	if zero != availabilityFailClosed {
		t.Errorf("the zero availabilityPosture is %v, want fail-closed", zero)
	}
}
