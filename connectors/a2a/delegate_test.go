// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// --- governance test doubles ----------------------------------------------------

// fakeGate is a configurable DelegationGate. It echoes the requested PlanHash so an
// "approved" decision is correctly bound to the plan the Delegator computed.
type fakeGate struct {
	status  GateStatus
	bindNil bool // when true, return an empty PlanHash (simulating an unbound gate)
	lastReq DelegationRequest
}

func (g *fakeGate) Authorize(_ context.Context, req DelegationRequest) (GateDecision, error) {
	g.lastReq = req
	bind := req.PlanHash
	if g.bindNil {
		bind = ""
	}
	return GateDecision{ApprovalRef: "appr-1", Status: g.status, PlanHash: bind}, nil
}

// capAuditor captures decision records for assertions.
type capAuditor struct {
	mu   sync.Mutex
	decs []DelegationDecision
}

func (a *capAuditor) Record(_ context.Context, d DelegationDecision) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.decs = append(a.decs, d)
}

func (a *capAuditor) last() DelegationDecision {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.decs) == 0 {
		return DelegationDecision{}
	}
	return a.decs[len(a.decs)-1]
}

// rpcTaskResp builds a JSON-RPC v1.0 response carrying a Task with the given id +
// state, wrapped in the SendMessageResponse oneof ("task" member). GetTask/
// CancelTask return the bare Task instead; resultToTask accepts both, so the same
// helper serves every unary test (the bare shape has its own explicit test).
func rpcTaskResp(id, state string) []byte {
	return []byte(`{"jsonrpc":"2.0","id":"x","result":{"task":{"id":"` + id + `","contextId":"c","status":{"state":"` + state + `"}}}}`)
}

// verifiedDoer returns a stubDoer serving a signed (operator-anchored, trustVerified)
// card for agent + the given RPC response, plus the operator trust-anchor JWKS.
func verifiedDoer(t *testing.T, agent, rpc string) (*stubDoer, []byte) {
	t.Helper()
	priv, jwks := keypair(t, "k1")
	card := signedCardBytes(t, priv, "k1", baseCard(agent))
	return &stubDoer{cardBytes: card, rpcBytes: []byte(rpc)}, jwks
}

func billingAllowlist() *Allowlist {
	return NewAllowlist([]AllowRule{{Agent: "billing", Skill: "summarize", Scopes: []string{"reports:read"}}})
}

func okSpec() DelegateSpec {
	return DelegateSpec{
		AgentName: "billing", AgentURL: "https://billing.example.com",
		Skill: "summarize", Scope: "reports:read", Text: "summarize the Q2 report",
	}
}

// --- tests ----------------------------------------------------------------------

// TestDelegateGovernedSuccess: verified card + allowlist match + approved gate →
// delegated, bound to the PlanHash, audited allowed.
func TestDelegateGovernedSuccess(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	aud := &capAuditor{}
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
		Auditor:   aud,
	})
	res, err := d.Delegate(context.Background(), okSpec())
	if err != nil {
		t.Fatalf("expected delegation to succeed, got %v", err)
	}
	if res.State != TaskStateSubmitted {
		t.Errorf("state = %q, want SUBMITTED", res.State)
	}
	if doer.postCount != 1 {
		t.Errorf("expected exactly 1 SendMessage POST, got %d", doer.postCount)
	}
	last := aud.last()
	if !last.Allowed || last.Reason != "delegated" {
		t.Errorf("audit: got allowed=%v reason=%q, want allowed reason=delegated", last.Allowed, last.Reason)
	}
	want := PlanHash("billing", "summarize", "reports:read", hashParams("summarize", "", len(okSpec().Text)))
	if last.PlanHash != want {
		t.Errorf("audit plan hash = %q, want %q (delegation must be bound to the plan)", last.PlanHash, want)
	}
}

func TestDelegateBindsCallerParamsHash(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	gate := &fakeGate{status: StatusApproved}
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      gate,
	})
	spec := okSpec()
	spec.ParamsHash = "work-item-owner-fence-brief-criteria-digest"
	if _, err := d.Delegate(context.Background(), spec); err != nil {
		t.Fatalf("delegate with caller params hash: %v", err)
	}
	want := PlanHash(spec.AgentName, spec.Skill, spec.Scope, spec.ParamsHash)
	if gate.lastReq.PlanHash != want {
		t.Fatalf("plan hash = %q, want complete caller-bound hash %q", gate.lastReq.PlanHash, want)
	}
}

func TestDelegatorTestVerifiesWithoutEmission(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("unused", "TASK_STATE_SUBMITTED")))
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
	})
	spec := okSpec()
	spec.ParamsHash = "complete-work-plan"
	result, err := d.Test(context.Background(), spec)
	if err != nil {
		t.Fatalf("test delegation: %v", err)
	}
	if result.PlanHash != DelegationPlanHash(spec) || result.Trust != string(trustVerified) {
		t.Fatalf("test result = %+v, want exact verified plan", result)
	}
	if doer.postCount != 0 {
		t.Fatalf("non-actuating Test emitted %d POST request(s)", doer.postCount)
	}
}

// TestDelegateUnverifiedCardDenied: an unsigned card is NOT trustVerified → delegation
// refused, NOTHING emitted (deny-closed at verification).
func TestDelegateUnverifiedCardDenied(t *testing.T) {
	unsigned := mustJSON(t, baseCard("billing")) // no signatures
	doer := &stubDoer{cardBytes: unsigned, rpcBytes: rpcTaskResp("t1", "TASK_STATE_SUBMITTED")}
	_, jwks := keypair(t, "k1") // operator anchor present, but the card is unsigned
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
	})
	if _, err := d.Delegate(context.Background(), okSpec()); err == nil {
		t.Fatal("expected delegation to an unverified card to be DENIED")
	}
	if doer.postCount != 0 {
		t.Errorf("an unverified card must emit NOTHING, got %d POSTs", doer.postCount)
	}
}

// TestDelegateAllowlistDeny: a verified card whose (agent,skill,scope) is not on the
// allowlist → DenyError, nothing emitted, audited as a deny.
func TestDelegateAllowlistDeny(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	aud := &capAuditor{}
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: NewAllowlist([]AllowRule{{Agent: "billing", Skill: "translate", Scopes: []string{"x"}}}),
		Gate:      &fakeGate{status: StatusApproved},
		Auditor:   aud,
	})
	_, err := d.Delegate(context.Background(), okSpec())
	var de *DenyError
	if !errors.As(err, &de) {
		t.Fatalf("expected a DenyError for an unlisted skill, got %v", err)
	}
	if doer.postCount != 0 {
		t.Errorf("an allowlist deny must emit NOTHING, got %d POSTs", doer.postCount)
	}
	if aud.last().Allowed {
		t.Error("an allowlist deny must be audited as not-allowed")
	}
}

// TestDelegateGateNotApproved: allowlist allows but the gate is pending → not delegated.
func TestDelegateGateNotApproved(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusPending},
	})
	_, err := d.Delegate(context.Background(), okSpec())
	var de *DenyError
	if !errors.As(err, &de) {
		t.Fatalf("a pending gate must DENY the delegation, got %v", err)
	}
	if doer.postCount != 0 {
		t.Errorf("a non-approved gate must emit NOTHING, got %d POSTs", doer.postCount)
	}
}

// TestDelegateDenyClosedDefaultGate: no gate wired → denyDelegationGate → deny.
//
// It pins the REASON, not merely that something failed. `err != nil` on its own is
// satisfied by a transport fault, an unverifiable Agent Card or a JWKS that will not
// load — none of which say anything about the gate, yet every one of which would leave
// this test green while the deny-by-default path never ran. The product emits the
// distinction on purpose (pep.go: StatusNoGate, "It is NOT a silent no-op") and
// delegate.go carries it into DenyError.Reason, so the test asserts exactly that.
func TestDelegateDenyClosedDefaultGate(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		// Gate intentionally nil => deny-closed default.
	})
	_, err := d.Delegate(context.Background(), okSpec())
	if err == nil {
		t.Fatal("a Delegator with no gate must deny-closed")
	}
	// A policy refusal, not a transport error: DenyError is the typed seam for "the PEP
	// refused", so an infrastructure failure cannot masquerade as enforcement.
	var de *DenyError
	if !errors.As(err, &de) {
		t.Fatalf("a no-gate refusal must be a policy DenyError, got %T: %v", err, err)
	}
	// ...refused for THIS reason, matched EXACTLY. A containment check is not a contract
	// here: "no_gate" is a substring of any status that ends in it, and a gate returning
	// GateStatus("gate_no_gate") took a different path and still turned this test green.
	// GateStatus is an open string type whose only allow is "approved" (pep.go), so the
	// oracle has to be the whole reason, not a fragment of it.
	wantReason := "delegation not approved by governance (" + string(StatusNoGate) + ")"
	if de.Reason != wantReason {
		t.Fatalf("the refusal must read exactly %q so a different status cannot pass for it; got %q",
			wantReason, de.Reason)
	}
	// ...and it must come from the deny-closed DEFAULT. Without this, a wired gate that
	// merely reports no_gate would certify a default that was never installed — and this
	// test exists to certify the default.
	if _, isDefault := d.gate.(denyDelegationGate); !isDefault {
		t.Fatalf("this test certifies the deny-closed default; the delegator's gate is %T", d.gate)
	}
	if doer.postCount != 0 {
		t.Errorf("deny-closed gate must emit NOTHING, got %d POSTs", doer.postCount)
	}
}

// TestDelegateGateUnboundPlanDenied: an "approved" decision that is NOT bound to the
// computed plan (anti-TOCTOU) must still be refused.
func TestDelegateGateUnboundPlanDenied(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	// bindNil makes the gate echo an empty plan; but to truly test a MISMATCH we use a
	// gate that returns a different plan hash.
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &mismatchGate{},
	})
	_, err := d.Delegate(context.Background(), okSpec())
	var de *DenyError
	if !errors.As(err, &de) {
		t.Fatalf("an approval bound to the WRONG plan must DENY, got %v", err)
	}
	if doer.postCount != 0 {
		t.Errorf("a plan mismatch must emit NOTHING, got %d POSTs", doer.postCount)
	}
}

type mismatchGate struct{}

func (mismatchGate) Authorize(_ context.Context, req DelegationRequest) (GateDecision, error) {
	return GateDecision{ApprovalRef: "appr-x", Status: StatusApproved, PlanHash: "WRONG-PLAN-" + req.PlanHash}, nil
}

// TestDelegateUnspecifiedNotSuccess: a remote that returns TASK_STATE_UNSPECIFIED must
// NOT be treated as a delivered result.
func TestDelegateUnspecifiedNotSuccess(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_UNSPECIFIED")))
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
	})
	res, err := d.Delegate(context.Background(), okSpec())
	if err != nil {
		t.Fatalf("delegation should still complete the call: %v", err)
	}
	if taskSucceeded(res.State) {
		t.Error("TASK_STATE_UNSPECIFIED must never be treated as success")
	}
	if res.Terminal {
		t.Error("TASK_STATE_UNSPECIFIED is not terminal")
	}
}

// TestListTasksPageTokenPagination: ListTasks speaks the v1.0 wire shape — request
// pageToken/pageSize, response tasks/nextPageToken/totalSize (a2a.proto; NOT the
// cursor/nextCursor names the non-normative whats-new page shows).
func TestListTasksPageTokenPagination(t *testing.T) {
	listResp := `{"jsonrpc":"2.0","id":"x","result":{"tasks":[` +
		`{"id":"t1","status":{"state":"TASK_STATE_WORKING"}},` +
		`{"id":"t2","status":{"state":"TASK_STATE_COMPLETED"}}],"nextPageToken":"page-2","pageSize":2,"totalSize":7}}`
	doer, jwks := verifiedDoer(t, "billing", listResp)
	d := NewDelegator(DelegatorConfig{Emit: EmitConfig{TrustJWKS: jwks, Doer: doer}, Allowlist: billingAllowlist(), Gate: &fakeGate{status: StatusApproved}})
	page, err := d.ListTasks(context.Background(), ListSpec{AgentName: "billing", AgentURL: "https://billing.example.com", PageToken: "page-1", PageSize: 50})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(page.Tasks) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(page.Tasks))
	}
	if page.NextPageToken != "page-2" || page.TotalSize != 7 {
		t.Errorf("page meta = token %q total %d, want page-2 / 7", page.NextPageToken, page.TotalSize)
	}
	if page.Tasks[0].State != TaskStateWorking || page.Tasks[1].State != TaskStateCompleted {
		t.Errorf("task states = %q,%q", page.Tasks[0].State, page.Tasks[1].State)
	}
	// The request must use the v1.0 field names.
	body := string(doer.postBody)
	if !strings.Contains(body, `"pageToken":"page-1"`) || !strings.Contains(body, `"pageSize":50`) {
		t.Errorf("ListTasks request must carry pageToken/pageSize, got %s", body)
	}
	if strings.Contains(body, "cursor") {
		t.Errorf("ListTasks request must not use the non-normative cursor naming: %s", body)
	}
}

// TestGetTaskBareResultParses: GetTask returns the BARE Task object (rpc GetTask
// returns (Task), no oneof wrapper) — the bare shape must parse first-class.
func TestGetTaskBareResultParses(t *testing.T) {
	bare := `{"jsonrpc":"2.0","id":"x","result":{"id":"t1","contextId":"c","status":{"state":"TASK_STATE_WORKING"}}}`
	doer, jwks := verifiedDoer(t, "billing", bare)
	d := NewDelegator(DelegatorConfig{Emit: EmitConfig{TrustJWKS: jwks, Doer: doer}, Allowlist: billingAllowlist(), Gate: &fakeGate{status: StatusApproved}})
	res, err := d.GetTask(context.Background(), TaskRef{AgentName: "billing", AgentURL: "https://billing.example.com", TaskID: "t1"})
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if res.TaskID != "t1" || res.State != TaskStateWorking {
		t.Fatalf("bare Task result mishandled: %+v", res)
	}
}

// TestGetExtendedAgentCardGatedAndVerified: the extended card is requested only when
// the SIGNED base card advertises the extendedAgentCard capability (§3.3.4), with NO
// params member, and the RETURNED card's own signatures are verified and labeled.
func TestGetExtendedAgentCardGatedAndVerified(t *testing.T) {
	// Base card WITHOUT the capability → deny-closed CapabilityError, no RPC.
	doer, jwks := verifiedDoer(t, "billing", `{}`)
	d := NewDelegator(DelegatorConfig{Emit: EmitConfig{TrustJWKS: jwks, Doer: doer}, Allowlist: billingAllowlist(), Gate: &fakeGate{status: StatusApproved}})
	_, err := d.GetExtendedAgentCard(context.Background(), "billing", "https://billing.example.com")
	var ce *CapabilityError
	if !errors.As(err, &ce) {
		t.Fatalf("extended card without the capability must be a CapabilityError, got %v", err)
	}
	if doer.postCount != 0 {
		t.Fatalf("no RPC may be issued without the extendedAgentCard capability, got %d", doer.postCount)
	}

	// Base card WITH the capability → the call goes out without params and the
	// returned card's trust outcome is reported honestly (unsigned here).
	priv, jwks2 := keypair(t, "k1")
	base := baseCard("billing")
	base["capabilities"] = map[string]any{"streaming": true, "extendedAgentCard": true}
	signed := signedCardBytes(t, priv, "k1", base)
	extended := mustJSON(t, baseCard("billing")) // unsigned extended card
	doer2 := &stubDoer{cardBytes: signed, rpcBytes: []byte(`{"jsonrpc":"2.0","id":"x","result":` + string(extended) + `}`)}
	d2 := NewDelegator(DelegatorConfig{Emit: EmitConfig{TrustJWKS: jwks2, Doer: doer2}, Allowlist: billingAllowlist(), Gate: &fakeGate{status: StatusApproved}})
	ext, err := d2.GetExtendedAgentCard(context.Background(), "billing", "https://billing.example.com")
	if err != nil {
		t.Fatalf("GetExtendedAgentCard: %v", err)
	}
	if ext.Card.Name != "billing" {
		t.Errorf("extended card not decoded: %+v", ext.Card)
	}
	if ext.Trust != string(trustUnsigned) {
		t.Errorf("an unsigned extended card must be labeled unsigned, got %q", ext.Trust)
	}
	if strings.Contains(string(doer2.postBody), `"params"`) {
		t.Errorf("GetExtendedAgentCard must carry no params member, got %s", doer2.postBody)
	}
}

// TestReconcileIllegalTransition: a remote re-opening a terminal Task is rejected; the
// prior terminal state is kept.
func TestReconcileIllegalTransition(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_WORKING")))
	d := NewDelegator(DelegatorConfig{Emit: EmitConfig{TrustJWKS: jwks, Doer: doer}, Allowlist: billingAllowlist(), Gate: &fakeGate{status: StatusApproved}})
	prior := TaskResult{TaskID: "t1", State: TaskStateCompleted, Terminal: true}
	out, legal, err := d.Reconcile(context.Background(), prior, TaskRef{AgentName: "billing", AgentURL: "https://billing.example.com", TaskID: "t1"})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if legal {
		t.Error("re-opening a terminal Task must be flagged ILLEGAL")
	}
	if out.State != TaskStateCompleted {
		t.Errorf("illegal transition must keep the prior terminal state, got %q", out.State)
	}
}
