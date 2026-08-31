// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CheckVerdict is one independently evidenced authorization predicate. Unknown
// is deliberately the zero value: an omitted, malformed or unavailable check
// can never become a clean authorization fact by default.
type CheckVerdict uint8

const (
	// CheckUnknown means the predicate could not be established.
	CheckUnknown CheckVerdict = iota
	// CheckClean means the predicate was established and permits continuation.
	CheckClean
	// CheckBroken means the predicate was established and denies continuation.
	CheckBroken
)

// Valid reports whether v is one of the closed CheckVerdict values.
func (v CheckVerdict) Valid() bool {
	return v <= CheckBroken
}

// CheckEvidence carries a typed verdict plus a bounded, non-sensitive machine
// code. Code is descriptive only; no authorization branch inspects its text.
type CheckEvidence struct {
	Verdict CheckVerdict
	Code    string
}

// EvidenceOutcome is the tri-state result of the complete evidence decision.
// Unknown is the zero value so missing evidence never defaults to allow or to a
// business denial.
type EvidenceOutcome uint8

const (
	// EvidenceUnknown means at least one necessary predicate was unavailable and
	// no independently established denial dominated it.
	EvidenceUnknown EvidenceOutcome = iota
	// EvidenceDeny means at least one predicate was definitively broken.
	EvidenceDeny
	// EvidenceAllow means every necessary predicate was definitively clean.
	EvidenceAllow
)

// Valid reports whether o is one of the closed EvidenceOutcome values.
func (o EvidenceOutcome) Valid() bool {
	return o <= EvidenceAllow
}

// AuthorizationEvidence separates the three predicates a caller must preserve
// when it converts core authorization into a wider control-plane witness. Facts
// are canonical, duplicate-free version references suitable for the store's
// single AuthoritySnapshotLocker call. On DENY they attest one deterministic,
// independently sufficient negative rather than every diagnostic component.
// The value is not a transferable bearer or a self-contained request proof:
// trusted composition code must consume the direct result for the same Request
// and must never accept caller-supplied or replayed AuthorizationEvidence.
type AuthorizationEvidence struct {
	Outcome        EvidenceOutcome
	CorePermission CheckEvidence
	ResourceGuard  CheckEvidence
	ForbidAbsence  CheckEvidence
	Facts          []store.AuthorizationFactRef
	ObservedAt     time.Time
	FreshUntil     time.Time
}

// ScopedEvidenceDecision is the opt-in, typed companion to ScopedDecision.
// Effect retains the grant/forbid/abstain algebra, while ResourceGuard and
// ForbidAbsence state independently why the scoped evaluation is safe to use.
// A producer that establishes any non-UNKNOWN fact must provide a finite
// evidence window.
type ScopedEvidenceDecision struct {
	Effect        Effect
	ResourceGuard CheckEvidence
	ForbidAbsence CheckEvidence
	Facts         []store.AuthorizationFactRef
	ObservedAt    time.Time
	FreshUntil    time.Time
}

// ScopedEvidenceAuthorizer is an optional extension implemented by a scoped
// engine that can produce structured evidence. Merely implementing
// ScopedAuthorizer is intentionally insufficient: the legacy result collapses
// resource guards, authored forbids and evaluation failures.
type ScopedEvidenceAuthorizer interface {
	ScopedEvidence(context.Context, Request) (ScopedEvidenceDecision, error)
}

// PolicyEvidenceDecision is the opt-in typed result of a deny-overlay. A clean
// verdict proves no configured forbid matched; broken is an established forbid;
// unknown is evaluator unavailability rather than a business denial.
type PolicyEvidenceDecision struct {
	ForbidAbsence CheckEvidence
	Facts         []store.AuthorizationFactRef
	ObservedAt    time.Time
	FreshUntil    time.Time
}

// PolicyEvidenceEvaluator is an optional extension implemented by a policy
// evaluator that can distinguish a policy decision from unavailable evidence.
// A legacy PolicyEvaluator is never called by AuthorizeEvidence and contributes
// UNKNOWN instead.
type PolicyEvidenceEvaluator interface {
	EvaluateEvidence(context.Context, Request) (PolicyEvidenceDecision, error)
}

const (
	maxEvidenceCodeBytes = 128
	maxEvidenceFacts     = 64
)

var (
	checkGuardNotEvaluated = CheckEvidence{
		Verdict: CheckUnknown,
		Code:    "resource_guard_not_evaluated",
	}
	checkForbidNotEvaluated = CheckEvidence{
		Verdict: CheckUnknown,
		Code:    "forbid_absence_not_evaluated",
	}
)

// AuthorizeEvidence evaluates the opt-in evidence seams without changing the
// historical Authorize contract. It never derives typed state from Decision.Reason
// or ScopedDecision.Reason. A configured legacy engine, an error, a panic, a
// malformed contribution becomes UNKNOWN locally rather than fabricating an
// allow or erasing an independently established denial.
func (az *Authorizer) AuthorizeEvidence(ctx context.Context, req Request) AuthorizationEvidence {
	if az == nil {
		return unknownAuthorizationEvidence("authorizer_unavailable")
	}
	baseRequest := cloneEvidenceRequest(req)

	restricted, restrictionAllows := baseRequest.Principal.restrictedPermission(
		baseRequest.Tenant,
		baseRequest.Permission,
	)
	if restricted && !restrictionAllows {
		return finalizeAuthorizationEvidence(
			CheckEvidence{Verdict: CheckBroken, Code: "credential_ceiling_denied"},
			checkGuardNotEvaluated,
			checkForbidNotEvaluated,
			nil,
			evidenceWindow{},
		)
	}

	// Each producer gets an independent deep copy. A producer may retain or
	// mutate every map/slice it can observe without changing core's snapshot or
	// the other producer's question.
	scoped := az.scopedEvidence(ctx, cloneEvidenceRequest(baseRequest))
	policy := az.policyEvidence(ctx, cloneEvidenceRequest(baseRequest))

	corePermission := CheckEvidence{Verdict: CheckUnknown, Code: "core_permission_unavailable"}
	switch {
	case restricted && restrictionAllows:
		corePermission = CheckEvidence{Verdict: CheckClean, Code: "credential_ceiling_permitted"}
	case az.rbacAllows(baseRequest):
		corePermission = CheckEvidence{Verdict: CheckClean, Code: "rbac_permitted"}
	case !scoped.known:
		// A legacy/unavailable scoped engine might have supplied the positive grant
		// that RBAC lacks. That is UNKNOWN, not an RBAC business denial.
		corePermission = CheckEvidence{Verdict: CheckUnknown, Code: "scoped_grant_unavailable"}
	case scoped.decision.Effect == EffectGrant:
		corePermission = CheckEvidence{Verdict: CheckClean, Code: "scoped_grant_permitted"}
	default:
		corePermission = CheckEvidence{Verdict: CheckBroken, Code: "core_permission_denied"}
	}

	principalFact := store.AuthorizationFactRef{}
	principalWindow := evidenceWindow{}
	if corePermission.Verdict == CheckClean {
		var ok bool
		principalFact, principalWindow, ok = principalAuthorizationEvidence(
			baseRequest.Principal,
			baseRequest.Tenant,
		)
		if !ok {
			corePermission = CheckEvidence{
				Verdict: CheckUnknown,
				Code:    "principal_authority_unverified",
			}
		}
	}

	return foldAuthorizationEvidence(
		corePermission,
		scoped,
		policy,
		principalFact,
		principalWindow,
	)
}

type scopedEvidenceContribution struct {
	decision ScopedEvidenceDecision
	window   evidenceWindow
	known    bool
	typed    bool
}

type policyEvidenceContribution struct {
	decision PolicyEvidenceDecision
	window   evidenceWindow
}

func (az *Authorizer) scopedEvidence(
	ctx context.Context,
	req Request,
) scopedEvidenceContribution {
	if az.scoped == nil {
		return scopedEvidenceContribution{
			decision: ScopedEvidenceDecision{
				Effect:        EffectAbstain,
				ResourceGuard: CheckEvidence{Verdict: CheckClean, Code: "resource_guard_not_configured"},
				ForbidAbsence: CheckEvidence{Verdict: CheckClean, Code: "scoped_forbid_not_configured"},
			},
			known: true,
		}
	}
	if nilEvidenceProducer(az.scoped) {
		return scopedEvidenceContribution{
			decision: unknownScopedEvidence("scoped_evidence_unavailable"),
		}
	}
	producer, ok := az.scoped.(ScopedEvidenceAuthorizer)
	if !ok {
		return scopedEvidenceContribution{
			decision: unknownScopedEvidence("scoped_evidence_legacy"),
		}
	}
	decision, err := scopedEvidenceSafe(ctx, producer, req)
	if err != nil {
		return scopedEvidenceContribution{
			decision: unknownScopedEvidence("scoped_evidence_unavailable"),
		}
	}
	decision, window, ok := normalizeScopedEvidence(decision, req.Tenant)
	if !ok {
		return scopedEvidenceContribution{
			decision: unknownScopedEvidence("scoped_evidence_malformed"),
		}
	}
	return scopedEvidenceContribution{
		decision: decision,
		window:   window,
		// EffectAbstain is not by itself a witnessed no-grant: producers also use
		// the zero/abstain effect when the scoped runtime is unavailable. Only an
		// abstention whose guard and forbid predicates are both CLEAN can close the
		// positive-grant question for a subsequent RBAC miss.
		known: decision.Effect != EffectAbstain ||
			(decision.ResourceGuard.Verdict == CheckClean &&
				decision.ForbidAbsence.Verdict == CheckClean),
		typed: true,
	}
}

func (az *Authorizer) policyEvidence(
	ctx context.Context,
	req Request,
) policyEvidenceContribution {
	if az.eval == nil {
		return policyEvidenceContribution{decision: PolicyEvidenceDecision{
			ForbidAbsence: CheckEvidence{Verdict: CheckClean, Code: "policy_forbid_not_configured"},
		}}
	}
	if nilEvidenceProducer(az.eval) {
		return policyEvidenceContribution{decision: PolicyEvidenceDecision{
			ForbidAbsence: CheckEvidence{Verdict: CheckUnknown, Code: "policy_evidence_unavailable"},
		}}
	}
	producer, ok := az.eval.(PolicyEvidenceEvaluator)
	if !ok {
		return policyEvidenceContribution{decision: PolicyEvidenceDecision{
			ForbidAbsence: CheckEvidence{Verdict: CheckUnknown, Code: "policy_evidence_legacy"},
		}}
	}
	decision, err := policyEvidenceSafe(ctx, producer, req)
	if err != nil {
		return policyEvidenceContribution{decision: PolicyEvidenceDecision{
			ForbidAbsence: CheckEvidence{Verdict: CheckUnknown, Code: "policy_evidence_unavailable"},
		}}
	}
	decision, window, ok := normalizePolicyEvidence(decision, req.Tenant)
	if !ok {
		return policyEvidenceContribution{decision: PolicyEvidenceDecision{
			ForbidAbsence: CheckEvidence{Verdict: CheckUnknown, Code: "policy_evidence_malformed"},
		}}
	}
	return policyEvidenceContribution{decision: decision, window: window}
}

func nilEvidenceProducer(producer any) bool {
	value := reflect.ValueOf(producer)
	if !value.IsValid() {
		return true
	}
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func cloneEvidenceRequest(req Request) Request {
	req.Principal = cloneEvidencePrincipal(req.Principal)
	if req.Resource.Extra != nil {
		extra := make(map[string]string, len(req.Resource.Extra))
		for key, value := range req.Resource.Extra {
			extra[key] = value
		}
		req.Resource.Extra = extra
	}
	return req
}

func cloneEvidencePrincipal(principal Principal) Principal {
	principal.AMR = append([]string(nil), principal.AMR...)
	principal.audiences = append([]string(nil), principal.audiences...)

	if principal.grants != nil {
		grants := make(map[model.TenantID]string, len(principal.grants))
		for tenant, role := range principal.grants {
			grants[tenant] = role
		}
		principal.grants = grants
	}
	if principal.groups != nil {
		groups := make(map[model.TenantID][]string, len(principal.groups))
		for tenant, memberships := range principal.groups {
			groups[tenant] = append([]string(nil), memberships...)
		}
		principal.groups = groups
	}
	if principal.confined != nil {
		confined := make(map[model.TenantID]model.ID, len(principal.confined))
		for tenant, workspace := range principal.confined {
			confined[tenant] = workspace
		}
		principal.confined = confined
	}
	if principal.restricted != nil {
		restricted := make(map[model.TenantID]map[Permission]struct{}, len(principal.restricted))
		for tenant, permissions := range principal.restricted {
			set := make(map[Permission]struct{}, len(permissions))
			for permission := range permissions {
				set[permission] = struct{}{}
			}
			restricted[tenant] = set
		}
		principal.restricted = restricted
	}
	if principal.localMeta != nil {
		meta := make(map[string]any, len(principal.localMeta))
		for key, value := range principal.localMeta {
			meta[key] = value
		}
		principal.localMeta = meta
	}
	return principal
}

func normalizeScopedEvidence(
	decision ScopedEvidenceDecision,
	tenant model.TenantID,
) (ScopedEvidenceDecision, evidenceWindow, bool) {
	if !validScopedEvidence(decision) || !validEvidenceFactsForTenant(decision.Facts, tenant) {
		return ScopedEvidenceDecision{}, evidenceWindow{}, false
	}
	facts, ok := canonicalEvidenceFacts(decision.Facts)
	if !ok {
		return ScopedEvidenceDecision{}, evidenceWindow{}, false
	}
	window := evidenceWindow{}
	if !window.add(decision.ObservedAt, decision.FreshUntil) || !window.validIntersection() {
		return ScopedEvidenceDecision{}, evidenceWindow{}, false
	}
	decision.Facts = facts
	return decision, window, true
}

func normalizePolicyEvidence(
	decision PolicyEvidenceDecision,
	tenant model.TenantID,
) (PolicyEvidenceDecision, evidenceWindow, bool) {
	if !validPolicyEvidence(decision) || !validEvidenceFactsForTenant(decision.Facts, tenant) {
		return PolicyEvidenceDecision{}, evidenceWindow{}, false
	}
	facts, ok := canonicalEvidenceFacts(decision.Facts)
	if !ok {
		return PolicyEvidenceDecision{}, evidenceWindow{}, false
	}
	window := evidenceWindow{}
	if !window.add(decision.ObservedAt, decision.FreshUntil) || !window.validIntersection() {
		return PolicyEvidenceDecision{}, evidenceWindow{}, false
	}
	decision.Facts = facts
	return decision, window, true
}

func validEvidenceFactsForTenant(facts []store.AuthorizationFactRef, tenant model.TenantID) bool {
	for _, fact := range facts {
		switch fact.Kind {
		case model.DirectoryEpochKind, model.AuthorizationEpochKind:
			if fact.ID != model.ID(tenant) {
				return false
			}
		}
	}
	return true
}

func principalAuthorizationEvidence(
	principal Principal,
	tenant model.TenantID,
) (store.AuthorizationFactRef, evidenceWindow, bool) {
	evidence := principal.evidence
	ref, ok := principal.Ref()
	if !ok || evidence.tenant != tenant || evidence.ref != ref ||
		!validPrincipalDirectoryEpochFact(tenant, evidence.directoryEpoch) ||
		evidence.directoryEpoch != (store.AuthorizationFactRef{
			Kind: model.DirectoryEpochKind, ID: model.ID(tenant),
			Version: evidence.directoryEpoch.Version,
		}) || !validPrincipalAuthoritySeal(principal) {
		return store.AuthorizationFactRef{}, evidenceWindow{}, false
	}
	window := evidenceWindow{}
	if !window.add(evidence.observedAt, evidence.freshUntil) || !window.validIntersection() {
		return store.AuthorizationFactRef{}, evidenceWindow{}, false
	}
	return evidence.directoryEpoch, window, true
}

func foldAuthorizationEvidence(
	corePermission CheckEvidence,
	scoped scopedEvidenceContribution,
	policy policyEvidenceContribution,
	principalFact store.AuthorizationFactRef,
	principalWindow evidenceWindow,
) AuthorizationEvidence {
	resourceGuard := scoped.decision.ResourceGuard
	forbidAbsence := andCheckEvidence(
		scoped.decision.ForbidAbsence,
		policy.decision.ForbidAbsence,
		"forbid_absence_clean",
	)
	outcome := combineEvidenceOutcome(corePermission, resourceGuard, forbidAbsence)

	switch outcome {
	case EvidenceUnknown:
		return finalizeAuthorizationEvidence(
			corePermission, resourceGuard, forbidAbsence, nil, evidenceWindow{},
		)
	case EvidenceDeny:
		facts, window := denialEvidenceProof(corePermission, scoped, policy)
		return finalizeAuthorizationEvidence(
			corePermission, resourceGuard, forbidAbsence, facts, window,
		)
	case EvidenceAllow:
		facts, ok := canonicalEvidenceFacts(
			[]store.AuthorizationFactRef{principalFact},
			scoped.decision.Facts,
			policy.decision.Facts,
		)
		if !ok || principalFact == (store.AuthorizationFactRef{}) {
			return unknownAuthorizationEvidence("authorization_facts_conflict")
		}
		window := evidenceWindow{}
		if !window.addWindow(principalWindow) || !window.addWindow(scoped.window) ||
			!window.addWindow(policy.window) || !window.validIntersection() {
			return unknownAuthorizationEvidence("authorization_window_invalid")
		}
		return finalizeAuthorizationEvidence(
			corePermission, resourceGuard, forbidAbsence, facts, window,
		)
	default:
		return unknownAuthorizationEvidence("authorization_outcome_invalid")
	}
}

func denialEvidenceProof(
	corePermission CheckEvidence,
	scoped scopedEvidenceContribution,
	policy policyEvidenceContribution,
) ([]store.AuthorizationFactRef, evidenceWindow) {
	// One independently established negative is sufficient. Selecting a single
	// proof prevents a clean, unknown, or lower-priority negative contribution
	// with incompatible facts from turning a deny into fabricated uncertainty.
	if scoped.decision.ResourceGuard.Verdict == CheckBroken ||
		scoped.decision.ForbidAbsence.Verdict == CheckBroken {
		return append([]store.AuthorizationFactRef(nil), scoped.decision.Facts...), scoped.window
	}
	if corePermission.Verdict == CheckBroken {
		// A typed scoped no-grant is the durable witness for the RBAC miss.
		// CheckEvidence.Code remains descriptive and is never authorization input.
		if scoped.typed {
			return append([]store.AuthorizationFactRef(nil), scoped.decision.Facts...), scoped.window
		}
		return nil, evidenceWindow{}
	}
	if policy.decision.ForbidAbsence.Verdict == CheckBroken {
		return append([]store.AuthorizationFactRef(nil), policy.decision.Facts...), policy.window
	}
	return nil, evidenceWindow{}
}

func scopedEvidenceSafe(
	ctx context.Context,
	producer ScopedEvidenceAuthorizer,
	req Request,
) (decision ScopedEvidenceDecision, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("auth: scoped evidence authorizer panicked: %v", recovered)
		}
	}()
	return producer.ScopedEvidence(ctx, req)
}

func policyEvidenceSafe(
	ctx context.Context,
	producer PolicyEvidenceEvaluator,
	req Request,
) (decision PolicyEvidenceDecision, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("auth: policy evidence evaluator panicked: %v", recovered)
		}
	}()
	return producer.EvaluateEvidence(ctx, req)
}

func validScopedEvidence(decision ScopedEvidenceDecision) bool {
	if decision.Effect < EffectAbstain || decision.Effect > EffectForbid ||
		!validCheckEvidence(decision.ResourceGuard) ||
		!validCheckEvidence(decision.ForbidAbsence) {
		return false
	}
	switch decision.Effect {
	case EffectGrant:
		if decision.ResourceGuard.Verdict != CheckClean ||
			decision.ForbidAbsence.Verdict != CheckClean {
			return false
		}
	case EffectForbid:
		if decision.ResourceGuard.Verdict != CheckBroken &&
			decision.ForbidAbsence.Verdict != CheckBroken {
			return false
		}
	case EffectAbstain:
		if decision.ResourceGuard.Verdict == CheckBroken ||
			decision.ForbidAbsence.Verdict == CheckBroken {
			return false
		}
	}
	return validContributionWindow(
		decision.ObservedAt,
		decision.FreshUntil,
		decision.ResourceGuard.Verdict != CheckUnknown ||
			decision.ForbidAbsence.Verdict != CheckUnknown ||
			decision.Effect != EffectAbstain || len(decision.Facts) > 0,
	)
}

func validPolicyEvidence(decision PolicyEvidenceDecision) bool {
	if !validCheckEvidence(decision.ForbidAbsence) {
		return false
	}
	return validContributionWindow(
		decision.ObservedAt,
		decision.FreshUntil,
		decision.ForbidAbsence.Verdict != CheckUnknown || len(decision.Facts) > 0,
	)
}

func validCheckEvidence(evidence CheckEvidence) bool {
	if !evidence.Verdict.Valid() || len(evidence.Code) == 0 ||
		len(evidence.Code) > maxEvidenceCodeBytes || strings.TrimSpace(evidence.Code) != evidence.Code {
		return false
	}
	for _, r := range evidence.Code {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func validContributionWindow(observedAt, freshUntil time.Time, required bool) bool {
	if observedAt.IsZero() && freshUntil.IsZero() {
		return !required
	}
	return !observedAt.IsZero() && freshUntil.After(observedAt)
}

func unknownScopedEvidence(code string) ScopedEvidenceDecision {
	return ScopedEvidenceDecision{
		Effect:        EffectAbstain,
		ResourceGuard: CheckEvidence{Verdict: CheckUnknown, Code: code},
		ForbidAbsence: CheckEvidence{Verdict: CheckUnknown, Code: code},
	}
}

func andCheckEvidence(left, right CheckEvidence, cleanCode string) CheckEvidence {
	if left.Verdict == CheckBroken {
		return left
	}
	if right.Verdict == CheckBroken {
		return right
	}
	if left.Verdict == CheckUnknown {
		return left
	}
	if right.Verdict == CheckUnknown {
		return right
	}
	return CheckEvidence{Verdict: CheckClean, Code: cleanCode}
}

func combineEvidenceOutcome(checks ...CheckEvidence) EvidenceOutcome {
	unknown := false
	for _, check := range checks {
		switch check.Verdict {
		case CheckBroken:
			return EvidenceDeny
		case CheckUnknown:
			unknown = true
		}
	}
	if unknown {
		return EvidenceUnknown
	}
	return EvidenceAllow
}

func finalizeAuthorizationEvidence(
	corePermission CheckEvidence,
	resourceGuard CheckEvidence,
	forbidAbsence CheckEvidence,
	facts []store.AuthorizationFactRef,
	window evidenceWindow,
) AuthorizationEvidence {
	return AuthorizationEvidence{
		Outcome:        combineEvidenceOutcome(corePermission, resourceGuard, forbidAbsence),
		CorePermission: corePermission,
		ResourceGuard:  resourceGuard,
		ForbidAbsence:  forbidAbsence,
		Facts:          facts,
		ObservedAt:     window.observedAt,
		FreshUntil:     window.freshUntil,
	}
}

func unknownAuthorizationEvidence(code string) AuthorizationEvidence {
	unknown := CheckEvidence{Verdict: CheckUnknown, Code: code}
	return AuthorizationEvidence{
		Outcome:        EvidenceUnknown,
		CorePermission: unknown,
		ResourceGuard:  unknown,
		ForbidAbsence:  unknown,
	}
}

type evidenceWindow struct {
	observedAt time.Time
	freshUntil time.Time
	seen       bool
}

func (window *evidenceWindow) add(observedAt, freshUntil time.Time) bool {
	if observedAt.IsZero() && freshUntil.IsZero() {
		return true
	}
	if observedAt.IsZero() || !freshUntil.After(observedAt) {
		return false
	}
	observedAt = observedAt.UTC()
	freshUntil = freshUntil.UTC()
	if !window.seen || observedAt.After(window.observedAt) {
		window.observedAt = observedAt
	}
	if !window.seen || freshUntil.Before(window.freshUntil) {
		window.freshUntil = freshUntil
	}
	window.seen = true
	return true
}

func (window *evidenceWindow) addWindow(contribution evidenceWindow) bool {
	if !contribution.seen {
		return true
	}
	return window.add(contribution.observedAt, contribution.freshUntil)
}

func (window evidenceWindow) validIntersection() bool {
	return !window.seen || window.freshUntil.After(window.observedAt)
}

type evidenceFactKey struct {
	kind model.Kind
	id   model.ID
}

type evidenceLeaseKey struct {
	kind    model.Kind
	subject string
}

type evidenceLeaseWitness struct {
	present  bool
	subject  string
	fence    int64
	deadline string
}

func canonicalEvidenceFacts(groups ...[]store.AuthorizationFactRef) (
	[]store.AuthorizationFactRef,
	bool,
) {
	unique := make(map[evidenceFactKey]store.AuthorizationFactRef)
	leases := make(map[evidenceLeaseKey]evidenceFactKey)
	for _, group := range groups {
		for _, fact := range group {
			if !validEvidenceFact(fact) {
				return nil, false
			}
			key := evidenceFactKey{kind: fact.Kind, id: fact.ID}
			if prior, exists := unique[key]; exists {
				if prior.Version != fact.Version ||
					evidenceLeaseOf(prior) != evidenceLeaseOf(fact) {
					return nil, false
				}
				continue
			}
			lease := evidenceLeaseOf(fact)
			if lease.present {
				leaseKey := evidenceLeaseKey{kind: fact.Kind, subject: lease.subject}
				if prior, exists := leases[leaseKey]; exists && prior != key {
					return nil, false
				}
				leases[leaseKey] = key
			}
			unique[key] = fact
			if len(unique) > maxEvidenceFacts {
				return nil, false
			}
		}
	}

	canonical := make([]store.AuthorizationFactRef, 0, len(unique))
	for _, fact := range unique {
		canonical = append(canonical, fact)
	}
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].Kind != canonical[j].Kind {
			return canonical[i].Kind < canonical[j].Kind
		}
		return canonical[i].ID.String() < canonical[j].ID.String()
	})
	return canonical, true
}

func validEvidenceFact(fact store.AuthorizationFactRef) bool {
	if !fact.Kind.Valid() || fact.ID.IsZero() || fact.Version < 1 {
		return false
	}
	parsed, err := model.ParseID(fact.ID.String())
	if err != nil || parsed != fact.ID {
		return false
	}
	if fact.Kind == model.DirectoryEpochKind || fact.Kind == model.AuthorizationEpochKind {
		plain := store.AuthorizationFactRef{
			Kind: fact.Kind, ID: fact.ID, Version: fact.Version,
		}
		return fact == plain
	}
	return true
}

func evidenceLeaseOf(fact store.AuthorizationFactRef) evidenceLeaseWitness {
	subject, fence, deadline, ok := fact.LeaseFenceWitness()
	if !ok {
		return evidenceLeaseWitness{}
	}
	return evidenceLeaseWitness{
		present: true, subject: subject, fence: fence, deadline: deadline.String(),
	}
}
