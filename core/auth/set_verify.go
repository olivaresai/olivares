// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"hash"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// SET verification shared by every SET receiver profile (SCIM, CAEP, RISC): the JWS
// algorithm allowlist, the signature check against a publisher's public keys and the
// issuer/audience/iat binding. The publisher registry (set_publisher.go) validates
// keys with the same parser. The receivers themselves are Business.

// Errors the SET verification surfaces. A receiver maps these to RFC 8935 delivery
// responses (a 400 with a machine-readable "err" code, or 202 on success).
var (
	// ErrSCIMSetSignature means the SET's JWS signature did not verify against any
	// configured publisher key (or used an unaccepted algorithm).
	ErrSCIMSetSignature = errors.New("auth: SCIM SET signature verification failed")
	// ErrSCIMSetIssuer means the SET issuer/audience did not match the configured
	// publisher.
	ErrSCIMSetIssuer = errors.New("auth: SCIM SET issuer or audience mismatch")
	// ErrSETClaims means the SET lacks what RFC 8417 requires of every SET (iat,
	// jti, the secevent+jwt typ) or carries an exp that has passed.
	ErrSETClaims = errors.New("auth: SET lacks a required claim or has expired")
)

// acceptedSetAlgs is the pinned JWS algorithm allowlist for SET verification.
// Only asymmetric signatures are accepted (a SET is signed by the publisher's
// private key and verified with its public key); "none" and the symmetric HS*
// family are rejected, matching the federation posture (never accept "none").
var acceptedSetAlgs = map[string]bool{
	"RS256": true, "RS384": true, "RS512": true,
	"PS256": true, "PS384": true, "PS512": true,
	"ES256": true, "ES384": true, "ES512": true,
	"EdDSA": true,
}

// esCurves binds each ES algorithm to the one curve RFC 7518 §3.4 names for it.
var esCurves = map[string]string{"ES256": "P-256", "ES384": "P-384", "ES512": "P-521"}

// checkIssuerAudience enforces the claims every SET carries (RFC 8417 §2.2-2.3: iat
// and jti, and a secevent+jwt typ so it is never taken for another kind of JWT), the
// configured issuer/audience binding, and the freshness of iat. It fails closed: a
// publisher that binds no issuer or no audience verifies nothing. exp is NOT
// RECOMMENDED for a SET (RFC 8417 §2.2), so it is optional, and honoured when
// present. It accepts the generic SETPublisher and SETEnvelope so it can be shared
// across SET receiver profiles (SCIM, CAEP, RISC).
func checkIssuerAudience(pub SETPublisher, env SETEnvelope, now model.Timestamp) error {
	if env.IssuedAt <= 0 || env.JTI == "" || strings.TrimPrefix(strings.ToLower(env.Typ), "application/") != "secevent+jwt" {
		return ErrSETClaims
	}
	if pub.Issuer == "" || env.Issuer != pub.Issuer {
		return ErrSCIMSetIssuer
	}
	if !slices.ContainsFunc(env.Audience, func(aud string) bool { return slices.Contains(pub.Audiences, aud) }) {
		return ErrSCIMSetIssuer
	}
	skew := pub.MaxClockSkewSeconds
	if skew <= 0 {
		skew = 120
	}
	if env.IssuedAt > now.Time().Add(time.Duration(skew)*time.Second).Unix() {
		return ErrSCIMSetIssuer
	}
	// Reject a stale SET (captured-and-replayed): bound how far in the past iat
	// may be. This caps the replay window even without jti de-dup.
	maxAge := pub.MaxAgeSeconds
	if maxAge <= 0 {
		maxAge = 3600
	}
	if env.IssuedAt < now.Time().Add(-time.Duration(maxAge)*time.Second).Unix() {
		return ErrSCIMSetIssuer
	}
	if env.ExpiresAt != 0 && env.ExpiresAt <= now.Time().Add(-time.Duration(skew)*time.Second).Unix() {
		return ErrSETClaims
	}
	return nil
}

// verifySETSignature verifies env's JWS signature against the configured keys.
// Key selection is STRICT on kid: when the SET header carries a kid, ONLY a key
// whose kid matches EXACTLY is eligible — a keyless configured key is never a
// fallback for a kid-bearing SET (otherwise a SET could name an arbitrary kid and
// be verified by an unrelated keyless key). When the SET carries no kid, every
// key whose alg matches is a candidate. The alg must be in the pinned allowlist.
// It accepts the generic SETPublisher and SETEnvelope so it can be shared across
// SET receiver profiles (SCIM, CAEP, RISC).
func verifySETSignature(pub SETPublisher, env SETEnvelope) error {
	if !acceptedSetAlgs[env.Alg] {
		return ErrSCIMSetSignature
	}
	for _, k := range pub.Keys {
		if env.Kid != "" && k.Kid != env.Kid {
			continue
		}
		if k.Alg != env.Alg {
			continue
		}
		p, err := parsePKIXPublic(k.PEM)
		if err != nil {
			continue
		}
		if jwsVerify(env.Alg, p, env.SigningInput, env.Signature) == nil {
			return nil
		}
	}
	return ErrSCIMSetSignature
}

// parsePKIXPublic decodes a PEM-encoded PKIX (SubjectPublicKeyInfo) public key.
func parsePKIXPublic(pemStr string) (crypto.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("auth: not a PEM public key")
	}
	return x509.ParsePKIXPublicKey(block.Bytes)
}

// jwsVerify verifies a compact-JWS signature over signingInput using pub and the
// JWS algorithm alg. It supports the asymmetric RS/PS/ES/EdDSA families; the
// caller has already checked alg is in the allowlist.
func jwsVerify(alg string, pub crypto.PublicKey, signingInput, sig []byte) error {
	switch alg {
	case "RS256", "RS384", "RS512":
		rsaPub, ok := pub.(*rsa.PublicKey)
		if !ok {
			return ErrSCIMSetSignature
		}
		h, hashed := digest(alg, signingInput)
		return rsa.VerifyPKCS1v15(rsaPub, h, hashed, sig)
	case "PS256", "PS384", "PS512":
		rsaPub, ok := pub.(*rsa.PublicKey)
		if !ok {
			return ErrSCIMSetSignature
		}
		h, hashed := digest(alg, signingInput)
		return rsa.VerifyPSS(rsaPub, h, hashed, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: h})
	case "ES256", "ES384", "ES512":
		ecPub, ok := pub.(*ecdsa.PublicKey)
		if !ok || ecPub.Curve.Params().Name != esCurves[alg] {
			return ErrSCIMSetSignature
		}
		// JWS ECDSA signatures are the fixed-width R||S concatenation (RFC 7518
		// §3.4), not ASN.1. Each half is the curve's byte size.
		n := (ecPub.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*n {
			return ErrSCIMSetSignature
		}
		r := new(big.Int).SetBytes(sig[:n])
		s := new(big.Int).SetBytes(sig[n:])
		_, hashed := digest(alg, signingInput)
		if ecdsa.Verify(ecPub, hashed, r, s) {
			return nil
		}
		return ErrSCIMSetSignature
	case "EdDSA":
		edPub, ok := pub.(ed25519.PublicKey)
		if !ok {
			return ErrSCIMSetSignature
		}
		if ed25519.Verify(edPub, signingInput, sig) {
			return nil
		}
		return ErrSCIMSetSignature
	default:
		return ErrSCIMSetSignature
	}
}

// digest returns the hash function an alg uses and the digest of signingInput
// (Ed25519 needs no pre-hash, so callers ignore the returned values for EdDSA).
func digest(alg string, signingInput []byte) (crypto.Hash, []byte) {
	var h hash.Hash
	var ch crypto.Hash
	switch alg[2:] {
	case "384":
		ch, h = crypto.SHA384, sha512.New384()
	case "512":
		ch, h = crypto.SHA512, sha512.New()
	default:
		ch, h = crypto.SHA256, sha256.New()
	}
	h.Write(signingInput)
	return ch, h.Sum(nil)
}
