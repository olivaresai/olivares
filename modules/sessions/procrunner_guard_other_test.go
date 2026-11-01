// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package sessions

import (
	"os/exec"
	"testing"
)

// Without a process-tree guard, setup must refuse a mode that could leave detached
// descendants behind. Ordinary sessions keep their existing process-group behavior.
func TestWatchEngineRefusesUnconfirmedSetupCleanup(t *testing.T) {
	if _, err := watchEngine(exec.Command("/bin/true"), true); err == nil {
		t.Fatal("setup cleanup without a process-tree guard was accepted")
	}
	if _, err := watchEngine(exec.Command("/bin/true"), false); err != nil {
		t.Fatalf("ordinary session guard behavior changed: %v", err)
	}
}
