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
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/olivaresai/olivares/core/model"
)

const (
	communicationCursorTokenPrefix  = "c2v1"
	communicationCursorTokenVersion = 1
	communicationCursorTokenDomain  = "olivares.sessions.inbox.cursor.c2.v1\x00"

	communicationCursorTokenTTL       = 5 * time.Minute
	communicationCursorTokenClockSkew = 30 * time.Second
	communicationCursorTokenReserve   = communicationCursorTokenTTL + communicationCursorTokenClockSkew

	communicationCursorTokenMinKeyBytes = sha256.Size
	communicationCursorTokenMaxKeyBytes = 4096
	communicationCursorTokenMaxKeys     = 32
	communicationCursorTokenMaxKIDBytes = 64
	communicationCursorTokenMaxJSON     = 1024
	communicationCursorTokenMaxBytes    = 2048

	communicationCursorTokenReaderKind  = RecipientUser
	communicationCursorTokenMailboxKind = MailboxPersonal
)

var (
	errCommunicationCursorTokenInvalid = errors.New(
		"sessions: invalid communication cursor token",
	)
	errCommunicationCursorTokenExpired = fmt.Errorf(
		"%w: expired",
		errCommunicationCursorTokenInvalid,
	)
	errCommunicationCursorTokenUnavailable = errors.New(
		"sessions: communication cursor token keyring unavailable",
	)
)

// communicationCursorTokenKey is configuration input only. The constructor
// copies every byte into an immutable keyring snapshot; callers may reuse or
// erase their input buffers without changing tokens minted by that snapshot.
type communicationCursorTokenKey struct {
	kid      string
	material []byte
}

// communicationCursorTokenKeyring has one current signing key and a bounded
// verification set. Rotation constructs a new snapshot that signs with the new
// KID while retaining old keys for at least the token TTL plus clock skew.
type communicationCursorTokenKeyring struct {
	signingKID string
	keys       map[string][]byte
}

// communicationCursorTokenClaims is navigation state, never authorization
// state. The service must re-observe all mutable authority before applying a
// cursor advance. All fields remain package-private until the C2 API boundary
// has its own deliberately narrower wire types.
type communicationCursorTokenClaims struct {
	tenantID         model.TenantID
	workspaceID      model.ID
	readerKind       RecipientKind
	readerRef        model.ID
	mailboxKind      MailboxKind
	mailboxRef       model.ID
	carrierClass     string
	filterHash       []byte
	cursorID         model.ID
	cursorVersion    int64
	baseDeliverySeq  int64
	afterDeliverySeq int64
	deliveryID       model.ID
	issuedAt         time.Time
	expiresAt        time.Time
}

// communicationCursorTokenWireClaims fixes both the accepted field vocabulary
// and the canonical JSON member order. Optional IDs are absent, never null or
// empty strings, in the only two states where the contract permits omission.
type communicationCursorTokenWireClaims struct {
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

// newCommunicationCursorTokenKeyring validates the complete rotation snapshot.
// A duplicate KID is ambiguous even when both byte slices happen to match, and
// a missing current KID keeps the feature unavailable rather than selecting an
// arbitrary verifier as signer.
func newCommunicationCursorTokenKeyring(
	signingKID string,
	configured []communicationCursorTokenKey,
) (*communicationCursorTokenKeyring, error) {
	if !validCommunicationCursorTokenKID(signingKID) ||
		len(configured) == 0 || len(configured) > communicationCursorTokenMaxKeys {
		return nil, communicationCursorTokenUnavailable("invalid keyring shape")
	}

	keys := make(map[string][]byte, len(configured))
	for _, configuredKey := range configured {
		if !validCommunicationCursorTokenKID(configuredKey.kid) ||
			len(configuredKey.material) < communicationCursorTokenMinKeyBytes ||
			len(configuredKey.material) > communicationCursorTokenMaxKeyBytes {
			return nil, communicationCursorTokenUnavailable("invalid key configuration")
		}
		if _, duplicate := keys[configuredKey.kid]; duplicate {
			return nil, communicationCursorTokenUnavailable("ambiguous key identifier")
		}
		keys[configuredKey.kid] = append([]byte(nil), configuredKey.material...)
	}
	if _, present := keys[signingKID]; !present {
		return nil, communicationCursorTokenUnavailable("current signing key is missing")
	}

	return &communicationCursorTokenKeyring{
		signingKID: signingKID,
		keys:       keys,
	}, nil
}

// mint emits the exact c2v1 compact form. observedAt must be the GET's database
// time; the helper deliberately accepts no independent expiry so every token
// has the one canonical five-minute lifetime.
func (r *communicationCursorTokenKeyring) mint(
	claims communicationCursorTokenClaims,
	observedAt time.Time,
) (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	key := r.keys[r.signingKID]
	if observedAt.IsZero() {
		return "", communicationCursorTokenInvalid("database observation time is missing")
	}
	issuedAtUnix := observedAt.Unix()
	if issuedAtUnix <= 0 ||
		issuedAtUnix > math.MaxInt64-int64(communicationCursorTokenReserve/time.Second) {
		return "", communicationCursorTokenInvalid("database observation time is out of range")
	}

	claims = claims.clone()
	claims.issuedAt = time.Unix(issuedAtUnix, 0).UTC()
	claims.expiresAt = time.Unix(
		issuedAtUnix+int64(communicationCursorTokenTTL/time.Second),
		0,
	).UTC()
	wire, err := communicationCursorTokenClaimsToWire(claims)
	if err != nil {
		return "", err
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return "", communicationCursorTokenInvalid("claims cannot be encoded")
	}
	if len(canonical) == 0 || len(canonical) > communicationCursorTokenMaxJSON {
		return "", communicationCursorTokenInvalid("claims exceed the canonical bound")
	}

	mac := communicationCursorTokenMAC(key, r.signingKID, canonical)
	encoder := base64.RawURLEncoding
	token := communicationCursorTokenPrefix + "." +
		encoder.EncodeToString([]byte(r.signingKID)) + "." +
		encoder.EncodeToString(canonical) + "." +
		encoder.EncodeToString(mac[:])
	if len(token) > communicationCursorTokenMaxBytes {
		return "", communicationCursorTokenInvalid("token exceeds the compact bound")
	}
	return token, nil
}

// verify authenticates a bounded token before parsing its JSON claims, then
// enforces the canonical representation and freshness under the supplied DB
// observation time. Exact committed receipt replay must happen before calling
// this method, because replay remains authoritative after navigation expiry.
func (r *communicationCursorTokenKeyring) verify(
	token string,
	observedAt time.Time,
) (communicationCursorTokenClaims, error) {
	if err := r.validate(); err != nil {
		return communicationCursorTokenClaims{}, err
	}
	if observedAt.IsZero() {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("database observation time is missing")
	}
	segments, err := splitCommunicationCursorToken(token)
	if err != nil {
		return communicationCursorTokenClaims{}, err
	}
	kidBytes, err := decodeCanonicalCommunicationCursorTokenSegment(
		segments.kid, communicationCursorTokenMaxKIDBytes,
	)
	if err != nil {
		return communicationCursorTokenClaims{}, err
	}
	kid := string(kidBytes)
	if !validCommunicationCursorTokenKID(kid) {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("key identifier is invalid")
	}
	claimsJSON, err := decodeCanonicalCommunicationCursorTokenSegment(
		segments.claims, communicationCursorTokenMaxJSON,
	)
	if err != nil || len(claimsJSON) == 0 {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("claims encoding is invalid")
	}
	if len(segments.mac) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("authentication code length is invalid")
	}
	presentedMAC, err := decodeCanonicalCommunicationCursorTokenSegment(segments.mac, sha256.Size)
	if err != nil || len(presentedMAC) != sha256.Size {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("authentication code is invalid")
	}

	key, present := r.keys[kid]
	if !present {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("key identifier is unknown")
	}
	if len(key) < communicationCursorTokenMinKeyBytes ||
		len(key) > communicationCursorTokenMaxKeyBytes {
		return communicationCursorTokenClaims{},
			communicationCursorTokenUnavailable("verification key is unavailable")
	}
	expectedMAC := communicationCursorTokenMAC(key, kid, claimsJSON)
	if !hmac.Equal(presentedMAC, expectedMAC[:]) {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("authentication failed")
	}

	claims, err := decodeCommunicationCursorTokenClaims(claimsJSON)
	if err != nil {
		return communicationCursorTokenClaims{}, err
	}
	nowUnix := observedAt.Unix()
	if nowUnix <= 0 {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("database observation time is out of range")
	}
	issuedAtUnix := claims.issuedAt.Unix()
	expiresAtUnix := claims.expiresAt.Unix()
	skewSeconds := int64(communicationCursorTokenClockSkew / time.Second)
	if issuedAtUnix > nowUnix && issuedAtUnix-nowUnix > skewSeconds {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("token is not yet valid")
	}
	// The upper bound is closed: at exp+skew the navigation token is dead.
	if nowUnix >= expiresAtUnix && nowUnix-expiresAtUnix >= skewSeconds {
		return communicationCursorTokenClaims{}, errCommunicationCursorTokenExpired
	}
	return claims.clone(), nil
}

type communicationCursorTokenSegments struct {
	kid    string
	claims string
	mac    string
}

// splitCommunicationCursorToken establishes the total allocation bound before
// any delimiter work. Three Cuts then prove the exact four-segment grammar
// without allocating a slice proportional to attacker-controlled dot count.
func splitCommunicationCursorToken(token string) (communicationCursorTokenSegments, error) {
	if !validCommunicationCursorTokenCompactBound(token) {
		return communicationCursorTokenSegments{},
			communicationCursorTokenInvalid("token length is invalid")
	}
	prefix, rest, ok := strings.Cut(token, ".")
	if !ok || prefix != communicationCursorTokenPrefix {
		return communicationCursorTokenSegments{},
			communicationCursorTokenInvalid("token syntax or version is invalid")
	}
	kid, rest, ok := strings.Cut(rest, ".")
	if !ok {
		return communicationCursorTokenSegments{},
			communicationCursorTokenInvalid("token syntax is invalid")
	}
	claims, mac, ok := strings.Cut(rest, ".")
	if !ok || strings.Contains(mac, ".") {
		return communicationCursorTokenSegments{},
			communicationCursorTokenInvalid("token syntax is invalid")
	}
	return communicationCursorTokenSegments{kid: kid, claims: claims, mac: mac}, nil
}

func validCommunicationCursorTokenCompactBound(token string) bool {
	return len(token) > 0 && len(token) <= communicationCursorTokenMaxBytes
}

func (r *communicationCursorTokenKeyring) validate() error {
	if r == nil || !validCommunicationCursorTokenKID(r.signingKID) ||
		len(r.keys) == 0 || len(r.keys) > communicationCursorTokenMaxKeys {
		return communicationCursorTokenUnavailable("keyring is not configured")
	}
	for kid, key := range r.keys {
		if !validCommunicationCursorTokenKID(kid) ||
			len(key) < communicationCursorTokenMinKeyBytes ||
			len(key) > communicationCursorTokenMaxKeyBytes {
			return communicationCursorTokenUnavailable("verification key is unavailable")
		}
	}
	if _, present := r.keys[r.signingKID]; !present {
		return communicationCursorTokenUnavailable("current signing key is unavailable")
	}
	return nil
}

func communicationCursorTokenClaimsToWire(
	claims communicationCursorTokenClaims,
) (communicationCursorTokenWireClaims, error) {
	if err := validateCommunicationCursorTokenClaims(claims); err != nil {
		return communicationCursorTokenWireClaims{}, err
	}
	wire := communicationCursorTokenWireClaims{
		Version:          communicationCursorTokenVersion,
		TenantID:         claims.tenantID.String(),
		WorkspaceID:      claims.workspaceID.String(),
		ReaderKind:       string(claims.readerKind),
		ReaderRef:        claims.readerRef.String(),
		MailboxKind:      string(claims.mailboxKind),
		MailboxRef:       claims.mailboxRef.String(),
		CarrierClass:     claims.carrierClass,
		FilterHash:       base64.RawURLEncoding.EncodeToString(claims.filterHash),
		CursorVersion:    claims.cursorVersion,
		BaseDeliverySeq:  claims.baseDeliverySeq,
		AfterDeliverySeq: claims.afterDeliverySeq,
		IssuedAtUnix:     claims.issuedAt.Unix(),
		ExpiresAtUnix:    claims.expiresAt.Unix(),
	}
	if claims.cursorVersion > 0 {
		wire.CursorID = claims.cursorID.String()
	}
	if claims.afterDeliverySeq > claims.baseDeliverySeq {
		wire.DeliveryID = claims.deliveryID.String()
	}
	return wire, nil
}

func decodeCommunicationCursorTokenClaims(
	raw []byte,
) (communicationCursorTokenClaims, error) {
	if len(raw) == 0 || len(raw) > communicationCursorTokenMaxJSON {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("claims length is invalid")
	}
	if err := validateCommunicationCursorTokenJSONKeys(raw); err != nil {
		return communicationCursorTokenClaims{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var wire communicationCursorTokenWireClaims
	if err := decoder.Decode(&wire); err != nil {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("claims JSON is invalid")
	}
	if err := decoder.Decode(new(struct{})); err != io.EOF {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("claims JSON has trailing data")
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(canonical, raw) {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("claims JSON is not canonical")
	}

	filterHash, err := decodeCanonicalCommunicationCursorTokenSegment(wire.FilterHash, sha256.Size)
	if err != nil || len(filterHash) != sha256.Size {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("filter hash is invalid")
	}
	claims := communicationCursorTokenClaims{
		tenantID:         model.TenantID(wire.TenantID),
		workspaceID:      model.ID(wire.WorkspaceID),
		readerKind:       RecipientKind(wire.ReaderKind),
		readerRef:        model.ID(wire.ReaderRef),
		mailboxKind:      MailboxKind(wire.MailboxKind),
		mailboxRef:       model.ID(wire.MailboxRef),
		carrierClass:     wire.CarrierClass,
		filterHash:       append([]byte(nil), filterHash...),
		cursorID:         model.ID(wire.CursorID),
		cursorVersion:    wire.CursorVersion,
		baseDeliverySeq:  wire.BaseDeliverySeq,
		afterDeliverySeq: wire.AfterDeliverySeq,
		deliveryID:       model.ID(wire.DeliveryID),
		issuedAt:         time.Unix(wire.IssuedAtUnix, 0).UTC(),
		expiresAt:        time.Unix(wire.ExpiresAtUnix, 0).UTC(),
	}
	if wire.Version != communicationCursorTokenVersion {
		return communicationCursorTokenClaims{},
			communicationCursorTokenInvalid("claims version is invalid")
	}
	if err := validateCommunicationCursorTokenClaims(claims); err != nil {
		return communicationCursorTokenClaims{}, err
	}
	return claims, nil
}

func validateCommunicationCursorTokenClaims(claims communicationCursorTokenClaims) error {
	directNoticeFilterHash, err := directNoticeCursorFilterHash()
	if err != nil {
		return communicationCursorTokenUnavailable("canonical filter hash is unavailable")
	}
	if !validCommunicationCursorTokenTenantID(claims.tenantID) ||
		!validCommunicationCursorTokenID(claims.workspaceID) ||
		claims.readerKind != communicationCursorTokenReaderKind ||
		!validCommunicationCursorTokenID(claims.readerRef) ||
		claims.mailboxKind != communicationCursorTokenMailboxKind ||
		claims.mailboxRef != claims.readerRef ||
		claims.carrierClass != string(CursorCarrierDirectNoticeV1) ||
		len(claims.filterHash) != sha256.Size ||
		!bytes.Equal(claims.filterHash, directNoticeFilterHash[:]) {
		return communicationCursorTokenInvalid("claims scope is invalid")
	}
	if claims.cursorVersion < 0 || claims.baseDeliverySeq < 0 ||
		claims.afterDeliverySeq < claims.baseDeliverySeq {
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

	issuedAtUnix := claims.issuedAt.Unix()
	expiresAtUnix := claims.expiresAt.Unix()
	if claims.issuedAt.IsZero() || claims.expiresAt.IsZero() || issuedAtUnix <= 0 ||
		expiresAtUnix <= issuedAtUnix ||
		expiresAtUnix-issuedAtUnix != int64(communicationCursorTokenTTL/time.Second) {
		return communicationCursorTokenInvalid("claims lifetime is invalid")
	}
	return nil
}

func validateCommunicationCursorTokenJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return communicationCursorTokenInvalid("claims must be one JSON object")
	}
	seen := make([]string, 0, 16)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return communicationCursorTokenInvalid("claims key is invalid")
		}
		key, ok := token.(string)
		if !ok || !validCommunicationCursorTokenJSONKey(key) {
			return communicationCursorTokenInvalid("claims key is unknown")
		}
		for _, prior := range seen {
			if strings.EqualFold(prior, key) {
				return communicationCursorTokenInvalid("claims key is duplicated")
			}
		}
		seen = append(seen, key)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return communicationCursorTokenInvalid("claims value is invalid")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return communicationCursorTokenInvalid("claims object is incomplete")
	}
	if err := decoder.Decode(new(struct{})); err != io.EOF {
		return communicationCursorTokenInvalid("claims JSON has trailing data")
	}
	return nil
}

func validCommunicationCursorTokenJSONKey(key string) bool {
	switch key {
	case "v", "ten", "ws", "rk", "rr", "mk", "mr", "cc", "fh", "cid",
		"cv", "base", "after", "did", "iat", "exp":
		return true
	default:
		return false
	}
}

func decodeCanonicalCommunicationCursorTokenSegment(
	segment string,
	maxDecoded int,
) ([]byte, error) {
	if segment == "" || maxDecoded < 1 || strings.Contains(segment, "=") ||
		len(segment) > base64.RawURLEncoding.EncodedLen(maxDecoded) {
		return nil, communicationCursorTokenInvalid("base64url segment is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil || len(decoded) == 0 || len(decoded) > maxDecoded ||
		base64.RawURLEncoding.EncodeToString(decoded) != segment {
		return nil, communicationCursorTokenInvalid("base64url segment is not canonical")
	}
	return decoded, nil
}

func communicationCursorTokenMAC(key []byte, kid string, claims []byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(communicationCursorTokenDomain))
	_, _ = mac.Write([]byte(kid))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(claims)
	var result [sha256.Size]byte
	copy(result[:], mac.Sum(nil))
	return result
}

func validCommunicationCursorTokenKID(kid string) bool {
	if len(kid) == 0 || len(kid) > communicationCursorTokenMaxKIDBytes {
		return false
	}
	for i := range len(kid) {
		c := kid[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func validCommunicationCursorTokenTenantID(tenant model.TenantID) bool {
	return tenant != model.SystemTenantID && validCommunicationCursorTokenUUIDv7(tenant.String())
}

func validCommunicationCursorTokenID(id model.ID) bool {
	return validCommunicationCursorTokenUUIDv7(id.String())
}

func validCommunicationCursorTokenUUIDv7(raw string) bool {
	parsed, err := uuid.Parse(raw)
	return err == nil && parsed != uuid.Nil && parsed.String() == raw &&
		parsed.Version() == uuid.Version(7) && parsed.Variant() == uuid.RFC4122
}

func (claims communicationCursorTokenClaims) clone() communicationCursorTokenClaims {
	claims.filterHash = append([]byte(nil), claims.filterHash...)
	return claims
}

func communicationCursorTokenInvalid(reason string) error {
	return fmt.Errorf("%w: %s", errCommunicationCursorTokenInvalid, reason)
}

func communicationCursorTokenUnavailable(reason string) error {
	return fmt.Errorf("%w: %s", errCommunicationCursorTokenUnavailable, reason)
}
