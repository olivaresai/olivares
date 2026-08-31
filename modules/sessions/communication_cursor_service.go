// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	directNoticeCursorAdvanceOperation   = "inbox.cursor.advance"
	directNoticeCursorAdvanceAuditAction = "sessions.communication.inbox_cursor.advance"
	directNoticeCursorPathPrefix         = "/v1/m/sessions/inbox/cursors/personal/"
	directNoticeCursorScanBatch          = 128
	directNoticeCursorScanBound          = 4096
	directNoticeCursorBarrierBound       = 4096
	directNoticeCursorApplyCommitmentV1  = 1
)

var (
	errDirectNoticeCursorVersionRequired = errors.New(
		"sessions: direct notice cursor version_required",
	)
	errDirectNoticeCursorVersionMismatch = errors.New(
		"sessions: direct notice cursor version_mismatch",
	)
	errDirectNoticeCursorIdempotencyReused = fmt.Errorf(
		"%w: idempotency_key_reused", store.ErrConflict,
	)
	errDirectNoticeCursorReplayNeedsFreshAudit = errors.New(
		"sessions: direct notice cursor replay requires a fresh audit view",
	)
)

// DirectNoticeCursorAdvanceCommand is a private service envelope. A future
// module-owned handler derives Method and Path from the admitted route; neither
// tenant, workspace, actor, mailbox nor filter is accepted from this value.
type DirectNoticeCursorAdvanceCommand struct {
	CursorToken    string `json:"cursor"`
	IfMatch        string `json:"-"`
	IdempotencyKey string `json:"-"`
	Method         string `json:"-"`
	Path           string `json:"-"`
}

// DirectNoticeCursorAdvanceResult is reconstructible from the closed receipt.
// It deliberately contains neither a navigation token nor authority evidence.
type DirectNoticeCursorAdvanceResult struct {
	CommandID  model.ID                     `json:"command_id"`
	CursorID   model.ID                     `json:"cursor_id"`
	Version    int64                        `json:"version"`
	ETag       string                       `json:"etag"`
	Projection InboxCursorReceiptProjection `json:"projection"`
	AuditSeq   int64                        `json:"audit_seq"`
	Replayed   bool                         `json:"replayed"`
}

// directNoticeCursorService remains private until the route and readiness
// owners wire a durable key source. Keeping the keyring here lets the complete
// SQLite service/apply cut be exercised without making a public endpoint live.
type directNoticeCursorService struct {
	module         *Module
	keyring        *communicationCursorTokenKeyring
	candidateBound int
	newID          func() model.ID
}

func newDirectNoticeCursorService(
	module *Module,
	keyring *communicationCursorTokenKeyring,
) *directNoticeCursorService {
	return &directNoticeCursorService{
		module: module, keyring: keyring,
		candidateBound: directNoticeCursorScanBound,
		newID:          model.NewID,
	}
}

type directNoticeCursorNormalizedCommand struct {
	command            DirectNoticeCursorAdvanceCommand
	scope              DirectoryScopeRef
	principal          CommunicationPrincipal
	reader             RecipientRef
	expectedVersion    int64
	filter             CursorFilter
	filterHash         []byte
	commandScope       string
	actorFingerprint   []byte
	idempotencyKeyHash []byte
	requestDigest      []byte
}

type directNoticeCursorCandidate struct {
	deliveryID model.ID
	sequence   int64
	core       ReadWitness
	coreDenied bool
	ids        directNoticeCarrierIDs
}

type directNoticeCursorPreflight struct {
	normalized directNoticeCursorNormalizedCommand
	claims     communicationCursorTokenClaims
	resolution PrincipalResolution
	closure    ChannelGrantSubjectClosure
	candidates []directNoticeCursorCandidate
	facts      []store.AuthorizationFactRef
	cursorID   model.ID
	commandID  model.ID
	receiptID  model.ID
}

// Advance applies one explicit personal DirectNotice cursor command. Exact
// receipt replay precedes key availability and token freshness; every fresh
// mutation is completed by exactly one mutateCommunication callback.
func (s *directNoticeCursorService) Advance(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	cmd DirectNoticeCursorAdvanceCommand,
) (DirectNoticeCursorAdvanceResult, error) {
	normalized, err := normalizeDirectNoticeCursorAdvanceCommand(scope, principal, cmd)
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	if replay, found, replayErr := s.lookupReplay(ctx, normalized); replayErr != nil {
		return DirectNoticeCursorAdvanceResult{}, replayErr
	} else if found {
		replay.Replayed = true
		return replay, nil
	}

	observedAt, err := s.observeDBNow(ctx, scope)
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	claims, err := s.keyring.verify(cmd.CursorToken, observedAt)
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	if err := validateDirectNoticeCursorTokenBinding(normalized, claims); err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	preflight, err := s.preflight(ctx, normalized, claims)
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}

	var result DirectNoticeCursorAdvanceResult
	err = s.module.mutateCommunication(ctx, scope, func(tx *communicationTx) error {
		result, err = applyDirectNoticeCursorAdvance(ctx, tx, preflight)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) ||
			errors.Is(err, errDirectNoticeCursorReplayNeedsFreshAudit) {
			if replay, found, replayErr := s.lookupReplay(ctx, normalized); replayErr != nil {
				return DirectNoticeCursorAdvanceResult{}, replayErr
			} else if found {
				replay.Replayed = true
				return replay, nil
			}
		}
		return DirectNoticeCursorAdvanceResult{}, normalizeDirectNoticeCursorMutationError(err)
	}
	return result, nil
}

func normalizeDirectNoticeCursorAdvanceCommand(
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	cmd DirectNoticeCursorAdvanceCommand,
) (directNoticeCursorNormalizedCommand, error) {
	if err := scope.Validate(); err != nil {
		return directNoticeCursorNormalizedCommand{}, err
	}
	if err := ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return directNoticeCursorNormalizedCommand{}, err
	}
	if principal.UserID == "" || principal.SessionID != "" {
		return directNoticeCursorNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel,
			"DirectNotice cursor requires a claim-free authenticated User",
		)
	}
	if !validCommunicationCursorTokenCompactBound(cmd.CursorToken) {
		return directNoticeCursorNormalizedCommand{}, communicationCursorTokenInvalid(
			"token length is invalid",
		)
	}
	if cmd.Method != http.MethodPut || cmd.Path != directNoticeCursorPathPrefix+principal.UserID.String() {
		return directNoticeCursorNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel, "DirectNotice cursor method or path is not server-derived",
		)
	}
	expectedVersion, err := parseDirectNoticeCursorETag(cmd.IfMatch)
	if err != nil {
		return directNoticeCursorNormalizedCommand{}, err
	}
	idempotencyID, err := model.ParseID(cmd.IdempotencyKey)
	if err != nil || !validCanonicalCommunicationID(idempotencyID) ||
		idempotencyID.String() != cmd.IdempotencyKey {
		return directNoticeCursorNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel, "DirectNotice cursor idempotency key is invalid",
		)
	}
	filter, filterHash, err := CanonicalCursorFilter(CursorFilter{
		CarrierClass: CursorCarrierDirectNoticeV1,
		MailboxKind:  MailboxPersonal,
	})
	if err != nil {
		return directNoticeCursorNormalizedCommand{}, err
	}
	commandScope := fmt.Sprintf(
		"%s %s;workspace=%s;filter=%s",
		cmd.Method, cmd.Path, scope.WorkspaceID,
		base64.RawURLEncoding.EncodeToString(filterHash),
	)
	if !validateOpaqueRef(commandScope) {
		return directNoticeCursorNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel, "DirectNotice cursor command scope is invalid",
		)
	}
	actorRaw, err := canonicalJSON(CommunicationActorRef{
		Kind: ActorUser, Ref: principal.UserID.String(),
	})
	if err != nil {
		return directNoticeCursorNormalizedCommand{}, err
	}
	actorFingerprint := sha256.Sum256(actorRaw)
	idempotencyHash := sha256.Sum256([]byte(cmd.IdempotencyKey))
	requestRaw, err := canonicalJSON(struct {
		Method  string `json:"method"`
		Path    string `json:"path"`
		Token   string `json:"token"`
		IfMatch string `json:"if_match"`
	}{Method: cmd.Method, Path: cmd.Path, Token: cmd.CursorToken, IfMatch: cmd.IfMatch})
	if err != nil {
		return directNoticeCursorNormalizedCommand{}, err
	}
	requestDigest := sha256.Sum256(requestRaw)
	return directNoticeCursorNormalizedCommand{
		command: cmd, scope: scope, principal: principal,
		reader:          RecipientRef{Kind: RecipientUser, Ref: principal.UserID.String()},
		expectedVersion: expectedVersion, filter: filter,
		filterHash: append([]byte(nil), filterHash...), commandScope: commandScope,
		actorFingerprint: actorFingerprint[:], idempotencyKeyHash: idempotencyHash[:],
		requestDigest: requestDigest[:],
	}, nil
}

func parseDirectNoticeCursorETag(value string) (int64, error) {
	if value == "" {
		return 0, errDirectNoticeCursorVersionRequired
	}
	if len(value) < 4 || value[0:2] != "\"v" || value[len(value)-1] != '"' {
		return 0, communicationError(ErrInvalidCommunicationModel,
			"DirectNotice cursor If-Match is not a strong version tag")
	}
	version, err := strconv.ParseInt(value[2:len(value)-1], 10, 64)
	if err != nil || version < 0 || value != fmt.Sprintf("\"v%d\"", version) {
		return 0, communicationError(ErrInvalidCommunicationModel,
			"DirectNotice cursor If-Match is not canonical")
	}
	return version, nil
}

func validateDirectNoticeCursorTokenBinding(
	normalized directNoticeCursorNormalizedCommand,
	claims communicationCursorTokenClaims,
) error {
	if claims.tenantID != normalized.scope.TenantID ||
		claims.workspaceID != normalized.scope.WorkspaceID ||
		claims.readerKind != RecipientUser || claims.readerRef != normalized.principal.UserID ||
		claims.mailboxKind != MailboxPersonal || claims.mailboxRef != normalized.principal.UserID ||
		claims.carrierClass != string(CursorCarrierDirectNoticeV1) ||
		!bytes.Equal(claims.filterHash, normalized.filterHash) {
		return communicationCursorTokenInvalid("claims do not match the admitted cursor resource")
	}
	if claims.cursorVersion != normalized.expectedVersion {
		return errDirectNoticeCursorVersionMismatch
	}
	return nil
}

func (s *directNoticeCursorService) observeDBNow(
	ctx context.Context,
	scope DirectoryScopeRef,
) (time.Time, error) {
	if s == nil || s.module == nil {
		return time.Time{}, communicationTransactionUnavailable("cursor service", nil)
	}
	var observed model.Timestamp
	err := s.module.viewCommunication(ctx, scope, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return communicationTransactionUnavailable("cursor verification clock", nil)
		}
		var err error
		observed, err = clock.TransactionNow(ctx)
		return err
	})
	if err != nil {
		return time.Time{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor verification DB time is unavailable: %v", err,
		)
	}
	return observed.Time(), nil
}

func (s *directNoticeCursorService) lookupReplay(
	ctx context.Context,
	normalized directNoticeCursorNormalizedCommand,
) (DirectNoticeCursorAdvanceResult, bool, error) {
	if s == nil || s.module == nil {
		return DirectNoticeCursorAdvanceResult{}, false,
			communicationTransactionUnavailable("cursor service", nil)
	}
	var result DirectNoticeCursorAdvanceResult
	var found bool
	err := s.module.viewCommunication(ctx, normalized.scope, func(sc store.Scope) error {
		repo, err := sc.Ext(communicationCommandKind)
		if err != nil {
			return err
		}
		result, found, err = lookupDirectNoticeCursorReceipt(
			ctx, repo, normalized,
		)
		if err != nil || !found {
			return err
		}
		audit := sc.Audit()
		reader, ok := audit.(store.VerifiedAuditAnchorReader)
		if !ok {
			return communicationError(ErrCommunicationEvidenceUnknown,
				"cursor receipt audit reader is unavailable")
		}
		receipt, receiptErr := readDirectNoticeCursorReceipt(
			ctx, repo, normalized,
		)
		if receiptErr != nil {
			return receiptErr
		}
		return verifyDirectNoticeCursorAuditAnchor(
			ctx, reader, normalized, receipt,
		)
	})
	return result, found, err
}

func lookupDirectNoticeCursorReceipt(
	ctx context.Context,
	repo interface {
		List(context.Context, model.Query) ([]model.Record, model.Page, error)
	},
	normalized directNoticeCursorNormalizedCommand,
) (DirectNoticeCursorAdvanceResult, bool, error) {
	receipt, found, err := findDirectNoticeCursorReceipt(ctx, repo, normalized)
	if err != nil || !found {
		return DirectNoticeCursorAdvanceResult{}, found, err
	}
	result, err := directNoticeCursorResultFromReceipt(receipt)
	return result, true, err
}

func readDirectNoticeCursorReceipt(
	ctx context.Context,
	repo interface {
		List(context.Context, model.Query) ([]model.Record, model.Page, error)
	},
	normalized directNoticeCursorNormalizedCommand,
) (CommunicationCommandReceipt, error) {
	receipt, found, err := findDirectNoticeCursorReceipt(ctx, repo, normalized)
	if err != nil {
		return CommunicationCommandReceipt{}, err
	}
	if !found {
		return CommunicationCommandReceipt{}, store.ErrNotFound
	}
	return receipt, nil
}

func findDirectNoticeCursorReceipt(
	ctx context.Context,
	repo interface {
		List(context.Context, model.Query) ([]model.Record, model.Page, error)
	},
	normalized directNoticeCursorNormalizedCommand,
) (CommunicationCommandReceipt, bool, error) {
	rows, page, err := repo.List(ctx, model.Query{Filters: []model.Filter{
		{Column: colCommActorFingerprint, Op: model.OpEq, Value: normalized.actorFingerprint},
		{Column: colCommCommandScope, Op: model.OpEq, Value: normalized.commandScope},
		{Column: colCommIdempotencyKeyHash, Op: model.OpEq, Value: normalized.idempotencyKeyHash},
	}, Limit: 2})
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	if len(rows) == 0 {
		if page.HasMore {
			return CommunicationCommandReceipt{}, false, communicationError(
				ErrCommunicationEvidenceUnknown, "cursor receipt lookup is incomplete")
		}
		return CommunicationCommandReceipt{}, false, nil
	}
	if len(rows) != 1 || page.HasMore {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor receipt uniqueness is unavailable")
	}
	receipt, err := communicationCommandReceiptFromRecord(rows[0])
	if err != nil {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor receipt cannot be decoded")
	}
	if !bytes.Equal(receipt.RequestDigest, normalized.requestDigest) {
		return CommunicationCommandReceipt{}, false, errDirectNoticeCursorIdempotencyReused
	}
	if receipt.TenantID != normalized.scope.TenantID ||
		receipt.WorkspaceID != normalized.scope.WorkspaceID ||
		receipt.CommandScope != normalized.commandScope ||
		!bytes.Equal(receipt.ActorFingerprint, normalized.actorFingerprint) ||
		!bytes.Equal(receipt.IdempotencyKeyHash, normalized.idempotencyKeyHash) {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor receipt crosses command scope")
	}
	return receipt, true, nil
}

func directNoticeCursorResultFromReceipt(
	receipt CommunicationCommandReceipt,
) (DirectNoticeCursorAdvanceResult, error) {
	if err := ValidateCommunicationCommandReceipt(receipt); err != nil ||
		receipt.ResultKind != string(inboxCursorKind) || receipt.ResultID == "" ||
		receipt.HTTPStatus != http.StatusOK || receipt.EventID != "" ||
		receipt.ResponseProjectionJSON.InboxCursor == nil ||
		receipt.ResponseProjectionJSON.Version < 1 {
		return DirectNoticeCursorAdvanceResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor receipt response is unavailable")
	}
	projection := *receipt.ResponseProjectionJSON.InboxCursor
	return DirectNoticeCursorAdvanceResult{
		CommandID: receipt.CommandID, CursorID: receipt.ResultID,
		Version:    receipt.ResponseProjectionJSON.Version,
		ETag:       fmt.Sprintf("\"v%d\"", receipt.ResponseProjectionJSON.Version),
		Projection: projection, AuditSeq: receipt.AuditSeq,
	}, nil
}

func verifyDirectNoticeCursorAuditAnchor(
	ctx context.Context,
	reader store.VerifiedAuditAnchorReader,
	normalized directNoticeCursorNormalizedCommand,
	receipt CommunicationCommandReceipt,
) error {
	event, metaCanonical, found, err := reader.ReadVerifiedAuditAnchor(ctx, receipt.AuditSeq)
	if err != nil || !found || event.Seq != receipt.AuditSeq ||
		event.TenantID != normalized.scope.TenantID ||
		event.Actor != directNoticeActor(normalized.principal) ||
		event.ActorKind != model.ActorUser ||
		event.Action != directNoticeCursorAdvanceAuditAction ||
		event.TargetKind != communicationCommandKind || event.TargetID != receipt.CommandID ||
		!bytes.Equal(event.PayloadHash, receipt.PlanHash) ||
		!bytes.Equal(event.Hash, receipt.AuditHash) ||
		!validateDirectNoticeCursorAuditMeta(metaCanonical, normalized.scope, receipt) {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"cursor receipt audit anchor does not match")
	}
	return nil
}

func validateDirectNoticeCursorAuditMeta(
	metaCanonical string,
	scope DirectoryScopeRef,
	receipt CommunicationCommandReceipt,
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
		"workspace_id": {}, "workspace_binding_version": {},
		"command_scope": {}, "cursor_id": {}, "cursor_version": {}, "last_seen_seq": {},
		"apply_commitment_version": {}, "apply_commitment": {},
		"trace_id": {}, "span_id": {},
	}
	for key := range meta {
		if _, present := allowed[key]; !present {
			return false
		}
	}
	if receipt.ResponseProjectionJSON.InboxCursor == nil {
		return false
	}
	workspaceBinding, workspaceBindingOK := auditMetaInt64(meta["workspace_binding_version"])
	cursorVersion, cursorVersionOK := auditMetaInt64(meta["cursor_version"])
	lastSeenSeq, lastSeenSeqOK := auditMetaInt64(meta["last_seen_seq"])
	commitmentVersion, commitmentVersionOK := auditMetaInt64(meta["apply_commitment_version"])
	commitment, commitmentErr := directNoticeCursorApplyCommitmentFromReceipt(receipt)
	commitmentText, commitmentOK := meta["apply_commitment"].(string)
	if meta["workspace_id"] != scope.WorkspaceID.String() ||
		meta["command_scope"] != receipt.CommandScope ||
		meta["cursor_id"] != receipt.ResultID.String() ||
		!workspaceBindingOK || workspaceBinding != 1 ||
		!cursorVersionOK || cursorVersion != receipt.ResponseProjectionJSON.Version ||
		!lastSeenSeqOK || lastSeenSeq != receipt.ResponseProjectionJSON.InboxCursor.LastSeenSeq ||
		!commitmentVersionOK || commitmentVersion != directNoticeCursorApplyCommitmentV1 ||
		commitmentErr != nil || !commitmentOK || commitmentText != hex.EncodeToString(commitment) {
		return false
	}
	trace, hasTrace := meta["trace_id"]
	span, hasSpan := meta["span_id"]
	return hasTrace == hasSpan && (!hasTrace ||
		(validAuditCorrelationID(trace, 32) && validAuditCorrelationID(span, 16)))
}

func (s *directNoticeCursorService) preflight(
	ctx context.Context,
	normalized directNoticeCursorNormalizedCommand,
	claims communicationCursorTokenClaims,
) (directNoticeCursorPreflight, error) {
	bound := s.candidateBound
	if bound < 1 || bound > directNoticeCursorScanBound {
		return directNoticeCursorPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor scan bound is unavailable",
		)
	}
	identities, err := s.scanCandidateIdentities(
		ctx, normalized.scope, normalized.reader,
		claims.baseDeliverySeq, claims.afterDeliverySeq, claims.deliveryID, bound,
	)
	if err != nil {
		return directNoticeCursorPreflight{}, err
	}
	candidates := make([]directNoticeCursorCandidate, len(identities))
	for index, identity := range identities {
		entity := EntityRef{
			TenantID: normalized.scope.TenantID, WorkspaceID: normalized.scope.WorkspaceID,
			Kind: messageDeliveryKind, ID: identity.deliveryID,
		}
		core, denied, authErr := s.module.authorizeDirectNoticeReadCore(
			ctx, normalized.principal, entity,
		)
		if authErr != nil {
			return directNoticeCursorPreflight{}, authErr
		}
		candidates[index] = directNoticeCursorCandidate{
			deliveryID: identity.deliveryID, sequence: identity.sequence,
			core: core, coreDenied: denied,
		}
	}
	needCarrierAuthority := false
	for _, candidate := range candidates {
		needCarrierAuthority = needCarrierAuthority || !candidate.coreDenied
	}
	resolution, closure, err := s.resolveReaderAuthority(
		ctx, normalized, needCarrierAuthority,
	)
	if err != nil {
		return directNoticeCursorPreflight{}, err
	}
	if needCarrierAuthority {
		if err := s.resolveAllowedCarrierIDs(ctx, normalized, candidates); err != nil {
			return directNoticeCursorPreflight{}, err
		}
	}
	factsInput := []store.AuthorizationFactRef{{
		Kind: model.DirectoryEpochKind, ID: model.ID(normalized.scope.TenantID),
		Version: resolution.Recipient.DirectoryEpoch,
	}}
	for _, candidate := range candidates {
		candidateFacts, factErr := directNoticeReadAuthorityFacts(
			candidate.core, normalized.scope.TenantID, resolution.Recipient.DirectoryEpoch,
		)
		if factErr != nil {
			return directNoticeCursorPreflight{}, factErr
		}
		factsInput = append(factsInput, candidateFacts...)
	}
	facts, err := canonicalAuthorizationFactUnion(factsInput)
	if err != nil {
		return directNoticeCursorPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor authority fact union is unavailable",
		)
	}
	cursorID := claims.cursorID
	if claims.cursorVersion == 0 {
		cursorID = s.newID()
	}
	return directNoticeCursorPreflight{
		normalized: normalized, claims: claims, resolution: resolution, closure: closure,
		candidates: candidates, facts: facts, cursorID: cursorID,
		commandID: s.newID(), receiptID: s.newID(),
	}, nil
}

type directNoticeCursorIdentity struct {
	deliveryID model.ID
	sequence   int64
}

func (s *directNoticeCursorService) scanCandidateIdentities(
	ctx context.Context,
	scope DirectoryScopeRef,
	reader RecipientRef,
	fromExclusive int64,
	toInclusive int64,
	targetID model.ID,
	bound int,
) ([]directNoticeCursorIdentity, error) {
	var result []directNoticeCursorIdentity
	err := s.module.viewCommunication(ctx, scope, func(sc store.Scope) error {
		repo, err := sc.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		var scanErr error
		result, scanErr = scanDirectNoticeCursorIdentityRange(
			ctx, repo, reader, fromExclusive, toInclusive, targetID, bound,
		)
		return scanErr
	})
	return result, err
}

func scanDirectNoticeCursorIdentityRange(
	ctx context.Context,
	repo interface {
		List(context.Context, model.Query) ([]model.Record, model.Page, error)
	},
	reader RecipientRef,
	fromExclusive int64,
	toInclusive int64,
	targetID model.ID,
	bound int,
) ([]directNoticeCursorIdentity, error) {
	if reader.Validate() != nil || reader.Kind != RecipientUser || fromExclusive < 0 ||
		toInclusive < fromExclusive || bound < 1 || bound > directNoticeCursorScanBound {
		return nil, communicationError(ErrCommunicationEvidenceUnknown,
			"cursor identity scan input is malformed")
	}
	if toInclusive == fromExclusive && targetID != "" {
		return nil, errDirectNoticeCursorVersionMismatch
	}
	identities := make([]directNoticeCursorIdentity, 0)
	seenIDs := make(map[model.ID]struct{})
	after := fromExclusive
	for {
		rows, page, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{
				{Column: colCommRecipientKind, Op: model.OpEq, Value: string(reader.Kind)},
				{Column: colCommRecipientRef, Op: model.OpEq, Value: reader.Ref},
				{Column: colCommDeliverySeq, Op: model.OpGt, Value: after},
				{Column: colCommDeliverySeq, Op: model.OpLte, Value: toInclusive},
			},
			Sort: []model.Sort{{Column: colCommDeliverySeq}}, Limit: directNoticeCursorScanBatch,
		})
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			if page.HasMore {
				return nil, communicationError(ErrCommunicationEvidenceUnknown,
					"cursor identity scan is truncated without progress")
			}
			break
		}
		for _, row := range rows {
			id, parseErr := directNoticeRecordID(row, model.ColID)
			sequence := row.Int(colCommDeliverySeq)
			if parseErr != nil || sequence <= after || sequence > toInclusive {
				return nil, communicationError(ErrCommunicationEvidenceUnknown,
					"cursor identity scan is unordered")
			}
			if _, duplicate := seenIDs[id]; duplicate {
				return nil, communicationError(ErrCommunicationEvidenceUnknown,
					"cursor identity scan repeats a Delivery")
			}
			seenIDs[id] = struct{}{}
			identities = append(identities, directNoticeCursorIdentity{
				deliveryID: id, sequence: sequence,
			})
			if len(identities) > bound {
				return nil, communicationError(ErrCommunicationEvidenceUnknown,
					"cursor identity scan exceeds its bound")
			}
			after = sequence
		}
		if !page.HasMore {
			// A final sentinel read makes a false HasMore=false shape fail closed.
			probe, probePage, probeErr := repo.List(ctx, model.Query{
				Filters: []model.Filter{
					{Column: colCommRecipientKind, Op: model.OpEq, Value: string(reader.Kind)},
					{Column: colCommRecipientRef, Op: model.OpEq, Value: reader.Ref},
					{Column: colCommDeliverySeq, Op: model.OpGt, Value: after},
					{Column: colCommDeliverySeq, Op: model.OpLte, Value: toInclusive},
				},
				Sort: []model.Sort{{Column: colCommDeliverySeq}}, Limit: 1,
			})
			if probeErr != nil || len(probe) != 0 || probePage.HasMore {
				return nil, communicationError(ErrCommunicationEvidenceUnknown,
					"cursor identity scan final page is incomplete")
			}
			break
		}
		if len(identities) >= bound {
			return nil, communicationError(ErrCommunicationEvidenceUnknown,
				"cursor identity scan exceeds its bound")
		}
	}
	if toInclusive == fromExclusive {
		if len(identities) != 0 {
			return nil, errDirectNoticeCursorVersionMismatch
		}
		return identities, nil
	}
	if len(identities) == 0 || identities[len(identities)-1].sequence != toInclusive ||
		identities[len(identities)-1].deliveryID != targetID {
		return nil, errDirectNoticeCursorVersionMismatch
	}
	return identities, nil
}

func (s *directNoticeCursorService) resolveReaderAuthority(
	ctx context.Context,
	normalized directNoticeCursorNormalizedCommand,
	needCarrierAuthority bool,
) (PrincipalResolution, ChannelGrantSubjectClosure, error) {
	m := s.module
	if !communicationPortBound(m.communicationDirectoryResolver) ||
		(needCarrierAuthority && !communicationPortBound(m.communicationGrantClosure)) {
		return PrincipalResolution{}, ChannelGrantSubjectClosure{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor reader authority ports are unavailable",
		)
	}
	resolution, err := m.communicationDirectoryResolver.ResolvePrincipal(
		ctx, normalized.scope, normalized.principal,
	)
	if err != nil || ValidatePrincipalResolution(resolution) != nil ||
		resolution.Scope != normalized.scope || resolution.Principal != normalized.principal {
		return PrincipalResolution{}, ChannelGrantSubjectClosure{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor principal resolution is unavailable",
		)
	}
	switch resolution.Outcome {
	case PrincipalUnknown:
		return PrincipalResolution{}, ChannelGrantSubjectClosure{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor principal resolution is unavailable",
		)
	case PrincipalNotFound:
		return PrincipalResolution{}, ChannelGrantSubjectClosure{}, errDirectNoticePrincipalNotFound
	}
	if resolution.Recipient == nil || resolution.Recipient.Recipient != normalized.reader ||
		resolution.Recipient.DirectoryEpoch < 1 {
		return PrincipalResolution{}, ChannelGrantSubjectClosure{}, errDirectNoticePrincipalNotFound
	}
	if !needCarrierAuthority {
		return resolution, ChannelGrantSubjectClosure{}, nil
	}
	closure, err := m.communicationGrantClosure.ResolveChannelGrantSubjects(
		ctx, normalized.scope, normalized.principal,
	)
	if err != nil || closure.Scope != normalized.scope || closure.Principal != normalized.principal ||
		closure.DirectoryEpoch != resolution.Recipient.DirectoryEpoch || !closure.Outcome.Valid() ||
		!boundedToken(closure.Code, 128) || !validateOpaqueRef(closure.EvidenceRef) ||
		closure.ObservedAt.IsZero() || !closure.FreshUntil.After(closure.ObservedAt) {
		return PrincipalResolution{}, ChannelGrantSubjectClosure{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor grant closure is unavailable",
		)
	}
	if closure.Outcome == ReadUnknown {
		return PrincipalResolution{}, ChannelGrantSubjectClosure{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor grant closure is unavailable",
		)
	}
	return resolution, closure, nil
}

func (s *directNoticeCursorService) resolveAllowedCarrierIDs(
	ctx context.Context,
	normalized directNoticeCursorNormalizedCommand,
	candidates []directNoticeCursorCandidate,
) error {
	return s.module.viewCommunication(ctx, normalized.scope, func(sc store.Scope) error {
		deliveryRepo, err := sc.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		messageRepo, err := sc.Ext(messageKind)
		if err != nil {
			return err
		}
		for index := range candidates {
			candidate := &candidates[index]
			if candidate.coreDenied {
				continue
			}
			deliveryRecord, getErr := deliveryRepo.Get(ctx, candidate.deliveryID)
			if getErr != nil {
				return directNoticeReadUnknown("cursor Delivery identity is unavailable", getErr)
			}
			if deliveryRecord.String(colCommRecipientKind) != string(normalized.reader.Kind) ||
				deliveryRecord.String(colCommRecipientRef) != normalized.reader.Ref ||
				deliveryRecord.Int(colCommDeliverySeq) != candidate.sequence {
				return communicationError(ErrCommunicationEvidenceUnknown,
					"cursor Delivery changed mailbox identity")
			}
			messageID, parseErr := directNoticeRecordID(deliveryRecord, colCommMessageID)
			if parseErr != nil {
				return parseErr
			}
			messageRecord, getErr := messageRepo.Get(ctx, messageID)
			if getErr != nil {
				return directNoticeReadUnknown("cursor Message identity is unavailable", getErr)
			}
			channelID, parseErr := directNoticeRecordID(messageRecord, colCommChannelID)
			if parseErr != nil {
				return parseErr
			}
			candidate.ids = directNoticeCarrierIDs{
				MessageID: messageID, DeliveryID: candidate.deliveryID,
				ChannelID: channelID, DeliverySeq: candidate.sequence,
			}
		}
		return nil
	})
}

type directNoticeCursorLockedState struct {
	cursor         *InboxCursor
	activeBarriers []InboxCursorBarrier
	channels       map[model.ID]directNoticeReadLockedChannel
	carriers       map[model.ID]directNoticeReadLockedCarrier
	identities     []directNoticeCursorIdentity
	epoch          model.DirectoryEpoch
	tombstones     map[model.ID]*store.DirectoryTombstoneWitness
}

func applyDirectNoticeCursorAdvance(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticeCursorPreflight,
) (DirectNoticeCursorAdvanceResult, error) {
	if tx == nil {
		return DirectNoticeCursorAdvanceResult{},
			communicationTransactionUnavailable("cursor transaction", nil)
	}
	if err := tx.lockTransaction(ctx, directNoticeCursorIdempotencyLockKey(preflight.normalized)); err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	receiptRepo, err := tx.repo(communicationCommandKind)
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	if _, found, replayErr := findDirectNoticeCursorReceipt(
		ctx, receiptRepo, preflight.normalized,
	); replayErr != nil {
		return DirectNoticeCursorAdvanceResult{}, replayErr
	} else if found {
		// The transaction lookup only closes the idempotency race. Never accept
		// its receipt without reconstructing it and verifying its audit anchor
		// through one outer, consistent View.
		return DirectNoticeCursorAdvanceResult{}, errDirectNoticeCursorReplayNeedsFreshAudit
	}
	if err := tx.lockAuthoritySnapshot(ctx, preflight.facts); err != nil {
		return DirectNoticeCursorAdvanceResult{},
			normalizeDirectNoticeAuthorityLockError(err)
	}
	if preflight.normalized.principal.SessionID != "" {
		return DirectNoticeCursorAdvanceResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice cursor Claim authority is unsupported",
		)
	}
	locked, err := lockDirectNoticeCursorState(ctx, tx, preflight)
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	if err := tx.lockAuditAppends(ctx); err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	if err := tx.refreshNow(ctx); err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	plan, err := materializeDirectNoticeCursorPlan(ctx, tx, preflight, locked)
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	if plan.Verdict != VerdictClean || len(plan.RequiredClaims) != 0 ||
		!equalDirectNoticeAuthorityFacts(preflight.facts, plan.Facts) {
		return DirectNoticeCursorAdvanceResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"cursor planner did not preserve the locked authority snapshot",
		)
	}
	planHash, err := canonicalDirectNoticeCursorPlanHash(preflight, plan)
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	projection, err := directNoticeCursorReceiptProjection(plan, locked.activeBarriers)
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	result, err := persistDirectNoticeCursorAdvance(
		ctx, tx, preflight, locked, plan, planHash, projection,
	)
	return result, err
}

func directNoticeCursorIdempotencyLockKey(
	normalized directNoticeCursorNormalizedCommand,
) string {
	digest := sha256.Sum256(bytes.Join([][]byte{
		[]byte(normalized.scope.TenantID), normalized.actorFingerprint,
		[]byte(normalized.commandScope), normalized.idempotencyKeyHash,
	}, []byte{0}))
	return "sessions:communication:cursor:idem:" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func directNoticeCursorIdentityLockKey(
	normalized directNoticeCursorNormalizedCommand,
) string {
	digest := sha256.Sum256(bytes.Join([][]byte{
		[]byte(normalized.scope.TenantID), []byte(normalized.scope.WorkspaceID),
		[]byte(normalized.reader.Kind), []byte(normalized.reader.Ref),
		[]byte(MailboxPersonal), []byte(normalized.reader.Ref), normalized.filterHash,
	}, []byte{0}))
	return "sessions:communication:cursor:identity:" +
		base64.RawURLEncoding.EncodeToString(digest[:])
}

func lockDirectNoticeCursorState(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticeCursorPreflight,
) (directNoticeCursorLockedState, error) {
	normalized := preflight.normalized
	channelSet := make(map[model.ID]struct{})
	for _, candidate := range preflight.candidates {
		if !candidate.coreDenied {
			if !validCanonicalCommunicationID(candidate.ids.ChannelID) {
				return directNoticeCursorLockedState{}, communicationError(
					ErrCommunicationEvidenceUnknown, "cursor carrier Channel is unavailable")
			}
			channelSet[candidate.ids.ChannelID] = struct{}{}
		}
	}
	channelIDs := directNoticeSortedIDSet(channelSet)
	channels := make(map[model.ID]directNoticeReadLockedChannel, len(channelIDs))
	for _, channelID := range channelIDs {
		record, err := tx.lockRecord(ctx, channelKind, channelID)
		if err != nil {
			return directNoticeCursorLockedState{}, normalizeDirectNoticeLockedNotFound(err)
		}
		channel, err := channelFromRecord(record)
		if err != nil || channel.ID != channelID || channel.TenantID != normalized.scope.TenantID ||
			channel.WorkspaceID != normalized.scope.WorkspaceID {
			return directNoticeCursorLockedState{}, directNoticeReadUnknown(
				"cursor locked Channel is malformed", err,
			)
		}
		channels[channelID] = directNoticeReadLockedChannel{Channel: channel}
	}
	for _, channelID := range channelIDs {
		grants, err := lockCurrentChannelGrants(ctx, tx, channelID)
		if err != nil {
			return directNoticeCursorLockedState{}, err
		}
		channel := channels[channelID]
		channel.Grants = grants
		channels[channelID] = channel
	}

	if err := tx.lockTransaction(ctx, directNoticeCursorIdentityLockKey(normalized)); err != nil {
		return directNoticeCursorLockedState{}, err
	}
	cursor, activeBarriers, err := lockDirectNoticeCursorIdentity(ctx, tx, preflight)
	if err != nil {
		return directNoticeCursorLockedState{}, err
	}
	deliveryRepo, err := tx.repo(messageDeliveryKind)
	if err != nil {
		return directNoticeCursorLockedState{}, err
	}
	identities, err := scanDirectNoticeCursorIdentityRange(
		ctx, deliveryRepo, normalized.reader,
		preflight.claims.baseDeliverySeq, preflight.claims.afterDeliverySeq,
		preflight.claims.deliveryID, directNoticeCursorScanBound,
	)
	if err != nil {
		return directNoticeCursorLockedState{}, err
	}
	if !equalDirectNoticeCursorCandidateSets(preflight.candidates, identities) {
		return directNoticeCursorLockedState{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor mailbox changed between observation and lock",
		)
	}
	carriers, err := lockDirectNoticeCursorCarriers(ctx, tx, preflight)
	if err != nil {
		return directNoticeCursorLockedState{}, err
	}
	epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
	if err != nil || epoch.Validate() != nil || epoch.TenantID != normalized.scope.TenantID ||
		preflight.resolution.Recipient == nil ||
		epoch.Version != preflight.resolution.Recipient.DirectoryEpoch {
		return directNoticeCursorLockedState{}, directNoticeReadUnknown(
			"cursor directory epoch is unavailable", err,
		)
	}
	tombstones := make(map[model.ID]*store.DirectoryTombstoneWitness)
	for _, candidate := range preflight.candidates {
		if candidate.coreDenied {
			continue
		}
		carrier, present := carriers[candidate.ids.MessageID]
		if !present {
			return directNoticeCursorLockedState{}, communicationError(
				ErrCommunicationEvidenceUnknown, "cursor locked carrier is unavailable")
		}
		delivery, present := directNoticeCursorDeliveryByID(
			carrier.Deliveries, candidate.deliveryID,
		)
		if !present {
			return directNoticeCursorLockedState{}, communicationError(
				ErrCommunicationEvidenceUnknown, "cursor target Delivery left its locked carrier")
		}
		if delivery.State != DeliveryUndeliverable {
			continue
		}
		principalID, parseErr := model.ParseID(delivery.Recipient.Ref)
		if parseErr != nil || !validCanonicalCommunicationID(principalID) {
			return directNoticeCursorLockedState{}, communicationError(
				ErrCommunicationEvidenceUnknown, "cursor tombstone principal is invalid")
		}
		principalRef := store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalUser, PrincipalRef: principalID,
		}
		witness, found, readErr := tx.directorySnapshotReader().ReadDirectoryTombstone(
			ctx, principalRef,
		)
		if readErr != nil || !found {
			return directNoticeCursorLockedState{}, communicationError(
				ErrCommunicationEvidenceUnknown, "cursor tombstone evidence is unavailable")
		}
		witnessCopy := witness
		tombstones[delivery.ID] = &witnessCopy
	}
	return directNoticeCursorLockedState{
		cursor: cursor, activeBarriers: activeBarriers, channels: channels,
		carriers: carriers, identities: identities, epoch: epoch, tombstones: tombstones,
	}, nil
}

func lockDirectNoticeCursorIdentity(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticeCursorPreflight,
) (*InboxCursor, []InboxCursorBarrier, error) {
	normalized := preflight.normalized
	repo, err := tx.repo(inboxCursorKind)
	if err != nil {
		return nil, nil, err
	}
	filters := directNoticeCursorIdentityFilters(normalized)
	rows, page, err := repo.List(ctx, model.Query{Filters: filters, Limit: 2})
	if err != nil {
		return nil, nil, err
	}
	if len(rows) > 1 || page.HasMore {
		return nil, nil, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor durable identity is ambiguous")
	}
	if len(rows) == 0 {
		if preflight.claims.cursorVersion != 0 {
			return nil, nil, errDirectNoticeCursorVersionMismatch
		}
		barrierRepo, barrierErr := tx.repo(inboxCursorBarrierKind)
		if barrierErr != nil {
			return nil, nil, barrierErr
		}
		barrierRows, barrierPage, barrierErr := barrierRepo.List(ctx, model.Query{
			Filters: append(filters, model.Filter{
				Column: colCommState, Op: model.OpEq, Value: string(CursorBarrierActive),
			}),
			Limit: 1,
		})
		if barrierErr != nil || len(barrierRows) != 0 || barrierPage.HasMore {
			return nil, nil, communicationError(
				ErrCommunicationEvidenceUnknown, "virtual cursor has orphan active barriers")
		}
		return nil, []InboxCursorBarrier{}, nil
	}
	id, err := directNoticeRecordID(rows[0], model.ColID)
	if err != nil {
		return nil, nil, err
	}
	lockedRecord, err := tx.lockRecord(ctx, inboxCursorKind, id)
	if err != nil {
		return nil, nil, normalizeDirectNoticeLockedNotFound(err)
	}
	cursor, err := inboxCursorFromRecord(lockedRecord)
	if err != nil {
		return nil, nil, communicationError(
			ErrCommunicationEvidenceUnknown, "locked cursor cannot be decoded")
	}
	if preflight.claims.cursorVersion == 0 || cursor.ID != preflight.claims.cursorID ||
		cursor.Version != preflight.claims.cursorVersion ||
		cursor.LastSeenSeq != preflight.claims.baseDeliverySeq || cursor.Reader != normalized.reader ||
		cursor.MailboxKind != MailboxPersonal || cursor.MailboxRef != normalized.reader.Ref ||
		!bytes.Equal(cursor.FilterHash, normalized.filterHash) {
		return nil, nil, errDirectNoticeCursorVersionMismatch
	}
	barrierRecords, err := lockDirectNoticeRecordSet(
		ctx, tx, inboxCursorBarrierKind,
		append(filters, model.Filter{
			Column: colCommState, Op: model.OpEq, Value: string(CursorBarrierActive),
		}),
		directNoticeCursorBarrierBound,
	)
	if err != nil {
		return nil, nil, err
	}
	barriers := make([]InboxCursorBarrier, 0, len(barrierRecords))
	for _, record := range barrierRecords {
		barrier, decodeErr := inboxCursorBarrierFromRecord(record, cursor)
		if decodeErr != nil {
			return nil, nil, communicationError(
				ErrCommunicationEvidenceUnknown, "active cursor barrier cannot be decoded")
		}
		barriers = append(barriers, barrier)
	}
	return &cursor, barriers, nil
}

func directNoticeCursorIdentityFilters(
	normalized directNoticeCursorNormalizedCommand,
) []model.Filter {
	return []model.Filter{
		{Column: colCommReaderKind, Op: model.OpEq, Value: string(normalized.reader.Kind)},
		{Column: colCommReaderRef, Op: model.OpEq, Value: normalized.reader.Ref},
		{Column: colCommMailboxKind, Op: model.OpEq, Value: string(MailboxPersonal)},
		{Column: colCommMailboxRef, Op: model.OpEq, Value: normalized.reader.Ref},
		{Column: colCommFilterHash, Op: model.OpEq, Value: normalized.filterHash},
	}
}

func equalDirectNoticeCursorCandidateSets(
	preflight []directNoticeCursorCandidate,
	locked []directNoticeCursorIdentity,
) bool {
	if len(preflight) != len(locked) {
		return false
	}
	for index := range preflight {
		if preflight[index].deliveryID != locked[index].deliveryID ||
			preflight[index].sequence != locked[index].sequence {
			return false
		}
	}
	return true
}

func lockDirectNoticeCursorCarriers(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticeCursorPreflight,
) (map[model.ID]directNoticeReadLockedCarrier, error) {
	messageSet := make(map[model.ID]struct{})
	for _, candidate := range preflight.candidates {
		if !candidate.coreDenied {
			messageSet[candidate.ids.MessageID] = struct{}{}
		}
	}
	messageIDs := directNoticeSortedIDSet(messageSet)
	messageRecords := make(map[model.ID]model.Record, len(messageIDs))
	for _, messageID := range messageIDs {
		record, err := tx.lockRecord(ctx, messageKind, messageID)
		if err != nil {
			return nil, normalizeDirectNoticeLockedNotFound(err)
		}
		messageRecords[messageID] = record
	}
	deliverySpecs := make([]directNoticeReadSetSpec, 0, len(messageIDs))
	audienceSpecs := make([]directNoticeReadSetSpec, 0, len(messageIDs))
	for _, messageID := range messageIDs {
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
	deliveryRecords, err := lockDirectNoticeBatchRecordSets(
		ctx, tx, messageDeliveryKind, deliverySpecs,
	)
	if err != nil {
		return nil, err
	}
	audienceRecords, err := lockDirectNoticeBatchRecordSets(
		ctx, tx, messageAudienceKind, audienceSpecs,
	)
	if err != nil {
		return nil, err
	}
	carriers := make(map[model.ID]directNoticeReadLockedCarrier, len(messageIDs))
	contributionSpecs := make([]directNoticeReadSetSpec, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		deliveries := make([]MessageDelivery, 0, len(deliveryRecords[messageID]))
		requiredCount := int64(0)
		for _, record := range deliveryRecords[messageID] {
			delivery, decodeErr := messageDeliveryFromRecord(record)
			if decodeErr != nil || delivery.MessageID != messageID {
				return nil, directNoticeReadUnknown("cursor locked Delivery set is malformed", decodeErr)
			}
			if delivery.Required {
				requiredCount++
			}
			deliveries = append(deliveries, delivery)
		}
		message, decodeErr := messageFromRecord(messageRecords[messageID], requiredCount)
		if decodeErr != nil {
			return nil, directNoticeReadUnknown("cursor locked Message is malformed", decodeErr)
		}
		audiences := make([]MessageAudience, 0, len(audienceRecords[messageID]))
		queries := make([][]model.Filter, 0, len(audienceRecords[messageID]))
		for _, record := range audienceRecords[messageID] {
			audience, audienceErr := messageAudienceFromRecord(record)
			if audienceErr != nil || audience.MessageID != messageID {
				return nil, directNoticeReadUnknown("cursor locked audience set is malformed", audienceErr)
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
	contributionRecords, err := lockDirectNoticeBatchRecordSets(
		ctx, tx, messageAudienceRecipientKind, contributionSpecs,
	)
	if err != nil {
		return nil, err
	}
	for _, messageID := range messageIDs {
		carrier := carriers[messageID]
		for _, record := range contributionRecords[messageID] {
			contribution, decodeErr := messageAudienceRecipientFromRecord(record)
			if decodeErr != nil {
				return nil, directNoticeReadUnknown(
					"cursor locked contribution set is malformed", decodeErr,
				)
			}
			carrier.Contributions = append(carrier.Contributions, contribution)
		}
		carriers[messageID] = carrier
	}
	return carriers, nil
}

func directNoticeCursorDeliveryByID(
	deliveries []MessageDelivery,
	id model.ID,
) (MessageDelivery, bool) {
	for _, delivery := range deliveries {
		if delivery.ID == id {
			return delivery, true
		}
	}
	return MessageDelivery{}, false
}

func materializeDirectNoticeCursorPlan(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticeCursorPreflight,
	locked directNoticeCursorLockedState,
) (CursorAdvancePlan, error) {
	dbNow := tx.now.Time()
	normalized := preflight.normalized
	needCarrierAuthority := len(locked.channels) != 0
	if preflight.resolution.Recipient == nil ||
		!communicationEvidenceCurrent(
			preflight.resolution.ObservedAt, preflight.resolution.FreshUntil, dbNow,
		) || (needCarrierAuthority && !communicationEvidenceCurrent(
		preflight.closure.ObservedAt, preflight.closure.FreshUntil, dbNow,
	)) || (needCarrierAuthority && preflight.closure.DirectoryEpoch != locked.epoch.Version) {
		return CursorAdvancePlan{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor authority expired while waiting for locks",
		)
	}
	entries := make([]CursorScanEntry, 0, len(preflight.candidates))
	for _, candidate := range preflight.candidates {
		entry, err := buildDirectNoticeCursorScanEntry(
			ctx, tx, preflight, locked, candidate, dbNow,
		)
		if err != nil {
			return CursorAdvancePlan{}, err
		}
		entries = append(entries, entry)
	}
	witness := CursorMailboxScanWitness{
		Scope: normalized.scope, Reader: normalized.reader,
		MailboxKind: MailboxPersonal, MailboxRef: normalized.reader.Ref,
		FilterHash:       append([]byte(nil), normalized.filterHash...),
		FromExclusive:    preflight.claims.baseDeliverySeq,
		ToInclusive:      preflight.claims.afterDeliverySeq,
		TargetDeliveryID: preflight.claims.deliveryID,
		EntryCount:       int64(len(entries)), HasMore: false, ObservedAt: dbNow,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "mailbox_scan_locked",
			EvidenceRef: "same_tx:direct_notice_cursor_scan",
		},
	}
	digest, err := CanonicalCursorMailboxScanDigest(witness, entries)
	if err != nil {
		return CursorAdvancePlan{}, err
	}
	witness.Digest = digest
	if locked.cursor == nil {
		plan, err := PlanInitialInboxCursorAdvance(InitialInboxCursorAdvanceInput{
			Scope: normalized.scope, Principal: preflight.resolution,
			CursorID: preflight.cursorID, Reader: normalized.reader,
			MailboxKind: MailboxPersonal, MailboxRef: normalized.reader.Ref,
			ExpectedVersion: 0, Filter: normalized.filter,
			RequestedSeq: preflight.claims.afterDeliverySeq, DBNow: dbNow,
			Absence: InboxCursorAbsenceWitness{
				Scope: normalized.scope, Reader: normalized.reader,
				MailboxKind: MailboxPersonal, MailboxRef: normalized.reader.Ref,
				FilterHash: append([]byte(nil), normalized.filterHash...),
				ObservedAt: dbNow,
				Evidence: AuthorityEvidence{
					Verdict: VerdictClean, Code: "cursor_identity_absent",
					EvidenceRef: "same_tx:direct_notice_cursor_absence",
				},
			},
			ScanWitness: witness, Scan: entries,
		})
		return requireCleanDirectNoticeCursorPlan(plan, err)
	}
	barrierDigest, err := CanonicalCursorBarrierSetDigest(
		*locked.cursor, locked.activeBarriers,
	)
	if err != nil {
		return CursorAdvancePlan{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor barrier set cannot be sealed",
		)
	}
	plan, err := PlanInboxCursorAdvance(CursorAdvanceInput{
		Scope: normalized.scope, Principal: preflight.resolution,
		Cursor: *locked.cursor, ExpectedVersion: normalized.expectedVersion,
		Filter: normalized.filter, RequestedSeq: preflight.claims.afterDeliverySeq,
		DBNow: dbNow, ActiveBarriers: locked.activeBarriers,
		BarrierSetWitness: CursorBarrierSetWitness{
			Scope: normalized.scope, CursorID: locked.cursor.ID,
			BarrierCount: int64(len(locked.activeBarriers)), Digest: barrierDigest,
			ObservedAt: dbNow,
			Evidence: AuthorityEvidence{
				Verdict: VerdictClean, Code: "cursor_barriers_locked",
				EvidenceRef: "same_tx:direct_notice_cursor_barriers",
			},
		},
		ScanWitness: witness, Scan: entries,
	})
	return requireCleanDirectNoticeCursorPlan(plan, err)
}

func requireCleanDirectNoticeCursorPlan(
	plan CursorAdvancePlan,
	err error,
) (CursorAdvancePlan, error) {
	if err != nil {
		if errors.Is(err, ErrInvalidCommunicationTransition) {
			return CursorAdvancePlan{}, errDirectNoticeCursorVersionMismatch
		}
		return CursorAdvancePlan{}, err
	}
	if plan.Verdict != VerdictClean {
		return CursorAdvancePlan{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor plan is incomplete: %s", plan.Code,
		)
	}
	return plan, nil
}

func buildDirectNoticeCursorScanEntry(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticeCursorPreflight,
	locked directNoticeCursorLockedState,
	candidate directNoticeCursorCandidate,
	dbNow time.Time,
) (CursorScanEntry, error) {
	entry := CursorScanEntry{
		TenantID:    preflight.normalized.scope.TenantID,
		WorkspaceID: preflight.normalized.scope.WorkspaceID,
		DeliveryID:  candidate.deliveryID, Sequence: candidate.sequence,
	}
	core := candidate.core
	entry.Core = &core
	if !communicationEvidenceCurrent(core.ObservedAt, core.FreshUntil, dbNow) {
		return CursorScanEntry{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor core evidence expired while locking",
		)
	}
	if candidate.coreDenied {
		return entry, nil
	}
	carrier, present := locked.carriers[candidate.ids.MessageID]
	if !present {
		return CursorScanEntry{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor locked carrier is unavailable",
		)
	}
	delivery, present := directNoticeCursorDeliveryByID(carrier.Deliveries, candidate.deliveryID)
	if !present || delivery.DeliverySeq != candidate.sequence ||
		delivery.Recipient != preflight.normalized.reader {
		return CursorScanEntry{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor Delivery changed its mailbox lineage",
		)
	}
	channel, present := locked.channels[candidate.ids.ChannelID]
	if !present || channel.Channel.ID != carrier.Message.ChannelID {
		return CursorScanEntry{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor carrier changed Channel",
		)
	}
	if directNoticeReadRowsCarryFutureDBTime(
		channel.Channel, channel.Grants, carrier.Message, carrier.Deliveries,
		carrier.Audiences, carrier.Contributions, dbNow,
	) {
		return CursorScanEntry{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor locked rows carry future DB time",
		)
	}
	if len(carrier.Audiences) == 0 || len(carrier.Contributions) == 0 {
		return CursorScanEntry{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor audience graph is unavailable",
		)
	}
	entryFacts, err := directNoticeReadAuthorityFacts(
		candidate.core, preflight.normalized.scope.TenantID, locked.epoch.Version,
	)
	if err != nil {
		return CursorScanEntry{}, err
	}
	readerPreflight := directNoticeReaderPreflight{
		Scope: preflight.normalized.scope, Principal: preflight.normalized.principal,
		Recipient: preflight.normalized.reader, Resolution: preflight.resolution,
		Closure: preflight.closure, Core: candidate.core, Facts: entryFacts,
	}
	currentAudience, err := buildDirectNoticeCurrentAudience(
		readerPreflight, carrier.Message, delivery,
		carrier.Audiences, carrier.Contributions, dbNow,
	)
	if err != nil {
		return CursorScanEntry{}, err
	}
	grantEvidence := EvaluateCurrentChannelGrant(
		ChannelGrantSnapshot{
			Verdict: VerdictClean, Code: "channel_grants_locked",
			ACLRevision: channel.Channel.ACLRevision,
			ObservedAt:  dbNow, Grants: channel.Grants,
		},
		preflight.normalized.scope.TenantID, preflight.normalized.scope.WorkspaceID,
		channel.Channel.ID, preflight.closure, ChannelGrantRead, dbNow,
	)
	requiredCount := int64(0)
	for _, item := range carrier.Deliveries {
		if item.Required {
			requiredCount++
		}
	}
	clean := func(code, ref string) AuthorityEvidence {
		return AuthorityEvidence{Verdict: VerdictClean, Code: code, EvidenceRef: ref}
	}
	carrierRef := ProtectedCarrierRef{
		Entity: candidate.core.Entity, ChannelID: channel.Channel.ID,
		MessageID: carrier.Message.ID, DeliveryID: delivery.ID,
	}
	readEvidence := ProtectedReadEvidence{
		Scope: preflight.normalized.scope, ChannelID: channel.Channel.ID,
		ChannelACLRevision: channel.Channel.ACLRevision, DBNow: dbNow,
		Operation: CommunicationRead, Carrier: carrierRef,
		CarrierState: ProtectedCarrierSnapshot{
			Message: carrier.Message, Delivery: delivery,
			RequiredDeliveryCount: requiredCount, ObservedAt: dbNow,
			Evidence: clean("carrier_rows_locked", "same_tx:direct_notice_cursor_carrier"),
		},
		Core: candidate.core, Principal: preflight.normalized.principal,
		PrincipalResolution: preflight.resolution, Recipient: preflight.normalized.reader,
		DirectoryEpoch: store.AuthorizationFactRef{
			Kind: model.DirectoryEpochKind, ID: model.ID(preflight.normalized.scope.TenantID),
			Version: locked.epoch.Version,
		},
		CurrentChannelGrant: grantEvidence,
		EntityRecipientGuard: BoundEntityRecipientEvidence{
			Scope: preflight.normalized.scope, Carrier: carrierRef,
			Principal: preflight.normalized.principal, Recipient: preflight.normalized.reader,
			DirectoryEpoch: locked.epoch.Version, EvaluatedAt: dbNow,
			Evidence: clean("entity_recipient_current", "same_tx:direct_notice_cursor_recipient"),
		},
		CurrentAudience: currentAudience,
	}
	entry.Delivery = &delivery
	message := carrier.Message
	entry.Message = &message
	entry.ReadEvidence = &readEvidence
	entry.CarrierSet = &CursorCarrierSetWitness{
		Scope: preflight.normalized.scope, MessageID: carrier.Message.ID,
		DeliveryID: delivery.ID, DeliveryCount: int64(len(carrier.Deliveries)),
		ObservedAt: dbNow,
		Evidence:   clean("carrier_set_locked", "same_tx:direct_notice_cursor_carrier_set"),
	}
	if witness := locked.tombstones[delivery.ID]; witness != nil {
		copyWitness := *witness
		entry.Tombstone = &copyWitness
	}
	_ = ctx
	_ = tx
	return entry, nil
}

type directNoticeCursorPlanHashInput struct {
	Operation     string            `json:"operation"`
	RequestDigest []byte            `json:"request_digest"`
	Scope         DirectoryScopeRef `json:"scope"`
	CursorID      model.ID          `json:"cursor_id"`
	Plan          CursorAdvancePlan `json:"plan"`
}

func canonicalDirectNoticeCursorPlanHash(
	preflight directNoticeCursorPreflight,
	plan CursorAdvancePlan,
) ([]byte, error) {
	raw, err := canonicalJSON(directNoticeCursorPlanHashInput{
		Operation:     directNoticeCursorAdvanceOperation,
		RequestDigest: append([]byte(nil), preflight.normalized.requestDigest...),
		Scope:         preflight.normalized.scope, CursorID: plan.After.ID, Plan: plan,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

// directNoticeCursorApplyCommitment seals every replay-relevant receipt field
// under the audit chain. AuditSeq, AuditHash and ResponseDigest are deliberately
// excluded: all three are derived after the append, while the closed projection
// itself is included directly.
type directNoticeCursorApplyCommitment struct {
	SchemaVersion      int64                                  `json:"schema_version"`
	TenantID           model.TenantID                         `json:"tenant_id"`
	WorkspaceID        model.ID                               `json:"workspace_id"`
	ActorFingerprint   []byte                                 `json:"actor_fingerprint"`
	CommandScope       string                                 `json:"command_scope"`
	IdempotencyKeyHash []byte                                 `json:"idempotency_key_hash"`
	RequestDigest      []byte                                 `json:"request_digest"`
	CommandID          model.ID                               `json:"command_id"`
	ReceiptID          model.ID                               `json:"receipt_id"`
	ResultKind         string                                 `json:"result_kind"`
	ResultID           model.ID                               `json:"result_id"`
	EventID            model.ID                               `json:"event_id,omitempty"`
	PlanHash           []byte                                 `json:"plan_hash"`
	Projection         CommunicationCommandResponseProjection `json:"projection"`
	HTTPStatus         int                                    `json:"http_status"`
	CompletedAt        string                                 `json:"completed_at"`
}

func canonicalDirectNoticeCursorApplyCommitment(
	value directNoticeCursorApplyCommitment,
) ([]byte, error) {
	raw, err := canonicalJSON(value)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func directNoticeCursorApplyCommitmentFromPlan(
	preflight directNoticeCursorPreflight,
	plan CursorAdvancePlan,
	planHash []byte,
	projection InboxCursorReceiptProjection,
	dbNow time.Time,
) ([]byte, error) {
	projectionCopy := projection
	return canonicalDirectNoticeCursorApplyCommitment(directNoticeCursorApplyCommitment{
		SchemaVersion:      directNoticeCursorApplyCommitmentV1,
		TenantID:           preflight.normalized.scope.TenantID,
		WorkspaceID:        preflight.normalized.scope.WorkspaceID,
		ActorFingerprint:   append([]byte(nil), preflight.normalized.actorFingerprint...),
		CommandScope:       preflight.normalized.commandScope,
		IdempotencyKeyHash: append([]byte(nil), preflight.normalized.idempotencyKeyHash...),
		RequestDigest:      append([]byte(nil), preflight.normalized.requestDigest...),
		CommandID:          preflight.commandID, ReceiptID: preflight.receiptID,
		ResultKind: string(inboxCursorKind), ResultID: plan.After.ID,
		PlanHash: append([]byte(nil), planHash...),
		Projection: CommunicationCommandResponseProjection{
			Version: plan.After.Version, InboxCursor: &projectionCopy,
		},
		HTTPStatus: http.StatusOK, CompletedAt: dbNow.UTC().Format(time.RFC3339Nano),
	})
}

func directNoticeCursorApplyCommitmentFromReceipt(
	receipt CommunicationCommandReceipt,
) ([]byte, error) {
	if err := ValidateCommunicationCommandReceipt(receipt); err != nil {
		return nil, err
	}
	return canonicalDirectNoticeCursorApplyCommitment(directNoticeCursorApplyCommitment{
		SchemaVersion: directNoticeCursorApplyCommitmentV1,
		TenantID:      receipt.TenantID, WorkspaceID: receipt.WorkspaceID,
		ActorFingerprint:   append([]byte(nil), receipt.ActorFingerprint...),
		CommandScope:       receipt.CommandScope,
		IdempotencyKeyHash: append([]byte(nil), receipt.IdempotencyKeyHash...),
		RequestDigest:      append([]byte(nil), receipt.RequestDigest...),
		CommandID:          receipt.CommandID, ReceiptID: receipt.ID,
		ResultKind: receipt.ResultKind, ResultID: receipt.ResultID, EventID: receipt.EventID,
		PlanHash:    append([]byte(nil), receipt.PlanHash...),
		Projection:  receipt.ResponseProjectionJSON,
		HTTPStatus:  receipt.HTTPStatus,
		CompletedAt: receipt.CompletedAt.UTC().Format(time.RFC3339Nano),
	})
}

func directNoticeCursorReceiptProjection(
	plan CursorAdvancePlan,
	prior []InboxCursorBarrier,
) (InboxCursorReceiptProjection, error) {
	type candidate struct {
		deliveryID model.ID
		sequence   int64
		cause      CursorBarrierCause
	}
	resolved := make(map[model.ID]struct{}, len(plan.Resolve))
	for _, effect := range plan.Resolve {
		resolved[effect.BarrierID] = struct{}{}
	}
	remaining := make([]candidate, 0, len(prior)+len(plan.Create))
	for _, barrier := range prior {
		if _, removed := resolved[barrier.ID]; !removed {
			remaining = append(remaining, candidate{
				deliveryID: barrier.DeliveryID, sequence: barrier.BarrierSeq, cause: barrier.Cause,
			})
		}
	}
	for _, effect := range plan.Create {
		remaining = append(remaining, candidate{
			deliveryID: effect.DeliveryID, sequence: effect.BarrierSeq, cause: effect.Cause,
		})
	}
	sort.Slice(remaining, func(i, j int) bool {
		if remaining[i].sequence != remaining[j].sequence {
			return remaining[i].sequence < remaining[j].sequence
		}
		return remaining[i].deliveryID.String() < remaining[j].deliveryID.String()
	})
	projection := InboxCursorReceiptProjection{LastSeenSeq: plan.After.LastSeenSeq}
	if len(remaining) != 0 {
		if remaining[0].sequence <= plan.After.LastSeenSeq || !remaining[0].cause.Valid() {
			return InboxCursorReceiptProjection{}, communicationError(
				ErrCommunicationEvidenceUnknown, "cursor result barrier is inconsistent",
			)
		}
		projection.BarrierDeliveryID = remaining[0].deliveryID
		projection.BarrierReason = remaining[0].cause
	}
	return projection, nil
}

func persistDirectNoticeCursorAdvance(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticeCursorPreflight,
	locked directNoticeCursorLockedState,
	plan CursorAdvancePlan,
	planHash []byte,
	projection InboxCursorReceiptProjection,
) (DirectNoticeCursorAdvanceResult, error) {
	dbNow := tx.now.Time()
	for _, effect := range plan.Expire {
		record, err := messageDeliveryToRecord(effect.After)
		if err != nil {
			return DirectNoticeCursorAdvanceResult{}, err
		}
		record[model.ColVersion] = effect.Before.Version
		if _, err = tx.update(ctx, messageDeliveryKind, record); err != nil {
			return DirectNoticeCursorAdvanceResult{}, err
		}
	}
	barriersByID := make(map[model.ID]InboxCursorBarrier, len(locked.activeBarriers))
	for _, barrier := range locked.activeBarriers {
		barriersByID[barrier.ID] = barrier
	}
	for _, effect := range plan.Resolve {
		before, present := barriersByID[effect.BarrierID]
		if !present || before.DeliveryID != effect.DeliveryID ||
			before.BarrierSeq != effect.BarrierSeq {
			return DirectNoticeCursorAdvanceResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "cursor barrier resolution lost its locked row",
			)
		}
		after := before
		after.Version++
		after.UpdatedAt = dbNow
		after.State = CursorBarrierResolved
		after.ResolvedAt = &dbNow
		record, err := inboxCursorBarrierToRecord(after, plan.After)
		if err != nil {
			return DirectNoticeCursorAdvanceResult{}, err
		}
		record[model.ColVersion] = before.Version
		if _, err = tx.update(ctx, inboxCursorBarrierKind, record); err != nil {
			return DirectNoticeCursorAdvanceResult{}, err
		}
	}
	if plan.CreateCursor {
		record, err := inboxCursorToRecord(plan.After)
		if err != nil {
			return DirectNoticeCursorAdvanceResult{}, err
		}
		if _, err = tx.createWithID(ctx, inboxCursorKind, plan.After.ID, record); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return DirectNoticeCursorAdvanceResult{}, errDirectNoticeCursorVersionMismatch
			}
			return DirectNoticeCursorAdvanceResult{}, err
		}
	}
	for _, effect := range plan.Create {
		barrier := InboxCursorBarrier{
			MutableCommunicationEntity: MutableCommunicationEntity{
				CommunicationEntity: CommunicationEntity{
					ID: model.NewID(), TenantID: plan.After.TenantID,
					WorkspaceID: plan.After.WorkspaceID, Version: 1, CreatedAt: dbNow,
				},
				UpdatedAt: dbNow,
			},
			Reader: plan.After.Reader, MailboxKind: plan.After.MailboxKind,
			MailboxRef: plan.After.MailboxRef,
			FilterHash: append([]byte(nil), plan.After.FilterHash...),
			DeliveryID: effect.DeliveryID, BarrierSeq: effect.BarrierSeq,
			Cause: effect.Cause, State: CursorBarrierActive, ReasonCode: effect.ReasonCode,
		}
		record, err := inboxCursorBarrierToRecord(barrier, plan.After)
		if err != nil {
			return DirectNoticeCursorAdvanceResult{}, err
		}
		if _, err = tx.createWithID(ctx, inboxCursorBarrierKind, barrier.ID, record); err != nil {
			return DirectNoticeCursorAdvanceResult{}, err
		}
	}
	if !plan.CreateCursor && plan.Changed {
		record, err := inboxCursorToRecord(plan.After)
		if err != nil {
			return DirectNoticeCursorAdvanceResult{}, err
		}
		record[model.ColVersion] = plan.Before.Version
		if _, err = tx.update(ctx, inboxCursorKind, record); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return DirectNoticeCursorAdvanceResult{}, errDirectNoticeCursorVersionMismatch
			}
			return DirectNoticeCursorAdvanceResult{}, err
		}
	}
	applyCommitment, err := directNoticeCursorApplyCommitmentFromPlan(
		preflight, plan, planHash, projection, dbNow,
	)
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	audit, err := tx.appendAudit(ctx, model.AuditDraft{
		Actor: directNoticeActor(preflight.normalized.principal), ActorKind: model.ActorUser,
		Action: directNoticeCursorAdvanceAuditAction, TargetKind: communicationCommandKind,
		TargetID: preflight.commandID, PayloadHash: append([]byte(nil), planHash...),
		Meta: map[string]any{
			"workspace_id":  preflight.normalized.scope.WorkspaceID.String(),
			"command_scope": preflight.normalized.commandScope,
			"cursor_id":     plan.After.ID.String(), "cursor_version": plan.After.Version,
			"last_seen_seq":            plan.After.LastSeenSeq,
			"apply_commitment_version": directNoticeCursorApplyCommitmentV1,
			"apply_commitment":         hex.EncodeToString(applyCommitment),
		},
	})
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	if audit.Seq < 1 || len(audit.Hash) != sha256.Size {
		return DirectNoticeCursorAdvanceResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "cursor audit append returned no durable anchor",
		)
	}
	result := DirectNoticeCursorAdvanceResult{
		CommandID: preflight.commandID, CursorID: plan.After.ID,
		Version: plan.After.Version, ETag: fmt.Sprintf("\"v%d\"", plan.After.Version),
		Projection: projection, AuditSeq: audit.Seq,
	}
	receipt, err := buildDirectNoticeCursorReceipt(
		dbNow, preflight, plan, planHash, audit, result,
	)
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	reconstructedCommitment, err := directNoticeCursorApplyCommitmentFromReceipt(receipt)
	if err != nil || !bytes.Equal(reconstructedCommitment, applyCommitment) {
		return DirectNoticeCursorAdvanceResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"cursor receipt does not reconstruct its audited apply commitment",
		)
	}
	record, err := communicationCommandReceiptToRecord(receipt)
	if err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	if _, err = tx.createWithID(
		ctx, communicationCommandKind, receipt.ID, record,
	); err != nil {
		return DirectNoticeCursorAdvanceResult{}, err
	}
	return result, nil
}

func buildDirectNoticeCursorReceipt(
	dbNow time.Time,
	preflight directNoticeCursorPreflight,
	plan CursorAdvancePlan,
	planHash []byte,
	audit model.AuditEvent,
	result DirectNoticeCursorAdvanceResult,
) (CommunicationCommandReceipt, error) {
	projection := result.Projection
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: preflight.receiptID, TenantID: preflight.normalized.scope.TenantID,
				WorkspaceID: preflight.normalized.scope.WorkspaceID,
				Version:     1, CreatedAt: dbNow,
			},
		},
		CommandID:          preflight.commandID,
		ActorFingerprint:   append([]byte(nil), preflight.normalized.actorFingerprint...),
		CommandScope:       preflight.normalized.commandScope,
		IdempotencyKeyHash: append([]byte(nil), preflight.normalized.idempotencyKeyHash...),
		RequestDigest:      append([]byte(nil), preflight.normalized.requestDigest...),
		PlanHash:           append([]byte(nil), planHash...), ResultKind: string(inboxCursorKind),
		ResultID: plan.After.ID, HTTPStatus: http.StatusOK,
		ResponseProjectionJSON: CommunicationCommandResponseProjection{
			Version: plan.After.Version, InboxCursor: &projection,
		},
		AuditSeq: audit.Seq, AuditHash: append([]byte(nil), audit.Hash...),
		CompletedAt: dbNow,
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

func normalizeDirectNoticeCursorMutationError(err error) error {
	if errors.Is(err, errDirectNoticeCursorReplayNeedsFreshAudit) {
		return communicationError(ErrCommunicationEvidenceUnknown,
			"cursor replay receipt is unavailable in the outer audit view")
	}
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
		if errors.Is(err, errDirectNoticeCursorIdempotencyReused) {
			return err
		}
		return communicationError(ErrCommunicationEvidenceUnknown,
			"cursor state changed while locking")
	}
	return err
}
