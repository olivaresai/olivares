// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// classDenyEval is a PolicyEvaluator that returns a fixed DENY with a chosen
// provenance class and reason, optionally recording that it was evaluated.
type classDenyEval struct {
	class  auth.DecisionClass
	reason string
	called *bool
}

func (e classDenyEval) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	if e.called != nil {
		*e.called = true
	}
	return auth.Decision{Allow: false, Reason: e.reason, Class: e.class}, nil
}

// E1b — "invariant dominates". The chain must not stop at the first policy
// deny: a later platform-invariant deny must win, or observe would shadow the first
// policy deny and silently drop the invariant.
func TestChainInvariantDominatesLaterMember(t *testing.T) {
	ch := composeEvaluators(nil,
		classDenyEval{class: auth.ClassPolicy, reason: "abac policy"},
		classDenyEval{class: auth.ClassInvariant, reason: "opa invariant"})
	dec, err := ch.Evaluate(context.Background(), pdpReq())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if dec.Allow {
		t.Fatal("chain must deny")
	}
	if dec.Class == auth.ClassPolicy {
		t.Fatalf("a later invariant must dominate the earlier policy deny, got policy: %q", dec.Reason)
	}
	if dec.Reason != "opa invariant" {
		t.Fatalf("chain must return the invariant deny, got %q", dec.Reason)
	}
}

// With no invariant in the chain, the FIRST policy deny is returned (order-stable),
// and its ClassPolicy provenance survives so observe can shadow it.
func TestChainReturnsFirstPolicyDenyWhenNoInvariant(t *testing.T) {
	ch := composeEvaluators(nil,
		classDenyEval{class: auth.ClassPolicy, reason: "abac policy"},
		classDenyEval{class: auth.ClassPolicy, reason: "cedar policy"})
	dec, _ := ch.Evaluate(context.Background(), pdpReq())
	if dec.Class != auth.ClassPolicy {
		t.Fatalf("want ClassPolicy, got %d (%q)", dec.Class, dec.Reason)
	}
	if dec.Reason != "abac policy" {
		t.Fatalf("want the FIRST policy deny, got %q", dec.Reason)
	}
}

// An invariant deny short-circuits: nothing can override it, so later members are
// not evaluated (correctness + no wasted external PDP calls).
func TestChainInvariantShortCircuits(t *testing.T) {
	secondCalled := false
	ch := composeEvaluators(nil,
		classDenyEval{class: auth.ClassInvariant, reason: "kill-switch"},
		classDenyEval{class: auth.ClassPolicy, reason: "abac policy", called: &secondCalled})
	dec, _ := ch.Evaluate(context.Background(), pdpReq())
	if dec.Reason != "kill-switch" {
		t.Fatalf("an invariant deny must win immediately, got %q", dec.Reason)
	}
	if secondCalled {
		t.Fatal("chain must short-circuit on an invariant deny (a later member was evaluated)")
	}
}

// An allow before a policy deny must not flatten the surviving class to invariant.
func TestChainPreservesPolicyClassThroughAllow(t *testing.T) {
	ch := composeEvaluators(nil,
		stubEval{allow: true},
		classDenyEval{class: auth.ClassPolicy, reason: "abac policy"})
	dec, _ := ch.Evaluate(context.Background(), pdpReq())
	if dec.Allow {
		t.Fatal("chain must deny")
	}
	if dec.Class != auth.ClassPolicy {
		t.Fatalf("policy class must survive an earlier allow, got %d", dec.Class)
	}
}

// A deny with no explicit class (zero value) is treated as invariant-dominant, per
// the fail-safe contract (shadowability is keyed on == ClassPolicy, never on
// != ClassInvariant). It must dominate an earlier explicit policy deny.
func TestChainUnclassifiedDenyDominates(t *testing.T) {
	ch := composeEvaluators(nil,
		classDenyEval{class: auth.ClassPolicy, reason: "abac policy"},
		stubEval{allow: false}) // stubEval deny has zero Class
	dec, _ := ch.Evaluate(context.Background(), pdpReq())
	if dec.Class == auth.ClassPolicy {
		t.Fatalf("an unclassified (zero) deny must dominate a policy deny, got policy: %q", dec.Reason)
	}
	if dec.Reason != "stub" {
		t.Fatalf("want the unclassified deny, got %q", dec.Reason)
	}
}

// onDeny must fire EXACTLY ONCE with the EFFECTIVE (returned) decision — the
// dominating invariant, not the earlier policy deny — so the ledger records what
// actually enforced, with no double entry. The call count is asserted so a regression
// that also audited the remembered firstPolicyDeny would fail (last-write-wins alone
// would not catch it).
func TestChainOnDenyReceivesEffectiveDecisionExactlyOnce(t *testing.T) {
	var got auth.Decision
	calls := 0
	ch := composeEvaluators(func(_ auth.Request, d auth.Decision) { got = d; calls++ },
		classDenyEval{class: auth.ClassPolicy, reason: "abac policy"},
		classDenyEval{class: auth.ClassInvariant, reason: "opa invariant"})
	if _, err := ch.Evaluate(context.Background(), pdpReq()); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if calls != 1 {
		t.Fatalf("onDeny must fire exactly once, fired %d times", calls)
	}
	if got.Reason != "opa invariant" {
		t.Fatalf("onDeny must receive the effective invariant deny, got %q", got.Reason)
	}
}

// The end-of-loop "first policy deny" branch has its OWN onDeny call site (distinct
// from the invariant-immediate branch). It must fire exactly once with the FIRST
// policy deny — the branch that is new to E1b (remember-and-continue) and the most
// regression-prone, so it needs live-hook coverage of its own.
func TestChainOnDenyFirstPolicyBranchExactlyOnce(t *testing.T) {
	var got auth.Decision
	calls := 0
	ch := composeEvaluators(func(_ auth.Request, d auth.Decision) { got = d; calls++ },
		classDenyEval{class: auth.ClassPolicy, reason: "first policy"},
		classDenyEval{class: auth.ClassPolicy, reason: "second policy"})
	if _, err := ch.Evaluate(context.Background(), pdpReq()); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if calls != 1 {
		t.Fatalf("onDeny must fire exactly once on the first-policy branch, fired %d times", calls)
	}
	if got.Reason != "first policy" {
		t.Fatalf("onDeny must receive the FIRST policy deny, got %q", got.Reason)
	}
}

// A member ERROR fails closed early and must NOT invoke onDeny (there is no decision
// to record; the fail-closed deny is audited upstream by the Authorizer/governance
// layer). This pins the intentional asymmetry so a future change can't silently start
// (or stop) auditing the error path through this hook.
func TestChainMemberErrorDoesNotInvokeOnDeny(t *testing.T) {
	calls := 0
	boom := errors.New("pdp down")
	ch := composeEvaluators(func(auth.Request, auth.Decision) { calls++ },
		stubEval{allow: true}, stubEval{err: boom})
	if _, err := ch.Evaluate(context.Background(), pdpReq()); !errors.Is(err, boom) {
		t.Fatalf("chain must propagate the member error (fail closed), got %v", err)
	}
	if calls != 0 {
		t.Fatalf("onDeny must not fire on a member error, fired %d times", calls)
	}
}

// The security-critical [policy, error] ordering: a member ERROR after a remembered
// policy deny must DOMINATE — the chain returns the error (fail-closed, the Authorizer
// denies invariant), never the shadowable policy deny. A transient PDP/OPA failure must
// not be silently downgraded to a business deny that observe could shadow.
func TestChainMemberErrorDominatesRememberedPolicy(t *testing.T) {
	calls := 0
	boom := errors.New("opa down")
	dec, err := composeEvaluators(func(auth.Request, auth.Decision) { calls++ },
		classDenyEval{class: auth.ClassPolicy, reason: "abac policy"},
		stubEval{err: boom}).Evaluate(context.Background(), pdpReq())
	if !errors.Is(err, boom) {
		t.Fatalf("a later error must dominate the remembered policy deny (fail-closed), got dec=%+v err=%v", dec, err)
	}
	if calls != 0 {
		t.Fatalf("onDeny must not fire when the chain fails closed on an error, fired %d times", calls)
	}
}

// An explicit UNKNOWN, non-zero DecisionClass must dominate a policy deny: shadowability
// is keyed on == ClassPolicy, never on != ClassInvariant, so a future/unknown enum value
// stays non-shadowable (fail-safe forward-compatibility).
func TestChainUnknownNonZeroClassDominates(t *testing.T) {
	ch := composeEvaluators(nil,
		classDenyEval{class: auth.ClassPolicy, reason: "abac policy"},
		classDenyEval{class: auth.DecisionClass(42), reason: "unknown-future"})
	dec, _ := ch.Evaluate(context.Background(), pdpReq())
	if dec.Class == auth.ClassPolicy {
		t.Fatalf("an unknown non-zero class must dominate a policy deny, got policy: %q", dec.Reason)
	}
	if dec.Reason != "unknown-future" {
		t.Fatalf("want the unknown-class deny to win, got %q", dec.Reason)
	}
}
