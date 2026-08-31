// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agentcore

import "testing"

// TestExportDualControlCountsPeopleNotCredentials: an enforcement-WEAKENING
// export needs two humans. An audit-actor string names a credential — "user:<id>" for a
// session, "token:<id>" for a token — so one person holding both renders two of them,
// and a quorum counted on credentials would let that one person weaken enforcement alone.
func TestExportDualControlCountsAccountsNotCredentials(t *testing.T) {
	oneHuman := ExportGateDecision{
		Status:          ExportApproved,
		Approvers:       []string{"user:alice", "token:cred-7"},
		ApproverPersons: []string{"alice"},
	}
	if oneHuman.HasDualControl() {
		t.Error("one human behind two credentials must NOT satisfy dual control")
	}
	twoHumans := ExportGateDecision{
		Status:          ExportApproved,
		Approvers:       []string{"user:alice", "user:bob"},
		ApproverPersons: []string{"alice", "bob"},
	}
	if !twoHumans.HasDualControl() {
		t.Error("two distinct humans must satisfy dual control")
	}
	// Credentials are provenance only: they neither create nor veto a quorum.
	if (ExportGateDecision{Status: ExportApproved, Approvers: []string{"user:alice", "user:bob"}}).HasDualControl() {
		t.Error("credentials alone are not humans")
	}
	if !(ExportGateDecision{Status: ExportApproved, ApproverPersons: []string{"alice", "bob"}}).HasDualControl() {
		t.Error("two humans must count regardless of the credential list")
	}
}
