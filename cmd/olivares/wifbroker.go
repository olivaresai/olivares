// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	claudewif "github.com/olivaresai/olivares/connectors/claude-wif"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/connectors/spiffe"
	"github.com/olivaresai/olivares/core/model"
	executor "github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/modules/sessions"
)

// wifbroker.go is the in-process Workload Identity Federation credential broker:
// the AGPL composition-root glue the license boundary forbids the Apache connectors from
// doing themselves. It joins connectors/spiffe (the attested JWT-SVID assertion from the
// local SPIRE Workload API) to connectors/claude-wif's Exchanger (the RFC 7523 jwt-bearer
// exchange that mints a short-lived sk-ant-oat) and resolves the PER-TENANT federation rule
// from the wifGraphAdapter, so a governed Claude Code launch (sessions plane) or a deploy/
// sandbox actuation (executor plane) authenticates as an attested, short-lived workload
// identity instead of carrying the static token-in-file an external attester rotates. This
// removes the Vault Agent / spiffe-helper sidecar from the operator (we fetch the SVID and
// run the exchange in-process), the "finish WIF open" adoption win — only the hosted
// multi-tenant broker is a paid (cloud) concern.
//
// VERIFIED INJECTION PATH (decision 2026-06-19, doc-backed): the minted sk-ant-oat is
// injected as ANTHROPIC_AUTH_TOKEN — the env var Claude Code reads "Custom value for the
// Authorization header (the value you set here will be prefixed with Bearer )"
// (code.claude.com/docs/en/env-vars), and the WIF token response says "Pass it as
// Authorization: Bearer <token>" (platform.claude.com/docs/en/manage-claude/wif-reference).
// We host-mint (rather than handing the CLI a raw assertion via ANTHROPIC_IDENTITY_TOKEN_FILE
// + the federation env vars, which the CLI also honors) so the EXCHANGE stays under Olivares
// governance: per-tenant rule resolution, the ledger audit (MintedToken.Audit(), no secret),
// the scope ceiling, and a deny-closed posture. The sessions module already injects
// Credential.Token as ANTHROPIC_AUTH_TOKEN (runtime_bridge.go); this broker only supplies the
// token. ANTHROPIC_BASE_URL (the gateway) is untouched.
//
// HONESTY (docs/SECURITY-HARDENING.md): governed credential injection is VERIFIED-DEPLOYED, not an unbreakable
// seal. The enforcement we DO make at launch is real: the procRunner's env allowlist strips
// host ANTHROPIC_*/CLAUDE_CODE_* and our injected ANTHROPIC_AUTH_TOKEN (+ ANTHROPIC_BASE_URL)
// is authoritative for the child — a host env var cannot shadow it. What it is NOT is a
// sandbox: a process that re-sets ANTHROPIC_BASE_URL/ANTHROPIC_API_KEY for its OWN subprocesses
// (a tool or shell the agent runs inside the workspace), or an operator misconfiguration, can
// still route around it — in-session tool-calls are governed by the managed PEP, not by
// this env injection. Every governance decision HERE is DENY-CLOSED: a missing assertion, an
// unresolved/ambiguous rule, or an exchange rejection returns an error — never a default
// credential and never a silent downgrade to the static file path.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): the minted AccessToken is a SECRET held in memory only for the
// launch (and cached for re-use until just before expiry); it is NEVER logged or persisted.
// Only the non-secret credential id, scheme, NotAfter and the ExchangeAudit reach a log/ledger.

// WIF broker environment (deny-closed; the broker is opt-in per plane — sessions via
// OLIVARES_SESSION_RUNTIME_WIF, executor via the credential kind "wif").
const (
	// envWIFBaseURL overrides the WIF exchange endpoint (default api.anthropic.com via the
	// connector). The exchange mints the OAuth token; it is NOT the inference gateway.
	envWIFBaseURL = "OLIVARES_WIF_BASE_URL"
	// envWIFSpiffeSocket overrides the SPIRE Workload API address; empty uses the
	// SPIFFE-standard SPIFFE_ENDPOINT_SOCKET. No endpoint => mint is deny-closed.
	envWIFSpiffeSocket = "OLIVARES_WIF_SPIFFE_SOCKET"
	// envWIFTrustDomain is the optional home trust domain for the Workload client.
	envWIFTrustDomain = "OLIVARES_WIF_TRUST_DOMAIN"
	// envWIFRefreshSlack re-mints this long before NotAfter (no refresh token on the WIF
	// path => re-run the exchange). Default 60s; clamped to never exceed half a token's
	// lifetime so a short (e.g. 60s) token stays usable.
	envWIFRefreshSlack = "OLIVARES_WIF_REFRESH_SLACK"
	// envSessionRuntimeWIF opts the SESSIONS plane into the in-process WIF broker (else the
	// rotated token-file compat path stays the default).
	envSessionRuntimeWIF = "OLIVARES_SESSION_RUNTIME_WIF"
	// envSessionWIFRule optionally pins WHICH declared rule the sessions plane mints under
	// (required only when a tenant declares more than one federation rule).
	envSessionWIFRule = "OLIVARES_SESSION_RUNTIME_WIF_RULE"
)

const (
	wifScheme              = "wif"
	defaultWIFRefreshSlack = 60 * time.Second
	minWIFRefreshSlack     = 5 * time.Second
)

// assertionMinter produces a freshly-attested JWT-SVID assertion for an audience. The
// production implementation lazily dials the local SPIRE Workload API; tests inject a fake.
type assertionMinter interface {
	mint(ctx context.Context, audience string) (string, error)
}

// federationResolver resolves a tenant to its DECLARED WIF exchange targets. The
// wifGraphAdapter satisfies it; tests inject a fake.
type federationResolver interface {
	FederationExchangeParams(tenant model.TenantID) ([]claudewif.ExchangeParams, bool)
}

// wifCredentialBroker mints and caches short-lived sk-ant-oat inference credentials via the
// claude-wif WIF exchange. It is safe for concurrent use across both planes; a minted token
// is cached per (tenant, rule) and re-exchanged before NotAfter.
type wifCredentialBroker struct {
	exch    *claudewif.Exchanger
	assert  assertionMinter
	resolve federationResolver
	log     *slog.Logger

	now   func() time.Time // injectable clock (tests); production = time.Now
	slack time.Duration

	mu      sync.Mutex
	entries map[wifCacheKey]*wifCacheEntry
}

type wifCacheKey struct {
	tenant model.TenantID
	rule   string
}

// wifCacheEntry is the per-(tenant,rule) cached mint. Its own mutex serializes mints for
// that key (effectively single-flight), so a burst of launches triggers ONE exchange and
// the rest re-use the cached token; distinct keys mint concurrently.
type wifCacheEntry struct {
	mu         sync.Mutex
	cred       brokeredCredential
	obtainedAt time.Time
	set        bool
}

// brokeredCredential is the broker's neutral mint result. token is the SECRET sk-ant-oat
// (used transiently, never logged/persisted); the rest is non-secret and ledger-safe.
type brokeredCredential struct {
	id       string
	token    string
	scheme   string
	notAfter time.Time
}

// newWIFCredentialBroker builds the broker from the environment. It performs NO I/O here
// (the SPIRE Workload API is dialed lazily on first mint), so constructing it when WIF is
// not opted in anywhere is free — the per-plane adapters gate actual use. doer is the
// shared trace-instrumented HTTP transport (reused so the exchange is traced like the rest
// of the engine→Claude hop); a nil doer makes the connector use its default client.
func newWIFCredentialBroker(getenv func(string) string, resolve federationResolver, doer modelprovider.Doer, log *slog.Logger) *wifCredentialBroker {
	slack := defaultWIFRefreshSlack
	if raw := strings.TrimSpace(getenv(envWIFRefreshSlack)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d >= minWIFRefreshSlack {
			slack = d
		}
	}
	return &wifCredentialBroker{
		exch:    claudewif.NewExchanger(strings.TrimSpace(getenv(envWIFBaseURL)), doer),
		assert:  newSpiffeAssertionMinter(getenv, log),
		resolve: resolve,
		log:     log,
		now:     time.Now,
		slack:   slack,
		entries: map[wifCacheKey]*wifCacheEntry{},
	}
}

// sessionSource adapts the broker to the sessions CredentialSource seam (opt-in via
// OLIVARES_SESSION_RUNTIME_WIF). It mints under the LAUNCH's tenant; ruleHint disambiguates
// when the tenant declares more than one rule. Deny-closed: any failure returns an error,
// which fails the stream-json launch closed (the module has no static fallback).
func (b *wifCredentialBroker) sessionSource(ruleHint string) sessions.CredentialSource {
	return sessions.CredentialSourceFunc(func(ctx context.Context, req sessions.CredentialRequest) (sessions.Credential, error) {
		c, err := b.mint(ctx, req.Tenant, ruleHint)
		if err != nil {
			return sessions.Credential{}, err
		}
		return sessions.Credential{ID: c.id, Token: c.token, Scheme: c.scheme, NotAfter: c.notAfter}, nil
	})
}

// executorSource adapts the broker to the executor CredentialSource seam (opt-in via the
// deploy executor credential kind "wif"). The executor MintRequest carries no tenant, so the
// operator names the tenant (and optionally the rule) in the credential config. Deny-closed.
func (b *wifCredentialBroker) executorSource(tenant model.TenantID, ruleHint string) executor.CredentialSource {
	return executor.MintFunc(func(ctx context.Context, _ executor.MintRequest) (executor.Credential, error) {
		c, err := b.mint(ctx, tenant, ruleHint)
		if err != nil {
			return executor.Credential{}, err
		}
		return executor.Credential{ID: c.id, Token: c.token, Scheme: c.scheme, NotAfter: c.notAfter}, nil
	})
}

// mint resolves the tenant's federation rule, returns a cached-still-fresh token, or runs a
// fresh exchange (and caches it). Fail-closed on every error path.
func (b *wifCredentialBroker) mint(ctx context.Context, tenant model.TenantID, ruleHint string) (brokeredCredential, error) {
	params, err := b.resolveTarget(tenant, ruleHint)
	if err != nil {
		return brokeredCredential{}, err
	}
	entry := b.entryFor(wifCacheKey{tenant: tenant, rule: params.FederationRuleID})

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.set && b.fresh(entry) {
		return entry.cred, nil
	}

	tok, err := b.exchange(ctx, params)
	if err != nil {
		return brokeredCredential{}, err
	}
	obtained := b.now()
	cred := brokeredCredential{
		id:       wifScheme + ":" + params.FederationRuleID + ":" + tokenFingerprint(tok.AccessToken),
		token:    tok.AccessToken,
		scheme:   wifScheme,
		notAfter: obtained.Add(time.Duration(tok.ExpiresIn) * time.Second),
	}
	entry.cred, entry.obtainedAt, entry.set = cred, obtained, true

	if b.log != nil {
		a := tok.Audit() // non-secret provenance; NEVER the token
		b.log.Info("wif: minted ephemeral inference credential",
			"tenant", tenant.String(), "credential_id", cred.id,
			"federation_rule", a.FederationRuleID, "organization", orDash(a.OrganizationID),
			"service_account", a.ServiceAccountID, "workspace", orDash(a.WorkspaceID),
			"scope", orDash(a.Scope), "token_type", a.TokenType,
			"not_after", cred.notAfter.UTC().Format(time.RFC3339))
	}
	return cred, nil
}

// resolveTarget picks the exchange target for a tenant: the rule matching ruleHint, or the
// sole declared rule when none is pinned. Zero declared rules, an unknown hint, or an
// ambiguous (>1) set with no hint are all deny-closed.
func (b *wifCredentialBroker) resolveTarget(tenant model.TenantID, ruleHint string) (claudewif.ExchangeParams, error) {
	if tenant.IsZero() {
		return claudewif.ExchangeParams{}, errors.New("wif: empty tenant; cannot resolve a federation rule (deny-closed)")
	}
	targets, ok := b.resolve.FederationExchangeParams(tenant)
	if !ok || len(targets) == 0 {
		return claudewif.ExchangeParams{}, fmt.Errorf("wif: no federation rule declared for tenant %s (deny-closed)", tenant)
	}
	if ruleHint = strings.TrimSpace(ruleHint); ruleHint != "" {
		for _, t := range targets {
			if t.FederationRuleID == ruleHint {
				return t, nil
			}
		}
		return claudewif.ExchangeParams{}, fmt.Errorf("wif: federation rule %q is not declared for tenant %s (deny-closed)", ruleHint, tenant)
	}
	if len(targets) == 1 {
		return targets[0], nil
	}
	return claudewif.ExchangeParams{}, fmt.Errorf("wif: tenant %s declares %d federation rules; set an explicit rule id (deny-closed)", tenant, len(targets))
}

// exchange fetches a fresh attested assertion and runs the WIF exchange. Both halves are
// deny-closed; a non-positive lifetime is rejected (an unusable credential, not a default).
func (b *wifCredentialBroker) exchange(ctx context.Context, params claudewif.ExchangeParams) (claudewif.MintedToken, error) {
	assertion, err := b.assert.mint(ctx, spiffe.AnthropicAudience)
	if err != nil {
		return claudewif.MintedToken{}, fmt.Errorf("wif: attested assertion unavailable (deny-closed): %w", err)
	}
	tok, err := b.exch.Exchange(ctx, assertion, params)
	if err != nil {
		return claudewif.MintedToken{}, fmt.Errorf("wif: exchange rejected (deny-closed): %w", err)
	}
	if tok.AccessToken == "" {
		return claudewif.MintedToken{}, errors.New("wif: exchange returned no access token (deny-closed)")
	}
	if tok.ExpiresIn <= 0 {
		return claudewif.MintedToken{}, errors.New("wif: exchange returned a non-positive token lifetime (deny-closed)")
	}
	return tok, nil
}

// fresh reports whether a cached token is still usable accounting for the refresh slack
// (capped at half the token's lifetime so a short token is not perpetually "stale").
func (b *wifCredentialBroker) fresh(e *wifCacheEntry) bool {
	exp := e.cred.notAfter
	if exp.IsZero() {
		return false
	}
	slack := b.slack
	if life := exp.Sub(e.obtainedAt); life > 0 && slack > life/2 {
		slack = life / 2
	}
	return b.now().Before(exp.Add(-slack))
}

// entryFor returns the cache entry for a key, creating it under the broker mutex. The
// per-entry mutex (not held here) serializes the actual mint for that key.
func (b *wifCredentialBroker) entryFor(key wifCacheKey) *wifCacheEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[key]
	if e == nil {
		e = &wifCacheEntry{}
		b.entries[key] = e
	}
	return e
}

// Close releases the lazily-dialed SPIRE Workload API connection (nil-safe; a no-op when the
// broker never minted). Called from engine.Close on shutdown.
func (b *wifCredentialBroker) Close() error {
	if b == nil {
		return nil
	}
	if c, ok := b.assert.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

// tokenFingerprint is a short, non-reversible SHA-256 prefix used ONLY as a non-secret audit
// correlation id for a minted token (mirrors executor.fileTokenSource) — never the token.
func tokenFingerprint(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])[:12]
}

// spiffeAssertionMinter is the production assertionMinter: it lazily dials the local SPIRE
// Workload API on first use, then mints a JWT-SVID per call (a fresh, short-lived assertion;
// there is no refresh token for the downstream exchange). It holds the SVID in memory only
// and never logs or persists it (docs/SECURITY-HARDENING.md).
type spiffeAssertionMinter struct {
	socket string
	td     string
	log    *slog.Logger
	// dial is spiffe.Dial in production (injectable in tests).
	dial func(ctx context.Context, cfg spiffe.WorkloadConfig) (*spiffe.Workload, error)

	mu sync.Mutex
	wl *spiffe.Workload
}

func newSpiffeAssertionMinter(getenv func(string) string, log *slog.Logger) *spiffeAssertionMinter {
	return &spiffeAssertionMinter{
		socket: strings.TrimSpace(getenv(envWIFSpiffeSocket)),
		td:     strings.TrimSpace(getenv(envWIFTrustDomain)),
		log:    log,
		dial:   spiffe.Dial,
	}
}

// mint fetches a JWT-SVID for the audience and pre-flights it locally (subject is a SPIFFE
// ID, audience contains the Anthropic audience) so a misconfigured audience is a clear local
// error, not an opaque server-side invalid_grant. We use the raw fetch (not the connector's
// FetchAnthropicAssertion) deliberately: that helper's static-key-shadowing guard inspects
// the PROCESS env, but the control plane may legitimately hold ANTHROPIC_API_KEY for its own
// FinOps/inference reads — the guard is for a workload that USES the SDK precedence, which we
// do not (the child gets a sanitized env + our injected bearer).
func (s *spiffeAssertionMinter) mint(ctx context.Context, audience string) (string, error) {
	w, err := s.workload(ctx)
	if err != nil {
		return "", err
	}
	svid, err := w.FetchJWTSVID(ctx, audience)
	if err != nil {
		return "", fmt.Errorf("fetch JWT-SVID: %w", err)
	}
	token := svid.Marshal()
	if _, err := spiffe.InspectAnthropicAssertion(token); err != nil {
		return "", err
	}
	return token, nil
}

// workload returns the dialed Workload, dialing once under the mutex. A prior failure is NOT
// cached (a transient SPIRE outage at first launch recovers on the next mint). No configured
// endpoint is deny-closed with a clear, actionable error.
func (s *spiffeAssertionMinter) workload(ctx context.Context) (*spiffe.Workload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wl != nil {
		return s.wl, nil
	}
	w, err := s.dial(ctx, spiffe.WorkloadConfig{SocketAddr: s.socket, TrustDomain: s.td})
	if err != nil {
		if errors.Is(err, spiffe.ErrNoWorkloadAPI) {
			return nil, errors.New("no SPIRE Workload API endpoint (set OLIVARES_WIF_SPIFFE_SOCKET or SPIFFE_ENDPOINT_SOCKET); WIF mint deny-closed")
		}
		return nil, fmt.Errorf("dial SPIRE Workload API: %w", err)
	}
	s.wl = w
	return w, nil
}

// Close releases the Workload API connection (safe to call once; no-op if never dialed).
func (s *spiffeAssertionMinter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wl == nil {
		return nil
	}
	err := s.wl.Close()
	s.wl = nil
	return err
}
