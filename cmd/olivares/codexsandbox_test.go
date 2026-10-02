// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

// Default driver construction never switches off the native sandbox merely
// because Landlock exists. The actual native-probe/fallback ordering is exercised
// in sessions.TestCodexNativeSandboxProbeKeepsOrRefusesBeforeFallback.
func TestCodexSandboxIsOffOnlyWhereOlivaresConfines(t *testing.T) {
	args := firstHourCodexDriver().LaunchArgs(sessions.DriverLaunch{})
	if strings.Contains(strings.Join(args, " "), sessions.CodexSandboxDangerFull) {
		t.Fatalf("default driver disabled its native sandbox without a probe: %v", args)
	}
	if !strings.Contains(strings.Join(args, " "), "check_for_update_on_startup=false") {
		t.Fatalf("default driver did not pin native auto-update off: %v", args)
	}
}
