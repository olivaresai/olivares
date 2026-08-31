// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// DeliveryDigestSize is the byte length of every digest carried by the
// communication-delivery driver contract. Drivers receive digests, never the
// request payload or a provider receipt body.
const DeliveryDigestSize = sha256.Size

const (
	maxDeliveryIdentifierBytes  = 512
	maxDeliveryTokenBytes       = 128
	maxDeliveryEvidenceRefBytes = 512
)

var (
	// ErrInvalidDeliveryDispatch means a dispatch is incomplete, non-canonical, or
	// carries an invalid immutable binding.
	ErrInvalidDeliveryDispatch = errors.New("invalid communication delivery dispatch")
	// ErrInvalidDeliveryAttemptResult means an attempt outcome, transmit boundary,
	// and provider receipt hash do not form one of the closed legal products.
	ErrInvalidDeliveryAttemptResult = errors.New("invalid communication delivery attempt result")
	// ErrInvalidDeliveryCapabilityWitness means a witness is not bound to a valid
	// endpoint identity or contains an unknown capability bit.
	ErrInvalidDeliveryCapabilityWitness = errors.New("invalid communication delivery capability witness")
	// ErrInvalidDeliveryReconciliation means a reconciliation request is not bound
	// to a valid dispatch.
	ErrInvalidDeliveryReconciliation = errors.New("invalid communication delivery reconciliation")
	// ErrInvalidDeliveryReconciliationResult means a reconciliation verdict and its
	// evidence do not form one of the closed legal products.
	ErrInvalidDeliveryReconciliationResult = errors.New("invalid communication delivery reconciliation result")
)

// DeliveryMessageKind is the closed message-kind vocabulary a wake may reveal.
// It is routing metadata, not message content.
type DeliveryMessageKind string

const (
	// DeliveryMessageKindNotice is a directed informational message.
	DeliveryMessageKindNotice DeliveryMessageKind = "notice"
	// DeliveryMessageKindAnnouncement is a channel announcement.
	DeliveryMessageKindAnnouncement DeliveryMessageKind = "announcement"
	// DeliveryMessageKindRequest asks a recipient to act on a WorkItem.
	DeliveryMessageKindRequest DeliveryMessageKind = "request"
	// DeliveryMessageKindDecisionRequest asks the recipient for a governed decision.
	DeliveryMessageKindDecisionRequest DeliveryMessageKind = "decision_request"
	// DeliveryMessageKindHandoffOffer offers transfer of WorkItem ownership.
	DeliveryMessageKindHandoffOffer DeliveryMessageKind = "handoff_offer"
	// DeliveryMessageKindSystem is a server-originated coordination message.
	DeliveryMessageKindSystem DeliveryMessageKind = "system"
)

// Valid reports whether k is in the closed message-kind vocabulary.
func (k DeliveryMessageKind) Valid() bool {
	switch k {
	case DeliveryMessageKindNotice,
		DeliveryMessageKindAnnouncement,
		DeliveryMessageKindRequest,
		DeliveryMessageKindDecisionRequest,
		DeliveryMessageKindHandoffOffer,
		DeliveryMessageKindSystem:
		return true
	default:
		return false
	}
}

// DeliveryUrgency is the closed urgency vocabulary a wake may reveal.
type DeliveryUrgency string

const (
	// DeliveryUrgencyNormal is the default delivery priority.
	DeliveryUrgencyNormal DeliveryUrgency = "normal"
	// DeliveryUrgencyHigh requests elevated operator attention.
	DeliveryUrgencyHigh DeliveryUrgency = "high"
	// DeliveryUrgencyCritical requests immediate operator attention.
	DeliveryUrgencyCritical DeliveryUrgency = "critical"
)

// Valid reports whether u is in the closed urgency vocabulary.
func (u DeliveryUrgency) Valid() bool {
	switch u {
	case DeliveryUrgencyNormal, DeliveryUrgencyHigh, DeliveryUrgencyCritical:
		return true
	default:
		return false
	}
}

// DeliveryDispatchParams contains the immutable, payload-free input for one
// external delivery attempt.
//
// The surface intentionally has no subject, body, sender-authored instructions,
// ProtectedPayload, URL, credential, secret reference, bearer, or provider response.
// A driver may emit only a fixed Olivares wake containing the identifiers and closed
// metadata below; the recipient fetches content through the authenticated API.
// EndpointFingerprint and RequestDigest are raw SHA-256 digest bytes. The service,
// never the driver, derives RequestDigest from the canonical complete wake binding and
// verifies it before/after the callback. Both digests are copied by NewDeliveryDispatch
// and are never references to caller-owned memory.
type DeliveryDispatchParams struct {
	TenantID            string
	WorkspaceID         string
	DeliveryID          string
	MessageID           string
	DispatchID          string
	AttemptID           string
	EndpointID          string
	EndpointGeneration  uint64
	EndpointFingerprint []byte
	Provider            string
	Transport           string
	OperationID         OperationID
	RequestDigest       []byte
	MessageKind         DeliveryMessageKind
	Urgency             DeliveryUrgency
	WorkItemID          string
	AckDueAt            time.Time
}

// DeliveryDispatch is the immutable driver request for exactly one durable
// dispatch attempt. Its fields are private so a validated request cannot be rebound
// after construction. Slice accessors and copies never alias its internal storage.
//
// OperationID is the provider-facing idempotency identity. Reusing it for any
// different immutable binding is a conflict, even if RequestDigest is unchanged.
// Every field in this request participates in DeliveryIdempotencyIdentity.
type DeliveryDispatch struct {
	tenantID            string
	workspaceID         string
	deliveryID          string
	messageID           string
	dispatchID          string
	attemptID           string
	endpointID          string
	endpointGeneration  uint64
	endpointFingerprint []byte
	provider            string
	transport           string
	operationID         OperationID
	requestDigest       []byte
	messageKind         DeliveryMessageKind
	urgency             DeliveryUrgency
	workItemID          string
	ackDueAt            time.Time
}

// NewDeliveryDispatch validates and defensively copies a payload-free dispatch.
func NewDeliveryDispatch(p DeliveryDispatchParams) (DeliveryDispatch, error) {
	d := DeliveryDispatch{
		tenantID:            p.TenantID,
		workspaceID:         p.WorkspaceID,
		deliveryID:          p.DeliveryID,
		messageID:           p.MessageID,
		dispatchID:          p.DispatchID,
		attemptID:           p.AttemptID,
		endpointID:          p.EndpointID,
		endpointGeneration:  p.EndpointGeneration,
		endpointFingerprint: cloneDeliveryBytes(p.EndpointFingerprint),
		provider:            p.Provider,
		transport:           p.Transport,
		operationID:         p.OperationID,
		requestDigest:       cloneDeliveryBytes(p.RequestDigest),
		messageKind:         p.MessageKind,
		urgency:             p.Urgency,
		workItemID:          p.WorkItemID,
		ackDueAt:            canonicalDeliveryTime(p.AckDueAt),
	}
	if err := d.Validate(); err != nil {
		return DeliveryDispatch{}, err
	}
	return d, nil
}

// Validate checks every immutable dispatch binding. It never includes field values
// in errors, so malformed external identifiers cannot become log content through a
// validation error.
func (d DeliveryDispatch) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"tenant_id", d.tenantID},
		{"workspace_id", d.workspaceID},
		{"delivery_id", d.deliveryID},
		{"message_id", d.messageID},
		{"dispatch_id", d.dispatchID},
		{"attempt_id", d.attemptID},
		{"endpoint_id", d.endpointID},
	} {
		if !validDeliveryIdentifier(field.value, false) {
			return fmt.Errorf("%w: %s", ErrInvalidDeliveryDispatch, field.name)
		}
	}
	if d.endpointGeneration == 0 {
		return fmt.Errorf("%w: endpoint_generation", ErrInvalidDeliveryDispatch)
	}
	if !validDeliveryDigest(d.endpointFingerprint) {
		return fmt.Errorf("%w: endpoint_fingerprint", ErrInvalidDeliveryDispatch)
	}
	if !validDeliveryToken(d.provider) {
		return fmt.Errorf("%w: provider", ErrInvalidDeliveryDispatch)
	}
	if !validDeliveryToken(d.transport) {
		return fmt.Errorf("%w: transport", ErrInvalidDeliveryDispatch)
	}
	if !validDeliveryIdentifier(string(d.operationID), false) {
		return fmt.Errorf("%w: operation_id", ErrInvalidDeliveryDispatch)
	}
	if !validDeliveryDigest(d.requestDigest) {
		return fmt.Errorf("%w: request_digest", ErrInvalidDeliveryDispatch)
	}
	if !d.messageKind.Valid() {
		return fmt.Errorf("%w: message_kind", ErrInvalidDeliveryDispatch)
	}
	if !d.urgency.Valid() {
		return fmt.Errorf("%w: urgency", ErrInvalidDeliveryDispatch)
	}
	if !validDeliveryIdentifier(d.workItemID, true) {
		return fmt.Errorf("%w: work_item_id", ErrInvalidDeliveryDispatch)
	}
	if !d.ackDueAt.IsZero() && canonicalDeliveryTime(d.ackDueAt) != d.ackDueAt {
		return fmt.Errorf("%w: ack_due_at", ErrInvalidDeliveryDispatch)
	}
	return nil
}

// Params returns a defensive snapshot suitable for reconstructing this request.
func (d DeliveryDispatch) Params() DeliveryDispatchParams {
	return DeliveryDispatchParams{
		TenantID:            d.tenantID,
		WorkspaceID:         d.workspaceID,
		DeliveryID:          d.deliveryID,
		MessageID:           d.messageID,
		DispatchID:          d.dispatchID,
		AttemptID:           d.attemptID,
		EndpointID:          d.endpointID,
		EndpointGeneration:  d.endpointGeneration,
		EndpointFingerprint: cloneDeliveryBytes(d.endpointFingerprint),
		Provider:            d.provider,
		Transport:           d.transport,
		OperationID:         d.operationID,
		RequestDigest:       cloneDeliveryBytes(d.requestDigest),
		MessageKind:         d.messageKind,
		Urgency:             d.urgency,
		WorkItemID:          d.workItemID,
		AckDueAt:            d.ackDueAt,
	}
}

// Clone returns an independent copy of d.
func (d DeliveryDispatch) Clone() DeliveryDispatch {
	d.endpointFingerprint = cloneDeliveryBytes(d.endpointFingerprint)
	d.requestDigest = cloneDeliveryBytes(d.requestDigest)
	return d
}

// TenantID returns the durable tenant binding.
func (d DeliveryDispatch) TenantID() string { return d.tenantID }

// WorkspaceID returns the durable workspace binding.
func (d DeliveryDispatch) WorkspaceID() string { return d.workspaceID }

// DeliveryID returns the MessageDelivery identifier being woken.
func (d DeliveryDispatch) DeliveryID() string { return d.deliveryID }

// MessageID returns the immutable message identifier.
func (d DeliveryDispatch) MessageID() string { return d.messageID }

// DispatchID returns the durable dispatch-generation identifier.
func (d DeliveryDispatch) DispatchID() string { return d.dispatchID }

// AttemptID returns the single-assignment attempt identifier.
func (d DeliveryDispatch) AttemptID() string { return d.attemptID }

// EndpointID returns the frozen endpoint identifier.
func (d DeliveryDispatch) EndpointID() string { return d.endpointID }

// EndpointGeneration returns the frozen endpoint generation.
func (d DeliveryDispatch) EndpointGeneration() uint64 { return d.endpointGeneration }

// EndpointFingerprint returns a defensive copy of the frozen SHA-256 fingerprint.
func (d DeliveryDispatch) EndpointFingerprint() []byte {
	return cloneDeliveryBytes(d.endpointFingerprint)
}

// Provider returns the registered provider key.
func (d DeliveryDispatch) Provider() string { return d.provider }

// Transport returns the registered transport key.
func (d DeliveryDispatch) Transport() string { return d.transport }

// OperationID returns the stable provider-facing idempotency identity.
func (d DeliveryDispatch) OperationID() OperationID { return d.operationID }

// RequestDigest returns a defensive copy of the SHA-256 digest of the full
// payload-free wake request binding.
func (d DeliveryDispatch) RequestDigest() []byte {
	return cloneDeliveryBytes(d.requestDigest)
}

// MessageKind returns the closed message kind exposed by the wake.
func (d DeliveryDispatch) MessageKind() DeliveryMessageKind { return d.messageKind }

// Urgency returns the closed urgency exposed by the wake.
func (d DeliveryDispatch) Urgency() DeliveryUrgency { return d.urgency }

// WorkItemID returns the optional governed WorkItem reference.
func (d DeliveryDispatch) WorkItemID() string { return d.workItemID }

// AckDueAt returns the optional acknowledgement deadline in canonical UTC form.
func (d DeliveryDispatch) AckDueAt() time.Time { return d.ackDueAt }

// DeliveryEndpointParams contains the immutable, payload-free identity presented
// to a notifier before a dispatch is claimed. EndpointFingerprint is copied.
type DeliveryEndpointParams struct {
	TenantID            string
	WorkspaceID         string
	EndpointID          string
	EndpointGeneration  uint64
	EndpointFingerprint []byte
	Provider            string
	Transport           string
}

// DeliveryEndpointIdentity binds a capability witness to the exact endpoint
// generation and configuration fingerprint that a dispatch will use.
type DeliveryEndpointIdentity struct {
	tenantID            string
	workspaceID         string
	endpointID          string
	endpointGeneration  uint64
	endpointFingerprint []byte
	provider            string
	transport           string
}

// NewDeliveryEndpointIdentity validates and defensively copies an endpoint identity.
func NewDeliveryEndpointIdentity(p DeliveryEndpointParams) (DeliveryEndpointIdentity, error) {
	e := DeliveryEndpointIdentity{
		tenantID:            p.TenantID,
		workspaceID:         p.WorkspaceID,
		endpointID:          p.EndpointID,
		endpointGeneration:  p.EndpointGeneration,
		endpointFingerprint: cloneDeliveryBytes(p.EndpointFingerprint),
		provider:            p.Provider,
		transport:           p.Transport,
	}
	if err := e.Validate(); err != nil {
		return DeliveryEndpointIdentity{}, err
	}
	return e, nil
}

// Validate checks the full endpoint-generation binding.
func (e DeliveryEndpointIdentity) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"tenant_id", e.tenantID},
		{"workspace_id", e.workspaceID},
		{"endpoint_id", e.endpointID},
	} {
		if !validDeliveryIdentifier(field.value, false) {
			return fmt.Errorf("%w: endpoint %s", ErrInvalidDeliveryCapabilityWitness, field.name)
		}
	}
	if e.endpointGeneration == 0 {
		return fmt.Errorf("%w: endpoint generation", ErrInvalidDeliveryCapabilityWitness)
	}
	if !validDeliveryDigest(e.endpointFingerprint) {
		return fmt.Errorf("%w: endpoint fingerprint", ErrInvalidDeliveryCapabilityWitness)
	}
	if !validDeliveryToken(e.provider) {
		return fmt.Errorf("%w: endpoint provider", ErrInvalidDeliveryCapabilityWitness)
	}
	if !validDeliveryToken(e.transport) {
		return fmt.Errorf("%w: endpoint transport", ErrInvalidDeliveryCapabilityWitness)
	}
	return nil
}

// Params returns a defensive endpoint-identity snapshot.
func (e DeliveryEndpointIdentity) Params() DeliveryEndpointParams {
	return DeliveryEndpointParams{
		TenantID:            e.tenantID,
		WorkspaceID:         e.workspaceID,
		EndpointID:          e.endpointID,
		EndpointGeneration:  e.endpointGeneration,
		EndpointFingerprint: cloneDeliveryBytes(e.endpointFingerprint),
		Provider:            e.provider,
		Transport:           e.transport,
	}
}

// Clone returns an independent endpoint-identity copy.
func (e DeliveryEndpointIdentity) Clone() DeliveryEndpointIdentity {
	e.endpointFingerprint = cloneDeliveryBytes(e.endpointFingerprint)
	return e
}

// TenantID returns the endpoint tenant binding.
func (e DeliveryEndpointIdentity) TenantID() string { return e.tenantID }

// WorkspaceID returns the endpoint workspace binding.
func (e DeliveryEndpointIdentity) WorkspaceID() string { return e.workspaceID }

// EndpointID returns the endpoint identifier.
func (e DeliveryEndpointIdentity) EndpointID() string { return e.endpointID }

// EndpointGeneration returns the frozen endpoint generation.
func (e DeliveryEndpointIdentity) EndpointGeneration() uint64 { return e.endpointGeneration }

// EndpointFingerprint returns a defensive copy of the SHA-256 fingerprint.
func (e DeliveryEndpointIdentity) EndpointFingerprint() []byte {
	return cloneDeliveryBytes(e.endpointFingerprint)
}

// Provider returns the registered provider key.
func (e DeliveryEndpointIdentity) Provider() string { return e.provider }

// Transport returns the registered transport key.
func (e DeliveryEndpointIdentity) Transport() string { return e.transport }

// Equal reports whether both identities name the exact same endpoint generation
// and configuration fingerprint.
func (e DeliveryEndpointIdentity) Equal(other DeliveryEndpointIdentity) bool {
	if e.Validate() != nil || other.Validate() != nil {
		return false
	}
	return equalDeliveryEndpointIdentity(e, other)
}

// EndpointIdentity returns the exact endpoint binding carried by d.
func (d DeliveryDispatch) EndpointIdentity() DeliveryEndpointIdentity {
	return DeliveryEndpointIdentity{
		tenantID:            d.tenantID,
		workspaceID:         d.workspaceID,
		endpointID:          d.endpointID,
		endpointGeneration:  d.endpointGeneration,
		endpointFingerprint: cloneDeliveryBytes(d.endpointFingerprint),
		provider:            d.provider,
		transport:           d.transport,
	}
}

// DeliveryCapability is one member of the closed notifier-capability vocabulary.
type DeliveryCapability uint8

const (
	// DeliveryCapabilityWake allows a notifier to emit a payload-free wake.
	DeliveryCapabilityWake DeliveryCapability = 1 << iota
	// DeliveryCapabilityReconcile allows explicit observation after an
	// indeterminate attempt.
	DeliveryCapabilityReconcile
	// DeliveryCapabilityIdempotency means the provider honors OperationID for the
	// exact RequestDigest binding.
	DeliveryCapabilityIdempotency
	// DeliveryCapabilityActiveTurn means the transport can surface the wake into an
	// active governed session turn.
	DeliveryCapabilityActiveTurn
)

const allDeliveryCapabilities = DeliveryCapabilityWake |
	DeliveryCapabilityReconcile |
	DeliveryCapabilityIdempotency |
	DeliveryCapabilityActiveTurn

// Valid reports whether c names exactly one known capability.
func (c DeliveryCapability) Valid() bool {
	return c == DeliveryCapabilityWake ||
		c == DeliveryCapabilityReconcile ||
		c == DeliveryCapabilityIdempotency ||
		c == DeliveryCapabilityActiveTurn
}

// String returns the stable capability vocabulary.
func (c DeliveryCapability) String() string {
	switch c {
	case DeliveryCapabilityWake:
		return "wake"
	case DeliveryCapabilityReconcile:
		return "reconcile"
	case DeliveryCapabilityIdempotency:
		return "idempotency"
	case DeliveryCapabilityActiveTurn:
		return "active_turn"
	default:
		return "invalid"
	}
}

// DeliveryCapabilities is a closed bit set. Every combination is representable:
// capabilities are independent observations and are never inferred from Provider,
// Transport, or one another. In particular, a provider name cannot imply Wake.
type DeliveryCapabilities struct {
	mask DeliveryCapability
}

// NewDeliveryCapabilities constructs an explicit capability set.
func NewDeliveryCapabilities(wake, reconcile, idempotency, activeTurn bool) DeliveryCapabilities {
	var mask DeliveryCapability
	if wake {
		mask |= DeliveryCapabilityWake
	}
	if reconcile {
		mask |= DeliveryCapabilityReconcile
	}
	if idempotency {
		mask |= DeliveryCapabilityIdempotency
	}
	if activeTurn {
		mask |= DeliveryCapabilityActiveTurn
	}
	return DeliveryCapabilities{mask: mask}
}

// Validate rejects capability bits added without a versioned SDK change.
func (c DeliveryCapabilities) Validate() error {
	if c.mask&^allDeliveryCapabilities != 0 {
		return fmt.Errorf("%w: unknown capability", ErrInvalidDeliveryCapabilityWitness)
	}
	return nil
}

// Has reports whether c explicitly contains capability. Unknown and compound values
// never match.
func (c DeliveryCapabilities) Has(capability DeliveryCapability) bool {
	return capability.Valid() && c.Validate() == nil && c.mask&capability != 0
}

// Wake reports the explicit wake capability.
func (c DeliveryCapabilities) Wake() bool { return c.Has(DeliveryCapabilityWake) }

// Reconcile reports the explicit reconciliation capability.
func (c DeliveryCapabilities) Reconcile() bool { return c.Has(DeliveryCapabilityReconcile) }

// Idempotency reports the explicit provider-idempotency capability.
func (c DeliveryCapabilities) Idempotency() bool { return c.Has(DeliveryCapabilityIdempotency) }

// ActiveTurn reports the explicit active-turn capability.
func (c DeliveryCapabilities) ActiveTurn() bool { return c.Has(DeliveryCapabilityActiveTurn) }

// DeliveryCapabilityWitness binds an explicit capability set to the exact endpoint
// generation and fingerprint that was inspected. The zero value is invalid.
type DeliveryCapabilityWitness struct {
	endpoint     DeliveryEndpointIdentity
	capabilities DeliveryCapabilities
}

// NewDeliveryCapabilityWitness validates and defensively copies a witness.
func NewDeliveryCapabilityWitness(
	endpoint DeliveryEndpointIdentity,
	capabilities DeliveryCapabilities,
) (DeliveryCapabilityWitness, error) {
	w := DeliveryCapabilityWitness{
		endpoint:     endpoint.Clone(),
		capabilities: capabilities,
	}
	if err := w.Validate(); err != nil {
		return DeliveryCapabilityWitness{}, err
	}
	return w, nil
}

// Validate checks both the endpoint binding and the closed capability mask.
func (w DeliveryCapabilityWitness) Validate() error {
	if err := w.endpoint.Validate(); err != nil {
		return fmt.Errorf("%w: endpoint binding", ErrInvalidDeliveryCapabilityWitness)
	}
	// El `if` de arriba SÍ hace falta: envuelve con ErrInvalidDeliveryCapabilityWitness. Éste no
	// añadía nada, así que devuelve directo y la asimetría —contexto para el endpoint, error propio
	// para las capacidades— se conserva tal cual estaba.
	return w.capabilities.Validate()
}

// Endpoint returns a defensive copy of the witnessed endpoint identity.
func (w DeliveryCapabilityWitness) Endpoint() DeliveryEndpointIdentity {
	return w.endpoint.Clone()
}

// Capabilities returns the closed witnessed capability set.
func (w DeliveryCapabilityWitness) Capabilities() DeliveryCapabilities {
	return w.capabilities
}

// Matches reports whether the witness applies to the exact endpoint generation and
// fingerprint the caller is about to use.
func (w DeliveryCapabilityWitness) Matches(endpoint DeliveryEndpointIdentity) bool {
	return w.Validate() == nil && w.endpoint.Equal(endpoint)
}

// Clone returns an independent witness copy.
func (w DeliveryCapabilityWitness) Clone() DeliveryCapabilityWitness {
	w.endpoint = w.endpoint.Clone()
	return w
}

// NormalizeDeliveryCapabilityWitness applies the fail-closed callback rule. A
// driver error or invalid witness returns the invalid zero value. The error text is
// deliberately neither inspected nor copied. A valid witness is defensively copied.
func NormalizeDeliveryCapabilityWitness(
	witness DeliveryCapabilityWitness,
	err error,
) DeliveryCapabilityWitness {
	if err != nil || witness.Validate() != nil {
		return DeliveryCapabilityWitness{}
	}
	return witness.Clone()
}

// UsableDeliveryCapabilityWitness applies the fail-closed callback rule. A driver
// error invalidates a simultaneous witness without inspecting the error text; a nil
// error is insufficient unless the witness is valid and bound to expected exactly.
func UsableDeliveryCapabilityWitness(
	expected DeliveryEndpointIdentity,
	witness DeliveryCapabilityWitness,
	err error,
) bool {
	return NormalizeDeliveryCapabilityWitness(witness, err).Matches(expected)
}

// DeliveryIdempotencyIdentity is the complete immutable binding of one driver
// invocation. It deliberately contains the whole DeliveryDispatch, not merely
// OperationID and RequestDigest: the same operation ID cannot be rebound to a
// different endpoint, attempt, message, closed metadata, or deadline.
type DeliveryIdempotencyIdentity struct {
	dispatch DeliveryDispatch
}

// IdempotencyIdentity returns a defensive, complete identity for d.
func (d DeliveryDispatch) IdempotencyIdentity() DeliveryIdempotencyIdentity {
	return DeliveryIdempotencyIdentity{dispatch: d.Clone()}
}

// Validate checks that the identity contains a complete valid request.
func (i DeliveryIdempotencyIdentity) Validate() error {
	return i.dispatch.Validate()
}

// Dispatch returns a defensive copy of the exact request bound by the identity.
func (i DeliveryIdempotencyIdentity) Dispatch() DeliveryDispatch {
	return i.dispatch.Clone()
}

// Equal reports whether two identities bind every request field identically.
func (i DeliveryIdempotencyIdentity) Equal(other DeliveryIdempotencyIdentity) bool {
	if i.Validate() != nil || other.Validate() != nil {
		return false
	}
	return equalDeliveryDispatch(i.dispatch, other.dispatch)
}

// SameOperationKey reports whether two valid identities compete for the same
// tenant-scoped OperationID. If this is true while Equal is false, the caller must
// refuse the rebind instead of emitting either request.
func (i DeliveryIdempotencyIdentity) SameOperationKey(other DeliveryIdempotencyIdentity) bool {
	if i.Validate() != nil || other.Validate() != nil {
		return false
	}
	return i.dispatch.tenantID == other.dispatch.tenantID &&
		i.dispatch.operationID == other.dispatch.operationID
}

// Conflicts reports whether the same tenant-scoped OperationID was rebound to any
// different immutable request field.
func (i DeliveryIdempotencyIdentity) Conflicts(other DeliveryIdempotencyIdentity) bool {
	return i.SameOperationKey(other) && !i.Equal(other)
}

// DeliveryTransmitBoundary states what the driver proved about crossing the
// external transmission boundary. The zero value is deliberately unknown.
type DeliveryTransmitBoundary uint8

const (
	// DeliveryBoundaryUnknown means the driver cannot prove whether any bytes or
	// equivalent provider-side effect crossed the boundary.
	DeliveryBoundaryUnknown DeliveryTransmitBoundary = iota
	// DeliveryBoundaryCrossed means the provider accepted the request and returned
	// the receipt whose digest accompanies the result.
	DeliveryBoundaryCrossed
	// DeliveryBoundaryNotCrossed means the driver proved refusal before any external
	// transmission or equivalent provider-side effect.
	DeliveryBoundaryNotCrossed
)

// Valid reports whether b is in the closed boundary vocabulary.
func (b DeliveryTransmitBoundary) Valid() bool {
	return b == DeliveryBoundaryUnknown || b == DeliveryBoundaryCrossed || b == DeliveryBoundaryNotCrossed
}

// String returns the stable ledger vocabulary for b.
func (b DeliveryTransmitBoundary) String() string {
	switch b {
	case DeliveryBoundaryUnknown:
		return "unknown"
	case DeliveryBoundaryCrossed:
		return "crossed"
	case DeliveryBoundaryNotCrossed:
		return "not_crossed"
	default:
		return "invalid"
	}
}

// DeliveryAttemptOutcome is the closed result of one external driver invocation.
// Its zero value is deliberately indeterminate.
type DeliveryAttemptOutcome uint8

const (
	// DeliveryAttemptIndeterminate means acceptance and the boundary cannot be
	// established. It is quiescent and must never trigger a blind retry.
	DeliveryAttemptIndeterminate DeliveryAttemptOutcome = iota
	// DeliveryAttemptAccepted means the provider demonstrably accepted the wake and
	// returned a receipt represented only by its SHA-256 hash. It proves neither that
	// the recipient read or acknowledged the delivery nor that it took any action.
	DeliveryAttemptAccepted
	// DeliveryAttemptRefusedBeforeBoundary means the driver proved that the request
	// did not cross the external boundary. This is the only attempt result eligible
	// for a safe successor retry.
	DeliveryAttemptRefusedBeforeBoundary
)

// Valid reports whether o is in the closed attempt-outcome vocabulary.
func (o DeliveryAttemptOutcome) Valid() bool {
	return o == DeliveryAttemptIndeterminate ||
		o == DeliveryAttemptAccepted ||
		o == DeliveryAttemptRefusedBeforeBoundary
}

// String returns the stable ledger vocabulary for o.
func (o DeliveryAttemptOutcome) String() string {
	switch o {
	case DeliveryAttemptIndeterminate:
		return "indeterminate"
	case DeliveryAttemptAccepted:
		return "accepted"
	case DeliveryAttemptRefusedBeforeBoundary:
		return "refused_before_boundary"
	default:
		return "invalid"
	}
}

// DeliveryAttemptResult is one of exactly three products:
//
//   - indeterminate + unknown boundary + no receipt hash;
//   - accepted + crossed boundary + a SHA-256 provider receipt hash;
//   - refused-before-boundary + not-crossed boundary + no receipt hash.
//
// The zero value is the first product and therefore fails safely. No raw provider
// response or error text can be stored in this type.
type DeliveryAttemptResult struct {
	outcome             DeliveryAttemptOutcome
	boundary            DeliveryTransmitBoundary
	providerReceiptHash []byte
}

// NewDeliveryAttemptResult validates and defensively copies a closed attempt result.
// On validation failure it returns the safe zero/indeterminate result plus an error.
func NewDeliveryAttemptResult(
	outcome DeliveryAttemptOutcome,
	boundary DeliveryTransmitBoundary,
	providerReceiptHash []byte,
) (DeliveryAttemptResult, error) {
	r := DeliveryAttemptResult{
		outcome:             outcome,
		boundary:            boundary,
		providerReceiptHash: cloneDeliveryBytes(providerReceiptHash),
	}
	if err := r.Validate(); err != nil {
		return DeliveryAttemptResult{}, err
	}
	return r, nil
}

// Validate checks the exact closed outcome/boundary/receipt cross-product.
func (r DeliveryAttemptResult) Validate() error {
	if !r.outcome.Valid() {
		return fmt.Errorf("%w: outcome", ErrInvalidDeliveryAttemptResult)
	}
	if !r.boundary.Valid() {
		return fmt.Errorf("%w: boundary", ErrInvalidDeliveryAttemptResult)
	}
	hasReceipt := len(r.providerReceiptHash) != 0
	switch r.outcome {
	case DeliveryAttemptIndeterminate:
		if r.boundary != DeliveryBoundaryUnknown || hasReceipt {
			return fmt.Errorf("%w: indeterminate product", ErrInvalidDeliveryAttemptResult)
		}
	case DeliveryAttemptAccepted:
		if r.boundary != DeliveryBoundaryCrossed || !validDeliveryDigest(r.providerReceiptHash) {
			return fmt.Errorf("%w: accepted product", ErrInvalidDeliveryAttemptResult)
		}
	case DeliveryAttemptRefusedBeforeBoundary:
		if r.boundary != DeliveryBoundaryNotCrossed || hasReceipt {
			return fmt.Errorf("%w: refusal product", ErrInvalidDeliveryAttemptResult)
		}
	}
	return nil
}

// Outcome returns the closed attempt outcome.
func (r DeliveryAttemptResult) Outcome() DeliveryAttemptOutcome { return r.outcome }

// Boundary returns the closed external-boundary verdict.
func (r DeliveryAttemptResult) Boundary() DeliveryTransmitBoundary { return r.boundary }

// ProviderReceiptHash returns a defensive copy of the SHA-256 acceptance receipt hash.
func (r DeliveryAttemptResult) ProviderReceiptHash() []byte {
	return cloneDeliveryBytes(r.providerReceiptHash)
}

// Clone returns an independent result copy.
func (r DeliveryAttemptResult) Clone() DeliveryAttemptResult {
	r.providerReceiptHash = cloneDeliveryBytes(r.providerReceiptHash)
	return r
}

// NormalizeDeliveryAttemptResult applies the fail-closed callback rule. A driver
// error or invalid result returns the safe zero/indeterminate product. The error text
// is deliberately neither inspected nor copied. A valid result is defensively copied.
func NormalizeDeliveryAttemptResult(result DeliveryAttemptResult, err error) DeliveryAttemptResult {
	if err != nil || result.Validate() != nil {
		return DeliveryAttemptResult{}
	}
	return result.Clone()
}

// DeliveryReconciliation is a payload-free request to observe the exact immutable
// dispatch attempt after an indeterminate boundary result.
type DeliveryReconciliation struct {
	identity DeliveryIdempotencyIdentity
}

// NewDeliveryReconciliation binds reconciliation to every immutable dispatch field.
func NewDeliveryReconciliation(dispatch DeliveryDispatch) (DeliveryReconciliation, error) {
	if err := dispatch.Validate(); err != nil {
		return DeliveryReconciliation{}, fmt.Errorf("%w: %v", ErrInvalidDeliveryReconciliation, err)
	}
	return DeliveryReconciliation{identity: dispatch.IdempotencyIdentity()}, nil
}

// Validate checks that reconciliation is bound to a complete valid dispatch.
func (r DeliveryReconciliation) Validate() error {
	if err := r.identity.Validate(); err != nil {
		return fmt.Errorf("%w: dispatch binding", ErrInvalidDeliveryReconciliation)
	}
	return nil
}

// Dispatch returns a defensive copy of the exact request being reconciled.
func (r DeliveryReconciliation) Dispatch() DeliveryDispatch {
	return r.identity.Dispatch()
}

// IdempotencyIdentity returns a defensive copy of the exact reconciliation binding.
func (r DeliveryReconciliation) IdempotencyIdentity() DeliveryIdempotencyIdentity {
	return DeliveryIdempotencyIdentity{dispatch: r.identity.dispatch.Clone()}
}

// Clone returns an independent reconciliation request.
func (r DeliveryReconciliation) Clone() DeliveryReconciliation {
	return DeliveryReconciliation{identity: r.IdempotencyIdentity()}
}

// DeliveryReconciliationOutcome is the closed result of explicitly observing an
// indeterminate provider boundary. The zero value remains indeterminate.
type DeliveryReconciliationOutcome uint8

const (
	// DeliveryReconciliationIndeterminate means the driver still cannot establish
	// acceptance. The dispatch remains quiescent.
	DeliveryReconciliationIndeterminate DeliveryReconciliationOutcome = iota
	// DeliveryReconciliationAccepted means evidence proves provider acceptance of the
	// wake. It never proves recipient read, Ack, decision, handoff, or WorkItem action.
	DeliveryReconciliationAccepted
	// DeliveryReconciliationNotAccepted means evidence proves the provider did not
	// accept the request.
	DeliveryReconciliationNotAccepted
)

// Valid reports whether o is in the closed reconciliation vocabulary.
func (o DeliveryReconciliationOutcome) Valid() bool {
	return o == DeliveryReconciliationIndeterminate ||
		o == DeliveryReconciliationAccepted ||
		o == DeliveryReconciliationNotAccepted
}

// String returns the stable ledger vocabulary for o.
func (o DeliveryReconciliationOutcome) String() string {
	switch o {
	case DeliveryReconciliationIndeterminate:
		return "indeterminate"
	case DeliveryReconciliationAccepted:
		return "accepted"
	case DeliveryReconciliationNotAccepted:
		return "not_accepted"
	default:
		return "invalid"
	}
}

// DeliveryReconciliationResult is one of exactly three products:
//
//   - indeterminate, with no evidence ref and no receipt hash;
//   - accepted, with a bounded evidence ref and SHA-256 provider receipt hash;
//   - not accepted, with a bounded evidence ref and no receipt hash.
//
// EvidenceRef is an opaque, non-sensitive handle to evidence already obtained by
// the adapter. It is never provider response text and never a credential. The service
// records its own DB time after the callback; no driver timestamp is authoritative.
type DeliveryReconciliationResult struct {
	outcome             DeliveryReconciliationOutcome
	providerReceiptHash []byte
	evidenceRef         string
}

// NewDeliveryReconciliationResult validates and defensively copies a closed
// reconciliation result. On failure it returns the safe zero result plus an error.
func NewDeliveryReconciliationResult(
	outcome DeliveryReconciliationOutcome,
	providerReceiptHash []byte,
	evidenceRef string,
) (DeliveryReconciliationResult, error) {
	r := DeliveryReconciliationResult{
		outcome:             outcome,
		providerReceiptHash: cloneDeliveryBytes(providerReceiptHash),
		evidenceRef:         evidenceRef,
	}
	if err := r.Validate(); err != nil {
		return DeliveryReconciliationResult{}, err
	}
	return r, nil
}

// Validate checks the exact closed outcome/evidence/receipt cross-product.
func (r DeliveryReconciliationResult) Validate() error {
	if !r.outcome.Valid() {
		return fmt.Errorf("%w: outcome", ErrInvalidDeliveryReconciliationResult)
	}
	hasReceipt := len(r.providerReceiptHash) != 0
	hasEvidence := r.evidenceRef != ""
	switch r.outcome {
	case DeliveryReconciliationIndeterminate:
		if hasReceipt || hasEvidence {
			return fmt.Errorf("%w: indeterminate product", ErrInvalidDeliveryReconciliationResult)
		}
	case DeliveryReconciliationAccepted:
		if !validDeliveryDigest(r.providerReceiptHash) || !validDeliveryEvidenceRef(r.evidenceRef) {
			return fmt.Errorf("%w: accepted product", ErrInvalidDeliveryReconciliationResult)
		}
	case DeliveryReconciliationNotAccepted:
		if hasReceipt || !validDeliveryEvidenceRef(r.evidenceRef) {
			return fmt.Errorf("%w: not-accepted product", ErrInvalidDeliveryReconciliationResult)
		}
	}
	return nil
}

// Outcome returns the closed reconciliation outcome.
func (r DeliveryReconciliationResult) Outcome() DeliveryReconciliationOutcome { return r.outcome }

// ProviderReceiptHash returns a defensive copy of the SHA-256 acceptance receipt hash.
func (r DeliveryReconciliationResult) ProviderReceiptHash() []byte {
	return cloneDeliveryBytes(r.providerReceiptHash)
}

// EvidenceRef returns the bounded opaque evidence handle.
func (r DeliveryReconciliationResult) EvidenceRef() string { return r.evidenceRef }

// Clone returns an independent reconciliation-result copy.
func (r DeliveryReconciliationResult) Clone() DeliveryReconciliationResult {
	r.providerReceiptHash = cloneDeliveryBytes(r.providerReceiptHash)
	return r
}

// NormalizeDeliveryReconciliationResult applies the fail-closed callback rule. A
// driver error or invalid result returns the safe zero/indeterminate product. The
// error text is deliberately neither inspected nor copied. A valid result is copied.
func NormalizeDeliveryReconciliationResult(
	result DeliveryReconciliationResult,
	err error,
) DeliveryReconciliationResult {
	if err != nil || result.Validate() != nil {
		return DeliveryReconciliationResult{}
	}
	return result.Clone()
}

// DeliveryNotifier is the payload-free external wake driver seam.
//
// Capabilities is a side-effect-free local witness operation: it MUST perform no
// network call and no external I/O. The caller checks its endpoint binding before claim
// and again before I/O; a missing/mismatched/invalid witness refuses the dispatch.
// Provider and Transport names never imply a capability.
//
// Notify and Reconcile execute only after the caller has committed its claim/attempt
// transaction. If any method returns err != nil, the caller MUST ignore any simultaneous
// witness/result and classify the observation as unavailable or indeterminate/unknown.
// The error is volatile diagnostic material: callers MUST NOT persist it, copy it into
// evidence, or log it without their normal redaction boundary. A nil error still requires
// result.Validate; an invalid result is also indeterminate/unknown. Only
// RefusedBeforeBoundary proves a safe retry boundary.
type DeliveryNotifier interface {
	Capabilities(context.Context, DeliveryEndpointIdentity) (DeliveryCapabilityWitness, error)
	Notify(context.Context, DeliveryDispatch) (DeliveryAttemptResult, error)
	Reconcile(context.Context, DeliveryReconciliation) (DeliveryReconciliationResult, error)
}

func cloneDeliveryBytes(in []byte) []byte {
	if in == nil {
		return nil
	}
	return bytes.Clone(in)
}

func validDeliveryDigest(digest []byte) bool {
	if len(digest) != DeliveryDigestSize {
		return false
	}
	var nonZero byte
	for _, b := range digest {
		nonZero |= b
	}
	return nonZero != 0
}

func validDeliveryIdentifier(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	if !utf8.ValidString(value) ||
		len(value) > maxDeliveryIdentifierBytes ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func validDeliveryToken(value string) bool {
	if value == "" || !utf8.ValidString(value) || len(value) > maxDeliveryTokenBytes {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		switch r {
		case '-', '.', '_', ':', '/':
			continue
		default:
			return false
		}
	}
	return true
}

func validDeliveryEvidenceRef(value string) bool {
	if value == "" ||
		!utf8.ValidString(value) ||
		len(value) > maxDeliveryEvidenceRefBytes ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func canonicalDeliveryTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.Round(0).UTC()
}

func equalDeliveryDispatch(left, right DeliveryDispatch) bool {
	return left.tenantID == right.tenantID &&
		left.workspaceID == right.workspaceID &&
		left.deliveryID == right.deliveryID &&
		left.messageID == right.messageID &&
		left.dispatchID == right.dispatchID &&
		left.attemptID == right.attemptID &&
		left.endpointID == right.endpointID &&
		left.endpointGeneration == right.endpointGeneration &&
		bytes.Equal(left.endpointFingerprint, right.endpointFingerprint) &&
		left.provider == right.provider &&
		left.transport == right.transport &&
		left.operationID == right.operationID &&
		bytes.Equal(left.requestDigest, right.requestDigest) &&
		left.messageKind == right.messageKind &&
		left.urgency == right.urgency &&
		left.workItemID == right.workItemID &&
		left.ackDueAt.Equal(right.ackDueAt)
}

func equalDeliveryEndpointIdentity(left, right DeliveryEndpointIdentity) bool {
	return left.tenantID == right.tenantID &&
		left.workspaceID == right.workspaceID &&
		left.endpointID == right.endpointID &&
		left.endpointGeneration == right.endpointGeneration &&
		bytes.Equal(left.endpointFingerprint, right.endpointFingerprint) &&
		left.provider == right.provider &&
		left.transport == right.transport
}
