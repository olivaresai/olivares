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

// The two administrative continuations are their OWN navigation families, with
// their own MAC domains: "c3a1" for the administrable-Channel catalog and
// "c3g1" for one Channel's grant generations. They are NOT the read catalog's
// "c3n1": a token minted for a read listing must not resume an administrative
// one, and the separate domain makes that a verification failure rather than a
// convention. The keyring, its rotation, its five-minute lifetime and its
// database-time clock are the existing ones.
//
// Like c3n1 they are base64-readable and MAC-authenticated, NOT encrypted:
// anyone holding one decodes its JSON segment. So the claims may only carry
// coordinates the accompanying response ALREADY disclosed to this reader —
// tenant, workspace, reader binding, filter hash, the anchor actually returned,
// and (for grants) the Channel identity and revisions the page itself returned.
// A hidden Channel, a grant of another subject, a store cursor and a count are
// never claims.
//
// A token conveys no authority: every page re-resolves the credential, the
// reader identity, the grant-subject closure and the exact per-Channel core
// ChannelAdmin decision before the anchor is consulted at all.
const (
	communicationChannelAdministrationNavigationPrefix  = "c3a1"
	communicationChannelAdministrationNavigationVersion = 1
	communicationChannelAdministrationNavigationDomain  = "olivares.sessions.channel.administration.c3.v1\x00"
	communicationChannelAdministrationFilterDomain      = "olivares.sessions.channel.administration.filter.v1|can_admin=true|order=channel_id:asc|state="

	communicationChannelGrantAdministrationNavigationPrefix  = "c3g1"
	communicationChannelGrantAdministrationNavigationVersion = 1
	communicationChannelGrantAdministrationNavigationDomain  = "olivares.sessions.channel.grants.c3.v1\x00"
	communicationChannelGrantAdministrationFilterDomain      = "olivares.sessions.channel.grants.filter.v1|order=grant_id:asc"
)

type communicationChannelAdministrationNavigationClaims struct {
	tenantID      model.TenantID
	workspaceID   model.ID
	reader        RecipientRef
	filterHash    []byte
	lastChannelID model.ID
	issuedAt      time.Time
	expiresAt     time.Time
}

type communicationChannelAdministrationNavigationWireClaims struct {
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

type communicationChannelGrantAdministrationNavigationClaims struct {
	tenantID       model.TenantID
	workspaceID    model.ID
	reader         RecipientRef
	channelID      model.ID
	channelVersion int64
	aclRevision    int64
	filterHash     []byte
	lastGrantID    model.ID
	issuedAt       time.Time
	expiresAt      time.Time
}

type communicationChannelGrantAdministrationNavigationWireClaims struct {
	Version        int    `json:"v"`
	TenantID       string `json:"ten"`
	WorkspaceID    string `json:"ws"`
	ReaderKind     string `json:"rk"`
	ReaderRef      string `json:"rr"`
	ChannelID      string `json:"ch"`
	ChannelVersion int64  `json:"cv"`
	ACLRevision    int64  `json:"acl"`
	FilterHash     string `json:"fh"`
	LastGrantID    string `json:"lg"`
	IssuedAtUnix   int64  `json:"iat"`
	ExpiresAtUnix  int64  `json:"exp"`
}

// channelAdministrationFilterHash commits the administrative catalog's state
// selection and ordering, so a token minted for one selection cannot resume a
// differently shaped listing.
func channelAdministrationFilterHash(state ChannelAdministrationStateFilter) [sha256.Size]byte {
	return sha256.Sum256([]byte(communicationChannelAdministrationFilterDomain + string(state)))
}

// channelGrantAdministrationFilterHash commits the grant page's persisted-state
// selection and its optional exact subject. An absent subject is committed as
// the empty kind/ref, so "all subjects" and "one subject" are different filters.
func channelGrantAdministrationFilterHash(
	state ChannelGrantAdministrationStateFilter,
	subject CommunicationSubjectRef,
) [sha256.Size]byte {
	return sha256.Sum256([]byte(
		communicationChannelGrantAdministrationFilterDomain +
			"|state=" + string(state) +
			"|subject_kind=" + string(subject.Kind) +
			"|subject_ref=" + subject.Ref,
	))
}

func (r *communicationCursorTokenKeyring) mintChannelAdministrationNavigation(
	claims communicationChannelAdministrationNavigationClaims,
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
	if err := validateCommunicationChannelAdministrationNavigationClaims(claims); err != nil {
		return "", err
	}
	wire := communicationChannelAdministrationNavigationWireClaims{
		Version:  communicationChannelAdministrationNavigationVersion,
		TenantID: claims.tenantID.String(), WorkspaceID: claims.workspaceID.String(),
		ReaderKind: string(claims.reader.Kind), ReaderRef: claims.reader.Ref,
		FilterHash:    base64.RawURLEncoding.EncodeToString(claims.filterHash),
		LastChannelID: claims.lastChannelID.String(),
		IssuedAtUnix:  claims.issuedAt.Unix(), ExpiresAtUnix: claims.expiresAt.Unix(),
	}
	canonical, err := json.Marshal(wire)
	if err != nil || len(canonical) > communicationCursorTokenMaxJSON {
		return "", communicationCursorTokenInvalid("administration navigation claims cannot be encoded")
	}
	return assembleCommunicationChannelAdministrationToken(
		r, communicationChannelAdministrationNavigationPrefix,
		communicationChannelAdministrationNavigationDomain, canonical,
	)
}

func (r *communicationCursorTokenKeyring) verifyChannelAdministrationNavigation(
	token string,
	observedAt time.Time,
) (communicationChannelAdministrationNavigationClaims, error) {
	raw, err := openCommunicationChannelAdministrationToken(
		r, token, communicationChannelAdministrationNavigationPrefix,
		communicationChannelAdministrationNavigationDomain,
	)
	if err != nil {
		return communicationChannelAdministrationNavigationClaims{}, err
	}
	var wire communicationChannelAdministrationNavigationWireClaims
	if err := decodeCanonicalCommunicationChannelAdministrationClaims(raw, &wire); err != nil {
		return communicationChannelAdministrationNavigationClaims{}, err
	}
	if wire.Version != communicationChannelAdministrationNavigationVersion {
		return communicationChannelAdministrationNavigationClaims{}, communicationCursorTokenInvalid(
			"administration navigation claims JSON is not canonical",
		)
	}
	filterHash, err := decodeCanonicalCommunicationCursorTokenSegment(wire.FilterHash, sha256.Size)
	if err != nil {
		return communicationChannelAdministrationNavigationClaims{}, err
	}
	claims := communicationChannelAdministrationNavigationClaims{
		tenantID: model.TenantID(wire.TenantID), workspaceID: model.ID(wire.WorkspaceID),
		reader:        RecipientRef{Kind: RecipientKind(wire.ReaderKind), Ref: wire.ReaderRef},
		filterHash:    filterHash,
		lastChannelID: model.ID(wire.LastChannelID),
		issuedAt:      time.Unix(wire.IssuedAtUnix, 0).UTC(),
		expiresAt:     time.Unix(wire.ExpiresAtUnix, 0).UTC(),
	}
	if err := validateCommunicationChannelAdministrationNavigationClaims(claims); err != nil {
		return communicationChannelAdministrationNavigationClaims{}, err
	}
	if err := communicationChannelAdministrationTokenLifetimeCurrent(
		claims.issuedAt, claims.expiresAt, observedAt,
	); err != nil {
		return communicationChannelAdministrationNavigationClaims{}, err
	}
	return claims, nil
}

func validateCommunicationChannelAdministrationNavigationClaims(
	claims communicationChannelAdministrationNavigationClaims,
) error {
	if !validCommunicationCursorTokenTenantID(claims.tenantID) ||
		!validCommunicationCursorTokenID(claims.workspaceID) || claims.reader.Validate() != nil ||
		!communicationChannelAdministrationFilterHashKnown(claims.filterHash) {
		return communicationCursorTokenInvalid("administration navigation scope is invalid")
	}
	if !validCommunicationCursorTokenID(claims.lastChannelID) {
		return communicationCursorTokenInvalid("administration navigation anchor is invalid")
	}
	if claims.issuedAt.IsZero() || claims.expiresAt.Sub(claims.issuedAt) != communicationCursorTokenTTL {
		return communicationCursorTokenInvalid("administration navigation lifetime is invalid")
	}
	return nil
}

// communicationChannelAdministrationFilterHashKnown accepts only a hash this
// build can itself produce for one of the closed state selections, so a token
// cannot smuggle an unrecognized listing shape past verification.
func communicationChannelAdministrationFilterHashKnown(hash []byte) bool {
	for _, state := range channelAdministrationStateFilters() {
		known := channelAdministrationFilterHash(state)
		if bytes.Equal(hash, known[:]) {
			return true
		}
	}
	return false
}

func (r *communicationCursorTokenKeyring) mintChannelGrantAdministrationNavigation(
	claims communicationChannelGrantAdministrationNavigationClaims,
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
	if err := validateCommunicationChannelGrantAdministrationNavigationClaims(claims); err != nil {
		return "", err
	}
	wire := communicationChannelGrantAdministrationNavigationWireClaims{
		Version:  communicationChannelGrantAdministrationNavigationVersion,
		TenantID: claims.tenantID.String(), WorkspaceID: claims.workspaceID.String(),
		ReaderKind: string(claims.reader.Kind), ReaderRef: claims.reader.Ref,
		ChannelID: claims.channelID.String(), ChannelVersion: claims.channelVersion,
		ACLRevision:  claims.aclRevision,
		FilterHash:   base64.RawURLEncoding.EncodeToString(claims.filterHash),
		LastGrantID:  claims.lastGrantID.String(),
		IssuedAtUnix: claims.issuedAt.Unix(), ExpiresAtUnix: claims.expiresAt.Unix(),
	}
	canonical, err := json.Marshal(wire)
	if err != nil || len(canonical) > communicationCursorTokenMaxJSON {
		return "", communicationCursorTokenInvalid("grant navigation claims cannot be encoded")
	}
	return assembleCommunicationChannelAdministrationToken(
		r, communicationChannelGrantAdministrationNavigationPrefix,
		communicationChannelGrantAdministrationNavigationDomain, canonical,
	)
}

func (r *communicationCursorTokenKeyring) verifyChannelGrantAdministrationNavigation(
	token string,
	observedAt time.Time,
) (communicationChannelGrantAdministrationNavigationClaims, error) {
	raw, err := openCommunicationChannelAdministrationToken(
		r, token, communicationChannelGrantAdministrationNavigationPrefix,
		communicationChannelGrantAdministrationNavigationDomain,
	)
	if err != nil {
		return communicationChannelGrantAdministrationNavigationClaims{}, err
	}
	var wire communicationChannelGrantAdministrationNavigationWireClaims
	if err := decodeCanonicalCommunicationChannelAdministrationClaims(raw, &wire); err != nil {
		return communicationChannelGrantAdministrationNavigationClaims{}, err
	}
	if wire.Version != communicationChannelGrantAdministrationNavigationVersion {
		return communicationChannelGrantAdministrationNavigationClaims{}, communicationCursorTokenInvalid(
			"grant navigation claims JSON is not canonical",
		)
	}
	filterHash, err := decodeCanonicalCommunicationCursorTokenSegment(wire.FilterHash, sha256.Size)
	if err != nil {
		return communicationChannelGrantAdministrationNavigationClaims{}, err
	}
	claims := communicationChannelGrantAdministrationNavigationClaims{
		tenantID: model.TenantID(wire.TenantID), workspaceID: model.ID(wire.WorkspaceID),
		reader:         RecipientRef{Kind: RecipientKind(wire.ReaderKind), Ref: wire.ReaderRef},
		channelID:      model.ID(wire.ChannelID),
		channelVersion: wire.ChannelVersion, aclRevision: wire.ACLRevision,
		filterHash:  filterHash,
		lastGrantID: model.ID(wire.LastGrantID),
		issuedAt:    time.Unix(wire.IssuedAtUnix, 0).UTC(),
		expiresAt:   time.Unix(wire.ExpiresAtUnix, 0).UTC(),
	}
	if err := validateCommunicationChannelGrantAdministrationNavigationClaims(claims); err != nil {
		return communicationChannelGrantAdministrationNavigationClaims{}, err
	}
	if err := communicationChannelAdministrationTokenLifetimeCurrent(
		claims.issuedAt, claims.expiresAt, observedAt,
	); err != nil {
		return communicationChannelGrantAdministrationNavigationClaims{}, err
	}
	return claims, nil
}

func validateCommunicationChannelGrantAdministrationNavigationClaims(
	claims communicationChannelGrantAdministrationNavigationClaims,
) error {
	if !validCommunicationCursorTokenTenantID(claims.tenantID) ||
		!validCommunicationCursorTokenID(claims.workspaceID) || claims.reader.Validate() != nil ||
		!validCommunicationCursorTokenID(claims.channelID) ||
		claims.channelVersion < 1 || claims.aclRevision < 1 ||
		len(claims.filterHash) != sha256.Size {
		return communicationCursorTokenInvalid("grant navigation scope is invalid")
	}
	if !validCommunicationCursorTokenID(claims.lastGrantID) {
		return communicationCursorTokenInvalid("grant navigation anchor is invalid")
	}
	if claims.issuedAt.IsZero() || claims.expiresAt.Sub(claims.issuedAt) != communicationCursorTokenTTL {
		return communicationCursorTokenInvalid("grant navigation lifetime is invalid")
	}
	return nil
}

// assembleCommunicationChannelAdministrationToken renders the compact form both
// administrative families share. The MAC binds the family domain, so a segment
// lifted from one family never verifies under the other.
func assembleCommunicationChannelAdministrationToken(
	r *communicationCursorTokenKeyring,
	prefix, domain string,
	canonical []byte,
) (string, error) {
	mac := communicationChannelAdministrationMAC(r.keys[r.signingKID], domain, r.signingKID, canonical)
	token := prefix + "." +
		base64.RawURLEncoding.EncodeToString([]byte(r.signingKID)) + "." +
		base64.RawURLEncoding.EncodeToString(canonical) + "." +
		base64.RawURLEncoding.EncodeToString(mac[:])
	if len(token) > communicationCursorTokenMaxBytes {
		return "", communicationCursorTokenInvalid("administration navigation token exceeds the compact bound")
	}
	return token, nil
}

// openCommunicationChannelAdministrationToken checks family, key identifier and
// MAC before any claim is parsed, and returns the authenticated claims bytes.
func openCommunicationChannelAdministrationToken(
	r *communicationCursorTokenKeyring,
	token, prefix, domain string,
) ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != prefix {
		return nil, communicationCursorTokenInvalid(
			"administration navigation token syntax or family is invalid",
		)
	}
	kidRaw, err := decodeCanonicalCommunicationCursorTokenSegment(
		parts[1], communicationCursorTokenMaxKIDBytes,
	)
	if err != nil || !validCommunicationCursorTokenKID(string(kidRaw)) {
		return nil, communicationCursorTokenInvalid(
			"administration navigation key identifier is invalid",
		)
	}
	kid := string(kidRaw)
	key, ok := r.keys[kid]
	if !ok {
		return nil, communicationCursorTokenInvalid(
			"administration navigation key identifier is unknown",
		)
	}
	raw, err := decodeCanonicalCommunicationCursorTokenSegment(parts[2], communicationCursorTokenMaxJSON)
	if err != nil {
		return nil, err
	}
	presented, err := decodeCanonicalCommunicationCursorTokenSegment(parts[3], sha256.Size)
	if err != nil {
		return nil, err
	}
	want := communicationChannelAdministrationMAC(key, domain, kid, raw)
	if !hmac.Equal(presented, want[:]) {
		return nil, communicationCursorTokenInvalid(
			"administration navigation authentication failed",
		)
	}
	return raw, nil
}

// decodeCanonicalCommunicationChannelAdministrationClaims refuses unknown
// fields, trailing data and any encoding that is not the exact canonical
// marshalling of the wire struct.
func decodeCanonicalCommunicationChannelAdministrationClaims(raw []byte, wire any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(wire); err != nil {
		return communicationCursorTokenInvalid("administration navigation claims JSON is invalid")
	}
	if err := decoder.Decode(new(struct{})); err != io.EOF {
		return communicationCursorTokenInvalid("administration navigation claims JSON has trailing data")
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(canonical, raw) {
		return communicationCursorTokenInvalid("administration navigation claims JSON is not canonical")
	}
	return nil
}

// communicationChannelAdministrationTokenLifetimeCurrent judges the token
// against DATABASE time, with the keyring's existing skew allowance.
func communicationChannelAdministrationTokenLifetimeCurrent(
	issuedAt, expiresAt, observedAt time.Time,
) error {
	if observedAt.IsZero() {
		return communicationCursorTokenInvalid("database observation time is missing")
	}
	now := observedAt.Unix()
	skew := int64(communicationCursorTokenClockSkew / time.Second)
	if now+skew < issuedAt.Unix() {
		return communicationCursorTokenInvalid("administration navigation token is not yet valid")
	}
	if now-skew >= expiresAt.Unix() {
		return errCommunicationCursorTokenExpired
	}
	return nil
}

func communicationChannelAdministrationMAC(
	key []byte,
	domain, kid string,
	claims []byte,
) [sha256.Size]byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte(kid))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(claims)
	var out [sha256.Size]byte
	copy(out[:], mac.Sum(nil))
	return out
}
