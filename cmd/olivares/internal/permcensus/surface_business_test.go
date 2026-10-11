// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package permcensus

import (
	"strings"
	"testing"
)

// The Business table is the untagged base; Community's refusals replace it, so
// this runs only in the Business build (Community asserts its own in
// TestCommunityRouteSurfaces).
func TestBusinessRouteSurfaces(t *testing.T) {
	if !Business {
		t.Skip("Community build: TestCommunityRouteSurfaces covers these routes")
	}
	for key, want := range map[string]string{
		"finops POST /seats":                  "api-only",
		"governance POST /breakglass/consume": "api-only",
		"finops GET /seats/utilization":       "console",
		"governance GET /breakglass":          "console",
	} {
		parts := strings.SplitN(key, " ", 3)
		got := routeSurface(parts[0], parts[1], parts[2], nil)
		if got != want {
			t.Errorf("%s surface = %q, want %q", key, got, want)
		}
	}
}
