// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

func TestEditionHelpDistinguishesBaseBusinessFromAddOns(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	for _, edition := range []struct {
		testName string
		name     string
		addOns   bool
	}{
		{"Community", "community", false},
		{"Business base", "enterprise", false},
	} {
		t.Run(edition.testName, func(t *testing.T) {
			thisEdition.name, thisEdition.addOnsLinked = edition.name, edition.addOns
			root := newRootCmd()
			for _, path := range []string{"redteam", "finops", "orchestration", "mcp pins", "reporting schedules", "threatintel", "hooks"} {
				command, _, err := root.Find(strings.Fields(path))
				if err != nil || command == root {
					t.Fatalf("published command %s disappeared: %v", path, err)
				}
				visible := edition.addOns || (edition.name != "community" && (path == "redteam" || path == "finops"))
				if command.Hidden == visible {
					t.Errorf("%s: hidden=%v, want visible=%v", path, command.Hidden, visible)
				}
			}
		})
	}
}
