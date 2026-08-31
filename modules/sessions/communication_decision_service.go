// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	decisionResponseMethod      = http.MethodPost
	decisionResponseOperation   = "decision.request.respond"
	decisionResponseAuditAction = "sessions.communication.decision_request.respond"
	decisionResponseEventType   = "work.decision.request.responded"
	decisionDeadlineOperation   = "decision.request.expire"
	decisionDeadlineAuditAction = "sessions.communication.decision_request.expire"
	decisionDeadlineEventType   = "work.decision.request.expired"
	decisionDeadlineElapsedCode = "decision_deadline_elapsed"
)

var (
	errDecisionResponseVersionRequired = errors.New(
		"sessions: DecisionResponse version_required",
	)
	errDecisionResponseVersionMismatch = fmt.Errorf(
		"%w: DecisionResponse version_mismatch", store.ErrConflict,
	)
	errDecisionResponseIdempotencyReused = fmt.Errorf(
		"%w: DecisionResponse idempotency_key_reused", store.ErrConflict,
	)
)

// DecisionRequestResponseCommand is the caller-authored portion of one
// DecisionRequest transition. Carrier IDs, actor, accepted delivery, response
// ID and any WorkDecision ID are always server-authored.
type DecisionRequestResponseCommand struct {
	Transition        DecisionTransition      `json:"transition"`
	Response          DecisionResponseContent `json:"response"`
	BlockerWorkItemID model.ID                `json:"blocker_work_item_id,omitempty"`
	IfMatch           string                  `json:"-"`
	IdempotencyKey    string                  `json:"-"`
}

// DecisionRequestResponseResult is the closed, content-free result retained
// in the durable communication command receipt.
type DecisionRequestResponseResult struct {
	CommandID      model.ID             `json:"command_id"`
	RequestID      model.ID             `json:"request_id"`
	ResponseID     model.ID             `json:"response_id"`
	MessageID      model.ID             `json:"message_id"`
	WorkItemID     model.ID             `json:"work_item_id"`
	WorkDecisionID model.ID             `json:"work_decision_id,omitempty"`
	EventID        model.ID             `json:"event_id"`
	Version        int64                `json:"version"`
	ETag           string               `json:"etag"`
	State          DecisionRequestState `json:"state"`
	AuditSeq       int64                `json:"audit_seq"`
	Replayed       bool                 `json:"-"`
}

type decisionResponseNormalizedCommand struct {
	command            DecisionRequestResponseCommand
	scope              DirectoryScopeRef
	principal          CommunicationPrincipal
	requestID          model.ID
	expectedVersion    int64
	method             string
	path               string
	commandScope       string
	actor              CommunicationActorRef
	actorFingerprint   []byte
	idempotencyKeyHash []byte
	requestDigest      []byte
}

type decisionResponseIDs struct {
	Command  model.ID
	Receipt  model.ID
	Response model.ID
}

func newDecisionResponseIDs() decisionResponseIDs {
	return decisionResponseIDs{
		Command: model.NewID(), Receipt: model.NewID(), Response: model.NewID(),
	}
}

type decisionResponsePreparedContent struct {
	response      ProtectedPayload
	choiceWitness *DecisionChoiceWitness
	requestHash   []byte
	responseHash  []byte
}

// decisionDeadlineAuthorizer is the narrow, private reaper-composition port.
// It returns both the system worker authority and the exact current C5 reader
// preflight for the DecisionRequest carrier. Neither is accepted from wire
// input, and both are pinned again in the effect transaction.
type decisionDeadlineAuthorizer interface {
	AuthorizeDecisionDeadline(
		context.Context,
		DirectoryScopeRef,
		model.ID,
	) (decisionDeadlineAuthority, error)
}

type decisionDeadlineAuthority struct {
	Actor      CommunicationActorRef
	Reader     directNoticeReaderPreflight
	Facts      []store.AuthorizationFactRef
	ObservedAt time.Time
	FreshUntil time.Time
	Evidence   AuthorityEvidence
}

type decisionDeadlineService struct {
	module     *Module
	authorizer decisionDeadlineAuthorizer
	newID      func() model.ID
}

// decisionDeadlineCommand is private while K3 remains publicly OFF.
type decisionDeadlineCommand struct {
	RequestID       model.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

type decisionDeadlineNormalized struct {
	decisionResponseNormalizedCommand
	authority decisionDeadlineAuthority
}

// RespondDecisionRequest is the future handler-facing boundary. K3 remains
// deny-closed until the aggregate readiness conjunction becomes effective.
func (m *Module) RespondDecisionRequest(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	requestID model.ID,
	cmd DecisionRequestResponseCommand,
) (DecisionRequestResponseResult, error) {
	return m.respondDecisionRequestWithCurrentAuthority(
		ctx, scope, ref, requestID, cmd, true,
	)
}

// newDecisionDeadlineService is intentionally private and unwired. Future
// composition can attach a reaper without exposing public K3 traffic.
func newDecisionDeadlineService(
	module *Module,
	authorizer decisionDeadlineAuthorizer,
	newID func() model.ID,
) (*decisionDeadlineService, error) {
	if module == nil || authorizer == nil {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest deadline ports are unavailable",
		)
	}
	if newID == nil {
		newID = model.NewID
	}
	return &decisionDeadlineService{module: module, authorizer: authorizer, newID: newID}, nil
}

// Expire closes one non-terminal DecisionRequest at or after its durable due
// time. The system response contains only a protected, server-authored reason.
func (s *decisionDeadlineService) Expire(
	ctx context.Context,
	scope DirectoryScopeRef,
	cmd decisionDeadlineCommand,
) (DecisionRequestResponseResult, error) {
	if s == nil || s.module == nil || s.authorizer == nil || ctx == nil {
		return DecisionRequestResponseResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest deadline service is unavailable",
		)
	}
	authority, err := s.authorizer.AuthorizeDecisionDeadline(ctx, scope, cmd.RequestID)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	normalized, err := normalizeDecisionDeadlineCommand(scope, authority, cmd)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	ids, err := newDecisionDeadlineIDs(s.newID)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	prepared, err := s.module.prepareDecisionDeadlineContent(
		ctx, normalized, ids.Response,
	)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	return s.module.applyDecisionRequestDeadline(ctx, normalized, ids, prepared)
}

func newDecisionDeadlineIDs(newID func() model.ID) (decisionResponseIDs, error) {
	if newID == nil {
		return decisionResponseIDs{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DecisionRequest deadline ID source is unavailable",
		)
	}
	ids := decisionResponseIDs{Command: newID(), Receipt: newID(), Response: newID()}
	if !validCanonicalCommunicationID(ids.Command) ||
		!validCanonicalCommunicationID(ids.Receipt) ||
		!validCanonicalCommunicationID(ids.Response) ||
		ids.Command == ids.Receipt || ids.Command == ids.Response || ids.Receipt == ids.Response {
		return decisionResponseIDs{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DecisionRequest deadline IDs are unavailable",
		)
	}
	return ids, nil
}

// respondDecisionRequestWithAuthority is the private integration seam. It
// bypasses only aggregate readiness; exact Core authority, current grant,
// current audience, ETag and idempotency remain mandatory.
func (m *Module) respondDecisionRequestWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	requestID model.ID,
	cmd DecisionRequestResponseCommand,
) (DecisionRequestResponseResult, error) {
	return m.respondDecisionRequestWithCurrentAuthority(
		ctx, scope, ref, requestID, cmd, false,
	)
}

func (m *Module) respondDecisionRequestWithCurrentAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	requestID model.ID,
	cmd DecisionRequestResponseCommand,
	requireReadiness bool,
) (DecisionRequestResponseResult, error) {
	question, bound, inspected, identity, normalized, window, err :=
		m.prepareDecisionResponseAuthority(ctx, scope, ref, requestID, cmd)
	if err != nil {
		return DecisionRequestResponseResult{}, normalizeDecisionResponseError(err)
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return DecisionRequestResponseResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}

	receipt, found, err := m.lookupDecisionResponseReceipt(ctx, normalized)
	if err != nil {
		return DecisionRequestResponseResult{}, normalizeDecisionResponseError(err)
	}
	if found {
		if !bytes.Equal(receipt.RequestDigest, normalized.requestDigest) {
			return DecisionRequestResponseResult{}, errDecisionResponseIdempotencyReused
		}
		if _, err := m.authorizeDecisionRequestCarrier(
			ctx, question, bound, inspected, identity, window, normalized, false,
		); err != nil {
			return DecisionRequestResponseResult{}, normalizeDecisionResponseError(err)
		}
		result, err := m.decisionResponseResultFromReceipt(ctx, normalized, receipt)
		if err != nil {
			return DecisionRequestResponseResult{}, normalizeDecisionResponseError(err)
		}
		result.Replayed = true
		return result, nil
	}

	// Protected content is opened and sealed outside the write transaction.
	// The authorization transaction returns only an AAD-bound open plan and a
	// finite authority window; the write transaction reauthorizes the carrier.
	authorized, err := m.authorizeDecisionRequestCarrier(
		ctx, question, bound, inspected, identity, window, normalized, true,
	)
	if err != nil {
		return DecisionRequestResponseResult{}, normalizeDecisionResponseError(err)
	}
	ids := newDecisionResponseIDs()
	prepared, err := m.prepareDecisionResponseContent(ctx, authorized, normalized, ids.Response)
	if err != nil {
		return DecisionRequestResponseResult{}, normalizeDecisionResponseError(err)
	}

	question, bound, inspected, identity, rebound, window, err :=
		m.prepareDecisionResponseAuthority(ctx, scope, ref, requestID, cmd)
	if err != nil {
		return DecisionRequestResponseResult{}, normalizeDecisionResponseError(err)
	}
	if !equalDecisionResponseNormalized(normalized, rebound) {
		return DecisionRequestResponseResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionResponse authority rebind changed the exact command",
		)
	}
	result, err := m.applyDecisionRequestResponse(
		ctx, question, bound, inspected, identity, window,
		normalized, ids, prepared,
	)
	return result, normalizeDecisionResponseError(err)
}

func (m *Module) prepareDecisionResponseAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	requestID model.ID,
	cmd DecisionRequestResponseCommand,
) (
	communicationAuthorityQuestion,
	communicationRequestAuthority,
	communicationRequestAuthorityInspection,
	directNoticeReaderIdentityPreflight,
	decisionResponseNormalizedCommand,
	communicationAuthorityWindow,
	error,
) {
	if !validCanonicalCommunicationID(requestID) {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			decisionResponseNormalizedCommand{}, communicationAuthorityWindow{},
			communicationError(ErrInvalidCommunicationModel, "invalid DecisionRequest target")
	}
	question, err := newCommunicationAuthorityQuestion(
		scope, decisionRequestKind, requestID, CommunicationDecisionRequestWrite,
	)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			decisionResponseNormalizedCommand{}, communicationAuthorityWindow{}, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			decisionResponseNormalizedCommand{}, communicationAuthorityWindow{}, err
	}
	inspected, err := bound.contextFor(question)
	if err != nil || inspected.question != question {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			decisionResponseNormalizedCommand{}, communicationAuthorityWindow{},
			communicationError(
				ErrCommunicationEvidenceUnknown,
				"decision-write authority context crossed its exact request",
			)
	}
	if err := requireDirectNoticeUserBackedPrincipal(inspected); err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			decisionResponseNormalizedCommand{}, communicationAuthorityWindow{}, err
	}
	normalized, err := normalizeDecisionRequestResponseCommand(
		scope, inspected.principal, requestID, cmd,
	)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			decisionResponseNormalizedCommand{}, communicationAuthorityWindow{}, err
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(
		ctx, scope, inspected.principal, nil,
	)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			decisionResponseNormalizedCommand{}, communicationAuthorityWindow{}, err
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			decisionResponseNormalizedCommand{}, communicationAuthorityWindow{}, err
	}
	return question, bound, inspected, identity, normalized, window, nil
}

func normalizeDecisionRequestResponseCommand(
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	requestID model.ID,
	cmd DecisionRequestResponseCommand,
) (decisionResponseNormalizedCommand, error) {
	if err := scope.Validate(); err != nil {
		return decisionResponseNormalizedCommand{}, err
	}
	if err := ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return decisionResponseNormalizedCommand{}, err
	}
	if principal.UserID == "" || principal.AgentExternalID != "" || principal.SessionID != "" ||
		principal.SessionRunRef != "" || principal.SessionFence != 0 ||
		principal.SessionWorkspaceID != "" || principal.PurposeRestricted || principal.System ||
		principal.SystemActorRef != "" || principal.SystemGrantAgentID != "" ||
		!validCanonicalCommunicationID(requestID) {
		return decisionResponseNormalizedCommand{}, communicationError(
			ErrCommunicationForbidden,
			"DecisionResponse requires a claim-free authenticated User",
		)
	}
	if _, err := NextDecisionRequestState(DecisionPending, cmd.Transition); err != nil &&
		cmd.Transition != DecisionBlock {
		return decisionResponseNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel, "invalid DecisionResponse transition",
		)
	}
	if cmd.Transition == DecisionExpire {
		return decisionResponseNormalizedCommand{}, communicationError(
			ErrCommunicationForbidden,
			"DecisionRequest expiry is reserved for the database-time worker",
		)
	}
	if _, err := CanonicalProtectedPayloadSlot(PayloadSlotDecisionResponse, cmd.Response); err != nil {
		return decisionResponseNormalizedCommand{}, err
	}
	if cmd.Transition == DecisionResolve {
		if !boundedToken(cmd.Response.ChoiceKey, 128) {
			return decisionResponseNormalizedCommand{}, communicationError(
				ErrInvalidCommunicationModel, "resolved DecisionResponse requires a choice key",
			)
		}
	} else if cmd.Response.ChoiceKey != "" {
		return decisionResponseNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel,
			"non-resolved DecisionResponse must not carry a choice key",
		)
	}
	if cmd.Transition == DecisionBlock {
		if cmd.BlockerWorkItemID != "" && !validCanonicalCommunicationID(cmd.BlockerWorkItemID) {
			return decisionResponseNormalizedCommand{}, communicationError(
				ErrInvalidCommunicationModel, "invalid blocker WorkItem",
			)
		}
	} else if cmd.BlockerWorkItemID != "" {
		return decisionResponseNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel,
			"only blocked DecisionResponse may carry a blocker WorkItem",
		)
	}
	expectedVersion, err := parseDecisionResponseETag(cmd.IfMatch)
	if err != nil {
		return decisionResponseNormalizedCommand{}, err
	}
	idempotencyID, err := model.ParseID(cmd.IdempotencyKey)
	if err != nil || !validCanonicalCommunicationID(idempotencyID) ||
		idempotencyID.String() != cmd.IdempotencyKey {
		return decisionResponseNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel, "DecisionResponse idempotency key is invalid",
		)
	}
	path := "/v1/m/sessions/decision-requests/" + requestID.String() + "/responses"
	commandScope := fmt.Sprintf(
		"%s %s;workspace=%s", decisionResponseMethod, path, scope.WorkspaceID,
	)
	if !validateOpaqueRef(commandScope) {
		return decisionResponseNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel, "DecisionResponse command scope is invalid",
		)
	}
	actor := CommunicationActorRef{Kind: ActorUser, Ref: principal.UserID.String()}
	actorRaw, err := canonicalJSON(actor)
	if err != nil {
		return decisionResponseNormalizedCommand{}, err
	}
	actorFingerprint := sha256.Sum256(actorRaw)
	idempotencyHash := sha256.Sum256([]byte(cmd.IdempotencyKey))
	requestRaw, err := canonicalJSON(struct {
		Operation         string                  `json:"operation"`
		Method            string                  `json:"method"`
		Path              string                  `json:"path"`
		RequestID         model.ID                `json:"request_id"`
		Transition        DecisionTransition      `json:"transition"`
		Response          DecisionResponseContent `json:"response"`
		BlockerWorkItemID model.ID                `json:"blocker_work_item_id,omitempty"`
		IfMatch           string                  `json:"if_match"`
	}{
		Operation: decisionResponseOperation, Method: decisionResponseMethod,
		Path: path, RequestID: requestID, Transition: cmd.Transition,
		Response: cmd.Response, BlockerWorkItemID: cmd.BlockerWorkItemID,
		IfMatch: cmd.IfMatch,
	})
	if err != nil {
		return decisionResponseNormalizedCommand{}, err
	}
	requestDigest := sha256.Sum256(requestRaw)
	return decisionResponseNormalizedCommand{
		command: cmd, scope: scope, principal: principal, requestID: requestID,
		expectedVersion: expectedVersion, method: decisionResponseMethod, path: path,
		commandScope: commandScope, actor: actor,
		actorFingerprint: actorFingerprint[:], idempotencyKeyHash: idempotencyHash[:],
		requestDigest: requestDigest[:],
	}, nil
}

func normalizeDecisionDeadlineCommand(
	scope DirectoryScopeRef,
	authority decisionDeadlineAuthority,
	cmd decisionDeadlineCommand,
) (decisionDeadlineNormalized, error) {
	if err := scope.Validate(); err != nil {
		return decisionDeadlineNormalized{}, err
	}
	if !validCanonicalCommunicationID(cmd.RequestID) || cmd.ExpectedVersion < 1 {
		return decisionDeadlineNormalized{}, communicationError(
			ErrInvalidCommunicationModel, "invalid DecisionRequest deadline command",
		)
	}
	if err := validateDecisionDeadlineAuthority(scope, cmd.RequestID, authority); err != nil {
		return decisionDeadlineNormalized{}, err
	}
	keyID, err := model.ParseID(cmd.IdempotencyKey)
	if err != nil || !validCanonicalCommunicationID(keyID) || keyID.String() != cmd.IdempotencyKey {
		return decisionDeadlineNormalized{}, communicationError(
			ErrInvalidCommunicationModel,
			"DecisionRequest deadline idempotency key is invalid",
		)
	}
	response := DecisionResponseContent{Reason: CommunicationReasonContent{
		Code: decisionDeadlineElapsedCode,
		Text: "Decision request deadline elapsed",
	}}
	if _, err := CanonicalProtectedPayloadSlot(PayloadSlotDecisionResponse, response); err != nil {
		return decisionDeadlineNormalized{}, err
	}
	path := "/internal/decision-requests/" + cmd.RequestID.String() + "/deadline"
	commandScope := fmt.Sprintf("REAPER %s;workspace=%s", path, scope.WorkspaceID)
	if !validateOpaqueRef(commandScope) {
		return decisionDeadlineNormalized{}, communicationError(
			ErrInvalidCommunicationModel,
			"DecisionRequest deadline command scope is invalid",
		)
	}
	actorRaw, err := canonicalJSON(authority.Actor)
	if err != nil {
		return decisionDeadlineNormalized{}, err
	}
	actorFingerprint := sha256.Sum256(actorRaw)
	idempotencyHash := sha256.Sum256([]byte(cmd.IdempotencyKey))
	requestRaw, err := canonicalJSON(struct {
		Operation       string                  `json:"operation"`
		RequestID       model.ID                `json:"request_id"`
		ExpectedVersion int64                   `json:"expected_version"`
		Response        DecisionResponseContent `json:"response"`
	}{decisionDeadlineOperation, cmd.RequestID, cmd.ExpectedVersion, response})
	if err != nil {
		return decisionDeadlineNormalized{}, err
	}
	requestDigest := sha256.Sum256(requestRaw)
	authority.Reader.Core = cloneCommunicationRequestAuthorityWitness(authority.Reader.Core)
	authority.Reader.Resolution = cloneDirectNoticePrincipalResolution(authority.Reader.Resolution)
	authority.Reader.Closure = cloneDirectNoticeChannelGrantSubjectClosure(authority.Reader.Closure)
	authority.Reader.Facts = append([]store.AuthorizationFactRef(nil), authority.Reader.Facts...)
	authority.Facts = append([]store.AuthorizationFactRef(nil), authority.Facts...)
	normalized := decisionResponseNormalizedCommand{
		command: DecisionRequestResponseCommand{
			Transition: DecisionExpire, Response: response,
			IfMatch:        fmt.Sprintf("\"v%d\"", cmd.ExpectedVersion),
			IdempotencyKey: cmd.IdempotencyKey,
		},
		scope: scope, principal: authority.Reader.Principal, requestID: cmd.RequestID,
		expectedVersion: cmd.ExpectedVersion, method: "REAPER", path: path,
		commandScope: commandScope, actor: authority.Actor,
		actorFingerprint: actorFingerprint[:], idempotencyKeyHash: idempotencyHash[:],
		requestDigest: requestDigest[:],
	}
	return decisionDeadlineNormalized{
		decisionResponseNormalizedCommand: normalized, authority: authority,
	}, nil
}

func validateDecisionDeadlineAuthority(
	scope DirectoryScopeRef,
	requestID model.ID,
	authority decisionDeadlineAuthority,
) error {
	if authority.Actor.Validate() != nil || authority.Actor.Kind != ActorSystem {
		return communicationError(
			ErrCommunicationForbidden,
			"DecisionRequest deadline requires the system reaper",
		)
	}
	if authority.ObservedAt.IsZero() || !authority.FreshUntil.After(authority.ObservedAt) ||
		ValidateAuthorityEvidence(authority.Evidence) != nil ||
		evidenceVerdict(authority.Evidence) != VerdictClean {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest deadline authority is unavailable",
		)
	}
	reader := authority.Reader
	identity := directNoticeReaderIdentityPreflight{
		Scope: reader.Scope, Principal: reader.Principal, Recipient: reader.Recipient,
		Resolution: reader.Resolution, Closure: reader.Closure,
	}
	rebuilt, err := directNoticeReaderPreflightWithCore(identity, reader.Core)
	wantEntity := EntityRef{
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		Kind: decisionRequestKind, ID: requestID,
	}
	if err != nil || reader.Scope != scope || reader.Core.Outcome != ReadAllow ||
		reader.Core.Operation != CommunicationDecisionRequestWrite ||
		reader.Core.Entity != wantEntity || reader.Core.Principal != reader.Principal ||
		ValidatePrincipalResolution(reader.Resolution) != nil ||
		reader.Resolution.Outcome != PrincipalResolved || reader.Resolution.Recipient == nil ||
		reader.Resolution.Recipient.Recipient != reader.Recipient ||
		reader.Resolution.Scope != scope || reader.Resolution.Principal != reader.Principal ||
		reader.Closure.Scope != scope || reader.Closure.Principal != reader.Principal ||
		reader.Closure.DirectoryEpoch != reader.Resolution.Recipient.DirectoryEpoch ||
		reader.Closure.Outcome != ReadAllow ||
		!equalDirectNoticeAuthorityFacts(rebuilt.Facts, reader.Facts) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest deadline C5 authority is unavailable",
		)
	}
	facts, err := CanonicalAuthorizationFacts(authority.Facts)
	if err != nil || len(facts) == 0 || !equalDirectNoticeAuthorityFacts(facts, authority.Facts) ||
		!decisionDeadlineFactsContain(facts, reader.Facts) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest deadline authority facts are unavailable",
		)
	}
	return nil
}

func decisionDeadlineFactsContain(
	all []store.AuthorizationFactRef,
	required []store.AuthorizationFactRef,
) bool {
	type factKey struct {
		kind model.Kind
		id   model.ID
	}
	indexed := make(map[factKey]store.AuthorizationFactRef, len(all))
	for _, fact := range all {
		indexed[factKey{kind: fact.Kind, id: fact.ID}] = fact
	}
	for _, fact := range required {
		if indexed[factKey{kind: fact.Kind, id: fact.ID}] != fact {
			return false
		}
	}
	return true
}

func (m *Module) prepareDecisionDeadlineContent(
	ctx context.Context,
	normalized decisionDeadlineNormalized,
	responseID model.ID,
) (decisionResponsePreparedContent, error) {
	if !validCanonicalCommunicationID(responseID) {
		return decisionResponsePreparedContent{}, communicationError(
			ErrInvalidCommunicationModel,
			"invalid DecisionRequest deadline response ID",
		)
	}
	var prepared decisionResponsePreparedContent
	err := m.viewCommunication(ctx, normalized.scope, func(sc store.Scope) error {
		requests, err := sc.Ext(decisionRequestKind)
		if err != nil {
			return err
		}
		requestRecord, err := requests.Get(ctx, normalized.requestID)
		if err != nil {
			return err
		}
		request, err := decisionRequestFromRecord(requestRecord)
		if err != nil {
			return err
		}
		messages, err := sc.Ext(messageKind)
		if err != nil {
			return err
		}
		messageRecord, err := messages.Get(ctx, request.MessageID)
		if err != nil {
			return err
		}
		deliveries, err := sc.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		rows, page, err := deliveries.List(ctx, model.Query{Filters: []model.Filter{{
			Column: colCommMessageID, Op: model.OpEq, Value: request.MessageID.String(),
		}}, Limit: directNoticeReadSetBound})
		if err != nil || page.HasMore {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"DecisionRequest deadline Delivery set is unavailable",
			)
		}
		required := int64(0)
		for _, row := range rows {
			delivery, decodeErr := messageDeliveryFromRecord(row)
			if decodeErr != nil {
				return decodeErr
			}
			if delivery.Required {
				required++
			}
		}
		message, err := messageFromRecord(messageRecord, required)
		if err != nil {
			return err
		}
		if err := ValidateDecisionRequestLineage(message, request); err != nil {
			return err
		}
		policy := protectedPayloadPolicyFrom(request.Request)
		schema, _ := PayloadSlotDecisionResponse.schema()
		response, err := PrepareProtectedPayload(
			ctx, m.communicationSealer, PayloadSlotDecisionResponse, policy,
			ContentAAD{
				TenantID:    normalized.scope.TenantID,
				WorkspaceID: normalized.scope.WorkspaceID,
				ChannelID:   message.ChannelID, EntityKind: decisionResponseKind,
				EntityID: responseID, Schema: schema,
				ProtectionGeneration: policy.ProtectionGeneration,
			},
			normalized.command.Response,
		)
		if err != nil {
			return err
		}
		requestHash, err := CanonicalProtectedPayloadEnvelopeHash(request.Request)
		if err != nil {
			return err
		}
		responseHash, err := CanonicalProtectedPayloadEnvelopeHash(response)
		if err != nil {
			return err
		}
		prepared = decisionResponsePreparedContent{
			response: response, requestHash: requestHash, responseHash: responseHash,
		}
		return nil
	})
	return prepared, err
}

func parseDecisionResponseETag(value string) (int64, error) {
	if value == "" {
		return 0, errDecisionResponseVersionRequired
	}
	if len(value) < 4 || value[:2] != "\"v" || value[len(value)-1] != '"' {
		return 0, communicationError(
			ErrInvalidCommunicationModel,
			"DecisionResponse If-Match is not a strong version tag",
		)
	}
	version, err := strconv.ParseInt(value[2:len(value)-1], 10, 64)
	if err != nil || version < 1 || value != fmt.Sprintf("\"v%d\"", version) {
		return 0, communicationError(
			ErrInvalidCommunicationModel,
			"DecisionResponse If-Match is not canonical",
		)
	}
	return version, nil
}

func equalDecisionResponseNormalized(left, right decisionResponseNormalizedCommand) bool {
	return left.command.Transition == right.command.Transition &&
		left.command.Response.ChoiceKey == right.command.Response.ChoiceKey &&
		canonicalCommunicationValueEqual(left.command.Response.Reason, right.command.Response.Reason) &&
		left.command.BlockerWorkItemID == right.command.BlockerWorkItemID &&
		left.command.IfMatch == right.command.IfMatch &&
		left.command.IdempotencyKey == right.command.IdempotencyKey &&
		left.scope == right.scope && left.principal == right.principal &&
		left.requestID == right.requestID && left.expectedVersion == right.expectedVersion &&
		left.method == right.method && left.path == right.path &&
		left.commandScope == right.commandScope && left.actor == right.actor &&
		bytes.Equal(left.actorFingerprint, right.actorFingerprint) &&
		bytes.Equal(left.idempotencyKeyHash, right.idempotencyKeyHash) &&
		bytes.Equal(left.requestDigest, right.requestDigest)
}

func (m *Module) prepareDecisionResponseContent(
	ctx context.Context,
	authorized decisionRequestAuthorizedCarrier,
	normalized decisionResponseNormalizedCommand,
	responseID model.ID,
) (decisionResponsePreparedContent, error) {
	raw, err := OpenProtectedPayload(ctx, m.communicationSealer, authorized.requestOpenPlan)
	if err != nil {
		return decisionResponsePreparedContent{}, err
	}
	var requestContent DecisionRequestContent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&requestContent); err != nil {
		return decisionResponsePreparedContent{}, communicationError(
			ErrCommunicationEvidenceUnknown, "opened DecisionRequest content is malformed",
		)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return decisionResponsePreparedContent{}, communicationError(
			ErrCommunicationEvidenceUnknown, "opened DecisionRequest content has trailing values",
		)
	}
	if normalized.command.Transition == DecisionResolve {
		found := false
		for _, choice := range requestContent.Choices {
			found = found || choice.Key == normalized.command.Response.ChoiceKey
		}
		if !found {
			return decisionResponsePreparedContent{}, communicationError(
				ErrCommunicationForbidden,
				"DecisionResponse choice is absent from the request",
			)
		}
	}
	schema, _ := PayloadSlotDecisionResponse.schema()
	policy := protectedPayloadPolicyFrom(authorized.request.Request)
	response, err := PrepareProtectedPayload(
		ctx, m.communicationSealer, PayloadSlotDecisionResponse, policy,
		ContentAAD{
			TenantID: normalized.scope.TenantID, WorkspaceID: normalized.scope.WorkspaceID,
			ChannelID: authorized.message.ChannelID, EntityKind: decisionResponseKind,
			EntityID: responseID, Schema: schema,
			ProtectionGeneration: policy.ProtectionGeneration,
		},
		normalized.command.Response,
	)
	if err != nil {
		return decisionResponsePreparedContent{}, err
	}
	requestHash, err := CanonicalProtectedPayloadEnvelopeHash(authorized.request.Request)
	if err != nil {
		return decisionResponsePreparedContent{}, err
	}
	responseHash, err := CanonicalProtectedPayloadEnvelopeHash(response)
	if err != nil {
		return decisionResponsePreparedContent{}, err
	}
	prepared := decisionResponsePreparedContent{
		response: response, requestHash: requestHash, responseHash: responseHash,
	}
	if normalized.command.Transition == DecisionResolve {
		prepared.choiceWitness = &DecisionChoiceWitness{
			Scope: normalized.scope, RequestID: normalized.requestID,
			RequestEnvelopeHash:  append([]byte(nil), requestHash...),
			ResponseEnvelopeHash: append([]byte(nil), responseHash...),
			ChoiceKey:            normalized.command.Response.ChoiceKey,
			ObservedAt:           authorized.observedAt, FreshUntil: authorized.freshUntil,
			Evidence: AuthorityEvidence{
				Verdict: VerdictClean, Code: "choice_member",
				EvidenceRef: "authorized_open:decision_request_choice",
			},
		}
	}
	return prepared, nil
}

func (m *Module) lookupDecisionResponseReceipt(
	ctx context.Context,
	normalized decisionResponseNormalizedCommand,
) (CommunicationCommandReceipt, bool, error) {
	var receipt CommunicationCommandReceipt
	var found bool
	err := m.viewCommunication(ctx, normalized.scope, func(sc store.Scope) error {
		repo, err := sc.Ext(communicationCommandKind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(ctx, model.Query{Filters: []model.Filter{
			{Column: colCommActorFingerprint, Op: model.OpEq, Value: normalized.actorFingerprint},
			{Column: colCommCommandScope, Op: model.OpEq, Value: normalized.commandScope},
			{Column: colCommIdempotencyKeyHash, Op: model.OpEq, Value: normalized.idempotencyKeyHash},
		}, Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) == 0 && !page.HasMore {
			return nil
		}
		if len(rows) != 1 || page.HasMore {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"DecisionResponse receipt uniqueness is unavailable",
			)
		}
		receipt, err = communicationCommandReceiptFromRecord(rows[0])
		if err != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"DecisionResponse receipt cannot be decoded",
			)
		}
		if receipt.TenantID != normalized.scope.TenantID ||
			receipt.WorkspaceID != normalized.scope.WorkspaceID ||
			receipt.CommandScope != normalized.commandScope ||
			!bytes.Equal(receipt.ActorFingerprint, normalized.actorFingerprint) ||
			!bytes.Equal(receipt.IdempotencyKeyHash, normalized.idempotencyKeyHash) {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"DecisionResponse receipt crosses command scope",
			)
		}
		found = true
		return nil
	})
	return receipt, found, err
}

func (m *Module) decisionResponseResultFromReceipt(
	ctx context.Context,
	normalized decisionResponseNormalizedCommand,
	receipt CommunicationCommandReceipt,
) (DecisionRequestResponseResult, error) {
	workPrincipal, err := decisionResponseWorkPrincipal(normalized)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	_, auditAction, _, err := decisionResponseLifecycleMetadata(normalized)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	projection := receipt.ResponseProjectionJSON
	state := DecisionRequestState(projection.State)
	workDecisionID := projection.IDs["result_id"]
	if ValidateCommunicationCommandReceipt(receipt) != nil ||
		receipt.ResultKind != string(decisionResponseKind) || receipt.ResultID == "" ||
		receipt.HTTPStatus != http.StatusOK || receipt.EventID == "" ||
		projection.IDs["response_id"] != receipt.ResultID ||
		projection.IDs["request_id"] != normalized.requestID ||
		projection.IDs["message_id"] == "" || projection.IDs["work_item_id"] == "" ||
		projection.IDs["event_id"] != receipt.EventID || projection.Version < 2 ||
		!state.Valid() || !bytes.Equal(projection.Digests["request"], receipt.RequestDigest) ||
		!bytes.Equal(projection.Digests["plan"], receipt.PlanHash) ||
		len(projection.Digests["response"]) != sha256.Size ||
		(state == DecisionResolved) != validCanonicalCommunicationID(workDecisionID) {
		return DecisionRequestResponseResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionResponse receipt projection is unavailable",
		)
	}
	result := DecisionRequestResponseResult{
		CommandID: receipt.CommandID, RequestID: normalized.requestID,
		ResponseID: receipt.ResultID, MessageID: projection.IDs["message_id"],
		WorkItemID: projection.IDs["work_item_id"], WorkDecisionID: workDecisionID,
		EventID: receipt.EventID, Version: projection.Version,
		ETag: fmt.Sprintf("\"v%d\"", projection.Version), State: state,
		AuditSeq: receipt.AuditSeq,
	}
	err = m.viewCommunication(ctx, normalized.scope, func(sc store.Scope) error {
		reader, ok := sc.Audit().(store.VerifiedAuditAnchorReader)
		if !ok {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"DecisionResponse verified audit reader is unavailable",
			)
		}
		audit, _, present, err := reader.ReadVerifiedAuditAnchor(ctx, receipt.AuditSeq)
		if err != nil || !present || audit.Seq != receipt.AuditSeq ||
			audit.TenantID != normalized.scope.TenantID ||
			audit.Actor != workPrincipal.Actor ||
			audit.ActorKind != workPrincipal.ActorKind || audit.Action != auditAction ||
			audit.TargetKind != communicationCommandKind || audit.TargetID != receipt.CommandID ||
			!bytes.Equal(audit.PayloadHash, receipt.PlanHash) ||
			!bytes.Equal(audit.Hash, receipt.AuditHash) {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"DecisionResponse receipt audit anchor does not match",
			)
		}
		events, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		eventRows, _, err := events.List(ctx, model.Query{Filters: []model.Filter{{
			Column: colEventID, Op: model.OpEq, Value: receipt.EventID.String(),
		}}, Limit: 2})
		if err != nil || len(eventRows) != 1 ||
			eventRows[0].String(colEventCommandID) != receipt.CommandID.String() ||
			eventRows[0].Int(colEventAuditSeq) != receipt.AuditSeq ||
			!bytes.Equal(eventRows[0].Bytes(colEventAuditHash), receipt.AuditHash) {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"DecisionResponse receipt event anchor is unavailable",
			)
		}
		outboxes, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		outboxRows, _, err := outboxes.List(ctx, model.Query{Filters: []model.Filter{{
			Column: colOutboxEventID, Op: model.OpEq, Value: receipt.EventID.String(),
		}}, Limit: 2})
		if err != nil || len(outboxRows) != 1 {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"DecisionResponse receipt Outbox anchor is unavailable",
			)
		}
		return nil
	})
	return result, err
}

func normalizeDecisionResponseError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrConflict) || errors.Is(err, errDecisionResponseVersionMismatch) ||
		errors.Is(err, errDecisionResponseIdempotencyReused) {
		return err
	}
	return err
}
