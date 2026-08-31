// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "testing"

// The severity ladder of the public status page. It is the rule that lets
// `not_configured` exist without ever hiding a fault: a component nobody
// provisioned outranks `operational` (the plane delivers less than the whole
// product) and is outranked by every fault, so the aggregate word degrades the
// moment anything actually breaks.
func TestWorstSeverityLadder(t *testing.T) {
	for _, tc := range []struct {
		a, b, want string
	}{
		{statusOperational, statusNotConfigured, statusNotConfigured},
		{statusNotConfigured, statusOperational, statusNotConfigured},
		{statusNotConfigured, statusDegraded, statusDegraded},
		{statusDegraded, statusNotConfigured, statusDegraded},
		{statusNotConfigured, statusOutage, statusOutage},
		{statusOutage, statusNotConfigured, statusOutage},
		{statusDegraded, statusOutage, statusOutage},
		{statusOperational, statusOperational, statusOperational},
		// Deny-closed: a value this build does not recognize is a fault, never
		// healthy and never merely unconfigured.
		{statusOperational, "quarantined", "quarantined"},
		{statusNotConfigured, "quarantined", "quarantined"},
		{"quarantined", statusOutage, statusOutage},
	} {
		if got := worst(tc.a, tc.b); got != tc.want {
			t.Errorf("worst(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}
