// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// ProtocolBindingStore is the private K5 persistence port used by the
// composition-root A2A and MCP adapters. Public REST access is implemented by
// the sessions module itself and never exposes these effecting methods.
type ProtocolBindingStore interface {
	PlanProtocolBindingSpec(context.Context, model.TenantID, ProtocolBindingSpecCommand) (ProtocolBindingSpecPlan, error)
	ApplyProtocolBindingSpec(context.Context, model.TenantID, ProtocolBindingSpecCommand) (ProtocolBindingSpecResult, error)
	ReserveProtocolBinding(context.Context, model.TenantID, ProtocolBindingReservation) (ProtocolBinding, error)
	SettleProtocolBinding(context.Context, model.TenantID, ProtocolBindingSettlement) (ProtocolBinding, error)
	ObserveProtocolBinding(context.Context, model.TenantID, ProtocolBindingObservation) (ProtocolBinding, error)
	RequestProtocolBindingCancel(context.Context, model.TenantID, ProtocolBindingCancelIntent) (ProtocolBinding, error)
	GetProtocolBinding(context.Context, model.TenantID, ProtocolBindingRef) (ProtocolBinding, error)
	ListProtocolBindings(context.Context, model.TenantID, ProtocolBindingQuery) (ProtocolBindingPage, error)
}

var _ ProtocolBindingStore = (*Module)(nil)

var (
	ErrInvalidProtocolBinding  = errors.New("sessions: invalid protocol binding")
	ErrProtocolBindingConflict = errors.New("sessions: protocol binding conflict")
	ErrProtocolBindingNotFound = errors.New("sessions: protocol binding not found")
	ErrProtocolBindingUnknown  = errors.New("sessions: protocol binding evidence unavailable")
)

type BindingProtocol string

const (
	BindingProtocolA2A BindingProtocol = "a2a"
	BindingProtocolMCP BindingProtocol = "mcp"
)

type BindingDirection string

const (
	BindingInbound       BindingDirection = "inbound"
	BindingOutbound      BindingDirection = "outbound"
	BindingBidirectional BindingDirection = "bidirectional"
)

type BindingLocalKind string

const (
	BindingLocalWorkItem BindingLocalKind = "work_item"
	BindingLocalAgent    BindingLocalKind = "agent"
	BindingLocalModel    BindingLocalKind = "model"
	BindingLocalChannel  BindingLocalKind = "channel"
)

type BindingCurrencyPolicy string

const BindingCurrencyPinned BindingCurrencyPolicy = "pinned"

type ProtocolBindingSpecState string

const (
	ProtocolBindingSpecDraft      ProtocolBindingSpecState = "draft"
	ProtocolBindingSpecActive     ProtocolBindingSpecState = "active"
	ProtocolBindingSpecDisabled   ProtocolBindingSpecState = "disabled"
	ProtocolBindingSpecSuperseded ProtocolBindingSpecState = "superseded"
)

type ProtocolBindingSpecOperation string

const (
	ProtocolBindingSpecCreateDraft ProtocolBindingSpecOperation = "draft"
	ProtocolBindingSpecActivate    ProtocolBindingSpecOperation = "activate"
	ProtocolBindingSpecDisable     ProtocolBindingSpecOperation = "disable"
)

// ProtocolObservationVerdict deliberately uses the connector-facing English
// vocabulary. Adapters map it to the product AssessmentVerdict at their public
// boundary; UNKNOWN can never be collapsed into CLEAN.
type ProtocolObservationVerdict string

const (
	ProtocolObservationClean   ProtocolObservationVerdict = "CLEAN"
	ProtocolObservationBroken  ProtocolObservationVerdict = "BROKEN"
	ProtocolObservationUnknown ProtocolObservationVerdict = "UNKNOWN"
)

type ProtocolBindingResultKind string

const (
	ProtocolBindingResultTask    ProtocolBindingResultKind = "task"
	ProtocolBindingResultMessage ProtocolBindingResultKind = "message"
	// ProtocolBindingResultTaskOrMessage is valid only while reserving the A2A
	// SendMessage response union. Settlement always pins one concrete kind.
	ProtocolBindingResultTaskOrMessage ProtocolBindingResultKind = "task_or_message"
)

type ProtocolMappingCardinality string

const (
	ProtocolMappingOneToOne  ProtocolMappingCardinality = "one_to_one"
	ProtocolMappingOneToMany ProtocolMappingCardinality = "one_to_many"
	ProtocolMappingManyToOne ProtocolMappingCardinality = "many_to_one"
)

type ProtocolMappingTransform string

const (
	ProtocolTransformIdentity  ProtocolMappingTransform = "identity"
	ProtocolTransformText      ProtocolMappingTransform = "text"
	ProtocolTransformReference ProtocolMappingTransform = "reference"
	ProtocolTransformMetadata  ProtocolMappingTransform = "metadata"
	ProtocolTransformStatus    ProtocolMappingTransform = "status"
)

// ProtocolMappingRule is deliberately declarative. No JavaScript, template,
// command, URL fetch, or other executable transform can enter the durable spec.
type ProtocolMappingRule struct {
	Source      string                     `json:"source"`
	Target      string                     `json:"target"`
	Cardinality ProtocolMappingCardinality `json:"cardinality"`
	Transform   ProtocolMappingTransform   `json:"transform"`
}

// ProtocolBindingLoss records a known semantic loss and its explicit witness.
// An active generation may contain no unaccepted loss.
type ProtocolBindingLoss struct {
	Field         string `json:"field"`
	ReasonCode    string `json:"reason_code"`
	Accepted      bool   `json:"accepted"`
	AcceptanceRef string `json:"acceptance_ref,omitempty"`
}

type ProtocolBindingValidation struct {
	Verdict    ProtocolObservationVerdict `json:"verdict"`
	Code       string                     `json:"code"`
	ObservedAt time.Time                  `json:"observed_at,omitempty"`
}

// ProtocolBindingSpecInput is desired configuration for exactly one immutable
// generation. LocalSelector is canonicalized JSON; mapping and loss lists are
// sorted and canonicalized before hashing and persistence.
type ProtocolBindingSpecInput struct {
	WorkspaceID          model.ID                  `json:"workspace_id"`
	BindingKey           string                    `json:"binding_key"`
	Generation           int64                     `json:"generation"`
	Protocol             BindingProtocol           `json:"protocol"`
	ProtocolVersion      string                    `json:"protocol_version"`
	Direction            BindingDirection          `json:"direction"`
	LocalKind            BindingLocalKind          `json:"local_kind"`
	LocalSelector        json.RawMessage           `json:"local_selector"`
	PeerAuthority        string                    `json:"peer_authority"`
	RemoteResourceKind   string                    `json:"remote_resource_kind"`
	RemoteResourceRef    string                    `json:"remote_resource_ref"`
	MappingSchema        string                    `json:"mapping_schema"`
	Mapping              []ProtocolMappingRule     `json:"mapping"`
	KnownLosses          []ProtocolBindingLoss     `json:"known_losses"`
	RuleRefs             []string                  `json:"rule_refs"`
	PermissionProfileRef string                    `json:"permission_profile_ref"`
	CurrencyPolicy       BindingCurrencyPolicy     `json:"currency_policy"`
	Validation           ProtocolBindingValidation `json:"validation"`
	SupersedesID         model.ID                  `json:"supersedes_id,omitempty"`
}

type ProtocolBindingSpecCommand struct {
	Operation        ProtocolBindingSpecOperation `json:"operation"`
	WorkspaceID      model.ID                     `json:"workspace_id"`
	SpecID           model.ID                     `json:"spec_id,omitempty"`
	ExpectedVersion  int64                        `json:"expected_version,omitempty"`
	Input            *ProtocolBindingSpecInput    `json:"input,omitempty"`
	IdempotencyKey   string                       `json:"-"`
	ExpectedPlanHash string                       `json:"-"`
	// validationOverride is populated only by the sessions HTTP boundary after
	// a server-owned K5 validator has observed the configured protocol peer.
	validationOverride *ProtocolBindingValidation
}

type ProtocolBindingSpec struct {
	MutableCommunicationEntity
	BindingKey           string                    `json:"binding_key"`
	Generation           int64                     `json:"generation"`
	Protocol             BindingProtocol           `json:"protocol"`
	ProtocolVersion      string                    `json:"protocol_version"`
	Direction            BindingDirection          `json:"direction"`
	LocalKind            BindingLocalKind          `json:"local_kind"`
	LocalSelector        json.RawMessage           `json:"local_selector"`
	PeerAuthority        string                    `json:"peer_authority"`
	RemoteResourceKind   string                    `json:"remote_resource_kind"`
	RemoteResourceRef    string                    `json:"remote_resource_ref"`
	MappingSchema        string                    `json:"mapping_schema"`
	Mapping              []ProtocolMappingRule     `json:"mapping"`
	MappingHash          []byte                    `json:"mapping_hash"`
	KnownLosses          []ProtocolBindingLoss     `json:"known_losses"`
	LossesHash           []byte                    `json:"losses_hash"`
	RuleRefs             []string                  `json:"rule_refs"`
	PermissionProfileRef string                    `json:"permission_profile_ref"`
	CurrencyPolicy       BindingCurrencyPolicy     `json:"currency_policy"`
	Validation           ProtocolBindingValidation `json:"validation"`
	State                ProtocolBindingSpecState  `json:"state"`
	SupersedesID         model.ID                  `json:"supersedes_id,omitempty"`
	SpecHash             []byte                    `json:"spec_hash"`
	PlanHash             []byte                    `json:"plan_hash"`
}

type ProtocolBindingSpecPlan struct {
	Verdict       ProtocolObservationVerdict   `json:"verdict"`
	Code          string                       `json:"code"`
	Validation    ProtocolBindingValidation    `json:"validation"`
	PlanHash      string                       `json:"plan_hash"`
	Operation     ProtocolBindingSpecOperation `json:"operation"`
	WorkspaceID   model.ID                     `json:"workspace_id"`
	SpecID        model.ID                     `json:"spec_id,omitempty"`
	Generation    int64                        `json:"generation"`
	PriorActiveID model.ID                     `json:"prior_active_id,omitempty"`
	SpecHash      string                       `json:"spec_hash"`
	MappingHash   string                       `json:"mapping_hash"`
	LossesHash    string                       `json:"losses_hash"`
}

type ProtocolBindingSpecResult struct {
	ProtocolBindingSpecPlan
	Spec     ProtocolBindingSpec `json:"spec"`
	Replayed bool                `json:"replayed"`
}

// ProtocolBindingReservation is the complete pre-transmit commitment. The
// active spec supplies protocol/version/mapping/losses; the request cannot
// weaken those pins. DispatchKey is a stable semantic idempotency key.
type ProtocolBindingReservation struct {
	WorkspaceID           model.ID                   `json:"workspace_id"`
	BindingSpecID         model.ID                   `json:"binding_spec_id"`
	BindingSpecGeneration int64                      `json:"binding_spec_generation"`
	ExpectedDirection     BindingDirection           `json:"expected_direction"`
	WorkItemID            model.ID                   `json:"work_item_id,omitempty"`
	MessageID             model.ID                   `json:"message_id,omitempty"`
	DeliveryID            model.ID                   `json:"delivery_id,omitempty"`
	AttemptID             model.ID                   `json:"attempt_id,omitempty"`
	DispatchKey           string                     `json:"dispatch_key"`
	ExpectedExternalKind  string                     `json:"expected_external_kind"`
	ExpectedExternalID    string                     `json:"expected_external_id,omitempty"`
	Generation            int64                      `json:"generation"`
	OwnerKind             string                     `json:"owner_kind,omitempty"`
	OwnerRef              string                     `json:"owner_ref,omitempty"`
	OwnerDigest           []byte                     `json:"owner_digest,omitempty"`
	OwnerEpoch            int64                      `json:"owner_epoch,omitempty"`
	LeaseFence            int64                      `json:"lease_fence,omitempty"`
	MCPTask               *ProtocolMCPTaskProjection `json:"mcp_task,omitempty"`
	// ProtocolMetadataJSON is the canonical durable projection consumed by
	// composition adapters after restart. For MCP it decodes exactly as MCPTask.
	ProtocolMetadataJSON json.RawMessage `json:"protocol_metadata_json,omitempty"`
}

// ProtocolMCPTaskOwner is the connector-neutral copy of the MCP canonical
// owner tuple. It lets cmd reconstruct connector state without creating a
// second MCP Task authority inside sessions.
type ProtocolMCPTaskOwner struct {
	Subject     string `json:"subject"`
	IsDelegated bool   `json:"is_delegated"`
	ActAs       string `json:"act_as,omitempty"`
	Issuer      string `json:"issuer"`
	ClientID    string `json:"client_id"`
}

// ProtocolMCPTaskProjection is immutable registration intent. External ID and
// current status remain first-class ProtocolBinding fields.
type ProtocolMCPTaskProjection struct {
	Owner                ProtocolMCPTaskOwner          `json:"owner"`
	Tool                 string                        `json:"tool"`
	RequiredScope        string                        `json:"required_scope"`
	Destructive          bool                          `json:"destructive"`
	CreatedAt            time.Time                     `json:"created_at"`
	TTLMs                *int64                        `json:"ttl_ms,omitempty"`
	PollIntervalMs       *int64                        `json:"poll_interval_ms,omitempty"`
	InitialStatus        string                        `json:"initial_status"`
	InitialStatusReason  string                        `json:"initial_status_reason,omitempty"`
	UpstreamDescriptor   string                        `json:"upstream_descriptor"`
	ProtocolRevision     string                        `json:"protocol_revision"`
	OriginOperationID    string                        `json:"origin_operation_id"`
	OriginEffectDigest   string                        `json:"origin_effect_digest"`
	InitialInputRequests []ProtocolInterruptRequestRef `json:"initial_input_requests,omitempty"`
}

type ProtocolBindingSettlement struct {
	BindingID         model.ID                   `json:"binding_id"`
	Generation        int64                      `json:"generation"`
	ExpectedVersion   int64                      `json:"expected_version"`
	DispatchKey       string                     `json:"dispatch_key"`
	ResultKind        ProtocolBindingResultKind  `json:"result_kind"`
	ExternalID        string                     `json:"external_id,omitempty"`
	ContextID         string                     `json:"context_id,omitempty"`
	ExternalMessageID string                     `json:"external_message_id,omitempty"`
	LocalState        string                     `json:"local_state"`
	RemoteState       string                     `json:"remote_state"`
	RemoteRevision    string                     `json:"remote_revision,omitempty"`
	Verdict           ProtocolObservationVerdict `json:"verdict"`
	Code              string                     `json:"code"`
	Observed          bool                       `json:"observed"`
	DetailHash        []byte                     `json:"detail_hash,omitempty"`
	TTLMs             *int64                     `json:"ttl_ms,omitempty"`
	PollIntervalMs    *int64                     `json:"poll_interval_ms,omitempty"`
	Terminal          bool                       `json:"terminal"`
}

type ProtocolBindingObservation struct {
	BindingID         model.ID                   `json:"binding_id"`
	Generation        int64                      `json:"generation"`
	ExpectedVersion   int64                      `json:"expected_version"`
	SemanticKey       string                     `json:"semantic_key"`
	PeerAuthority     string                     `json:"peer_authority"`
	ExternalID        string                     `json:"external_id,omitempty"`
	ContextID         string                     `json:"context_id,omitempty"`
	ExternalMessageID string                     `json:"external_message_id,omitempty"`
	LocalState        string                     `json:"local_state"`
	RemoteState       string                     `json:"remote_state"`
	RemoteRevision    string                     `json:"remote_revision,omitempty"`
	Verdict           ProtocolObservationVerdict `json:"verdict"`
	Code              string                     `json:"code"`
	Observed          bool                       `json:"observed"`
	DetailHash        []byte                     `json:"detail_hash,omitempty"`
	TTLMs             *int64                     `json:"ttl_ms,omitempty"`
	PollIntervalMs    *int64                     `json:"poll_interval_ms,omitempty"`
	Terminal          bool                       `json:"terminal"`
}

type ProtocolBindingCancelIntent struct {
	BindingID       model.ID `json:"binding_id"`
	Generation      int64    `json:"generation"`
	ExpectedVersion int64    `json:"expected_version"`
	SemanticKey     string   `json:"semantic_key"`
	ReasonCode      string   `json:"reason_code"`
}

type ProtocolBinding struct {
	MutableCommunicationEntity
	BindingSpecID         model.ID                   `json:"binding_spec_id"`
	BindingSpecGeneration int64                      `json:"binding_spec_generation"`
	PinnedSpecHash        []byte                     `json:"pinned_spec_hash"`
	PinnedMappingHash     []byte                     `json:"pinned_mapping_hash"`
	PinnedLossesHash      []byte                     `json:"pinned_losses_hash"`
	WorkItemID            model.ID                   `json:"work_item_id,omitempty"`
	MessageID             model.ID                   `json:"message_id,omitempty"`
	DeliveryID            model.ID                   `json:"delivery_id,omitempty"`
	Protocol              BindingProtocol            `json:"protocol"`
	ProtocolVersion       string                     `json:"protocol_version"`
	Direction             BindingDirection           `json:"direction"`
	PeerAuthority         string                     `json:"peer_authority"`
	RemoteResourceRef     string                     `json:"remote_resource_ref"`
	AttemptID             model.ID                   `json:"attempt_id"`
	Generation            int64                      `json:"generation"`
	SyntheticSID          string                     `json:"synthetic_sid"`
	OwnerKind             string                     `json:"owner_kind,omitempty"`
	OwnerRef              string                     `json:"owner_ref,omitempty"`
	OwnerDigest           []byte                     `json:"owner_digest,omitempty"`
	OwnerEpoch            int64                      `json:"owner_epoch,omitempty"`
	LeaseFence            int64                      `json:"lease_fence,omitempty"`
	ExternalKind          string                     `json:"external_kind"`
	ExternalID            string                     `json:"external_id,omitempty"`
	ContextID             string                     `json:"context_id,omitempty"`
	ExternalMessageID     string                     `json:"external_message_id,omitempty"`
	LocalState            string                     `json:"local_state"`
	RemoteState           string                     `json:"remote_state"`
	RemoteRevision        string                     `json:"remote_revision,omitempty"`
	ObservationVerdict    ProtocolObservationVerdict `json:"observation_verdict"`
	ObservationCode       string                     `json:"observation_code"`
	LastObservedAt        *time.Time                 `json:"last_observed_at,omitempty"`
	DetailHash            []byte                     `json:"detail_hash,omitempty"`
	CurrentTTLMs          *int64                     `json:"current_ttl_ms,omitempty"`
	CurrentPollIntervalMs *int64                     `json:"current_poll_interval_ms,omitempty"`
	Terminal              bool                       `json:"terminal"`
	CancelRequested       bool                       `json:"cancel_requested"`
	CancelRequestedAt     *time.Time                 `json:"cancel_requested_at,omitempty"`
	CancelReasonCode      string                     `json:"cancel_reason_code,omitempty"`
	MCPTask               *ProtocolMCPTaskProjection `json:"mcp_task,omitempty"`
	MCPTaskHash           []byte                     `json:"mcp_task_hash,omitempty"`
	ProtocolMetadataJSON  json.RawMessage            `json:"protocol_metadata_json,omitempty"`
	LastCommandID         model.ID                   `json:"last_command_id"`
	LastEventID           model.ID                   `json:"last_event_id"`
	LastEventSeq          int64                      `json:"last_event_seq"`
	// Replayed is transport metadata: it is true only when Reserve returned an
	// already committed reservation. Callers must transmit only when false.
	Replayed bool `json:"-"`
}

// ProtocolBindingRef selects either an exact local ID or an exact external
// generation. WorkspaceID may be zero only for an exact ID lookup; external
// selectors always require it.
type ProtocolBindingRef struct {
	WorkspaceID   model.ID        `json:"workspace_id"`
	ID            model.ID        `json:"id,omitempty"`
	Protocol      BindingProtocol `json:"protocol,omitempty"`
	PeerAuthority string          `json:"peer_authority,omitempty"`
	ExternalKind  string          `json:"external_kind,omitempty"`
	ExternalID    string          `json:"external_id,omitempty"`
	// Generation zero resolves the one non-terminal current external binding.
	Generation int64 `json:"generation,omitempty"`
}

type ProtocolBindingQuery struct {
	WorkspaceID   model.ID                   `json:"workspace_id"`
	BindingSpecID model.ID                   `json:"binding_spec_id,omitempty"`
	WorkItemID    model.ID                   `json:"work_item_id,omitempty"`
	Protocol      BindingProtocol            `json:"protocol,omitempty"`
	PeerAuthority string                     `json:"peer_authority,omitempty"`
	OwnerKind     string                     `json:"owner_kind,omitempty"`
	OwnerRef      string                     `json:"owner_ref,omitempty"`
	ExternalKind  string                     `json:"external_kind,omitempty"`
	ExternalID    string                     `json:"external_id,omitempty"`
	Verdict       ProtocolObservationVerdict `json:"verdict,omitempty"`
	Terminal      *bool                      `json:"terminal,omitempty"`
	Limit         int                        `json:"limit,omitempty"`
	Cursor        string                     `json:"cursor,omitempty"`
}

type ProtocolBindingPage struct {
	Items      api.JSONArray[ProtocolBinding] `json:"items"`
	NextCursor string                         `json:"next_cursor,omitempty"`
	HasMore    bool                           `json:"has_more"`
}
