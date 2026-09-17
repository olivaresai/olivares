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

// The incoming-handoff continuation ("h3n1") is navigation state and nothing
// else. It is minted only after a page's bound transaction committed and is
// anchored solely to the LAST OFFER ACTUALLY RETURNED to the recipient, so every
// claim it carries was already disclosed by the response it accompanies.
//
// It is a SEPARATE TOKEN DOMAIN from the direct-notice cursor ("c2n1", whose
// contract is a personal mailbox cursor over delivery sequences) and from the
// channel catalog ("c3n1"). The prefix, the MAC domain and the filter domain all
// differ, so a token minted for one family cannot resume another even under the
// same keyring: verification rejects the prefix before it looks at anything else,
// and the MAC would not match if it did.
//
// The token is base64-readable and MAC-authenticated, not encrypted: anyone
// holding it can decode its JSON segment and read tenant, workspace, recipient
// binding, state filter and anchor. The HMAC prevents forgery and cross-family
// splicing; it provides no confidentiality, which is why a hidden Handoff ID, a
// scanned candidate count or a raw store cursor must never be placed in the
// claims. The anchor pair (ack_deadline, handoff id) is the coordinate of a row
// the reader was just shown.
//
// A token conveys NO authority: every page re-resolves the current credential,
// the recipient identity, the grant closure and the exact per-Delivery core
// decisions before the anchor is even consulted.
const (
	communicationIncomingHandoffNavigationPrefix  = "h3n1"
	communicationIncomingHandoffNavigationVersion = 1
	communicationIncomingHandoffNavigationDomain  = "olivares.sessions.handoff.inbox.h3.v1\x00"
	// communicationIncomingHandoffCarrierClass names the carrier family this
	// navigation walks: a Handoff offer reached through its own exact Delivery.
	communicationIncomingHandoffCarrierClass = "handoff_delivery_v1"
	// communicationIncomingHandoffFilterDomain commits the listing shape. The
	// selected state is appended before hashing, so a token minted for one state
	// filter cannot resume a differently filtered listing.
	communicationIncomingHandoffFilterDomain = "olivares.sessions.handoff.inbox.filter.v1" +
		"|carrier=" + communicationIncomingHandoffCarrierClass +
		"|order=ack_deadline:asc,handoff_id:asc|state="
)

type communicationIncomingHandoffNavigationClaims struct {
	tenantID    model.TenantID
	workspaceID model.ID
	recipient   RecipientRef
	state       HandoffState
	filterHash  []byte
	// anchorDeadline is the canonical timestamp text of the last returned offer.
	// It is carried as text, not as a Unix second, because the keyset is exact:
	// ack_deadline is persisted as fixed-width canonical text on both engines and
	// a truncated anchor would silently skip or repeat rows sharing a second.
	anchorDeadline  string
	anchorHandoffID model.ID
	// sessionSID/sessionFence bind a session recipient's exact claim identity to
	// the token. A token minted under one claim generation cannot be presented
	// after a takeover, even by the same SID.
	sessionSID   string
	sessionFence int64
	issuedAt     time.Time
	expiresAt    time.Time
}

type communicationIncomingHandoffNavigationWireClaims struct {
	Version         int    `json:"v"`
	TenantID        string `json:"ten"`
	WorkspaceID     string `json:"ws"`
	RecipientKind   string `json:"rk"`
	RecipientRef    string `json:"rr"`
	CarrierClass    string `json:"cc"`
	State           string `json:"st"`
	FilterHash      string `json:"fh"`
	AnchorDeadline  string `json:"ad"`
	AnchorHandoffID string `json:"ah"`
	SessionSID      string `json:"sid,omitempty"`
	SessionFence    int64  `json:"fen,omitempty"`
	IssuedAtUnix    int64  `json:"iat"`
	ExpiresAtUnix   int64  `json:"exp"`
}

// incomingHandoffFilterHash commits the filter and ordering of one exact state
// selection. Every accepted state has its own hash, so the token cannot be
// replayed against another filter.
func incomingHandoffFilterHash(state HandoffState) ([sha256.Size]byte, error) {
	if !validIncomingHandoffStateFilter(state) {
		return [sha256.Size]byte{}, communicationCursorTokenInvalid(
			"incoming handoff filter state is invalid",
		)
	}
	return sha256.Sum256([]byte(communicationIncomingHandoffFilterDomain + string(state))), nil
}

func validIncomingHandoffStateFilter(state HandoffState) bool {
	switch state {
	case HandoffOffered, HandoffAccepted, HandoffRejected, HandoffWithdrawn, HandoffExpired:
		return true
	default:
		return false
	}
}

func (r *communicationCursorTokenKeyring) mintIncomingHandoffNavigation(
	claims communicationIncomingHandoffNavigationClaims,
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
	if err := validateCommunicationIncomingHandoffNavigationClaims(claims); err != nil {
		return "", err
	}
	wire := communicationIncomingHandoffNavigationWireClaims{
		Version:  communicationIncomingHandoffNavigationVersion,
		TenantID: claims.tenantID.String(), WorkspaceID: claims.workspaceID.String(),
		RecipientKind: string(claims.recipient.Kind), RecipientRef: claims.recipient.Ref,
		CarrierClass: communicationIncomingHandoffCarrierClass, State: string(claims.state),
		FilterHash:      base64.RawURLEncoding.EncodeToString(claims.filterHash),
		AnchorDeadline:  claims.anchorDeadline,
		AnchorHandoffID: claims.anchorHandoffID.String(),
		SessionSID:      claims.sessionSID, SessionFence: claims.sessionFence,
		IssuedAtUnix: claims.issuedAt.Unix(), ExpiresAtUnix: claims.expiresAt.Unix(),
	}
	canonical, err := json.Marshal(wire)
	if err != nil || len(canonical) > communicationCursorTokenMaxJSON {
		return "", communicationCursorTokenInvalid(
			"incoming handoff navigation claims cannot be encoded",
		)
	}
	mac := communicationIncomingHandoffNavigationMAC(r.keys[r.signingKID], r.signingKID, canonical)
	token := communicationIncomingHandoffNavigationPrefix + "." +
		base64.RawURLEncoding.EncodeToString([]byte(r.signingKID)) + "." +
		base64.RawURLEncoding.EncodeToString(canonical) + "." +
		base64.RawURLEncoding.EncodeToString(mac[:])
	if len(token) > communicationCursorTokenMaxBytes {
		return "", communicationCursorTokenInvalid(
			"incoming handoff navigation token exceeds the compact bound",
		)
	}
	return token, nil
}

func (r *communicationCursorTokenKeyring) verifyIncomingHandoffNavigation(
	token string,
	observedAt time.Time,
) (communicationIncomingHandoffNavigationClaims, error) {
	if err := r.validate(); err != nil {
		return communicationIncomingHandoffNavigationClaims{}, err
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != communicationIncomingHandoffNavigationPrefix {
		return communicationIncomingHandoffNavigationClaims{}, communicationCursorTokenInvalid(
			"incoming handoff navigation token syntax or family is invalid",
		)
	}
	kidRaw, err := decodeCanonicalCommunicationCursorTokenSegment(
		parts[1], communicationCursorTokenMaxKIDBytes,
	)
	if err != nil || !validCommunicationCursorTokenKID(string(kidRaw)) {
		return communicationIncomingHandoffNavigationClaims{}, communicationCursorTokenInvalid(
			"incoming handoff navigation key identifier is invalid",
		)
	}
	kid := string(kidRaw)
	key, ok := r.keys[kid]
	if !ok {
		return communicationIncomingHandoffNavigationClaims{}, communicationCursorTokenInvalid(
			"incoming handoff navigation key identifier is unknown",
		)
	}
	raw, err := decodeCanonicalCommunicationCursorTokenSegment(parts[2], communicationCursorTokenMaxJSON)
	if err != nil {
		return communicationIncomingHandoffNavigationClaims{}, err
	}
	presented, err := decodeCanonicalCommunicationCursorTokenSegment(parts[3], sha256.Size)
	if err != nil {
		return communicationIncomingHandoffNavigationClaims{}, err
	}
	want := communicationIncomingHandoffNavigationMAC(key, kid, raw)
	if !hmac.Equal(presented, want[:]) {
		return communicationIncomingHandoffNavigationClaims{}, communicationCursorTokenInvalid(
			"incoming handoff navigation authentication failed",
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var wire communicationIncomingHandoffNavigationWireClaims
	if err := decoder.Decode(&wire); err != nil {
		return communicationIncomingHandoffNavigationClaims{}, communicationCursorTokenInvalid(
			"incoming handoff navigation claims JSON is invalid",
		)
	}
	if err := decoder.Decode(new(struct{})); err != io.EOF {
		return communicationIncomingHandoffNavigationClaims{}, communicationCursorTokenInvalid(
			"incoming handoff navigation claims JSON has trailing data",
		)
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(canonical, raw) ||
		wire.Version != communicationIncomingHandoffNavigationVersion {
		return communicationIncomingHandoffNavigationClaims{}, communicationCursorTokenInvalid(
			"incoming handoff navigation claims JSON is not canonical",
		)
	}
	filterHash, err := decodeCanonicalCommunicationCursorTokenSegment(wire.FilterHash, sha256.Size)
	if err != nil {
		return communicationIncomingHandoffNavigationClaims{}, err
	}
	if wire.CarrierClass != communicationIncomingHandoffCarrierClass {
		return communicationIncomingHandoffNavigationClaims{}, communicationCursorTokenInvalid(
			"incoming handoff navigation carrier binding is invalid",
		)
	}
	claims := communicationIncomingHandoffNavigationClaims{
		tenantID: model.TenantID(wire.TenantID), workspaceID: model.ID(wire.WorkspaceID),
		recipient: RecipientRef{
			Kind: RecipientKind(wire.RecipientKind), Ref: wire.RecipientRef,
		},
		state: HandoffState(wire.State), filterHash: filterHash,
		anchorDeadline: wire.AnchorDeadline, anchorHandoffID: model.ID(wire.AnchorHandoffID),
		sessionSID: wire.SessionSID, sessionFence: wire.SessionFence,
		issuedAt:  time.Unix(wire.IssuedAtUnix, 0).UTC(),
		expiresAt: time.Unix(wire.ExpiresAtUnix, 0).UTC(),
	}
	if err := validateCommunicationIncomingHandoffNavigationClaims(claims); err != nil {
		return communicationIncomingHandoffNavigationClaims{}, err
	}
	if observedAt.IsZero() {
		return communicationIncomingHandoffNavigationClaims{}, communicationCursorTokenInvalid(
			"database observation time is missing",
		)
	}
	now := observedAt.Unix()
	skew := int64(communicationCursorTokenClockSkew / time.Second)
	if now+skew < claims.issuedAt.Unix() {
		return communicationIncomingHandoffNavigationClaims{}, communicationCursorTokenInvalid(
			"incoming handoff navigation token is not yet valid",
		)
	}
	if now-skew >= claims.expiresAt.Unix() {
		return communicationIncomingHandoffNavigationClaims{}, errCommunicationCursorTokenExpired
	}
	return claims, nil
}

func validateCommunicationIncomingHandoffNavigationClaims(
	claims communicationIncomingHandoffNavigationClaims,
) error {
	filter, err := incomingHandoffFilterHash(claims.state)
	if err != nil {
		return err
	}
	if !validCommunicationCursorTokenTenantID(claims.tenantID) ||
		!validCommunicationCursorTokenID(claims.workspaceID) ||
		claims.recipient.Validate() != nil || !bytes.Equal(claims.filterHash, filter[:]) {
		return communicationCursorTokenInvalid("incoming handoff navigation scope is invalid")
	}
	if !validCommunicationCursorTokenID(claims.anchorHandoffID) {
		return communicationCursorTokenInvalid("incoming handoff navigation anchor is invalid")
	}
	// The anchor must be canonical timestamp text and must round-trip exactly:
	// the keyset compares these bytes against the stored column, so text the
	// store could never have produced is refused before it reaches a query.
	parsed, err := model.ParseTimestamp(claims.anchorDeadline)
	if err != nil || parsed.String() != claims.anchorDeadline || parsed.IsZero() {
		return communicationCursorTokenInvalid("incoming handoff navigation anchor time is invalid")
	}
	// A session recipient's anchor carries its exact live claim generation; any
	// other recipient kind carries none. Neither may borrow the other's shape.
	if claims.recipient.Kind == RecipientSession {
		if claims.sessionSID != claims.recipient.Ref || claims.sessionFence < 1 {
			return communicationCursorTokenInvalid(
				"incoming handoff navigation session binding is invalid",
			)
		}
	} else if claims.sessionSID != "" || claims.sessionFence != 0 {
		return communicationCursorTokenInvalid(
			"incoming handoff navigation carries a foreign session binding",
		)
	}
	if claims.issuedAt.IsZero() ||
		claims.expiresAt.Sub(claims.issuedAt) != communicationCursorTokenTTL {
		return communicationCursorTokenInvalid("incoming handoff navigation lifetime is invalid")
	}
	return nil
}

func communicationIncomingHandoffNavigationMAC(
	key []byte,
	kid string,
	claims []byte,
) [sha256.Size]byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(communicationIncomingHandoffNavigationDomain))
	_, _ = mac.Write([]byte(kid))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(claims)
	var out [sha256.Size]byte
	copy(out[:], mac.Sum(nil))
	return out
}
