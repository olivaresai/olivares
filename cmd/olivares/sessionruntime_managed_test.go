// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"
)

// TestSessionWorkspaceRootIsUnderTheDataDirectory pins the derivation of the
// per-session workspace root, including the two cases that yield none — because
// "no root" is deny-closed for a workspaceless launch, and a guessed absolute path
// would put a session's files somewhere nobody configured.
func TestSessionWorkspaceRootIsUnderTheDataDirectory(t *testing.T) {
	t.Parallel()

	if got := sessionWorkspaceRootFor("/var/lib/olivares"); got != "/var/lib/olivares/session-workspaces" {
		t.Fatalf("root = %q", got)
	}
	if got := sessionWorkspaceRootFor("/var/lib/olivares/"); got != "/var/lib/olivares/session-workspaces" {
		t.Fatalf("a trailing separator changed the root: %q", got)
	}
	for _, in := range []string{"", "   ", "relative/path"} {
		if got := sessionWorkspaceRootFor(in); got != "" {
			t.Fatalf("data directory %q yielded root %q; want none", in, got)
		}
	}
}
