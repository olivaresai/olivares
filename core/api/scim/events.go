// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// This file adds the wire layer of an inbound SCIM Security Event Token (SET)
// receiver (RFC 9967, "SCIM Profile for Security Event Tokens", on top of the
// RFC 8417 SET / RFC 7519 JWT envelope). It is pure wire logic — event-URI
// taxonomy, the SET claim codec, and a compact-JWS *splitter* — and imports only
// the standard library, exactly like the rest of this package. The cryptographic
// signature verification and the credential-cutting action live in core/auth
// (Authenticator.SCIMReceiveEvent), never here.
//
// IDN-11: the SET receiver turns offboarding-on-next-poll into
// offboarding-on-event for agents/NHIs that may hold live credentials. The event
// taxonomy below was verified against the PUBLISHED RFC 9967 (IETF Standards
// Track) on 2026-06-06, recorded in the verification ledger. It is real text,
// not a draft, so the URIs are committed here (contrast the still-draft SCIM
// device model, which is deliberately NOT hard-coded — see scim.go).
package scim

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// SETContentType is the media type of a Security Event Token (RFC 8417 §2.3); a
// SET is a signed JWT, so it is delivered as application/secevent+jwt.
const SETContentType = "application/secevent+jwt"

// eventPrefix is the RFC 9967 SCIM event URI prefix.
const eventPrefix = "urn:ietf:params:scim:event"

// SCIM SET event type URIs (RFC 9967). Provisioning events carry either a "full"
// data payload or a "notice" attribute list; the two access-affecting events
// (delete, deactivate) and their re-enable counterpart (activate) are the ones a
// deprovisioning receiver acts on. Verified against the published RFC 9967.
const (
	EventProvCreateNotice = eventPrefix + ":prov:create:notice"
	EventProvCreateFull   = eventPrefix + ":prov:create:full"
	EventProvPatchNotice  = eventPrefix + ":prov:patch:notice"
	EventProvPatchFull    = eventPrefix + ":prov:patch:full"
	EventProvPutNotice    = eventPrefix + ":prov:put:notice"
	EventProvPutFull      = eventPrefix + ":prov:put:full"
	EventProvDelete       = eventPrefix + ":prov:delete"
	EventProvActivate     = eventPrefix + ":prov:activate"
	EventProvDeactivate   = eventPrefix + ":prov:deactivate"
	EventFeedAdd          = eventPrefix + ":feed:add"
	EventFeedRemove       = eventPrefix + ":feed:remove"
	EventMiscAsyncResp    = eventPrefix + ":misc:asyncresp"
)

// EventAction is the lifecycle effect a receiver derives from a SET event URI.
// Only the three access-affecting events map to an action; everything else is
// acknowledged and ignored (a SET receiver must not fail on events it chooses
// not to act on — RFC 8935 §2.4).
type EventAction string

const (
	// ActionDeprovision is the leaver path: remove the membership and cut
	// tenant-bound credentials (prov:delete).
	ActionDeprovision EventAction = "deprovision"
	// ActionDisable cuts access but preserves the record: revoke tokens+sessions
	// and mark inactive (prov:deactivate).
	ActionDisable EventAction = "disable"
	// ActionActivate re-enables a previously disabled member (prov:activate).
	ActionActivate EventAction = "activate"
	// ActionIgnore is acknowledged-but-not-acted-on (create/patch/put/feed/misc):
	// these are provisioning replication signals, not deprovisioning triggers.
	ActionIgnore EventAction = "ignore"
)

// ActionForEvent maps a SET event URI to the access action a deprovisioning
// receiver takes. Unknown or non-access URIs return ActionIgnore.
func ActionForEvent(uri string) EventAction {
	switch uri {
	case EventProvDelete:
		return ActionDeprovision
	case EventProvDeactivate:
		return ActionDisable
	case EventProvActivate:
		return ActionActivate
	default:
		return ActionIgnore
	}
}

// SubjectID is the SET subject identifier (RFC 9967 uses the "scim" subject
// identifier format from the Sub-Id draft/SSF): a relative SCIM resource URI plus
// the optional externalId/id. The receiver resolves it to a tenant member.
type SubjectID struct {
	// Format is the subject identifier format; RFC 9967 uses "scim".
	Format string `json:"format"`
	// URI is the SCIM resource path, e.g. "Users/2819c223-7f76-453a-919d-...".
	URI string `json:"uri"`
	// ExternalID is the IdP's stable id (optional, an alternative match key).
	ExternalID string `json:"externalId,omitempty"`
	// ID is the resource id (optional, kept for backwards compatibility).
	ID string `json:"id,omitempty"`
}

// ResourcePath splits SubjectID.URI ("Users/{id}") into its resource type and id.
// A bare id or empty URI yields ("", id) / ("", "").
func (s SubjectID) ResourcePath() (resourceType, id string) {
	u := strings.Trim(s.URI, "/")
	if u == "" {
		return "", ""
	}
	if i := strings.LastIndex(u, "/"); i >= 0 {
		return u[:i], u[i+1:]
	}
	return "", u
}

// Audience is the JWT aud claim, which RFC 7519 allows to be either a single
// string or an array of strings.
type Audience []string

// UnmarshalJSON accepts aud as a string or a []string.
func (a *Audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = Audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = Audience(many)
	return nil
}

// SecurityEventToken is the decoded SET claim set (RFC 8417 envelope + RFC 9967
// SCIM events). The signature is verified separately (core/auth); this is the
// already-decoded payload.
type SecurityEventToken struct {
	// Issuer is the SET issuer (iss) — the IdP/transmitter.
	Issuer string `json:"iss"`
	// Audience is the intended receiver(s) (aud).
	Audience Audience `json:"aud"`
	// IssuedAt is the issuance time (iat, epoch seconds).
	IssuedAt int64 `json:"iat"`
	// JTI is the unique SET id (jti) — the de-dup key.
	JTI string `json:"jti"`
	// SubjectID is the SCIM-format subject identifier (sub_id, RFC 9967).
	SubjectID *SubjectID `json:"sub_id"`
	// Events maps each event URI to its (full|notice) payload object.
	Events map[string]json.RawMessage `json:"events"`
	// TxnID groups SETs emitted from the same SCIM transaction (txn).
	TxnID string `json:"txn,omitempty"`
}

// EventURIs returns the event-URI keys carried by the SET, in no particular
// order (a SET typically carries exactly one).
func (s SecurityEventToken) EventURIs() []string {
	out := make([]string, 0, len(s.Events))
	for uri := range s.Events {
		out = append(out, uri)
	}
	return out
}

// JWSHeader is the protected header of a compact JWS (RFC 7515 §4).
type JWSHeader struct {
	// Alg is the signature algorithm; "none" is rejected by the verifier.
	Alg string `json:"alg"`
	// Kid is the optional key id used to select the verification key.
	Kid string `json:"kid,omitempty"`
	// Typ is the optional token type (e.g. "secevent+jwt").
	Typ string `json:"typ,omitempty"`
}

// ErrMalformedJWS means the input is not a three-part compact JWS.
var ErrMalformedJWS = errors.New("scim: malformed compact JWS (want header.payload.signature)")

// ParseCompactJWS splits a compact JWS serialization into its decoded protected
// header, decoded payload, the exact signing input (the ASCII "header.payload"
// substring the signature covers), and the decoded signature bytes. It does NOT
// verify the signature — that is the verifier's job in core/auth — and it does
// not decode the SET claims (see DecodeSET).
func ParseCompactJWS(token []byte) (hdr JWSHeader, payload, signingInput, signature []byte, err error) {
	s := strings.TrimSpace(string(token))
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return JWSHeader{}, nil, nil, nil, ErrMalformedJWS
	}
	rawHdr, err := b64.DecodeString(parts[0])
	if err != nil {
		return JWSHeader{}, nil, nil, nil, ErrMalformedJWS
	}
	if err := json.Unmarshal(rawHdr, &hdr); err != nil {
		return JWSHeader{}, nil, nil, nil, ErrMalformedJWS
	}
	if payload, err = b64.DecodeString(parts[1]); err != nil {
		return JWSHeader{}, nil, nil, nil, ErrMalformedJWS
	}
	if signature, err = b64.DecodeString(parts[2]); err != nil {
		return JWSHeader{}, nil, nil, nil, ErrMalformedJWS
	}
	signingInput = []byte(parts[0] + "." + parts[1])
	return hdr, payload, signingInput, signature, nil
}

// DecodeSET unmarshals the SET payload claims from a decoded JWS payload.
func DecodeSET(payload []byte) (SecurityEventToken, error) {
	var set SecurityEventToken
	if err := json.Unmarshal(payload, &set); err != nil {
		return SecurityEventToken{}, err
	}
	return set, nil
}

// b64 is the unpadded base64url alphabet JWS/JWT uses (RFC 7515 §2).
var b64 = base64.RawURLEncoding
