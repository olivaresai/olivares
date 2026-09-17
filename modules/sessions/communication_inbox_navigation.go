// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	communicationInboxNavigationPrefix  = "c2n1"
	communicationInboxNavigationVersion = 1
	communicationInboxNavigationDomain  = "olivares.sessions.inbox.navigation.c2.v1\x00"
)

// DirectNoticeInboxRequest is the public inbox navigation contract. The
// continuation is opaque: delivery sequence numbers remain an internal scan
// coordinate and are never accepted from an HTTP caller.
type DirectNoticeInboxRequest struct {
	Continuation string `json:"continuation,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

type communicationInboxNavigationClaims struct {
	tenantID         model.TenantID
	workspaceID      model.ID
	reader           RecipientRef
	filterHash       []byte
	cursorID         model.ID
	cursorVersion    int64
	baseDeliverySeq  int64
	afterDeliverySeq int64
	deliveryID       model.ID
	issuedAt         time.Time
	expiresAt        time.Time
}

type communicationInboxNavigationWireClaims struct {
	Version          int    `json:"v"`
	TenantID         string `json:"ten"`
	WorkspaceID      string `json:"ws"`
	ReaderKind       string `json:"rk"`
	ReaderRef        string `json:"rr"`
	MailboxKind      string `json:"mk"`
	MailboxRef       string `json:"mr"`
	CarrierClass     string `json:"cc"`
	FilterHash       string `json:"fh"`
	CursorID         string `json:"cid,omitempty"`
	CursorVersion    int64  `json:"cv"`
	BaseDeliverySeq  int64  `json:"base"`
	AfterDeliverySeq int64  `json:"after"`
	DeliveryID       string `json:"did"`
	IssuedAtUnix     int64  `json:"iat"`
	ExpiresAtUnix    int64  `json:"exp"`
}

type communicationInboxCursorLineage struct {
	cursorID        model.ID
	cursorVersion   int64
	baseDeliverySeq int64
}

func (l communicationInboxCursorLineage) matchesClaims(claims communicationInboxNavigationClaims) bool {
	return l.cursorID == claims.cursorID && l.cursorVersion == claims.cursorVersion &&
		l.baseDeliverySeq == claims.baseDeliverySeq
}

func (l communicationInboxCursorLineage) equal(other communicationInboxCursorLineage) bool {
	return l == other
}

func (r *communicationCursorTokenKeyring) mintInboxNavigation(
	claims communicationInboxNavigationClaims,
	observedAt time.Time,
) (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	issuedAtUnix := observedAt.Unix()
	if observedAt.IsZero() || issuedAtUnix <= 0 ||
		issuedAtUnix > math.MaxInt64-int64(communicationCursorTokenReserve/time.Second) {
		return "", communicationCursorTokenInvalid("database observation time is out of range")
	}
	claims.filterHash = append([]byte(nil), claims.filterHash...)
	claims.issuedAt = time.Unix(issuedAtUnix, 0).UTC()
	claims.expiresAt = claims.issuedAt.Add(communicationCursorTokenTTL)
	if err := validateCommunicationInboxNavigationClaims(claims); err != nil {
		return "", err
	}
	wire := communicationInboxNavigationWireClaims{
		Version:  communicationInboxNavigationVersion,
		TenantID: claims.tenantID.String(), WorkspaceID: claims.workspaceID.String(),
		ReaderKind: string(claims.reader.Kind), ReaderRef: claims.reader.Ref,
		MailboxKind: string(MailboxPersonal), MailboxRef: claims.reader.Ref,
		CarrierClass:  string(CursorCarrierDirectNoticeV1),
		FilterHash:    base64.RawURLEncoding.EncodeToString(claims.filterHash),
		CursorVersion: claims.cursorVersion, BaseDeliverySeq: claims.baseDeliverySeq,
		AfterDeliverySeq: claims.afterDeliverySeq, DeliveryID: claims.deliveryID.String(),
		IssuedAtUnix: claims.issuedAt.Unix(), ExpiresAtUnix: claims.expiresAt.Unix(),
	}
	if claims.cursorVersion > 0 {
		wire.CursorID = claims.cursorID.String()
	}
	canonical, err := json.Marshal(wire)
	if err != nil || len(canonical) > communicationCursorTokenMaxJSON {
		return "", communicationCursorTokenInvalid("navigation claims cannot be encoded")
	}
	mac := communicationInboxNavigationMAC(r.keys[r.signingKID], r.signingKID, canonical)
	token := communicationInboxNavigationPrefix + "." +
		base64.RawURLEncoding.EncodeToString([]byte(r.signingKID)) + "." +
		base64.RawURLEncoding.EncodeToString(canonical) + "." +
		base64.RawURLEncoding.EncodeToString(mac[:])
	if len(token) > communicationCursorTokenMaxBytes {
		return "", communicationCursorTokenInvalid("navigation token exceeds the compact bound")
	}
	return token, nil
}

func (r *communicationCursorTokenKeyring) verifyInboxNavigation(
	token string,
	observedAt time.Time,
) (communicationInboxNavigationClaims, error) {
	if err := r.validate(); err != nil {
		return communicationInboxNavigationClaims{}, err
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != communicationInboxNavigationPrefix {
		return communicationInboxNavigationClaims{}, communicationCursorTokenInvalid("navigation token syntax or version is invalid")
	}
	kidRaw, err := decodeCanonicalCommunicationCursorTokenSegment(parts[1], communicationCursorTokenMaxKIDBytes)
	if err != nil || !validCommunicationCursorTokenKID(string(kidRaw)) {
		return communicationInboxNavigationClaims{}, communicationCursorTokenInvalid("navigation key identifier is invalid")
	}
	kid := string(kidRaw)
	key, ok := r.keys[kid]
	if !ok {
		return communicationInboxNavigationClaims{}, communicationCursorTokenInvalid("navigation key identifier is unknown")
	}
	raw, err := decodeCanonicalCommunicationCursorTokenSegment(parts[2], communicationCursorTokenMaxJSON)
	if err != nil {
		return communicationInboxNavigationClaims{}, err
	}
	presented, err := decodeCanonicalCommunicationCursorTokenSegment(parts[3], sha256.Size)
	if err != nil {
		return communicationInboxNavigationClaims{}, err
	}
	want := communicationInboxNavigationMAC(key, kid, raw)
	if !hmac.Equal(presented, want[:]) {
		return communicationInboxNavigationClaims{}, communicationCursorTokenInvalid("navigation authentication failed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var wire communicationInboxNavigationWireClaims
	if err := decoder.Decode(&wire); err != nil {
		return communicationInboxNavigationClaims{}, communicationCursorTokenInvalid("navigation claims JSON is invalid")
	}
	if err := decoder.Decode(new(struct{})); err != io.EOF {
		return communicationInboxNavigationClaims{}, communicationCursorTokenInvalid("navigation claims JSON has trailing data")
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(canonical, raw) || wire.Version != communicationInboxNavigationVersion {
		return communicationInboxNavigationClaims{}, communicationCursorTokenInvalid("navigation claims JSON is not canonical")
	}
	filterHash, err := decodeCanonicalCommunicationCursorTokenSegment(wire.FilterHash, sha256.Size)
	if err != nil {
		return communicationInboxNavigationClaims{}, err
	}
	if wire.MailboxKind != string(MailboxPersonal) || wire.MailboxRef != wire.ReaderRef ||
		wire.CarrierClass != string(CursorCarrierDirectNoticeV1) {
		return communicationInboxNavigationClaims{}, communicationCursorTokenInvalid("navigation mailbox binding is invalid")
	}
	claims := communicationInboxNavigationClaims{
		tenantID: model.TenantID(wire.TenantID), workspaceID: model.ID(wire.WorkspaceID),
		reader:     RecipientRef{Kind: RecipientKind(wire.ReaderKind), Ref: wire.ReaderRef},
		filterHash: filterHash, cursorID: model.ID(wire.CursorID),
		cursorVersion: wire.CursorVersion, baseDeliverySeq: wire.BaseDeliverySeq,
		afterDeliverySeq: wire.AfterDeliverySeq, deliveryID: model.ID(wire.DeliveryID),
		issuedAt:  time.Unix(wire.IssuedAtUnix, 0).UTC(),
		expiresAt: time.Unix(wire.ExpiresAtUnix, 0).UTC(),
	}
	if err := validateCommunicationInboxNavigationClaims(claims); err != nil {
		return communicationInboxNavigationClaims{}, err
	}
	if observedAt.IsZero() {
		return communicationInboxNavigationClaims{}, communicationCursorTokenInvalid("database observation time is missing")
	}
	now := observedAt.Unix()
	skew := int64(communicationCursorTokenClockSkew / time.Second)
	if now+skew < claims.issuedAt.Unix() {
		return communicationInboxNavigationClaims{}, communicationCursorTokenInvalid("navigation token is not yet valid")
	}
	if now-skew >= claims.expiresAt.Unix() {
		return communicationInboxNavigationClaims{}, errCommunicationCursorTokenExpired
	}
	return claims, nil
}

func validateCommunicationInboxNavigationClaims(claims communicationInboxNavigationClaims) error {
	filter, err := directNoticeCursorFilterHash()
	if err != nil || !validCommunicationCursorTokenTenantID(claims.tenantID) ||
		!validCommunicationCursorTokenID(claims.workspaceID) || claims.reader.Validate() != nil ||
		!bytes.Equal(claims.filterHash, filter[:]) {
		return communicationCursorTokenInvalid("navigation scope is invalid")
	}
	if claims.cursorVersion < 0 || claims.baseDeliverySeq < 0 ||
		claims.afterDeliverySeq <= claims.baseDeliverySeq || claims.deliveryID.IsZero() {
		return communicationCursorTokenInvalid("navigation position is invalid")
	}
	if claims.cursorVersion == 0 {
		if !claims.cursorID.IsZero() || claims.baseDeliverySeq != 0 {
			return communicationCursorTokenInvalid("virtual navigation lineage is invalid")
		}
	} else if !validCommunicationCursorTokenID(claims.cursorID) {
		return communicationCursorTokenInvalid("durable navigation lineage is invalid")
	}
	if !validCommunicationCursorTokenID(claims.deliveryID) || claims.issuedAt.IsZero() ||
		claims.expiresAt.Sub(claims.issuedAt) != communicationCursorTokenTTL {
		return communicationCursorTokenInvalid("navigation target or lifetime is invalid")
	}
	return nil
}

func communicationInboxNavigationMAC(key []byte, kid string, claims []byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(communicationInboxNavigationDomain))
	_, _ = mac.Write([]byte(kid))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(claims)
	var result [sha256.Size]byte
	copy(result[:], mac.Sum(nil))
	return result
}

func (m *Module) observeCommunicationInboxCursorLineage(
	ctx context.Context,
	scope DirectoryScopeRef,
	reader RecipientRef,
	target *directNoticeInboxCandidate,
) (communicationInboxCursorLineage, time.Time, error) {
	filter, filterHash, err := CanonicalCursorFilter(CursorFilter{
		CarrierClass: CursorCarrierDirectNoticeV1, MailboxKind: MailboxPersonal,
	})
	_ = filter
	if err != nil {
		return communicationInboxCursorLineage{}, time.Time{}, err
	}
	var lineage communicationInboxCursorLineage
	var observedAt time.Time
	err = m.viewCommunication(ctx, scope, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return communicationTransactionUnavailable("inbox navigation clock", nil)
		}
		now, err := clock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		observedAt = now.Time()
		repo, err := sc.Ext(inboxCursorKind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(ctx, model.Query{Filters: []model.Filter{
			{Column: colCommReaderKind, Op: model.OpEq, Value: string(reader.Kind)},
			{Column: colCommReaderRef, Op: model.OpEq, Value: reader.Ref},
			{Column: colCommMailboxKind, Op: model.OpEq, Value: string(MailboxPersonal)},
			{Column: colCommMailboxRef, Op: model.OpEq, Value: reader.Ref},
			{Column: colCommFilterHash, Op: model.OpEq, Value: filterHash},
		}, Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) > 1 || page.HasMore {
			return communicationError(ErrCommunicationEvidenceUnknown, "inbox cursor identity is ambiguous")
		}
		if len(rows) == 1 {
			cursor, err := inboxCursorFromRecord(rows[0])
			if err != nil {
				return err
			}
			lineage = communicationInboxCursorLineage{
				cursorID: cursor.ID, cursorVersion: cursor.Version,
				baseDeliverySeq: cursor.LastSeenSeq,
			}
		}
		if target == nil {
			return nil
		}
		if !validCanonicalCommunicationID(target.DeliveryID) ||
			target.DeliverySeq <= lineage.baseDeliverySeq {
			return communicationCursorTokenInvalid("navigation target is outside the current lineage")
		}
		deliveries, err := sc.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		delivery, err := deliveries.Get(ctx, target.DeliveryID)
		if err != nil {
			return communicationCursorTokenInvalid("navigation target is unavailable")
		}
		if delivery.String(colCommRecipientKind) != string(reader.Kind) ||
			delivery.String(colCommRecipientRef) != reader.Ref ||
			delivery.Int(colCommDeliverySeq) != target.DeliverySeq {
			return communicationCursorTokenInvalid("navigation target crossed its mailbox")
		}
		return nil
	})
	return lineage, observedAt, err
}

func (m *Module) resolveCommunicationInboxNavigation(
	ctx context.Context,
	scope DirectoryScopeRef,
	reader RecipientRef,
	token string,
) (communicationInboxNavigationClaims, communicationInboxCursorLineage, time.Time, error) {
	lineage, observedAt, err := m.observeCommunicationInboxCursorLineage(ctx, scope, reader, nil)
	if err != nil {
		return communicationInboxNavigationClaims{}, communicationInboxCursorLineage{}, time.Time{}, err
	}
	claims, err := m.communicationCursorTokenKeyring().verifyInboxNavigation(token, observedAt)
	if err != nil {
		return communicationInboxNavigationClaims{}, communicationInboxCursorLineage{}, time.Time{}, err
	}
	if claims.tenantID != scope.TenantID || claims.workspaceID != scope.WorkspaceID ||
		claims.reader != reader {
		return communicationInboxNavigationClaims{}, communicationInboxCursorLineage{}, time.Time{},
			communicationCursorTokenInvalid("navigation token crossed its authenticated mailbox")
	}
	if !lineage.matchesClaims(claims) {
		return communicationInboxNavigationClaims{}, communicationInboxCursorLineage{}, time.Time{},
			errDirectNoticeCursorVersionMismatch
	}
	target := directNoticeInboxCandidate{
		DeliveryID: claims.deliveryID, DeliverySeq: claims.afterDeliverySeq,
	}
	checked, checkedAt, err := m.observeCommunicationInboxCursorLineage(ctx, scope, reader, &target)
	if err != nil {
		return communicationInboxNavigationClaims{}, communicationInboxCursorLineage{}, time.Time{}, err
	}
	if !lineage.equal(checked) {
		return communicationInboxNavigationClaims{}, communicationInboxCursorLineage{}, time.Time{},
			errDirectNoticeCursorVersionMismatch
	}
	return claims, checked, checkedAt, nil
}

func (m *Module) ListDirectNoticeInbox(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	request DirectNoticeInboxRequest,
) (DirectNoticeInboxPage, error) {
	if request.Limit < 0 || request.Limit > directNoticeInboxMaximumLimit ||
		len(request.Continuation) > communicationCursorTokenMaxBytes {
		return DirectNoticeInboxPage{}, communicationError(
			ErrInvalidCommunicationModel, "invalid direct notice inbox navigation",
		)
	}
	identity, err := m.bindCurrentCommunicationInboxIdentity(ctx, scope, ref)
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}
	readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
	if readinessErr != nil || !readiness.Effective {
		return DirectNoticeInboxPage{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
		)
	}
	reader, _, err := m.communicationPrincipalRecipient(ctx, scope, identity.principal)
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}
	lineage, observedAt, err := m.observeCommunicationInboxCursorLineage(ctx, scope, reader, nil)
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}
	after := lineage.baseDeliverySeq
	if request.Continuation != "" {
		claims, resolvedLineage, tokenObservedAt, err := m.resolveCommunicationInboxNavigation(
			ctx, scope, reader, request.Continuation,
		)
		if err != nil {
			return DirectNoticeInboxPage{}, err
		}
		lineage, observedAt, after = resolvedLineage, tokenObservedAt, claims.afterDeliverySeq
	}
	query := DirectNoticeInboxQuery{AfterDeliverySeq: after, Limit: request.Limit}
	query, err = normalizeDirectNoticeInboxQuery(query)
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}
	page, err := m.listDirectNoticeInboxWithBoundAuthority(ctx, identity, query, OpenProtectedPayload)
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}
	current, currentAt, err := m.observeCommunicationInboxCursorLineage(ctx, scope, reader, nil)
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}
	if !lineage.equal(current) {
		return DirectNoticeInboxPage{}, errDirectNoticeCursorVersionMismatch
	}
	if currentAt.After(observedAt) {
		observedAt = currentAt
	}
	if len(page.Items) == 0 {
		return page, nil
	}
	last := page.Items[len(page.Items)-1].Delivery
	filterHash, err := directNoticeCursorFilterHash()
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}
	token, err := m.communicationCursorTokenKeyring().mintInboxNavigation(
		communicationInboxNavigationClaims{
			tenantID: scope.TenantID, workspaceID: scope.WorkspaceID, reader: reader,
			filterHash: filterHash[:], cursorID: lineage.cursorID,
			cursorVersion: lineage.cursorVersion, baseDeliverySeq: lineage.baseDeliverySeq,
			afterDeliverySeq: last.DeliverySeq, deliveryID: last.ID,
		},
		observedAt,
	)
	if err != nil {
		return DirectNoticeInboxPage{}, err
	}
	page.CursorTarget = token
	if page.HasMore {
		page.Continuation = token
	}
	return page, nil
}
