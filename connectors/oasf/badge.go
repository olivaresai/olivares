// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package oasf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// The honest four-state badge verification status a record's "badge" attribute
// carries (see the package doc for why only JOSE can ever reach "verified").
const (
	badgeNone       = "none"
	badgeVerified   = "verified"
	badgeUnverified = "unverified"
	badgeInvalid    = "invalid"
)

// statusRank orders the statuses for per-record aggregation when several badges
// reference the same record: any verified badge wins; otherwise a tampered/
// malformed badge ("invalid") outranks a merely-unverifiable EMBEDDED_PROOF one
// ("unverified"), so the stronger negative signal surfaces.
var statusRank = map[string]int{
	badgeNone:       0,
	badgeUnverified: 1,
	badgeInvalid:    2,
	badgeVerified:   3,
}

// The two OASF schema hosts tolerated in a badge's credentialSchema (verified
// 2026-06-11): the live Linux Foundation host, and the dead agntcy.org host the
// AGNTCY identity spec itself still references.
const (
	schemaHostCurrent = "schema.oasf.outshift.com"
	schemaHostLegacy  = "schema.oasf.agntcy.org"
)

// badgeAllowedAlgs is the asymmetric signature allowlist for a JOSE-enveloped
// badge, pinned at parse time exactly like the SPIFFE JWT-SVID verifier
// (connectors/spiffe/svid.go): HMAC (HS*) and "none" are rejected by omission —
// the defense against an algorithm-confusion downgrade.
var badgeAllowedAlgs = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.ES256, jose.ES384, jose.ES512,
	jose.PS256, jose.PS384, jose.PS512,
}

// badgeOutcome is one parsed badge's verification result: the record reference
// it asserts ("oasf:<name>@<version>" when the embedded record parsed, else a
// badge-derived file ref) and its status (verified/unverified/invalid; never
// "none", which is the absence of any badge).
type badgeOutcome struct {
	ref    string
	status string
}

// envelope is the AGNTCY enveloped-credential wrapper (identity spec v1alpha1).
type envelope struct {
	EnvelopeType string          `json:"envelope_type"`
	Value        json.RawMessage `json:"value"`
}

// verifiableCredential is the slice of an Agent Badge VC this connector reads.
// The AGNTCY identity spec is v1alpha1 and mixes VCDM 1.1/2.0 field names, so
// only the fields needed for record matching and proof-presence are typed;
// credentialSchema is kept raw because it appears both as an object and as an
// array in the wild.
type verifiableCredential struct {
	CredentialSubject struct {
		Badge json.RawMessage `json:"badge"`
	} `json:"credentialSubject"`
	CredentialSchema json.RawMessage `json:"credentialSchema"`
	Proof            json.RawMessage `json:"proof"`
}

// parseVC unmarshals a VC JSON object.
func parseVC(raw []byte) (verifiableCredential, bool) {
	var vc verifiableCredential
	if err := json.Unmarshal(raw, &vc); err != nil {
		return verifiableCredential{}, false
	}
	return vc, true
}

// recordRef extracts the embedded record's "oasf:<name>@<version>" reference.
// credentialSubject.badge appears both as the record object and as a JSON
// string containing the record (the v1alpha1 spec is not consistent), so both
// forms are accepted. Both name and version must be present.
func (vc verifiableCredential) recordRef() (string, bool) {
	raw := bytes.TrimSpace(vc.CredentialSubject.Badge)
	if len(raw) == 0 {
		return "", false
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", false
		}
		raw = []byte(s)
	}
	var probe struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", false
	}
	if probe.Name == "" || probe.Version == "" {
		return "", false
	}
	return "oasf:" + probe.Name + "@" + probe.Version, true
}

// schemaTolerated reports whether the VC's credentialSchema (when present)
// names an OASF schema on one of the two tolerated hosts. An absent
// credentialSchema is tolerated (the v1alpha1 examples are inconsistent about
// carrying it); a present one naming neither host is not an OASF agent badge.
// Both object and array forms parse (mixed VCDM 1.1/2.0 shapes).
func (vc verifiableCredential) schemaTolerated() bool {
	raw := bytes.TrimSpace(vc.CredentialSchema)
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	type entry struct {
		ID string `json:"id"`
	}
	var entries []entry
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return false
		}
	} else {
		var e entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return false
		}
		entries = []entry{e}
	}
	for _, e := range entries {
		if strings.Contains(e.ID, schemaHostCurrent) || strings.Contains(e.ID, schemaHostLegacy) {
			return true
		}
	}
	return false
}

// parseBadge classifies and verifies one badge document: a JOSE or
// EMBEDDED_PROOF envelope, a bare VC object, or a compact JWS string (raw or
// JSON-quoted). Crypto/shape failures yield an "invalid" outcome (never an
// error); only a trust-anchor fetch failure (the issuer JWKS URL) is a hard
// error, so a transient network fault never masquerades as a tampered badge.
func parseBadge(ctx context.Context, r *keyResolver, data []byte, fileRef string) (badgeOutcome, error) {
	body := bytes.TrimSpace(data)
	if len(body) == 0 {
		return badgeOutcome{ref: fileRef, status: badgeInvalid}, nil
	}
	switch body[0] {
	case '{':
		var env envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return badgeOutcome{ref: fileRef, status: badgeInvalid}, nil
		}
		switch env.EnvelopeType {
		case "JOSE":
			var token string
			if err := json.Unmarshal(env.Value, &token); err != nil {
				return badgeOutcome{ref: fileRef, status: badgeInvalid}, nil
			}
			return verifyJOSE(ctx, r, token, fileRef)
		case "EMBEDDED_PROOF":
			return embeddedOutcome(env.Value, fileRef), nil
		case "":
			// No envelope_type: a bare VC object.
			return embeddedOutcome(body, fileRef), nil
		default:
			return badgeOutcome{ref: fileRef, status: badgeInvalid}, nil
		}
	case '"':
		var token string
		if err := json.Unmarshal(body, &token); err != nil {
			return badgeOutcome{ref: fileRef, status: badgeInvalid}, nil
		}
		return verifyJOSE(ctx, r, token, fileRef)
	default:
		return verifyJOSE(ctx, r, string(body), fileRef)
	}
}

// embeddedOutcome handles an EMBEDDED_PROOF (DataIntegrityProof) badge or a
// bare VC object. We deliberately do NOT implement W3C Data Integrity
// canonicalization against the spec's non-conformant Proof (it lacks
// verificationMethod/created/cryptosuite), so a well-formed embedded-proof
// badge is honestly "unverified" — never trusted, never "verified". A
// malformed one (no parsable VC, no matching record reference, an alien
// credentialSchema, or no proof member at all) is "invalid": ANY
// embedded-proof-shaped badge — enveloped or bare — must at least CARRY a
// proof to earn "unverified"; a proof-less credential is not a badge, it is a
// claim, and labeling it merely "unverified" would understate that.
func embeddedOutcome(vcRaw []byte, fileRef string) badgeOutcome {
	vc, ok := parseVC(vcRaw)
	if !ok {
		return badgeOutcome{ref: fileRef, status: badgeInvalid}
	}
	ref, ok := vc.recordRef()
	if !ok {
		return badgeOutcome{ref: fileRef, status: badgeInvalid}
	}
	if !vc.schemaTolerated() {
		return badgeOutcome{ref: ref, status: badgeInvalid}
	}
	if len(bytes.TrimSpace(vc.Proof)) == 0 || string(bytes.TrimSpace(vc.Proof)) == "null" {
		return badgeOutcome{ref: ref, status: badgeInvalid}
	}
	return badgeOutcome{ref: ref, status: badgeUnverified}
}

// verifyJOSE verifies a compact-JWS badge against the operator JWKS: allowlist
// pinned at parse, key resolved by kid, signature verified, then the VC payload
// must carry a matching record reference and a tolerated credentialSchema. Any
// failure along that path is an "invalid" outcome; a JWKS-URL fetch failure is
// the only hard error.
func verifyJOSE(ctx context.Context, r *keyResolver, token, fileRef string) (badgeOutcome, error) {
	parsed, err := jose.ParseSigned(strings.TrimSpace(token), badgeAllowedAlgs)
	if err != nil {
		return badgeOutcome{ref: fileRef, status: badgeInvalid}, nil
	}
	if len(parsed.Signatures) == 0 {
		return badgeOutcome{ref: fileRef, status: badgeInvalid}, nil
	}
	key, err := r.resolve(ctx, parsed.Signatures[0].Header.KeyID)
	if err != nil {
		return badgeOutcome{}, err
	}
	if key == nil {
		// No trust anchor configured, or no key for this kid: verification fails.
		return badgeOutcome{ref: unverifiedRef(parsed, fileRef), status: badgeInvalid}, nil
	}
	payload, err := parsed.Verify(key)
	if err != nil {
		return badgeOutcome{ref: unverifiedRef(parsed, fileRef), status: badgeInvalid}, nil
	}
	vc, ok := parseVC(payload)
	if !ok {
		return badgeOutcome{ref: fileRef, status: badgeInvalid}, nil
	}
	ref, ok := vc.recordRef()
	if !ok {
		return badgeOutcome{ref: fileRef, status: badgeInvalid}, nil
	}
	if !vc.schemaTolerated() {
		return badgeOutcome{ref: ref, status: badgeInvalid}, nil
	}
	return badgeOutcome{ref: ref, status: badgeVerified}, nil
}

// unverifiedRef best-effort derives the record ref a FAILED JWS badge CLAIMS,
// from its UNVERIFIED payload — used ONLY to label the "invalid" outcome and
// its finding (a negative, diagnostic signal; never a trust grant — "verified"
// always outranks "invalid" in the per-record aggregation). It falls back to
// the badge-file ref when the payload yields no record reference.
func unverifiedRef(parsed *jose.JSONWebSignature, fileRef string) string {
	if vc, ok := parseVC(parsed.UnsafePayloadWithoutVerification()); ok {
		if ref, ok := vc.recordRef(); ok {
			return ref
		}
	}
	return fileRef
}

// keyResolver resolves verification keys for ONE evaluation pass. It starts
// from the inline keyset (parsed once in Open and never mutated after) and
// lazily fetches the issuer JWKS URL AT MOST ONCE per pass, keeping the fetched
// set LOCAL to the pass: Snapshot and Gather can run concurrently (the engine
// re-polls the source while governance syncs the roster), so no shared mutable
// state may survive a call — and N badges with unknown kids cost one fetch, not
// N. A rotated signing key is picked up by the next pass's fetch.
type keyResolver struct {
	s       *Source
	keyset  *jose.JSONWebKeySet
	fetched bool
}

// newKeyResolver seeds a per-pass resolver from the Source's immutable config.
func (s *Source) newKeyResolver() *keyResolver {
	return &keyResolver{s: s, keyset: s.keyset}
}

// resolve finds the verification key for kid, fetching the issuer_jwks_url on
// a miss (once per pass). A nil key with a nil error means "no key" (a
// verification failure for the caller); a non-nil error means the trust anchor
// itself could not be fetched.
func (r *keyResolver) resolve(ctx context.Context, kid string) (*jose.JSONWebKey, error) {
	if k := lookupKey(r.keyset, kid); k != nil {
		return k, nil
	}
	if !r.fetched && r.s.issuerJWKSURL != "" {
		r.fetched = true
		ks, err := r.s.fetchJWKS(ctx)
		if err != nil {
			return nil, err
		}
		r.keyset = ks
		return lookupKey(r.keyset, kid), nil
	}
	return nil, nil
}

// fetchJWKS GETs the operator's issuer JWKS (public key material, non-secret)
// through the shared read-only httpx client, bounded by the configured timeout.
func (s *Source) fetchJWKS(ctx context.Context) (*jose.JSONWebKeySet, error) {
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	client := httpx.New(s.issuerJWKSURL, s.doer, nil, nil)
	var ks jose.JSONWebKeySet
	if err := client.GetJSON(ctx, "", nil, &ks); err != nil {
		return nil, fmt.Errorf("oasf: fetch issuer jwks: %w", err)
	}
	return &ks, nil
}

// lookupKey finds a key by kid, or the sole key when the set has exactly one
// and no kid was given (the svid.go convention). Returns nil when none matches.
func lookupKey(ks *jose.JSONWebKeySet, kid string) *jose.JSONWebKey {
	if ks == nil {
		return nil
	}
	if kid != "" {
		if matches := ks.Key(kid); len(matches) > 0 {
			return &matches[0]
		}
		return nil
	}
	if len(ks.Keys) == 1 {
		return &ks.Keys[0]
	}
	return nil
}
