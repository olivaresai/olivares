// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import "testing"

// TestAdminActionDualControlCountsPeopleNotCredentials: the actuator's own
// dual-control re-verification must count HUMANS. An audit-actor string names a
// credential — "user:<id>" for a session, "token:<id>" for a token — so one person
// holding both renders two of them, and a quorum counted on credentials would let that
// one person archive a workspace or grant workspace admin alone.
func TestAdminActionDualControlCountsAccountsNotCredentials(t *testing.T) {
	oneHuman := AdminActionDecision{
		Status:          AdminApproved,
		Approvers:       []string{"user:alice", "token:cred-7"},
		ApproverPersons: []string{"alice"},
	}
	if oneHuman.HasDualControl() {
		t.Error("one human behind two credentials must NOT satisfy dual control")
	}
	twoHumans := AdminActionDecision{
		Status:          AdminApproved,
		Approvers:       []string{"user:alice", "user:bob"},
		ApproverPersons: []string{"alice", "bob"},
	}
	if !twoHumans.HasDualControl() {
		t.Error("two distinct humans must satisfy dual control")
	}
	// Credentials are provenance only: they neither create nor veto a quorum.
	if (AdminActionDecision{Status: AdminApproved, Approvers: []string{"user:alice", "user:bob"}}).HasDualControl() {
		t.Error("credentials alone are not humans")
	}
	if !(AdminActionDecision{Status: AdminApproved, ApproverPersons: []string{"alice", "bob"}}).HasDualControl() {
		t.Error("two humans must count regardless of the credential list")
	}
}
