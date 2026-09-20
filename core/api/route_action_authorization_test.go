// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// ⛔ THE VALUE THE ENGINE ALREADY WIRES AS THE ROW PORT MUST CARRY THE ACTION PORT. A consumer
// reaches the one-action question by a CHECKED ASSERTION on the port it already holds, so a method
// that landed on some other type would leave every such consumer finding nothing and withholding
// the decision — a refusal wearing the shape of an absent feature, which is the one failure a
// reader cannot tell from "not wired yet".
//
// ⚠ AND IT IS CHECKED AT PACKAGE INITIALISATION, NOT BY THE COMPILER, said here rather than
// implied: the constructor's static result is an interface, so an interface-to-interface
// assignment would not compile even when the method is present. This is the strongest form
// available from outside the package, and it fails the whole binary rather than one case.
var _ api.RouteActionAuthorizationPort = api.NewReadRowAuthorizationPort(nil, nil).(api.RouteActionAuthorizationPort)

// TestAuthorizeActionKeepsUndecidedApartFromDenied measures the property a gate needs and an offer
// list does not: THREE ANSWERS, told apart all the way to the caller.
//
// ⛔ A DENIAL AND AN UNDECIDED ARE NEVER INTERCHANGEABLE. "The policy says no" and "nothing
// evaluated it" have different remedies — one client must stop, the other must retry — and a port
// that collapses them hands the caller a verdict the engine never reached. Collapsed the other
// way round it is worse: an absence read as a permit.
//
// ⛔ AND THE EXISTING OFFER LIST IS A CONTROL HERE, NOT A DEFECT TO CURE. Many-actions-over-one-
// resource drops an action it cannot decide and continues, which is right for a LIST OF BUTTONS —
// an absent button is the safe default and the caller can still attempt it — and wrong for a GATE,
// where the absence is the whole answer. The last case below pins that behaviour so nobody
// "fixes" it into an error on the strength of the four cases above it.
func TestAuthorizeActionKeepsUndecidedApartFromDenied(t *testing.T) {
	// The uninstalled port needs no fixture at all: a port whose authorizer was never wired must
	// say "I could not look" and must not pass for either of the other two answers.
	t.Run("no authorizer installed", func(t *testing.T) {
		port, ok := api.NewRowAuthorizationPort(nil).(api.RouteActionAuthorizationPort)
		if !ok {
			t.Fatal("the row port does not carry the action port, so no consumer can reach it")
		}
		witness, err := port.AuthorizeAction(t.Context(), auth.Principal{}, "t1",
			auth.ResourceAttrs{Kind: "agent", ID: "a"},
			api.RouteActionRequirement{Permission: "agent:read"})
		if err == nil {
			t.Fatal("a port with no authorizer authorized an action: an uninstalled dependency " +
				"must fail toward refusing, and it must refuse by saying nothing was evaluated")
		}
		if !errors.Is(err, auth.ErrAuthorizerUnavailable) {
			t.Errorf("the error is not the 'could not look' one: %v", err)
		}
		if errors.Is(err, auth.ErrRouteDenied) || errors.Is(err, auth.ErrRouteUndecided) {
			t.Errorf("an absent authorizer also reads as a policy answer: %v", err)
		}
		if witness.Allows(time.Now()) {
			t.Error("a refused decision carried a witness that authorizes")
		}
	})

	policy := &decisionHorizonPolicy{}
	var az *auth.Authorizer
	h := newHarnessOpts(t, func(o *api.Options) {
		policy.store = o.Store
		az = auth.NewAuthorizer(policy)
		o.Authorizer = az
	})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "route-action-port")
	token := witnessTenantPrincipal(t, h, admin, tenant) // a VIEWER in this tenant

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ⛔ TWO PRINCIPALS, AND THE DIFFERENCE BETWEEN THEM IS THE WHOLE THIRD ANSWER. `authenticated`
	// is who the caller is; `reconstructed` is that same caller with a sealed, windowed authority
	// fact beside them. The evidence path REQUIRES the second, so the first produces a decision
	// that could not be established — not a denial, because nothing said no.
	authenticated, err := h.authr.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("authenticate the tenant member: %v", err)
	}
	wired := api.NewReadRowAuthorizationPort(az, h.authr)
	port, ok := wired.(api.RouteActionAuthorizationPort)
	if !ok {
		t.Fatal("the value the engine already wires as the row port does not carry the action " +
			"port: a consumer reaching it by a checked assertion finds nothing")
	}
	ctx, reconstructed, err := wired.RefreshReadPrincipal(ctx, authenticated, tenant)
	if err != nil {
		t.Fatalf("reconstruct the principal's authority: %v", err)
	}
	if role, ok := reconstructed.RoleIn(tenant); !ok || role != auth.RoleViewer {
		t.Fatalf("reconstructed role = %q/%t, want the fixture's %q", role, ok, auth.RoleViewer)
	}

	const perm auth.Permission = "agent:read"
	resource := auth.ResourceAttrs{Kind: "agent", ID: model.NewID().String()}
	permitted := api.RouteActionRequirement{Permission: perm}
	// ⛔ THE METADATA COMES FROM THE ACTION'S OWN ROUTE, so this is the shape of an action whose
	// route asks for a role floor the fixture's viewer does not reach. That is a decision the
	// policy TOOK, which is exactly what makes it a denial and not an absence.
	forbidden := api.RouteActionRequirement{Permission: perm, Metadata: api.RouteMetadata{
		RouteMetadata: auth.RouteMetadata{CedarAction: "session:read", RBACMinimumRole: auth.RoleEditor},
	}}

	t.Run("the policy forbids", func(t *testing.T) {
		witness, err := port.AuthorizeAction(ctx, reconstructed, tenant, resource, forbidden)
		if err == nil {
			t.Fatal("an action whose route the caller's role does not reach was authorized")
		}
		if !errors.Is(err, auth.ErrRouteDenied) {
			t.Fatalf("a policy denial did not arrive as the denial: %v", err)
		}
		if errors.Is(err, auth.ErrRouteUndecided) || errors.Is(err, auth.ErrAuthorizerUnavailable) {
			t.Errorf("a denial also reads as 'could not establish', so a caller cannot tell the "+
				"answer that must not be retried from the one that must: %v", err)
		}
		if witness.Allows(time.Now()) {
			t.Error("a denial carried a witness that authorizes")
		}
	})

	t.Run("the decision cannot be established", func(t *testing.T) {
		witness, err := port.AuthorizeAction(ctx, authenticated, tenant, resource, permitted)
		if err == nil {
			t.Fatal("an action nobody could decide was AUTHORIZED: the authority behind this " +
				"caller was never reconstructed, so no evaluation happened at all")
		}
		if !errors.Is(err, auth.ErrRouteUndecided) {
			t.Fatalf("the third answer did not arrive as itself: %v", err)
		}
		if errors.Is(err, auth.ErrRouteDenied) {
			t.Errorf("an undecided decision also reads as a denial: a caller mapping these to "+
				"the wire would answer 'you may not' where nothing was evaluated: %v", err)
		}
		if witness.Allows(time.Now()) {
			t.Error("an undecided decision carried a witness that authorizes")
		}
	})

	t.Run("the policy permits", func(t *testing.T) {
		witness, err := port.AuthorizeAction(ctx, reconstructed, tenant, resource, permitted)
		if err != nil {
			t.Fatalf("a permitted action was refused: %v\nWithout this case the three refusals "+
				"above are satisfied by a port that can only ever refuse.", err)
		}
		// ⛔ "NON-ZERO" IS ASSERTED AS THE PROPERTY THAT MAKES A WITNESS WORTH HOLDING, not as a
		// field comparison: the zero value was minted by nobody and answers no question, so the
		// whole check refuses it. Comparing the struct against its zero value is not available —
		// it carries a slice — and would be the weaker claim even if it were.
		question := auth.Request{
			Principal: reconstructed, Permission: perm, Tenant: tenant, Resource: resource,
			Route: permitted.Metadata.RouteMetadata,
		}
		if !witness.VerifyFor(time.Now(), question) {
			t.Fatal("the port permitted and returned a witness that does not answer the question " +
				"it was asked: a field that arrives without binding its question reads like an " +
				"authorization and is not one")
		}
	})

	t.Run("the offer list still offers", func(t *testing.T) {
		// THE SAME undecided action, through the many-actions path, must still report an offer
		// set without it AND no error.
		set, err := wired.DecideActions(ctx, authenticated, tenant, resource,
			[]api.RouteActionRequirement{permitted})
		if err != nil {
			t.Fatalf("the offer list turned an undecided action into an error: %v\nIts callers "+
				"rely on an absent button being the safe default; the gate is the new port, and "+
				"curing one by breaking the other trades a leak for an outage.", err)
		}
		if len(set.Actions) != 0 || len(set.Witnesses) != 0 {
			t.Fatalf("an action nobody could decide was OFFERED: %+v", set)
		}
	})
}
