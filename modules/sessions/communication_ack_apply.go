// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type directNoticeAckLockedState struct {
	ids           directNoticeCarrierIDs
	channel       Channel
	grants        []ChannelGrant
	message       Message
	deliveries    []MessageDelivery
	audiences     []MessageAudience
	contributions []MessageAudienceRecipient
	acks          []MessageAck
	tombstone     *store.DirectoryTombstoneWitness
	epoch         model.DirectoryEpoch
	requiredCount int64
}

type directNoticeAckEventAggregate struct {
	kind       model.Kind
	id         model.ID
	currentSeq int64
	nextSeq    int64
	workItem   model.Record
	workRepo   store.TransactionStampedGenericRepo
}

type directNoticeAckAuthorityLock struct {
	consume func(
		*communicationTx,
		directNoticeAckPreflight,
		directNoticeReaderPreflight,
	) bool
}

const (
	directNoticeAckAuthorityPreflightDomain = "olivares.sessions.direct-notice-ack.authority-preflight.v1"
	directNoticeAckReaderIdentityDomain     = "olivares.sessions.direct-notice-ack.reader-identity.v1"
)

type directNoticeAckAuthorityReaderCommitment struct {
	Scope               DirectoryScopeRef                     `json:"scope"`
	Principal           directNoticePublishAuthorityPrincipal `json:"principal"`
	Recipient           RecipientRef                          `json:"recipient"`
	Resolution          PrincipalResolution                   `json:"resolution"`
	ResolutionPrincipal directNoticePublishAuthorityPrincipal `json:"resolution_principal"`
	Closure             ChannelGrantSubjectClosure            `json:"closure"`
	ClosurePrincipal    directNoticePublishAuthorityPrincipal `json:"closure_principal"`
	Core                ReadWitness                           `json:"core"`
	CorePrincipal       directNoticePublishAuthorityPrincipal `json:"core_principal"`
	Facts               []store.AuthorizationFactRef          `json:"facts"`
}

type directNoticeAckAuthorityIdentity struct {
	BindingID    *communicationRequestAuthorityBindingID
	Scope        DirectoryScopeRef
	Principal    CommunicationPrincipal
	DeliveryID   model.ID
	CommandScope string
	IDs          directNoticeAckIDs
	Actor        [sha256.Size]byte
	Idempotency  [sha256.Size]byte
	Request      [sha256.Size]byte
	Reader       [sha256.Size]byte
}

type directNoticeAckReaderIdentityProjection struct {
	Scope               DirectoryScopeRef                     `json:"scope"`
	Principal           directNoticePublishAuthorityPrincipal `json:"principal"`
	Recipient           RecipientRef                          `json:"recipient"`
	Resolution          PrincipalResolution                   `json:"resolution"`
	ResolutionPrincipal directNoticePublishAuthorityPrincipal `json:"resolution_principal"`
	Closure             ChannelGrantSubjectClosure            `json:"closure"`
	ClosurePrincipal    directNoticePublishAuthorityPrincipal `json:"closure_principal"`
}

func validateDirectNoticeAckIdentityPreflight(
	identity directNoticeReaderIdentityPreflight,
	normalized directNoticeAckNormalizedCommand,
) error {
	resolution := identity.Resolution
	closure := identity.Closure
	if identity.Scope != normalized.scope || identity.Principal != normalized.principal ||
		identity.Recipient != (RecipientRef{
			Kind: RecipientUser, Ref: normalized.principal.UserID.String(),
		}) || ValidateCommunicationPrincipalForScope(identity.Principal, identity.Scope) != nil ||
		ValidatePrincipalResolution(resolution) != nil ||
		resolution.Outcome != PrincipalResolved || resolution.Scope != identity.Scope ||
		resolution.Principal != identity.Principal || resolution.Recipient == nil ||
		resolution.Recipient.Recipient != identity.Recipient ||
		closure.Scope != identity.Scope || closure.Principal != identity.Principal ||
		closure.DirectoryEpoch != resolution.Recipient.DirectoryEpoch ||
		!closure.Outcome.Valid() || closure.Outcome == ReadUnknown ||
		!boundedToken(closure.Code, 128) || !validateOpaqueRef(closure.EvidenceRef) ||
		closure.ObservedAt.IsZero() || !closure.FreshUntil.After(closure.ObservedAt) ||
		len(closure.Subjects) > directNoticeReadSetBound {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack reader identity is stale or malformed",
		)
	}
	if _, err := directNoticeReaderAuthorityWindow(identity); err != nil {
		return err
	}
	seen := make(map[CommunicationSubjectRef]struct{}, len(closure.Subjects))
	for _, subject := range closure.Subjects {
		if subject.Validate() != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"DirectNotice Ack reader closure contains an invalid subject",
			)
		}
		if _, duplicate := seen[subject]; duplicate {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"DirectNotice Ack reader closure contains duplicate subjects",
			)
		}
		seen[subject] = struct{}{}
	}
	return nil
}

func directNoticeAckReaderIdentityCommitment(
	identity directNoticeReaderIdentityPreflight,
	normalized directNoticeAckNormalizedCommand,
) ([sha256.Size]byte, error) {
	if err := validateDirectNoticeAckIdentityPreflight(identity, normalized); err != nil {
		return [sha256.Size]byte{}, err
	}
	raw, err := canonicalJSON(directNoticeAckReaderIdentityProjection{
		Scope:     identity.Scope,
		Principal: directNoticePublishAuthorityPrincipalFrom(identity.Principal),
		Recipient: identity.Recipient, Resolution: identity.Resolution,
		ResolutionPrincipal: directNoticePublishAuthorityPrincipalFrom(
			identity.Resolution.Principal,
		),
		Closure:          identity.Closure,
		ClosurePrincipal: directNoticePublishAuthorityPrincipalFrom(identity.Closure.Principal),
	})
	if err != nil {
		return [sha256.Size]byte{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack reader identity cannot be committed",
		)
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, directNoticeAckReaderIdentityDomain)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(raw)
	var commitment [sha256.Size]byte
	copy(commitment[:], hash.Sum(nil))
	return commitment, nil
}

func directNoticeAckAuthorityReaderProjection(
	reader directNoticeReaderPreflight,
	facts []store.AuthorizationFactRef,
) directNoticeAckAuthorityReaderCommitment {
	return directNoticeAckAuthorityReaderCommitment{
		Scope:     reader.Scope,
		Principal: directNoticePublishAuthorityPrincipalFrom(reader.Principal),
		Recipient: reader.Recipient, Resolution: reader.Resolution,
		ResolutionPrincipal: directNoticePublishAuthorityPrincipalFrom(
			reader.Resolution.Principal,
		),
		Closure:          reader.Closure,
		ClosurePrincipal: directNoticePublishAuthorityPrincipalFrom(reader.Closure.Principal),
		Core:             reader.Core,
		CorePrincipal:    directNoticePublishAuthorityPrincipalFrom(reader.Core.Principal),
		Facts:            append([]store.AuthorizationFactRef(nil), facts...),
	}
}

func directNoticeAckAuthorityIdentityFor(
	preflight directNoticeAckPreflight,
	reader directNoticeReaderPreflight,
) (directNoticeAckAuthorityIdentity, []store.AuthorizationFactRef, error) {
	wantReaderEntity := EntityRef{
		TenantID: preflight.normalized.scope.TenantID, Kind: messageDeliveryKind,
		ID:          preflight.normalized.deliveryID,
		WorkspaceID: preflight.normalized.scope.WorkspaceID,
	}
	identityCommitment, identityErr := directNoticeAckReaderIdentityCommitment(
		preflight.identity, preflight.normalized,
	)
	if preflight.bindingID == nil || identityErr != nil ||
		identityCommitment != preflight.identityCommitment ||
		ValidateReadWitness(preflight.core) != nil || preflight.core.Outcome != ReadAllow ||
		reader.Scope != preflight.normalized.scope ||
		reader.Principal != preflight.normalized.principal ||
		reader.Core.Entity != wantReaderEntity ||
		reader.Core.Operation != CommunicationDeliveryWrite ||
		reader.Core.Principal != preflight.normalized.principal ||
		reader.Core.Principal != preflight.core.Principal ||
		!canonicalCommunicationValueEqual(reader.Core, preflight.core) {
		return directNoticeAckAuthorityIdentity{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack authority snapshot crossed its request binding",
		)
	}
	expectedReader, err := directNoticeReaderPreflightWithCore(preflight.identity, preflight.core)
	if err != nil {
		return directNoticeAckAuthorityIdentity{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack reader authority cannot be reconstructed",
		)
	}
	wantNormalized, err := normalizeDirectNoticeDeliveryAckCommand(
		preflight.normalized.scope, preflight.normalized.principal,
		preflight.normalized.deliveryID, preflight.normalized.command,
	)
	if err == nil {
		wantNormalized, err = bindDirectNoticeAckCarrier(
			wantNormalized, preflight.normalized.carrier,
		)
	}
	if err != nil || !equalDirectNoticeAckNormalized(wantNormalized, preflight.normalized) ||
		len(preflight.normalized.actorFingerprint) != sha256.Size ||
		len(preflight.normalized.idempotencyKeyHash) != sha256.Size ||
		len(preflight.normalized.requestDigest) != sha256.Size {
		return directNoticeAckAuthorityIdentity{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack authority request is not canonically normalized",
		)
	}
	ids := [...]model.ID{
		preflight.ids.Ack, preflight.ids.Command, preflight.ids.Event, preflight.ids.Receipt,
	}
	for index, id := range ids {
		if !validCanonicalCommunicationID(id) {
			return directNoticeAckAuthorityIdentity{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"DirectNotice Ack authority IDs are unavailable",
			)
		}
		for prior := range index {
			if id == ids[prior] {
				return directNoticeAckAuthorityIdentity{}, nil, communicationError(
					ErrCommunicationEvidenceUnknown,
					"DirectNotice Ack authority IDs are not unique",
				)
			}
		}
	}
	facts, err := CanonicalAuthorizationFacts(reader.Facts)
	expectedFacts, expectedErr := CanonicalAuthorizationFacts(expectedReader.Facts)
	if err != nil || expectedErr != nil || !equalDirectNoticeAuthorityFacts(facts, reader.Facts) ||
		!equalDirectNoticeAuthorityFacts(facts, expectedFacts) {
		return directNoticeAckAuthorityIdentity{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack authority facts are not canonical",
		)
	}
	projection := directNoticeAckAuthorityReaderProjection(reader, facts)
	raw, err := canonicalJSON(projection)
	expectedRaw, expectedErr := canonicalJSON(
		directNoticeAckAuthorityReaderProjection(expectedReader, expectedFacts),
	)
	if err != nil || expectedErr != nil || !bytes.Equal(raw, expectedRaw) {
		return directNoticeAckAuthorityIdentity{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack authority preflight cannot be committed",
		)
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, directNoticeAckAuthorityPreflightDomain)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(raw)
	identity := directNoticeAckAuthorityIdentity{
		BindingID: preflight.bindingID, Scope: preflight.normalized.scope,
		Principal: preflight.normalized.principal, DeliveryID: preflight.normalized.deliveryID,
		CommandScope: preflight.normalized.commandScope, IDs: preflight.ids,
	}
	copy(identity.Actor[:], preflight.normalized.actorFingerprint)
	copy(identity.Idempotency[:], preflight.normalized.idempotencyKeyHash)
	copy(identity.Request[:], preflight.normalized.requestDigest)
	copy(identity.Reader[:], hash.Sum(nil))
	return identity, facts, nil
}

func lockDirectNoticeAckAuthoritySnapshot(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticeAckPreflight,
	reader directNoticeReaderPreflight,
) (directNoticeAckAuthorityLock, error) {
	if tx == nil || preflight.bindingID == nil || preflight.bindingID != tx.requestBindingID {
		return directNoticeAckAuthorityLock{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack authority snapshot crossed its request binding",
		)
	}
	localWindow, err := directNoticeReaderAuthorityWindow(preflight.identity)
	if err != nil {
		return directNoticeAckAuthorityLock{}, err
	}
	expectedRequest, err := (communicationRequestAuthoritySnapshot{
		facts:      append([]store.AuthorizationFactRef(nil), preflight.core.Facts...),
		observedAt: preflight.core.ObservedAt,
		freshUntil: preflight.core.FreshUntil,
		bindingID:  preflight.bindingID,
	}).narrowTo(localWindow)
	if err != nil || len(tx.claimAuthorityFacts) != 0 ||
		tx.requestBindingID != expectedRequest.bindingID ||
		!equalDirectNoticeAuthorityFacts(tx.requestAuthorityFacts, expectedRequest.facts) ||
		!tx.requestObservedAt.Equal(expectedRequest.observedAt) ||
		!tx.requestFreshUntil.Equal(expectedRequest.freshUntil) {
		return directNoticeAckAuthorityLock{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack transaction carries a different authority window",
		)
	}
	authorityIdentity, facts, err := directNoticeAckAuthorityIdentityFor(preflight, reader)
	if err != nil {
		return directNoticeAckAuthorityLock{}, err
	}
	if err := tx.lockAuthoritySnapshot(ctx, facts); err != nil {
		return directNoticeAckAuthorityLock{}, normalizeDirectNoticeAuthorityLockError(err)
	}
	sealedTx := tx
	sealedIdentity := authorityIdentity
	sealedFacts := append([]store.AuthorizationFactRef(nil), facts...)
	var consumed atomic.Bool
	return directNoticeAckAuthorityLock{consume: func(
		candidateTx *communicationTx,
		candidatePreflight directNoticeAckPreflight,
		candidateReader directNoticeReaderPreflight,
	) bool {
		candidateIdentity, candidateFacts, err := directNoticeAckAuthorityIdentityFor(
			candidatePreflight, candidateReader,
		)
		return err == nil && candidateTx == sealedTx &&
			candidatePreflight.bindingID == candidateTx.requestBindingID &&
			candidateIdentity == sealedIdentity &&
			equalDirectNoticeAuthorityFacts(candidateFacts, sealedFacts) &&
			consumed.CompareAndSwap(false, true)
	}}, nil
}

func equalDirectNoticeAckNormalized(
	left directNoticeAckNormalizedCommand,
	right directNoticeAckNormalizedCommand,
) bool {
	return left.command == right.command && left.scope == right.scope &&
		left.principal == right.principal && left.deliveryID == right.deliveryID &&
		left.expectedVersion == right.expectedVersion && left.method == right.method &&
		left.path == right.path && left.commandScope == right.commandScope &&
		left.carrier == right.carrier &&
		bytes.Equal(left.actorFingerprint, right.actorFingerprint) &&
		bytes.Equal(left.idempotencyKeyHash, right.idempotencyKeyHash) &&
		bytes.Equal(left.requestDigest, right.requestDigest)
}

func lockDirectNoticeAckState(
	ctx context.Context,
	tx *communicationTx,
	normalized directNoticeAckNormalizedCommand,
	preflight directNoticeReaderPreflight,
) (directNoticeAckLockedState, error) {
	if tx == nil {
		return directNoticeAckLockedState{},
			communicationTransactionUnavailable("DirectNotice Ack transaction", nil)
	}
	deliveryRepo, err := tx.repo(messageDeliveryKind)
	if err != nil {
		return directNoticeAckLockedState{}, err
	}
	deliveryObservation, err := deliveryRepo.Get(ctx, normalized.deliveryID)
	if err != nil {
		return directNoticeAckLockedState{}, normalizeDirectNoticeLockedNotFound(err)
	}
	messageID, err := directNoticeRecordID(deliveryObservation, colCommMessageID)
	if err != nil {
		return directNoticeAckLockedState{}, err
	}
	deliverySeq := deliveryObservation.Int(colCommDeliverySeq)
	if deliverySeq < 1 {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack Delivery sequence is unavailable", nil,
		)
	}
	messageRepo, err := tx.repo(messageKind)
	if err != nil {
		return directNoticeAckLockedState{}, err
	}
	messageObservation, err := messageRepo.Get(ctx, messageID)
	if err != nil {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack Message observation is unavailable", err,
		)
	}
	channelID, err := directNoticeRecordID(messageObservation, colCommChannelID)
	if err != nil {
		return directNoticeAckLockedState{}, err
	}
	ids := directNoticeCarrierIDs{
		MessageID: messageID, DeliveryID: normalized.deliveryID,
		ChannelID: channelID, DeliverySeq: deliverySeq,
	}
	if err := tx.lockTransaction(
		ctx, directNoticeMessageLockKey(normalized.scope, messageID),
	); err != nil {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack Message lock is unavailable", err,
		)
	}
	channelRecord, err := tx.lockRecord(ctx, channelKind, channelID)
	if err != nil {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack Channel lock is unavailable", err,
		)
	}
	channel, err := channelFromRecord(channelRecord)
	if err != nil || channel.ID != channelID || channel.TenantID != normalized.scope.TenantID ||
		channel.WorkspaceID != normalized.scope.WorkspaceID {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack locked Channel is malformed or unsupported", err,
		)
	}
	grants, err := lockCurrentChannelGrants(ctx, tx, channel.ID)
	if err != nil {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack ChannelGrant snapshot is unavailable", err,
		)
	}
	messageRecord, err := tx.lockRecord(ctx, messageKind, messageID)
	if err != nil {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack Message lock is unavailable", err,
		)
	}
	deliveryRecords, err := lockDirectNoticeRecordSet(
		ctx, tx, messageDeliveryKind,
		[]model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: messageID.String()}},
		directNoticeReadSetBound,
	)
	if err != nil {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack Delivery set is unavailable", err,
		)
	}
	deliveries := make([]MessageDelivery, 0, len(deliveryRecords))
	requiredCount := int64(0)
	for _, record := range deliveryRecords {
		delivery, decodeErr := messageDeliveryFromRecord(record)
		if decodeErr != nil || delivery.MessageID != messageID {
			return directNoticeAckLockedState{}, directNoticeReadUnknown(
				"DirectNotice Ack locked Delivery set is malformed", decodeErr,
			)
		}
		if delivery.Required {
			requiredCount++
		}
		deliveries = append(deliveries, delivery)
	}
	message, err := messageFromRecord(messageRecord, requiredCount)
	if err != nil {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack locked Message is malformed", err,
		)
	}
	if !directNoticeAckHistoricalStorageCarrier(channel, message) {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack supports only historical storage-protected carriers", nil,
		)
	}
	audienceRecords, err := lockDirectNoticeRecordSet(
		ctx, tx, messageAudienceKind,
		[]model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: messageID.String()}},
		64,
	)
	if err != nil {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack Audience set is unavailable", err,
		)
	}
	audiences := make([]MessageAudience, 0, len(audienceRecords))
	for _, record := range audienceRecords {
		audience, decodeErr := messageAudienceFromRecord(record)
		if decodeErr != nil || audience.MessageID != messageID {
			return directNoticeAckLockedState{}, directNoticeReadUnknown(
				"DirectNotice Ack locked Audience set is malformed", decodeErr,
			)
		}
		audiences = append(audiences, audience)
	}
	contributionRecords, err := lockDirectNoticeContributionSet(ctx, tx, audiences)
	if err != nil {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack audience contribution set is unavailable", err,
		)
	}
	contributions := make([]MessageAudienceRecipient, 0, len(contributionRecords))
	for _, record := range contributionRecords {
		contribution, decodeErr := messageAudienceRecipientFromRecord(record)
		if decodeErr != nil {
			return directNoticeAckLockedState{}, directNoticeReadUnknown(
				"DirectNotice Ack locked contribution set is malformed", decodeErr,
			)
		}
		contributions = append(contributions, contribution)
	}
	targetDelivery, err := exactDirectNoticeAckCarrierGraph(
		normalized, preflight, ids, channel, message, deliveries, audiences, contributions,
	)
	if err != nil {
		return directNoticeAckLockedState{}, err
	}
	var tombstone *store.DirectoryTombstoneWitness
	if targetDelivery.State == DeliveryUndeliverable {
		principalID, parseErr := model.ParseID(targetDelivery.Recipient.Ref)
		if parseErr != nil || targetDelivery.Recipient.Kind != RecipientUser {
			return directNoticeAckLockedState{}, directNoticeReadUnknown(
				"undeliverable DirectNotice recipient is non-canonical", parseErr,
			)
		}
		witness, found, readErr := tx.directorySnapshotReader().ReadDirectoryTombstone(
			ctx,
			store.DirectoryPrincipalRef{
				PrincipalKind: model.DirectoryPrincipalUser, PrincipalRef: principalID,
			},
		)
		if readErr != nil || !found {
			return directNoticeAckLockedState{}, directNoticeReadUnknown(
				"undeliverable DirectNotice tombstone evidence is unavailable", readErr,
			)
		}
		tombstone = &witness
	}
	ackRecords, err := lockDirectNoticeRecordSet(
		ctx, tx, messageAckKind,
		[]model.Filter{{Column: colCommDeliveryID, Op: model.OpEq, Value: normalized.deliveryID.String()}},
		1,
	)
	if err != nil {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack evidence set is unavailable", err,
		)
	}
	acks := make([]MessageAck, 0, len(ackRecords))
	for _, record := range ackRecords {
		ack, decodeErr := messageAckFromRecord(record)
		if decodeErr != nil || ack.DeliveryID != normalized.deliveryID ||
			ack.TenantID != normalized.scope.TenantID || ack.WorkspaceID != normalized.scope.WorkspaceID {
			return directNoticeAckLockedState{}, directNoticeReadUnknown(
				"DirectNotice Ack evidence set is malformed", decodeErr,
			)
		}
		acks = append(acks, ack)
	}
	epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
	if err != nil || epoch.Validate() != nil || epoch.TenantID != normalized.scope.TenantID ||
		preflight.Resolution.Recipient == nil ||
		epoch.Version != preflight.Resolution.Recipient.DirectoryEpoch {
		return directNoticeAckLockedState{}, directNoticeReadUnknown(
			"DirectNotice Ack locked directory epoch is unavailable", err,
		)
	}
	return directNoticeAckLockedState{
		ids: ids, channel: channel, grants: grants, message: message,
		deliveries: deliveries, audiences: audiences, contributions: contributions,
		acks: acks, tombstone: tombstone, epoch: epoch, requiredCount: requiredCount,
	}, nil
}

// directNoticeAckHistoricalStorageCarrier binds Ack to the Message's immutable
// creation protection instead of reopening it against the Channel's current
// generation. Sensitivity changes may advance a still-storage Channel, while
// the irreversible storage->application_sealed transition must place every
// historical plain generation strictly before the current one.
func directNoticeAckHistoricalStorageCarrier(channel Channel, message Message) bool {
	if message.Payload.Encoding != PayloadPlainJSON ||
		message.Payload.ProtectionGeneration < 1 || channel.ProtectionGeneration < 1 {
		return false
	}
	switch channel.ContentProtection {
	case ContentProtectionStorage:
		return message.Payload.ProtectionGeneration <= channel.ProtectionGeneration
	case ContentProtectionApplicationSealed:
		return message.Payload.ProtectionGeneration < channel.ProtectionGeneration
	default:
		return false
	}
}

func exactDirectNoticeAckCarrierGraph(
	normalized directNoticeAckNormalizedCommand,
	preflight directNoticeReaderPreflight,
	ids directNoticeCarrierIDs,
	channel Channel,
	message Message,
	deliveries []MessageDelivery,
	audiences []MessageAudience,
	contributions []MessageAudienceRecipient,
) (MessageDelivery, error) {
	if normalized.carrier.class == "" {
		return exactDirectNoticeReadGraph(
			preflight, ids, channel, message, deliveries, audiences, contributions,
		)
	}
	selector := normalized.carrier
	if selector.class != directNoticeAckCarrierWorkflowWorkTask ||
		selector.validate(normalized.scope, normalized.principal) != nil ||
		message.Kind != MessageWorkTask || message.WorkItemID != selector.workItemID ||
		message.ChannelID != selector.channelID || channel.ID != selector.channelID ||
		preflight.Recipient != (RecipientRef{Kind: RecipientUser, Ref: selector.userID.String()}) {
		return MessageDelivery{}, directNoticeReadNotFound(
			"carrier is not the selected workflow WorkTask",
		)
	}
	// The direct-user audience graph is identical. Project only the two
	// immutable Message fields that distinguish a WorkTask, after binding them
	// to the private selector, so the existing exact graph remains normative.
	projected := message
	projected.Kind = MessageNotice
	projected.WorkItemID = ""
	return exactDirectNoticeReadGraph(
		preflight, ids, channel, projected, deliveries, audiences, contributions,
	)
}

func lockDirectNoticeAckEventAggregate(
	ctx context.Context,
	tx *communicationTx,
	sc store.Scope,
	normalized directNoticeAckNormalizedCommand,
	locked directNoticeAckLockedState,
) (directNoticeAckEventAggregate, error) {
	if normalized.carrier.class == "" {
		if locked.message.Version == math.MaxInt64 || locked.message.LastEventSeq == math.MaxInt64 ||
			locked.message.Version != locked.message.LastEventSeq+1 {
			return directNoticeAckEventAggregate{}, communicationError(
				ErrCommunicationEvidenceUnknown, "DirectNotice Message event sequence is unavailable",
			)
		}
		return directNoticeAckEventAggregate{
			kind: messageKind, id: locked.message.ID,
			currentSeq: locked.message.LastEventSeq, nextSeq: locked.message.LastEventSeq + 1,
		}, nil
	}
	selector := normalized.carrier
	if selector.class != directNoticeAckCarrierWorkflowWorkTask || sc == nil || tx == nil ||
		locked.message.WorkItemID != selector.workItemID {
		return directNoticeAckEventAggregate{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow WorkTask Ack aggregate is unavailable",
		)
	}
	repo, err := lifecycleWorkItemRepository(sc)
	if err != nil {
		return directNoticeAckEventAggregate{}, err
	}
	locker, ok := repo.(store.RowLocker[model.Record])
	if !ok {
		return directNoticeAckEventAggregate{}, communicationTransactionUnavailable(
			"workflow WorkTask Ack WorkItem row lock", nil,
		)
	}
	var item model.Record
	err = runCommunicationBoundAuthorityLocalLock(tx.boundAuthorityState, func() error {
		var lockErr error
		item, lockErr = locker.Lock(ctx, selector.workItemID)
		return lockErr
	})
	if err != nil {
		return directNoticeAckEventAggregate{}, err
	}
	currentSeq := item.Int(colWorkLastEventSeq)
	if recordID(item) != selector.workItemID ||
		item.String(colWorkWorkspaceID) != normalized.scope.WorkspaceID.String() ||
		item.Int(model.ColVersion) < 1 || currentSeq < 1 || currentSeq == math.MaxInt64 {
		return directNoticeAckEventAggregate{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow WorkTask Ack WorkItem changed lineage",
		)
	}
	return directNoticeAckEventAggregate{
		kind: workItemKind, id: selector.workItemID,
		currentSeq: currentSeq, nextSeq: currentSeq + 1,
		workItem: cloneMessageLifecycleRecord(item), workRepo: repo,
	}, nil
}

func directNoticeAckAuthorityEvidence(
	tx *communicationTx,
	normalized directNoticeAckNormalizedCommand,
	preflight directNoticeReaderPreflight,
	locked directNoticeAckLockedState,
) (ReadGateEvidence, ProtectedReadDecision, error) {
	dbNow := tx.now.Time()
	if preflight.Core.Operation != CommunicationDeliveryWrite ||
		preflight.Core.Entity != (EntityRef{
			TenantID: preflight.Scope.TenantID, Kind: messageDeliveryKind,
			ID: locked.ids.DeliveryID, WorkspaceID: preflight.Scope.WorkspaceID,
		}) || directNoticeReadRowsCarryFutureDBTime(
		locked.channel, locked.grants, locked.message, locked.deliveries,
		locked.audiences, locked.contributions, dbNow,
	) || !communicationEvidenceCurrent(
		preflight.Core.ObservedAt, preflight.Core.FreshUntil, dbNow,
	) || !communicationEvidenceCurrent(
		preflight.Resolution.ObservedAt, preflight.Resolution.FreshUntil, dbNow,
	) || !communicationEvidenceCurrent(
		preflight.Closure.ObservedAt, preflight.Closure.FreshUntil, dbNow,
	) {
		return ReadGateEvidence{}, ProtectedReadDecision{}, directNoticeReadUnknown(
			"DirectNotice Ack authority expired while waiting for locks", nil,
		)
	}
	delivery, err := exactDirectNoticeAckCarrierGraph(
		normalized, preflight, locked.ids, locked.channel, locked.message, locked.deliveries,
		locked.audiences, locked.contributions,
	)
	if err != nil {
		return ReadGateEvidence{}, ProtectedReadDecision{}, err
	}
	grantEvidence := EvaluateCurrentChannelGrant(
		ChannelGrantSnapshot{
			Verdict: VerdictClean, Code: "channel_grants_locked",
			ACLRevision: locked.channel.ACLRevision, ObservedAt: dbNow,
			Grants: locked.grants,
		},
		preflight.Scope.TenantID, preflight.Scope.WorkspaceID, locked.channel.ID,
		preflight.Closure, ChannelGrantRead, dbNow,
	)
	directoryFact := store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: model.ID(preflight.Scope.TenantID),
		Version: locked.epoch.Version,
	}
	carrier := ProtectedCarrierRef{
		Entity: preflight.Core.Entity, ChannelID: locked.channel.ID,
		MessageID: locked.message.ID, DeliveryID: delivery.ID,
	}
	currentAudience, err := buildDirectNoticeCurrentAudience(
		preflight, locked.message, delivery, locked.audiences, locked.contributions, dbNow,
	)
	if err != nil {
		return ReadGateEvidence{}, ProtectedReadDecision{}, err
	}
	clean := func(code, ref string) AuthorityEvidence {
		return AuthorityEvidence{Verdict: VerdictClean, Code: code, EvidenceRef: ref}
	}
	evidence := ReadGateEvidence{
		Scope: preflight.Scope, ChannelID: locked.channel.ID,
		ChannelACLRevision: locked.channel.ACLRevision, DBNow: dbNow,
		Operation: CommunicationDeliveryWrite, Carrier: carrier,
		CarrierState: ProtectedCarrierSnapshot{
			Message: locked.message, Delivery: delivery,
			RequiredDeliveryCount: locked.requiredCount, ObservedAt: dbNow,
			Evidence: clean("carrier_rows_locked", "same_tx:direct_notice_ack_carrier"),
		},
		Core: preflight.Core, Principal: preflight.Principal,
		PrincipalResolution: preflight.Resolution, Recipient: preflight.Recipient,
		DirectoryEpoch: directoryFact, CurrentChannelGrant: grantEvidence,
		EntityRecipientGuard: BoundEntityRecipientEvidence{
			Scope: preflight.Scope, Carrier: carrier, Principal: preflight.Principal,
			Recipient: preflight.Recipient, DirectoryEpoch: locked.epoch.Version,
			EvaluatedAt: dbNow,
			Evidence:    clean("entity_recipient_current", "same_tx:direct_notice_ack_recipient"),
		},
		CurrentAudience: currentAudience,
	}
	decision, err := EvaluateCarrierGate(evidence)
	if err != nil {
		return ReadGateEvidence{}, ProtectedReadDecision{}, err
	}
	switch decision.Verdict {
	case VerdictUnknown:
		return ReadGateEvidence{}, ProtectedReadDecision{}, directNoticeReadUnknown(
			"DirectNotice Ack carrier gate is unavailable", nil,
		)
	case VerdictBroken:
		return ReadGateEvidence{}, ProtectedReadDecision{}, communicationError(
			ErrCommunicationForbidden, "DirectNotice Ack carrier authority denied",
		)
	case VerdictClean:
	default:
		return ReadGateEvidence{}, ProtectedReadDecision{}, directNoticeReadUnknown(
			"DirectNotice Ack carrier gate has no verdict", nil,
		)
	}
	if len(decision.RequiredClaims) != 0 ||
		len(decision.SurvivingContributionIDs) != 1 ||
		decision.SurvivingContributionIDs[0] != locked.contributions[0].ID ||
		!equalDirectNoticeAuthorityFacts(preflight.Facts, decision.Facts) {
		return ReadGateEvidence{}, ProtectedReadDecision{}, directNoticeReadUnknown(
			"Direct User Ack returned non-direct authority effects", nil,
		)
	}
	grantFreshUntil, constrained, err := directNoticeReadGrantFreshUntil(
		locked.grants, preflight.Closure, dbNow,
	)
	if err != nil {
		return ReadGateEvidence{}, ProtectedReadDecision{}, err
	}
	if constrained {
		if err := tx.narrowRequestAuthorityFreshUntil(grantFreshUntil); err != nil {
			return ReadGateEvidence{}, ProtectedReadDecision{}, err
		}
	}
	return evidence, decision, nil
}

func applyDirectNoticeDeliveryAck(
	ctx context.Context,
	tx *communicationTx,
	workflowScope store.Scope,
	preflight directNoticeAckPreflight,
	readerPreflight directNoticeReaderPreflight,
	authorityLock directNoticeAckAuthorityLock,
) (DirectNoticeDeliveryAckResult, error) {
	if tx == nil || preflight.bindingID == nil ||
		preflight.bindingID != tx.requestBindingID || authorityLock.consume == nil ||
		!authorityLock.consume(tx, preflight, readerPreflight) {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack apply crossed its request binding",
		)
	}
	locked, err := lockDirectNoticeAckState(ctx, tx, preflight.normalized, readerPreflight)
	if err != nil {
		return DirectNoticeDeliveryAckResult{},
			confirmDirectNoticeAckNegativeAfterRefresh(ctx, tx, err)
	}
	aggregate, err := lockDirectNoticeAckEventAggregate(
		ctx, tx, workflowScope, preflight.normalized, locked,
	)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	if err := tx.lockAuditAppends(ctx); err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	if err := tx.refreshNow(ctx); err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	evidence, decision, err := directNoticeAckAuthorityEvidence(
		tx, preflight.normalized, readerPreflight, locked,
	)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	if err := validateDirectNoticeAckLockedCarrierLifecycle(
		tx, locked, evidence.CarrierState.Delivery,
	); err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	if len(locked.acks) != 0 {
		if directNoticeAckMatchesLockedLifecycle(
			locked.acks[0], evidence.CarrierState.Delivery, locked.message, tx.now.Time(),
		) {
			return DirectNoticeDeliveryAckResult{}, errDirectNoticeAckAlreadyAcknowledged
		}
		return DirectNoticeDeliveryAckResult{}, directNoticeReadUnknown(
			"DirectNotice Delivery carries inconsistent Ack evidence", nil,
		)
	}
	if evidence.CarrierState.Delivery.State == DeliveryAcknowledged {
		return DirectNoticeDeliveryAckResult{}, directNoticeReadUnknown(
			"acknowledged DirectNotice Delivery lacks Ack evidence", nil,
		)
	}
	delivery := evidence.CarrierState.Delivery
	if delivery.Version != preflight.normalized.expectedVersion {
		return DirectNoticeDeliveryAckResult{}, errDirectNoticeAckVersionMismatch
	}
	actor := CommunicationActorRef{Kind: ActorUser, Ref: preflight.normalized.principal.UserID.String()}
	plan, err := PlanMessageAck(
		delivery, preflight.ids.Ack, actor, nil, &evidence, nil, tx.now.Time(),
	)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidCommunicationTransition):
			return DirectNoticeDeliveryAckResult{}, communicationError(
				ErrCommunicationTerminal,
				"DirectNotice Delivery cannot be acknowledged in its current lifecycle",
			)
		case errors.Is(err, ErrCommunicationEvidenceUnknown),
			errors.Is(err, ErrCommunicationForbidden),
			errors.Is(err, ErrCommunicationTerminal):
			return DirectNoticeDeliveryAckResult{}, err
		default:
			return DirectNoticeDeliveryAckResult{}, directNoticeReadUnknown(
				"DirectNotice Ack plan is unavailable", err,
			)
		}
	}
	if plan.LinksEffectiveAck {
		if deadline, constrained := directNoticeAckEffectiveDeadline(plan.Before); constrained {
			if err := tx.narrowRequestAuthorityFreshUntil(deadline); err != nil {
				return DirectNoticeDeliveryAckResult{}, err
			}
		}
	}
	if plan.Before.ID != preflight.normalized.deliveryID || plan.Ack.ID != preflight.ids.Ack ||
		plan.Ack.Actor != actor || plan.Ack.OnBehalfOf != nil || plan.Ack.Note != nil ||
		plan.Authority.Verdict != VerdictClean ||
		!equalDirectNoticeAuthorityFacts(plan.Authority.Facts, decision.Facts) ||
		(plan.LinksEffectiveAck == plan.Ack.Late) ||
		(plan.MaterializesExpiry && !plan.Ack.Late) {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack planner returned a widened shape",
		)
	}
	messageAfter := locked.message
	if preflight.normalized.carrier.class == "" {
		messageAfter.Version++
		messageAfter.LastEventSeq++
		messageAfter.UpdatedAt = tx.now.Time()
	}
	if err := ValidateMessage(messageAfter, locked.requiredCount); err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	if !directNoticeAckMatchesLockedLifecycle(
		plan.Ack, plan.After, messageAfter, tx.now.Time(),
	) {
		return DirectNoticeDeliveryAckResult{}, directNoticeReadUnknown(
			"DirectNotice Ack planner returned inconsistent lifecycle evidence", nil,
		)
	}
	fulfillment, err := projectDirectNoticeAckFulfillment(
		messageAfter, []MessageDelivery{plan.After}, locked.requiredCount, tx.now.Time(),
	)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	planHash, err := canonicalDirectNoticeAckPlanHash(
		preflight, locked, plan, messageAfter, aggregate, fulfillment,
	)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	result := DirectNoticeDeliveryAckResult{
		CommandID: preflight.ids.Command, AckID: plan.Ack.ID,
		DeliveryID: plan.After.ID, MessageID: messageAfter.ID, EventID: preflight.ids.Event,
		Version: plan.After.Version, ETag: fmt.Sprintf("\"v%d\"", plan.After.Version),
		State: plan.After.State, Late: plan.Ack.Late, Fulfillment: fulfillment,
		messageEventSeq: aggregate.nextSeq,
	}
	applyCommitment, err := directNoticeAckApplyCommitmentFromResult(
		preflight, planHash, result, result.messageEventSeq, tx.now.Time(),
	)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	audit, err := tx.appendAudit(ctx, model.AuditDraft{
		Actor: directNoticeActor(preflight.normalized.principal), ActorKind: model.ActorUser,
		Action: directNoticeAckAuditAction, TargetKind: communicationCommandKind,
		TargetID: preflight.ids.Command, PayloadHash: append([]byte(nil), planHash...),
		Meta: map[string]any{
			"workspace_id":  preflight.normalized.scope.WorkspaceID.String(),
			"command_scope": preflight.normalized.commandScope,
			"delivery_id":   plan.After.ID.String(), "message_id": messageAfter.ID.String(),
			"ack_id": plan.Ack.ID.String(), "late": plan.Ack.Late,
			"message_event_seq":        result.messageEventSeq,
			"apply_commitment_version": directNoticeAckApplyCommitmentV1,
			"apply_commitment":         hex.EncodeToString(applyCommitment),
		},
	})
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	if audit.Seq < 1 || len(audit.Hash) != sha256.Size {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack audit append returned no durable anchor",
		)
	}
	result.AuditSeq = audit.Seq
	if err := persistDirectNoticeDeliveryAck(
		ctx, tx, preflight, locked, plan, messageAfter, aggregate, planHash, audit, result,
	); err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	return result, nil
}

func confirmDirectNoticeAckNegativeAfterRefresh(
	ctx context.Context,
	tx *communicationTx,
	candidate error,
) error {
	if errors.Is(candidate, ErrCommunicationEvidenceUnknown) {
		return candidate
	}
	known := errors.Is(candidate, ErrCommunicationNotFound) ||
		errors.Is(candidate, ErrCommunicationForbidden)
	if !known {
		if errors.Is(candidate, store.ErrConflict) {
			return directNoticeReadUnknown(
				"DirectNotice Ack carrier changed while locking", candidate,
			)
		}
		return candidate
	}
	if err := tx.refreshNow(ctx); err != nil {
		return directNoticeReadUnknown(
			"DirectNotice Ack negative could not be confirmed at fresh DB time", err,
		)
	}
	return candidate
}

func validateDirectNoticeAckLockedCarrierLifecycle(
	tx *communicationTx,
	locked directNoticeAckLockedState,
	delivery MessageDelivery,
) error {
	if tx == nil || delivery.ID != locked.ids.DeliveryID || delivery.MessageID != locked.message.ID ||
		len(locked.acks) > 1 ||
		(locked.message.State != MessagePublished && delivery.State == DeliveryAvailable) {
		return directNoticeReadUnknown(
			"DirectNotice Ack carrier lifecycle is unavailable", nil,
		)
	}
	var ack *MessageAck
	if len(locked.acks) == 1 {
		candidate := locked.acks[0]
		if ValidateMessageAck(candidate) != nil || candidate.DeliveryID != delivery.ID ||
			candidate.TenantID != delivery.TenantID ||
			candidate.WorkspaceID != delivery.WorkspaceID ||
			!directNoticeAckTargetsRecipient(candidate, delivery.Recipient) ||
			candidate.AcknowledgedAt.After(tx.now.Time()) {
			return directNoticeReadUnknown(
				"DirectNotice Ack carrier carries malformed Ack evidence", nil,
			)
		}
		ack = &candidate
	}
	switch delivery.State {
	case DeliveryAvailable:
		if ack != nil {
			return directNoticeReadUnknown(
				"available DirectNotice Delivery carries Ack evidence", nil,
			)
		}
		return nil
	case DeliveryAcknowledged:
		if ack == nil || ack.ID != delivery.AckID || ack.Late ||
			delivery.AcknowledgedAt == nil || !ack.AcknowledgedAt.Equal(*delivery.AcknowledgedAt) {
			return directNoticeReadUnknown(
				"acknowledged DirectNotice Delivery lacks exact Ack evidence", nil,
			)
		}
		return nil
	case DeliveryExpired:
		if !directNoticeDeliveryDeadlineElapsedAt(delivery, delivery.UpdatedAt) ||
			(ack != nil && (!ack.Late ||
				!directNoticeDeliveryDeadlineElapsedAt(delivery, ack.AcknowledgedAt))) {
			return directNoticeReadUnknown(
				"expired DirectNotice Delivery has impossible deadline evidence", nil,
			)
		}
		return nil
	case DeliveryRetracted:
		if locked.message.State != MessageRetracted || locked.message.TerminalAt == nil ||
			delivery.UpdatedAt.Before(*locked.message.TerminalAt) ||
			(ack != nil && (!ack.Late || ack.AcknowledgedAt.Before(*locked.message.TerminalAt))) {
			return directNoticeReadUnknown(
				"retracted DirectNotice Delivery lacks terminal lineage", nil,
			)
		}
		return nil
	case DeliveryUndeliverable:
		if ack != nil || delivery.Recipient.Kind != RecipientUser || locked.tombstone == nil {
			return directNoticeReadUnknown(
				"undeliverable DirectNotice Delivery carries unavailable evidence", nil,
			)
		}
		principalID, err := model.ParseID(delivery.Recipient.Ref)
		if err != nil {
			return directNoticeReadUnknown(
				"undeliverable DirectNotice recipient is non-canonical", nil,
			)
		}
		principalRef := store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalUser, PrincipalRef: principalID,
		}
		witness := *locked.tombstone
		if witness.Principal != principalRef ||
			witness.TombstoneKind != delivery.RetirementTombstoneKind ||
			witness.TombstoneID != delivery.RetirementTombstoneID ||
			witness.TombstoneVersion != delivery.RetirementTombstoneVersion ||
			witness.RetirementEpoch != delivery.RetirementEpoch ||
			validateUndeliverableWitness(delivery, witness) != nil {
			return directNoticeReadUnknown(
				"undeliverable DirectNotice tombstone evidence does not match", nil,
			)
		}
		return nil
	default:
		return directNoticeReadUnknown(
			"DirectNotice Ack carrier lifecycle is unavailable", nil,
		)
	}
}

func directNoticeAckEffectiveDeadline(delivery MessageDelivery) (time.Time, bool) {
	var deadline time.Time
	for _, candidate := range []*time.Time{delivery.AckDueAt, delivery.ExpiresAt} {
		if candidate != nil && (deadline.IsZero() || candidate.Before(deadline)) {
			deadline = candidate.UTC()
		}
	}
	return deadline, !deadline.IsZero()
}

func directNoticeAckMatchesLockedLifecycle(
	ack MessageAck,
	delivery MessageDelivery,
	message Message,
	dbNow time.Time,
) bool {
	if ValidateMessageAck(ack) != nil || ack.DeliveryID != delivery.ID ||
		ack.TenantID != delivery.TenantID || ack.WorkspaceID != delivery.WorkspaceID ||
		!directNoticeAckTargetsRecipient(ack, delivery.Recipient) || dbNow.IsZero() ||
		ack.AcknowledgedAt.After(dbNow) {
		return false
	}
	if !ack.Late {
		return delivery.State == DeliveryAcknowledged && delivery.AckID == ack.ID &&
			delivery.AcknowledgedAt != nil && ack.AcknowledgedAt.Equal(*delivery.AcknowledgedAt)
	}
	switch delivery.State {
	case DeliveryExpired:
		return directNoticeDeliveryDeadlineElapsedAt(delivery, delivery.UpdatedAt) &&
			directNoticeDeliveryDeadlineElapsedAt(delivery, ack.AcknowledgedAt)
	case DeliveryRetracted:
		return message.State == MessageRetracted && message.TerminalAt != nil &&
			!delivery.UpdatedAt.Before(*message.TerminalAt) &&
			!ack.AcknowledgedAt.Before(*message.TerminalAt)
	default:
		return false
	}
}

func projectDirectNoticeAckFulfillment(
	message Message,
	deliveries []MessageDelivery,
	requiredCount int64,
	dbNow time.Time,
) (FulfillmentProjection, error) {
	digest, err := CanonicalFulfillmentDeliverySetDigest(deliveries)
	if err != nil {
		return FulfillmentProjection{}, err
	}
	const evidenceRef = "same_tx_direct_notice_ack_fulfillment"
	return ProjectMessageFulfillment(message, deliveries, FulfillmentDeliverySetWitness{
		Scope:     DirectoryScopeRef{TenantID: message.TenantID, WorkspaceID: message.WorkspaceID},
		MessageID: message.ID, DeliveryCount: int64(len(deliveries)),
		RequiredCount: requiredCount, Digest: digest, ObservedAt: dbNow,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "deliveries_locked", EvidenceRef: evidenceRef,
		},
		EvidenceRef: evidenceRef,
	}, dbNow)
}

type directNoticeAckPlanDigestV1 struct {
	SchemaVersion       int64                        `json:"schema_version"`
	Operation           string                       `json:"operation"`
	Method              string                       `json:"method"`
	Path                string                       `json:"path"`
	Permission          CommunicationOperation       `json:"permission"`
	AuditAction         string                       `json:"audit_action"`
	EventType           string                       `json:"event_type"`
	Scope               DirectoryScopeRef            `json:"scope"`
	Actor               CommunicationActorRef        `json:"actor"`
	RequestDigest       []byte                       `json:"request_digest"`
	ChannelID           model.ID                     `json:"channel_id"`
	ChannelVersion      int64                        `json:"channel_version"`
	ChannelACLRevision  int64                        `json:"channel_acl_revision"`
	MessageBefore       Message                      `json:"message_before"`
	MessageAfter        Message                      `json:"message_after"`
	DeliveryBefore      MessageDelivery              `json:"delivery_before"`
	DeliveryAfter       MessageDelivery              `json:"delivery_after"`
	Ack                 MessageAck                   `json:"ack"`
	EventAggregateKind  model.Kind                   `json:"event_aggregate_kind"`
	EventAggregateID    model.ID                     `json:"event_aggregate_id"`
	EventSequenceBefore int64                        `json:"event_sequence_before"`
	EventSequenceAfter  int64                        `json:"event_sequence_after"`
	LinksEffectiveAck   bool                         `json:"links_effective_ack"`
	MaterializesExpiry  bool                         `json:"materializes_expiry"`
	Facts               []store.AuthorizationFactRef `json:"facts"`
	Fulfillment         FulfillmentProjection        `json:"fulfillment"`
	RowEffects          []string                     `json:"row_effects"`
}

func canonicalDirectNoticeAckPlanHash(
	preflight directNoticeAckPreflight,
	locked directNoticeAckLockedState,
	plan MessageAckPlan,
	messageAfter Message,
	aggregate directNoticeAckEventAggregate,
	fulfillment FulfillmentProjection,
) ([]byte, error) {
	rowEffects := []string{
		"message_delivery:update_if_changed", "message_ack:create",
		"message:event_cas", "work_event:create", "work_outbox:create",
		"command_receipt:create",
	}
	if aggregate.kind == workItemKind {
		rowEffects[2] = "work_item:event_cas"
	}
	input := directNoticeAckPlanDigestV1{
		SchemaVersion: 1, Operation: directNoticeAckOperation,
		Method: preflight.normalized.method, Path: preflight.normalized.path,
		Permission: CommunicationDeliveryWrite, AuditAction: directNoticeAckAuditAction,
		EventType: communicationMessageAcknowledged, Scope: preflight.normalized.scope,
		Actor:         plan.Ack.Actor,
		RequestDigest: append([]byte(nil), preflight.normalized.requestDigest...),
		ChannelID:     locked.channel.ID, ChannelVersion: locked.channel.Version,
		ChannelACLRevision: locked.channel.ACLRevision,
		MessageBefore:      locked.message, MessageAfter: messageAfter,
		DeliveryBefore: plan.Before, DeliveryAfter: plan.After, Ack: plan.Ack,
		EventAggregateKind: aggregate.kind, EventAggregateID: aggregate.id,
		EventSequenceBefore: aggregate.currentSeq, EventSequenceAfter: aggregate.nextSeq,
		LinksEffectiveAck:  plan.LinksEffectiveAck,
		MaterializesExpiry: plan.MaterializesExpiry,
		Facts:              sortedDirectNoticeFacts(plan.Authority.Facts), Fulfillment: fulfillment,
		RowEffects: rowEffects,
	}
	raw, err := canonicalJSON(input)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

type directNoticeAckApplyCommitment struct {
	SchemaVersion      int64                 `json:"schema_version"`
	TenantID           model.TenantID        `json:"tenant_id"`
	WorkspaceID        model.ID              `json:"workspace_id"`
	ActorFingerprint   []byte                `json:"actor_fingerprint"`
	CommandScope       string                `json:"command_scope"`
	IdempotencyKeyHash []byte                `json:"idempotency_key_hash"`
	RequestDigest      []byte                `json:"request_digest"`
	PlanHash           []byte                `json:"plan_hash"`
	ReceiptID          model.ID              `json:"receipt_id"`
	CommandID          model.ID              `json:"command_id"`
	AckID              model.ID              `json:"ack_id"`
	DeliveryID         model.ID              `json:"delivery_id"`
	MessageID          model.ID              `json:"message_id"`
	EventID            model.ID              `json:"event_id"`
	MessageEventSeq    int64                 `json:"message_event_seq"`
	DeliveryVersion    int64                 `json:"delivery_version"`
	DeliveryState      MessageDeliveryState  `json:"delivery_state"`
	Late               bool                  `json:"late"`
	Fulfillment        FulfillmentProjection `json:"fulfillment"`
	HTTPStatus         int                   `json:"http_status"`
	CompletedAt        string                `json:"completed_at"`
}

func canonicalDirectNoticeAckApplyCommitment(
	value directNoticeAckApplyCommitment,
) ([]byte, error) {
	raw, err := canonicalJSON(value)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func directNoticeAckApplyCommitmentFromResult(
	preflight directNoticeAckPreflight,
	planHash []byte,
	result DirectNoticeDeliveryAckResult,
	messageEventSeq int64,
	dbNow time.Time,
) ([]byte, error) {
	return canonicalDirectNoticeAckApplyCommitment(directNoticeAckApplyCommitment{
		SchemaVersion:      directNoticeAckApplyCommitmentV1,
		TenantID:           preflight.normalized.scope.TenantID,
		WorkspaceID:        preflight.normalized.scope.WorkspaceID,
		ActorFingerprint:   append([]byte(nil), preflight.normalized.actorFingerprint...),
		CommandScope:       preflight.normalized.commandScope,
		IdempotencyKeyHash: append([]byte(nil), preflight.normalized.idempotencyKeyHash...),
		RequestDigest:      append([]byte(nil), preflight.normalized.requestDigest...),
		PlanHash:           append([]byte(nil), planHash...), ReceiptID: preflight.ids.Receipt,
		CommandID: result.CommandID, AckID: result.AckID, DeliveryID: result.DeliveryID,
		MessageID: result.MessageID, EventID: result.EventID,
		MessageEventSeq: messageEventSeq, DeliveryVersion: result.Version,
		DeliveryState: result.State, Late: result.Late, Fulfillment: result.Fulfillment,
		HTTPStatus: http.StatusOK, CompletedAt: dbNow.UTC().Format(time.RFC3339Nano),
	})
}

func directNoticeAckApplyCommitmentFromReceipt(
	receipt CommunicationCommandReceipt,
	result DirectNoticeDeliveryAckResult,
) ([]byte, error) {
	return canonicalDirectNoticeAckApplyCommitment(directNoticeAckApplyCommitment{
		SchemaVersion: directNoticeAckApplyCommitmentV1,
		TenantID:      receipt.TenantID, WorkspaceID: receipt.WorkspaceID,
		ActorFingerprint:   append([]byte(nil), receipt.ActorFingerprint...),
		CommandScope:       receipt.CommandScope,
		IdempotencyKeyHash: append([]byte(nil), receipt.IdempotencyKeyHash...),
		RequestDigest:      append([]byte(nil), receipt.RequestDigest...),
		PlanHash:           append([]byte(nil), receipt.PlanHash...), ReceiptID: receipt.ID,
		CommandID: result.CommandID, AckID: result.AckID, DeliveryID: result.DeliveryID,
		MessageID: result.MessageID, EventID: result.EventID,
		MessageEventSeq: result.messageEventSeq, DeliveryVersion: result.Version,
		DeliveryState: result.State, Late: result.Late, Fulfillment: result.Fulfillment,
		HTTPStatus:  receipt.HTTPStatus,
		CompletedAt: receipt.CompletedAt.UTC().Format(time.RFC3339Nano),
	})
}

type directNoticeAckEventPayloadV1 struct {
	SchemaVersion   int64                 `json:"schema_version"`
	Command         string                `json:"command"`
	ResultKind      string                `json:"result_kind"`
	ResultID        model.ID              `json:"result_id"`
	AckID           model.ID              `json:"ack_id"`
	DeliveryID      model.ID              `json:"delivery_id"`
	MessageID       model.ID              `json:"message_id"`
	EventSequence   int64                 `json:"event_sequence"`
	DeliveryVersion int64                 `json:"delivery_version"`
	DeliveryState   MessageDeliveryState  `json:"delivery_state"`
	Late            bool                  `json:"late"`
	Fulfillment     FulfillmentProjection `json:"fulfillment"`
	PlanHash        string                `json:"plan_hash"`
}

func directNoticeAckEventPayload(
	result DirectNoticeDeliveryAckResult,
) (directNoticeAckEventPayloadV1, error) {
	if result.messageEventSeq < 1 || len(result.ETag) == 0 {
		return directNoticeAckEventPayloadV1{}, communicationError(
			ErrInvalidCommunicationModel, "DirectNotice Ack event projection is invalid",
		)
	}
	return directNoticeAckEventPayloadV1{
		SchemaVersion: 1, Command: directNoticeAckOperation,
		ResultKind: string(messageAckKind), ResultID: result.AckID, AckID: result.AckID,
		DeliveryID: result.DeliveryID, MessageID: result.MessageID,
		EventSequence: result.messageEventSeq, DeliveryVersion: result.Version,
		DeliveryState: result.State, Late: result.Late, Fulfillment: result.Fulfillment,
	}, nil
}

func canonicalDirectNoticeAckEventPayload(
	result DirectNoticeDeliveryAckResult,
	planHash []byte,
) ([]byte, error) {
	payload, err := directNoticeAckEventPayload(result)
	if err != nil {
		return nil, err
	}
	payload.PlanHash = hex.EncodeToString(planHash)
	return canonicalJSON(payload)
}

func decodeDirectNoticeAckEventPayload(raw []byte) (directNoticeAckEventPayloadV1, error) {
	var payload directNoticeAckEventPayloadV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return directNoticeAckEventPayloadV1{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return directNoticeAckEventPayloadV1{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack Event payload has trailing values",
		)
	}
	canonical, err := canonicalJSON(payload)
	if err != nil || !bytes.Equal(canonical, raw) || payload.SchemaVersion != 1 {
		return directNoticeAckEventPayloadV1{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack Event payload version is unavailable",
		)
	}
	return payload, nil
}

func persistDirectNoticeDeliveryAck(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticeAckPreflight,
	locked directNoticeAckLockedState,
	plan MessageAckPlan,
	messageAfter Message,
	aggregate directNoticeAckEventAggregate,
	planHash []byte,
	audit model.AuditEvent,
	result DirectNoticeDeliveryAckResult,
) error {
	if plan.After.Version != plan.Before.Version {
		record, err := messageDeliveryToRecord(plan.After)
		if err != nil {
			return err
		}
		record[model.ColVersion] = plan.Before.Version
		if _, err = tx.update(ctx, messageDeliveryKind, record); err != nil {
			return err
		}
	}
	ackRecord, err := messageAckToRecord(plan.Ack)
	if err != nil {
		return err
	}
	if _, err = tx.createWithID(ctx, messageAckKind, plan.Ack.ID, ackRecord); err != nil {
		return err
	}
	if aggregate.kind == workItemKind {
		if aggregate.workRepo == nil || recordID(aggregate.workItem) != aggregate.id ||
			aggregate.nextSeq != aggregate.currentSeq+1 {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow WorkTask Ack aggregate changed before apply",
			)
		}
		itemAfter := cloneMessageLifecycleRecord(aggregate.workItem)
		itemAfter[colWorkLastEventSeq] = aggregate.nextSeq
		updated, updateErr := runCommunicationBoundAuthorityEffect(
			tx.boundAuthorityState,
			func() (model.Record, error) {
				return aggregate.workRepo.UpdateAtTransactionTime(ctx, itemAfter)
			},
		)
		if updateErr != nil || recordID(updated) != aggregate.id ||
			updated.Int(colWorkLastEventSeq) != aggregate.nextSeq {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow WorkTask Ack aggregate CAS is unavailable",
			)
		}
	} else {
		messageRecord, encodeErr := messageToRecord(messageAfter, locked.requiredCount)
		if encodeErr != nil {
			return encodeErr
		}
		messageRecord[model.ColVersion] = locked.message.Version
		if _, err = tx.update(ctx, messageKind, messageRecord); err != nil {
			return err
		}
	}
	eventPayload, err := canonicalDirectNoticeAckEventPayload(result, planHash)
	if err != nil || len(eventPayload) > 16*1024 {
		return communicationError(
			ErrInvalidCommunicationModel, "DirectNotice Ack Event payload is invalid",
		)
	}
	if _, err = tx.create(ctx, workEventKind, model.Record{
		colWorkWorkspaceID: preflight.normalized.scope.WorkspaceID.String(),
		colEventID:         preflight.ids.Event.String(), colEventAggregateKind: string(aggregate.kind),
		colEventAggregateID: aggregate.id.String(), colEventSeq: aggregate.nextSeq,
		colEventType: communicationMessageAcknowledged, colEventActorKind: string(ActorUser),
		colEventActorRef:   preflight.normalized.principal.UserID.String(),
		colEventOccurredAt: tx.now.String(), colEventPayload: string(eventPayload),
		colEventPayloadHash: hashBytes(eventPayload),
		colEventCommandID:   preflight.ids.Command.String(),
		colEventAuditSeq:    audit.Seq, colEventAuditHash: append([]byte(nil), audit.Hash...),
	}); err != nil {
		return err
	}
	if _, err = tx.create(ctx, workOutboxKind, model.Record{
		colWorkWorkspaceID: preflight.normalized.scope.WorkspaceID.String(),
		colOutboxEventID:   preflight.ids.Event.String(), colOutboxState: "pending",
		colOutboxAttempts: int64(0), colOutboxNextAttemptAt: tx.now.String(),
		colOutboxClaimOwner: nil, colOutboxClaimUntil: nil,
		colOutboxPublishedAt: nil, colOutboxLastOutcome: nil,
	}); err != nil {
		return err
	}
	receipt, err := buildDirectNoticeAckReceipt(
		tx.now.Time(), preflight, planHash, audit, result,
	)
	if err != nil {
		return err
	}
	wantCommitment, err := directNoticeAckApplyCommitmentFromResult(
		preflight, planHash, result, result.messageEventSeq, tx.now.Time(),
	)
	if err != nil {
		return err
	}
	gotCommitment, err := directNoticeAckApplyCommitmentFromReceipt(receipt, result)
	if err != nil || !bytes.Equal(wantCommitment, gotCommitment) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack receipt does not reconstruct its audited commitment",
		)
	}
	receiptRecord, err := communicationCommandReceiptToRecord(receipt)
	if err != nil {
		return err
	}
	_, err = tx.createWithID(ctx, communicationCommandKind, receipt.ID, receiptRecord)
	return err
}

func buildDirectNoticeAckReceipt(
	dbNow time.Time,
	preflight directNoticeAckPreflight,
	planHash []byte,
	audit model.AuditEvent,
	result DirectNoticeDeliveryAckResult,
) (CommunicationCommandReceipt, error) {
	projection := CommunicationCommandResponseProjection{
		IDs: map[string]model.ID{
			"ack_id": result.AckID, "delivery_id": result.DeliveryID,
			"message_id": result.MessageID, "event_id": result.EventID,
		},
		Version: result.Version, State: string(result.State),
		Counts: map[string]int64{
			"required":     result.Fulfillment.Required,
			"acknowledged": result.Fulfillment.Acknowledged,
			"viable":       result.Fulfillment.Viable, "unmet": result.Fulfillment.Unmet,
			"quorum": result.Fulfillment.Quorum,
		},
		Digests: map[string][]byte{
			"request": append([]byte(nil), preflight.normalized.requestDigest...),
			"plan":    append([]byte(nil), planHash...),
		},
	}
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: preflight.ids.Receipt, TenantID: preflight.normalized.scope.TenantID,
				WorkspaceID: preflight.normalized.scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
			},
		},
		CommandID:          preflight.ids.Command,
		ActorFingerprint:   append([]byte(nil), preflight.normalized.actorFingerprint...),
		CommandScope:       preflight.normalized.commandScope,
		IdempotencyKeyHash: append([]byte(nil), preflight.normalized.idempotencyKeyHash...),
		RequestDigest:      append([]byte(nil), preflight.normalized.requestDigest...),
		PlanHash:           append([]byte(nil), planHash...), ResultKind: string(messageAckKind),
		ResultID: result.AckID, HTTPStatus: http.StatusOK,
		ResponseProjectionJSON: projection, EventID: result.EventID,
		AuditSeq: audit.Seq, AuditHash: append([]byte(nil), audit.Hash...), CompletedAt: dbNow,
	}
	binding, err := CanonicalCommunicationReceiptResponseBinding(receipt)
	if err != nil {
		return CommunicationCommandReceipt{}, err
	}
	digest := sha256.Sum256(binding)
	receipt.ResponseDigest = digest[:]
	if err := ValidateCommunicationCommandReceipt(receipt); err != nil {
		return CommunicationCommandReceipt{}, err
	}
	return receipt, nil
}

func directNoticeAckResultFromReceipt(
	ctx context.Context,
	resolve communicationReadRepositoryResolver,
	normalized directNoticeAckNormalizedCommand,
	receipt CommunicationCommandReceipt,
) (DirectNoticeDeliveryAckResult, error) {
	projection := receipt.ResponseProjectionJSON
	state := MessageDeliveryState(projection.State)
	if ValidateCommunicationCommandReceipt(receipt) != nil ||
		receipt.ResultKind != string(messageAckKind) || receipt.ResultID == "" ||
		receipt.HTTPStatus != http.StatusOK || receipt.SealKeyVersion != "" ||
		receipt.DigestKeyVersion != "" || receipt.EventID == "" ||
		len(projection.IDs) != 4 || len(projection.Counts) != 5 ||
		len(projection.Digests) != 2 || projection.Version < 1 || !state.Valid() ||
		projection.IDs["ack_id"] != receipt.ResultID ||
		projection.IDs["delivery_id"] != normalized.deliveryID ||
		projection.IDs["message_id"] == "" || projection.IDs["event_id"] != receipt.EventID ||
		!bytes.Equal(projection.Digests["request"], receipt.RequestDigest) ||
		!bytes.Equal(projection.Digests["plan"], receipt.PlanHash) {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack receipt projection is unavailable",
		)
	}
	for _, key := range []string{"required", "acknowledged", "viable", "unmet", "quorum"} {
		if _, present := projection.Counts[key]; !present {
			return DirectNoticeDeliveryAckResult{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"DirectNotice Ack receipt lacks fulfillment count %s", key,
			)
		}
	}
	result := DirectNoticeDeliveryAckResult{
		CommandID: receipt.CommandID, AckID: receipt.ResultID,
		DeliveryID: normalized.deliveryID, MessageID: projection.IDs["message_id"],
		EventID: receipt.EventID, Version: projection.Version,
		ETag: fmt.Sprintf("\"v%d\"", projection.Version), State: state,
		Fulfillment: FulfillmentProjection{
			Required:     projection.Counts["required"],
			Acknowledged: projection.Counts["acknowledged"],
			Viable:       projection.Counts["viable"], Unmet: projection.Counts["unmet"],
			Quorum: projection.Counts["quorum"],
		},
		AuditSeq: receipt.AuditSeq,
	}
	ackRepo, err := resolve(messageAckKind)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	ackRecord, err := ackRepo.Get(ctx, result.AckID)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack row anchor is unavailable",
		)
	}
	ack, err := messageAckFromRecord(ackRecord)
	if err != nil || ack.ID != result.AckID || ack.DeliveryID != result.DeliveryID ||
		ack.TenantID != normalized.scope.TenantID ||
		ack.WorkspaceID != normalized.scope.WorkspaceID ||
		ack.Actor != (CommunicationActorRef{Kind: ActorUser, Ref: normalized.principal.UserID.String()}) ||
		ack.OnBehalfOf != nil || ack.Note != nil || !ack.CreatedAt.Equal(receipt.CompletedAt) ||
		!ack.AcknowledgedAt.Equal(receipt.CompletedAt) {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack row anchor does not match",
		)
	}
	result.Late = ack.Late
	deliveryRepo, err := resolve(messageDeliveryKind)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	deliveryRecord, err := deliveryRepo.Get(ctx, result.DeliveryID)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack Delivery anchor is unavailable",
		)
	}
	delivery, err := messageDeliveryFromRecord(deliveryRecord)
	if err != nil || delivery.ID != result.DeliveryID || delivery.MessageID != result.MessageID ||
		delivery.TenantID != normalized.scope.TenantID ||
		delivery.WorkspaceID != normalized.scope.WorkspaceID ||
		delivery.Recipient != (RecipientRef{Kind: RecipientUser, Ref: normalized.principal.UserID.String()}) ||
		delivery.Version < result.Version || delivery.State != result.State ||
		(!ack.Late && delivery.UpdatedAt.Before(receipt.CompletedAt)) {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack Delivery anchor does not match",
		)
	}
	messageRepo, err := resolve(messageKind)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	messageRecord, err := messageRepo.Get(ctx, result.MessageID)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack Message anchor is unavailable",
		)
	}
	requiredCount := int64(0)
	if delivery.Required {
		requiredCount = 1
	}
	message, err := messageFromRecord(messageRecord, requiredCount)
	if err != nil || message.ID != result.MessageID || message.TenantID != normalized.scope.TenantID ||
		message.WorkspaceID != normalized.scope.WorkspaceID ||
		!directNoticeAckMessageMatchesCarrier(normalized, message) || message.ThreadID != message.ID ||
		!directNoticeAckMessageReceiptTimeMatches(normalized, message, receipt.CompletedAt) ||
		ValidateMessageDeliveryLineage(message, delivery) != nil ||
		!directNoticeAckMatchesLockedLifecycle(ack, delivery, message, receipt.CompletedAt) {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack Message lifecycle does not match",
		)
	}
	events, err := resolve(workEventKind)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	eventRows, page, err := events.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colEventID, Op: model.OpEq, Value: result.EventID.String(),
	}}, Limit: 2})
	if err != nil || len(eventRows) != 1 || page.HasMore {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack Event anchor is unavailable",
		)
	}
	event := eventRows[0]
	eventRecordID, eventRecordIDErr := model.ParseID(event.String(model.ColID))
	eventOccurredAt, occurredErr := model.ParseTimestamp(event.String(colEventOccurredAt))
	eventCreatedAt, createdErr := model.ParseTimestamp(event.String(model.ColCreatedAt))
	eventUpdatedAt, updatedErr := model.ParseTimestamp(event.String(model.ColUpdatedAt))
	storedPayload := []byte(event.String(colEventPayload))
	payload, payloadErr := decodeDirectNoticeAckEventPayload(storedPayload)
	if payloadErr == nil {
		result.Fulfillment.State = payload.Fulfillment.State
	}
	aggregateKind, aggregateID, aggregateOK := directNoticeAckEventAggregateRef(
		normalized, result.MessageID,
	)
	if payloadErr != nil || eventRecordIDErr != nil ||
		!aggregateOK ||
		!validCanonicalCommunicationID(eventRecordID) ||
		occurredErr != nil || createdErr != nil || updatedErr != nil ||
		event.String(model.ColTenantID) != normalized.scope.TenantID.String() ||
		event.Int(model.ColVersion) != 1 ||
		event.String(colEventID) != result.EventID.String() ||
		event.String(colWorkWorkspaceID) != normalized.scope.WorkspaceID.String() ||
		!eventOccurredAt.Time().Equal(receipt.CompletedAt) ||
		!eventCreatedAt.Time().Equal(receipt.CompletedAt) ||
		!eventUpdatedAt.Time().Equal(receipt.CompletedAt) ||
		event.String(colEventAggregateKind) != string(aggregateKind) ||
		event.String(colEventAggregateID) != aggregateID.String() ||
		event.Int(colEventSeq) < 1 || event.String(colEventType) != communicationMessageAcknowledged ||
		event.String(colEventActorKind) != string(ActorUser) ||
		event.String(colEventActorRef) != normalized.principal.UserID.String() ||
		event.String(colEventCommandID) != receipt.CommandID.String() ||
		event.Int(colEventAuditSeq) != receipt.AuditSeq ||
		!bytes.Equal(event.Bytes(colEventAuditHash), receipt.AuditHash) ||
		!bytes.Equal(event.Bytes(colEventPayloadHash), hashBytes(storedPayload)) ||
		payload.Command != directNoticeAckOperation ||
		payload.ResultKind != string(messageAckKind) || payload.ResultID != result.AckID ||
		payload.AckID != result.AckID || payload.DeliveryID != result.DeliveryID ||
		payload.MessageID != result.MessageID || payload.EventSequence != event.Int(colEventSeq) ||
		payload.DeliveryVersion != result.Version || payload.DeliveryState != result.State ||
		payload.Late != result.Late || payload.Fulfillment != result.Fulfillment ||
		payload.PlanHash != hex.EncodeToString(receipt.PlanHash) {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack Event anchor does not match",
		)
	}
	result.messageEventSeq = payload.EventSequence
	if err := validateDirectNoticeAckFulfillmentProjection(message, result.Fulfillment); err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	if !directNoticeAckReceiptAggregateCurrent(normalized, message, result.messageEventSeq) {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack event aggregate CAS is unavailable",
		)
	}
	outboxes, err := resolve(workOutboxKind)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	outboxRows, outboxPage, err := outboxes.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colOutboxEventID, Op: model.OpEq, Value: result.EventID.String(),
	}}, Limit: 2})
	if err != nil || len(outboxRows) != 1 || outboxPage.HasMore ||
		validateWorkOutboxEvidence(outboxRows[0]) != nil ||
		outboxRows[0].String(model.ColTenantID) != normalized.scope.TenantID.String() ||
		outboxRows[0].String(colOutboxEventID) != result.EventID.String() ||
		outboxRows[0].String(colWorkWorkspaceID) != normalized.scope.WorkspaceID.String() {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack Outbox anchor is unavailable",
		)
	}
	outboxCreatedAt, err := model.ParseTimestamp(outboxRows[0].String(model.ColCreatedAt))
	if err != nil || !outboxCreatedAt.Time().Equal(receipt.CompletedAt) {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack Outbox time does not match",
		)
	}
	return result, nil
}

func directNoticeAckMessageMatchesCarrier(
	normalized directNoticeAckNormalizedCommand,
	message Message,
) bool {
	selector := normalized.carrier
	if selector.class == "" {
		return selector == (directNoticeAckCarrierSelector{}) &&
			message.Kind == MessageNotice && message.WorkItemID == ""
	}
	return selector.class == directNoticeAckCarrierWorkflowWorkTask &&
		selector.validate(normalized.scope, normalized.principal) == nil &&
		message.Kind == MessageWorkTask && message.WorkItemID == selector.workItemID &&
		message.ChannelID == selector.channelID
}

func directNoticeAckMessageReceiptTimeMatches(
	normalized directNoticeAckNormalizedCommand,
	message Message,
	completedAt time.Time,
) bool {
	if normalized.carrier.class == "" {
		return !message.UpdatedAt.Before(completedAt)
	}
	return normalized.carrier.class == directNoticeAckCarrierWorkflowWorkTask &&
		!message.UpdatedAt.After(completedAt)
}

func directNoticeAckEventAggregateRef(
	normalized directNoticeAckNormalizedCommand,
	messageID model.ID,
) (model.Kind, model.ID, bool) {
	if normalized.carrier.class == "" {
		return messageKind, messageID, validCanonicalCommunicationID(messageID)
	}
	selector := normalized.carrier
	if selector.class != directNoticeAckCarrierWorkflowWorkTask ||
		selector.validate(normalized.scope, normalized.principal) != nil {
		return "", "", false
	}
	return workItemKind, selector.workItemID, true
}

func directNoticeAckReceiptAggregateCurrent(
	normalized directNoticeAckNormalizedCommand,
	message Message,
	eventSeq int64,
) bool {
	if eventSeq < 1 {
		return false
	}
	if normalized.carrier.class == "" {
		return message.LastEventSeq >= eventSeq && message.Version == message.LastEventSeq+1
	}
	return normalized.carrier.class == directNoticeAckCarrierWorkflowWorkTask &&
		message.LastEventSeq == 0 && message.WorkItemID == normalized.carrier.workItemID
}

func validateDirectNoticeAckFulfillmentProjection(
	message Message,
	projection FulfillmentProjection,
) error {
	if projection.Required != projection.Acknowledged+projection.Viable+projection.Unmet {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack fulfillment does not balance",
		)
	}
	want := FulfillmentProjection{
		Required: projection.Required, Acknowledged: projection.Acknowledged,
		Viable: projection.Viable, Unmet: projection.Unmet, Quorum: projection.Quorum,
	}
	switch message.AckPolicy {
	case AckPolicyNone:
		if want.Required != 0 || want.Quorum != 0 {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"DirectNotice Ack none-policy fulfillment is invalid")
		}
		want.State = FulfillmentNotRequired
	case AckPolicyEachRequired:
		if want.Required < 1 || want.Quorum != 0 {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"DirectNotice Ack each-required fulfillment is invalid")
		}
		switch {
		case want.Unmet > 0:
			want.State = FulfillmentUnmet
		case want.Acknowledged == want.Required:
			want.State = FulfillmentMet
		default:
			want.State = FulfillmentPending
		}
	case AckPolicyQuorum:
		if want.Quorum < 1 || want.Quorum > want.Required {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"DirectNotice Ack quorum fulfillment is invalid")
		}
		switch {
		case want.Acknowledged >= want.Quorum:
			want.State = FulfillmentMet
		case want.Acknowledged+want.Viable < want.Quorum:
			want.State = FulfillmentUnmet
		default:
			want.State = FulfillmentPending
		}
	default:
		return communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack policy is unavailable",
		)
	}
	if projection != want {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack fulfillment projection is invalid",
		)
	}
	return nil
}

func verifyDirectNoticeAckAuditAnchor(
	ctx context.Context,
	reader store.VerifiedAuditAnchorReader,
	normalized directNoticeAckNormalizedCommand,
	receipt CommunicationCommandReceipt,
	result DirectNoticeDeliveryAckResult,
) error {
	event, metaCanonical, found, err := reader.ReadVerifiedAuditAnchor(ctx, receipt.AuditSeq)
	if err != nil || !found || event.Seq != receipt.AuditSeq ||
		event.TenantID != normalized.scope.TenantID ||
		event.Actor != directNoticeActor(normalized.principal) ||
		event.ActorKind != model.ActorUser || event.Action != directNoticeAckAuditAction ||
		event.TargetKind != communicationCommandKind || event.TargetID != receipt.CommandID ||
		!bytes.Equal(event.PayloadHash, receipt.PlanHash) ||
		!bytes.Equal(event.Hash, receipt.AuditHash) ||
		!validateDirectNoticeAckAuditMeta(metaCanonical, normalized, receipt, result) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack receipt audit anchor does not match",
		)
	}
	return nil
}

func validateDirectNoticeAckAuditMeta(
	metaCanonical string,
	normalized directNoticeAckNormalizedCommand,
	receipt CommunicationCommandReceipt,
	result DirectNoticeDeliveryAckResult,
) bool {
	decoder := json.NewDecoder(bytes.NewBufferString(metaCanonical))
	decoder.UseNumber()
	var meta map[string]any
	if err := decoder.Decode(&meta); err != nil {
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return false
	}
	allowed := map[string]struct{}{
		"workspace_id": {}, "workspace_binding_version": {}, "command_scope": {},
		"delivery_id": {}, "message_id": {}, "ack_id": {}, "late": {},
		"message_event_seq": {}, "apply_commitment_version": {},
		"apply_commitment": {}, "trace_id": {}, "span_id": {},
	}
	for key := range meta {
		if _, present := allowed[key]; !present {
			return false
		}
	}
	workspaceBinding, workspaceOK := auditMetaInt64(meta["workspace_binding_version"])
	eventSeq, eventSeqOK := auditMetaInt64(meta["message_event_seq"])
	commitmentVersion, commitmentVersionOK := auditMetaInt64(meta["apply_commitment_version"])
	late, lateOK := meta["late"].(bool)
	commitment, commitmentErr := directNoticeAckApplyCommitmentFromReceipt(receipt, result)
	commitmentText, commitmentOK := meta["apply_commitment"].(string)
	if meta["workspace_id"] != normalized.scope.WorkspaceID.String() ||
		meta["command_scope"] != receipt.CommandScope ||
		meta["delivery_id"] != result.DeliveryID.String() ||
		meta["message_id"] != result.MessageID.String() ||
		meta["ack_id"] != result.AckID.String() || !lateOK || late != result.Late ||
		!workspaceOK || workspaceBinding != 1 ||
		!eventSeqOK || eventSeq != result.messageEventSeq ||
		!commitmentVersionOK || commitmentVersion != directNoticeAckApplyCommitmentV1 ||
		commitmentErr != nil || !commitmentOK ||
		commitmentText != hex.EncodeToString(commitment) {
		return false
	}
	trace, hasTrace := meta["trace_id"]
	span, hasSpan := meta["span_id"]
	return hasTrace == hasSpan && (!hasTrace ||
		(validAuditCorrelationID(trace, 32) && validAuditCorrelationID(span, 16)))
}

func confirmDirectNoticeAckReplayLocked(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticeAckPreflight,
	readerPreflight directNoticeReaderPreflight,
	receipt CommunicationCommandReceipt,
	result DirectNoticeDeliveryAckResult,
	authorityLock directNoticeAckAuthorityLock,
) error {
	return confirmDirectNoticeAckReplayLockedWithScope(
		ctx, tx, nil, preflight, readerPreflight, receipt, result, authorityLock,
	)
}

func confirmDirectNoticeAckReplayLockedWithScope(
	ctx context.Context,
	tx *communicationTx,
	workflowScope store.Scope,
	preflight directNoticeAckPreflight,
	readerPreflight directNoticeReaderPreflight,
	receipt CommunicationCommandReceipt,
	result DirectNoticeDeliveryAckResult,
	authorityLock directNoticeAckAuthorityLock,
) error {
	if tx == nil || preflight.bindingID == nil ||
		preflight.bindingID != tx.requestBindingID || authorityLock.consume == nil ||
		!authorityLock.consume(tx, preflight, readerPreflight) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack replay crossed its request binding",
		)
	}
	locked, err := lockDirectNoticeAckState(ctx, tx, preflight.normalized, readerPreflight)
	if err != nil {
		return confirmDirectNoticeAckNegativeAfterRefresh(ctx, tx, err)
	}
	aggregate, err := lockDirectNoticeAckEventAggregate(
		ctx, tx, workflowScope, preflight.normalized, locked,
	)
	if err != nil {
		return err
	}
	reconstructed, err := directNoticeAckResultFromReceipt(
		ctx,
		func(kind model.Kind) (communicationReadRepository, error) {
			return tx.repo(kind)
		},
		preflight.normalized,
		receipt,
	)
	if err != nil {
		return err
	}
	if reconstructed != result {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack replay projection changed before authority confirmation",
		)
	}
	if err := tx.refreshNow(ctx); err != nil {
		return err
	}
	evidence, decision, err := directNoticeAckAuthorityEvidence(
		tx, preflight.normalized, readerPreflight, locked,
	)
	if err != nil {
		return err
	}
	if err := validateDirectNoticeAckLockedCarrierLifecycle(
		tx, locked, evidence.CarrierState.Delivery,
	); err != nil {
		return err
	}
	if locked.message.Version == math.MaxInt64 || locked.message.LastEventSeq == math.MaxInt64 ||
		locked.message.Version != locked.message.LastEventSeq+1 {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack replay Message event sequence is unavailable",
		)
	}
	currentFulfillment, err := projectDirectNoticeAckFulfillment(
		locked.message, locked.deliveries, locked.requiredCount, tx.now.Time(),
	)
	if err != nil {
		return err
	}
	if decision.Verdict != VerdictClean || len(locked.acks) != 1 ||
		locked.acks[0].ID != receipt.ResultID || locked.acks[0].ID != result.AckID ||
		!directNoticeAckMatchesLockedLifecycle(locked.acks[0],
			evidence.CarrierState.Delivery, locked.message, tx.now.Time()) ||
		evidence.CarrierState.Delivery.ID != result.DeliveryID ||
		evidence.CarrierState.Delivery.Version < result.Version ||
		evidence.CarrierState.Delivery.State != result.State ||
		locked.message.ID != result.MessageID ||
		!directNoticeAckReplayAggregateCurrent(
			preflight.normalized, locked.message, aggregate, result.messageEventSeq,
		) ||
		currentFulfillment != result.Fulfillment {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack replay graph changed before authority confirmation",
		)
	}
	return nil
}

func directNoticeAckReplayAggregateCurrent(
	normalized directNoticeAckNormalizedCommand,
	message Message,
	aggregate directNoticeAckEventAggregate,
	eventSeq int64,
) bool {
	if !directNoticeAckReceiptAggregateCurrent(normalized, message, eventSeq) {
		return false
	}
	if normalized.carrier.class == "" {
		return aggregate.kind == messageKind && aggregate.id == message.ID &&
			aggregate.currentSeq >= eventSeq
	}
	return aggregate.kind == workItemKind && aggregate.id == normalized.carrier.workItemID &&
		aggregate.currentSeq >= eventSeq
}
