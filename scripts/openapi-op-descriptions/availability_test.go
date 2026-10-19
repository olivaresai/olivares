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
	// claimed counts the routes the rows check per handler file, total the routes
	// that file declares a handler for: a route no row names must fail the test, not
	// go unchecked.
	claimed, total := map[string]int{}, map[string]int{}
	for _, tc := range []struct {
		// source is the file that declares the handler, not the one that registers the
		// route: the review-quality route is registered in approval_reviewquality_noenterprise.go
		// and answers with the businessRequired declared in breakglass_noenterprise.go.
		pkg, source, description string
		// route, when set, keeps only the routes under this pattern: one handler file
		// can answer for the seams of several Business capabilities.
		route string
		count int
	}{
		{"deploy", "routes_noenterprise.go", "Returns HTTP 501 because deployment is a Business feature; no deployment action is performed.", "", 17},
		{"finops", "edition_noenterprise.go", "Returns HTTP 501 because FinOps is a Business feature; no FinOps action is performed.", "", 35},
		{"governance", "breakglass_noenterprise.go", "Returns HTTP 501 with business_required because break-glass access requires Business; no break-glass action is performed.", "/breakglass", 7},
		{"governance", "breakglass_noenterprise.go", "Returns HTTP 501 with business_required because approval review quality requires Business; no report is produced.", "/approvals/review-quality", 1},
		{"redteam", "routes_noenterprise.go", "Returns HTTP 501 because red team is a Business feature; no red team action is performed.", "", 9},
		{"security", "forensic_export_community.go", "Returns HTTP 501 with audit_export_unavailable because forensic case export requires Business; no case is exported.", "", 1},
		{"observability", "export_community.go", "Returns HTTP 501 with observability_export_unavailable because telemetry export requires Business; no trace is exported.", "", 1},
		{"posture-export", "export_community.go", "Returns HTTP 501 with posture_export_unavailable because posture export requires Business; no posture is exported.", "", 1},
	} {
		name := tc.pkg
		if tc.route != "" {
			name += strings.ReplaceAll(tc.route, "/", "_")
		}
		t.Run(name, func(t *testing.T) {
			routes, err := routesInPackage(filepath.Join(root, modulesDirRel, tc.pkg), root)
			if err != nil {
				t.Fatal(err)
			}
			file := tc.pkg + "/" + tc.source
			count, inFile := 0, 0
			for _, route := range routes {
				if !strings.HasPrefix(route.handlerPos, filepath.Join(modulesDirRel, tc.pkg, tc.source)+":") {
					continue
				}
				inFile++
				if tc.route != "" && route.pattern != tc.route && !strings.HasPrefix(route.pattern, tc.route+"/") {
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
			total[file] = inFile
			claimed[file] += count
			if count != tc.count {
				t.Errorf("got %d availability registrations, want %d", count, tc.count)
			}
		})
	}
	for file, n := range total {
		if claimed[file] != n {
			t.Errorf("%s declares the handler of %d routes and the rows check %d: every one needs a row", file, n, claimed[file])
		}
	}
}
