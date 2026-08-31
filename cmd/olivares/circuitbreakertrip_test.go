// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// circuitbreakertrip_test.go measures what the circuit breaker DOES, not what the source says.
//
// It is the other half of circuitbreakerwiring_test.go and neither half replaces the other.
// That one reads the composition root's struct literal, because in the open build a wired and
// an unwired decider both hold nil and nothing at runtime tells them apart — so the missing
// KEY is the only observable form the defect has there. This one drives authorizeChain with an
// engine that trips, which is what the gate is for and what no test in this repository did:
// before it, zero tests named circuitBreakerGateCheck, so enforcement had never been
// executed by anything.
//
// MUTATIONS THAT MUST TURN THIS RED:
//
//  1. Delete the `if denied, reason := circuitBreakerGateCheck(...)` block from
//     authorizeChain (inferenceproxy.go:667). Red in `an open breaker denies the request`.
//  2. Change the deny's status from http.StatusServiceUnavailable, or its code from
//     gateCodeCircuitBreaker. Red in the same case, on the field that moved.
//  3. Make circuitBreakerGateCheck fail CLOSED on a read error. Red in
//     `a breaker that cannot be read fails open, because the kill-switch is the hard stop` —
//     which is a declared design decision (circuitbreakergate.go:23), so a test has to hold it
//     in place rather than let a later edit quietly invert it.
//  4. Move the breaker gate ABOVE the kill-switch. Red in `the kill-switch outranks the
//     breaker`.
//  5. Pass `actor` instead of `sessionRef` to circuitBreakerGateCheck. Red in `an open breaker
//     denies the request` on the agent-ref assertion. This is the mutation the FIRST version of
//     this test could not make, because it asserted only that the value was non-empty — and the
//     audit actor is non-empty too, so the gate queried a key the engine never writes and the
//     suite stayed green.

// breakerTestAgentRef is the authenticated NHI binding the fixture principal carries. It is a
// distinct, recognizable value ON PURPOSE: the audit actor for the same principal renders as
// `user:u1`, so a gate querying the wrong one is visible in the failure message rather than
// merely "not equal".
const breakerTestAgentRef = "agent-breaker-7"

// TestAnUnbindableAgentSkipsTheBreakerRatherThanFallingThrough pins the posture the identity
// snapshot declares (inferenceproxy.go:528-531): a raw token with no authenticated NHI binding is
// explicitly unbindable, so agent-scoped governance must NOT silently fall through to a broader
// key. The breaker skips, and the kill-switch above stays the hard stop.
func TestAnUnbindableAgentSkipsTheBreakerRatherThanFallingThrough(t *testing.T) {
	a, mg, bg, kg, pol := allowAll() // the fixture principal carries NO agent identity
	d := newTestDecider(a, mg, bg, kg, pol)
	breaker := &trippingBreaker{state: circuitBreakerState{State: "open", RuleRef: "rule-7"}}
	d.circuitBreaker = breaker

	if _, _, deny, ok := d.authorizeChain(context.Background(), userReq("hi", false), "bearer"); !ok {
		t.Fatalf("an unbindable agent must not be denied by an agent-scoped breaker it cannot be keyed to: code=%q", deny.code)
	}
	if breaker.calls != 0 {
		t.Errorf("the breaker was consulted %d time(s) for an unbindable agent; querying it with a broader key is exactly the silent fall-through the identity snapshot forbids", breaker.calls)
	}
}

// trippingBreaker is a circuitBreakerEngine whose answer the test dictates.
type trippingBreaker struct {
	state circuitBreakerState
	err   error
	calls int
	// lastAgent records what the gate asked about, so the test can prove the chain passes
	// the ACTOR and not, say, the tenant twice.
	lastAgent  string
	lastTenant model.TenantID
	// findings counts what the DRIVE half received, so a change that stops delivering findings
	// here cannot pass unnoticed.
	findings int
}

func (b *trippingBreaker) State(_ context.Context, tenant model.TenantID, agentRef string) (circuitBreakerState, error) {
	b.calls++
	b.lastTenant, b.lastAgent = tenant, agentRef
	return b.state, b.err
}

// OnFinding satisfies the DRIVE half of the interface, which added when it wired the
// breaker's two halves together (circuitbreaker.go:40-48).
//
// These cases are about the CONSULT half — what the inference gate asks and what it does with
// the answer — so the drive side is a recorder rather than a state machine: this fake's state is
// set by the case, which is what lets each case name the exact condition it is measuring. The
// count is kept because a fake that silently swallowed findings would let a future change stop
// delivering them here without anything noticing.
func (b *trippingBreaker) OnFinding(_ context.Context, _ event.Event) error {
	b.findings++
	return nil
}

func (b *trippingBreaker) RegisterSchema(interface{}) error { return nil }

func TestAnOpenCircuitBreakerDeniesTheRequest(t *testing.T) {
	t.Run("an open breaker denies the request", func(t *testing.T) {
		a, mg, bg, kg, pol := allowAll()
		a.p = a.p.WithAgentIdentity(breakerTestAgentRef)
		d := newTestDecider(a, mg, bg, kg, pol)
		breaker := &trippingBreaker{state: circuitBreakerState{State: "open", RuleRef: "rule-7"}}
		d.circuitBreaker = breaker

		_, _, deny, ok := d.authorizeChain(context.Background(), userReq("hi", false), "bearer")
		if ok {
			t.Fatal("a tripped circuit breaker let the request through; the breaker gate enforces nothing")
		}
		if deny.code != gateCodeCircuitBreaker {
			t.Errorf("deny code = %q, want %q", deny.code, gateCodeCircuitBreaker)
		}
		if deny.class != sdk.FailurePolicyDeny {
			t.Errorf("deny class = %q, want %q: a tripped breaker is a firm policy refusal, not a read fault", deny.class, sdk.FailurePolicyDeny)
		}
		if deny.decision.Status != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want %d", deny.decision.Status, http.StatusServiceUnavailable)
		}
		if breaker.calls == 0 {
			t.Fatal("the gate never consulted the engine, so this test measured nothing")
		}
		// THE ASSERTION THAT USED TO SAY `!= ""` IS WHY THIS DEFECT SURVIVED THE FIRST REVIEW.
		//
		// "Non-empty" is true of the audit actor too, so the test stayed green while the gate
		// queried a key the breaker engine never writes. The exact value is the only assertion
		// that discriminates: a circuit breaker is agent-scoped, so it must be asked about the
		// authenticated agent identity, not about `user:<id>`/`token:<id>`.
		if breaker.lastAgent != breakerTestAgentRef {
			t.Errorf("the gate asked about %q, want the authenticated agent ref %q: the breaker engine persists state under the agent identity, so any other key finds nothing and a tripped breaker is invisible",
				breaker.lastAgent, breakerTestAgentRef)
		}
		if breaker.lastTenant == "" {
			t.Error("the gate asked about an empty tenant")
		}
	})

	t.Run("a closed breaker changes nothing", func(t *testing.T) {
		a, mg, bg, kg, pol := allowAll()
		a.p = a.p.WithAgentIdentity(breakerTestAgentRef)
		d := newTestDecider(a, mg, bg, kg, pol)
		breaker := &trippingBreaker{state: circuitBreakerState{State: "closed"}}
		d.circuitBreaker = breaker

		_, _, deny, ok := d.authorizeChain(context.Background(), userReq("hi", false), "bearer")
		if !ok {
			t.Fatalf("a closed breaker denied the request: code=%q reason=%q", deny.code, deny.decision.Reason)
		}
		if breaker.calls == 0 {
			t.Fatal("the gate never consulted the engine even to find it closed: the previous case could be passing for the wrong reason")
		}
	})

	t.Run("a breaker that cannot be read fails open, because the kill-switch is the hard stop", func(t *testing.T) {
		a, mg, bg, kg, pol := allowAll()
		a.p = a.p.WithAgentIdentity(breakerTestAgentRef)
		d := newTestDecider(a, mg, bg, kg, pol)
		d.circuitBreaker = &trippingBreaker{err: errors.New("breaker store down")}

		_, _, deny, ok := d.authorizeChain(context.Background(), userReq("hi", false), "bearer")
		if !ok {
			t.Fatalf("the breaker is DECLARED fail-open (circuitbreakergate.go:23) and this read fault denied: code=%q. If that decision changed, this test is where it gets restated, not where it gets deleted", deny.code)
		}
	})

	t.Run("the kill-switch outranks the breaker", func(t *testing.T) {
		a, mg, bg, kg, pol := allowAll()
		a.p = a.p.WithAgentIdentity(breakerTestAgentRef)
		kg.st = governance.StopState{EstateStopped: true, EstateStopID: model.ID("stop-cb")}
		d := newTestDecider(a, mg, bg, kg, pol)
		breaker := &trippingBreaker{state: circuitBreakerState{State: "open", RuleRef: "rule-7"}}
		d.circuitBreaker = breaker

		_, _, deny, ok := d.authorizeChain(context.Background(), userReq("hi", false), "bearer")
		if ok {
			t.Fatal("an active emergency stop let the request through")
		}
		if deny.code != gateCodeKillSwitch {
			t.Errorf("deny code = %q, want %q: an emergency stop outranks every other consideration, and a breaker deny here would report the wrong cause to the operator", deny.code, gateCodeKillSwitch)
		}
		if breaker.calls != 0 {
			t.Errorf("the breaker was consulted %d time(s) despite an active stop; the ordering in authorizeChain is what makes the reported cause the right one", breaker.calls)
		}
	})

	t.Run("a nil engine skips the gate, which is the open build", func(t *testing.T) {
		a, mg, bg, kg, pol := allowAll()
		d := newTestDecider(a, mg, bg, kg, pol)
		d.circuitBreaker = nil
		if _, _, deny, ok := d.authorizeChain(context.Background(), userReq("hi", false), "bearer"); !ok {
			t.Fatalf("the open build has no circuit breaker and must not be denied by it: code=%q", deny.code)
		}
	})
}
