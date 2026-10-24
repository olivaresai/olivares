// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"testing"
)

// The sweep is scheduled before the engine starts, and compliance can be turned
// on later: while the module is off the tick must do nothing, not even read the
// store (the zero store here panics on any read).
func TestRetentionSweepDoesNothingWhileComplianceIsOff(t *testing.T) {
	l := &retentionSweepLoop{log: slog.Default(), active: func() bool { return false }}
	if err := l.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce while off = %v, want nil", err)
	}
}
