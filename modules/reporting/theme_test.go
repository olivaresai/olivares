// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import "testing"

func TestStatusBadgeClassMapsKnownAndUnknownStatuses(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"satisfied", "badge-satisfied"},
		{"by_design", "badge-by-design"},
		{"partial", "badge-partial"},
		{"gap", "badge-gap"},
		{"unmapped", "badge-unmapped"},
		{"unexpected", "badge-unmapped"},
	}
	for _, tt := range tests {
		if got := statusBadgeClass(tt.status); got != tt.want {
			t.Fatalf("statusBadgeClass(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestBudgetBarClassMapsThresholdsAndOverBudget(t *testing.T) {
	tests := []struct {
		pct  int
		over bool
		want string
	}{
		{20, false, "progress-ok"},
		{80, false, "progress-ok"},
		{81, false, "progress-warn"},
		{10, true, "progress-over"},
	}
	for _, tt := range tests {
		if got := budgetBarClass(tt.pct, tt.over); got != tt.want {
			t.Fatalf("budgetBarClass(%d,%v) = %q, want %q", tt.pct, tt.over, got, tt.want)
		}
	}
}
