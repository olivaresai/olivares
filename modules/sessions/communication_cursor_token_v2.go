// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

const (
	communicationCursorTokenV2Prefix  = "c2v2"
	communicationCursorTokenV2Version = 2
	communicationCursorTokenV2Domain  = "olivares.sessions.inbox.cursor.c2.v2\x00"
)

type communicationCursorTokenV2WireClaims struct {
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
	DeliveryID       string `json:"did,omitempty"`
	IssuedAtUnix     int64  `json:"iat"`
	ExpiresAtUnix    int64  `json:"exp"`
}

func (claims communicationCursorTokenClaims) readerReference() string {
	if claims.readerRefText != "" {
		return claims.readerRefText
	}
	return claims.readerRef.String()
}

func (claims communicationCursorTokenClaims) mailboxReference() string {
	if claims.mailboxRefText != "" {
		return claims.mailboxRefText
	}
	return claims.mailboxRef.String()
}

func (r *communicationCursorTokenKeyring) mintV2(
	claims communicationCursorTokenClaims,
	observedAt time.Time,
) (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	if observedAt.IsZero() {
		return "", communicationCursorTokenInvalid("database observation time is missing")
	}
	issuedAtUnix := observedAt.Unix()
	if issuedAtUnix <= 0 || issuedAtUnix > math.MaxInt64-int64(communicationCursorTokenReserve/time.Second) {
		return "", communicationCursorTokenInvalid("database observation time is out of range")
	}
	claims = claims.clone()
	claims.tokenVersion = communicationCursorTokenV2Version
	claims.issuedAt = time.Unix(issuedAtUnix, 0).UTC()
	claims.expiresAt = time.Unix(issuedAtUnix+int64(communicationCursorTokenTTL/time.Second), 0).UTC()
	if err := validateCommunicationCursorTokenV2Claims(claims); err != nil {
		return "", err
	}
	wire := communicationCursorTokenV2WireClaims{
		Version: communicationCursorTokenV2Version, TenantID: claims.tenantID.String(),
		WorkspaceID: claims.workspaceID.String(), ReaderKind: string(claims.readerKind),
		ReaderRef: claims.readerReference(), MailboxKind: string(claims.mailboxKind),
		MailboxRef: claims.mailboxReference(), CarrierClass: claims.carrierClass,
		FilterHash:    base64.RawURLEncoding.EncodeToString(claims.filterHash),
		CursorVersion: claims.cursorVersion, BaseDeliverySeq: claims.baseDeliverySeq,
		AfterDeliverySeq: claims.afterDeliverySeq, IssuedAtUnix: claims.issuedAt.Unix(),
		ExpiresAtUnix: claims.expiresAt.Unix(),
	}
	if claims.cursorVersion > 0 {
		wire.CursorID = claims.cursorID.String()
	}
	if claims.afterDeliverySeq > claims.baseDeliverySeq {
		wire.DeliveryID = claims.deliveryID.String()
	}
	canonical, err := json.Marshal(wire)
	if err != nil || len(canonical) > communicationCursorTokenMaxJSON {
		return "", communicationCursorTokenInvalid("claims cannot be encoded")
	}
	mac := communicationCursorTokenV2MAC(r.keys[r.signingKID], r.signingKID, canonical)
	token := communicationCursorTokenV2Prefix + "." +
		base64.RawURLEncoding.EncodeToString([]byte(r.signingKID)) + "." +
		base64.RawURLEncoding.EncodeToString(canonical) + "." +
		base64.RawURLEncoding.EncodeToString(mac[:])
	if len(token) > communicationCursorTokenMaxBytes {
		return "", communicationCursorTokenInvalid("token exceeds the compact bound")
	}
	return token, nil
}

func (r *communicationCursorTokenKeyring) verifyV2(
	token string,
	observedAt time.Time,
) (communicationCursorTokenClaims, error) {
	if err := r.validate(); err != nil {
		return communicationCursorTokenClaims{}, err
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != communicationCursorTokenV2Prefix {
		return communicationCursorTokenClaims{}, communicationCursorTokenInvalid("token syntax or version is invalid")
	}
	kidRaw, err := decodeCanonicalCommunicationCursorTokenSegment(parts[1], communicationCursorTokenMaxKIDBytes)
	if err != nil {
		return communicationCursorTokenClaims{}, err
	}
	kid := string(kidRaw)
	key, ok := r.keys[kid]
	if !ok {
		return communicationCursorTokenClaims{}, communicationCursorTokenInvalid("key identifier is unknown")
	}
	raw, err := decodeCanonicalCommunicationCursorTokenSegment(parts[2], communicationCursorTokenMaxJSON)
	if err != nil {
		return communicationCursorTokenClaims{}, err
	}
	presented, err := decodeCanonicalCommunicationCursorTokenSegment(parts[3], sha256.Size)
	if err != nil {
		return communicationCursorTokenClaims{}, err
	}
	want := communicationCursorTokenV2MAC(key, kid, raw)
	if !hmac.Equal(presented, want[:]) {
		return communicationCursorTokenClaims{}, communicationCursorTokenInvalid("authentication failed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var wire communicationCursorTokenV2WireClaims
	if err := decoder.Decode(&wire); err != nil {
		return communicationCursorTokenClaims{}, communicationCursorTokenInvalid("claims JSON is invalid")
	}
	if err := decoder.Decode(new(struct{})); err != io.EOF {
		return communicationCursorTokenClaims{}, communicationCursorTokenInvalid("claims JSON has trailing data")
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(canonical, raw) || wire.Version != communicationCursorTokenV2Version {
		return communicationCursorTokenClaims{}, communicationCursorTokenInvalid("claims JSON is not canonical")
	}
	filterHash, err := decodeCanonicalCommunicationCursorTokenSegment(wire.FilterHash, sha256.Size)
	if err != nil {
		return communicationCursorTokenClaims{}, err
	}
	claims := communicationCursorTokenClaims{
		tokenVersion: communicationCursorTokenV2Version,
		tenantID:     model.TenantID(wire.TenantID), workspaceID: model.ID(wire.WorkspaceID),
		readerKind: RecipientKind(wire.ReaderKind), readerRefText: wire.ReaderRef,
		mailboxKind: MailboxKind(wire.MailboxKind), mailboxRefText: wire.MailboxRef,
		carrierClass: wire.CarrierClass, filterHash: filterHash,
		cursorID: model.ID(wire.CursorID), cursorVersion: wire.CursorVersion,
		baseDeliverySeq: wire.BaseDeliverySeq, afterDeliverySeq: wire.AfterDeliverySeq,
		deliveryID: model.ID(wire.DeliveryID), issuedAt: time.Unix(wire.IssuedAtUnix, 0).UTC(),
		expiresAt: time.Unix(wire.ExpiresAtUnix, 0).UTC(),
	}
	if err := validateCommunicationCursorTokenV2Claims(claims); err != nil {
		return communicationCursorTokenClaims{}, err
	}
	if observedAt.IsZero() {
		return communicationCursorTokenClaims{}, communicationCursorTokenInvalid("database observation time is missing")
	}
	now := observedAt.Unix()
	skew := int64(communicationCursorTokenClockSkew / time.Second)
	if now+skew < claims.issuedAt.Unix() {
		return communicationCursorTokenClaims{}, communicationCursorTokenInvalid("token is not yet valid")
	}
	if now-skew >= claims.expiresAt.Unix() {
		return communicationCursorTokenClaims{}, errCommunicationCursorTokenExpired
	}
	return claims, nil
}

func validateCommunicationCursorTokenV2Claims(claims communicationCursorTokenClaims) error {
	filter, err := directNoticeCursorFilterHash()
	reader := RecipientRef{Kind: claims.readerKind, Ref: claims.readerReference()}
	if err != nil || !validCommunicationCursorTokenTenantID(claims.tenantID) ||
		!validCommunicationCursorTokenID(claims.workspaceID) || reader.Validate() != nil ||
		claims.mailboxKind != MailboxPersonal || claims.mailboxReference() != reader.Ref ||
		claims.carrierClass != string(CursorCarrierDirectNoticeV1) ||
		!bytes.Equal(claims.filterHash, filter[:]) {
		return communicationCursorTokenInvalid("claims scope is invalid")
	}
	if claims.cursorVersion < 0 || claims.baseDeliverySeq < 0 || claims.afterDeliverySeq < claims.baseDeliverySeq {
		return communicationCursorTokenInvalid("claims position is invalid")
	}
	if claims.cursorVersion == 0 {
		if claims.cursorID != "" || claims.baseDeliverySeq != 0 {
			return communicationCursorTokenInvalid("virtual cursor lineage is invalid")
		}
	} else if !validCommunicationCursorTokenID(claims.cursorID) {
		return communicationCursorTokenInvalid("durable cursor lineage is invalid")
	}
	if claims.afterDeliverySeq == claims.baseDeliverySeq {
		if claims.deliveryID != "" {
			return communicationCursorTokenInvalid("unchanged target must omit delivery")
		}
	} else if !validCommunicationCursorTokenID(claims.deliveryID) {
		return communicationCursorTokenInvalid("advancing target delivery is invalid")
	}
	if claims.issuedAt.IsZero() || claims.expiresAt.Sub(claims.issuedAt) != communicationCursorTokenTTL {
		return communicationCursorTokenInvalid("claims lifetime is invalid")
	}
	return nil
}

func communicationCursorTokenV2MAC(key []byte, kid string, claims []byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(communicationCursorTokenV2Domain))
	_, _ = mac.Write([]byte(kid))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(claims)
	var result [sha256.Size]byte
	copy(result[:], mac.Sum(nil))
	return result
}
