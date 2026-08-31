// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	directNoticeInboxDefaultLimit   = 50
	directNoticeInboxMaximumLimit   = 200
	directNoticeInboxScanBatch      = 128
	directNoticeInboxCandidateBound = 4096
	directNoticeReadSetPageSize     = 128
	directNoticeReadSetBound        = 4096
)

var errDirectNoticePrincipalNotFound = fmt.Errorf(
	"%w: principal has no personal communication recipient",
	ErrCommunicationNotFound,
)

// DirectNoticeInboxQuery is an intentionally narrow private keyset over the
// immutable delivery sequence. AfterDeliverySeq is not the contractual REST
// cursor: activation remains blocked until WP-2 C2 supplies a CursorBarrier for
// mutable visibility across pages.
type DirectNoticeInboxQuery struct {
	AfterDeliverySeq int64 `json:"after_delivery_seq,omitempty"`
	Limit            int   `json:"limit,omitempty"`
}

// DirectNoticeMessageView exposes opened content without exposing its stored
// ProtectedPayload envelope or any sealing key metadata.
type DirectNoticeMessageView struct {
	ID           model.ID              `json:"id"`
	Version      int64                 `json:"version"`
	ChannelID    model.ID              `json:"channel_id"`
	ThreadID     model.ID              `json:"thread_id"`
	State        MessageState          `json:"state"`
	Sender       CommunicationActorRef `json:"sender"`
	Content      MessageContent        `json:"content"`
	Urgency      MessageUrgency        `json:"urgency"`
	AckPolicy    AckPolicy             `json:"ack_policy"`
	AckQuorum    int64                 `json:"ack_quorum,omitempty"`
	AvailableAt  time.Time             `json:"available_at"`
	AckDueAt     *time.Time            `json:"ack_due_at,omitempty"`
	ExpiresAt    *time.Time            `json:"expires_at,omitempty"`
	PublishedAt  *time.Time            `json:"published_at,omitempty"`
	TerminalAt   *time.Time            `json:"terminal_at,omitempty"`
	TerminalCode string                `json:"terminal_code,omitempty"`
}

// DirectNoticeDeliveryView is the personal delivery envelope paired with a
// DirectNoticeMessageView. It does not expose routing or authority evidence.
type DirectNoticeDeliveryView struct {
	ID             model.ID             `json:"id"`
	Version        int64                `json:"version"`
	MessageID      model.ID             `json:"message_id"`
	Recipient      RecipientRef         `json:"recipient"`
	DeliverySeq    int64                `json:"delivery_seq"`
	Required       bool                 `json:"required"`
	State          MessageDeliveryState `json:"state"`
	AvailableAt    time.Time            `json:"available_at"`
	FirstSeenAt    *time.Time           `json:"first_seen_at,omitempty"`
	AckDueAt       *time.Time           `json:"ack_due_at,omitempty"`
	ExpiresAt      *time.Time           `json:"expires_at,omitempty"`
	AcknowledgedAt *time.Time           `json:"acknowledged_at,omitempty"`
}

type DirectNoticeReadResult struct {
	Message     DirectNoticeMessageView  `json:"message"`
	Delivery    DirectNoticeDeliveryView `json:"delivery"`
	Fulfillment FulfillmentProjection    `json:"fulfillment"`
}

// DirectNoticeInboxPage reports only visible results. It deliberately has no
// total, scanned-candidate count or other side channel for hidden deliveries.
type DirectNoticeInboxPage struct {
	Items                api.JSONArray[DirectNoticeReadResult] `json:"items"`
	NextAfterDeliverySeq int64                                 `json:"next_after_delivery_seq"`
	HasMore              bool                                  `json:"has_more"`
}

type directNoticeCarrierIDs struct {
	MessageID   model.ID
	DeliveryID  model.ID
	ChannelID   model.ID
	DeliverySeq int64
}

type directNoticeInboxCandidate struct {
	DeliveryID  model.ID
	DeliverySeq int64
}

type directNoticeReaderPreflight struct {
	Scope      DirectoryScopeRef
	Principal  CommunicationPrincipal
	Recipient  RecipientRef
	Resolution PrincipalResolution
	Closure    ChannelGrantSubjectClosure
	Core       ReadWitness
	Facts      []store.AuthorizationFactRef
}

// directNoticeReaderIdentityPreflight contains only server-derived local
// authority. Core evidence remains sealed behind communicationRequestAuthority
// until the exact request is consumed inside its one-shot transaction.
type directNoticeReaderIdentityPreflight struct {
	Scope      DirectoryScopeRef
	Principal  CommunicationPrincipal
	Recipient  RecipientRef
	Resolution PrincipalResolution
	Closure    ChannelGrantSubjectClosure
}

type directNoticeAuthorizedRead struct {
	Message     Message
	Delivery    MessageDelivery
	Fulfillment FulfillmentProjection
	OpenPlan    ProtectedPayloadOpenPlan
}

type directNoticeReadAuthorizationInput struct {
	Preflight directNoticeReaderPreflight
	IDs       directNoticeCarrierIDs
}

type directNoticeReadLockedChannel struct {
	Channel Channel
	Grants  []ChannelGrant
}

type directNoticeReadLockedCarrier struct {
	Message       Message
	Deliveries    []MessageDelivery
	Audiences     []MessageAudience
	Contributions []MessageAudienceRecipient
}

type directNoticeReadSetSpec struct {
	OwnerID model.ID
	Queries [][]model.Filter
	Bound   int
}

type directNoticePayloadOpener func(
	context.Context,
	CommunicationContentSealer,
	ProtectedPayloadOpenPlan,
) (json.RawMessage, error)

// GetDirectNoticeMessage is the future handler-facing point read. The complete
// K3 readiness conjunction intentionally remains false while the remaining
// resolver, permission and pump cuts are absent, so this vertical mounts no route.
func (m *Module) GetDirectNoticeMessage(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	messageID model.ID,
) (DirectNoticeReadResult, error) {
	return m.getDirectNoticeMessageWithCurrentAuthority(
		ctx, scope, ref, messageID, OpenProtectedPayload, true,
	)
}

// getDirectNoticeMessageWithAuthority is the private exact-authority seam. It
// bypasses only the still-OFF aggregate readiness conjunction; it retains the
// same opaque credential resolution, exact Core question, authority snapshot,
// local evidence window and post-commit content open as the public boundary.
func (m *Module) getDirectNoticeMessageWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	messageID model.ID,
) (DirectNoticeReadResult, error) {
	return m.getDirectNoticeMessageWithCurrentAuthority(
		ctx, scope, ref, messageID, OpenProtectedPayload, false,
	)
}

func (m *Module) getDirectNoticeMessageWithAuthorityAndOpener(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	messageID model.ID,
	opener directNoticePayloadOpener,
) (DirectNoticeReadResult, error) {
	return m.getDirectNoticeMessageWithCurrentAuthority(
		ctx, scope, ref, messageID, opener, false,
	)
}

func (m *Module) getDirectNoticeMessageWithCurrentAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	messageID model.ID,
	opener directNoticePayloadOpener,
	requireReadiness bool,
) (DirectNoticeReadResult, error) {
	if !validCanonicalCommunicationID(messageID) || opener == nil {
		return DirectNoticeReadResult{}, communicationError(
			ErrInvalidCommunicationModel, "invalid direct notice point-read target",
		)
	}
	question, err := newCommunicationAuthorityQuestion(
		scope, messageKind, messageID, CommunicationRead,
	)
	if err != nil {
		return DirectNoticeReadResult{}, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return DirectNoticeReadResult{}, normalizeDirectNoticePointReadError(err)
	}
	inspected, err := bound.contextFor(question)
	if err != nil || inspected.question != question {
		return DirectNoticeReadResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"message-read authority context crossed its exact request",
		)
	}
	if err := requireDirectNoticeUserBackedPrincipal(inspected); err != nil {
		return DirectNoticeReadResult{}, normalizeDirectNoticePointReadError(err)
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return DirectNoticeReadResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(
		ctx, scope, inspected.principal, nil,
	)
	if err != nil {
		return DirectNoticeReadResult{}, normalizeDirectNoticePointReadError(err)
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	if err != nil {
		return DirectNoticeReadResult{}, err
	}
	authorized, hidden, err := m.authorizeDirectNoticeReadWithAuthority(
		ctx, question, bound, inspected, identity, messageID, window,
	)
	if err != nil {
		return DirectNoticeReadResult{}, normalizeDirectNoticePointReadError(err)
	}
	if hidden {
		return DirectNoticeReadResult{}, directNoticeReadNotFound("message is not visible")
	}
	return m.openDirectNoticeRead(ctx, authorized, opener)
}

func (m *Module) getDirectNoticeMessage(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	messageID model.ID,
) (DirectNoticeReadResult, error) {
	return m.getDirectNoticeMessageWithOpener(
		ctx, scope, principal, messageID, OpenProtectedPayload,
	)
}

func (m *Module) getDirectNoticeMessageWithOpener(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	messageID model.ID,
	opener directNoticePayloadOpener,
) (DirectNoticeReadResult, error) {
	if err := validateDirectNoticeReader(scope, principal); err != nil {
		return DirectNoticeReadResult{}, err
	}
	if !validCanonicalCommunicationID(messageID) || opener == nil {
		return DirectNoticeReadResult{}, communicationError(
			ErrInvalidCommunicationModel, "invalid direct notice point-read target",
		)
	}
	entity := EntityRef{
		TenantID: scope.TenantID, Kind: messageKind, ID: messageID,
		WorkspaceID: scope.WorkspaceID,
	}
	core, denied, err := m.authorizeDirectNoticeReadCore(ctx, principal, entity)
	if err != nil {
		return DirectNoticeReadResult{}, err
	}
	if denied {
		return DirectNoticeReadResult{}, directNoticeReadNotFound("message is not visible")
	}
	preflight, err := m.preflightDirectNoticeReader(ctx, scope, principal, core)
	if err != nil {
		return DirectNoticeReadResult{}, normalizeDirectNoticePointReadError(err)
	}
	ids, err := m.resolveDirectNoticeMessageIDs(ctx, preflight, messageID)
	if err != nil {
		return DirectNoticeReadResult{}, normalizeDirectNoticePointReadError(err)
	}
	authorized, err := m.authorizeDirectNoticeRead(ctx, preflight, ids)
	if err != nil {
		return DirectNoticeReadResult{}, normalizeDirectNoticePointReadError(err)
	}
	return m.openDirectNoticeRead(ctx, authorized, opener)
}

// ListDirectNoticeInbox is the future handler-facing personal inbox. The
// public boundary remains readiness-gated and therefore OFF during WP-2.
func (m *Module) ListDirectNoticeInbox(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	query DirectNoticeInboxQuery,
) (DirectNoticeInboxPage, error) {
	return m.listDirectNoticeInboxWithCurrentAuthority(
		ctx, scope, ref, query, OpenProtectedPayload, true,
	)
}

func (m *Module) listDirectNoticeInbox(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	query DirectNoticeInboxQuery,
) (DirectNoticeInboxPage, error) {
	return m.listDirectNoticeInboxWithOpener(
		ctx, scope, principal, query, OpenProtectedPayload,
	)
}

func (m *Module) listDirectNoticeInboxWithOpener(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	query DirectNoticeInboxQuery,
	opener directNoticePayloadOpener,
) (DirectNoticeInboxPage, error) {
	return m.listDirectNoticeInboxBoundedWithOpener(
		ctx, scope, principal, query, directNoticeInboxCandidateBound, opener,
	)
}

func (m *Module) listDirectNoticeInboxBoundedWithOpener(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	query DirectNoticeInboxQuery,
	candidateBound int,
	opener directNoticePayloadOpener,
) (DirectNoticeInboxPage, error) {
	if err := validateDirectNoticeReader(scope, principal); err != nil {
		return DirectNoticeInboxPage{}, err
	}
	query, err := normalizeDirectNoticeInboxQuery(query)
	if err != nil || opener == nil {
		if err != nil {
			return DirectNoticeInboxPage{}, err
		}
		return DirectNoticeInboxPage{}, communicationError(
			ErrInvalidCommunicationModel, "direct notice content opener is unavailable",
		)
	}
	if candidateBound < 1 {
		return DirectNoticeInboxPage{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice inbox candidate bound is unavailable",
		)
	}
	recipient := RecipientRef{Kind: RecipientUser, Ref: principal.UserID.String()}
	wantVisible := query.Limit + 1
	authorized := make([]directNoticeAuthorizedRead, 0, wantVisible)
	scanAfter := query.AfterDeliverySeq
	candidateWork := 0
	var shared *directNoticeReaderPreflight

	for len(authorized) < wantVisible {
		if candidateWork >= candidateBound {
			return DirectNoticeInboxPage{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"direct notice inbox candidate work exceeds bound",
			)
		}
		batchLimit := min(directNoticeInboxScanBatch, candidateBound-candidateWork)
		candidates, hasMore, readErr := m.readDirectNoticeInboxCandidateIDs(
			ctx, scope, recipient, scanAfter, batchLimit,
		)
		if readErr != nil {
			return DirectNoticeInboxPage{}, readErr
		}
		if len(candidates) == 0 {
			if hasMore {
				return DirectNoticeInboxPage{}, communicationError(
					ErrCommunicationEvidenceUnknown,
					"inbox candidate scan was truncated without a continuation",
				)
			}
			break
		}
		candidateWork += len(candidates)
		for _, candidate := range candidates {
			scanAfter = candidate.DeliverySeq
			entity := EntityRef{
				TenantID: scope.TenantID, Kind: messageDeliveryKind,
				ID: candidate.DeliveryID, WorkspaceID: scope.WorkspaceID,
			}
			core, denied, authErr := m.authorizeDirectNoticeReadCore(ctx, principal, entity)
			if authErr != nil {
				return DirectNoticeInboxPage{}, authErr
			}
			// A core BROKEN verdict is filtered before any carrier-specific
			// resolver, grant-closure or open observation.
			if denied {
				continue
			}
			if shared == nil {
				preflight, preflightErr := m.preflightDirectNoticeReader(
					ctx, scope, principal, core,
				)
				if preflightErr != nil {
					if errors.Is(preflightErr, errDirectNoticePrincipalNotFound) {
						return emptyDirectNoticeInboxPage(query), nil
					}
					return DirectNoticeInboxPage{}, preflightErr
				}
				shared = &preflight
			}
			preflight := *shared
			preflight.Core = core
			preflight.Facts, authErr = directNoticeReadAuthorityFacts(
				core, scope.TenantID, preflight.Resolution.Recipient.DirectoryEpoch,
			)
			if authErr != nil {
				return DirectNoticeInboxPage{}, authErr
			}
			if authErr = m.requireDirectNoticePreflightCurrent(preflight); authErr != nil {
				return DirectNoticeInboxPage{}, authErr
			}
			ids, resolveErr := m.resolveDirectNoticeDeliveryIDs(ctx, preflight, candidate)
			if resolveErr != nil {
				if errors.Is(resolveErr, ErrCommunicationNotFound) || errors.Is(resolveErr, store.ErrNotFound) {
					continue
				}
				return DirectNoticeInboxPage{}, resolveErr
			}
			plan, authorizeErr := m.authorizeDirectNoticeRead(ctx, preflight, ids)
			if authorizeErr != nil {
				if errors.Is(authorizeErr, ErrCommunicationNotFound) ||
					errors.Is(authorizeErr, ErrCommunicationForbidden) ||
					errors.Is(authorizeErr, store.ErrNotFound) {
					continue
				}
				return DirectNoticeInboxPage{}, authorizeErr
			}
			authorized = append(authorized, plan)
			if len(authorized) == wantVisible {
				break
			}
		}
		if len(authorized) == wantVisible || !hasMore {
			break
		}
	}

	// Candidate filtering can consume authority TTL or observe revocation. Gather
	// fresh Core, resolver and closure evidence for every page and lookahead plan,
	// then close them under one deterministic authorization snapshot. No payload
	// opens until that batch commits. Any per-carrier drift is UNKNOWN: silently
	// dropping a selected plan could skip a later visible candidate.
	inputs, revalidateErr := m.prepareDirectNoticeInboxRevalidations(
		ctx, scope, principal, authorized,
	)
	if revalidateErr != nil {
		if errors.Is(revalidateErr, errDirectNoticePrincipalNotFound) {
			return emptyDirectNoticeInboxPage(query), nil
		}
		return DirectNoticeInboxPage{}, revalidateErr
	}
	revalidated := make([]directNoticeAuthorizedRead, 0, len(inputs))
	if len(inputs) != 0 {
		revalidated, err = m.authorizeDirectNoticeReadBatch(ctx, scope, inputs)
		if err != nil {
			return DirectNoticeInboxPage{}, directNoticeInboxRevalidationUnknown(err)
		}
		for index := range revalidated {
			if !sameDirectNoticeReadIdentity(revalidated[index], authorized[index]) {
				return DirectNoticeInboxPage{}, directNoticeInboxRevalidationUnknown(
					directNoticeReadUnknown("selected inbox carrier changed identity", nil),
				)
			}
		}
	}
	visible := revalidated
	if len(visible) > query.Limit {
		visible = visible[:query.Limit]
	}
	page := emptyDirectNoticeInboxPage(query)
	page.Items = make([]DirectNoticeReadResult, 0, len(visible))
	page.HasMore = len(revalidated) > query.Limit
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

func emptyDirectNoticeInboxPage(query DirectNoticeInboxQuery) DirectNoticeInboxPage {
	return DirectNoticeInboxPage{
		Items:                make(api.JSONArray[DirectNoticeReadResult], 0),
		NextAfterDeliverySeq: query.AfterDeliverySeq,
		HasMore:              false,
	}
}

func (m *Module) requireDirectNoticePreflightCurrent(
	preflight directNoticeReaderPreflight,
) error {
	observedAt := m.clock.Now().Time()
	if !communicationEvidenceCurrent(
		preflight.Core.ObservedAt, preflight.Core.FreshUntil, observedAt,
	) || !communicationEvidenceCurrent(
		preflight.Resolution.ObservedAt, preflight.Resolution.FreshUntil, observedAt,
	) || !communicationEvidenceCurrent(
		preflight.Closure.ObservedAt, preflight.Closure.FreshUntil, observedAt,
	) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"direct notice authority expired before carrier discovery",
		)
	}
	return nil
}

func (m *Module) prepareDirectNoticeInboxRevalidations(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	selected []directNoticeAuthorizedRead,
) ([]directNoticeReadAuthorizationInput, error) {
	if len(selected) == 0 {
		return []directNoticeReadAuthorizationInput{}, nil
	}
	cores := make([]ReadWitness, len(selected))
	for index, plan := range selected {
		entity := EntityRef{
			TenantID: scope.TenantID, Kind: messageDeliveryKind,
			ID: plan.Delivery.ID, WorkspaceID: scope.WorkspaceID,
		}
		core, denied, err := m.authorizeDirectNoticeReadCore(ctx, principal, entity)
		if err != nil {
			return nil, err
		}
		if denied {
			return nil, directNoticeInboxRevalidationUnknown(
				directNoticeReadNotFound("selected inbox Delivery is no longer visible"),
			)
		}
		cores[index] = core
	}
	preflights, err := m.preflightDirectNoticeReaders(ctx, scope, principal, cores)
	if err != nil {
		return nil, err
	}
	inputs := make([]directNoticeReadAuthorizationInput, len(selected))
	for index, plan := range selected {
		preflight := preflights[index]
		if err := m.requireDirectNoticePreflightCurrent(preflight); err != nil {
			return nil, err
		}
		ids, err := m.resolveDirectNoticeDeliveryIDs(ctx, preflight, directNoticeInboxCandidate{
			DeliveryID: plan.Delivery.ID, DeliverySeq: plan.Delivery.DeliverySeq,
		})
		if err != nil {
			return nil, directNoticeInboxRevalidationUnknown(err)
		}
		if ids.MessageID != plan.Message.ID || ids.ChannelID != plan.Message.ChannelID ||
			ids.DeliveryID != plan.Delivery.ID || ids.DeliverySeq != plan.Delivery.DeliverySeq {
			return nil, directNoticeInboxRevalidationUnknown(
				directNoticeReadUnknown("selected inbox carrier changed identity", nil),
			)
		}
		inputs[index] = directNoticeReadAuthorizationInput{Preflight: preflight, IDs: ids}
	}
	return inputs, nil
}

func sameDirectNoticeReadIdentity(left, right directNoticeAuthorizedRead) bool {
	return left.Message.ID == right.Message.ID && left.Message.ChannelID == right.Message.ChannelID &&
		left.Delivery.ID == right.Delivery.ID && left.Delivery.MessageID == right.Delivery.MessageID &&
		left.Delivery.DeliverySeq == right.Delivery.DeliverySeq
}

func directNoticeInboxRevalidationUnknown(cause error) error {
	if errors.Is(cause, ErrCommunicationEvidenceUnknown) {
		return cause
	}
	return directNoticeReadUnknown("selected inbox authority changed before open", cause)
}

func validateDirectNoticeReader(
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) error {
	if err := ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return err
	}
	if principal.UserID == "" {
		return communicationError(
			ErrInvalidCommunicationModel,
			"direct notice read requires an authenticated User principal",
		)
	}
	return nil
}

func normalizeDirectNoticeInboxQuery(query DirectNoticeInboxQuery) (DirectNoticeInboxQuery, error) {
	if query.AfterDeliverySeq < 0 || query.Limit < 0 || query.Limit > directNoticeInboxMaximumLimit {
		return DirectNoticeInboxQuery{}, communicationError(
			ErrInvalidCommunicationModel, "invalid direct notice inbox keyset",
		)
	}
	if query.Limit == 0 {
		query.Limit = directNoticeInboxDefaultLimit
	}
	return query, nil
}

func (m *Module) authorizeDirectNoticeReadCore(
	ctx context.Context,
	principal CommunicationPrincipal,
	entity EntityRef,
) (ReadWitness, bool, error) {
	if !communicationPortBound(m.communicationReadAuthorizer) {
		return ReadWitness{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice read authorizer is unavailable",
		)
	}
	witness, err := m.communicationReadAuthorizer.AuthorizeEntityRead(ctx, principal, entity)
	if err != nil || ValidateReadWitness(witness) != nil {
		return ReadWitness{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice core authorization is unavailable",
		)
	}
	if witness.Entity != entity || witness.Operation != CommunicationRead || witness.Principal != principal {
		return ReadWitness{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice core authorization crosses request scope",
		)
	}
	if !communicationEvidenceCurrent(witness.ObservedAt, witness.FreshUntil, m.clock.Now().Time()) {
		return ReadWitness{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice core authorization is stale",
		)
	}
	switch witness.Outcome {
	case ReadUnknown:
		return ReadWitness{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice core authorization is unavailable",
		)
	case ReadDeny:
		return witness, true, nil
	case ReadAllow:
		return witness, false, nil
	default:
		return ReadWitness{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice core authorization has no verdict",
		)
	}
}

func (m *Module) preflightDirectNoticeReader(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	core ReadWitness,
) (directNoticeReaderPreflight, error) {
	preflights, err := m.preflightDirectNoticeReaders(
		ctx, scope, principal, []ReadWitness{core},
	)
	if err != nil {
		return directNoticeReaderPreflight{}, err
	}
	return preflights[0], nil
}

func (m *Module) preflightDirectNoticeReaders(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	cores []ReadWitness,
) ([]directNoticeReaderPreflight, error) {
	if !communicationPortBound(m.communicationDirectoryResolver) ||
		!communicationPortBound(m.communicationGrantClosure) {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice read resolver ports are unavailable",
		)
	}
	if len(cores) == 0 || len(cores) > directNoticeInboxMaximumLimit+1 {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice read preflight batch has invalid size",
		)
	}
	for _, core := range cores {
		if ValidateReadWitness(core) != nil || core.Outcome != ReadAllow ||
			core.Principal != principal || core.Entity.TenantID != scope.TenantID ||
			core.Entity.WorkspaceID != scope.WorkspaceID {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown, "direct notice core batch crosses request scope",
			)
		}
	}
	observedAt := m.clock.Now().Time()
	if !directNoticeReadCoresCurrent(cores, observedAt) {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice core batch expired before resolution",
		)
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(
		ctx, scope, principal,
		func(at time.Time) bool { return directNoticeReadCoresCurrent(cores, at) },
	)
	if err != nil {
		return nil, err
	}
	preflights := make([]directNoticeReaderPreflight, len(cores))
	for index, core := range cores {
		preflight, factErr := directNoticeReaderPreflightWithCore(identity, core)
		if factErr != nil {
			return nil, factErr
		}
		preflights[index] = preflight
	}
	return preflights, nil
}

func (m *Module) preflightDirectNoticeReaderIdentity(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	coreCurrent func(time.Time) bool,
) (directNoticeReaderIdentityPreflight, error) {
	if !communicationPortBound(m.communicationDirectoryResolver) ||
		!communicationPortBound(m.communicationGrantClosure) {
		return directNoticeReaderIdentityPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice read resolver ports are unavailable",
		)
	}
	if err := ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return directNoticeReaderIdentityPreflight{}, err
	}
	current := func(at time.Time) bool {
		return coreCurrent == nil || coreCurrent(at)
	}
	observedAt := m.clock.Now().Time()
	if !current(observedAt) {
		return directNoticeReaderIdentityPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice core expired before resolution",
		)
	}
	resolution, err := m.communicationDirectoryResolver.ResolvePrincipal(ctx, scope, principal)
	if err != nil || ValidatePrincipalResolution(resolution) != nil {
		return directNoticeReaderIdentityPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice principal resolution is unavailable",
		)
	}
	resolution = cloneDirectNoticePrincipalResolution(resolution)
	if resolution.Scope != scope || resolution.Principal != principal {
		return directNoticeReaderIdentityPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"direct notice principal resolution crosses request scope",
		)
	}
	observedAt = m.clock.Now().Time()
	if !current(observedAt) ||
		!communicationEvidenceCurrent(resolution.ObservedAt, resolution.FreshUntil, observedAt) {
		return directNoticeReaderIdentityPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"direct notice core or principal resolution expired during resolution",
		)
	}
	switch resolution.Outcome {
	case PrincipalUnknown:
		return directNoticeReaderIdentityPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice principal resolution is unavailable",
		)
	case PrincipalNotFound:
		return directNoticeReaderIdentityPreflight{}, errDirectNoticePrincipalNotFound
	}
	if resolution.Recipient == nil || resolution.Recipient.Recipient.Kind != RecipientUser ||
		resolution.Recipient.Recipient.Ref != principal.UserID.String() {
		return directNoticeReaderIdentityPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"direct notice principal did not resolve to the authenticated User",
		)
	}
	closure, err := m.communicationGrantClosure.ResolveChannelGrantSubjects(ctx, scope, principal)
	if err != nil {
		return directNoticeReaderIdentityPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice ChannelGrant closure is unavailable",
		)
	}
	closure = cloneDirectNoticeChannelGrantSubjectClosure(closure)
	observedAt = m.clock.Now().Time()
	if closure.Scope != scope || closure.Principal != principal || closure.DirectoryEpoch < 1 ||
		closure.DirectoryEpoch != resolution.Recipient.DirectoryEpoch || !closure.Outcome.Valid() ||
		!boundedToken(closure.Code, 128) || !validateOpaqueRef(closure.EvidenceRef) ||
		!current(observedAt) ||
		!communicationEvidenceCurrent(closure.ObservedAt, closure.FreshUntil, observedAt) ||
		!communicationEvidenceCurrent(resolution.ObservedAt, resolution.FreshUntil, observedAt) {
		return directNoticeReaderIdentityPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice authority closure is stale or malformed",
		)
	}
	if closure.Outcome == ReadUnknown {
		return directNoticeReaderIdentityPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice ChannelGrant closure is unavailable",
		)
	}
	return directNoticeReaderIdentityPreflight{
		Scope: scope, Principal: principal, Recipient: resolution.Recipient.Recipient,
		Resolution: resolution, Closure: closure,
	}, nil
}

func directNoticeReaderPreflightWithCore(
	identity directNoticeReaderIdentityPreflight,
	core ReadWitness,
) (directNoticeReaderPreflight, error) {
	if identity.Resolution.Recipient == nil {
		return directNoticeReaderPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice principal resolution is unavailable",
		)
	}
	facts, err := directNoticeReadAuthorityFacts(
		core, identity.Scope.TenantID, identity.Resolution.Recipient.DirectoryEpoch,
	)
	if err != nil {
		return directNoticeReaderPreflight{}, err
	}
	return directNoticeReaderPreflight{
		Scope: identity.Scope, Principal: identity.Principal, Recipient: identity.Recipient,
		Resolution: cloneDirectNoticePrincipalResolution(identity.Resolution),
		Closure:    cloneDirectNoticeChannelGrantSubjectClosure(identity.Closure),
		Core:       cloneCommunicationRequestAuthorityWitness(core),
		Facts:      append([]store.AuthorizationFactRef(nil), facts...),
	}, nil
}

func cloneDirectNoticePrincipalResolution(
	resolution PrincipalResolution,
) PrincipalResolution {
	result := resolution
	if resolution.Recipient != nil {
		recipient := cloneDirectNoticeRecipientSnapshot(*resolution.Recipient)
		result.Recipient = &recipient
	}
	return result
}

func directNoticeReaderAuthorityWindow(
	identity directNoticeReaderIdentityPreflight,
) (communicationAuthorityWindow, error) {
	observedAt := identity.Resolution.ObservedAt
	if identity.Closure.ObservedAt.After(observedAt) {
		observedAt = identity.Closure.ObservedAt
	}
	freshUntil := identity.Resolution.FreshUntil
	if identity.Closure.FreshUntil.Before(freshUntil) {
		freshUntil = identity.Closure.FreshUntil
	}
	return newCommunicationAuthorityWindow(observedAt, freshUntil)
}

func directNoticeReadCoresCurrent(cores []ReadWitness, observedAt time.Time) bool {
	for _, core := range cores {
		if !communicationEvidenceCurrent(core.ObservedAt, core.FreshUntil, observedAt) {
			return false
		}
	}
	return true
}

func directNoticeReadAuthorityFacts(
	core ReadWitness,
	tenantID model.TenantID,
	directoryEpoch int64,
) ([]store.AuthorizationFactRef, error) {
	if directoryEpoch < 1 || !directNoticeCoreWitnessBindsDirectoryEpoch(
		core, tenantID, directoryEpoch,
	) {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"direct notice read authority is not bound to the current directory epoch",
		)
	}
	fact := store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: model.ID(tenantID), Version: directoryEpoch,
	}
	return canonicalAuthorizationFactUnion(append(
		append([]store.AuthorizationFactRef(nil), core.Facts...), fact,
	))
}

func (m *Module) resolveDirectNoticeMessageIDs(
	ctx context.Context,
	preflight directNoticeReaderPreflight,
	messageID model.ID,
) (directNoticeCarrierIDs, error) {
	identity := directNoticeReaderIdentityPreflight{
		Scope: preflight.Scope, Principal: preflight.Principal,
		Recipient: preflight.Recipient, Resolution: preflight.Resolution,
		Closure: preflight.Closure,
	}
	var ids directNoticeCarrierIDs
	err := m.viewCommunication(ctx, preflight.Scope, func(sc store.Scope) error {
		var resolveErr error
		ids, resolveErr = resolveDirectNoticeMessageIDsFromRepositories(
			ctx,
			func(kind model.Kind) (communicationReadRepository, error) { return sc.Ext(kind) },
			identity,
			messageID,
		)
		return resolveErr
	})
	return ids, err
}

func resolveDirectNoticeMessageIDsFromRepositories(
	ctx context.Context,
	resolve communicationReadRepositoryResolver,
	preflight directNoticeReaderIdentityPreflight,
	messageID model.ID,
) (directNoticeCarrierIDs, error) {
	ids := directNoticeCarrierIDs{MessageID: messageID}
	if resolve == nil {
		return directNoticeCarrierIDs{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice read repositories are unavailable",
		)
	}
	messageRepo, err := resolve(messageKind)
	if err != nil {
		return directNoticeCarrierIDs{}, err
	}
	messageRecord, err := messageRepo.Get(ctx, messageID)
	if err != nil {
		return directNoticeCarrierIDs{}, err
	}
	ids.ChannelID, err = directNoticeRecordID(messageRecord, colCommChannelID)
	if err != nil {
		return directNoticeCarrierIDs{}, err
	}
	deliveryRepo, err := resolve(messageDeliveryKind)
	if err != nil {
		return directNoticeCarrierIDs{}, err
	}
	rows, page, err := deliveryRepo.List(ctx, model.Query{Filters: []model.Filter{
		{Column: colCommMessageID, Op: model.OpEq, Value: messageID.String()},
		{Column: colCommRecipientKind, Op: model.OpEq, Value: string(preflight.Recipient.Kind)},
		{Column: colCommRecipientRef, Op: model.OpEq, Value: preflight.Recipient.Ref},
	}, Limit: 2})
	if err != nil {
		return directNoticeCarrierIDs{}, err
	}
	if len(rows) == 0 {
		return directNoticeCarrierIDs{}, store.ErrNotFound
	}
	if len(rows) != 1 || page.HasMore {
		return directNoticeCarrierIDs{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"direct notice point read has an ambiguous Delivery",
		)
	}
	ids.DeliveryID, err = directNoticeRecordID(rows[0], model.ColID)
	if err != nil {
		return directNoticeCarrierIDs{}, err
	}
	ids.DeliverySeq = rows[0].Int(colCommDeliverySeq)
	if ids.DeliverySeq < 1 {
		return directNoticeCarrierIDs{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice Delivery sequence is malformed",
		)
	}
	return ids, nil
}

func (m *Module) resolveDirectNoticeDeliveryIDs(
	ctx context.Context,
	preflight directNoticeReaderPreflight,
	candidate directNoticeInboxCandidate,
) (directNoticeCarrierIDs, error) {
	ids := directNoticeCarrierIDs{
		DeliveryID: candidate.DeliveryID, DeliverySeq: candidate.DeliverySeq,
	}
	err := m.viewCommunication(ctx, preflight.Scope, func(sc store.Scope) error {
		deliveryRepo, err := sc.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		deliveryRecord, err := deliveryRepo.Get(ctx, candidate.DeliveryID)
		if err != nil {
			return err
		}
		if deliveryRecord.String(colCommRecipientKind) != string(preflight.Recipient.Kind) ||
			deliveryRecord.String(colCommRecipientRef) != preflight.Recipient.Ref ||
			deliveryRecord.Int(colCommDeliverySeq) != candidate.DeliverySeq {
			return directNoticeReadNotFound("Delivery left the personal inbox binding")
		}
		ids.MessageID, err = directNoticeRecordID(deliveryRecord, colCommMessageID)
		if err != nil {
			return err
		}
		messageRepo, err := sc.Ext(messageKind)
		if err != nil {
			return err
		}
		messageRecord, err := messageRepo.Get(ctx, ids.MessageID)
		if err != nil {
			return err
		}
		ids.ChannelID, err = directNoticeRecordID(messageRecord, colCommChannelID)
		return err
	})
	return ids, err
}

func (m *Module) readDirectNoticeInboxCandidateIDs(
	ctx context.Context,
	scope DirectoryScopeRef,
	recipient RecipientRef,
	after int64,
	limit int,
) ([]directNoticeInboxCandidate, bool, error) {
	var candidates []directNoticeInboxCandidate
	var hasMore bool
	err := m.viewCommunication(ctx, scope, func(sc store.Scope) error {
		repo, err := sc.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{
				{Column: colCommRecipientKind, Op: model.OpEq, Value: string(recipient.Kind)},
				{Column: colCommRecipientRef, Op: model.OpEq, Value: recipient.Ref},
				{Column: colCommDeliverySeq, Op: model.OpGt, Value: after},
			},
			Sort: []model.Sort{{Column: colCommDeliverySeq}}, Limit: limit,
		})
		if err != nil {
			return err
		}
		hasMore = page.HasMore
		previous := after
		for _, row := range rows {
			id, parseErr := directNoticeRecordID(row, model.ColID)
			sequence := row.Int(colCommDeliverySeq)
			if parseErr != nil || sequence <= previous {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"personal inbox candidate ordering is malformed",
				)
			}
			previous = sequence
			candidates = append(candidates, directNoticeInboxCandidate{
				DeliveryID: id, DeliverySeq: sequence,
			})
		}
		return nil
	})
	return candidates, hasMore, err
}

func directNoticeRecordID(record model.Record, column string) (model.ID, error) {
	id, err := model.ParseID(record.String(column))
	if err != nil || !validCanonicalCommunicationID(id) {
		return "", communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice row has an invalid %s", column,
		)
	}
	return id, nil
}

func (m *Module) authorizeDirectNoticeRead(
	ctx context.Context,
	preflight directNoticeReaderPreflight,
	ids directNoticeCarrierIDs,
) (directNoticeAuthorizedRead, error) {
	var authorized directNoticeAuthorizedRead
	err := m.mutateCommunication(ctx, preflight.Scope, func(tx *communicationTx) error {
		if err := tx.lockAuthoritySnapshot(ctx, preflight.Facts); err != nil {
			return normalizeDirectNoticeAuthorityLockError(err)
		}
		var err error
		authorized, _, err = authorizeDirectNoticeReadLocked(ctx, tx, preflight, ids)
		return err
	})
	return authorized, normalizeDirectNoticeAuthorizationError(err)
}

func (m *Module) authorizeDirectNoticeReadWithAuthority(
	ctx context.Context,
	question communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	inspected communicationRequestAuthorityInspection,
	identity directNoticeReaderIdentityPreflight,
	messageID model.ID,
	window communicationAuthorityWindow,
) (directNoticeAuthorizedRead, bool, error) {
	var authorized directNoticeAuthorizedRead
	var hidden bool
	err := m.mutateCommunicationWithNarrowedAuthority(
		ctx, question, bound, CommunicationClaimAuthoritySnapshot{}, window,
		func(tx *communicationTx, consumed communicationRequestAuthorityContext) error {
			if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
				return err
			}
			if consumed.question != question || consumed.question.entity != (EntityRef{
				TenantID: identity.Scope.TenantID, Kind: messageKind, ID: messageID,
				WorkspaceID: identity.Scope.WorkspaceID,
			}) || consumed.question.operation != CommunicationRead ||
				consumed.principal != identity.Principal {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"message-read authority crossed point-read preflight",
				)
			}
			preflight, err := directNoticeReaderPreflightWithCore(identity, consumed.witness)
			if err != nil {
				return err
			}
			if err := tx.validateAuthorityFreshness(tx.now); err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, preflight.Facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}
			if err := tx.lockTransaction(
				ctx, directNoticeMessageLockKey(identity.Scope, messageID),
			); err != nil {
				return directNoticeReadUnknown(
					"direct notice point-read lock is unavailable", err,
				)
			}
			ids, err := resolveDirectNoticeMessageIDsFromRepositories(
				ctx,
				func(kind model.Kind) (communicationReadRepository, error) {
					return tx.repo(kind)
				},
				identity,
				messageID,
			)
			if errors.Is(err, ErrCommunicationEvidenceUnknown) {
				return err
			}
			if errors.Is(err, store.ErrNotFound) {
				if refreshErr := tx.refreshNow(ctx); refreshErr != nil {
					return refreshErr
				}
				hidden = true
				return nil
			}
			if err != nil {
				return err
			}
			var refreshed bool
			authorized, refreshed, err = authorizeDirectNoticeReadLocked(
				ctx, tx, preflight, ids,
			)
			if errors.Is(err, ErrCommunicationEvidenceUnknown) {
				return err
			}
			if errors.Is(err, ErrCommunicationNotFound) || errors.Is(err, ErrCommunicationForbidden) ||
				errors.Is(err, store.ErrNotFound) {
				if !refreshed {
					if refreshErr := tx.refreshNow(ctx); refreshErr != nil {
						return directNoticeReadUnknown(
							"direct notice carrier changed before final confirmation",
							refreshErr,
						)
					}
				}
				hidden = true
				return nil
			}
			return err
		},
	)
	if err != nil && (errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, ErrCommunicationNotFound) || errors.Is(err, ErrCommunicationForbidden)) {
		err = directNoticeReadUnknown(
			"direct notice absence was not confirmed by the bound transaction", err,
		)
	}
	return authorized, hidden, normalizeDirectNoticeAuthorizationError(err)
}

func authorizeDirectNoticeReadLocked(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticeReaderPreflight,
	ids directNoticeCarrierIDs,
) (directNoticeAuthorizedRead, bool, error) {
	var authorized directNoticeAuthorizedRead
	channelRecord, err := tx.lockRecord(ctx, channelKind, ids.ChannelID)
	if err != nil {
		return directNoticeAuthorizedRead{}, false, normalizeDirectNoticeLockedNotFound(err)
	}
	channel, err := channelFromRecord(channelRecord)
	if err != nil {
		return directNoticeAuthorizedRead{}, false, directNoticeReadUnknown("locked Channel is malformed", err)
	}
	if channel.ID != ids.ChannelID || channel.TenantID != preflight.Scope.TenantID ||
		channel.WorkspaceID != preflight.Scope.WorkspaceID {
		return directNoticeAuthorizedRead{}, false, directNoticeReadNotFound("Channel crosses direct notice scope")
	}
	grants, err := lockCurrentChannelGrants(ctx, tx, channel.ID)
	if err != nil {
		return directNoticeAuthorizedRead{}, false, err
	}
	messageRecord, err := tx.lockRecord(ctx, messageKind, ids.MessageID)
	if err != nil {
		return directNoticeAuthorizedRead{}, false, normalizeDirectNoticeLockedNotFound(err)
	}
	deliveryRecords, err := lockDirectNoticeRecordSet(
		ctx, tx, messageDeliveryKind,
		[]model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: ids.MessageID.String()}},
		directNoticeReadSetBound,
	)
	if err != nil {
		return directNoticeAuthorizedRead{}, false, err
	}
	deliveries := make([]MessageDelivery, 0, len(deliveryRecords))
	requiredCount := int64(0)
	for _, record := range deliveryRecords {
		delivery, decodeErr := messageDeliveryFromRecord(record)
		if decodeErr != nil {
			return directNoticeAuthorizedRead{}, false, directNoticeReadUnknown("locked Delivery set is malformed", decodeErr)
		}
		if delivery.MessageID != ids.MessageID {
			return directNoticeAuthorizedRead{}, false, directNoticeReadUnknown("locked Delivery left Message set", nil)
		}
		if delivery.Required {
			requiredCount++
		}
		deliveries = append(deliveries, delivery)
	}
	message, err := messageFromRecord(messageRecord, requiredCount)
	if err != nil {
		return directNoticeAuthorizedRead{}, false, directNoticeReadUnknown("locked Message is malformed", err)
	}
	audienceRecords, err := lockDirectNoticeRecordSet(
		ctx, tx, messageAudienceKind,
		[]model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: ids.MessageID.String()}},
		64,
	)
	if err != nil {
		return directNoticeAuthorizedRead{}, false, err
	}
	audiences := make([]MessageAudience, 0, len(audienceRecords))
	for _, record := range audienceRecords {
		audience, decodeErr := messageAudienceFromRecord(record)
		if decodeErr != nil {
			return directNoticeAuthorizedRead{}, false, directNoticeReadUnknown("locked MessageAudience set is malformed", decodeErr)
		}
		if audience.MessageID != ids.MessageID {
			return directNoticeAuthorizedRead{}, false, directNoticeReadUnknown("locked audience left Message set", nil)
		}
		audiences = append(audiences, audience)
	}
	contributionRecords, err := lockDirectNoticeContributionSet(ctx, tx, audiences)
	if err != nil {
		return directNoticeAuthorizedRead{}, false, err
	}
	contributions := make([]MessageAudienceRecipient, 0, len(contributionRecords))
	for _, record := range contributionRecords {
		contribution, decodeErr := messageAudienceRecipientFromRecord(record)
		if decodeErr != nil {
			return directNoticeAuthorizedRead{}, false, directNoticeReadUnknown("locked audience contribution set is malformed", decodeErr)
		}
		contributions = append(contributions, contribution)
	}
	epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
	if err != nil || epoch.Validate() != nil || epoch.TenantID != preflight.Scope.TenantID ||
		epoch.Version != preflight.Resolution.Recipient.DirectoryEpoch {
		return directNoticeAuthorizedRead{}, false, directNoticeReadUnknown("locked directory epoch is unavailable", err)
	}
	if err := tx.refreshNow(ctx); err != nil {
		return directNoticeAuthorizedRead{}, false, err
	}
	authorized, err = evaluateDirectNoticeLockedRead(
		preflight, ids,
		directNoticeReadLockedChannel{Channel: channel, Grants: grants},
		directNoticeReadLockedCarrier{
			Message: message, Deliveries: deliveries,
			Audiences: audiences, Contributions: contributions,
		},
		epoch, tx.now.Time(),
	)
	if err != nil {
		return directNoticeAuthorizedRead{}, true, err
	}
	if len(tx.requestAuthorityFacts) != 0 {
		grantFreshUntil, constrained, deadlineErr := directNoticeReadGrantFreshUntil(
			grants, preflight.Closure, tx.now.Time(),
		)
		if deadlineErr != nil {
			return directNoticeAuthorizedRead{}, true, deadlineErr
		}
		if constrained {
			if deadlineErr := tx.narrowRequestAuthorityFreshUntil(grantFreshUntil); deadlineErr != nil {
				return directNoticeAuthorizedRead{}, true, deadlineErr
			}
		}
	}
	return authorized, true, nil
}

// directNoticeReadGrantFreshUntil returns the OR-horizon of the locked grants
// that currently confer read. If any matching grant has no expiry, durable
// grant authority adds no time bound. Otherwise authority remains live until
// the latest matching expiry, because any one current grant is sufficient.
func directNoticeReadGrantFreshUntil(
	grants []ChannelGrant,
	closure ChannelGrantSubjectClosure,
	dbNow time.Time,
) (time.Time, bool, error) {
	if dbNow.IsZero() || closure.Outcome != ReadAllow {
		return time.Time{}, false, directNoticeReadUnknown(
			"current ChannelGrant horizon is unavailable", nil,
		)
	}
	subjects := make(map[CommunicationSubjectRef]struct{}, len(closure.Subjects))
	for _, subject := range closure.Subjects {
		if subject.Validate() != nil {
			return time.Time{}, false, directNoticeReadUnknown(
				"current ChannelGrant closure is malformed", nil,
			)
		}
		if _, duplicate := subjects[subject]; duplicate {
			return time.Time{}, false, directNoticeReadUnknown(
				"current ChannelGrant closure is ambiguous", nil,
			)
		}
		subjects[subject] = struct{}{}
	}
	found := false
	var latest time.Time
	for _, grant := range grants {
		if grant.State != ChannelGrantActive || !grant.CanRead {
			continue
		}
		if _, matches := subjects[grant.Subject]; !matches {
			continue
		}
		if grant.ExpiresAt != nil && !dbNow.Before(*grant.ExpiresAt) {
			continue
		}
		found = true
		if grant.ExpiresAt == nil {
			return time.Time{}, false, nil
		}
		if grant.ExpiresAt.After(latest) {
			latest = grant.ExpiresAt.UTC()
		}
	}
	if !found || latest.IsZero() {
		return time.Time{}, false, directNoticeReadUnknown(
			"current ChannelGrant horizon is unavailable", nil,
		)
	}
	return latest, true, nil
}

func normalizeDirectNoticeAuthorizationError(err error) error {
	if errors.Is(err, store.ErrConflict) {
		return directNoticeReadUnknown("direct notice authority changed while locking", err)
	}
	return err
}

func normalizeDirectNoticeAuthorityLockError(err error) error {
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
		return directNoticeReadUnknown("direct notice authority changed while locking", err)
	}
	return err
}

type directNoticeReadBatchPreparation struct {
	facts      []store.AuthorizationFactRef
	channelIDs []model.ID
	messageIDs []model.ID
}

func prepareDirectNoticeReadBatch(
	scope DirectoryScopeRef,
	inputs []directNoticeReadAuthorizationInput,
) (directNoticeReadBatchPreparation, error) {
	return prepareDirectNoticeReadBatchBounded(
		scope, inputs, directNoticeInboxMaximumLimit+1,
	)
}

func prepareDirectNoticeReadBatchBounded(
	scope DirectoryScopeRef,
	inputs []directNoticeReadAuthorizationInput,
	bound int,
) (directNoticeReadBatchPreparation, error) {
	if bound < 1 || bound > directNoticeInboxCandidateBound ||
		len(inputs) == 0 || len(inputs) > bound {
		return directNoticeReadBatchPreparation{}, directNoticeReadUnknown(
			"direct notice authorization batch has invalid size", nil,
		)
	}
	allFacts := make([]store.AuthorizationFactRef, 0)
	channelSet := make(map[model.ID]struct{})
	messageSet := make(map[model.ID]struct{})
	deliverySet := make(map[model.ID]struct{})
	deliverySequences := make(map[int64]struct{})
	var principal CommunicationPrincipal
	var recipient RecipientRef
	var resolution PrincipalResolution
	var closure ChannelGrantSubjectClosure
	for index, input := range inputs {
		preflight := input.Preflight
		ids := input.IDs
		if preflight.Scope != scope || preflight.Resolution.Recipient == nil ||
			preflight.Core.Outcome != ReadAllow ||
			!validCanonicalCommunicationID(ids.ChannelID) ||
			!validCanonicalCommunicationID(ids.MessageID) ||
			!validCanonicalCommunicationID(ids.DeliveryID) || ids.DeliverySeq < 1 {
			return directNoticeReadBatchPreparation{}, directNoticeReadUnknown(
				"direct notice authorization batch is malformed", nil,
			)
		}
		if index == 0 {
			principal = preflight.Principal
			recipient = preflight.Recipient
			resolution = preflight.Resolution
			closure = preflight.Closure
		} else if preflight.Principal != principal || preflight.Recipient != recipient ||
			!canonicalCommunicationValueEqual(preflight.Resolution, resolution) ||
			!canonicalCommunicationValueEqual(preflight.Closure, closure) {
			return directNoticeReadBatchPreparation{}, directNoticeReadUnknown(
				"direct notice authorization batch crosses reader", nil,
			)
		}
		if _, duplicate := messageSet[ids.MessageID]; duplicate {
			return directNoticeReadBatchPreparation{}, directNoticeReadUnknown(
				"direct notice authorization batch repeats Message", nil,
			)
		}
		if _, duplicate := deliverySet[ids.DeliveryID]; duplicate {
			return directNoticeReadBatchPreparation{}, directNoticeReadUnknown(
				"direct notice authorization batch repeats Delivery", nil,
			)
		}
		if _, duplicate := deliverySequences[ids.DeliverySeq]; duplicate {
			return directNoticeReadBatchPreparation{}, directNoticeReadUnknown(
				"direct notice authorization batch repeats sequence", nil,
			)
		}
		channelSet[ids.ChannelID] = struct{}{}
		messageSet[ids.MessageID] = struct{}{}
		deliverySet[ids.DeliveryID] = struct{}{}
		deliverySequences[ids.DeliverySeq] = struct{}{}
		allFacts = append(allFacts, preflight.Facts...)
	}
	facts, err := canonicalAuthorizationFactUnion(allFacts)
	if err != nil {
		return directNoticeReadBatchPreparation{}, directNoticeReadUnknown(
			"direct notice batch authority facts are unavailable", err,
		)
	}
	return directNoticeReadBatchPreparation{
		facts: facts, channelIDs: directNoticeSortedIDSet(channelSet),
		messageIDs: directNoticeSortedIDSet(messageSet),
	}, nil
}

func (m *Module) authorizeDirectNoticeReadBatch(
	ctx context.Context,
	scope DirectoryScopeRef,
	inputs []directNoticeReadAuthorizationInput,
) ([]directNoticeAuthorizedRead, error) {
	prepared, err := prepareDirectNoticeReadBatch(scope, inputs)
	if err != nil {
		return nil, err
	}
	var authorized []directNoticeAuthorizedRead
	err = m.mutateCommunication(ctx, scope, func(tx *communicationTx) error {
		if err := tx.lockAuthoritySnapshot(ctx, prepared.facts); err != nil {
			return normalizeDirectNoticeAuthorityLockError(err)
		}
		var authorizeErr error
		authorized, authorizeErr = authorizeDirectNoticeReadBatchLocked(
			ctx, tx, scope, inputs, prepared, false, 0,
		)
		return authorizeErr
	})
	return authorized, normalizeDirectNoticeAuthorizationError(err)
}

func authorizeDirectNoticeReadBatchLocked(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	inputs []directNoticeReadAuthorizationInput,
	prepared directNoticeReadBatchPreparation,
	hideDenied bool,
	visibleLimit int,
) ([]directNoticeAuthorizedRead, error) {
	if tx == nil || len(inputs) == 0 || len(prepared.channelIDs) == 0 ||
		len(prepared.messageIDs) == 0 || visibleLimit < 0 {
		return nil, directNoticeReadUnknown(
			"direct notice locked authorization batch is malformed", nil,
		)
	}
	channels := make(
		map[model.ID]directNoticeReadLockedChannel, len(prepared.channelIDs),
	)
	for _, channelID := range prepared.channelIDs {
		record, lockErr := tx.lockRecord(ctx, channelKind, channelID)
		if lockErr != nil {
			return nil, normalizeDirectNoticeLockedNotFound(lockErr)
		}
		channel, decodeErr := channelFromRecord(record)
		if decodeErr != nil {
			return nil, directNoticeReadUnknown("locked Channel is malformed", decodeErr)
		}
		if channel.ID != channelID || channel.TenantID != scope.TenantID ||
			channel.WorkspaceID != scope.WorkspaceID {
			return nil, directNoticeReadNotFound("Channel crosses direct notice batch scope")
		}
		channels[channelID] = directNoticeReadLockedChannel{Channel: channel}
	}
	for _, channelID := range prepared.channelIDs {
		grants, grantErr := lockCurrentChannelGrants(ctx, tx, channelID)
		if grantErr != nil {
			return nil, grantErr
		}
		channel := channels[channelID]
		channel.Grants = grants
		channels[channelID] = channel
	}

	messageRecords := make(map[model.ID]model.Record, len(prepared.messageIDs))
	for _, messageID := range prepared.messageIDs {
		record, lockErr := tx.lockRecord(ctx, messageKind, messageID)
		if lockErr != nil {
			return nil, normalizeDirectNoticeLockedNotFound(lockErr)
		}
		messageRecords[messageID] = record
	}
	deliverySpecs := make([]directNoticeReadSetSpec, 0, len(prepared.messageIDs))
	audienceSpecs := make([]directNoticeReadSetSpec, 0, len(prepared.messageIDs))
	for _, messageID := range prepared.messageIDs {
		deliverySpecs = append(deliverySpecs, directNoticeReadSetSpec{
			OwnerID: messageID,
			Queries: [][]model.Filter{{{
				Column: colCommMessageID, Op: model.OpEq, Value: messageID.String(),
			}}},
			Bound: directNoticeReadSetBound,
		})
		audienceSpecs = append(audienceSpecs, directNoticeReadSetSpec{
			OwnerID: messageID,
			Queries: [][]model.Filter{{{
				Column: colCommMessageID, Op: model.OpEq, Value: messageID.String(),
			}}},
			Bound: 64,
		})
	}
	deliveryRecords, lockErr := lockDirectNoticeBatchRecordSets(
		ctx, tx, messageDeliveryKind, deliverySpecs,
	)
	if lockErr != nil {
		return nil, lockErr
	}
	audienceRecords, lockErr := lockDirectNoticeBatchRecordSets(
		ctx, tx, messageAudienceKind, audienceSpecs,
	)
	if lockErr != nil {
		return nil, lockErr
	}

	carriers := make(map[model.ID]directNoticeReadLockedCarrier, len(prepared.messageIDs))
	contributionSpecs := make([]directNoticeReadSetSpec, 0, len(prepared.messageIDs))
	for _, messageID := range prepared.messageIDs {
		deliveries := make([]MessageDelivery, 0, len(deliveryRecords[messageID]))
		requiredCount := int64(0)
		for _, record := range deliveryRecords[messageID] {
			delivery, decodeErr := messageDeliveryFromRecord(record)
			if decodeErr != nil || delivery.MessageID != messageID {
				return nil, directNoticeReadUnknown("locked Delivery set is malformed", decodeErr)
			}
			if delivery.Required {
				requiredCount++
			}
			deliveries = append(deliveries, delivery)
		}
		message, decodeErr := messageFromRecord(messageRecords[messageID], requiredCount)
		if decodeErr != nil {
			return nil, directNoticeReadUnknown("locked Message is malformed", decodeErr)
		}
		audiences := make([]MessageAudience, 0, len(audienceRecords[messageID]))
		queries := make([][]model.Filter, 0, len(audienceRecords[messageID]))
		for _, record := range audienceRecords[messageID] {
			audience, audienceErr := messageAudienceFromRecord(record)
			if audienceErr != nil || audience.MessageID != messageID {
				return nil, directNoticeReadUnknown(
					"locked MessageAudience set is malformed", audienceErr,
				)
			}
			audiences = append(audiences, audience)
			queries = append(queries, []model.Filter{{
				Column: colCommMessageAudienceID, Op: model.OpEq, Value: audience.ID.String(),
			}})
		}
		carriers[messageID] = directNoticeReadLockedCarrier{
			Message: message, Deliveries: deliveries, Audiences: audiences,
		}
		contributionSpecs = append(contributionSpecs, directNoticeReadSetSpec{
			OwnerID: messageID, Queries: queries, Bound: directNoticeReadSetBound,
		})
	}
	contributionRecords, lockErr := lockDirectNoticeBatchRecordSets(
		ctx, tx, messageAudienceRecipientKind, contributionSpecs,
	)
	if lockErr != nil {
		return nil, lockErr
	}
	for _, messageID := range prepared.messageIDs {
		carrier := carriers[messageID]
		carrier.Contributions = make(
			[]MessageAudienceRecipient, 0, len(contributionRecords[messageID]),
		)
		for _, record := range contributionRecords[messageID] {
			contribution, decodeErr := messageAudienceRecipientFromRecord(record)
			if decodeErr != nil {
				return nil, directNoticeReadUnknown(
					"locked audience contribution set is malformed", decodeErr,
				)
			}
			carrier.Contributions = append(carrier.Contributions, contribution)
		}
		carriers[messageID] = carrier
	}

	epoch, epochErr := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
	if epochErr != nil || epoch.Validate() != nil || epoch.TenantID != scope.TenantID {
		return nil, directNoticeReadUnknown("locked directory epoch is unavailable", epochErr)
	}
	if err := tx.refreshNow(ctx); err != nil {
		return nil, err
	}
	dbNow := tx.now.Time()
	authorized := make([]directNoticeAuthorizedRead, 0, len(inputs))
	for _, input := range inputs {
		plan, evaluateErr := evaluateDirectNoticeLockedRead(
			input.Preflight, input.IDs, channels[input.IDs.ChannelID],
			carriers[input.IDs.MessageID], epoch, dbNow,
		)
		if evaluateErr != nil {
			if hideDenied && directNoticeReadIsHidden(evaluateErr) {
				continue
			}
			return nil, evaluateErr
		}
		if len(tx.requestAuthorityFacts) != 0 {
			grantFreshUntil, constrained, deadlineErr := directNoticeReadGrantFreshUntil(
				channels[input.IDs.ChannelID].Grants, input.Preflight.Closure, dbNow,
			)
			if deadlineErr != nil {
				return nil, deadlineErr
			}
			if constrained {
				if deadlineErr := tx.narrowRequestAuthorityFreshUntil(grantFreshUntil); deadlineErr != nil {
					return nil, deadlineErr
				}
			}
		}
		authorized = append(authorized, plan)
		if visibleLimit > 0 && len(authorized) == visibleLimit {
			break
		}
	}
	return authorized, nil
}

func directNoticeReadIsHidden(err error) bool {
	return errors.Is(err, ErrCommunicationNotFound) ||
		errors.Is(err, ErrCommunicationForbidden) || errors.Is(err, store.ErrNotFound)
}

func directNoticeSortedIDSet(set map[model.ID]struct{}) []model.ID {
	ids := make([]model.ID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

func lockDirectNoticeBatchRecordSets(
	ctx context.Context,
	tx *communicationTx,
	kind model.Kind,
	specs []directNoticeReadSetSpec,
) (map[model.ID][]model.Record, error) {
	repo, err := tx.repo(kind)
	if err != nil {
		return nil, err
	}
	ordered := append([]directNoticeReadSetSpec(nil), specs...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].OwnerID.String() < ordered[j].OwnerID.String()
	})
	owners := make(map[model.ID]model.ID)
	sets := make(map[model.ID][]model.Record, len(ordered))
	seenOwners := make(map[model.ID]struct{}, len(ordered))
	for _, spec := range ordered {
		if !validCanonicalCommunicationID(spec.OwnerID) || spec.Bound < 1 {
			return nil, directNoticeReadUnknown("direct notice batch row-set spec is malformed", nil)
		}
		if _, duplicate := seenOwners[spec.OwnerID]; duplicate {
			return nil, directNoticeReadUnknown("direct notice batch repeats row-set owner", nil)
		}
		seenOwners[spec.OwnerID] = struct{}{}
		sets[spec.OwnerID] = make([]model.Record, 0)
		ownerCount := 0
		for _, filters := range spec.Queries {
			query := model.Query{
				Filters: append([]model.Filter(nil), filters...),
				Limit:   directNoticeReadSetPageSize,
			}
			seenCursors := make(map[string]struct{})
			for {
				rows, page, listErr := repo.List(ctx, query)
				if listErr != nil {
					return nil, listErr
				}
				for _, row := range rows {
					id, parseErr := directNoticeRecordID(row, model.ColID)
					if parseErr != nil {
						return nil, parseErr
					}
					if _, duplicate := owners[id]; duplicate {
						return nil, directNoticeReadUnknown(
							"direct notice batch row set repeats an ID", nil,
						)
					}
					ownerCount++
					if ownerCount > spec.Bound {
						return nil, directNoticeReadUnknown(
							"direct notice batch row set exceeds bound", nil,
						)
					}
					owners[id] = spec.OwnerID
				}
				if !page.HasMore {
					break
				}
				next, cursorErr := advanceDirectNoticeReadCursor(
					query.Cursor, page.Cursor, len(rows), seenCursors,
				)
				if cursorErr != nil {
					return nil, cursorErr
				}
				query.Cursor = next
			}
		}
	}
	ids := make([]model.ID, 0, len(owners))
	for id := range owners {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, id := range ids {
		record, lockErr := tx.lockRecord(ctx, kind, id)
		if lockErr != nil {
			return nil, normalizeDirectNoticeLockedNotFound(lockErr)
		}
		owner := owners[id]
		sets[owner] = append(sets[owner], record)
	}
	return sets, nil
}

func advanceDirectNoticeReadCursor(
	current string,
	next string,
	rowCount int,
	seen map[string]struct{},
) (string, error) {
	if rowCount < 1 || next == "" || next == current {
		return "", directNoticeReadUnknown(
			"direct notice row-set pagination did not advance", nil,
		)
	}
	if _, repeated := seen[next]; repeated {
		return "", directNoticeReadUnknown(
			"direct notice row-set pagination repeated a cursor", nil,
		)
	}
	seen[next] = struct{}{}
	return next, nil
}

func evaluateDirectNoticeLockedRead(
	preflight directNoticeReaderPreflight,
	ids directNoticeCarrierIDs,
	lockedChannel directNoticeReadLockedChannel,
	lockedCarrier directNoticeReadLockedCarrier,
	epoch model.DirectoryEpoch,
	dbNow time.Time,
) (directNoticeAuthorizedRead, error) {
	channel := lockedChannel.Channel
	grants := lockedChannel.Grants
	message := lockedCarrier.Message
	deliveries := lockedCarrier.Deliveries
	audiences := lockedCarrier.Audiences
	contributions := lockedCarrier.Contributions
	if preflight.Resolution.Recipient == nil || epoch.Validate() != nil ||
		epoch.TenantID != preflight.Scope.TenantID ||
		epoch.Version != preflight.Resolution.Recipient.DirectoryEpoch {
		return directNoticeAuthorizedRead{}, directNoticeReadUnknown(
			"locked directory epoch is unavailable", nil,
		)
	}
	if directNoticeReadRowsCarryFutureDBTime(
		channel, grants, message, deliveries, audiences, contributions, dbNow,
	) || !communicationEvidenceCurrent(
		preflight.Core.ObservedAt, preflight.Core.FreshUntil, dbNow,
	) || !communicationEvidenceCurrent(
		preflight.Resolution.ObservedAt, preflight.Resolution.FreshUntil, dbNow,
	) || !dbNow.Before(preflight.Resolution.FreshUntil) || !communicationEvidenceCurrent(
		preflight.Closure.ObservedAt, preflight.Closure.FreshUntil, dbNow,
	) {
		return directNoticeAuthorizedRead{}, directNoticeReadUnknown(
			"direct notice authority expired while waiting for locks", nil,
		)
	}
	delivery, err := exactDirectNoticeReadGraph(
		preflight, ids, channel, message, deliveries, audiences, contributions,
	)
	if err != nil {
		return directNoticeAuthorizedRead{}, err
	}
	requiredCount := int64(0)
	for _, candidate := range deliveries {
		if candidate.Required {
			requiredCount++
		}
	}
	grantEvidence := EvaluateCurrentChannelGrant(
		ChannelGrantSnapshot{
			Verdict: VerdictClean, Code: "channel_grants_locked",
			ACLRevision: channel.ACLRevision, ObservedAt: dbNow, Grants: grants,
		},
		preflight.Scope.TenantID, preflight.Scope.WorkspaceID, channel.ID,
		preflight.Closure, ChannelGrantRead, dbNow,
	)
	directoryFact := store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: model.ID(preflight.Scope.TenantID),
		Version: epoch.Version,
	}
	carrier := ProtectedCarrierRef{
		Entity: preflight.Core.Entity, ChannelID: channel.ID,
		MessageID: message.ID, DeliveryID: delivery.ID,
	}
	currentAudience, err := buildDirectNoticeCurrentAudience(
		preflight, message, delivery, audiences, contributions, dbNow,
	)
	if err != nil {
		return directNoticeAuthorizedRead{}, err
	}
	clean := func(code, ref string) AuthorityEvidence {
		return AuthorityEvidence{Verdict: VerdictClean, Code: code, EvidenceRef: ref}
	}
	decision, err := EvaluateReadGate(ReadGateEvidence{
		Scope: preflight.Scope, ChannelID: channel.ID,
		ChannelACLRevision: channel.ACLRevision, DBNow: dbNow,
		Operation: CommunicationRead, Carrier: carrier,
		CarrierState: ProtectedCarrierSnapshot{
			Message: message, Delivery: delivery, RequiredDeliveryCount: requiredCount,
			ObservedAt: dbNow,
			Evidence:   clean("carrier_rows_locked", "same_tx:direct_notice_carrier"),
		},
		Core: preflight.Core, Principal: preflight.Principal,
		PrincipalResolution: preflight.Resolution, Recipient: preflight.Recipient,
		DirectoryEpoch:      directoryFact,
		CurrentChannelGrant: grantEvidence,
		EntityRecipientGuard: BoundEntityRecipientEvidence{
			Scope: preflight.Scope, Carrier: carrier, Principal: preflight.Principal,
			Recipient: preflight.Recipient, DirectoryEpoch: epoch.Version,
			EvaluatedAt: dbNow,
			Evidence:    clean("entity_recipient_current", "same_tx:direct_notice_recipient"),
		},
		CurrentAudience: currentAudience,
	})
	if err != nil {
		return directNoticeAuthorizedRead{}, err
	}
	switch decision.Verdict {
	case VerdictUnknown:
		return directNoticeAuthorizedRead{}, directNoticeReadUnknown(
			"direct notice read gate is unavailable", nil,
		)
	case VerdictBroken:
		return directNoticeAuthorizedRead{}, directNoticeReadNotFound("direct notice is not visible")
	case VerdictClean:
	default:
		return directNoticeAuthorizedRead{}, directNoticeReadUnknown(
			"direct notice read gate has no verdict", nil,
		)
	}
	if len(decision.RequiredClaims) != 0 ||
		len(decision.SurvivingContributionIDs) != 1 ||
		decision.SurvivingContributionIDs[0] != contributions[0].ID ||
		!equalDirectNoticeAuthorityFacts(preflight.Facts, decision.Facts) {
		return directNoticeAuthorizedRead{}, directNoticeReadUnknown(
			"direct User read returned non-direct authority effects", nil,
		)
	}
	digest, err := CanonicalFulfillmentDeliverySetDigest(deliveries)
	if err != nil {
		return directNoticeAuthorizedRead{}, directNoticeReadUnknown(
			"fulfillment Delivery set is malformed", err,
		)
	}
	const fulfillmentRef = "same_tx:direct_notice_fulfillment"
	fulfillment, err := ProjectMessageFulfillment(
		message, deliveries, FulfillmentDeliverySetWitness{
			Scope: preflight.Scope, MessageID: message.ID,
			DeliveryCount: int64(len(deliveries)), RequiredCount: requiredCount,
			Digest: digest, ObservedAt: dbNow,
			Evidence:    clean("deliveries_locked", fulfillmentRef),
			EvidenceRef: fulfillmentRef,
		}, dbNow,
	)
	if err != nil {
		return directNoticeAuthorizedRead{}, directNoticeReadUnknown(
			"message fulfillment is unavailable", err,
		)
	}
	if message.Payload.Encoding != PayloadPlainJSON {
		return directNoticeAuthorizedRead{}, directNoticeReadUnknown(
			"private DirectNotice reader supports only historical plain payloads", nil,
		)
	}
	aad := ContentAAD{
		TenantID: preflight.Scope.TenantID, WorkspaceID: preflight.Scope.WorkspaceID,
		ChannelID: message.ChannelID, EntityKind: messageKind, EntityID: message.ID,
		Schema:               message.Payload.Schema,
		ProtectionGeneration: message.Payload.ProtectionGeneration,
	}
	openPlan, err := PlanProtectedPayloadRead(
		message.Payload, PayloadSlotMessage, protectedPayloadPolicyFrom(message.Payload), aad, aad,
	)
	if err != nil || openPlan.RequiresSealer {
		return directNoticeAuthorizedRead{}, directNoticeReadUnknown(
			"historical DirectNotice payload cannot be opened", err,
		)
	}
	return directNoticeAuthorizedRead{
		Message: message, Delivery: delivery, Fulfillment: fulfillment, OpenPlan: openPlan,
	}, nil
}

func directNoticeReadRowsCarryFutureDBTime(
	channel Channel,
	grants []ChannelGrant,
	message Message,
	deliveries []MessageDelivery,
	audiences []MessageAudience,
	contributions []MessageAudienceRecipient,
	dbNow time.Time,
) bool {
	if channel.CreatedAt.After(dbNow) || channel.UpdatedAt.After(dbNow) ||
		message.CreatedAt.After(dbNow) || message.UpdatedAt.After(dbNow) {
		return true
	}
	for _, grant := range grants {
		if grant.CreatedAt.After(dbNow) || grant.UpdatedAt.After(dbNow) {
			return true
		}
	}
	for _, delivery := range deliveries {
		if delivery.CreatedAt.After(dbNow) || delivery.UpdatedAt.After(dbNow) {
			return true
		}
	}
	for _, audience := range audiences {
		if audience.CreatedAt.After(dbNow) || audience.DirectorySnapshotAt.After(dbNow) {
			return true
		}
	}
	for _, contribution := range contributions {
		if contribution.CreatedAt.After(dbNow) {
			return true
		}
	}
	return false
}

func lockDirectNoticeRecordSet(
	ctx context.Context,
	tx *communicationTx,
	kind model.Kind,
	filters []model.Filter,
	bound int,
) ([]model.Record, error) {
	repo, err := tx.repo(kind)
	if err != nil {
		return nil, err
	}
	query := model.Query{Filters: append([]model.Filter(nil), filters...), Limit: directNoticeReadSetPageSize}
	ids := make([]model.ID, 0)
	seen := make(map[model.ID]struct{})
	seenCursors := make(map[string]struct{})
	for {
		rows, page, listErr := repo.List(ctx, query)
		if listErr != nil {
			return nil, listErr
		}
		for _, row := range rows {
			id, parseErr := directNoticeRecordID(row, model.ColID)
			if parseErr != nil {
				return nil, parseErr
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, directNoticeReadUnknown("locked row set repeats an ID", nil)
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
			if len(ids) > bound {
				return nil, directNoticeReadUnknown("direct notice row set exceeds bound", nil)
			}
		}
		if !page.HasMore {
			break
		}
		next, cursorErr := advanceDirectNoticeReadCursor(
			query.Cursor, page.Cursor, len(rows), seenCursors,
		)
		if cursorErr != nil {
			return nil, cursorErr
		}
		query.Cursor = next
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	locked := make([]model.Record, 0, len(ids))
	for _, id := range ids {
		record, lockErr := tx.lockRecord(ctx, kind, id)
		if lockErr != nil {
			return nil, normalizeDirectNoticeLockedNotFound(lockErr)
		}
		locked = append(locked, record)
	}
	return locked, nil
}

func lockDirectNoticeContributionSet(
	ctx context.Context,
	tx *communicationTx,
	audiences []MessageAudience,
) ([]model.Record, error) {
	repo, err := tx.repo(messageAudienceRecipientKind)
	if err != nil {
		return nil, err
	}
	ids := make([]model.ID, 0)
	seen := make(map[model.ID]struct{})
	ordered := append([]MessageAudience(nil), audiences...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID.String() < ordered[j].ID.String() })
	for _, audience := range ordered {
		query := model.Query{
			Filters: []model.Filter{{
				Column: colCommMessageAudienceID, Op: model.OpEq, Value: audience.ID.String(),
			}},
			Limit: directNoticeReadSetPageSize,
		}
		seenCursors := make(map[string]struct{})
		for {
			rows, page, listErr := repo.List(ctx, query)
			if listErr != nil {
				return nil, listErr
			}
			for _, row := range rows {
				id, parseErr := directNoticeRecordID(row, model.ColID)
				if parseErr != nil {
					return nil, parseErr
				}
				if _, duplicate := seen[id]; duplicate {
					return nil, directNoticeReadUnknown("audience contribution set repeats an ID", nil)
				}
				seen[id] = struct{}{}
				ids = append(ids, id)
				if len(ids) > directNoticeReadSetBound {
					return nil, directNoticeReadUnknown("audience contribution set exceeds bound", nil)
				}
			}
			if !page.HasMore {
				break
			}
			next, cursorErr := advanceDirectNoticeReadCursor(
				query.Cursor, page.Cursor, len(rows), seenCursors,
			)
			if cursorErr != nil {
				return nil, cursorErr
			}
			query.Cursor = next
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	locked := make([]model.Record, 0, len(ids))
	for _, id := range ids {
		record, lockErr := tx.lockRecord(ctx, messageAudienceRecipientKind, id)
		if lockErr != nil {
			return nil, normalizeDirectNoticeLockedNotFound(lockErr)
		}
		locked = append(locked, record)
	}
	return locked, nil
}

func exactDirectNoticeReadGraph(
	preflight directNoticeReaderPreflight,
	ids directNoticeCarrierIDs,
	channel Channel,
	message Message,
	deliveries []MessageDelivery,
	audiences []MessageAudience,
	contributions []MessageAudienceRecipient,
) (MessageDelivery, error) {
	if len(deliveries) != 1 || len(audiences) != 1 || len(contributions) != 1 {
		return MessageDelivery{}, directNoticeReadNotFound("carrier is not an exact direct notice")
	}
	delivery := deliveries[0]
	audience := audiences[0]
	contribution := contributions[0]
	if message.ID != ids.MessageID || message.ChannelID != channel.ID ||
		message.TenantID != preflight.Scope.TenantID || message.WorkspaceID != preflight.Scope.WorkspaceID ||
		message.Kind != MessageNotice || message.WorkItemID != "" || message.ThreadID != message.ID ||
		message.ReplyToID != "" || message.AutomationDepth != 0 || message.Sender.Kind != ActorUser ||
		delivery.ID != ids.DeliveryID || delivery.DeliverySeq != ids.DeliverySeq ||
		delivery.Recipient != preflight.Recipient || delivery.Recipient.Kind != RecipientUser ||
		audience.MessageID != message.ID || audience.Ordinal != 1 || audience.RouteRuleID != "" ||
		audience.Selector.Kind != AudienceUser || audience.Selector.Ref != delivery.Recipient.Ref ||
		audience.ResolvedCount != 1 || contribution.MessageAudienceID != audience.ID ||
		contribution.MessageDeliveryID != delivery.ID || contribution.Recipient != delivery.Recipient ||
		contribution.RecipientEpoch != delivery.RecipientEpoch ||
		contribution.Selector != audience.Selector || contribution.CausalKind != CausalDirect ||
		contribution.CausalRef != delivery.Recipient.Ref || contribution.CausalFactKind != "" ||
		contribution.CausalFactID != "" || contribution.CausalFactVersion != 0 ||
		contribution.ObservedSessionSID != "" || contribution.ObservedClaimFence != 0 ||
		contribution.OriginalSubscriber != nil || contribution.SubscriptionID != "" ||
		contribution.SubscriptionGeneration != 0 || contribution.RouteRuleID != "" ||
		contribution.RouteRuleGeneration != 0 {
		return MessageDelivery{}, directNoticeReadNotFound("carrier is not a direct User notice")
	}
	if err := ValidateMessageDeliveryLineage(message, delivery); err != nil {
		return MessageDelivery{}, directNoticeReadUnknown("direct notice lineage is malformed", err)
	}
	if audience.DirectoryEpoch != contribution.DirectoryEpoch ||
		audience.ChannelACLRevision != contribution.ChannelACLRevision ||
		audience.RouteRevision != contribution.RouteRevision ||
		audience.SubscriptionRevision != contribution.SubscriptionRevision {
		return MessageDelivery{}, directNoticeReadUnknown("direct notice audience provenance diverges", nil)
	}
	fold, err := FoldAudienceContributions(contributions)
	if err != nil {
		return MessageDelivery{}, directNoticeReadUnknown("direct notice audience fold is malformed", err)
	}
	if fold.Recipient != delivery.Recipient || fold.Required != delivery.Required ||
		fold.WakePolicy != delivery.WakePolicy ||
		!canonicalCommunicationValueEqual(fold.RouteReasons, delivery.RouteReasons) {
		return MessageDelivery{}, directNoticeReadUnknown("direct notice Delivery diverges from audience fold", nil)
	}
	audienceHash, err := CanonicalMessageAudienceHash(message, audiences, contributions)
	if err != nil || !bytes.Equal(audienceHash, message.AudienceHash) {
		return MessageDelivery{}, directNoticeReadUnknown("direct notice audience seal is unavailable", err)
	}
	return delivery, nil
}

func buildDirectNoticeCurrentAudience(
	preflight directNoticeReaderPreflight,
	message Message,
	delivery MessageDelivery,
	audiences []MessageAudience,
	contributions []MessageAudienceRecipient,
	dbNow time.Time,
) (CurrentAudienceEvidence, error) {
	setDigest, err := CanonicalCurrentAudienceSetDigest(
		preflight.Scope, message.ID, message.Version, audiences, contributions,
	)
	if err != nil {
		return CurrentAudienceEvidence{}, directNoticeReadUnknown(
			"direct notice current audience set cannot be sealed", err,
		)
	}
	clean := func(code, ref string) AuthorityEvidence {
		return AuthorityEvidence{Verdict: VerdictClean, Code: code, EvidenceRef: ref}
	}
	boundRecipient := func(check RecipientAuthorityCheck) BoundRecipientAuthorityEvidence {
		return BoundRecipientAuthorityEvidence{
			Scope: preflight.Scope, Recipient: preflight.Recipient,
			DirectoryEpoch: preflight.Resolution.Recipient.DirectoryEpoch,
			Check:          check, ObservedAt: dbNow,
			Evidence: clean("recipient_current", "resolver:direct_notice_recipient"),
		}
	}
	contribution := contributions[0]
	audience := audiences[0]
	return CurrentAudienceEvidence{
		TenantID: preflight.Scope.TenantID, WorkspaceID: preflight.Scope.WorkspaceID,
		Recipient: preflight.Recipient, DeliveryID: delivery.ID, MessageID: message.ID,
		DirectoryEpoch: preflight.Resolution.Recipient.DirectoryEpoch,
		ObservedAt:     dbNow, FreshUntil: preflight.Resolution.FreshUntil,
		RecipientExists:        boundRecipient(RecipientCheckExists),
		RecipientEligible:      boundRecipient(RecipientCheckEligible),
		RecipientNotTombstoned: boundRecipient(RecipientCheckNotTombstoned),
		SetWitness: CurrentAudienceSetWitness{
			Scope: preflight.Scope, MessageID: message.ID, MessageVersion: message.Version,
			DeliveryID: delivery.ID, Recipient: delivery.Recipient,
			MessageAudienceHash: append([]byte(nil), message.AudienceHash...),
			AudienceCount:       int64(len(audiences)),
			ContributionCount:   int64(len(contributions)),
			Audiences:           append([]MessageAudience(nil), audiences...),
			Contributions:       append([]MessageAudienceRecipient(nil), contributions...),
			SetDigest:           setDigest, ObservedAt: dbNow,
			Evidence: clean("audience_set_locked", "same_tx:direct_notice_audience_set"),
		},
		Contributions: []CausalContributionEvidence{{
			Audience: audience, Contribution: contribution,
			Witness: CausalAuthorityWitness{
				Kind: CausalAuthorityDirectPrincipal, Scope: preflight.Scope,
				ContributionID: contribution.ID, Recipient: delivery.Recipient,
				CausalKind: CausalDirect, CausalRef: delivery.Recipient.Ref,
				DirectoryEpoch: preflight.Resolution.Recipient.DirectoryEpoch,
				ObservedAt:     dbNow,
				Evidence:       clean("direct_principal_current", "resolver:direct_notice_principal"),
			},
		}},
	}, nil
}

func (m *Module) openDirectNoticeRead(
	ctx context.Context,
	authorized directNoticeAuthorizedRead,
	opener directNoticePayloadOpener,
) (DirectNoticeReadResult, error) {
	raw, err := opener(ctx, m.communicationSealer, authorized.OpenPlan)
	if err != nil {
		return DirectNoticeReadResult{}, err
	}
	var content MessageContent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&content); err != nil {
		return DirectNoticeReadResult{}, directNoticeReadUnknown(
			"opened DirectNotice content is malformed", err,
		)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return DirectNoticeReadResult{}, directNoticeReadUnknown(
			"opened DirectNotice content has trailing values", err,
		)
	}
	canonical, err := CanonicalMessageContent(content)
	if err != nil || !bytes.Equal(canonical, raw) {
		return DirectNoticeReadResult{}, directNoticeReadUnknown(
			"opened DirectNotice content is not canonical", err,
		)
	}
	message := authorized.Message
	delivery := authorized.Delivery
	return DirectNoticeReadResult{
		Message: DirectNoticeMessageView{
			ID: message.ID, Version: message.Version, ChannelID: message.ChannelID,
			ThreadID: message.ThreadID, State: message.State, Sender: message.Sender,
			Content: content, Urgency: message.Urgency, AckPolicy: message.AckPolicy,
			AckQuorum: message.AckQuorum, AvailableAt: message.AvailableAt,
			AckDueAt:    cloneDirectNoticeTime(message.AckDueAt),
			ExpiresAt:   cloneDirectNoticeTime(message.ExpiresAt),
			PublishedAt: cloneDirectNoticeTime(message.PublishedAt),
			TerminalAt:  cloneDirectNoticeTime(message.TerminalAt), TerminalCode: message.TerminalCode,
		},
		Delivery: DirectNoticeDeliveryView{
			ID: delivery.ID, Version: delivery.Version, MessageID: delivery.MessageID,
			Recipient: delivery.Recipient, DeliverySeq: delivery.DeliverySeq,
			Required: delivery.Required, State: delivery.State, AvailableAt: delivery.AvailableAt,
			FirstSeenAt:    cloneDirectNoticeTime(delivery.FirstSeenAt),
			AckDueAt:       cloneDirectNoticeTime(delivery.AckDueAt),
			ExpiresAt:      cloneDirectNoticeTime(delivery.ExpiresAt),
			AcknowledgedAt: cloneDirectNoticeTime(delivery.AcknowledgedAt),
		},
		Fulfillment: authorized.Fulfillment,
	}, nil
}

func cloneDirectNoticeTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func directNoticeReadNotFound(message string) error {
	return communicationError(ErrCommunicationNotFound, "%s", message)
}

func directNoticeReadUnknown(message string, cause error) error {
	if cause == nil {
		return communicationError(ErrCommunicationEvidenceUnknown, "%s", message)
	}
	return fmt.Errorf("%w: %s: %v", ErrCommunicationEvidenceUnknown, message, cause)
}

func normalizeDirectNoticeLockedNotFound(err error) error {
	if errors.Is(err, ErrCommunicationEvidenceUnknown) {
		return err
	}
	if errors.Is(err, store.ErrNotFound) {
		return directNoticeReadNotFound("direct notice carrier is absent from scope")
	}
	return err
}

func normalizeDirectNoticePointReadError(err error) error {
	if errors.Is(err, ErrCommunicationEvidenceUnknown) {
		return err
	}
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, ErrCommunicationForbidden) {
		return directNoticeReadNotFound("message is not visible")
	}
	return err
}
