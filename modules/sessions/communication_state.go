// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	maxMessageBlocks      = 64
	maxMessageReferences  = 64
	maxMessageBytes       = 64 * 1024
	maxMessageTextBytes   = 32 * 1024
	maxSealedPayloadBytes = 128 * 1024
)

var (
	ErrInvalidCommunicationModel      = errors.New("invalid communication model")
	ErrInvalidCommunicationTransition = errors.New("invalid communication transition")
	ErrCommunicationTerminal          = errors.New("communication entity is terminal")
	ErrCommunicationEvidenceUnknown   = errors.New("communication evidence unavailable")
	ErrCommunicationSnapshotStale     = errors.New("communication authority snapshot stale")
	ErrCommunicationForbidden         = errors.New("communication authority denied")
	ErrCommunicationNotFound          = errors.New("communication principal not found")
	// ErrCommunicationPlanChanged is the optimistic apply precondition. It is
	// deliberately distinct from store.ErrConflict: a stale semantic plan maps to
	// HTTP 412, while an idempotency key rebound to different intent remains 409.
	ErrCommunicationPlanChanged = errors.New("communication publish plan changed")
)

func communicationError(base error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", base, fmt.Sprintf(format, args...))
}

func oneOf[T ~string](value T, allowed ...T) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func (v ChannelKind) Valid() bool {
	return oneOf(v, ChannelCoordination, ChannelWork, ChannelIncident, ChannelAnnouncement, ChannelPrivate)
}

func (v ChannelState) Valid() bool { return oneOf(v, ChannelActive, ChannelArchived) }

func (v ChannelSensitivity) Valid() bool {
	return oneOf(v, ChannelInternal, ChannelRestricted)
}

func (v ContentProtection) Valid() bool {
	return oneOf(v, ContentProtectionStorage, ContentProtectionApplicationSealed)
}

func (v CommunicationSubjectKind) Valid() bool {
	return oneOf(v, SubjectUser, SubjectUserGroup, SubjectAgent, SubjectAgentGroup, SubjectSession)
}

func (v ChannelGrantState) Valid() bool {
	return oneOf(v, ChannelGrantActive, ChannelGrantRevoked, ChannelGrantExpired)
}

func (v ChannelGrantBit) Valid() bool {
	return oneOf(v, ChannelGrantRead, ChannelGrantWrite, ChannelGrantAdmin)
}

func (v ChannelSubscriptionMode) Valid() bool {
	return oneOf(v, SubscriptionAll, SubscriptionMentions, SubscriptionCritical, SubscriptionNone)
}

func (v WakePolicy) Valid() bool { return oneOf(v, WakeNone, WakePrimary, WakeAll, WakeInherit) }

func (v ChannelSubscriptionState) Valid() bool {
	return oneOf(v, SubscriptionActive, SubscriptionPaused, SubscriptionRevoked)
}

func (v ChannelLabelState) Valid() bool { return oneOf(v, ChannelLabelActive, ChannelLabelDisabled) }

func (v ChannelRouteSourceKind) Valid() bool {
	return oneOf(v, RouteSourceUserMessage, RouteSourceWorkEvent, RouteSourceSystemEvent, RouteSourceProtocol)
}

func (v ChannelRouteAudienceKind) Valid() bool {
	return oneOf(v, RouteAudienceSubscribers, RouteAudienceUserGroup, RouteAudienceAgentGroup,
		RouteAudienceWorkspaceMember)
}

func (v ChannelRouteState) Valid() bool { return oneOf(v, ChannelRouteActive, ChannelRouteDisabled) }

func (v CommunicationEndpointSupport) Valid() bool {
	return oneOf(v, EndpointStable, EndpointPreview, EndpointExperimental)
}

func (v CommunicationEndpointState) Valid() bool {
	return oneOf(v, EndpointActive, EndpointStale, EndpointDisabled)
}

func (v PayloadEncoding) Valid() bool { return oneOf(v, PayloadPlainJSON, PayloadSealedV1) }

func (v MessageContentBlockKind) Valid() bool {
	return oneOf(v, ContentBlockText, ContentBlockReference, ContentBlockStatus, ContentBlockActionRef)
}

func (v TextFormat) Valid() bool { return oneOf(v, TextPlain, TextMarkdown) }

func (v MessageKind) Valid() bool {
	return oneOf(v, MessageNotice, MessageAnnouncement, MessageRequest, MessageDecisionRequest,
		MessageHandoffOffer, MessageSystem)
}

func (v MessageState) Valid() bool {
	return oneOf(v, MessageDraft, MessagePublished, MessageRetracted, MessageExpired, MessageDiscarded)
}

func (v MessageUrgency) Valid() bool { return oneOf(v, UrgencyNormal, UrgencyHigh, UrgencyCritical) }

func (v AckPolicy) Valid() bool {
	return oneOf(v, AckPolicyNone, AckPolicyEachRequired, AckPolicyQuorum)
}

func (v AudienceSelectorKind) Valid() bool {
	return oneOf(v, AudienceUser, AudienceUserGroup, AudienceAgent, AudienceAgentGroup, AudienceSession,
		AudienceSubscribers, AudienceWorkspaceMembers)
}

func (v RecipientKind) Valid() bool { return oneOf(v, RecipientUser, RecipientAgent, RecipientSession) }

func (v CommunicationActorKind) Valid() bool {
	return oneOf(v, ActorUser, ActorAgent, ActorSession, ActorSystem)
}

func (v AudienceCausalKind) Valid() bool {
	return oneOf(v, CausalDirect, CausalUserGroup, CausalAgentGroup, CausalWorkspaceMember,
		CausalSubscriber)
}

func (v MessageDeliveryState) Valid() bool {
	return oneOf(v, DeliveryAvailable, DeliveryAcknowledged, DeliveryExpired, DeliveryRetracted,
		DeliveryUndeliverable)
}

func (v MailboxKind) Valid() bool { return oneOf(v, MailboxPersonal, MailboxChannel) }

func (v CursorBarrierCause) Valid() bool {
	return oneOf(v, BarrierNotYetAvailable, BarrierTemporarilyInvisible)
}

func (v CursorBarrierState) Valid() bool {
	return oneOf(v, CursorBarrierActive, CursorBarrierResolved)
}

func (v MessageAckKind) Valid() bool { return v == MessageAckReceived }

func (v CommunicationGuardKind) Valid() bool {
	return oneOf(v, CommunicationGuardDeliverySequence, CommunicationGuardRouteRevision)
}

func (v DecisionRequestState) Valid() bool {
	return oneOf(v, DecisionPending, DecisionAccepted, DecisionBlocked, DecisionResolved, DecisionRejected,
		DecisionCanceled, DecisionExpired)
}

func (v HandoffState) Valid() bool {
	return oneOf(v, HandoffOffered, HandoffAccepted, HandoffRejected, HandoffWithdrawn, HandoffExpired)
}

func (v DeliveryDispatchState) Valid() bool {
	return oneOf(v, DispatchPending, DispatchInFlight, DispatchSucceeded, DispatchFailed, DispatchUnknown,
		DispatchDeadLetter, DispatchSuperseded)
}

func (v DeliveryAttemptState) Valid() bool {
	return oneOf(v, AttemptReserved, AttemptFinished, AttemptAbandoned)
}

func (v TransmitBoundary) Valid() bool {
	return oneOf(v, TransmitNotCrossed, TransmitCrossed, TransmitUnknown)
}

func (v FulfillmentState) Valid() bool {
	return oneOf(v, FulfillmentNotRequired, FulfillmentPending, FulfillmentMet, FulfillmentUnmet)
}

func (v ReadOutcome) Valid() bool { return oneOf(v, ReadAllow, ReadDeny, ReadUnknown) }

func (v CommunicationOperation) Valid() bool {
	return oneOf(v, CommunicationRead, CommunicationDeliveryWrite, CommunicationDeliveryAdmin,
		CommunicationDecisionRequestWrite, CommunicationMessageSend, CommunicationHandoffResponse)
}

func (v PrincipalResolutionOutcome) Valid() bool {
	return oneOf(v, PrincipalResolved, PrincipalNotFound, PrincipalUnknown)
}

func validAssessmentVerdict(v AssessmentVerdict) bool {
	return oneOf(v, VerdictClean, VerdictBroken, VerdictUnknown)
}

func validCanonicalCommunicationID(id model.ID) bool {
	raw := id.String()
	parsed, err := uuid.Parse(raw)
	return err == nil && parsed.String() == raw && parsed.Version() == uuid.Version(7) &&
		parsed.Variant() == uuid.RFC4122
}

// validCanonicalCommunicationSID is deliberately stricter than the legacy K2
// SID parser: K3 references may only carry the canonical osn_<UUIDv7> form.
// This keeps opaque provider/session strings and older UUID variants out of
// authority-bearing communication rows.
func validCanonicalCommunicationSID(sid string) bool {
	const prefix = "osn_"
	if !strings.HasPrefix(sid, prefix) || len(sid) != len(prefix)+36 {
		return false
	}
	return validCanonicalCommunicationID(model.ID(strings.TrimPrefix(sid, prefix)))
}

func validCanonicalCommunicationTenant(tenant model.TenantID) bool {
	return validCanonicalCommunicationID(model.ID(tenant)) && tenant != model.SystemTenantID
}

func validateCommunicationEntity(entity CommunicationEntity) error {
	if !validCanonicalCommunicationID(entity.ID) || !validCanonicalCommunicationTenant(entity.TenantID) ||
		!validCanonicalCommunicationID(entity.WorkspaceID) || entity.Version < 1 || entity.CreatedAt.IsZero() {
		return communicationError(ErrInvalidCommunicationModel, "invalid entity identity or lineage")
	}
	return nil
}

func validateMutableCommunicationEntity(entity MutableCommunicationEntity) error {
	if err := validateCommunicationEntity(entity.CommunicationEntity); err != nil {
		return err
	}
	if entity.UpdatedAt.IsZero() || entity.UpdatedAt.Before(entity.CreatedAt) {
		return communicationError(ErrInvalidCommunicationModel, "invalid mutable timestamps")
	}
	return nil
}

func validateAppendOnlyCommunicationEntity(entity AppendOnlyCommunicationEntity) error {
	if err := validateCommunicationEntity(entity.CommunicationEntity); err != nil {
		return err
	}
	if entity.Version != 1 {
		return communicationError(ErrInvalidCommunicationModel,
			"append-only communication entity version must be one")
	}
	return nil
}

func validateOpaqueRef(ref string) bool {
	return boundedText(ref, 1, 512) && strings.TrimSpace(ref) == ref &&
		!strings.ContainsAny(ref, "\x00\r\n")
}

func (r CommunicationSubjectRef) Validate() error {
	if !r.Kind.Valid() {
		return communicationError(ErrInvalidCommunicationModel, "unknown subject kind %q", r.Kind)
	}
	if r.Kind == SubjectSession {
		if !validCanonicalCommunicationSID(r.Ref) {
			return communicationError(ErrInvalidCommunicationModel, "non-canonical session subject")
		}
		return nil
	}
	if !validCanonicalCommunicationID(model.ID(r.Ref)) {
		return communicationError(ErrInvalidCommunicationModel, "non-canonical subject ref")
	}
	return nil
}

func (r RecipientRef) Validate() error {
	if !r.Kind.Valid() {
		return communicationError(ErrInvalidCommunicationModel, "unknown recipient kind %q", r.Kind)
	}
	if r.Kind == RecipientSession {
		if !validCanonicalCommunicationSID(r.Ref) {
			return communicationError(ErrInvalidCommunicationModel, "non-canonical session recipient")
		}
		return nil
	}
	if !validCanonicalCommunicationID(model.ID(r.Ref)) {
		return communicationError(ErrInvalidCommunicationModel, "non-canonical recipient ref")
	}
	return nil
}

func (r CommunicationActorRef) Validate() error {
	if !r.Kind.Valid() {
		return communicationError(ErrInvalidCommunicationModel, "unknown actor kind %q", r.Kind)
	}
	switch r.Kind {
	case ActorSession:
		if !validCanonicalCommunicationSID(r.Ref) {
			return communicationError(ErrInvalidCommunicationModel, "non-canonical session actor")
		}
	case ActorSystem:
		if !validateOpaqueRef(r.Ref) {
			return communicationError(ErrInvalidCommunicationModel, "invalid system actor")
		}
	default:
		if !validCanonicalCommunicationID(model.ID(r.Ref)) {
			return communicationError(ErrInvalidCommunicationModel, "non-canonical actor ref")
		}
	}
	return nil
}

func (s AudienceSelector) Validate() error {
	if !s.Kind.Valid() || !s.WakePolicy.Valid() || s.WakePolicy == WakeInherit {
		return communicationError(ErrInvalidCommunicationModel, "invalid audience selector vocabulary")
	}
	switch s.Kind {
	case AudienceSubscribers, AudienceWorkspaceMembers:
		if s.Ref != "" {
			return communicationError(ErrInvalidCommunicationModel, "selector %q cannot carry ref", s.Kind)
		}
	case AudienceSession:
		if !validCanonicalCommunicationSID(s.Ref) {
			return communicationError(ErrInvalidCommunicationModel, "non-canonical session selector")
		}
	default:
		if !validCanonicalCommunicationID(model.ID(s.Ref)) {
			return communicationError(ErrInvalidCommunicationModel, "non-canonical selector ref")
		}
	}
	return nil
}

func (s DirectoryScopeRef) Validate() error {
	if !validCanonicalCommunicationTenant(s.TenantID) || !validCanonicalCommunicationID(s.WorkspaceID) {
		return communicationError(ErrInvalidCommunicationModel, "invalid server-derived directory scope")
	}
	return nil
}

// ValidateCommunicationPrincipal checks only server-authored identity shape. It
// deliberately cannot turn AgentExternalID into a canonical recipient.
func ValidateCommunicationPrincipal(principal CommunicationPrincipal) error {
	if principal.System {
		if principal.UserID != "" || principal.AgentExternalID != "" || principal.SessionID != "" ||
			principal.SessionRunRef != "" || principal.SessionFence != 0 ||
			principal.SessionWorkspaceID != "" || principal.PurposeRestricted ||
			!validateOpaqueRef(principal.SystemActorRef) ||
			!validCanonicalCommunicationID(principal.SystemGrantAgentID) {
			return communicationError(ErrInvalidCommunicationModel, "system principal carries user/session facts")
		}
		return nil
	}
	if principal.SystemActorRef != "" || principal.SystemGrantAgentID != "" {
		return communicationError(ErrInvalidCommunicationModel,
			"non-system principal carries system authority binding")
	}
	if principal.UserID != "" {
		if !validCanonicalCommunicationID(principal.UserID) || principal.AgentExternalID != "" ||
			principal.SessionID != "" || principal.SessionRunRef != "" || principal.SessionFence != 0 ||
			principal.SessionWorkspaceID != "" || principal.PurposeRestricted {
			return communicationError(ErrInvalidCommunicationModel, "invalid user principal shape")
		}
		return nil
	}
	if principal.SessionID != "" {
		if !validCanonicalCommunicationSID(principal.SessionID) ||
			!validCanonicalCommunicationID(principal.SessionWorkspaceID) ||
			!validCanonicalCommunicationID(model.ID(principal.SessionRunRef)) || principal.SessionFence < 1 ||
			!principal.PurposeRestricted ||
			(principal.AgentExternalID != "" && !validateOpaqueRef(principal.AgentExternalID)) {
			return communicationError(ErrInvalidCommunicationModel, "invalid communication-session binding")
		}
		return nil
	}
	if !validateOpaqueRef(principal.AgentExternalID) || principal.SessionRunRef != "" ||
		principal.SessionFence != 0 || principal.SessionWorkspaceID != "" || principal.PurposeRestricted {
		return communicationError(ErrInvalidCommunicationModel, "invalid unresolved agent principal shape")
	}
	return nil
}

// ValidateCommunicationPrincipalForScope applies the server-derived workspace
// ceiling. Ordinary users and unresolved agents carry no body-supplied
// workspace; a communication-session bearer is confined to its exact bound
// workspace.
func ValidateCommunicationPrincipalForScope(
	principal CommunicationPrincipal,
	scope DirectoryScopeRef,
) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if err := ValidateCommunicationPrincipal(principal); err != nil {
		return err
	}
	if principal.SessionID != "" && principal.SessionWorkspaceID != scope.WorkspaceID {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"communication-session principal crosses workspace scope")
	}
	return nil
}

// CanonicalPrincipalRecipient returns only identities already canonical in core
// auth. An agent external ID intentionally returns ok=false and must go through
// authoritative resolver evidence.
func CanonicalPrincipalRecipient(principal CommunicationPrincipal) (recipient RecipientRef, ok bool) {
	if ValidateCommunicationPrincipal(principal) != nil {
		return RecipientRef{}, false
	}
	switch {
	case principal.UserID != "":
		return RecipientRef{Kind: RecipientUser, Ref: principal.UserID.String()}, true
	case principal.SessionID != "":
		return RecipientRef{Kind: RecipientSession, Ref: principal.SessionID}, true
	default:
		return RecipientRef{}, false
	}
}

type directoryRosterHashInput struct {
	Recipient        RecipientRef                 `json:"recipient"`
	RecipientEpoch   int64                        `json:"recipient_epoch"`
	Eligible         bool                         `json:"eligible"`
	TombstoneKind    model.Kind                   `json:"tombstone_kind,omitempty"`
	TombstoneID      model.ID                     `json:"tombstone_id,omitempty"`
	TombstoneVersion int64                        `json:"tombstone_version,omitempty"`
	PrincipalKind    model.DirectoryPrincipalKind `json:"principal_kind,omitempty"`
	PrincipalRef     model.ID                     `json:"principal_ref,omitempty"`
	WorkspaceRef     model.ID                     `json:"workspace_ref,omitempty"`
	RetirementEpoch  int64                        `json:"retirement_epoch,omitempty"`
}

type directoryRosterDigestInput struct {
	Scope      DirectoryScopeRef          `json:"scope"`
	Epoch      int64                      `json:"epoch"`
	Recipients []directoryRosterHashInput `json:"recipients"`
}

func validateRecipientTombstone(snapshot RecipientSnapshot) error {
	if snapshot.Tombstone == nil {
		return nil
	}
	witness := *snapshot.Tombstone
	if snapshot.Eligible || snapshot.Recipient.Kind == RecipientSession ||
		witness.Principal.Validate() != nil || !validCanonicalCommunicationID(witness.TombstoneID) ||
		witness.TombstoneVersion != 1 || witness.RetirementEpoch < 1 ||
		witness.Principal.PrincipalRef.String() != snapshot.Recipient.Ref {
		return communicationError(ErrCommunicationEvidenceUnknown, "invalid recipient tombstone snapshot")
	}
	switch snapshot.Recipient.Kind {
	case RecipientUser:
		if witness.TombstoneKind != model.UserTombstoneKind ||
			witness.Principal.PrincipalKind != model.DirectoryPrincipalUser ||
			witness.Principal.WorkspaceRef != "" {
			return communicationError(ErrCommunicationEvidenceUnknown, "invalid User tombstone snapshot")
		}
	case RecipientAgent:
		if witness.TombstoneKind != model.DirectoryTombstoneKind {
			return communicationError(ErrCommunicationEvidenceUnknown, "invalid Agent tombstone kind")
		}
		switch witness.Principal.PrincipalKind {
		case model.DirectoryPrincipalIdentity:
			if witness.Principal.WorkspaceRef != "" {
				return communicationError(ErrCommunicationEvidenceUnknown,
					"Identity tombstone snapshot carries workspace")
			}
		case model.DirectoryPrincipalAgent:
			if witness.Principal.WorkspaceRef != snapshot.Scope.WorkspaceID {
				return communicationError(ErrCommunicationEvidenceUnknown,
					"Agent tombstone snapshot crosses workspace")
			}
		default:
			return communicationError(ErrCommunicationEvidenceUnknown, "invalid Agent tombstone principal")
		}
	}
	return nil
}

func validateRecipientSnapshot(snapshot RecipientSnapshot) error {
	if err := snapshot.Scope.Validate(); err != nil {
		return err
	}
	if snapshot.Recipient.Validate() != nil || snapshot.RecipientEpoch < 1 || snapshot.DirectoryEpoch < 1 {
		return communicationError(ErrInvalidCommunicationModel, "invalid recipient snapshot")
	}
	if err := validateRecipientTombstone(snapshot); err != nil {
		return err
	}
	if snapshot.Tombstone != nil && snapshot.Tombstone.RetirementEpoch > snapshot.DirectoryEpoch {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"recipient tombstone is newer than directory snapshot")
	}
	return nil
}

func ValidatePrincipalResolution(resolution PrincipalResolution) error {
	if !resolution.Outcome.Valid() || !boundedToken(resolution.Code, 128) ||
		resolution.ObservedAt.IsZero() || !resolution.FreshUntil.After(resolution.ObservedAt) ||
		resolution.Scope.Validate() != nil ||
		ValidateCommunicationPrincipalForScope(resolution.Principal, resolution.Scope) != nil {
		return communicationError(ErrInvalidCommunicationModel, "invalid principal resolution envelope")
	}
	if resolution.Outcome != PrincipalResolved {
		if resolution.Recipient != nil {
			return communicationError(ErrInvalidCommunicationModel,
				"unresolved principal carries canonical recipient")
		}
		return nil
	}
	if resolution.Recipient == nil || resolution.Recipient.Scope != resolution.Scope ||
		!resolution.Recipient.Eligible || resolution.Recipient.Tombstone != nil {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"resolved principal lacks current eligible recipient")
	}
	if err := validateRecipientSnapshot(*resolution.Recipient); err != nil {
		return err
	}
	if canonical, ok := CanonicalPrincipalRecipient(resolution.Principal); ok {
		if canonical != resolution.Recipient.Recipient {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"principal resolver changed canonical User/session identity")
		}
	} else if resolution.Principal.AgentExternalID == "" ||
		resolution.Recipient.Recipient.Kind != RecipientAgent {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"external AgentIdentity did not resolve to canonical Agent recipient")
	}
	return nil
}

// CanonicalDirectoryRosterHash binds canonical recipients, eligibility and
// typed retirement evidence without comparing opaque recipient epochs to
// retirement epochs.
func CanonicalDirectoryRosterHash(
	scope DirectoryScopeRef,
	epoch int64,
	recipients []RecipientSnapshot,
) ([]byte, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if epoch < 1 {
		return nil, communicationError(ErrCommunicationEvidenceUnknown, "directory epoch is below one")
	}
	ordered := append([]RecipientSnapshot(nil), recipients...)
	for _, recipient := range ordered {
		if recipient.Scope != scope || recipient.DirectoryEpoch != epoch {
			return nil, communicationError(ErrCommunicationEvidenceUnknown,
				"recipient is not bound to snapshot scope and epoch")
		}
		if err := validateRecipientSnapshot(recipient); err != nil {
			return nil, err
		}
		if recipient.Tombstone != nil && recipient.Tombstone.RetirementEpoch > epoch {
			return nil, communicationError(ErrCommunicationEvidenceUnknown,
				"recipient tombstone is newer than directory snapshot")
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Recipient.Kind != ordered[j].Recipient.Kind {
			return ordered[i].Recipient.Kind < ordered[j].Recipient.Kind
		}
		return ordered[i].Recipient.Ref < ordered[j].Recipient.Ref
	})
	hashInput := make([]directoryRosterHashInput, 0, len(ordered))
	for i, recipient := range ordered {
		if i > 0 && recipient.Recipient == ordered[i-1].Recipient {
			return nil, communicationError(ErrInvalidCommunicationModel, "duplicate recipient snapshot")
		}
		entry := directoryRosterHashInput{
			Recipient: recipient.Recipient, RecipientEpoch: recipient.RecipientEpoch, Eligible: recipient.Eligible,
		}
		if recipient.Tombstone != nil {
			entry.TombstoneKind = recipient.Tombstone.TombstoneKind
			entry.TombstoneID = recipient.Tombstone.TombstoneID
			entry.TombstoneVersion = recipient.Tombstone.TombstoneVersion
			entry.PrincipalKind = recipient.Tombstone.Principal.PrincipalKind
			entry.PrincipalRef = recipient.Tombstone.Principal.PrincipalRef
			entry.WorkspaceRef = recipient.Tombstone.Principal.WorkspaceRef
			entry.RetirementEpoch = recipient.Tombstone.RetirementEpoch
		}
		hashInput = append(hashInput, entry)
	}
	canonical, err := canonicalJSON(directoryRosterDigestInput{
		Scope: scope, Epoch: epoch, Recipients: hashInput,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(canonical)
	return digest[:], nil
}

func ValidatePublicationAudienceRequest(request PublicationAudienceRequest) error {
	if request.Scope.Validate() != nil || !validCanonicalCommunicationID(request.ChannelID) ||
		request.ChannelACLRevision < 1 || request.RouteRevision < 1 || request.SubscriptionRevision < 1 ||
		!request.MessageKind.Valid() || !request.Urgency.Valid() ||
		request.Sender.Validate() != nil || !request.SourceKind.Valid() ||
		!request.ChannelDefaultWake.Valid() || request.ChannelDefaultWake == WakeInherit ||
		!request.ContentProtection.Valid() || request.ProtectionGeneration < 1 || request.RequestedAt.IsZero() ||
		((len(request.LabelsJSON) == 0) != (len(request.LabelsHash) == 0)) ||
		(len(request.LabelsHash) != 0 && len(request.LabelsHash) != sha256.Size) ||
		len(request.Selectors) == 0 || len(request.Selectors) > 64 || len(request.MentionedRecipients) > 64 {
		return communicationError(ErrInvalidCommunicationModel, "invalid publication audience request")
	}
	if (request.SourceKind == RouteSourceUserMessage && request.EventType != "") ||
		(request.SourceKind != RouteSourceUserMessage && !boundedToken(request.EventType, 256)) {
		return communicationError(ErrInvalidCommunicationModel,
			"publication audience source and event type are inconsistent")
	}
	for _, selector := range request.Selectors {
		if err := selector.Validate(); err != nil {
			return err
		}
	}
	if len(request.LabelsJSON) != 0 {
		if _, err := validateCanonicalLabelMap(request.LabelsJSON); err != nil {
			return err
		}
		digest := sha256.Sum256(request.LabelsJSON)
		if !bytes.Equal(request.LabelsHash, digest[:]) {
			return communicationError(ErrInvalidCommunicationModel,
				"publication audience labels hash does not match canonical values")
		}
	}
	for index, recipient := range request.MentionedRecipients {
		if err := recipient.Validate(); err != nil {
			return err
		}
		if index > 0 {
			previous := request.MentionedRecipients[index-1]
			if previous.Kind > recipient.Kind ||
				(previous.Kind == recipient.Kind && previous.Ref >= recipient.Ref) {
				return communicationError(ErrInvalidCommunicationModel,
					"mentioned recipients are not canonical and unique")
			}
		}
	}
	return nil
}

func CanonicalPublicationAudienceRequestHash(request PublicationAudienceRequest) ([]byte, error) {
	if err := ValidatePublicationAudienceRequest(request); err != nil {
		return nil, err
	}
	raw, err := canonicalJSON(request)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func CanonicalPublicationAudienceSnapshotHash(snapshot DirectorySnapshot) ([]byte, error) {
	if err := ValidateDirectorySnapshot(snapshot); err != nil {
		return nil, err
	}
	raw, err := canonicalJSON(snapshot)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func validatePublicationAudienceAttestation(
	request PublicationAudienceRequest,
	snapshot DirectorySnapshot,
	attestation PublicationAudienceAttestation,
	dbNow time.Time,
) error {
	if err := ValidatePublicationAudienceRequest(request); err != nil {
		return err
	}
	if request.RequestedAt.After(snapshot.ObservedAt) {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"publication audience request postdates its attested snapshot")
	}
	if err := ValidateDirectorySnapshotForSelectors(snapshot, request.Selectors); err != nil {
		return err
	}
	if dbNow.IsZero() || snapshot.ObservedAt.After(dbNow) || dbNow.After(snapshot.FreshUntil) {
		return communicationError(ErrCommunicationSnapshotStale,
			"publication audience attestation is stale at mutation DB time")
	}
	requestHash, err := CanonicalPublicationAudienceRequestHash(request)
	if err != nil {
		return err
	}
	snapshotHash, err := CanonicalPublicationAudienceSnapshotHash(snapshot)
	if err != nil {
		return err
	}
	if attestation.Scope != request.Scope || attestation.Scope != snapshot.Scope ||
		attestation.DirectoryEpoch != snapshot.Epoch || attestation.ObservedAt != snapshot.ObservedAt ||
		attestation.FreshUntil != snapshot.FreshUntil ||
		!bytes.Equal(attestation.RequestHash, requestHash) ||
		!bytes.Equal(attestation.SnapshotHash, snapshotHash) ||
		ValidateAuthorityEvidence(attestation.Evidence) != nil ||
		evidenceVerdict(attestation.Evidence) != VerdictClean {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"publication audience attestation does not bind request and snapshot")
	}
	return nil
}

// ValidateDirectorySnapshot checks the tenant/workspace fence, canonical
// roster digest and every selector-to-recipient causal arc returned by the
// resolver. Freshness against DB time is rechecked by the later apply phase.
func ValidateDirectorySnapshot(snapshot DirectorySnapshot) error {
	if err := snapshot.Scope.Validate(); err != nil {
		return err
	}
	if snapshot.Epoch < 1 || snapshot.ObservedAt.IsZero() ||
		!snapshot.FreshUntil.After(snapshot.ObservedAt) || len(snapshot.RosterHash) != sha256.Size {
		return communicationError(ErrCommunicationEvidenceUnknown, "invalid directory snapshot envelope")
	}
	if len(snapshot.Selectors) == 0 || len(snapshot.Selectors) > 64 {
		return communicationError(ErrInvalidCommunicationModel, "directory snapshot lacks requested selectors")
	}
	for _, selector := range snapshot.Selectors {
		if err := selector.Validate(); err != nil {
			return err
		}
	}
	roster := make(map[RecipientRef]RecipientSnapshot, len(snapshot.Recipients))
	for _, recipient := range snapshot.Recipients {
		if recipient.Scope != snapshot.Scope {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"recipient snapshot crosses directory scope")
		}
		if err := validateRecipientSnapshot(recipient); err != nil {
			return err
		}
		if _, duplicate := roster[recipient.Recipient]; duplicate {
			return communicationError(ErrInvalidCommunicationModel, "duplicate recipient snapshot")
		}
		roster[recipient.Recipient] = recipient
	}
	wantHash, err := CanonicalDirectoryRosterHash(snapshot.Scope, snapshot.Epoch, snapshot.Recipients)
	if err != nil {
		return err
	}
	if !bytes.Equal(snapshot.RosterHash, wantHash) {
		return communicationError(ErrCommunicationEvidenceUnknown, "directory roster hash mismatch")
	}
	seen := make(map[resolvedAudienceArcKey]struct{}, len(snapshot.Contributions))
	selectorRecipients := make(map[int64]map[RecipientRef]struct{}, len(snapshot.Selectors))
	for _, contribution := range snapshot.Contributions {
		if contribution.SelectorOrdinal < 1 || contribution.SelectorOrdinal > int64(len(snapshot.Selectors)) ||
			contribution.Selector != snapshot.Selectors[contribution.SelectorOrdinal-1] ||
			contribution.Recipient.Scope != snapshot.Scope ||
			contribution.Recipient.DirectoryEpoch != snapshot.Epoch ||
			!contribution.Recipient.Eligible || contribution.Recipient.Tombstone != nil ||
			(contribution.Selector.Required && !contribution.Required) ||
			!contribution.WakePolicy.Valid() || contribution.WakePolicy == WakeInherit ||
			(contribution.RouteRuleID == "") != (contribution.RouteRuleGeneration == 0) ||
			(contribution.RouteRuleID != "" && (!validCanonicalCommunicationID(contribution.RouteRuleID) ||
				contribution.RouteRuleGeneration < 1)) {
			return communicationError(ErrInvalidCommunicationModel, "invalid resolved audience contribution")
		}
		if err := validateCanonicalRouteReasons(contribution.RouteReasons); err != nil {
			return err
		}
		rosterRecipient, present := roster[contribution.Recipient.Recipient]
		if !present || rosterRecipient.RecipientEpoch != contribution.Recipient.RecipientEpoch ||
			rosterRecipient.DirectoryEpoch != contribution.Recipient.DirectoryEpoch ||
			rosterRecipient.Eligible != contribution.Recipient.Eligible || rosterRecipient.Tombstone != nil {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"audience contribution is not bound to roster")
		}
		var factKind model.Kind
		var factID model.ID
		var factVersion int64
		if contribution.CausalFact != nil {
			factKind = contribution.CausalFact.Kind
			factID = contribution.CausalFact.ID
			factVersion = contribution.CausalFact.Version
		}
		if err := validateAudienceCausality(
			contribution.Selector, contribution.Recipient.Recipient, snapshot.Scope.WorkspaceID,
			contribution.CausalKind, contribution.CausalRef, factKind, factID, factVersion,
			contribution.ObservedSessionSID, contribution.ObservedClaimFence,
			contribution.OriginalSubscriber, contribution.SubscriptionID,
			contribution.SubscriptionGeneration,
		); err != nil {
			return err
		}
		key := resolvedAudienceArcKey{
			SelectorOrdinal: contribution.SelectorOrdinal,
			Arc:             resolvedAudienceCausalArcIdentity(contribution),
		}
		if _, duplicate := seen[key]; duplicate {
			return communicationError(ErrInvalidCommunicationModel,
				"duplicate selector-recipient causal arc")
		}
		seen[key] = struct{}{}
		if selectorRecipients[contribution.SelectorOrdinal] == nil {
			selectorRecipients[contribution.SelectorOrdinal] = make(map[RecipientRef]struct{})
		}
		selectorRecipients[contribution.SelectorOrdinal][contribution.Recipient.Recipient] = struct{}{}
	}
	for i, selector := range snapshot.Selectors {
		count := len(selectorRecipients[int64(i+1)])
		direct := oneOf(selector.Kind, AudienceUser, AudienceAgent, AudienceSession)
		if (selector.Required && count == 0) || (direct && count != 1) {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"selector %d has no authoritative recipient coverage", i+1)
		}
	}
	return nil
}

func ValidateDirectorySnapshotForSelectors(
	snapshot DirectorySnapshot,
	expected []AudienceSelector,
) error {
	if err := ValidateDirectorySnapshot(snapshot); err != nil {
		return err
	}
	if len(expected) != len(snapshot.Selectors) {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"directory snapshot selector count does not match request")
	}
	for i, selector := range expected {
		if err := selector.Validate(); err != nil {
			return err
		}
		if selector != snapshot.Selectors[i] {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"directory snapshot selector %d does not match request", i+1)
		}
	}
	return nil
}

func ValidateDirectorySnapshotAt(snapshot DirectorySnapshot, dbNow time.Time) error {
	if err := ValidateDirectorySnapshot(snapshot); err != nil {
		return err
	}
	if dbNow.IsZero() || snapshot.ObservedAt.After(dbNow) || dbNow.After(snapshot.FreshUntil) {
		return communicationError(ErrCommunicationSnapshotStale, "directory snapshot is not current at DB time")
	}
	return nil
}

// DirectorySnapshotAuthorityFact returns the allowlisted epoch witness that an
// apply transaction can hand to AuthoritySnapshotLocker. Per-recipient
// membership facts remain provenance; Channel and revision rows are locked by
// their dedicated K3 path.
func DirectorySnapshotAuthorityFact(snapshot DirectorySnapshot) (store.AuthorizationFactRef, error) {
	if err := ValidateDirectorySnapshot(snapshot); err != nil {
		return store.AuthorizationFactRef{}, err
	}
	return store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: model.ID(snapshot.Scope.TenantID), Version: snapshot.Epoch,
	}, nil
}

func ValidateContentAAD(aad ContentAAD) error {
	if !validCanonicalCommunicationTenant(aad.TenantID) || !validCanonicalCommunicationID(aad.WorkspaceID) ||
		!validCanonicalCommunicationID(aad.ChannelID) || !validCanonicalCommunicationID(aad.EntityID) ||
		!validateOpaqueRef(string(aad.EntityKind)) || !validateOpaqueRef(aad.Schema) ||
		aad.ProtectionGeneration < 1 {
		return communicationError(ErrInvalidCommunicationModel, "invalid protected-content AAD")
	}
	return nil
}

func ValidateProtectedPayload(payload ProtectedPayload) error {
	if !payload.Encoding.Valid() || !validateOpaqueRef(payload.Schema) || payload.ProtectionGeneration < 1 ||
		len(payload.Digest) != sha256.Size {
		return communicationError(ErrInvalidCommunicationModel, "invalid protected payload metadata")
	}
	switch payload.Encoding {
	case PayloadPlainJSON:
		if len(payload.PlainJSON) == 0 || payload.Sealed != nil || payload.SealKeyVersion != "" ||
			payload.DigestKeyVersion != "" || len(payload.PlainJSON) > maxMessageBytes {
			return communicationError(ErrInvalidCommunicationModel, "plain payload has sealed fields")
		}
		canonical, err := canonicalJSON(payload.PlainJSON)
		if err != nil || !bytes.Equal(canonical, payload.PlainJSON) {
			return communicationError(ErrInvalidCommunicationModel, "plain payload is not canonical JSON")
		}
		digest := sha256.Sum256(payload.PlainJSON)
		if !bytes.Equal(payload.Digest, digest[:]) {
			return communicationError(ErrInvalidCommunicationModel, "plain payload digest mismatch")
		}
	case PayloadSealedV1:
		if len(payload.PlainJSON) != 0 || payload.Sealed == nil || len(payload.Sealed.Ciphertext) == 0 ||
			len(payload.Sealed.Ciphertext) > maxSealedPayloadBytes ||
			!validateOpaqueRef(payload.SealKeyVersion) || !validateOpaqueRef(payload.DigestKeyVersion) ||
			payload.Sealed.KeyVersion != payload.SealKeyVersion {
			return communicationError(ErrInvalidCommunicationModel, "sealed payload envelope mismatch")
		}
	}
	return nil
}

// ProtectedPayloadSlot prevents a valid ciphertext/plain envelope from being
// replayed into a different human-content field of the same carrier.
type ProtectedPayloadSlot string

const (
	PayloadSlotMessage               ProtectedPayloadSlot = "message"
	PayloadSlotMessageTerminalReason ProtectedPayloadSlot = "message_terminal_reason"
	PayloadSlotAckNote               ProtectedPayloadSlot = "ack_note"
	PayloadSlotDecisionRequest       ProtectedPayloadSlot = "decision_request"
	PayloadSlotDecisionResponse      ProtectedPayloadSlot = "decision_response"
	PayloadSlotHandoff               ProtectedPayloadSlot = "handoff"
	PayloadSlotHandoffTerminalReason ProtectedPayloadSlot = "handoff_terminal_reason"
)

type CommunicationReasonContent struct {
	Code       string             `json:"code"`
	Text       string             `json:"text,omitempty"`
	References []ContentReference `json:"references,omitempty"`
}

type DecisionChoice struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type DecisionRequestContent struct {
	Question string           `json:"question"`
	Choices  []DecisionChoice `json:"choices"`
}

type DecisionResponseContent struct {
	ChoiceKey string                     `json:"choice_key,omitempty"`
	Reason    CommunicationReasonContent `json:"reason"`
}

type HandoffContent struct {
	Summary      string             `json:"summary"`
	NextAction   string             `json:"next_action"`
	Risk         string             `json:"risk,omitempty"`
	ArtifactRefs []ContentReference `json:"artifact_refs,omitempty"`
}

type ProtectedPayloadPolicy struct {
	Encoding             PayloadEncoding `json:"encoding"`
	ProtectionGeneration int64           `json:"protection_generation"`
}

func (slot ProtectedPayloadSlot) schema() (string, bool) {
	switch slot {
	case PayloadSlotMessage:
		return "communication.message.v1", true
	case PayloadSlotMessageTerminalReason:
		return "communication.message-terminal-reason.v1", true
	case PayloadSlotAckNote:
		return "communication.ack-note.v1", true
	case PayloadSlotDecisionRequest:
		return "communication.decision-request.v1", true
	case PayloadSlotDecisionResponse:
		return "communication.decision-response.v1", true
	case PayloadSlotHandoff:
		return "communication.handoff.v1", true
	case PayloadSlotHandoffTerminalReason:
		return "communication.handoff-terminal-reason.v1", true
	default:
		return "", false
	}
}

func validateContentReference(reference ContentReference) error {
	if !validateOpaqueRef(reference.Kind) || !validateOpaqueRef(reference.Ref) ||
		(reference.Hash != "" && !validateOpaqueRef(reference.Hash)) {
		return communicationError(ErrInvalidCommunicationModel, "invalid protected content reference")
	}
	return nil
}

func canonicalReasonContent(content CommunicationReasonContent) ([]byte, error) {
	if !boundedToken(content.Code, 128) ||
		(content.Text != "" && !boundedText(content.Text, 1, maxMessageTextBytes)) ||
		len(content.References) > maxMessageReferences {
		return nil, communicationError(ErrInvalidCommunicationModel, "invalid protected reason content")
	}
	for _, reference := range content.References {
		if err := validateContentReference(reference); err != nil {
			return nil, err
		}
	}
	return canonicalJSON(content)
}

func CanonicalProtectedPayloadSlot(slot ProtectedPayloadSlot, value any) ([]byte, error) {
	if _, ok := slot.schema(); !ok {
		return nil, communicationError(ErrInvalidCommunicationModel, "unknown protected payload slot")
	}
	var raw []byte
	var err error
	switch slot {
	case PayloadSlotMessage:
		content, ok := value.(MessageContent)
		if !ok {
			return nil, communicationError(ErrInvalidCommunicationModel, "Message slot has wrong content type")
		}
		raw, err = CanonicalMessageContent(content)
	case PayloadSlotMessageTerminalReason, PayloadSlotAckNote, PayloadSlotHandoffTerminalReason:
		content, ok := value.(CommunicationReasonContent)
		if !ok {
			return nil, communicationError(ErrInvalidCommunicationModel, "reason slot has wrong content type")
		}
		raw, err = canonicalReasonContent(content)
	case PayloadSlotDecisionRequest:
		content, ok := value.(DecisionRequestContent)
		if !ok || !boundedText(content.Question, 1, maxMessageTextBytes) ||
			len(content.Choices) < 1 || len(content.Choices) > 64 {
			return nil, communicationError(ErrInvalidCommunicationModel, "invalid decision request content")
		}
		seen := make(map[string]struct{}, len(content.Choices))
		for _, choice := range content.Choices {
			if !boundedToken(choice.Key, 128) || !boundedText(choice.Label, 1, 512) {
				return nil, communicationError(ErrInvalidCommunicationModel, "invalid decision choice")
			}
			if _, duplicate := seen[choice.Key]; duplicate {
				return nil, communicationError(ErrInvalidCommunicationModel, "duplicate decision choice")
			}
			seen[choice.Key] = struct{}{}
		}
		raw, err = canonicalJSON(content)
	case PayloadSlotDecisionResponse:
		content, ok := value.(DecisionResponseContent)
		if !ok || (content.ChoiceKey != "" && !boundedToken(content.ChoiceKey, 128)) {
			return nil, communicationError(ErrInvalidCommunicationModel, "invalid decision response content")
		}
		if _, reasonErr := canonicalReasonContent(content.Reason); reasonErr != nil {
			return nil, reasonErr
		}
		raw, err = canonicalJSON(content)
	case PayloadSlotHandoff:
		content, ok := value.(HandoffContent)
		if !ok || !boundedText(content.Summary, 1, maxMessageTextBytes) ||
			!boundedText(content.NextAction, 1, maxMessageTextBytes) ||
			(content.Risk != "" && !boundedText(content.Risk, 1, maxMessageTextBytes)) ||
			len(content.ArtifactRefs) > maxMessageReferences {
			return nil, communicationError(ErrInvalidCommunicationModel, "invalid Handoff content")
		}
		for _, reference := range content.ArtifactRefs {
			if err := validateContentReference(reference); err != nil {
				return nil, err
			}
		}
		raw, err = canonicalJSON(content)
	}
	if err != nil || len(raw) > maxMessageBytes {
		return nil, communicationError(ErrInvalidCommunicationModel, "protected payload slot exceeds limit")
	}
	return raw, nil
}

func validatePlainPayloadSlot(slot ProtectedPayloadSlot, raw json.RawMessage) error {
	var value any
	switch slot {
	case PayloadSlotMessage:
		value = &MessageContent{}
	case PayloadSlotMessageTerminalReason, PayloadSlotAckNote, PayloadSlotHandoffTerminalReason:
		value = &CommunicationReasonContent{}
	case PayloadSlotDecisionRequest:
		value = &DecisionRequestContent{}
	case PayloadSlotDecisionResponse:
		value = &DecisionResponseContent{}
	case PayloadSlotHandoff:
		value = &HandoffContent{}
	default:
		return communicationError(ErrInvalidCommunicationModel, "unknown protected payload slot")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return communicationError(ErrInvalidCommunicationModel, "invalid protected payload slot content")
	}
	var canonical []byte
	var err error
	switch typed := value.(type) {
	case *MessageContent:
		canonical, err = CanonicalProtectedPayloadSlot(slot, *typed)
	case *CommunicationReasonContent:
		canonical, err = CanonicalProtectedPayloadSlot(slot, *typed)
	case *DecisionRequestContent:
		canonical, err = CanonicalProtectedPayloadSlot(slot, *typed)
	case *DecisionResponseContent:
		canonical, err = CanonicalProtectedPayloadSlot(slot, *typed)
	case *HandoffContent:
		canonical, err = CanonicalProtectedPayloadSlot(slot, *typed)
	}
	if err != nil || !bytes.Equal(canonical, raw) {
		return communicationError(ErrInvalidCommunicationModel, "protected payload slot is not canonical")
	}
	return nil
}

func ValidateProtectedPayloadSlot(
	payload ProtectedPayload,
	slot ProtectedPayloadSlot,
	policy ProtectedPayloadPolicy,
) error {
	if err := ValidateProtectedPayload(payload); err != nil {
		return err
	}
	schema, ok := slot.schema()
	if !ok || payload.Schema != schema || !policy.Encoding.Valid() ||
		policy.ProtectionGeneration < 1 || payload.Encoding != policy.Encoding ||
		payload.ProtectionGeneration != policy.ProtectionGeneration {
		return communicationError(ErrInvalidCommunicationModel,
			"protected payload does not match slot and carrier protection")
	}
	if payload.Encoding == PayloadPlainJSON {
		return validatePlainPayloadSlot(slot, payload.PlainJSON)
	}
	return nil
}

func ProtectedPayloadPolicyForChannel(channel Channel) (ProtectedPayloadPolicy, error) {
	if err := ValidateChannel(channel); err != nil {
		return ProtectedPayloadPolicy{}, err
	}
	encoding := PayloadPlainJSON
	if channel.ContentProtection == ContentProtectionApplicationSealed {
		encoding = PayloadSealedV1
	}
	return ProtectedPayloadPolicy{
		Encoding: encoding, ProtectionGeneration: channel.ProtectionGeneration,
	}, nil
}

func protectedPayloadPolicyFrom(payload ProtectedPayload) ProtectedPayloadPolicy {
	return ProtectedPayloadPolicy{
		Encoding: payload.Encoding, ProtectionGeneration: payload.ProtectionGeneration,
	}
}

type ProtectedPayloadOpenPlan struct {
	Slot             ProtectedPayloadSlot `json:"slot"`
	AAD              ContentAAD           `json:"aad"`
	Encoding         PayloadEncoding      `json:"encoding"`
	PlainJSON        json.RawMessage      `json:"plain_json,omitempty"`
	Ciphertext       []byte               `json:"ciphertext,omitempty"`
	SealKeyVersion   string               `json:"seal_key_version,omitempty"`
	Digest           []byte               `json:"digest"`
	DigestKeyVersion string               `json:"digest_key_version,omitempty"`
	RequiresSealer   bool                 `json:"requires_sealer"`
}

// PlanProtectedPayloadOpen validates every value needed by WP-2 before any
// sealer/KMS I/O. It never opens bytes itself.
func PlanProtectedPayloadOpen(
	payload ProtectedPayload,
	slot ProtectedPayloadSlot,
	policy ProtectedPayloadPolicy,
	aad ContentAAD,
	lockedCarrierAAD ContentAAD,
) (ProtectedPayloadOpenPlan, error) {
	if err := ValidateProtectedPayloadSlot(payload, slot, policy); err != nil {
		return ProtectedPayloadOpenPlan{}, err
	}
	if err := ValidateContentAAD(aad); err != nil {
		return ProtectedPayloadOpenPlan{}, err
	}
	if err := ValidateContentAAD(lockedCarrierAAD); err != nil {
		return ProtectedPayloadOpenPlan{}, err
	}
	wantEntityKind := protectedPayloadEntityKind(slot)
	if aad != lockedCarrierAAD || aad.EntityKind != wantEntityKind || aad.Schema != payload.Schema ||
		aad.ProtectionGeneration != payload.ProtectionGeneration {
		return ProtectedPayloadOpenPlan{}, communicationError(ErrInvalidCommunicationModel,
			"protected payload AAD does not match slot carrier, schema or generation")
	}
	plan := ProtectedPayloadOpenPlan{
		Slot: slot, AAD: aad, Encoding: payload.Encoding,
		Digest: append([]byte(nil), payload.Digest...), DigestKeyVersion: payload.DigestKeyVersion,
	}
	if payload.Encoding == PayloadPlainJSON {
		plan.PlainJSON = append(json.RawMessage(nil), payload.PlainJSON...)
		return plan, nil
	}
	plan.Ciphertext = append([]byte(nil), payload.Sealed.Ciphertext...)
	plan.SealKeyVersion = payload.SealKeyVersion
	plan.RequiresSealer = true
	return plan, nil
}

func validateCanonicalJSONHash(raw json.RawMessage, digest []byte, allowEmpty bool) error {
	if len(raw) == 0 {
		if allowEmpty && len(digest) == 0 {
			return nil
		}
		return communicationError(ErrInvalidCommunicationModel, "JSON/hash pair is incomplete")
	}
	canonical, err := canonicalJSON(raw)
	if err != nil || !bytes.Equal(canonical, raw) || len(canonical) > maxMessageBytes ||
		len(digest) != sha256.Size {
		return communicationError(ErrInvalidCommunicationModel, "JSON/hash pair is not canonical")
	}
	want := sha256.Sum256(canonical)
	if !bytes.Equal(want[:], digest) {
		return communicationError(ErrInvalidCommunicationModel, "JSON/hash digest mismatch")
	}
	return nil
}

func ValidateChannel(channel Channel) error {
	if err := validateMutableCommunicationEntity(channel.MutableCommunicationEntity); err != nil {
		return err
	}
	if !boundedToken(channel.Slug, 128) || !boundedText(channel.Name, 1, 256) ||
		(channel.Description != "" && !boundedText(channel.Description, 1, 4096)) ||
		!channel.Kind.Valid() || !channel.State.Valid() || !channel.Sensitivity.Valid() ||
		!channel.ContentProtection.Valid() || channel.ProtectionGeneration < 1 ||
		!channel.DefaultAckPolicy.Valid() || !channel.DefaultWake.Valid() || channel.DefaultWake == WakeInherit ||
		channel.MaxFanout < 1 || channel.MaxAutomationDepth < 0 || channel.ACLRevision < 1 ||
		channel.RouteRevision < 1 || channel.SubscriptionRevision < 1 ||
		(channel.RetentionPolicyRef != "" && !validateOpaqueRef(channel.RetentionPolicyRef)) {
		return communicationError(ErrInvalidCommunicationModel, "invalid Channel shape")
	}
	if channel.Sensitivity == ChannelRestricted &&
		channel.ContentProtection != ContentProtectionApplicationSealed {
		return communicationError(ErrInvalidCommunicationModel, "restricted Channel is not application-sealed")
	}
	if (channel.DefaultAckPolicy == AckPolicyNone) != (channel.DefaultAckTimeoutMS == 0) ||
		channel.DefaultAckTimeoutMS < 0 {
		return communicationError(ErrInvalidCommunicationModel, "invalid Channel default Ack timeout")
	}
	return nil
}

// ValidateChannelUpdate closes the one-way privacy transitions. Historical
// messages retain their creation generation; this validator is for the Channel
// update CAS itself, not for reopening old carriers.
func ValidateChannelUpdate(before, after Channel) error {
	if err := ValidateChannel(before); err != nil {
		return err
	}
	if err := ValidateChannel(after); err != nil {
		return err
	}
	if after.ID != before.ID || after.TenantID != before.TenantID ||
		after.WorkspaceID != before.WorkspaceID || after.CreatedAt != before.CreatedAt ||
		after.Version != before.Version+1 || after.UpdatedAt.Before(before.UpdatedAt) ||
		after.Slug != before.Slug || after.Kind != before.Kind ||
		after.ACLRevision < before.ACLRevision || after.RouteRevision < before.RouteRevision ||
		after.SubscriptionRevision < before.SubscriptionRevision {
		return communicationError(ErrInvalidCommunicationModel, "Channel update crosses immutable lineage")
	}
	if before.State == ChannelArchived && after.State != before.State {
		return communicationError(ErrCommunicationTerminal, "archived Channel changed")
	}
	if before.State != after.State {
		if _, err := NextChannelState(before.State, after.State); err != nil {
			return err
		}
	}
	protectionChanged := before.Sensitivity != after.Sensitivity ||
		before.ContentProtection != after.ContentProtection
	if before.Sensitivity == ChannelRestricted && after.Sensitivity != ChannelRestricted {
		return communicationError(ErrInvalidCommunicationTransition, "Channel sensitivity decreased")
	}
	if before.ContentProtection == ContentProtectionApplicationSealed &&
		after.ContentProtection != ContentProtectionApplicationSealed {
		return communicationError(ErrInvalidCommunicationTransition, "Channel content protection decreased")
	}
	wantGeneration := before.ProtectionGeneration
	if protectionChanged {
		wantGeneration++
	}
	if after.ProtectionGeneration != wantGeneration {
		return communicationError(ErrInvalidCommunicationModel, "Channel protection generation mismatch")
	}
	return nil
}

func ValidateChannelGrant(grant ChannelGrant) error {
	if err := validateMutableCommunicationEntity(grant.MutableCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(grant.ChannelID) || grant.Subject.Validate() != nil ||
		grant.Generation < 1 || (!grant.CanRead && !grant.CanWrite && !grant.CanAdmin) ||
		!grant.State.Valid() || grant.GrantedBy.Validate() != nil ||
		(grant.ExpiresAt != nil && !grant.ExpiresAt.After(grant.CreatedAt)) ||
		(grant.SupersedesID != "" && (!validCanonicalCommunicationID(grant.SupersedesID) ||
			grant.SupersedesID == grant.ID)) ||
		((grant.Generation == 1) != (grant.SupersedesID == "")) {
		return communicationError(ErrInvalidCommunicationModel, "invalid ChannelGrant shape")
	}
	if (grant.State == ChannelGrantRevoked) != (grant.RevokedBy != nil) ||
		(grant.RevokedBy != nil && grant.RevokedBy.Validate() != nil) {
		return communicationError(ErrInvalidCommunicationModel, "invalid ChannelGrant terminal evidence")
	}
	if grant.State == ChannelGrantExpired &&
		(grant.ExpiresAt == nil || grant.UpdatedAt.Before(*grant.ExpiresAt)) {
		return communicationError(ErrInvalidCommunicationModel, "expired ChannelGrant lacks elapsed expiry")
	}
	return nil
}

func ValidateChannelSubscription(subscription ChannelSubscription) error {
	if err := validateMutableCommunicationEntity(subscription.MutableCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(subscription.ChannelID) || subscription.Subscriber.Validate() != nil ||
		subscription.Generation < 1 || !subscription.Mode.Valid() || !subscription.Wake.Valid() ||
		subscription.Wake == WakeInherit || !subscription.State.Valid() ||
		(subscription.SupersedesID != "" && (!validCanonicalCommunicationID(subscription.SupersedesID) ||
			subscription.SupersedesID == subscription.ID)) ||
		((subscription.Generation == 1) != (subscription.SupersedesID == "")) {
		return communicationError(ErrInvalidCommunicationModel, "invalid ChannelSubscription shape")
	}
	if err := validateCanonicalJSONHash(subscription.FilterJSON, subscription.FilterHash, true); err != nil {
		return err
	}
	if subscription.Mode == SubscriptionNone &&
		(subscription.Wake != WakeNone || subscription.RequiredForCritical) {
		return communicationError(ErrInvalidCommunicationModel, "disabled subscription carries delivery effects")
	}
	return nil
}

func ValidateChannelLabelDefinition(label ChannelLabelDefinition) error {
	if err := validateMutableCommunicationEntity(label.MutableCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(label.ChannelID) || !boundedToken(label.Key, 128) ||
		label.Generation < 1 || label.Classification != ChannelLabelNonSensitive || !label.State.Valid() {
		return communicationError(ErrInvalidCommunicationModel, "invalid ChannelLabelDefinition shape")
	}
	if err := validateCanonicalJSONHash(label.AllowedValuesJSON, label.ValuesHash, false); err != nil {
		return err
	}
	var values []string
	if err := json.Unmarshal(label.AllowedValuesJSON, &values); err != nil || len(values) == 0 || len(values) > 64 {
		return communicationError(ErrInvalidCommunicationModel, "label vocabulary is not a bounded array")
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if !boundedToken(value, 128) {
			return communicationError(ErrInvalidCommunicationModel, "invalid label vocabulary value")
		}
		if index > 0 && values[index-1] >= value {
			return communicationError(ErrInvalidCommunicationModel,
				"label vocabulary must be unique and canonically sorted")
		}
		if _, duplicate := seen[value]; duplicate {
			return communicationError(ErrInvalidCommunicationModel, "duplicate label vocabulary value")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func ValidateChannelRouteRule(route ChannelRouteRule) error {
	if err := validateMutableCommunicationEntity(route.MutableCommunicationEntity); err != nil {
		return err
	}
	if !boundedToken(route.RouteKey, 128) || route.Generation < 1 || route.Priority < 0 ||
		!route.SourceKind.Valid() || !validCanonicalCommunicationID(route.TargetChannelID) ||
		!route.AudienceKind.Valid() || !route.AckPolicy.Valid() || !route.WakePolicy.Valid() ||
		!route.State.Valid() ||
		(route.SupersedesID != "" && (!validCanonicalCommunicationID(route.SupersedesID) ||
			route.SupersedesID == route.ID)) ||
		((route.Generation == 1) != (route.SupersedesID == "")) {
		return communicationError(ErrInvalidCommunicationModel, "invalid ChannelRouteRule shape")
	}
	if route.EventType != "" && !boundedToken(route.EventType, 256) {
		return communicationError(ErrInvalidCommunicationModel, "invalid route event type")
	}
	if route.MessageKind != "" && !route.MessageKind.Valid() {
		return communicationError(ErrInvalidCommunicationModel, "invalid route Message kind")
	}
	if route.MinimumUrgency != "" && !route.MinimumUrgency.Valid() {
		return communicationError(ErrInvalidCommunicationModel, "invalid route urgency")
	}
	if route.CatchAll {
		if route.EventType != "" || route.MessageKind != "" || route.MinimumUrgency != "" ||
			len(route.LabelMatchJSON) != 0 {
			return communicationError(ErrInvalidCommunicationModel, "catch-all route carries matcher")
		}
	} else if route.SourceKind == RouteSourceUserMessage {
		if route.EventType != "" || route.MessageKind == "" {
			return communicationError(ErrInvalidCommunicationModel, "user-message route matcher is incomplete")
		}
	} else if route.EventType == "" || route.MessageKind != "" || route.MinimumUrgency != "" {
		return communicationError(ErrInvalidCommunicationModel, "event route matcher is incomplete")
	}
	if len(route.LabelMatchJSON) != 0 {
		labels, err := validateCanonicalLabelMap(route.LabelMatchJSON)
		if err != nil || len(labels) == 0 {
			return communicationError(ErrInvalidCommunicationModel, "invalid route label matcher")
		}
	}
	wantsRef := oneOf(route.AudienceKind, RouteAudienceUserGroup, RouteAudienceAgentGroup)
	if wantsRef != (route.AudienceRef != "") ||
		(route.AudienceRef != "" && !validCanonicalCommunicationID(model.ID(route.AudienceRef))) {
		return communicationError(ErrInvalidCommunicationModel, "invalid route audience ref")
	}
	if !route.CatchAll && route.EventType == "" && route.MessageKind == "" &&
		route.MinimumUrgency == "" && len(route.LabelMatchJSON) == 0 {
		return communicationError(ErrInvalidCommunicationModel, "empty route matcher is not catch-all")
	}
	return nil
}

func validateCanonicalLabelMap(raw json.RawMessage) (map[string]string, error) {
	canonical, err := canonicalJSON(raw)
	if err != nil || !bytes.Equal(canonical, raw) || len(canonical) > 8192 {
		return nil, communicationError(ErrInvalidCommunicationModel, "labels are not canonical JSON")
	}
	var labels map[string]string
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&labels); err != nil || labels == nil || len(labels) > 32 {
		return nil, communicationError(ErrInvalidCommunicationModel, "labels are not a bounded exact matcher")
	}
	for key, value := range labels {
		if !boundedToken(key, 128) || !boundedToken(value, 128) {
			return nil, communicationError(ErrInvalidCommunicationModel,
				"label keys and values must use bounded vocabulary tokens")
		}
	}
	return labels, nil
}

func ValidateCommunicationEndpoint(endpoint CommunicationEndpoint) error {
	if err := validateMutableCommunicationEntity(endpoint.MutableCommunicationEntity); err != nil {
		return err
	}
	if endpoint.Owner.Validate() != nil || !validCommunicationProviderKey(endpoint.ProviderKey) ||
		!boundedToken(endpoint.Transport, 128) || !validateOpaqueRef(endpoint.EndpointRef) ||
		!endpoint.SupportLevel.Valid() || endpoint.Priority < 0 || !endpoint.State.Valid() ||
		endpoint.Generation < 1 ||
		(endpoint.TransportFingerprint != "" && !validateOpaqueRef(endpoint.TransportFingerprint)) ||
		(endpoint.SecretRef != "" && !validateOpaqueRef(endpoint.SecretRef)) ||
		(endpoint.HeartbeatExpiresAt != nil && endpoint.HeartbeatExpiresAt.Before(endpoint.CreatedAt)) {
		return communicationError(ErrInvalidCommunicationModel, "invalid CommunicationEndpoint shape")
	}
	if (endpoint.Owner.Kind == RecipientSession) != (endpoint.SessionSID != "") ||
		(endpoint.SessionSID != "" && (endpoint.SessionSID != endpoint.Owner.Ref ||
			!validCanonicalCommunicationSID(endpoint.SessionSID))) {
		return communicationError(ErrInvalidCommunicationModel, "invalid endpoint session binding")
	}
	canonical, err := canonicalJSON(endpoint.CapabilitiesJSON)
	if err != nil || !bytes.Equal(canonical, endpoint.CapabilitiesJSON) || len(canonical) > maxMessageBytes {
		return communicationError(ErrInvalidCommunicationModel, "invalid endpoint capabilities")
	}
	return nil
}

func validCommunicationProviderKey(provider string) bool {
	if oneOf(provider, "claude-channel", "claude-stream-json", "codex-app-server", "a2a", "mcp") {
		return true
	}
	const prefix = "driver:"
	return strings.HasPrefix(provider, prefix) && boundedToken(strings.TrimPrefix(provider, prefix), 96)
}

func containsRawMarkdownHTML(text string) bool {
	for offset := 0; offset < len(text); offset++ {
		if text[offset] != '<' || offset+1 >= len(text) {
			continue
		}
		next := text[offset+1]
		candidate := (next >= 'A' && next <= 'Z') || (next >= 'a' && next <= 'z') ||
			next == '/' || next == '!' || next == '?'
		if !candidate {
			continue
		}
		end := strings.IndexByte(text[offset+1:], '>')
		if end < 0 {
			continue
		}
		inside := text[offset+1 : offset+1+end]
		if !strings.HasPrefix(inside, "http://") && !strings.HasPrefix(inside, "https://") &&
			!strings.HasPrefix(inside, "mailto:") {
			return true
		}
		offset += end + 1
	}
	return false
}

// CanonicalMessageContent validates the closed block grammar and returns the
// exact bytes to hash or seal. Markdown headings remain ordinary text bytes.
func CanonicalMessageContent(content MessageContent) ([]byte, error) {
	if !boundedText(content.Subject, 1, 256) || !utf8.ValidString(content.Subject) ||
		len(content.Blocks) < 1 || len(content.Blocks) > maxMessageBlocks {
		return nil, communicationError(ErrInvalidCommunicationModel, "invalid message subject or block count")
	}
	references := 0
	for i, block := range content.Blocks {
		if !block.Type.Valid() {
			return nil, communicationError(ErrInvalidCommunicationModel, "block %d has unknown type", i)
		}
		switch block.Type {
		case ContentBlockText:
			if !block.Format.Valid() || !boundedText(block.Text, 1, maxMessageTextBytes) ||
				block.Reference != nil || block.Code != "" ||
				(block.Format == TextMarkdown && containsRawMarkdownHTML(block.Text)) {
				return nil, communicationError(ErrInvalidCommunicationModel, "invalid text block %d", i)
			}
		case ContentBlockReference, ContentBlockActionRef:
			references++
			if block.Format != "" || block.Text != "" || block.Code != "" || block.Reference == nil ||
				!validateOpaqueRef(block.Reference.Kind) || !validateOpaqueRef(block.Reference.Ref) ||
				(block.Reference.Hash != "" && !validateOpaqueRef(block.Reference.Hash)) {
				return nil, communicationError(ErrInvalidCommunicationModel, "invalid reference block %d", i)
			}
		case ContentBlockStatus:
			if block.Format != "" || block.Text != "" || block.Reference != nil ||
				!boundedToken(block.Code, 128) {
				return nil, communicationError(ErrInvalidCommunicationModel, "invalid status block %d", i)
			}
		}
	}
	if references > maxMessageReferences {
		return nil, communicationError(ErrInvalidCommunicationModel, "too many message references")
	}
	canonical, err := canonicalJSON(content)
	if err != nil || len(canonical) > maxMessageBytes {
		return nil, communicationError(ErrInvalidCommunicationModel, "canonical message exceeds limit")
	}
	return canonical, nil
}

func ValidateMessage(message Message, requiredCount int64) error {
	if err := validateMutableCommunicationEntity(message.MutableCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(message.ChannelID) || !validCanonicalCommunicationID(message.ThreadID) ||
		!message.Kind.Valid() || !message.State.Valid() || !message.Urgency.Valid() || !message.AckPolicy.Valid() ||
		message.AvailableAt.IsZero() || message.AvailableAt.Before(message.CreatedAt) ||
		message.AutomationDepth < 0 || message.LastEventSeq < 0 {
		return communicationError(ErrInvalidCommunicationModel, "invalid message envelope")
	}
	if message.ReplyToID == "" {
		if message.ThreadID != message.ID {
			return communicationError(ErrInvalidCommunicationModel, "root Message thread must equal its ID")
		}
	} else if !validCanonicalCommunicationID(message.ReplyToID) || message.ReplyToID == message.ID {
		return communicationError(ErrInvalidCommunicationModel, "invalid Message reply parent")
	}
	if err := message.Sender.Validate(); err != nil {
		return err
	}
	if err := ValidateProtectedPayloadSlot(message.Payload, PayloadSlotMessage,
		protectedPayloadPolicyFrom(message.Payload)); err != nil {
		return err
	}
	if len(message.LabelsJSON) == 0 {
		if len(message.LabelsHash) != 0 {
			return communicationError(ErrInvalidCommunicationModel, "Message labels hash has no labels")
		}
	} else {
		_, err := validateCanonicalLabelMap(message.LabelsJSON)
		if err != nil || len(message.LabelsHash) != sha256.Size {
			return communicationError(ErrInvalidCommunicationModel, "invalid canonical Message labels")
		}
		labelsDigest := sha256.Sum256(message.LabelsJSON)
		if !bytes.Equal(labelsDigest[:], message.LabelsHash) {
			return communicationError(ErrInvalidCommunicationModel, "Message labels hash mismatch")
		}
	}
	for name, ref := range map[string]model.ID{
		"supersedes":   message.SupersedesID,
		"origin_event": message.OriginEventID,
	} {
		if ref != "" && (!validCanonicalCommunicationID(ref) || ref == message.ID) {
			return communicationError(ErrInvalidCommunicationModel, "invalid Message %s ref", name)
		}
	}
	if message.TerminalReason != nil {
		if err := ValidateProtectedPayloadSlot(*message.TerminalReason,
			PayloadSlotMessageTerminalReason, protectedPayloadPolicyFrom(message.Payload)); err != nil {
			return err
		}
	}
	if oneOf(message.Kind, MessageRequest, MessageDecisionRequest, MessageHandoffOffer) {
		if !validCanonicalCommunicationID(message.WorkItemID) {
			return communicationError(ErrInvalidCommunicationModel, "actionable message requires WorkItem")
		}
	} else if message.WorkItemID != "" && !validCanonicalCommunicationID(message.WorkItemID) {
		return communicationError(ErrInvalidCommunicationModel, "invalid optional WorkItem")
	}
	if (message.WorkItemID != "" && message.LastEventSeq != 0) ||
		(message.WorkItemID == "" && message.State == MessageDraft && message.LastEventSeq != 0) ||
		(message.WorkItemID == "" && message.State == MessagePublished && message.LastEventSeq < 1) ||
		(message.WorkItemID == "" && oneOf(message.State, MessageRetracted, MessageExpired) &&
			message.LastEventSeq < 2) ||
		(message.WorkItemID == "" && message.State == MessageDiscarded && message.LastEventSeq < 1) {
		return communicationError(ErrInvalidCommunicationModel, "invalid Message aggregate event sequence")
	}
	if message.AckDueAt != nil && message.AckDueAt.Before(message.AvailableAt) {
		return communicationError(ErrInvalidCommunicationModel, "ack deadline precedes availability")
	}
	if message.ExpiresAt != nil && !message.ExpiresAt.After(message.AvailableAt) {
		return communicationError(ErrInvalidCommunicationModel, "expiry does not follow availability")
	}
	if message.AckDueAt != nil && message.ExpiresAt != nil && message.AckDueAt.After(*message.ExpiresAt) {
		return communicationError(ErrInvalidCommunicationModel, "ack deadline follows expiry")
	}
	switch message.AckPolicy {
	case AckPolicyNone:
		if message.AckQuorum != 0 || message.AckDueAt != nil || requiredCount != 0 {
			return communicationError(ErrInvalidCommunicationModel, "none policy carries Ack requirements")
		}
	case AckPolicyEachRequired:
		if message.AckQuorum != 0 || message.AckDueAt == nil || requiredCount < 1 {
			return communicationError(ErrInvalidCommunicationModel, "invalid each-required policy")
		}
	case AckPolicyQuorum:
		if message.AckDueAt == nil || message.AckQuorum < 1 || message.AckQuorum > requiredCount {
			return communicationError(ErrInvalidCommunicationModel, "invalid quorum policy")
		}
	}
	terminal := oneOf(message.State, MessageRetracted, MessageExpired, MessageDiscarded)
	if terminal {
		if message.TerminalAt == nil || message.TerminalAt.Before(message.CreatedAt) ||
			message.TerminalAt.After(message.UpdatedAt) || !boundedToken(message.TerminalCode, 128) {
			return communicationError(ErrInvalidCommunicationModel,
				"terminal Message lacks bounded code and DB timestamp")
		}
	} else if message.TerminalAt != nil || message.TerminalCode != "" || message.TerminalReason != nil {
		return communicationError(ErrInvalidCommunicationModel, "non-terminal Message carries terminal evidence")
	}
	if oneOf(message.State, MessagePublished, MessageRetracted, MessageExpired) {
		if message.PublishedAt == nil || message.PublishedAt.Before(message.CreatedAt) ||
			message.PublishedAt.After(message.UpdatedAt) {
			return communicationError(ErrInvalidCommunicationModel, "published Message lacks DB timestamp")
		}
		if len(message.AudienceHash) != sha256.Size {
			return communicationError(ErrInvalidCommunicationModel, "published Message lacks sealed audience hash")
		}
	} else if message.PublishedAt != nil {
		return communicationError(ErrInvalidCommunicationModel, "unpublished Message carries publish timestamp")
	} else if len(message.AudienceHash) != 0 {
		return communicationError(ErrInvalidCommunicationModel, "unpublished Message carries audience hash")
	}
	if message.PublishedAt != nil && message.TerminalAt != nil && message.TerminalAt.Before(*message.PublishedAt) {
		return communicationError(ErrInvalidCommunicationModel, "Message terminal time precedes publication")
	}
	if message.State == MessageExpired &&
		(message.ExpiresAt == nil || message.TerminalAt == nil || message.TerminalAt.Before(*message.ExpiresAt)) {
		return communicationError(ErrInvalidCommunicationModel, "Message expired before expires_at")
	}
	return nil
}

// ValidateMessageForPublishChannel binds creation metadata against the locked
// Channel generation. It must not be used to reopen historical messages after
// a Channel upgrades from storage to application_sealed.
func ValidateMessageForPublishChannel(message Message, channel Channel, requiredCount int64) error {
	if err := ValidateMessage(message, requiredCount); err != nil {
		return err
	}
	if err := ValidateChannel(channel); err != nil {
		return err
	}
	if message.TenantID != channel.TenantID || message.WorkspaceID != channel.WorkspaceID ||
		message.ChannelID != channel.ID ||
		message.Payload.ProtectionGeneration != channel.ProtectionGeneration {
		return communicationError(ErrInvalidCommunicationModel,
			"Message payload is not bound to exact Channel protection generation")
	}
	wantEncoding := PayloadPlainJSON
	if channel.ContentProtection == ContentProtectionApplicationSealed {
		wantEncoding = PayloadSealedV1
	}
	if message.Payload.Encoding != wantEncoding {
		return communicationError(ErrInvalidCommunicationModel,
			"Message payload encoding does not match Channel protection")
	}
	policy, err := ProtectedPayloadPolicyForChannel(channel)
	if err != nil {
		return err
	}
	if err := ValidateProtectedPayloadSlot(message.Payload, PayloadSlotMessage, policy); err != nil {
		return err
	}
	return nil
}

type ChannelLabelSnapshot struct {
	Scope           DirectoryScopeRef        `json:"scope"`
	ChannelID       model.ID                 `json:"channel_id"`
	RouteRevision   int64                    `json:"route_revision"`
	Definitions     []ChannelLabelDefinition `json:"definitions"`
	ObservedAt      time.Time                `json:"observed_at"`
	FreshUntil      time.Time                `json:"fresh_until,omitempty"`
	SameTransaction bool                     `json:"same_transaction"`
	Evidence        AuthorityEvidence        `json:"evidence"`
}

// ValidateMessageLabelsForPublish proves that every published label came from
// the current active non-sensitive admin vocabulary. Canonical JSON/hash alone
// is not authorization to introduce a label value.
func ValidateMessageLabelsForPublish(
	message Message,
	channel Channel,
	requiredCount int64,
	snapshot ChannelLabelSnapshot,
	dbNow time.Time,
) error {
	if err := ValidateMessageForPublishChannel(message, channel, requiredCount); err != nil {
		return err
	}
	scope := DirectoryScopeRef{TenantID: channel.TenantID, WorkspaceID: channel.WorkspaceID}
	current := snapshot.SameTransaction && snapshot.ObservedAt == dbNow && snapshot.FreshUntil.IsZero()
	if !snapshot.SameTransaction {
		current = !snapshot.ObservedAt.IsZero() && snapshot.FreshUntil.After(snapshot.ObservedAt) &&
			!snapshot.ObservedAt.After(dbNow) && !dbNow.After(snapshot.FreshUntil)
	}
	if snapshot.Scope != scope || snapshot.ChannelID != channel.ID ||
		snapshot.RouteRevision != channel.RouteRevision || !current ||
		ValidateAuthorityEvidence(snapshot.Evidence) != nil || evidenceVerdict(snapshot.Evidence) != VerdictClean {
		return communicationError(ErrCommunicationEvidenceUnknown, "label definition snapshot is not current")
	}
	labels := map[string]string{}
	if len(message.LabelsJSON) != 0 {
		if err := json.Unmarshal(message.LabelsJSON, &labels); err != nil {
			return communicationError(ErrInvalidCommunicationModel, "Message labels are not string values")
		}
	}
	definitions := make(map[string]map[string]struct{}, len(snapshot.Definitions))
	for _, definition := range snapshot.Definitions {
		if err := ValidateChannelLabelDefinition(definition); err != nil {
			return err
		}
		if definition.TenantID != scope.TenantID || definition.WorkspaceID != scope.WorkspaceID ||
			definition.ChannelID != channel.ID || definition.State != ChannelLabelActive {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"label definition crosses Channel snapshot")
		}
		if _, duplicate := definitions[definition.Key]; duplicate {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"label snapshot has multiple active generations")
		}
		var values []string
		if err := json.Unmarshal(definition.AllowedValuesJSON, &values); err != nil {
			return err
		}
		allowed := make(map[string]struct{}, len(values))
		for _, value := range values {
			allowed[value] = struct{}{}
		}
		definitions[definition.Key] = allowed
	}
	for key, value := range labels {
		allowed, present := definitions[key]
		if !present {
			return communicationError(ErrInvalidCommunicationModel, "Message uses undefined label %q", key)
		}
		if _, present := allowed[value]; !present {
			return communicationError(ErrInvalidCommunicationModel, "Message uses disallowed label value")
		}
	}
	return nil
}

// ValidateChannelRouteRuleLabels proves that an exact route matcher uses the
// same active non-sensitive vocabulary as the locked target Channel.
func ValidateChannelRouteRuleLabels(
	route ChannelRouteRule,
	channel Channel,
	snapshot ChannelLabelSnapshot,
	dbNow time.Time,
) error {
	if err := ValidateChannelRouteRule(route); err != nil {
		return err
	}
	if err := ValidateChannel(channel); err != nil {
		return err
	}
	if route.TenantID != channel.TenantID || route.WorkspaceID != channel.WorkspaceID ||
		route.TargetChannelID != channel.ID {
		return communicationError(ErrInvalidCommunicationModel,
			"route label matcher crosses target Channel")
	}
	if len(route.LabelMatchJSON) == 0 {
		return nil
	}
	labels, err := validateCanonicalLabelMap(route.LabelMatchJSON)
	if err != nil {
		return err
	}
	scope := DirectoryScopeRef{TenantID: channel.TenantID, WorkspaceID: channel.WorkspaceID}
	current := snapshot.SameTransaction && snapshot.ObservedAt == dbNow && snapshot.FreshUntil.IsZero()
	if !snapshot.SameTransaction {
		current = !snapshot.ObservedAt.IsZero() && snapshot.FreshUntil.After(snapshot.ObservedAt) &&
			!snapshot.ObservedAt.After(dbNow) && !dbNow.After(snapshot.FreshUntil)
	}
	if snapshot.Scope != scope || snapshot.ChannelID != channel.ID ||
		snapshot.RouteRevision != channel.RouteRevision || !current ||
		ValidateAuthorityEvidence(snapshot.Evidence) != nil || evidenceVerdict(snapshot.Evidence) != VerdictClean {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"route label definition snapshot is not current")
	}
	definitions := make(map[string]map[string]struct{}, len(snapshot.Definitions))
	for _, definition := range snapshot.Definitions {
		if err := ValidateChannelLabelDefinition(definition); err != nil {
			return err
		}
		if definition.TenantID != scope.TenantID || definition.WorkspaceID != scope.WorkspaceID ||
			definition.ChannelID != channel.ID || definition.State != ChannelLabelActive {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"route label definition crosses Channel snapshot")
		}
		if _, duplicate := definitions[definition.Key]; duplicate {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"route label snapshot has multiple active generations")
		}
		var values []string
		if err := json.Unmarshal(definition.AllowedValuesJSON, &values); err != nil {
			return err
		}
		allowed := make(map[string]struct{}, len(values))
		for _, value := range values {
			allowed[value] = struct{}{}
		}
		definitions[definition.Key] = allowed
	}
	for key, value := range labels {
		allowed, present := definitions[key]
		if !present {
			return communicationError(ErrInvalidCommunicationModel,
				"route uses undefined label %q", key)
		}
		if _, present := allowed[value]; !present {
			return communicationError(ErrInvalidCommunicationModel,
				"route uses value outside registered label vocabulary")
		}
	}
	return nil
}

// ValidateMessageReplyLineage rejects thread splicing. The parent determines
// every lineage field, including the exact optional WorkItem: a reply to a
// standalone Message must remain standalone.
func ValidateMessageReplyLineage(parent, reply Message) error {
	if !validCanonicalCommunicationID(parent.ID) || !validCanonicalCommunicationID(reply.ID) ||
		parent.ID == reply.ID || reply.ReplyToID != parent.ID ||
		parent.TenantID != reply.TenantID || parent.WorkspaceID != reply.WorkspaceID ||
		parent.ChannelID != reply.ChannelID || parent.ThreadID != reply.ThreadID ||
		parent.WorkItemID != reply.WorkItemID {
		return communicationError(ErrInvalidCommunicationModel,
			"reply does not inherit parent tenant, workspace, channel, thread and WorkItem")
	}
	if !validCanonicalCommunicationTenant(parent.TenantID) ||
		!validCanonicalCommunicationID(parent.WorkspaceID) ||
		!validCanonicalCommunicationID(parent.ChannelID) ||
		!validCanonicalCommunicationID(parent.ThreadID) ||
		(parent.WorkItemID != "" && !validCanonicalCommunicationID(parent.WorkItemID)) {
		return communicationError(ErrInvalidCommunicationModel, "invalid reply parent lineage")
	}
	return nil
}

// ValidateMessageDeliveryLineage closes the cross-row state and deadline
// invariants used by cursor, fulfillment and later store adapters.
func ValidateMessageDeliveryLineage(message Message, delivery MessageDelivery) error {
	if delivery.TenantID != message.TenantID || delivery.WorkspaceID != message.WorkspaceID ||
		delivery.MessageID != message.ID || delivery.AvailableAt != message.AvailableAt ||
		!equalOptionalTime(delivery.ExpiresAt, message.ExpiresAt) ||
		(delivery.Required && !equalOptionalTime(delivery.AckDueAt, message.AckDueAt)) ||
		(message.AckPolicy == AckPolicyNone && delivery.AckDueAt != nil) {
		return communicationError(ErrInvalidCommunicationModel, "Delivery crosses Message lineage")
	}
	if oneOf(message.State, MessageDraft, MessageDiscarded) {
		return communicationError(ErrInvalidCommunicationModel, "unpublished Message carries Delivery")
	}
	if (message.State == MessagePublished && delivery.State == DeliveryRetracted) ||
		(message.State == MessageRetracted && delivery.State == DeliveryAvailable) ||
		(message.State == MessageExpired && oneOf(delivery.State, DeliveryAvailable, DeliveryRetracted)) {
		return communicationError(ErrInvalidCommunicationModel, "terminal Message retains available Delivery")
	}
	return nil
}

func equalOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func ValidateDecisionRequestLineage(message Message, request DecisionRequest) error {
	if message.TenantID != request.TenantID || message.WorkspaceID != request.WorkspaceID ||
		message.ID != request.MessageID || message.WorkItemID != request.WorkItemID ||
		message.Kind != MessageDecisionRequest ||
		(message.ExpiresAt != nil && message.ExpiresAt.Before(request.DueAt)) {
		return communicationError(ErrInvalidCommunicationModel,
			"DecisionRequest does not match exact Message/WorkItem/deadline lineage")
	}
	if err := ValidateProtectedPayloadSlot(request.Request, PayloadSlotDecisionRequest,
		protectedPayloadPolicyFrom(message.Payload)); err != nil {
		return err
	}
	return nil
}

func ValidateHandoffLineage(
	message Message,
	delivery MessageDelivery,
	handoff Handoff,
	requiredDeliveryCount int64,
) error {
	if err := ValidateMessageDeliveryLineage(message, delivery); err != nil {
		return err
	}
	if message.TenantID != handoff.TenantID || message.WorkspaceID != handoff.WorkspaceID ||
		message.ID != handoff.MessageID || message.WorkItemID != handoff.WorkItemID ||
		message.Kind != MessageHandoffOffer || delivery.ID != handoff.DeliveryID ||
		delivery.Recipient != handoff.To || !delivery.Required || requiredDeliveryCount != 1 ||
		(message.ExpiresAt != nil && message.ExpiresAt.Before(handoff.AckDeadline)) {
		return communicationError(ErrInvalidCommunicationModel,
			"Handoff does not match exact Message/Delivery/target/deadline lineage")
	}
	if err := ValidateProtectedPayloadSlot(handoff.Payload, PayloadSlotHandoff,
		protectedPayloadPolicyFrom(message.Payload)); err != nil {
		return err
	}
	return nil
}

func hasRetirementEvidence(delivery MessageDelivery) bool {
	return delivery.RetirementTombstoneKind != "" || delivery.RetirementTombstoneID != "" ||
		delivery.RetirementTombstoneVersion != 0 || delivery.RetirementEpoch != 0 ||
		delivery.UndeliverableAt != nil || delivery.UndeliverableCode != ""
}

func ValidateMessageDelivery(delivery MessageDelivery) error {
	if err := validateMutableCommunicationEntity(delivery.MutableCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(delivery.MessageID) || delivery.Recipient.Validate() != nil ||
		delivery.RecipientEpoch < 1 || delivery.DeliverySeq < 1 || !delivery.WakePolicy.Valid() ||
		delivery.WakePolicy == WakeInherit || !delivery.State.Valid() || delivery.AvailableAt.IsZero() ||
		delivery.AvailableAt.Before(delivery.CreatedAt) {
		return communicationError(ErrInvalidCommunicationModel, "invalid delivery envelope")
	}
	if err := validateCanonicalRouteReasons(delivery.RouteReasons); err != nil {
		return err
	}
	if delivery.Required && delivery.AckDueAt == nil {
		return communicationError(ErrInvalidCommunicationModel, "required delivery lacks Ack deadline")
	}
	if delivery.AckDueAt != nil && delivery.AckDueAt.Before(delivery.AvailableAt) {
		return communicationError(ErrInvalidCommunicationModel, "delivery Ack deadline precedes availability")
	}
	if delivery.ExpiresAt != nil && !delivery.ExpiresAt.After(delivery.AvailableAt) {
		return communicationError(ErrInvalidCommunicationModel, "delivery expiry does not follow availability")
	}
	if delivery.AckDueAt != nil && delivery.ExpiresAt != nil && delivery.AckDueAt.After(*delivery.ExpiresAt) {
		return communicationError(ErrInvalidCommunicationModel, "delivery Ack deadline follows expiry")
	}
	if delivery.FirstSeenAt != nil && (delivery.FirstSeenAt.Before(delivery.AvailableAt) ||
		delivery.FirstSeenAt.Before(delivery.CreatedAt) || delivery.FirstSeenAt.After(delivery.UpdatedAt)) {
		return communicationError(ErrInvalidCommunicationModel, "invalid Delivery first-seen time")
	}
	wakePresent := delivery.LastWakeVerdict != "" || delivery.LastWakeCode != "" || delivery.LastWakeAt != nil
	if wakePresent {
		if !validAssessmentVerdict(delivery.LastWakeVerdict) || !boundedToken(delivery.LastWakeCode, 128) ||
			delivery.LastWakeAt == nil || delivery.LastWakeAt.Before(delivery.AvailableAt) ||
			delivery.LastWakeAt.Before(delivery.CreatedAt) || delivery.LastWakeAt.After(delivery.UpdatedAt) {
			return communicationError(ErrInvalidCommunicationModel, "invalid Delivery wake evidence")
		}
	}
	linked := delivery.AckID != "" || delivery.AcknowledgedAt != nil
	switch delivery.State {
	case DeliveryAvailable:
		if linked || hasRetirementEvidence(delivery) {
			return communicationError(ErrInvalidCommunicationModel, "available delivery carries terminal evidence")
		}
	case DeliveryAcknowledged:
		if !validCanonicalCommunicationID(delivery.AckID) || delivery.AcknowledgedAt == nil ||
			delivery.AcknowledgedAt.IsZero() || delivery.AcknowledgedAt.Before(delivery.CreatedAt) ||
			delivery.AcknowledgedAt.Before(delivery.AvailableAt) ||
			delivery.AcknowledgedAt.After(delivery.UpdatedAt) ||
			(delivery.AckDueAt != nil && delivery.AcknowledgedAt.After(*delivery.AckDueAt)) ||
			hasRetirementEvidence(delivery) {
			return communicationError(ErrInvalidCommunicationModel, "invalid effective Ack")
		}
	case DeliveryExpired, DeliveryRetracted:
		if linked || hasRetirementEvidence(delivery) {
			return communicationError(ErrInvalidCommunicationModel, "late Ack linked to terminal delivery")
		}
	case DeliveryUndeliverable:
		wantTombstoneKind := model.UserTombstoneKind
		if delivery.Recipient.Kind == RecipientAgent {
			wantTombstoneKind = model.DirectoryTombstoneKind
		}
		if delivery.Recipient.Kind == RecipientSession || linked ||
			!validCanonicalCommunicationID(delivery.RetirementTombstoneID) ||
			delivery.RetirementTombstoneKind != wantTombstoneKind || delivery.RetirementTombstoneVersion != 1 ||
			delivery.RetirementEpoch < 1 || delivery.UndeliverableAt == nil ||
			delivery.UndeliverableAt.Before(delivery.CreatedAt) ||
			delivery.UndeliverableAt.After(delivery.UpdatedAt) || !boundedToken(delivery.UndeliverableCode, 128) {
			return communicationError(ErrInvalidCommunicationModel, "invalid undeliverable evidence")
		}
	}
	return nil
}

type MessageDeliverySeenPlan struct {
	Before             MessageDelivery `json:"before"`
	After              MessageDelivery `json:"after"`
	Changed            bool            `json:"changed"`
	MaterializesExpiry bool            `json:"materializes_expiry"`
	ExpiryCode         string          `json:"expiry_code,omitempty"`
}

func deliveryExpiryDeadlineElapsed(delivery MessageDelivery, dbNow time.Time) bool {
	return (delivery.AckDueAt != nil && !dbNow.Before(*delivery.AckDueAt)) ||
		(delivery.ExpiresAt != nil && !dbNow.Before(*delivery.ExpiresAt))
}

func PlanMessageDeliverySeen(delivery MessageDelivery, dbNow time.Time) (MessageDeliverySeenPlan, error) {
	if err := ValidateMessageDelivery(delivery); err != nil {
		return MessageDeliverySeenPlan{}, err
	}
	if dbNow.IsZero() || dbNow.Before(delivery.UpdatedAt) || dbNow.Before(delivery.AvailableAt) {
		return MessageDeliverySeenPlan{}, communicationError(ErrInvalidCommunicationModel,
			"invalid Delivery seen DB time")
	}
	if delivery.State == DeliveryUndeliverable {
		return MessageDeliverySeenPlan{}, communicationError(ErrCommunicationTerminal,
			"undeliverable Delivery cannot be seen")
	}
	if delivery.State == DeliveryAvailable && deliveryExpiryDeadlineElapsed(delivery, dbNow) {
		expiry, err := PlanMessageDeliveryExpiry(delivery, dbNow)
		if err != nil {
			return MessageDeliverySeenPlan{}, err
		}
		return MessageDeliverySeenPlan{
			Before: delivery, After: expiry.After, Changed: true,
			MaterializesExpiry: true, ExpiryCode: expiry.Code,
		}, nil
	}
	if delivery.FirstSeenAt != nil {
		return MessageDeliverySeenPlan{Before: delivery, After: delivery, Changed: false}, nil
	}
	after := delivery
	after.Version++
	after.UpdatedAt = dbNow
	after.FirstSeenAt = &dbNow
	if err := ValidateMessageDelivery(after); err != nil {
		return MessageDeliverySeenPlan{}, err
	}
	return MessageDeliverySeenPlan{Before: delivery, After: after, Changed: true}, nil
}

type MessageDeliveryExpiryPlan struct {
	Before MessageDelivery `json:"before"`
	After  MessageDelivery `json:"after"`
	Code   string          `json:"code"`
}

func PlanMessageDeliveryExpiry(
	delivery MessageDelivery,
	dbNow time.Time,
) (MessageDeliveryExpiryPlan, error) {
	if err := ValidateMessageDelivery(delivery); err != nil {
		return MessageDeliveryExpiryPlan{}, err
	}
	if delivery.State != DeliveryAvailable {
		return MessageDeliveryExpiryPlan{}, communicationError(ErrCommunicationTerminal,
			"Delivery state %s cannot materialize expiry", delivery.State)
	}
	if dbNow.IsZero() || dbNow.Before(delivery.UpdatedAt) {
		return MessageDeliveryExpiryPlan{}, communicationError(ErrInvalidCommunicationModel,
			"invalid Delivery expiry DB time")
	}
	code := ""
	if delivery.AckDueAt != nil && !dbNow.Before(*delivery.AckDueAt) {
		code = "ack_deadline_elapsed"
	}
	if delivery.ExpiresAt != nil && !dbNow.Before(*delivery.ExpiresAt) {
		if code == "" {
			code = "delivery_expired"
		}
	}
	if code == "" {
		return MessageDeliveryExpiryPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"Delivery expiry deadline has not elapsed")
	}
	after := delivery
	after.Version++
	after.UpdatedAt = dbNow
	after.State = DeliveryExpired
	if err := ValidateMessageDelivery(after); err != nil {
		return MessageDeliveryExpiryPlan{}, err
	}
	return MessageDeliveryExpiryPlan{Before: delivery, After: after, Code: code}, nil
}

type MessageDeliveryRetractionPlan struct {
	Before             MessageDelivery `json:"before"`
	After              MessageDelivery `json:"after"`
	MaterializesExpiry bool            `json:"materializes_expiry"`
	ExpiryCode         string          `json:"expiry_code,omitempty"`
}

func PlanMessageDeliveryRetraction(
	delivery MessageDelivery,
	dbNow time.Time,
) (MessageDeliveryRetractionPlan, error) {
	if err := ValidateMessageDelivery(delivery); err != nil {
		return MessageDeliveryRetractionPlan{}, err
	}
	if delivery.State != DeliveryAvailable || dbNow.IsZero() || dbNow.Before(delivery.UpdatedAt) {
		return MessageDeliveryRetractionPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"only an available Delivery can be retracted")
	}
	if deliveryExpiryDeadlineElapsed(delivery, dbNow) {
		expiry, err := PlanMessageDeliveryExpiry(delivery, dbNow)
		if err != nil {
			return MessageDeliveryRetractionPlan{}, err
		}
		return MessageDeliveryRetractionPlan{
			Before: delivery, After: expiry.After, MaterializesExpiry: true, ExpiryCode: expiry.Code,
		}, nil
	}
	after := delivery
	after.Version++
	after.UpdatedAt = dbNow
	after.State = DeliveryRetracted
	if err := ValidateMessageDelivery(after); err != nil {
		return MessageDeliveryRetractionPlan{}, err
	}
	return MessageDeliveryRetractionPlan{Before: delivery, After: after}, nil
}

func ValidateMessageAudience(audience MessageAudience) error {
	if err := validateAppendOnlyCommunicationEntity(audience.AppendOnlyCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(audience.MessageID) || audience.Ordinal < 1 ||
		audience.Selector.Validate() != nil ||
		(audience.RouteRuleID != "" && !validCanonicalCommunicationID(audience.RouteRuleID)) ||
		audience.ChannelACLRevision < 1 || audience.RouteRevision < 1 ||
		audience.SubscriptionRevision < 1 || audience.DirectoryEpoch < 1 ||
		audience.DirectorySnapshotAt.IsZero() || audience.ResolvedCount < 0 ||
		len(audience.SelectorHash) != sha256.Size || len(audience.ResolvedHash) != sha256.Size {
		return communicationError(ErrInvalidCommunicationModel, "invalid MessageAudience shape")
	}
	selectorBytes, err := canonicalJSON(audience.Selector)
	if err != nil {
		return err
	}
	selectorHash := sha256.Sum256(selectorBytes)
	if !bytes.Equal(selectorHash[:], audience.SelectorHash) {
		return communicationError(ErrInvalidCommunicationModel, "MessageAudience selector hash mismatch")
	}
	if oneOf(audience.Selector.Kind, AudienceUser, AudienceAgent, AudienceSession) &&
		audience.ResolvedCount != 1 {
		return communicationError(ErrInvalidCommunicationModel,
			"explicit MessageAudience selector must resolve exactly once")
	}
	if audience.Selector.Required && audience.ResolvedCount == 0 {
		return communicationError(ErrInvalidCommunicationModel, "required MessageAudience resolved empty")
	}
	return nil
}

func ValidateMessageAck(ack MessageAck) error {
	if err := validateAppendOnlyCommunicationEntity(ack.AppendOnlyCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(ack.DeliveryID) || !ack.Kind.Valid() || ack.Actor.Validate() != nil ||
		ack.Actor.Kind == ActorSystem || ack.AcknowledgedAt.IsZero() || ack.AcknowledgedAt != ack.CreatedAt {
		return communicationError(ErrInvalidCommunicationModel, "invalid MessageAck shape")
	}
	if ack.OnBehalfOf != nil {
		if ack.OnBehalfOf.Validate() != nil || ack.Note == nil {
			return communicationError(ErrInvalidCommunicationModel, "on-behalf Ack lacks recipient/reason")
		}
	}
	if ack.Note != nil {
		if err := ValidateProtectedPayloadSlot(*ack.Note, PayloadSlotAckNote,
			protectedPayloadPolicyFrom(*ack.Note)); err != nil {
			return err
		}
	}
	return nil
}

func ValidateCommunicationGuard(guard CommunicationGuard) error {
	if err := validateMutableCommunicationEntity(guard.MutableCommunicationEntity); err != nil {
		return err
	}
	if !guard.Kind.Valid() || guard.NextSeq < 1 || guard.LastDBTime.IsZero() ||
		guard.LastDBTime.Before(guard.CreatedAt) || guard.LastDBTime.After(guard.UpdatedAt) {
		return communicationError(ErrInvalidCommunicationModel, "invalid CommunicationGuard shape")
	}
	return nil
}

type CommunicationGuardAdvancePlan struct {
	Before       CommunicationGuard `json:"before"`
	After        CommunicationGuard `json:"after"`
	AllocatedSeq []int64            `json:"allocated_seq"`
}

// PlanCommunicationGuardAdvance allocates one contiguous delivery sequence
// range while advancing DB time monotonically. Apply must CAS Before.Version
// and persist After in the same mutation as every row using AllocatedSeq.
func PlanCommunicationGuardAdvance(
	before CommunicationGuard,
	count int64,
	dbNow time.Time,
) (CommunicationGuardAdvancePlan, error) {
	if err := ValidateCommunicationGuard(before); err != nil {
		return CommunicationGuardAdvancePlan{}, err
	}
	if before.Kind != CommunicationGuardDeliverySequence || count < 1 ||
		dbNow.IsZero() || dbNow.Before(before.LastDBTime) || dbNow.Before(before.UpdatedAt) ||
		before.NextSeq > math.MaxInt64-count {
		return CommunicationGuardAdvancePlan{}, communicationError(ErrInvalidCommunicationTransition,
			"CommunicationGuard cannot allocate requested sequence range")
	}
	after := before
	after.Version++
	after.UpdatedAt = dbNow
	after.LastDBTime = dbNow
	after.NextSeq += count
	allocated := make([]int64, count)
	for index := range allocated {
		allocated[index] = before.NextSeq + int64(index)
	}
	if err := ValidateCommunicationGuard(after); err != nil {
		return CommunicationGuardAdvancePlan{}, err
	}
	return CommunicationGuardAdvancePlan{Before: before, After: after, AllocatedSeq: allocated}, nil
}

func validCommunicationProjectionState(state string) bool {
	if state == "" {
		return true
	}
	return oneOf(state,
		string(ChannelActive), string(ChannelArchived),
		string(ChannelGrantActive), string(ChannelGrantRevoked), string(ChannelGrantExpired),
		string(SubscriptionActive), string(SubscriptionPaused), string(SubscriptionRevoked),
		string(ChannelLabelActive), string(ChannelLabelDisabled),
		string(ChannelRouteActive), string(ChannelRouteDisabled),
		string(EndpointActive), string(EndpointStale), string(EndpointDisabled),
		string(MessageDraft), string(MessagePublished), string(MessageRetracted), string(MessageExpired),
		string(MessageDiscarded), string(DeliveryAvailable), string(DeliveryAcknowledged),
		string(DeliveryExpired), string(DeliveryRetracted), string(DeliveryUndeliverable),
		string(DecisionPending), string(DecisionAccepted), string(DecisionBlocked), string(DecisionResolved),
		string(DecisionRejected), string(DecisionCanceled), string(DecisionExpired),
		string(HandoffOffered), string(HandoffAccepted), string(HandoffRejected), string(HandoffWithdrawn),
		string(HandoffExpired), string(DispatchPending), string(DispatchInFlight), string(DispatchSucceeded),
		string(DispatchFailed), string(DispatchUnknown), string(DispatchDeadLetter), string(DispatchSuperseded))
}

func validateInboxCursorReceiptProjection(
	receipt CommunicationCommandReceipt,
	projection CommunicationCommandResponseProjection,
) error {
	cursorResult := receipt.ResultKind == string(inboxCursorKind)
	if !cursorResult {
		if projection.InboxCursor != nil {
			return communicationError(ErrInvalidCommunicationModel,
				"non-cursor receipt carries an inbox cursor projection")
		}
		return nil
	}
	if receipt.ResultID == "" || receipt.HTTPStatus != 200 || receipt.EventID != "" ||
		projection.Version < 1 || projection.State != "" || len(projection.IDs) != 0 ||
		len(projection.Counts) != 0 || len(projection.Digests) != 0 || projection.InboxCursor == nil {
		return communicationError(ErrInvalidCommunicationModel,
			"cursor receipt does not carry the exact closed response projection")
	}
	cursor := projection.InboxCursor
	if cursor.LastSeenSeq < 0 ||
		((cursor.BarrierDeliveryID == "") != (cursor.BarrierReason == "")) ||
		(cursor.BarrierDeliveryID != "" && !validCanonicalCommunicationID(cursor.BarrierDeliveryID)) ||
		(cursor.BarrierReason != "" && !cursor.BarrierReason.Valid()) {
		return communicationError(ErrInvalidCommunicationModel,
			"cursor receipt carries an invalid barrier projection")
	}
	return nil
}

func ValidateCommunicationCommandReceipt(receipt CommunicationCommandReceipt) error {
	if err := validateAppendOnlyCommunicationEntity(receipt.AppendOnlyCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(receipt.CommandID) || len(receipt.ActorFingerprint) != sha256.Size ||
		!validateOpaqueRef(receipt.CommandScope) || len(receipt.IdempotencyKeyHash) != sha256.Size ||
		len(receipt.RequestDigest) != sha256.Size || len(receipt.PlanHash) != sha256.Size ||
		!validateOpaqueRef(receipt.ResultKind) || receipt.HTTPStatus < 100 || receipt.HTTPStatus > 599 ||
		len(receipt.ResponseDigest) != sha256.Size || receipt.CompletedAt.IsZero() ||
		receipt.CompletedAt != receipt.CreatedAt ||
		(receipt.ResultID != "" && !validCanonicalCommunicationID(receipt.ResultID)) ||
		(receipt.EventID != "" && !validCanonicalCommunicationID(receipt.EventID)) ||
		(receipt.SealKeyVersion != "" && !validateOpaqueRef(receipt.SealKeyVersion)) ||
		(receipt.DigestKeyVersion != "" && !validateOpaqueRef(receipt.DigestKeyVersion)) ||
		((receipt.SealKeyVersion == "") != (receipt.DigestKeyVersion == "")) ||
		(receipt.AuditSeq < 0) ||
		((receipt.AuditSeq == 0) != (len(receipt.AuditHash) == 0)) ||
		(receipt.AuditSeq > 0 && len(receipt.AuditHash) != sha256.Size) {
		return communicationError(ErrInvalidCommunicationModel, "invalid CommunicationCommandReceipt shape")
	}
	projection := receipt.ResponseProjectionJSON
	if len(projection.IDs)+len(projection.Counts)+len(projection.Digests) > 32 || projection.Version < 0 ||
		!validCommunicationProjectionState(projection.State) {
		return communicationError(ErrInvalidCommunicationModel, "invalid receipt response projection")
	}
	allowedIDs := map[string]bool{
		"channel_id": true, "message_id": true, "delivery_id": true, "ack_id": true,
		"request_id": true, "response_id": true, "handoff_id": true, "dispatch_id": true,
		"attempt_id": true, "result_id": true, "work_item_id": true, "event_id": true,
	}
	allowedCounts := map[string]bool{
		"required": true, "acknowledged": true, "viable": true, "unmet": true, "quorum": true,
		"resolved_count": true, "delivery_count": true,
	}
	allowedDigests := map[string]bool{
		"request": true, "plan": true, "response": true, "audience": true,
		"route_reasons": true, "contributions": true, "payload": true,
	}
	for key, id := range projection.IDs {
		if !allowedIDs[key] || !validCanonicalCommunicationID(id) {
			return communicationError(ErrInvalidCommunicationModel, "receipt projection carries invalid ID")
		}
	}
	for key, count := range projection.Counts {
		if !allowedCounts[key] || count < 0 {
			return communicationError(ErrInvalidCommunicationModel, "receipt projection carries invalid count")
		}
	}
	for key, digest := range projection.Digests {
		if !allowedDigests[key] || len(digest) != sha256.Size {
			return communicationError(ErrInvalidCommunicationModel, "receipt projection carries invalid digest")
		}
	}
	if err := validateInboxCursorReceiptProjection(receipt, projection); err != nil {
		return err
	}
	canonical, err := canonicalJSON(projection)
	if err != nil || len(canonical) > 4096 {
		return communicationError(ErrInvalidCommunicationModel, "receipt response projection is not bounded")
	}
	binding, err := CanonicalCommunicationReceiptResponseBinding(receipt)
	if err != nil {
		return err
	}
	if receipt.DigestKeyVersion == "" {
		want := sha256.Sum256(binding)
		if !bytes.Equal(receipt.ResponseDigest, want[:]) {
			return communicationError(ErrInvalidCommunicationModel,
				"plain receipt response digest does not bind the closed projection")
		}
	}
	return nil
}

type communicationReceiptResponseBinding struct {
	PlanHash   []byte                                 `json:"plan_hash"`
	ResultKind string                                 `json:"result_kind"`
	ResultID   model.ID                               `json:"result_id,omitempty"`
	HTTPStatus int                                    `json:"http_status"`
	Response   CommunicationCommandResponseProjection `json:"response"`
	EventID    model.ID                               `json:"event_id,omitempty"`
	AuditSeq   int64                                  `json:"audit_seq"`
	AuditHash  []byte                                 `json:"audit_hash,omitempty"`
}

func CanonicalCommunicationReceiptResponseBinding(
	receipt CommunicationCommandReceipt,
) ([]byte, error) {
	if len(receipt.PlanHash) != sha256.Size || !validateOpaqueRef(receipt.ResultKind) ||
		receipt.HTTPStatus < 100 || receipt.HTTPStatus > 599 ||
		(receipt.ResultID != "" && !validCanonicalCommunicationID(receipt.ResultID)) ||
		(receipt.EventID != "" && !validCanonicalCommunicationID(receipt.EventID)) ||
		(receipt.AuditSeq < 0) || ((receipt.AuditSeq == 0) != (len(receipt.AuditHash) == 0)) ||
		(receipt.AuditSeq > 0 && len(receipt.AuditHash) != sha256.Size) {
		return nil, communicationError(ErrInvalidCommunicationModel,
			"receipt result binding is invalid")
	}
	return canonicalJSON(communicationReceiptResponseBinding{
		PlanHash: append([]byte(nil), receipt.PlanHash...), ResultKind: receipt.ResultKind,
		ResultID: receipt.ResultID, HTTPStatus: receipt.HTTPStatus,
		Response: receipt.ResponseProjectionJSON, EventID: receipt.EventID,
		AuditSeq: receipt.AuditSeq, AuditHash: append([]byte(nil), receipt.AuditHash...),
	})
}

// CommunicationReceiptDigestWitness is produced only after WP-2 calls the
// versioned digest verifier. E validates its exact receipt/binding identity;
// it never substitutes SHA-256 for a keyed digest.
type CommunicationReceiptDigestWitness struct {
	ReceiptID        model.ID          `json:"receipt_id"`
	CommandID        model.ID          `json:"command_id"`
	DigestKeyVersion string            `json:"digest_key_version"`
	ResponseDigest   []byte            `json:"response_digest"`
	BindingHash      []byte            `json:"binding_hash"`
	ObservedAt       time.Time         `json:"observed_at"`
	Verification     AuthorityEvidence `json:"verification"`
}

func ValidateCommunicationReceiptDigestWitness(
	receipt CommunicationCommandReceipt,
	witness CommunicationReceiptDigestWitness,
) error {
	if receipt.DigestKeyVersion == "" {
		return communicationError(ErrInvalidCommunicationModel,
			"plain receipt must not carry keyed digest verification")
	}
	binding, err := CanonicalCommunicationReceiptResponseBinding(receipt)
	if err != nil {
		return err
	}
	bindingHash := sha256.Sum256(binding)
	if witness.ReceiptID != receipt.ID || witness.CommandID != receipt.CommandID ||
		witness.DigestKeyVersion != receipt.DigestKeyVersion ||
		!bytes.Equal(witness.ResponseDigest, receipt.ResponseDigest) ||
		!bytes.Equal(witness.BindingHash, bindingHash[:]) || witness.ObservedAt != receipt.CompletedAt ||
		ValidateAuthorityEvidence(witness.Verification) != nil ||
		evidenceVerdict(witness.Verification) != VerdictClean {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"keyed receipt digest has not been verified with its persisted key version")
	}
	return nil
}

type MessageTransition string

const (
	MessagePublish MessageTransition = "publish"
	MessageDiscard MessageTransition = "discard"
	MessageRetract MessageTransition = "retract"
	MessageExpire  MessageTransition = "expire"
)

func NextMessageState(current MessageState, transition MessageTransition) (MessageState, error) {
	if !current.Valid() {
		return "", communicationError(ErrInvalidCommunicationModel, "unknown Message state %q", current)
	}
	if oneOf(current, MessageRetracted, MessageExpired, MessageDiscarded) {
		return "", communicationError(ErrCommunicationTerminal, "Message state %q is absorbing", current)
	}
	switch {
	case current == MessageDraft && transition == MessagePublish:
		return MessagePublished, nil
	case current == MessageDraft && transition == MessageDiscard:
		return MessageDiscarded, nil
	case current == MessagePublished && transition == MessageRetract:
		return MessageRetracted, nil
	case current == MessagePublished && transition == MessageExpire:
		return MessageExpired, nil
	default:
		return "", communicationError(ErrInvalidCommunicationTransition, "%s from Message %s", transition, current)
	}
}

type MessageTransitionPlan struct {
	Before          Message                        `json:"before"`
	After           Message                        `json:"after"`
	DeliveryPlans   []MessageDeliveryTerminalPlan  `json:"delivery_plans,omitempty"`
	DecisionPlan    *DecisionRequestTransitionPlan `json:"decision_plan,omitempty"`
	HandoffPlan     *HandoffTransitionPlan         `json:"handoff_plan,omitempty"`
	ExpectedEffects int64                          `json:"expected_effects"`
}

type MessageDeliveryTerminalPlan struct {
	Before MessageDelivery `json:"before"`
	After  MessageDelivery `json:"after"`
}

type MessageTerminalCarrierSetWitness struct {
	Scope             DirectoryScopeRef `json:"scope"`
	MessageID         model.ID          `json:"message_id"`
	DeliveryCount     int64             `json:"delivery_count"`
	DeliveryDigest    []byte            `json:"delivery_digest"`
	DecisionRequestID model.ID          `json:"decision_request_id,omitempty"`
	HandoffID         model.ID          `json:"handoff_id,omitempty"`
	ObservedAt        time.Time         `json:"observed_at"`
	Evidence          AuthorityEvidence `json:"evidence"`
}

type MessageDecisionCascadeInput struct {
	Request        DecisionRequest               `json:"request"`
	ResponseEntity AppendOnlyCommunicationEntity `json:"response_entity"`
	Actor          CommunicationActorRef         `json:"actor"`
	Response       ProtectedPayload              `json:"response"`
}

type MessageHandoffCascadeInput struct {
	Handoff      Handoff          `json:"handoff"`
	TerminalCode string           `json:"terminal_code"`
	Reason       ProtectedPayload `json:"reason"`
}

type MessageTransitionInput struct {
	Before         Message                          `json:"before"`
	Transition     MessageTransition                `json:"transition"`
	Deliveries     []MessageDelivery                `json:"deliveries"`
	Decision       *MessageDecisionCascadeInput     `json:"decision,omitempty"`
	Handoff        *MessageHandoffCascadeInput      `json:"handoff,omitempty"`
	CarrierSet     MessageTerminalCarrierSetWitness `json:"carrier_set"`
	TerminalCode   string                           `json:"terminal_code"`
	TerminalReason ProtectedPayload                 `json:"terminal_reason"`
	DBNow          time.Time                        `json:"db_now"`
}

// PlanMessageTransition is the DB-time-aware atomic terminal cascade. Publish
// has its own planner because it creates the audience and delivery set.
func PlanMessageTransition(input MessageTransitionInput) (MessageTransitionPlan, error) {
	before := input.Before
	transition := input.Transition
	deliveries := input.Deliveries
	dbNow := input.DBNow
	requiredCount := int64(0)
	for _, delivery := range deliveries {
		if delivery.Required {
			requiredCount++
		}
	}
	if err := ValidateMessage(before, requiredCount); err != nil {
		return MessageTransitionPlan{}, err
	}
	to, err := NextMessageState(before.State, transition)
	if err != nil {
		return MessageTransitionPlan{}, err
	}
	if dbNow.IsZero() || dbNow.Before(before.UpdatedAt) {
		return MessageTransitionPlan{}, communicationError(ErrInvalidCommunicationModel, "invalid Message DB time")
	}
	after := before
	after.Version++
	after.UpdatedAt = dbNow
	after.State = to
	switch transition {
	case MessagePublish:
		return MessageTransitionPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"Message publish requires PlanMessagePublish")
	case MessageDiscard, MessageRetract, MessageExpire:
		if !boundedToken(input.TerminalCode, 128) {
			return MessageTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
				"Message terminal transition lacks bounded code")
		}
		if err := ValidateProtectedPayloadSlot(input.TerminalReason,
			PayloadSlotMessageTerminalReason, protectedPayloadPolicyFrom(before.Payload)); err != nil {
			return MessageTransitionPlan{}, err
		}
		if transition == MessageExpire &&
			(before.ExpiresAt == nil || dbNow.Before(*before.ExpiresAt)) {
			return MessageTransitionPlan{}, communicationError(ErrInvalidCommunicationTransition,
				"Message expiry deadline has not elapsed")
		}
		after.TerminalAt = &dbNow
		after.TerminalCode = input.TerminalCode
		after.TerminalReason = &input.TerminalReason
		if after.WorkItemID == "" {
			after.LastEventSeq++
		}
	}
	if input.CarrierSet.Scope != (DirectoryScopeRef{TenantID: before.TenantID, WorkspaceID: before.WorkspaceID}) ||
		input.CarrierSet.MessageID != before.ID || input.CarrierSet.DeliveryCount != int64(len(deliveries)) ||
		input.CarrierSet.ObservedAt != dbNow || ValidateAuthorityEvidence(input.CarrierSet.Evidence) != nil ||
		evidenceVerdict(input.CarrierSet.Evidence) != VerdictClean {
		return MessageTransitionPlan{}, communicationError(ErrCommunicationEvidenceUnknown,
			"terminal carrier set is not a complete same-transaction snapshot")
	}
	deliveryDigest, err := CanonicalFulfillmentDeliverySetDigest(deliveries)
	if err != nil {
		return MessageTransitionPlan{}, err
	}
	if !bytes.Equal(deliveryDigest, input.CarrierSet.DeliveryDigest) {
		return MessageTransitionPlan{}, communicationError(ErrCommunicationEvidenceUnknown,
			"terminal carrier delivery set digest mismatch")
	}
	if transition == MessageDiscard {
		if len(deliveries) != 0 || input.Decision != nil || input.Handoff != nil ||
			input.CarrierSet.DecisionRequestID != "" || input.CarrierSet.HandoffID != "" {
			return MessageTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
				"discarded draft carries published child carriers")
		}
	}
	plan := MessageTransitionPlan{Before: before, After: after, ExpectedEffects: 1}
	if transition == MessageRetract || transition == MessageExpire {
		seenDeliveries := make(map[model.ID]struct{}, len(deliveries))
		for _, delivery := range deliveries {
			if _, duplicate := seenDeliveries[delivery.ID]; duplicate {
				return MessageTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
					"terminal cascade contains duplicate Delivery")
			}
			seenDeliveries[delivery.ID] = struct{}{}
			if err := ValidateMessageDelivery(delivery); err != nil {
				return MessageTransitionPlan{}, err
			}
			if err := ValidateMessageDeliveryLineage(before, delivery); err != nil {
				return MessageTransitionPlan{}, err
			}
			if delivery.State != DeliveryAvailable {
				continue
			}
			var terminal MessageDelivery
			if transition == MessageRetract {
				retraction, err := PlanMessageDeliveryRetraction(delivery, dbNow)
				if err != nil {
					return MessageTransitionPlan{}, err
				}
				terminal = retraction.After
			} else {
				expiry, err := PlanMessageDeliveryExpiry(delivery, dbNow)
				if err != nil {
					return MessageTransitionPlan{}, err
				}
				terminal = expiry.After
			}
			plan.DeliveryPlans = append(plan.DeliveryPlans, MessageDeliveryTerminalPlan{
				Before: delivery, After: terminal,
			})
		}
		plan.ExpectedEffects += int64(len(plan.DeliveryPlans))
	}
	if input.Decision == nil {
		if input.CarrierSet.DecisionRequestID != "" ||
			(oneOf(transition, MessageRetract, MessageExpire) && before.Kind == MessageDecisionRequest) {
			return MessageTransitionPlan{}, communicationError(ErrCommunicationEvidenceUnknown,
				"linked DecisionRequest is absent from terminal cascade")
		}
	} else {
		decision := input.Decision
		if input.CarrierSet.DecisionRequestID != decision.Request.ID ||
			before.Kind != MessageDecisionRequest {
			return MessageTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
				"orphan DecisionRequest in terminal cascade")
		}
		if err := ValidateDecisionRequest(decision.Request); err != nil {
			return MessageTransitionPlan{}, err
		}
		if err := ValidateDecisionRequestLineage(before, decision.Request); err != nil {
			return MessageTransitionPlan{}, err
		}
		if !oneOf(decision.Request.State, DecisionResolved, DecisionRejected, DecisionCanceled, DecisionExpired) {
			decisionTransition := DecisionCancel
			decisionCode := "message_retracted"
			if transition == MessageExpire {
				decisionTransition = DecisionExpire
				decisionCode = "message_expired"
			}
			decisionPlan, err := PlanDecisionRequestTransition(
				decision.Request, decisionTransition, decision.ResponseEntity, decision.Actor,
				decision.Response, nil, "", "", "", decisionCode, dbNow,
			)
			if err != nil {
				return MessageTransitionPlan{}, err
			}
			plan.DecisionPlan = &decisionPlan
			plan.ExpectedEffects += 2
		}
	}
	if input.Handoff == nil {
		if input.CarrierSet.HandoffID != "" ||
			(oneOf(transition, MessageRetract, MessageExpire) && before.Kind == MessageHandoffOffer) {
			return MessageTransitionPlan{}, communicationError(ErrCommunicationEvidenceUnknown,
				"linked Handoff is absent from terminal cascade")
		}
	} else {
		handoff := input.Handoff
		if input.CarrierSet.HandoffID != handoff.Handoff.ID || before.Kind != MessageHandoffOffer {
			return MessageTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
				"orphan Handoff in terminal cascade")
		}
		var linkedDelivery *MessageDelivery
		for index := range deliveries {
			if deliveries[index].ID == handoff.Handoff.DeliveryID {
				linkedDelivery = &deliveries[index]
				break
			}
		}
		if linkedDelivery == nil ||
			ValidateHandoffLineage(before, *linkedDelivery, handoff.Handoff, requiredCount) != nil {
			return MessageTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
				"Handoff terminal cascade crosses Message/Delivery lineage")
		}
		if handoff.Handoff.State == HandoffOffered {
			handoffTransition := HandoffWithdraw
			if transition == MessageExpire {
				handoffTransition = HandoffExpire
			}
			handoffPlan, err := PlanHandoffTransition(
				handoff.Handoff, handoffTransition, "", 0, handoff.TerminalCode,
				&handoff.Reason, dbNow,
			)
			if err != nil {
				return MessageTransitionPlan{}, err
			}
			plan.HandoffPlan = &handoffPlan
			plan.ExpectedEffects++
		}
	}
	if err := ValidateMessage(after, requiredCount); err != nil {
		return MessageTransitionPlan{}, err
	}
	plan.After = after
	return plan, nil
}

type DecisionTransition string

const (
	DecisionAccept  DecisionTransition = "accept"
	DecisionBlock   DecisionTransition = "block"
	DecisionResolve DecisionTransition = "resolve"
	DecisionReject  DecisionTransition = "reject"
	DecisionCancel  DecisionTransition = "cancel"
	DecisionExpire  DecisionTransition = "expire"
)

func NextDecisionRequestState(current DecisionRequestState, transition DecisionTransition) (DecisionRequestState, error) {
	if !current.Valid() {
		return "", communicationError(ErrInvalidCommunicationModel, "unknown DecisionRequest state %q", current)
	}
	if oneOf(current, DecisionResolved, DecisionRejected, DecisionCanceled, DecisionExpired) {
		return "", communicationError(ErrCommunicationTerminal, "DecisionRequest state %q is absorbing", current)
	}
	switch transition {
	case DecisionAccept:
		if oneOf(current, DecisionPending, DecisionBlocked) {
			return DecisionAccepted, nil
		}
	case DecisionBlock:
		if current == DecisionAccepted {
			return DecisionBlocked, nil
		}
	case DecisionResolve:
		return DecisionResolved, nil
	case DecisionReject:
		return DecisionRejected, nil
	case DecisionCancel:
		return DecisionCanceled, nil
	case DecisionExpire:
		return DecisionExpired, nil
	}
	return "", communicationError(ErrInvalidCommunicationTransition, "%s from DecisionRequest %s",
		transition, current)
}

type HandoffTransition string

const (
	HandoffAccept   HandoffTransition = "accept"
	HandoffReject   HandoffTransition = "reject"
	HandoffWithdraw HandoffTransition = "withdraw"
	HandoffExpire   HandoffTransition = "expire"
)

func NextHandoffState(current HandoffState, transition HandoffTransition) (HandoffState, error) {
	if !current.Valid() {
		return "", communicationError(ErrInvalidCommunicationModel, "unknown Handoff state %q", current)
	}
	if current != HandoffOffered {
		return "", communicationError(ErrCommunicationTerminal, "Handoff state %q is absorbing", current)
	}
	switch transition {
	case HandoffAccept:
		return HandoffAccepted, nil
	case HandoffReject:
		return HandoffRejected, nil
	case HandoffWithdraw:
		return HandoffWithdrawn, nil
	case HandoffExpire:
		return HandoffExpired, nil
	default:
		return "", communicationError(ErrInvalidCommunicationTransition, "%s from Handoff %s", transition, current)
	}
}

// ValidateDecisionRequest closes the state-dependent shape. In particular,
// custody (accepted) is not a WorkDecision and only resolved may retain a
// WorkDecision reference.
func ValidateDecisionRequest(request DecisionRequest) error {
	if err := validateMutableCommunicationEntity(request.MutableCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(request.MessageID) ||
		!validCanonicalCommunicationID(request.WorkItemID) ||
		!boundedToken(request.DecisionKey, 128) || request.Requester.Validate() != nil ||
		request.Owner.Validate() != nil || !request.State.Valid() ||
		ValidateProtectedPayloadSlot(request.Request, PayloadSlotDecisionRequest,
			protectedPayloadPolicyFrom(request.Request)) != nil ||
		!boundedToken(request.AuthorityRequirement, 256) || request.DueAt.IsZero() ||
		!request.DueAt.After(request.CreatedAt) || request.LastResponseSeq < 0 ||
		request.Version != request.LastResponseSeq+1 {
		return communicationError(ErrInvalidCommunicationModel, "invalid DecisionRequest envelope")
	}
	accepted := request.AcceptedDeliveryID != "" || request.AcceptedAt != nil
	if accepted != (request.AcceptedDeliveryID != "" && request.AcceptedAt != nil) ||
		(request.AcceptedDeliveryID != "" && !validCanonicalCommunicationID(request.AcceptedDeliveryID)) ||
		(request.AcceptedAt != nil && (request.AcceptedAt.Before(request.CreatedAt) ||
			request.AcceptedAt.After(request.UpdatedAt))) {
		return communicationError(ErrInvalidCommunicationModel, "invalid DecisionRequest custody evidence")
	}
	switch request.State {
	case DecisionPending:
		if request.LastResponseSeq != 0 || accepted || request.BlockedCode != "" || request.TerminalCode != "" ||
			request.ResolvedDecisionID != "" {
			return communicationError(ErrInvalidCommunicationModel, "pending decision carries effects")
		}
	case DecisionAccepted:
		if request.LastResponseSeq < 1 || request.LastResponseSeq%2 != 1 || !accepted ||
			request.BlockedCode != "" || request.TerminalCode != "" ||
			request.ResolvedDecisionID != "" {
			return communicationError(ErrInvalidCommunicationModel, "accepted decision shape is inconsistent")
		}
	case DecisionBlocked:
		if request.LastResponseSeq < 2 || request.LastResponseSeq%2 != 0 || !accepted ||
			!boundedToken(request.BlockedCode, 128) || request.TerminalCode != "" ||
			request.ResolvedDecisionID != "" {
			return communicationError(ErrInvalidCommunicationModel, "blocked decision lacks blocker evidence")
		}
	case DecisionResolved:
		if request.LastResponseSeq < 1 || request.BlockedCode != "" || !boundedToken(request.TerminalCode, 128) ||
			!validCanonicalCommunicationID(request.ResolvedDecisionID) {
			return communicationError(ErrInvalidCommunicationModel, "resolved decision lacks WorkDecision")
		}
	case DecisionRejected, DecisionCanceled, DecisionExpired:
		if request.LastResponseSeq < 1 || request.BlockedCode != "" || !boundedToken(request.TerminalCode, 128) ||
			request.ResolvedDecisionID != "" {
			return communicationError(ErrInvalidCommunicationModel, "terminal decision fabricates WorkDecision")
		}
	}
	if request.State == DecisionExpired && request.UpdatedAt.Before(request.DueAt) {
		return communicationError(ErrInvalidCommunicationModel, "DecisionRequest expired before due_at")
	}
	return nil
}

type DecisionRequestTransitionPlan struct {
	Before        DecisionRequest        `json:"before"`
	After         DecisionRequest        `json:"after"`
	Response      DecisionResponse       `json:"response"`
	ChoiceWitness *DecisionChoiceWitness `json:"choice_witness,omitempty"`
}

// DecisionChoiceWitness is produced by the trusted payload opener before the
// mutation. For sealed carriers it proves, without persisting plaintext, that
// the exact response choice is a member of the exact request choice set.
type DecisionChoiceWitness struct {
	Scope                DirectoryScopeRef `json:"scope"`
	RequestID            model.ID          `json:"request_id"`
	RequestEnvelopeHash  []byte            `json:"request_envelope_hash"`
	ResponseEnvelopeHash []byte            `json:"response_envelope_hash"`
	ChoiceKey            string            `json:"choice_key"`
	ObservedAt           time.Time         `json:"observed_at"`
	FreshUntil           time.Time         `json:"fresh_until"`
	Evidence             AuthorityEvidence `json:"evidence"`
}

func CanonicalProtectedPayloadEnvelopeHash(payload ProtectedPayload) ([]byte, error) {
	if err := ValidateProtectedPayload(payload); err != nil {
		return nil, err
	}
	raw, err := canonicalJSON(payload)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func validateDecisionChoiceWitness(
	request DecisionRequest,
	response ProtectedPayload,
	witness *DecisionChoiceWitness,
	dbNow time.Time,
) error {
	if witness == nil {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"resolved DecisionRequest lacks a choice-set witness")
	}
	scope := DirectoryScopeRef{TenantID: request.TenantID, WorkspaceID: request.WorkspaceID}
	requestHash, requestHashErr := CanonicalProtectedPayloadEnvelopeHash(request.Request)
	responseHash, responseHashErr := CanonicalProtectedPayloadEnvelopeHash(response)
	if requestHashErr != nil || responseHashErr != nil || witness.Scope != scope ||
		witness.RequestID != request.ID || !boundedToken(witness.ChoiceKey, 128) ||
		witness.ObservedAt.IsZero() || !witness.FreshUntil.After(witness.ObservedAt) ||
		witness.ObservedAt.After(dbNow) || dbNow.After(witness.FreshUntil) ||
		len(witness.RequestEnvelopeHash) != sha256.Size ||
		len(witness.ResponseEnvelopeHash) != sha256.Size ||
		!bytes.Equal(witness.RequestEnvelopeHash, requestHash) ||
		!bytes.Equal(witness.ResponseEnvelopeHash, responseHash) ||
		ValidateAuthorityEvidence(witness.Evidence) != nil {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"Decision choice witness is stale or crosses request/response lineage")
	}
	switch evidenceVerdict(witness.Evidence) {
	case VerdictUnknown:
		return communicationError(ErrCommunicationEvidenceUnknown,
			"Decision choice membership is unavailable")
	case VerdictBroken:
		return communicationError(ErrCommunicationForbidden,
			"Decision response choice is not allowed by the request")
	}
	if request.Request.Encoding == PayloadPlainJSON && response.Encoding == PayloadPlainJSON {
		var requestContent DecisionRequestContent
		var responseContent DecisionResponseContent
		if err := json.Unmarshal(request.Request.PlainJSON, &requestContent); err != nil {
			return communicationError(ErrInvalidCommunicationModel, "invalid plain DecisionRequest choice set")
		}
		if err := json.Unmarshal(response.PlainJSON, &responseContent); err != nil ||
			responseContent.ChoiceKey != witness.ChoiceKey {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"Decision choice witness does not name the exact plain response")
		}
		found := false
		for _, choice := range requestContent.Choices {
			found = found || choice.Key == witness.ChoiceKey
		}
		if !found {
			return communicationError(ErrCommunicationForbidden,
				"Decision response choice is absent from the request")
		}
	}
	return nil
}

// PlanDecisionRequestTransition produces the request+response pair that must
// be applied atomically. WorkDecisionID is accepted only for resolve; an
// accept therefore cannot decide or acknowledge anything implicitly.
func PlanDecisionRequestTransition(
	before DecisionRequest,
	transition DecisionTransition,
	responseEntity AppendOnlyCommunicationEntity,
	actor CommunicationActorRef,
	responsePayload ProtectedPayload,
	choiceWitness *DecisionChoiceWitness,
	acceptedDeliveryID model.ID,
	blockerWorkItemID model.ID,
	workDecisionID model.ID,
	code string,
	dbNow time.Time,
) (DecisionRequestTransitionPlan, error) {
	if err := ValidateDecisionRequest(before); err != nil {
		return DecisionRequestTransitionPlan{}, err
	}
	to, err := NextDecisionRequestState(before.State, transition)
	if err != nil {
		return DecisionRequestTransitionPlan{}, err
	}
	if err := validateAppendOnlyCommunicationEntity(responseEntity); err != nil {
		return DecisionRequestTransitionPlan{}, err
	}
	if responseEntity.TenantID != before.TenantID || responseEntity.WorkspaceID != before.WorkspaceID ||
		responseEntity.CreatedAt != dbNow || actor.Validate() != nil ||
		ValidateProtectedPayloadSlot(responsePayload, PayloadSlotDecisionResponse,
			protectedPayloadPolicyFrom(before.Request)) != nil || dbNow.IsZero() || dbNow.Before(before.UpdatedAt) {
		return DecisionRequestTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
			"invalid DecisionResponse input")
	}
	if oneOf(transition, DecisionAccept, DecisionBlock, DecisionResolve, DecisionReject) &&
		!dbNow.Before(before.DueAt) {
		return DecisionRequestTransitionPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"DecisionRequest deadline requires expiry")
	}
	after := before
	after.Version++
	after.UpdatedAt = dbNow
	after.State = to
	after.LastResponseSeq++
	after.BlockedCode = ""
	after.TerminalCode = ""
	after.ResolvedDecisionID = ""
	if transition != DecisionResolve && choiceWitness != nil {
		return DecisionRequestTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
			"non-resolved DecisionRequest carries a choice witness")
	}
	switch transition {
	case DecisionAccept:
		if !validCanonicalCommunicationID(acceptedDeliveryID) || blockerWorkItemID != "" ||
			workDecisionID != "" || code != "" {
			return DecisionRequestTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
				"accept must only take custody")
		}
		after.AcceptedDeliveryID = acceptedDeliveryID
		after.AcceptedAt = &dbNow
	case DecisionBlock:
		if acceptedDeliveryID != "" || workDecisionID != "" || !boundedToken(code, 128) ||
			(blockerWorkItemID != "" && !validCanonicalCommunicationID(blockerWorkItemID)) {
			return DecisionRequestTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
				"invalid blocked decision effects")
		}
		after.BlockedCode = code
	case DecisionResolve:
		if acceptedDeliveryID != "" || blockerWorkItemID != "" ||
			!validCanonicalCommunicationID(workDecisionID) || !boundedToken(code, 128) {
			return DecisionRequestTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
				"resolve requires exact WorkDecision")
		}
		if err := validateDecisionChoiceWitness(before, responsePayload, choiceWitness, dbNow); err != nil {
			return DecisionRequestTransitionPlan{}, err
		}
		after.ResolvedDecisionID = workDecisionID
		after.TerminalCode = code
	case DecisionReject, DecisionCancel, DecisionExpire:
		if acceptedDeliveryID != "" || blockerWorkItemID != "" || workDecisionID != "" ||
			!boundedToken(code, 128) {
			return DecisionRequestTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
				"terminal decision carries invalid effects")
		}
		if transition == DecisionExpire && dbNow.Before(before.DueAt) {
			return DecisionRequestTransitionPlan{}, communicationError(ErrInvalidCommunicationTransition,
				"DecisionRequest deadline has not elapsed")
		}
		after.TerminalCode = code
	}
	response := DecisionResponse{
		AppendOnlyCommunicationEntity: responseEntity, RequestID: before.ID,
		ResponseSeq: after.LastResponseSeq, FromState: before.State, ToState: to, Actor: actor,
		Response: responsePayload, AcceptedDeliveryID: after.AcceptedDeliveryID,
		BlockerWorkItemID: blockerWorkItemID,
		WorkDecisionID:    workDecisionID, RespondedAt: dbNow,
	}
	if err := ValidateDecisionRequest(after); err != nil {
		return DecisionRequestTransitionPlan{}, err
	}
	if err := ValidateDecisionResponse(response, before, after); err != nil {
		return DecisionRequestTransitionPlan{}, err
	}
	return DecisionRequestTransitionPlan{
		Before: before, After: after, Response: response, ChoiceWitness: choiceWitness,
	}, nil
}

func ValidateDecisionResponse(response DecisionResponse, before, after DecisionRequest) error {
	if err := validateAppendOnlyCommunicationEntity(response.AppendOnlyCommunicationEntity); err != nil {
		return err
	}
	if response.TenantID != before.TenantID || response.WorkspaceID != before.WorkspaceID ||
		after.TenantID != before.TenantID || after.WorkspaceID != before.WorkspaceID ||
		after.ID != before.ID || after.CreatedAt != before.CreatedAt || after.Version != before.Version+1 ||
		after.MessageID != before.MessageID || after.WorkItemID != before.WorkItemID ||
		after.DecisionKey != before.DecisionKey || after.Requester != before.Requester ||
		after.Owner != before.Owner || after.AuthorityRequirement != before.AuthorityRequirement ||
		after.DueAt != before.DueAt ||
		response.RequestID != before.ID || response.ResponseSeq != before.LastResponseSeq+1 ||
		after.LastResponseSeq != response.ResponseSeq || response.FromState != before.State ||
		response.ToState != after.State || response.AcceptedDeliveryID != after.AcceptedDeliveryID ||
		(response.AcceptedDeliveryID != "" && !validCanonicalCommunicationID(response.AcceptedDeliveryID)) ||
		response.Actor.Validate() != nil ||
		ValidateProtectedPayloadSlot(response.Response, PayloadSlotDecisionResponse,
			protectedPayloadPolicyFrom(before.Request)) != nil || response.RespondedAt.IsZero() ||
		response.RespondedAt != response.CreatedAt || response.RespondedAt != after.UpdatedAt {
		return communicationError(ErrInvalidCommunicationModel, "DecisionResponse crosses request lineage")
	}
	beforeRequest, beforeErr := canonicalJSON(before.Request)
	afterRequest, afterErr := canonicalJSON(after.Request)
	if beforeErr != nil || afterErr != nil || !bytes.Equal(beforeRequest, afterRequest) {
		return communicationError(ErrInvalidCommunicationModel, "DecisionRequest immutable payload changed")
	}
	if _, err := NextDecisionRequestState(response.FromState, transitionForDecisionStates(response.FromState,
		response.ToState)); err != nil {
		return err
	}
	if (response.ToState == DecisionResolved) != validCanonicalCommunicationID(response.WorkDecisionID) ||
		response.WorkDecisionID != after.ResolvedDecisionID ||
		(response.ToState != DecisionBlocked && response.BlockerWorkItemID != "") ||
		(response.BlockerWorkItemID != "" && !validCanonicalCommunicationID(response.BlockerWorkItemID)) {
		return communicationError(ErrInvalidCommunicationModel, "DecisionResponse effects do not match transition")
	}
	return nil
}

func transitionForDecisionStates(from, to DecisionRequestState) DecisionTransition {
	switch to {
	case DecisionAccepted:
		return DecisionAccept
	case DecisionBlocked:
		return DecisionBlock
	case DecisionResolved:
		return DecisionResolve
	case DecisionRejected:
		return DecisionReject
	case DecisionCanceled:
		return DecisionCancel
	case DecisionExpired:
		return DecisionExpire
	default:
		return DecisionTransition("invalid:" + string(from) + ":" + string(to))
	}
}

type handoffContextHashInput struct {
	TenantID             model.TenantID `json:"tenant_id"`
	WorkspaceID          model.ID       `json:"workspace_id"`
	WorkItemID           model.ID       `json:"work_item_id"`
	MessageID            model.ID       `json:"message_id"`
	DeliveryID           model.ID       `json:"delivery_id"`
	From                 RecipientRef   `json:"from"`
	FromOwnerEpoch       int64          `json:"from_owner_epoch"`
	To                   RecipientRef   `json:"to"`
	OfferedLeaseFence    int64          `json:"offered_lease_fence,omitempty"`
	ContextEventSeq      int64          `json:"context_event_seq"`
	PayloadSchema        string         `json:"payload_schema"`
	PayloadDigest        []byte         `json:"payload_digest"`
	ProtectionGeneration int64          `json:"protection_generation"`
}

func CanonicalHandoffContextHash(handoff Handoff) ([]byte, error) {
	if !validCanonicalCommunicationTenant(handoff.TenantID) ||
		!validCanonicalCommunicationID(handoff.WorkspaceID) ||
		!validCanonicalCommunicationID(handoff.WorkItemID) ||
		!validCanonicalCommunicationID(handoff.MessageID) ||
		!validCanonicalCommunicationID(handoff.DeliveryID) || handoff.From.Validate() != nil ||
		handoff.To.Validate() != nil || handoff.FromOwnerEpoch < 1 || handoff.OfferedLeaseFence < 0 ||
		handoff.ContextEventSeq < 1 || ValidateProtectedPayloadSlot(handoff.Payload, PayloadSlotHandoff,
		protectedPayloadPolicyFrom(handoff.Payload)) != nil {
		return nil, communicationError(ErrInvalidCommunicationModel, "invalid Handoff context input")
	}
	canonical, err := canonicalJSON(handoffContextHashInput{
		TenantID: handoff.TenantID, WorkspaceID: handoff.WorkspaceID,
		WorkItemID: handoff.WorkItemID, MessageID: handoff.MessageID, DeliveryID: handoff.DeliveryID,
		From: handoff.From, FromOwnerEpoch: handoff.FromOwnerEpoch, To: handoff.To,
		OfferedLeaseFence: handoff.OfferedLeaseFence, ContextEventSeq: handoff.ContextEventSeq,
		PayloadSchema: handoff.Payload.Schema, PayloadDigest: handoff.Payload.Digest,
		ProtectionGeneration: handoff.Payload.ProtectionGeneration,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(canonical)
	return digest[:], nil
}

// ValidateHandoff makes the lease-sensitive effects state-dependent. An offer
// never carries a resulting fence; only accepted can carry the Ack and new
// fence produced by the wider K2 atomic transaction.
func ValidateHandoff(handoff Handoff) error {
	if err := validateMutableCommunicationEntity(handoff.MutableCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(handoff.WorkItemID) ||
		!validCanonicalCommunicationID(handoff.MessageID) ||
		!validCanonicalCommunicationID(handoff.DeliveryID) || handoff.From.Validate() != nil ||
		handoff.To.Validate() != nil || handoff.From == handoff.To || handoff.FromOwnerEpoch < 1 ||
		handoff.OfferedLeaseFence < 0 || handoff.ContextEventSeq < 1 ||
		len(handoff.ContextHash) != sha256.Size || ValidateProtectedPayloadSlot(handoff.Payload,
		PayloadSlotHandoff, protectedPayloadPolicyFrom(handoff.Payload)) != nil ||
		!handoff.State.Valid() || !handoff.AckDeadline.After(handoff.CreatedAt) {
		return communicationError(ErrInvalidCommunicationModel, "invalid Handoff envelope")
	}
	wantContextHash, err := CanonicalHandoffContextHash(handoff)
	if err != nil || !bytes.Equal(handoff.ContextHash, wantContextHash) {
		return communicationError(ErrInvalidCommunicationModel, "Handoff context hash mismatch")
	}
	times := 0
	for _, value := range []*time.Time{handoff.AcceptedAt, handoff.RejectedAt, handoff.WithdrawnAt, handoff.ExpiredAt} {
		if value != nil {
			times++
			if value.Before(handoff.CreatedAt) || value.After(handoff.UpdatedAt) {
				return communicationError(ErrInvalidCommunicationModel, "invalid Handoff terminal time")
			}
		}
	}
	if handoff.TerminalReason != nil && ValidateProtectedPayloadSlot(*handoff.TerminalReason,
		PayloadSlotHandoffTerminalReason, protectedPayloadPolicyFrom(handoff.Payload)) != nil {
		return communicationError(ErrInvalidCommunicationModel, "invalid Handoff protected reason")
	}
	switch handoff.State {
	case HandoffOffered:
		if times != 0 || handoff.AckID != "" || handoff.TerminalCode != "" ||
			handoff.TerminalReason != nil || handoff.ResultingLeaseFence != 0 {
			return communicationError(ErrInvalidCommunicationModel, "offered Handoff changes lease or terminal state")
		}
	case HandoffAccepted:
		if times != 1 || handoff.AcceptedAt == nil || !handoff.AcceptedAt.Before(handoff.AckDeadline) ||
			!validCanonicalCommunicationID(handoff.AckID) ||
			handoff.ResultingLeaseFence < 1 ||
			handoff.ResultingLeaseFence <= handoff.OfferedLeaseFence ||
			handoff.TerminalCode != "" || handoff.TerminalReason != nil {
			return communicationError(ErrInvalidCommunicationModel, "accepted Handoff lacks atomic lease effects")
		}
	case HandoffRejected, HandoffWithdrawn, HandoffExpired:
		wantTime := handoff.RejectedAt
		if handoff.State == HandoffWithdrawn {
			wantTime = handoff.WithdrawnAt
		} else if handoff.State == HandoffExpired {
			wantTime = handoff.ExpiredAt
		}
		if times != 1 || wantTime == nil || handoff.AckID != "" ||
			handoff.ResultingLeaseFence != 0 || !boundedToken(handoff.TerminalCode, 128) ||
			handoff.TerminalReason == nil {
			return communicationError(ErrInvalidCommunicationModel, "terminal Handoff has invalid effects")
		}
		if handoff.State == HandoffExpired && handoff.ExpiredAt.Before(handoff.AckDeadline) {
			return communicationError(ErrInvalidCommunicationModel, "Handoff expired before deadline")
		}
	}
	return nil
}

type HandoffTransitionPlan struct {
	Before       Handoff `json:"before"`
	After        Handoff `json:"after"`
	CreatesAck   bool    `json:"creates_ack"`
	ChangesLease bool    `json:"changes_lease"`
}

func PlanHandoffTransition(
	before Handoff,
	transition HandoffTransition,
	ackID model.ID,
	resultingLeaseFence int64,
	terminalCode string,
	terminalReason *ProtectedPayload,
	dbNow time.Time,
) (HandoffTransitionPlan, error) {
	if err := ValidateHandoff(before); err != nil {
		return HandoffTransitionPlan{}, err
	}
	to, err := NextHandoffState(before.State, transition)
	if err != nil {
		return HandoffTransitionPlan{}, err
	}
	if dbNow.IsZero() || dbNow.Before(before.UpdatedAt) {
		return HandoffTransitionPlan{}, communicationError(ErrInvalidCommunicationModel, "invalid Handoff DB time")
	}
	after := before
	after.Version++
	after.UpdatedAt = dbNow
	after.State = to
	plan := HandoffTransitionPlan{Before: before}
	if transition == HandoffAccept {
		if !dbNow.Before(before.AckDeadline) || !validCanonicalCommunicationID(ackID) ||
			resultingLeaseFence < 1 || resultingLeaseFence <= before.OfferedLeaseFence ||
			terminalCode != "" || terminalReason != nil {
			return HandoffTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
				"accept Handoff requires exact Ack and monotonic lease fence")
		}
		after.AckID = ackID
		after.AcceptedAt = &dbNow
		after.ResultingLeaseFence = resultingLeaseFence
		plan.CreatesAck = true
		plan.ChangesLease = true
	} else {
		if ackID != "" || resultingLeaseFence != 0 || !boundedToken(terminalCode, 128) ||
			terminalReason == nil {
			return HandoffTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
				"non-accept Handoff cannot change lease")
		}
		if terminalReason != nil && ValidateProtectedPayloadSlot(*terminalReason,
			PayloadSlotHandoffTerminalReason, protectedPayloadPolicyFrom(before.Payload)) != nil {
			return HandoffTransitionPlan{}, communicationError(ErrInvalidCommunicationModel,
				"invalid Handoff terminal reason")
		}
		after.TerminalCode = terminalCode
		after.TerminalReason = terminalReason
		switch transition {
		case HandoffReject:
			after.RejectedAt = &dbNow
		case HandoffWithdraw:
			after.WithdrawnAt = &dbNow
		case HandoffExpire:
			if dbNow.Before(before.AckDeadline) {
				return HandoffTransitionPlan{}, communicationError(ErrInvalidCommunicationTransition,
					"Handoff deadline has not elapsed")
			}
			after.ExpiredAt = &dbNow
		}
	}
	if err := ValidateHandoff(after); err != nil {
		return HandoffTransitionPlan{}, err
	}
	if err := validateHandoffTransitionLineage(before, after); err != nil {
		return HandoffTransitionPlan{}, err
	}
	plan.After = after
	return plan, nil
}

func validateHandoffTransitionLineage(before, after Handoff) error {
	if after.ID != before.ID || after.TenantID != before.TenantID ||
		after.WorkspaceID != before.WorkspaceID || after.CreatedAt != before.CreatedAt ||
		after.Version != before.Version+1 || after.WorkItemID != before.WorkItemID ||
		after.MessageID != before.MessageID || after.DeliveryID != before.DeliveryID ||
		after.From != before.From || after.FromOwnerEpoch != before.FromOwnerEpoch ||
		after.To != before.To || after.OfferedLeaseFence != before.OfferedLeaseFence ||
		after.ContextEventSeq != before.ContextEventSeq || !bytes.Equal(after.ContextHash, before.ContextHash) ||
		after.AckDeadline != before.AckDeadline {
		return communicationError(ErrInvalidCommunicationModel, "Handoff transition changed immutable lineage")
	}
	beforePayload, beforeErr := canonicalJSON(before.Payload)
	afterPayload, afterErr := canonicalJSON(after.Payload)
	if beforeErr != nil || afterErr != nil || !bytes.Equal(beforePayload, afterPayload) {
		return communicationError(ErrInvalidCommunicationModel, "Handoff transition changed protected payload")
	}
	return nil
}

func NextChannelState(current, target ChannelState) (ChannelState, error) {
	if !current.Valid() || !target.Valid() {
		return "", communicationError(ErrInvalidCommunicationModel, "invalid Channel state")
	}
	if current == ChannelArchived {
		return "", communicationError(ErrCommunicationTerminal, "Channel archived")
	}
	if target != ChannelArchived {
		return "", communicationError(ErrInvalidCommunicationTransition, "Channel active to %s", target)
	}
	return target, nil
}

func NextChannelGrantState(current, target ChannelGrantState) (ChannelGrantState, error) {
	if !current.Valid() || !target.Valid() {
		return "", communicationError(ErrInvalidCommunicationModel, "invalid ChannelGrant state")
	}
	if current != ChannelGrantActive {
		return "", communicationError(ErrCommunicationTerminal, "ChannelGrant %s", current)
	}
	if !oneOf(target, ChannelGrantRevoked, ChannelGrantExpired) {
		return "", communicationError(ErrInvalidCommunicationTransition, "ChannelGrant active to %s", target)
	}
	return target, nil
}

func NextChannelSubscriptionState(current, target ChannelSubscriptionState) (ChannelSubscriptionState, error) {
	if !current.Valid() || !target.Valid() {
		return "", communicationError(ErrInvalidCommunicationModel, "invalid ChannelSubscription state")
	}
	if current == SubscriptionRevoked {
		return "", communicationError(ErrCommunicationTerminal, "ChannelSubscription revoked")
	}
	if (current == SubscriptionActive && oneOf(target, SubscriptionPaused, SubscriptionRevoked)) ||
		(current == SubscriptionPaused && oneOf(target, SubscriptionActive, SubscriptionRevoked)) {
		return target, nil
	}
	return "", communicationError(ErrInvalidCommunicationTransition, "ChannelSubscription %s to %s", current, target)
}

func NextChannelLabelState(current, target ChannelLabelState) (ChannelLabelState, error) {
	if !current.Valid() || !target.Valid() {
		return "", communicationError(ErrInvalidCommunicationModel, "invalid ChannelLabel state")
	}
	if current == ChannelLabelDisabled {
		return "", communicationError(ErrCommunicationTerminal, "ChannelLabel disabled")
	}
	if target != ChannelLabelDisabled {
		return "", communicationError(ErrInvalidCommunicationTransition, "ChannelLabel active to %s", target)
	}
	return target, nil
}

func NextChannelRouteState(current, target ChannelRouteState) (ChannelRouteState, error) {
	if !current.Valid() || !target.Valid() {
		return "", communicationError(ErrInvalidCommunicationModel, "invalid ChannelRoute state")
	}
	if current == ChannelRouteDisabled {
		return "", communicationError(ErrCommunicationTerminal, "ChannelRoute disabled")
	}
	if target != ChannelRouteDisabled {
		return "", communicationError(ErrInvalidCommunicationTransition, "ChannelRoute active to %s", target)
	}
	return target, nil
}

func NextCommunicationEndpointState(current, target CommunicationEndpointState) (CommunicationEndpointState, error) {
	if !current.Valid() || !target.Valid() {
		return "", communicationError(ErrInvalidCommunicationModel, "invalid CommunicationEndpoint state")
	}
	if current == EndpointDisabled {
		return "", communicationError(ErrCommunicationTerminal, "CommunicationEndpoint disabled")
	}
	if (current == EndpointActive && oneOf(target, EndpointStale, EndpointDisabled)) ||
		(current == EndpointStale && oneOf(target, EndpointActive, EndpointDisabled)) {
		return target, nil
	}
	return "", communicationError(ErrInvalidCommunicationTransition, "CommunicationEndpoint %s to %s",
		current, target)
}

func NextCursorBarrierState(current, target CursorBarrierState) (CursorBarrierState, error) {
	if !current.Valid() || !target.Valid() {
		return "", communicationError(ErrInvalidCommunicationModel, "invalid cursor barrier state")
	}
	if current == CursorBarrierResolved {
		return "", communicationError(ErrCommunicationTerminal, "cursor barrier resolved")
	}
	if target != CursorBarrierResolved {
		return "", communicationError(ErrInvalidCommunicationTransition, "cursor barrier active to %s", target)
	}
	return target, nil
}

func NextDeliveryAttemptState(current, target DeliveryAttemptState) (DeliveryAttemptState, error) {
	if !current.Valid() || !target.Valid() {
		return "", communicationError(ErrInvalidCommunicationModel, "invalid DeliveryAttempt state")
	}
	if current != AttemptReserved {
		return "", communicationError(ErrCommunicationTerminal, "DeliveryAttempt %s", current)
	}
	if !oneOf(target, AttemptFinished, AttemptAbandoned) {
		return "", communicationError(ErrInvalidCommunicationTransition, "DeliveryAttempt reserved to %s", target)
	}
	return target, nil
}

// NextDeliveryDispatchState covers all single-row transitions. Superseding is
// excluded because it is valid only together with a successor plan.
func NextDeliveryDispatchState(current, target DeliveryDispatchState) (DeliveryDispatchState, error) {
	if !current.Valid() || !target.Valid() {
		return "", communicationError(ErrInvalidCommunicationModel, "invalid DeliveryDispatch state")
	}
	if oneOf(current, DispatchSucceeded, DispatchDeadLetter, DispatchSuperseded) {
		return "", communicationError(ErrCommunicationTerminal, "DeliveryDispatch %s", current)
	}
	allowed := false
	switch current {
	case DispatchPending:
		allowed = target == DispatchInFlight
	case DispatchInFlight:
		allowed = oneOf(target, DispatchSucceeded, DispatchFailed, DispatchUnknown)
	case DispatchFailed:
		allowed = target == DispatchDeadLetter
	case DispatchUnknown:
		allowed = oneOf(target, DispatchSucceeded, DispatchFailed, DispatchDeadLetter)
	}
	if !allowed {
		return "", communicationError(ErrInvalidCommunicationTransition,
			"DeliveryDispatch %s to %s; supersede requires successor plan", current, target)
	}
	return target, nil
}

type UndeliverablePlan struct {
	Before            MessageDelivery                 `json:"before"`
	After             MessageDelivery                 `json:"after"`
	RecipientSnapshot RecipientSnapshot               `json:"recipient_snapshot"`
	Witness           store.DirectoryTombstoneWitness `json:"witness"`
	CreatesAck        bool                            `json:"creates_ack"`
}

func validateUndeliverableWitness(delivery MessageDelivery, witness store.DirectoryTombstoneWitness) error {
	if delivery.Recipient.Kind == RecipientSession {
		return communicationError(ErrInvalidCommunicationModel, "sessions have no retirement tombstone")
	}
	if err := witness.Principal.Validate(); err != nil || !validCanonicalCommunicationID(witness.TombstoneID) ||
		witness.TombstoneVersion != 1 || witness.RetirementEpoch < 1 {
		return communicationError(ErrCommunicationEvidenceUnknown, "invalid tombstone witness")
	}
	wantTombstoneKind := model.DirectoryTombstoneKind
	if delivery.Recipient.Kind == RecipientUser {
		wantTombstoneKind = model.UserTombstoneKind
	}
	principalKindMatches := witness.Principal.PrincipalKind == model.DirectoryPrincipalUser
	if delivery.Recipient.Kind == RecipientAgent {
		principalKindMatches = oneOf(witness.Principal.PrincipalKind,
			model.DirectoryPrincipalIdentity, model.DirectoryPrincipalAgent)
	}
	if !principalKindMatches || witness.TombstoneKind != wantTombstoneKind ||
		witness.Principal.PrincipalRef.String() != delivery.Recipient.Ref {
		return communicationError(ErrCommunicationEvidenceUnknown, "tombstone does not name exact recipient")
	}
	if delivery.Recipient.Kind == RecipientUser && witness.Principal.WorkspaceRef != "" {
		return communicationError(ErrCommunicationEvidenceUnknown, "User tombstone carries workspace")
	}
	if delivery.Recipient.Kind == RecipientAgent {
		switch witness.Principal.PrincipalKind {
		case model.DirectoryPrincipalIdentity:
			if witness.Principal.WorkspaceRef != "" {
				return communicationError(ErrCommunicationEvidenceUnknown,
					"Identity tombstone carries workspace")
			}
		case model.DirectoryPrincipalAgent:
			if witness.Principal.WorkspaceRef != delivery.WorkspaceID {
				return communicationError(ErrCommunicationEvidenceUnknown,
					"Agent tombstone does not name exact workspace")
			}
		}
	}
	return nil
}

// PlanUndeliverable implements C1. The only accepted cause is a complete core
// tombstone witness; reversible disappearance has no representable input here.
func PlanUndeliverable(
	message Message,
	delivery MessageDelivery,
	recipientSnapshot RecipientSnapshot,
	dbNow time.Time,
	code string,
) (UndeliverablePlan, error) {
	if err := ValidateMessageDelivery(delivery); err != nil {
		return UndeliverablePlan{}, err
	}
	validationRequired := int64(0)
	if message.AckPolicy == AckPolicyEachRequired {
		validationRequired = 1
	} else if message.AckPolicy == AckPolicyQuorum {
		validationRequired = message.AckQuorum
	}
	if err := ValidateMessage(message, validationRequired); err != nil {
		return UndeliverablePlan{}, err
	}
	if err := ValidateMessageDeliveryLineage(message, delivery); err != nil {
		return UndeliverablePlan{}, err
	}
	if message.State != MessagePublished || delivery.TenantID != message.TenantID ||
		delivery.WorkspaceID != message.WorkspaceID ||
		delivery.MessageID != message.ID || recipientSnapshot.Scope.TenantID != delivery.TenantID ||
		recipientSnapshot.Scope.WorkspaceID != delivery.WorkspaceID ||
		recipientSnapshot.Recipient != delivery.Recipient || recipientSnapshot.Eligible ||
		recipientSnapshot.Tombstone == nil {
		return UndeliverablePlan{}, communicationError(ErrInvalidCommunicationModel,
			"undeliverable plan crosses Message, directory scope or recipient")
	}
	if err := validateRecipientSnapshot(recipientSnapshot); err != nil {
		return UndeliverablePlan{}, err
	}
	if delivery.State != DeliveryAvailable {
		return UndeliverablePlan{}, communicationError(ErrCommunicationTerminal,
			"cannot retire delivery in state %s", delivery.State)
	}
	if dbNow.IsZero() || dbNow.Before(delivery.UpdatedAt) || dbNow.Before(message.UpdatedAt) ||
		!boundedToken(code, 128) {
		return UndeliverablePlan{}, communicationError(ErrInvalidCommunicationModel, "invalid retirement input")
	}
	if delivery.Required && delivery.AckDueAt != nil && !dbNow.Before(*delivery.AckDueAt) {
		return UndeliverablePlan{}, communicationError(ErrInvalidCommunicationTransition,
			"Ack deadline must materialize expiry before retirement")
	}
	if message.AckPolicy == AckPolicyNone && (message.AckDueAt != nil || delivery.AckDueAt != nil) {
		return UndeliverablePlan{}, communicationError(ErrInvalidCommunicationModel,
			"AckPolicy none carries an Ack deadline")
	}
	if delivery.Required && (message.AckPolicy == AckPolicyNone || delivery.AckDueAt == nil ||
		message.AckDueAt == nil || !delivery.AckDueAt.Equal(*message.AckDueAt)) {
		return UndeliverablePlan{}, communicationError(ErrInvalidCommunicationModel,
			"required retired delivery lacks Ack policy/deadline")
	}
	witness := *recipientSnapshot.Tombstone
	if err := validateUndeliverableWitness(delivery, witness); err != nil {
		return UndeliverablePlan{}, err
	}
	after := delivery
	after.Version++
	after.UpdatedAt = dbNow
	after.State = DeliveryUndeliverable
	after.RetirementTombstoneKind = witness.TombstoneKind
	after.RetirementTombstoneID = witness.TombstoneID
	after.RetirementTombstoneVersion = witness.TombstoneVersion
	after.RetirementEpoch = witness.RetirementEpoch
	after.UndeliverableAt = &dbNow
	after.UndeliverableCode = code
	if err := ValidateMessageDelivery(after); err != nil {
		return UndeliverablePlan{}, err
	}
	return UndeliverablePlan{
		Before: delivery, After: after, RecipientSnapshot: recipientSnapshot,
		Witness: witness, CreatesAck: false,
	}, nil
}

type MessageAckPlan struct {
	Before             MessageDelivery       `json:"before"`
	After              MessageDelivery       `json:"after"`
	Ack                MessageAck            `json:"ack"`
	Authority          ProtectedReadDecision `json:"authority"`
	LinksEffectiveAck  bool                  `json:"links_effective_ack"`
	MaterializesExpiry bool                  `json:"materializes_expiry"`
}

func PlanMessageAck(
	delivery MessageDelivery,
	ackID model.ID,
	actor CommunicationActorRef,
	onBehalfOf *RecipientRef,
	authority *ReadGateEvidence,
	note *ProtectedPayload,
	dbNow time.Time,
) (MessageAckPlan, error) {
	if err := ValidateMessageDelivery(delivery); err != nil {
		return MessageAckPlan{}, err
	}
	if !validCanonicalCommunicationID(ackID) || dbNow.IsZero() || dbNow.Before(delivery.UpdatedAt) ||
		actor.Validate() != nil ||
		actor.Kind == ActorSystem {
		return MessageAckPlan{}, communicationError(ErrInvalidCommunicationModel, "invalid Ack identity")
	}
	if dbNow.Before(delivery.AvailableAt) {
		return MessageAckPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"scheduled delivery cannot be acknowledged")
	}
	wantOperation := CommunicationDeliveryWrite
	if onBehalfOf != nil {
		wantOperation = CommunicationDeliveryAdmin
	}
	actorRecipient := RecipientRef{Kind: RecipientKind(actor.Kind), Ref: actor.Ref}
	if actorRecipient.Validate() != nil || authority == nil || authority.Operation != wantOperation ||
		authority.Carrier.Entity != (EntityRef{TenantID: delivery.TenantID,
			Kind: model.Kind("sessions.message_delivery"), ID: delivery.ID,
			WorkspaceID: delivery.WorkspaceID}) || authority.Recipient != delivery.Recipient {
		return MessageAckPlan{}, communicationError(ErrInvalidCommunicationModel,
			"Ack lacks exact composed carrier authority")
	}
	if authority.DBNow != dbNow {
		return MessageAckPlan{}, communicationError(ErrCommunicationEvidenceUnknown,
			"Ack authority was not evaluated at mutation DB time")
	}
	lockedDelivery, lockedErr := canonicalJSON(authority.CarrierState.Delivery)
	commandDelivery, commandErr := canonicalJSON(delivery)
	if lockedErr != nil || commandErr != nil || !bytes.Equal(lockedDelivery, commandDelivery) {
		return MessageAckPlan{}, communicationError(ErrCommunicationEvidenceUnknown,
			"Ack Delivery changed after authority evaluation")
	}
	authorityDecision, err := EvaluateCarrierGate(*authority)
	if err != nil {
		return MessageAckPlan{}, err
	}
	if authorityDecision.Verdict == VerdictUnknown {
		return MessageAckPlan{}, communicationError(ErrCommunicationEvidenceUnknown,
			"Ack carrier authority is unavailable")
	}
	if authorityDecision.Verdict != VerdictClean ||
		authorityDecision.PrincipalRecipient != actorRecipient || len(authorityDecision.Facts) == 0 {
		return MessageAckPlan{}, communicationError(ErrCommunicationForbidden,
			"Ack carrier authority denied")
	}
	if onBehalfOf != nil {
		if err := onBehalfOf.Validate(); err != nil || *onBehalfOf != delivery.Recipient {
			if err != nil {
				return MessageAckPlan{}, err
			}
			return MessageAckPlan{}, communicationError(ErrInvalidCommunicationModel,
				"Ack on-behalf recipient does not match delivery")
		}
		if note == nil {
			return MessageAckPlan{}, communicationError(ErrInvalidCommunicationModel,
				"Ack on-behalf requires protected reason")
		}
	} else {
		if actorRecipient != delivery.Recipient {
			return MessageAckPlan{}, communicationError(ErrInvalidCommunicationModel,
				"Ack actor does not match delivery recipient")
		}
		if err := actorRecipient.Validate(); err != nil {
			return MessageAckPlan{}, err
		}
	}
	if note != nil {
		if err := ValidateProtectedPayloadSlot(*note, PayloadSlotAckNote,
			protectedPayloadPolicyFrom(authority.CarrierState.Message.Payload)); err != nil {
			return MessageAckPlan{}, err
		}
	}
	if oneOf(delivery.State, DeliveryAcknowledged, DeliveryUndeliverable) {
		return MessageAckPlan{}, communicationError(ErrCommunicationTerminal,
			"delivery %s rejects Ack", delivery.State)
	}
	after := delivery
	late := oneOf(delivery.State, DeliveryExpired, DeliveryRetracted)
	materializesExpiry := false
	deadlineElapsed := delivery.AckDueAt != nil && !dbNow.Before(*delivery.AckDueAt)
	expiryElapsed := delivery.ExpiresAt != nil && !dbNow.Before(*delivery.ExpiresAt)
	if delivery.State == DeliveryAvailable && (deadlineElapsed || expiryElapsed) {
		after.State = DeliveryExpired
		after.Version++
		after.UpdatedAt = dbNow
		late = true
		materializesExpiry = true
	}
	linked := !late
	if linked {
		after.State = DeliveryAcknowledged
		after.Version++
		after.UpdatedAt = dbNow
		after.AckID = ackID
		after.AcknowledgedAt = &dbNow
	}
	ack := MessageAck{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: ackID, TenantID: delivery.TenantID, WorkspaceID: delivery.WorkspaceID,
			Version: 1, CreatedAt: dbNow,
		}},
		DeliveryID: delivery.ID, Kind: MessageAckReceived, Actor: actor, OnBehalfOf: onBehalfOf,
		Note: note, AcknowledgedAt: dbNow, Late: late,
	}
	if err := ValidateMessageDelivery(after); err != nil {
		return MessageAckPlan{}, err
	}
	if err := ValidateMessageAck(ack); err != nil {
		return MessageAckPlan{}, err
	}
	return MessageAckPlan{
		Before: delivery, After: after, Ack: ack, Authority: authorityDecision, LinksEffectiveAck: linked,
		MaterializesExpiry: materializesExpiry,
	}, nil
}

type FulfillmentProjection struct {
	State        FulfillmentState `json:"state"`
	Required     int64            `json:"required"`
	Acknowledged int64            `json:"acknowledged"`
	Viable       int64            `json:"viable"`
	Unmet        int64            `json:"unmet"`
	Quorum       int64            `json:"quorum,omitempty"`
}

type FulfillmentDeliverySetWitness struct {
	Scope         DirectoryScopeRef `json:"scope"`
	MessageID     model.ID          `json:"message_id"`
	DeliveryCount int64             `json:"delivery_count"`
	RequiredCount int64             `json:"required_count"`
	Digest        []byte            `json:"digest"`
	ObservedAt    time.Time         `json:"observed_at"`
	Evidence      AuthorityEvidence `json:"evidence"`
	EvidenceRef   string            `json:"evidence_ref"`
}

type fulfillmentDeliveryDigestInput struct {
	ID                         model.ID             `json:"id"`
	Version                    int64                `json:"version"`
	Recipient                  RecipientRef         `json:"recipient"`
	RecipientEpoch             int64                `json:"recipient_epoch"`
	DeliverySeq                int64                `json:"delivery_seq"`
	Required                   bool                 `json:"required"`
	State                      MessageDeliveryState `json:"state"`
	AvailableAt                time.Time            `json:"available_at"`
	AckDueAt                   *time.Time           `json:"ack_due_at,omitempty"`
	ExpiresAt                  *time.Time           `json:"expires_at,omitempty"`
	AckID                      model.ID             `json:"ack_id,omitempty"`
	AcknowledgedAt             *time.Time           `json:"acknowledged_at,omitempty"`
	RetirementTombstoneKind    model.Kind           `json:"retirement_tombstone_kind,omitempty"`
	RetirementTombstoneID      model.ID             `json:"retirement_tombstone_id,omitempty"`
	RetirementTombstoneVersion int64                `json:"retirement_tombstone_version,omitempty"`
	RetirementEpoch            int64                `json:"retirement_epoch,omitempty"`
	UndeliverableAt            *time.Time           `json:"undeliverable_at,omitempty"`
}

func CanonicalFulfillmentDeliverySetDigest(deliveries []MessageDelivery) ([]byte, error) {
	ordered := append([]MessageDelivery(nil), deliveries...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].DeliverySeq != ordered[j].DeliverySeq {
			return ordered[i].DeliverySeq < ordered[j].DeliverySeq
		}
		return ordered[i].ID.String() < ordered[j].ID.String()
	})
	inputs := make([]fulfillmentDeliveryDigestInput, 0, len(ordered))
	for _, delivery := range ordered {
		if err := ValidateMessageDelivery(delivery); err != nil {
			return nil, err
		}
		inputs = append(inputs, fulfillmentDeliveryDigestInput{
			ID: delivery.ID, Version: delivery.Version, Recipient: delivery.Recipient,
			RecipientEpoch: delivery.RecipientEpoch, DeliverySeq: delivery.DeliverySeq,
			Required: delivery.Required, State: delivery.State, AvailableAt: delivery.AvailableAt,
			AckDueAt: delivery.AckDueAt, ExpiresAt: delivery.ExpiresAt, AckID: delivery.AckID,
			AcknowledgedAt:             delivery.AcknowledgedAt,
			RetirementTombstoneKind:    delivery.RetirementTombstoneKind,
			RetirementTombstoneID:      delivery.RetirementTombstoneID,
			RetirementTombstoneVersion: delivery.RetirementTombstoneVersion,
			RetirementEpoch:            delivery.RetirementEpoch, UndeliverableAt: delivery.UndeliverableAt,
		})
	}
	raw, err := canonicalJSON(inputs)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

// ProjectMessageFulfillment implements C3 from one caller-supplied DB time. It
// never persists a global fulfillment lifecycle.
func ProjectMessageFulfillment(
	message Message,
	deliveries []MessageDelivery,
	witness FulfillmentDeliverySetWitness,
	dbNow time.Time,
) (FulfillmentProjection, error) {
	if !oneOf(message.State, MessagePublished, MessageRetracted, MessageExpired) ||
		dbNow.IsZero() || dbNow.Before(message.UpdatedAt) || witness.Scope.Validate() != nil ||
		witness.Scope.TenantID != message.TenantID ||
		witness.Scope.WorkspaceID != message.WorkspaceID || witness.MessageID != message.ID ||
		witness.DeliveryCount != int64(len(deliveries)) || witness.RequiredCount < 0 ||
		len(witness.Digest) != sha256.Size || witness.ObservedAt != dbNow ||
		ValidateAuthorityEvidence(witness.Evidence) != nil ||
		evidenceVerdict(witness.Evidence) != VerdictClean || !validateOpaqueRef(witness.EvidenceRef) ||
		witness.Evidence.EvidenceRef != witness.EvidenceRef {
		return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel, "invalid fulfillment input")
	}
	requiredCount := int64(0)
	seenIDs := make(map[model.ID]struct{}, len(deliveries))
	seenRecipients := make(map[RecipientRef]struct{}, len(deliveries))
	seenSequences := make(map[int64]struct{}, len(deliveries))
	for _, delivery := range deliveries {
		if err := ValidateMessageDelivery(delivery); err != nil {
			return FulfillmentProjection{}, err
		}
		if err := ValidateMessageDeliveryLineage(message, delivery); err != nil {
			return FulfillmentProjection{}, err
		}
		if delivery.TenantID != message.TenantID || delivery.WorkspaceID != message.WorkspaceID ||
			delivery.MessageID != message.ID || dbNow.Before(delivery.UpdatedAt) ||
			(message.State != MessagePublished && delivery.State == DeliveryAvailable) {
			return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel,
				"fulfillment delivery crosses Message lineage")
		}
		if _, duplicate := seenIDs[delivery.ID]; duplicate {
			return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel,
				"duplicate Delivery ID in fulfillment projection")
		}
		if _, duplicate := seenRecipients[delivery.Recipient]; duplicate {
			return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel,
				"duplicate recipient in fulfillment projection")
		}
		if _, duplicate := seenSequences[delivery.DeliverySeq]; duplicate {
			return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel,
				"duplicate delivery sequence in fulfillment projection")
		}
		seenIDs[delivery.ID] = struct{}{}
		seenRecipients[delivery.Recipient] = struct{}{}
		seenSequences[delivery.DeliverySeq] = struct{}{}
		if delivery.Required {
			requiredCount++
			if message.AckDueAt == nil || delivery.AckDueAt == nil ||
				!delivery.AckDueAt.Equal(*message.AckDueAt) {
				return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel,
					"required delivery does not preserve Message Ack deadline")
			}
		}
		if message.AckPolicy == AckPolicyNone && delivery.AckDueAt != nil {
			return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel,
				"none-policy Delivery carries Ack deadline")
		}
	}
	digest, err := CanonicalFulfillmentDeliverySetDigest(deliveries)
	if err != nil {
		return FulfillmentProjection{}, err
	}
	if requiredCount != witness.RequiredCount || !bytes.Equal(digest, witness.Digest) {
		return FulfillmentProjection{}, communicationError(ErrCommunicationEvidenceUnknown,
			"fulfillment input is not the complete required Delivery set")
	}
	if err := ValidateMessage(message, requiredCount); err != nil {
		return FulfillmentProjection{}, err
	}
	policy := message.AckPolicy
	quorum := message.AckQuorum
	projection := FulfillmentProjection{Quorum: quorum}
	for _, delivery := range deliveries {
		if !delivery.Required {
			continue
		}
		projection.Required++
		if delivery.AckDueAt == nil {
			return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel,
				"required delivery lacks Ack deadline")
		}
		switch delivery.State {
		case DeliveryAcknowledged:
			if delivery.AckID == "" || delivery.AcknowledgedAt == nil ||
				delivery.AcknowledgedAt.After(*delivery.AckDueAt) {
				return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel,
					"acknowledged delivery lacks timely effective Ack")
			}
			projection.Acknowledged++
		case DeliveryAvailable:
			if dbNow.Before(*delivery.AckDueAt) {
				projection.Viable++
			} else {
				projection.Unmet++
			}
		case DeliveryExpired, DeliveryRetracted, DeliveryUndeliverable:
			projection.Unmet++
		default:
			return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel,
				"unknown required delivery state %q", delivery.State)
		}
	}
	if projection.Required != projection.Acknowledged+projection.Viable+projection.Unmet {
		return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel, "R != A+V+U")
	}
	switch policy {
	case AckPolicyNone:
		if quorum != 0 || projection.Required != 0 {
			return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel,
				"none policy has required deliveries")
		}
		projection.State = FulfillmentNotRequired
	case AckPolicyEachRequired:
		if quorum != 0 || projection.Required < 1 {
			return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel,
				"invalid each-required vector")
		}
		switch {
		case projection.Unmet > 0:
			projection.State = FulfillmentUnmet
		case projection.Acknowledged == projection.Required:
			projection.State = FulfillmentMet
		default:
			projection.State = FulfillmentPending
		}
	case AckPolicyQuorum:
		if quorum < 1 || quorum > projection.Required {
			return FulfillmentProjection{}, communicationError(ErrInvalidCommunicationModel, "invalid quorum")
		}
		switch {
		case projection.Acknowledged >= quorum:
			projection.State = FulfillmentMet
		case projection.Acknowledged+projection.Viable < quorum:
			projection.State = FulfillmentUnmet
		default:
			projection.State = FulfillmentPending
		}
	}
	return projection, nil
}

type AudienceFold struct {
	Recipient         RecipientRef               `json:"recipient"`
	DeliveryID        model.ID                   `json:"delivery_id"`
	Required          bool                       `json:"required"`
	WakePolicy        WakePolicy                 `json:"wake_policy"`
	RouteReasons      []RouteReason              `json:"route_reasons"`
	RouteReasonsHash  []byte                     `json:"route_reasons_hash"`
	Contributions     []MessageAudienceRecipient `json:"contributions"`
	ContributionsHash []byte                     `json:"contributions_hash"`
}

func validateCausalFact(kind model.Kind, id model.ID, version int64) (bool, error) {
	absent := kind == "" && id == "" && version == 0
	present := kind != "" && id != "" && version > 0
	if (!absent && !present) || (present &&
		(!validateOpaqueRef(string(kind)) || !validCanonicalCommunicationID(id))) {
		return false, communicationError(ErrInvalidCommunicationModel, "invalid causal fact witness")
	}
	return present, nil
}

func validateSubscriberExpansion(subject *CommunicationSubjectRef, recipient RecipientRef) (bool, error) {
	if subject == nil || subject.Validate() != nil {
		return false, communicationError(ErrInvalidCommunicationModel,
			"subscriber causal contribution lacks original subscriber")
	}
	switch subject.Kind {
	case SubjectUser:
		return false, requireCommunication(subject.Ref == recipient.Ref && recipient.Kind == RecipientUser,
			"direct User subscriber does not match recipient")
	case SubjectAgent:
		return false, requireCommunication(subject.Ref == recipient.Ref && recipient.Kind == RecipientAgent,
			"direct Agent subscriber does not match recipient")
	case SubjectSession:
		return false, requireCommunication(subject.Ref == recipient.Ref && recipient.Kind == RecipientSession,
			"direct session subscriber does not match recipient")
	case SubjectUserGroup:
		return true, requireCommunication(recipient.Kind == RecipientUser,
			"User group subscriber produced non-User recipient")
	case SubjectAgentGroup:
		return true, requireCommunication(recipient.Kind == RecipientAgent,
			"Agent group subscriber produced non-Agent recipient")
	default:
		return false, communicationError(ErrInvalidCommunicationModel, "unknown original subscriber kind")
	}
}

func requireCommunication(condition bool, message string) error {
	if !condition {
		return communicationError(ErrInvalidCommunicationModel, "%s", message)
	}
	return nil
}

func validateAudienceCausality(
	selector AudienceSelector,
	recipient RecipientRef,
	workspaceID model.ID,
	causalKind AudienceCausalKind,
	causalRef string,
	factKind model.Kind,
	factID model.ID,
	factVersion int64,
	observedSessionSID string,
	observedClaimFence int64,
	originalSubscriber *CommunicationSubjectRef,
	subscriptionID model.ID,
	subscriptionGeneration int64,
) error {
	if selector.Validate() != nil || recipient.Validate() != nil ||
		!validCanonicalCommunicationID(workspaceID) || !causalKind.Valid() || !validateOpaqueRef(causalRef) {
		return communicationError(ErrInvalidCommunicationModel, "invalid causal binding envelope")
	}
	factPresent, err := validateCausalFact(factKind, factID, factVersion)
	if err != nil {
		return err
	}
	if recipient.Kind == RecipientSession {
		if observedSessionSID != recipient.Ref || observedClaimFence < 1 {
			return communicationError(ErrInvalidCommunicationModel,
				"session contribution lacks exact Claim fence")
		}
	} else if observedSessionSID != "" || observedClaimFence != 0 {
		return communicationError(ErrInvalidCommunicationModel,
			"non-session contribution carries Claim fence")
	}
	subscriptionPresent := subscriptionID != "" && subscriptionGeneration > 0
	if (subscriptionID == "") != (subscriptionGeneration == 0) ||
		(subscriptionPresent && !validCanonicalCommunicationID(subscriptionID)) {
		return communicationError(ErrInvalidCommunicationModel, "invalid subscription provenance")
	}
	if causalKind != CausalSubscriber && (subscriptionPresent || originalSubscriber != nil) {
		return communicationError(ErrInvalidCommunicationModel,
			"non-subscriber causality carries subscriber provenance")
	}

	switch causalKind {
	case CausalDirect:
		if causalRef != selector.Ref || causalRef != recipient.Ref || factPresent {
			return communicationError(ErrInvalidCommunicationModel, "direct causality does not name recipient")
		}
		validPair := (selector.Kind == AudienceUser && recipient.Kind == RecipientUser) ||
			(selector.Kind == AudienceAgent && recipient.Kind == RecipientAgent) ||
			(selector.Kind == AudienceSession && recipient.Kind == RecipientSession)
		return requireCommunication(validPair, "direct selector kind does not match recipient")
	case CausalUserGroup:
		return requireCommunication(selector.Kind == AudienceUserGroup && recipient.Kind == RecipientUser &&
			causalRef == selector.Ref && factPresent && factKind == model.Kind("core.user_group_member"),
			"invalid User group causal binding")
	case CausalAgentGroup:
		return requireCommunication(selector.Kind == AudienceAgentGroup && recipient.Kind == RecipientAgent &&
			causalRef == selector.Ref && factPresent && factKind == model.Kind("core.agent_group_member"),
			"invalid Agent group causal binding")
	case CausalWorkspaceMember:
		factMatchesRecipient := (recipient.Kind == RecipientUser && factKind == model.Kind("core.membership")) ||
			(recipient.Kind == RecipientAgent && factKind == model.Kind("core.agent"))
		return requireCommunication(selector.Kind == AudienceWorkspaceMembers && selector.Ref == "" &&
			oneOf(recipient.Kind, RecipientUser, RecipientAgent) && causalRef == workspaceID.String() &&
			factPresent && factMatchesRecipient,
			"invalid workspace-member causal binding")
	case CausalSubscriber:
		if selector.Kind != AudienceSubscribers || !subscriptionPresent {
			return communicationError(ErrInvalidCommunicationModel, "invalid subscriber causal binding")
		}
		factRequired, err := validateSubscriberExpansion(originalSubscriber, recipient)
		if err != nil {
			return err
		}
		factKindMatches := true
		if factRequired && originalSubscriber.Kind == SubjectUserGroup {
			factKindMatches = factKind == model.Kind("core.user_group_member")
		} else if factRequired && originalSubscriber.Kind == SubjectAgentGroup {
			factKindMatches = factKind == model.Kind("core.agent_group_member")
		}
		if causalRef != originalSubscriber.Ref || (factRequired && (!factPresent || !factKindMatches)) ||
			(!factRequired && factPresent) {
			return communicationError(ErrInvalidCommunicationModel,
				"subscriber causality loses original subject evidence")
		}
		return nil
	default:
		return communicationError(ErrInvalidCommunicationModel, "unknown audience causality")
	}
}

type audienceCausalArcHashInput struct {
	MessageAudienceID      model.ID                 `json:"message_audience_id"`
	MessageDeliveryID      model.ID                 `json:"message_delivery_id"`
	Recipient              RecipientRef             `json:"recipient"`
	CausalKind             AudienceCausalKind       `json:"causal_kind"`
	CausalRef              string                   `json:"causal_ref"`
	ObservedSessionSID     string                   `json:"observed_session_sid,omitempty"`
	ObservedClaimFence     int64                    `json:"observed_claim_fence,omitempty"`
	OriginalSubscriber     *CommunicationSubjectRef `json:"original_subscriber,omitempty"`
	SubscriptionID         model.ID                 `json:"subscription_id,omitempty"`
	SubscriptionGeneration int64                    `json:"subscription_generation,omitempty"`
	RouteRuleID            model.ID                 `json:"route_rule_id,omitempty"`
	RouteRuleGeneration    int64                    `json:"route_rule_generation,omitempty"`
}

func validateAudienceContributionCore(contribution MessageAudienceRecipient) error {
	if err := validateAppendOnlyCommunicationEntity(contribution.AppendOnlyCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(contribution.MessageAudienceID) ||
		!validCanonicalCommunicationID(contribution.MessageDeliveryID) || contribution.Recipient.Validate() != nil ||
		contribution.RecipientEpoch < 1 || !contribution.WakePolicy.Valid() ||
		contribution.WakePolicy == WakeInherit || contribution.Selector.Validate() != nil ||
		contribution.DirectoryEpoch < 1 || contribution.ChannelACLRevision < 1 ||
		contribution.RouteRevision < 1 || contribution.SubscriptionRevision < 1 ||
		!contribution.CausalKind.Valid() || !validateOpaqueRef(contribution.CausalRef) {
		return communicationError(ErrInvalidCommunicationModel, "invalid audience contribution")
	}
	if err := validateAudienceCausality(
		contribution.Selector, contribution.Recipient, contribution.WorkspaceID,
		contribution.CausalKind, contribution.CausalRef,
		contribution.CausalFactKind, contribution.CausalFactID, contribution.CausalFactVersion,
		contribution.ObservedSessionSID, contribution.ObservedClaimFence,
		contribution.OriginalSubscriber, contribution.SubscriptionID,
		contribution.SubscriptionGeneration,
	); err != nil {
		return err
	}
	if (contribution.RouteRuleID == "") != (contribution.RouteRuleGeneration == 0) ||
		(contribution.RouteRuleID != "" && (!validCanonicalCommunicationID(contribution.RouteRuleID) ||
			contribution.RouteRuleGeneration < 1)) {
		return communicationError(ErrInvalidCommunicationModel, "invalid route provenance")
	}
	if err := validateCanonicalRouteReasons(contribution.RouteReasons); err != nil {
		return err
	}
	return nil
}

func CanonicalAudienceCausalArcHash(contribution MessageAudienceRecipient) ([]byte, error) {
	if err := validateAudienceContributionCore(contribution); err != nil {
		return nil, err
	}
	raw, err := canonicalJSON(audienceCausalArcHashInput{
		MessageAudienceID: contribution.MessageAudienceID, MessageDeliveryID: contribution.MessageDeliveryID,
		Recipient: contribution.Recipient, CausalKind: contribution.CausalKind,
		CausalRef:          contribution.CausalRef,
		ObservedSessionSID: contribution.ObservedSessionSID,
		ObservedClaimFence: contribution.ObservedClaimFence,
		OriginalSubscriber: contribution.OriginalSubscriber, SubscriptionID: contribution.SubscriptionID,
		SubscriptionGeneration: contribution.SubscriptionGeneration,
		RouteRuleID:            contribution.RouteRuleID, RouteRuleGeneration: contribution.RouteRuleGeneration,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func validateAudienceContribution(contribution MessageAudienceRecipient) error {
	if err := validateAudienceContributionCore(contribution); err != nil {
		return err
	}
	want, err := CanonicalAudienceCausalArcHash(contribution)
	if err != nil {
		return err
	}
	if len(contribution.CausalArcHash) != sha256.Size || !bytes.Equal(contribution.CausalArcHash, want) {
		return communicationError(ErrInvalidCommunicationModel, "audience contribution causal arc hash mismatch")
	}
	return nil
}

func validateCanonicalRouteReasons(reasons []RouteReason) error {
	if len(reasons) < 1 || len(reasons) > 32 {
		return communicationError(ErrInvalidCommunicationModel,
			"route reasons must be a bounded non-empty set")
	}
	for index, reason := range reasons {
		if !boundedToken(string(reason), 128) ||
			(index > 0 && reasons[index-1] >= reason) {
			return communicationError(ErrInvalidCommunicationModel,
				"route reasons must be bounded, unique and canonically sorted")
		}
	}
	return nil
}

func wakeRank(policy WakePolicy) int {
	switch policy {
	case WakeAll:
		return 3
	case WakePrimary:
		return 2
	case WakeNone:
		return 1
	default:
		return 0
	}
}

type audienceCausalArcIdentity struct {
	Recipient              RecipientRef
	CausalKind             AudienceCausalKind
	CausalRef              string
	ObservedSessionSID     string
	ObservedClaimFence     int64
	OriginalSubscriberKind CommunicationSubjectKind
	OriginalSubscriberRef  string
	SubscriptionID         model.ID
	SubscriptionGeneration int64
	RouteRuleID            model.ID
	RouteRuleGeneration    int64
}

func resolvedAudienceCausalArcIdentity(contribution ResolvedAudienceContribution) audienceCausalArcIdentity {
	identity := audienceCausalArcIdentity{
		Recipient: contribution.Recipient.Recipient, CausalKind: contribution.CausalKind,
		CausalRef: contribution.CausalRef, ObservedSessionSID: contribution.ObservedSessionSID,
		ObservedClaimFence: contribution.ObservedClaimFence, SubscriptionID: contribution.SubscriptionID,
		SubscriptionGeneration: contribution.SubscriptionGeneration,
		RouteRuleID:            contribution.RouteRuleID, RouteRuleGeneration: contribution.RouteRuleGeneration,
	}
	if contribution.OriginalSubscriber != nil {
		identity.OriginalSubscriberKind = contribution.OriginalSubscriber.Kind
		identity.OriginalSubscriberRef = contribution.OriginalSubscriber.Ref
	}
	return identity
}

func publishedAudienceCausalArcIdentity(contribution MessageAudienceRecipient) audienceCausalArcIdentity {
	identity := audienceCausalArcIdentity{
		Recipient: contribution.Recipient, CausalKind: contribution.CausalKind,
		CausalRef:          contribution.CausalRef,
		ObservedSessionSID: contribution.ObservedSessionSID,
		ObservedClaimFence: contribution.ObservedClaimFence, SubscriptionID: contribution.SubscriptionID,
		SubscriptionGeneration: contribution.SubscriptionGeneration,
		RouteRuleID:            contribution.RouteRuleID, RouteRuleGeneration: contribution.RouteRuleGeneration,
	}
	if contribution.OriginalSubscriber != nil {
		identity.OriginalSubscriberKind = contribution.OriginalSubscriber.Kind
		identity.OriginalSubscriberRef = contribution.OriginalSubscriber.Ref
	}
	return identity
}

type resolvedAudienceArcKey struct {
	SelectorOrdinal int64
	Arc             audienceCausalArcIdentity
}

type publishedAudienceArcKey struct {
	MessageAudienceID model.ID
	Arc               audienceCausalArcIdentity
}

func audienceContributionKey(contribution MessageAudienceRecipient) string {
	return strings.Join([]string{
		string(contribution.Selector.Kind), contribution.Selector.Ref,
		string(contribution.CausalKind), contribution.CausalRef,
		string(contribution.CausalFactKind), contribution.CausalFactID.String(),
		fmt.Sprint(contribution.CausalFactVersion),
		contribution.MessageAudienceID.String(), contribution.ID.String(),
	}, "\x00")
}

func normalizeRouteReasons(reasons []RouteReason) []RouteReason {
	normalized := append([]RouteReason(nil), reasons...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	out := normalized[:0]
	for _, reason := range normalized {
		if len(out) == 0 || out[len(out)-1] != reason {
			out = append(out, reason)
		}
	}
	return out
}

type audienceContributionHashInput struct {
	AudienceID             model.ID                 `json:"audience_id"`
	Recipient              RecipientRef             `json:"recipient"`
	RecipientEpoch         int64                    `json:"recipient_epoch"`
	Required               bool                     `json:"required"`
	WakePolicy             WakePolicy               `json:"wake_policy"`
	RouteReasons           []RouteReason            `json:"route_reasons"`
	Selector               AudienceSelector         `json:"selector"`
	DirectoryEpoch         int64                    `json:"directory_epoch"`
	ChannelACLRevision     int64                    `json:"channel_acl_revision"`
	RouteRevision          int64                    `json:"route_revision"`
	SubscriptionRevision   int64                    `json:"subscription_revision"`
	CausalKind             AudienceCausalKind       `json:"causal_kind"`
	CausalRef              string                   `json:"causal_ref"`
	CausalFactKind         model.Kind               `json:"causal_fact_kind,omitempty"`
	CausalFactID           model.ID                 `json:"causal_fact_id,omitempty"`
	CausalFactVersion      int64                    `json:"causal_fact_version,omitempty"`
	ObservedSessionSID     string                   `json:"observed_session_sid,omitempty"`
	ObservedClaimFence     int64                    `json:"observed_claim_fence,omitempty"`
	OriginalSubscriber     *CommunicationSubjectRef `json:"original_subscriber,omitempty"`
	SubscriptionID         model.ID                 `json:"subscription_id,omitempty"`
	SubscriptionGeneration int64                    `json:"subscription_generation,omitempty"`
	RouteRuleID            model.ID                 `json:"route_rule_id,omitempty"`
	RouteRuleGeneration    int64                    `json:"route_rule_generation,omitempty"`
	CausalArcHash          []byte                   `json:"causal_arc_hash"`
}

type sealedAudienceRowHashInput struct {
	AudienceID           model.ID         `json:"audience_id"`
	Ordinal              int64            `json:"ordinal"`
	Selector             AudienceSelector `json:"selector"`
	RouteRuleID          model.ID         `json:"route_rule_id,omitempty"`
	ChannelACLRevision   int64            `json:"channel_acl_revision"`
	RouteRevision        int64            `json:"route_revision"`
	SubscriptionRevision int64            `json:"subscription_revision"`
	DirectoryEpoch       int64            `json:"directory_epoch"`
	DirectorySnapshotAt  time.Time        `json:"directory_snapshot_at"`
	ResolvedCount        int64            `json:"resolved_count"`
	SelectorHash         []byte           `json:"selector_hash"`
	ResolvedHash         []byte           `json:"resolved_hash"`
}

func canonicalResolvedAudienceHash(
	audience MessageAudience,
	contributions []MessageAudienceRecipient,
) ([]byte, error) {
	ordered := append([]MessageAudienceRecipient(nil), contributions...)
	sort.Slice(ordered, func(i, j int) bool { return audienceContributionKey(ordered[i]) < audienceContributionKey(ordered[j]) })
	inputs := make([]audienceContributionHashInput, 0, len(ordered))
	seenArcs := make(map[publishedAudienceArcKey]struct{}, len(ordered))
	for _, contribution := range ordered {
		if err := validateAudienceContribution(contribution); err != nil {
			return nil, err
		}
		if contribution.MessageAudienceID != audience.ID || contribution.TenantID != audience.TenantID ||
			contribution.WorkspaceID != audience.WorkspaceID || contribution.Selector != audience.Selector ||
			contribution.DirectoryEpoch != audience.DirectoryEpoch ||
			contribution.ChannelACLRevision != audience.ChannelACLRevision ||
			contribution.RouteRevision != audience.RouteRevision ||
			contribution.SubscriptionRevision != audience.SubscriptionRevision {
			return nil, communicationError(ErrInvalidCommunicationModel,
				"audience recipient crosses selector publication lineage")
		}
		if audience.RouteRuleID != "" && contribution.RouteRuleID != audience.RouteRuleID {
			return nil, communicationError(ErrInvalidCommunicationModel,
				"audience recipient loses route rule lineage")
		}
		arcKey := publishedAudienceArcKey{
			MessageAudienceID: contribution.MessageAudienceID,
			Arc:               publishedAudienceCausalArcIdentity(contribution),
		}
		if _, duplicate := seenArcs[arcKey]; duplicate {
			return nil, communicationError(ErrInvalidCommunicationModel,
				"audience selector contains duplicate recipient causal arc")
		}
		seenArcs[arcKey] = struct{}{}
		inputs = append(inputs, audienceContributionHashInput{
			AudienceID: contribution.MessageAudienceID, Recipient: contribution.Recipient,
			RecipientEpoch: contribution.RecipientEpoch, Required: contribution.Required,
			WakePolicy: contribution.WakePolicy, RouteReasons: normalizeRouteReasons(contribution.RouteReasons),
			Selector: contribution.Selector, DirectoryEpoch: contribution.DirectoryEpoch,
			ChannelACLRevision: contribution.ChannelACLRevision, RouteRevision: contribution.RouteRevision,
			SubscriptionRevision: contribution.SubscriptionRevision, CausalKind: contribution.CausalKind,
			CausalRef: contribution.CausalRef, CausalFactKind: contribution.CausalFactKind,
			CausalFactID: contribution.CausalFactID, CausalFactVersion: contribution.CausalFactVersion,
			ObservedSessionSID: contribution.ObservedSessionSID,
			ObservedClaimFence: contribution.ObservedClaimFence,
			OriginalSubscriber: contribution.OriginalSubscriber, SubscriptionID: contribution.SubscriptionID,
			SubscriptionGeneration: contribution.SubscriptionGeneration,
			RouteRuleID:            contribution.RouteRuleID, RouteRuleGeneration: contribution.RouteRuleGeneration,
			CausalArcHash: contribution.CausalArcHash,
		})
	}
	raw, err := canonicalJSON(inputs)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

// CanonicalMessageAudienceHash validates the complete selector × recipient
// publication snapshot and returns the Message audience seal. It prevents a
// caller from supplying an arbitrary 32-byte AudienceHash.
func CanonicalMessageAudienceHash(
	message Message,
	audiences []MessageAudience,
	contributions []MessageAudienceRecipient,
) ([]byte, error) {
	if !validCanonicalCommunicationID(message.ID) || !validCanonicalCommunicationTenant(message.TenantID) ||
		!validCanonicalCommunicationID(message.WorkspaceID) || len(audiences) == 0 || len(audiences) > 64 {
		return nil, communicationError(ErrInvalidCommunicationModel, "invalid Message audience seal input")
	}
	byAudience := make(map[model.ID][]MessageAudienceRecipient, len(audiences))
	for _, contribution := range contributions {
		byAudience[contribution.MessageAudienceID] = append(byAudience[contribution.MessageAudienceID], contribution)
	}
	ordered := append([]MessageAudience(nil), audiences...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Ordinal < ordered[j].Ordinal })
	rows := make([]sealedAudienceRowHashInput, 0, len(ordered))
	seenIDs := make(map[model.ID]struct{}, len(ordered))
	usedContributions := 0
	for index, audience := range ordered {
		if err := ValidateMessageAudience(audience); err != nil {
			return nil, err
		}
		if audience.TenantID != message.TenantID || audience.WorkspaceID != message.WorkspaceID ||
			audience.MessageID != message.ID || audience.Ordinal != int64(index+1) {
			return nil, communicationError(ErrInvalidCommunicationModel,
				"MessageAudience crosses Message lineage or ordinal sequence")
		}
		if _, duplicate := seenIDs[audience.ID]; duplicate {
			return nil, communicationError(ErrInvalidCommunicationModel, "duplicate MessageAudience ID")
		}
		seenIDs[audience.ID] = struct{}{}
		resolved := byAudience[audience.ID]
		usedContributions += len(resolved)
		resolvedRecipients := make(map[RecipientRef]struct{}, len(resolved))
		for _, contribution := range resolved {
			resolvedRecipients[contribution.Recipient] = struct{}{}
		}
		if int64(len(resolvedRecipients)) != audience.ResolvedCount {
			return nil, communicationError(ErrInvalidCommunicationModel, "audience resolved count mismatch")
		}
		wantResolvedHash, err := canonicalResolvedAudienceHash(audience, resolved)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(wantResolvedHash, audience.ResolvedHash) {
			return nil, communicationError(ErrInvalidCommunicationModel, "audience resolved hash mismatch")
		}
		rows = append(rows, sealedAudienceRowHashInput{
			AudienceID: audience.ID, Ordinal: audience.Ordinal, Selector: audience.Selector,
			RouteRuleID: audience.RouteRuleID, ChannelACLRevision: audience.ChannelACLRevision,
			RouteRevision: audience.RouteRevision, SubscriptionRevision: audience.SubscriptionRevision,
			DirectoryEpoch: audience.DirectoryEpoch, DirectorySnapshotAt: audience.DirectorySnapshotAt,
			ResolvedCount: audience.ResolvedCount, SelectorHash: audience.SelectorHash,
			ResolvedHash: audience.ResolvedHash,
		})
	}
	if usedContributions != len(contributions) {
		return nil, communicationError(ErrInvalidCommunicationModel, "orphan audience recipient contribution")
	}
	raw, err := canonicalJSON(rows)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func validatePublishAudienceDerivation(
	message Message,
	snapshot DirectorySnapshot,
	audiences []MessageAudience,
	contributions []MessageAudienceRecipient,
	deliveries []MessageDelivery,
	dbNow time.Time,
) (int64, error) {
	audienceByID := make(map[model.ID]MessageAudience, len(audiences))
	for _, audience := range audiences {
		audienceByID[audience.ID] = audience
	}
	expected := make(map[resolvedAudienceArcKey]ResolvedAudienceContribution, len(snapshot.Contributions))
	for _, contribution := range snapshot.Contributions {
		key := resolvedAudienceArcKey{
			SelectorOrdinal: contribution.SelectorOrdinal,
			Arc:             resolvedAudienceCausalArcIdentity(contribution),
		}
		if _, duplicate := expected[key]; duplicate {
			return 0, communicationError(ErrInvalidCommunicationModel,
				"authoritative directory snapshot repeats a causal arc")
		}
		expected[key] = contribution
	}
	byRecipient := make(map[RecipientRef][]MessageAudienceRecipient)
	for _, row := range contributions {
		audience, present := audienceByID[row.MessageAudienceID]
		if !present {
			return 0, communicationError(ErrInvalidCommunicationModel, "audience recipient has unknown selector row")
		}
		key := resolvedAudienceArcKey{
			SelectorOrdinal: audience.Ordinal,
			Arc:             publishedAudienceCausalArcIdentity(row),
		}
		resolved, present := expected[key]
		if !present || resolved.Selector != row.Selector ||
			resolved.Recipient.RecipientEpoch != row.RecipientEpoch ||
			resolved.Recipient.DirectoryEpoch != row.DirectoryEpoch || resolved.CausalKind != row.CausalKind ||
			resolved.Required != row.Required || resolved.WakePolicy != row.WakePolicy ||
			!equalRouteReasons(resolved.RouteReasons, row.RouteReasons) ||
			resolved.RouteRuleID != row.RouteRuleID ||
			resolved.RouteRuleGeneration != row.RouteRuleGeneration ||
			resolved.CausalRef != row.CausalRef || resolved.ObservedSessionSID != row.ObservedSessionSID ||
			resolved.ObservedClaimFence != row.ObservedClaimFence ||
			resolved.SubscriptionID != row.SubscriptionID ||
			resolved.SubscriptionGeneration != row.SubscriptionGeneration ||
			!equalCommunicationSubjectPtr(resolved.OriginalSubscriber, row.OriginalSubscriber) ||
			!equalAuthorizationFactPtr(resolved.CausalFact, row.CausalFactKind,
				row.CausalFactID, row.CausalFactVersion) {
			return 0, communicationError(ErrInvalidCommunicationModel,
				"audience recipient was not produced by authoritative directory snapshot")
		}
		delete(expected, key)
		byRecipient[row.Recipient] = append(byRecipient[row.Recipient], row)
	}
	if len(expected) != 0 {
		return 0, communicationError(ErrInvalidCommunicationModel,
			"authoritative directory contribution missing from publication")
	}
	deliveryByRecipient := make(map[RecipientRef]MessageDelivery, len(deliveries))
	for _, delivery := range deliveries {
		if err := ValidateMessageDelivery(delivery); err != nil {
			return 0, err
		}
		if delivery.TenantID != message.TenantID || delivery.WorkspaceID != message.WorkspaceID ||
			delivery.MessageID != message.ID || delivery.Version != 1 || delivery.CreatedAt != dbNow ||
			delivery.UpdatedAt != dbNow || delivery.State != DeliveryAvailable ||
			delivery.AvailableAt != message.AvailableAt || !equalOptionalTime(delivery.ExpiresAt, message.ExpiresAt) {
			return 0, communicationError(ErrInvalidCommunicationModel, "publish Delivery crosses Message plan")
		}
		if _, duplicate := deliveryByRecipient[delivery.Recipient]; duplicate {
			return 0, communicationError(ErrInvalidCommunicationModel, "duplicate publish Delivery recipient")
		}
		deliveryByRecipient[delivery.Recipient] = delivery
	}
	requiredCount := int64(0)
	for recipient, rows := range byRecipient {
		fold, err := FoldAudienceContributions(rows)
		if err != nil {
			return 0, err
		}
		delivery, present := deliveryByRecipient[recipient]
		if !present || delivery.ID != fold.DeliveryID || delivery.RecipientEpoch != rows[0].RecipientEpoch ||
			delivery.Required != fold.Required || delivery.WakePolicy != fold.WakePolicy ||
			!equalRouteReasons(normalizeRouteReasons(delivery.RouteReasons), fold.RouteReasons) {
			return 0, communicationError(ErrInvalidCommunicationModel,
				"publish Delivery does not equal canonical audience fold")
		}
		if delivery.Required {
			requiredCount++
			if !equalOptionalTime(delivery.AckDueAt, message.AckDueAt) {
				return 0, communicationError(ErrInvalidCommunicationModel,
					"required publish Delivery loses Message Ack deadline")
			}
		} else if message.AckPolicy == AckPolicyNone && delivery.AckDueAt != nil {
			return 0, communicationError(ErrInvalidCommunicationModel,
				"AckPolicy none publish Delivery carries deadline")
		}
		delete(deliveryByRecipient, recipient)
	}
	if len(deliveryByRecipient) != 0 {
		return 0, communicationError(ErrInvalidCommunicationModel, "publish plan contains orphan Delivery")
	}
	return requiredCount, nil
}

// MessagePublishChannelFence is the complete Channel/directory revision tuple
// that Apply must compare while the corresponding rows are locked.
type MessagePublishChannelFence struct {
	ChannelID            model.ID  `json:"channel_id"`
	DirectoryEpoch       int64     `json:"directory_epoch"`
	ACLRevision          int64     `json:"acl_revision"`
	RouteRevision        int64     `json:"route_revision"`
	SubscriptionRevision int64     `json:"subscription_revision"`
	EvaluatedAt          time.Time `json:"evaluated_at"`
}

// MessagePublishInput contains only server-authored or preflight evidence.
// The caller may preallocate row IDs, but it cannot choose sender, revisions,
// directory epoch, sequence allocation, or the audience seal.
type MessagePublishInput struct {
	Draft                 Message                        `json:"draft"`
	Channel               Channel                        `json:"channel"`
	AudienceRequest       PublicationAudienceRequest     `json:"audience_request"`
	AudienceAttestation   PublicationAudienceAttestation `json:"audience_attestation"`
	Snapshot              DirectorySnapshot              `json:"snapshot"`
	Audiences             []MessageAudience              `json:"audiences"`
	Contributions         []MessageAudienceRecipient     `json:"contributions"`
	Deliveries            []MessageDelivery              `json:"deliveries"`
	Labels                ChannelLabelSnapshot           `json:"labels"`
	SendGate              SendGateEvidence               `json:"send_gate"`
	Principal             CommunicationPrincipal         `json:"-"`
	Sender                CommunicationActorRef          `json:"sender"`
	SourceKind            ChannelRouteSourceKind         `json:"source_kind"`
	EventType             string                         `json:"event_type,omitempty"`
	MentionedRecipients   []RecipientRef                 `json:"mentioned_recipients,omitempty"`
	SenderResolution      *PrincipalResolution           `json:"sender_resolution,omitempty"`
	Parent                *Message                       `json:"parent,omitempty"`
	DeliverySequenceGuard CommunicationGuard             `json:"delivery_sequence_guard"`
	DBNow                 time.Time                      `json:"db_now"`
}

type MessagePublishPlan struct {
	Before         Message                        `json:"before"`
	After          Message                        `json:"after"`
	Audiences      []MessageAudience              `json:"audiences"`
	Contributions  []MessageAudienceRecipient     `json:"contributions"`
	Deliveries     []MessageDelivery              `json:"deliveries"`
	RequiredCount  int64                          `json:"required_count"`
	GuardAdvance   *CommunicationGuardAdvancePlan `json:"guard_advance,omitempty"`
	Facts          []store.AuthorizationFactRef   `json:"facts"`
	RequiredClaims []CommunicationClaimRef        `json:"required_claims,omitempty"`
	ChannelFence   MessagePublishChannelFence     `json:"channel_fence"`
}

func actorMatchesRecipient(actor CommunicationActorRef, recipient RecipientRef) bool {
	if actor.Ref != recipient.Ref {
		return false
	}
	switch actor.Kind {
	case ActorUser:
		return recipient.Kind == RecipientUser
	case ActorAgent:
		return recipient.Kind == RecipientAgent
	case ActorSession:
		return recipient.Kind == RecipientSession
	default:
		return false
	}
}

func validatePublishSender(
	principal CommunicationPrincipal,
	actor CommunicationActorRef,
	resolution *PrincipalResolution,
	scope DirectoryScopeRef,
	directoryEpoch int64,
	dbNow time.Time,
) error {
	if err := ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return err
	}
	if err := actor.Validate(); err != nil {
		return err
	}
	switch {
	case principal.System:
		if actor != (CommunicationActorRef{Kind: ActorSystem, Ref: principal.SystemActorRef}) ||
			resolution != nil {
			return communicationError(ErrInvalidCommunicationModel,
				"system publish sender is not server-derived")
		}
	case principal.UserID != "":
		if actor != (CommunicationActorRef{Kind: ActorUser, Ref: principal.UserID.String()}) ||
			resolution != nil {
			return communicationError(ErrInvalidCommunicationModel,
				"user publish sender is not server-derived")
		}
	case principal.SessionID != "":
		if actor != (CommunicationActorRef{Kind: ActorSession, Ref: principal.SessionID}) ||
			resolution != nil {
			return communicationError(ErrInvalidCommunicationModel,
				"session publish sender is not server-derived")
		}
	default:
		if resolution == nil {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"external Agent sender resolution is unavailable")
		}
		if err := ValidatePrincipalResolution(*resolution); err != nil {
			return err
		}
		if resolution.Scope != scope || resolution.Principal != principal ||
			!communicationEvidenceCurrent(resolution.ObservedAt, resolution.FreshUntil, dbNow) {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"publish sender resolution is stale or crosses authority scope")
		}
		switch resolution.Outcome {
		case PrincipalUnknown:
			return communicationError(ErrCommunicationEvidenceUnknown,
				"external Agent sender resolution is unavailable")
		case PrincipalNotFound:
			return communicationError(ErrCommunicationNotFound,
				"external Agent sender is not registered")
		case PrincipalResolved:
			if resolution.Recipient == nil ||
				resolution.Recipient.DirectoryEpoch != directoryEpoch {
				return communicationError(ErrCommunicationEvidenceUnknown,
					"publish sender resolution does not match directory snapshot")
			}
			if resolution.Recipient.Recipient.Kind != RecipientAgent ||
				actor != (CommunicationActorRef{Kind: ActorAgent,
					Ref: resolution.Recipient.Recipient.Ref}) {
				return communicationError(ErrInvalidCommunicationModel,
					"resolved Agent publish sender is not server-derived")
			}
		}
	}
	return nil
}

func validatePublishReply(draft Message, parent *Message, dbNow time.Time) error {
	if draft.ReplyToID == "" {
		if parent != nil {
			return communicationError(ErrInvalidCommunicationModel,
				"root Message publish carries a reply parent")
		}
		return nil
	}
	if parent == nil {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"reply parent is absent from the locked publish snapshot")
	}
	requiredFloor := int64(0)
	switch parent.AckPolicy {
	case AckPolicyEachRequired:
		requiredFloor = 1
	case AckPolicyQuorum:
		requiredFloor = parent.AckQuorum
	}
	if err := ValidateMessage(*parent, requiredFloor); err != nil {
		return err
	}
	if parent.State != MessagePublished || parent.PublishedAt == nil ||
		parent.PublishedAt.After(dbNow) || parent.UpdatedAt.After(dbNow) ||
		dbNow.Before(parent.AvailableAt) ||
		(parent.ExpiresAt != nil && !dbNow.Before(*parent.ExpiresAt)) {
		return communicationError(ErrCommunicationNotFound,
			"reply parent is not currently published and legible")
	}
	return ValidateMessageReplyLineage(*parent, draft)
}

// PlanMessagePublish is the authoritative pure publish planner. Apply must
// persist every returned row, the guard CAS, event/outbox and receipt in one
// mutation; a caller may not apply a subset of this plan.
func PlanMessagePublish(input MessagePublishInput) (MessagePublishPlan, error) {
	if input.DBNow.IsZero() || input.Draft.State != MessageDraft ||
		input.DBNow.Before(input.Draft.UpdatedAt) || input.Channel.State != ChannelActive {
		return MessagePublishPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"Message publish requires an active Channel, draft and monotonic DB time")
	}
	if input.Draft.AvailableAt.Before(input.DBNow) ||
		(input.Draft.AckDueAt != nil && !input.Draft.AckDueAt.After(input.DBNow)) ||
		(input.Draft.ExpiresAt != nil && !input.Draft.ExpiresAt.After(input.DBNow)) {
		return MessagePublishPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"Message publish cannot materialize an already elapsed availability window")
	}
	if err := ValidateChannel(input.Channel); err != nil {
		return MessagePublishPlan{}, err
	}
	if input.Draft.AutomationDepth > input.Channel.MaxAutomationDepth {
		return MessagePublishPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"Message automation depth exceeds the locked Channel ceiling")
	}
	scope := DirectoryScopeRef{TenantID: input.Channel.TenantID, WorkspaceID: input.Channel.WorkspaceID}
	if input.Snapshot.Scope != scope || input.Draft.TenantID != scope.TenantID ||
		input.Draft.WorkspaceID != scope.WorkspaceID || input.Draft.ChannelID != input.Channel.ID {
		return MessagePublishPlan{}, communicationError(ErrInvalidCommunicationModel,
			"publish input crosses Channel lineage")
	}
	if input.AudienceRequest.Scope != scope || input.AudienceRequest.ChannelID != input.Channel.ID ||
		input.AudienceRequest.ChannelACLRevision != input.Channel.ACLRevision ||
		input.AudienceRequest.RouteRevision != input.Channel.RouteRevision ||
		input.AudienceRequest.SubscriptionRevision != input.Channel.SubscriptionRevision ||
		input.AudienceRequest.MessageKind != input.Draft.Kind ||
		input.AudienceRequest.Urgency != input.Draft.Urgency ||
		input.AudienceRequest.Sender != input.Sender ||
		input.AudienceRequest.SourceKind != input.SourceKind ||
		input.AudienceRequest.EventType != input.EventType ||
		!equalRecipientRefs(input.AudienceRequest.MentionedRecipients, input.MentionedRecipients) ||
		!bytes.Equal(input.AudienceRequest.LabelsJSON, input.Draft.LabelsJSON) ||
		!bytes.Equal(input.AudienceRequest.LabelsHash, input.Draft.LabelsHash) ||
		input.AudienceRequest.ChannelDefaultWake != input.Channel.DefaultWake ||
		input.AudienceRequest.ContentProtection != input.Channel.ContentProtection ||
		input.AudienceRequest.ProtectionGeneration != input.Channel.ProtectionGeneration {
		return MessagePublishPlan{}, communicationError(ErrInvalidCommunicationModel,
			"publication audience request is not bound to Message and Channel policy")
	}
	if err := validatePublicationAudienceAttestation(
		input.AudienceRequest, input.Snapshot, input.AudienceAttestation, input.DBNow,
	); err != nil {
		return MessagePublishPlan{}, err
	}
	if err := validatePublishReply(input.Draft, input.Parent, input.DBNow); err != nil {
		return MessagePublishPlan{}, err
	}
	if err := ValidateDirectorySnapshotForSelectors(input.Snapshot, input.Snapshot.Selectors); err != nil {
		return MessagePublishPlan{}, err
	}
	if err := ValidateDirectorySnapshotAt(input.Snapshot, input.DBNow); err != nil {
		return MessagePublishPlan{}, err
	}
	if err := validatePublishSender(input.Principal, input.Sender, input.SenderResolution,
		scope, input.Snapshot.Epoch, input.DBNow); err != nil {
		return MessagePublishPlan{}, err
	}
	if input.Draft.Sender != input.Sender || input.SendGate.Principal != input.Principal ||
		input.SendGate.DBNow != input.DBNow || input.SendGate.Scope != scope ||
		input.SendGate.ChannelID != input.Channel.ID ||
		input.SendGate.ChannelACLRevision != input.Channel.ACLRevision {
		return MessagePublishPlan{}, communicationError(ErrInvalidCommunicationModel,
			"publish sender or send authority is not server-bound")
	}
	sendDecision, err := EvaluateSendGate(input.SendGate)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	switch sendDecision.Verdict {
	case VerdictUnknown:
		return MessagePublishPlan{}, communicationError(ErrCommunicationEvidenceUnknown,
			"message-send authority is unavailable")
	case VerdictBroken:
		return MessagePublishPlan{}, communicationError(ErrCommunicationForbidden,
			"message-send authority denied")
	}
	directoryFact, err := DirectorySnapshotAuthorityFact(input.Snapshot)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	if input.SendGate.DirectoryEpoch != directoryFact {
		return MessagePublishPlan{}, communicationError(ErrCommunicationSnapshotStale,
			"send authority and audience snapshot use different directory epochs")
	}
	if len(input.Audiences) != len(input.Snapshot.Selectors) ||
		int64(len(input.Deliveries)) > input.Channel.MaxFanout {
		return MessagePublishPlan{}, communicationError(ErrInvalidCommunicationModel,
			"publish audience rows or fanout do not match plan")
	}
	for index, audience := range input.Audiences {
		if audience.Ordinal != int64(index+1) || audience.Selector != input.Snapshot.Selectors[index] ||
			audience.TenantID != scope.TenantID || audience.WorkspaceID != scope.WorkspaceID ||
			audience.MessageID != input.Draft.ID || audience.CreatedAt != input.DBNow ||
			audience.ChannelACLRevision != input.Channel.ACLRevision ||
			audience.RouteRevision != input.Channel.RouteRevision ||
			audience.SubscriptionRevision != input.Channel.SubscriptionRevision ||
			audience.DirectoryEpoch != input.Snapshot.Epoch ||
			audience.DirectorySnapshotAt != input.Snapshot.ObservedAt {
			return MessagePublishPlan{}, communicationError(ErrInvalidCommunicationModel,
				"MessageAudience does not preserve locked publication revisions")
		}
	}
	for _, contribution := range input.Contributions {
		if contribution.CreatedAt != input.DBNow {
			return MessagePublishPlan{}, communicationError(ErrInvalidCommunicationModel,
				"audience contribution was not created at publish DB time")
		}
		if actorMatchesRecipient(input.Sender, contribution.Recipient) &&
			contribution.CausalKind != CausalDirect {
			return MessagePublishPlan{}, communicationError(ErrInvalidCommunicationModel,
				"derived audience contains authenticated sender")
		}
	}
	guard := input.DeliverySequenceGuard
	if err := ValidateCommunicationGuard(guard); err != nil {
		return MessagePublishPlan{}, err
	}
	if guard.TenantID != scope.TenantID || guard.WorkspaceID != scope.WorkspaceID ||
		guard.Kind != CommunicationGuardDeliverySequence {
		return MessagePublishPlan{}, communicationError(ErrInvalidCommunicationModel,
			"publish delivery guard crosses workspace")
	}
	deliveries := append([]MessageDelivery(nil), input.Deliveries...)
	sort.Slice(deliveries, func(i, j int) bool {
		if deliveries[i].Recipient.Kind != deliveries[j].Recipient.Kind {
			return deliveries[i].Recipient.Kind < deliveries[j].Recipient.Kind
		}
		return deliveries[i].Recipient.Ref < deliveries[j].Recipient.Ref
	})
	var guardAdvance *CommunicationGuardAdvancePlan
	if len(deliveries) > 0 {
		advance, err := PlanCommunicationGuardAdvance(guard, int64(len(deliveries)), input.DBNow)
		if err != nil {
			return MessagePublishPlan{}, err
		}
		for index := range deliveries {
			if deliveries[index].DeliverySeq != 0 &&
				deliveries[index].DeliverySeq != advance.AllocatedSeq[index] {
				return MessagePublishPlan{}, communicationError(ErrInvalidCommunicationModel,
					"caller-selected Delivery sequence does not match guard allocation")
			}
			deliveries[index].DeliverySeq = advance.AllocatedSeq[index]
		}
		guardAdvance = &advance
	}
	after := input.Draft
	after.Version++
	after.UpdatedAt = input.DBNow
	after.State = MessagePublished
	after.PublishedAt = &input.DBNow
	if after.WorkItemID == "" {
		after.LastEventSeq++
	}
	requiredCount, err := validatePublishAudienceDerivation(
		after, input.Snapshot, input.Audiences, input.Contributions, deliveries, input.DBNow,
	)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	if err := ValidateMessage(input.Draft, requiredCount); err != nil {
		return MessagePublishPlan{}, err
	}
	audienceHash, err := CanonicalMessageAudienceHash(after, input.Audiences, input.Contributions)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	after.AudienceHash = audienceHash
	if err := ValidateMessageLabelsForPublish(after, input.Channel, requiredCount, input.Labels, input.DBNow); err != nil {
		return MessagePublishPlan{}, err
	}
	if err := ValidateMessage(after, requiredCount); err != nil {
		return MessagePublishPlan{}, err
	}
	facts, err := canonicalAuthorizationFactUnion(append(
		append([]store.AuthorizationFactRef(nil), sendDecision.Facts...), directoryFact,
	))
	if err != nil {
		return MessagePublishPlan{}, err
	}
	claims := append([]CommunicationClaimRef(nil), sendDecision.RequiredClaims...)
	for _, contribution := range input.Contributions {
		if contribution.Recipient.Kind == RecipientSession {
			claims = append(claims, CommunicationClaimRef{
				SessionSID: contribution.ObservedSessionSID, Fence: contribution.ObservedClaimFence,
			})
		}
	}
	claims = canonicalCommunicationClaims(claims)
	return MessagePublishPlan{
		Before: input.Draft, After: after, Audiences: append([]MessageAudience(nil), input.Audiences...),
		Contributions: append([]MessageAudienceRecipient(nil), input.Contributions...),
		Deliveries:    deliveries, RequiredCount: requiredCount, GuardAdvance: guardAdvance,
		Facts: facts, RequiredClaims: claims,
		ChannelFence: MessagePublishChannelFence{
			ChannelID: input.Channel.ID, DirectoryEpoch: input.Snapshot.Epoch,
			ACLRevision: input.Channel.ACLRevision, RouteRevision: input.Channel.RouteRevision,
			SubscriptionRevision: input.Channel.SubscriptionRevision, EvaluatedAt: input.DBNow,
		},
	}, nil
}

func equalCommunicationSubjectPtr(left, right *CommunicationSubjectRef) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func equalAuthorizationFactPtr(
	fact *store.AuthorizationFactRef,
	kind model.Kind,
	id model.ID,
	version int64,
) bool {
	if fact == nil {
		return kind == "" && id == "" && version == 0
	}
	return fact.Kind == kind && fact.ID == id && fact.Version == version
}

func equalRouteReasons(left, right []RouteReason) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func equalRecipientRefs(left, right []RecipientRef) bool {
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

// FoldAudienceContributions implements the immutable audience fold: a single
// Delivery per canonical recipient while preserving every selector cause.
func FoldAudienceContributions(contributions []MessageAudienceRecipient) (AudienceFold, error) {
	if len(contributions) == 0 {
		return AudienceFold{}, communicationError(ErrInvalidCommunicationModel, "empty audience fold")
	}
	ordered := append([]MessageAudienceRecipient(nil), contributions...)
	semanticRows := make(map[publishedAudienceArcKey]struct{}, len(ordered))
	for i := range ordered {
		if err := validateAudienceContribution(ordered[i]); err != nil {
			return AudienceFold{}, err
		}
		ordered[i].RouteReasons = normalizeRouteReasons(ordered[i].RouteReasons)
		if ordered[i].OriginalSubscriber != nil {
			subscriber := *ordered[i].OriginalSubscriber
			ordered[i].OriginalSubscriber = &subscriber
		}
		semanticKey := publishedAudienceArcKey{
			MessageAudienceID: ordered[i].MessageAudienceID,
			Arc:               publishedAudienceCausalArcIdentity(ordered[i]),
		}
		if _, duplicate := semanticRows[semanticKey]; duplicate {
			return AudienceFold{}, communicationError(ErrInvalidCommunicationModel,
				"duplicate selector-recipient causal arc")
		}
		semanticRows[semanticKey] = struct{}{}
	}
	sort.Slice(ordered, func(i, j int) bool {
		return audienceContributionKey(ordered[i]) < audienceContributionKey(ordered[j])
	})
	result := AudienceFold{
		Recipient: ordered[0].Recipient, DeliveryID: ordered[0].MessageDeliveryID,
		WakePolicy: WakeNone, Contributions: ordered,
	}
	reasons := make(map[RouteReason]struct{})
	hashInput := make([]audienceContributionHashInput, 0, len(ordered))
	for _, contribution := range ordered {
		if contribution.Recipient != result.Recipient || contribution.MessageDeliveryID != result.DeliveryID ||
			contribution.TenantID != ordered[0].TenantID || contribution.WorkspaceID != ordered[0].WorkspaceID ||
			contribution.RecipientEpoch != ordered[0].RecipientEpoch ||
			contribution.DirectoryEpoch != ordered[0].DirectoryEpoch ||
			contribution.ChannelACLRevision != ordered[0].ChannelACLRevision ||
			contribution.RouteRevision != ordered[0].RouteRevision ||
			contribution.SubscriptionRevision != ordered[0].SubscriptionRevision {
			return AudienceFold{}, communicationError(ErrInvalidCommunicationModel,
				"audience fold crosses lineage, recipient epoch or publication revisions")
		}
		result.Required = result.Required || contribution.Required
		if wakeRank(contribution.WakePolicy) > wakeRank(result.WakePolicy) {
			result.WakePolicy = contribution.WakePolicy
		}
		for _, reason := range contribution.RouteReasons {
			reasons[reason] = struct{}{}
		}
		hashInput = append(hashInput, audienceContributionHashInput{
			AudienceID: contribution.MessageAudienceID, Recipient: contribution.Recipient,
			RecipientEpoch: contribution.RecipientEpoch, Required: contribution.Required,
			WakePolicy: contribution.WakePolicy, RouteReasons: contribution.RouteReasons, Selector: contribution.Selector,
			DirectoryEpoch: contribution.DirectoryEpoch, ChannelACLRevision: contribution.ChannelACLRevision,
			RouteRevision: contribution.RouteRevision, SubscriptionRevision: contribution.SubscriptionRevision,
			CausalKind: contribution.CausalKind, CausalRef: contribution.CausalRef,
			CausalFactKind: contribution.CausalFactKind, CausalFactID: contribution.CausalFactID,
			CausalFactVersion:  contribution.CausalFactVersion,
			ObservedSessionSID: contribution.ObservedSessionSID,
			ObservedClaimFence: contribution.ObservedClaimFence,
			OriginalSubscriber: contribution.OriginalSubscriber, SubscriptionID: contribution.SubscriptionID,
			SubscriptionGeneration: contribution.SubscriptionGeneration, RouteRuleID: contribution.RouteRuleID,
			RouteRuleGeneration: contribution.RouteRuleGeneration,
			CausalArcHash:       contribution.CausalArcHash,
		})
	}
	result.RouteReasons = make([]RouteReason, 0, len(reasons))
	for reason := range reasons {
		result.RouteReasons = append(result.RouteReasons, reason)
	}
	sort.Slice(result.RouteReasons, func(i, j int) bool { return result.RouteReasons[i] < result.RouteReasons[j] })
	reasonBytes, err := canonicalJSON(result.RouteReasons)
	if err != nil {
		return AudienceFold{}, err
	}
	reasonHash := sha256.Sum256(reasonBytes)
	result.RouteReasonsHash = reasonHash[:]
	contributionBytes, err := canonicalJSON(hashInput)
	if err != nil {
		return AudienceFold{}, err
	}
	contributionHash := sha256.Sum256(contributionBytes)
	result.ContributionsHash = contributionHash[:]
	return result, nil
}

type AuthorityEvidence struct {
	Verdict     AssessmentVerdict `json:"verdict"`
	Code        string            `json:"code"`
	EvidenceRef string            `json:"evidence_ref,omitempty"`
}

type NamedAuthorityEvidence struct {
	Name     string            `json:"name"`
	Evidence AuthorityEvidence `json:"evidence"`
}

type AuthorityDecision struct {
	Verdict AssessmentVerdict        `json:"verdict"`
	Code    string                   `json:"code"`
	Checks  []NamedAuthorityEvidence `json:"checks"`
}

func ValidateAuthorityEvidence(evidence AuthorityEvidence) error {
	if !validAssessmentVerdict(evidence.Verdict) || !boundedToken(evidence.Code, 128) ||
		!validateOpaqueRef(evidence.EvidenceRef) {
		return communicationError(ErrInvalidCommunicationModel, "invalid authority evidence")
	}
	return nil
}

func evidenceVerdict(evidence AuthorityEvidence) AssessmentVerdict {
	if !validAssessmentVerdict(evidence.Verdict) {
		return VerdictUnknown
	}
	return evidence.Verdict
}

func andVerdicts(verdicts ...AssessmentVerdict) AssessmentVerdict {
	result := VerdictClean
	for _, verdict := range verdicts {
		if verdict == VerdictBroken {
			return VerdictBroken
		}
		if verdict != VerdictClean {
			result = VerdictUnknown
		}
	}
	return result
}

func orVerdicts(verdicts ...AssessmentVerdict) AssessmentVerdict {
	result := VerdictBroken
	for _, verdict := range verdicts {
		if verdict == VerdictClean {
			return VerdictClean
		}
		if verdict != VerdictBroken {
			result = VerdictUnknown
		}
	}
	return result
}

func evaluateAuthorityChecks(checks []NamedAuthorityEvidence) AuthorityDecision {
	verdicts := make([]AssessmentVerdict, 0, len(checks))
	for _, check := range checks {
		verdicts = append(verdicts, evidenceVerdict(check.Evidence))
	}
	verdict := andVerdicts(verdicts...)
	code := "authorized"
	if verdict == VerdictBroken {
		code = "forbidden"
		for _, check := range checks {
			if evidenceVerdict(check.Evidence) == VerdictBroken {
				code = check.Evidence.Code
				if code == "" {
					code = "forbidden_" + check.Name
				}
				break
			}
		}
	} else if verdict == VerdictUnknown {
		code = "authority_unavailable"
		for _, check := range checks {
			if evidenceVerdict(check.Evidence) == VerdictUnknown {
				code = check.Evidence.Code
				if code == "" {
					code = "unknown_" + check.Name
				}
				break
			}
		}
	}
	return AuthorityDecision{Verdict: verdict, Code: code, Checks: checks}
}

type BoundChannelReadEvidence struct {
	TenantID           model.TenantID         `json:"tenant_id"`
	WorkspaceID        model.ID               `json:"workspace_id"`
	ChannelID          model.ID               `json:"channel_id"`
	Principal          CommunicationPrincipal `json:"-"`
	Bit                ChannelGrantBit        `json:"bit"`
	GrantID            model.ID               `json:"grant_id,omitempty"`
	GrantVersion       int64                  `json:"grant_version,omitempty"`
	DirectoryEpoch     int64                  `json:"directory_epoch"`
	ChannelACLRevision int64                  `json:"channel_acl_revision"`
	EvaluatedAt        time.Time              `json:"evaluated_at"`
	Evidence           AuthorityEvidence      `json:"evidence"`
}

type BoundEntityRecipientEvidence struct {
	Scope          DirectoryScopeRef      `json:"scope"`
	Carrier        ProtectedCarrierRef    `json:"carrier"`
	Principal      CommunicationPrincipal `json:"-"`
	Recipient      RecipientRef           `json:"recipient"`
	DirectoryEpoch int64                  `json:"directory_epoch"`
	EvaluatedAt    time.Time              `json:"evaluated_at"`
	Evidence       AuthorityEvidence      `json:"evidence"`
}

type ProtectedCarrierRef struct {
	Entity     EntityRef `json:"entity"`
	ChannelID  model.ID  `json:"channel_id"`
	MessageID  model.ID  `json:"message_id"`
	DeliveryID model.ID  `json:"delivery_id"`
}

func validateProtectedCarrierRef(carrier ProtectedCarrierRef) error {
	if !validCanonicalCommunicationTenant(carrier.Entity.TenantID) ||
		!validCanonicalCommunicationID(carrier.Entity.WorkspaceID) ||
		!validCanonicalCommunicationID(carrier.Entity.ID) ||
		!validCanonicalCommunicationID(carrier.ChannelID) ||
		!validCanonicalCommunicationID(carrier.MessageID) ||
		!validCanonicalCommunicationID(carrier.DeliveryID) {
		return communicationError(ErrInvalidCommunicationModel, "invalid protected carrier binding")
	}
	switch carrier.Entity.Kind {
	case model.Kind("sessions.message"):
		if carrier.Entity.ID != carrier.MessageID {
			return communicationError(ErrInvalidCommunicationModel, "Message carrier crosses Message")
		}
	case model.Kind("sessions.message_delivery"):
		if carrier.Entity.ID != carrier.DeliveryID {
			return communicationError(ErrInvalidCommunicationModel, "Delivery carrier crosses Delivery")
		}
	case model.Kind("sessions.decision_request"), model.Kind("sessions.handoff"):
		// Message and Delivery IDs come from the same locked carrier row.
	default:
		return communicationError(ErrInvalidCommunicationModel, "unsupported protected carrier kind")
	}
	return nil
}

// ProtectedCarrierSnapshot binds a gate to the rows actually locked by the
// service. The complete-delivery witness supplies required-count validation;
// it is not a caller-provided count.
type ProtectedCarrierSnapshot struct {
	Message               Message           `json:"message"`
	Delivery              MessageDelivery   `json:"delivery"`
	DecisionRequest       *DecisionRequest  `json:"decision_request,omitempty"`
	Handoff               *Handoff          `json:"handoff,omitempty"`
	RequiredDeliveryCount int64             `json:"required_delivery_count"`
	ObservedAt            time.Time         `json:"observed_at"`
	Evidence              AuthorityEvidence `json:"evidence"`
}

func validateProtectedCarrierSnapshot(
	snapshot ProtectedCarrierSnapshot,
	carrier ProtectedCarrierRef,
	scope DirectoryScopeRef,
	channelID model.ID,
	recipient RecipientRef,
	dbNow time.Time,
) error {
	if snapshot.ObservedAt != dbNow || ValidateAuthorityEvidence(snapshot.Evidence) != nil ||
		evidenceVerdict(snapshot.Evidence) != VerdictClean || snapshot.RequiredDeliveryCount < 0 {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"protected carrier rows are not a complete same-transaction snapshot")
	}
	message := snapshot.Message
	delivery := snapshot.Delivery
	if err := ValidateMessage(message, snapshot.RequiredDeliveryCount); err != nil {
		return err
	}
	if err := ValidateMessageDelivery(delivery); err != nil {
		return err
	}
	if err := ValidateMessageDeliveryLineage(message, delivery); err != nil {
		return err
	}
	if message.TenantID != scope.TenantID || message.WorkspaceID != scope.WorkspaceID ||
		message.ChannelID != channelID || message.ID != carrier.MessageID ||
		delivery.ID != carrier.DeliveryID || delivery.Recipient != recipient {
		return communicationError(ErrInvalidCommunicationModel,
			"protected carrier snapshot crosses scope, Channel, Message or recipient")
	}
	switch carrier.Entity.Kind {
	case model.Kind("sessions.message"):
		if carrier.Entity.ID != message.ID || snapshot.DecisionRequest != nil || snapshot.Handoff != nil {
			return communicationError(ErrInvalidCommunicationModel, "invalid Message carrier rows")
		}
	case model.Kind("sessions.message_delivery"):
		if carrier.Entity.ID != delivery.ID || snapshot.DecisionRequest != nil || snapshot.Handoff != nil {
			return communicationError(ErrInvalidCommunicationModel, "invalid Delivery carrier rows")
		}
	case model.Kind("sessions.decision_request"):
		if snapshot.DecisionRequest == nil || snapshot.Handoff != nil ||
			carrier.Entity.ID != snapshot.DecisionRequest.ID {
			return communicationError(ErrInvalidCommunicationModel, "Decision carrier row is absent")
		}
		if err := ValidateDecisionRequest(*snapshot.DecisionRequest); err != nil {
			return err
		}
		if err := ValidateDecisionRequestLineage(message, *snapshot.DecisionRequest); err != nil {
			return err
		}
	case model.Kind("sessions.handoff"):
		if snapshot.Handoff == nil || snapshot.DecisionRequest != nil ||
			carrier.Entity.ID != snapshot.Handoff.ID {
			return communicationError(ErrInvalidCommunicationModel, "Handoff carrier row is absent")
		}
		if err := ValidateHandoff(*snapshot.Handoff); err != nil {
			return err
		}
		if err := ValidateHandoffLineage(message, delivery, *snapshot.Handoff,
			snapshot.RequiredDeliveryCount); err != nil {
			return err
		}
	default:
		return communicationError(ErrInvalidCommunicationModel, "unsupported protected carrier kind")
	}
	return nil
}

type ReadGateEvidence struct {
	Scope                DirectoryScopeRef            `json:"scope"`
	ChannelID            model.ID                     `json:"channel_id"`
	ChannelACLRevision   int64                        `json:"channel_acl_revision"`
	DBNow                time.Time                    `json:"db_now"`
	Operation            CommunicationOperation       `json:"operation"`
	Carrier              ProtectedCarrierRef          `json:"carrier"`
	CarrierState         ProtectedCarrierSnapshot     `json:"carrier_state"`
	Core                 ReadWitness                  `json:"core"`
	Principal            CommunicationPrincipal       `json:"-"`
	PrincipalResolution  PrincipalResolution          `json:"principal_resolution"`
	Recipient            RecipientRef                 `json:"recipient"`
	DirectoryEpoch       store.AuthorizationFactRef   `json:"directory_epoch"`
	CurrentChannelGrant  BoundChannelReadEvidence     `json:"current_channel_grant"`
	EntityRecipientGuard BoundEntityRecipientEvidence `json:"entity_recipient_guard"`
	CurrentAudience      CurrentAudienceEvidence      `json:"current_audience"`
}

type ProtectedReadEvidence = ReadGateEvidence

type ProtectedReadDecision struct {
	AuthorityDecision
	Entity                   EntityRef                    `json:"entity"`
	Operation                CommunicationOperation       `json:"operation"`
	Principal                CommunicationPrincipal       `json:"-"`
	PrincipalRecipient       RecipientRef                 `json:"principal_recipient"`
	Recipient                RecipientRef                 `json:"recipient"`
	Facts                    []store.AuthorizationFactRef `json:"facts"`
	SurvivingContributionIDs []model.ID                   `json:"surviving_contribution_ids,omitempty"`
	RequiredClaims           []CommunicationClaimRef      `json:"required_claims,omitempty"`
}

func combineAuthorityEvidence(name string, evidence ...AuthorityEvidence) AuthorityEvidence {
	verdicts := make([]AssessmentVerdict, 0, len(evidence))
	for _, item := range evidence {
		verdicts = append(verdicts, evidenceVerdict(item))
	}
	verdict := andVerdicts(verdicts...)
	code := name + "_current"
	for _, item := range evidence {
		if evidenceVerdict(item) == verdict && item.Code != "" {
			code = item.Code
			break
		}
	}
	return AuthorityEvidence{Verdict: verdict, Code: code, EvidenceRef: "derived:" + name}
}

func communicationOperationGrantBit(operation CommunicationOperation) (ChannelGrantBit, bool) {
	switch operation {
	case CommunicationRead, CommunicationDeliveryWrite, CommunicationDeliveryAdmin,
		CommunicationDecisionRequestWrite, CommunicationHandoffResponse:
		return ChannelGrantRead, true
	default:
		return "", false
	}
}

func communicationCarrierOperationMatches(kind model.Kind, operation CommunicationOperation) bool {
	if operation == CommunicationRead {
		return oneOf(kind, model.Kind("sessions.message"), model.Kind("sessions.message_delivery"),
			model.Kind("sessions.decision_request"), model.Kind("sessions.handoff"))
	}
	switch kind {
	case model.Kind("sessions.message_delivery"):
		return oneOf(operation, CommunicationDeliveryWrite, CommunicationDeliveryAdmin)
	case model.Kind("sessions.decision_request"):
		return operation == CommunicationDecisionRequestWrite
	case model.Kind("sessions.handoff"):
		return operation == CommunicationHandoffResponse
	default:
		return false
	}
}

func communicationRestrictedPurposeAllowsCarrier(kind model.Kind, operation CommunicationOperation) bool {
	switch operation {
	case CommunicationRead, CommunicationDeliveryWrite:
		return kind == model.Kind("sessions.message_delivery")
	case CommunicationHandoffResponse:
		return kind == model.Kind("sessions.handoff")
	default:
		return false
	}
}

func communicationEvidenceCurrent(observedAt, freshUntil, dbNow time.Time) bool {
	return !observedAt.IsZero() && freshUntil.After(observedAt) &&
		!observedAt.After(dbNow) && !dbNow.After(freshUntil)
}

// EvaluateCarrierGate composes the same four current-authority doors for reads
// and existing-carrier mutations. The operation is server-selected and stays
// bound to the core witness; a read witness cannot be relabelled as Ack/admin.
func EvaluateCarrierGate(input ReadGateEvidence) (ProtectedReadDecision, error) {
	if err := ValidateReadWitness(input.Core); err != nil {
		return ProtectedReadDecision{}, err
	}
	if err := ValidateCommunicationPrincipalForScope(input.Principal, input.Scope); err != nil {
		return ProtectedReadDecision{}, err
	}
	if err := ValidatePrincipalResolution(input.PrincipalResolution); err != nil {
		return ProtectedReadDecision{}, err
	}
	if err := validateProtectedCarrierRef(input.Carrier); err != nil {
		return ProtectedReadDecision{}, err
	}
	if !communicationCarrierOperationMatches(input.Carrier.Entity.Kind, input.Operation) {
		return ProtectedReadDecision{}, communicationError(ErrInvalidCommunicationModel,
			"protected carrier operation does not match entity kind")
	}
	if input.PrincipalResolution.Scope != input.Scope ||
		input.PrincipalResolution.Principal != input.Principal ||
		!communicationEvidenceCurrent(input.PrincipalResolution.ObservedAt,
			input.PrincipalResolution.FreshUntil, input.DBNow) {
		return ProtectedReadDecision{}, communicationError(ErrCommunicationEvidenceUnknown,
			"principal resolution is stale or crosses authority scope")
	}
	switch input.PrincipalResolution.Outcome {
	case PrincipalUnknown:
		return ProtectedReadDecision{}, communicationError(ErrCommunicationEvidenceUnknown,
			"principal resolution is unavailable")
	case PrincipalNotFound:
		return ProtectedReadDecision{}, communicationError(ErrCommunicationNotFound,
			"principal is not mapped to a current communication recipient")
	}
	if err := validateProtectedCarrierSnapshot(input.CarrierState, input.Carrier, input.Scope,
		input.ChannelID, input.Recipient, input.DBNow); err != nil {
		return ProtectedReadDecision{}, err
	}
	wantBit, operationOK := communicationOperationGrantBit(input.Operation)
	grantVerdict := evidenceVerdict(input.CurrentChannelGrant.Evidence)
	grantPairValid := validCanonicalCommunicationID(input.CurrentChannelGrant.GrantID) &&
		input.CurrentChannelGrant.GrantVersion > 0
	grantPairEmpty := input.CurrentChannelGrant.GrantID == "" &&
		input.CurrentChannelGrant.GrantVersion == 0
	grantBindingValid := grantPairValid || (grantVerdict != VerdictClean && grantPairEmpty)
	if input.Scope.Validate() != nil || !validCanonicalCommunicationID(input.ChannelID) ||
		input.ChannelACLRevision < 1 || input.DBNow.IsZero() || !operationOK ||
		input.Core.Operation != input.Operation || input.Core.Principal != input.Principal ||
		!communicationEvidenceCurrent(input.Core.ObservedAt, input.Core.FreshUntil, input.DBNow) ||
		input.Core.Entity.TenantID != input.Scope.TenantID ||
		input.Core.Entity.WorkspaceID != input.Scope.WorkspaceID ||
		input.Core.Entity != input.Carrier.Entity || input.Carrier.Entity.TenantID != input.Scope.TenantID ||
		input.Carrier.Entity.WorkspaceID != input.Scope.WorkspaceID ||
		input.Carrier.ChannelID != input.ChannelID || input.Recipient.Validate() != nil ||
		input.CurrentChannelGrant.Principal != input.Principal ||
		input.CurrentChannelGrant.TenantID != input.Scope.TenantID ||
		input.CurrentChannelGrant.WorkspaceID != input.Scope.WorkspaceID ||
		input.CurrentChannelGrant.ChannelID != input.ChannelID ||
		input.CurrentChannelGrant.Bit != wantBit ||
		input.CurrentChannelGrant.DirectoryEpoch != input.DirectoryEpoch.Version ||
		input.CurrentChannelGrant.ChannelACLRevision != input.ChannelACLRevision ||
		input.CurrentChannelGrant.EvaluatedAt != input.DBNow ||
		ValidateAuthorityEvidence(input.CurrentChannelGrant.Evidence) != nil || !grantBindingValid ||
		input.EntityRecipientGuard.Scope != input.Scope ||
		input.EntityRecipientGuard.Carrier != input.Carrier ||
		input.EntityRecipientGuard.Principal != input.Principal ||
		input.EntityRecipientGuard.Recipient != input.Recipient ||
		input.EntityRecipientGuard.DirectoryEpoch != input.DirectoryEpoch.Version ||
		input.EntityRecipientGuard.EvaluatedAt != input.DBNow ||
		ValidateAuthorityEvidence(input.EntityRecipientGuard.Evidence) != nil ||
		input.PrincipalResolution.Outcome != PrincipalResolved ||
		input.PrincipalResolution.Recipient == nil ||
		input.PrincipalResolution.Recipient.DirectoryEpoch != input.DirectoryEpoch.Version ||
		input.CurrentAudience.TenantID != input.Core.Entity.TenantID ||
		input.CurrentAudience.WorkspaceID != input.Core.Entity.WorkspaceID ||
		input.CurrentAudience.Recipient != input.Recipient ||
		input.CurrentAudience.DeliveryID != input.Carrier.DeliveryID ||
		input.CurrentAudience.MessageID != input.Carrier.MessageID ||
		input.CurrentAudience.DirectoryEpoch != input.DirectoryEpoch.Version ||
		!communicationEvidenceCurrent(input.CurrentAudience.ObservedAt,
			input.CurrentAudience.FreshUntil, input.DBNow) {
		return ProtectedReadDecision{}, communicationError(ErrInvalidCommunicationModel,
			"protected-read evidence is not bound to principal, entity and recipient")
	}
	principalRecipient := input.PrincipalResolution.Recipient.Recipient
	if input.Principal.PurposeRestricted &&
		!communicationRestrictedPurposeAllowsCarrier(input.Carrier.Entity.Kind, input.Operation) {
		return ProtectedReadDecision{}, communicationError(ErrCommunicationForbidden,
			"communication-session purpose ceiling forbids carrier operation")
	}
	if input.Operation != CommunicationDeliveryAdmin && principalRecipient != input.Recipient {
		return ProtectedReadDecision{}, communicationError(ErrCommunicationForbidden,
			"principal does not resolve to carrier recipient")
	}
	if input.DirectoryEpoch.Kind != model.DirectoryEpochKind ||
		input.DirectoryEpoch.ID != model.ID(input.Core.Entity.TenantID) || input.DirectoryEpoch.Version < 1 {
		return ProtectedReadDecision{}, communicationError(ErrInvalidCommunicationModel,
			"protected-read evidence lacks exact directory epoch")
	}
	if input.CurrentAudience.SetWitness.ObservedAt != input.DBNow ||
		input.CurrentAudience.SetWitness.MessageVersion != input.CarrierState.Message.Version ||
		!bytes.Equal(input.CurrentAudience.SetWitness.MessageAudienceHash,
			input.CarrierState.Message.AudienceHash) {
		return ProtectedReadDecision{}, communicationError(ErrCommunicationEvidenceUnknown,
			"current audience set does not match the locked Message seal")
	}
	audienceDecision := EvaluateCurrentAudienceDetailed(input.CurrentAudience)
	facts := append(append([]store.AuthorizationFactRef(nil), input.Core.Facts...), input.DirectoryEpoch)
	canonicalFacts, err := canonicalAuthorizationFactUnion(facts)
	if err != nil {
		return ProtectedReadDecision{}, communicationError(ErrCommunicationEvidenceUnknown,
			"protected carrier fact set cannot be represented: %v", err)
	}
	decision := evaluateAuthorityChecks([]NamedAuthorityEvidence{
		{Name: "core_permission", Evidence: input.Core.CorePermission},
		{Name: "channel_read_grant", Evidence: input.CurrentChannelGrant.Evidence},
		{Name: "entity_recipient_guard", Evidence: combineAuthorityEvidence(
			"entity_recipient_guard", input.Core.ResourceGuard, input.EntityRecipientGuard.Evidence,
		)},
		{Name: "current_audience_cause", Evidence: audienceDecision.AuthorityEvidence},
		{Name: "no_forbid", Evidence: input.Core.ForbidAbsence},
	})
	claims := append([]CommunicationClaimRef(nil), audienceDecision.RequiredClaims...)
	if input.Principal.SessionID != "" {
		claims = append(claims, CommunicationClaimRef{
			SessionSID: input.Principal.SessionID, Fence: input.Principal.SessionFence,
		})
	}
	claims = canonicalCommunicationClaims(claims)
	if decision.Verdict != VerdictClean {
		audienceDecision.SurvivingContributionIDs = nil
		canonicalFacts = nil
		claims = nil
	}
	result := ProtectedReadDecision{
		AuthorityDecision: decision, Entity: input.Core.Entity, Operation: input.Operation,
		Principal: input.Principal, PrincipalRecipient: principalRecipient,
		Recipient: input.Recipient, Facts: canonicalFacts,
		SurvivingContributionIDs: append([]model.ID(nil), audienceDecision.SurvivingContributionIDs...),
		RequiredClaims:           claims,
	}
	if err := ValidateProtectedReadDecision(result); err != nil {
		return ProtectedReadDecision{}, err
	}
	return result, nil
}

// EvaluateReadGate is the read-only C5 entry point. Existing-carrier writes use
// EvaluateCarrierGate with their exact closed operation.
func EvaluateReadGate(input ReadGateEvidence) (ProtectedReadDecision, error) {
	if input.Operation != CommunicationRead {
		return ProtectedReadDecision{}, communicationError(ErrInvalidCommunicationModel,
			"read gate requires read operation")
	}
	return EvaluateCarrierGate(input)
}

func EvaluateProtectedRead(input ProtectedReadEvidence) (ProtectedReadDecision, error) {
	return EvaluateReadGate(input)
}

// CanonicalAuthorizationFacts validates, deduplicates and deterministically
// orders the exact version witnesses handed to LockAuthoritySnapshot.
func CanonicalAuthorizationFacts(facts []store.AuthorizationFactRef) ([]store.AuthorizationFactRef, error) {
	if len(facts) > 64 {
		return nil, communicationError(ErrInvalidCommunicationModel, "authorization fact set exceeds 64")
	}
	canonical := append([]store.AuthorizationFactRef(nil), facts...)
	for _, fact := range canonical {
		if !oneOf(fact.Kind, model.Kind("core.identity"), model.Kind("core.agent"),
			model.Kind("core.membership"), model.Kind("core.user_group_member"),
			model.Kind("core.agent_group_member"), model.DirectoryEpochKind,
			model.AuthorizationEpochKind, model.Kind("governance.nhi_lifecycle")) ||
			!validCanonicalCommunicationID(fact.ID) || fact.Version < 1 {
			return nil, communicationError(ErrInvalidCommunicationModel, "invalid authorization fact")
		}
	}
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].Kind != canonical[j].Kind {
			return canonical[i].Kind < canonical[j].Kind
		}
		return canonical[i].ID.String() < canonical[j].ID.String()
	})
	for i := 1; i < len(canonical); i++ {
		if canonical[i].Kind == canonical[i-1].Kind && canonical[i].ID == canonical[i-1].ID {
			return nil, communicationError(ErrInvalidCommunicationModel, "duplicate authorization fact")
		}
	}
	return canonical, nil
}

func canonicalAuthorizationFactUnion(
	facts []store.AuthorizationFactRef,
) ([]store.AuthorizationFactRef, error) {
	type key struct {
		kind model.Kind
		id   model.ID
	}
	unique := make(map[key]store.AuthorizationFactRef, len(facts))
	for _, fact := range facts {
		factKey := key{kind: fact.Kind, id: fact.ID}
		if prior, present := unique[factKey]; present && prior.Version != fact.Version {
			return nil, communicationError(ErrCommunicationEvidenceUnknown,
				"authorization fact versions disagree")
		}
		unique[factKey] = fact
	}
	if len(unique) > 64 {
		return nil, communicationError(ErrInvalidCommunicationModel, "authorization fact set exceeds 64")
	}
	deduplicated := make([]store.AuthorizationFactRef, 0, len(unique))
	for _, fact := range unique {
		deduplicated = append(deduplicated, fact)
	}
	return CanonicalAuthorizationFacts(deduplicated)
}

func ValidateReadWitness(witness ReadWitness) error {
	if !witness.Outcome.Valid() || !boundedToken(witness.Code, 128) || witness.ObservedAt.IsZero() ||
		!witness.FreshUntil.After(witness.ObservedAt) ||
		!witness.Operation.Valid() ||
		!validCanonicalCommunicationTenant(witness.Entity.TenantID) ||
		!validateOpaqueRef(string(witness.Entity.Kind)) ||
		!validCanonicalCommunicationID(witness.Entity.ID) ||
		!validCanonicalCommunicationID(witness.Entity.WorkspaceID) {
		return communicationError(ErrInvalidCommunicationModel, "invalid read witness envelope")
	}
	if err := ValidateCommunicationPrincipalForScope(witness.Principal, DirectoryScopeRef{
		TenantID: witness.Entity.TenantID, WorkspaceID: witness.Entity.WorkspaceID,
	}); err != nil {
		return err
	}
	for _, evidence := range []AuthorityEvidence{
		witness.CorePermission, witness.ResourceGuard, witness.ForbidAbsence,
	} {
		if err := ValidateAuthorityEvidence(evidence); err != nil {
			return err
		}
	}
	verdict := andVerdicts(
		evidenceVerdict(witness.CorePermission),
		evidenceVerdict(witness.ResourceGuard),
		evidenceVerdict(witness.ForbidAbsence),
	)
	want := ReadUnknown
	switch verdict {
	case VerdictClean:
		want = ReadAllow
	case VerdictBroken:
		want = ReadDeny
	}
	if witness.Outcome != want {
		return communicationError(ErrInvalidCommunicationModel,
			"read outcome %s does not match gate verdict %s", witness.Outcome, verdict)
	}
	_, err := CanonicalAuthorizationFacts(witness.Facts)
	if err != nil {
		return err
	}
	return nil
}

type SendGateEvidence struct {
	Scope                    DirectoryScopeRef          `json:"scope"`
	ChannelID                model.ID                   `json:"channel_id"`
	ChannelACLRevision       int64                      `json:"channel_acl_revision"`
	DBNow                    time.Time                  `json:"db_now"`
	Principal                CommunicationPrincipal     `json:"-"`
	Core                     ReadWitness                `json:"core"`
	DirectoryEpoch           store.AuthorizationFactRef `json:"directory_epoch"`
	CurrentChannelWriteGrant BoundChannelReadEvidence   `json:"current_channel_write_grant"`
}

type SendGateDecision struct {
	AuthorityDecision
	Scope          DirectoryScopeRef            `json:"scope"`
	ChannelID      model.ID                     `json:"channel_id"`
	Principal      CommunicationPrincipal       `json:"-"`
	Facts          []store.AuthorizationFactRef `json:"facts"`
	RequiredClaims []CommunicationClaimRef      `json:"required_claims,omitempty"`
}

// EvaluateSendGate is intentionally independent from C5: a write-only caller
// may send a new carrier but cannot open one, while read-only cannot send.
func EvaluateSendGate(input SendGateEvidence) (SendGateDecision, error) {
	if input.Scope.Validate() != nil || !validCanonicalCommunicationID(input.ChannelID) ||
		input.ChannelACLRevision < 1 || input.DBNow.IsZero() ||
		ValidateCommunicationPrincipalForScope(input.Principal, input.Scope) != nil {
		return SendGateDecision{}, communicationError(ErrInvalidCommunicationModel,
			"invalid send-gate target")
	}
	if err := ValidateReadWitness(input.Core); err != nil {
		return SendGateDecision{}, err
	}
	grantVerdict := evidenceVerdict(input.CurrentChannelWriteGrant.Evidence)
	grantPairValid := validCanonicalCommunicationID(input.CurrentChannelWriteGrant.GrantID) &&
		input.CurrentChannelWriteGrant.GrantVersion > 0
	grantPairEmpty := input.CurrentChannelWriteGrant.GrantID == "" &&
		input.CurrentChannelWriteGrant.GrantVersion == 0
	grantBindingValid := grantPairValid || (grantVerdict != VerdictClean && grantPairEmpty)
	if input.Core.Operation != CommunicationMessageSend || input.Core.Principal != input.Principal ||
		!communicationEvidenceCurrent(input.Core.ObservedAt, input.Core.FreshUntil, input.DBNow) ||
		input.Core.Entity != (EntityRef{TenantID: input.Scope.TenantID,
			Kind: model.Kind("sessions.channel"), ID: input.ChannelID, WorkspaceID: input.Scope.WorkspaceID}) ||
		input.CurrentChannelWriteGrant.TenantID != input.Scope.TenantID ||
		input.CurrentChannelWriteGrant.WorkspaceID != input.Scope.WorkspaceID ||
		input.CurrentChannelWriteGrant.ChannelID != input.ChannelID ||
		input.CurrentChannelWriteGrant.Principal != input.Principal ||
		input.CurrentChannelWriteGrant.Bit != ChannelGrantWrite ||
		!grantBindingValid ||
		input.CurrentChannelWriteGrant.DirectoryEpoch != input.DirectoryEpoch.Version ||
		input.CurrentChannelWriteGrant.ChannelACLRevision != input.ChannelACLRevision ||
		input.CurrentChannelWriteGrant.EvaluatedAt != input.DBNow ||
		ValidateAuthorityEvidence(input.CurrentChannelWriteGrant.Evidence) != nil ||
		input.DirectoryEpoch.Kind != model.DirectoryEpochKind ||
		input.DirectoryEpoch.ID != model.ID(input.Scope.TenantID) || input.DirectoryEpoch.Version < 1 {
		return SendGateDecision{}, communicationError(ErrInvalidCommunicationModel,
			"send evidence does not prove message-send and ChannelGrant.write")
	}
	facts, err := canonicalAuthorizationFactUnion(append(
		append([]store.AuthorizationFactRef(nil), input.Core.Facts...), input.DirectoryEpoch,
	))
	if err != nil {
		return SendGateDecision{}, err
	}
	decision := evaluateAuthorityChecks([]NamedAuthorityEvidence{
		{Name: "core_message_send_permission", Evidence: input.Core.CorePermission},
		{Name: "channel_write_grant", Evidence: input.CurrentChannelWriteGrant.Evidence},
		{Name: "resource_guards", Evidence: input.Core.ResourceGuard},
		{Name: "no_forbid", Evidence: input.Core.ForbidAbsence},
	})
	claims := []CommunicationClaimRef(nil)
	if decision.Verdict == VerdictClean && input.Principal.SessionID != "" {
		claims = []CommunicationClaimRef{{
			SessionSID: input.Principal.SessionID, Fence: input.Principal.SessionFence,
		}}
	}
	return SendGateDecision{
		AuthorityDecision: decision, Scope: input.Scope, ChannelID: input.ChannelID,
		Principal: input.Principal, Facts: facts, RequiredClaims: claims,
	}, nil
}

type ChannelGrantSnapshot struct {
	Verdict     AssessmentVerdict `json:"verdict"`
	Code        string            `json:"code"`
	ACLRevision int64             `json:"acl_revision"`
	ObservedAt  time.Time         `json:"observed_at"`
	Grants      []ChannelGrant    `json:"grants"`
}

type ChannelGrantSubjectClosure struct {
	Scope          DirectoryScopeRef         `json:"scope"`
	Principal      CommunicationPrincipal    `json:"-"`
	DirectoryEpoch int64                     `json:"directory_epoch"`
	Outcome        ReadOutcome               `json:"outcome"`
	Code           string                    `json:"code"`
	Subjects       []CommunicationSubjectRef `json:"subjects"`
	ObservedAt     time.Time                 `json:"observed_at"`
	FreshUntil     time.Time                 `json:"fresh_until"`
	EvidenceRef    string                    `json:"evidence_ref"`
}

func grantHasBit(grant ChannelGrant, bit ChannelGrantBit) bool {
	switch bit {
	case ChannelGrantRead:
		return grant.CanRead
	case ChannelGrantWrite:
		return grant.CanWrite
	case ChannelGrantAdmin:
		return grant.CanAdmin
	default:
		return false
	}
}

func validateChannelGrantSnapshotRow(
	grant ChannelGrant,
	tenantID model.TenantID,
	workspaceID model.ID,
	channelID model.ID,
) error {
	if err := ValidateChannelGrant(grant); err != nil {
		return err
	}
	if grant.TenantID != tenantID || grant.WorkspaceID != workspaceID || grant.ChannelID != channelID ||
		grant.Subject.Validate() != nil {
		return communicationError(ErrInvalidCommunicationModel, "invalid current ChannelGrant snapshot")
	}
	return nil
}

// EvaluateCurrentChannelGrant keeps read/write/admin independent and evaluates
// expiry against the caller's DB time.
func EvaluateCurrentChannelGrant(
	snapshot ChannelGrantSnapshot,
	tenantID model.TenantID,
	workspaceID model.ID,
	channelID model.ID,
	closure ChannelGrantSubjectClosure,
	bit ChannelGrantBit,
	dbNow time.Time,
) BoundChannelReadEvidence {
	bound := func(evidence AuthorityEvidence, selected ...ChannelGrant) BoundChannelReadEvidence {
		if evidence.EvidenceRef == "" {
			evidence.EvidenceRef = closure.EvidenceRef
		}
		result := BoundChannelReadEvidence{
			TenantID: tenantID, WorkspaceID: workspaceID, ChannelID: channelID,
			Principal: closure.Principal, Bit: bit, DirectoryEpoch: closure.DirectoryEpoch,
			ChannelACLRevision: snapshot.ACLRevision, EvaluatedAt: dbNow, Evidence: evidence,
		}
		if len(selected) == 1 {
			result.GrantID = selected[0].ID
			result.GrantVersion = selected[0].Version
		}
		return result
	}
	if snapshot.Verdict == VerdictUnknown || !validAssessmentVerdict(snapshot.Verdict) || dbNow.IsZero() ||
		!validCanonicalCommunicationTenant(tenantID) || !validCanonicalCommunicationID(workspaceID) ||
		!validCanonicalCommunicationID(channelID) || snapshot.ACLRevision < 1 || snapshot.ObservedAt != dbNow ||
		ValidateCommunicationPrincipalForScope(closure.Principal, DirectoryScopeRef{
			TenantID: tenantID, WorkspaceID: workspaceID,
		}) != nil ||
		closure.Scope != (DirectoryScopeRef{TenantID: tenantID, WorkspaceID: workspaceID}) ||
		closure.DirectoryEpoch < 1 || !closure.Outcome.Valid() || !boundedToken(closure.Code, 128) ||
		closure.ObservedAt.After(dbNow) || !closure.FreshUntil.After(closure.ObservedAt) ||
		dbNow.After(closure.FreshUntil) || !validateOpaqueRef(closure.EvidenceRef) {
		return bound(AuthorityEvidence{Verdict: VerdictUnknown, Code: "channel_grant_unavailable"})
	}
	if closure.Outcome == ReadUnknown {
		return bound(AuthorityEvidence{Verdict: VerdictUnknown, Code: closure.Code})
	}
	if closure.Outcome == ReadDeny {
		return bound(AuthorityEvidence{Verdict: VerdictBroken, Code: closure.Code})
	}
	if snapshot.Verdict == VerdictBroken || !bit.Valid() {
		return bound(AuthorityEvidence{Verdict: VerdictBroken, Code: "channel_grant_denied"})
	}
	subjects := make(map[CommunicationSubjectRef]struct{}, len(closure.Subjects))
	for _, subject := range closure.Subjects {
		if subject.Validate() != nil {
			return bound(AuthorityEvidence{Verdict: VerdictUnknown, Code: "invalid_subject_closure"})
		}
		if _, duplicate := subjects[subject]; duplicate {
			return bound(AuthorityEvidence{Verdict: VerdictUnknown, Code: "duplicate_subject_closure"})
		}
		subjects[subject] = struct{}{}
	}
	if closure.Principal.System {
		directAgent := CommunicationSubjectRef{
			Kind: SubjectAgent, Ref: closure.Principal.SystemGrantAgentID.String(),
		}
		if _, present := subjects[directAgent]; !present {
			return bound(AuthorityEvidence{
				Verdict: VerdictUnknown, Code: "system_agent_binding_unavailable",
			})
		}
	}
	for _, grant := range snapshot.Grants {
		if err := validateChannelGrantSnapshotRow(grant, tenantID, workspaceID, channelID); err != nil {
			return bound(AuthorityEvidence{Verdict: VerdictUnknown, Code: "invalid_channel_grant"})
		}
	}
	matching := make([]ChannelGrant, 0, len(snapshot.Grants))
	for _, grant := range snapshot.Grants {
		if grant.State != ChannelGrantActive || (grant.ExpiresAt != nil && !dbNow.Before(*grant.ExpiresAt)) {
			continue
		}
		if _, matches := subjects[grant.Subject]; matches && grantHasBit(grant, bit) {
			matching = append(matching, grant)
		}
	}
	if len(matching) > 0 {
		sort.Slice(matching, func(i, j int) bool { return matching[i].ID.String() < matching[j].ID.String() })
		return bound(AuthorityEvidence{Verdict: VerdictClean, Code: "channel_grant_current"}, matching[0])
	}
	return bound(AuthorityEvidence{Verdict: VerdictBroken, Code: "channel_grant_missing"})
}

type CausalAuthorityKind string

const (
	CausalAuthorityDirectPrincipal CausalAuthorityKind = "direct_principal"
	CausalAuthorityUserGroup       CausalAuthorityKind = "user_group_membership"
	CausalAuthorityAgentGroup      CausalAuthorityKind = "agent_group_membership"
	CausalAuthorityWorkspaceUser   CausalAuthorityKind = "workspace_user_membership"
	CausalAuthorityWorkspaceAgent  CausalAuthorityKind = "workspace_agent"
	CausalAuthoritySubscriber      CausalAuthorityKind = "subscriber"
	CausalAuthoritySessionClaim    CausalAuthorityKind = "session_claim"
)

type CausalAuthorityWitness struct {
	Kind               CausalAuthorityKind         `json:"kind"`
	Scope              DirectoryScopeRef           `json:"scope"`
	ContributionID     model.ID                    `json:"contribution_id"`
	Recipient          RecipientRef                `json:"recipient"`
	CausalKind         AudienceCausalKind          `json:"causal_kind"`
	CausalRef          string                      `json:"causal_ref"`
	CurrentFact        *store.AuthorizationFactRef `json:"current_fact,omitempty"`
	CurrentRelation    *CausalRelationWitness      `json:"current_relation,omitempty"`
	ObservedSessionSID string                      `json:"observed_session_sid,omitempty"`
	ObservedClaimFence int64                       `json:"observed_claim_fence,omitempty"`
	DirectoryEpoch     int64                       `json:"directory_epoch"`
	ObservedAt         time.Time                   `json:"observed_at"`
	Evidence           AuthorityEvidence           `json:"evidence"`
}

// CausalRelationWitness names the exact current relationship represented by
// CurrentFact. Historic audience fact IDs remain provenance: removing and
// recreating this same relation may legitimately produce a new current fact.
type CausalRelationWitness struct {
	Scope                  DirectoryScopeRef           `json:"scope"`
	Recipient              RecipientRef                `json:"recipient"`
	Subject                CommunicationSubjectRef     `json:"subject"`
	CausalKind             AudienceCausalKind          `json:"causal_kind"`
	CausalRef              string                      `json:"causal_ref"`
	DirectoryEpoch         int64                       `json:"directory_epoch"`
	SubscriptionID         model.ID                    `json:"subscription_id,omitempty"`
	SubscriptionGeneration int64                       `json:"subscription_generation,omitempty"`
	CurrentFact            *store.AuthorizationFactRef `json:"current_fact,omitempty"`
}

type CausalContributionEvidence struct {
	Audience     MessageAudience          `json:"audience"`
	Contribution MessageAudienceRecipient `json:"contribution"`
	Witness      CausalAuthorityWitness   `json:"witness"`
}

type RecipientAuthorityCheck string

const (
	RecipientCheckExists        RecipientAuthorityCheck = "exists"
	RecipientCheckEligible      RecipientAuthorityCheck = "eligible"
	RecipientCheckNotTombstoned RecipientAuthorityCheck = "not_tombstoned"
)

type BoundRecipientAuthorityEvidence struct {
	Scope          DirectoryScopeRef       `json:"scope"`
	Recipient      RecipientRef            `json:"recipient"`
	DirectoryEpoch int64                   `json:"directory_epoch"`
	Check          RecipientAuthorityCheck `json:"check"`
	ObservedAt     time.Time               `json:"observed_at"`
	Evidence       AuthorityEvidence       `json:"evidence"`
}

// CurrentAudienceSetWitness is a same-transaction attestation over the
// complete audience projection of the locked Message. MessageAudienceHash
// binds the immutable publication semantics; SetDigest additionally binds
// row identities and Delivery lineage, which are intentionally outside the
// Message audience seal.
type CurrentAudienceSetWitness struct {
	Scope               DirectoryScopeRef          `json:"scope"`
	MessageID           model.ID                   `json:"message_id"`
	MessageVersion      int64                      `json:"message_version"`
	DeliveryID          model.ID                   `json:"delivery_id"`
	Recipient           RecipientRef               `json:"recipient"`
	MessageAudienceHash []byte                     `json:"message_audience_hash"`
	AudienceCount       int64                      `json:"audience_count"`
	ContributionCount   int64                      `json:"contribution_count"`
	Audiences           []MessageAudience          `json:"audiences"`
	Contributions       []MessageAudienceRecipient `json:"contributions"`
	SetDigest           []byte                     `json:"set_digest"`
	ObservedAt          time.Time                  `json:"observed_at"`
	Evidence            AuthorityEvidence          `json:"evidence"`
}

type currentAudienceSetDigestInput struct {
	Scope          DirectoryScopeRef          `json:"scope"`
	MessageID      model.ID                   `json:"message_id"`
	MessageVersion int64                      `json:"message_version"`
	Audiences      []MessageAudience          `json:"audiences"`
	Contributions  []MessageAudienceRecipient `json:"contributions"`
}

// CanonicalCurrentAudienceSetDigest seals the exact durable row set, including
// append-only row IDs and Delivery IDs. The caller must populate it from the
// same locked snapshot used for CurrentAudience evaluation.
func CanonicalCurrentAudienceSetDigest(
	scope DirectoryScopeRef,
	messageID model.ID,
	messageVersion int64,
	audiences []MessageAudience,
	contributions []MessageAudienceRecipient,
) ([]byte, error) {
	if scope.Validate() != nil || !validCanonicalCommunicationID(messageID) || messageVersion < 1 ||
		len(audiences) < 1 || len(audiences) > 64 {
		return nil, communicationError(ErrInvalidCommunicationModel,
			"invalid current audience set digest input")
	}
	orderedAudiences := append([]MessageAudience(nil), audiences...)
	sort.Slice(orderedAudiences, func(i, j int) bool {
		if orderedAudiences[i].Ordinal != orderedAudiences[j].Ordinal {
			return orderedAudiences[i].Ordinal < orderedAudiences[j].Ordinal
		}
		return orderedAudiences[i].ID.String() < orderedAudiences[j].ID.String()
	})
	orderedContributions := append([]MessageAudienceRecipient(nil), contributions...)
	sort.Slice(orderedContributions, func(i, j int) bool {
		left, right := orderedContributions[i], orderedContributions[j]
		if left.MessageAudienceID != right.MessageAudienceID {
			return left.MessageAudienceID.String() < right.MessageAudienceID.String()
		}
		if left.Recipient != right.Recipient {
			if left.Recipient.Kind != right.Recipient.Kind {
				return left.Recipient.Kind < right.Recipient.Kind
			}
			return left.Recipient.Ref < right.Recipient.Ref
		}
		if compared := bytes.Compare(left.CausalArcHash, right.CausalArcHash); compared != 0 {
			return compared < 0
		}
		return left.ID.String() < right.ID.String()
	})
	raw, err := canonicalJSON(currentAudienceSetDigestInput{
		Scope: scope, MessageID: messageID, MessageVersion: messageVersion, Audiences: orderedAudiences,
		Contributions: orderedContributions,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

type CurrentAudienceEvidence struct {
	TenantID               model.TenantID                  `json:"tenant_id"`
	WorkspaceID            model.ID                        `json:"workspace_id"`
	Recipient              RecipientRef                    `json:"recipient"`
	DeliveryID             model.ID                        `json:"delivery_id"`
	MessageID              model.ID                        `json:"message_id"`
	DirectoryEpoch         int64                           `json:"directory_epoch"`
	ObservedAt             time.Time                       `json:"observed_at"`
	FreshUntil             time.Time                       `json:"fresh_until"`
	RecipientExists        BoundRecipientAuthorityEvidence `json:"recipient_exists"`
	RecipientEligible      BoundRecipientAuthorityEvidence `json:"recipient_eligible"`
	RecipientNotTombstoned BoundRecipientAuthorityEvidence `json:"recipient_not_tombstoned"`
	SetWitness             CurrentAudienceSetWitness       `json:"set_witness"`
	Contributions          []CausalContributionEvidence    `json:"contributions"`
}

type CommunicationClaimRef struct {
	SessionSID string `json:"session_sid"`
	Fence      int64  `json:"fence"`
}

type CurrentAudienceDecision struct {
	AuthorityEvidence
	SurvivingContributionIDs []model.ID              `json:"surviving_contribution_ids,omitempty"`
	RequiredClaims           []CommunicationClaimRef `json:"required_claims,omitempty"`
}

func ValidateProtectedReadDecision(decision ProtectedReadDecision) error {
	if !decision.Operation.Valid() || !validAssessmentVerdict(decision.Verdict) ||
		!boundedToken(decision.Code, 128) || decision.Recipient.Validate() != nil ||
		decision.PrincipalRecipient.Validate() != nil ||
		ValidateCommunicationPrincipalForScope(decision.Principal, DirectoryScopeRef{
			TenantID: decision.Entity.TenantID, WorkspaceID: decision.Entity.WorkspaceID,
		}) != nil {
		return communicationError(ErrInvalidCommunicationModel, "invalid protected carrier decision")
	}
	for _, check := range decision.Checks {
		if !boundedToken(check.Name, 128) || ValidateAuthorityEvidence(check.Evidence) != nil {
			return communicationError(ErrInvalidCommunicationModel, "invalid protected decision check")
		}
	}
	want := evaluateAuthorityChecks(decision.Checks)
	if want.Verdict != decision.Verdict || want.Code != decision.Code {
		return communicationError(ErrInvalidCommunicationModel, "protected decision verdict mismatch")
	}
	if _, err := CanonicalAuthorizationFacts(decision.Facts); err != nil {
		return err
	}
	if decision.Verdict == VerdictClean {
		if len(decision.Facts) == 0 {
			return communicationError(ErrInvalidCommunicationModel, "clean protected decision lacks facts")
		}
		got := canonicalCommunicationClaims(append([]CommunicationClaimRef(nil), decision.RequiredClaims...))
		claimsMatch := len(got) == len(decision.RequiredClaims)
		for i := range got {
			claimsMatch = claimsMatch && got[i] == decision.RequiredClaims[i]
		}
		if !claimsMatch {
			return communicationError(ErrInvalidCommunicationModel, "invalid protected decision Claims")
		}
	} else if len(decision.SurvivingContributionIDs) != 0 || len(decision.RequiredClaims) != 0 {
		return communicationError(ErrInvalidCommunicationModel,
			"non-clean protected decision carries mutation effects")
	}
	return nil
}

// EvaluateCurrentAudience first applies current recipient eligibility to every
// causal kind, then requires at least one surviving original contribution.
func EvaluateCurrentAudience(input CurrentAudienceEvidence) AuthorityEvidence {
	return EvaluateCurrentAudienceDetailed(input).AuthorityEvidence
}

func canonicalCommunicationValueEqual(left, right any) bool {
	leftRaw, leftErr := canonicalJSON(left)
	rightRaw, rightErr := canonicalJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}

func validateCurrentAudienceSetWitness(input CurrentAudienceEvidence) bool {
	witness := input.SetWitness
	scope := DirectoryScopeRef{TenantID: input.TenantID, WorkspaceID: input.WorkspaceID}
	if witness.Scope != scope || witness.MessageID != input.MessageID || witness.MessageVersion < 1 ||
		witness.DeliveryID != input.DeliveryID || witness.Recipient != input.Recipient ||
		witness.ObservedAt.IsZero() || witness.AudienceCount != int64(len(witness.Audiences)) ||
		witness.ContributionCount != int64(len(witness.Contributions)) ||
		len(witness.MessageAudienceHash) != sha256.Size || len(witness.SetDigest) != sha256.Size ||
		ValidateAuthorityEvidence(witness.Evidence) != nil ||
		evidenceVerdict(witness.Evidence) != VerdictClean {
		return false
	}
	seenContributionIDs := make(map[model.ID]struct{}, len(witness.Contributions))
	for _, contribution := range witness.Contributions {
		if _, duplicate := seenContributionIDs[contribution.ID]; duplicate {
			return false
		}
		seenContributionIDs[contribution.ID] = struct{}{}
	}
	message := Message{MutableCommunicationEntity: MutableCommunicationEntity{
		CommunicationEntity: CommunicationEntity{
			ID: input.MessageID, TenantID: input.TenantID, WorkspaceID: input.WorkspaceID,
			Version: witness.MessageVersion,
		},
	}}
	audienceHash, err := CanonicalMessageAudienceHash(message, witness.Audiences, witness.Contributions)
	if err != nil || !bytes.Equal(audienceHash, witness.MessageAudienceHash) {
		return false
	}
	setDigest, err := CanonicalCurrentAudienceSetDigest(
		scope, input.MessageID, witness.MessageVersion, witness.Audiences, witness.Contributions,
	)
	if err != nil || !bytes.Equal(setDigest, witness.SetDigest) {
		return false
	}
	audienceByID := make(map[model.ID]MessageAudience, len(witness.Audiences))
	for _, audience := range witness.Audiences {
		if _, duplicate := audienceByID[audience.ID]; duplicate {
			return false
		}
		audienceByID[audience.ID] = audience
	}
	targetRows := make(map[model.ID]MessageAudienceRecipient)
	for _, row := range witness.Contributions {
		sameDelivery := row.MessageDeliveryID == input.DeliveryID
		sameRecipient := row.Recipient == input.Recipient
		if sameDelivery != sameRecipient {
			return false
		}
		if sameDelivery {
			if _, duplicate := targetRows[row.ID]; duplicate {
				return false
			}
			targetRows[row.ID] = row
		}
	}
	if len(targetRows) == 0 || len(input.Contributions) != len(targetRows) {
		return false
	}
	seen := make(map[model.ID]struct{}, len(input.Contributions))
	for _, item := range input.Contributions {
		row, present := targetRows[item.Contribution.ID]
		audience, audiencePresent := audienceByID[row.MessageAudienceID]
		if !present || !audiencePresent || !canonicalCommunicationValueEqual(row, item.Contribution) ||
			!canonicalCommunicationValueEqual(audience, item.Audience) {
			return false
		}
		if _, duplicate := seen[row.ID]; duplicate {
			return false
		}
		seen[row.ID] = struct{}{}
	}
	return len(seen) == len(targetRows)
}

func EvaluateCurrentAudienceDetailed(input CurrentAudienceEvidence) CurrentAudienceDecision {
	result := func(verdict AssessmentVerdict, code string) CurrentAudienceDecision {
		return CurrentAudienceDecision{AuthorityEvidence: AuthorityEvidence{
			Verdict: verdict, Code: code, EvidenceRef: "derived:current_audience",
		}}
	}
	if !validCanonicalCommunicationTenant(input.TenantID) ||
		!validCanonicalCommunicationID(input.WorkspaceID) || input.Recipient.Validate() != nil ||
		!validCanonicalCommunicationID(input.DeliveryID) || !validCanonicalCommunicationID(input.MessageID) ||
		input.DirectoryEpoch < 1 ||
		input.ObservedAt.IsZero() || !input.FreshUntil.After(input.ObservedAt) {
		return result(VerdictUnknown, "invalid_audience_binding")
	}
	if !validateCurrentAudienceSetWitness(input) {
		return result(VerdictUnknown, "audience_set_unavailable")
	}
	scope := DirectoryScopeRef{TenantID: input.TenantID, WorkspaceID: input.WorkspaceID}
	checks := []struct {
		want     RecipientAuthorityCheck
		evidence BoundRecipientAuthorityEvidence
	}{
		{want: RecipientCheckExists, evidence: input.RecipientExists},
		{want: RecipientCheckEligible, evidence: input.RecipientEligible},
		{want: RecipientCheckNotTombstoned, evidence: input.RecipientNotTombstoned},
	}
	for _, check := range checks {
		if check.evidence.Scope != scope || check.evidence.Recipient != input.Recipient ||
			check.evidence.DirectoryEpoch != input.DirectoryEpoch || check.evidence.Check != check.want ||
			check.evidence.ObservedAt != input.ObservedAt ||
			ValidateAuthorityEvidence(check.evidence.Evidence) != nil {
			return result(VerdictUnknown, "recipient_authority_unbound")
		}
	}
	recipientVerdict := andVerdicts(
		evidenceVerdict(input.RecipientExists.Evidence),
		evidenceVerdict(input.RecipientEligible.Evidence),
		evidenceVerdict(input.RecipientNotTombstoned.Evidence),
	)
	type cleanAudienceCause struct {
		contributionID model.ID
		causalArcHash  []byte
		session        bool
		claim          CommunicationClaimRef
	}
	causeVerdicts := make([]AssessmentVerdict, 0, len(input.Contributions))
	cleanCauses := make([]cleanAudienceCause, 0, len(input.Contributions))
	for _, contribution := range input.Contributions {
		audience := contribution.Audience
		row := contribution.Contribution
		witness := contribution.Witness
		if ValidateMessageAudience(audience) != nil || audience.TenantID != input.TenantID ||
			audience.WorkspaceID != input.WorkspaceID || audience.MessageID != input.MessageID ||
			row.MessageAudienceID != audience.ID || row.Selector != audience.Selector ||
			row.DirectoryEpoch != audience.DirectoryEpoch ||
			row.ChannelACLRevision != audience.ChannelACLRevision ||
			row.RouteRevision != audience.RouteRevision ||
			row.SubscriptionRevision != audience.SubscriptionRevision ||
			validateAudienceContribution(row) != nil || row.TenantID != input.TenantID ||
			row.WorkspaceID != input.WorkspaceID || row.Recipient != input.Recipient ||
			row.MessageDeliveryID != input.DeliveryID || witness.ContributionID != row.ID ||
			witness.Scope != scope ||
			witness.Recipient != row.Recipient || witness.CausalKind != row.CausalKind ||
			witness.CausalRef != row.CausalRef || witness.DirectoryEpoch != input.DirectoryEpoch ||
			witness.ObservedAt != input.ObservedAt ||
			ValidateAuthorityEvidence(witness.Evidence) != nil ||
			!causalAuthorityWitnessMatches(row, witness) {
			causeVerdicts = append(causeVerdicts, VerdictUnknown)
			continue
		}
		witnessVerdict := evidenceVerdict(witness.Evidence)
		causeVerdicts = append(causeVerdicts, witnessVerdict)
		if witnessVerdict == VerdictClean {
			candidate := cleanAudienceCause{
				contributionID: row.ID, causalArcHash: append([]byte(nil), row.CausalArcHash...),
			}
			if witness.Kind == CausalAuthoritySessionClaim {
				candidate.session = true
				candidate.claim = CommunicationClaimRef{
					SessionSID: witness.ObservedSessionSID, Fence: witness.ObservedClaimFence,
				}
			}
			cleanCauses = append(cleanCauses, candidate)
		}
	}
	causeVerdict := VerdictBroken
	if len(causeVerdicts) > 0 {
		causeVerdict = orVerdicts(causeVerdicts...)
	}
	verdict := andVerdicts(recipientVerdict, causeVerdict)
	switch verdict {
	case VerdictClean:
		sort.Slice(cleanCauses, func(i, j int) bool {
			if cleanCauses[i].session != cleanCauses[j].session {
				return !cleanCauses[i].session
			}
			if compared := bytes.Compare(cleanCauses[i].causalArcHash, cleanCauses[j].causalArcHash); compared != 0 {
				return compared < 0
			}
			return cleanCauses[i].contributionID.String() < cleanCauses[j].contributionID.String()
		})
		selected := cleanCauses[0]
		claims := []CommunicationClaimRef(nil)
		if selected.session {
			claims = []CommunicationClaimRef{selected.claim}
		}
		return CurrentAudienceDecision{
			AuthorityEvidence: AuthorityEvidence{
				Verdict: VerdictClean, Code: "current_audience_cause",
				EvidenceRef: "derived:current_audience",
			},
			SurvivingContributionIDs: []model.ID{selected.contributionID}, RequiredClaims: claims,
		}
	case VerdictBroken:
		return result(VerdictBroken, "audience_cause_revoked")
	default:
		return result(VerdictUnknown, "audience_cause_unavailable")
	}
}

func canonicalCommunicationClaims(claims []CommunicationClaimRef) []CommunicationClaimRef {
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].SessionSID != claims[j].SessionSID {
			return claims[i].SessionSID < claims[j].SessionSID
		}
		return claims[i].Fence < claims[j].Fence
	})
	result := claims[:0]
	for _, claim := range claims {
		if !validCanonicalCommunicationSID(claim.SessionSID) || claim.Fence < 1 {
			continue
		}
		if len(result) == 0 || result[len(result)-1] != claim {
			result = append(result, claim)
		}
	}
	return result
}

func causalAuthorityWitnessMatches(row MessageAudienceRecipient, witness CausalAuthorityWitness) bool {
	relationMatches := func(subject CommunicationSubjectRef, kind model.Kind, factRequired bool) bool {
		relation := witness.CurrentRelation
		if relation == nil || relation.Scope != witness.Scope || relation.Recipient != row.Recipient ||
			relation.Subject != subject || relation.CausalKind != row.CausalKind ||
			relation.CausalRef != row.CausalRef || relation.DirectoryEpoch != witness.DirectoryEpoch ||
			relation.SubscriptionID != row.SubscriptionID ||
			relation.SubscriptionGeneration != row.SubscriptionGeneration {
			return false
		}
		if !factRequired {
			return witness.CurrentFact == nil && relation.CurrentFact == nil
		}
		return witness.CurrentFact != nil && relation.CurrentFact != nil &&
			*witness.CurrentFact == *relation.CurrentFact && witness.CurrentFact.Kind == kind &&
			validCanonicalCommunicationID(witness.CurrentFact.ID) && witness.CurrentFact.Version > 0
	}
	sessionMatches := func() bool {
		return witness.Kind == CausalAuthoritySessionClaim && witness.CurrentFact == nil &&
			witness.ObservedSessionSID == row.ObservedSessionSID &&
			witness.ObservedClaimFence == row.ObservedClaimFence && row.ObservedClaimFence > 0
	}
	if row.Recipient.Kind != RecipientSession &&
		(witness.ObservedSessionSID != "" || witness.ObservedClaimFence != 0) {
		return false
	}
	switch row.CausalKind {
	case CausalDirect:
		if row.Recipient.Kind == RecipientSession {
			return sessionMatches() && witness.CurrentRelation == nil
		}
		return witness.Kind == CausalAuthorityDirectPrincipal && witness.CurrentFact == nil &&
			witness.CurrentRelation == nil
	case CausalUserGroup:
		return witness.Kind == CausalAuthorityUserGroup && relationMatches(CommunicationSubjectRef{
			Kind: SubjectUserGroup, Ref: row.CausalRef,
		}, model.Kind("core.user_group_member"), true)
	case CausalAgentGroup:
		return witness.Kind == CausalAuthorityAgentGroup && relationMatches(CommunicationSubjectRef{
			Kind: SubjectAgentGroup, Ref: row.CausalRef,
		}, model.Kind("core.agent_group_member"), true)
	case CausalWorkspaceMember:
		if row.Recipient.Kind == RecipientUser {
			return witness.Kind == CausalAuthorityWorkspaceUser && relationMatches(CommunicationSubjectRef{
				Kind: SubjectUser, Ref: row.Recipient.Ref,
			}, model.Kind("core.membership"), true)
		}
		return witness.Kind == CausalAuthorityWorkspaceAgent && relationMatches(CommunicationSubjectRef{
			Kind: SubjectAgent, Ref: row.Recipient.Ref,
		}, model.Kind("core.agent"), true)
	case CausalSubscriber:
		if row.OriginalSubscriber == nil {
			return false
		}
		switch row.OriginalSubscriber.Kind {
		case SubjectSession:
			return sessionMatches() && relationMatches(*row.OriginalSubscriber, "", false)
		case SubjectUserGroup:
			return witness.Kind == CausalAuthoritySubscriber && relationMatches(
				*row.OriginalSubscriber, model.Kind("core.user_group_member"), true)
		case SubjectAgentGroup:
			return witness.Kind == CausalAuthoritySubscriber && relationMatches(
				*row.OriginalSubscriber, model.Kind("core.agent_group_member"), true)
		case SubjectUser, SubjectAgent:
			return witness.Kind == CausalAuthoritySubscriber &&
				relationMatches(*row.OriginalSubscriber, "", false)
		}
	}
	return false
}

type CursorDeliveryVisibility string

const (
	CursorDeliveryVisible              CursorDeliveryVisibility = "visible"
	CursorDeliveryForeignGap           CursorDeliveryVisibility = "foreign_gap"
	CursorDeliveryUndeliverable        CursorDeliveryVisibility = "definitively_undeliverable"
	CursorDeliveryNotYetAvailable      CursorDeliveryVisibility = "not_yet_available"
	CursorDeliveryTemporarilyInvisible CursorDeliveryVisibility = "temporarily_invisible"
	CursorDeliveryEvidenceUnknown      CursorDeliveryVisibility = "unknown"
)

func (v CursorDeliveryVisibility) Valid() bool {
	return oneOf(v, CursorDeliveryVisible, CursorDeliveryForeignGap, CursorDeliveryUndeliverable,
		CursorDeliveryNotYetAvailable, CursorDeliveryTemporarilyInvisible, CursorDeliveryEvidenceUnknown)
}

// CursorCarrierClass identifies an immutable carrier graph rather than a
// mutable Message projection. C2 deliberately has one class: the exact
// personal User DirectNotice graph used by the private inbox read slice.
type CursorCarrierClass string

const (
	CursorCarrierDirectNoticeV1 CursorCarrierClass = "direct_notice_v1"

	directNoticeCursorFilterSchema = "sessions.direct_notice_cursor_filter.v1"
	maxCursorMailboxScanEntries    = 4096
)

func (v CursorCarrierClass) Valid() bool { return v == CursorCarrierDirectNoticeV1 }

// CursorFilter is deliberately closed over immutable Message envelope fields.
// Mutable state, action projections, current visibility and time windows have
// no representation here and therefore cannot change a cursor's meaning.
type CursorFilter struct {
	CarrierClass CursorCarrierClass `json:"carrier_class,omitempty"`
	MailboxKind  MailboxKind        `json:"mailbox_kind,omitempty"`
	ChannelIDs   []model.ID         `json:"channel_ids,omitempty"`
	WorkItemIDs  []model.ID         `json:"work_item_ids,omitempty"`
	MessageKinds []MessageKind      `json:"message_kinds,omitempty"`
	Urgencies    []MessageUrgency   `json:"urgencies,omitempty"`
}

type CursorImmutableEnvelope struct {
	CarrierClass CursorCarrierClass `json:"carrier_class,omitempty"`
	MailboxKind  MailboxKind        `json:"mailbox_kind,omitempty"`
	MessageID    model.ID           `json:"message_id"`
	ChannelID    model.ID           `json:"channel_id"`
	WorkItemID   model.ID           `json:"work_item_id,omitempty"`
	Kind         MessageKind        `json:"message_kind"`
	Urgency      MessageUrgency     `json:"urgency"`
}

type directNoticeCursorFilterHashInput struct {
	Schema       string             `json:"schema"`
	CarrierClass CursorCarrierClass `json:"carrier_class"`
	MailboxKind  MailboxKind        `json:"mailbox_kind"`
}

func directNoticeCursorFilterHash() ([sha256.Size]byte, error) {
	raw, err := canonicalJSON(directNoticeCursorFilterHashInput{
		Schema:       directNoticeCursorFilterSchema,
		CarrierClass: CursorCarrierDirectNoticeV1,
		MailboxKind:  MailboxPersonal,
	})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

func CanonicalCursorFilter(filter CursorFilter) (CursorFilter, []byte, error) {
	if len(filter.ChannelIDs)+len(filter.WorkItemIDs)+len(filter.MessageKinds)+len(filter.Urgencies) > 64 {
		return CursorFilter{}, nil, communicationError(ErrInvalidCommunicationModel,
			"cursor filter exceeds 64 terms")
	}
	if filter.CarrierClass != "" {
		if !filter.CarrierClass.Valid() || filter.MailboxKind != MailboxPersonal ||
			len(filter.ChannelIDs) != 0 || len(filter.WorkItemIDs) != 0 ||
			len(filter.MessageKinds) != 0 || len(filter.Urgencies) != 0 {
			return CursorFilter{}, nil, communicationError(ErrInvalidCommunicationModel,
				"carrier cursor filter is not the fixed personal DirectNotice filter")
		}
		canonical := CursorFilter{
			CarrierClass: CursorCarrierDirectNoticeV1,
			MailboxKind:  MailboxPersonal,
		}
		digest, err := directNoticeCursorFilterHash()
		if err != nil {
			return CursorFilter{}, nil, err
		}
		return canonical, digest[:], nil
	}
	if filter.MailboxKind != "" {
		return CursorFilter{}, nil, communicationError(ErrInvalidCommunicationModel,
			"cursor mailbox kind requires a carrier class")
	}
	canonical := CursorFilter{}
	canonicalizeIDs := func(values []model.ID) ([]model.ID, error) {
		out := append([]model.ID(nil), values...)
		for _, value := range out {
			if !validCanonicalCommunicationID(value) {
				return nil, communicationError(ErrInvalidCommunicationModel,
					"cursor filter contains non-canonical ID")
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
		result := out[:0]
		for _, value := range out {
			if len(result) == 0 || result[len(result)-1] != value {
				result = append(result, value)
			}
		}
		return result, nil
	}
	var err error
	canonical.ChannelIDs, err = canonicalizeIDs(filter.ChannelIDs)
	if err != nil {
		return CursorFilter{}, nil, err
	}
	canonical.WorkItemIDs, err = canonicalizeIDs(filter.WorkItemIDs)
	if err != nil {
		return CursorFilter{}, nil, err
	}
	canonical.MessageKinds = append([]MessageKind(nil), filter.MessageKinds...)
	for _, kind := range canonical.MessageKinds {
		if !kind.Valid() {
			return CursorFilter{}, nil, communicationError(ErrInvalidCommunicationModel,
				"cursor filter contains unknown Message kind")
		}
	}
	sort.Slice(canonical.MessageKinds, func(i, j int) bool {
		return canonical.MessageKinds[i] < canonical.MessageKinds[j]
	})
	canonical.MessageKinds = compactCommunicationStrings(canonical.MessageKinds)
	canonical.Urgencies = append([]MessageUrgency(nil), filter.Urgencies...)
	for _, urgency := range canonical.Urgencies {
		if !urgency.Valid() {
			return CursorFilter{}, nil, communicationError(ErrInvalidCommunicationModel,
				"cursor filter contains unknown urgency")
		}
	}
	sort.Slice(canonical.Urgencies, func(i, j int) bool { return canonical.Urgencies[i] < canonical.Urgencies[j] })
	canonical.Urgencies = compactCommunicationStrings(canonical.Urgencies)
	raw, err := canonicalJSON(canonical)
	if err != nil {
		return CursorFilter{}, nil, err
	}
	digest := sha256.Sum256(raw)
	return canonical, digest[:], nil
}

func compactCommunicationStrings[T ~string](values []T) []T {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

type CursorScanEntry struct {
	TenantID     model.TenantID                   `json:"tenant_id"`
	WorkspaceID  model.ID                         `json:"workspace_id"`
	DeliveryID   model.ID                         `json:"delivery_id"`
	Sequence     int64                            `json:"sequence"`
	Core         *ReadWitness                     `json:"core,omitempty"`
	Delivery     *MessageDelivery                 `json:"delivery,omitempty"`
	Message      *Message                         `json:"message,omitempty"`
	CarrierSet   *CursorCarrierSetWitness         `json:"carrier_set,omitempty"`
	ReadEvidence *ProtectedReadEvidence           `json:"read_evidence,omitempty"`
	Tombstone    *store.DirectoryTombstoneWitness `json:"tombstone,omitempty"`
}

// CursorCarrierSetWitness proves that the locked Message has exactly the
// Delivery named by the scan entry. Audience and contribution completeness are
// independently carried by CurrentAudienceSetWitness.
type CursorCarrierSetWitness struct {
	Scope         DirectoryScopeRef `json:"scope"`
	MessageID     model.ID          `json:"message_id"`
	DeliveryID    model.ID          `json:"delivery_id"`
	DeliveryCount int64             `json:"delivery_count"`
	ObservedAt    time.Time         `json:"observed_at"`
	Evidence      AuthorityEvidence `json:"evidence"`
}

type CursorAdvanceInput struct {
	Scope             DirectoryScopeRef        `json:"scope"`
	Principal         PrincipalResolution      `json:"principal"`
	Cursor            InboxCursor              `json:"cursor"`
	ExpectedVersion   int64                    `json:"expected_version"`
	Filter            CursorFilter             `json:"filter"`
	RequestedSeq      int64                    `json:"requested_seq"`
	DBNow             time.Time                `json:"db_now"`
	ActiveBarriers    []InboxCursorBarrier     `json:"active_barriers"`
	BarrierSetWitness CursorBarrierSetWitness  `json:"barrier_set_witness"`
	ScanWitness       CursorMailboxScanWitness `json:"scan_witness"`
	Scan              []CursorScanEntry        `json:"scan"`
}

// InitialInboxCursorAdvanceInput is the virtual-v0 form. CursorID is generated
// by the server, while Absence proves that the complete durable identity lookup
// returned no row at the same DB instant as the scan.
type InitialInboxCursorAdvanceInput struct {
	Scope           DirectoryScopeRef         `json:"scope"`
	Principal       PrincipalResolution       `json:"principal"`
	CursorID        model.ID                  `json:"cursor_id"`
	Reader          RecipientRef              `json:"reader"`
	MailboxKind     MailboxKind               `json:"mailbox_kind"`
	MailboxRef      string                    `json:"mailbox_ref"`
	ExpectedVersion int64                     `json:"expected_version"`
	Filter          CursorFilter              `json:"filter"`
	RequestedSeq    int64                     `json:"requested_seq"`
	DBNow           time.Time                 `json:"db_now"`
	Absence         InboxCursorAbsenceWitness `json:"absence_witness"`
	ScanWitness     CursorMailboxScanWitness  `json:"scan_witness"`
	Scan            []CursorScanEntry         `json:"scan"`
}

// InboxCursorAbsenceWitness attests a complete zero-row lookup by the exact
// durable cursor identity. It is not inferred from an empty generic list.
type InboxCursorAbsenceWitness struct {
	Scope       DirectoryScopeRef `json:"scope"`
	Reader      RecipientRef      `json:"reader"`
	MailboxKind MailboxKind       `json:"mailbox_kind"`
	MailboxRef  string            `json:"mailbox_ref"`
	FilterHash  []byte            `json:"filter_hash"`
	ObservedAt  time.Time         `json:"observed_at"`
	Evidence    AuthorityEvidence `json:"evidence"`
}

// CursorMailboxScanWitness binds a finite sparse scan of one mailbox. Sequence
// gaps are intentionally absent from Entries: another mailbox may own them.
// TargetDeliveryID binds the signed navigation target when ToInclusive moves.
type CursorMailboxScanWitness struct {
	Scope            DirectoryScopeRef `json:"scope"`
	Reader           RecipientRef      `json:"reader"`
	MailboxKind      MailboxKind       `json:"mailbox_kind"`
	MailboxRef       string            `json:"mailbox_ref"`
	FilterHash       []byte            `json:"filter_hash"`
	FromExclusive    int64             `json:"from_exclusive"`
	ToInclusive      int64             `json:"to_inclusive"`
	TargetDeliveryID model.ID          `json:"target_delivery_id,omitempty"`
	EntryCount       int64             `json:"entry_count"`
	Digest           []byte            `json:"digest"`
	FinalStoreCursor string            `json:"final_store_cursor,omitempty"`
	HasMore          bool              `json:"has_more"`
	ObservedAt       time.Time         `json:"observed_at"`
	Evidence         AuthorityEvidence `json:"evidence"`
}

type cursorMailboxScanDigestEntry struct {
	DeliveryID model.ID `json:"delivery_id"`
	Sequence   int64    `json:"sequence"`
}

type cursorMailboxScanDigestInput struct {
	Scope            DirectoryScopeRef              `json:"scope"`
	Reader           RecipientRef                   `json:"reader"`
	MailboxKind      MailboxKind                    `json:"mailbox_kind"`
	MailboxRef       string                         `json:"mailbox_ref"`
	FilterHash       []byte                         `json:"filter_hash"`
	FromExclusive    int64                          `json:"from_exclusive"`
	ToInclusive      int64                          `json:"to_inclusive"`
	TargetDeliveryID model.ID                       `json:"target_delivery_id,omitempty"`
	Entries          []cursorMailboxScanDigestEntry `json:"entries"`
	FinalStoreCursor string                         `json:"final_store_cursor,omitempty"`
	HasMore          bool                           `json:"has_more"`
}

// CanonicalCursorMailboxScanDigest seals the ordered mailbox identities, not
// the authority projections attached to them. This lets the service reobserve
// current authority while still proving that it rescanned the exact finite
// recipient range named by the navigation token.
func CanonicalCursorMailboxScanDigest(
	witness CursorMailboxScanWitness,
	entries []CursorScanEntry,
) ([]byte, error) {
	if witness.Scope.Validate() != nil || witness.Reader.Validate() != nil ||
		!witness.MailboxKind.Valid() || !validateOpaqueRef(witness.MailboxRef) ||
		len(witness.FilterHash) != sha256.Size || witness.FromExclusive < 0 ||
		witness.ToInclusive < witness.FromExclusive || witness.EntryCount != int64(len(entries)) ||
		len(entries) > maxCursorMailboxScanEntries ||
		(witness.FinalStoreCursor != "" && !validateOpaqueRef(witness.FinalStoreCursor)) {
		return nil, communicationError(ErrCommunicationEvidenceUnknown,
			"cursor mailbox scan witness is malformed")
	}
	if (witness.MailboxKind == MailboxPersonal && witness.MailboxRef != witness.Reader.Ref) ||
		(witness.MailboxKind == MailboxChannel &&
			!validCanonicalCommunicationID(model.ID(witness.MailboxRef))) {
		return nil, communicationError(ErrCommunicationEvidenceUnknown,
			"cursor mailbox scan witness crosses mailbox identity")
	}
	identities := make([]cursorMailboxScanDigestEntry, 0, len(entries))
	seenIDs := make(map[model.ID]struct{}, len(entries))
	var priorSeq int64
	for index, entry := range entries {
		if entry.TenantID != witness.Scope.TenantID || entry.WorkspaceID != witness.Scope.WorkspaceID ||
			!validCanonicalCommunicationID(entry.DeliveryID) || entry.Sequence <= witness.FromExclusive ||
			entry.Sequence > witness.ToInclusive || (index > 0 && entry.Sequence <= priorSeq) {
			return nil, communicationError(ErrCommunicationEvidenceUnknown,
				"cursor mailbox scan is unordered or crosses scope")
		}
		if _, duplicate := seenIDs[entry.DeliveryID]; duplicate {
			return nil, communicationError(ErrCommunicationEvidenceUnknown,
				"cursor mailbox scan repeats a Delivery")
		}
		seenIDs[entry.DeliveryID] = struct{}{}
		priorSeq = entry.Sequence
		identities = append(identities, cursorMailboxScanDigestEntry{
			DeliveryID: entry.DeliveryID,
			Sequence:   entry.Sequence,
		})
	}
	if witness.ToInclusive == witness.FromExclusive {
		if len(entries) != 0 || witness.TargetDeliveryID != "" {
			return nil, communicationError(ErrCommunicationEvidenceUnknown,
				"stationary cursor scan carries a target")
		}
	} else if len(entries) == 0 || !validCanonicalCommunicationID(witness.TargetDeliveryID) ||
		entries[len(entries)-1].Sequence != witness.ToInclusive ||
		entries[len(entries)-1].DeliveryID != witness.TargetDeliveryID {
		return nil, communicationError(ErrCommunicationEvidenceUnknown,
			"cursor mailbox scan does not contain its exact target")
	}
	raw, err := canonicalJSON(cursorMailboxScanDigestInput{
		Scope: witness.Scope, Reader: witness.Reader, MailboxKind: witness.MailboxKind,
		MailboxRef: witness.MailboxRef, FilterHash: append([]byte(nil), witness.FilterHash...),
		FromExclusive: witness.FromExclusive, ToInclusive: witness.ToInclusive,
		TargetDeliveryID: witness.TargetDeliveryID, Entries: identities,
		FinalStoreCursor: witness.FinalStoreCursor, HasMore: witness.HasMore,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func validateCursorMailboxScanWitness(
	witness CursorMailboxScanWitness,
	entries []CursorScanEntry,
	scope DirectoryScopeRef,
	cursor InboxCursor,
	requestedSeq int64,
	dbNow time.Time,
) error {
	if witness.Scope != scope || witness.Reader != cursor.Reader ||
		witness.MailboxKind != cursor.MailboxKind || witness.MailboxRef != cursor.MailboxRef ||
		!bytes.Equal(witness.FilterHash, cursor.FilterHash) ||
		witness.FromExclusive != cursor.LastSeenSeq || witness.ToInclusive != requestedSeq ||
		witness.HasMore || witness.ObservedAt != dbNow ||
		ValidateAuthorityEvidence(witness.Evidence) != nil ||
		evidenceVerdict(witness.Evidence) != VerdictClean {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"cursor mailbox scan is not a complete same-transaction witness")
	}
	digest, err := CanonicalCursorMailboxScanDigest(witness, entries)
	if err != nil || len(witness.Digest) != sha256.Size || !bytes.Equal(witness.Digest, digest) {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"cursor mailbox scan digest is unavailable")
	}
	return nil
}

func validateInboxCursorAbsenceWitness(
	witness InboxCursorAbsenceWitness,
	scope DirectoryScopeRef,
	reader RecipientRef,
	mailboxKind MailboxKind,
	mailboxRef string,
	filterHash []byte,
	dbNow time.Time,
) error {
	if witness.Scope != scope || witness.Reader != reader || witness.MailboxKind != mailboxKind ||
		witness.MailboxRef != mailboxRef || !bytes.Equal(witness.FilterHash, filterHash) ||
		witness.ObservedAt != dbNow || ValidateAuthorityEvidence(witness.Evidence) != nil ||
		evidenceVerdict(witness.Evidence) != VerdictClean {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"cursor absence is not a complete same-transaction witness")
	}
	return nil
}

type CursorBarrierSetWitness struct {
	Scope        DirectoryScopeRef `json:"scope"`
	CursorID     model.ID          `json:"cursor_id"`
	BarrierCount int64             `json:"barrier_count"`
	Digest       []byte            `json:"digest"`
	ObservedAt   time.Time         `json:"observed_at"`
	Evidence     AuthorityEvidence `json:"evidence"`
}

type CursorBarrierCreation struct {
	DeliveryID model.ID           `json:"delivery_id"`
	BarrierSeq int64              `json:"barrier_seq"`
	Cause      CursorBarrierCause `json:"cause"`
	ReasonCode string             `json:"reason_code"`
}

type CursorBarrierResolution struct {
	BarrierID  model.ID `json:"barrier_id"`
	DeliveryID model.ID `json:"delivery_id"`
	BarrierSeq int64    `json:"barrier_seq"`
	ReasonCode string   `json:"reason_code"`
}

type CursorAdvancePlan struct {
	Before         InboxCursor                  `json:"before"`
	After          InboxCursor                  `json:"after"`
	Verdict        AssessmentVerdict            `json:"verdict"`
	Code           string                       `json:"code"`
	CreateCursor   bool                         `json:"create_cursor"`
	Changed        bool                         `json:"changed"`
	PriorSeq       int64                        `json:"prior_seq"`
	RequestedSeq   int64                        `json:"requested_seq"`
	EffectiveSeq   int64                        `json:"effective_seq"`
	Expire         []MessageDeliveryExpiryPlan  `json:"expire_deliveries,omitempty"`
	Create         []CursorBarrierCreation      `json:"create_barriers,omitempty"`
	Resolve        []CursorBarrierResolution    `json:"resolve_barriers,omitempty"`
	Facts          []store.AuthorizationFactRef `json:"facts,omitempty"`
	RequiredClaims []CommunicationClaimRef      `json:"required_claims,omitempty"`
	ChannelFences  []CursorChannelFence         `json:"channel_fences,omitempty"`
}

type CursorChannelFence struct {
	ChannelID      model.ID          `json:"channel_id"`
	ACLRevision    int64             `json:"acl_revision"`
	GrantID        model.ID          `json:"grant_id,omitempty"`
	GrantVersion   int64             `json:"grant_version,omitempty"`
	GrantVerdict   AssessmentVerdict `json:"grant_verdict"`
	DirectoryEpoch int64             `json:"directory_epoch"`
	EvaluatedAt    time.Time         `json:"evaluated_at"`
}

func validateInboxCursor(cursor InboxCursor) error {
	if err := validateMutableCommunicationEntity(cursor.MutableCommunicationEntity); err != nil {
		return err
	}
	if cursor.Reader.Validate() != nil || !cursor.MailboxKind.Valid() || !validateOpaqueRef(cursor.MailboxRef) ||
		cursor.LastSeenSeq < 0 || cursor.LastSeenAt.IsZero() || len(cursor.FilterHash) != sha256.Size {
		return communicationError(ErrInvalidCommunicationModel, "invalid inbox cursor")
	}
	if (cursor.MailboxKind == MailboxPersonal && cursor.MailboxRef != cursor.Reader.Ref) ||
		(cursor.MailboxKind == MailboxChannel && !validCanonicalCommunicationID(model.ID(cursor.MailboxRef))) {
		return communicationError(ErrInvalidCommunicationModel, "cursor mailbox is not canonical")
	}
	return nil
}

func validateActiveBarrier(cursor InboxCursor, barrier InboxCursorBarrier) error {
	if err := validateMutableCommunicationEntity(barrier.MutableCommunicationEntity); err != nil {
		return err
	}
	if barrier.TenantID != cursor.TenantID || barrier.WorkspaceID != cursor.WorkspaceID ||
		barrier.Reader != cursor.Reader || barrier.MailboxKind != cursor.MailboxKind ||
		barrier.MailboxRef != cursor.MailboxRef || !bytes.Equal(barrier.FilterHash, cursor.FilterHash) ||
		!validCanonicalCommunicationID(barrier.DeliveryID) || barrier.BarrierSeq <= cursor.LastSeenSeq ||
		!barrier.Cause.Valid() || barrier.State != CursorBarrierActive || barrier.ResolvedAt != nil ||
		!boundedToken(barrier.ReasonCode, 128) {
		return communicationError(ErrInvalidCommunicationModel, "invalid active cursor barrier")
	}
	return nil
}

func ValidateInboxCursorBarrier(cursor InboxCursor, barrier InboxCursorBarrier) error {
	if err := validateMutableCommunicationEntity(barrier.MutableCommunicationEntity); err != nil {
		return err
	}
	if barrier.TenantID != cursor.TenantID || barrier.WorkspaceID != cursor.WorkspaceID ||
		barrier.Reader != cursor.Reader || barrier.MailboxKind != cursor.MailboxKind ||
		barrier.MailboxRef != cursor.MailboxRef || !bytes.Equal(barrier.FilterHash, cursor.FilterHash) ||
		!validCanonicalCommunicationID(barrier.DeliveryID) || barrier.BarrierSeq < 1 ||
		!barrier.Cause.Valid() || !barrier.State.Valid() || !boundedToken(barrier.ReasonCode, 128) {
		return communicationError(ErrInvalidCommunicationModel, "invalid cursor barrier")
	}
	switch barrier.State {
	case CursorBarrierActive:
		if barrier.BarrierSeq <= cursor.LastSeenSeq || barrier.ResolvedAt != nil {
			return communicationError(ErrInvalidCommunicationModel, "invalid active cursor barrier")
		}
	case CursorBarrierResolved:
		if barrier.ResolvedAt == nil || barrier.ResolvedAt.Before(barrier.CreatedAt) ||
			barrier.ResolvedAt.After(barrier.UpdatedAt) {
			return communicationError(ErrInvalidCommunicationModel, "invalid resolved cursor barrier")
		}
	}
	return nil
}

func CanonicalCursorBarrierSetDigest(
	cursor InboxCursor,
	barriers []InboxCursorBarrier,
) ([]byte, error) {
	ordered := append([]InboxCursorBarrier(nil), barriers...)
	for _, barrier := range ordered {
		if err := validateActiveBarrier(cursor, barrier); err != nil {
			return nil, err
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID.String() < ordered[j].ID.String() })
	for index := 1; index < len(ordered); index++ {
		if ordered[index-1].ID == ordered[index].ID {
			return nil, communicationError(ErrInvalidCommunicationModel,
				"duplicate active cursor barrier")
		}
	}
	raw, err := canonicalJSON(ordered)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func cursorFilterMatches(filter CursorFilter, envelope CursorImmutableEnvelope) bool {
	containsID := func(values []model.ID, value model.ID) bool {
		return len(values) == 0 || sort.Search(len(values), func(i int) bool {
			return values[i].String() >= value.String()
		}) < len(values) && values[sort.Search(len(values), func(i int) bool {
			return values[i].String() >= value.String()
		})] == value
	}
	containsKind := func(values []MessageKind, value MessageKind) bool {
		i := sort.Search(len(values), func(i int) bool { return values[i] >= value })
		return len(values) == 0 || (i < len(values) && values[i] == value)
	}
	containsUrgency := func(values []MessageUrgency, value MessageUrgency) bool {
		i := sort.Search(len(values), func(i int) bool { return values[i] >= value })
		return len(values) == 0 || (i < len(values) && values[i] == value)
	}
	return (filter.CarrierClass == "" || filter.CarrierClass == envelope.CarrierClass) &&
		(filter.MailboxKind == "" || filter.MailboxKind == envelope.MailboxKind) &&
		containsID(filter.ChannelIDs, envelope.ChannelID) &&
		containsID(filter.WorkItemIDs, envelope.WorkItemID) &&
		containsKind(filter.MessageKinds, envelope.Kind) && containsUrgency(filter.Urgencies, envelope.Urgency)
}

type cursorScanAssessment struct {
	Visibility CursorDeliveryVisibility
	Reason     string
	Facts      []store.AuthorizationFactRef
	Claims     []CommunicationClaimRef
	Fence      *CursorChannelFence
}

func cursorScanCoreWitness(
	input CursorAdvanceInput,
	entry CursorScanEntry,
) (*ReadWitness, error) {
	core := entry.Core
	if entry.ReadEvidence != nil {
		readCore := &entry.ReadEvidence.Core
		if core == nil {
			core = readCore
		} else if core.Principal != readCore.Principal ||
			!canonicalCommunicationValueEqual(*core, *readCore) {
			return nil, communicationError(ErrInvalidCommunicationModel,
				"cursor core witnesses disagree")
		}
	}
	if core == nil {
		return nil, nil
	}
	if err := ValidateReadWitness(*core); err != nil {
		return nil, err
	}
	if core.Entity.TenantID != input.Scope.TenantID ||
		core.Entity.WorkspaceID != input.Scope.WorkspaceID ||
		core.Entity.Kind != model.Kind("sessions.message_delivery") ||
		core.Entity.ID != entry.DeliveryID || core.Operation != CommunicationRead ||
		core.Principal != input.Principal.Principal {
		return nil, communicationError(ErrInvalidCommunicationModel,
			"cursor core witness does not name the exact Delivery")
	}
	return core, nil
}

func cursorObservedFacts(
	input CursorAdvanceInput,
	core ReadWitness,
) ([]store.AuthorizationFactRef, error) {
	facts := append([]store.AuthorizationFactRef(nil), core.Facts...)
	facts = append(facts, store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: model.ID(input.Scope.TenantID),
		Version: input.Principal.Recipient.DirectoryEpoch,
	})
	return canonicalAuthorizationFactUnion(facts)
}

func validateCursorCarrierCompleteness(
	input CursorAdvanceInput,
	entry CursorScanEntry,
	message Message,
	delivery MessageDelivery,
) error {
	if entry.ReadEvidence == nil || entry.CarrierSet == nil {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"cursor carrier completeness is unavailable")
	}
	carrierSet := *entry.CarrierSet
	if carrierSet.Scope != input.Scope || carrierSet.MessageID != message.ID ||
		carrierSet.DeliveryID != delivery.ID || carrierSet.DeliveryCount < 1 ||
		carrierSet.ObservedAt != input.DBNow || ValidateAuthorityEvidence(carrierSet.Evidence) != nil ||
		evidenceVerdict(carrierSet.Evidence) != VerdictClean {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"cursor carrier set is not a complete same-transaction witness")
	}
	if !validateCurrentAudienceSetWitness(entry.ReadEvidence.CurrentAudience) {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"cursor audience set is not a complete same-transaction witness")
	}
	current := entry.ReadEvidence.CurrentAudience.SetWitness
	if current.Scope != input.Scope || current.MessageID != message.ID ||
		current.DeliveryID != delivery.ID || current.Recipient != delivery.Recipient ||
		current.ObservedAt != input.DBNow {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"cursor audience graph does not name the exact carrier")
	}
	rowsByDelivery := make(map[model.ID][]MessageAudienceRecipient, len(current.Contributions))
	deliveryByRecipient := make(map[RecipientRef]model.ID, len(current.Contributions))
	for _, contribution := range current.Contributions {
		if prior, exists := deliveryByRecipient[contribution.Recipient]; exists && prior != contribution.MessageDeliveryID {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"cursor audience graph assigns multiple Deliveries to one recipient")
		}
		deliveryByRecipient[contribution.Recipient] = contribution.MessageDeliveryID
		rowsByDelivery[contribution.MessageDeliveryID] = append(
			rowsByDelivery[contribution.MessageDeliveryID], contribution,
		)
	}
	if int64(len(rowsByDelivery)) != carrierSet.DeliveryCount {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"cursor carrier count differs from the complete audience graph")
	}
	requiredDeliveryCount := int64(0)
	targetFound := false
	for deliveryID, rows := range rowsByDelivery {
		fold, err := FoldAudienceContributions(rows)
		if err != nil {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"cursor audience graph cannot be folded: %v", err)
		}
		if fold.Required {
			requiredDeliveryCount++
		}
		if deliveryID != delivery.ID {
			continue
		}
		targetFound = true
		if fold.DeliveryID != delivery.ID || fold.Recipient != delivery.Recipient ||
			rows[0].RecipientEpoch != delivery.RecipientEpoch || fold.Required != delivery.Required ||
			fold.WakePolicy != delivery.WakePolicy ||
			!canonicalCommunicationValueEqual(fold.RouteReasons, delivery.RouteReasons) {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"cursor Delivery differs from its complete audience fold")
		}
	}
	if !targetFound ||
		requiredDeliveryCount != entry.ReadEvidence.CarrierState.RequiredDeliveryCount {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"cursor required Delivery count differs from the complete audience graph")
	}
	return nil
}

func cursorDirectNoticeCarrierMatches(
	input CursorAdvanceInput,
	entry CursorScanEntry,
	message Message,
	delivery MessageDelivery,
) (bool, error) {
	carrierSet := *entry.CarrierSet
	current := entry.ReadEvidence.CurrentAudience.SetWitness
	requiredDeliveryCount := int64(0)
	if delivery.Required {
		requiredDeliveryCount = 1
	}
	if carrierSet.DeliveryCount != 1 ||
		entry.ReadEvidence.CarrierState.RequiredDeliveryCount != requiredDeliveryCount ||
		len(current.Audiences) != 1 ||
		len(current.Contributions) != 1 {
		return false, nil
	}
	audience := current.Audiences[0]
	contribution := current.Contributions[0]
	if message.Kind != MessageNotice || message.WorkItemID != "" || message.ThreadID != message.ID ||
		message.ReplyToID != "" || message.SupersedesID != "" || message.OriginEventID != "" ||
		message.AutomationDepth != 0 || len(message.LabelsJSON) != 0 || len(message.LabelsHash) != 0 ||
		message.ExpiresAt != nil || message.Payload.Encoding != PayloadPlainJSON ||
		len(message.Payload.PlainJSON) == 0 || message.Payload.Sealed != nil ||
		message.Payload.SealKeyVersion != "" || message.Payload.DigestKeyVersion != "" ||
		message.Sender.Kind != ActorUser || delivery.ExpiresAt != nil ||
		len(delivery.RouteReasons) != 1 || delivery.RouteReasons[0] != RouteReason("direct") ||
		delivery.Recipient != input.Cursor.Reader || delivery.Recipient.Kind != RecipientUser ||
		audience.MessageID != message.ID || audience.Ordinal != 1 || audience.RouteRuleID != "" ||
		audience.Selector.Kind != AudienceUser || audience.Selector.Ref != delivery.Recipient.Ref ||
		audience.Selector.Required != delivery.Required ||
		audience.Selector.WakePolicy != delivery.WakePolicy || audience.ResolvedCount != 1 ||
		contribution.MessageAudienceID != audience.ID ||
		contribution.MessageDeliveryID != delivery.ID || contribution.Recipient != delivery.Recipient ||
		contribution.RecipientEpoch != delivery.RecipientEpoch || contribution.Selector != audience.Selector ||
		contribution.Required != delivery.Required || contribution.WakePolicy != delivery.WakePolicy ||
		len(contribution.RouteReasons) != 1 || contribution.RouteReasons[0] != RouteReason("direct") ||
		contribution.CausalKind != CausalDirect || contribution.CausalRef != delivery.Recipient.Ref ||
		contribution.CausalFactKind != "" || contribution.CausalFactID != "" ||
		contribution.CausalFactVersion != 0 || contribution.ObservedSessionSID != "" ||
		contribution.ObservedClaimFence != 0 || contribution.OriginalSubscriber != nil ||
		contribution.SubscriptionID != "" || contribution.SubscriptionGeneration != 0 ||
		contribution.RouteRuleID != "" || contribution.RouteRuleGeneration != 0 {
		return false, nil
	}
	if audience.DirectoryEpoch != contribution.DirectoryEpoch ||
		audience.ChannelACLRevision != contribution.ChannelACLRevision ||
		audience.RouteRevision != contribution.RouteRevision ||
		audience.SubscriptionRevision != contribution.SubscriptionRevision {
		return false, communicationError(ErrCommunicationEvidenceUnknown,
			"cursor DirectNotice audience provenance diverges")
	}
	fold, err := FoldAudienceContributions(current.Contributions)
	if err != nil || fold.Recipient != delivery.Recipient || fold.Required != delivery.Required ||
		fold.WakePolicy != delivery.WakePolicy ||
		!canonicalCommunicationValueEqual(fold.RouteReasons, delivery.RouteReasons) {
		return false, communicationError(ErrCommunicationEvidenceUnknown,
			"cursor DirectNotice Delivery diverges from its audience fold")
	}
	return true, nil
}

func cursorUndeliverableWitnessMatches(
	delivery MessageDelivery,
	witness *store.DirectoryTombstoneWitness,
	currentDirectoryEpoch int64,
) bool {
	return witness != nil && validateUndeliverableWitness(delivery, *witness) == nil &&
		witness.TombstoneKind == delivery.RetirementTombstoneKind &&
		witness.TombstoneID == delivery.RetirementTombstoneID &&
		witness.TombstoneVersion == delivery.RetirementTombstoneVersion &&
		witness.RetirementEpoch == delivery.RetirementEpoch &&
		witness.RetirementEpoch <= currentDirectoryEpoch
}

func cursorCarrierSnapshotMatches(
	message Message,
	delivery MessageDelivery,
	snapshot ProtectedCarrierSnapshot,
) bool {
	return canonicalCommunicationValueEqual(message, snapshot.Message) &&
		canonicalCommunicationValueEqual(delivery, snapshot.Delivery)
}

func classifyCursorScanEntry(
	input CursorAdvanceInput,
	filter CursorFilter,
	entry CursorScanEntry,
) (cursorScanAssessment, error) {
	if entry.TenantID != input.Scope.TenantID || entry.WorkspaceID != input.Scope.WorkspaceID ||
		!validCanonicalCommunicationID(entry.DeliveryID) || entry.Sequence <= input.Cursor.LastSeenSeq ||
		entry.Sequence > input.RequestedSeq {
		return cursorScanAssessment{}, communicationError(ErrInvalidCommunicationModel,
			"invalid cursor scan entry")
	}
	core, err := cursorScanCoreWitness(input, entry)
	if err != nil {
		return cursorScanAssessment{}, err
	}
	if core == nil || !communicationEvidenceCurrent(core.ObservedAt, core.FreshUntil, input.DBNow) {
		return cursorScanAssessment{
			Visibility: CursorDeliveryEvidenceUnknown,
			Reason:     "cursor_core_evidence_unavailable",
		}, nil
	}
	facts, err := cursorObservedFacts(input, *core)
	if err != nil {
		return cursorScanAssessment{
			Visibility: CursorDeliveryEvidenceUnknown,
			Reason:     "cursor_authority_facts_unavailable",
		}, nil
	}
	switch core.Outcome {
	case ReadUnknown:
		return cursorScanAssessment{
			Visibility: CursorDeliveryEvidenceUnknown, Reason: core.Code,
		}, nil
	case ReadDeny:
		return cursorScanAssessment{
			Visibility: CursorDeliveryTemporarilyInvisible, Reason: core.Code, Facts: facts,
		}, nil
	}
	if entry.Delivery == nil || entry.Message == nil || entry.ReadEvidence == nil {
		return cursorScanAssessment{
			Visibility: CursorDeliveryEvidenceUnknown,
			Reason:     "cursor_carrier_evidence_unavailable",
		}, nil
	}
	delivery := *entry.Delivery
	if err := ValidateMessageDelivery(delivery); err != nil {
		return cursorScanAssessment{}, err
	}
	if delivery.TenantID != input.Scope.TenantID || delivery.WorkspaceID != input.Scope.WorkspaceID ||
		delivery.ID != entry.DeliveryID || delivery.DeliverySeq != entry.Sequence {
		return cursorScanAssessment{}, communicationError(ErrInvalidCommunicationModel,
			"cursor entry does not match exact Delivery lineage")
	}
	message := *entry.Message
	validationRequired := int64(0)
	if message.AckPolicy == AckPolicyEachRequired {
		validationRequired = 1
	} else if message.AckPolicy == AckPolicyQuorum {
		validationRequired = message.AckQuorum
	}
	if err := ValidateMessage(message, validationRequired); err != nil {
		return cursorScanAssessment{}, err
	}
	if err := ValidateMessageDeliveryLineage(message, delivery); err != nil {
		return cursorScanAssessment{}, err
	}
	if message.ID != delivery.MessageID || message.TenantID != input.Scope.TenantID ||
		message.WorkspaceID != input.Scope.WorkspaceID {
		return cursorScanAssessment{}, communicationError(ErrInvalidCommunicationModel,
			"cursor Message does not match exact Delivery lineage")
	}
	if delivery.Recipient != input.Cursor.Reader ||
		(input.Cursor.MailboxKind == MailboxChannel && message.ChannelID.String() != input.Cursor.MailboxRef) {
		return cursorScanAssessment{
			Visibility: CursorDeliveryEvidenceUnknown,
			Reason:     "cursor_mailbox_scan_crosses_recipient",
		}, nil
	}
	entity := entry.ReadEvidence.Core.Entity
	if entry.ReadEvidence.Principal != input.Principal.Principal ||
		entry.ReadEvidence.DBNow != input.DBNow ||
		entry.ReadEvidence.Recipient != input.Cursor.Reader ||
		entry.ReadEvidence.ChannelID != message.ChannelID ||
		entry.ReadEvidence.Carrier.MessageID != message.ID ||
		entry.ReadEvidence.Carrier.DeliveryID != delivery.ID ||
		entry.ReadEvidence.CurrentChannelGrant.ChannelID != message.ChannelID ||
		entry.ReadEvidence.DirectoryEpoch.Version != input.Principal.Recipient.DirectoryEpoch ||
		entity.TenantID != input.Scope.TenantID || entity.WorkspaceID != input.Scope.WorkspaceID ||
		entity.Kind != model.Kind("sessions.message_delivery") || entity.ID != delivery.ID {
		return cursorScanAssessment{}, communicationError(ErrInvalidCommunicationModel,
			"cursor read witness does not name exact Delivery")
	}
	if !cursorCarrierSnapshotMatches(message, delivery, entry.ReadEvidence.CarrierState) {
		return cursorScanAssessment{}, communicationError(ErrCommunicationEvidenceUnknown,
			"cursor carrier snapshot differs from the locked Message or Delivery")
	}
	decision, err := EvaluateProtectedRead(*entry.ReadEvidence)
	if err != nil {
		if errors.Is(err, ErrCommunicationEvidenceUnknown) || errors.Is(err, ErrCommunicationNotFound) ||
			errors.Is(err, ErrCommunicationForbidden) {
			return cursorScanAssessment{
				Visibility: CursorDeliveryEvidenceUnknown,
				Reason:     "cursor_read_evidence_unavailable",
			}, nil
		}
		return cursorScanAssessment{}, err
	}
	if decision.Verdict == VerdictUnknown {
		return cursorScanAssessment{
			Visibility: CursorDeliveryEvidenceUnknown, Reason: decision.Code,
		}, nil
	}
	for _, check := range decision.Checks {
		if evidenceVerdict(check.Evidence) == VerdictUnknown {
			return cursorScanAssessment{
				Visibility: CursorDeliveryEvidenceUnknown,
				Reason:     "cursor_read_evidence_unavailable",
			}, nil
		}
	}
	if len(decision.RequiredClaims) != 0 {
		return cursorScanAssessment{
			Visibility: CursorDeliveryEvidenceUnknown,
			Reason:     "direct_notice_claim_authority_unsupported",
		}, nil
	}
	facts = append(facts, decision.Facts...)
	fence := CursorChannelFence{
		ChannelID: entry.ReadEvidence.ChannelID, ACLRevision: entry.ReadEvidence.ChannelACLRevision,
		GrantID:        entry.ReadEvidence.CurrentChannelGrant.GrantID,
		GrantVersion:   entry.ReadEvidence.CurrentChannelGrant.GrantVersion,
		GrantVerdict:   evidenceVerdict(entry.ReadEvidence.CurrentChannelGrant.Evidence),
		DirectoryEpoch: entry.ReadEvidence.DirectoryEpoch.Version, EvaluatedAt: entry.ReadEvidence.DBNow,
	}
	if err := validateCursorCarrierCompleteness(input, entry, message, delivery); err != nil {
		return cursorScanAssessment{
			Visibility: CursorDeliveryEvidenceUnknown,
			Reason:     "cursor_carrier_completeness_unavailable",
		}, nil
	}
	directNotice, err := cursorDirectNoticeCarrierMatches(input, entry, message, delivery)
	if err != nil {
		return cursorScanAssessment{
			Visibility: CursorDeliveryEvidenceUnknown,
			Reason:     "cursor_carrier_graph_unavailable",
		}, nil
	}
	envelope := CursorImmutableEnvelope{
		MailboxKind: input.Cursor.MailboxKind, MessageID: message.ID, ChannelID: message.ChannelID,
		WorkItemID: message.WorkItemID, Kind: message.Kind, Urgency: message.Urgency,
	}
	if directNotice {
		envelope.CarrierClass = CursorCarrierDirectNoticeV1
	}
	if !cursorFilterMatches(filter, envelope) {
		return cursorScanAssessment{
			Visibility: CursorDeliveryForeignGap, Reason: "foreign_gap", Facts: facts, Fence: &fence,
		}, nil
	}
	if delivery.State == DeliveryUndeliverable {
		if !cursorUndeliverableWitnessMatches(
			delivery, entry.Tombstone, entry.ReadEvidence.DirectoryEpoch.Version,
		) {
			return cursorScanAssessment{
				Visibility: CursorDeliveryEvidenceUnknown,
				Reason:     "cursor_tombstone_evidence_unavailable",
			}, nil
		}
		return cursorScanAssessment{
			Visibility: CursorDeliveryUndeliverable, Reason: "definitively_undeliverable",
			Facts: facts, Fence: &fence,
		}, nil
	}
	if input.DBNow.Before(delivery.AvailableAt) {
		return cursorScanAssessment{
			Visibility: CursorDeliveryNotYetAvailable, Reason: "not_yet_available",
			Facts: facts, Claims: decision.RequiredClaims, Fence: &fence,
		}, nil
	}
	switch decision.Verdict {
	case VerdictClean:
		return cursorScanAssessment{
			Visibility: CursorDeliveryVisible, Reason: decision.Code, Facts: facts,
			Claims: decision.RequiredClaims, Fence: &fence,
		}, nil
	case VerdictBroken:
		return cursorScanAssessment{
			Visibility: CursorDeliveryTemporarilyInvisible, Reason: decision.Code,
			Facts: facts, Fence: &fence,
		}, nil
	default:
		return cursorScanAssessment{
			Visibility: CursorDeliveryEvidenceUnknown, Reason: decision.Code,
		}, nil
	}
}

func cloneInboxCursor(cursor InboxCursor) InboxCursor {
	cloned := cursor
	cloned.FilterHash = append([]byte(nil), cursor.FilterHash...)
	return cloned
}

func unknownCursorPlan(input CursorAdvanceInput, code string) CursorAdvancePlan {
	return CursorAdvancePlan{
		Before: cloneInboxCursor(input.Cursor), After: cloneInboxCursor(input.Cursor),
		Verdict: VerdictUnknown, Code: code, PriorSeq: input.Cursor.LastSeenSeq,
		RequestedSeq: input.RequestedSeq, EffectiveSeq: input.Cursor.LastSeenSeq,
	}
}

func unknownInitialCursorPlan(input InitialInboxCursorAdvanceInput, code string) CursorAdvancePlan {
	return CursorAdvancePlan{
		Verdict: VerdictUnknown, Code: code, PriorSeq: 0,
		RequestedSeq: input.RequestedSeq, EffectiveSeq: 0,
	}
}

func validateDirectNoticeCursorIdentity(cursor InboxCursor, filter CursorFilter) error {
	if cursor.Reader.Kind != RecipientUser || cursor.MailboxKind != MailboxPersonal ||
		cursor.MailboxRef != cursor.Reader.Ref || filter.CarrierClass != CursorCarrierDirectNoticeV1 ||
		filter.MailboxKind != MailboxPersonal {
		return communicationError(ErrInvalidCommunicationModel,
			"C2 requires the fixed personal User DirectNotice cursor identity")
	}
	return nil
}

// PlanCursorAdvance is retained as the historical pure-state entry point. New
// C2 code should call PlanInboxCursorAdvance, which names the durable resource.
func PlanCursorAdvance(input CursorAdvanceInput) (CursorAdvancePlan, error) {
	return PlanInboxCursorAdvance(input)
}

// PlanInboxCursorAdvance implements C2 for an existing durable cursor. It is
// invoked only by an explicit PUT; therefore regrant or elapsed schedule never
// clears a barrier in background.
func PlanInboxCursorAdvance(input CursorAdvanceInput) (CursorAdvancePlan, error) {
	if err := validateInboxCursor(input.Cursor); err != nil {
		return CursorAdvancePlan{}, err
	}
	if input.ExpectedVersion < 1 || input.ExpectedVersion != input.Cursor.Version {
		return CursorAdvancePlan{}, communicationError(ErrInvalidCommunicationTransition,
			"cursor version precondition does not match")
	}
	return planInboxCursorAdvance(input, false)
}

// PlanInitialInboxCursorAdvance plans the first explicit PUT against virtual
// v0. It consumes a server-generated UUID and an exact absence witness instead
// of fabricating a mutable pre-existing cursor.
func PlanInitialInboxCursorAdvance(
	input InitialInboxCursorAdvanceInput,
) (CursorAdvancePlan, error) {
	if err := input.Scope.Validate(); err != nil {
		return CursorAdvancePlan{}, err
	}
	if !validCanonicalCommunicationID(input.CursorID) || input.Reader.Validate() != nil ||
		input.Reader.Kind != RecipientUser || input.MailboxKind != MailboxPersonal ||
		input.MailboxRef != input.Reader.Ref || input.DBNow.IsZero() || input.RequestedSeq < 0 {
		return CursorAdvancePlan{}, communicationError(ErrInvalidCommunicationModel,
			"invalid virtual-v0 cursor input")
	}
	if input.ExpectedVersion != 0 {
		return CursorAdvancePlan{}, communicationError(ErrInvalidCommunicationTransition,
			"virtual cursor version precondition does not match")
	}
	canonicalFilter, filterHash, err := CanonicalCursorFilter(input.Filter)
	if err != nil {
		return CursorAdvancePlan{}, err
	}
	if canonicalFilter.CarrierClass != CursorCarrierDirectNoticeV1 ||
		canonicalFilter.MailboxKind != MailboxPersonal {
		return CursorAdvancePlan{}, communicationError(ErrInvalidCommunicationModel,
			"virtual-v0 cursor requires the fixed DirectNotice filter")
	}
	if err := validateInboxCursorAbsenceWitness(
		input.Absence, input.Scope, input.Reader, input.MailboxKind, input.MailboxRef,
		filterHash, input.DBNow,
	); err != nil {
		return unknownInitialCursorPlan(input, "cursor_absence_unavailable"), nil
	}
	cursor := InboxCursor{
		MutableCommunicationEntity: MutableCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: input.CursorID, TenantID: input.Scope.TenantID, WorkspaceID: input.Scope.WorkspaceID,
				Version: 1, CreatedAt: input.DBNow,
			},
			UpdatedAt: input.DBNow,
		},
		Reader: input.Reader, MailboxKind: input.MailboxKind, MailboxRef: input.MailboxRef,
		LastSeenSeq: 0, LastSeenAt: input.DBNow, FilterHash: append([]byte(nil), filterHash...),
	}
	if err := validateInboxCursor(cursor); err != nil {
		return CursorAdvancePlan{}, err
	}
	planInput := CursorAdvanceInput{
		Scope: input.Scope, Principal: input.Principal, Cursor: cursor, ExpectedVersion: 1,
		Filter: canonicalFilter, RequestedSeq: input.RequestedSeq, DBNow: input.DBNow,
		ScanWitness: input.ScanWitness, Scan: input.Scan,
	}
	plan, err := planInboxCursorAdvance(planInput, true)
	if err != nil {
		return CursorAdvancePlan{}, err
	}
	if plan.Verdict == VerdictUnknown {
		return unknownInitialCursorPlan(input, plan.Code), nil
	}
	return plan, nil
}

func planInboxCursorAdvance(
	input CursorAdvanceInput,
	initial bool,
) (CursorAdvancePlan, error) {
	if err := input.Scope.Validate(); err != nil {
		return CursorAdvancePlan{}, err
	}
	if input.Cursor.TenantID != input.Scope.TenantID || input.Cursor.WorkspaceID != input.Scope.WorkspaceID ||
		input.DBNow.IsZero() || input.DBNow.Before(input.Cursor.LastSeenAt) {
		return CursorAdvancePlan{}, communicationError(ErrInvalidCommunicationModel,
			"cursor does not match server scope or DB time")
	}
	if err := ValidatePrincipalResolution(input.Principal); err != nil {
		return CursorAdvancePlan{}, err
	}
	if input.Principal.Scope != input.Scope {
		return CursorAdvancePlan{}, communicationError(ErrInvalidCommunicationModel,
			"cursor principal resolution crosses server scope")
	}
	if !communicationEvidenceCurrent(input.Principal.ObservedAt, input.Principal.FreshUntil, input.DBNow) {
		return unknownCursorPlan(input, "cursor_principal_stale"), nil
	}
	if input.Principal.Outcome != PrincipalResolved {
		return unknownCursorPlan(input, "cursor_principal_unresolved"), nil
	}
	if input.Principal.Principal.SessionID != "" {
		return unknownCursorPlan(input, "direct_notice_claim_authority_unsupported"), nil
	}
	if input.Principal.Recipient == nil || input.Principal.Recipient.Recipient != input.Cursor.Reader {
		return CursorAdvancePlan{}, communicationError(ErrInvalidCommunicationModel,
			"cursor reader does not match resolved principal")
	}
	canonicalFilter, filterHash, err := CanonicalCursorFilter(input.Filter)
	if err != nil {
		return CursorAdvancePlan{}, err
	}
	if err := validateDirectNoticeCursorIdentity(input.Cursor, canonicalFilter); err != nil {
		return CursorAdvancePlan{}, err
	}
	if !bytes.Equal(filterHash, input.Cursor.FilterHash) {
		return CursorAdvancePlan{}, communicationError(ErrInvalidCommunicationModel,
			"cursor filter hash does not match immutable filter")
	}
	if input.RequestedSeq < input.Cursor.LastSeenSeq {
		return CursorAdvancePlan{}, communicationError(ErrInvalidCommunicationTransition, "cursor cannot rewind")
	}
	if err := validateCursorMailboxScanWitness(
		input.ScanWitness, input.Scan, input.Scope, input.Cursor, input.RequestedSeq, input.DBNow,
	); err != nil {
		return unknownCursorPlan(input, "cursor_scan_incomplete"), nil
	}
	if initial {
		if len(input.ActiveBarriers) != 0 {
			return CursorAdvancePlan{}, communicationError(ErrInvalidCommunicationModel,
				"virtual-v0 cursor cannot carry durable barriers")
		}
	} else {
		barrierDigest, digestErr := CanonicalCursorBarrierSetDigest(input.Cursor, input.ActiveBarriers)
		if digestErr != nil {
			return unknownCursorPlan(input, "cursor_barrier_set_unavailable"), nil
		}
		barrierWitness := input.BarrierSetWitness
		if barrierWitness.Scope != input.Scope || barrierWitness.CursorID != input.Cursor.ID ||
			barrierWitness.BarrierCount != int64(len(input.ActiveBarriers)) ||
			!bytes.Equal(barrierWitness.Digest, barrierDigest) || barrierWitness.ObservedAt != input.DBNow ||
			ValidateAuthorityEvidence(barrierWitness.Evidence) != nil ||
			evidenceVerdict(barrierWitness.Evidence) != VerdictClean {
			return unknownCursorPlan(input, "cursor_barrier_set_unavailable"), nil
		}
	}
	entries := make(map[model.ID]CursorScanEntry, len(input.Scan))
	visibility := make(map[model.ID]CursorDeliveryVisibility, len(input.Scan))
	reasons := make(map[model.ID]string, len(input.Scan))
	factsInput := []store.AuthorizationFactRef{{
		Kind: model.DirectoryEpochKind, ID: model.ID(input.Scope.TenantID),
		Version: input.Principal.Recipient.DirectoryEpoch,
	}}
	claims := make([]CommunicationClaimRef, 0)
	fenceByChannel := make(map[model.ID]CursorChannelFence)
	expire := make([]MessageDeliveryExpiryPlan, 0)
	evidenceUnknown := false
	for _, scannedEntry := range input.Scan {
		entry := scannedEntry
		assessment, classifyErr := classifyCursorScanEntry(input, canonicalFilter, entry)
		if classifyErr != nil {
			return unknownCursorPlan(input, "cursor_evidence_unavailable"), nil
		}
		if entry.Delivery != nil && entry.Delivery.State == DeliveryAvailable &&
			assessment.Visibility != CursorDeliveryForeignGap &&
			assessment.Visibility != CursorDeliveryEvidenceUnknown {
			deadlineElapsed := (entry.Delivery.AckDueAt != nil && !input.DBNow.Before(*entry.Delivery.AckDueAt)) ||
				(entry.Delivery.ExpiresAt != nil && !input.DBNow.Before(*entry.Delivery.ExpiresAt))
			if deadlineElapsed {
				expiry, expiryErr := PlanMessageDeliveryExpiry(*entry.Delivery, input.DBNow)
				if expiryErr != nil {
					return unknownCursorPlan(input, "cursor_expiry_evidence_unavailable"), nil
				}
				expire = append(expire, expiry)
			}
		}
		if _, duplicate := entries[entry.DeliveryID]; duplicate {
			return unknownCursorPlan(input, "cursor_scan_incomplete"), nil
		}
		entries[entry.DeliveryID] = entry
		visibility[entry.DeliveryID] = assessment.Visibility
		reasons[entry.DeliveryID] = assessment.Reason
		factsInput = append(factsInput, assessment.Facts...)
		claims = append(claims, assessment.Claims...)
		if assessment.Fence != nil {
			fence := *assessment.Fence
			if prior, present := fenceByChannel[fence.ChannelID]; present && prior != fence {
				return unknownCursorPlan(input, "cursor_channel_fence_inconsistent"), nil
			}
			fenceByChannel[fence.ChannelID] = fence
		}
		if assessment.Visibility == CursorDeliveryEvidenceUnknown {
			evidenceUnknown = true
		}
	}
	activeByDelivery := make(map[model.ID]InboxCursorBarrier, len(input.ActiveBarriers))
	remainingBarrierSeqs := make([]int64, 0, len(input.ActiveBarriers)+len(input.Scan))
	resolve := make([]CursorBarrierResolution, 0, len(input.ActiveBarriers))
	for _, barrier := range input.ActiveBarriers {
		if err := validateActiveBarrier(input.Cursor, barrier); err != nil {
			return unknownCursorPlan(input, "cursor_barrier_set_unavailable"), nil
		}
		if _, duplicate := activeByDelivery[barrier.DeliveryID]; duplicate {
			return unknownCursorPlan(input, "cursor_barrier_set_unavailable"), nil
		}
		activeByDelivery[barrier.DeliveryID] = barrier
		if barrier.BarrierSeq > input.RequestedSeq {
			continue
		}
		entry, present := entries[barrier.DeliveryID]
		if !present {
			return unknownCursorPlan(input, "barrier_evidence_unavailable"), nil
		}
		if entry.Sequence != barrier.BarrierSeq {
			return unknownCursorPlan(input, "barrier_sequence_mismatch"), nil
		}
		switch visibility[entry.DeliveryID] {
		case CursorDeliveryVisible, CursorDeliveryForeignGap, CursorDeliveryUndeliverable:
			resolve = append(resolve, CursorBarrierResolution{
				BarrierID: barrier.ID, DeliveryID: barrier.DeliveryID, BarrierSeq: barrier.BarrierSeq,
				ReasonCode: "visibility_revalidated",
			})
		case CursorDeliveryNotYetAvailable, CursorDeliveryTemporarilyInvisible:
			remainingBarrierSeqs = append(remainingBarrierSeqs, barrier.BarrierSeq)
		default:
			return unknownCursorPlan(input, "barrier_evidence_inconsistent"), nil
		}
	}
	if evidenceUnknown {
		return unknownCursorPlan(input, "cursor_evidence_unavailable"), nil
	}
	create := make([]CursorBarrierCreation, 0)
	for _, entry := range input.Scan {
		if _, exists := activeByDelivery[entry.DeliveryID]; exists {
			continue
		}
		var cause CursorBarrierCause
		switch visibility[entry.DeliveryID] {
		case CursorDeliveryNotYetAvailable:
			cause = BarrierNotYetAvailable
		case CursorDeliveryTemporarilyInvisible:
			cause = BarrierTemporarilyInvisible
		default:
			continue
		}
		reason := reasons[entry.DeliveryID]
		if reason == "" {
			reason = string(cause)
		}
		if !boundedToken(reason, 128) {
			return unknownCursorPlan(input, "cursor_barrier_reason_unavailable"), nil
		}
		create = append(create, CursorBarrierCreation{
			DeliveryID: entry.DeliveryID, BarrierSeq: entry.Sequence, Cause: cause, ReasonCode: reason,
		})
		remainingBarrierSeqs = append(remainingBarrierSeqs, entry.Sequence)
	}
	sort.Slice(create, func(i, j int) bool {
		if create[i].BarrierSeq != create[j].BarrierSeq {
			return create[i].BarrierSeq < create[j].BarrierSeq
		}
		return create[i].DeliveryID.String() < create[j].DeliveryID.String()
	})
	sort.Slice(resolve, func(i, j int) bool {
		if resolve[i].BarrierSeq != resolve[j].BarrierSeq {
			return resolve[i].BarrierSeq < resolve[j].BarrierSeq
		}
		return resolve[i].BarrierID.String() < resolve[j].BarrierID.String()
	})
	sort.Slice(expire, func(i, j int) bool {
		if expire[i].Before.DeliverySeq != expire[j].Before.DeliverySeq {
			return expire[i].Before.DeliverySeq < expire[j].Before.DeliverySeq
		}
		return expire[i].Before.ID.String() < expire[j].Before.ID.String()
	})
	effective := input.RequestedSeq
	for _, barrierSeq := range remainingBarrierSeqs {
		if candidate := barrierSeq - 1; candidate < effective {
			effective = candidate
		}
	}
	if effective < input.Cursor.LastSeenSeq {
		effective = input.Cursor.LastSeenSeq
	}
	facts, factErr := canonicalAuthorizationFactUnion(factsInput)
	if factErr != nil {
		return unknownCursorPlan(input, "cursor_authority_facts_unavailable"), nil
	}
	claims = canonicalCommunicationClaims(claims)
	if len(claims) != 0 {
		return unknownCursorPlan(input, "direct_notice_claim_authority_unsupported"), nil
	}
	fences := make([]CursorChannelFence, 0, len(fenceByChannel))
	for _, fence := range fenceByChannel {
		fences = append(fences, fence)
	}
	sort.Slice(fences, func(i, j int) bool { return fences[i].ChannelID.String() < fences[j].ChannelID.String() })
	changed := initial || effective != input.Cursor.LastSeenSeq || len(expire) != 0 ||
		len(create) != 0 || len(resolve) != 0
	after := cloneInboxCursor(input.Cursor)
	code := "cursor_unchanged"
	if initial {
		code = "cursor_created"
		after.LastSeenSeq = effective
	} else if changed {
		code = "cursor_advanced"
		after.Version++
		after.UpdatedAt = input.DBNow
		after.LastSeenAt = input.DBNow
		after.LastSeenSeq = effective
	}
	if err := validateInboxCursor(after); err != nil {
		return CursorAdvancePlan{}, err
	}
	before := cloneInboxCursor(input.Cursor)
	if initial {
		before = InboxCursor{}
	}
	return CursorAdvancePlan{
		Before: before, After: after, Verdict: VerdictClean, Code: code,
		CreateCursor: initial, Changed: changed, PriorSeq: input.Cursor.LastSeenSeq,
		RequestedSeq: input.RequestedSeq, EffectiveSeq: effective, Expire: expire,
		Create: create, Resolve: resolve, Facts: facts, RequiredClaims: nil, ChannelFences: fences,
	}, nil
}

func ValidateDispatchRouteIdentity(identity DispatchRouteIdentity) error {
	if !validCanonicalCommunicationID(identity.EndpointID) || identity.EndpointGeneration < 1 ||
		identity.PolicyGeneration < 1 {
		return communicationError(ErrInvalidCommunicationModel, "invalid dispatch route identity")
	}
	if (identity.RouteRuleID == "") != (identity.RouteRuleGeneration == 0) ||
		(identity.RouteRuleID != "" && (!validCanonicalCommunicationID(identity.RouteRuleID) ||
			identity.RouteRuleGeneration < 1)) {
		return communicationError(ErrInvalidCommunicationModel, "invalid dispatch route rule identity")
	}
	return nil
}

func (identity DispatchRouteIdentity) Equal(other DispatchRouteIdentity) bool {
	return identity.EndpointID == other.EndpointID &&
		identity.EndpointGeneration == other.EndpointGeneration &&
		identity.RouteRuleID == other.RouteRuleID &&
		identity.RouteRuleGeneration == other.RouteRuleGeneration &&
		identity.PolicyGeneration == other.PolicyGeneration
}

func dispatchRouteIdentity(dispatch DeliveryDispatch) DispatchRouteIdentity {
	return DispatchRouteIdentity{
		EndpointID: dispatch.EndpointID, EndpointGeneration: dispatch.EndpointGeneration,
		RouteRuleID: dispatch.RouteRuleID, RouteRuleGeneration: dispatch.RouteRuleGeneration,
		PolicyGeneration: dispatch.PolicyGeneration,
	}
}

type dispatchProviderAcceptanceHashInput struct {
	DispatchID         model.ID          `json:"dispatch_id"`
	AttemptID          model.ID          `json:"attempt_id"`
	EndpointID         model.ID          `json:"endpoint_id"`
	EndpointGeneration int64             `json:"endpoint_generation"`
	ObservedAt         time.Time         `json:"observed_at"`
	Acceptance         AuthorityEvidence `json:"acceptance"`
}

func CanonicalDispatchProviderAcceptanceHash(
	witness DispatchProviderAcceptanceWitness,
) ([]byte, error) {
	if !validCanonicalCommunicationID(witness.DispatchID) ||
		!validCanonicalCommunicationID(witness.AttemptID) ||
		!validCanonicalCommunicationID(witness.EndpointID) || witness.EndpointGeneration < 1 ||
		witness.ObservedAt.IsZero() || ValidateAuthorityEvidence(witness.Acceptance) != nil {
		return nil, communicationError(ErrInvalidCommunicationModel,
			"invalid provider acceptance witness")
	}
	raw, err := canonicalJSON(dispatchProviderAcceptanceHashInput(witness))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func dispatchHasReconciliation(dispatch DeliveryDispatch) bool {
	return dispatch.ReconciledAttemptID != "" || dispatch.ReconciledEndpointID != "" ||
		dispatch.ReconciledEndpointGeneration != 0 || dispatch.ReconciliationVerdict != "" ||
		dispatch.ReconciliationCode != "" || dispatch.ReconciliationEvidenceRef != "" ||
		dispatch.ReconciliationObservedAt != nil || len(dispatch.ProviderAcceptanceHash) != 0
}

func validateDurableDispatchReconciliation(dispatch DeliveryDispatch) error {
	if !dispatchHasReconciliation(dispatch) {
		if dispatch.State == DispatchSuperseded && dispatch.LastVerdict == VerdictUnknown {
			return communicationError(ErrInvalidCommunicationModel,
				"UNKNOWN superseded dispatch lacks durable no-acceptance reconciliation")
		}
		if dispatch.State == DispatchDeadLetter && dispatch.LastVerdict == VerdictUnknown {
			return communicationError(ErrInvalidCommunicationModel,
				"UNKNOWN dead-letter dispatch lacks durable reconciliation")
		}
		return nil
	}
	if !validCanonicalCommunicationID(dispatch.ReconciledAttemptID) ||
		dispatch.ReconciledEndpointID != dispatch.EndpointID ||
		dispatch.ReconciledEndpointGeneration != dispatch.EndpointGeneration ||
		!validAssessmentVerdict(dispatch.ReconciliationVerdict) ||
		!boundedToken(dispatch.ReconciliationCode, 128) ||
		!validateOpaqueRef(dispatch.ReconciliationEvidenceRef) ||
		dispatch.ReconciliationObservedAt == nil ||
		dispatch.ReconciliationObservedAt.Before(dispatch.CreatedAt) ||
		dispatch.ReconciliationObservedAt.After(dispatch.UpdatedAt) ||
		len(dispatch.ProviderAcceptanceHash) != sha256.Size || dispatch.AttemptCount != 1 ||
		!oneOf(dispatch.State, DispatchSucceeded, DispatchFailed, DispatchDeadLetter, DispatchSuperseded) {
		return communicationError(ErrInvalidCommunicationModel,
			"invalid durable dispatch reconciliation binding")
	}
	if dispatch.State == DispatchSuperseded && dispatch.LastVerdict == VerdictUnknown &&
		dispatch.ReconciliationVerdict != VerdictBroken {
		return communicationError(ErrInvalidCommunicationModel,
			"UNKNOWN successor lacks proven provider non-acceptance")
	}
	if dispatch.State == DispatchDeadLetter && dispatch.LastVerdict == VerdictUnknown &&
		dispatch.ReconciliationVerdict != VerdictUnknown {
		return communicationError(ErrInvalidCommunicationModel,
			"UNKNOWN dead-letter carries conclusive reconciliation")
	}
	return nil
}

func ValidateDeliveryDispatch(dispatch DeliveryDispatch) error {
	if err := validateMutableCommunicationEntity(dispatch.MutableCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(dispatch.DeliveryID) ||
		!validCanonicalCommunicationID(dispatch.RootDispatchID) ||
		!validCanonicalCommunicationID(dispatch.EndpointID) || dispatch.EndpointGeneration < 1 ||
		dispatch.DispatchGeneration < 1 || dispatch.RerouteRung < 0 || dispatch.PolicyGeneration < 1 ||
		!dispatch.State.Valid() || dispatch.AttemptCount < 0 || dispatch.AttemptCount > 1 ||
		len(dispatch.IdempotencyKeyHash) != sha256.Size {
		return communicationError(ErrInvalidCommunicationModel, "invalid DeliveryDispatch envelope")
	}
	if err := ValidateDispatchRouteIdentity(dispatchRouteIdentity(dispatch)); err != nil {
		return err
	}
	if err := validateDurableDispatchReconciliation(dispatch); err != nil {
		return err
	}
	if dispatch.DispatchGeneration == 1 {
		if dispatch.ID != dispatch.RootDispatchID || dispatch.PredecessorID != "" || dispatch.RerouteRung != 0 {
			return communicationError(ErrInvalidCommunicationModel, "invalid initial dispatch lineage")
		}
	} else if !validCanonicalCommunicationID(dispatch.PredecessorID) || dispatch.ID == dispatch.RootDispatchID {
		return communicationError(ErrInvalidCommunicationModel, "invalid successor dispatch lineage")
	}
	if oneOf(dispatch.State, DispatchFailed, DispatchUnknown) &&
		(dispatch.ResolutionDeadlineAt == nil || dispatch.ResolutionDeadlineAt.IsZero() ||
			!dispatch.ResolutionDeadlineAt.After(dispatch.CreatedAt)) {
		return communicationError(ErrInvalidCommunicationModel, "unsettled dispatch lacks resolution deadline")
	}
	if oneOf(dispatch.State, DispatchSucceeded, DispatchDeadLetter, DispatchSuperseded) && dispatch.SettledAt == nil {
		return communicationError(ErrInvalidCommunicationModel, "terminal dispatch lacks settlement")
	}
	if !oneOf(dispatch.State, DispatchSucceeded, DispatchDeadLetter, DispatchSuperseded) &&
		dispatch.SettledAt != nil {
		return communicationError(ErrInvalidCommunicationModel, "non-terminal dispatch carries settlement")
	}
	if dispatch.SettledAt != nil && dispatch.SettledAt.Before(dispatch.CreatedAt) {
		return communicationError(ErrInvalidCommunicationModel, "dispatch settlement precedes creation")
	}
	claimed := dispatch.ClaimOwner != "" || dispatch.ClaimUntil != nil
	if claimed != (boundedToken(dispatch.ClaimOwner, 128) && dispatch.ClaimUntil != nil) ||
		(dispatch.ClaimUntil != nil && !dispatch.ClaimUntil.After(dispatch.CreatedAt)) {
		return communicationError(ErrInvalidCommunicationModel, "invalid dispatch claim shape")
	}
	if dispatch.NextAttemptAt != nil &&
		(dispatch.State != DispatchPending || dispatch.NextAttemptAt.Before(dispatch.UpdatedAt)) {
		return communicationError(ErrInvalidCommunicationModel, "invalid dispatch next-attempt schedule")
	}
	switch dispatch.State {
	case DispatchPending:
		if dispatch.AttemptCount != 0 || claimed || dispatch.LastVerdict != "" || dispatch.LastCode != "" ||
			dispatch.ResolutionDeadlineAt != nil || dispatch.ResolutionCode != "" || dispatch.SettledAt != nil {
			return communicationError(ErrInvalidCommunicationModel, "pending dispatch carries attempt result")
		}
	case DispatchInFlight:
		if dispatch.AttemptCount != 1 || !claimed || dispatch.LastVerdict != "" || dispatch.LastCode != "" ||
			dispatch.ResolutionDeadlineAt != nil || dispatch.ResolutionCode != "" || dispatch.SettledAt != nil {
			return communicationError(ErrInvalidCommunicationModel, "in-flight dispatch lacks exact claim")
		}
	case DispatchSucceeded:
		if dispatch.AttemptCount != 1 || claimed || dispatch.LastVerdict != VerdictClean ||
			!boundedToken(dispatch.LastCode, 128) || dispatch.ResolutionDeadlineAt != nil ||
			!boundedToken(dispatch.ResolutionCode, 128) {
			return communicationError(ErrInvalidCommunicationModel, "succeeded dispatch lacks acceptance evidence")
		}
	case DispatchFailed:
		if dispatch.AttemptCount != 1 || claimed || dispatch.LastVerdict != VerdictBroken ||
			!boundedToken(dispatch.LastCode, 128) || !boundedToken(dispatch.ResolutionCode, 128) ||
			dispatch.ResolutionDeadlineAt == nil || !dispatch.ResolutionDeadlineAt.After(dispatch.UpdatedAt) {
			return communicationError(ErrInvalidCommunicationModel, "failed dispatch lacks retry boundary")
		}
	case DispatchUnknown:
		if dispatch.AttemptCount != 1 || claimed || dispatch.LastVerdict != VerdictUnknown ||
			!boundedToken(dispatch.LastCode, 128) || !boundedToken(dispatch.ResolutionCode, 128) ||
			dispatch.ResolutionDeadlineAt == nil || !dispatch.ResolutionDeadlineAt.After(dispatch.UpdatedAt) {
			return communicationError(ErrInvalidCommunicationModel, "unknown dispatch lacks reconciliation boundary")
		}
	case DispatchDeadLetter:
		if dispatch.AttemptCount != 1 || claimed ||
			!oneOf(dispatch.LastVerdict, VerdictBroken, VerdictUnknown) ||
			!boundedToken(dispatch.LastCode, 128) || !boundedToken(dispatch.ResolutionCode, 128) ||
			dispatch.ResolutionDeadlineAt != nil {
			return communicationError(ErrInvalidCommunicationModel, "dead-letter dispatch lacks terminal evidence")
		}
	case DispatchSuperseded:
		if dispatch.AttemptCount != 1 || claimed ||
			!oneOf(dispatch.LastVerdict, VerdictBroken, VerdictUnknown) ||
			!boundedToken(dispatch.LastCode, 128) || !boundedToken(dispatch.ResolutionCode, 128) ||
			dispatch.ResolutionDeadlineAt != nil {
			return communicationError(ErrInvalidCommunicationModel, "superseded dispatch loses predecessor result")
		}
	}
	return nil
}

func ValidateDispatchResolutionDeadline(dispatch DeliveryDispatch, dbNow time.Time) error {
	if err := ValidateDeliveryDispatch(dispatch); err != nil {
		return err
	}
	if dbNow.IsZero() {
		return communicationError(ErrCommunicationEvidenceUnknown, "DB time unavailable")
	}
	if oneOf(dispatch.State, DispatchFailed, DispatchUnknown) &&
		!dbNow.Before(*dispatch.ResolutionDeadlineAt) {
		return communicationError(ErrInvalidCommunicationModel,
			"dispatch deadline elapsed without settlement")
	}
	return nil
}

func ValidateDeliveryAttempt(attempt DeliveryAttempt) error {
	if err := validateMutableCommunicationEntity(attempt.MutableCommunicationEntity); err != nil {
		return err
	}
	if !validCanonicalCommunicationID(attempt.DispatchID) || attempt.AttemptSeq != 1 ||
		!attempt.State.Valid() || !attempt.TransmitBoundary.Valid() || attempt.StartedAt.IsZero() ||
		attempt.StartedAt.Before(attempt.CreatedAt) || len(attempt.RequestHash) != sha256.Size ||
		(len(attempt.ProviderReceiptHash) != 0 && len(attempt.ProviderReceiptHash) != sha256.Size) {
		return communicationError(ErrInvalidCommunicationModel, "invalid DeliveryAttempt envelope")
	}
	if attempt.State == AttemptReserved {
		if attempt.TransmitBoundary != TransmitUnknown || attempt.FinishedAt != nil || attempt.Verdict != "" ||
			attempt.Code != "" || len(attempt.ProviderReceiptHash) != 0 {
			return communicationError(ErrInvalidCommunicationModel, "reserved Attempt carries settlement")
		}
		return nil
	}
	if attempt.FinishedAt == nil || attempt.FinishedAt.Before(attempt.StartedAt) ||
		attempt.FinishedAt.After(attempt.UpdatedAt) ||
		!validAssessmentVerdict(attempt.Verdict) || !boundedToken(attempt.Code, 128) {
		return communicationError(ErrInvalidCommunicationModel, "settled Attempt lacks single-assignment evidence")
	}
	if attempt.State == AttemptAbandoned && (attempt.TransmitBoundary != TransmitUnknown ||
		attempt.Verdict != VerdictUnknown || len(attempt.ProviderReceiptHash) != 0) {
		return communicationError(ErrInvalidCommunicationModel,
			"abandoned Attempt fabricates transmit or provider evidence")
	}
	if attempt.State == AttemptFinished {
		shapeOK := (attempt.TransmitBoundary == TransmitCrossed && attempt.Verdict == VerdictClean &&
			len(attempt.ProviderReceiptHash) == sha256.Size) ||
			(attempt.TransmitBoundary == TransmitNotCrossed && attempt.Verdict == VerdictBroken &&
				len(attempt.ProviderReceiptHash) == 0) ||
			(attempt.TransmitBoundary == TransmitUnknown && attempt.Verdict == VerdictUnknown &&
				len(attempt.ProviderReceiptHash) == 0)
		if !shapeOK {
			return communicationError(ErrInvalidCommunicationModel,
				"finished Attempt result contradicts transmit boundary")
		}
	}
	return nil
}

// ValidateDispatchLineage checks the pure no-fork/monotonic shape of complete
// roots. Database uniqueness and atomic write enforcement remain Slice F/WP-3.
func ValidateDispatchLineage(dispatches []DeliveryDispatch) error {
	byID := make(map[model.ID]DeliveryDispatch, len(dispatches))
	generations := make(map[string]struct{}, len(dispatches))
	hashes := make(map[string]struct{}, len(dispatches))
	children := make(map[model.ID]int, len(dispatches))
	active := make(map[model.ID]int, len(dispatches))
	initial := make(map[model.ID]int, len(dispatches))
	for _, dispatch := range dispatches {
		if err := ValidateDeliveryDispatch(dispatch); err != nil {
			return err
		}
		if _, duplicate := byID[dispatch.ID]; duplicate {
			return communicationError(ErrInvalidCommunicationModel, "duplicate dispatch ID")
		}
		byID[dispatch.ID] = dispatch
		generationKey := dispatch.RootDispatchID.String() + "\x00" + fmt.Sprint(dispatch.DispatchGeneration)
		if _, duplicate := generations[generationKey]; duplicate {
			return communicationError(ErrInvalidCommunicationModel, "duplicate dispatch root generation")
		}
		generations[generationKey] = struct{}{}
		hashKey := dispatch.RootDispatchID.String() + "\x00" + string(dispatch.IdempotencyKeyHash)
		if _, duplicate := hashes[hashKey]; duplicate {
			return communicationError(ErrInvalidCommunicationModel, "dispatch lineage reuses idempotency hash")
		}
		hashes[hashKey] = struct{}{}
		if dispatch.PredecessorID != "" {
			children[dispatch.PredecessorID]++
			if children[dispatch.PredecessorID] > 1 {
				return communicationError(ErrInvalidCommunicationModel, "dispatch lineage fork")
			}
		}
		if oneOf(dispatch.State, DispatchPending, DispatchInFlight) {
			active[dispatch.RootDispatchID]++
			if active[dispatch.RootDispatchID] > 1 {
				return communicationError(ErrInvalidCommunicationModel, "multiple current dispatches per root")
			}
		}
		if dispatch.DispatchGeneration == 1 {
			initial[dispatch.RootDispatchID]++
		}
	}
	for root, count := range initial {
		if count != 1 {
			return communicationError(ErrInvalidCommunicationModel, "dispatch root has %d initial rows", count)
		}
		if _, exists := byID[root]; !exists {
			return communicationError(ErrInvalidCommunicationModel, "dispatch root row missing")
		}
	}
	for _, dispatch := range dispatches {
		childCount := children[dispatch.ID]
		if (dispatch.State == DispatchSuperseded) != (childCount == 1) {
			return communicationError(ErrInvalidCommunicationModel,
				"dispatch supersession is not paired with exactly one successor")
		}
	}
	for _, dispatch := range dispatches {
		if dispatch.DispatchGeneration == 1 {
			continue
		}
		predecessor, exists := byID[dispatch.PredecessorID]
		if !exists || predecessor.RootDispatchID != dispatch.RootDispatchID ||
			predecessor.DispatchGeneration+1 != dispatch.DispatchGeneration ||
			predecessor.TenantID != dispatch.TenantID || predecessor.WorkspaceID != dispatch.WorkspaceID ||
			predecessor.DeliveryID != dispatch.DeliveryID || predecessor.State != DispatchSuperseded ||
			dispatch.CreatedAt != predecessor.UpdatedAt || predecessor.SettledAt == nil ||
			dispatch.CreatedAt != *predecessor.SettledAt ||
			dispatch.RerouteRung < predecessor.RerouteRung ||
			dispatch.RerouteRung > predecessor.RerouteRung+1 {
			return communicationError(ErrInvalidCommunicationModel, "invalid dispatch predecessor lineage")
		}
		if dispatch.RerouteRung == predecessor.RerouteRung &&
			!dispatchRouteIdentity(dispatch).Equal(dispatchRouteIdentity(predecessor)) {
			return communicationError(ErrInvalidCommunicationModel,
				"dispatch route identity changed without reroute rung")
		}
	}
	return nil
}

// ValidateDispatchAttempts binds the one single-assignment external invocation
// admitted by each immutable dispatch generation.
func ValidateDispatchAttempts(
	dispatches []DeliveryDispatch,
	attempts []DeliveryAttempt,
	reconciliations []DispatchProviderAcceptanceWitness,
) error {
	byID := make(map[model.ID]DeliveryDispatch, len(dispatches))
	for _, dispatch := range dispatches {
		if err := ValidateDeliveryDispatch(dispatch); err != nil {
			return err
		}
		if _, duplicate := byID[dispatch.ID]; duplicate {
			return communicationError(ErrInvalidCommunicationModel, "duplicate dispatch ID")
		}
		byID[dispatch.ID] = dispatch
	}
	counts := make(map[model.ID]int64, len(attempts))
	byDispatch := make(map[model.ID]DeliveryAttempt, len(attempts))
	attemptIDs := make(map[model.ID]struct{}, len(attempts))
	for _, attempt := range attempts {
		if err := ValidateDeliveryAttempt(attempt); err != nil {
			return err
		}
		if _, duplicate := attemptIDs[attempt.ID]; duplicate {
			return communicationError(ErrInvalidCommunicationModel, "duplicate DeliveryAttempt ID")
		}
		attemptIDs[attempt.ID] = struct{}{}
		dispatch, present := byID[attempt.DispatchID]
		if !present || attempt.TenantID != dispatch.TenantID || attempt.WorkspaceID != dispatch.WorkspaceID ||
			attempt.AttemptSeq != 1 {
			return communicationError(ErrInvalidCommunicationModel,
				"DeliveryAttempt crosses dispatch generation")
		}
		counts[attempt.DispatchID]++
		if counts[attempt.DispatchID] > 1 {
			return communicationError(ErrInvalidCommunicationModel,
				"dispatch generation has multiple external invocations")
		}
		byDispatch[attempt.DispatchID] = attempt
	}
	reconciledByDispatch := make(map[model.ID]DispatchProviderAcceptanceWitness, len(reconciliations))
	for _, witness := range reconciliations {
		dispatch, present := byID[witness.DispatchID]
		attempt, attemptPresent := byDispatch[witness.DispatchID]
		witnessHash, hashErr := CanonicalDispatchProviderAcceptanceHash(witness)
		if !present || !attemptPresent || witness.AttemptID != attempt.ID ||
			witness.EndpointID != dispatch.EndpointID ||
			witness.EndpointGeneration != dispatch.EndpointGeneration || witness.ObservedAt.IsZero() ||
			witness.ObservedAt.After(dispatch.UpdatedAt) ||
			ValidateAuthorityEvidence(witness.Acceptance) != nil || hashErr != nil ||
			dispatch.ReconciledAttemptID != witness.AttemptID ||
			dispatch.ReconciledEndpointID != witness.EndpointID ||
			dispatch.ReconciledEndpointGeneration != witness.EndpointGeneration ||
			dispatch.ReconciliationVerdict != witness.Acceptance.Verdict ||
			dispatch.ReconciliationCode != witness.Acceptance.Code ||
			dispatch.ReconciliationEvidenceRef != witness.Acceptance.EvidenceRef ||
			dispatch.ReconciliationObservedAt == nil ||
			*dispatch.ReconciliationObservedAt != witness.ObservedAt ||
			!bytes.Equal(dispatch.ProviderAcceptanceHash, witnessHash) {
			return communicationError(ErrInvalidCommunicationModel,
				"reconciliation witness crosses Dispatch/Attempt lineage")
		}
		if _, duplicate := reconciledByDispatch[witness.DispatchID]; duplicate {
			return communicationError(ErrInvalidCommunicationModel, "duplicate dispatch reconciliation witness")
		}
		reconciledByDispatch[witness.DispatchID] = witness
	}
	for _, dispatch := range dispatches {
		if dispatch.AttemptCount != counts[dispatch.ID] {
			return communicationError(ErrInvalidCommunicationModel,
				"dispatch AttemptCount does not match single-assignment evidence")
		}
		witness, hasWitness := reconciledByDispatch[dispatch.ID]
		if dispatchHasReconciliation(dispatch) != hasWitness {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"durable dispatch reconciliation lacks its exact provider witness")
		}
		if dispatch.AttemptCount == 1 &&
			!dispatchAttemptStateCompatible(dispatch, byDispatch[dispatch.ID], witness, hasWitness) {
			return communicationError(ErrInvalidCommunicationModel,
				"dispatch state contradicts its single-assignment Attempt")
		}
	}
	return nil
}

func durableDispatchReconciliationMatches(
	dispatch DeliveryDispatch,
	attempt DeliveryAttempt,
	verdict AssessmentVerdict,
) bool {
	return dispatchHasReconciliation(dispatch) && dispatch.ReconciledAttemptID == attempt.ID &&
		dispatch.ReconciledEndpointID == dispatch.EndpointID &&
		dispatch.ReconciledEndpointGeneration == dispatch.EndpointGeneration &&
		dispatch.ReconciliationVerdict == verdict && len(dispatch.ProviderAcceptanceHash) == sha256.Size
}

func dispatchAttemptStateCompatible(
	dispatch DeliveryDispatch,
	attempt DeliveryAttempt,
	reconciliation DispatchProviderAcceptanceWitness,
	hasReconciliation bool,
) bool {
	if attempt.DispatchID != dispatch.ID || attempt.AttemptSeq != 1 {
		return false
	}
	switch dispatch.State {
	case DispatchInFlight:
		return attempt.State == AttemptReserved
	case DispatchSucceeded:
		return (attempt.State == AttemptFinished && attempt.TransmitBoundary == TransmitCrossed &&
			attempt.Verdict == VerdictClean && dispatch.LastVerdict == attempt.Verdict) ||
			(attempt.TransmitBoundary == TransmitUnknown && attempt.Verdict == VerdictUnknown &&
				durableDispatchReconciliationMatches(dispatch, attempt, VerdictClean) &&
				(!hasReconciliation || evidenceVerdict(reconciliation.Acceptance) == VerdictClean))
	case DispatchFailed:
		return (attempt.State == AttemptFinished && attempt.TransmitBoundary == TransmitNotCrossed &&
			attempt.Verdict == VerdictBroken && dispatch.LastVerdict == attempt.Verdict) ||
			(attempt.TransmitBoundary == TransmitUnknown && attempt.Verdict == VerdictUnknown &&
				durableDispatchReconciliationMatches(dispatch, attempt, VerdictBroken) &&
				(!hasReconciliation || evidenceVerdict(reconciliation.Acceptance) == VerdictBroken))
	case DispatchUnknown:
		return oneOf(attempt.State, AttemptFinished, AttemptAbandoned) &&
			attempt.TransmitBoundary == TransmitUnknown && attempt.Verdict == VerdictUnknown &&
			dispatch.LastVerdict == attempt.Verdict
	case DispatchDeadLetter:
		if attempt.Verdict == VerdictUnknown {
			return durableDispatchReconciliationMatches(dispatch, attempt, VerdictUnknown) &&
				(!hasReconciliation || evidenceVerdict(reconciliation.Acceptance) == VerdictUnknown)
		}
		return oneOf(attempt.State, AttemptFinished, AttemptAbandoned) &&
			dispatch.LastVerdict == attempt.Verdict
	case DispatchSuperseded:
		if attempt.TransmitBoundary == TransmitUnknown && attempt.Verdict == VerdictUnknown {
			return durableDispatchReconciliationMatches(dispatch, attempt, VerdictBroken) &&
				(!hasReconciliation || (ValidateAuthorityEvidence(reconciliation.Acceptance) == nil &&
					evidenceVerdict(reconciliation.Acceptance) == VerdictBroken))
		}
		return attempt.State == AttemptFinished && attempt.TransmitBoundary == TransmitNotCrossed &&
			attempt.Verdict == VerdictBroken && dispatch.LastVerdict == VerdictBroken
	default:
		return false
	}
}

func NewInitialDeliveryDispatch(
	entity MutableCommunicationEntity,
	deliveryID model.ID,
	route DispatchRouteIdentity,
	idempotencyKeyHash []byte,
) (DeliveryDispatch, error) {
	if err := validateMutableCommunicationEntity(entity); err != nil {
		return DeliveryDispatch{}, err
	}
	if err := ValidateDispatchRouteIdentity(route); err != nil {
		return DeliveryDispatch{}, err
	}
	if !validCanonicalCommunicationID(deliveryID) || len(idempotencyKeyHash) != sha256.Size {
		return DeliveryDispatch{}, communicationError(ErrInvalidCommunicationModel, "invalid initial dispatch input")
	}
	dispatch := DeliveryDispatch{
		MutableCommunicationEntity: entity, DeliveryID: deliveryID, RootDispatchID: entity.ID,
		EndpointID: route.EndpointID, EndpointGeneration: route.EndpointGeneration,
		RouteRuleID: route.RouteRuleID, RouteRuleGeneration: route.RouteRuleGeneration,
		DispatchGeneration: 1, RerouteRung: 0, PolicyGeneration: route.PolicyGeneration,
		State: DispatchPending, IdempotencyKeyHash: append([]byte(nil), idempotencyKeyHash...),
	}
	if err := ValidateDeliveryDispatch(dispatch); err != nil {
		return DeliveryDispatch{}, err
	}
	return dispatch, nil
}

type DispatchAttemptClaimPlan struct {
	DispatchBefore DeliveryDispatch `json:"dispatch_before"`
	DispatchAfter  DeliveryDispatch `json:"dispatch_after"`
	Attempt        DeliveryAttempt  `json:"attempt"`
}

// PlanDispatchAttemptClaim reserves the single external invocation and moves
// its dispatch in the same pure plan. No driver call belongs inside Apply.
func PlanDispatchAttemptClaim(
	dispatch DeliveryDispatch,
	attemptEntity MutableCommunicationEntity,
	claimOwner string,
	claimUntil time.Time,
	requestHash []byte,
	dbNow time.Time,
) (DispatchAttemptClaimPlan, error) {
	if err := ValidateDeliveryDispatch(dispatch); err != nil {
		return DispatchAttemptClaimPlan{}, err
	}
	if dispatch.State != DispatchPending || dispatch.AttemptCount != 0 {
		return DispatchAttemptClaimPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"dispatch generation is not claimable")
	}
	if dispatch.NextAttemptAt != nil && dbNow.Before(*dispatch.NextAttemptAt) {
		return DispatchAttemptClaimPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"dispatch next-attempt time has not arrived")
	}
	if err := validateMutableCommunicationEntity(attemptEntity); err != nil {
		return DispatchAttemptClaimPlan{}, err
	}
	if attemptEntity.TenantID != dispatch.TenantID || attemptEntity.WorkspaceID != dispatch.WorkspaceID ||
		attemptEntity.CreatedAt != dbNow || attemptEntity.UpdatedAt != dbNow ||
		!boundedToken(claimOwner, 128) || dbNow.IsZero() || dbNow.Before(dispatch.UpdatedAt) ||
		!claimUntil.After(dbNow) || len(requestHash) != sha256.Size {
		return DispatchAttemptClaimPlan{}, communicationError(ErrInvalidCommunicationModel,
			"invalid dispatch claim evidence")
	}
	after := dispatch
	after.Version++
	after.UpdatedAt = dbNow
	after.State = DispatchInFlight
	after.AttemptCount = 1
	after.NextAttemptAt = nil
	after.ClaimOwner = claimOwner
	after.ClaimUntil = &claimUntil
	attempt := DeliveryAttempt{
		MutableCommunicationEntity: attemptEntity, DispatchID: dispatch.ID, AttemptSeq: 1,
		State: AttemptReserved, StartedAt: dbNow, TransmitBoundary: TransmitUnknown,
		RequestHash: append([]byte(nil), requestHash...),
	}
	if err := ValidateDeliveryDispatch(after); err != nil {
		return DispatchAttemptClaimPlan{}, err
	}
	if err := ValidateDeliveryAttempt(attempt); err != nil {
		return DispatchAttemptClaimPlan{}, err
	}
	return DispatchAttemptClaimPlan{DispatchBefore: dispatch, DispatchAfter: after, Attempt: attempt}, nil
}

type DispatchAttemptFinishInput struct {
	TargetState          DeliveryDispatchState `json:"target_state"`
	TransmitBoundary     TransmitBoundary      `json:"transmit_boundary"`
	Verdict              AssessmentVerdict     `json:"verdict"`
	Code                 string                `json:"code"`
	ProviderReceiptHash  []byte                `json:"provider_receipt_hash,omitempty"`
	ResolutionDeadlineAt *time.Time            `json:"resolution_deadline_at,omitempty"`
}

type DispatchAttemptFinishPlan struct {
	DispatchBefore DeliveryDispatch `json:"dispatch_before"`
	DispatchAfter  DeliveryDispatch `json:"dispatch_after"`
	AttemptBefore  DeliveryAttempt  `json:"attempt_before"`
	AttemptAfter   DeliveryAttempt  `json:"attempt_after"`
}

// PlanDispatchAttemptFinish settles the exact reserved attempt and matching
// dispatch generation together after the external callback has returned.
func PlanDispatchAttemptFinish(
	dispatch DeliveryDispatch,
	attempt DeliveryAttempt,
	claimOwner string,
	result DispatchAttemptFinishInput,
	dbNow time.Time,
) (DispatchAttemptFinishPlan, error) {
	if err := validateDispatchAttemptPairForSettlement(dispatch, attempt, claimOwner, dbNow); err != nil {
		return DispatchAttemptFinishPlan{}, err
	}
	wantBoundary, wantVerdict := TransmitUnknown, VerdictUnknown
	switch result.TargetState {
	case DispatchSucceeded:
		wantBoundary, wantVerdict = TransmitCrossed, VerdictClean
		if len(result.ProviderReceiptHash) != sha256.Size || result.ResolutionDeadlineAt != nil {
			return DispatchAttemptFinishPlan{}, communicationError(ErrInvalidCommunicationModel,
				"success lacks provider acceptance receipt")
		}
	case DispatchFailed:
		wantBoundary, wantVerdict = TransmitNotCrossed, VerdictBroken
		if len(result.ProviderReceiptHash) != 0 || result.ResolutionDeadlineAt == nil ||
			!result.ResolutionDeadlineAt.After(dbNow) {
			return DispatchAttemptFinishPlan{}, communicationError(ErrInvalidCommunicationModel,
				"failure lacks future resolution deadline")
		}
	case DispatchUnknown:
		if len(result.ProviderReceiptHash) != 0 || result.ResolutionDeadlineAt == nil ||
			!result.ResolutionDeadlineAt.After(dbNow) {
			return DispatchAttemptFinishPlan{}, communicationError(ErrInvalidCommunicationModel,
				"unknown result lacks future reconciliation deadline")
		}
	default:
		return DispatchAttemptFinishPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"attempt cannot finish dispatch as %s", result.TargetState)
	}
	if result.TransmitBoundary != wantBoundary || result.Verdict != wantVerdict ||
		!boundedToken(result.Code, 128) {
		return DispatchAttemptFinishPlan{}, communicationError(ErrInvalidCommunicationModel,
			"attempt result contradicts dispatch state")
	}
	attemptAfter := attempt
	attemptAfter.Version++
	attemptAfter.UpdatedAt = dbNow
	attemptAfter.State = AttemptFinished
	attemptAfter.FinishedAt = &dbNow
	attemptAfter.TransmitBoundary = result.TransmitBoundary
	attemptAfter.Verdict = result.Verdict
	attemptAfter.Code = result.Code
	attemptAfter.ProviderReceiptHash = append([]byte(nil), result.ProviderReceiptHash...)
	dispatchAfter := dispatch
	dispatchAfter.Version++
	dispatchAfter.UpdatedAt = dbNow
	dispatchAfter.State = result.TargetState
	dispatchAfter.ClaimOwner = ""
	dispatchAfter.ClaimUntil = nil
	dispatchAfter.LastVerdict = result.Verdict
	dispatchAfter.LastCode = result.Code
	dispatchAfter.ResolutionCode = result.Code
	dispatchAfter.ResolutionDeadlineAt = result.ResolutionDeadlineAt
	if result.TargetState == DispatchSucceeded {
		dispatchAfter.SettledAt = &dbNow
	}
	if err := ValidateDeliveryAttempt(attemptAfter); err != nil {
		return DispatchAttemptFinishPlan{}, err
	}
	if err := ValidateDeliveryDispatch(dispatchAfter); err != nil {
		return DispatchAttemptFinishPlan{}, err
	}
	return DispatchAttemptFinishPlan{
		DispatchBefore: dispatch, DispatchAfter: dispatchAfter,
		AttemptBefore: attempt, AttemptAfter: attemptAfter,
	}, nil
}

func validateDispatchAttemptPairForSettlement(
	dispatch DeliveryDispatch,
	attempt DeliveryAttempt,
	claimOwner string,
	dbNow time.Time,
) error {
	if err := ValidateDeliveryDispatch(dispatch); err != nil {
		return err
	}
	if err := ValidateDeliveryAttempt(attempt); err != nil {
		return err
	}
	if dispatch.State != DispatchInFlight || dispatch.AttemptCount != 1 ||
		dispatch.ClaimOwner != claimOwner || !boundedToken(claimOwner, 128) || dispatch.ClaimUntil == nil ||
		!dbNow.Before(*dispatch.ClaimUntil) || dbNow.Before(dispatch.UpdatedAt) || dbNow.Before(attempt.UpdatedAt) ||
		attempt.State != AttemptReserved || attempt.DispatchID != dispatch.ID ||
		attempt.TenantID != dispatch.TenantID || attempt.WorkspaceID != dispatch.WorkspaceID {
		return communicationError(ErrInvalidCommunicationTransition,
			"dispatch and reserved Attempt are not the same live claim")
	}
	return nil
}

// PlanDispatchAttemptAbandon is the worker-death path. It cannot assert that
// the transmit boundary was or was not crossed, so both attempt and dispatch
// remain explicitly UNKNOWN until reconciliation.
func PlanDispatchAttemptAbandon(
	dispatch DeliveryDispatch,
	attempt DeliveryAttempt,
	claimOwner string,
	code string,
	resolutionDeadline time.Time,
	dbNow time.Time,
) (DispatchAttemptFinishPlan, error) {
	if err := ValidateDeliveryDispatch(dispatch); err != nil {
		return DispatchAttemptFinishPlan{}, err
	}
	if err := ValidateDeliveryAttempt(attempt); err != nil {
		return DispatchAttemptFinishPlan{}, err
	}
	if dispatch.State != DispatchInFlight || dispatch.AttemptCount != 1 ||
		dispatch.ClaimOwner != claimOwner || !boundedToken(claimOwner, 128) || dispatch.ClaimUntil == nil ||
		dbNow.Before(*dispatch.ClaimUntil) || dbNow.Before(dispatch.UpdatedAt) || dbNow.Before(attempt.UpdatedAt) ||
		attempt.State != AttemptReserved || attempt.DispatchID != dispatch.ID ||
		attempt.TenantID != dispatch.TenantID || attempt.WorkspaceID != dispatch.WorkspaceID {
		return DispatchAttemptFinishPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"dispatch and reserved Attempt are not the same expired claim")
	}
	if !boundedToken(code, 128) || !resolutionDeadline.After(dbNow) {
		return DispatchAttemptFinishPlan{}, communicationError(ErrInvalidCommunicationModel,
			"invalid abandonment evidence")
	}
	attemptAfter := attempt
	attemptAfter.Version++
	attemptAfter.UpdatedAt = dbNow
	attemptAfter.State = AttemptAbandoned
	attemptAfter.FinishedAt = &dbNow
	attemptAfter.TransmitBoundary = TransmitUnknown
	attemptAfter.Verdict = VerdictUnknown
	attemptAfter.Code = code
	dispatchAfter := dispatch
	dispatchAfter.Version++
	dispatchAfter.UpdatedAt = dbNow
	dispatchAfter.State = DispatchUnknown
	dispatchAfter.ClaimOwner = ""
	dispatchAfter.ClaimUntil = nil
	dispatchAfter.LastVerdict = VerdictUnknown
	dispatchAfter.LastCode = code
	dispatchAfter.ResolutionCode = code
	dispatchAfter.ResolutionDeadlineAt = &resolutionDeadline
	if err := ValidateDeliveryAttempt(attemptAfter); err != nil {
		return DispatchAttemptFinishPlan{}, err
	}
	if err := ValidateDeliveryDispatch(dispatchAfter); err != nil {
		return DispatchAttemptFinishPlan{}, err
	}
	return DispatchAttemptFinishPlan{
		DispatchBefore: dispatch, DispatchAfter: dispatchAfter,
		AttemptBefore: attempt, AttemptAfter: attemptAfter,
	}, nil
}

type DispatchReconcilePlan struct {
	Before             DeliveryDispatch                  `json:"before"`
	After              DeliveryDispatch                  `json:"after"`
	Attempt            DeliveryAttempt                   `json:"attempt"`
	ProviderAcceptance DispatchProviderAcceptanceWitness `json:"provider_acceptance"`
}

type DispatchProviderAcceptanceWitness struct {
	DispatchID         model.ID          `json:"dispatch_id"`
	AttemptID          model.ID          `json:"attempt_id"`
	EndpointID         model.ID          `json:"endpoint_id"`
	EndpointGeneration int64             `json:"endpoint_generation"`
	ObservedAt         time.Time         `json:"observed_at"`
	Acceptance         AuthorityEvidence `json:"acceptance"`
}

// PlanDispatchReconcile resolves UNKNOWN only from provider acceptance
// evidence. CLEAN means accepted, BROKEN means proven not accepted, and an
// inconclusive witness may only dead-letter after the DB deadline.
func PlanDispatchReconcile(
	dispatch DeliveryDispatch,
	attempt DeliveryAttempt,
	providerAcceptance DispatchProviderAcceptanceWitness,
	target DeliveryDispatchState,
	code string,
	resolutionDeadline *time.Time,
	dbNow time.Time,
) (DispatchReconcilePlan, error) {
	if err := ValidateDeliveryDispatch(dispatch); err != nil {
		return DispatchReconcilePlan{}, err
	}
	if err := ValidateDeliveryAttempt(attempt); err != nil {
		return DispatchReconcilePlan{}, err
	}
	if dispatch.State != DispatchUnknown ||
		!dispatchAttemptStateCompatible(dispatch, attempt, DispatchProviderAcceptanceWitness{}, false) ||
		providerAcceptance.DispatchID != dispatch.ID || providerAcceptance.AttemptID != attempt.ID ||
		providerAcceptance.EndpointID != dispatch.EndpointID ||
		providerAcceptance.EndpointGeneration != dispatch.EndpointGeneration ||
		providerAcceptance.ObservedAt.Before(dispatch.UpdatedAt) || providerAcceptance.ObservedAt.After(dbNow) ||
		ValidateAuthorityEvidence(providerAcceptance.Acceptance) != nil || !boundedToken(code, 128) ||
		dbNow.IsZero() || dbNow.Before(dispatch.UpdatedAt) || dbNow.Before(attempt.UpdatedAt) {
		return DispatchReconcilePlan{}, communicationError(ErrInvalidCommunicationModel,
			"invalid UNKNOWN reconciliation evidence")
	}
	after := dispatch
	after.Version++
	after.UpdatedAt = dbNow
	after.State = target
	after.LastCode = code
	after.ResolutionCode = code
	after.ResolutionDeadlineAt = resolutionDeadline
	acceptanceHash, err := CanonicalDispatchProviderAcceptanceHash(providerAcceptance)
	if err != nil {
		return DispatchReconcilePlan{}, err
	}
	after.ReconciledAttemptID = attempt.ID
	after.ReconciledEndpointID = dispatch.EndpointID
	after.ReconciledEndpointGeneration = dispatch.EndpointGeneration
	after.ReconciliationVerdict = providerAcceptance.Acceptance.Verdict
	after.ReconciliationCode = providerAcceptance.Acceptance.Code
	after.ReconciliationEvidenceRef = providerAcceptance.Acceptance.EvidenceRef
	observedAt := providerAcceptance.ObservedAt
	after.ReconciliationObservedAt = &observedAt
	after.ProviderAcceptanceHash = acceptanceHash
	switch target {
	case DispatchSucceeded:
		if evidenceVerdict(providerAcceptance.Acceptance) != VerdictClean || resolutionDeadline != nil {
			return DispatchReconcilePlan{}, communicationError(ErrCommunicationEvidenceUnknown,
				"provider has not proven acceptance")
		}
		after.LastVerdict = VerdictClean
		after.SettledAt = &dbNow
	case DispatchFailed:
		if evidenceVerdict(providerAcceptance.Acceptance) != VerdictBroken || resolutionDeadline == nil ||
			!resolutionDeadline.After(dbNow) {
			return DispatchReconcilePlan{}, communicationError(ErrCommunicationEvidenceUnknown,
				"provider has not proven non-acceptance")
		}
		after.LastVerdict = VerdictBroken
	case DispatchDeadLetter:
		if evidenceVerdict(providerAcceptance.Acceptance) != VerdictUnknown || resolutionDeadline != nil ||
			dispatch.ResolutionDeadlineAt == nil || dbNow.Before(*dispatch.ResolutionDeadlineAt) {
			return DispatchReconcilePlan{}, communicationError(ErrInvalidCommunicationTransition,
				"indeterminate dispatch cannot dead-letter before deadline")
		}
		after.LastVerdict = VerdictUnknown
		after.SettledAt = &dbNow
	default:
		return DispatchReconcilePlan{}, communicationError(ErrInvalidCommunicationTransition,
			"UNKNOWN dispatch cannot reconcile to %s", target)
	}
	if err := ValidateDeliveryDispatch(after); err != nil {
		return DispatchReconcilePlan{}, err
	}
	return DispatchReconcilePlan{
		Before: dispatch, After: after, Attempt: attempt, ProviderAcceptance: providerAcceptance,
	}, nil
}

type DispatchDeadLetterWitness struct {
	DispatchID model.ID          `json:"dispatch_id"`
	AttemptID  model.ID          `json:"attempt_id"`
	ObservedAt time.Time         `json:"observed_at"`
	Evidence   AuthorityEvidence `json:"evidence"`
}

type DispatchDeadLetterPlan struct {
	Before    DeliveryDispatch          `json:"before"`
	After     DeliveryDispatch          `json:"after"`
	Attempt   DeliveryAttempt           `json:"attempt"`
	Authority DispatchDeadLetterWitness `json:"authority"`
}

// PlanFailedDispatchDeadLetter is the only no-successor terminal path for a
// failed generation. It becomes legal at (not before) its DB deadline.
func PlanFailedDispatchDeadLetter(
	dispatch DeliveryDispatch,
	attempt DeliveryAttempt,
	authority DispatchDeadLetterWitness,
	code string,
	dbNow time.Time,
) (DispatchDeadLetterPlan, error) {
	if err := ValidateDeliveryDispatch(dispatch); err != nil {
		return DispatchDeadLetterPlan{}, err
	}
	if err := ValidateDeliveryAttempt(attempt); err != nil {
		return DispatchDeadLetterPlan{}, err
	}
	if dispatch.State != DispatchFailed || dispatch.ResolutionDeadlineAt == nil ||
		dbNow.Before(*dispatch.ResolutionDeadlineAt) || dbNow.Before(dispatch.UpdatedAt) ||
		authority.DispatchID != dispatch.ID || authority.AttemptID != attempt.ID ||
		authority.ObservedAt != dbNow || ValidateAuthorityEvidence(authority.Evidence) != nil ||
		evidenceVerdict(authority.Evidence) != VerdictClean || !boundedToken(code, 128) ||
		!dispatchAttemptStateCompatible(dispatch, attempt, DispatchProviderAcceptanceWitness{}, false) {
		return DispatchDeadLetterPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"failed dispatch cannot dead-letter before its exact deadline")
	}
	after := dispatch
	after.Version++
	after.UpdatedAt = dbNow
	after.State = DispatchDeadLetter
	after.ResolutionDeadlineAt = nil
	after.ResolutionCode = code
	after.LastCode = code
	after.SettledAt = &dbNow
	if err := ValidateDeliveryDispatch(after); err != nil {
		return DispatchDeadLetterPlan{}, err
	}
	return DispatchDeadLetterPlan{Before: dispatch, After: after, Attempt: attempt, Authority: authority}, nil
}

type DispatchSuccessorPlan struct {
	PredecessorBefore  DeliveryDispatch           `json:"predecessor_before"`
	PredecessorAfter   DeliveryDispatch           `json:"predecessor_after"`
	PredecessorAttempt DeliveryAttempt            `json:"predecessor_attempt"`
	Successor          DeliveryDispatch           `json:"successor"`
	PriorRoute         DispatchRouteAttestation   `json:"prior_route"`
	SuccessorRoute     DispatchRouteIdentity      `json:"successor_route"`
	Authority          DispatchSuccessorAuthority `json:"authority"`
}

// DispatchRouteAttestation is the authoritative pure input naming the full
// route tuple used by a durable dispatch. Slice F must choose and attest its
// durable representation before schema digests; E does not invent a column.
type DispatchRouteAttestation struct {
	DispatchID model.ID              `json:"dispatch_id"`
	Route      DispatchRouteIdentity `json:"route"`
	ObservedAt time.Time             `json:"observed_at"`
	Evidence   AuthorityEvidence     `json:"evidence"`
}

type DispatchSuccessorAuthority struct {
	PredecessorID        model.ID                          `json:"predecessor_id"`
	AttemptID            model.ID                          `json:"attempt_id"`
	ProviderNoAcceptance DispatchProviderAcceptanceWitness `json:"provider_no_acceptance"`
	RootHistory          DispatchRootHistoryWitness        `json:"root_history"`
	CommandAuthorization AuthorityEvidence                 `json:"command_authorization"`
	EvidenceRef          string                            `json:"evidence_ref"`
}

type DispatchRootHistoryEntry struct {
	DispatchID         model.ID `json:"dispatch_id"`
	DispatchGeneration int64    `json:"dispatch_generation"`
	IdempotencyKeyHash []byte   `json:"idempotency_key_hash"`
}

// DispatchRootHistoryWitness is the same-transaction complete key history for
// one dispatch root. It prevents a later generation from cycling back to any
// prior provider idempotency key, not merely its immediate predecessor's key.
type DispatchRootHistoryWitness struct {
	Scope          DirectoryScopeRef          `json:"scope"`
	RootDispatchID model.ID                   `json:"root_dispatch_id"`
	Entries        []DispatchRootHistoryEntry `json:"entries"`
	ObservedAt     time.Time                  `json:"observed_at"`
	Evidence       AuthorityEvidence          `json:"evidence"`
}

func validateDispatchRootHistory(
	witness DispatchRootHistoryWitness,
	predecessor DeliveryDispatch,
	successorKeyHash []byte,
	dbNow time.Time,
) error {
	scope := DirectoryScopeRef{TenantID: predecessor.TenantID, WorkspaceID: predecessor.WorkspaceID}
	if witness.Scope != scope || witness.RootDispatchID != predecessor.RootDispatchID ||
		witness.ObservedAt != dbNow || ValidateAuthorityEvidence(witness.Evidence) != nil ||
		evidenceVerdict(witness.Evidence) != VerdictClean ||
		len(witness.Entries) != int(predecessor.DispatchGeneration) {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"dispatch root history is not a complete same-transaction witness")
	}
	seenIDs := make(map[model.ID]struct{}, len(witness.Entries))
	seenHashes := make(map[string]struct{}, len(witness.Entries))
	for index, entry := range witness.Entries {
		if !validCanonicalCommunicationID(entry.DispatchID) ||
			entry.DispatchGeneration != int64(index+1) || len(entry.IdempotencyKeyHash) != sha256.Size {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"dispatch root history is not canonical and contiguous")
		}
		if _, duplicate := seenIDs[entry.DispatchID]; duplicate {
			return communicationError(ErrCommunicationEvidenceUnknown, "dispatch root history repeats an ID")
		}
		key := string(entry.IdempotencyKeyHash)
		if _, duplicate := seenHashes[key]; duplicate {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"dispatch root history already repeats an idempotency key")
		}
		seenIDs[entry.DispatchID] = struct{}{}
		seenHashes[key] = struct{}{}
	}
	latest := witness.Entries[len(witness.Entries)-1]
	if latest.DispatchID != predecessor.ID ||
		!bytes.Equal(latest.IdempotencyKeyHash, predecessor.IdempotencyKeyHash) {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"dispatch root history does not end at the locked predecessor")
	}
	if _, reused := seenHashes[string(successorKeyHash)]; reused {
		return communicationError(ErrInvalidCommunicationModel,
			"dispatch successor reuses a root idempotency key")
	}
	return nil
}

// PlanDispatchSuccessor implements C4 as one indivisible two-row plan. It never
// exposes failed -> pending on the predecessor.
func PlanDispatchSuccessor(
	predecessor DeliveryDispatch,
	predecessorAttempt DeliveryAttempt,
	authority DispatchSuccessorAuthority,
	priorRoute DispatchRouteAttestation,
	successorID model.ID,
	successorRoute DispatchRouteIdentity,
	successorKeyHash []byte,
	dbNow time.Time,
) (DispatchSuccessorPlan, error) {
	if err := ValidateDeliveryDispatch(predecessor); err != nil {
		return DispatchSuccessorPlan{}, err
	}
	if !oneOf(predecessor.State, DispatchFailed, DispatchUnknown) {
		return DispatchSuccessorPlan{}, communicationError(ErrInvalidCommunicationTransition,
			"dispatch %s cannot receive successor", predecessor.State)
	}
	if err := ValidateDeliveryAttempt(predecessorAttempt); err != nil {
		return DispatchSuccessorPlan{}, err
	}
	if predecessorAttempt.DispatchID != predecessor.ID ||
		predecessorAttempt.TenantID != predecessor.TenantID ||
		predecessorAttempt.WorkspaceID != predecessor.WorkspaceID ||
		predecessorAttempt.State == AttemptReserved || authority.PredecessorID != predecessor.ID ||
		authority.AttemptID != predecessorAttempt.ID || !validateOpaqueRef(authority.EvidenceRef) ||
		ValidateAuthorityEvidence(authority.CommandAuthorization) != nil ||
		authority.CommandAuthorization.EvidenceRef != authority.EvidenceRef ||
		evidenceVerdict(authority.CommandAuthorization) != VerdictClean {
		return DispatchSuccessorPlan{}, communicationError(ErrInvalidCommunicationModel,
			"successor authority is not bound to predecessor Attempt")
	}
	if predecessor.State == DispatchFailed {
		directFailure := predecessorAttempt.State == AttemptFinished &&
			predecessorAttempt.TransmitBoundary == TransmitNotCrossed &&
			predecessorAttempt.Verdict == VerdictBroken && predecessor.LastVerdict == VerdictBroken
		reconciledFailure := predecessorAttempt.TransmitBoundary == TransmitUnknown &&
			predecessorAttempt.Verdict == VerdictUnknown && predecessor.LastVerdict == VerdictBroken &&
			durableDispatchReconciliationMatches(predecessor, predecessorAttempt, VerdictBroken)
		if !directFailure && !reconciledFailure {
			return DispatchSuccessorPlan{}, communicationError(ErrInvalidCommunicationModel,
				"failed predecessor is not proven pre-transmit")
		}
	} else if authority.ProviderNoAcceptance.DispatchID != predecessor.ID ||
		authority.ProviderNoAcceptance.AttemptID != predecessorAttempt.ID ||
		authority.ProviderNoAcceptance.EndpointID != predecessor.EndpointID ||
		authority.ProviderNoAcceptance.EndpointGeneration != predecessor.EndpointGeneration ||
		authority.ProviderNoAcceptance.ObservedAt.Before(predecessor.UpdatedAt) ||
		authority.ProviderNoAcceptance.ObservedAt.After(dbNow) ||
		ValidateAuthorityEvidence(authority.ProviderNoAcceptance.Acceptance) != nil ||
		evidenceVerdict(authority.ProviderNoAcceptance.Acceptance) != VerdictBroken ||
		predecessorAttempt.TransmitBoundary != TransmitUnknown ||
		predecessorAttempt.Verdict != VerdictUnknown || predecessor.LastVerdict != VerdictUnknown {
		return DispatchSuccessorPlan{}, communicationError(ErrCommunicationEvidenceUnknown,
			"unknown predecessor lacks provider no-acceptance witness")
	}
	if err := ValidateDispatchRouteIdentity(priorRoute.Route); err != nil {
		return DispatchSuccessorPlan{}, err
	}
	if priorRoute.DispatchID != predecessor.ID || priorRoute.ObservedAt.Before(predecessor.UpdatedAt) ||
		priorRoute.ObservedAt.After(dbNow) || ValidateAuthorityEvidence(priorRoute.Evidence) != nil ||
		evidenceVerdict(priorRoute.Evidence) != VerdictClean ||
		!priorRoute.Route.Equal(dispatchRouteIdentity(predecessor)) {
		return DispatchSuccessorPlan{}, communicationError(ErrInvalidCommunicationModel,
			"prior route does not match predecessor")
	}
	if err := ValidateDispatchRouteIdentity(successorRoute); err != nil {
		return DispatchSuccessorPlan{}, err
	}
	if !validCanonicalCommunicationID(successorID) || successorID == predecessor.ID ||
		successorID == predecessor.RootDispatchID || len(successorKeyHash) != sha256.Size ||
		bytes.Equal(successorKeyHash, predecessor.IdempotencyKeyHash) || dbNow.IsZero() ||
		dbNow.Before(predecessor.UpdatedAt) {
		return DispatchSuccessorPlan{}, communicationError(ErrInvalidCommunicationModel, "invalid successor identity")
	}
	if err := validateDispatchRootHistory(authority.RootHistory, predecessor, successorKeyHash, dbNow); err != nil {
		return DispatchSuccessorPlan{}, err
	}
	predecessorAfter := predecessor
	predecessorAfter.Version++
	predecessorAfter.UpdatedAt = dbNow
	predecessorAfter.State = DispatchSuperseded
	predecessorAfter.ResolutionCode = "successor_created"
	predecessorAfter.ResolutionDeadlineAt = nil
	predecessorAfter.SettledAt = &dbNow
	if predecessor.State == DispatchUnknown {
		acceptanceHash, err := CanonicalDispatchProviderAcceptanceHash(authority.ProviderNoAcceptance)
		if err != nil {
			return DispatchSuccessorPlan{}, err
		}
		predecessorAfter.ReconciledAttemptID = predecessorAttempt.ID
		predecessorAfter.ReconciledEndpointID = predecessor.EndpointID
		predecessorAfter.ReconciledEndpointGeneration = predecessor.EndpointGeneration
		predecessorAfter.ReconciliationVerdict = authority.ProviderNoAcceptance.Acceptance.Verdict
		predecessorAfter.ReconciliationCode = authority.ProviderNoAcceptance.Acceptance.Code
		predecessorAfter.ReconciliationEvidenceRef = authority.ProviderNoAcceptance.Acceptance.EvidenceRef
		observedAt := authority.ProviderNoAcceptance.ObservedAt
		predecessorAfter.ReconciliationObservedAt = &observedAt
		predecessorAfter.ProviderAcceptanceHash = acceptanceHash
	}
	rung := predecessor.RerouteRung
	if !priorRoute.Route.Equal(successorRoute) {
		rung++
	}
	successor := DeliveryDispatch{
		MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: successorID, TenantID: predecessor.TenantID, WorkspaceID: predecessor.WorkspaceID,
			Version: 1, CreatedAt: dbNow,
		}, UpdatedAt: dbNow},
		DeliveryID: predecessor.DeliveryID, RootDispatchID: predecessor.RootDispatchID,
		PredecessorID: predecessor.ID, EndpointID: successorRoute.EndpointID,
		EndpointGeneration: successorRoute.EndpointGeneration,
		RouteRuleID:        successorRoute.RouteRuleID, RouteRuleGeneration: successorRoute.RouteRuleGeneration,
		DispatchGeneration: predecessor.DispatchGeneration + 1, RerouteRung: rung,
		PolicyGeneration: successorRoute.PolicyGeneration, State: DispatchPending,
		IdempotencyKeyHash: append([]byte(nil), successorKeyHash...),
	}
	if err := ValidateDeliveryDispatch(predecessorAfter); err != nil {
		return DispatchSuccessorPlan{}, err
	}
	if err := ValidateDeliveryDispatch(successor); err != nil {
		return DispatchSuccessorPlan{}, err
	}
	return DispatchSuccessorPlan{
		PredecessorBefore: predecessor, PredecessorAfter: predecessorAfter, Successor: successor,
		PredecessorAttempt: predecessorAttempt, PriorRoute: priorRoute,
		SuccessorRoute: successorRoute, Authority: authority,
	}, nil
}
