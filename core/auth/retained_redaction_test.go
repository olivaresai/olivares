// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "testing"

func TestRetainedRedactedInputCannotBeReplayedAsComplete(t *testing.T) {
	// Even an inconsistent completeness claim cannot turn a substituted input
	// into a historical authorization answer.
	snapshot := RetainedAuthorization{Version: 1, Complete: true, InputRedacted: true, Superadmin: true}
	got, err := ReplayRetainedAuthorization(snapshot, ScopedDecision{}, Decision{Allow: true})
	if err == nil || got != EvidenceUnknown {
		t.Fatal("redacted input was accepted as complete historical authority")
	}
}
