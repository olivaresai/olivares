// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"encoding/json"
	"fmt"
	"strings"

	jose "github.com/go-jose/go-jose/v4"
)

// Agent Card ISSUANCE (E5) — the signing counterpart of verify.go. Olivares can
// sign the Agent Card of an agent it governs so peers can establish its identity
// out-of-band, using the SAME machinery the verifier reconstructs: a DETACHED JWS
// (RFC 7515 Appendix F — protected header + signature, no inline payload) over the
// JCS-canonical (RFC 8785) form of the card with the v1.0 §8.4.3 transforms
// (proto-default implicit-presence properties removed, the `signatures` field excluded).
// A card produced here verifies with verifyCard against the corresponding public JWKS
// (round-trip proven in the tests).
//
// KEY CUSTODY (domain separation — the non-delegable operator decision): the
// card-issuance key is a DEDICATED asymmetric key that MUST be distinct from the license
// and OTA-update signing keys (and from the audit key). This connector never mints or
// holds a raw key of its own: the composition root wires a CardSigner built from the
// dedicated key (or an HSM/KMS-backed one). The signer seam is why a future key domain
// can move to a hardware boundary without touching this connector. NewJOSECardSigner
// rejects a symmetric or "none" algorithm and a public-only key, so a card can never be
// signed with a key that has no verifiable public half — the anti-algorithm-confusion
// posture on the verify side (a2aAllowedAlgs) is mirrored here at construction.

// CardSigner produces a detached JWS over the JCS-canonical Agent Card payload. An
// implementation custodies the dedicated card-issuance key; it returns the base64url
// protected header and signature that make up an AgentCardSignature. It is the seam the
// composition root binds to a go-jose signer (NewJOSECardSigner) or a KMS/HSM signer.
type CardSigner interface {
	// SignPayload signs the exact JCS-canonical payload bytes and returns the detached
	// JWS protected header and signature (both base64url, no inline payload).
	SignPayload(payload []byte) (protected, signature string, err error)
}

// joseCardSigner signs with an in-memory go-jose signer over a dedicated private JWK.
type joseCardSigner struct {
	signer jose.Signer
}

// NewJOSECardSigner builds a CardSigner from a DEDICATED asymmetric private JWK
// (Ed25519 / EC / RSA). It fails closed on: a public-only key (nothing to sign with), a
// symmetric or "none" algorithm (a card verifier needs a PUBLIC key — an HMAC-signed
// card would let anyone holding the shared secret forge identity), or an algorithm
// outside the verify-side allowlist. The key's kid rides the protected header so a
// verifier can select the matching public key from a JWKS.
func NewJOSECardSigner(key jose.JSONWebKey) (CardSigner, error) {
	if key.IsPublic() {
		return nil, fmt.Errorf("a2a: card signer requires a PRIVATE key")
	}
	alg := jose.SignatureAlgorithm(key.Algorithm)
	if !cardSignerAlgAllowed(alg) {
		return nil, fmt.Errorf("a2a: card signer requires an asymmetric signature algorithm, got %q", key.Algorithm)
	}
	opts := &jose.SignerOptions{}
	if key.KeyID != "" {
		opts = opts.WithHeader("kid", key.KeyID)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key.Key}, opts)
	if err != nil {
		return nil, fmt.Errorf("a2a: build card signer: %w", err)
	}
	return &joseCardSigner{signer: signer}, nil
}

// cardSignerAlgAllowed reports whether alg is one of the asymmetric algorithms the
// verifier accepts (a2aAllowedAlgs) — HS* and "none" are rejected by omission, exactly
// as on the verify side, so issuance can never produce a card the verifier would reject
// as an algorithm-confusion attempt.
func cardSignerAlgAllowed(alg jose.SignatureAlgorithm) bool {
	for _, a := range a2aAllowedAlgs {
		if a == alg {
			return true
		}
	}
	return false
}

// SignCardJSON returns the card JSON with one fresh detached JWS signature appended to
// its `signatures` array, over the §8.4.3 JCS-canonical payload (proto-default fields
// removed, ALL signatures excluded) — the exact payload verify.go reconstructs first.
//
// It signs the card's RAW bytes losslessly (decodeGeneric, UseNumber — the SAME
// representation verifyCard canonicalizes), NEVER a typed re-marshal: the AgentCard
// struct models only a subset of the A2A v1.0 schema, so signing a re-marshaled struct
// would silently DROP unmodeled fields (iconUrl, documentationUrl, defaultInput/Output
// Modes, per-skill tags/description, capability extensions, …) and attest a different,
// stripped card than the operator serves. Signing the raw bytes binds every field and
// re-emits every field. Pre-existing signatures are preserved (a card may carry several
// from different issuers). It fails closed on a non-object card, a canonicalization
// failure, or a signer error.
func SignCardJSON(cardJSON []byte, signer CardSigner) ([]byte, error) {
	if signer == nil {
		return nil, fmt.Errorf("a2a: nil card signer")
	}
	raw, err := decodeGeneric(cardJSON)
	if err != nil {
		return nil, fmt.Errorf("a2a: decode card for signing: %w", err)
	}
	payloads, err := canonicalPayloads(raw)
	if err != nil || len(payloads) == 0 {
		return nil, fmt.Errorf("a2a: card could not be canonicalized for signing: %w", err)
	}
	// Sign the NORMALIZED (§8.4.3) payload — canonicalPayloads returns it first — so the
	// signature binds the normative form the verifier checks before the legacy fallback.
	protected, signature, err := signer.SignPayload(payloads[0])
	if err != nil {
		return nil, fmt.Errorf("a2a: sign agent card: %w", err)
	}
	if protected == "" || signature == "" {
		return nil, fmt.Errorf("a2a: card signer returned an empty detached signature")
	}
	// Append the detached signature to the card's `signatures` array, preserving every
	// other field verbatim (json.Number keeps numeric literals exact on re-marshal).
	sigs, _ := raw["signatures"].([]any)
	raw["signatures"] = append(sigs, map[string]any{"protected": protected, "signature": signature})
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("a2a: re-marshal signed card: %w", err)
	}
	return out, nil
}

// SignPayload signs payload and returns the detached JWS protected header + signature.
// go-jose's DetachedCompactSerialize renders "<protected>..<signature>" (empty middle
// payload segment); we split it into the two AgentCardSignature fields.
func (s *joseCardSigner) SignPayload(payload []byte) (string, string, error) {
	jws, err := s.signer.Sign(payload)
	if err != nil {
		return "", "", fmt.Errorf("a2a: jose sign: %w", err)
	}
	detached, err := jws.DetachedCompactSerialize()
	if err != nil {
		return "", "", fmt.Errorf("a2a: detached serialize: %w", err)
	}
	parts := strings.Split(detached, ".")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("a2a: unexpected detached JWS serialization")
	}
	return parts[0], parts[2], nil
}
