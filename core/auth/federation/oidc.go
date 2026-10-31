// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// oidcProvider implements the OIDC Authorization Code + S256 PKCE login flow per
// the verified spec (OIDC Core/Discovery 1.0, OAuth 2.1 / RFC 9700). The core
// generates and persists state/nonce/PKCE-verifier; this provider assembles the
// IdP redirect from them, then on callback exchanges the code with the verifier,
// verifies the ID token (signature/iss/aud/exp), and — critically — verifies the
// nonce itself (go-oidc does NOT). It accepts only RS256/ES256 and never sets any
// Insecure*/Skip* verifier option.
type oidcProvider struct {
	// An explicit AMR list replaces default distinct-factor inference.
	allowAMRCombination bool
	assurance           model.FederationAssuranceMapping
	provider            *oidc.Provider
	clientID            string
	clientSecret        string
	verifier            *oidc.IDTokenVerifier
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
		allowAMRCombination: true,
		assurance:           assuranceMapping(nil),
		provider:            prov,
		clientID:            clientID,
		clientSecret:        clientSecret,
		groupsClaim:         strings.TrimSpace(groupsClaim),
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
	// Assurance comes only from the signature-verified ID token, before any
	// best-effort UserInfo merge. auth_time is the event, never iat or callback time.
	aal, authenticatedAt := o.assertionAssurance(idToken)
	_ = idToken.Claims(&claims)
	_ = idToken.Claims(&raw)
	if raw == nil {
		raw = map[string]any{} // defensive: a degenerate token must never nil-panic the merge/read below
	}
	// The group-overage marker is read from each source on its own: the UserInfo merge
	// below keeps only the keys the token lacks, so one source's `_claim_names` would
	// hide the other's.
	idTokenNames := raw["_claim_names"]
	var userInfoNames any
	// Fetch UserInfo when it can supply a claim the ID token lacks: the email (often
	// absent in the code flow) OR the configured groups claim. Many IdPs keep the ID
	// token minimal and expose groups only at UserInfo (e.g. Azure AD's groups
	// overage), so reading groups solely from the ID token would silently drop them
	// and no-op the reconciliation. A UserInfo error never fails an otherwise-valid
	// login, but where it was the only place the groups could come from it leaves them
	// unknown, and the login is flagged below rather than read as "no groups".
	groupsMissing := o.groupsClaim != "" && len(claimStrings(raw[o.groupsClaim])) == 0
	userInfoAnswered := false
	userInfoGroupsNull := false
	userInfoWhy := "userinfo_failed" // only read while UserInfo has not answered
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
			if ui.Subject != idToken.Subject {
				userInfoWhy = "userinfo_subject_mismatch"
			} else {
				// Verification of the token's email cannot verify a different
				// address supplied by UserInfo without its own explicit claim.
				var uiClaims idClaims
				if ui.Claims(&uiClaims) != nil || uiClaims.Email != "" && uiClaims.Email != claims.Email {
					claims.EmailVerified = nil
				}
				_ = ui.Claims(&claims)
				var uraw map[string]any
				if ui.Claims(&uraw) == nil {
					userInfoAnswered = true
					userInfoNames = uraw["_claim_names"]
					for k, v := range uraw {
						// The groups claim is what sent us here when the token's was empty or
						// unreadable, so UserInfo's list replaces it. An explicit null is not
						// a list and stays indeterminate; other claims only fill a gap.
						if k == o.groupsClaim && groupsMissing && v == nil {
							userInfoGroupsNull = true
						}
						if _, ok := raw[k]; !ok || k == o.groupsClaim && groupsMissing && v != nil {
							raw[k] = v
						}
					}
				}
			}
		} else if groupsMissing && o.provider.UserInfoEndpoint() != "" {
			// The failed call was where the groups had to come from. Keep the stable
			// reason, never the upstream error: go-oidc includes the raw response body,
			// which can carry assertion data or credentials.
			slog.Warn("federation: oidc userinfo call failed while the groups were missing", "reason", userInfoWhy)
		}
	}
	if claims.Email == "" {
		return auth.FederatedIdentity{}, fmt.Errorf("oidc: no email claim")
	}
	// An explicit false still rejects the assertion. An omitted claim can serve
	// an exact subject match, but cannot bootstrap an existing account by email.
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
	id := auth.FederatedIdentity{Protocol: auth.ProtocolOIDC, Subject: subject, Issuer: idToken.Issuer, Email: claims.Email, EmailVerified: claims.EmailVerified != nil && *claims.EmailVerified, DisplayName: claims.Name}
	id.AAL, id.AuthenticatedAt = aal, authenticatedAt
	if o.groupsClaim != "" {
		id.Groups = claimStrings(raw[o.groupsClaim])
		// Entra above 200 groups drops the claim and lists it in _claim_names instead
		// (OIDC Core 5.6.2). The list is then incomplete whatever else we hold for it,
		// UserInfo included, so the login is flagged and CompleteSSO decides. A marker
		// from either source counts.
		// ponytail: Entra's `hasgroups` (implicit/hybrid flows only) is not read; this
		// provider runs only the code flow. Upgrade when an implicit flow is added.
		// Besides the marker, two more ways the list is unknown rather than empty: the claim
		// is present but unreadable, so Groups may be partial or blank; or the ID token's
		// claim is absent or empty, which sent us to a UserInfo that failed or answered for
		// another subject, so the list may be there. An IdP that advertises no UserInfo
		// endpoint has nowhere else to look, so there absence is its answer.
		switch {
		case claimListed(idTokenNames, o.groupsClaim) || claimListed(userInfoNames, o.groupsClaim):
			id.GroupsIndeterminateWhy = "groups_overage"
		case userInfoGroupsNull:
			id.GroupsIndeterminateWhy = "groups_claim_unreadable"
		case !claimReadable(raw[o.groupsClaim]):
			id.GroupsIndeterminateWhy = "groups_claim_unreadable"
		case groupsMissing && !userInfoAnswered && o.provider.UserInfoEndpoint() != "":
			id.GroupsIndeterminateWhy = userInfoWhy
		}
		id.GroupsIndeterminate = id.GroupsIndeterminateWhy != ""
	}
	return id, nil
}

// claimListed reports whether a `_claim_names` value says the IdP supplies claim
// elsewhere (OIDC Core 5.6.2). A value that is present but not an object cannot be
// read, so it counts as listing the claim: unknown must not become "no groups". go-oidc
// already rejects such a value in the ID token; UserInfo is not checked by it.
func claimListed(names any, claim string) bool {
	switch t := names.(type) {
	case nil:
		return false
	case map[string]any:
		_, ok := t[claim]
		return ok
	}
	return true
}

// claimReadable reports whether a groups claim value is one claimStrings reads in full:
// absent or null, one string, or an array of strings. Anything else (an object, a number,
// a boolean, an array with a non-string member) is present but unreadable, so the list
// claimStrings returns for it is not the IdP's answer and must not stand for "no groups".
func claimReadable(v any) bool {
	switch t := v.(type) {
	case nil, string:
		return true
	case []any:
		for _, e := range t {
			if _, ok := e.(string); !ok {
				return false
			}
		}
		return true
	}
	return false
}

// claimStrings coerces a JSON claim value to a list of non-empty strings. It
// accepts a JSON array (the usual groups shape — Okta/Auth0/Entra) or a single
// string (some IdPs send one group unwrapped); anything else yields nil. It never
// errors, so it cannot tell an empty claim from an unreadable one: the caller asks
// claimReadable and flags the login rather than trusting an empty list.
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
