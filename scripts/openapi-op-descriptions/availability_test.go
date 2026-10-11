// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCommunityAvailabilityDescriptions(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	m, err := compose(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pkg, source, description string
		count                    int
	}{
		{"finops", "edition_noenterprise.go", "Returns HTTP 501 because FinOps is a Business feature; no FinOps action is performed.", 35},
		{"governance", "breakglass_noenterprise.go", "Returns HTTP 501 with business_required because break-glass access requires Business; no break-glass action is performed.", 7},
		{"redteam", "routes_noenterprise.go", "Returns HTTP 501 because red team is a Business feature; no red team action is performed.", 9},
		{"security", "forensic_export_community.go", "Returns HTTP 501 with audit_export_unavailable because forensic case export requires Business; no case is exported.", 1},
		{"observability", "export_community.go", "Returns HTTP 501 with observability_export_unavailable because telemetry export requires Business; no trace is exported.", 1},
		{"posture-export", "export_community.go", "Returns HTTP 501 with posture_export_unavailable because posture export requires Business; no posture is exported.", 1},
	} {
		t.Run(tc.pkg, func(t *testing.T) {
			routes, err := routesInPackage(filepath.Join(root, modulesDirRel, tc.pkg), root)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, route := range routes {
				if !strings.HasPrefix(route.handlerPos, filepath.Join(modulesDirRel, tc.pkg, tc.source)+":") {
					continue
				}
				count++
				t.Run(route.key(), func(t *testing.T) {
					if got := m.entries[route.key()].description; got != tc.description {
						t.Errorf("composed description = %q, want %q", got, tc.description)
					}
					if got := m.published[route.key()]; got != tc.description {
						t.Errorf("published description = %q, want %q", got, tc.description)
					}
				})
			}
			if count != tc.count {
				t.Errorf("got %d availability registrations, want %d", count, tc.count)
			}
		})
	}
}
