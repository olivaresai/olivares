// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	messageDerivedRerouteScope  = "message.derived.reroute"
	messageDerivedEscalateScope = "message.derived.escalate-overdue"

	messageDerivedRerouteAudit  = "sessions.communication.message.reroute"
	messageDerivedEscalateAudit = "sessions.communication.message.escalate-overdue"

	messageDerivedReroutedEvent  = "work.message.rerouted"
	messageDerivedEscalatedEvent = "work.message.escalated"

	messageDerivedEscalatorRef = "message-overdue-escalator"
	messageDerivedMaxStep      = int64(64)
)

type messageDerivedAction string

const (
	messageDerivedReroute  messageDerivedAction = "reroute"
	messageDerivedEscalate messageDerivedAction = "escalate_overdue"
)

// messageDerivedAuthorizer is private while K3 is OFF. The returned witness
// must bind the exact derived action while Core and ChannelGrant evidence bind
// the new publication to the ordinary message-send boundary.
type messageDerivedAuthorizer interface {
	AuthorizeMessageDerived(
		context.Context,
		DirectoryScopeRef,
		model.ID,
		RecipientRef,
		messageDerivedAction,
	) (messageDerivedAuthority, error)
}

type messageDerivedAuthority struct {
	Actor                 CommunicationActorRef
	Principal             CommunicationPrincipal
	SenderResolution      *PrincipalResolution
	SenderGrantClosure    ChannelGrantSubjectClosure
	RecipientGrantClosure ChannelGrantSubjectClosure
	CoreWitness           ReadWitness
	Facts                 []store.AuthorizationFactRef
	ObservedAt            time.Time
	FreshUntil            time.Time
	ActionEvidence        AuthorityEvidence
}

type messageDerivedService struct {
	module     *Module
	authorizer messageDerivedAuthorizer
	newID      func() model.ID
}

// newMessageDerivedService is deliberately private and absent from the K3
// composition root. Compiling this vertical cannot make the capability live.
func newMessageDerivedService(
	module *Module,
	authorizer messageDerivedAuthorizer,
	newID func() model.ID,
) (*messageDerivedService, error) {
	if module == nil || authorizer == nil {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message ports are unavailable",
		)
	}
	if newID == nil {
		newID = model.NewID
	}
	return &messageDerivedService{module: module, authorizer: authorizer, newID: newID}, nil
}

type messageRerouteCommand struct {
	MessageID       model.ID
	ExpectedVersion int64
	Recipient       RecipientRef
	IdempotencyKey  string
}

type messageEscalateOverdueCommand struct {
	MessageID       model.ID
	ExpectedVersion int64
	OriginEventID   model.ID
	Step            int64
	Recipient       RecipientRef
}

type messageDerivedResult struct {
	CommandID       model.ID
	SourceMessageID model.ID
	MessageID       model.ID
	DeliveryID      model.ID
	EventID         model.ID
	Version         int64
	State           MessageState
	AutomationDepth int64
	AuditSeq        int64
	Replayed        bool
}

type messageDerivedCommandProjection struct {
	MessageID       model.ID     `json:"message_id"`
	ExpectedVersion int64        `json:"expected_version"`
	OriginEventID   model.ID     `json:"origin_event_id,omitempty"`
	Step            int64        `json:"step,omitempty"`
	Recipient       RecipientRef `json:"recipient"`
}

type messageDerivedNormalized struct {
	action              messageDerivedAction
	command             messageDerivedCommandProjection
	scope               DirectoryScopeRef
	authority           messageDerivedAuthority
	source              Message
	sourceDeliveries    []MessageDelivery
	channel             Channel
	audienceRequest     PublicationAudienceRequest
	audienceAttestation PublicationAudienceAttestation
	snapshot            DirectorySnapshot
	sourceHash          []byte
	actorFingerprint    []byte
	idempotencyKeyHash  []byte
	requestDigest       []byte
	commandScope        string
	automationDepth     int64
	sourceKind          ChannelRouteSourceKind
	eventType           string
}

func (s *messageDerivedService) Reroute(
	ctx context.Context,
	scope DirectoryScopeRef,
	command messageRerouteCommand,
) (messageDerivedResult, error) {
	if s == nil || s.module == nil || s.authorizer == nil || ctx == nil {
		return messageDerivedResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Message reroute service is unavailable",
		)
	}
	keyID, err := model.ParseID(command.IdempotencyKey)
	if err != nil || !validCanonicalCommunicationID(keyID) || keyID.String() != command.IdempotencyKey {
		return messageDerivedResult{}, communicationError(
			ErrInvalidCommunicationModel, "Message reroute requires a canonical UUIDv7 idempotency key",
		)
	}
	normalized, err := s.normalizeDerived(ctx, scope, messageDerivedReroute,
		messageDerivedCommandProjection{
			MessageID: command.MessageID, ExpectedVersion: command.ExpectedVersion,
			Recipient: command.Recipient,
		}, []byte(command.IdempotencyKey))
	if err != nil {
		return messageDerivedResult{}, normalizeMessageDerivedError(err)
	}
	result, err := s.applyDerived(ctx, normalized, s.allocateDerivedIDs())
	return result, normalizeMessageDerivedError(err)
}

// EscalateOverdue has no caller-selected idempotency key. The exact
// (scope, source Message, overdue origin Event, step) tuple is the durable key,
// so retries cannot fork a step merely by changing request metadata.
func (s *messageDerivedService) EscalateOverdue(
	ctx context.Context,
	scope DirectoryScopeRef,
	command messageEscalateOverdueCommand,
) (messageDerivedResult, error) {
	if s == nil || s.module == nil || s.authorizer == nil || ctx == nil {
		return messageDerivedResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Message overdue escalation service is unavailable",
		)
	}
	key, err := canonicalJSON(struct {
		Domain        string            `json:"domain"`
		Scope         DirectoryScopeRef `json:"scope"`
		MessageID     model.ID          `json:"message_id"`
		OriginEventID model.ID          `json:"origin_event_id"`
		Step          int64             `json:"step"`
	}{
		Domain: "olivares.sessions.message-escalation-step.v1", Scope: scope,
		MessageID: command.MessageID, OriginEventID: command.OriginEventID, Step: command.Step,
	})
	if err != nil {
		return messageDerivedResult{}, err
	}
	normalized, err := s.normalizeDerived(ctx, scope, messageDerivedEscalate,
		messageDerivedCommandProjection{
			MessageID: command.MessageID, ExpectedVersion: command.ExpectedVersion,
			OriginEventID: command.OriginEventID, Step: command.Step, Recipient: command.Recipient,
		}, key)
	if err != nil {
		return messageDerivedResult{}, normalizeMessageDerivedError(err)
	}
	result, err := s.applyDerived(ctx, normalized, s.allocateDerivedIDs())
	return result, normalizeMessageDerivedError(err)
}

func cloneMessageDerivedAuthority(authority messageDerivedAuthority) messageDerivedAuthority {
	result := authority
	result.Facts = append([]store.AuthorizationFactRef(nil), authority.Facts...)
	result.CoreWitness = cloneCommunicationRequestAuthorityWitness(authority.CoreWitness)
	result.SenderGrantClosure = cloneDirectNoticeChannelGrantSubjectClosure(
		authority.SenderGrantClosure,
	)
	result.RecipientGrantClosure = cloneDirectNoticeChannelGrantSubjectClosure(
		authority.RecipientGrantClosure,
	)
	if authority.SenderResolution != nil {
		resolution := cloneDirectNoticePrincipalResolution(*authority.SenderResolution)
		result.SenderResolution = &resolution
	}
	return result
}

func messageDerivedSelector(recipient RecipientRef, required bool, wake WakePolicy) (AudienceSelector, error) {
	selector := AudienceSelector{Ref: recipient.Ref, Required: required, WakePolicy: wake}
	switch recipient.Kind {
	case RecipientUser:
		selector.Kind = AudienceUser
	case RecipientAgent:
		selector.Kind = AudienceAgent
	case RecipientSession:
		selector.Kind = AudienceSession
	default:
		return AudienceSelector{}, communicationError(
			ErrInvalidCommunicationModel, "derived Message recipient kind is invalid",
		)
	}
	if err := selector.Validate(); err != nil {
		return AudienceSelector{}, err
	}
	return selector, nil
}

func messageDerivedDirectSubject(recipient RecipientRef) (CommunicationSubjectRef, error) {
	subject := CommunicationSubjectRef{Ref: recipient.Ref}
	switch recipient.Kind {
	case RecipientUser:
		subject.Kind = SubjectUser
	case RecipientAgent:
		subject.Kind = SubjectAgent
	case RecipientSession:
		subject.Kind = SubjectSession
	default:
		return CommunicationSubjectRef{}, communicationError(
			ErrInvalidCommunicationModel, "derived Message recipient kind is invalid",
		)
	}
	if err := subject.Validate(); err != nil {
		return CommunicationSubjectRef{}, err
	}
	return subject, nil
}

func messageDerivedSenderSubject(authority messageDerivedAuthority) (CommunicationSubjectRef, error) {
	if authority.Actor.Kind == ActorSystem {
		subject := CommunicationSubjectRef{
			Kind: SubjectAgent, Ref: authority.Principal.SystemGrantAgentID.String(),
		}
		return subject, subject.Validate()
	}
	recipient := RecipientRef{Ref: authority.Actor.Ref}
	switch authority.Actor.Kind {
	case ActorUser:
		recipient.Kind = RecipientUser
	case ActorAgent:
		recipient.Kind = RecipientAgent
	case ActorSession:
		recipient.Kind = RecipientSession
	default:
		return CommunicationSubjectRef{}, communicationError(
			ErrInvalidCommunicationModel, "derived Message sender kind is invalid",
		)
	}
	return messageDerivedDirectSubject(recipient)
}

func closureContainsMessageDerivedSubject(
	closure ChannelGrantSubjectClosure,
	want CommunicationSubjectRef,
) bool {
	for _, subject := range closure.Subjects {
		if subject == want {
			return true
		}
	}
	return false
}

func validateMessageDerivedAuthority(
	scope DirectoryScopeRef,
	channel Channel,
	target RecipientRef,
	action messageDerivedAction,
	authority messageDerivedAuthority,
) error {
	if authority.Actor.Validate() != nil ||
		ValidateCommunicationPrincipalForScope(authority.Principal, scope) != nil ||
		authority.ObservedAt.IsZero() || !authority.FreshUntil.After(authority.ObservedAt) ||
		ValidateAuthorityEvidence(authority.ActionEvidence) != nil ||
		evidenceVerdict(authority.ActionEvidence) != VerdictClean ||
		ValidateReadWitness(authority.CoreWitness) != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message authority is unavailable",
		)
	}
	if action == messageDerivedEscalate {
		if authority.Actor != (CommunicationActorRef{Kind: ActorSystem, Ref: messageDerivedEscalatorRef}) ||
			!authority.Principal.System || authority.Principal.SystemActorRef != authority.Actor.Ref {
			return communicationError(
				ErrCommunicationForbidden, "overdue escalation requires the bounded system worker",
			)
		}
	} else if authority.Actor.Kind == ActorSystem {
		return communicationError(
			ErrCommunicationForbidden, "explicit Message reroute requires a non-system administrator",
		)
	}
	if authority.CoreWitness.Outcome != ReadAllow ||
		authority.CoreWitness.Principal != authority.Principal ||
		authority.CoreWitness.Operation != CommunicationMessageSend ||
		authority.CoreWitness.Entity != (EntityRef{
			TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			Kind: channelKind, ID: channel.ID,
		}) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message send witness crosses its Channel",
		)
	}
	senderSubject, err := messageDerivedSenderSubject(authority)
	if err != nil || authority.SenderGrantClosure.Scope != scope ||
		authority.SenderGrantClosure.Principal != authority.Principal ||
		!closureContainsMessageDerivedSubject(authority.SenderGrantClosure, senderSubject) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message sender grant closure is unavailable",
		)
	}
	targetSubject, err := messageDerivedDirectSubject(target)
	if err != nil || authority.RecipientGrantClosure.Scope != scope ||
		!closureContainsMessageDerivedSubject(authority.RecipientGrantClosure, targetSubject) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message recipient grant closure is unavailable",
		)
	}
	facts, err := CanonicalAuthorizationFacts(authority.Facts)
	if err != nil || len(facts) == 0 || !equalCommunicationAuthorityFacts(facts, authority.Facts) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message authority facts are unavailable",
		)
	}
	return nil
}

func (s *messageDerivedService) normalizeDerived(
	ctx context.Context,
	scope DirectoryScopeRef,
	action messageDerivedAction,
	command messageDerivedCommandProjection,
	idempotencyMaterial []byte,
) (messageDerivedNormalized, error) {
	if err := scope.Validate(); err != nil {
		return messageDerivedNormalized{}, err
	}
	if !validCanonicalCommunicationID(command.MessageID) || command.ExpectedVersion < 1 ||
		command.Recipient.Validate() != nil || len(idempotencyMaterial) == 0 {
		return messageDerivedNormalized{}, communicationError(
			ErrInvalidCommunicationModel, "invalid derived Message command",
		)
	}
	if action == messageDerivedEscalate {
		if !validCanonicalCommunicationID(command.OriginEventID) ||
			command.Step < 1 || command.Step > messageDerivedMaxStep {
			return messageDerivedNormalized{}, communicationError(
				ErrInvalidCommunicationModel, "invalid overdue escalation step",
			)
		}
	} else if action != messageDerivedReroute || command.OriginEventID != "" || command.Step != 0 {
		return messageDerivedNormalized{}, communicationError(
			ErrInvalidCommunicationModel, "invalid Message reroute lineage",
		)
	}
	authority, err := s.authorizer.AuthorizeMessageDerived(
		ctx, scope, command.MessageID, command.Recipient, action,
	)
	if err != nil {
		return messageDerivedNormalized{}, err
	}
	authority = cloneMessageDerivedAuthority(authority)
	var source Message
	var deliveries []MessageDelivery
	var channel Channel
	err = s.module.viewCommunication(ctx, scope, func(sc store.Scope) error {
		var readErr error
		source, deliveries, readErr = readMessageLifecycleCarrierView(ctx, sc, command.MessageID)
		if readErr != nil {
			return readErr
		}
		repo, readErr := sc.Ext(channelKind)
		if readErr != nil {
			return readErr
		}
		record, readErr := repo.Get(ctx, source.ChannelID)
		if readErr != nil {
			return readErr
		}
		channel, readErr = channelFromRecord(record)
		return readErr
	})
	if err != nil {
		return messageDerivedNormalized{}, err
	}
	if source.TenantID != scope.TenantID || source.WorkspaceID != scope.WorkspaceID ||
		channel.TenantID != scope.TenantID || channel.WorkspaceID != scope.WorkspaceID ||
		channel.ID != source.ChannelID || channel.State != ChannelActive ||
		channel.ContentProtection != ContentProtectionStorage {
		return messageDerivedNormalized{}, communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message source or Channel is unavailable",
		)
	}
	if action == messageDerivedReroute {
		if !oneOf(source.State, MessageRetracted, MessageExpired, MessageDiscarded) {
			return messageDerivedNormalized{}, communicationError(
				ErrInvalidCommunicationTransition,
				"Message reroute requires a terminal predecessor for immutable supersedes lineage",
			)
		}
	} else if source.State != MessagePublished {
		return messageDerivedNormalized{}, communicationError(
			ErrInvalidCommunicationTransition, "overdue escalation requires a published Message",
		)
	}
	if source.AutomationDepth > channel.MaxAutomationDepth-command.Step {
		return messageDerivedNormalized{}, communicationError(
			ErrInvalidCommunicationTransition, "derived Message exceeds the Channel automation ceiling",
		)
	}
	if err := validateMessageDerivedAuthority(scope, channel, command.Recipient, action, authority); err != nil {
		return messageDerivedNormalized{}, err
	}
	selector, err := messageDerivedSelector(
		command.Recipient, channel.DefaultAckPolicy != AckPolicyNone, channel.DefaultWake,
	)
	if err != nil {
		return messageDerivedNormalized{}, err
	}
	messageKind := source.Kind
	sourceKind := RouteSourceUserMessage
	eventType := ""
	commandScope := messageDerivedRerouteScope
	if action == messageDerivedEscalate {
		messageKind = MessageSystem
		sourceKind = RouteSourceSystemEvent
		eventType = messageLifecycleOverdueEvent
		commandScope = messageDerivedEscalateScope
	}
	audienceRequest := PublicationAudienceRequest{
		Scope: scope, ChannelID: channel.ID, ChannelACLRevision: channel.ACLRevision,
		RouteRevision: channel.RouteRevision, SubscriptionRevision: channel.SubscriptionRevision,
		MessageKind: messageKind, Urgency: source.Urgency, Sender: authority.Actor,
		SourceKind: sourceKind, EventType: eventType,
		LabelsJSON:         append([]byte(nil), source.LabelsJSON...),
		LabelsHash:         append([]byte(nil), source.LabelsHash...),
		ChannelDefaultWake: channel.DefaultWake, ContentProtection: channel.ContentProtection,
		ProtectionGeneration: channel.ProtectionGeneration, RequestedAt: authority.ObservedAt,
		Selectors: []AudienceSelector{selector},
	}
	if err := ValidatePublicationAudienceRequest(audienceRequest); err != nil {
		return messageDerivedNormalized{}, err
	}
	if s.module.communicationAudienceAttestor == nil {
		return messageDerivedNormalized{}, communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message audience attestor is unavailable",
		)
	}
	snapshot, attestation, err := s.module.communicationAudienceAttestor.AttestPublicationAudience(
		ctx, cloneDirectNoticePublicationAudienceRequest(audienceRequest),
	)
	if err != nil {
		return messageDerivedNormalized{}, communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message audience attestation failed",
		)
	}
	snapshot = cloneDirectNoticeDirectorySnapshot(snapshot)
	attestation = cloneDirectNoticePublicationAudienceAttestation(attestation)
	if err := validateDirectNoticeSnapshot(
		audienceRequest, snapshot, attestation, command.Recipient,
	); err != nil {
		return messageDerivedNormalized{}, communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message audience evidence is malformed",
		)
	}
	if authority.SenderGrantClosure.DirectoryEpoch != snapshot.Epoch ||
		authority.RecipientGrantClosure.DirectoryEpoch != snapshot.Epoch {
		return messageDerivedNormalized{}, communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message grant closures are stale",
		)
	}
	if err := validatePublishSender(
		authority.Principal, authority.Actor, authority.SenderResolution,
		scope, snapshot.Epoch, authority.ObservedAt,
	); err != nil {
		return messageDerivedNormalized{}, err
	}
	lockPreflight := directNoticePublishPreflight{
		Scope: scope, Principal: authority.Principal, Sender: authority.Actor, Channel: channel,
		AudienceRequest: audienceRequest, AudienceAttestation: attestation, Snapshot: snapshot,
		GrantClosure:          authority.SenderGrantClosure,
		RecipientGrantClosure: authority.RecipientGrantClosure,
		CoreWitness:           authority.CoreWitness,
	}
	facts, err := directNoticePublishAuthorityFacts(lockPreflight)
	if err != nil || !equalDirectNoticeAuthorityFacts(facts, authority.Facts) {
		return messageDerivedNormalized{}, communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message facts do not bind the audience epoch",
		)
	}
	sourceHash, err := canonicalMessageDerivedSourceHash(source, deliveries)
	if err != nil {
		return messageDerivedNormalized{}, err
	}
	actorFingerprint, err := messageLifecycleActorFingerprint(scope, authority.Actor)
	if err != nil {
		return messageDerivedNormalized{}, err
	}
	idempotencyHash := sha256.Sum256(idempotencyMaterial)
	requestBytes, err := canonicalJSON(struct {
		SchemaVersion int64                           `json:"schema_version"`
		Action        messageDerivedAction            `json:"action"`
		Scope         DirectoryScopeRef               `json:"scope"`
		Command       messageDerivedCommandProjection `json:"command"`
	}{1, action, scope, command})
	if err != nil {
		return messageDerivedNormalized{}, err
	}
	requestDigest := sha256.Sum256(requestBytes)
	return messageDerivedNormalized{
		action: action, command: command, scope: scope, authority: authority,
		source: source, sourceDeliveries: append([]MessageDelivery(nil), deliveries...), channel: channel,
		audienceRequest:     cloneDirectNoticePublicationAudienceRequest(audienceRequest),
		audienceAttestation: attestation, snapshot: snapshot, sourceHash: sourceHash,
		actorFingerprint: actorFingerprint, idempotencyKeyHash: idempotencyHash[:],
		requestDigest: requestDigest[:], commandScope: commandScope,
		automationDepth: source.AutomationDepth + command.Step,
		sourceKind:      sourceKind, eventType: eventType,
	}, nil
}

func normalizeMessageDerivedError(err error) error {
	if err == nil {
		return nil
	}
	if err == store.ErrNotFound {
		return communicationError(ErrCommunicationNotFound, "derived Message target is unavailable")
	}
	return fmt.Errorf("derived message: %w", err)
}
