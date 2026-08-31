// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	cedar "github.com/cedar-policy/cedar-go"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Typed evidence is deliberately additive. Nothing wires these optional seams into
// readiness, routing, or an authorization decision in this cut. The producers below
// therefore make no attempt to recover a legacy decision's provenance: a missing,
// malformed, panicking, or legacy contributor remains UNKNOWN.
var (
	_ auth.PolicyEvidenceEvaluator  = (*evaluator)(nil)
	_ auth.PolicyEvidenceEvaluator  = (*chainEvaluator)(nil)
	_ auth.PolicyEvidenceEvaluator  = (*scopedEngine)(nil)
	_ auth.ScopedEvidenceAuthorizer = (*scopedEngine)(nil)
)

const (
	evidenceCodeNativeUnavailable = "governance_native_abac_evidence_unavailable"
	evidenceCodeNativeClean       = "governance_native_abac_forbid_absent"
	evidenceCodeNativeBroken      = "governance_native_abac_forbid_matched"

	evidenceCodeChainUnavailable = "governance_policy_evidence_unavailable"
	evidenceCodeChainLegacy      = "governance_policy_evidence_legacy"
	evidenceCodeChainClean       = "governance_policy_forbid_absent"
	evidenceCodeChainBroken      = "governance_policy_forbid_matched"

	evidenceCodeScopedUnavailable = "governance_scoped_evidence_unavailable"
	evidenceCodeScopedGuardClean  = "governance_resource_guard_not_applicable"
	evidenceCodeScopedGuardBroken = "governance_resource_guard_broken"
	evidenceCodeScopedClean       = "governance_scoped_forbid_absent"
	evidenceCodeScopedBroken      = "governance_scoped_forbid_matched"
)

var (
	errEvidenceUnavailable = errors.New("governance: authorization evidence unavailable")
	errEvidenceMalformed   = errors.New("governance: authorization evidence malformed")
	// A duplicate policy ID is not just an unavailable later row: two payloads claim
	// to be the same durable authority. Even an earlier matching deny cannot tell
	// which payload is authoritative, so this distinct error never upgrades to
	// BROKEN in the native scan.
	errEvidenceAmbiguous = errors.New("governance: authorization evidence ambiguous")
)

// EvaluateEvidence establishes the native ABAC deny-overlay directly from one
// tenant-pinned View. It intentionally does not call compiledFor: that cache is a
// legacy enforcement optimization, whereas evidence needs every active canonical row,
// the exact authorization epoch, and the database's transaction clock in one snapshot.
//
// The caller supplies the sole finite evidence horizon through ctx.Deadline. There is
// no locally invented TTL: without an authoritative finite deadline this producer has
// no bounded CLEAN/BROKEN witness and returns UNKNOWN.
func (e *evaluator) EvaluateEvidence(
	ctx context.Context,
	req auth.Request,
) (decision auth.PolicyEvidenceDecision, err error) {
	decision = unknownGovernancePolicyEvidence(evidenceCodeNativeUnavailable)
	defer func() {
		if recover() != nil {
			decision = unknownGovernancePolicyEvidence(evidenceCodeNativeUnavailable)
			err = nil
		}
	}()
	if e == nil || e.data == nil || req.Tenant.IsZero() || req.Tenant.IsSystem() {
		return decision, nil
	}

	var (
		fact          store.AuthorizationFactRef
		observedAt    time.Time
		freshUntil    time.Time
		matchedForbid bool
	)
	viewErr := e.data.View(ctx, req.Tenant, func(sc store.Scope) error {
		var readErr error
		observedAt, freshUntil, fact, readErr = governanceEvidenceWitness(ctx, sc, req.Tenant, time.Time{})
		if readErr != nil {
			return readErr
		}
		scan, listErr := scanCanonicalABACEvidenceRules(ctx, sc, req.Tenant, req)
		if listErr != nil {
			// Continue draining/validating after a match, but a canonical matched
			// deny is independently established and dominates a later ordinary
			// repository/corruption outage. A duplicate ID is different: it makes
			// the durable authority itself ambiguous, so it remains UNKNOWN.
			if scan.matched && !errors.Is(listErr, errEvidenceAmbiguous) {
				matchedForbid = true
				return nil
			}
			return listErr
		}
		matchedForbid = scan.matched
		return nil
	})
	if viewErr != nil {
		return decision, nil
	}
	check := auth.CheckEvidence{Verdict: auth.CheckClean, Code: evidenceCodeNativeClean}
	if matchedForbid {
		check = auth.CheckEvidence{Verdict: auth.CheckBroken, Code: evidenceCodeNativeBroken}
	}
	return auth.PolicyEvidenceDecision{
		ForbidAbsence: check,
		Facts:         []store.AuthorizationFactRef{fact},
		ObservedAt:    observedAt,
		FreshUntil:    freshUntil,
	}, nil
}

// EvaluateEvidence folds only opt-in typed members. A legacy OPA/static/external
// Cedar evaluator is deliberately not invoked: its historical Decision cannot prove
// typed provenance. UNKNOWN members do not stop the scan, so an independent later
// BROKEN contribution remains a decisive restriction. onDeny is enforcement/audit
// plumbing and is likewise never called by this observational seam.
func (c *chainEvaluator) EvaluateEvidence(
	ctx context.Context,
	req auth.Request,
) (decision auth.PolicyEvidenceDecision, err error) {
	decision = unknownGovernancePolicyEvidence(evidenceCodeChainUnavailable)
	defer func() {
		if recover() != nil {
			decision = unknownGovernancePolicyEvidence(evidenceCodeChainUnavailable)
			err = nil
		}
	}()
	if c == nil || len(c.members) == 0 {
		return decision, nil
	}

	var (
		cleanContributions  []auth.PolicyEvidenceDecision
		brokenContributions []auth.PolicyEvidenceDecision
		unknown             bool
	)
	for _, member := range c.members {
		producer, ok := member.(auth.PolicyEvidenceEvaluator)
		if !ok {
			// Keep scanning: a legacy member is UNKNOWN, not an excuse to hide a
			// later explicitly established forbid.
			unknown = true
			continue
		}
		contribution, callErr := governancePolicyEvidenceSafe(ctx, producer, req)
		if callErr != nil || !validGovernancePolicyEvidence(contribution) {
			unknown = true
			continue
		}
		switch contribution.ForbidAbsence.Verdict {
		case auth.CheckBroken:
			brokenContributions = append(brokenContributions, contribution)
		case auth.CheckClean:
			cleanContributions = append(cleanContributions, contribution)
		default:
			unknown = true
		}
	}

	// A BROKEN witness is retained only with the facts/window belonging to BROKEN
	// contributors. It must not borrow a clean contributor's horizon just because
	// both happen to be in one chain.
	if len(brokenContributions) > 0 {
		facts, observedAt, freshUntil, ok := combineGovernancePolicyEvidence(brokenContributions)
		if !ok {
			return decision, nil
		}
		return auth.PolicyEvidenceDecision{
			ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckBroken, Code: evidenceCodeChainBroken},
			Facts:         facts,
			ObservedAt:    observedAt,
			FreshUntil:    freshUntil,
		}, nil
	}
	if unknown {
		return unknownGovernancePolicyEvidence(evidenceCodeChainLegacy), nil
	}
	if len(cleanContributions) == 0 {
		return decision, nil
	}
	facts, observedAt, freshUntil, ok := combineGovernancePolicyEvidence(cleanContributions)
	if !ok {
		return decision, nil
	}
	return auth.PolicyEvidenceDecision{
		ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckClean, Code: evidenceCodeChainClean},
		Facts:         facts,
		ObservedAt:    observedAt,
		FreshUntil:    freshUntil,
	}, nil
}

// EvaluateEvidence is the typed restrict-view companion for the authored scoped
// Cedar member when it is present in a policy chain (for example hook PEPs). It uses
// the same immutable runtime snapshot/bracket as ScopedEvidence, but deliberately
// evaluates the basic graph used by legacy Evaluate. Scope-unresolvable or diagnostic
// failures are UNKNOWN; only a clean Cedar forbid reason is an established BROKEN fact.
func (e *scopedEngine) EvaluateEvidence(
	ctx context.Context,
	req auth.Request,
) (decision auth.PolicyEvidenceDecision, err error) {
	decision = unknownGovernancePolicyEvidence(evidenceCodeScopedUnavailable)
	defer func() {
		if recover() != nil {
			decision = unknownGovernancePolicyEvidence(evidenceCodeScopedUnavailable)
			err = nil
		}
	}()
	before, loaded := e.tenantState(req.Tenant)
	if !governanceEvidenceScopedStateUsable(req.Tenant, before, loaded) || e == nil || e.resolver == nil || e.resolver.data == nil {
		return decision, nil
	}
	deadline, deadlineErr := governanceEvidenceScopedDeadline(before, e.maxStaleness)
	if deadlineErr != nil {
		return decision, nil
	}

	var (
		fact       store.AuthorizationFactRef
		observedAt time.Time
		freshUntil time.Time
		diag       cedar.Diagnostic
		scopeFail  bool
	)
	viewErr := e.resolver.data.View(ctx, req.Tenant, func(sc store.Scope) error {
		var readErr error
		observedAt, freshUntil, fact, readErr = governanceEvidenceWitness(ctx, sc, req.Tenant, deadline)
		if readErr != nil {
			return readErr
		}
		if fact != before.generation {
			return errEvidenceUnavailable
		}
		if before.set != nil {
			_, diag, scopeFail = evalGrantBasicDetailed(before.set, req, observedAt)
		}
		return nil
	})
	after, afterLoaded := e.tenantState(req.Tenant)
	if viewErr != nil || !governanceEvidenceScopedStateStable(req.Tenant, before, loaded, after, afterLoaded, fact) {
		return decision, nil
	}
	check := auth.CheckEvidence{Verdict: auth.CheckClean, Code: evidenceCodeScopedClean}
	// A cleanly matched authored forbid is independently established, even if a
	// different policy emitted a diagnostic in the same Cedar call. Do not let an
	// unrelated permit error erase a real restriction witness. H-03 and errored
	// forbid paths have no such reason and remain UNKNOWN below.
	if before.set != nil && hasCedarForbidReason(before.set.policies, diag) {
		check = auth.CheckEvidence{Verdict: auth.CheckBroken, Code: evidenceCodeScopedBroken}
	} else if before.set != nil && (scopeFail || len(diag.Errors) > 0 || hasErroredForbid(before.set.policies, diag)) {
		return decision, nil
	}
	return auth.PolicyEvidenceDecision{
		ForbidAbsence: check,
		Facts:         []store.AuthorizationFactRef{fact},
		ObservedAt:    observedAt,
		FreshUntil:    freshUntil,
	}, nil
}

// ScopedEvidence evaluates the live authored Cedar snapshot with its exact runtime
// operation bracket. Scope resolution, database clock, and authorization epoch share
// one View; a reload/replay, generation drift, missing binding, or unavailable state
// before/after the View yields UNKNOWN rather than a torn CLEAN/BROKEN witness.
//
// A scope lineage lookup is intentionally not reported CLEAN in this cut. The only
// durable fact currently available here is core.authorization_epoch, which covers
// policy writers but not workspace/resource/session/agent-group lineage. A View makes
// that read coherent, not revalidable after return. An explicit Cedar forbid can still
// be BROKEN; grants degrade to ABSTAIN while ResourceGuard remains UNKNOWN.
func (e *scopedEngine) ScopedEvidence(
	ctx context.Context,
	req auth.Request,
) (decision auth.ScopedEvidenceDecision, err error) {
	decision = unknownGovernanceScopedEvidence(evidenceCodeScopedUnavailable)
	defer func() {
		if recover() != nil {
			decision = unknownGovernanceScopedEvidence(evidenceCodeScopedUnavailable)
			err = nil
		}
	}()
	before, loaded := e.tenantState(req.Tenant)
	if !governanceEvidenceScopedStateUsable(req.Tenant, before, loaded) || e == nil || e.resolver == nil || e.resolver.data == nil {
		return decision, nil
	}
	deadline, deadlineErr := governanceEvidenceScopedDeadline(before, e.maxStaleness)
	if deadlineErr != nil {
		return decision, nil
	}

	var (
		fact          store.AuthorizationFactRef
		observedAt    time.Time
		freshUntil    time.Time
		cedarDecision cedar.Decision
		diag          cedar.Diagnostic
		lineageRead   bool
		resourceGuard auth.CheckEvidence
	)
	viewErr := e.resolver.data.View(ctx, req.Tenant, func(sc store.Scope) error {
		var readErr error
		observedAt, freshUntil, fact, readErr = governanceEvidenceWitness(ctx, sc, req.Tenant, deadline)
		if readErr != nil {
			return readErr
		}
		if fact != before.generation {
			return errEvidenceUnavailable
		}
		var guardErr error
		resourceGuard, guardErr = governanceEvidenceConfinement(ctx, sc, req)
		if guardErr != nil {
			return guardErr
		}
		// An mismatch/indeterminate mutation is an independently established
		// class-invariant forbid. Preserve it even if Cedar would need a lineage
		// read or has a diagnostic: there is no need to fabricate a policy witness
		// before returning this stronger resource-guard fact.
		if resourceGuard.Verdict == auth.CheckBroken {
			return nil
		}

		if before.set == nil {
			return nil
		}
		em, resource, principal, readsLineage, scopeErr := governanceEvidenceScope(ctx, sc, e.resolver, req)
		if scopeErr != nil {
			return scopeErr
		}
		lineageRead = readsLineage
		cedarDecision, diag = cedar.Authorize(before.set.policies, em, cedar.Request{
			Principal: principal,
			Action:    actionUID(req),
			Resource:  resource,
			Context:   scopedContext(req, observedAt),
		})
		return nil
	})
	after, afterLoaded := e.tenantState(req.Tenant)
	if viewErr != nil || !governanceEvidenceScopedStateStable(req.Tenant, before, loaded, after, afterLoaded, fact) {
		return decision, nil
	}
	if resourceGuard.Verdict == auth.CheckBroken {
		return auth.ScopedEvidenceDecision{
			Effect:        auth.EffectForbid,
			ResourceGuard: resourceGuard,
			ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: evidenceCodeScopedUnavailable},
			Facts:         []store.AuthorizationFactRef{fact},
			ObservedAt:    observedAt,
			FreshUntil:    freshUntil,
		}, nil
	}
	// The result can be CLEAN without a store lookup (unconfined or a declared
	// same-workspace target). Cedar scope resolution, however, can independently read
	// workspace/resource/session/group lineage. authorization_epoch does not fence those
	// rows, so such a read degrades the clean resource guard to UNKNOWN and prevents a
	// reusable typed ALLOW unless an explicit forbid independently dominates.
	if lineageRead && resourceGuard.Verdict == auth.CheckClean {
		resourceGuard = auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: evidenceCodeScopedUnavailable}
	}
	forbid := auth.CheckEvidence{Verdict: auth.CheckClean, Code: evidenceCodeScopedClean}
	effect := auth.EffectAbstain
	// A cleanly matched authored forbid remains a BROKEN fact even when another
	// policy emitted an error. It is an independent restriction witness, whereas a
	// diagnostic/H-03 path with no forbid reason is only enforcement fail-closed.
	if before.set != nil && hasCedarForbidReason(before.set.policies, diag) {
		forbid = auth.CheckEvidence{Verdict: auth.CheckBroken, Code: evidenceCodeScopedBroken}
		effect = auth.EffectForbid
	} else if before.set != nil && (len(diag.Errors) > 0 || hasErroredForbid(before.set.policies, diag)) {
		return decision, nil
	}
	// A positive grant cannot be attested if resource lineage was read without its own
	// revalidable fact. The legacy enforcement decision remains unchanged; only typed
	// evidence conservatively abstains.
	if before.set != nil && cedarDecision == cedar.Allow && resourceGuard.Verdict == auth.CheckClean {
		if !e.grantExpiredState(before, loaded, observedAt) {
			effect = auth.EffectGrant
		}
	}
	return auth.ScopedEvidenceDecision{
		Effect:        effect,
		ResourceGuard: resourceGuard,
		ForbidAbsence: forbid,
		Facts:         []store.AuthorizationFactRef{fact},
		ObservedAt:    observedAt,
		FreshUntil:    freshUntil,
	}, nil
}

func unknownGovernancePolicyEvidence(code string) auth.PolicyEvidenceDecision {
	return auth.PolicyEvidenceDecision{ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: code}}
}

func unknownGovernanceScopedEvidence(code string) auth.ScopedEvidenceDecision {
	unknown := auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: code}
	return auth.ScopedEvidenceDecision{Effect: auth.EffectAbstain, ResourceGuard: unknown, ForbidAbsence: unknown}
}

// governanceEvidenceWitness returns the only fact this cut can establish: the exact
// tenant authorization epoch read inside the same View as the database clock. It never
// substitutes e.clock/time.Now, and it rejects an absent/expired context deadline rather
// than minting a TTL locally.
func governanceEvidenceWitness(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	ddilDeadline time.Time,
) (observedAt, freshUntil time.Time, fact store.AuthorizationFactRef, err error) {
	if sc == nil || sc.Tenant() != tenant {
		return time.Time{}, time.Time{}, store.AuthorizationFactRef{}, errEvidenceUnavailable
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.IsZero() {
		return time.Time{}, time.Time{}, store.AuthorizationFactRef{}, errEvidenceUnavailable
	}
	clock, ok := sc.(store.TransactionClock)
	if !ok {
		return time.Time{}, time.Time{}, store.AuthorizationFactRef{}, errEvidenceUnavailable
	}
	now, err := clock.TransactionNow(ctx)
	if err != nil || now.IsZero() {
		return time.Time{}, time.Time{}, store.AuthorizationFactRef{}, errEvidenceUnavailable
	}
	reader, ok := sc.(store.AuthorizationEpochReader)
	if !ok {
		return time.Time{}, time.Time{}, store.AuthorizationFactRef{}, errEvidenceUnavailable
	}
	fact, err = reader.ReadAuthorizationEpoch(ctx)
	if err != nil || !validGovernanceEvidenceEpochFact(tenant, fact) {
		return time.Time{}, time.Time{}, store.AuthorizationFactRef{}, errEvidenceUnavailable
	}
	observedAt = now.Time().UTC()
	freshUntil = deadline.UTC()
	if !ddilDeadline.IsZero() && ddilDeadline.Before(freshUntil) {
		freshUntil = ddilDeadline.UTC()
	}
	if !freshUntil.After(observedAt) {
		return time.Time{}, time.Time{}, store.AuthorizationFactRef{}, errEvidenceUnavailable
	}
	return observedAt, freshUntil, fact, nil
}

type canonicalABACEvidenceScan struct {
	matched bool
}

func scanCanonicalABACEvidenceRules(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	req auth.Request,
) (canonicalABACEvidenceScan, error) {
	// Do not trust a filtered repository response as proof of absence. A decorator
	// could omit an active ABAC row only for the kind/enabled query and manufacture a
	// CLEAN result. Read the complete tenant policy collection, validate its shape,
	// then apply the ABAC/enabled selection locally.
	q := model.Query{Limit: listCap}
	seenCursors := map[string]struct{}{}
	seenPolicy := map[model.ID]struct{}{}
	var scan canonicalABACEvidenceScan
	// Preserve the first ordinary malformed/outage witness but keep draining every
	// row/page that the repository did return. In particular, a duplicate durable
	// ID must dominate an earlier independently matching deny regardless of where
	// a malformed sibling appears in the page. A List error itself has no further
	// observable rows, so it is returned after any rows already validated.
	var firstErr error
	recordErr := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	for {
		page, next, err := sc.Policies().List(ctx, q)
		if err != nil {
			recordErr(err)
			break
		}
		for _, policy := range page {
			parsedID, parseErr := model.ParseID(policy.ID.String())
			if policy.ID.IsZero() || parseErr != nil || parsedID != policy.ID {
				recordErr(errEvidenceMalformed)
				continue
			}
			if _, duplicate := seenPolicy[policy.ID]; duplicate {
				return scan, errEvidenceAmbiguous
			}
			seenPolicy[policy.ID] = struct{}{}
			if policy.Version < 1 || policy.TenantID != tenant {
				recordErr(errEvidenceMalformed)
				continue
			}
			if policy.Kind != policyKindABAC || !policy.Enabled {
				continue
			}
			canonicalRules, policyErr := canonicalABACEvidenceRules(policy)
			if policyErr != nil {
				recordErr(policyErr)
				continue
			}
			// A match is recorded only after this whole row proved canonical. Keep
			// reading later pages: a later ordinary error cannot erase this explicit
			// deny, but an ambiguous duplicate still can (above).
			for _, rule := range canonicalRules {
				if rule.matches(req) {
					scan.matched = true
				}
			}
		}
		if !next.HasMore {
			break
		}
		if next.Cursor == "" {
			recordErr(errEvidenceMalformed)
			break
		}
		if _, repeated := seenCursors[next.Cursor]; repeated {
			recordErr(errEvidenceMalformed)
			break
		}
		seenCursors[next.Cursor] = struct{}{}
		q.Cursor = next.Cursor
	}
	return scan, firstErr
}

// canonicalABACEvidenceRules repeats authoring-time grammar validation and then checks
// that the durable bytes were already canonical. It must not silently normalize a
// tampered row: doing so would turn malformed policy authority into an evidence claim.
func canonicalABACEvidenceRules(policy model.Policy) ([]abacRule, error) {
	raw, err := json.Marshal(policy.Spec)
	if err != nil {
		return nil, errEvidenceMalformed
	}
	canonical, message := canonicalizeABAC(raw)
	if message != "" {
		return nil, errEvidenceMalformed
	}
	equal, err := canonicalPolicySpecsEqual(policy.Spec, canonical)
	if err != nil || !equal {
		return nil, errEvidenceMalformed
	}
	spec, err := parseABACSpec(canonical)
	if err != nil || len(spec.Rules) == 0 {
		return nil, errEvidenceMalformed
	}
	return spec.Rules, nil
}

// governanceEvidenceScopedDeadline caps evidence at the same effective freshness
// deadline that governs live positive scoped grants: tenant bound when positive,
// otherwise the deployment bound. A selected policy under a bound needs a durable DB
// anchor; a signed DDIL selection additionally requires its signed replay anchors to
// agree with that clock. An unselected empty union has no policy that can expire.
func governanceEvidenceScopedDeadline(state scopedTenantState, deploymentBound time.Duration) (time.Time, error) {
	hasRevision := state.freshness.AdoptedRevision != ""
	hasCreated := !state.freshness.AdoptedCreatedAt.IsZero()
	hasAdoptedSelection := state.selection.adopted > 0
	hasAdoptedDigest := state.adoptedDigest != ""
	// DDIL authority is an indivisible tuple even when its effective staleness
	// bound is zero. Validate it before the no-bound early return: accepting a
	// partial signed tuple as an unbounded local policy would make an altered
	// adopted surface look harmless to typed evidence.
	if hasRevision != hasCreated || hasAdoptedSelection != hasAdoptedDigest ||
		hasAdoptedSelection != hasRevision {
		return time.Time{}, errEvidenceMalformed
	}
	if state.freshness.MaxStaleness < 0 || deploymentBound < 0 {
		return time.Time{}, errEvidenceMalformed
	}
	if hasAdoptedSelection {
		if state.freshness.RefreshedAt.IsZero() ||
			!state.freshness.RefreshedAt.Equal(state.freshness.AdoptedCreatedAt) ||
			state.adoptedDigest != state.freshness.AdoptedRevision {
			return time.Time{}, errEvidenceMalformed
		}
	}
	// A tenant override exists only for a signed DDIL adoption. C3 reload rejects
	// this durable shape too; repeat the check here so a manually constructed or
	// decorated runtime state cannot extend an evidence window from it.
	if state.freshness.MaxStaleness > 0 && !hasAdoptedSelection {
		return time.Time{}, errEvidenceMalformed
	}
	bound := deploymentBound
	if state.freshness.MaxStaleness > 0 {
		bound = state.freshness.MaxStaleness
	}
	if bound == 0 || state.selection == (activationID{}) {
		return time.Time{}, nil
	}
	if state.freshness.RefreshedAt.IsZero() {
		return time.Time{}, errEvidenceMalformed
	}
	return state.freshness.RefreshedAt.Add(bound).UTC(), nil
}

func governanceEvidenceScopedStateUsable(tenant model.TenantID, state scopedTenantState, loaded bool) bool {
	return loaded && state.available && state.freshnessValid && !state.identityIncomplete &&
		validGovernanceEvidenceEpochFact(tenant, state.generation) && hasCedarCompiledBinding(state) &&
		state.operation != nil
}

// validGovernanceEvidenceEpochFact is stricter than the writer-side helper: evidence
// must reject a non-canonical tenant spelling and a leased/fenced reference even when
// Kind/ID/Version happen to look like core.authorization_epoch. This producer has no
// lease subject/fence semantics to preserve, and a malformed fact must never become a
// CLEAN/BROKEN provenance witness that a later locker rejects.
func validGovernanceEvidenceEpochFact(tenant model.TenantID, fact store.AuthorizationFactRef) bool {
	parsedTenant, err := model.ParseTenantID(tenant.String())
	if err != nil || parsedTenant != tenant || tenant.IsZero() || tenant.IsSystem() ||
		!validPolicyAuthorizationEpochFact(tenant, fact) {
		return false
	}
	_, _, _, leased := fact.LeaseFenceWitness()
	return !leased
}

func governanceEvidenceScopedStateStable(
	tenant model.TenantID,
	before scopedTenantState,
	beforeLoaded bool,
	after scopedTenantState,
	afterLoaded bool,
	fact store.AuthorizationFactRef,
) bool {
	return governanceEvidenceScopedStateUsable(tenant, before, beforeLoaded) &&
		governanceEvidenceScopedStateUsable(tenant, after, afterLoaded) &&
		before.generation == fact && after.generation == fact &&
		sameCedarRuntimeCapture(before, beforeLoaded, after, afterLoaded) &&
		sameCedarAuthorityState(before, after)
}

func governanceEvidenceScope(
	ctx context.Context,
	sc store.Scope,
	resolver *scopeResolver,
	req auth.Request,
) (cedar.EntityMap, cedar.EntityUID, cedar.EntityUID, bool, error) {
	if resolver == nil {
		return nil, cedar.EntityUID{}, cedar.EntityUID{}, false, errEvidenceUnavailable
	}
	em := cedar.EntityMap{}
	principal := buildPrincipalEntity(req, em)
	resource := resourceUID(req)
	attrs := baseResourceAttrs(req)
	parents, extra, err := resolver.readScope(ctx, sc, req, em)
	if err != nil {
		return nil, cedar.EntityUID{}, cedar.EntityUID{}, false, err
	}
	for key, value := range extra {
		attrs[key] = value
	}
	em[resource] = cedar.Entity{
		UID:        resource,
		Parents:    cedar.NewEntityUIDSet(parents...),
		Attributes: cedar.NewRecord(attrs),
	}
	return em, resource, principal, governanceEvidenceScopeReadsLineage(req), nil
}

func governanceEvidenceScopeReadsLineage(req auth.Request) bool {
	if req.Resource.ID == "" {
		return !req.Resource.WorkspaceID.IsZero()
	}
	switch req.Resource.Kind {
	case "agent", "session", "resource", "agent_group":
		return true
	default:
		return !req.Resource.WorkspaceID.IsZero()
	}
}

// governanceEvidenceConfinement is the subset whose result can be represented
// safely with the sole fact available to this producer. It deliberately mirrors the
// legacy Scoped ordering, but performs all store reads through the View already held by
// ScopedEvidence (rather than calling targetWorkspace, which would open a second View).
//
// A no-store result can be CLEAN: an unconfined principal, or a confined principal
// targeting its declared workspace, cannot be invalidated by resource lineage drift.
// A store-resolved same-workspace entity is only UNKNOWN because authorization_epoch
// does not cover that lineage after the View returns. A mismatch or the legacy
// indeterminate write/recon cases are independently established BROKEN guards.
func governanceEvidenceConfinement(
	ctx context.Context,
	sc store.Scope,
	req auth.Request,
) (auth.CheckEvidence, error) {
	confinedWorkspace, confined := req.Principal.ConfinedWorkspaceIn(req.Tenant)
	if !confined || req.Principal.Superadmin {
		return auth.CheckEvidence{Verdict: auth.CheckClean, Code: evidenceCodeScopedGuardClean}, nil
	}

	target, known, readLineage, err := governanceEvidenceTargetWorkspace(ctx, sc, req)
	if err != nil {
		return auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: evidenceCodeScopedUnavailable}, err
	}
	switch {
	case known && target != confinedWorkspace:
		return auth.CheckEvidence{Verdict: auth.CheckBroken, Code: evidenceCodeScopedGuardBroken}, nil
	case known && readLineage:
		return auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: evidenceCodeScopedUnavailable}, nil
	case known:
		return auth.CheckEvidence{Verdict: auth.CheckClean, Code: evidenceCodeScopedGuardClean}, nil
	case auth.IsAccessGraphReconPerm(req.Permission):
		return auth.CheckEvidence{Verdict: auth.CheckBroken, Code: evidenceCodeScopedGuardBroken}, nil
	case req.Permission.Verb() != auth.VerbRead:
		return auth.CheckEvidence{Verdict: auth.CheckBroken, Code: evidenceCodeScopedGuardBroken}, nil
	case readLineage:
		// The entity lookup did not find a target, but it still consulted volatile
		// lineage. An ordinary read is not an denial, yet its guard cannot be
		// reported CLEAN from authorization_epoch alone.
		return auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: evidenceCodeScopedUnavailable}, nil
	default:
		// An indeterminate ordinary read has no tenant-wide mutation/recon escape;
		// legacy abstains here, so its guard is clean rather than invented
		// unknown authority.
		return auth.CheckEvidence{Verdict: auth.CheckClean, Code: evidenceCodeScopedGuardClean}, nil
	}
}

// governanceEvidenceTargetWorkspace is targetWorkspace's single-View counterpart.
// `readLineage` is true whenever a tree entity lookup was attempted, including a
// not-found result; callers use it to avoid treating that volatile lineage as
// fact-bound CLEAN.
func governanceEvidenceTargetWorkspace(
	ctx context.Context,
	sc store.Scope,
	req auth.Request,
) (target model.ID, known, readLineage bool, err error) {
	id := model.ID(req.Resource.ID)
	if id.IsZero() {
		if req.Resource.WorkspaceID.IsZero() {
			return model.ID(""), false, false, nil
		}
		return req.Resource.WorkspaceID, true, false, nil
	}

	switch req.Resource.Kind {
	case "agent":
		entity, getErr := sc.Agents().Get(ctx, id)
		if errors.Is(getErr, store.ErrNotFound) {
			return model.ID(""), false, true, nil
		}
		if getErr != nil {
			return model.ID(""), false, false, getErr
		}
		return entity.WorkspaceID, true, true, nil
	case "session":
		entity, getErr := sc.Sessions().Get(ctx, id)
		if errors.Is(getErr, store.ErrNotFound) {
			return model.ID(""), false, true, nil
		}
		if getErr != nil {
			return model.ID(""), false, false, getErr
		}
		return entity.WorkspaceID, true, true, nil
	case "resource":
		entity, getErr := sc.Resources().Get(ctx, id)
		if errors.Is(getErr, store.ErrNotFound) {
			return model.ID(""), false, true, nil
		}
		if getErr != nil {
			return model.ID(""), false, false, getErr
		}
		return entity.WorkspaceID, true, true, nil
	case "agent_group":
		entity, getErr := sc.AgentGroups().Get(ctx, id)
		if errors.Is(getErr, store.ErrNotFound) {
			return model.ID(""), false, true, nil
		}
		if getErr != nil {
			return model.ID(""), false, false, getErr
		}
		return entity.WorkspaceID, true, true, nil
	default:
		if req.Resource.WorkspaceID.IsZero() {
			return model.ID(""), false, false, nil
		}
		return req.Resource.WorkspaceID, true, false, nil
	}
}

func governancePolicyEvidenceSafe(
	ctx context.Context,
	producer auth.PolicyEvidenceEvaluator,
	req auth.Request,
) (decision auth.PolicyEvidenceDecision, err error) {
	defer func() {
		if recover() != nil {
			err = errEvidenceUnavailable
		}
	}()
	return producer.EvaluateEvidence(ctx, req)
}

func validGovernancePolicyEvidence(decision auth.PolicyEvidenceDecision) bool {
	if !validGovernanceEvidenceCheck(decision.ForbidAbsence) ||
		!validGovernanceEvidenceFacts(decision.Facts) {
		return false
	}
	requiresWindow := decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) > 0
	if decision.ObservedAt.IsZero() && decision.FreshUntil.IsZero() {
		return !requiresWindow
	}
	return !decision.ObservedAt.IsZero() && decision.FreshUntil.After(decision.ObservedAt)
}

func validGovernanceEvidenceCheck(check auth.CheckEvidence) bool {
	if !check.Verdict.Valid() || check.Code == "" || len(check.Code) > 128 || strings.TrimSpace(check.Code) != check.Code {
		return false
	}
	for _, r := range check.Code {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

// hasCedarForbidReason distinguishes a policy reason from an H-03/diagnostic
// fail-closed result. Cedar reasons carry the exact policy ID, so a permit reason
// or a bare non-Allow decision cannot be promoted into a forbid evidence witness.
func hasCedarForbidReason(policies *cedar.PolicySet, diag cedar.Diagnostic) bool {
	if policies == nil {
		return false
	}
	for _, reason := range diag.Reasons {
		if policy := policies.Get(reason.PolicyID); policy != nil && policy.Effect() == cedar.Forbid {
			return true
		}
	}
	return false
}

func validGovernanceEvidenceFacts(facts []store.AuthorizationFactRef) bool {
	if len(facts) > 64 {
		return false
	}
	for _, fact := range facts {
		if !fact.Kind.Valid() || fact.ID.IsZero() || fact.Version < 1 {
			return false
		}
		parsed, err := model.ParseID(fact.ID.String())
		if err != nil || parsed != fact.ID {
			return false
		}
	}
	return true
}

type governanceEvidenceFactKey struct {
	kind model.Kind
	id   model.ID
}

type governanceEvidenceLease struct {
	present  bool
	subject  string
	fence    int64
	deadline string
}

func combineGovernancePolicyEvidence(
	contributions []auth.PolicyEvidenceDecision,
) ([]store.AuthorizationFactRef, time.Time, time.Time, bool) {
	if len(contributions) == 0 {
		return nil, time.Time{}, time.Time{}, false
	}
	unique := map[governanceEvidenceFactKey]store.AuthorizationFactRef{}
	leases := map[struct {
		kind    model.Kind
		subject string
	}]governanceEvidenceFactKey{}
	var observedAt, freshUntil time.Time
	for _, contribution := range contributions {
		if !validGovernancePolicyEvidence(contribution) || contribution.ObservedAt.IsZero() || contribution.FreshUntil.IsZero() {
			return nil, time.Time{}, time.Time{}, false
		}
		if observedAt.IsZero() || contribution.ObservedAt.After(observedAt) {
			observedAt = contribution.ObservedAt.UTC()
		}
		if freshUntil.IsZero() || contribution.FreshUntil.Before(freshUntil) {
			freshUntil = contribution.FreshUntil.UTC()
		}
		for _, fact := range contribution.Facts {
			key := governanceEvidenceFactKey{kind: fact.Kind, id: fact.ID}
			lease := governanceEvidenceLeaseOf(fact)
			if prior, found := unique[key]; found {
				if prior.Version != fact.Version || governanceEvidenceLeaseOf(prior) != lease {
					return nil, time.Time{}, time.Time{}, false
				}
				continue
			}
			if lease.present {
				leaseKey := struct {
					kind    model.Kind
					subject string
				}{kind: fact.Kind, subject: lease.subject}
				if prior, found := leases[leaseKey]; found && prior != key {
					return nil, time.Time{}, time.Time{}, false
				}
				leases[leaseKey] = key
			}
			unique[key] = fact
			if len(unique) > 64 {
				return nil, time.Time{}, time.Time{}, false
			}
		}
	}
	if !freshUntil.After(observedAt) {
		return nil, time.Time{}, time.Time{}, false
	}
	facts := make([]store.AuthorizationFactRef, 0, len(unique))
	for _, fact := range unique {
		facts = append(facts, fact)
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].Kind != facts[j].Kind {
			return facts[i].Kind < facts[j].Kind
		}
		return facts[i].ID.String() < facts[j].ID.String()
	})
	return facts, observedAt, freshUntil, true
}

func governanceEvidenceLeaseOf(fact store.AuthorizationFactRef) governanceEvidenceLease {
	subject, fence, deadline, ok := fact.LeaseFenceWitness()
	if !ok {
		return governanceEvidenceLease{}
	}
	return governanceEvidenceLease{present: true, subject: subject, fence: fence, deadline: deadline.String()}
}
