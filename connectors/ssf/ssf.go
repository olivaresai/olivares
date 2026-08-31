// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package ssf is the Olivares AI receiver for the OpenID Shared Signals Framework
// (SSF 1.0) and the Continuous Access Evaluation Profile (CAEP 1.0) — the
// near-real-time kill-switch for robotic users / agents. It is a
// SET-consumer (a Security Event Token receiver, RFC 8417) that accepts PUSHED SETs
// (RFC 8935) from a configured transmitter (an IdP). When the transmitter revokes
// an agent's session or credential, the receiver reflects it as a governance
// FindingReport that the control plane applies as a kill-switch on the agent's
// observed session/credential (the application is governance's job; this
// connector supplies the verified signal).
//
// CAEP 1.0 (Final): the SET's "events" claim is keyed by the CAEP event-type URI
// (https://schemas.openid.net/secevent/caep/event-type/...). The receiver acts on
// session-revoked and a credential revocation as hard kill-switches (High), and on
// token-claims-change / assurance-level-change / device-compliance-change /
// risk-level-change as re-evaluation signals. Poll delivery (RFC 8936) and the SSF
// stream-configuration management API are a documented post-v1 seam.
//
// Security posture (docs/SECURITY-HARDENING.md): an inbound receiver is the hardened exception
// to the no-listener default. It binds LOOPBACK by default and refuses a
// non-loopback bind unless the operator opts in. Crucially it does not trust the
// network: it acts ONLY on a SET whose signature verifies against the transmitter's
// JWKS and whose audience is bound to this receiver — an unsigned, wrongly-signed,
// alg-confused or mis-audienced SET is REJECTED. The algorithm allow-list is
// asymmetric-only (HMAC and "none" rejected), the anti-downgrade defense. It reads
// only event METADATA (the subject reference, the event type) — never a credential
// value. It imports only the SDK and the shared Apache helpers — never the engine.
package ssf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.ssf"

// defaultListenAddr is the loopback default for the inbound receiver.
const defaultListenAddr = "127.0.0.1:8843"

// defaultPath is the default push-delivery endpoint path.
const defaultPath = "/ssf/events"

// maxSETBytes bounds a received SET body (a SET is small; this protects memory).
const maxSETBytes = 256 << 10

// ssfAllowedAlgs is the asymmetric signature allow-list for a SET. Symmetric
// (HMAC) and "none" are rejected by omission — pinning the allow-list at parse
// time is the defense against an algorithm-confusion downgrade (mirrors the
// SPIFFE verifier).
var ssfAllowedAlgs = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.ES256, jose.ES384, jose.ES512,
	jose.PS256, jose.PS384, jose.PS512,
}

// setLeeway is the clock-skew tolerance applied to a SET's iat freshness check.
const setLeeway = 5 * time.Minute

// Source is the SSF/CAEP receiver. It satisfies sdk.SourceConnector: Gather runs
// the inbound HTTP server and blocks until ctx is canceled, emitting a finding for
// each verified CAEP event.
type Source struct {
	listenAddr      string
	path            string
	allowPublicBind bool
	audience        string // the SET aud MUST contain this (this receiver's id)
	issuer          string // when set, the SET iss MUST equal this transmitter
	jwksURL         string
	doer            httpx.Doer
	now             func() time.Time

	mu     sync.Mutex
	keyset *jose.JSONWebKeySet // cached; re-fetched on a kid miss
	lis    net.Listener        // bound in Open (so a bind error surfaces early)
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns an ssf receiver with default configuration.
func New() *Source { return &Source{listenAddr: defaultListenAddr, path: defaultPath} }

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "OpenID SSF / CAEP receiver (agent kill-switch)",
		Description: "Receives signed Security Event Tokens (SSF push / CAEP 1.0) and applies session-revoked / credential-change as a governance kill-switch. Verifies signature + audience; rejects anything unsigned or mis-audienced.",
		ConfigFields: []sdk.ConfigField{
			{Key: "listen_addr", Type: sdk.FieldString, Default: defaultListenAddr, Description: "Receiver bind address. Loopback by default; a non-loopback bind is refused unless allow_public_bind=true."},
			{Key: "path", Type: sdk.FieldString, Default: defaultPath, Description: "Push-delivery endpoint path."},
			{Key: "allow_public_bind", Type: sdk.FieldBool, Default: "false", Description: "DANGEROUS: allow binding the receiver to a non-loopback address. Keep loopback (secure default); put a TLS/mTLS gateway in front."},
			{Key: "audience", Type: sdk.FieldString, Required: true, Description: "This receiver's audience identifier; a SET whose aud does not include it is rejected (confused-deputy defense)."},
			{Key: "issuer", Type: sdk.FieldString, Description: "Expected transmitter issuer; when set, a SET with a different iss is rejected."},
			{Key: "jwks", Type: sdk.FieldString, Secret: false, Description: "Inline transmitter JWKS (public keys) used to verify the SET signature."},
			{Key: "jwks_url", Type: sdk.FieldString, Description: "Read-only JWKS endpoint of the transmitter (alternative to inline jwks)."},
		},
	}
}

// Open reads configuration, loads the verification keys, and binds the loopback
// listener now (so a bind/permission error surfaces here). A receiver with no key
// source or no audience is a configuration error — an unverifying receiver would
// be a security hole, so it is refused rather than mounted (docs/SECURITY-HARDENING.md).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.listenAddr = firstNonEmpty(strings.TrimSpace(cfg.Get("listen_addr")), defaultListenAddr)
	s.path = firstNonEmpty(strings.TrimSpace(cfg.Get("path")), defaultPath)
	s.allowPublicBind = cfg.GetBool("allow_public_bind", false)
	s.audience = strings.TrimSpace(cfg.Get("audience"))
	s.issuer = strings.TrimSpace(cfg.Get("issuer"))
	s.jwksURL = strings.TrimSpace(cfg.Get("jwks_url"))

	if s.audience == "" {
		return fmt.Errorf("ssf: audience is required (the SET aud is verified against it; an unbound receiver is a confused-deputy hole)")
	}
	if raw := strings.TrimSpace(cfg.Get("jwks")); raw != "" {
		var ks jose.JSONWebKeySet
		if err := json.Unmarshal([]byte(raw), &ks); err != nil {
			return fmt.Errorf("ssf: parse inline jwks: %w", err)
		}
		s.keyset = &ks
	}
	if s.keyset == nil && s.jwksURL == "" {
		return fmt.Errorf("ssf: a verification key source is required (inline jwks or jwks_url); a receiver that cannot verify a SET must not be mounted")
	}
	// One admission point for every socket this product opens. The SET
	// receiver has no TLS of its own; it is meant to sit behind a TLS gateway.
	lis, err := netbind.Listen(context.Background(), "tcp", s.listenAddr, netbind.Policy{
		Component:   "ssf",
		Purpose:     "SET receiver",
		AllowPublic: s.allowPublicBind,
		OptIn:       "allow_public_bind",
	})
	if err != nil {
		return fmt.Errorf("ssf: bind receiver %s: %w", s.listenAddr, err)
	}
	s.lis = lis
	return nil
}

// Gather runs the inbound HTTP receiver and blocks until ctx is canceled, emitting
// a FindingReport for each verified CAEP event. It is a streaming source.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.lis == nil {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc(s.path, s.handle(ctx, sink))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err := srv.Serve(s.lis)
	if err == http.ErrServerClosed {
		return ctx.Err()
	}
	return err
}

// Close releases the listener. http.Server.Serve already closes the listener when
// Gather's Shutdown fires, so a post-Gather Close double-closes it — that is benign
// (net.ErrClosed is swallowed), and Close before Gather still closes cleanly.
func (s *Source) Close(context.Context) error {
	if s.lis != nil {
		if err := s.lis.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return err
		}
	}
	return nil
}

// handle is the push-delivery endpoint. It verifies the SET and emits a finding
// per recognized CAEP event, answering 202 Accepted on success and 400 with an
// SSF error body on a verification failure (RFC 8935 §2.4). A SET that fails
// verification produces NO finding — the kill-switch is never applied on an
// unverified signal.
func (s *Source) handle(emitCtx context.Context, sink sdk.Sink) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxSETBytes))
		if err != nil {
			writeSETError(w, "invalid_request", "cannot read body")
			return
		}
		set, verr := s.verifySET(r.Context(), strings.TrimSpace(string(body)))
		if verr != nil {
			// Do not echo the underlying reason verbatim (avoid an oracle); a fixed code.
			writeSETError(w, "authentication_failed", "SET verification failed")
			return
		}
		emitted := true
		for etype, raw := range set.Events {
			ev, derr := decodeCAEPEvent(raw)
			if derr != nil {
				continue
			}
			f, ok := deriveFinding(etype, ev, set.SubID, set.Iss, set.Jti, s.clock())
			if !ok {
				continue
			}
			if err := sink.Emit(emitCtx, f); err != nil {
				emitted = false
			}
		}
		if !emitted {
			http.Error(w, "downstream unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

// verifySET parses, signature-verifies and audience/issuer-validates a SET. It
// enforces the asymmetric-algorithm allow-list, verifies the signature against the
// transmitter JWKS by kid, requires the configured audience in aud, requires the
// configured issuer (when set) and a non-future iat. It returns the decoded SET on
// success, an error otherwise.
func (s *Source) verifySET(ctx context.Context, token string) (setToken, error) {
	if token == "" {
		return setToken{}, fmt.Errorf("ssf: empty SET")
	}
	jws, err := jose.ParseSigned(token, ssfAllowedAlgs)
	if err != nil {
		return setToken{}, fmt.Errorf("ssf: parse SET: %w", err)
	}
	if len(jws.Signatures) == 0 {
		return setToken{}, fmt.Errorf("ssf: SET has no signature")
	}
	kid := jws.Signatures[0].Header.KeyID
	key, err := s.resolveKey(ctx, kid)
	if err != nil {
		return setToken{}, err
	}
	payload, err := jws.Verify(key)
	if err != nil {
		return setToken{}, fmt.Errorf("ssf: SET signature: %w", err)
	}
	var set setToken
	if err := json.Unmarshal(payload, &set); err != nil {
		return setToken{}, fmt.Errorf("ssf: decode SET claims: %w", err)
	}
	if !set.Aud.contains(s.audience) {
		return setToken{}, fmt.Errorf("ssf: SET audience %v does not include this receiver %q", []string(set.Aud), s.audience)
	}
	if s.issuer != "" && set.Iss != s.issuer {
		return setToken{}, fmt.Errorf("ssf: SET issuer %q != expected transmitter", set.Iss)
	}
	if set.Iat > 0 && time.Unix(set.Iat, 0).After(s.clock().Add(setLeeway)) {
		return setToken{}, fmt.Errorf("ssf: SET issued in the future")
	}
	// A SET exp is optional (RFC 8417), but when present it MUST bound the token:
	// an expired (e.g. replayed) revocation SET must not fire the kill-switch.
	if set.Exp > 0 && s.clock().After(time.Unix(set.Exp, 0).Add(setLeeway)) {
		return setToken{}, fmt.Errorf("ssf: SET expired")
	}
	if len(set.Events) == 0 {
		return setToken{}, fmt.Errorf("ssf: SET carries no events")
	}
	return set, nil
}

// resolveKey finds the verification key for kid in the cached keyset, fetching the
// JWKS URL on a miss (so a rotated signing key is picked up). Concurrency-safe: the
// HTTP server calls it from many goroutines.
func (s *Source) resolveKey(ctx context.Context, kid string) (*jose.JSONWebKey, error) {
	s.mu.Lock()
	if k := lookupKey(s.keyset, kid); k != nil {
		s.mu.Unlock()
		return k, nil
	}
	hasURL := s.jwksURL != ""
	s.mu.Unlock()

	if hasURL {
		ks, err := s.fetchJWKS(ctx)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.keyset = ks
		k := lookupKey(s.keyset, kid)
		s.mu.Unlock()
		if k != nil {
			return k, nil
		}
	}
	return nil, fmt.Errorf("ssf: no key for kid %q in transmitter JWKS", kid)
}

// fetchJWKS GETs the read-only JWKS endpoint and parses it (public key material).
func (s *Source) fetchJWKS(ctx context.Context) (*jose.JSONWebKeySet, error) {
	client := httpx.New(s.jwksURL, s.doer, nil, nil)
	var ks jose.JSONWebKeySet
	if err := client.GetJSON(ctx, "", nil, &ks); err != nil {
		return nil, fmt.Errorf("ssf: fetch jwks: %w", err)
	}
	return &ks, nil
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

// setToken is the decoded Security Event Token claim set (RFC 8417 + SSF). The
// connector reads only the routing claims and the events map; it never reads a
// credential value out of an event payload.
type setToken struct {
	Iss    string                     `json:"iss"`
	Aud    audience                   `json:"aud"`
	Iat    int64                      `json:"iat"`
	Exp    int64                      `json:"exp,omitempty"`
	Jti    string                     `json:"jti"`
	SubID  *subjectID                 `json:"sub_id"`
	Events map[string]json.RawMessage `json:"events"`
}

// audience accepts a SET aud that is either a string or an array of strings.
type audience []string

// UnmarshalJSON decodes a string or a []string into the audience.
func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

func (a audience) contains(v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}

// writeSETError writes the RFC 8935 §2.4 SET error response.
func writeSETError(w http.ResponseWriter, errCode, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"err": errCode, "description": description})
}

// lookupKey finds a key by kid, or the sole key when the set has exactly one and
// no kid was given. Returns nil when no key matches.
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
