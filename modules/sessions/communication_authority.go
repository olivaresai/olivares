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

const communicationCoreAuthorizationEvidenceRef = "core.authorization_evidence.v1"

// These two private ports retain the exact core resolver/authorizer pair without
// widening either interface. The existing core Authenticator and Authorizer
// satisfy both ports directly, and the bundle is indivisible: binding it with
// either half missing stores nil, so one pointer check proves both.
//
// This comment used to end "does not make them a readiness term or route traffic
// through them". BOTH HALVES OF THAT ARE NOW FALSE, and they stopped being true
// at different moments:
//   - traffic: Ack already routes through the binder
//     (bindCurrentCommunicationRequestAuthority in communication_ack_service.go)
//     and so does the batch path (communication_authority_batch.go).
//   - readiness: as of 2026-08-26 this bundle IS the PermissionsReady term (see
//     communication_readiness.go), because the two CoreEntity* ports cannot
//     authorize faithfully from a CommunicationPrincipal alone -- it carries no
//     role, no membership and no AAL, and rbacAllows needs the roles.
//
// The stale half survived two readings of this file. A contradiction dies in the
// commit that creates it, not in a later one.
type communicationPrincipalAuthorityResolver interface {
	ResolvePrincipalScope(context.Context, auth.PrincipalRef, model.TenantID) (auth.Principal, error)
}

type communicationAuthorizationEvidenceSource interface {
	AuthorizeEvidence(context.Context, auth.Request) auth.AuthorizationEvidence
}

// communicationRequestAuthoritySources is an indivisible late-bound pair. A
// single bundle pointer prevents a rebind from retaining one half of an older
// resolver/source combination.
type communicationRequestAuthoritySources struct {
	resolver communicationPrincipalAuthorityResolver
	source   communicationAuthorizationEvidenceSource
}

var (
	_ communicationPrincipalAuthorityResolver  = (*auth.Authenticator)(nil)
	_ communicationAuthorizationEvidenceSource = (*auth.Authorizer)(nil)
)

func (m *Module) useCommunicationRequestAuthoritySources(
	resolver communicationPrincipalAuthorityResolver,
	source communicationAuthorizationEvidenceSource,
) {
	if !communicationPortBound(resolver) || !communicationPortBound(source) {
		m.communicationAuthoritySources = nil
		return
	}
	m.communicationAuthoritySources = &communicationRequestAuthoritySources{
		resolver: resolver,
		source:   source,
	}
}

func (m *Module) bindCurrentCommunicationRequestAuthority(
	ctx context.Context,
	ref auth.PrincipalRef,
	question communicationAuthorityQuestion,
) (communicationRequestAuthority, error) {
	sources := m.communicationAuthoritySources
	if sources == nil || !communicationPortBound(sources.resolver) ||
		!communicationPortBound(sources.source) {
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication request authority sources are unavailable",
		)
	}
	return bindCommunicationRequestAuthority(ctx, sources.resolver, sources.source, ref, question)
}

// communicationAuthorityQuestion is the server-selected, exact authorization
// question. All fields are private and permission is derived from the closed
// entity/operation pair; a caller can never select a weaker permission.
type communicationAuthorityQuestion struct {
	entity     EntityRef
	operation  CommunicationOperation
	permission auth.Permission
}

func newCommunicationAuthorityQuestion(
	scope DirectoryScopeRef,
	kind model.Kind,
	id model.ID,
	operation CommunicationOperation,
) (communicationAuthorityQuestion, error) {
	if err := scope.Validate(); err != nil {
		return communicationAuthorityQuestion{}, err
	}
	if !validCanonicalCommunicationID(id) {
		return communicationAuthorityQuestion{}, communicationError(
			ErrInvalidCommunicationModel, "invalid communication authority entity",
		)
	}
	permission, ok := communicationAuthorityPermission(kind, operation)
	if !ok {
		return communicationAuthorityQuestion{}, communicationError(
			ErrInvalidCommunicationModel,
			"unsupported communication authority entity/operation pair",
		)
	}
	question := communicationAuthorityQuestion{
		entity: EntityRef{
			TenantID: scope.TenantID, Kind: kind, ID: id, WorkspaceID: scope.WorkspaceID,
		},
		operation: operation, permission: permission,
	}
	if err := question.validate(); err != nil {
		return communicationAuthorityQuestion{}, err
	}
	return question, nil
}

func (q communicationAuthorityQuestion) validate() error {
	scope := DirectoryScopeRef{TenantID: q.entity.TenantID, WorkspaceID: q.entity.WorkspaceID}
	if err := scope.Validate(); err != nil {
		return err
	}
	permission, ok := communicationAuthorityPermission(q.entity.Kind, q.operation)
	if !ok || permission != q.permission || !validCanonicalCommunicationID(q.entity.ID) {
		return communicationError(
			ErrInvalidCommunicationModel, "invalid communication authority question",
		)
	}
	return nil
}

func communicationAuthorityPermission(
	kind model.Kind,
	operation CommunicationOperation,
) (auth.Permission, bool) {
	switch operation {
	case CommunicationRead:
		switch kind {
		case channelKind:
			return permChannelRead, true
		case messageKind:
			return permMessageRead, true
		case messageDeliveryKind:
			return permDeliveryRead, true
		case decisionRequestKind:
			return permDecisionRequestRead, true
		case handoffKind:
			return permHandoffRead, true
		}
	case CommunicationDeliveryWrite:
		if kind == messageDeliveryKind {
			return permDeliveryWrite, true
		}
	case CommunicationDeliveryAdmin:
		if kind == messageDeliveryKind {
			return permDeliveryAdmin, true
		}
	case CommunicationDecisionRequestWrite:
		if kind == decisionRequestKind {
			return permDecisionRequestWrite, true
		}
	case CommunicationMessageSend:
		if kind == channelKind {
			return permMessageSendWrite, true
		}
	case CommunicationHandoffResponse:
		if kind == handoffKind {
			return permHandoffResponseWrite, true
		}
	}
	return "", false
}

// communicationRequestAuthority is the only successful result of the direct
// binder. Its closure is derived from one opaque credential reference and seals
// the exact question, so a later service cannot reuse evidence for another
// tenant, resource, or verb. DENY and UNKNOWN never produce this value.
type communicationRequestAuthority struct {
	access func(
		communicationRequestAuthorityAccess,
		communicationAuthorityQuestion,
		CommunicationClaimAuthoritySnapshot,
	) (communicationRequestAuthorityAccessResult, error)
}

type communicationRequestAuthorityAccess uint8

const (
	communicationRequestAuthorityInspect communicationRequestAuthorityAccess = iota + 1
	communicationRequestAuthorityConsume
)

type communicationRequestAuthorityAccessResult struct {
	snapshot   communicationRequestAuthoritySnapshot
	inspection communicationRequestAuthorityInspection
	context    communicationRequestAuthorityContext
}

// communicationRequestAuthorityBindingID is a non-zero-size, opaque identity
// marker shared only by the inspection and consumed context from one binding.
// Pointer equality prevents splicing an inspection from binding A onto a
// consumed witness from binding B without exposing a credential ref/capability.
type communicationRequestAuthorityBindingID struct {
	marker byte
}

// communicationRequestAuthorityInspection exposes only the server-derived
// identity sealed by the binder. It is a distinct type from the post-CAS
// context, so preflight code cannot accidentally access authorization evidence.
type communicationRequestAuthorityInspection struct {
	question  communicationAuthorityQuestion
	principal CommunicationPrincipal
	bindingID *communicationRequestAuthorityBindingID
}

// communicationRequestAuthorityContext is the identity and exact question
// that core authorized. A later effect callback receives this sealed value
// directly; it never accepts a second caller-supplied CommunicationPrincipal.
type communicationRequestAuthorityContext struct {
	question  communicationAuthorityQuestion
	principal CommunicationPrincipal
	bindingID *communicationRequestAuthorityBindingID
	witness   ReadWitness
}

// bindCommunicationRequestAuthority reconstructs current authority for one
// exact authenticated credential, creates the auth.Request in sessions, and
// seals the direct typed verdict behind one inspect-or-consume closure. It must
// run before any communication Mutate; the returned value contains no auth
// resolver, authorizer, View, Store, or raw bearer capability.
func bindCommunicationRequestAuthority(
	ctx context.Context,
	resolver communicationPrincipalAuthorityResolver,
	source communicationAuthorizationEvidenceSource,
	ref auth.PrincipalRef,
	question communicationAuthorityQuestion,
) (communicationRequestAuthority, error) {
	if ctx == nil || !communicationPortBound(resolver) || !communicationPortBound(source) ||
		ref == (auth.PrincipalRef{}) {
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication request authority is unavailable",
		)
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline || deadline.IsZero() {
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication authority requires a finite deadline",
		)
	}
	if err := question.validate(); err != nil {
		return communicationRequestAuthority{}, err
	}

	resolved, err := resolver.ResolvePrincipalScope(ctx, ref, question.entity.TenantID)
	if err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			return communicationRequestAuthority{}, communicationError(
				ErrCommunicationEvidenceUnknown, "authenticated credential is no longer current",
			)
		}
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication principal authority is unavailable",
		)
	}
	resolvedRef, ok := resolved.Ref()
	if !ok || resolvedRef != ref {
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "resolved principal crossed its credential reference",
		)
	}
	principal, err := communicationPrincipalFromResolvedAuth(resolved)
	if err != nil {
		return communicationRequestAuthority{}, err
	}

	request := auth.Request{
		Principal:  resolved,
		Permission: question.permission,
		Tenant:     question.entity.TenantID,
		Resource: auth.ResourceAttrs{
			Kind:        string(question.entity.Kind),
			ID:          question.entity.ID.String(),
			WorkspaceID: question.entity.WorkspaceID,
		},
	}
	evidence := source.AuthorizeEvidence(ctx, request)
	outcome, facts, err := validateCommunicationCoreAuthorizationEvidence(
		evidence, question.entity.TenantID,
	)
	if err != nil {
		return communicationRequestAuthority{}, err
	}
	// ResolvePrincipalScope clips its private principal evidence to the request
	// deadline. A direct authorization result can only narrow that intersection;
	// it must never claim freshness beyond the same request lifetime.
	if !evidence.FreshUntil.IsZero() && evidence.FreshUntil.After(deadline) {
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"core communication authority exceeds the request deadline",
		)
	}
	switch outcome {
	case ReadDeny:
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationForbidden, "core communication authority denied",
		)
	case ReadUnknown:
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "core communication authority is unavailable",
		)
	case ReadAllow:
		// Core's resource guard must deny a crossed confinement. If a malformed
		// source says ALLOW anyway, do not let the derived sessions principal
		// cross its server-authored workspace.
		if err := ValidateCommunicationPrincipalForScope(
			principal,
			DirectoryScopeRef{
				TenantID: question.entity.TenantID, WorkspaceID: question.entity.WorkspaceID,
			},
		); err != nil {
			return communicationRequestAuthority{}, communicationError(
				ErrCommunicationEvidenceUnknown, "core authority crossed principal scope",
			)
		}
	default:
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "core communication authority has no verdict",
		)
	}

	witness := ReadWitness{
		Outcome: ReadAllow, Code: "core_authorization_allowed",
		Entity: question.entity, Operation: question.operation, Principal: principal,
		ObservedAt: evidence.ObservedAt, FreshUntil: evidence.FreshUntil,
		CorePermission: communicationAuthorityEvidence(evidence.CorePermission),
		ResourceGuard:  communicationAuthorityEvidence(evidence.ResourceGuard),
		ForbidAbsence:  communicationAuthorityEvidence(evidence.ForbidAbsence),
		Facts:          append([]store.AuthorizationFactRef(nil), facts...),
		EvidenceRef:    communicationCoreAuthorizationEvidenceRef,
	}
	if err := ValidateReadWitness(witness); err != nil {
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "core communication authority is malformed",
		)
	}
	consumed := &atomic.Bool{}
	sealedQuestion := question
	sealedBindingID := &communicationRequestAuthorityBindingID{marker: 1}
	sealedSnapshot := communicationRequestAuthoritySnapshot{
		facts:      append([]store.AuthorizationFactRef(nil), facts...),
		observedAt: witness.ObservedAt,
		freshUntil: witness.FreshUntil,
		bindingID:  sealedBindingID,
	}
	sealedContext := communicationRequestAuthorityContext{
		question: question, principal: principal, bindingID: sealedBindingID,
	}
	sealedWitness := cloneCommunicationRequestAuthorityWitness(witness)
	return communicationRequestAuthority{access: func(
		access communicationRequestAuthorityAccess,
		expected communicationAuthorityQuestion,
		claims CommunicationClaimAuthoritySnapshot,
	) (communicationRequestAuthorityAccessResult, error) {
		if expected.validate() != nil || expected != sealedQuestion {
			return communicationRequestAuthorityAccessResult{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication request authority binding is malformed",
			)
		}
		switch access {
		case communicationRequestAuthorityInspect:
			return communicationRequestAuthorityAccessResult{
				inspection: communicationRequestAuthorityInspection{
					question:  sealedContext.question,
					principal: sealedContext.principal,
					bindingID: sealedContext.bindingID,
				},
			}, nil
		case communicationRequestAuthorityConsume:
			if err := requireCommunicationSessionClaim(sealedContext.principal, claims); err != nil {
				return communicationRequestAuthorityAccessResult{}, err
			}
		default:
			return communicationRequestAuthorityAccessResult{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication request authority access is malformed",
			)
		}
		if !consumed.CompareAndSwap(false, true) {
			return communicationRequestAuthorityAccessResult{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication request authority was already consumed",
			)
		}
		return communicationRequestAuthorityAccessResult{
			snapshot: communicationRequestAuthoritySnapshot{
				facts:      append([]store.AuthorizationFactRef(nil), sealedSnapshot.facts...),
				observedAt: sealedSnapshot.observedAt,
				freshUntil: sealedSnapshot.freshUntil,
				bindingID:  sealedSnapshot.bindingID,
			},
			context: communicationRequestAuthorityContext{
				question:  sealedContext.question,
				principal: sealedContext.principal,
				bindingID: sealedContext.bindingID,
				witness:   cloneCommunicationRequestAuthorityWitness(sealedWitness),
			},
		}, nil
	}}, nil
}

func cloneCommunicationRequestAuthorityWitness(witness ReadWitness) ReadWitness {
	witness.Facts = append([]store.AuthorizationFactRef(nil), witness.Facts...)
	return witness
}

func requireCommunicationSessionClaim(
	principal CommunicationPrincipal,
	claims CommunicationClaimAuthoritySnapshot,
) error {
	if principal.SessionID == "" {
		return nil
	}
	for _, fact := range claims.facts {
		sid, fence, _, ok := fact.LeaseFenceWitness()
		if ok && fact.Kind == claimKind && sid == principal.SessionID &&
			fence == principal.SessionFence {
			return nil
		}
	}
	return communicationError(
		ErrCommunicationEvidenceUnknown,
		"communication-session authority lacks its exact Claim witness",
	)
}

func communicationPrincipalFromResolvedAuth(
	resolved auth.Principal,
) (CommunicationPrincipal, error) {
	if _, delegated := resolved.ActAs(); delegated {
		return CommunicationPrincipal{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delegated credentials are not K3 principals",
		)
	}

	var principal CommunicationPrincipal
	switch {
	case resolved.IsCommunicationSessionCredential():
		principal = CommunicationPrincipal{
			AgentExternalID:    resolved.AgentIdentity,
			SessionID:          resolved.SessionIdentity,
			SessionRunRef:      resolved.SessionRunRef,
			SessionFence:       resolved.SessionFence,
			SessionWorkspaceID: resolved.SessionWorkspaceID,
			PurposeRestricted:  true,
		}
	case resolved.IsPurposeRestricted() || resolved.SessionIdentity != "" ||
		resolved.SessionRunRef != "" || resolved.SessionFence != 0 ||
		!resolved.SessionWorkspaceID.IsZero():
		return CommunicationPrincipal{}, communicationError(
			ErrCommunicationEvidenceUnknown, "unsupported purpose or session credential",
		)
	case resolved.AgentIdentity != "":
		// Agent identity deliberately wins over an owning UserID on an agent
		// token. The user is not an alternate ChannelGrant subject.
		principal = CommunicationPrincipal{AgentExternalID: resolved.AgentIdentity}
	case !resolved.UserID.IsZero():
		principal = CommunicationPrincipal{UserID: resolved.UserID}
	default:
		return CommunicationPrincipal{}, communicationError(
			ErrCommunicationEvidenceUnknown, "authenticated credential has no K3 identity",
		)
	}
	if err := ValidateCommunicationPrincipal(principal); err != nil {
		return CommunicationPrincipal{}, communicationError(
			ErrCommunicationEvidenceUnknown, "resolved K3 principal is malformed",
		)
	}
	return principal, nil
}

func validateCommunicationCoreAuthorizationEvidence(
	evidence auth.AuthorizationEvidence,
	tenant model.TenantID,
) (ReadOutcome, []store.AuthorizationFactRef, error) {
	checks := []auth.CheckEvidence{
		evidence.CorePermission, evidence.ResourceGuard, evidence.ForbidAbsence,
	}
	verdicts := make([]AssessmentVerdict, 0, len(checks))
	for _, check := range checks {
		if !check.Verdict.Valid() || !boundedToken(check.Code, 128) {
			return ReadUnknown, nil, communicationError(
				ErrCommunicationEvidenceUnknown, "core authorization check is malformed",
			)
		}
		switch check.Verdict {
		case auth.CheckClean:
			verdicts = append(verdicts, VerdictClean)
		case auth.CheckBroken:
			verdicts = append(verdicts, VerdictBroken)
		default:
			verdicts = append(verdicts, VerdictUnknown)
		}
	}
	want := ReadUnknown
	switch andVerdicts(verdicts...) {
	case VerdictClean:
		want = ReadAllow
	case VerdictBroken:
		want = ReadDeny
	}
	got := ReadUnknown
	switch evidence.Outcome {
	case auth.EvidenceAllow:
		got = ReadAllow
	case auth.EvidenceDeny:
		got = ReadDeny
	case auth.EvidenceUnknown:
		got = ReadUnknown
	default:
		return ReadUnknown, nil, communicationError(
			ErrCommunicationEvidenceUnknown, "core authorization outcome is malformed",
		)
	}
	if got != want {
		return ReadUnknown, nil, communicationError(
			ErrCommunicationEvidenceUnknown, "core authorization outcome contradicts its checks",
		)
	}

	windowZero := evidence.ObservedAt.IsZero() && evidence.FreshUntil.IsZero()
	windowFinite := !evidence.ObservedAt.IsZero() && evidence.FreshUntil.After(evidence.ObservedAt)
	if !windowZero && !windowFinite {
		return ReadUnknown, nil, communicationError(
			ErrCommunicationEvidenceUnknown, "core authorization window is malformed",
		)
	}
	if got == ReadAllow && (!windowFinite || len(evidence.Facts) == 0) {
		return ReadUnknown, nil, communicationError(
			ErrCommunicationEvidenceUnknown, "allowed core authority lacks a finite proof",
		)
	}
	if got == ReadUnknown && (!windowZero || len(evidence.Facts) != 0) {
		return ReadUnknown, nil, communicationError(
			ErrCommunicationEvidenceUnknown, "unknown core authority carries positive proof",
		)
	}
	if !windowFinite && len(evidence.Facts) != 0 {
		return ReadUnknown, nil, communicationError(
			ErrCommunicationEvidenceUnknown, "core authority facts lack a finite window",
		)
	}

	facts, err := validateCommunicationAuthorityFacts(
		evidence.Facts, tenant, got == ReadAllow,
	)
	if err != nil {
		return ReadUnknown, nil, err
	}
	return got, facts, nil
}

func validateCommunicationAuthorityFacts(
	facts []store.AuthorizationFactRef,
	tenant model.TenantID,
	requireAuthorityEpochs bool,
) ([]store.AuthorizationFactRef, error) {
	canonical, err := CanonicalAuthorizationFacts(facts)
	if err != nil || !equalCommunicationAuthorityFacts(canonical, facts) {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "core authorization facts are not canonical",
		)
	}
	foundDirectoryEpoch := false
	foundAuthorizationEpoch := false
	for _, fact := range canonical {
		if fact.Kind != model.DirectoryEpochKind && fact.Kind != model.AuthorizationEpochKind {
			continue
		}
		if fact.ID != model.ID(tenant) {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown, "core authorization epoch crosses tenant",
			)
		}
		plain := store.AuthorizationFactRef{
			Kind: fact.Kind, ID: fact.ID, Version: fact.Version,
		}
		if fact != plain {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"core authorization epoch carries a leased witness",
			)
		}
		foundDirectoryEpoch = foundDirectoryEpoch || fact.Kind == model.DirectoryEpochKind
		foundAuthorizationEpoch = foundAuthorizationEpoch || fact.Kind == model.AuthorizationEpochKind
	}
	if requireAuthorityEpochs && (!foundDirectoryEpoch || !foundAuthorizationEpoch) {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"allowed core authority is not bound to directory and authorization epochs",
		)
	}
	return canonical, nil
}

func communicationAuthorityEvidence(check auth.CheckEvidence) AuthorityEvidence {
	verdict := VerdictUnknown
	switch check.Verdict {
	case auth.CheckClean:
		verdict = VerdictClean
	case auth.CheckBroken:
		verdict = VerdictBroken
	}
	return AuthorityEvidence{
		Verdict: verdict, Code: check.Code, EvidenceRef: communicationCoreAuthorizationEvidenceRef,
	}
}

func equalCommunicationAuthorityFacts(
	left []store.AuthorizationFactRef,
	right []store.AuthorizationFactRef,
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

// communicationRequestAuthoritySnapshot is the data-only transaction token
// derived from a successful exact request binding. It is intentionally private,
// opaque to JSON, and contains no capability that could open an AuthView from a
// communication Mutate.
type communicationRequestAuthoritySnapshot struct {
	facts      []store.AuthorizationFactRef
	observedAt time.Time
	freshUntil time.Time
	bindingID  *communicationRequestAuthorityBindingID
}

// communicationAuthorityWindow is a finite, local-evidence interval that can
// only narrow an already validated request-authority snapshot. It carries no
// facts or capability: the exact reader derives it from its owned principal
// resolution and ChannelGrant closure before consuming the one-shot binding.
type communicationAuthorityWindow struct {
	observedAt time.Time
	freshUntil time.Time
}

func newCommunicationAuthorityWindow(
	observedAt time.Time,
	freshUntil time.Time,
) (communicationAuthorityWindow, error) {
	window := communicationAuthorityWindow{
		observedAt: observedAt.UTC(),
		freshUntil: freshUntil.UTC(),
	}
	if err := window.validate(); err != nil {
		return communicationAuthorityWindow{}, err
	}
	return window, nil
}

func (w communicationAuthorityWindow) validate() error {
	if w.observedAt.IsZero() || !w.freshUntil.After(w.observedAt) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication local authority window is unavailable",
		)
	}
	return nil
}

// narrowTo intersects a validated core request with local authority. The
// intersection is performed only after transactionSnapshot has verified the
// exact snapshot/witness equality, so a local service can shorten but never
// extend or replace core authority.
func (s communicationRequestAuthoritySnapshot) narrowTo(
	window communicationAuthorityWindow,
) (communicationRequestAuthoritySnapshot, error) {
	if s.empty() || s.validate() != nil || window.validate() != nil {
		return communicationRequestAuthoritySnapshot{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication authority window cannot be intersected",
		)
	}
	result := communicationRequestAuthoritySnapshot{
		facts:      append([]store.AuthorizationFactRef(nil), s.facts...),
		observedAt: s.observedAt.UTC(),
		freshUntil: s.freshUntil.UTC(),
		bindingID:  s.bindingID,
	}
	if window.observedAt.After(result.observedAt) {
		result.observedAt = window.observedAt
	}
	if window.freshUntil.Before(result.freshUntil) {
		result.freshUntil = window.freshUntil
	}
	if !result.freshUntil.After(result.observedAt) {
		return communicationRequestAuthoritySnapshot{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication authority windows do not overlap",
		)
	}
	return result, nil
}

func (a communicationRequestAuthority) transactionSnapshot(
	expected communicationAuthorityQuestion,
	claims CommunicationClaimAuthoritySnapshot,
) (
	communicationRequestAuthoritySnapshot,
	communicationRequestAuthorityContext,
	error,
) {
	if a.access == nil {
		return communicationRequestAuthoritySnapshot{}, communicationRequestAuthorityContext{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication request authority binding is malformed",
		)
	}
	result, err := a.access(communicationRequestAuthorityConsume, expected, claims)
	if err != nil {
		return communicationRequestAuthoritySnapshot{}, communicationRequestAuthorityContext{}, err
	}
	if result.snapshot.empty() || result.snapshot.validate() != nil ||
		result.context.bindingID == nil || result.snapshot.bindingID != result.context.bindingID ||
		result.context.question != expected ||
		ValidateReadWitness(result.context.witness) != nil ||
		result.context.witness.Outcome != ReadAllow ||
		result.context.witness.Entity != expected.entity ||
		result.context.witness.Operation != expected.operation ||
		result.context.witness.Principal != result.context.principal ||
		!equalCommunicationAuthorityFacts(
			result.snapshot.facts, result.context.witness.Facts,
		) || !result.snapshot.observedAt.Equal(result.context.witness.ObservedAt) ||
		!result.snapshot.freshUntil.Equal(result.context.witness.FreshUntil) {
		return communicationRequestAuthoritySnapshot{}, communicationRequestAuthorityContext{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication request authority consumption is malformed",
		)
	}
	return result.snapshot, result.context, nil
}

// contextFor returns a defensive, non-consuming view of the exact identity
// sealed by the binder. It exposes no witness, facts, resolver/authorizer, or
// raw bearer; only a successful consume can contribute facts to a transaction.
func (a communicationRequestAuthority) contextFor(
	expected communicationAuthorityQuestion,
) (communicationRequestAuthorityInspection, error) {
	if a.access == nil {
		return communicationRequestAuthorityInspection{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication request authority binding is malformed",
		)
	}
	result, err := a.access(
		communicationRequestAuthorityInspect,
		expected,
		CommunicationClaimAuthoritySnapshot{},
	)
	if err != nil {
		return communicationRequestAuthorityInspection{}, err
	}
	if result.inspection.bindingID == nil || result.inspection.question != expected ||
		ValidateCommunicationPrincipalForScope(
			result.inspection.principal,
			DirectoryScopeRef{
				TenantID: expected.entity.TenantID, WorkspaceID: expected.entity.WorkspaceID,
			},
		) != nil {
		return communicationRequestAuthorityInspection{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication request authority inspection is malformed",
		)
	}
	return result.inspection, nil
}

func (s communicationRequestAuthoritySnapshot) empty() bool {
	return len(s.facts) == 0 && s.observedAt.IsZero() && s.freshUntil.IsZero()
}

func (s communicationRequestAuthoritySnapshot) validate() error {
	if s.empty() {
		return nil
	}
	if len(s.facts) == 0 || s.observedAt.IsZero() || !s.freshUntil.After(s.observedAt) {
		return communicationError(
			ErrInvalidCommunicationModel, "invalid request authority snapshot",
		)
	}
	facts, err := CanonicalAuthorizationFacts(s.facts)
	if err != nil || !equalCommunicationAuthorityFacts(facts, s.facts) {
		return communicationError(
			ErrInvalidCommunicationModel, "non-canonical request authority snapshot",
		)
	}
	return nil
}
