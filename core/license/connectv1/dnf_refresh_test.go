// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package connectv1

import "testing"

// The literals below are the DNF wire names, written out so a renamed constant cannot move the wire
// with it. connectv1 does not pin operations in its canonical proof vectors; IntendedRoute is the pin.

// TestDnfRefreshIsBoundToItsOwnRoute: the challenge operation dnf-refresh is bound to POST
// /connect/dnf-refresh only, so its challenge cannot be spent on /connect/refresh or /connect/apt-refresh.
func TestDnfRefreshIsBoundToItsOwnRoute(t *testing.T) {
	m, p, err := IntendedRoute("dnf-refresh", "dep_1")
	if err != nil || m != "POST" || p != "/connect/dnf-refresh" {
		t.Fatalf("IntendedRoute(dnf-refresh) = %s %s %v, want POST /connect/dnf-refresh", m, p, err)
	}
	if OpDnfRefresh != "dnf-refresh" || PathDnfRefresh != "/connect/dnf-refresh" {
		t.Fatalf("wire names OpDnfRefresh=%q PathDnfRefresh=%q", OpDnfRefresh, PathDnfRefresh)
	}
	if _, rp, _ := IntendedRoute("refresh", "dep_1"); rp == p {
		t.Fatal("refresh and dnf-refresh share a route")
	}
	if _, ap, _ := IntendedRoute("apt-refresh", "dep_1"); ap == p {
		t.Fatal("apt-refresh and dnf-refresh share a route")
	}
}
