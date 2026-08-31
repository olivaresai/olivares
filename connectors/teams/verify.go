// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// verify.go is the NATIVE Microsoft Bot Framework / Azure Bot Service inbound-token
// verifier — the "Connector to bot" direction — for a Teams Adaptive Card Universal
// Action (Action.Execute → adaptiveCard/action Invoke). It closes the follow-up
// (the ITSM/chatops contract, §c): the HITL inbound receiver can
// now authenticate a Teams approve/deny callback by its OWN native RS256 JWT instead of the
// generic operator-reproduced HMAC, once the operator registers a bot. The HMAC path stays
// the default when no bot is registered (the receiver chooses the scheme per provider).
//
// VALIDATION RULES (VERIFIED primary source, jun-2026, Microsoft Learn "Connector to bot"
// — learn.microsoft.com/azure/bot-service/rest-api/bot-framework-rest-connector-authentication):
//  1. the token is the Authorization: Bearer JWT;
//  2. iss MUST be a trusted issuer (public cloud: https://api.botframework.com) — exact
//     string match;
//  3. aud MUST equal the bot's Microsoft App ID (the confused-deputy defense, req. 4);
//  4. the signature MUST verify with RS256 against a key from the OpenID metadata's
//     jwks_uri (req. 6); alg:none / any non-allowlisted alg is rejected at parse;
//  5. the token MUST be within exp/nbf (5-minute industry-standard clock skew, req. 5);
//  6. the serviceUrl claim MUST match the Activity.serviceUrl (req. 7), binding the token
//     to the channel endpoint;
//  7. the signing-key list MAY be cached but MUST refresh at least every 24 hours.
//
// The metadata URL, issuer set and audience are operator-overridable so the emulator and
// the US-gov / sovereign clouds work without a code change (api.botframework.us /
// login.botframework.azure.us; emulator MSA issuers). Microsoft: "Implementers shouldn't
// expose a way to disable validation" — there is no skip mode here. go-jose does NOT
// mandate an exp claim, so its presence is asserted explicitly (gotcha).
//
// It imports only go-jose and the standard library — never the engine (Apache boundary).
package teams

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

// Default public-cloud Bot Framework constants (VERIFIED).
const (
	// DefaultMetadataURL is the static, hardcodable OpenID metadata document for the
	// public-cloud Bot Connector.
	DefaultMetadataURL = "https://login.botframework.com/v1/.well-known/openidconfiguration"
	// DefaultIssuer is the required iss for the public-cloud Bot Connector (protocol v3.1
	// & v3.2).
	DefaultIssuer = "https://api.botframework.com"
	// DefaultClockSkew is Microsoft's stated industry-standard tolerance.
	DefaultClockSkew = 5 * time.Minute

	// jwksRefreshInterval is the Microsoft-mandated refresh floor for the key cache.
	jwksRefreshInterval = 24 * time.Hour
	// jwksMinRefresh rate-limits forced refreshes on an unknown kid (rotation pickup
	// without letting a bad kid trigger a refetch storm).
	jwksMinRefresh = time.Minute

	maxMetaBody = 1 << 20
)

// rs256Only is the default accepted signature algorithm set: the Bot Framework metadata
// declares id_token_signing_alg_values_supported = ["RS256"]. HMAC and "none" are rejected
// by omission (the algorithm-confusion defense).
var rs256Only = []jose.SignatureAlgorithm{jose.RS256}

// httpDoer is the minimal HTTP interface (satisfied by *http.Client).
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// VerifierConfig is the operator configuration for the Bot Framework JWT verifier.
type VerifierConfig struct {
	// AppID is the bot's Microsoft App ID — the REQUIRED aud. Without it the verifier
	// cannot be built (a verifier with no audience to bind would accept tokens minted for
	// any bot — the confused-deputy hole).
	AppID string
	// MetadataURL is the OpenID metadata document URL (default DefaultMetadataURL). The
	// emulator / gov clouds override it.
	MetadataURL string
	// Issuers is the set of accepted iss values (default [DefaultIssuer]). The emulator and
	// gov clouds add their own.
	Issuers []string
	// ClockSkew is the exp/nbf tolerance (default DefaultClockSkew).
	ClockSkew time.Duration
	// RequireServiceURL enforces the serviceUrl-claim ↔ Activity.serviceUrl binding
	// (default true). Disable only when the activity carries no serviceUrl (non-channel
	// test paths).
	RequireServiceURL *bool
	// Doer is the HTTP transport for metadata/JWKS fetches (tests inject a stub). nil =>
	// a default client with a timeout.
	Doer httpDoer
}

// Verifier validates inbound Bot Framework JWTs. It caches the OpenID metadata's jwks_uri
// and the signing keys, refreshing at least every 24 hours and on an unknown kid.
type Verifier struct {
	appID             string
	metadataURL       string
	issuers           map[string]bool
	skew              time.Duration
	requireServiceURL bool
	doer              httpDoer

	mu          sync.Mutex
	jwksURL     string
	keys        *jose.JSONWebKeySet
	fetchedAt   time.Time
	lastAttempt time.Time
}

// Claims is the validated subset the caller needs (the receiver maps identity from the
// activity body, not from the token — the token authenticates the channel, not the user).
type Claims struct {
	Issuer     string
	Audience   []string
	ServiceURL string
}

// NewVerifier builds a verifier. It fails closed if AppID is empty: an audience-less
// verifier would accept a token minted for any other bot.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	appID := strings.TrimSpace(cfg.AppID)
	if appID == "" {
		return nil, fmt.Errorf("teams: bot app_id is required (it is the mandatory token audience; an empty audience would accept any bot's token)")
	}
	v := &Verifier{
		appID:             appID,
		metadataURL:       firstNonEmptyStr(strings.TrimSpace(cfg.MetadataURL), DefaultMetadataURL),
		issuers:           map[string]bool{},
		skew:              cfg.ClockSkew,
		requireServiceURL: true,
		doer:              cfg.Doer,
	}
	if v.skew <= 0 {
		v.skew = DefaultClockSkew
	}
	if cfg.RequireServiceURL != nil {
		v.requireServiceURL = *cfg.RequireServiceURL
	}
	issuers := cfg.Issuers
	if len(issuers) == 0 {
		issuers = []string{DefaultIssuer}
	}
	for _, iss := range issuers {
		if iss = strings.TrimSpace(iss); iss != "" {
			v.issuers[iss] = true
		}
	}
	if len(v.issuers) == 0 {
		return nil, fmt.Errorf("teams: at least one trusted issuer is required")
	}
	// The OpenID metadata document is the ROOT OF TRUST for the signing keys — fetching it
	// over cleartext would let a network MITM serve a forged JWKS and mint accepted tokens.
	// Require https (loopback http is allowed for the Bot Framework emulator). Fail closed
	// at construction rather than at the first callback.
	if err := requireSecureURL(v.metadataURL); err != nil {
		return nil, err
	}
	if v.doer == nil {
		v.doer = &http.Client{
			Timeout: 15 * time.Second,
			// Refuse a redirect that downgrades the metadata/JWKS fetch to cleartext.
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return requireSecureURL(req.URL.String())
			},
		}
	}
	return v, nil
}

// requireSecureURL fails closed unless raw is https, with a loopback carve-out so the Bot
// Framework emulator (which serves its metadata on localhost over http) keeps working.
func requireSecureURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("teams: invalid URL %q: %w", raw, err)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		switch u.Hostname() {
		case "127.0.0.1", "::1", "localhost":
			return nil
		}
	}
	return fmt.Errorf("teams: %q must use https (http allowed only for loopback/emulator)", raw)
}

// Verify authenticates the Authorization header against the activity body, fail-closed. It
// extracts the Bearer JWT, verifies its RS256 signature against the cached Bot Framework
// JWKS, and validates iss ∈ trusted, aud == app_id, exp/nbf within skew, and (when
// required) the serviceUrl claim against the activity's serviceUrl. On any failure it
// returns an error and the caller rejects WITHOUT reaching the engine.
func (v *Verifier) Verify(ctx context.Context, authzHeader string, body []byte, now time.Time) (Claims, error) {
	raw := bearerToken(authzHeader)
	if raw == "" {
		return Claims{}, fmt.Errorf("teams: no Bearer token in Authorization header")
	}
	parsed, err := jwt.ParseSigned(raw, rs256Only)
	if err != nil {
		return Claims{}, fmt.Errorf("teams: token is not a verifiable RS256 JWT: %w", err)
	}
	if len(parsed.Headers) == 0 {
		return Claims{}, fmt.Errorf("teams: token has no JOSE header")
	}
	key, err := v.resolveKey(ctx, parsed.Headers[0].KeyID, now)
	if err != nil {
		return Claims{}, err
	}

	var std jwt.Claims
	var ext map[string]json.RawMessage
	if err := parsed.Claims(key, &std, &ext); err != nil {
		return Claims{}, fmt.Errorf("teams: token signature: %w", err)
	}

	// iss MUST be a trusted issuer (exact string match — no normalization).
	if !v.issuers[std.Issuer] {
		return Claims{}, fmt.Errorf("teams: token issuer %q is not a trusted Bot Framework issuer", std.Issuer)
	}
	// aud MUST contain the bot App ID (the confused-deputy reject).
	if !std.Audience.Contains(v.appID) {
		return Claims{}, fmt.Errorf("teams: token audience does not name this bot (confused-deputy reject)")
	}
	// exp MUST be present (go-jose does not require it) and within skew; nbf within skew.
	if std.Expiry == nil {
		return Claims{}, fmt.Errorf("teams: token has no exp claim")
	}
	if now.After(std.Expiry.Time().Add(v.skew)) {
		return Claims{}, fmt.Errorf("teams: token expired")
	}
	if std.NotBefore != nil && now.Before(std.NotBefore.Time().Add(-v.skew)) {
		return Claims{}, fmt.Errorf("teams: token not yet valid (nbf)")
	}

	// serviceUrl binding (req. 7): the token's serviceUrl claim MUST match the activity's.
	svcClaim := serviceURLClaim(ext)
	if v.requireServiceURL {
		activitySvc := activityServiceURL(body)
		if activitySvc == "" {
			return Claims{}, fmt.Errorf("teams: activity carries no serviceUrl to bind the token to")
		}
		if svcClaim != activitySvc {
			return Claims{}, fmt.Errorf("teams: token serviceUrl claim does not match the activity serviceUrl")
		}
	}

	return Claims{Issuer: std.Issuer, Audience: std.Audience, ServiceURL: svcClaim}, nil
}

// resolveKey returns the signing key for kid from the cache, refreshing the OpenID
// metadata + JWKS when the cache is empty, older than the 24h floor, or missing the kid (a
// rotation), rate-limited by jwksMinRefresh. The fetch runs under the lock; the inbound
// HITL callback path is human-rate, so simple serialization is acceptable.
func (v *Verifier) resolveKey(ctx context.Context, kid string, now time.Time) (*jose.JSONWebKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	fresh := !v.fetchedAt.IsZero() && now.Sub(v.fetchedAt) < jwksRefreshInterval
	if v.keys != nil && fresh {
		if k := keyFromSet(v.keys, kid); k != nil {
			return k, nil
		}
	}
	// Need a (re)fetch: stale, empty, or unknown kid. Rate-limit forced refreshes.
	if v.keys != nil && fresh && !v.lastAttempt.IsZero() && now.Sub(v.lastAttempt) < jwksMinRefresh {
		if k := keyFromSet(v.keys, kid); k != nil {
			return k, nil
		}
		return nil, fmt.Errorf("teams: no signing key for kid %q (refresh rate-limited)", kid)
	}
	v.lastAttempt = now
	if err := v.refresh(ctx); err != nil {
		// On a refresh failure, fall back to a still-usable cached key rather than fail a
		// valid token on a transient metadata outage.
		if k := keyFromSet(v.keys, kid); k != nil {
			return k, nil
		}
		return nil, err
	}
	v.fetchedAt = now
	if k := keyFromSet(v.keys, kid); k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("teams: no signing key for kid %q in the Bot Framework JWKS", kid)
}

// refresh fetches the OpenID metadata (for the current jwks_uri) and then the JWKS.
func (v *Verifier) refresh(ctx context.Context) error {
	var meta struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := v.fetchJSON(ctx, v.metadataURL, &meta); err != nil {
		return fmt.Errorf("teams: fetch OpenID metadata: %w", err)
	}
	if strings.TrimSpace(meta.JWKSURI) == "" {
		return fmt.Errorf("teams: OpenID metadata has no jwks_uri")
	}
	// The jwks_uri comes from the (possibly compromised) metadata document — even an https
	// metadata URL could point the KEY fetch at cleartext. Enforce https here too before
	// trusting it as the signing-key source.
	if err := requireSecureURL(meta.JWKSURI); err != nil {
		return err
	}
	v.jwksURL = meta.JWKSURI
	var set jose.JSONWebKeySet
	if err := v.fetchJSON(ctx, v.jwksURL, &set); err != nil {
		return fmt.Errorf("teams: fetch JWKS: %w", err)
	}
	if len(set.Keys) == 0 {
		return fmt.Errorf("teams: Bot Framework JWKS is empty")
	}
	v.keys = &set
	return nil
}

// fetchJSON GETs url and decodes the JSON body into out.
func (v *Verifier) fetchJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.doer.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxMetaBody))
	return json.Unmarshal(body, out)
}

// keyFromSet returns the verification key for kid (kid match first, then a single-key
// fallback for a set that omits kids), or nil.
func keyFromSet(set *jose.JSONWebKeySet, kid string) *jose.JSONWebKey {
	if set == nil {
		return nil
	}
	if kid != "" {
		if ks := set.Key(kid); len(ks) > 0 {
			return &ks[0]
		}
	}
	if len(set.Keys) == 1 {
		return &set.Keys[0]
	}
	return nil
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header (scheme
// match is case-insensitive per RFC 7235).
func bearerToken(h string) string {
	h = strings.TrimSpace(h)
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// serviceURLClaim reads the Bot Framework serviceUrl claim, which the SDK emits as the
// lowercase "serviceurl"; "serviceUrl" is accepted as a fallback.
func serviceURLClaim(ext map[string]json.RawMessage) string {
	for _, k := range []string{"serviceurl", "serviceUrl"} {
		if raw, ok := ext[k]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil && s != "" {
				return s
			}
		}
	}
	return ""
}

// activityServiceURL extracts the root serviceUrl from the inbound activity body.
func activityServiceURL(body []byte) string {
	var a struct {
		ServiceURL string `json:"serviceUrl"`
	}
	_ = json.Unmarshal(body, &a)
	return strings.TrimSpace(a.ServiceURL)
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
