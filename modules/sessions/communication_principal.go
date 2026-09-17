// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// communicationPrincipalRecipient resolves the authenticated identity to its
// canonical mailbox identity. In particular, an AgentIdentity is never stored
// as though it were the canonical Agent UUID.
func (m *Module) communicationPrincipalRecipient(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) (RecipientRef, PrincipalResolution, error) {
	if !communicationPortBound(m.communicationDirectoryResolver) {
		return RecipientRef{}, PrincipalResolution{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication principal resolver is unavailable",
		)
	}
	resolution, err := m.communicationDirectoryResolver.ResolvePrincipal(ctx, scope, principal)
	if err != nil || ValidatePrincipalResolution(resolution) != nil ||
		resolution.Scope != scope || resolution.Principal != principal {
		return RecipientRef{}, PrincipalResolution{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication principal resolution is unavailable",
		)
	}
	if resolution.Outcome != PrincipalResolved || resolution.Recipient == nil ||
		!resolution.Recipient.Eligible {
		if resolution.Outcome == PrincipalUnknown {
			return RecipientRef{}, PrincipalResolution{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication principal resolution is unavailable",
			)
		}
		return RecipientRef{}, PrincipalResolution{}, communicationError(
			ErrCommunicationForbidden, "communication principal is not eligible",
		)
	}
	return resolution.Recipient.Recipient, cloneDirectNoticePrincipalResolution(resolution), nil
}

func communicationActorForRecipient(recipient RecipientRef) (CommunicationActorRef, error) {
	kind := ActorUser
	switch recipient.Kind {
	case RecipientUser:
		kind = ActorUser
	case RecipientAgent:
		kind = ActorAgent
	case RecipientSession:
		kind = ActorSession
	default:
		return CommunicationActorRef{}, communicationError(
			ErrInvalidCommunicationModel, "unsupported communication actor recipient",
		)
	}
	actor := CommunicationActorRef{Kind: kind, Ref: recipient.Ref}
	if err := actor.Validate(); err != nil {
		return CommunicationActorRef{}, err
	}
	return actor, nil
}

func (m *Module) communicationActorForPrincipal(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) (CommunicationActorRef, error) {
	actor, _, err := m.communicationActorAndResolutionForPrincipal(ctx, scope, principal)
	return actor, err
}

// communicationActorAndResolutionForPrincipal keeps the authoritative
// external-Agent-to-canonical-Agent bridge attached to the sender it produced.
// A caller must never retain only the canonical UUID and thereby discard the
// directory evidence needed by the same-transaction publish planner.
func (m *Module) communicationActorAndResolutionForPrincipal(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) (CommunicationActorRef, *PrincipalResolution, error) {
	if recipient, ok := CanonicalPrincipalRecipient(principal); ok {
		actor, err := communicationActorForRecipient(recipient)
		return actor, nil, err
	}
	recipient, resolution, err := m.communicationPrincipalRecipient(ctx, scope, principal)
	if err != nil {
		return CommunicationActorRef{}, nil, err
	}
	actor, err := communicationActorForRecipient(recipient)
	if err != nil {
		return CommunicationActorRef{}, nil, err
	}
	return actor, &resolution, nil
}

func communicationRecipientForActor(actor CommunicationActorRef) (RecipientRef, error) {
	kind := RecipientUser
	switch actor.Kind {
	case ActorUser:
		kind = RecipientUser
	case ActorAgent:
		kind = RecipientAgent
	case ActorSession:
		kind = RecipientSession
	default:
		return RecipientRef{}, communicationError(
			ErrInvalidCommunicationModel, "unsupported communication recipient actor",
		)
	}
	recipient := RecipientRef{Kind: kind, Ref: actor.Ref}
	if err := recipient.Validate(); err != nil {
		return RecipientRef{}, err
	}
	return recipient, nil
}

func communicationAuditActor(actor CommunicationActorRef) (string, string) {
	kind := model.ActorUser
	switch actor.Kind {
	case ActorAgent, ActorSession:
		kind = model.ActorAgent
	case ActorSystem:
		kind = model.ActorSystem
	}
	return string(actor.Kind) + ":" + actor.Ref, kind
}

func recipientAudienceKind(recipient RecipientRef) (AudienceSelectorKind, error) {
	switch recipient.Kind {
	case RecipientUser:
		return AudienceUser, nil
	case RecipientAgent:
		return AudienceAgent, nil
	case RecipientSession:
		return AudienceSession, nil
	default:
		return "", communicationError(
			ErrInvalidCommunicationModel, "unsupported direct recipient kind",
		)
	}
}

// communicationRecipientGrantPrincipal constructs only a server-derived
// principal shape for resolving the target's read grants. Users and live
// sessions retain their real identity tuple. A canonical Agent target uses the
// existing system-to-Agent grant seam because the directory API deliberately
// has no reverse mapping from UUID to an external login identifier.
func (m *Module) communicationRecipientGrantPrincipal(
	ctx context.Context,
	scope DirectoryScopeRef,
	recipient RecipientRef,
) (CommunicationPrincipal, error) {
	switch recipient.Kind {
	case RecipientUser:
		return CommunicationPrincipal{UserID: model.ID(recipient.Ref)}, nil
	case RecipientAgent:
		return CommunicationPrincipal{
			System: true, SystemActorRef: "direct-recipient-grant",
			SystemGrantAgentID: model.ID(recipient.Ref),
		}, nil
	case RecipientSession:
		witness, err := m.CommunicationSessionRecipient(
			ctx, scope.TenantID, scope.WorkspaceID, recipient.Ref,
		)
		if err != nil || !witness.Found || !witness.WorkspaceEligible || !witness.Active ||
			witness.RunRef == "" {
			return CommunicationPrincipal{}, communicationError(
				ErrCommunicationEvidenceUnknown, "session recipient authority is unavailable",
			)
		}
		return CommunicationPrincipal{
			AgentExternalID: witness.AgentRef, SessionID: witness.SID,
			SessionRunRef: witness.RunRef, SessionFence: witness.Fence,
			SessionWorkspaceID: scope.WorkspaceID, PurposeRestricted: true,
		}, nil
	default:
		return CommunicationPrincipal{}, communicationError(
			ErrInvalidCommunicationModel, "unsupported direct recipient kind",
		)
	}
}

// communicationClaimAuthoritySnapshot reads request-local Claim candidates
// from the sessions plane and turns them into opaque leased store facts. The
// subsequent communication transaction locks and revalidates the exact row,
// version, SID, fence, state and DB-time deadline before any effect.
func (m *Module) communicationClaimAuthoritySnapshot(
	ctx context.Context,
	tenant model.TenantID,
	claims []CommunicationClaimRef,
) (CommunicationClaimAuthoritySnapshot, error) {
	claims = canonicalCommunicationClaims(append([]CommunicationClaimRef(nil), claims...))
	if len(claims) == 0 {
		return CommunicationClaimAuthoritySnapshot{}, nil
	}
	if m == nil || m.data == nil {
		return CommunicationClaimAuthoritySnapshot{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Claim authority store is unavailable",
		)
	}
	facts := make([]store.AuthorizationFactRef, 0, len(claims))
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline || deadline.IsZero() {
		return CommunicationClaimAuthoritySnapshot{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Claim authority requires a finite deadline",
		)
	}
	claimCtx, cancelClaim := communicationCoreAuthorityContext(ctx, deadline)
	defer cancelClaim()
	err := m.data.View(claimCtx, tenant, func(sc store.Scope) error {
		for _, claim := range claims {
			record, found, err := findClaim(claimCtx, sc, claim.SessionSID)
			if err != nil {
				return err
			}
			if !found || record.String(colClaimState) != claimActive ||
				record.Int(colFence) != claim.Fence {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "Claim authority is no longer current",
				)
			}
			id, err := model.ParseID(record.String(model.ColID))
			if err != nil || id.IsZero() || record.Int(model.ColVersion) < 1 {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "Claim authority row is malformed",
				)
			}
			deadline, err := model.ParseTimestamp(record.String(colLeaseExpires))
			if err != nil {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "Claim authority deadline is malformed",
				)
			}
			fact, err := store.NewLeaseFenceAuthorizationFactRef(
				claimKind, id, record.Int(model.ColVersion), claim.SessionSID, claim.Fence, deadline,
			)
			if err != nil {
				return fmt.Errorf("communication Claim fact: %w", err)
			}
			facts = append(facts, fact)
		}
		return nil
	})
	if err != nil {
		return CommunicationClaimAuthoritySnapshot{}, err
	}
	return NewCommunicationClaimAuthoritySnapshot(claims, facts)
}

func communicationClaimsForPrincipal(principal CommunicationPrincipal) []CommunicationClaimRef {
	if principal.SessionID == "" {
		return nil
	}
	return []CommunicationClaimRef{{
		SessionSID: principal.SessionID, Fence: principal.SessionFence,
	}}
}

func communicationPrincipalMatchesRecipient(
	principal CommunicationPrincipal,
	recipient RecipientRef,
) bool {
	switch {
	case principal.UserID != "":
		return recipient == (RecipientRef{Kind: RecipientUser, Ref: principal.UserID.String()})
	case principal.SessionID != "":
		return recipient == (RecipientRef{Kind: RecipientSession, Ref: principal.SessionID})
	case principal.AgentExternalID != "":
		return recipient.Kind == RecipientAgent && recipient.Validate() == nil
	default:
		return false
	}
}

func communicationClaimsEqualSnapshot(
	claims []CommunicationClaimRef,
	snapshot CommunicationClaimAuthoritySnapshot,
) bool {
	want := canonicalCommunicationClaims(append([]CommunicationClaimRef(nil), claims...))
	got := make([]CommunicationClaimRef, 0, len(snapshot.facts))
	for _, fact := range snapshot.facts {
		sid, fence, _, ok := fact.LeaseFenceWitness()
		if !ok || fact.Kind != claimKind {
			return false
		}
		got = append(got, CommunicationClaimRef{SessionSID: sid, Fence: fence})
	}
	got = canonicalCommunicationClaims(got)
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

func communicationClaimsContainedInSnapshot(
	claims []CommunicationClaimRef,
	snapshot CommunicationClaimAuthoritySnapshot,
) bool {
	want := canonicalCommunicationClaims(append([]CommunicationClaimRef(nil), claims...))
	got := make(map[CommunicationClaimRef]struct{}, len(snapshot.facts))
	for _, fact := range snapshot.facts {
		sid, fence, _, ok := fact.LeaseFenceWitness()
		if !ok || fact.Kind != claimKind {
			return false
		}
		got[CommunicationClaimRef{SessionSID: sid, Fence: fence}] = struct{}{}
	}
	for _, claim := range want {
		if _, found := got[claim]; !found {
			return false
		}
	}
	return true
}
