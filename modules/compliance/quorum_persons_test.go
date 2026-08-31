// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"net/http"
	"testing"
)

// quorum_persons_test.go holds this module's quorum to PEOPLE.
//
// Every dual-control re-check here used to count GateDecision.Approvers — audit-actor
// strings. Actor() renders "user:<UserID>" for a session and "token:<CredID>" for a
// token, so ONE human holding both contributes TWO strings and clears a two-human bar
// alone. The ordinary fixtures cannot show this, because they give every approver one
// credential; these state the identities APART (setIdentities) so the difference is
// visible, and so a mutant that goes back to counting credentials cannot pass.
//
// Nothing here depends on the invariants that protect the real system three modules
// away (the unique index on (tenant_id, approval_id, decider_user) and the two handler
// guards that refuse a decision from a principal with no stable user). That is
// deliberate: the point of the change is that this module's own check is now sufficient.

// TestErasureDeniesOneHumanBehindTwoCredentials: an IRREVERSIBLE erasure needs two
// humans. Two credentials belonging to one person are not two humans.
func TestErasureDeniesOneAccountBehindTwoCredentials(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.setIdentities(GateStatusApproved, "apr-ok",
		[]string{"user:alice", "token:cred-7"}, // two credentials…
		[]string{"alice"})                      // …one human
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "rtbf")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	seedSubjectRows(h, tenant, "agent-1")
	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-1", "case_ref": "DSR-DUAL-1", "reason": "gdpr art 17",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	if r = h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr); r.code != http.StatusForbidden {
		t.Fatalf("one human behind two credentials must NOT release an irreversible erasure, got %d %s", r.code, r.raw)
	}
}

// TestHoldReleaseDeniesOneHumanBehindTwoCredentials: releasing a legal hold lifts a
// preservation obligation — the same two-human bar, the same trap.
func TestHoldReleaseDeniesOneAccountBehindTwoCredentials(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.setIdentities(GateStatusApproved, "apr-hold",
		[]string{"user:alice", "token:cred-7"}, []string{"alice"})
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "holds")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	r := h.do("POST", "/v1/m/compliance/holds", owner, map[string]any{
		"matter_ref": "MAT-DUAL-1", "scope_kind": "tenant", "reason": "litigation",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create hold = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	r = h.do("POST", "/v1/m/compliance/holds/"+id+"/release", owner, map[string]any{"reason": "matter closed"}, hdr)
	if r.code != http.StatusForbidden {
		t.Fatalf("one human behind two credentials must NOT release a legal hold, got %d %s", r.code, r.raw)
	}
}

// TestClaudeFileDeleteDeniesOneHumanBehindTwoCredentials covers the third counter in
// this module (filesQuorumOK), which carried its own duplicate of the count.
func TestClaudeFileDeleteDeniesOneAccountBehindTwoCredentials(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.setIdentities(GateStatusApproved, "apr-1",
		[]string{"user:alice", "token:cred-7"}, []string{"alice"})
	fe := &stubFileEraser{wired: true, confID: "provider-confirmation-7"}
	h, owner, _, hdr := filesTestSetup(t, gate, fe)

	r := h.do("POST", "/v1/m/compliance/claude-files/file_x/erase", owner, map[string]any{"reason": "DSR-7"}, hdr)
	if r.code == http.StatusOK {
		t.Fatalf("one human behind two credentials must NOT delete a customer file, got %d %s", r.code, r.raw)
	}
	if got := fe.deletedIDs(); len(got) != 0 {
		t.Fatalf("nothing may be deleted without two humans, deleted=%v", got)
	}
}

// TestQuorumCountsPeopleNotCredentials pins the predicate itself, so the intent survives
// even if every handler above is rewritten.
func TestQuorumCountsAccountsNotCredentials(t *testing.T) {
	oneHuman := GateDecision{
		Status:          GateStatusApproved,
		Approvers:       []string{"user:alice", "token:cred-7"},
		ApproverPersons: []string{"alice"},
	}
	if got := oneHuman.Quorum(); got != 1 {
		t.Fatalf("one human with two credentials is a quorum of ONE, got %d", got)
	}
	twoHumans := GateDecision{
		Status:          GateStatusApproved,
		Approvers:       []string{"user:alice", "user:bob"},
		ApproverPersons: []string{"alice", "bob"},
	}
	if got := twoHumans.Quorum(); got != 2 {
		t.Fatalf("two distinct humans are a quorum of TWO, got %d", got)
	}
	// Credentials are provenance only: their absence must not veto a real quorum, and
	// their presence must not create one.
	if got := (GateDecision{ApproverPersons: []string{"alice", "bob"}}).Quorum(); got != 2 {
		t.Fatalf("the quorum must not depend on the credential list, got %d", got)
	}
	if got := (GateDecision{Approvers: []string{"user:alice", "user:bob"}}).Quorum(); got != 0 {
		t.Fatalf("credentials alone are not humans, got %d", got)
	}
}
