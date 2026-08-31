// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// issuertrust.go is the ISSUER-KEYED trust store shared by every inbound-JWT
// validator in this connector: the RS bearer validator (tokenvalidate.go) and the
// Enterprise-Managed Authorization ID-JAG validator (idjag.go). It exists because
// the 2026-07-28 RC makes "credentials are keyed by the issuer that owns them" a
// spec-level rule (SEP-2352 for client credentials; RFC 9068 §4 has always required
// it of the RS: "The resource server MUST use the keys provided by the authorization
// server" — the keys of THE issuer named in the token, not a shared keyring):
//
//   - every trust anchor (inline JWKS, JWKS URL, introspection endpoint+credential)
//     belongs to EXACTLY ONE issuer and is looked up by the token's iss value with
//     SIMPLE STRING COMPARISON — no normalization, no case folding, no
//     trailing-slash tolerance (RFC 9207 §2.4 semantics applied to storage);
//   - a key configured for issuer A can therefore never verify a token claiming
//     issuer B, even on a kid collision (no cross-issuer collisions);
//   - per-issuer JWKS refresh is serialized under the issuer's own lock (this also
//     fixes the validator's unsynchronized anchor refresh on rotation).

// IssuerTrust is the operator configuration for ONE trusted token issuer: the exact
// issuer identifier (REQUIRED — it is the lookup key) and that issuer's own trust
// anchors. At least one anchor (inline JWKS, JWKS URL, or introspection endpoint)
// must be set. IntrospectionAuth is the RS's OWN credential at this issuer's
// introspection endpoint (out-of-band operator config, never the inbound token).
//
// OPERATOR NOTE on multiple introspection endpoints: an opaque token names no
// issuer, so it is presented to EACH configured introspection endpoint in
// declaration order until one answers active (tokenvalidate.go). Co-configure
// introspection only for issuers that may see each other's bearers — every
// configured AS is a disclosure target for opaque tokens of the others.
type IssuerTrust struct {
	Issuer            string
	JWKS              []byte
	JWKSURL           string
	IntrospectionURL  string
	IntrospectionAuth string
	// ClaimedDomains are the email domains this issuer is authoritative for (its
	// home-realm claim, mirroring the first-party SSO U5 boundary). Optional,
	// non-secret operator config. It bounds the Enterprise-Managed Authorization
	// verified-email fallback: an issuer may vouch by bare email only for a domain it
	// claims, so a trusted-but-domain-scoped IdP cannot seize an account in a domain it
	// does not own (the cross-IdP EMA takeover, F1). Empty ⇒ unconstrained (safe
	// only as the sole trusted issuer; enforced engine-side, not here).
	ClaimedDomains []string
}

// trustedIssuer is the resolved runtime state for one issuer. The anchor keyset is
// guarded by mu; resolveKey refreshes it from JWKSURL on a kid miss (key rotation)
// with the NETWORK CALL OUTSIDE the lock, single-flighted, and rate-limited — the
// kid comes from the UNVERIFIED JOSE header, so without those bounds an attacker
// spraying random kids at a trusted issuer would serialize every request behind
// JWKS fetches (pre-auth starvation).
type trustedIssuer struct {
	issuer           string
	jwksURL          string
	introspectionURL string
	introspectAuth   string
	claimedDomains   []string // normalized home-realm domains (U5 / EMA email-fallback bound)

	mu          sync.Mutex
	anchor      *jose.JSONWebKeySet
	refreshing  bool      // a JWKS refresh is in flight (single-flight)
	lastRefresh time.Time // when the last refresh COMPLETED (success or failure)
}

// jwksRefreshMinInterval is the minimum spacing between JWKS refreshes for one
// issuer — the negative cache: a kid that stays unknown after a refresh cannot
// force another fetch until the interval passes (a real rotation is picked up on
// the first miss; random kids are answered from the cached set).
const jwksRefreshMinInterval = 60 * time.Second

// issuerKeyring is the issuer-keyed trust set. order preserves declaration order so
// fan-out paths (opaque-token introspection) are deterministic.
type issuerKeyring struct {
	issuers map[string]*trustedIssuer
	order   []string
}

// newIssuerKeyring resolves the operator's issuer list. Fail-closed construction: an
// entry without an issuer identifier, a duplicate issuer, an unparseable inline JWKS,
// or an entry with no trust anchor at all is a configuration error — a validator that
// cannot attribute every anchor to exactly one issuer is never built.
func newIssuerKeyring(entries []IssuerTrust) (*issuerKeyring, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("mcp: rs: at least one trusted issuer is required (RFC 9068 §4: iss validation is mandatory, so a token trust anchor must be keyed by its issuer)")
	}
	k := &issuerKeyring{issuers: map[string]*trustedIssuer{}}
	for i, e := range entries {
		iss := strings.TrimSpace(e.Issuer)
		if iss == "" {
			return nil, fmt.Errorf("mcp: rs: trusted issuer #%d has no issuer identifier (RFC 9068 §4: the iss check is fail-closed and cannot be skipped)", i)
		}
		if _, dup := k.issuers[iss]; dup {
			return nil, fmt.Errorf("mcp: rs: duplicate trusted issuer %q (ambiguous trust must never resolve nondeterministically)", iss)
		}
		ti := &trustedIssuer{
			issuer:           iss,
			jwksURL:          strings.TrimSpace(e.JWKSURL),
			introspectionURL: strings.TrimSpace(e.IntrospectionURL),
			introspectAuth:   e.IntrospectionAuth,
			claimedDomains:   normalizeClaimedDomains(e.ClaimedDomains),
		}
		if len(e.JWKS) > 0 {
			set, err := parseJWKSBytes(e.JWKS)
			if err != nil {
				return nil, fmt.Errorf("mcp: rs: issuer %q: %w", iss, err)
			}
			ti.anchor = set
		}
		if ti.anchor == nil && ti.jwksURL == "" && ti.introspectionURL == "" {
			return nil, fmt.Errorf("mcp: rs: issuer %q declares no trust anchor (issuer_jwks, jwks_url, or introspection_url)", iss)
		}
		k.issuers[iss] = ti
		k.order = append(k.order, iss)
	}
	return k, nil
}

// lookup resolves an iss value to its trusted issuer by SIMPLE STRING COMPARISON —
// deliberately no TrimSpace, no case folding, no URL normalization: an issuer that
// differs in any byte is a different issuer (RFC 9207 §2.4 / RFC 9068 §4 "exactly
// match").
func (k *issuerKeyring) lookup(iss string) (*trustedIssuer, bool) {
	ti, ok := k.issuers[iss]
	return ti, ok
}

// size is the number of distinct trusted issuers. The EMA verified-email fallback is
// authorized for an UNCONSTRAINED issuer only when this is 1 (the single-IdP
// deployment); a multi-issuer keyring requires an explicit domain claim (F1).
func (k *issuerKeyring) size() int { return len(k.issuers) }

// normalizeClaimedDomains canonicalizes an issuer's home-realm domains: lower-cased,
// trimmed, leading '@' and trailing '.' stripped, blanks and duplicates dropped. It
// deliberately mirrors core/model.NormalizeFederationDomain WITHOUT importing it — the
// Apache-2.0 connectors must not depend on the AGPL engine (scripts/check-boundary.sh)
// — and the engine re-normalizes on compare, so a byte-skew can never widen authority.
func normalizeClaimedDomains(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimPrefix(d, "@")
		d = strings.TrimSuffix(d, ".")
		if d == "" {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// introspectors returns the trusted issuers that expose an introspection endpoint, in
// declaration order (the deterministic fan-out order for opaque tokens).
func (k *issuerKeyring) introspectors() []*trustedIssuer {
	var out []*trustedIssuer
	for _, iss := range k.order {
		if ti := k.issuers[iss]; ti.introspectionURL != "" {
			out = append(out, ti)
		}
	}
	return out
}

// resolveKey finds THIS issuer's verification key for kid: the cached anchor first,
// then an SSRF-guarded refresh from the issuer's JWKS URL on a miss (key rotation).
// A key is only ever served from the issuer's OWN set — there is no cross-issuer
// fallback by construction. The refresh is single-flighted and rate-limited
// (jwksRefreshMinInterval) and the HTTP fetch runs OUTSIDE the lock; a request that
// cannot refresh (another refresh in flight, or inside the interval) fails closed
// against the cached set rather than queueing — kid is attacker-readable pre-auth,
// and a lock held across the network would let it starve legitimate validations.
func (ti *trustedIssuer) resolveKey(ctx context.Context, doer httpDoer, kid string, now time.Time) (*jose.JSONWebKey, error) {
	ti.mu.Lock()
	if k := keyFromSet(ti.anchor, kid); k != nil {
		ti.mu.Unlock()
		return k, nil
	}
	noKey := fmt.Errorf("mcp: rs: no signing key for kid %q in the trust bundle of issuer %q", kid, ti.issuer)
	if ti.jwksURL == "" || ti.refreshing ||
		(!ti.lastRefresh.IsZero() && now.Sub(ti.lastRefresh) < jwksRefreshMinInterval) {
		ti.mu.Unlock()
		return nil, noKey
	}
	ti.refreshing = true
	ti.mu.Unlock()

	set, ferr := fetchJSON[jose.JSONWebKeySet](ctx, doer, ti.jwksURL)

	ti.mu.Lock()
	ti.refreshing = false
	ti.lastRefresh = now
	if ferr == nil && len(set.Keys) > 0 {
		ti.anchor = &set
	}
	k := keyFromSet(ti.anchor, kid)
	ti.mu.Unlock()
	if k != nil {
		return k, nil
	}
	if ferr != nil {
		return nil, fmt.Errorf("mcp: rs: fetch jwks for issuer %q: %w", ti.issuer, ferr)
	}
	return nil, noKey
}
