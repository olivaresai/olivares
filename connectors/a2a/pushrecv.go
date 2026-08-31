// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

// pushrecv.go is the A2A v1.0 push-notification RECEIVER (AIP-05 §c): the inbound
// webhook a remote agent POSTs Task lifecycle updates to. A2A's enterprise model
// requires the SERVER to authenticate to the client's webhook with an asymmetric JWT
// (JWKS-verifiable, iss/aud), so the receiver is a real attack surface and is built
// FAIL-CLOSED:
//
//   - the inbound JWT is verified against an operator trust anchor (inline JWKS, or a
//     JWKS URL fetched through an SSRF-guarded client), pinned to asymmetric algs
//     (HMAC/"none" rejected by omission);
//   - iss MUST be on the operator domain allowlist; aud MUST be this webhook's
//     audience; exp/nbf/iat are checked with bounded skew;
//   - jti is replay-cached (a re-presented token is rejected), so a captured push
//     cannot be replayed;
//   - the body is parsed to a minimal Task reference + state only (never artifacts or
//     text, docs/SECURITY-HARDENING.md).
//
// Any failure → 401/403 and nothing reaches onUpdate. It is the honest evolution of
// lifecycle OBSERVATION (it watches TASK_STATE_* transitions without sitting in any
// request data path), wired by the composition root; the connector owns the
// verification, the cmd owns the mount + the onUpdate handler that records edges.

// maxPushBody caps a push-notification request body.
const maxPushBody = 1 << 20 // 1 MiB

// defaultReplayTTL bounds how long a jti is remembered for replay defense. It should
// comfortably exceed the token's validity window; a token presented after it expires
// is rejected by the exp check regardless.
const defaultReplayTTL = 10 * time.Minute

// TaskUpdate is a minimal-data Task lifecycle update delivered by a verified push. It
// carries references + FSM state only — no artifacts, no message text (docs/SECURITY-HARDENING.md).
type TaskUpdate struct {
	TaskID    string
	ContextID string
	State     TaskState
	Interrupt bool
	Terminal  bool
	Sender    string // the verified token issuer (iss), for attribution
	// ReplayID and ReplayExpiresAt are verified JWT claims passed only to the
	// durable composition callback. They must never be logged or persisted raw.
	ReplayID        string    `json:"-"`
	ReplayExpiresAt time.Time `json:"-"`
}

// ErrReplay is returned by a durable replay authority when a verified provider
// token has already been committed. The HTTP boundary maps it to 401 without
// exposing store details.
var ErrReplay = errors.New("a2a: provider replay")

// PushReceiverConfig configures the inbound push receiver. Three controls are REQUIRED
// (a receiver missing any of them is never constructed): Audience (this webhook's
// expected aud — an empty audience would accept tokens minted for any resource); a
// trust anchor — IssuerJWKS (inline JWK Set, safest, no fetch) OR JWKSURL (SSRF-guarded
// fetch); and AllowedIssuers (the issuer allowlist — every accepted token's iss MUST be
// on it, else any issuer with a trusted key could impersonate another). OnUpdate
// receives each verified update.
type PushReceiverConfig struct {
	Audience       string
	IssuerJWKS     []byte
	JWKSURL        string
	AllowedIssuers []string
	OnUpdate       func(context.Context, TaskUpdate)
	OnReply        func(context.Context, ReplyEvent)
	// OnUpdateDurable is the K5 settlement seam. When configured, a recognized
	// lifecycle update is acknowledged to the peer only after this callback has
	// durably recorded it. An error returns 503 so the sender can retry with a new
	// notification token; OnUpdate remains an optional post-settlement observer.
	OnUpdateDurable func(context.Context, TaskUpdate) error
	// OnReplyDurable is the equivalent commit-before-ack seam for Message and
	// artifactUpdate values. The callback receives only the bounded projection.
	OnReplyDurable func(context.Context, ReplyEvent) error
	ReplayTTL      time.Duration
	Clock          func() time.Time
	Doer           httpGetter // injected in tests; nil => SSRF-guarded client

	// RequireClientAttestation turns on the OPTIONAL runtime-admission gate:
	// every inbound push must additionally carry a valid OAuth-Client-Attestation /
	// OAuth-Client-Attestation-PoP pair (draft-ietf-oauth-attestation-based-client-
	// auth-09 — a DRAFT, hence policy opt-in, default off) verified against
	// AttesterJWKS, the operator's trust anchor of Client Attester keys. Deny-closed
	// when enabled: enabling it without an attester anchor fails construction, and
	// any attestation failure is a 401 before push-token verification. Disabling it
	// never weakens the always-on push JWT verification.
	RequireClientAttestation bool
	AttesterJWKS             []byte
}

// httpGetter is the minimal HTTP interface the JWKS fetch needs (satisfied by
// *http.Client).
type httpGetter interface {
	Do(*http.Request) (*http.Response, error)
}

// PushReceiver is the inbound A2A push-notification webhook handler. Build it with
// NewPushReceiver; it implements http.Handler and is safe for concurrent use.
type PushReceiver struct {
	audience        string
	allowedIssuers  map[string]struct{}
	anchor          *jose.JSONWebKeySet
	jwksURL         string
	onUpdate        func(context.Context, TaskUpdate)
	onUpdateDurable func(context.Context, TaskUpdate) error
	onReply         func(context.Context, ReplyEvent)
	onReplyDurable  func(context.Context, ReplyEvent) error
	doer            httpGetter
	now             func() time.Time
	replay          *replayCache
	attest          *attestVerifier // non-nil only when RequireClientAttestation
}

// NewPushReceiver builds a push receiver from config. It returns an error for a
// missing audience or a missing trust anchor (no inline JWKS and no JWKS URL) — a
// receiver that cannot verify is never silently constructed open.
func NewPushReceiver(cfg PushReceiverConfig) (*PushReceiver, error) {
	if strings.TrimSpace(cfg.Audience) == "" {
		return nil, fmt.Errorf("a2a: push receiver requires an audience (the webhook's expected aud)")
	}
	if len(cfg.IssuerJWKS) == 0 && strings.TrimSpace(cfg.JWKSURL) == "" {
		return nil, fmt.Errorf("a2a: push receiver requires a trust anchor (inline issuer_jwks or jwks_url)")
	}
	r := &PushReceiver{
		audience:        cfg.Audience,
		allowedIssuers:  map[string]struct{}{},
		jwksURL:         strings.TrimSpace(cfg.JWKSURL),
		onUpdate:        cfg.OnUpdate,
		onUpdateDurable: cfg.OnUpdateDurable,
		onReply:         cfg.OnReply,
		onReplyDurable:  cfg.OnReplyDurable,
		doer:            cfg.Doer,
		now:             cfg.Clock,
	}
	for _, iss := range cfg.AllowedIssuers {
		if iss = strings.TrimSpace(iss); iss != "" {
			r.allowedIssuers[iss] = struct{}{}
		}
	}
	// FAIL-CLOSED: an issuer allowlist is REQUIRED (the design guarantee is "iss MUST be
	// on the operator allowlist"). Without it, ANY issuer whose key is in the trust
	// anchor would be accepted — a federation/shared-key impersonation. A receiver that
	// cannot pin the issuer is never silently constructed.
	if len(r.allowedIssuers) == 0 {
		return nil, fmt.Errorf("a2a: push receiver requires a non-empty allowed_issuers allowlist (every accepted push token's iss MUST be on it)")
	}
	if len(cfg.IssuerJWKS) > 0 {
		set, err := parseJWKS(cfg.IssuerJWKS)
		if err != nil {
			return nil, err
		}
		r.anchor = set
	}
	if r.doer == nil {
		r.doer = pushSSRFClient()
	}
	if r.now == nil {
		r.now = time.Now
	}
	ttl := cfg.ReplayTTL
	if ttl <= 0 {
		ttl = defaultReplayTTL
	}
	r.replay = newReplayCache(ttl, r.now)
	// FAIL-CLOSED: the attestation admission gate cannot be enabled without an
	// attester trust anchor (an anchorless gate could verify nothing).
	if cfg.RequireClientAttestation {
		set, err := parseJWKS(cfg.AttesterJWKS)
		if err != nil {
			return nil, err
		}
		if set == nil || len(set.Keys) == 0 {
			return nil, fmt.Errorf("a2a: push receiver requires attester_jwks when client attestation is required")
		}
		r.attest = &attestVerifier{anchor: set, audience: r.audience, replay: r.replay}
	}
	return r, nil
}

// ServeHTTP verifies a push and, on success, hands a minimal TaskUpdate to onUpdate.
// Every verification failure is a fail-closed 401/403 and nothing reaches onUpdate.
func (r *PushReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Runtime-admission gate (opt-in, deny-closed when enabled, attest.go): the
	// sending runtime must prove a trusted attestation BEFORE its notification
	// token is even considered.
	if r.attest != nil {
		if _, err := r.attest.verify(req, r.now()); err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", error_description="client attestation required"`)
			http.Error(w, "client attestation required", http.StatusUnauthorized)
			return
		}
	}
	tok := bearerToken(req.Header.Get("Authorization"))
	if tok == "" {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_request"`)
		http.Error(w, "missing bearer", http.StatusUnauthorized)
		return
	}
	claims, err := r.verifyToken(req.Context(), tok)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if claims.ID == "" {
		http.Error(w, "replay", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, maxPushBody))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	upd, reply, recognized, parseErr := parsePushBody(body, claims.Issuer)
	if parseErr != nil {
		http.Error(w, "invalid push", http.StatusBadRequest)
		return
	}
	if !recognized {
		http.Error(w, "unrecognized push", http.StatusBadRequest)
		return
	}
	// The durable callback for the recognized variant is the replay authority.
	// Observe-only deployments retain the bounded process cache. Admission occurs
	// after projection so a durable failure never burns the sender's retry.
	if reply != nil {
		reply.ReplayID = claims.ID
		reply.ReplayExpiresAt = timeOfDate(claims.Expiry)
		if r.onReplyDurable != nil {
			if err := r.onReplyDurable(req.Context(), *reply); err != nil {
				r.writeDurableError(w, err)
				return
			}
		} else if !r.replay.admit(claims.ID, timeOfDate(claims.Expiry)) {
			http.Error(w, "replay", http.StatusUnauthorized)
			return
		}
		if r.onReply != nil {
			r.onReply(req.Context(), *reply)
		}
	} else {
		upd.ReplayID = claims.ID
		upd.ReplayExpiresAt = timeOfDate(claims.Expiry)
		if r.onUpdateDurable != nil {
			if err := r.onUpdateDurable(req.Context(), upd); err != nil {
				r.writeDurableError(w, err)
				return
			}
		} else if !r.replay.admit(claims.ID, timeOfDate(claims.Expiry)) {
			http.Error(w, "replay", http.StatusUnauthorized)
			return
		}
		if r.onUpdate != nil {
			r.onUpdate(req.Context(), upd)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *PushReceiver) writeDurableError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrReplay) {
		http.Error(w, "replay", http.StatusUnauthorized)
		return
	}
	http.Error(w, "update not durably recorded", http.StatusServiceUnavailable)
}

// verifyToken parses + verifies the inbound JWT fail-closed: asymmetric algs only,
// signature against the trust anchor (by kid), iss on the allowlist, aud == this
// webhook, and the validity window with bounded skew. It returns the validated claims
// or an error (never partial trust).
func (r *PushReceiver) verifyToken(ctx context.Context, token string) (jwt.Claims, error) {
	parsed, err := jwt.ParseSigned(token, a2aAllowedAlgs)
	if err != nil {
		return jwt.Claims{}, fmt.Errorf("a2a: push token parse: %w", err)
	}
	if len(parsed.Headers) == 0 {
		return jwt.Claims{}, fmt.Errorf("a2a: push token has no JOSE header")
	}
	kid := parsed.Headers[0].KeyID
	key, err := r.resolveKey(ctx, kid)
	if err != nil {
		return jwt.Claims{}, err
	}
	var claims jwt.Claims
	if err := parsed.Claims(key, &claims); err != nil {
		return jwt.Claims{}, fmt.Errorf("a2a: push token signature: %w", err)
	}
	// iss MUST be on the operator allowlist (the allowlist is required at construction,
	// so this check is unconditional — fail-closed: an unknown issuer is always rejected).
	if _, ok := r.allowedIssuers[claims.Issuer]; !ok {
		return jwt.Claims{}, fmt.Errorf("a2a: push token issuer %q not allowed", claims.Issuer)
	}
	// exp is REQUIRED (go-jose does not demand it): a token without an expiry would
	// outlive the replay cache's retention and become replayable once its jti is
	// evicted — the validity window must bound the replay window, fail-closed.
	if claims.Expiry == nil {
		return jwt.Claims{}, fmt.Errorf("a2a: push token has no exp")
	}
	// aud MUST name THIS webhook; exp/nbf/iat checked with a small skew leeway.
	if err := claims.Validate(jwt.Expected{
		AnyAudience: jwt.Audience{r.audience},
		Time:        r.now(),
	}); err != nil {
		return jwt.Claims{}, fmt.Errorf("a2a: push token claims: %w", err)
	}
	return claims, nil
}

// resolveKey finds the verification key for kid: the inline anchor first, then a
// (SSRF-guarded) JWKS fetch on a miss so a rotated signing key is picked up. A keyset
// with a single key and no kid in the header falls back to that key.
func (r *PushReceiver) resolveKey(ctx context.Context, kid string) (*jose.JSONWebKey, error) {
	if k := lookupAnchorKey(r.anchor, kid); k != nil {
		return k, nil
	}
	if r.jwksURL != "" {
		set, err := r.fetchJWKS(ctx)
		if err != nil {
			return nil, err
		}
		r.anchor = set
		if k := lookupAnchorKey(r.anchor, kid); k != nil {
			return k, nil
		}
	}
	return nil, fmt.Errorf("a2a: no push-trust key for kid %q", kid)
}

// fetchJWKS retrieves the JWK Set from the configured URL through the SSRF-guarded
// client (HTTPS, no reserved IPs, dial-time re-check).
func (r *PushReceiver) fetchJWKS(ctx context.Context) (*jose.JSONWebKeySet, error) {
	if err := requireHTTPS(r.jwksURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.jwksURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("a2a: fetch push jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxPushBody))
		return nil, fmt.Errorf("a2a: push jwks http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPushBody))
	if err != nil {
		return nil, err
	}
	return parseJWKS(body)
}

// lookupAnchorKey returns the verification key for kid from set (kid match first, then
// a single-key fallback), or nil. Mirrors verifyAgainstSet's kid-then-any selection
// but returns the key for jwt.Claims verification.
func lookupAnchorKey(set *jose.JSONWebKeySet, kid string) *jose.JSONWebKey {
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

// parsePushBody extracts one bounded TaskUpdate or ReplyEvent from a push body. In v1.0 the webhook
// payload is a StreamResponse object (§4.3.3) — exactly one of "task", "message",
// "statusUpdate" or "artifactUpdate" — delivered with Content-Type
// application/a2a+json (errata #1753); a bare Task / status-update object (the
// v0.3.0 webhook shape) is parsed as lenient fallback. Only the reference + state
// are read (minimal data). recognized is false for a body with no A2A shape at all.
func parsePushBody(body []byte, issuer string) (TaskUpdate, *ReplyEvent, bool, error) {
	var sr struct {
		Task           json.RawMessage `json:"task"`
		Message        json.RawMessage `json:"message"`
		StatusUpdate   json.RawMessage `json:"statusUpdate"`
		ArtifactUpdate json.RawMessage `json:"artifactUpdate"`
	}
	if json.Unmarshal(body, &sr) == nil {
		members := 0
		for _, raw := range []json.RawMessage{sr.Task, sr.Message, sr.StatusUpdate, sr.ArtifactUpdate} {
			if len(raw) > 0 {
				members++
			}
		}
		if members > 1 {
			return TaskUpdate{}, nil, true, fmt.Errorf("a2a: push StreamResponse has multiple values")
		}
		switch {
		case len(sr.StatusUpdate) > 0:
			body = sr.StatusUpdate
		case len(sr.Task) > 0:
			body = sr.Task
		case len(sr.Message) > 0:
			reply, err := projectMessageReplyEvent(sr.Message, issuer)
			if err != nil {
				return TaskUpdate{}, nil, true, err
			}
			return TaskUpdate{}, &reply, true, nil
		case len(sr.ArtifactUpdate) > 0:
			reply, err := projectArtifactReplyEvent(sr.ArtifactUpdate, issuer)
			if err != nil {
				return TaskUpdate{}, nil, true, err
			}
			return TaskUpdate{}, &reply, true, nil
		}
	}
	var u struct {
		ID        string `json:"id"`
		TaskID    string `json:"taskId"`
		ContextID string `json:"contextId"`
		Status    struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if json.Unmarshal(body, &u) != nil {
		return TaskUpdate{}, nil, false, nil
	}
	state := TaskState(strings.TrimSpace(u.Status.State))
	if state == "" {
		return TaskUpdate{}, nil, false, nil
	}
	id := u.TaskID
	if id == "" {
		id = u.ID
	}
	return TaskUpdate{
		TaskID:    id,
		ContextID: u.ContextID,
		State:     state,
		Interrupt: taskStateInterrupt(state),
		Terminal:  taskStateTerminal(state),
		Sender:    issuer,
	}, nil, true, nil
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header.
func bearerToken(header string) string {
	h := strings.TrimSpace(header)
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// timeOfDate converts a JWT NumericDate to time.Time (zero for nil).
func timeOfDate(d *jwt.NumericDate) time.Time {
	if d == nil {
		return time.Time{}
	}
	return d.Time()
}

// --- replay cache ---------------------------------------------------------------

// replayCache remembers seen jti values until their expiry (or a fixed TTL when the
// token carries no exp), so a captured push cannot be replayed. It evicts lazily.
type replayCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time
	seen map[string]time.Time
}

func newReplayCache(ttl time.Duration, now func() time.Time) *replayCache {
	return &replayCache{ttl: ttl, now: now, seen: map[string]time.Time{}}
}

// admit records jti and returns true if it was NOT seen before (admit it), false if it
// is a replay within its retention window. exp bounds retention (falls back to ttl).
func (c *replayCache) admit(jti string, exp time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	// Lazy eviction of expired entries (keeps the map bounded).
	for k, until := range c.seen {
		if now.After(until) {
			delete(c.seen, k)
		}
	}
	if until, ok := c.seen[jti]; ok && !now.After(until) {
		return false // replay within retention
	}
	until := now.Add(c.ttl)
	if !exp.IsZero() && exp.After(now) && exp.Before(until) {
		until = exp
	}
	c.seen[jti] = until
	return true
}

// --- SSRF-guarded JWKS client ---------------------------------------------------

// pushSSRFClient is the default HTTP client for a JWKS-URL fetch: it re-checks the
// resolved IP at dial time (closing the DNS-rebinding TOCTOU) and refuses reserved
// ranges. Loopback is allowed for local development. It mirrors the MCP connector's
// ssrfSafeClient so the push receiver does not hand-roll a weaker guard.
func pushSSRFClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("a2a: push jwks: cannot parse dial address %q", address)
			}
			if pushReservedIP(ip) {
				return fmt.Errorf("a2a: push jwks: refusing to dial reserved address %s", ip)
			}
			return nil
		},
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: dialer.DialContext}}
}

// pushReservedIP reports whether ip is a private/link-local/multicast/unspecified
// address an outbound JWKS fetch must never reach (loopback exempted for local dev).
func pushReservedIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return false
	}
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// requireHTTPS enforces HTTPS for an untrusted key-distribution URL (a push JWKS
// URL or a card-supplied jku; loopback host may use http for local dev). RFC 7515
// §4.1.2: a JWK Set retrieval over HTTP GET MUST use TLS.
func requireHTTPS(rawURL string) error {
	u := strings.TrimSpace(rawURL)
	lower := strings.ToLower(u)
	switch {
	case strings.HasPrefix(lower, "https://"):
		return nil
	case strings.HasPrefix(lower, "http://"):
		parsed, err := url.Parse(u)
		if err == nil && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1") {
			return nil
		}
	}
	return fmt.Errorf("a2a: key-set url %q must be https (RFC 7515 §4.1.2)", rawURL)
}
