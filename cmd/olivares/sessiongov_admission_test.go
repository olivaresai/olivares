// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// SG-02-b, the composition — and precisely how far this reaches, because the first
// version of this comment claimed more than the test earns.
//
// It exercises buildSessionLaunchGate, the constructor wireSessionGovernance calls,
// and its mutant is one line: make that function `return inner`, and this goes red.
// What it does NOT cover is the CALL SITE: a mutant that hands the inner gate
// straight to UseLaunchGate, bypassing this constructor, leaves this test green.
// A contrast measured that, so it is written here rather than implied away. Closing
// it needs an assertion driven through wireSessionGovernance itself — pack SG-02-f.
func TestBuildSessionLaunchGate_ComposesClaimAdmission(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	m := h.set.sessions
	if m == nil {
		t.Fatal("the harness has no sessions module")
	}
	tenant := model.TenantID(h.tenantA)

	// A session, claimed by somebody. This is the estate's own state, not a fixture
	// the gate was handed.
	sid, err := m.ResolveSession(ctx, tenant, sessions.SessionBinding{
		Provider: sessions.ProviderOperated, ExternalID: "run-composed", Origin: sessions.OriginOperated,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	lease, err := m.Claim(ctx, tenant, sid, "user:holder", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	reached := 0
	inner := launchGateStub(func() { reached++ })
	gate := buildSessionLaunchGate(h.set, inner)

	// The holder itself passes through to the inner gate.
	dec, err := gate.Authorize(ctx, tenant, sessions.LaunchIntent{
		RunRef: "run-composed", ClaimSID: sid, Holder: "user:holder", Fence: lease.Fence,
	})
	if err != nil || !dec.Allowed {
		t.Fatalf("the actual holder was refused: allowed=%v err=%v", dec.Allowed, err)
	}
	if reached != 1 {
		t.Fatalf("the inner gate ran %d times for an admissible launch, want 1", reached)
	}

	// A launcher riding somebody else's claim does not, and never reaches the inner
	// gate on its way to being refused.
	dec, err = gate.Authorize(ctx, tenant, sessions.LaunchIntent{
		RunRef: "run-composed", ClaimSID: sid, Holder: "user:intruder", Fence: lease.Fence,
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if dec.Allowed {
		t.Fatal("production's composed gate ADMITTED a launcher that holds nothing — the admission decorator is not wired")
	}
	if reached != 1 {
		t.Errorf("the inner gate ran for an inadmissible launch (%d calls total)", reached)
	}

	// And a launcher that names nobody is refused too.
	if dec, _ := gate.Authorize(ctx, tenant, sessions.LaunchIntent{
		RunRef: "run-composed", ClaimSID: sid,
	}); dec.Allowed {
		t.Error("production's composed gate admitted a launch with no holder")
	}
}

// launchGateStub counts the times the inner gate is reached.
type launchGateStub func()

func (f launchGateStub) Authorize(context.Context, model.TenantID, sessions.LaunchIntent) (sessions.LaunchDecision, error) {
	f()
	return sessions.LaunchDecision{Allowed: true}, nil
}

// brokenBridge is an approval bridge whose ERROR channel fires. It is not a bridge
// that says no — that distinction is the whole point of the tests below.
//
// The two failures are separate fields because they are separate call sites, and one
// field could not reach both: gateOnce returns on its own error, so a fake that always
// failed could never exercise the consume path at all. Separate fields are what makes
// each path configurable — not, as this comment first claimed, what makes a regression
// in either detectable.
type brokenBridge struct {
	openErr    error
	consumeErr error
	status     string
}

func (b brokenBridge) gateOnce(context.Context, model.TenantID, string, string, string, string, string, string) (string, string, string, error) {
	if b.openErr != nil {
		return "", "", "", b.openErr
	}
	return "appr-1", b.status, "", nil
}

func (b brokenBridge) consumeApproval(context.Context, model.TenantID, string, string, string) (bool, bool, error) {
	return false, false, b.consumeErr
}

// P1-R6-01, the cmd side. The launch is STILL DENIED — that is asserted below and is
// not in question — but a bridge whose error channel fired used to be denied with the
// same 403 a policy refusal gets, which tells an operator that a broken bridge is a
// decision and not to retry it. The seam for saying otherwise already existed
// (DeniedStatus, used by the budget and context controls); the HITL path was not
// using it.
func TestGateCriticalLaunch_ABridgeErrorIsDeniedAsAnOutageNotAsAVerdict(t *testing.T) {
	ctx := context.Background()
	g := &sessionLaunchGate{bridge: brokenBridge{openErr: errors.New("approval store unreachable")}}
	intent := sessions.LaunchIntent{
		RunRef: "run-hitl", Actor: "user:u1", ActorKind: model.ActorUser,
		PermissionMode: "bypassPermissions",
	}

	_, dec, ok := g.gateCriticalLaunch(ctx, model.TenantID("t"), intent, "bypassPermissions")
	if ok {
		t.Fatal("a privileged launch was allowed while its approval bridge was unreachable")
	}
	if dec.Allowed {
		t.Fatal("the decision allows the launch")
	}
	if dec.DeniedStatus != http.StatusServiceUnavailable {
		t.Errorf("DeniedStatus = %d, want 503: the bridge did not decide, it broke", dec.DeniedStatus)
	}
}

// And the line the fix must not cross: a bridge that is NOT WIRED is a configuration
// verdict, not an outage, and stays a 403.
func TestGateCriticalLaunch_AnUnwiredBridgeStaysForbidden(t *testing.T) {
	g := &sessionLaunchGate{bridge: nil}
	_, dec, ok := g.gateCriticalLaunch(context.Background(), model.TenantID("t"),
		sessions.LaunchIntent{RunRef: "run-hitl-2", PermissionMode: "bypassPermissions"}, "bypassPermissions")
	if ok || dec.Allowed {
		t.Fatal("a privileged launch was allowed with no approval bridge wired")
	}
	if dec.DeniedStatus != 0 && dec.DeniedStatus != http.StatusForbidden {
		t.Errorf("DeniedStatus = %d, want the forbidden default: this is configuration, not an outage",
			dec.DeniedStatus)
	}
}

// The SECOND call site, and it needed its own test: a consume that fails happens
// AFTER a human approved, so an isolated regression there would have survived the
// test above — which never reaches it.
func TestGateCriticalLaunch_AConsumeErrorIsDeniedAsAnOutageNotAsAVerdict(t *testing.T) {
	g := &sessionLaunchGate{bridge: brokenBridge{
		status: nbApproved, consumeErr: errors.New("approval store unreachable"),
	}}
	_, dec, ok := g.gateCriticalLaunch(context.Background(), model.TenantID("t"),
		sessions.LaunchIntent{RunRef: "run-hitl-3", PermissionMode: "bypassPermissions"}, "bypassPermissions")

	if ok || dec.Allowed {
		t.Fatal("a privileged launch was allowed when its approval could not be spent")
	}
	if dec.DeniedStatus != http.StatusServiceUnavailable {
		t.Errorf("DeniedStatus = %d, want 503: the consume returned an error, which says "+
			"nothing usable about the approval's state", dec.DeniedStatus)
	}
}
