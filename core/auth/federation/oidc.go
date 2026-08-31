// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"context"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/olivaresai/olivares/core/auth"
)

// oidcProvider implements the OIDC Authorization Code + S256 PKCE login flow per
// the verified spec (OIDC Core/Discovery 1.0, OAuth 2.1 / RFC 9700). The core
// generates and persists state/nonce/PKCE-verifier; this provider assembles the
// IdP redirect from them, then on callback exchanges the code with the verifier,
// verifies the ID token (signature/iss/aud/exp), and — critically — verifies the
// nonce itself (go-oidc does NOT). It accepts only RS256/ES256 and never sets any
// Insecure*/Skip* verifier option.
type oidcProvider struct {
	provider     *oidc.Provider
	clientID     string
	clientSecret string
	verifier     *oidc.IDTokenVerifier
	// groupsClaim is the ID-token/UserInfo claim carrying the subject's directory
	// groups (U1); "" ⇒ groups are not read. The claim value may be a JSON
	// array of strings or a single string; both are accepted.
	groupsClaim string
}

func oidcFromEnv(getenv func(string) string) (*Provider, error) {
	issuer := getenv(envOIDCIssuer)
	clientID := getenv(envOIDCClientID)
	clientSecret := getenv(envOIDCClientSecret)
	if issuer == "" || clientID == "" {
		return nil, fmt.Errorf("%w: %s and %s are required", ErrNotConfigured, envOIDCIssuer, envOIDCClientID)
	}
	return oidcFromParts(context.Background(), issuer, clientID, clientSecret, getenv(envOIDCGroupsClaim))
}

// oidcFromParts builds the OIDC provider from explicit parts (shared by the env
// and the managed-config paths). It performs discovery against the issuer,
// so a transient IdP outage surfaces here as ErrNotConfigured (fail-closed).
func oidcFromParts(ctx context.Context, issuer, clientID, clientSecret, groupsClaim string) (*Provider, error) {
	if issuer == "" || clientID == "" {
		return nil, fmt.Errorf("%w: oidc issuer and client_id are required", ErrNotConfigured)
	}
	if _, err := mustAbsURL(issuer); err != nil {
		return nil, err
	}
	ctx = oidc.ClientContext(ctx, httpClient())
	// NewProvider fetches /.well-known/openid-configuration and binds the issuer;
	// do not bypass its issuer match.
	prov, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: discovery: %v", ErrNotConfigured, err)
	}
	op := &oidcProvider{
		provider:     prov,
		clientID:     clientID,
		clientSecret: clientSecret,
		groupsClaim:  strings.TrimSpace(groupsClaim),
		// Pin the accepted signing algorithms; never accept "none".
		verifier: prov.Verifier(&oidc.Config{
			ClientID:             clientID,
			SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256},
		}),
	}
	return &Provider{protocol: auth.ProtocolOIDC, oidc: op}, nil
}

// oauthConfig builds a per-call oauth2.Config with the exact redirect URI (RFC
// 9700 exact-match) used for this login.
func (o *oidcProvider) oauthConfig(redirectURI string) oauth2.Config {
	return oauth2.Config{
		ClientID:     o.clientID,
		ClientSecret: o.clientSecret,
		Endpoint:     o.provider.Endpoint(),
		RedirectURL:  redirectURI,
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
}

// beginAuth assembles the authorization-request redirect with the core-provided
// state, nonce and S256 PKCE challenge. code_challenge_method=S256 is always sent
// explicitly (never the RFC 7636 "plain" default).
func (o *oidcProvider) beginAuth(_ context.Context, p auth.AuthParams) (string, error) {
	cfg := o.oauthConfig(p.RedirectURI)
	return cfg.AuthCodeURL(p.State,
		oidc.Nonce(p.Nonce),
		oauth2.SetAuthURLParam("code_challenge", p.PKCEChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	), nil
}

// idClaims are the identity claims read from the ID token / userinfo.
type idClaims struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified *bool  `json:"email_verified"`
	Name          string `json:"name"`
}

// validate exchanges the authorization code (with the PKCE verifier), verifies the
// ID token and nonce, enforces azp for multi-audience tokens, and resolves a
// verified email (falling back to UserInfo when the code flow omits it).
func (o *oidcProvider) validate(ctx context.Context, a auth.Assertion) (auth.FederatedIdentity, error) {
	ctx = oidc.ClientContext(ctx, httpClient())
	cfg := o.oauthConfig(a.RedirectURI)

	tok, err := cfg.Exchange(ctx, a.Raw, oauth2.VerifierOption(a.PKCEVerifier))
	if err != nil {
		return auth.FederatedIdentity{}, fmt.Errorf("oidc: token exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return auth.FederatedIdentity{}, fmt.Errorf("oidc: no id_token in response (openid scope not granted?)")
	}
	idToken, err := o.verifier.Verify(ctx, rawID)
	if err != nil {
		return auth.FederatedIdentity{}, fmt.Errorf("oidc: id_token verify: %w", err)
	}
	// go-oidc does NOT check the nonce — assert it ourselves (replay protection).
	if idToken.Nonce != a.Nonce {
		return auth.FederatedIdentity{}, fmt.Errorf("oidc: nonce mismatch")
	}
	// azp (OIDC Core §3.1.3.7 rule 5): if the authorized party is present it MUST
	// equal this client's id — REGARDLESS of the audience count — and a token with
	// more than one audience MUST carry an azp. The previous gate only rejected a
	// mismatched azp when len(aud)>1, so a single-audience token bearing an azp that
	// named a DIFFERENT client was accepted (an audience-confusion hole). Fixed here.
	var azpClaim struct {
		AZP string `json:"azp"`
	}
	_ = idToken.Claims(&azpClaim)
	if len(idToken.Audience) > 1 && azpClaim.AZP == "" {
		return auth.FederatedIdentity{}, fmt.Errorf("oidc: multi-audience id_token without azp")
	}
	if azpClaim.AZP != "" && azpClaim.AZP != o.clientID {
		return auth.FederatedIdentity{}, fmt.Errorf("oidc: azp does not authorize this client")
	}

	var claims idClaims
	// raw holds every ID-token claim so the configurable groups claim (U1,
	// whose key is not known at compile time) can be read alongside the typed ones.
	var raw map[string]any
	_ = idToken.Claims(&claims)
	_ = idToken.Claims(&raw)
	if raw == nil {
		raw = map[string]any{} // defensive: a degenerate token must never nil-panic the merge/read below
	}
	// Fetch UserInfo when it can supply a claim the ID token lacks: the email (often
	// absent in the code flow) OR the configured groups claim. Many IdPs keep the ID
	// token minimal and expose groups only at UserInfo (e.g. Azure AD's groups
	// overage), so reading groups solely from the ID token would silently drop them
	// and no-op the reconciliation. Best-effort: a UserInfo error never fails an
	// otherwise-valid login.
	groupsMissing := o.groupsClaim != "" && len(claimStrings(raw[o.groupsClaim])) == 0
	if claims.Email == "" || groupsMissing {
		if ui, uerr := o.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok)); uerr == nil {
			// OIDC Core §5.3.2: the UserInfo `sub` MUST be present and equal the ID
			// token `sub`; otherwise the response cannot be tied to this authenticated
			// subject and its claims MUST NOT be used. So the response is consumed ONLY
			// when its sub matches — a missing sub (non-conformant) or a mismatched one
			// (substitution/bug) is discarded, never merged. This protects both the
			// subject (the U3 correlation key, itself always taken from the signed
			// id_token below) and the email/groups it supplies. We discard rather than
			// hard-fail: the id_token stays the authoritative identity, so a login whose
			// email came from the id_token still succeeds, while one that DEPENDED on the
			// discarded UserInfo email fails cleanly at the "no email claim" check below.
			if ui.Subject == idToken.Subject {
				_ = ui.Claims(&claims)
				var uraw map[string]any
				if ui.Claims(&uraw) == nil {
					for k, v := range uraw {
						if _, ok := raw[k]; !ok {
							raw[k] = v
						}
					}
				}
			}
		}
	}
	if claims.Email == "" {
		return auth.FederatedIdentity{}, fmt.Errorf("oidc: no email claim")
	}
	// Trust the email unless the IdP explicitly marks it unverified. (Many
	// enterprise IdPs omit email_verified; an explicit false is rejected.)
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		return auth.FederatedIdentity{}, fmt.Errorf("oidc: email is not verified")
	}
	// The subject is the VERIFIED id_token `sub` (a required, signed claim), never the
	// post-UserInfo claims.Subject — UserInfo was already asserted to carry the same
	// sub above, and taking it from the token keeps the correlation key's provenance on
	// the signed assertion (U3).
	subject := idToken.Subject
	// idToken.Issuer is the verified `iss`: the verifier enforced it equal to the
	// discovery-bound issuer (SkipIssuerCheck is never set), so it is a trustworthy
	// qualifier for the subject, not a self-asserted claim (U3).
	id := auth.FederatedIdentity{Subject: subject, Issuer: idToken.Issuer, Email: claims.Email, DisplayName: claims.Name}
	if o.groupsClaim != "" {
		id.Groups = claimStrings(raw[o.groupsClaim])
	}
	return id, nil
}

// claimStrings coerces a JSON claim value to a list of non-empty strings. It
// accepts a JSON array (the usual groups shape — Okta/Auth0/Entra) or a single
// string (some IdPs send one group unwrapped); anything else yields nil. It never
// errors: a malformed groups claim simply asserts no groups (fail-inert — an
// unreadable claim must not fail an otherwise-valid login).
func claimStrings(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []string{s}
		}
	}
	return nil
}
