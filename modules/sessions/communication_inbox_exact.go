// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

// listDirectNoticeInboxWithAuthority is the exact private seam used while the
// aggregate K3 readiness conjunction remains OFF.
func (m *Module) listDirectNoticeInboxWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	query DirectNoticeInboxQuery,
) (DirectNoticeInboxPage, error) {
	return m.listDirectNoticeInboxWithCurrentAuthority(
		ctx, scope, ref, query, OpenProtectedPayload, false,
	)
}

func (m *Module) listDirectNoticeInboxWithAuthorityAndOpener(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	query DirectNoticeInboxQuery,
	opener directNoticePayloadOpener,
) (DirectNoticeInboxPage, error) {
	return m.listDirectNoticeInboxWithCurrentAuthority(
		ctx, scope, ref, query, opener, false,
	)
}

func (m *Module) listDirectNoticeInboxWithCurrentAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	query DirectNoticeInboxQuery,
	opener directNoticePayloadOpener,
	requireReadiness bool,
) (DirectNoticeInboxPage, error) {
	identity, err := m.bindCurrentCommunicationInboxIdentity(ctx, scope, ref)
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return DirectNoticeInboxPage{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}
	query, err = normalizeDirectNoticeInboxQuery(query)
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}
	if opener == nil {
		return DirectNoticeInboxPage{}, communicationError(
			ErrInvalidCommunicationModel,
			"direct notice content opener is unavailable",
		)
	}
	return m.listDirectNoticeInboxWithBoundAuthority(
		ctx, identity, query, opener,
	)
}

func (m *Module) listDirectNoticeInboxWithBoundAuthority(
	ctx context.Context,
	identity communicationInboxIdentityBinding,
	query DirectNoticeInboxQuery,
	opener directNoticePayloadOpener,
) (DirectNoticeInboxPage, error) {
	recipient := RecipientRef{Kind: RecipientUser, Ref: identity.principal.UserID.String()}
	candidates, truncated, err := m.readDirectNoticeInboxCandidateIDs(
		ctx, identity.scope, recipient, query.AfterDeliverySeq,
		directNoticeInboxCandidateBound,
	)
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}
	if truncated {
		return DirectNoticeInboxPage{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"direct notice inbox candidate work exceeds bound",
		)
	}
	if len(candidates) == 0 {
		return emptyDirectNoticeInboxPage(query), nil
	}
	batch, allowed, err := bindCommunicationInboxAuthorityBatch(ctx, identity, candidates)
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}
	if len(allowed) == 0 {
		return emptyDirectNoticeInboxPage(query), nil
	}
	reader, err := m.preflightDirectNoticeReaderIdentity(
		ctx, identity.scope, identity.principal, nil,
	)
	if err != nil {
		if errors.Is(err, errDirectNoticePrincipalNotFound) {
			return emptyDirectNoticeInboxPage(query), nil
		}
		return DirectNoticeInboxPage{}, err
	}
	window, err := directNoticeReaderAuthorityWindow(reader)
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}

	var authorized []directNoticeAuthorizedRead
	err = m.mutateCommunicationWithAuthorityBatch(
		ctx, identity.scope, identity, allowed, batch, window,
		func(
			tx *communicationTx,
			contexts []communicationRequestAuthorityBatchContext,
		) error {
			if len(contexts) != len(allowed) {
				return directNoticeReadUnknown(
					"direct notice inbox authority batch changed size", nil,
				)
			}
			preflights := make([]directNoticeReaderPreflight, len(contexts))
			allFacts := make([]store.AuthorizationFactRef, 0)
			for index, bound := range contexts {
				if bound.question != allowed[index].question ||
					bound.principal != identity.principal {
					return directNoticeReadUnknown(
						"direct notice inbox authority crossed its candidate", nil,
					)
				}
				preflight, preflightErr := directNoticeReaderPreflightWithCore(
					reader, bound.witness,
				)
				if preflightErr != nil {
					return preflightErr
				}
				preflights[index] = preflight
				allFacts = append(allFacts, preflight.Facts...)
			}
			facts, factErr := canonicalAuthorizationFactUnion(allFacts)
			if factErr != nil {
				return directNoticeReadUnknown(
					"direct notice inbox authority facts are unavailable", factErr,
				)
			}
			if err := tx.validateAuthorityFreshness(tx.now); err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}

			inputs := make([]directNoticeReadAuthorizationInput, 0, len(allowed))
			for index, candidate := range allowed {
				ids, resolveErr := resolveDirectNoticeInboxDeliveryIDsLocked(
					ctx, tx, reader, candidate.candidate,
				)
				if resolveErr != nil {
					if directNoticeReadIsHidden(resolveErr) {
						continue
					}
					return resolveErr
				}
				inputs = append(inputs, directNoticeReadAuthorizationInput{
					Preflight: preflights[index], IDs: ids,
				})
			}
			if len(inputs) == 0 {
				return tx.refreshNow(ctx)
			}
			prepared, prepareErr := prepareDirectNoticeReadBatchBounded(
				identity.scope, inputs, directNoticeInboxCandidateBound,
			)
			if prepareErr != nil {
				return prepareErr
			}
			var authorizeErr error
			authorized, authorizeErr = authorizeDirectNoticeReadBatchLocked(
				ctx, tx, identity.scope, inputs, prepared, true, query.Limit+1,
			)
			return authorizeErr
		},
	)
	if err != nil {
		return DirectNoticeInboxPage{}, normalizeDirectNoticeAuthorizationError(err)
	}

	visible := authorized
	if len(visible) > query.Limit {
		visible = visible[:query.Limit]
	}
	page := emptyDirectNoticeInboxPage(query)
	page.Items = make([]DirectNoticeReadResult, 0, len(visible))
	page.HasMore = len(authorized) > query.Limit
	for _, plan := range visible {
		result, openErr := m.openDirectNoticeRead(ctx, plan, opener)
		if openErr != nil {
			return DirectNoticeInboxPage{}, openErr
		}
		page.Items = append(page.Items, result)
		page.NextAfterDeliverySeq = result.Delivery.DeliverySeq
	}
	return page, nil
}

func resolveDirectNoticeInboxDeliveryIDsLocked(
	ctx context.Context,
	tx *communicationTx,
	reader directNoticeReaderIdentityPreflight,
	candidate directNoticeInboxCandidate,
) (directNoticeCarrierIDs, error) {
	if tx == nil || !validCanonicalCommunicationID(candidate.DeliveryID) ||
		candidate.DeliverySeq < 1 {
		return directNoticeCarrierIDs{}, directNoticeReadUnknown(
			"direct notice inbox candidate is malformed", nil,
		)
	}
	deliveryRepo, err := tx.repo(messageDeliveryKind)
	if err != nil {
		return directNoticeCarrierIDs{}, err
	}
	delivery, err := deliveryRepo.Get(ctx, candidate.DeliveryID)
	if err != nil {
		return directNoticeCarrierIDs{}, normalizeDirectNoticeLockedNotFound(err)
	}
	if delivery.String(colCommRecipientKind) != string(reader.Recipient.Kind) ||
		delivery.String(colCommRecipientRef) != reader.Recipient.Ref ||
		delivery.Int(colCommDeliverySeq) != candidate.DeliverySeq {
		return directNoticeCarrierIDs{}, directNoticeReadNotFound(
			"Delivery left the personal inbox binding",
		)
	}
	messageID, err := directNoticeRecordID(delivery, colCommMessageID)
	if err != nil {
		return directNoticeCarrierIDs{}, err
	}
	messageRepo, err := tx.repo(messageKind)
	if err != nil {
		return directNoticeCarrierIDs{}, err
	}
	message, err := messageRepo.Get(ctx, messageID)
	if err != nil {
		return directNoticeCarrierIDs{}, normalizeDirectNoticeLockedNotFound(err)
	}
	channelID, err := directNoticeRecordID(message, colCommChannelID)
	if err != nil {
		return directNoticeCarrierIDs{}, err
	}
	return directNoticeCarrierIDs{
		MessageID: messageID, DeliveryID: candidate.DeliveryID,
		ChannelID: channelID, DeliverySeq: candidate.DeliverySeq,
	}, nil
}
