// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	jose "github.com/go-jose/go-jose/v4"
)

// Agent Card signature verification (A2A v1.0, spec §8.4 — the procedure v1.0
// FORMALIZED; in v0.3.0 the signatures field existed with no normative procedure).
// A card is signed by JWS (RFC 7515, detached payload per Appendix F: the
// AgentCardSignature carries protected + signature, no payload member) over the
// JCS-canonical form (RFC 8785) of the card with (§8.4.1): proto-default-valued
// implicit-presence properties REMOVED (§5.7 field-presence rules, cardnorm.go) and
// the `signatures` field EXCLUDED. Verification (§8.4.3) reconstructs that payload
// from the received card and checks each signature.
//
// Interop note: the default-removal step is new in v1.0. The verifier tries the
// normalized payload first (the normative §8.4.3 procedure) and falls back to the
// literal payload (no default-removal — what pre-formalization signers, including
// Code, signed). Both transforms bind the same card content (a default-
// valued implicit-presence field is indistinguishable from an absent one in
// proto3), so accepting either never weakens what the signature attests.
//
// TRUST MODEL (honest, never silently upgrading trust — docs/SECURITY-HARDENING.md anti-evasion):
//   - trustVerified   — a signature verified against an OPERATOR-CONFIGURED trust
//     anchor (out-of-band JWKS). This is identity established by the operator → an
//     edge to this agent is `attributed`.
//   - trustSelfAsserted — a signature verified only against a key fetched from the
//     card's OWN jku. That proves integrity, NOT identity (the card vouches for
//     itself), so it is lower trust → `approximate`. Off by default (allow_jku_fetch).
//   - trustUnsigned   — no signatures present → UNTRUSTED.
//   - trustUnverified — signatures present but none verified (bad signature, missing
//     key, unsupported alg, or non-canonicalizable card) → UNTRUSTED, flagged.

// trustLevel is the verification outcome for an Agent Card.
type trustLevel string

const (
	trustVerified     trustLevel = "verified"      // against configured trust anchor
	trustSelfAsserted trustLevel = "self_asserted" // against the card's own jku only
	trustUnsigned     trustLevel = "unsigned"
	trustUnverified   trustLevel = "unverified"
)

// trusted reports whether the level establishes agent identity (configured trust).
// Only trustVerified makes an A2A edge `attributed`; everything else is approximate.
func (t trustLevel) trusted() bool { return t == trustVerified }

// a2aAllowedAlgs pins the asymmetric signature algorithms accepted at parse time —
// the defense against an algorithm-confusion downgrade (HMAC/"none" rejected by
// omission). EdDSA is included: A2A cards commonly sign with Ed25519 or ES256.
var a2aAllowedAlgs = []jose.SignatureAlgorithm{
	jose.EdDSA,
	jose.ES256, jose.ES384, jose.ES512,
	jose.RS256, jose.RS384, jose.RS512,
	jose.PS256, jose.PS384, jose.PS512,
}

// jkuResolver fetches a JWK Set from a card-supplied jku URL. It is nil unless
// allow_jku_fetch is enabled (the self-asserted, lower-trust path).
type jkuResolver func(ctx context.Context, jkuURL string) (*jose.JSONWebKeySet, error)

// verifyCard returns the trust level of a card. trustAnchor is the operator's
// out-of-band JWKS (may be nil/empty); resolveJKU is the (optional) self-asserted
// fallback. The returned detail is a short, non-sensitive reason for the finding.
func verifyCard(ctx context.Context, rc rawCard, trustAnchor *jose.JSONWebKeySet, resolveJKU jkuResolver) (trustLevel, string) {
	if len(rc.card.Signatures) == 0 {
		return trustUnsigned, "agent card carries no signatures"
	}
	payloads, err := canonicalPayloads(rc.raw)
	if err != nil {
		return trustUnverified, "agent card could not be canonicalized for verification"
	}

	sawJKU := false
	for _, payload := range payloads {
		for _, sig := range rc.card.Signatures {
			jws, err := reconstructJWS(sig, payload)
			if err != nil {
				continue
			}
			// 1) configured trust anchor (identity established out-of-band).
			if trustAnchor != nil && verifyAgainstSet(jws, trustAnchor) {
				return trustVerified, "signature verified against configured trust anchor"
			}
			// 2) self-asserted jku (integrity only), when explicitly allowed.
			if jku := jkuOf(jws); jku != "" {
				sawJKU = true
				if resolveJKU != nil {
					if set, ferr := resolveJKU(ctx, jku); ferr == nil && set != nil && verifyAgainstSet(jws, set) {
						return trustSelfAsserted, "signature verified only against the card's own jku (self-asserted)"
					}
				}
			}
		}
	}
	if sawJKU && resolveJKU == nil {
		return trustUnverified, "agent card signed with a self-asserted jku (jku-fetch disabled); identity not established"
	}
	return trustUnverified, "agent card signature(s) did not verify against the configured trust anchor"
}

// canonicalPayloads computes the candidate JCS-canonical signing payloads of the
// card with the `signatures` field removed: the v1.0 §8.4.3 form (proto-default
// properties removed, cardnorm.go) first, then the literal form (no default
// removal) for signers predating the v1.0 formalization. Identical forms are
// deduplicated. The caller's map is never mutated.
func canonicalPayloads(raw map[string]any) ([][]byte, error) {
	cp := make(map[string]any, len(raw))
	for k, v := range raw {
		if k == "signatures" {
			continue
		}
		cp[k] = v
	}
	normalized, err := jcsCanonical(normalizeCard(cp))
	if err != nil {
		return nil, err
	}
	literal, err := jcsCanonical(cp)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(normalized, literal) {
		return [][]byte{normalized}, nil
	}
	return [][]byte{normalized, literal}, nil
}

// reconstructJWS rebuilds a verifiable JWS from a detached AgentCardSignature and
// the externally-computed payload, as a flattened JWS JSON serialization go-jose
// can parse.
func reconstructJWS(sig AgentCardSignature, payload []byte) (*jose.JSONWebSignature, error) {
	if sig.Protected == "" || sig.Signature == "" {
		return nil, fmt.Errorf("a2a: signature missing protected/signature")
	}
	flat := fmt.Sprintf(`{"protected":%q,"payload":%q,"signature":%q}`,
		sig.Protected, base64.RawURLEncoding.EncodeToString(payload), sig.Signature)
	return jose.ParseSigned(flat, a2aAllowedAlgs)
}

// verifyAgainstSet reports whether jws verifies against any key in set. It tries
// the key named by the signature's kid first, then every key (a set may omit kids).
func verifyAgainstSet(jws *jose.JSONWebSignature, set *jose.JSONWebKeySet) bool {
	kid := ""
	if len(jws.Signatures) > 0 {
		kid = jws.Signatures[0].Header.KeyID
	}
	if kid != "" {
		for _, k := range set.Key(kid) {
			if _, err := jws.Verify(k); err == nil {
				return true
			}
		}
	}
	for i := range set.Keys {
		if _, err := jws.Verify(&set.Keys[i]); err == nil {
			return true
		}
	}
	return false
}

// jkuOf returns the jku (JWK Set URL) from a signature's protected header, if any.
func jkuOf(jws *jose.JSONWebSignature) string {
	if len(jws.Signatures) == 0 {
		return ""
	}
	if v, ok := jws.Signatures[0].Header.ExtraHeaders[jose.HeaderKey("jku")]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// parseJWKS parses an operator-supplied JWK Set (the trust anchor). Empty/nil
// yields a nil set (no configured trust).
func parseJWKS(raw []byte) (*jose.JSONWebKeySet, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("a2a: parse trust_jwks: %w", err)
	}
	return &set, nil
}
