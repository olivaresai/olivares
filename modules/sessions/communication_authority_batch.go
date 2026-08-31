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

// communicationInboxIdentityBinding retains one current, server-resolved User
// identity for the personal inbox request. It deliberately carries no resource
// authorization: every Delivery is still asked as its own exact question.
type communicationInboxIdentityBinding struct {
	scope     DirectoryScopeRef
	ref       auth.PrincipalRef
	resolved  auth.Principal
	principal CommunicationPrincipal
	deadline  time.Time
	source    communicationAuthorizationEvidenceSource
}

func (m *Module) bindCurrentCommunicationInboxIdentity(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
) (communicationInboxIdentityBinding, error) {
	if ctx == nil || ref == (auth.PrincipalRef{}) {
		return communicationInboxIdentityBinding{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication inbox identity is unavailable",
		)
	}
	if err := scope.Validate(); err != nil {
		return communicationInboxIdentityBinding{}, err
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.IsZero() {
		return communicationInboxIdentityBinding{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication inbox identity requires a finite deadline",
		)
	}
	sources := m.communicationAuthoritySources
	if sources == nil || !communicationPortBound(sources.resolver) ||
		!communicationPortBound(sources.source) {
		return communicationInboxIdentityBinding{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication inbox identity sources are unavailable",
		)
	}
	resolved, err := sources.resolver.ResolvePrincipalScope(ctx, ref, scope.TenantID)
	if err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			return communicationInboxIdentityBinding{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"authenticated inbox credential is no longer current",
			)
		}
		return communicationInboxIdentityBinding{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication inbox identity is unavailable",
		)
	}
	resolvedRef, ok := resolved.Ref()
	if !ok || resolvedRef != ref {
		return communicationInboxIdentityBinding{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"resolved inbox principal crossed its credential reference",
		)
	}
	principal, err := communicationPrincipalFromResolvedAuth(resolved)
	if err != nil {
		return communicationInboxIdentityBinding{}, err
	}
	if err := requireDirectNoticeInboxUserPrincipal(principal); err != nil {
		return communicationInboxIdentityBinding{}, err
	}
	resolved.AMR = append([]string(nil), resolved.AMR...)
	return communicationInboxIdentityBinding{
		scope: scope, ref: ref, resolved: resolved, principal: principal,
		deadline: deadline.UTC(), source: sources.source,
	}, nil
}

func requireDirectNoticeInboxUserPrincipal(principal CommunicationPrincipal) error {
	if principal.UserID == "" || principal.AgentExternalID != "" || principal.SessionID != "" ||
		principal.SessionRunRef != "" || principal.SessionFence != 0 ||
		principal.SessionWorkspaceID != "" || principal.PurposeRestricted || principal.System ||
		principal.SystemActorRef != "" || principal.SystemGrantAgentID != "" {
		return communicationError(
			ErrCommunicationForbidden,
			"direct notice inbox requires a claim-free user credential",
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
	deadline, ok := ctx.Deadline()
	if !ok || deadline.IsZero() || !deadline.UTC().Equal(identity.deadline) {
		return communicationRequestAuthorityBatch{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication inbox authority deadline changed",
		)
	}
	if len(candidates) == 0 || len(candidates) > directNoticeInboxCandidateBound {
		return communicationRequestAuthorityBatch{}, nil, communicationError(
			ErrInvalidCommunicationModel,
			"communication inbox authority batch has invalid size",
		)
	}
	resolvedRef, ok := identity.resolved.Ref()
	if !ok || resolvedRef != identity.ref {
		return communicationRequestAuthorityBatch{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication inbox authority identity changed",
		)
	}

	allowed := make([]communicationInboxAuthorityCandidate, 0, len(candidates))
	contexts := make([]communicationRequestAuthorityBatchContext, 0, len(candidates))
	allFacts := make([]store.AuthorizationFactRef, 0)
	seen := make(map[model.ID]struct{}, len(candidates))
	var latestObserved time.Time
	var earliestFresh time.Time
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
				"communication inbox authority exceeds the request deadline",
			)
		}
		switch outcome {
		case ReadDeny:
			continue
		case ReadUnknown:
			return communicationRequestAuthorityBatch{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication inbox candidate authority is unavailable",
			)
		case ReadAllow:
			if err := ValidateCommunicationPrincipalForScope(
				identity.principal, identity.scope,
			); err != nil {
				return communicationRequestAuthorityBatch{}, nil, communicationError(
					ErrCommunicationEvidenceUnknown,
					"communication inbox authority crossed principal scope",
				)
			}
		default:
			return communicationRequestAuthorityBatch{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication inbox candidate authority has no verdict",
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
				"communication inbox candidate authority is malformed",
			)
		}
		allowed = append(allowed, communicationInboxAuthorityCandidate{
			candidate: candidate, question: question,
		})
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
	if len(allowed) == 0 {
		return communicationRequestAuthorityBatch{}, []communicationInboxAuthorityCandidate{}, nil
	}
	facts, err := canonicalAuthorizationFactUnion(allFacts)
	if err != nil || latestObserved.IsZero() || !earliestFresh.After(latestObserved) {
		return communicationRequestAuthorityBatch{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication inbox authority batch has no common window",
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
			"communication inbox authority batch is malformed",
		)
	}
	sealedQuestions := make([]communicationAuthorityQuestion, len(allowed))
	sealedContexts := make([]communicationRequestAuthorityBatchContext, len(contexts))
	for index := range allowed {
		sealedQuestions[index] = allowed[index].question
		sealedContexts[index] = cloneCommunicationRequestAuthorityBatchContext(contexts[index])
	}
	consumed := &atomic.Bool{}
	return communicationRequestAuthorityBatch{access: func(
		expected []communicationAuthorityQuestion,
	) (communicationRequestAuthorityBatchResult, error) {
		if !equalCommunicationAuthorityQuestions(expected, sealedQuestions) ||
			!consumed.CompareAndSwap(false, true) {
			return communicationRequestAuthorityBatchResult{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication inbox authority batch cannot be consumed",
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
	}}, allowed, nil
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
			"communication inbox authority batch is unavailable",
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
			"communication inbox authority batch consumption is malformed",
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
				"communication inbox authority context is malformed",
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
			"communication inbox authority union changed before consumption",
		)
	}
	return result.snapshot, result.contexts, nil
}

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
	request, contexts, err := batch.transactionSnapshot(questions)
	if err != nil {
		return err
	}
	request, err = request.narrowTo(window)
	if err != nil {
		return err
	}
	return m.mutateCommunicationTransaction(
		ctx, scope, request, CommunicationClaimAuthoritySnapshot{},
		func(tx *communicationTx) error { return fn(tx, contexts) },
	)
}
