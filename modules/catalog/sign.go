// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// canonEntry is the canonical, signable identity of a catalog entry version. The
// field order is fixed and the spec's map keys are sorted by encoding/json, so the
// serialization — and therefore the content hash — is deterministic and identical
// at approve time and at verify time (both operate on the JSON-normalized spec).
// Every operator-authored, persisted field is covered (including the display
// name), so a later change to ANY of them breaks the hash (docs/SECURITY-HARDENING.md).
type canonEntry struct {
	Kind    string         `json:"kind"`
	Name    string         `json:"name"`
	Slug    string         `json:"slug"`
	Version string         `json:"version"`
	Summary string         `json:"summary"`
	Owner   string         `json:"owner"`
	Spec    map[string]any `json:"spec"`
}

// contentHash computes the SHA-256 of an entry's canonical preimage. It is the
// integrity pin: any later change to a pinned field changes the hash, so tampering
// is detectable even without a signature.
func contentHash(name, kind, slug, version, summary, owner string, spec map[string]any) ([]byte, error) {
	if spec == nil {
		spec = map[string]any{}
	}
	b, err := json.Marshal(canonEntry{Kind: kind, Name: name, Slug: slug, Version: version, Summary: summary, Owner: owner, Spec: spec})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}

// sign produces a detached Ed25519 signature over the 32-byte content hash, plus
// the public key (the self-contained verifier identity carried with the artifact —
// not a secret) and a short fingerprint for display.
func sign(priv ed25519.PrivateKey, hash []byte) (sigB64, pubB64, fingerprint string) {
	sig := ed25519.Sign(priv, hash)
	pub := priv.Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(sig),
		base64.StdEncoding.EncodeToString(pub),
		keyFingerprint(pub)
}

// keyFingerprint returns a short, stable display fingerprint of a public key.
func keyFingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}

// hexEncode renders bytes as a lowercase hex string.
func hexEncode(b []byte) string { return hex.EncodeToString(b) }

// verifyResult is the outcome of verifying an approved entry's integrity.
type verifyResult struct {
	HashOK      bool   // recomputed content hash matches the stored pin
	Signed      bool   // a signature is present
	SignatureOK bool   // the signature is a valid Ed25519 sig over the stored hash
	Verified    bool   // hash matches AND (unsigned OR signature valid)
	SignedByFP  string // signer key fingerprint (display), when signed
	StoredHash  string // the stored content hash (hex)
	Recomputed  string // the freshly recomputed content hash (hex)
	Reason      string // human-readable explanation
}

// verify recomputes the content hash from the entry's current fields, compares it
// to the stored pin, and (when present) verifies the detached signature over the
// stored hash using the public key carried with the entry.
//
// expectedFP is the fingerprint of THIS node's configured catalog signing key, or
// "" when no key is configured. When a key IS configured it is the trust anchor:
// an approved entry MUST carry a signature made by that key, so a stripped
// signature (downgrade) or a signature under any other key (substitution) is
// reported as NOT verified — closing the store-attacker paths the unkeyed hash
// alone cannot. When no key is configured, integrity rests on the content-hash pin
// plus the tamper-evident ledger (out of band); a carried signature is verified
// for information but cannot be anchored here.
func verify(storedHashHex, sigB64, pubB64, expectedFP, name, kind, slug, version, summary, owner string, spec map[string]any) verifyResult {
	res := verifyResult{StoredHash: storedHashHex, Signed: sigB64 != ""}
	recomputed, err := contentHash(name, kind, slug, version, summary, owner, spec)
	if err != nil {
		res.Reason = "could not recompute content hash"
		return res
	}
	res.Recomputed = hex.EncodeToString(recomputed)
	res.HashOK = storedHashHex != "" && storedHashHex == res.Recomputed

	if res.Signed {
		storedHashBytes, e1 := hex.DecodeString(storedHashHex)
		pub, e2 := base64.StdEncoding.DecodeString(pubB64)
		sig, e3 := base64.StdEncoding.DecodeString(sigB64)
		if e1 == nil && e2 == nil && e3 == nil && len(pub) == ed25519.PublicKeySize {
			res.SignatureOK = ed25519.Verify(ed25519.PublicKey(pub), storedHashBytes, sig)
			res.SignedByFP = keyFingerprint(ed25519.PublicKey(pub))
		}
	}

	// Pinned posture: a catalog key is configured at this node, so the signature is
	// the trust anchor and must be present, from that key, over the matching hash.
	if expectedFP != "" {
		switch {
		case !res.Signed:
			res.Reason = "no signature, but a catalog signing key is configured (possible downgrade)"
		case res.SignedByFP != expectedFP:
			res.Reason = "signed by a key other than the configured catalog key (got " + res.SignedByFP + ", expected " + expectedFP + ")"
		case !res.HashOK:
			res.Reason = "content hash does not match the stored pin (the entry was altered after approval)"
		case !res.SignatureOK:
			res.Reason = "signature does not verify against the configured catalog key"
		default:
			res.Verified = true
			res.Reason = "content hash matches and the signature is from the configured catalog key"
		}
		return res
	}

	// Unpinned posture: no catalog key at this node.
	if !res.Signed {
		res.Verified = res.HashOK
		if res.HashOK {
			res.Reason = "hash-pinned and ledger-attested (unsigned: no catalog signing key configured)"
		} else {
			res.Reason = "content hash does not match the stored pin"
		}
		return res
	}
	res.Verified = res.HashOK && res.SignatureOK
	switch {
	case res.Verified:
		res.Reason = "content hash matches and the carried Ed25519 signature is valid (signer not pinned: no catalog key at this node)"
	case !res.HashOK:
		res.Reason = "content hash does not match the stored pin (the entry was altered after approval)"
	default:
		res.Reason = "signature does not verify against the carried public key"
	}
	return res
}

// containsInlineCredential is a defensive heuristic that rejects the obvious ways a
// credential could end up persisted in a catalog spec: basic-auth userinfo in a
// URL, or a key/value that assigns a secret-like key. It is a guardrail enforcing
// minimal-data (docs/SECURITY-HARDENING.md), not a secret scanner; it never stores the match.
func containsInlineCredential(s string) bool {
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	if i := strings.Index(low, "://"); i >= 0 {
		rest := low[i+3:]
		if at := strings.IndexByte(rest, '@'); at >= 0 {
			if strings.IndexByte(rest[:at], ':') >= 0 {
				return true
			}
		}
	}
	for _, kw := range []string{"\"token\":", "\"secret\":", "\"password\":", "\"api_key\":", "\"apikey\":", "\"access_key\":", "\"client_secret\":", "token=", "secret=", "password=", "api_key=", "access_key=", "client_secret="} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}
