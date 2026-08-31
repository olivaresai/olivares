// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"context"
	"crypto"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/federation"
	"github.com/spiffe/go-spiffe/v2/spiffeid"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// SPIFFE Federation bundle-endpoint profiles (SPIFFE Trust Domain and Bundle spec).
const (
	// ProfileHTTPSWeb authenticates the bundle endpoint with Web PKI (standard TLS
	// against the system roots) — the default.
	ProfileHTTPSWeb = "https_web"
	// ProfileHTTPSSpiffe authenticates the bundle endpoint with SPIFFE
	// authentication: the endpoint presents an X.509-SVID, validated against the
	// bootstrap trust bundle AND required to carry exactly EndpointSpiffeID
	// (deny-closed). It bootstraps from an inline bundle for the endpoint's domain.
	ProfileHTTPSSpiffe = "https_spiffe"
)

// FederationEndpoint is one foreign trust domain the verifier federates with: its
// SPIFFE trust-bundle endpoint and the authentication profile to reach it. It is the
// per-domain shape of "federates_with".
type FederationEndpoint struct {
	// TrustDomain is the foreign trust-domain name (with or without the scheme).
	TrustDomain string
	// BundleEndpointURL is the URL serving that domain's SPIFFE trust bundle.
	BundleEndpointURL string
	// Profile is ProfileHTTPSWeb (default) or ProfileHTTPSSpiffe.
	Profile string
	// EndpointSpiffeID is the bundle endpoint server's SPIFFE ID. REQUIRED for
	// https_spiffe (the endpoint must present exactly this id, deny-closed); ignored
	// for https_web.
	EndpointSpiffeID string
}

// IDN-07 live SPIFFE: a JWT-SVID verifier. The offline path (Snapshot from an
// entries export) is unchanged; this is the OPT-IN live mode the host wires. It
// turns a presented JWT-SVID into a VerifiedSVID whose SPIFFE ID maps to the SAME
// roster identity the offline Snapshot creates (Identity.Ref == the SPIFFE ID), and
// whose raw token is forwarded as the assertion to the IDN-01 WIF exchange
// (connectors/claude-wif). It NEVER mints, stores or logs a credential — it only
// verifies one presented by a workload, then hands the verified token to the
// exchange (read-first, docs/SECURITY-HARDENING.md).
//
// multi-trust-domain federation. A verifier used to accept only a single
// static JWKS (an inline blob or the SPIRE OIDC Discovery Provider's keys URL),
// so a JWT-SVID minted in a FEDERATED foreign trust domain could not be verified
// at all. The verifier now also consumes SPIFFE **trust-bundle endpoints** (the
// SPIFFE Trust Domain and Bundle format, served by a SPIRE bundle endpoint) and
// inline SPIFFE bundles, keyed by trust domain. A presented JWT-SVID is verified
// against the bundle of ITS OWN trust domain — so several federated domains are
// trusted at once, deny-closed for any domain with no configured bundle. The
// legacy single-JWKS path is preserved unchanged (and is the fallback when a
// token's trust domain has no federated bundle), so existing deployments are
// untouched.

// svidAllowedAlgs is the asymmetric signature allow-list for a JWT-SVID. Symmetric
// (HMAC) and "none" are rejected by omission — pinning the allow-list at parse time
// is the defense against an algorithm-confusion downgrade. It mirrors the algorithms
// the Anthropic WIF endpoint itself accepts AND the go-spiffe jwtsvid allow-list, so
// the federated-bundle path and the legacy JWKS path reject the same set.
var svidAllowedAlgs = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.ES256, jose.ES384, jose.ES512,
	jose.PS256, jose.PS384, jose.PS512,
}

// svidLeeway is the clock-skew tolerance applied to exp/nbf/iat, matching the WIF
// endpoint's documented 30-second leeway.
const svidLeeway = 30 * time.Second

// VerifiedSVID is the result of verifying a presented JWT-SVID: the asserted SPIFFE
// ID (also the roster external_id), its audience and validity window, and the raw
// token to forward to the WIF exchange. It carries no private key.
type VerifiedSVID struct {
	SpiffeID  string
	Audience  []string
	IssuedAt  time.Time
	ExpiresAt time.Time

	assertion string // the raw verified JWT, forwarded as the WIF exchange assertion
}

// Assertion returns the raw verified JWT to present as the RFC 7523 assertion in the
// IDN-01 exchange. It is a bearer credential — use it transiently, never persist it.
func (v VerifiedSVID) Assertion() string { return v.assertion }

// RosterRef returns the identity reference this SVID maps to: the SPIFFE ID, which
// is exactly the Identity.Ref the offline Snapshot uses, so a verified workload
// converges on the same governed identity (external_id).
func (v VerifiedSVID) RosterRef() string { return v.SpiffeID }

// VerifierConfig configures the live JWT-SVID verifier. At least one trust source
// is required. The legacy single-domain source is an inline JWKS (JWKS) or a JWKS
// URL (JWKSURL, the SPIRE OIDC Discovery Provider's read-only keys endpoint),
// anchored to TrustDomain. The multi-trust-domain sources are SPIFFE
// trust-bundle endpoints (BundleEndpoints) and inline SPIFFE bundles (InlineBundles),
// each keyed by its own trust domain. Audience, when set, is required to appear in
// the SVID's aud. TrustDomain, when set, constrains the LEGACY source's accepted
// subjects (the federated bundles enforce their own domain by construction).
type VerifierConfig struct {
	TrustDomain string // e.g. "corp.example" (with or without the spiffe:// scheme)
	Audience    string // the expected audience the SVID must carry ("" => not checked)
	JWKS        string // inline JWKS JSON (the trust bundle public keys)
	JWKSURL     string // read-only JWKS endpoint (mutually exclusive with JWKS)

	// InlineBundles maps a trust-domain name to an inline SPIFFE trust-bundle JSON
	// document (the same format an endpoint serves), for air-gapped or statically
	// pinned federation. It is ALSO the bootstrap trust bundle for an https_spiffe
	// endpoint of that domain (its X.509 authorities authenticate the endpoint). A
	// malformed bundle fails at construction.
	InlineBundles map[string]string
	// Federations are the foreign trust domains to federate with via their SPIFFE
	// bundle endpoints. A JWT-SVID from such a domain is verified against the fetched
	// bundle; the bundle is re-fetched on a key-id miss (rotation) and when it is past
	// its spiffe_refresh_hint (freshness). Never the SPIRE write-capable admin API.
	Federations []FederationEndpoint
}

// bundleFetcher fetches a SPIFFE trust bundle for a federation endpoint. It is the
// profile-aware fetch in production and an injected fake in tests (a live bundle
// endpoint is not assumed in CI).
type bundleFetcher func(ctx context.Context, fe FederationEndpoint) (*spiffebundle.Bundle, error)

// Verifier verifies presented JWT-SVIDs. It is concurrency-safe; the legacy keyset
// and the federated bundle set are cached and re-fetched on a kid miss or a
// past-refresh-hint staleness.
type Verifier struct {
	trustDomain string // legacy source's optional domain constraint (bare name)
	audience    string

	// Legacy single-domain source (back-compat, unchanged behavior).
	jwksURL string
	doer    httpx.Doer

	// Multi-trust-domain federation.
	endpoints map[spiffeid.TrustDomain]FederationEndpoint
	fetch     bundleFetcher // injectable; nil => profile-aware federation.FetchBundle

	now func() time.Time

	// bootstrap is the IMMUTABLE operator-pinned X.509 trust store used to
	// authenticate an https_spiffe bundle endpoint (federation.WithSPIFFEAuth). It is
	// populated once from the inline bundles at construction and NEVER replaced by a
	// fetch — unlike fed, whose per-domain bundle storeBundle overwrites with the
	// freshly fetched (JWT) authorities. Keeping the two apart is what lets a re-fetch
	// (rotation / refresh_hint) still authenticate the endpoint after the first fetch
	// replaced fed[td] with a served bundle that need not re-carry the bootstrap CA.
	bootstrap *spiffebundle.Set

	mu        sync.Mutex
	keyset    *jose.JSONWebKeySet                // legacy cached keyset
	fed       *spiffebundle.Set                  // federated bundles, keyed by trust domain
	fetchedAt map[spiffeid.TrustDomain]time.Time // last fetch per domain (refresh_hint)
}

// NewVerifier builds a live JWT-SVID verifier from cfg. Inline material (the JWKS
// and inline bundles) is parsed immediately (a malformed value fails here); a JWKS
// URL or a bundle endpoint is fetched lazily on first use. It errors when no trust
// source is configured at all, or when an https_spiffe endpoint is missing its
// required endpoint_spiffe_id (deny-closed).
func NewVerifier(cfg VerifierConfig, doer httpx.Doer) (*Verifier, error) {
	v := &Verifier{
		trustDomain: normalizeTrustDomain(cfg.TrustDomain),
		audience:    strings.TrimSpace(cfg.Audience),
		jwksURL:     strings.TrimSpace(cfg.JWKSURL),
		doer:        doer,
		endpoints:   map[spiffeid.TrustDomain]FederationEndpoint{},
		fed:         spiffebundle.NewSet(),
		bootstrap:   spiffebundle.NewSet(),
		fetchedAt:   map[spiffeid.TrustDomain]time.Time{},
	}
	if raw := strings.TrimSpace(cfg.JWKS); raw != "" {
		var ks jose.JSONWebKeySet
		if err := json.Unmarshal([]byte(raw), &ks); err != nil {
			return nil, fmt.Errorf("spiffe: parse inline jwks: %w", err)
		}
		v.keyset = &ks
	}
	// Inline SPIFFE bundles: parsed now so a malformed one fails at construction.
	for tdName, doc := range cfg.InlineBundles {
		td, err := spiffeid.TrustDomainFromString(normalizeTrustDomain(tdName))
		if err != nil {
			return nil, fmt.Errorf("spiffe: inline bundle trust domain %q: %w", tdName, err)
		}
		b, err := spiffebundle.Parse(td, []byte(doc))
		if err != nil {
			return nil, fmt.Errorf("spiffe: parse inline bundle for %q: %w", td, err)
		}
		v.fed.Add(b)
		// The same bundle anchors https_spiffe endpoint authentication. bootstrap is
		// never replaced by a fetch, so the endpoint stays authenticatable across
		// re-fetches even though fed[td] is overwritten with the served bundle.
		v.bootstrap.Add(b)
	}
	// Federation endpoints: recorded now, fetched lazily on first use of their domain.
	for _, fe := range cfg.Federations {
		td, err := spiffeid.TrustDomainFromString(normalizeTrustDomain(fe.TrustDomain))
		if err != nil {
			return nil, fmt.Errorf("spiffe: federation trust domain %q: %w", fe.TrustDomain, err)
		}
		if strings.TrimSpace(fe.BundleEndpointURL) == "" {
			return nil, fmt.Errorf("spiffe: federation for %q has no bundle_endpoint_url", td)
		}
		profile := strings.TrimSpace(fe.Profile)
		switch profile {
		case "", ProfileHTTPSWeb:
			profile = ProfileHTTPSWeb
		case ProfileHTTPSSpiffe:
			// Deny-closed: an SPIFFE-authenticated endpoint MUST name the id it presents.
			if strings.TrimSpace(fe.EndpointSpiffeID) == "" {
				return nil, fmt.Errorf("spiffe: federation for %q uses https_spiffe but has no endpoint_spiffe_id", td)
			}
			if _, err := spiffeid.FromString(strings.TrimSpace(fe.EndpointSpiffeID)); err != nil {
				return nil, fmt.Errorf("spiffe: federation for %q endpoint_spiffe_id %q: %w", td, fe.EndpointSpiffeID, err)
			}
		default:
			return nil, fmt.Errorf("spiffe: federation for %q has unknown profile %q", td, fe.Profile)
		}
		v.endpoints[td] = FederationEndpoint{
			TrustDomain:       td.String(),
			BundleEndpointURL: strings.TrimSpace(fe.BundleEndpointURL),
			Profile:           profile,
			EndpointSpiffeID:  strings.TrimSpace(fe.EndpointSpiffeID),
		}
	}
	if v.keyset == nil && v.jwksURL == "" && v.fed.Len() == 0 && len(v.endpoints) == 0 {
		return nil, fmt.Errorf("spiffe: verifier needs an inline jwks, a jwks_url, an inline bundle, or a federation endpoint")
	}
	return v, nil
}

// Verify validates a presented JWT-SVID: it enforces the asymmetric-algorithm
// allow-list, resolves the verification key by the token's OWN trust domain (a
// federated bundle when one is configured for that domain, otherwise the legacy
// keyset), verifies the signature, and checks the SPIFFE-ID subject, the audience,
// and the validity window (exp/iat/nbf with skew leeway). An invalid signature, an
// expired or not-yet-valid token, a wrong audience, or a subject in a trust domain
// with no configured bundle are all rejected (deny-closed). On success it returns
// the VerifiedSVID carrying the raw token for the exchange.
func (vr *Verifier) Verify(ctx context.Context, token string) (VerifiedSVID, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return VerifiedSVID{}, fmt.Errorf("spiffe: verify: empty token")
	}
	parsed, err := jwt.ParseSigned(token, svidAllowedAlgs)
	if err != nil {
		return VerifiedSVID{}, fmt.Errorf("spiffe: verify: parse: %w", err)
	}
	if len(parsed.Headers) == 0 {
		return VerifiedSVID{}, fmt.Errorf("spiffe: verify: no JOSE header")
	}
	kid := parsed.Headers[0].KeyID

	// Read the (unverified) subject to learn which trust domain's bundle to use; the
	// signature is verified against that domain's key below before any claim is trusted.
	var unverified jwt.Claims
	if err := parsed.UnsafeClaimsWithoutVerification(&unverified); err != nil {
		return VerifiedSVID{}, fmt.Errorf("spiffe: verify: read subject: %w", err)
	}
	subjectTD := parseSpiffe(unverified.Subject).trustDomain()

	key, federated, err := vr.resolveKey(ctx, subjectTD, kid)
	if err != nil {
		return VerifiedSVID{}, err
	}

	var claims jwt.Claims
	if err := parsed.Claims(key, &claims); err != nil {
		return VerifiedSVID{}, fmt.Errorf("spiffe: verify: signature: %w", err)
	}
	// A federated bundle already pins the trust domain (the key came from that
	// domain's bundle); the legacy path enforces the configured single domain.
	expectTD := vr.trustDomain
	if federated {
		expectTD = subjectTD
	}
	if err := vr.validateClaims(claims, expectTD); err != nil {
		return VerifiedSVID{}, err
	}

	return VerifiedSVID{
		SpiffeID:  claims.Subject,
		Audience:  []string(claims.Audience),
		IssuedAt:  timeOf(claims.IssuedAt),
		ExpiresAt: timeOf(claims.Expiry),
		assertion: token,
	}, nil
}

// resolveKey finds the verification key for (trust domain, kid). A trust domain with
// a configured federated bundle (inline or endpoint) is resolved against that bundle;
// federated=true is returned so the caller knows the domain is pinned by the bundle.
// Otherwise it falls back to the legacy keyset (inline JWKS or JWKS URL). A token whose
// trust domain has neither a federated bundle nor a legacy keyset is rejected
// (deny-closed).
func (vr *Verifier) resolveKey(ctx context.Context, subjectTD, kid string) (crypto.PublicKey, bool, error) {
	td, tdErr := spiffeid.TrustDomainFromString(subjectTD)
	_, hasEndpoint := vr.endpoints[td]
	if tdErr == nil && (vr.fed.Has(td) || hasEndpoint) {
		key, err := vr.resolveFederatedKey(ctx, td, kid)
		return key, true, err
	}
	// Legacy single-domain path (unchanged): the inline JWKS / JWKS URL keyset.
	key, err := vr.resolveLegacyKey(ctx, kid)
	return key, false, err
}

// resolveFederatedKey resolves kid against the federated bundle for td. It first
// honors the bundle's spiffe_refresh_hint (re-fetching a stale bundle), then looks up
// the authority, fetching the endpoint on first use and re-fetching once on a kid miss
// (key rotation).
func (vr *Verifier) resolveFederatedKey(ctx context.Context, td spiffeid.TrustDomain, kid string) (crypto.PublicKey, error) {
	vr.refreshIfStale(ctx, td)
	if k, ok := vr.federatedAuthority(td, kid); ok {
		return k, nil
	}
	// Not present (bundle not yet fetched, or a rotated kid): (re-)fetch the endpoint.
	if fe, ok := vr.endpoints[td]; ok {
		b, err := vr.fetchBundle(ctx, fe)
		if err != nil {
			return nil, fmt.Errorf("spiffe: fetch bundle for %q: %w", td, err)
		}
		vr.storeBundle(td, b)
		if k, ok := vr.federatedAuthority(td, kid); ok {
			return k, nil
		}
	}
	return nil, fmt.Errorf("spiffe: verify: no key for kid %q in trust bundle for %q", kid, td)
}

// refreshIfStale re-fetches td's bundle when it is past its spiffe_refresh_hint, so a
// rotated authority is picked up proactively (the SPIFFE Federation refresh contract).
// It is best-effort: a fetch failure keeps the cached bundle (availability over a
// hard fail), and a domain with no endpoint or no hint is left untouched.
func (vr *Verifier) refreshIfStale(ctx context.Context, td spiffeid.TrustDomain) {
	fe, ok := vr.endpoints[td]
	if !ok {
		return // inline-only domain: nothing to refresh
	}
	vr.mu.Lock()
	b, err := vr.fed.GetBundleForTrustDomain(td)
	at := vr.fetchedAt[td]
	vr.mu.Unlock()
	if err != nil || at.IsZero() {
		return // not fetched yet; the lazy fetch below loads it
	}
	hint, hasHint := b.RefreshHint()
	if !hasHint || hint <= 0 || vr.clock().Sub(at) < hint {
		return // no hint, or still fresh
	}
	if nb, err := vr.fetchBundle(ctx, fe); err == nil {
		vr.storeBundle(td, nb)
	}
}

// storeBundle records a freshly fetched bundle for td and stamps its fetch time.
func (vr *Verifier) storeBundle(td spiffeid.TrustDomain, b *spiffebundle.Bundle) {
	vr.mu.Lock()
	vr.fed.Add(b)
	vr.fetchedAt[td] = vr.clock()
	vr.mu.Unlock()
}

// federatedAuthority returns the JWT authority for (td, kid) from the federated set.
func (vr *Verifier) federatedAuthority(td spiffeid.TrustDomain, kid string) (crypto.PublicKey, bool) {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	b, err := vr.fed.GetJWTBundleForTrustDomain(td)
	if err != nil {
		return nil, false
	}
	if kid != "" {
		return b.FindJWTAuthority(kid)
	}
	// No kid in the header: fall back to the sole authority when the bundle has one.
	if auths := b.JWTAuthorities(); len(auths) == 1 {
		for _, k := range auths {
			return k, true
		}
	}
	return nil, false
}

// fetchBundle calls the injected fetcher, or fetches via the endpoint's profile in
// production. https_web authenticates with the system Web PKI; https_spiffe requires
// the endpoint to present exactly EndpointSpiffeID, validated against the X.509
// authorities already trusted for its domain (the bootstrap inline bundle) —
// deny-closed: an unvalidated or mis-identified endpoint yields no bundle.
func (vr *Verifier) fetchBundle(ctx context.Context, fe FederationEndpoint) (*spiffebundle.Bundle, error) {
	if vr.fetch != nil {
		return vr.fetch(ctx, fe)
	}
	td, err := spiffeid.TrustDomainFromString(normalizeTrustDomain(fe.TrustDomain))
	if err != nil {
		return nil, fmt.Errorf("spiffe: federation trust domain %q: %w", fe.TrustDomain, err)
	}
	switch fe.Profile {
	case ProfileHTTPSSpiffe:
		endpointID, err := spiffeid.FromString(strings.TrimSpace(fe.EndpointSpiffeID))
		if err != nil {
			return nil, fmt.Errorf("spiffe: https_spiffe endpoint_spiffe_id %q: %w", fe.EndpointSpiffeID, err)
		}
		// vr.bootstrap is the IMMUTABLE operator-pinned X.509 root store: it must
		// already trust the endpoint's domain (an inline bootstrap bundle). Using
		// bootstrap (not the fetch-replaced fed) keeps re-fetches authenticatable. The
		// endpoint's served bundle is NOT trusted to declare its own roots (no
		// circular trust); a CA rotation is an explicit bootstrap-config update.
		// WithSPIFFEAuth pins AuthorizeID(endpointID), so a wrong id or an untrusted
		// chain fails the handshake — deny-closed, never an open accept.
		return federation.FetchBundle(ctx, td, fe.BundleEndpointURL, federation.WithSPIFFEAuth(vr.bootstrap, endpointID))
	default: // ProfileHTTPSWeb
		return federation.FetchBundle(ctx, td, fe.BundleEndpointURL)
	}
}

// resolveLegacyKey finds the verification key for kid in the cached legacy keyset,
// fetching the JWKS URL on a miss (so a rotated signing key is picked up). A keyset
// with a single key and no kid in the header falls back to that key.
func (vr *Verifier) resolveLegacyKey(ctx context.Context, kid string) (crypto.PublicKey, error) {
	vr.mu.Lock()
	if k := lookupKey(vr.keyset, kid); k != nil {
		vr.mu.Unlock()
		return k.Key, nil
	}
	vr.mu.Unlock()
	if vr.jwksURL != "" {
		ks, err := vr.fetchJWKS(ctx)
		if err != nil {
			return nil, err
		}
		vr.mu.Lock()
		vr.keyset = ks
		k := lookupKey(vr.keyset, kid)
		vr.mu.Unlock()
		if k != nil {
			return k.Key, nil
		}
	}
	return nil, fmt.Errorf("spiffe: verify: no key for kid %q in trust bundle", kid)
}

// fetchJWKS GETs the read-only JWKS endpoint and parses it. It uses the shared
// read-only httpx client; a JWKS is non-secret public key material.
func (vr *Verifier) fetchJWKS(ctx context.Context) (*jose.JSONWebKeySet, error) {
	client := httpx.New(vr.jwksURL, vr.doer, nil, nil)
	var ks jose.JSONWebKeySet
	if err := client.GetJSON(ctx, "", nil, &ks); err != nil {
		return nil, fmt.Errorf("spiffe: fetch jwks: %w", err)
	}
	return &ks, nil
}

// validateClaims enforces the SPIFFE-ID subject, the (optional) expected trust
// domain, the audience and the validity window.
func (vr *Verifier) validateClaims(c jwt.Claims, expectTD string) error {
	if !strings.HasPrefix(c.Subject, scheme) {
		return fmt.Errorf("spiffe: verify: subject %q is not a SPIFFE ID", c.Subject)
	}
	if expectTD != "" {
		if td := parseSpiffe(c.Subject).trustDomain(); td != expectTD {
			return fmt.Errorf("spiffe: verify: subject trust domain %q != expected %q", td, expectTD)
		}
	}
	if vr.audience != "" && !c.Audience.Contains(vr.audience) {
		return fmt.Errorf("spiffe: verify: audience %v does not include %q", []string(c.Audience), vr.audience)
	}
	now := vr.clock()
	if c.Expiry == nil {
		return fmt.Errorf("spiffe: verify: token has no exp")
	}
	if now.After(timeOf(c.Expiry).Add(svidLeeway)) {
		return fmt.Errorf("spiffe: verify: token expired")
	}
	if c.NotBefore != nil && now.Add(svidLeeway).Before(timeOf(c.NotBefore)) {
		return fmt.Errorf("spiffe: verify: token not yet valid")
	}
	if c.IssuedAt != nil && timeOf(c.IssuedAt).After(now.Add(svidLeeway)) {
		return fmt.Errorf("spiffe: verify: token issued in the future")
	}
	return nil
}

// clock returns the verifier's time source (injectable for tests).
func (vr *Verifier) clock() time.Time {
	if vr.now != nil {
		return vr.now()
	}
	return time.Now()
}

// lookupKey finds a key by kid, or the sole key when the set has exactly one and no
// kid was given. Returns nil when no key matches.
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

// timeOf converts a jwt.NumericDate to time.Time (zero when nil).
func timeOf(d *jwt.NumericDate) time.Time {
	if d == nil {
		return time.Time{}
	}
	return d.Time()
}
