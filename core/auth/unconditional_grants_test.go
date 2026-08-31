// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "testing"

// The two filters in MergeUnconditionalGrants are exercised DIRECTLY here, because the
// end-to-end tests cannot reach them: a structured grant cannot carry system:admin (the
// catalog refuses it) and no e2e case pairs a confined membership with a grant-sourced
// recon read. An adversarial contrast measured both branches at ZERO coverage — the guards
// were present, untested, and would have survived their own deletion.
//
// A filter nobody has seen fire is a comment with a syntax highlighter.

func TestMergeUnconditionalGrantsNeverCarriesSystemAdmin(t *testing.T) {
	role := []Permission{"agent:read"}
	granted := []Permission{"agent:write", PermSystemAdmin}

	for _, confined := range []bool{false, true} {
		got := MergeUnconditionalGrants(role, granted, confined)
		if !containsPerm(got, "agent:write") {
			t.Fatalf("confined=%v: the granted permission was dropped, so the system:admin "+
				"assertion below would pass for the wrong reason: %v", confined, got)
		}
		if containsPerm(got, PermSystemAdmin) {
			t.Errorf("confined=%v: merged set carries %q", confined, PermSystemAdmin)
		}
	}
}

func TestMergeUnconditionalGrantsDropsGrantSourcedReconReadsWhenConfined(t *testing.T) {
	// The recon read arrives by GRANT, which is the case no other test produces: a
	// confined principal is forbidden these whatever the action targets (F2), so a
	// grant must not put them back.
	recon := Permission("accessgraph:read")
	if !IsAccessGraphReconPerm(recon) {
		t.Fatalf("precondition: %q must be a recon read or this test asserts nothing", recon)
	}
	role := []Permission{"agent:read"}
	granted := []Permission{recon, "agent:write"}

	open := MergeUnconditionalGrants(role, granted, false)
	if !containsPerm(open, recon) {
		t.Fatalf("unconfined: %q must survive, or the confined assertion below is vacuous "+
			"— it would be asserting the absence of something never added: %v", recon, open)
	}
	confined := MergeUnconditionalGrants(role, granted, true)
	if containsPerm(confined, recon) {
		t.Errorf("confined: merged set carries the recon read %q", recon)
	}
	// ...and confinement removes ONLY that: a grant-sourced write it does not forbid
	// tenant-wide must survive, or the filter is hiding legitimate authority.
	if !containsPerm(confined, "agent:write") {
		t.Errorf("confined: agent:write was removed too; confinement must drop exactly the recon reads: %v", confined)
	}
}

func TestMergeUnconditionalGrantsIsSortedDeduplicatedAndDropsEmpty(t *testing.T) {
	role := []Permission{"tenant:read", "agent:read"}
	granted := []Permission{"agent:read", "", "agent:write"}
	got := MergeUnconditionalGrants(role, granted, false)

	want := []Permission{"agent:read", "agent:write", "tenant:read"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (sorted, deduplicated, no empty)", got, want)
		}
	}
}

// A nil report must leave the role's set exactly as it was: every deployment that wires no
// reporter takes this path on every whoami.
func TestMergeUnconditionalGrantsWithNoGrantsIsTheRoleSet(t *testing.T) {
	role := []Permission{"agent:read", "tenant:read"}
	got := MergeUnconditionalGrants(role, nil, false)
	if len(got) != len(role) {
		t.Fatalf("got %v, want the role set %v", got, role)
	}
	for i := range role {
		if got[i] != role[i] {
			t.Fatalf("got %v, want %v", got, role)
		}
	}
}

func containsPerm(perms []Permission, want Permission) bool {
	for _, p := range perms {
		if p == want {
			return true
		}
	}
	return false
}
