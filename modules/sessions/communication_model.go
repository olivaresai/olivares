// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CommunicationEntity is the common immutable identity and lineage carried by
// every K3 communication row. Tenant and workspace are server-resolved facts;
// they are never accepted as sender assertions from a command body.
type CommunicationEntity struct {
	ID          model.ID       `json:"id"`
	TenantID    model.TenantID `json:"tenant_id"`
	WorkspaceID model.ID       `json:"workspace_id"`
	Version     int64          `json:"version"`
	CreatedAt   time.Time      `json:"created_at"`
}

// MutableCommunicationEntity adds the store timestamp exposed by one of K3's
// fifteen mutable, no-delete entities. Append-only entities deliberately do not
// embed it in their public domain shape.
type MutableCommunicationEntity struct {
	CommunicationEntity
	UpdatedAt time.Time `json:"updated_at"`
}

// AppendOnlyCommunicationEntity marks one of the five append-only K3 entities.
type AppendOnlyCommunicationEntity struct {
	CommunicationEntity
}

type ChannelKind string

const (
	ChannelCoordination ChannelKind = "coordination"
	ChannelWork         ChannelKind = "work"
	ChannelIncident     ChannelKind = "incident"
	ChannelAnnouncement ChannelKind = "announcement"
	ChannelPrivate      ChannelKind = "private"
)

type ChannelState string

const (
	ChannelActive   ChannelState = "active"
	ChannelArchived ChannelState = "archived"
)

type ChannelSensitivity string

const (
	ChannelInternal   ChannelSensitivity = "internal"
	ChannelRestricted ChannelSensitivity = "restricted"
)

type ContentProtection string

const (
	ContentProtectionStorage           ContentProtection = "storage"
	ContentProtectionApplicationSealed ContentProtection = "application_sealed"
)

type CommunicationSubjectKind string

const (
	SubjectUser       CommunicationSubjectKind = "user"
	SubjectUserGroup  CommunicationSubjectKind = "user_group"
	SubjectAgent      CommunicationSubjectKind = "agent"
	SubjectAgentGroup CommunicationSubjectKind = "agent_group"
	SubjectSession    CommunicationSubjectKind = "session"
)

type CommunicationSubjectRef struct {
	Kind CommunicationSubjectKind `json:"kind"`
	Ref  string                   `json:"ref"`
}

type ChannelGrantState string

const (
	ChannelGrantActive  ChannelGrantState = "active"
	ChannelGrantRevoked ChannelGrantState = "revoked"
	ChannelGrantExpired ChannelGrantState = "expired"
)

type ChannelGrantBit string

const (
	ChannelGrantRead  ChannelGrantBit = "read"
	ChannelGrantWrite ChannelGrantBit = "write"
	ChannelGrantAdmin ChannelGrantBit = "admin"
)

type ChannelSubscriptionMode string

const (
	SubscriptionAll      ChannelSubscriptionMode = "all"
	SubscriptionMentions ChannelSubscriptionMode = "mentions"
	SubscriptionCritical ChannelSubscriptionMode = "critical"
	SubscriptionNone     ChannelSubscriptionMode = "none"
)

type WakePolicy string

const (
	WakeNone    WakePolicy = "none"
	WakePrimary WakePolicy = "primary"
	WakeAll     WakePolicy = "all"
	WakeInherit WakePolicy = "inherit"
)

type ChannelSubscriptionState string

const (
	SubscriptionActive  ChannelSubscriptionState = "active"
	SubscriptionPaused  ChannelSubscriptionState = "paused"
	SubscriptionRevoked ChannelSubscriptionState = "revoked"
)

type ChannelLabelState string

const (
	ChannelLabelActive   ChannelLabelState = "active"
	ChannelLabelDisabled ChannelLabelState = "disabled"
)

type ChannelLabelClassification string

const ChannelLabelNonSensitive ChannelLabelClassification = "non_sensitive"

type ChannelRouteSourceKind string

const (
	RouteSourceUserMessage ChannelRouteSourceKind = "user_message"
	RouteSourceWorkEvent   ChannelRouteSourceKind = "work_event"
	RouteSourceSystemEvent ChannelRouteSourceKind = "system_event"
	RouteSourceProtocol    ChannelRouteSourceKind = "protocol"
)

type ChannelRouteAudienceKind string

const (
	RouteAudienceSubscribers     ChannelRouteAudienceKind = "subscribers"
	RouteAudienceUserGroup       ChannelRouteAudienceKind = "user_group"
	RouteAudienceAgentGroup      ChannelRouteAudienceKind = "agent_group"
	RouteAudienceWorkspaceMember ChannelRouteAudienceKind = "workspace_members"
)

type ChannelRouteState string

const (
	ChannelRouteActive   ChannelRouteState = "active"
	ChannelRouteDisabled ChannelRouteState = "disabled"
)

type CommunicationEndpointSupport string

const (
	EndpointStable       CommunicationEndpointSupport = "stable"
	EndpointPreview      CommunicationEndpointSupport = "preview"
	EndpointExperimental CommunicationEndpointSupport = "experimental"
)

type CommunicationEndpointState string

const (
	EndpointActive   CommunicationEndpointState = "active"
	EndpointStale    CommunicationEndpointState = "stale"
	EndpointDisabled CommunicationEndpointState = "disabled"
)

type PayloadEncoding string

const (
	PayloadPlainJSON PayloadEncoding = "plain_json"
	PayloadSealedV1  PayloadEncoding = "sealed_v1"
)

// SealedPayload is the authenticated envelope returned by the sealer. The
// version inside the envelope must exactly match ProtectedPayload.SealKeyVersion
// before Open is called.
type SealedPayload struct {
	Ciphertext []byte `json:"ciphertext"`
	KeyVersion string `json:"key_version"`
}

// ProtectedPayload is the single logical representation used for human text
// and structured content. Seal and digest versions are intentionally distinct:
// rotation is allowed to choose different keys for encryption and digesting.
type ProtectedPayload struct {
	Encoding             PayloadEncoding `json:"encoding"`
	PlainJSON            json.RawMessage `json:"plain_json,omitempty"`
	Sealed               *SealedPayload  `json:"sealed,omitempty"`
	Schema               string          `json:"schema"`
	Digest               []byte          `json:"digest"`
	SealKeyVersion       string          `json:"seal_key_version,omitempty"`
	DigestKeyVersion     string          `json:"digest_key_version,omitempty"`
	ProtectionGeneration int64           `json:"protection_generation"`
}

// ContentAAD binds content to the exact server-created carrier. Key version is
// deliberately absent because Seal has not selected one when AAD is built.
type ContentAAD struct {
	TenantID             model.TenantID `json:"tenant_id"`
	WorkspaceID          model.ID       `json:"workspace_id"`
	ChannelID            model.ID       `json:"channel_id"`
	EntityKind           model.Kind     `json:"entity_kind"`
	EntityID             model.ID       `json:"entity_id"`
	Schema               string         `json:"schema"`
	ProtectionGeneration int64          `json:"protection_generation"`
}

type MessageContentBlockKind string

const (
	ContentBlockText      MessageContentBlockKind = "text"
	ContentBlockReference MessageContentBlockKind = "reference"
	ContentBlockStatus    MessageContentBlockKind = "status"
	ContentBlockActionRef MessageContentBlockKind = "action_ref"
)

type TextFormat string

const (
	TextPlain    TextFormat = "plain"
	TextMarkdown TextFormat = "markdown"
)

type ContentReference struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Hash string `json:"hash,omitempty"`
}

type MessageContentBlock struct {
	Type      MessageContentBlockKind `json:"type"`
	Format    TextFormat              `json:"format,omitempty"`
	Text      string                  `json:"text,omitempty"`
	Reference *ContentReference       `json:"reference,omitempty"`
	Code      string                  `json:"code,omitempty"`
}

type MessageContent struct {
	Subject string                `json:"subject"`
	Blocks  []MessageContentBlock `json:"blocks"`
}

type MessageKind string

const (
	MessageNotice          MessageKind = "notice"
	MessageAnnouncement    MessageKind = "announcement"
	MessageRequest         MessageKind = "request"
	MessageDecisionRequest MessageKind = "decision_request"
	MessageHandoffOffer    MessageKind = "handoff_offer"
	MessageSystem          MessageKind = "system"
)

type MessageState string

const (
	MessageDraft     MessageState = "draft"
	MessagePublished MessageState = "published"
	MessageRetracted MessageState = "retracted"
	MessageExpired   MessageState = "expired"
	MessageDiscarded MessageState = "discarded"
)

type MessageUrgency string

const (
	UrgencyNormal   MessageUrgency = "normal"
	UrgencyHigh     MessageUrgency = "high"
	UrgencyCritical MessageUrgency = "critical"
)

type AckPolicy string

const (
	AckPolicyNone         AckPolicy = "none"
	AckPolicyEachRequired AckPolicy = "each_required"
	AckPolicyQuorum       AckPolicy = "quorum"
)

type AudienceSelectorKind string

const (
	AudienceUser             AudienceSelectorKind = "user"
	AudienceUserGroup        AudienceSelectorKind = "user_group"
	AudienceAgent            AudienceSelectorKind = "agent"
	AudienceAgentGroup       AudienceSelectorKind = "agent_group"
	AudienceSession          AudienceSelectorKind = "session"
	AudienceSubscribers      AudienceSelectorKind = "subscribers"
	AudienceWorkspaceMembers AudienceSelectorKind = "workspace_members"
)

type AudienceSelector struct {
	Kind       AudienceSelectorKind `json:"kind"`
	Ref        string               `json:"ref,omitempty"`
	Required   bool                 `json:"required"`
	WakePolicy WakePolicy           `json:"wake_policy"`
}

type RecipientKind string

const (
	RecipientUser    RecipientKind = "user"
	RecipientAgent   RecipientKind = "agent"
	RecipientSession RecipientKind = "session"
)

type RecipientRef struct {
	Kind RecipientKind `json:"kind"`
	Ref  string        `json:"ref"`
}

type CommunicationActorKind string

const (
	ActorUser    CommunicationActorKind = "user"
	ActorAgent   CommunicationActorKind = "agent"
	ActorSession CommunicationActorKind = "session"
	ActorSystem  CommunicationActorKind = "system"
)

type CommunicationActorRef struct {
	Kind CommunicationActorKind `json:"kind"`
	Ref  string                 `json:"ref"`
}

type AudienceCausalKind string

const (
	CausalDirect          AudienceCausalKind = "direct"
	CausalUserGroup       AudienceCausalKind = "user_group"
	CausalAgentGroup      AudienceCausalKind = "agent_group"
	CausalWorkspaceMember AudienceCausalKind = "workspace_member"
	CausalSubscriber      AudienceCausalKind = "subscriber"
)

type RouteReason string

type MessageDeliveryState string

const (
	DeliveryAvailable     MessageDeliveryState = "available"
	DeliveryAcknowledged  MessageDeliveryState = "acknowledged"
	DeliveryExpired       MessageDeliveryState = "expired"
	DeliveryRetracted     MessageDeliveryState = "retracted"
	DeliveryUndeliverable MessageDeliveryState = "undeliverable"
)

type MailboxKind string

const (
	MailboxPersonal MailboxKind = "personal"
	MailboxChannel  MailboxKind = "channel"
)

type CursorBarrierCause string

const (
	BarrierNotYetAvailable      CursorBarrierCause = "not_yet_available"
	BarrierTemporarilyInvisible CursorBarrierCause = "temporarily_invisible"
)

type CursorBarrierState string

const (
	CursorBarrierActive   CursorBarrierState = "active"
	CursorBarrierResolved CursorBarrierState = "resolved"
)

type MessageAckKind string

const MessageAckReceived MessageAckKind = "received"

type CommunicationGuardKind string

const (
	CommunicationGuardDeliverySequence CommunicationGuardKind = "delivery_sequence"
	CommunicationGuardRouteRevision    CommunicationGuardKind = "route_revision"
)

type DecisionRequestState string

const (
	DecisionPending  DecisionRequestState = "pending"
	DecisionAccepted DecisionRequestState = "accepted"
	DecisionBlocked  DecisionRequestState = "blocked"
	DecisionResolved DecisionRequestState = "resolved"
	DecisionRejected DecisionRequestState = "rejected"
	DecisionCanceled DecisionRequestState = "canceled"
	DecisionExpired  DecisionRequestState = "expired"
)

type HandoffState string

const (
	HandoffOffered   HandoffState = "offered"
	HandoffAccepted  HandoffState = "accepted"
	HandoffRejected  HandoffState = "rejected"
	HandoffWithdrawn HandoffState = "withdrawn"
	HandoffExpired   HandoffState = "expired"
)

type DeliveryDispatchState string

const (
	DispatchPending    DeliveryDispatchState = "pending"
	DispatchInFlight   DeliveryDispatchState = "in_flight"
	DispatchSucceeded  DeliveryDispatchState = "succeeded"
	DispatchFailed     DeliveryDispatchState = "failed"
	DispatchUnknown    DeliveryDispatchState = "unknown"
	DispatchDeadLetter DeliveryDispatchState = "dead_letter"
	DispatchSuperseded DeliveryDispatchState = "superseded"
)

type DeliveryAttemptState string

const (
	AttemptReserved  DeliveryAttemptState = "reserved"
	AttemptFinished  DeliveryAttemptState = "finished"
	AttemptAbandoned DeliveryAttemptState = "abandoned"
)

type TransmitBoundary string

const (
	TransmitNotCrossed TransmitBoundary = "not_crossed"
	TransmitCrossed    TransmitBoundary = "crossed"
	TransmitUnknown    TransmitBoundary = "unknown"
)

type FulfillmentState string

const (
	FulfillmentNotRequired FulfillmentState = "not_required"
	FulfillmentPending     FulfillmentState = "pending"
	FulfillmentMet         FulfillmentState = "met"
	FulfillmentUnmet       FulfillmentState = "unmet_terminal"
)

// Channel is the root of the K3 communication authority plane.
type Channel struct {
	MutableCommunicationEntity
	Slug                 string             `json:"slug"`
	Name                 string             `json:"name"`
	Description          string             `json:"description,omitempty"`
	Kind                 ChannelKind        `json:"kind"`
	State                ChannelState       `json:"state"`
	Sensitivity          ChannelSensitivity `json:"sensitivity"`
	ContentProtection    ContentProtection  `json:"content_protection"`
	ProtectionGeneration int64              `json:"protection_generation"`
	DefaultAckPolicy     AckPolicy          `json:"default_ack_policy"`
	DefaultAckTimeoutMS  int64              `json:"default_ack_timeout_ms"`
	DefaultWake          WakePolicy         `json:"default_wake"`
	RetentionPolicyRef   string             `json:"retention_policy_ref,omitempty"`
	MaxFanout            int64              `json:"max_fanout"`
	MaxAutomationDepth   int64              `json:"max_automation_depth"`
	ACLRevision          int64              `json:"acl_revision"`
	RouteRevision        int64              `json:"route_revision"`
	SubscriptionRevision int64              `json:"subscription_revision"`
}

type ChannelGrant struct {
	MutableCommunicationEntity
	ChannelID    model.ID                `json:"channel_id"`
	Subject      CommunicationSubjectRef `json:"subject"`
	Generation   int64                   `json:"generation"`
	CanRead      bool                    `json:"can_read"`
	CanWrite     bool                    `json:"can_write"`
	CanAdmin     bool                    `json:"can_admin"`
	State        ChannelGrantState       `json:"state"`
	GrantedBy    CommunicationActorRef   `json:"granted_by"`
	RevokedBy    *CommunicationActorRef  `json:"revoked_by,omitempty"`
	ExpiresAt    *time.Time              `json:"expires_at,omitempty"`
	SupersedesID model.ID                `json:"supersedes_id,omitempty"`
}

type ChannelSubscription struct {
	MutableCommunicationEntity
	ChannelID           model.ID                 `json:"channel_id"`
	Subscriber          CommunicationSubjectRef  `json:"subscriber"`
	Generation          int64                    `json:"generation"`
	Mode                ChannelSubscriptionMode  `json:"mode"`
	Wake                WakePolicy               `json:"wake"`
	RequiredForCritical bool                     `json:"required_for_critical"`
	State               ChannelSubscriptionState `json:"state"`
	FilterJSON          json.RawMessage          `json:"filter_json,omitempty"`
	FilterHash          []byte                   `json:"filter_hash,omitempty"`
	SupersedesID        model.ID                 `json:"supersedes_id,omitempty"`
}

type ChannelLabelDefinition struct {
	MutableCommunicationEntity
	ChannelID         model.ID                   `json:"channel_id"`
	Key               string                     `json:"key"`
	Generation        int64                      `json:"generation"`
	AllowedValuesJSON json.RawMessage            `json:"allowed_values_json"`
	ValuesHash        []byte                     `json:"values_hash"`
	Classification    ChannelLabelClassification `json:"classification"`
	State             ChannelLabelState          `json:"state"`
}

type ChannelRouteRule struct {
	MutableCommunicationEntity
	RouteKey        string                   `json:"route_key"`
	Generation      int64                    `json:"generation"`
	Priority        int64                    `json:"priority"`
	SourceKind      ChannelRouteSourceKind   `json:"source_kind"`
	EventType       string                   `json:"event_type,omitempty"`
	MessageKind     MessageKind              `json:"message_kind,omitempty"`
	MinimumUrgency  MessageUrgency           `json:"minimum_urgency,omitempty"`
	LabelMatchJSON  json.RawMessage          `json:"label_match_json,omitempty"`
	TargetChannelID model.ID                 `json:"target_channel_id"`
	AudienceKind    ChannelRouteAudienceKind `json:"audience_kind"`
	AudienceRef     string                   `json:"audience_ref,omitempty"`
	AckPolicy       AckPolicy                `json:"ack_policy"`
	WakePolicy      WakePolicy               `json:"wake_policy"`
	CatchAll        bool                     `json:"catch_all"`
	State           ChannelRouteState        `json:"state"`
	SupersedesID    model.ID                 `json:"supersedes_id,omitempty"`
}

type CommunicationEndpoint struct {
	MutableCommunicationEntity
	Owner                RecipientRef                 `json:"owner"`
	ProviderKey          string                       `json:"provider_key"`
	Transport            string                       `json:"transport"`
	EndpointRef          string                       `json:"endpoint_ref"`
	SessionSID           string                       `json:"session_sid,omitempty"`
	CapabilitiesJSON     json.RawMessage              `json:"capabilities_json"`
	TransportFingerprint string                       `json:"transport_fingerprint,omitempty"`
	SupportLevel         CommunicationEndpointSupport `json:"support_level"`
	Priority             int64                        `json:"priority"`
	State                CommunicationEndpointState   `json:"state"`
	HeartbeatExpiresAt   *time.Time                   `json:"heartbeat_expires_at,omitempty"`
	Generation           int64                        `json:"generation"`
	SecretRef            string                       `json:"secret_ref,omitempty"`
}

type Message struct {
	MutableCommunicationEntity
	ChannelID       model.ID              `json:"channel_id"`
	WorkItemID      model.ID              `json:"work_item_id,omitempty"`
	ThreadID        model.ID              `json:"thread_id"`
	Kind            MessageKind           `json:"kind"`
	State           MessageState          `json:"state"`
	Sender          CommunicationActorRef `json:"sender"`
	Payload         ProtectedPayload      `json:"payload"`
	LabelsJSON      json.RawMessage       `json:"labels_json,omitempty"`
	LabelsHash      []byte                `json:"labels_hash,omitempty"`
	Urgency         MessageUrgency        `json:"urgency"`
	AckPolicy       AckPolicy             `json:"ack_policy"`
	AckQuorum       int64                 `json:"ack_quorum,omitempty"`
	AvailableAt     time.Time             `json:"available_at"`
	AckDueAt        *time.Time            `json:"ack_due_at,omitempty"`
	ExpiresAt       *time.Time            `json:"expires_at,omitempty"`
	ReplyToID       model.ID              `json:"reply_to_id,omitempty"`
	SupersedesID    model.ID              `json:"supersedes_id,omitempty"`
	OriginEventID   model.ID              `json:"origin_event_id,omitempty"`
	AutomationDepth int64                 `json:"automation_depth"`
	PublishedAt     *time.Time            `json:"published_at,omitempty"`
	TerminalAt      *time.Time            `json:"terminal_at,omitempty"`
	TerminalCode    string                `json:"terminal_code,omitempty"`
	TerminalReason  *ProtectedPayload     `json:"terminal_reason,omitempty"`
	AudienceHash    []byte                `json:"audience_hash,omitempty"`
	LastEventSeq    int64                 `json:"last_event_seq"`
}

type MessageAudience struct {
	AppendOnlyCommunicationEntity
	MessageID            model.ID         `json:"message_id"`
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

// MessageAudienceRecipient preserves one normalized selector contribution ×
// recipient × causal-arc row. Several distinct causal arcs may point to the
// same selector, recipient and globally deduplicated Delivery.
type MessageAudienceRecipient struct {
	AppendOnlyCommunicationEntity
	MessageAudienceID      model.ID                 `json:"message_audience_id"`
	MessageDeliveryID      model.ID                 `json:"message_delivery_id"`
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

type MessageDelivery struct {
	MutableCommunicationEntity
	MessageID                  model.ID             `json:"message_id"`
	Recipient                  RecipientRef         `json:"recipient"`
	RecipientEpoch             int64                `json:"recipient_epoch"`
	DeliverySeq                int64                `json:"delivery_seq"`
	Required                   bool                 `json:"required"`
	RouteReasons               []RouteReason        `json:"route_reasons"`
	WakePolicy                 WakePolicy           `json:"wake_policy"`
	State                      MessageDeliveryState `json:"state"`
	AvailableAt                time.Time            `json:"available_at"`
	FirstSeenAt                *time.Time           `json:"first_seen_at,omitempty"`
	AckDueAt                   *time.Time           `json:"ack_due_at,omitempty"`
	ExpiresAt                  *time.Time           `json:"expires_at,omitempty"`
	AckID                      model.ID             `json:"ack_id,omitempty"`
	AcknowledgedAt             *time.Time           `json:"acknowledged_at,omitempty"`
	LastWakeVerdict            AssessmentVerdict    `json:"last_wake_verdict,omitempty"`
	LastWakeCode               string               `json:"last_wake_code,omitempty"`
	LastWakeAt                 *time.Time           `json:"last_wake_at,omitempty"`
	RetirementTombstoneKind    model.Kind           `json:"retirement_tombstone_kind,omitempty"`
	RetirementTombstoneID      model.ID             `json:"retirement_tombstone_id,omitempty"`
	RetirementTombstoneVersion int64                `json:"retirement_tombstone_version,omitempty"`
	RetirementEpoch            int64                `json:"retirement_epoch,omitempty"`
	UndeliverableAt            *time.Time           `json:"undeliverable_at,omitempty"`
	UndeliverableCode          string               `json:"undeliverable_code,omitempty"`
}

type InboxCursor struct {
	MutableCommunicationEntity
	Reader      RecipientRef `json:"reader"`
	MailboxKind MailboxKind  `json:"mailbox_kind"`
	MailboxRef  string       `json:"mailbox_ref"`
	LastSeenSeq int64        `json:"last_seen_seq"`
	LastSeenAt  time.Time    `json:"last_seen_at"`
	FilterHash  []byte       `json:"filter_hash"`
}

type InboxCursorBarrier struct {
	MutableCommunicationEntity
	Reader      RecipientRef       `json:"reader"`
	MailboxKind MailboxKind        `json:"mailbox_kind"`
	MailboxRef  string             `json:"mailbox_ref"`
	FilterHash  []byte             `json:"filter_hash"`
	DeliveryID  model.ID           `json:"delivery_id"`
	BarrierSeq  int64              `json:"barrier_seq"`
	Cause       CursorBarrierCause `json:"cause"`
	State       CursorBarrierState `json:"state"`
	ResolvedAt  *time.Time         `json:"resolved_at,omitempty"`
	ReasonCode  string             `json:"reason_code"`
}

type MessageAck struct {
	AppendOnlyCommunicationEntity
	DeliveryID     model.ID              `json:"delivery_id"`
	Kind           MessageAckKind        `json:"ack_kind"`
	Actor          CommunicationActorRef `json:"actor"`
	OnBehalfOf     *RecipientRef         `json:"on_behalf_of,omitempty"`
	Note           *ProtectedPayload     `json:"note,omitempty"`
	AcknowledgedAt time.Time             `json:"acknowledged_at"`
	Late           bool                  `json:"late"`
}

type CommunicationGuard struct {
	MutableCommunicationEntity
	Kind       CommunicationGuardKind `json:"guard_kind"`
	NextSeq    int64                  `json:"next_seq"`
	LastDBTime time.Time              `json:"last_db_time"`
}

type DecisionRequest struct {
	MutableCommunicationEntity
	MessageID            model.ID                `json:"message_id"`
	WorkItemID           model.ID                `json:"work_item_id"`
	DecisionKey          string                  `json:"decision_key"`
	Requester            CommunicationActorRef   `json:"requester"`
	Owner                CommunicationSubjectRef `json:"owner"`
	AcceptedDeliveryID   model.ID                `json:"accepted_delivery_id,omitempty"`
	State                DecisionRequestState    `json:"state"`
	Request              ProtectedPayload        `json:"request"`
	AuthorityRequirement string                  `json:"authority_requirement"`
	DueAt                time.Time               `json:"due_at"`
	AcceptedAt           *time.Time              `json:"accepted_at,omitempty"`
	BlockedCode          string                  `json:"blocked_code,omitempty"`
	TerminalCode         string                  `json:"terminal_code,omitempty"`
	ResolvedDecisionID   model.ID                `json:"resolved_decision_id,omitempty"`
	LastResponseSeq      int64                   `json:"last_response_seq"`
}

type DecisionResponse struct {
	AppendOnlyCommunicationEntity
	RequestID          model.ID              `json:"request_id"`
	ResponseSeq        int64                 `json:"response_seq"`
	FromState          DecisionRequestState  `json:"from_state"`
	ToState            DecisionRequestState  `json:"to_state"`
	Actor              CommunicationActorRef `json:"actor"`
	Response           ProtectedPayload      `json:"response"`
	AcceptedDeliveryID model.ID              `json:"accepted_delivery_id,omitempty"`
	BlockerWorkItemID  model.ID              `json:"blocker_work_item_id,omitempty"`
	WorkDecisionID     model.ID              `json:"work_decision_id,omitempty"`
	RespondedAt        time.Time             `json:"responded_at"`
}

// Handoff is the sole K3 transfer entity. It extends sessions_work_handoff;
// there is no parallel communication-only handoff aggregate.
type Handoff struct {
	MutableCommunicationEntity
	WorkItemID          model.ID          `json:"work_item_id"`
	MessageID           model.ID          `json:"message_id"`
	DeliveryID          model.ID          `json:"delivery_id"`
	From                RecipientRef      `json:"from"`
	FromOwnerEpoch      int64             `json:"from_owner_epoch"`
	To                  RecipientRef      `json:"to"`
	OfferedLeaseFence   int64             `json:"offered_lease_fence,omitempty"`
	ContextEventSeq     int64             `json:"context_event_seq"`
	ContextHash         []byte            `json:"context_hash"`
	Payload             ProtectedPayload  `json:"handoff"`
	State               HandoffState      `json:"state"`
	AckDeadline         time.Time         `json:"ack_deadline"`
	AckID               model.ID          `json:"ack_id,omitempty"`
	AcceptedAt          *time.Time        `json:"accepted_at,omitempty"`
	RejectedAt          *time.Time        `json:"rejected_at,omitempty"`
	WithdrawnAt         *time.Time        `json:"withdrawn_at,omitempty"`
	ExpiredAt           *time.Time        `json:"expired_at,omitempty"`
	TerminalCode        string            `json:"terminal_code,omitempty"`
	TerminalReason      *ProtectedPayload `json:"terminal_reason,omitempty"`
	ResultingLeaseFence int64             `json:"resulting_lease_fence,omitempty"`
}

type DeliveryDispatch struct {
	MutableCommunicationEntity
	DeliveryID                   model.ID              `json:"delivery_id"`
	RootDispatchID               model.ID              `json:"root_dispatch_id"`
	PredecessorID                model.ID              `json:"predecessor_id,omitempty"`
	EndpointID                   model.ID              `json:"endpoint_id"`
	EndpointGeneration           int64                 `json:"endpoint_generation"`
	RouteRuleID                  model.ID              `json:"route_rule_id,omitempty"`
	RouteRuleGeneration          int64                 `json:"route_rule_generation,omitempty"`
	DispatchGeneration           int64                 `json:"dispatch_generation"`
	RerouteRung                  int64                 `json:"reroute_rung"`
	PolicyGeneration             int64                 `json:"policy_generation"`
	State                        DeliveryDispatchState `json:"state"`
	AttemptCount                 int64                 `json:"attempt_count"`
	NextAttemptAt                *time.Time            `json:"next_attempt_at,omitempty"`
	ClaimOwner                   string                `json:"claim_owner,omitempty"`
	ClaimUntil                   *time.Time            `json:"claim_until,omitempty"`
	IdempotencyKeyHash           []byte                `json:"idempotency_key_hash"`
	LastVerdict                  AssessmentVerdict     `json:"last_verdict,omitempty"`
	LastCode                     string                `json:"last_code,omitempty"`
	ResolutionDeadlineAt         *time.Time            `json:"resolution_deadline_at,omitempty"`
	ResolutionCode               string                `json:"resolution_code,omitempty"`
	ReconciledAttemptID          model.ID              `json:"reconciled_attempt_id,omitempty"`
	ReconciledEndpointID         model.ID              `json:"reconciled_endpoint_id,omitempty"`
	ReconciledEndpointGeneration int64                 `json:"reconciled_endpoint_generation,omitempty"`
	ReconciliationVerdict        AssessmentVerdict     `json:"reconciliation_verdict,omitempty"`
	ReconciliationCode           string                `json:"reconciliation_code,omitempty"`
	ReconciliationEvidenceRef    string                `json:"reconciliation_evidence_ref,omitempty"`
	ReconciliationObservedAt     *time.Time            `json:"reconciliation_observed_at,omitempty"`
	ProviderAcceptanceHash       []byte                `json:"provider_acceptance_hash,omitempty"`
	SettledAt                    *time.Time            `json:"settled_at,omitempty"`
}

type DeliveryAttempt struct {
	MutableCommunicationEntity
	DispatchID          model.ID             `json:"dispatch_id"`
	AttemptSeq          int64                `json:"attempt_seq"`
	State               DeliveryAttemptState `json:"state"`
	StartedAt           time.Time            `json:"started_at"`
	TransmitBoundary    TransmitBoundary     `json:"transmit_boundary"`
	FinishedAt          *time.Time           `json:"finished_at,omitempty"`
	Verdict             AssessmentVerdict    `json:"verdict,omitempty"`
	Code                string               `json:"code,omitempty"`
	ProviderReceiptHash []byte               `json:"provider_receipt_hash,omitempty"`
	RequestHash         []byte               `json:"request_hash"`
}

// InboxCursorReceiptProjection is the closed response body retained for one
// durable cursor advance. It carries no token, authority detail or content.
type InboxCursorReceiptProjection struct {
	LastSeenSeq       int64              `json:"last_seen_seq"`
	BarrierDeliveryID model.ID           `json:"barrier_delivery_id,omitempty"`
	BarrierReason     CursorBarrierCause `json:"barrier_reason,omitempty"`
}

// CommunicationCommandResponseProjection is the closed, non-sensitive shape
// retained by an idempotency receipt. Human content and ProtectedPayload have
// no field in this type.
type CommunicationCommandResponseProjection struct {
	IDs         map[string]model.ID           `json:"ids,omitempty"`
	Version     int64                         `json:"version,omitempty"`
	State       string                        `json:"state,omitempty"`
	Counts      map[string]int64              `json:"counts,omitempty"`
	Digests     map[string][]byte             `json:"digests,omitempty"`
	InboxCursor *InboxCursorReceiptProjection `json:"inbox_cursor,omitempty"`
}

type CommunicationCommandReceipt struct {
	AppendOnlyCommunicationEntity
	CommandID              model.ID                               `json:"command_id"`
	ActorFingerprint       []byte                                 `json:"actor_fingerprint"`
	CommandScope           string                                 `json:"command_scope"`
	IdempotencyKeyHash     []byte                                 `json:"idempotency_key_hash"`
	RequestDigest          []byte                                 `json:"request_digest"`
	SealKeyVersion         string                                 `json:"seal_key_version,omitempty"`
	DigestKeyVersion       string                                 `json:"digest_key_version,omitempty"`
	PlanHash               []byte                                 `json:"plan_hash"`
	ResultKind             string                                 `json:"result_kind"`
	ResultID               model.ID                               `json:"result_id,omitempty"`
	HTTPStatus             int                                    `json:"http_status"`
	ResponseProjectionJSON CommunicationCommandResponseProjection `json:"response_projection_json"`
	ResponseDigest         []byte                                 `json:"response_digest"`
	EventID                model.ID                               `json:"event_id,omitempty"`
	AuditSeq               int64                                  `json:"audit_seq"`
	AuditHash              []byte                                 `json:"audit_hash"`
	CompletedAt            time.Time                              `json:"completed_at"`
}

// CommunicationPrincipal is constructed from core auth state, never from a
// request body. AgentExternalID is intentionally not called AgentRef: core auth
// carries an external identity string, which must be resolved to a canonical K3
// recipient through an authoritative tri-state lookup before persistence.
type CommunicationPrincipal struct {
	UserID             model.ID `json:"-"`
	AgentExternalID    string   `json:"-"`
	SessionID          string   `json:"-"`
	SessionRunRef      string   `json:"-"`
	SessionFence       int64    `json:"-"`
	SessionWorkspaceID model.ID `json:"-"`
	PurposeRestricted  bool     `json:"-"`
	System             bool     `json:"-"`
	SystemActorRef     string   `json:"-"`
	SystemGrantAgentID model.ID `json:"-"`
}

// DirectoryScopeRef is derived by the service from the tenant-bound Channel or
// resource. In particular, workspace_members cannot be resolved from a tenant
// alone, and neither value is accepted from a command body.
type DirectoryScopeRef struct {
	TenantID    model.TenantID `json:"tenant_id"`
	WorkspaceID model.ID       `json:"workspace_id"`
}

type DirectorySnapshot struct {
	Scope         DirectoryScopeRef              `json:"scope"`
	Epoch         int64                          `json:"epoch"`
	Selectors     []AudienceSelector             `json:"selectors"`
	Recipients    []RecipientSnapshot            `json:"recipients"`
	Contributions []ResolvedAudienceContribution `json:"contributions"`
	RosterHash    []byte                         `json:"roster_hash"`
	ObservedAt    time.Time                      `json:"observed_at"`
	FreshUntil    time.Time                      `json:"fresh_until"`
}

type RecipientSnapshot struct {
	Scope          DirectoryScopeRef                `json:"scope"`
	Recipient      RecipientRef                     `json:"recipient"`
	RecipientEpoch int64                            `json:"recipient_epoch"`
	DirectoryEpoch int64                            `json:"directory_epoch"`
	Eligible       bool                             `json:"eligible"`
	Tombstone      *store.DirectoryTombstoneWitness `json:"tombstone,omitempty"`
}

type PrincipalResolutionOutcome string

const (
	PrincipalResolved PrincipalResolutionOutcome = "resolved"
	PrincipalNotFound PrincipalResolutionOutcome = "not_found"
	PrincipalUnknown  PrincipalResolutionOutcome = "unknown"
)

// PrincipalResolution is the authoritative tri-state bridge from core auth's
// external AgentIdentity to a canonical K3 recipient. It never relabels the
// external string itself as an Identity or Agent UUID.
type PrincipalResolution struct {
	Outcome    PrincipalResolutionOutcome `json:"outcome"`
	Code       string                     `json:"code"`
	Scope      DirectoryScopeRef          `json:"scope"`
	Principal  CommunicationPrincipal     `json:"-"`
	Recipient  *RecipientSnapshot         `json:"recipient,omitempty"`
	ObservedAt time.Time                  `json:"observed_at"`
	FreshUntil time.Time                  `json:"fresh_until"`
}

// ResolvedAudienceContribution preserves the selector-to-recipient arc and its
// authoritative directory fact before routing adds route/subscription
// provenance. A deduplicated recipient list alone cannot construct the
// append-only MessageAudienceRecipient rows.
type ResolvedAudienceContribution struct {
	SelectorOrdinal        int64                       `json:"selector_ordinal"`
	Selector               AudienceSelector            `json:"selector"`
	Recipient              RecipientSnapshot           `json:"recipient"`
	Required               bool                        `json:"required"`
	WakePolicy             WakePolicy                  `json:"wake_policy"`
	RouteReasons           []RouteReason               `json:"route_reasons"`
	RouteRuleID            model.ID                    `json:"route_rule_id,omitempty"`
	RouteRuleGeneration    int64                       `json:"route_rule_generation,omitempty"`
	CausalKind             AudienceCausalKind          `json:"causal_kind"`
	CausalRef              string                      `json:"causal_ref"`
	CausalFact             *store.AuthorizationFactRef `json:"causal_fact,omitempty"`
	ObservedSessionSID     string                      `json:"observed_session_sid,omitempty"`
	ObservedClaimFence     int64                       `json:"observed_claim_fence,omitempty"`
	OriginalSubscriber     *CommunicationSubjectRef    `json:"original_subscriber,omitempty"`
	SubscriptionID         model.ID                    `json:"subscription_id,omitempty"`
	SubscriptionGeneration int64                       `json:"subscription_generation,omitempty"`
}

// PublicationAudienceRequest binds the richer publication resolver to the
// exact Channel revisions observed by the service. The normative directory
// resolver remains available below for callers which do not own sessions
// subscription state.
type PublicationAudienceRequest struct {
	Scope                DirectoryScopeRef      `json:"scope"`
	ChannelID            model.ID               `json:"channel_id"`
	ChannelACLRevision   int64                  `json:"channel_acl_revision"`
	RouteRevision        int64                  `json:"route_revision"`
	SubscriptionRevision int64                  `json:"subscription_revision"`
	MessageKind          MessageKind            `json:"message_kind"`
	Urgency              MessageUrgency         `json:"urgency"`
	Sender               CommunicationActorRef  `json:"sender"`
	SourceKind           ChannelRouteSourceKind `json:"source_kind"`
	EventType            string                 `json:"event_type,omitempty"`
	LabelsJSON           json.RawMessage        `json:"labels_json,omitempty"`
	LabelsHash           []byte                 `json:"labels_hash,omitempty"`
	MentionedRecipients  []RecipientRef         `json:"mentioned_recipients,omitempty"`
	ChannelDefaultWake   WakePolicy             `json:"channel_default_wake"`
	ContentProtection    ContentProtection      `json:"content_protection"`
	ProtectionGeneration int64                  `json:"protection_generation"`
	RequestedAt          time.Time              `json:"requested_at"`
	Selectors            []AudienceSelector     `json:"selectors"`
}

// PublicationAudienceAttestation is the server-authored proof that the rich
// subscriber/route expansion was produced from one exact request and snapshot.
// Callers cannot freely choose effective required, wake or route provenance.
type PublicationAudienceAttestation struct {
	Scope          DirectoryScopeRef `json:"scope"`
	DirectoryEpoch int64             `json:"directory_epoch"`
	RequestHash    []byte            `json:"request_hash"`
	SnapshotHash   []byte            `json:"snapshot_hash"`
	ObservedAt     time.Time         `json:"observed_at"`
	FreshUntil     time.Time         `json:"fresh_until"`
	Evidence       AuthorityEvidence `json:"evidence"`
}

type EntityRef struct {
	TenantID    model.TenantID `json:"tenant_id"`
	Kind        model.Kind     `json:"kind"`
	ID          model.ID       `json:"id"`
	WorkspaceID model.ID       `json:"workspace_id"`
}

type ReadOutcome string

const (
	ReadAllow   ReadOutcome = "ALLOW"
	ReadDeny    ReadOutcome = "DENY"
	ReadUnknown ReadOutcome = "UNKNOWN"
)

// CommunicationOperation is the server-selected action presented to core.
// The entity kind plus this closed verb identifies the specific core
// permission; clients cannot downgrade a write to a read check.
type CommunicationOperation string

const (
	CommunicationRead                 CommunicationOperation = "read"
	CommunicationDeliveryWrite        CommunicationOperation = "sessions:delivery:write"
	CommunicationDeliveryAdmin        CommunicationOperation = "sessions:delivery:admin"
	CommunicationDecisionRequestWrite CommunicationOperation = "sessions:decision-request:write"
	CommunicationMessageSend          CommunicationOperation = "sessions:message-send:write"
	CommunicationHandoffResponse      CommunicationOperation = "sessions:handoff-response:write"
)

// ReadWitness contains only evidence a core-side adapter can produce without
// importing or reading the sessions namespace. Sessions composes it with the
// current ChannelGrant, recipient guard and audience causality.
type ReadWitness struct {
	Outcome        ReadOutcome                  `json:"outcome"`
	Code           string                       `json:"code"`
	Entity         EntityRef                    `json:"entity"`
	Operation      CommunicationOperation       `json:"operation"`
	Principal      CommunicationPrincipal       `json:"-"`
	ObservedAt     time.Time                    `json:"observed_at"`
	FreshUntil     time.Time                    `json:"fresh_until"`
	CorePermission AuthorityEvidence            `json:"core_permission"`
	ResourceGuard  AuthorityEvidence            `json:"resource_guard"`
	ForbidAbsence  AuthorityEvidence            `json:"forbid_absence"`
	Facts          []store.AuthorizationFactRef `json:"facts"`
	EvidenceRef    string                       `json:"evidence_ref,omitempty"`
}

type DirectorySnapshotResolver interface {
	ResolveAudience(context.Context, DirectoryScopeRef, []AudienceSelector) (DirectorySnapshot, error)
	ResolveRecipient(context.Context, DirectoryScopeRef, RecipientRef) (RecipientSnapshot, error)
	ResolvePrincipal(context.Context, DirectoryScopeRef, CommunicationPrincipal) (PrincipalResolution, error)
}

// PublicationAudienceAttestor composes core directory resolution with the
// Channel-local subscription snapshot. DirectorySnapshotResolver deliberately
// cannot expand subscribers because core neither owns ChannelID nor its
// subscription revision.
type PublicationAudienceAttestor interface {
	AttestPublicationAudience(context.Context, PublicationAudienceRequest) (
		DirectorySnapshot, PublicationAudienceAttestation, error)
}

type PublicationAudienceResolver = PublicationAudienceAttestor

// ChannelGrantSubjectClosureResolver is the server-only bridge from an auth
// principal to its current direct/group/session ChannelGrant subjects. WP-2
// must provide it; callers never assemble the subject closure from body data.
type ChannelGrantSubjectClosureResolver interface {
	ResolveChannelGrantSubjects(context.Context, DirectoryScopeRef, CommunicationPrincipal) (
		ChannelGrantSubjectClosure, error)
}

// CurrentAudienceSetReader runs inside Mutate after DB time is fixed and the
// Message is locked. It returns the complete audience projection, not a
// caller-selected subset for one Delivery.
type CurrentAudienceSetReader interface {
	ReadCurrentAudienceSet(
		context.Context, DirectoryScopeRef, model.ID, model.ID, RecipientRef, time.Time,
	) (CurrentAudienceSetWitness, error)
}

// MessageFulfillmentSnapshotReader must return the complete Delivery set and
// its same-transaction witness; a caller-provided count is not authoritative.
type MessageFulfillmentSnapshotReader interface {
	ReadMessageFulfillmentSnapshot(context.Context, DirectoryScopeRef, model.ID) (
		[]MessageDelivery, FulfillmentDeliverySetWitness, error)
}

type ChannelLabelSnapshotResolver interface {
	ResolveChannelLabels(context.Context, DirectoryScopeRef, model.ID) (ChannelLabelSnapshot, error)
}

type CoreEntityReadAuthorizer interface {
	AuthorizeEntityRead(context.Context, CommunicationPrincipal, EntityRef) (ReadWitness, error)
}

// CoreEntityOperationAuthorizer is the optional action-aware companion for
// writes such as Ack/seen/handoff response. It does not replace the normative
// core-only read port above.
type CoreEntityOperationAuthorizer interface {
	AuthorizeEntityOperation(context.Context, CommunicationPrincipal, EntityRef, CommunicationOperation) (ReadWitness, error)
}

// DispatchRouteAttestor reads the F-chosen durable representation and returns
// the complete route identity consumed by E's lineage planner.
type DispatchRouteAttestor interface {
	AttestDispatchRoute(context.Context, DirectoryScopeRef, model.ID) (DispatchRouteAttestation, error)
}

type CommunicationContentSealer interface {
	Seal(context.Context, ContentAAD, []byte) ([]byte, string, error)
	Open(context.Context, ContentAAD, []byte, string) ([]byte, error)
	Digest(context.Context, ContentAAD, []byte) ([]byte, string, error)
	VerifyDigest(context.Context, ContentAAD, []byte, []byte, string) (bool, error)
}

// CommunicationCredentialIssuer is the WP-1 name for the already implemented
// G seam. The alias prevents a second issuer contract with subtly different
// credential semantics.
type CommunicationCredentialSpec = CommunicationSessionCredentialRequest
type CommunicationCredential = CommunicationSessionCredential
type CommunicationCredentialIssuer = CommunicationSessionCredentialSource

// DispatchRouteIdentity is the exact durable tuple selected by routing.
type DispatchRouteIdentity struct {
	EndpointID          model.ID `json:"endpoint_id"`
	EndpointGeneration  int64    `json:"endpoint_generation"`
	RouteRuleID         model.ID `json:"route_rule_id,omitempty"`
	RouteRuleGeneration int64    `json:"route_rule_generation,omitempty"`
	PolicyGeneration    int64    `json:"policy_generation"`
}
