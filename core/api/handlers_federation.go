// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// SSO login federation handlers. They drive the auth.Federation seam through the
// SP-initiated round trip while keeping every secret-bearing flow value (the CSRF
// state, the OIDC nonce, the PKCE verifier, the SAML AuthnRequest id) server-side
// in the engine — only the S256 PKCE *challenge* is handed to the provider. The
// provider does the protocol crypto behind the seam; the core owns the flow.
//
// With the default NoFederation provider both endpoints return 501
// sso_not_configured, so the base AGPL build advertises the surface honestly
// without any IdP wired.

// ssoFlowName is the cookie that correlates the start and callback legs.
const ssoFlowName = "olv_sso"

// ssoFlowTTL bounds how long a login may sit between start and callback.
const ssoFlowTTL = 10 * time.Minute

// ssoFlow is the server-side state of one in-progress SSO login.
type ssoFlow struct {
	state       string
	nonce       string
	verifier    string
	requestID   string
	redirectURI string
	returnTo    string
	// scope + alias identify the IdP the start leg actually RESOLVED (U5), so the
	// callback completes against the SAME IdP by (scope, alias) — no drift from re-running
	// domain/priority selection — and the SELECTED scope (not the raw ?tenant= hint) governs
	// SCIM authority (D4) and group reconciliation (U2) in CompleteSSO. For the open build /
	// global login these are (SystemTenantID, "default").
	scope   string
	alias   string
	expires time.Time
}

// ssoFlowStore is a small in-memory store of in-progress SSO logins, keyed by an
// opaque cookie id. Login flows are short-lived and need not survive a restart (a
// dropped flow simply restarts the login), so an in-memory map avoids a schema
// table; entries expire and are swept lazily.
type ssoFlowStore struct {
	mu    sync.Mutex
	flows map[string]ssoFlow
}

func newSSOFlowStore() *ssoFlowStore { return &ssoFlowStore{flows: map[string]ssoFlow{}} }

func (s *ssoFlowStore) put(id string, f ssoFlow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Opportunistic sweep so abandoned flows do not accumulate.
	now := time.Now()
	for k, v := range s.flows {
		if now.After(v.expires) {
			delete(s.flows, k)
		}
	}
	s.flows[id] = f
}

// take returns and removes a flow (single-use), or ok=false if absent/expired.
func (s *ssoFlowStore) take(id string) (ssoFlow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flows[id]
	if !ok {
		return ssoFlow{}, false
	}
	delete(s.flows, id)
	if time.Now().After(f.expires) {
		return ssoFlow{}, false
	}
	return f, true
}

// federation resolves the active SSO provider for a login. When a managed config
// service is wired the provider is store-driven (resolved + cached per
// scope); otherwise it is the static provider injected at construction (the env
// path / NoFederation default). The tenant hint is the optional multi-IdP selector
// (reserved enterprise): the open build ignores it and always resolves the single
// global config; with the MultiIDP capability wired it picks the tenant's IdP.
func (s *Server) federation(r *http.Request, tenant model.TenantID) auth.Federation {
	if s.fedSvc != nil {
		fed, err := s.fedSvc.Resolve(r.Context(), tenant)
		if err != nil || fed == nil {
			return auth.NoFederation{} // fail closed: never 500 the login surface
		}
		return fed
	}
	return s.fed
}

// ssoTenantHint reads the optional multi-IdP tenant selector from the start request. It
// is the reserved enterprise path: a valid tenant UUID selects that tenant's IdP when the
// MultiIDP capability is wired; an absent or malformed hint yields "" (the global
// single-IdP config), so the open build is unaffected.
func ssoTenantHint(r *http.Request) model.TenantID {
	t, err := model.ParseTenantID(r.URL.Query().Get("tenant"))
	if err != nil {
		return ""
	}
	return t
}

// ssoSelection builds the U5 SelectionInput from the start request: the optional
// ?tenant= tenant hint, the home-realm ?domain= (the email domain — the login page extracts
// it client-side so the full address never transits the URL / access logs), and an explicit
// ?idp=<alias>. The domain is normalized to the same canonical form the config stores and
// the selector matches. The open build ignores all of it (single global IdP).
func ssoSelection(r *http.Request) auth.SelectionInput {
	// Populate Alias ONLY when ?idp= is actually present: NormalizeFederationAlias("")
	// returns "default", so normalizing an ABSENT param would make every plain login look
	// like an explicit ?idp=default and preempt the email-domain sub-ladder (a login would
	// always resolve the tenant's "default" IdP, never its domain-claiming sibling).
	alias := ""
	if raw := strings.TrimSpace(r.URL.Query().Get("idp")); raw != "" {
		alias = model.NormalizeFederationAlias(raw)
	}
	return auth.SelectionInput{
		Tenant:      ssoTenantHint(r),
		EmailDomain: model.NormalizeFederationDomain(r.URL.Query().Get("domain")),
		Alias:       alias,
	}
}

// resolveLoginStart resolves the IdP for a starting login and returns the provider plus the
// RESOLVED (scope, alias) to persist. With a managed config service it uses the full U5
// selection ladder (ResolveLogin); without one it falls back to the statically-injected
// provider (env / NoFederation), which is the global "default".
func (s *Server) resolveLoginStart(r *http.Request, in auth.SelectionInput) (auth.Federation, auth.ResolvedIdP) {
	if s.fedSvc != nil {
		return s.fedSvc.ResolveLogin(r.Context(), in)
	}
	return s.fed, auth.ResolvedIdP{Scope: auth.GlobalFederationScope, Alias: model.DefaultFederationAlias}
}

// resolveLoginCallback re-resolves the EXACT IdP the start leg selected, by (scope, alias),
// so the login completes against the same provider it began with and the selected scope
// governs completion. Without a managed config service it uses the static provider.
func (s *Server) resolveLoginCallback(r *http.Request, scope model.TenantID, alias string) (auth.Federation, auth.ResolvedIdP) {
	if s.fedSvc != nil {
		return s.fedSvc.ResolveByAlias(r.Context(), scope, alias)
	}
	return s.fed, auth.ResolvedIdP{Scope: auth.GlobalFederationScope, Alias: model.DefaultFederationAlias}
}

// handleSSOStart begins an SSO login: it generates the single-use flow secrets,
// asks the provider for the IdP redirect, persists the flow under a cookie, and
// 302-redirects the browser to the IdP.
func (s *Server) handleSSOStart(w http.ResponseWriter, r *http.Request) {
	flowID := randToken(16)
	state := randToken(16)
	nonce := randToken(16)
	requestID := "_" + randToken(16) // SAML ids must be NCName (not start with a digit)
	verifier := randToken(32)
	challenge := pkceS256(verifier)
	redirectURI := federationCallbackURL(r)

	fed, resolved := s.resolveLoginStart(r, ssoSelection(r))
	redirect, err := fed.BeginAuth(r.Context(), auth.AuthParams{
		State: state, Nonce: nonce, PKCEChallenge: challenge,
		RedirectURI: redirectURI, RequestID: requestID,
	})
	if err != nil {
		s.writeError(w, r, err) // ErrSSONotConfigured -> 501
		return
	}

	s.sso.put(flowID, ssoFlow{
		state: state, nonce: nonce, verifier: verifier, requestID: requestID,
		redirectURI: redirectURI, returnTo: r.URL.Query().Get("return_to"),
		scope:   resolved.Scope.String(),
		alias:   resolved.Alias,
		expires: s.clock.Now().Time().Add(ssoFlowTTL),
	})
	http.SetCookie(w, &http.Cookie{
		Name: ssoFlowName, Value: flowID, Path: "/v1/auth/federation",
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode,
		MaxAge: int(ssoFlowTTL.Seconds()),
	})
	http.Redirect(w, r, redirect, http.StatusFound)
}

// handleSSOCallback completes an SSO login: it loads the persisted flow, validates
// the assertion through the provider, find/provisions the local user, and mints
// an opaque session (returned like /auth/login).
func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	// Load the in-progress flow first: it carries the multi-IdP tenant hint, so the
	// SAME IdP the start leg used resolves here (including a deployment whose only
	// IdPs are per-tenant, with no global config — the global pre-check this
	// replaced wrongly 501'd that valid login).
	var flow ssoFlow
	var ok bool
	if c, err := r.Cookie(ssoFlowName); err == nil {
		flow, ok = s.sso.take(c.Value)
	}
	if !ok {
		// No valid in-progress flow. With no flow there is no tenant to name, so probe
		// the global provider: advertise 501 when SSO is entirely unconfigured (the
		// honest seam, same as the start leg), else 400 for a callback without a start.
		if s.federation(r, "").Protocol() == "" {
			s.writeError(w, r, auth.ErrSSONotConfigured)
			return
		}
		s.badRequest(w, r, "missing or expired SSO flow")
		return
	}
	// Clear the one-time cookie.
	http.SetCookie(w, &http.Cookie{Name: ssoFlowName, Value: "", Path: "/v1/auth/federation", MaxAge: -1})

	// Re-resolve the EXACT IdP the start leg selected, by (scope, alias), so the login
	// completes against the same provider it began with (no drift) and the SELECTED scope
	// governs completion.
	fed, resolved := s.resolveLoginCallback(r, model.TenantID(flow.scope), flow.alias)
	proto := fed.Protocol()
	if proto == "" {
		s.writeError(w, r, auth.ErrSSONotConfigured)
		return
	}

	_ = r.ParseForm()
	if e := r.Form.Get("error"); e != "" {
		s.writeError(w, r, auth.ErrUnauthenticated) // the IdP denied the login
		return
	}

	var assertion auth.Assertion
	switch proto {
	case auth.ProtocolOIDC:
		// CSRF: the returned state must equal the persisted state.
		if r.Form.Get("state") != flow.state {
			s.writeError(w, r, errForbidden)
			return
		}
		code := r.Form.Get("code")
		if code == "" {
			s.badRequest(w, r, "missing authorization code")
			return
		}
		assertion = auth.Assertion{
			Protocol: auth.ProtocolOIDC, Raw: code, Nonce: flow.nonce,
			PKCEVerifier: flow.verifier, RedirectURI: flow.redirectURI,
		}
	case auth.ProtocolSAML:
		resp := r.Form.Get("SAMLResponse")
		if resp == "" {
			s.badRequest(w, r, "missing SAMLResponse")
			return
		}
		assertion = auth.Assertion{
			Protocol: auth.ProtocolSAML, Raw: resp, RequestID: flow.requestID,
			RedirectURI: flow.redirectURI,
		}
	default:
		s.writeError(w, r, auth.ErrSSONotConfigured)
		return
	}

	identity, err := fed.ValidateAssertion(r.Context(), assertion)
	if err != nil {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	// U5 domain boundary: an IdP that claims domains may only vouch for identities
	// whose email is in those domains, so it cannot assert an out-of-domain address to
	// seize another account via the email-fallback path. The global/default IdP (no claimed
	// domains) is unconstrained.
	if !resolved.AllowsEmail(identity.Email) {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	// Complete under the SELECTED IdP's scope (U5), NOT the raw ?tenant= hint, so SCIM
	// authority (D4) and group reconciliation (U2) key on the tenant whose IdP actually
	// validated the assertion. The global login resolves scope = SystemTenantID.
	token, sess, err := s.authr.CompleteSSO(r.Context(), identity, clientIP(r), resolved.Scope, resolved.SCIMAuthoritative)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"session_id": sess.ID.String(),
		"expires_at": sess.ExpiresAt.String(),
		"return_to":  flow.returnTo,
	})
}

// federationCallbackURL builds the exact callback URL the provider must use as
// the OIDC redirect_uri / SAML ACS (RFC 9700 exact match).
func federationCallbackURL(r *http.Request) string {
	return schemeHost(r) + "/v1/auth/federation/callback"
}

// FederationCallbackURL is federationCallbackURL exported for MODULES that must report
// the same callback the login leg actually uses (the identity console's SSO status
// at /v1/m/identity/sso). It is exported rather than copied deliberately — the rule
// honors a trusted reverse proxy's X-Forwarded-Proto/Host, and a second implementation
// would drift from the one that decides the real redirect, handing an operator a
// redirect_uri their IdP would then reject on exact match.
func FederationCallbackURL(r *http.Request) string { return federationCallbackURL(r) }

// schemeHost derives scheme://host honoring a trusted reverse proxy's
// X-Forwarded-* headers (shared by SSO and SCIM absolute-URL construction).
func schemeHost(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host
}

// randToken returns a URL-safe base64 token of n random bytes.
func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// pkceS256 returns the RFC 7636 S256 code challenge for a verifier.
func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
