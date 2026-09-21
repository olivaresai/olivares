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
	//
	// ⚠ AND IT NAMES NO CEDAR ACTION, WHICH IT USED TO. Naming one asks WHOSE action it is — a
	// question answered by attribution and not by policy — and this case is about the denial. The
	// case that measures attribution is TestAuthorizeActionRefusesAnActionItsCallerCannotName.
	forbidden := api.RouteActionRequirement{Permission: perm, Metadata: api.RouteMetadata{
		RouteMetadata: auth.RouteMetadata{RBACMinimumRole: auth.RoleEditor},
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

// actionPortFixture is the live engine behind the gate: a real store, a real authenticator, a real
// authorizer, and a tenant member whose authority CAN be reconstructed.
//
// ⛔ IT CARRIES BOTH PRINCIPALS ON PURPOSE. `reconstructed` is the caller with a sealed, windowed
// authority fact beside them; `authenticated` is that same caller without one. The difference
// between the two IS the third answer, and a fixture that built only the first could not produce
// an undecided decision at all.
type actionPortFixture struct {
	ctx           context.Context
	port          api.RouteActionAuthorizationPort
	tenant        model.TenantID
	reconstructed auth.Principal
	authenticated auth.Principal
}

func newActionPortFixture(t *testing.T, slug string) actionPortFixture {
	t.Helper()
	policy := &decisionHorizonPolicy{}
	return newActionPortFixtureWith(t, slug, func(o *api.Options) *auth.Authorizer {
		policy.store = o.Store
		return auth.NewAuthorizer(policy)
	})
}

// scopedGrantEngine is a scoped engine that GRANTS, paired with authoredForbidPolicy below.
//
// ⛔ ITS BOOLEAN ARM IS DELIBERATELY INERT. Scoped abstains, so the ordinary routes this fixture's
// own setup drives — create the tenant, create a member, log in — decide exactly as they do with no
// scoped engine wired at all. The EVIDENCE arm is the one the governed path reads and the only arm
// these two doubles are about; a double that also moved the boolean path would be measuring its own
// fixture.
//
// ⛔ AND A GRANT COMES WITH TWO CLEAN PREDICATES, which is not a style choice: the engine refuses a
// grant whose resource guard or forbid absence is anything else, and a refused contribution
// degrades to UNKNOWN — an undecided decision, not the denial this fixture exists to produce.
type scopedGrantEngine struct{}

func (scopedGrantEngine) Scoped(context.Context, auth.Request) (auth.ScopedDecision, error) {
	return auth.ScopedDecision{Effect: auth.EffectAbstain, Reason: "fixture: no opinion on the boolean path"}, nil
}

func (scopedGrantEngine) ScopedEvidence(context.Context, auth.Request) (auth.ScopedEvidenceDecision, error) {
	now := time.Now()
	return auth.ScopedEvidenceDecision{
		Effect:        auth.EffectGrant,
		ResourceGuard: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "fixture_scope_resolved"},
		ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "fixture_no_scoped_forbid"},
		ObservedAt:    now,
		FreshUntil:    now.Add(time.Minute),
	}, nil
}

// authoredForbidPolicy is a deny-overlay whose EVIDENCE arm reports an AUTHORED forbid, so a
// request a positive scoped grant carried is denied ANYWAY.
//
// ⛔ THIS IS THE PAIRING THAT REACHES THE SCOPED-GRANT ANSWER, which the record called unreachable
// on reasoning that does not hold. The engine folds THREE predicates and denies as soon as ANY is
// broken, while the core-permission code stays whatever the grant wrote — so a CLEAN "the scoped
// grant permitted it" beside a BROKEN forbid absence is a denial carrying the scoped-grant code,
// which is exactly what the error map reads to pick that sentinel over the ordinary denial.
type authoredForbidPolicy struct{}

func (authoredForbidPolicy) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allow: true, Reason: "fixture: no restriction on the boolean path"}, nil
}

func (authoredForbidPolicy) EvaluateEvidence(context.Context, auth.Request) (auth.PolicyEvidenceDecision, error) {
	now := time.Now()
	return auth.PolicyEvidenceDecision{
		ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckBroken, Code: "fixture_authored_forbid"},
		ObservedAt:    now,
		FreshUntil:    now.Add(time.Minute),
	}, nil
}

// newScopedGrantActionPortFixture is the same engine with the two doubles above wired in, so every
// governed decision it takes reads "a positive scoped grant carried this, and a forbid stopped it".
func newScopedGrantActionPortFixture(t *testing.T, slug string) actionPortFixture {
	t.Helper()
	return newActionPortFixtureWith(t, slug, func(*api.Options) *auth.Authorizer {
		return auth.NewAuthorizer(authoredForbidPolicy{}, auth.WithScopedGrants(scopedGrantEngine{}))
	})
}

func newActionPortFixtureWith(t *testing.T, slug string, newAuthorizer func(*api.Options) *auth.Authorizer) actionPortFixture {
	t.Helper()
	var az *auth.Authorizer
	h := newHarnessOpts(t, func(o *api.Options) {
		az = newAuthorizer(o)
		o.Authorizer = az
	})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, slug)
	token := witnessTenantPrincipal(t, h, admin, tenant) // a VIEWER in this tenant

	// ⚠ THE BUDGET IS THE EVIDENCE WINDOW, which is why it is stated rather than defaulted: the
	// reconstruction ends its window at this deadline and the authorizer will not mint a witness
	// whose window excludes the present, so a budget too tight for a loaded runner reports a
	// permitted action as refused. Finite it must be — the reconstruction refuses a context with
	// no deadline at all.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	authenticated, err := h.authr.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("authenticate the tenant member: %v", err)
	}
	rows := api.NewReadRowAuthorizationPort(az, h.authr)
	port, ok := rows.(api.RouteActionAuthorizationPort)
	if !ok {
		t.Fatal("the value the engine already wires as the row port does not carry the action " +
			"port: a consumer reaching it by a checked assertion finds nothing")
	}
	ctx, reconstructed, err := rows.RefreshReadPrincipal(ctx, authenticated, tenant)
	if err != nil {
		t.Fatalf("reconstruct the principal's authority: %v", err)
	}
	if role, ok := reconstructed.RoleIn(tenant); !ok || role != auth.RoleViewer {
		t.Fatalf("reconstructed role = %q/%t, want the fixture's %q", role, ok, auth.RoleViewer)
	}
	return actionPortFixture{
		ctx: ctx, port: port, tenant: tenant,
		reconstructed: reconstructed, authenticated: authenticated,
	}
}

// resource returns a fresh row to decide over. Each case gets its own, because a witness binds the
// resource it was minted for and sharing one would let a case pass on its neighbour's answer.
func (actionPortFixture) resource() auth.ResourceAttrs {
	return auth.ResourceAttrs{Kind: "agent", ID: model.NewID().String()}
}

// actionPortPermission is the read permission the fixture's viewer holds.
const actionPortPermission auth.Permission = "agent:read"

// decisionAnswers is every sentinel the port may answer a DECISION with. A requirement the engine
// would refuse to mount must match NONE of them: nothing evaluated it.
var decisionAnswers = []error{
	auth.ErrRouteDenied, auth.ErrScopedGrantRequired, auth.ErrStepUpRequired,
	auth.ErrRouteUndecided, auth.ErrAuthorizerUnavailable,
}

// TestAuthorizeActionRefusesMetadataTheGovernedDoorWouldRefuse is the regression for a gate that
// decided from an UNSEALED declaration.
//
// ⛔ A MISTYPED ROLE FLOOR DOES NOT DENY — IT ADMITS, and that is the whole reason this cannot be
// left to the caller. An unknown role ranks 0 and a floor only bites when the caller's rank is
// BELOW it, so a floor of "editorr" ranks 0, no real role ranks below 0, and the restriction
// disappears: the same requirement one character apart returns a MINTED WITNESS where it returned
// a denial. An out-of-range assurance floor is the same family — the engine defines two levels, so
// a third denies every caller at the lower one and admits every caller at the higher one.
//
// ⛔ THE DOOR ALREADY REFUSES THIS, AT BOOT, WITH A PANIC, and that is what makes a port accepting
// a literal at the call a hole rather than a gap: while one registration path demands the check and
// another does not, the check is an OPTION — and an option is absent exactly in the case where it
// would have mattered, because the person who skipped it is the person who did not know they
// should not.
//
// ⛔ AND THE REFUSAL IS NOT ONE OF THE DECISION ANSWERS. "This declaration is impossible" is
// neither "the policy denied" nor "the decision could not be established": nothing was asked. A
// caller that folded it into the first would refuse someone no policy refused; into the second, it
// would tell them to retry a request that can never succeed.
func TestAuthorizeActionRefusesMetadataTheGovernedDoorWouldRefuse(t *testing.T) {
	f := newActionPortFixture(t, "action-port-metadata")

	refused := []struct {
		name string
		meta auth.RouteMetadata
		why  string
	}{
		{
			name: "a role floor the engine does not know",
			meta: auth.RouteMetadata{RBACMinimumRole: "editorr"},
			why: "an unknown role ranks 0, so this floor admits every principal that has any " +
				"role at all: the typo REMOVES a restriction instead of tightening one",
		},
		{
			name: "an assurance floor the engine does not define",
			meta: auth.RouteMetadata{MinimumAAL: 2},
			why: "the engine defines two assurance levels and no ceremony can produce a third, " +
				"so this floor denies every caller at the lower level and admits every caller " +
				"at the higher one - neither of the two things its author could have meant",
		},
	}
	for _, tc := range refused {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			resource := f.resource()
			witness, err := f.port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, resource,
				api.RouteActionRequirement{
					Permission: actionPortPermission,
					Metadata:   api.RouteMetadata{RouteMetadata: tc.meta},
				})
			if err == nil {
				t.Fatalf("the port DECIDED on a declaration the engine refuses to mount, and the "+
					"answer was an ALLOW: %s", tc.why)
			}
			for _, answer := range decisionAnswers {
				if errors.Is(err, answer) {
					t.Errorf("a malformed requirement was answered as a DECISION (%v): nothing "+
						"evaluated it, so neither a denial nor an undecided describes it - and a "+
						"caller mapping this onto the wire would publish a verdict the engine "+
						"never reached.\n%s", answer, tc.why)
				}
			}
			if witness.Allows(time.Now()) {
				t.Error("a refused requirement carried a witness that authorizes")
			}
		})
	}

	// ⛔ CONTROL: THE SAME FLOOR SPELLED CORRECTLY IS STILL A DENIAL. Without it, a port that
	// refused every requirement carrying a role floor would satisfy both cases above, and the cure
	// would be an outage wearing the shape of a fix.
	t.Run("control: the floor spelled correctly still denies", func(t *testing.T) {
		resource := f.resource()
		witness, err := f.port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, resource,
			api.RouteActionRequirement{
				Permission: actionPortPermission,
				Metadata: api.RouteMetadata{
					RouteMetadata: auth.RouteMetadata{RBACMinimumRole: auth.RoleEditor},
				},
			})
		if !errors.Is(err, auth.ErrRouteDenied) {
			t.Fatalf("a well-formed floor the caller does not reach stopped being a denial: %v", err)
		}
		if witness.Allows(time.Now()) {
			t.Error("a denial carried a witness that authorizes")
		}
	})

	// ⛔ CONTROL: VALID METADATA IS UNAFFECTED. The validation refuses DECLARATIONS, never
	// requests, so a requirement the engine would happily mount must still decide and still mint.
	t.Run("control: valid metadata still decides and still mints", func(t *testing.T) {
		resource := f.resource()
		requirement := api.RouteActionRequirement{Permission: actionPortPermission}
		witness, err := f.port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, resource, requirement)
		if err != nil {
			t.Fatalf("a well-formed requirement was refused: %v", err)
		}
		question := auth.Request{
			Principal: f.reconstructed, Permission: actionPortPermission, Tenant: f.tenant,
			Resource: resource, Route: requirement.Metadata.RouteMetadata,
		}
		if !witness.VerifyFor(time.Now(), question) {
			t.Fatal("the port permitted and returned a witness that does not answer the question " +
				"it was asked")
		}
	})
}

// portAnswer is one error a consumer of AuthorizeAction can receive: the sentinel errors.Is must
// match, the wire row the client contract gives it, and the call that produces it.
type portAnswer struct {
	name     string
	sentinel error
	wire     string
	// call produces the answer through the port. It is nil for an answer the engine's error map
	// names and no evidence path can reach today: the row stays because a consumer still needs an
	// arm for it, and a sentinel nobody lists is a sentinel nobody handles.
	call func(t *testing.T, f actionPortFixture) (auth.RouteAuthorizationWitness, error)
	// noLiveCase says WHY there is no call, so an empty arm cannot be read as an oversight.
	noLiveCase string
}

// TestAuthorizeActionNamesEveryAnswerAConsumerCanReceive enumerates the whole answer set, because
// an answer nobody named is an answer nobody maps.
//
// ⛔ A CONSUMER READS THESE WITH A CHAIN OF errors.Is, AND A CHAIN CANNOT SEPARATE TWO SENTINELS
// THAT MATCH EACH OTHER. So each answer is measured twice: it matches its own sentinel, and it
// matches NO other. Without the second half, a port that returned one error for everything would
// satisfy a table that only ever asked "does it match the one I expected".
//
// ⛔ AND THE DANGEROUS DIRECTION IS A REFUSAL SERVED AS AN OUTAGE. A consumer whose documented map
// has three arms and whose engine has six falls through to its default; if that default is the
// retryable one, a caller told "you may not" is told to try again forever, which is the exact
// inverse of the confusion this port exists to end.
func TestAuthorizeActionNamesEveryAnswerAConsumerCanReceive(t *testing.T) {
	f := newActionPortFixture(t, "action-port-answers")

	answers := []portAnswer{
		{
			name:     "the policy denied",
			sentinel: auth.ErrRouteDenied,
			wire:     "403 with the denial the route itself chose, or 404 when that route conceals; do not retry",
			call: func(t *testing.T, f actionPortFixture) (auth.RouteAuthorizationWitness, error) {
				return f.port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, f.resource(),
					api.RouteActionRequirement{
						Permission: actionPortPermission,
						Metadata: api.RouteMetadata{RouteMetadata: auth.RouteMetadata{
							RBACMinimumRole: auth.RoleEditor,
						}},
					})
			},
		},
		{
			name:     "the assurance floor is not met",
			sentinel: auth.ErrStepUpRequired,
			wire:     "403 step_up_required; repeat the ceremony and retry - a different remedy from a denial",
			call: func(t *testing.T, f actionPortFixture) (auth.RouteAuthorizationWitness, error) {
				return f.port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, f.resource(),
					api.RouteActionRequirement{
						Permission: actionPortPermission,
						Metadata: api.RouteMetadata{RouteMetadata: auth.RouteMetadata{
							MinimumAAL: auth.AAL3,
						}},
					})
			},
		},
		{
			// ⛔ THIS ROW ONCE SAID THE ANSWER WAS UNREACHABLE, ON REASONING THAT DOES NOT HOLD.
			// It argued that the codes naming a scoped grant are only ever reported clean or
			// unknown, never broken — true — and concluded that a DENY can therefore never carry
			// one. The engine folds THREE predicates and denies as soon as ANY of them is broken,
			// while the code stays whatever the core-permission arm wrote: a clean "the scoped
			// grant permitted it" beside a broken forbid absence IS that denial. An answer recorded
			// as unreachable is an answer nobody measures, and this one tells a caller who already
			// HAS a grant to go and get one.
			name:     "breadth of role does not reach this action",
			sentinel: auth.ErrScopedGrantRequired,
			wire:     "403, a denial whose remedy is a scoped grant rather than a broader role; do not retry",
			call: func(t *testing.T, _ actionPortFixture) (auth.RouteAuthorizationWitness, error) {
				g := newScopedGrantActionPortFixture(t, "action-port-answers-scoped")
				return g.port.AuthorizeAction(g.ctx, g.reconstructed, g.tenant, g.resource(),
					api.RouteActionRequirement{
						Permission: actionPortPermission,
						Metadata: api.RouteMetadata{RouteMetadata: auth.RouteMetadata{
							RBACMinimumRole: auth.RoleAdmin,
						}},
					})
			},
		},
		{
			name:     "the decision could not be established",
			sentinel: auth.ErrRouteUndecided,
			wire:     "503 route_decision_unavailable with Retry-After: 5; retry, because nobody said no",
			call: func(t *testing.T, f actionPortFixture) (auth.RouteAuthorizationWitness, error) {
				return f.port.AuthorizeAction(f.ctx, f.authenticated, f.tenant, f.resource(),
					api.RouteActionRequirement{Permission: actionPortPermission})
			},
		},
		{
			name:     "there was no authorizer to ask",
			sentinel: auth.ErrAuthorizerUnavailable,
			wire:     "the same 503 row: nothing was evaluated, so it is never a verdict",
			call: func(t *testing.T, f actionPortFixture) (auth.RouteAuthorizationWitness, error) {
				port, ok := api.NewRowAuthorizationPort(nil).(api.RouteActionAuthorizationPort)
				if !ok {
					return auth.RouteAuthorizationWitness{}, errors.New(
						"the row port does not carry the action port")
				}
				return port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, f.resource(),
					api.RouteActionRequirement{Permission: actionPortPermission})
			},
		},
		{
			// ⛔ A REQUIREMENT THE ENGINE WOULD REFUSE TO MOUNT IS NOT A DECISION AT ALL, so it
			// carries a name of its own - one that is NOT any of the five above. The row is
			// measured exactly as the others are: it must match this sentinel, and it must match
			// no other, so a caller cannot answer its own bug as a verdict.
			name:     "the requirement itself is malformed",
			sentinel: api.ErrRouteActionRequirementInvalid,
			wire:     "no row of the client contract: the engine refuses such a route at boot, so it must never be answered 403 or 404",
			call: func(t *testing.T, f actionPortFixture) (auth.RouteAuthorizationWitness, error) {
				return f.port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, f.resource(),
					api.RouteActionRequirement{
						Permission: actionPortPermission,
						Metadata: api.RouteMetadata{RouteMetadata: auth.RouteMetadata{
							RBACMinimumRole: "editorr",
						}},
					})
			},
		},
	}

	for _, a := range answers {
		t.Run(a.name, func(t *testing.T) {
			for _, other := range answers {
				if other.name == a.name || a.sentinel == nil || other.sentinel == nil {
					continue
				}
				if errors.Is(a.sentinel, other.sentinel) {
					t.Errorf("the sentinel for %q also matches the one for %q: a consumer reads "+
						"these with a chain of errors.Is, and a chain cannot separate two that "+
						"match each other", a.name, other.name)
				}
			}
			if a.call == nil {
				if a.noLiveCase == "" {
					t.Fatal("an answer with neither a live case nor a reason is an empty arm")
				}
				t.Logf("no live case, and here is why: %s", a.noLiveCase)
				return
			}
			witness, err := a.call(t, f)
			if err == nil {
				t.Fatalf("the port ALLOWED where the answer must be %q (%s)", a.name, a.wire)
			}
			if a.sentinel != nil && !errors.Is(err, a.sentinel) {
				t.Errorf("the answer does not match its own sentinel, so a consumer's arm for %q "+
					"never fires and the request falls through to the default: %v", a.name, err)
			}
			for _, other := range answers {
				if other.name == a.name || other.sentinel == nil {
					continue
				}
				if errors.Is(err, other.sentinel) {
					t.Errorf("the answer for %q ALSO reads as %q, so a consumer cannot tell\n"+
						"  %s\nfrom\n  %s", a.name, other.name, a.wire, other.wire)
				}
			}
			if witness.Allows(time.Now()) {
				t.Errorf("the refusal %q carried a witness that authorizes", a.name)
			}
		})
	}
}

// TestAuthorizeActionAnswersTheSameWhetherOrNotTheRouteConceals pins the half of the concealment
// obligation the port can hold — which is NOT the 404.
//
// ⛔ THE PORT MUST NOT KNOW. Whether a route hides a denial behind "no such row" is a decision that
// route made, and the caller passed it in. If the port's answer varied with it, provoking the
// variation would separate "this row does not exist" from "it exists and is not yours" — precisely
// the existence oracle concealment exists to close. So the two answers are compared against EACH
// OTHER rather than against any literal: identical, or the port has become the oracle.
//
// ⛔ AND THAT IS WHY THE OBLIGATION IS THE CALLER'S AND CANNOT BE ANYTHING ELSE. The field a
// consumer must read to answer 404 instead of 403 is the field it supplied, so the port already
// returns everything needed to honour it; a second answer carrying that decision back would be the
// leak rather than the cure.
func TestAuthorizeActionAnswersTheSameWhetherOrNotTheRouteConceals(t *testing.T) {
	// ⛔ BOTH DENIALS, BECAUSE THE ANSWER SET HAS TWO AND THE OBLIGATION COVERS BOTH. A consumer
	// told to write one arm per sentinel, and told about concealment only under the ordinary
	// denial, conceals that one as 404 and answers the scoped-grant one 403 — on the same route,
	// about the same row, confirming its existence through the arm nobody mentioned. The governed
	// door makes no such distinction: both land in the arm that writes the route's own denial.
	for _, tc := range []struct {
		name     string
		sentinel error
		fixture  func(t *testing.T) actionPortFixture
		meta     auth.RouteMetadata
	}{
		{
			name:     "the policy denied",
			sentinel: auth.ErrRouteDenied,
			fixture: func(t *testing.T) actionPortFixture {
				return newActionPortFixture(t, "action-port-conceal")
			},
			meta: auth.RouteMetadata{RBACMinimumRole: auth.RoleEditor},
		},
		{
			name:     "breadth of role is not the remedy",
			sentinel: auth.ErrScopedGrantRequired,
			fixture: func(t *testing.T) actionPortFixture {
				return newScopedGrantActionPortFixture(t, "action-port-conceal-scoped")
			},
			meta: auth.RouteMetadata{RBACMinimumRole: auth.RoleAdmin},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fixture(t)
			resource := f.resource()

			deny := func(conceal bool) (auth.RouteAuthorizationWitness, error) {
				return f.port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, resource,
					api.RouteActionRequirement{
						Permission: actionPortPermission,
						Metadata: api.RouteMetadata{
							ConcealDeniedAsNotFound: conceal,
							RouteMetadata:           tc.meta,
						},
					})
			}

			openWitness, openErr := deny(false)
			hiddenWitness, hiddenErr := deny(true)
			if !errors.Is(openErr, tc.sentinel) || !errors.Is(hiddenErr, tc.sentinel) {
				t.Fatalf("the denial changed shape: without concealment %v, with it %v",
					openErr, hiddenErr)
			}
			if openErr.Error() != hiddenErr.Error() {
				t.Errorf("the port answers DIFFERENTLY when the route conceals, which makes the "+
					"port itself an existence oracle:\n  without: %q\n  with:    %q",
					openErr.Error(), hiddenErr.Error())
			}
			if openWitness.Allows(time.Now()) || hiddenWitness.Allows(time.Now()) {
				t.Error("a denial carried a witness that authorizes")
			}
		})
	}
}

// The two namespaces and the two actions the attribution cases use.
//
// ⛔ THE FOREIGN ACTION IS REGISTERED TO ITS OWNER, NOT MERELY ABSENT, and the difference is the
// whole case. An action nobody declared is refused because the catalog is empty of it; an action
// ANOTHER module declared is refused because it is not this module's to name. Only the second
// measures ownership, and a case built on the first passes for a reason that disappears the day
// the catalog is populated differently.
const (
	actionPortNamespace   = "actionportdemo"
	actionPortForeignNS   = "actionportother"
	actionPortOwnAction   = "thing:act"
	actionPortOtherAction = "other:act"
	// A permission in the OTHER module's namespace. A role grants a module permission by verb
	// tier, so a viewer holds every module read there is - which is why naming one the caller's
	// module never declared reaches a decision rather than a refusal.
	actionPortForeignPermission auth.Permission = "actionportother:thing:read"
)

// declareActionPortModules seeds the catalog the way mounting two modules would.
//
// ⚠ IT RUNS AFTER THE FIXTURE, NOT BEFORE, and the order is load-bearing: building a server RESETS
// this catalog, so a registration made first would be wiped before the first decision. It is undone
// on cleanup for the same reason — the catalog is process-wide, and a test that left it seeded would
// decide the next test's answers.
func declareActionPortModules(t *testing.T) {
	t.Helper()
	t.Cleanup(auth.ResetModuleCatalog)
	if err := auth.RegisterModuleActions(actionPortNamespace, []auth.CedarAction{actionPortOwnAction}); err != nil {
		t.Fatalf("declare this module's action: %v", err)
	}
	if err := auth.RegisterModuleActions(actionPortForeignNS, []auth.CedarAction{actionPortOtherAction}); err != nil {
		t.Fatalf("declare the other module's action: %v", err)
	}
}

// moduleBoundActionPort is the optional capability a caller discovers by ASSERTION, which is this
// repository's own idiom for one: the governed registration door is found the same way.
//
// ⛔ IT IS NOT ON THE PORT'S INTERFACE, and that is deliberate twice over. Adding it would change a
// one-method contract into a two-method one, so a third party implementing the port — a test double,
// a recording wrapper — would stop compiling for a capability it does not provide. And a caller that
// finds it absent must NOT fall back to the unbound gate for a named action: the unbound gate is the
// one that cannot answer the question, which is why it refuses.
type moduleBoundActionPort interface {
	ForModule(namespace string) api.RouteActionAuthorizationPort
}

// boundTo returns the gate bound to one module's namespace, or fails saying what is missing.
func boundTo(t *testing.T, port api.RouteActionAuthorizationPort, namespace string) api.RouteActionAuthorizationPort {
	t.Helper()
	binder, ok := port.(moduleBoundActionPort)
	if !ok {
		t.Fatal("the gate carries no way to bind itself to the module that asks through it, so it " +
			"cannot run the half of the mount check that asks WHOSE action this is: a requirement " +
			"naming another module's action is validated, decided, and that action travels into " +
			"the request and into the witness's question digest")
	}
	return binder.ForModule(namespace)
}

// namedAction is one requirement over the fixture's permission that NAMES a Cedar action, which is
// the only shape the attribution half looks at.
func namedAction(action string) api.RouteActionRequirement {
	return api.RouteActionRequirement{
		Permission: actionPortPermission,
		Metadata:   api.RouteMetadata{RouteMetadata: auth.RouteMetadata{CedarAction: action}},
	}
}

// requireUnmountable is the shared shape of "the engine would refuse to mount this".
func requireUnmountable(t *testing.T, witness auth.RouteAuthorizationWitness, err error, why string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the gate DECIDED, and the answer was an ALLOW: %s", why)
	}
	if !errors.Is(err, api.ErrRouteActionRequirementInvalid) {
		t.Errorf("the refusal is not the malformed-requirement one: %v", err)
	}
	for _, answer := range decisionAnswers {
		if errors.Is(err, answer) {
			t.Errorf("a requirement the engine would refuse to mount was answered as a DECISION "+
				"(%v): nothing evaluated it, and a caller mapping this onto the wire would publish "+
				"a verdict the engine never reached", answer)
		}
	}
	if witness.Allows(time.Now()) {
		t.Error("a refused requirement carried a witness that authorizes")
	}
}

// TestAuthorizeActionRefusesAnActionItsCallerCannotName is the other half of "the engine would
// mount this route", and the half the gate was not running.
//
// ⛔ THE MOUNT CHECK HAS TWO HALVES AND THEY ASK DIFFERENT QUESTIONS. One asks whether the
// declaration is POSSIBLE — a role the engine knows, an assurance level it defines — and is a pure
// function of the metadata. The other asks WHOSE action this is, and it is not: a module may only
// name actions it declares itself, so the answer depends on the caller's identity and on what the
// running engine has registered. A gate that runs the first and skips the second refuses an
// impossible declaration and accepts a foreign one.
//
// ⛔ AND THE FOREIGN ACTION IS NOT COSMETIC: IT CHOOSES WHOSE POLICY STATEMENTS ANSWER. The action
// travels into the request, the policy engine matches statements written about THAT action, and it
// is sealed into the witness's question digest — so an effect is attributed to a decision taken
// about somebody else's verb, on a route the engine would refuse to start with.
//
// ⛔ THE RULE IS PER MODULE, NOT A GLOBAL SET, and that is why the gate must be BOUND before it can
// run it. A flat "does anybody declare this" would let a caller name another module's action while
// that module happens to be loaded, and stop working the day it is not — the hidden dependency the
// engine's own check exists to turn into a boot failure. A gate that cannot say which module is
// asking cannot ask the question, so it refuses instead of guessing.
func TestAuthorizeActionRefusesAnActionItsCallerCannotName(t *testing.T) {
	f := newActionPortFixture(t, "action-port-attribution")
	declareActionPortModules(t)

	t.Run("refused: an unbound gate cannot say whose action this is", func(t *testing.T) {
		witness, err := f.port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, f.resource(),
			namedAction(actionPortOwnAction))
		requireUnmountable(t, witness, err,
			"the gate was never bound to a module, so nothing established that this caller may "+
				"name this action - and the action still reached the policy engine and the witness")
	})

	t.Run("refused: another module's action", func(t *testing.T) {
		port := boundTo(t, f.port, actionPortNamespace)
		witness, err := port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, f.resource(),
			namedAction(actionPortOtherAction))
		requireUnmountable(t, witness, err,
			"the action belongs to another module, which declared it; this module did not, and a "+
				"route naming it is one the engine refuses to start with")
	})

	t.Run("control: the module's own declared action is decided", func(t *testing.T) {
		port := boundTo(t, f.port, actionPortNamespace)
		resource := f.resource()
		requirement := namedAction(actionPortOwnAction)
		witness, err := port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, resource, requirement)
		if err != nil {
			t.Fatalf("a module's own declared action was refused: %v\nWithout this control, a gate "+
				"that refused every named action would satisfy both refusals above and the cure "+
				"would be an outage wearing the shape of a fix.", err)
		}
		question := auth.Request{
			Principal: f.reconstructed, Permission: actionPortPermission, Tenant: f.tenant,
			Resource: resource, Route: requirement.Metadata.RouteMetadata,
		}
		if !witness.VerifyFor(time.Now(), question) {
			t.Fatal("the gate permitted and returned a witness that does not answer the question " +
				"it was asked")
		}
	})

	t.Run("pin: a requirement naming NO action is decided, and the permission half is NOT run", func(t *testing.T) {
		// ⛔ THIS PIN ONCE CLAIMED PARITY WITH THE DOOR AND THE CLAIM WAS FALSE. The door does skip
		// its ACTION check when a route names none — but it runs a second one this gate cannot:
		// every route's PERMISSION must appear in what its module declared, refused at boot for the
		// whole module. And a permission's namespace is deliberately NOT required to equal the
		// module's, because route-only modules reuse another's on purpose, so "the permission
		// already carries the namespace" answers nothing about who may ask.
		//
		// ⛔ SO WHAT IS PINNED HERE IS A DECISION, NOT A PARITY. With no action named the evaluated
		// action IS the permission, and it is sealed into the witness's question digest with no
		// check that this caller's module ever declared it. The case below the next one measures
		// exactly that gap rather than leaving it to a sentence.
		resource := f.resource()
		requirement := api.RouteActionRequirement{Permission: actionPortPermission}
		witness, err := f.port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, resource, requirement)
		if err != nil {
			t.Fatalf("a requirement that names no action was refused: %v", err)
		}
		question := auth.Request{
			Principal: f.reconstructed, Permission: actionPortPermission, Tenant: f.tenant,
			Resource: resource, Route: requirement.Metadata.RouteMetadata,
		}
		if !witness.VerifyFor(time.Now(), question) {
			t.Fatal("the gate permitted and returned a witness that does not answer the question " +
				"it was asked")
		}
	})

	// ⛔ THE NAMESPACE IS THE CALLER'S OWN WORD, AND THIS CASE PINS THAT IT IS. At the door the
	// namespace is the ENGINE's: the registrar carries the one the module registered under, and a
	// duplicate is refused before anything mounts. Here it arrives as an argument, so the gate
	// catches MISNAMING — a module asking for an action nobody gave it — and NOT a caller that
	// presents somebody else's namespace, which is a different failure with the same shape.
	//
	// ⚠ IT IS A PIN OF A LIMIT, NOT A CURE, and it is written so the limit cannot be believed
	// closed: the day the composition root hands each module a gate already bound to its registered
	// namespace, this case goes red and that is the signal it exists to give.
	t.Run("pin: the gate cannot check a namespace it was merely told", func(t *testing.T) {
		resource := f.resource()
		// A namespace this caller has no right to, and that namespace's own declared action.
		port := boundTo(t, f.port, actionPortForeignNS)
		requirement := namedAction(actionPortOtherAction)
		witness, err := port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, resource, requirement)
		if err != nil {
			t.Fatalf("this case pins a KNOWN LIMIT rather than a cure, and the limit has moved: "+
				"%v\nIf the gate now establishes the namespace itself, delete this case and say so "+
				"where the limit was written down.", err)
		}
		question := auth.Request{
			Principal: f.reconstructed, Permission: actionPortPermission, Tenant: f.tenant,
			Resource: resource, Route: requirement.Metadata.RouteMetadata,
		}
		if !witness.VerifyFor(time.Now(), question) {
			t.Fatal("the gate permitted and returned a witness that does not answer the question " +
				"it was asked")
		}
	})

	// ⛔ AND THE PERMISSION HALF IS NOT RUN HERE AT ALL, which is the other limit. The door refuses
	// to mount a module whose route requires a permission that module never declared; this gate
	// receives a permission as an argument and has no declaration to compare it with. With no
	// action named the permission IS the evaluated action, so it reaches the policy engine and the
	// witness's digest unattributed.
	t.Run("pin: a permission from a namespace this caller never declared is still decided", func(t *testing.T) {
		resource := f.resource()
		requirement := api.RouteActionRequirement{Permission: actionPortForeignPermission}
		witness, err := f.port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, resource, requirement)
		if err != nil {
			t.Fatalf("this case pins a KNOWN LIMIT rather than a cure, and the limit has moved: "+
				"%v\nIf the gate now checks the permission against what its module declared, delete "+
				"this case and say so where the limit was written down.", err)
		}
		question := auth.Request{
			Principal: f.reconstructed, Permission: actionPortForeignPermission, Tenant: f.tenant,
			Resource: resource, Route: requirement.Metadata.RouteMetadata,
		}
		if !witness.VerifyFor(time.Now(), question) {
			t.Fatal("the gate permitted and returned a witness that does not answer the question " +
				"it was asked")
		}
	})
}

// TestABoundGateCarriesNothingButTheGate is the regression for a narrowing that did not narrow.
//
// ⛔ BINDING MUST NOT HAND BACK MORE THAN IT WAS ASKED FOR. The value the engine wires is a READ
// port: its own DecideRows refuses a human reader without complete authority, and the complete path
// binds and validates that authority fact by fact. The gate is a method promoted from the plain
// value embedded inside it, so returning that plain value hands a caller a row port with the
// override SHED — one assertion away, on a value it obtained by asking for something narrower.
//
// ⛔ AND THE SHAPE IS THE WHOLE TEST, because the consequence is a page and not an error. A caller
// that binds once and then keeps that one value — the natural way to hold it — can read rows for a
// human principal through a path the wired value answers "I could not establish" to. Nothing
// refuses; a page is simply served.
func TestABoundGateCarriesNothingButTheGate(t *testing.T) {
	f := newActionPortFixture(t, "action-port-bound-shape")
	declareActionPortModules(t)
	bound := boundTo(t, f.port, actionPortNamespace)

	if rows, ok := bound.(api.RowAuthorizationPort); ok {
		t.Errorf("the bound gate also answers the ROW port (%T): binding shed the override that "+
			"makes the wired value refuse a human read without complete authority", bound)
		set, err := rows.DecideRows(f.ctx, f.reconstructed, f.tenant, actionPortPermission,
			api.RouteMetadata{}, []auth.ResourceAttrs{f.resource()})
		t.Errorf("and through it a read decided %d row(s) with err=%v, where the value the engine "+
			"wires refuses: a page served where the engine says it could not establish one",
			len(set.Allowed), err)
	}
	if _, ok := bound.(api.ReadRowAuthorizationPort); ok {
		t.Error("the bound gate also answers the READ row port, so it can refresh a principal's " +
			"authority - a door the caller asked for nothing of the kind")
	}
	if _, ok := bound.(api.CompleteReadRowAuthorizationPort); ok {
		t.Error("the bound gate also answers the COMPLETE read row port")
	}

	// ⛔ CONTROL, BOTH WAYS. The bound value must still answer the ONE question it exists for, or
	// the narrowing is an outage; and the value the engine wires must KEEP its row shape, or the
	// narrowing happened in the wrong place - it belongs to binding, not to the port.
	resource := f.resource()
	requirement := namedAction(actionPortOwnAction)
	witness, err := bound.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, resource, requirement)
	if err != nil {
		t.Fatalf("the bound gate stopped answering its own question: %v", err)
	}
	question := auth.Request{
		Principal: f.reconstructed, Permission: actionPortPermission, Tenant: f.tenant,
		Resource: resource, Route: requirement.Metadata.RouteMetadata,
	}
	if !witness.VerifyFor(time.Now(), question) {
		t.Fatal("the bound gate permitted and returned a witness that does not answer the question " +
			"it was asked")
	}
	if _, ok := f.port.(api.RowAuthorizationPort); !ok {
		t.Error("the value the engine wires stopped answering the row port: the narrowing belongs " +
			"to the act of binding, and moving it onto the port itself would take rows away from " +
			"every caller that never asked to bind")
	}
}

// TestAuthorizeActionAnswersTheScopedGrantSentinel gives the one answer that had no live case one,
// with the shape the contract names: a route that says breadth of role is not enough, a caller who
// holds a positive scoped grant, and a forbid that stops them anyway.
//
// ⛔ AN ANSWER NOBODY MEASURES IS AN ANSWER NOBODY MAPS. This one is the sharpest of the denials to
// get wrong, because its remedy contradicts its cause: it tells a caller who already HAS a scoped
// grant that a scoped grant is what they need. A consumer that never sees it in a case writes the
// arm from the doc alone, and the doc is what this file exists to hold honest.
func TestAuthorizeActionAnswersTheScopedGrantSentinel(t *testing.T) {
	f := newScopedGrantActionPortFixture(t, "action-port-scoped-grant")
	declareActionPortModules(t)
	port := boundTo(t, f.port, actionPortNamespace)

	// The route declares that breadth of role does not reach it, which is what removes the RBAC
	// term and leaves the positive grant carrying the request - and a route that says so must name
	// its action, so this requirement exercises the attribution half as well.
	requirement := api.RouteActionRequirement{
		Permission: actionPortPermission,
		Metadata: api.RouteMetadata{RouteMetadata: auth.RouteMetadata{
			CedarAction: actionPortOwnAction, RequireScopedGrant: true,
		}},
	}
	witness, err := port.AuthorizeAction(f.ctx, f.reconstructed, f.tenant, f.resource(), requirement)
	if err == nil {
		t.Fatal("a forbid did not stop a request a scoped grant carried")
	}
	if !errors.Is(err, auth.ErrScopedGrantRequired) {
		t.Fatalf("the answer is not the scoped-grant one: %v", err)
	}
	if errors.Is(err, auth.ErrRouteDenied) || errors.Is(err, auth.ErrRouteUndecided) ||
		errors.Is(err, api.ErrRouteActionRequirementInvalid) {
		t.Errorf("the scoped-grant answer also reads as another answer, so a consumer cannot tell "+
			"which remedy to offer: %v", err)
	}
	if witness.Allows(time.Now()) {
		t.Error("a denial carried a witness that authorizes")
	}
}
