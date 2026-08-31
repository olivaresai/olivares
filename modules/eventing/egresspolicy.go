// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The egress destination policy: the operator's ceiling on where a tenant may point
// its own event stream.
//
// Before it, validateEndpointURL was the whole answer: https, no credentials in the
// URL, a length cap, and a refusal of private or reserved IP LITERALS. Any public
// hostname passed, at editor tier. That is not a weak policy, it is the absence of
// one — in the module whose product thesis is governing egress. A tenant editor
// could route the tenant's own governance events, which are exactly the records that
// describe its estate, to any host on the internet, and nothing in the control plane
// expressed an opinion.
//
// Two places enforce it, and BOTH are necessary:
//
//   - authoring, so an operator's rule produces a clear 400 at the moment somebody
//     writes a destination rather than a dead-letter an hour later; and
//   - send, which is AUTHORITATIVE. Authoring-time alone is evadable three ways that
//     are all real in this tree: a policy edited afterwards would grandfather every
//     row written before it, the rendered URL a SinkRenderer returns is not the
//     stored endpoint (the interface permits any URL), and the CLI wrote the endpoint
//     column without going near the validator at all.
//
// This is the same conclusion modules/orchestration reached for routine policy,
// where the comment reads that a control enforced at fewer than all its seams is
// evadable through the ones it missed.

// EgressPolicySource answers "where may this tenant open a connection". The
// composition root wires it; nil means no policy is in force, which is the
// tri-state's ABSENT state and permits everything.
//
// The distinction between absent and empty is load-bearing and must not be
// collapsed. An empty allow-list has to deny everything for the algebra to be sound,
// and an absent policy has to permit everything or the first upgrade carrying this
// code silently breaks every subscription already in the field. See egress.Policy.
type EgressPolicySource interface {
	// EgressPolicy returns the policy in force for a tenant. An error is NOT an
	// absent policy: the caller turns it into a denial, because "the plane could not
	// decide" must never read as "the plane decided yes".
	EgressPolicy(ctx context.Context, tenant model.TenantID) (egress.Policy, error)
}

// WithEgressPolicy wires the operator's destination policy. Without it the module
// behaves exactly as before, which is the only upgrade-safe default.
func WithEgressPolicy(s EgressPolicySource) Option {
	return func(m *Module) {
		if s != nil {
			m.egress = s
		}
	}
}

// destinationDecider holds everything a destination decision needs, so that there is
// ONE implementation of the decision and not one per caller.
//
// It exists because the alternative already failed twice in this campaign. The CLI
// grew its own endpoint check and its own HTTP client, both copies of engine rules,
// and both copies were the ones that were wrong — one refused every destination named
// by hostname, the other carried a narrower reserved-address set. Unit G would have
// made it three: the rollout mode is the difference between permitting and denying an
// absent policy, and a copy that did not know about it would have quietly answered
// with the pre-unit semantics forever.
type destinationDecider struct {
	// policy is the operator's ceiling; nil means none is in force.
	policy EgressPolicySource
	// rollout is this deployment's durable disposition; nil means not wired, which
	// behaves as the pre-unit-G module did.
	rollout EgressRolloutSource
	// resolver looks a destination up once; nil uses the default resolver.
	resolver egress.Resolver
	// compat answers the compatibility question; nil means it cannot be consulted,
	// which resolves to the ENFORCED answer and never to a permit.
	compat *egressCompat
	// log records an unreadable policy or disposition. nil discards.
	log *slog.Logger
	// resolveRollout, when non-nil, supplies a CACHED disposition. The module passes
	// its own so a delivery burst does not read the row once per attempt; a one-shot
	// caller such as the CLI leaves it nil and reads directly.
	resolveRollout func(context.Context) (store.RolloutState, error)
}

// decider is the module's own configured decider.
func (m *Module) decider() destinationDecider {
	return destinationDecider{
		policy:         m.egress,
		rollout:        m.rollout,
		resolver:       m.resolver,
		compat:         m.compat,
		log:            m.log,
		resolveRollout: m.resolveRollout,
	}
}

// resolvePolicy reads the policy for a tenant, converting an unreadable one into a
// deny-everything policy rather than an absent one.
func (d destinationDecider) resolvePolicy(ctx context.Context, tenant model.TenantID) egress.Policy {
	if d.policy == nil {
		return egress.Policy{}
	}
	p, err := d.policy.EgressPolicy(ctx, tenant)
	if err != nil {
		if d.log != nil {
			d.log.Error("eventing: egress policy is unreadable; refusing every destination until it is",
				"tenant", tenant.String(), "err", err)
		}
		return egress.Indeterminate("policy:unreadable")
	}
	return p
}

// rolloutState returns the disposition in force for this decider.
func (d destinationDecider) rolloutState(ctx context.Context) (store.RolloutState, error) {
	if d.resolveRollout != nil {
		return d.resolveRollout(ctx)
	}
	if d.rollout == nil {
		return store.RolloutState{Key: EgressRolloutControlKey,
			ClassifiedMode: store.RolloutPolicyOptional, CurrentMode: store.RolloutPolicyOptional}, nil
	}
	st, err := d.rollout.EgressRollout(ctx)
	if err != nil {
		return store.RolloutState{}, err
	}
	if !st.CurrentMode.Valid() {
		return store.RolloutState{}, fmt.Errorf("eventing: durable rollout state for %q holds mode %q, which this binary does not know", st.Key, st.CurrentMode)
	}
	return st, nil
}

// resolvePolicy on the module reads the policy through its own decider. Kept as a
// method because callers that only need the policy (the compatibility report) should
// not have to know how a decision is assembled.
func (m *Module) resolvePolicy(ctx context.Context, tenant model.TenantID) egress.Policy {
	return m.decider().resolvePolicy(ctx, tenant)
}

// egressRequest is one destination decision.
//
// Purpose is EXPLICIT and SubscriptionRef names the subscription when there is one.
// Together they decide whether the compatibility record may be consulted at all: a
// create can never use an exception, because compatibility preserves what a
// deployment already had and never manufactures a new entitlement.
//
// An earlier draft inferred the purpose from whether the reference was empty. That
// was one implicit rule too many — a future create path that happened to pass an id
// would silently have gained an exception — so the vocabulary is closed and the rule
// is stated once, in mayUseException.
type egressRequest struct {
	Tenant          model.TenantID
	Purpose         EgressPurpose
	URL             string
	SubscriptionRef model.ID
}

// exceptionSubject is the subscription an exception may be looked up for, or empty
// when this request may not use one at all.
func (r egressRequest) exceptionSubject() model.ID {
	if !r.Purpose.mayUseException() {
		return ""
	}
	return r.SubscriptionRef
}

// authorizeDestination decides whether a destination may be dialed for this tenant
// and returns the addresses the caller must pin.
//
// The resolution happens HERE, once, and the permitted addresses come back with the
// verdict. A caller that took only the boolean and then dialed the name would ask
// DNS the same question a second time, and the second answer belongs to whoever
// controls the zone — which for a tenant-authored destination is the tenant.
//
// Two layers decide, in this order:
//
//  1. The deployment's DURABLE disposition (unit G). It is what an absent policy
//     MEANS here: deny on a deployment that never had destinations to protect, permit
//     on one that did. An unreadable disposition denies and retries.
//  2. The operator's policy, and — under compatibility mode only — the exact
//     destinations this deployment already had.
func (m *Module) authorizeDestination(ctx context.Context, req egressRequest) (egress.Decision, error) {
	return m.decider().authorize(ctx, req)
}

// authorize is the single implementation. See authorizeDestination for the contract.
func (dd destinationDecider) authorize(ctx context.Context, req egressRequest) (egress.Decision, error) {
	st, rerr := dd.rolloutState(ctx)
	if rerr != nil {
		// Not knowing whether the control is in force is not the same as it not being in
		// force. It denies, retryably, for the same reason an unreadable policy does.
		if dd.log != nil {
			dd.log.Error("eventing: the durable rollout state for the egress destination control is unreadable; refusing every destination until it can be read",
				"control", EgressRolloutControlKey, "err", rerr)
		}
		return egress.Decision{Code: egress.CodeRolloutUnavailable}, rerr
	}

	// Under compatibility mode the record of what this deployment ALREADY HAD is drawn
	// before this decision is answered, whatever the decision is and whether or not a
	// policy is authored yet.
	//
	// Drawing it here rather than at the first refusal is the correction that makes the
	// boundary mean anything: deferring it until a policy needed an exception would
	// record every subscription created during the operator's delay as pre-existing, so
	// the compatibility set would grow for as long as nobody authored a policy — the
	// defect this unit exists to close, arriving from the other side.
	if st.CurrentMode == store.RolloutLegacyCompat && dd.compat != nil {
		if req.Purpose == EgressDryRun {
			// A dry run is a READ, and it is registered at read tier. Letting it draw the
			// compatibility line would put an irreversible, once-only decision — WHEN the line
			// falls, and therefore how much activity lands after it — in the hands of a caller
			// who holds only a read permission. So it consults the record if it exists and
			// declines to answer if it does not, rather than creating it.
			present, perr := dd.compat.seedPresentCached(ctx, req.Tenant)
			if perr != nil || !present {
				return egress.Decision{Code: egress.CodeSeedIncomplete}, perr
			}
		} else if err := dd.compat.ensureSeed(ctx, req.Tenant); err != nil {
			// The BARRIER. An unseeded tenant is indistinguishable from a tenant with no
			// legacy destinations, and those two demand opposite answers — so this may not
			// answer until the record exists.
			if dd.log != nil {
				dd.log.Error("eventing: the egress compatibility record for this tenant could not be established, so a refusal cannot be distinguished from a grandfathered destination",
					"tenant", req.Tenant.String(), "err", err)
			}
			return egress.Decision{Code: egress.CodeSeedIncomplete}, err
		}
	}

	// The policy is consulted BEFORE the destination is canonicalized, and the order is
	// a correctness requirement rather than an optimization: parsing applies IDNA2008
	// with the strict profile, which refuses host syntaxes an existing subscription may
	// legitimately use — an underscore in a label is the common one, and internal names
	// are full of them. Canonicalizing first meant an estate with NO policy configured
	// started dead-lettering those subscriptions, with a message naming a policy that
	// did not exist.
	p := dd.resolvePolicy(ctx, req.Tenant)
	if p.Unavailable {
		// Nothing was decided about this destination, so do not pay for a parse or a
		// resolution to reach a verdict that is already fixed.
		return egress.Evaluate(p, egress.Destination{}, nil), nil
	}
	if !p.InForce {
		switch st.CurrentMode {
		case store.RolloutEnforced:
			// The whole point of the unit. This deployment enforces the control and nobody
			// has authorized anything, so there is nothing that COULD permit a destination.
			// It is not a fault and not a refusal of this particular destination: it is
			// configuration pending, with a different owner than the caller.
			return egress.Decision{Code: egress.CodePolicyRequired}, nil
		default:
			// Compatibility, or a recorded decision that the control need not be configured:
			// no policy, no parse, no lookup, no opinion — the posture this estate already
			// had. The dial-time guard still refuses private and reserved addresses.
			return egress.Decision{Permitted: true, Code: egress.CodeNoPolicy}, nil
		}
	}

	dest, perr := egress.ParseDestination(req.URL)
	if perr != nil {
		// The strict parser refuses this host. Under compatibility mode a destination
		// whose EXACT spelling this deployment already used keeps working, because unit F
		// promised precisely that while no policy was authored and a policy is not
		// supposed to be what breaks it. Counting the breakage instead of grandfathering
		// it would have made compatibility mode useless for the estates most likely to
		// need it — internal collectors, whose names are full of underscores.
		if allowed, code := dd.legacyRawAllows(ctx, st, req); allowed {
			return egress.Decision{Permitted: true, Code: code, PolicyRef: p.Ref}, nil
		} else if code != "" {
			return egress.Decision{Code: code, PolicyRef: p.Ref}, nil
		}
		return egress.Decision{Code: egress.CodeNotAllowlisted, PolicyRef: p.Ref}, perr
	}
	ips, rerr := egress.Resolve(ctx, dd.resolver, dest)
	if rerr != nil || len(ips) == 0 {
		return egress.Decision{Code: egress.CodeUnresolvable, PolicyRef: p.Ref}, rerr
	}
	d := egress.Evaluate(p, dest, ips)
	if d.Permitted || st.CurrentMode != store.RolloutLegacyCompat {
		return d, nil
	}
	// A policy IS authored and it refuses this destination, on a deployment that
	// predates the control. If this exact destination is one the deployment already had,
	// it keeps working until the operator enforces — which is the entire content of
	// compatibility mode, and why it is an exact list rather than a mode that waves
	// things through.
	//
	// A RETRYABLE denial is never overridden: "could not decide" is not a destination
	// this deployment ever had, and treating it as one would convert a transient outage
	// into a permit.
	if d.Retryable() || dd.compat == nil {
		return d, nil
	}
	subject := req.exceptionSubject()
	if subject == "" {
		return d, nil
	}
	// BOTH grammars are tried, and that is a durability fix rather than belt-and-braces. Which
	// grammar a destination was recorded under is whatever egress.ParseDestination said AT SEED
	// TIME; a parser that later starts accepting a spelling it used to refuse would compute only
	// the canonical digest and never find the raw row that is sitting there. The record outlives
	// the parser, so the lookup has to.
	allowed, lerr := dd.compat.exceptionAllowsAny(ctx, req.Tenant, subject,
		canonicalAuthority(dest), req.URL)
	if lerr != nil {
		return egress.Decision{Code: egress.CodeSeedIncomplete, PolicyRef: p.Ref}, lerr
	}
	if !allowed {
		return d, nil
	}
	// Permitted, and deliberately NOT as CodeAllowed. Every delivery carrying this code
	// is a row on the list of what stops working when the operator enforces, and an
	// operator who cannot count them cannot consent to the change.
	//
	// The pin is the resolution that was just performed, and the reserved floor is NOT
	// lifted: a grandfathered destination is one the deployment used to reach, which
	// means it was reachable under the floor already. Lifting it here would let a
	// compatibility record grant something no operator rule ever named.
	return egress.Decision{Permitted: true, Code: egress.CodeLegacyException, PolicyRef: p.Ref, Pin: ips}, nil
}

// legacyRawAllows answers the compatibility question for an endpoint the strict
// parser refuses. It returns (permitted, code); an empty code means "this is not a
// compatibility case at all, decide it the ordinary way".
//
// A permit here carries NO PIN, and that is not an oversight. There is nothing to
// pin: the host cannot be canonicalized, so it was never resolved through
// core/egress, and unit F's absent-policy path never pinned it either. The posture is
// therefore exactly the one this destination already had — the dialer's own
// reserved-address floor and nothing more — which is what "compatibility" is supposed
// to mean, and one more reason the list is finite and retired when the operator
// enforces.
func (dd destinationDecider) legacyRawAllows(ctx context.Context, st store.RolloutState, req egressRequest) (bool, string) {
	if st.CurrentMode != store.RolloutLegacyCompat || dd.compat == nil {
		return false, ""
	}
	subject := req.exceptionSubject()
	if subject == "" {
		return false, ""
	}
	auth, aerr := legacyRawAuthority(req.URL)
	if aerr != nil {
		return false, ""
	}
	allowed, lerr := dd.compat.exceptionAllows(ctx, req.Tenant, subject, auth)
	if lerr != nil {
		return false, egress.CodeSeedIncomplete
	}
	if !allowed {
		return false, ""
	}
	return true, egress.CodeLegacyException
}

// checkEndpointPolicy is the authoring-time courtesy check, returning the message a
// caller sees or "". It is SEPARATE from validateSubscription because it performs a
// DNS lookup, and this module's own rule (replay.go) is that the store must never
// host a network call inside an open transaction — which is exactly where the
// restore handler runs its validation.
//
// send() remains the authoritative seam; this exists so an author learns at the
// moment they write a destination instead of reading a dead-letter an hour later.
// It takes the subscription reference because an UPDATE of a subscription whose
// destination this deployment already had must stay editable under compatibility
// mode. Denying it would mean an operator who authors a narrow policy makes every
// pre-existing subscription impossible to touch — including to disable it, which is
// the one action they most likely want. A CREATE passes an empty reference and can
// therefore never inherit an exception.
func (m *Module) checkEndpointPolicy(ctx context.Context, tenant model.TenantID, purpose EgressPurpose, subRef model.ID, endpoint string) string {
	d, err := m.authorizeDestination(ctx, egressRequest{
		Tenant: tenant, Purpose: purpose, URL: endpoint, SubscriptionRef: subRef,
	})
	if d.Permitted {
		return ""
	}
	if err != nil {
		m.debugf("eventing: endpoint refused at authoring", "code", d.Code, "err", err)
	}
	return egressAuthoringError(endpoint, d.Code)
}

// EndpointChecker applies the destination rules to a candidate endpoint. It is
// EXPORTED for the one caller that writes the endpoint column without passing
// through an HTTP handler: the CLI's `eventing subscriptions create`.
//
// That path used to write the column raw — no scheme check, no address guard, no
// policy — while its own --endpoint help text promised "https required". Nothing
// enforced it, so an http:// endpoint written there was POSTed in cleartext, and any
// authoring-time policy was void for as long as a second write path existed. The fix
// is to share the definition rather than to copy it: a second copy is how the two
// answers drift apart again.
type EndpointChecker struct {
	// AllowLoopback mirrors WithAllowLoopback (tests and single-box development).
	AllowLoopback bool
	// Policy is the operator's destination policy; nil means none is in force.
	Policy EgressPolicySource
	// Resolver looks a destination up once; nil uses the default resolver.
	Resolver egress.Resolver
	// Rollout is this deployment's durable disposition (unit G). It is REQUIRED
	// for this check to agree with what a delivery will do: without it, a CLI on a
	// fresh install would report "no policy in force, permitted" while every send
	// refuses, and the CLI would be the second copy of the rule that got it wrong —
	// for the third time in this campaign.
	Rollout EgressRolloutSource
	// Compat, when non-nil, lets the check see the destinations this deployment
	// already had. The CLI supplies one built over the store it already opens, so
	// checking an EXISTING subscription gives the same answer its deliveries get.
	Compat *egressCompat
	// SubscriptionRef names the subscription being checked, when there is one, and
	// Purpose says what the check is FOR. A create leaves both at their zero values and
	// can never inherit a compatibility exception.
	SubscriptionRef model.ID
	Purpose         EgressPurpose
}

// NewEndpointChecker builds a checker that shares the engine's compatibility record,
// over any store surface with tenant-pinned transactions. It is how the CLI gets an
// answer identical to the API's rather than an approximation of it.
func NewEndpointChecker(allowLoopback bool, pol EgressPolicySource, rollout EgressRolloutSource, data EgressCompatStore, clock model.Clock) EndpointChecker {
	c := EndpointChecker{AllowLoopback: allowLoopback, Policy: pol, Rollout: rollout, Purpose: EgressCreate}
	if data != nil {
		c.Compat = newEgressCompat(data, clock)
	}
	return c
}

// Check reports why an endpoint may not be used, and RETURNS THE DECISION so a caller
// that goes on to send can pin the addresses it authorized.
//
// Returning only an error was a real defect and not a stylistic one: the caller
// discarded the resolution, opened its own connection, and let the dialer resolve the
// name a second time — which is precisely the rebinding gap the pin exists to close,
// reopened by an API that had no way to hand the answer over.
func (c EndpointChecker) Check(ctx context.Context, tenant model.TenantID, endpoint string) (egress.Decision, error) {
	// The transport rule first, because it is immutable: no rollout mode and no
	// operator policy makes cleartext or an unknown scheme acceptable.
	if msg := validateEndpointURL(endpoint, c.AllowLoopback); msg != "" {
		return egress.Decision{}, fmt.Errorf("endpoint: %s", msg)
	}
	// And then the SAME decision the engine makes, through the same code. This used to
	// be a parallel implementation of the policy walk, and a parallel implementation is
	// how the CLI came to refuse every hostname destination while the engine accepted
	// them.
	dd := destinationDecider{policy: c.Policy, rollout: c.Rollout, resolver: c.Resolver, compat: c.Compat}
	d, err := dd.authorize(ctx, egressRequest{
		Tenant: tenant, Purpose: c.Purpose, URL: endpoint, SubscriptionRef: c.SubscriptionRef,
	})
	if err != nil && !d.Permitted {
		// The underlying cause is kept, because a CLI user is the operator: unlike the
		// tenant-facing 400, they are entitled to know that the lookup failed rather than
		// that the destination was refused.
		return d, fmt.Errorf("endpoint: %s: %w", egressAuthoringError(endpoint, d.Code), err)
	}
	if !d.Permitted {
		return d, fmt.Errorf("endpoint: %s", egressAuthoringError(endpoint, d.Code))
	}
	return d, nil
}

// egressDenialOutcome maps a denial to the module's delivery outcome vocabulary.
//
// The token is derived from the STABLE denial code and carries no fragment of the
// policy — not the rule that would have matched, not how many rules exist, not
// whether the host was covered but the port was not... except that last one, which
// is deliberately distinguishable because an operator debugging their own
// destination needs it and it discloses only what the caller already supplied.
//
// The rest is withheld on purpose: a holder of eventing:subscription:write must not
// be able to enumerate an operator's allow-list by watching which destinations
// produce which message.
func egressDenialOutcome(code string) string {
	switch code {
	case egress.CodePortNotAllowed:
		return "egress_port_denied"
	case egress.CodeUnresolvable:
		return "egress_unresolvable"
	case egress.CodePolicyUnavailable:
		return "egress_policy_unavailable"
	case egress.CodeAddressNotAllowlisted:
		return "egress_address_denied"
	case egress.CodePolicyRequired:
		// Preserved verbatim rather than folded into the generic denial: it is the one
		// refusal whose remediation belongs to the PLATFORM OPERATOR, and an operator
		// reading the ledger has to be able to separate "this deployment has authorized
		// nothing yet" from "this destination was refused".
		return egress.CodePolicyRequired
	case egress.CodeRolloutUnavailable:
		return "egress_rollout_unavailable"
	case egress.CodeSeedIncomplete:
		return "egress_compat_seed_incomplete"
	default:
		return "egress_destination_denied"
	}
}

// egressAuthoringError is the message a caller sees when a destination is refused at
// authoring time. It names the destination the caller just supplied and nothing
// else, for the reason above.
func egressAuthoringError(dest string, code string) string {
	switch code {
	case egress.CodeUnresolvable:
		return fmt.Sprintf("endpoint %s did not resolve, so it cannot be checked against the operator's egress policy", dest)
	case egress.CodePolicyUnavailable:
		return "the operator's egress policy could not be read, so no destination can be authorized right now"
	case egress.CodeRolloutUnavailable, egress.CodeSeedIncomplete:
		// Deliberately the same text as an unreadable policy. Both mean "this plane
		// could not decide, try again", and distinguishing which internal record was
		// unavailable tells a tenant caller about the deployment's rollout posture
		// without telling them anything they can act on.
		return "the operator's egress policy could not be read, so no destination can be authorized right now"
	case egress.CodePolicyRequired:
		// The one denial that names its OWNER, because that is the only actionable fact
		// and withholding it would send the caller to debug their own destination
		// forever. It still discloses nothing about the policy: there is no policy.
		return "this deployment requires a platform operator to authorize event destinations before one can be used; no policy has been authored yet"
	default:
		// ONE message for every refusal, and that is the point rather than laziness.
		// Distinguishing "the host is allowed but not that port" from "the host is not
		// allowed" turns the 400 into a MEMBERSHIP ORACLE: a holder of
		// eventing:subscription:write probes a host on a port it expects to be closed,
		// and a port-specific refusal confirms the host is on the operator's
		// allow-list. The same argument retires the address-specific message. The
		// precise code still reaches the delivery ledger, where an operator reads it
		// and a tenant does not.
		return fmt.Sprintf("endpoint %s is not permitted by the operator's egress policy", dest)
	}
}
