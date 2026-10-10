// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

func TestWorkspaceAddHelpExplainsDLPLaunchApproval(t *testing.T) {
	help := helpFor(t, "agent", "workspace", "add")
	for _, want := range []string{
		`(default "label")`,
		"label requires a launch approval for every read-write session",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("workspace add help omits %q:\n%s", want, help)
		}
	}
}
