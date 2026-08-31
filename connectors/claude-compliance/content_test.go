// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudecompliance

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// ---- content enumeration (minimal-data read) ----------------------------------------

// TestEnumerateChats_DenyClosedWithoutKey proves enumeration is off without the CAK.
func TestEnumerateChats_DenyClosedWithoutKey(t *testing.T) {
	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	refs, err := s.EnumerateChats(context.Background(), []string{"user_1"})
	if err != nil || refs != nil {
		t.Fatalf("no CAK must return nil refs, got %v / %v", refs, err)
	}
}

// TestEnumerateChats_ReturnsRefsOnly proves enumeration is read-only, requires user_ids,
// and returns ONLY references/structural metadata — never names, bodies, or PII.
func TestEnumerateChats_ReturnsRefsOnly(t *testing.T) {
	doer := &routeDoer{handler: func(req *http.Request) (int, string) {
		if req.Method != http.MethodGet {
			t.Errorf("enumeration must be read-only, got %s", req.Method)
		}
		if got := req.URL.Query()["user_ids[]"]; len(got) != 1 || got[0] != "user_1" {
			t.Errorf("user_ids[] = %v, want [user_1] (the API requires it)", got)
		}
		// The fixture deliberately includes a sensitive "name" the connector must NOT map.
		return http.StatusOK, `{"data":[{"id":"claude_chat_1","name":"SECRET TITLE","organization_uuid":"org_a","project_id":"claude_proj_9","deleted_at":null,"created_at":"2026-06-01T00:00:00Z"}],"has_more":false}`
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"compliance_access_key": "sk-ant-api01-cak"}}); err != nil {
		t.Fatal(err)
	}
	refs, err := s.EnumerateChats(context.Background(), []string{"user_1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("want 1 ref, got %d", len(refs))
	}
	r := refs[0]
	if r.ID != "claude_chat_1" || r.Kind != "chat" || r.ProjectID != "claude_proj_9" {
		t.Errorf("ref = %+v", r)
	}
	// ContentRef has no field that can carry the chat name/body — the type itself is the
	// minimal-data guarantee. (Compile-time: no Name/Content field exists to assert on.)
}

// ---- RTBF DELETE: dual-control governed eraser ---------------------------------------

type stubEraseGate struct {
	status    EraseStatus
	approvers []string
	echoPlan  bool
	wrongPlan bool
	err       error
}

func (g stubEraseGate) Authorize(_ context.Context, req EraseRequest) (EraseDecision, error) {
	if g.err != nil {
		return EraseDecision{}, g.err
	}
	plan := ""
	if g.echoPlan {
		plan = req.PlanHash
	}
	if g.wrongPlan {
		plan = "WRONG-" + req.PlanHash
	}
	// A stub configured with N approvers models N distinct PEOPLE, each acting through one
	// credential, so it states both identities. A fixture that set only Approvers would be
	// asserting that credentials count as humans — the confusion removed.
	return EraseDecision{
		ApprovalRef: "appr-1", Status: g.status, PlanHash: plan,
		Approvers: g.approvers, ApproverPersons: g.approvers,
	}, nil
}

type capEraseAuditor struct{ recs []EraseRecord }

func (a *capEraseAuditor) Record(_ context.Context, r EraseRecord) { a.recs = append(a.recs, r) }

// deleteDoer records DELETEs and returns a configurable status (default 200).
type deleteDoer struct {
	reqs   []*http.Request
	status int
}

func (d *deleteDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	st := d.status
	if st == 0 {
		st = http.StatusOK
	}
	return &http.Response{StatusCode: st, Body: http.NoBody, Header: make(http.Header)}, nil
}

func twoApprovers() []string { return []string{"alice@corp", "bob@corp"} }
func sameApprover() []string { return []string{"alice@corp", "alice@corp"} }
func oneApprover() []string  { return []string{"alice@corp"} }
func allowChat() *EraseAllowlist {
	return NewEraseAllowlist([]EraseAllowRule{{Target: EraseChat, Subjects: []string{"claude_chat_1"}}})
}

// TestEraser_DenyClosedByDefault proves a zero-config eraser can NEVER delete.
func TestEraser_DenyClosedByDefault(t *testing.T) {
	doer := &deleteDoer{}
	e := NewEraser(EraserConfig{DeleteKey: "sk-ant-api01-del", Doer: doer})
	err := e.EraseChat(context.Background(), "claude_chat_1", EraseSpec{CaseRef: "RTBF-1"})
	var deny *EraseDenyError
	if !errors.As(err, &deny) {
		t.Fatalf("zero-config eraser must deny, got %v", err)
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("deny-closed eraser must issue NO DELETE, got %d", len(doer.reqs))
	}
}

// TestEraser_SingleApproverDeniedDualControl is the headline control: an APPROVED gate
// with only ONE approver does NOT satisfy dual-control, so the irreversible delete is
// refused and no DELETE is issued.
func TestEraser_SingleApproverDeniedDualControl(t *testing.T) {
	doer := &deleteDoer{}
	aud := &capEraseAuditor{}
	e := NewEraser(EraserConfig{
		DeleteKey: "sk-ant-api01-del", Doer: doer, Allowlist: allowChat(),
		Gate:    stubEraseGate{status: EraseApproved, approvers: oneApprover(), echoPlan: true},
		Auditor: aud,
	})
	err := e.EraseChat(context.Background(), "claude_chat_1", EraseSpec{CaseRef: "RTBF-1"})
	if err == nil {
		t.Fatal("a single approver must NOT authorize an irreversible RTBF delete")
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("dual-control failure must issue NO DELETE, got %d", len(doer.reqs))
	}
	if len(aud.recs) != 1 || aud.recs[0].Allowed || aud.recs[0].DualControl {
		t.Fatalf("the refusal must be audited (not allowed, no dual-control): %+v", aud.recs)
	}
}

// TestEraser_NonDistinctApproversDenied proves two approvals from the SAME principal do
// not satisfy two-person control.
func TestEraser_NonDistinctApproversDenied(t *testing.T) {
	doer := &deleteDoer{}
	e := NewEraser(EraserConfig{
		DeleteKey: "sk-ant-api01-del", Doer: doer, Allowlist: allowChat(),
		Gate: stubEraseGate{status: EraseApproved, approvers: sameApprover(), echoPlan: true},
	})
	if err := e.EraseChat(context.Background(), "claude_chat_1", EraseSpec{CaseRef: "RTBF-1"}); err == nil {
		t.Fatal("two approvals from the same principal must not satisfy dual-control")
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("non-distinct approvers must issue NO DELETE, got %d", len(doer.reqs))
	}
}

// TestEraser_AllowlistDeny proves an un-allowlisted target is refused before any gate.
func TestEraser_AllowlistDeny(t *testing.T) {
	doer := &deleteDoer{}
	e := NewEraser(EraserConfig{
		DeleteKey: "sk-ant-api01-del", Doer: doer,
		Allowlist: allowChat(), // only claude_chat_1
		Gate:      stubEraseGate{status: EraseApproved, approvers: twoApprovers(), echoPlan: true},
	})
	if err := e.EraseChat(context.Background(), "claude_chat_OTHER", EraseSpec{CaseRef: "RTBF-1"}); err == nil {
		t.Fatal("un-allowlisted chat must be denied")
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("allowlist deny must issue NO DELETE, got %d", len(doer.reqs))
	}
}

// TestEraser_PlanMismatchDenied proves an approval bound to a DIFFERENT plan is refused.
func TestEraser_PlanMismatchDenied(t *testing.T) {
	doer := &deleteDoer{}
	e := NewEraser(EraserConfig{
		DeleteKey: "sk-ant-api01-del", Doer: doer, Allowlist: allowChat(),
		Gate: stubEraseGate{status: EraseApproved, approvers: twoApprovers(), echoPlan: true, wrongPlan: true},
	})
	if err := e.EraseChat(context.Background(), "claude_chat_1", EraseSpec{CaseRef: "RTBF-1"}); err == nil {
		t.Fatal("plan mismatch must deny (anti-TOCTOU)")
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("plan mismatch must issue NO DELETE, got %d", len(doer.reqs))
	}
}

// TestEraser_DualControlApprovedExecutesDelete proves the happy path: allowlisted +
// approved + plan-bound + TWO distinct approvers ⇒ exactly one DELETE to the right path,
// audited as erased with the dual-control evidence and case reference.
func TestEraser_DualControlApprovedExecutesDelete(t *testing.T) {
	doer := &deleteDoer{}
	aud := &capEraseAuditor{}
	e := NewEraser(EraserConfig{
		DeleteKey: "sk-ant-api01-del", Doer: doer, Allowlist: allowChat(),
		Gate:    stubEraseGate{status: EraseApproved, approvers: twoApprovers(), echoPlan: true},
		Auditor: aud,
	})
	if err := e.EraseChat(context.Background(), "claude_chat_1", EraseSpec{CaseRef: "RTBF-77", RequestedBy: "dpo@corp"}); err != nil {
		t.Fatalf("dual-control approved erase must execute: %v", err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("want exactly 1 DELETE, got %d", len(doer.reqs))
	}
	req := doer.reqs[0]
	if req.Method != http.MethodDelete || req.URL.Path != "/v1/compliance/apps/chats/claude_chat_1" {
		t.Errorf("DELETE = %s %s, want DELETE /v1/compliance/apps/chats/claude_chat_1", req.Method, req.URL.Path)
	}
	if req.Header.Get("x-api-key") != "sk-ant-api01-del" {
		t.Errorf("erase must use the delete-scoped key, got %q", req.Header.Get("x-api-key"))
	}
	rec := aud.recs[len(aud.recs)-1]
	if !rec.Allowed || !rec.DualControl || rec.ApproverCount != 2 || rec.CaseRef != "RTBF-77" || rec.Reason != "erased (irreversible)" {
		t.Fatalf("erase audit must record allowed+dual-control+2 approvers+case: %+v", rec)
	}
}

// TestEraser_TargetPaths proves each erase target routes to the correct DELETE path.
func TestEraser_TargetPaths(t *testing.T) {
	cases := []struct {
		target EraseTarget
		ref    string
		path   string
	}{
		{EraseChat, "claude_chat_1", "/v1/compliance/apps/chats/claude_chat_1"},
		{EraseFile, "claude_file_1", "/v1/compliance/apps/chats/files/claude_file_1"},
		{EraseProject, "claude_proj_1", "/v1/compliance/apps/projects/claude_proj_1"},
		{EraseProjectDocument, "claude_proj_doc_1", "/v1/compliance/apps/projects/documents/claude_proj_doc_1"},
	}
	for _, c := range cases {
		doer := &deleteDoer{}
		e := NewEraser(EraserConfig{
			DeleteKey: "sk-ant-api01-del", Doer: doer,
			Allowlist: NewEraseAllowlist([]EraseAllowRule{{Target: c.target, Subjects: []string{c.ref}}}),
			Gate:      stubEraseGate{status: EraseApproved, approvers: twoApprovers(), echoPlan: true},
		})
		if err := e.Erase(context.Background(), c.target, c.ref, EraseSpec{CaseRef: "RTBF-1"}); err != nil {
			t.Fatalf("%s: %v", c.target, err)
		}
		if len(doer.reqs) != 1 || doer.reqs[0].URL.Path != c.path {
			t.Errorf("%s routed to %v, want %s", c.target, doer.reqs, c.path)
		}
	}
}

// TestEraser_409SurfacedHonestly proves a server 409 (e.g. a project with attached chats)
// is surfaced as an error, not swallowed, and audited as a failed execution.
func TestEraser_409SurfacedHonestly(t *testing.T) {
	doer := &deleteDoer{status: http.StatusConflict}
	aud := &capEraseAuditor{}
	e := NewEraser(EraserConfig{
		DeleteKey: "sk-ant-api01-del", Doer: doer,
		Allowlist: NewEraseAllowlist([]EraseAllowRule{{Target: EraseProject, Subjects: []string{"claude_proj_1"}}}),
		Gate:      stubEraseGate{status: EraseApproved, approvers: twoApprovers(), echoPlan: true},
		Auditor:   aud,
	})
	err := e.EraseProject(context.Background(), "claude_proj_1", EraseSpec{CaseRef: "RTBF-1"})
	if err == nil {
		t.Fatal("a 409 must surface as an error, never be swallowed")
	}
	var deny *EraseDenyError
	if errors.As(err, &deny) {
		t.Fatal("a 409 is a transport/precondition failure, not a policy denial")
	}
	rec := aud.recs[len(aud.recs)-1]
	if !rec.Allowed || rec.Reason != "approved+dual-control; deletion failed" {
		t.Fatalf("the failed execution must be audited honestly: %+v", rec)
	}
}

// TestEraseDecision_Allowed unit-checks the dual-control predicate directly. The quorum
// is counted on ApproverPersons — the PEOPLE — so every case here states who approved,
// not merely which credential did.
func TestEraseDecision_Allowed(t *testing.T) {
	if (EraseDecision{Status: EraseApproved, ApproverPersons: nil}).Allowed() {
		t.Error("zero approvers must not be Allowed")
	}
	if (EraseDecision{Status: EraseApproved, ApproverPersons: []string{}}).Allowed() {
		t.Error("empty approver slice must not be Allowed")
	}
	if (EraseDecision{Status: EraseApproved, ApproverPersons: []string{"", "  "}}).Allowed() {
		t.Error("blank approver entries must not count toward the quorum")
	}
	if (EraseDecision{Status: EraseApproved, ApproverPersons: oneApprover()}).Allowed() {
		t.Error("one approver must not be Allowed")
	}
	if (EraseDecision{Status: EraseApproved, ApproverPersons: sameApprover()}).Allowed() {
		t.Error("non-distinct approvers must not be Allowed")
	}
	if (EraseDecision{Status: ErasePending, ApproverPersons: twoApprovers()}).Allowed() {
		t.Error("a non-approved status must not be Allowed")
	}
	if !(EraseDecision{Status: EraseApproved, ApproverPersons: twoApprovers()}).Allowed() {
		t.Error("approved + 2 distinct approvers must be Allowed")
	}
	// the reproduction at this seam: ONE human holding a session and a token
	// renders TWO distinct credentials. A quorum counted on Approvers would clear the
	// two-person bar for an irreversible customer-content deletion on one person's say-so.
	if (EraseDecision{
		Status:          EraseApproved,
		Approvers:       []string{"user:alice", "token:cred-7"},
		ApproverPersons: []string{"alice"},
	}).Allowed() {
		t.Error("one human behind two credentials must NOT satisfy dual control")
	}
	// And the converse: credentials are provenance only. Their absence must not veto a
	// quorum that two real people did meet.
	if !(EraseDecision{
		Status:          EraseApproved,
		Approvers:       nil,
		ApproverPersons: twoApprovers(),
	}).Allowed() {
		t.Error("two distinct humans must be Allowed regardless of the credential list")
	}
}

// TestEnumerateChats_EmptyUserIDsDenied proves the "userIDs is required" contract is
// deny-closed independently of the key: with a valid CAK but no user ids, NO call is made.
func TestEnumerateChats_EmptyUserIDsDenied(t *testing.T) {
	doer := &routeDoer{handler: func(*http.Request) (int, string) {
		t.Fatal("empty userIDs must make NO content call")
		return 0, ""
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"compliance_access_key": "sk-ant-api01-cak"}}); err != nil {
		t.Fatal(err)
	}
	refs, err := s.EnumerateChats(context.Background(), []string{})
	if err != nil || refs != nil {
		t.Fatalf("empty userIDs must return (nil,nil), got %v / %v", refs, err)
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("empty userIDs must issue NO request, got %d", len(doer.reqs))
	}
}

// TestEraser_ApprovedEmptyPlanHashDenies proves the anti-TOCTOU hardening for RTBF: an
// APPROVED, dual-control decision that does NOT echo the plan (empty PlanHash) is refused
// — the connector requires the gate to prove it bound the exact erasure plan.
func TestEraser_ApprovedEmptyPlanHashDenies(t *testing.T) {
	doer := &deleteDoer{}
	aud := &capEraseAuditor{}
	e := NewEraser(EraserConfig{
		DeleteKey: "sk-ant-api01-del", Doer: doer, Allowlist: allowChat(),
		Gate:    stubEraseGate{status: EraseApproved, approvers: twoApprovers(), echoPlan: false}, // no plan echo
		Auditor: aud,
	})
	if err := e.EraseChat(context.Background(), "claude_chat_1", EraseSpec{CaseRef: "RTBF-1"}); err == nil {
		t.Fatal("an approval with empty PlanHash must be denied (anti-TOCTOU), even with dual-control")
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("unbound approval must issue NO DELETE, got %d", len(doer.reqs))
	}
	if aud.recs[len(aud.recs)-1].Allowed {
		t.Fatal("unbound approval must be audited as not-allowed")
	}
}

// TestEraser_GateAuthorizeErrorFailsClosed proves a gate error denies (fail-closed) with
// no DELETE and an honest audit reason.
func TestEraser_GateAuthorizeErrorFailsClosed(t *testing.T) {
	doer := &deleteDoer{}
	aud := &capEraseAuditor{}
	e := NewEraser(EraserConfig{
		DeleteKey: "sk-ant-api01-del", Doer: doer, Allowlist: allowChat(),
		Gate:    stubEraseGate{err: errors.New("approval bridge timeout")},
		Auditor: aud,
	})
	err := e.EraseChat(context.Background(), "claude_chat_1", EraseSpec{CaseRef: "RTBF-1"})
	if err == nil {
		t.Fatal("a gate error must deny (fail-closed)")
	}
	var deny *EraseDenyError
	if errors.As(err, &deny) {
		t.Fatal("a gate error is a fail-closed transport error, not a policy denial")
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("gate error must issue NO DELETE, got %d", len(doer.reqs))
	}
	rec := aud.recs[len(aud.recs)-1]
	if rec.Allowed || rec.Reason != "gate error (fail-closed)" {
		t.Fatalf("gate error must be audited fail-closed, got %+v", rec)
	}
}

// TestEraser_DenyPathAuditsCaseRef proves the RTBF case reference (the legal-basis
// provenance) survives into the audit even on a DENIED path — forensics must not be lost
// when an erasure is refused.
func TestEraser_DenyPathAuditsCaseRef(t *testing.T) {
	doer := &deleteDoer{}
	aud := &capEraseAuditor{}
	e := NewEraser(EraserConfig{
		DeleteKey: "sk-ant-api01-del", Doer: doer, Allowlist: allowChat(),
		Gate:    stubEraseGate{status: EraseApproved, approvers: oneApprover(), echoPlan: true}, // dual-control fails
		Auditor: aud,
	})
	if err := e.EraseChat(context.Background(), "claude_chat_1", EraseSpec{CaseRef: "RTBF-DENY-9", RequestedBy: "dpo@corp"}); err == nil {
		t.Fatal("single approver must deny")
	}
	rec := aud.recs[len(aud.recs)-1]
	if rec.Allowed || rec.CaseRef != "RTBF-DENY-9" || rec.RequestedBy != "dpo@corp" {
		t.Fatalf("a denied erasure must still audit the case reference + requester, got %+v", rec)
	}
}
