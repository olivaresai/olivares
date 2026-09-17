// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// communicationIdentityBinding retains one current, server-resolved directory
// principal identity for a collection request. It deliberately carries no
// resource authorization: every candidate row is still asked as its own exact
// question through bindCommunicationAuthorityBatch.
//
// It is resource-neutral. The personal inbox and the visible-channel catalog
// bind it through their own adapters below, each naming the principal shape it
// admits; the binding itself never learns which entity kind will be asked.
type communicationIdentityBinding struct {
	scope     DirectoryScopeRef
	ref       auth.PrincipalRef
	resolved  auth.Principal
	principal CommunicationPrincipal
	deadline  time.Time
	source    communicationAuthorizationEvidenceSource
}

// communicationInboxIdentityBinding is the inbox adapter's name for the
// resource-neutral binding; the exact inbox keeps its signatures unchanged.
type communicationInboxIdentityBinding = communicationIdentityBinding

// communicationPrincipalRequirement admits or refuses the derived principal
// shape for one collection surface. It is evaluated at binding and again at
// batch binding and transaction time, so a surface cannot widen the principal
// it admitted after the credential was re-resolved.
type communicationPrincipalRequirement func(CommunicationPrincipal) error

// communicationIdentityCause names WHY the neutral identity binder could not
// bind a current identity. Every cause shares one public disposition
// (ErrCommunicationEvidenceUnknown: 503 evidence_unavailable, VerdictUnknown)
// and none of them names a principal, a credential or a tenant; what differs is
// the operator diagnostic, which each surface adapter renders in its own wording
// (communicationRequestIdentityWording, communicationInboxIdentityWording). The
// causes are kept distinct on purpose: the independent review of the catalog
// increment (F2) measured that collapsing them into one string made four
// previously distinct inbox log causes indistinguishable.
type communicationIdentityCause int

const (
	// communicationIdentityUnavailable covers a malformed request shape (nil
	// context, empty credential reference, no principal requirement) and a
	// resolver failure that is not a stale credential.
	communicationIdentityUnavailable communicationIdentityCause = iota
	// communicationIdentityDeadlineMissing: the request carries no finite
	// deadline, so no later evidence window could be clipped to it.
	communicationIdentityDeadlineMissing
	// communicationIdentitySourcesUnavailable: the module has no bound
	// resolver/evidence-source pair.
	communicationIdentitySourcesUnavailable
	// communicationIdentityCredentialStale: the resolver reports the exact
	// credential as no longer authenticated.
	communicationIdentityCredentialStale
	// communicationIdentityReferenceCrossed: the resolver answered a principal
	// whose reference is not the one that was asked.
	communicationIdentityReferenceCrossed
	// communicationIdentityCauseCount sizes the wording tables; a table that
	// misses a cause fails the wording test instead of rendering an empty string.
	communicationIdentityCauseCount
)

// communicationRequestIdentityWording is the resource-neutral diagnostic per
// cause, used by every surface that binds through the neutral binder directly
// (the visible-channel catalog today).
var communicationRequestIdentityWording = [communicationIdentityCauseCount]string{
	communicationIdentityUnavailable:        "communication request identity is unavailable",
	communicationIdentityDeadlineMissing:    "communication request identity requires a finite deadline",
	communicationIdentitySourcesUnavailable: "communication request identity sources are unavailable",
	communicationIdentityCredentialStale:    "authenticated credential is no longer current",
	communicationIdentityReferenceCrossed:   "resolved principal crossed its credential reference",
}

// communicationInboxIdentityWording is the personal inbox's historical wording
// per cause, exactly as its callers and logs named the five failures before the
// binder was made resource-neutral.
var communicationInboxIdentityWording = [communicationIdentityCauseCount]string{
	communicationIdentityUnavailable:        "communication inbox identity is unavailable",
	communicationIdentityDeadlineMissing:    "communication inbox identity requires a finite deadline",
	communicationIdentitySourcesUnavailable: "communication inbox identity sources are unavailable",
	communicationIdentityCredentialStale:    "authenticated inbox credential is no longer current",
	communicationIdentityReferenceCrossed:   "resolved inbox principal crossed its credential reference",
}

// communicationIdentityError is the neutral binder's own failure. It wraps
// ErrCommunicationEvidenceUnknown, so errors.Is, communicationHTTPDisposition
// and every caller that classifies by sentinel behave exactly as before, and it
// carries its cause so a surface adapter can re-render the diagnostic in its
// own wording without rewriting evidence failures that did not originate here.
type communicationIdentityError struct {
	cause communicationIdentityCause
}

func newCommunicationIdentityError(cause communicationIdentityCause) error {
	return &communicationIdentityError{cause: cause}
}

func (e *communicationIdentityError) Error() string {
	return communicationError(
		ErrCommunicationEvidenceUnknown, "%s", communicationRequestIdentityWording[e.cause],
	).Error()
}

func (e *communicationIdentityError) Unwrap() error { return ErrCommunicationEvidenceUnknown }

// bindCurrentCommunicationIdentity re-resolves the exact credential reference
// with the bound authority sources and derives the K3 principal. Delegated,
// malformed, stale and unknown credentials fail closed; the request must carry a
// finite deadline because every later evidence window is clipped to it. Its own
// failures are communicationIdentityError values with a distinct cause; errors
// from the scope validator, the principal derivation and the surface's principal
// requirement pass through as they are.
func (m *Module) bindCurrentCommunicationIdentity(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	require communicationPrincipalRequirement,
) (communicationIdentityBinding, error) {
	if ctx == nil || ref == (auth.PrincipalRef{}) || require == nil {
		return communicationIdentityBinding{}, newCommunicationIdentityError(
			communicationIdentityUnavailable,
		)
	}
	if err := scope.Validate(); err != nil {
		return communicationIdentityBinding{}, err
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.IsZero() {
		return communicationIdentityBinding{}, newCommunicationIdentityError(
			communicationIdentityDeadlineMissing,
		)
	}
	sources := m.communicationAuthoritySources
	if sources == nil || !communicationPortBound(sources.resolver) ||
		!communicationPortBound(sources.source) {
		return communicationIdentityBinding{}, newCommunicationIdentityError(
			communicationIdentitySourcesUnavailable,
		)
	}
	resolved, err := sources.resolver.ResolvePrincipalScope(ctx, ref, scope.TenantID)
	if err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			return communicationIdentityBinding{}, newCommunicationIdentityError(
				communicationIdentityCredentialStale,
			)
		}
		return communicationIdentityBinding{}, newCommunicationIdentityError(
			communicationIdentityUnavailable,
		)
	}
	resolvedRef, ok := resolved.Ref()
	if !ok || resolvedRef != ref {
		return communicationIdentityBinding{}, newCommunicationIdentityError(
			communicationIdentityReferenceCrossed,
		)
	}
	principal, err := communicationPrincipalFromResolvedAuth(resolved)
	if err != nil {
		return communicationIdentityBinding{}, err
	}
	if err := require(principal); err != nil {
		return communicationIdentityBinding{}, err
	}
	resolved.AMR = append([]string(nil), resolved.AMR...)
	return communicationIdentityBinding{
		scope: scope, ref: ref, resolved: resolved, principal: principal,
		deadline: deadline.UTC(), source: sources.source,
	}, nil
}

// bindCurrentCommunicationInboxIdentity is the inbox adapter over the neutral
// binder: it admits exactly the directory principals the personal inbox serves.
func (m *Module) bindCurrentCommunicationInboxIdentity(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
) (communicationInboxIdentityBinding, error) {
	identity, err := m.bindCurrentCommunicationIdentity(
		ctx, scope, ref, requireDirectNoticeInboxUserPrincipal,
	)
	if err != nil {
		return communicationInboxIdentityBinding{}, communicationInboxIdentityError(err)
	}
	return identity, nil
}

// communicationInboxIdentityError renders the neutral binder's OWN failure in
// the inbox's historical wording for that exact cause, so the five diagnostics
// the inbox's callers and logs already name stay distinct. Any other error,
// including an evidence failure that did not originate in the binder, passes
// through untouched, exactly as the pre-extraction inbox binder returned it.
func communicationInboxIdentityError(err error) error {
	var identity *communicationIdentityError
	if !errors.As(err, &identity) {
		return err
	}
	return communicationError(
		ErrCommunicationEvidenceUnknown, "%s", communicationInboxIdentityWording[identity.cause],
	)
}

func requireDirectNoticeInboxUserPrincipal(principal CommunicationPrincipal) error {
	if ValidateCommunicationPrincipal(principal) != nil || principal.System {
		return communicationError(
			ErrCommunicationForbidden,
			"direct notice inbox requires a directory principal credential",
		)
	}
	return nil
}

type communicationInboxAuthorityCandidate struct {
	candidate directNoticeInboxCandidate
	question  communicationAuthorityQuestion
}

type communicationRequestAuthorityBatchContext struct {
	question  communicationAuthorityQuestion
	principal CommunicationPrincipal
	witness   ReadWitness
}

type communicationRequestAuthorityBatchResult struct {
	snapshot communicationRequestAuthoritySnapshot
	contexts []communicationRequestAuthorityBatchContext
}

// communicationRequestAuthorityBatch is a one-shot union of independently
// answered exact questions. The union only saves a transaction; it does not
// introduce a collection permission or a collection resource.
type communicationRequestAuthorityBatch struct {
	access func([]communicationAuthorityQuestion) (communicationRequestAuthorityBatchResult, error)
}

// bindCommunicationAuthorityBatch answers every server-built exact question for
// the rebound identity and seals the ALLOWED ones behind one consumable batch.
// Core DENY is filtered (its index is absent from the returned admission list);
// core UNKNOWN, a malformed witness, a question outside the identity's scope, a
// repeated entity or a deadline drift aborts the whole batch. Question order is
// preserved: the returned indexes are ascending positions into questions.
//
// Nothing here knows the entity kind: the inbox adapter asks about Deliveries,
// the channel catalog about Channels, each through its own server-built
// questions. The union of facts is canonical and consumed exactly once.
func bindCommunicationAuthorityBatch(
	ctx context.Context,
	identity communicationIdentityBinding,
	require communicationPrincipalRequirement,
	questions []communicationAuthorityQuestion,
) (communicationRequestAuthorityBatch, []int, error) {
	if ctx == nil || require == nil || identity.source == nil ||
		identity.ref == (auth.PrincipalRef{}) || identity.scope.Validate() != nil ||
		require(identity.principal) != nil || identity.deadline.IsZero() {
		return communicationRequestAuthorityBatch{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication authority batch is unavailable",
		)
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.IsZero() || !deadline.UTC().Equal(identity.deadline) {
		return communicationRequestAuthorityBatch{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication authority deadline changed",
		)
	}
	if len(questions) == 0 || len(questions) > directNoticeInboxCandidateBound {
		return communicationRequestAuthorityBatch{}, nil, communicationError(
			ErrInvalidCommunicationModel,
			"communication authority batch has invalid size",
		)
	}
	resolvedRef, ok := identity.resolved.Ref()
	if !ok || resolvedRef != identity.ref {
		return communicationRequestAuthorityBatch{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication authority identity changed",
		)
	}

	admitted := make([]int, 0, len(questions))
	contexts := make([]communicationRequestAuthorityBatchContext, 0, len(questions))
	allFacts := make([]store.AuthorizationFactRef, 0)
	seen := make(map[EntityRef]struct{}, len(questions))
	var latestObserved time.Time
	var earliestFresh time.Time
	for index, question := range questions {
		if question.validate() != nil ||
			question.entity.TenantID != identity.scope.TenantID ||
			question.entity.WorkspaceID != identity.scope.WorkspaceID {
			return communicationRequestAuthorityBatch{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication authority question crossed the request scope",
			)
		}
		if _, duplicate := seen[question.entity]; duplicate {
			return communicationRequestAuthorityBatch{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication authority question is repeated",
			)
		}
		seen[question.entity] = struct{}{}
		evidence := identity.source.AuthorizeEvidence(ctx, auth.Request{
			Principal: identity.resolved, Permission: question.permission,
			Tenant: question.entity.TenantID,
			Resource: auth.ResourceAttrs{
				Kind: string(question.entity.Kind), ID: question.entity.ID.String(),
				WorkspaceID: question.entity.WorkspaceID,
			},
		})
		outcome, facts, err := validateCommunicationCoreAuthorizationEvidence(
			evidence, identity.scope.TenantID,
		)
		if err != nil {
			return communicationRequestAuthorityBatch{}, nil, err
		}
		if !evidence.FreshUntil.IsZero() && evidence.FreshUntil.After(identity.deadline) {
			return communicationRequestAuthorityBatch{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication authority exceeds the request deadline",
			)
		}
		switch outcome {
		case ReadDeny:
			continue
		case ReadUnknown:
			return communicationRequestAuthorityBatch{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication candidate authority is unavailable",
			)
		case ReadAllow:
			if err := ValidateCommunicationPrincipalForScope(
				identity.principal, identity.scope,
			); err != nil {
				return communicationRequestAuthorityBatch{}, nil, communicationError(
					ErrCommunicationEvidenceUnknown,
					"communication authority crossed principal scope",
				)
			}
		default:
			return communicationRequestAuthorityBatch{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication candidate authority has no verdict",
			)
		}
		witness := ReadWitness{
			Outcome: ReadAllow, Code: "core_authorization_allowed",
			Entity: question.entity, Operation: question.operation,
			Principal: identity.principal, ObservedAt: evidence.ObservedAt,
			FreshUntil:     evidence.FreshUntil,
			CorePermission: communicationAuthorityEvidence(evidence.CorePermission),
			ResourceGuard:  communicationAuthorityEvidence(evidence.ResourceGuard),
			ForbidAbsence:  communicationAuthorityEvidence(evidence.ForbidAbsence),
			Facts:          append([]store.AuthorizationFactRef(nil), facts...),
			EvidenceRef:    communicationCoreAuthorizationEvidenceRef,
		}
		if err := ValidateReadWitness(witness); err != nil {
			return communicationRequestAuthorityBatch{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication candidate authority is malformed",
			)
		}
		admitted = append(admitted, index)
		contexts = append(contexts, communicationRequestAuthorityBatchContext{
			question: question, principal: identity.principal,
			witness: cloneCommunicationRequestAuthorityWitness(witness),
		})
		allFacts = append(allFacts, facts...)
		if witness.ObservedAt.After(latestObserved) {
			latestObserved = witness.ObservedAt
		}
		if earliestFresh.IsZero() || witness.FreshUntil.Before(earliestFresh) {
			earliestFresh = witness.FreshUntil
		}
	}
	if len(admitted) == 0 {
		return communicationRequestAuthorityBatch{}, []int{}, nil
	}
	facts, err := canonicalAuthorizationFactUnion(allFacts)
	if err != nil || latestObserved.IsZero() || !earliestFresh.After(latestObserved) {
		return communicationRequestAuthorityBatch{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication authority batch has no common window",
		)
	}
	bindingID := &communicationRequestAuthorityBindingID{marker: 1}
	snapshot := communicationRequestAuthoritySnapshot{
		facts: facts, observedAt: latestObserved.UTC(), freshUntil: earliestFresh.UTC(),
		bindingID: bindingID,
	}
	if err := snapshot.validate(); err != nil {
		return communicationRequestAuthorityBatch{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication authority batch is malformed",
		)
	}
	sealedQuestions := make([]communicationAuthorityQuestion, len(admitted))
	sealedContexts := make([]communicationRequestAuthorityBatchContext, len(contexts))
	for position, index := range admitted {
		sealedQuestions[position] = questions[index]
		sealedContexts[position] = cloneCommunicationRequestAuthorityBatchContext(contexts[position])
	}
	consumed := &atomic.Bool{}
	return communicationRequestAuthorityBatch{access: func(
		expected []communicationAuthorityQuestion,
	) (communicationRequestAuthorityBatchResult, error) {
		if !equalCommunicationAuthorityQuestions(expected, sealedQuestions) ||
			!consumed.CompareAndSwap(false, true) {
			return communicationRequestAuthorityBatchResult{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication authority batch cannot be consumed",
			)
		}
		result := communicationRequestAuthorityBatchResult{
			snapshot: communicationRequestAuthoritySnapshot{
				facts:      append([]store.AuthorizationFactRef(nil), snapshot.facts...),
				observedAt: snapshot.observedAt, freshUntil: snapshot.freshUntil,
				bindingID: snapshot.bindingID,
			},
			contexts: make([]communicationRequestAuthorityBatchContext, len(sealedContexts)),
		}
		for index := range sealedContexts {
			result.contexts[index] = cloneCommunicationRequestAuthorityBatchContext(
				sealedContexts[index],
			)
		}
		return result, nil
	}}, admitted, nil
}

// bindCommunicationInboxAuthorityBatch is the inbox adapter: it validates the
// personal inbox candidate stream (ascending delivery sequence, canonical IDs,
// no repeats), builds the exact Delivery/read questions and maps the admitted
// positions back onto the candidates.
func bindCommunicationInboxAuthorityBatch(
	ctx context.Context,
	identity communicationInboxIdentityBinding,
	candidates []directNoticeInboxCandidate,
) (
	communicationRequestAuthorityBatch,
	[]communicationInboxAuthorityCandidate,
	error,
) {
	if ctx == nil || identity.source == nil || identity.ref == (auth.PrincipalRef{}) ||
		identity.scope.Validate() != nil || requireDirectNoticeInboxUserPrincipal(identity.principal) != nil ||
		identity.deadline.IsZero() {
		return communicationRequestAuthorityBatch{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication inbox authority batch is unavailable",
		)
	}
	if len(candidates) == 0 || len(candidates) > directNoticeInboxCandidateBound {
		return communicationRequestAuthorityBatch{}, nil, communicationError(
			ErrInvalidCommunicationModel,
			"communication inbox authority batch has invalid size",
		)
	}
	questions := make([]communicationAuthorityQuestion, 0, len(candidates))
	seen := make(map[model.ID]struct{}, len(candidates))
	previousSequence := int64(0)
	for index, candidate := range candidates {
		if !validCanonicalCommunicationID(candidate.DeliveryID) || candidate.DeliverySeq < 1 ||
			(index > 0 && candidate.DeliverySeq <= previousSequence) {
			return communicationRequestAuthorityBatch{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication inbox authority candidates are malformed",
			)
		}
		previousSequence = candidate.DeliverySeq
		if _, duplicate := seen[candidate.DeliveryID]; duplicate {
			return communicationRequestAuthorityBatch{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication inbox authority candidate is repeated",
			)
		}
		seen[candidate.DeliveryID] = struct{}{}
		question, err := newCommunicationAuthorityQuestion(
			identity.scope, messageDeliveryKind, candidate.DeliveryID, CommunicationRead,
		)
		if err != nil {
			return communicationRequestAuthorityBatch{}, nil, err
		}
		questions = append(questions, question)
	}
	batch, admitted, err := bindCommunicationAuthorityBatch(
		ctx, identity, requireDirectNoticeInboxUserPrincipal, questions,
	)
	if err != nil {
		return communicationRequestAuthorityBatch{}, nil, err
	}
	allowed := make([]communicationInboxAuthorityCandidate, 0, len(admitted))
	for _, index := range admitted {
		allowed = append(allowed, communicationInboxAuthorityCandidate{
			candidate: candidates[index], question: questions[index],
		})
	}
	return batch, allowed, nil
}

func cloneCommunicationRequestAuthorityBatchContext(
	context communicationRequestAuthorityBatchContext,
) communicationRequestAuthorityBatchContext {
	context.witness = cloneCommunicationRequestAuthorityWitness(context.witness)
	return context
}

func equalCommunicationAuthorityQuestions(
	left []communicationAuthorityQuestion,
	right []communicationAuthorityQuestion,
) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (batch communicationRequestAuthorityBatch) transactionSnapshot(
	expected []communicationAuthorityQuestion,
) (
	communicationRequestAuthoritySnapshot,
	[]communicationRequestAuthorityBatchContext,
	error,
) {
	if batch.access == nil || len(expected) == 0 {
		return communicationRequestAuthoritySnapshot{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication authority batch is unavailable",
		)
	}
	result, err := batch.access(expected)
	if err != nil {
		return communicationRequestAuthoritySnapshot{}, nil, err
	}
	if result.snapshot.empty() || result.snapshot.validate() != nil ||
		len(result.contexts) != len(expected) {
		return communicationRequestAuthoritySnapshot{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication authority batch consumption is malformed",
		)
	}
	allFacts := make([]store.AuthorizationFactRef, 0)
	var latestObserved time.Time
	var earliestFresh time.Time
	for index, context := range result.contexts {
		if context.question != expected[index] || context.principal != context.witness.Principal ||
			ValidateReadWitness(context.witness) != nil || context.witness.Outcome != ReadAllow ||
			context.witness.Entity != expected[index].entity ||
			context.witness.Operation != expected[index].operation {
			return communicationRequestAuthoritySnapshot{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication authority context is malformed",
			)
		}
		allFacts = append(allFacts, context.witness.Facts...)
		if context.witness.ObservedAt.After(latestObserved) {
			latestObserved = context.witness.ObservedAt
		}
		if earliestFresh.IsZero() || context.witness.FreshUntil.Before(earliestFresh) {
			earliestFresh = context.witness.FreshUntil
		}
	}
	facts, err := canonicalAuthorizationFactUnion(allFacts)
	if err != nil || !equalCommunicationAuthorityFacts(facts, result.snapshot.facts) ||
		!latestObserved.Equal(result.snapshot.observedAt) ||
		!earliestFresh.Equal(result.snapshot.freshUntil) {
		return communicationRequestAuthoritySnapshot{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication authority union changed before consumption",
		)
	}
	return result.snapshot, result.contexts, nil
}

// mutateCommunicationWithBoundAuthorityBatch is the resource-neutral
// transaction wrapper: it consumes the sealed batch for exactly the expected
// questions, intersects the core window with the caller's local evidence window,
// samples the principal's Claim authority and opens ONE bound communication
// transaction. The callback receives the consumed contexts in question order.
func (m *Module) mutateCommunicationWithBoundAuthorityBatch(
	ctx context.Context,
	scope DirectoryScopeRef,
	identity communicationIdentityBinding,
	require communicationPrincipalRequirement,
	questions []communicationAuthorityQuestion,
	batch communicationRequestAuthorityBatch,
	window communicationAuthorityWindow,
	fn func(*communicationTx, []communicationRequestAuthorityBatchContext) error,
) error {
	if fn == nil || require == nil || scope != identity.scope || window.validate() != nil ||
		require(identity.principal) != nil || len(questions) == 0 {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication authority transaction is unavailable",
		)
	}
	for _, question := range questions {
		if question.validate() != nil || question.entity.TenantID != scope.TenantID ||
			question.entity.WorkspaceID != scope.WorkspaceID {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication authority question crossed the transaction scope",
			)
		}
	}
	request, contexts, err := batch.transactionSnapshot(questions)
	if err != nil {
		return err
	}
	request, err = request.narrowTo(window)
	if err != nil {
		return err
	}
	claims, err := m.communicationClaimAuthoritySnapshot(
		ctx, scope.TenantID, communicationClaimsForPrincipal(identity.principal),
	)
	if err != nil {
		return err
	}
	if !communicationClaimsEqualSnapshot(
		communicationClaimsForPrincipal(identity.principal), claims,
	) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication Claim authority changed",
		)
	}
	return m.mutateCommunicationTransaction(
		ctx, scope, request, claims,
		func(tx *communicationTx) error { return fn(tx, contexts) },
	)
}

// mutateCommunicationWithAuthorityBatch is the inbox adapter over the neutral
// wrapper: every admitted candidate must still be its own Delivery/read
// question before the transaction opens.
func (m *Module) mutateCommunicationWithAuthorityBatch(
	ctx context.Context,
	scope DirectoryScopeRef,
	identity communicationInboxIdentityBinding,
	allowed []communicationInboxAuthorityCandidate,
	batch communicationRequestAuthorityBatch,
	window communicationAuthorityWindow,
	fn func(*communicationTx, []communicationRequestAuthorityBatchContext) error,
) error {
	if fn == nil || scope != identity.scope || window.validate() != nil ||
		requireDirectNoticeInboxUserPrincipal(identity.principal) != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication inbox authority transaction is unavailable",
		)
	}
	questions := make([]communicationAuthorityQuestion, len(allowed))
	for index, candidate := range allowed {
		if candidate.candidate.DeliveryID != candidate.question.entity.ID ||
			candidate.question.entity.Kind != messageDeliveryKind ||
			candidate.question.operation != CommunicationRead {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication inbox authority candidate crossed its question",
			)
		}
		questions[index] = candidate.question
	}
	return m.mutateCommunicationWithBoundAuthorityBatch(
		ctx, scope, identity, requireDirectNoticeInboxUserPrincipal,
		questions, batch, window, fn,
	)
}
