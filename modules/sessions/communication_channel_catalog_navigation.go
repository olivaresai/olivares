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

// The visible-channel catalog continuation ("c3n1") is navigation state and
// nothing else. It is minted only after a page's bound transaction committed and
// is anchored solely to the LAST CHANNEL ACTUALLY RETURNED to the reader, so
// every claim it carries was already disclosed by the response it accompanies.
//
// The token is base64-readable and MAC-authenticated, not encrypted: anyone
// holding it can decode its JSON segment and read tenant, workspace, reader
// binding, filter hash and anchor. The HMAC prevents forgery and cross-family
// splicing; it provides no confidentiality, which is why a scanned hidden
// Channel ID, a grant ID, a candidate count or a raw store cursor must never be
// placed in the claims. A future design that needs a hidden coordinate needs
// confidential authenticated state and its own review.
//
// A token conveys no authority: every page re-resolves the current credential,
// reader identity, closure and exact per-Channel core decisions before the
// anchor is even consulted.
const (
	communicationChannelCatalogNavigationPrefix  = "c3n1"
	communicationChannelCatalogNavigationVersion = 1
	communicationChannelCatalogNavigationDomain  = "olivares.sessions.channel.catalog.c3.v1\x00"
	communicationChannelCatalogFilterDomain      = "olivares.sessions.channel.catalog.filter.v1|state=active|can_read=true|order=channel_id:asc"
)

type communicationChannelCatalogNavigationClaims struct {
	tenantID      model.TenantID
	workspaceID   model.ID
	reader        RecipientRef
	filterHash    []byte
	lastChannelID model.ID
	issuedAt      time.Time
	expiresAt     time.Time
}

type communicationChannelCatalogNavigationWireClaims struct {
	Version       int    `json:"v"`
	TenantID      string `json:"ten"`
	WorkspaceID   string `json:"ws"`
	ReaderKind    string `json:"rk"`
	ReaderRef     string `json:"rr"`
	FilterHash    string `json:"fh"`
	LastChannelID string `json:"lc"`
	IssuedAtUnix  int64  `json:"iat"`
	ExpiresAtUnix int64  `json:"exp"`
}

// channelCatalogFilterHash commits the catalog's filter and ordering so a token
// minted for one listing shape cannot resume a differently shaped listing.
func channelCatalogFilterHash() [sha256.Size]byte {
	return sha256.Sum256([]byte(communicationChannelCatalogFilterDomain))
}

func (r *communicationCursorTokenKeyring) mintChannelCatalogNavigation(
	claims communicationChannelCatalogNavigationClaims,
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
	if err := validateCommunicationChannelCatalogNavigationClaims(claims); err != nil {
		return "", err
	}
	wire := communicationChannelCatalogNavigationWireClaims{
		Version:  communicationChannelCatalogNavigationVersion,
		TenantID: claims.tenantID.String(), WorkspaceID: claims.workspaceID.String(),
		ReaderKind: string(claims.reader.Kind), ReaderRef: claims.reader.Ref,
		FilterHash:    base64.RawURLEncoding.EncodeToString(claims.filterHash),
		LastChannelID: claims.lastChannelID.String(),
		IssuedAtUnix:  claims.issuedAt.Unix(), ExpiresAtUnix: claims.expiresAt.Unix(),
	}
	canonical, err := json.Marshal(wire)
	if err != nil || len(canonical) > communicationCursorTokenMaxJSON {
		return "", communicationCursorTokenInvalid("catalog navigation claims cannot be encoded")
	}
	mac := communicationChannelCatalogNavigationMAC(r.keys[r.signingKID], r.signingKID, canonical)
	token := communicationChannelCatalogNavigationPrefix + "." +
		base64.RawURLEncoding.EncodeToString([]byte(r.signingKID)) + "." +
		base64.RawURLEncoding.EncodeToString(canonical) + "." +
		base64.RawURLEncoding.EncodeToString(mac[:])
	if len(token) > communicationCursorTokenMaxBytes {
		return "", communicationCursorTokenInvalid("catalog navigation token exceeds the compact bound")
	}
	return token, nil
}

func (r *communicationCursorTokenKeyring) verifyChannelCatalogNavigation(
	token string,
	observedAt time.Time,
) (communicationChannelCatalogNavigationClaims, error) {
	if err := r.validate(); err != nil {
		return communicationChannelCatalogNavigationClaims{}, err
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != communicationChannelCatalogNavigationPrefix {
		return communicationChannelCatalogNavigationClaims{}, communicationCursorTokenInvalid(
			"catalog navigation token syntax or family is invalid",
		)
	}
	kidRaw, err := decodeCanonicalCommunicationCursorTokenSegment(parts[1], communicationCursorTokenMaxKIDBytes)
	if err != nil || !validCommunicationCursorTokenKID(string(kidRaw)) {
		return communicationChannelCatalogNavigationClaims{}, communicationCursorTokenInvalid(
			"catalog navigation key identifier is invalid",
		)
	}
	kid := string(kidRaw)
	key, ok := r.keys[kid]
	if !ok {
		return communicationChannelCatalogNavigationClaims{}, communicationCursorTokenInvalid(
			"catalog navigation key identifier is unknown",
		)
	}
	raw, err := decodeCanonicalCommunicationCursorTokenSegment(parts[2], communicationCursorTokenMaxJSON)
	if err != nil {
		return communicationChannelCatalogNavigationClaims{}, err
	}
	presented, err := decodeCanonicalCommunicationCursorTokenSegment(parts[3], sha256.Size)
	if err != nil {
		return communicationChannelCatalogNavigationClaims{}, err
	}
	want := communicationChannelCatalogNavigationMAC(key, kid, raw)
	if !hmac.Equal(presented, want[:]) {
		return communicationChannelCatalogNavigationClaims{}, communicationCursorTokenInvalid(
			"catalog navigation authentication failed",
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var wire communicationChannelCatalogNavigationWireClaims
	if err := decoder.Decode(&wire); err != nil {
		return communicationChannelCatalogNavigationClaims{}, communicationCursorTokenInvalid(
			"catalog navigation claims JSON is invalid",
		)
	}
	if err := decoder.Decode(new(struct{})); err != io.EOF {
		return communicationChannelCatalogNavigationClaims{}, communicationCursorTokenInvalid(
			"catalog navigation claims JSON has trailing data",
		)
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(canonical, raw) ||
		wire.Version != communicationChannelCatalogNavigationVersion {
		return communicationChannelCatalogNavigationClaims{}, communicationCursorTokenInvalid(
			"catalog navigation claims JSON is not canonical",
		)
	}
	filterHash, err := decodeCanonicalCommunicationCursorTokenSegment(wire.FilterHash, sha256.Size)
	if err != nil {
		return communicationChannelCatalogNavigationClaims{}, err
	}
	claims := communicationChannelCatalogNavigationClaims{
		tenantID: model.TenantID(wire.TenantID), workspaceID: model.ID(wire.WorkspaceID),
		reader:        RecipientRef{Kind: RecipientKind(wire.ReaderKind), Ref: wire.ReaderRef},
		filterHash:    filterHash,
		lastChannelID: model.ID(wire.LastChannelID),
		issuedAt:      time.Unix(wire.IssuedAtUnix, 0).UTC(),
		expiresAt:     time.Unix(wire.ExpiresAtUnix, 0).UTC(),
	}
	if err := validateCommunicationChannelCatalogNavigationClaims(claims); err != nil {
		return communicationChannelCatalogNavigationClaims{}, err
	}
	if observedAt.IsZero() {
		return communicationChannelCatalogNavigationClaims{}, communicationCursorTokenInvalid(
			"database observation time is missing",
		)
	}
	now := observedAt.Unix()
	skew := int64(communicationCursorTokenClockSkew / time.Second)
	if now+skew < claims.issuedAt.Unix() {
		return communicationChannelCatalogNavigationClaims{}, communicationCursorTokenInvalid(
			"catalog navigation token is not yet valid",
		)
	}
	if now-skew >= claims.expiresAt.Unix() {
		return communicationChannelCatalogNavigationClaims{}, errCommunicationCursorTokenExpired
	}
	return claims, nil
}

func validateCommunicationChannelCatalogNavigationClaims(
	claims communicationChannelCatalogNavigationClaims,
) error {
	filter := channelCatalogFilterHash()
	if !validCommunicationCursorTokenTenantID(claims.tenantID) ||
		!validCommunicationCursorTokenID(claims.workspaceID) || claims.reader.Validate() != nil ||
		!bytes.Equal(claims.filterHash, filter[:]) {
		return communicationCursorTokenInvalid("catalog navigation scope is invalid")
	}
	if !validCommunicationCursorTokenID(claims.lastChannelID) {
		return communicationCursorTokenInvalid("catalog navigation anchor is invalid")
	}
	if claims.issuedAt.IsZero() || claims.expiresAt.Sub(claims.issuedAt) != communicationCursorTokenTTL {
		return communicationCursorTokenInvalid("catalog navigation lifetime is invalid")
	}
	return nil
}

func communicationChannelCatalogNavigationMAC(key []byte, kid string, claims []byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(communicationChannelCatalogNavigationDomain))
	_, _ = mac.Write([]byte(kid))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(claims)
	var out [sha256.Size]byte
	copy(out[:], mac.Sum(nil))
	return out
}
