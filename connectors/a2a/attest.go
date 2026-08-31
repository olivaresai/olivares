// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

// attest.go is the OPTIONAL attestation-based admission signal for inbound agent
// runtimes, implementing the verifier side of OAuth 2.0 Attestation-Based
// Client Authentication, draft-ietf-oauth-attestation-based-client-auth-09
// (2026-05-26 — an IETF DRAFT, not an RFC: that is exactly why this is an opt-in
// POLICY seam, default off, never a hard-coded protocol requirement; the header and
// typ names below are the -09 values and may change before publication).
//
// The model: an agent runtime carries a Client Attestation JWT minted by a trusted
// ATTESTER (its platform/deployer) binding a Client Instance Key (cnf/jwk), plus a
// per-request Proof-of-Possession JWT signed with that instance key. For the A2A
// connector the inbound surface where a remote runtime presents itself is the push
// webhook, so the PushReceiver gates admission on the pair when the operator's
// policy demands it (RequireClientAttestation):
//
//	OAuth-Client-Attestation:     <Client Attestation JWT>   (typ oauth-client-attestation+jwt)
//	OAuth-Client-Attestation-PoP: <PoP JWT>                  (typ oauth-client-attestation-pop+jwt)
//
// DENY-CLOSED when enabled (each check from the draft's validation section):
// precisely ONE of each header; algs pinned asymmetric (the draft's "not none, ...
// acceptable per local policy"); the attestation verifies against the operator's
// ATTESTER trust anchor; the cnf key is a PUBLIC key; the PoP verifies with the cnf
// key; the PoP aud names THIS webhook; the PoP jti is replay-cached. Any failure →
// 401 and the push never reaches token verification. When disabled (default), the
// headers are ignored entirely — admission stays the operator's call, and absence
// of the policy never weakens the existing push JWT verification, which ALWAYS runs.

// ABCA header names (draft -09 §"Client Attestation HTTP Headers"; case-insensitive
// per RFC 9110 — http.Header canonicalizes).
const (
	attestationHeader    = "OAuth-Client-Attestation"
	attestationPoPHeader = "OAuth-Client-Attestation-PoP"
)

// ABCA JOSE typ values (draft -09: the typ header parameter MUST be these).
const (
	attestationTyp    = "oauth-client-attestation+jwt"
	attestationPoPTyp = "oauth-client-attestation-pop+jwt"
)

// attestVerifier verifies the ABCA pair for inbound runtime admission. anchor is
// the operator's trust anchor of Client Attester public keys (NOT the push-token
// issuer anchor — attesters vouch for runtimes, issuers sign notifications).
type attestVerifier struct {
	anchor   *jose.JSONWebKeySet
	audience string
	replay   *replayCache
}

// attClaims are the Client Attestation JWT claims the verifier reads: sub is the
// attested client/runtime id; cnf.jwk is the bound Client Instance Key (RFC 7800).
type attClaims struct {
	jwt.Claims
	Cnf struct {
		JWK json.RawMessage `json:"jwk"`
	} `json:"cnf"`
}

// verify checks the attestation pair from req fail-closed, returning the attested
// client id (the attestation's sub) on success. now is the receiver's clock
// (injectable in tests).
func (v *attestVerifier) verify(req *http.Request, now time.Time) (string, error) {
	// One header each, exactly (draft: "precisely one ... header field").
	att, err := exactlyOneHeader(req, attestationHeader)
	if err != nil {
		return "", err
	}
	pop, err := exactlyOneHeader(req, attestationPoPHeader)
	if err != nil {
		return "", err
	}

	// --- Client Attestation JWT: trusted attester vouches for the runtime. ---
	attTok, err := jwt.ParseSigned(att, a2aAllowedAlgs)
	if err != nil {
		return "", fmt.Errorf("a2a: client attestation parse: %w", err)
	}
	if err := requireTyp(attTok, attestationTyp); err != nil {
		return "", err
	}
	key, err := v.anchorKey(attTok)
	if err != nil {
		return "", err
	}
	var ac attClaims
	if err := attTok.Claims(key, &ac); err != nil {
		return "", fmt.Errorf("a2a: client attestation signature: %w", err)
	}
	if strings.TrimSpace(ac.Subject) == "" {
		return "", fmt.Errorf("a2a: client attestation has no sub (client id)")
	}
	// exp is REQUIRED by the draft (and go-jose does not demand it by itself).
	if ac.Expiry == nil {
		return "", fmt.Errorf("a2a: client attestation has no exp")
	}
	if err := ac.Validate(jwt.Expected{Time: now}); err != nil {
		return "", fmt.Errorf("a2a: client attestation claims: %w", err)
	}
	if len(ac.Cnf.JWK) == 0 {
		return "", fmt.Errorf("a2a: client attestation carries no cnf key")
	}
	var instanceKey jose.JSONWebKey
	if err := json.Unmarshal(ac.Cnf.JWK, &instanceKey); err != nil {
		return "", fmt.Errorf("a2a: client attestation cnf key: %w", err)
	}
	// The draft requires the cnf key NOT to be a private key (a leaked/embedded
	// private key would let anyone mint PoPs).
	if !instanceKey.Valid() || !instanceKey.IsPublic() {
		return "", fmt.Errorf("a2a: client attestation cnf key is not a valid public key")
	}

	// --- PoP JWT: the runtime proves possession of the attested instance key. ---
	popTok, err := jwt.ParseSigned(pop, a2aAllowedAlgs)
	if err != nil {
		return "", fmt.Errorf("a2a: attestation pop parse: %w", err)
	}
	if err := requireTyp(popTok, attestationPoPTyp); err != nil {
		return "", err
	}
	var pc jwt.Claims
	if err := popTok.Claims(&instanceKey, &pc); err != nil {
		return "", fmt.Errorf("a2a: attestation pop signature (not the attested instance key): %w", err)
	}
	// iat is REQUIRED by the draft; aud MUST name this receiver; freshness via the
	// standard window checks.
	if pc.IssuedAt == nil {
		return "", fmt.Errorf("a2a: attestation pop has no iat")
	}
	if err := pc.Validate(jwt.Expected{AnyAudience: jwt.Audience{v.audience}, Time: now}); err != nil {
		return "", fmt.Errorf("a2a: attestation pop claims: %w", err)
	}
	// jti is REQUIRED (replay detection): a re-presented PoP is rejected.
	if pc.ID == "" || !v.replay.admit("abca:"+pc.ID, timeOfDate(pc.Expiry)) {
		return "", fmt.Errorf("a2a: attestation pop replayed or missing jti")
	}
	return ac.Subject, nil
}

// anchorKey resolves the attester verification key for the token's kid from the
// operator anchor (kid-first, single-key fallback — mirrors lookupAnchorKey).
func (v *attestVerifier) anchorKey(tok *jwt.JSONWebToken) (*jose.JSONWebKey, error) {
	if len(tok.Headers) == 0 {
		return nil, fmt.Errorf("a2a: client attestation has no JOSE header")
	}
	if k := lookupAnchorKey(v.anchor, tok.Headers[0].KeyID); k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("a2a: no attester trust key for kid %q", tok.Headers[0].KeyID)
}

// requireTyp enforces the draft's mandatory typ header parameter on a parsed JWT.
func requireTyp(tok *jwt.JSONWebToken, want string) error {
	if len(tok.Headers) == 0 {
		return fmt.Errorf("a2a: attestation token has no JOSE header")
	}
	got, _ := tok.Headers[0].ExtraHeaders[jose.HeaderType].(string)
	if !strings.EqualFold(strings.TrimSpace(got), want) {
		return fmt.Errorf("a2a: attestation token typ %q, want %q", got, want)
	}
	return nil
}

// exactlyOneHeader returns the single value of name, erroring when absent or
// repeated (the draft requires precisely one of each attestation header — a
// duplicate is an ambiguity an attacker could smuggle through).
func exactlyOneHeader(req *http.Request, name string) (string, error) {
	vals := req.Header.Values(name)
	if len(vals) != 1 || strings.TrimSpace(vals[0]) == "" {
		return "", fmt.Errorf("a2a: want exactly one %s header, got %d", name, len(vals))
	}
	return strings.TrimSpace(vals[0]), nil
}
