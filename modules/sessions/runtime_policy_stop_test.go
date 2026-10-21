// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// A hook's content inspection can end the run its credential belongs to, with the same
// stop the duration ceiling and the denial budget use.
func TestStopSessionForPolicyEndsTheRunsOwnSession(t *testing.T) {
	f := newDenialFixture(t, false)
	if err := f.h.m.StopSessionForPolicy(f.principal, "a decoy credential in the session home was touched"); err != nil {
		t.Fatalf("StopSessionForPolicy: %v", err)
	}
	waitFor(t, "the run to stop", func() bool {
		d, err := f.h.m.getRun(t.Context(), f.tenant, f.run)
		return err == nil && d.State == stateStopped
	})
}

// Only the run's own current session credential may end it: not a person's token, not an
// older generation of the credential, not another session's.
func TestStopSessionForPolicyRefusesAnyOtherPrincipal(t *testing.T) {
	f := newDenialFixture(t, false)
	human, err := auth.NewAuthenticator(f.h.st, nil).Authenticate(t.Context(), f.admin)
	if err != nil {
		t.Fatal(err)
	}
	stale, other := f.principal, f.principal
	stale.SessionFence++
	other.SessionIdentity = "another-session"
	for name, p := range map[string]auth.Principal{"human": human, "stale fence": stale, "other session": other} {
		if err := f.h.m.StopSessionForPolicy(p, "detail"); err == nil {
			t.Fatalf("%s: a run was stopped without its current session authority", name)
		}
	}
	settle()
	d, err := f.h.m.getRun(t.Context(), f.tenant, f.run)
	if err != nil || d.State == stateStopped {
		t.Fatalf("run state = %v err = %v; refused principals must leave the run alone", d.State, err)
	}
}
