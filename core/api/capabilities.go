// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// The SELF capability projection answers, for the CALLING credential only, whether one
// REGISTERED operation is authorized right now, or whether a registered COLLECTION is
// admissible right now. It is a projection of the authorization the route itself would
// perform — never a second, weaker copy of it — and it distinguishes three states from
// each other internally:
//
//   - a positive, which requires EVERY predicate of that route to be established AND a
//     finite evidence window to bound how long the observation may be shown;
//   - a known denial, which is the answer the route would really give;
//   - UNKNOWN, which is "I could not establish this" and is NEVER presented as "your
//     role does not allow it".
//
// Schema 2 discloses only usable positives on concealing operations. Their other
// authority outcomes become undisclosed/not_disclosed at final publication, while
// these internal DENY and UNKNOWN results remain distinct.
//
// ⛔ WHAT IT IS NOT. It is not a lease, not a witness, not a bearer, and not a promise
// that the operation will succeed: readiness, preconditions, If-Match, generation
// conflicts and the recording gate are separate classes and are not evaluated here. The
// handler re-authenticates, re-authorizes and fences transactionally on the real act,
// and it accepts nothing this projection produced.

// CapabilityState is the closed vocabulary of a projection result. The two kinds do not
// share values, so a client cannot present a collection answer as an operation answer.
type CapabilityState string

const (
	// CapabilityAllowed / CapabilityDenied / CapabilityUnknown answer an OPERATION.
	CapabilityAllowed CapabilityState = "allowed"
	CapabilityDenied  CapabilityState = "denied"
	CapabilityUnknown CapabilityState = "unknown"
	// CapabilityUndisclosed is a public non-verdict for a concealing operation.
	// It asserts neither denial, absence nor unavailable evidence.
	CapabilityUndisclosed CapabilityState = "undisclosed"
	// CapabilityReachable / CapabilityNotReachable answer a SURFACE: admission to the
	// collection itself. Reachable asserts nothing about rows — an authorized empty
	// list is reachable — and not_reachable never propagates to another route's
	// operation.
	CapabilityReachable    CapabilityState = "reachable"
	CapabilityNotReachable CapabilityState = "not_reachable"
)

// Capability question kinds.
const (
	capabilityKindSurface   = "surface"
	capabilityKindOperation = "operation"
)

// The closed code catalog. Every code below an UNKNOWN state means the projection could
// not be established; a reporter failure never becomes a denial and never becomes an
// allow.
const (
	// capabilityCodeNotSupported: the operation is not registered, or no adapter covers
	// the shape it was asked about.
	capabilityCodeNotSupported = "not_supported"
	// capabilityCodeInputsRequired: a declared AUTHORIZATION selector is missing or not
	// canonical. It is decided from the question alone, with no row read.
	capabilityCodeInputsRequired = "inputs_required"
	// capabilityCodeEngineUnready: a readiness condition prevents completing the proof.
	capabilityCodeEngineUnready = "engine_unready"
	// capabilityCodeEvidenceUnavailable: authority could not be established — a store
	// failure, an integrity mismatch, a legacy seam with no typed evidence, or a
	// decision with no usable freshness window.
	capabilityCodeEvidenceUnavailable = "evidence_unavailable"
	// capabilityCodeStepUpRequired: assurance is insufficient. Decided BEFORE any
	// lookup, and it promises nothing about what the policy would say afterwards.
	capabilityCodeStepUpRequired = "step_up_required"
	// capabilityCodeStale is the CLIENT's own verdict when its budget ran out or its
	// context changed. The server never emits it; it is published so the contract names
	// one vocabulary rather than two.
	capabilityCodeStale = "stale"
	// capabilityCodeNotPermitted is an established denial on a route that does not
	// conceal existence.
	capabilityCodeNotPermitted = "not_permitted"
	// capabilityCodeNotAvailable records the internal concealed denial/absence class.
	// Schema 2 never publishes it; final non-disclosure also covers UNKNOWN.
	capabilityCodeNotAvailable = "not_available"
	// capabilityCodeNotDisclosed never reveals which nonpositive authority outcome
	// a registered concealing operation produced.
	capabilityCodeNotDisclosed = "not_disclosed"
	// capabilityCodeAdmitted / capabilityCodeAuthorized label the two positives.
	capabilityCodeAdmitted   = "admitted"
	capabilityCodeAuthorized = "authorized"
)

// capabilityMaxQuestions is the ratified batch ceiling. It protects one request; it does
// not cap the inventory of operations and does not stop a later batch.
const capabilityMaxQuestions = 32

// capabilityMaxRefreshAfter is the ratified presentation ceiling. The real budget is the
// MINIMUM of this and the remaining evidence window, so a long-lived window never
// produces a long-lived claim.
const capabilityMaxRefreshAfter = 30 * time.Second

// ModuleCapabilityQuestion is what the engine hands a module when it needs the module's
// OWN authorization stage projected for one of the module's registered operations.
//
// Everything in it is SERVER-DERIVED: the principal is the authenticated caller, the
// tenant is the resolved one, and the resource is the outer resource the route wrapper
// itself would have used — with an entity's workspace read from the STORED row. A module
// must not accept any of it from a client, because none of it came from one.
type ModuleCapabilityQuestion struct {
	// Method and Pattern identify the registered operation, exactly as mounted.
	Method  string
	Pattern string
	// Permission is the route's declared permission.
	Permission auth.Permission
	// Principal is the authenticated caller. It is the subject; there is no other.
	Principal auth.Principal
	// Tenant is the resolved tenant of the request.
	Tenant model.TenantID
	// Resource is the outer resource the route would authorize against.
	Resource auth.ResourceAttrs
	// RequestedWorkspace is the workspace the CALLER named, or zero when it named
	// none. It is an assertion to be checked against the stored one, never a source of
	// authority.
	RequestedWorkspace model.ID
}

// ModuleCapabilityResult is a module's projection of its own authorization stage.
//
// The window is what THIS stage bounds, and a zero FreshUntil means "this stage imposes
// no horizon of its own" — the shape a non-expiring grant genuinely has. It never means
// "no evidence": the engine still requires the outer decision's own finite window before
// it will emit a positive, so a stage with no horizon narrows nothing and invents nothing.
// A module that DOES name a horizon must name a coherent one (ObservedAt before
// FreshUntil); an incoherent pair is refused rather than rounded.
type ModuleCapabilityResult struct {
	State      CapabilityState
	Code       string
	ObservedAt time.Time
	FreshUntil time.Time
}

// ModuleCapabilityOperation names ONE registered operation and NOTHING ELSE. It carries
// no target, no id and no row, so every question a module can answer from it is
// answerable BEFORE the engine has looked anything up.
//
// ⛔ THAT EMPTINESS IS THE POINT. Support and availability had to become target-free
// questions because asking them after the lookup is what made the answers depend on
// which rows exist — see SupportsModuleCapability below.
type ModuleCapabilityOperation struct {
	Method     string
	Pattern    string
	Permission auth.Permission
}

// ModuleCapabilityScope is the target-independent scope an availability probe needs: the
// authenticated caller, its tenant, and the workspace the QUESTION named. The workspace
// is the caller's declared selector, never a row's stored lineage — a probe that needed
// the row would defeat its own purpose.
type ModuleCapabilityScope struct {
	Tenant    model.TenantID
	Workspace model.ID
	Principal auth.Principal
}

// ModuleCapabilityAvailability reports whether the module's own authorization stage can
// be evaluated AT ALL for this caller and scope right now.
//
// ⛔ IT SEPARATES "I CANNOT LOOK" FROM "THE ANSWER IS NO", AND THAT DISTINCTION IS THE
// WHOLE REASON IT EXISTS. A principal that is simply not a directory principal of this
// scope is AVAILABLE — the stage can answer, and its answer will be a denial. Only a
// genuine outage (an unbound port, a resolver error, an unreadable closure) is
// unavailable. Collapsing the two would turn every legitimate refusal into UNKNOWN.
type ModuleCapabilityAvailability struct {
	// Available reports that the stage can be evaluated. False means the projection
	// must stop BEFORE resolving any target.
	Available bool
	// Code is the closed-catalog reason when Available is false.
	Code string
}

// ModuleCapabilityProjector is the OPTIONAL capability a module asserts when it can
// answer, WITHOUT executing the operation, whether its own authorization stage permits
// one of its registered operations right now.
//
// ⛔ A MODULE THAT DOES NOT IMPLEMENT IT IS UNKNOWN, NEVER ALLOWED. The engine's outer
// decision is only one of the stages a module route applies; presenting it alone as the
// answer would sell a partial proof as a complete one. That is why an absent adapter is
// not_supported rather than a fallback to the outer boolean.
//
// ⛔ AND THE PROJECTION MUST NOT PERFORM THE OPERATION. It may not call the mutation, the
// handler, a dry-run-then-rollback, or anything that writes rows, audit, expiry or a
// lease. The result is not consumable as authority by the later act.
type ModuleCapabilityProjector interface {
	// SupportsModuleCapability reports whether this module has a typed adapter for the
	// operation, decided from the REGISTERED operation alone.
	//
	// ⛔ IT IS A SEPARATE METHOD BECAUSE THE ANSWER MUST PRECEDE THE LOOKUP, and the
	// independent review measured what happens when it does not: on a healthy runtime,
	// asking about a registered-but-unadapted route returned `unknown/not_supported`
	// for an EXISTING hidden channel and `denied/not_available` for an absent id —
	// because the missing row was answered from the engine's concealment branch before
	// the module was ever consulted. Two different answers for "no adapter" is an
	// existence oracle that needs no outage at all.
	SupportsModuleCapability(ModuleCapabilityOperation) bool
	// ModuleCapabilityAvailable reports whether the stage can be evaluated for this
	// caller and scope, without naming or reading any target.
	//
	// ⛔ ALSO BEFORE THE LOOKUP, and for the sharper version of the same defect: with a
	// shared authority resolver failing, the review measured `unknown` for existing
	// channels and `denied` for an absent id. An outage that identifies hidden rows is
	// worse than the outage. Deciding common availability first makes every answer in
	// that state identical.
	ModuleCapabilityAvailable(
		context.Context, ModuleCapabilityOperation, ModuleCapabilityScope,
	) ModuleCapabilityAvailability
	// ProjectModuleCapability answers the stage for one resolved target.
	ProjectModuleCapability(context.Context, ModuleCapabilityQuestion) ModuleCapabilityResult
}

// capabilityProjection is the engine's internal result for one question, before it is
// rendered onto the wire.
type capabilityProjection struct {
	state      CapabilityState
	code       string
	observedAt time.Time
	freshUntil time.Time
	// inputRejected is set only at enumerated target-free request gates. A module
	// or authority producer cannot opt its result out of non-disclosure by code.
	inputRejected bool
}

// positive reports whether the projection is one of the two states that carry a budget.
func (p capabilityProjection) positive() bool {
	return p.state == CapabilityAllowed || p.state == CapabilityReachable
}

// unknownProjection is the deny-closed default. Every path that cannot establish a
// verdict goes through it, so an omitted branch cannot become an allow or a denial.
func unknownProjection(code string) capabilityProjection {
	return capabilityProjection{state: CapabilityUnknown, code: code}
}

// inputRejectedProjection preserves diagnostics for unregistered/unsupported shapes,
// canonical selectors and assurance, all checked before authority/target evaluation.
func inputRejectedProjection(code string) capabilityProjection {
	p := unknownProjection(code)
	p.inputRejected = true
	return p
}

// deniedFor records an ESTABLISHED denial with the concealment policy of that route: a
// route that hides existence answers one shape for absent, foreign and denied alike.
func deniedFor(descriptor routeDescriptor, kind string) capabilityProjection {
	state := CapabilityDenied
	if kind == capabilityKindSurface {
		state = CapabilityNotReachable
	}
	code := capabilityCodeNotPermitted
	if descriptor.entity != nil && descriptor.entity.ConcealDeniedAsNotFound {
		code = capabilityCodeNotAvailable
	}
	return capabilityProjection{state: state, code: code}
}

// projectOuterDecision runs the route's OWN outer authorization, through the same door
// the route was registered with, and requires a usable evidence window before it will
// call the result a positive.
//
// ⛔ A BOOLEAN ALLOW IS NOT A POSITIVE HERE, and that is the whole point of the second
// call. Authorize answers "would this request pass?" and nothing about for how long the
// answer may be shown; AuthorizeEvidence answers with typed predicates and a finite
// window. When the configured scoped engine or deny-overlay has no typed evidence seam
// (a legacy producer), the window cannot be established at all, so the positive is
// IMPOSSIBLE rather than approximated — that is UNKNOWN, not allow-with-a-guessed-TTL.
//
// ⛔ AND THE TWO ARE CROSS-CHECKED RATHER THAN AVERAGED. Authorize is the authority for a
// DENIAL, because it is literally what the wrapper would answer. If it allows while the
// evidence path establishes a denial, the two disagree about this request and nothing is
// established: that is UNKNOWN. Fabricating either answer from the disagreement would
// invent a decision the engine never made.
func (s *Server) projectOuterDecision(
	ctx context.Context,
	req auth.Request,
	governed routeGovernance,
) capabilityProjection {
	if s.authz == nil {
		return unknownProjection(capabilityCodeEvidenceUnavailable)
	}
	if governed {
		witness, err := s.authz.AuthorizeRoute(ctx, req)
		switch {
		case err == nil:
			return positiveFromWindow(witness.Decision.ObservedAt, witness.Decision.FreshUntil)
		case errors.Is(err, auth.ErrStepUpRequired):
			return unknownProjection(capabilityCodeStepUpRequired)
		case errors.Is(err, auth.ErrRouteUndecided), errors.Is(err, auth.ErrAuthorizerUnavailable):
			return unknownProjection(capabilityCodeEvidenceUnavailable)
		default:
			return capabilityProjection{state: CapabilityDenied, code: capabilityCodeNotPermitted}
		}
	}
	decision := s.authz.Authorize(ctx, req)
	evidence := s.authz.AuthorizeEvidence(ctx, req)
	switch {
	case !decision.Allow && evidence.Outcome == auth.EvidenceDeny:
		// BOTH established the same negative: the wrapper would refuse, and the typed
		// path names the broken predicate. That is a real denial.
		return capabilityProjection{state: CapabilityDenied, code: capabilityCodeNotPermitted}
	case decision.Allow && evidence.Outcome == auth.EvidenceAllow:
		return positiveFromWindow(evidence.ObservedAt, evidence.FreshUntil)
	default:
		// Everything else is UNKNOWN, and the case that forced this shape is
		// `!Allow` with EvidenceUnknown.
		//
		// ⛔ A FALSE BOOLEAN IS NOT AN AUTHORIZATION FACT. Authorize returns
		// `Allow:false` when the scoped engine or the deny-overlay ERRORS
		// (authorizer.go: "scoped: evaluation error", "policy: evaluation error") —
		// it fails closed, which is right for serving a request and wrong as a
		// statement about policy. The evidence path classifies exactly those as
		// UNKNOWN. Reading the boolean alone published a broken evaluator to an
		// operator as "your role does not allow it", which is the one mistranslation
		// this whole projection exists to prevent: the two answers have different
		// remedies, and only one of them is the operator's to act on.
		//
		// A disagreement in either direction lands here too, and deliberately: if the
		// wrapper and the typed path do not answer the same question the same way,
		// nothing about this request is established, and inventing either answer from
		// the discrepancy would be fabricating the one we happened to prefer.
		//
		// This reads only TYPED outcomes. It never parses Decision.Reason, never
		// changes core/auth, and never alters the door the real route goes through.
		return unknownProjection(capabilityCodeEvidenceUnavailable)
	}
}

// positiveFromWindow accepts a positive ONLY with a finite, ordered window. A decision
// whose window is absent, inverted or empty cannot bound how long it may be believed, so
// it is not a reusable positive.
func positiveFromWindow(observedAt, freshUntil time.Time) capabilityProjection {
	if observedAt.IsZero() || !freshUntil.After(observedAt) {
		return unknownProjection(capabilityCodeEvidenceUnavailable)
	}
	return capabilityProjection{
		state: CapabilityAllowed, code: capabilityCodeAuthorized,
		observedAt: observedAt.UTC(), freshUntil: freshUntil.UTC(),
	}
}

// narrowWindow intersects a projection's window with a further contribution. A
// contribution that closes earlier moves the horizon IN, never out.
//
// ⛔ A ZERO FreshUntil IS "THIS STAGE IMPOSES NO HORIZON", NOT "THIS STAGE HAS NO
// EVIDENCE", and reading it the second way is a real defect this comment records rather
// than describes in the abstract. The K3 OR-horizon is exactly that shape: among the
// grants that still confer the bit, a NON-EXPIRING one removes the extra bound
// (channelGrantBitFreshUntil reports constrained=false). Treating that as a malformed
// window turned the commonest positive in the product — an administrator holding a grant
// with no expiry — into UNKNOWN while the real route served 200. The causal battery
// caught it because it measures an expiring grant and a durable one in the same run.
//
// A contribution that names a horizon must still name a coherent one: an observation
// with no instant, or a horizon that does not outlast it, is not a window and is refused.
func (p capabilityProjection) narrowWindow(observedAt, freshUntil time.Time) capabilityProjection {
	if freshUntil.IsZero() {
		if !observedAt.IsZero() && observedAt.After(p.observedAt) {
			p.observedAt = observedAt.UTC()
		}
		return p
	}
	if observedAt.IsZero() || !freshUntil.After(observedAt) {
		return unknownProjection(capabilityCodeEvidenceUnavailable)
	}
	if observedAt.After(p.observedAt) {
		p.observedAt = observedAt.UTC()
	}
	if p.freshUntil.IsZero() || freshUntil.Before(p.freshUntil) {
		p.freshUntil = freshUntil.UTC()
	}
	return p
}

// refreshAfter is the presentation budget for a positive: the minimum of the operational
// ceiling and what REMAINS of the evidence window at this instant.
//
// ⛔ IT IS A REMAINDER, NOT A DURATION. Reading FreshUntil-ObservedAt would restart a
// window that has been open since the decision, so a caller could be handed 30 seconds
// of budget on a window with two left. A budget that has already run out is not a short
// budget: it is no positive at all.
func refreshAfter(now time.Time, freshUntil time.Time) (int64, bool) {
	remaining := freshUntil.Sub(now)
	if remaining <= 0 {
		return 0, false
	}
	if remaining > capabilityMaxRefreshAfter {
		remaining = capabilityMaxRefreshAfter
	}
	milliseconds := remaining.Milliseconds()
	if milliseconds <= 0 {
		return 0, false
	}
	return milliseconds, true
}
