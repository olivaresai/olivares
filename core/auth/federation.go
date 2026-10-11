// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrSSONotConfigured is returned by the default (NoFederation) provider when no
// IdP is wired. Single-IdP SSO is OPEN-CORE: the real OIDC/SAML provider
// (core/auth/federation) links into the base build, so this is the honest "no IdP
// configured yet" state, not a capped feature. Multi-IdP, SSO-enforcement/posture
// and managed SCIM are the reserved enterprise line (LICENSING.md).
var ErrSSONotConfigured = errors.New("auth: SSO not configured")

// ErrSSOProviderUnavailable means an IdP IS configured but its provider could not be
// resolved: it does not build (a broken SP keypair, an unreachable IdP metadata URL),
// the store failed, or its SP metadata cannot be produced. It is distinct from
// ErrSSONotConfigured so an operator can tell a misconfiguration from an absent IdP.
var ErrSSOProviderUnavailable = errors.New("auth: SSO provider unavailable")

// Protocol names the federation protocol a provider speaks.
const (
	// ProtocolOIDC is OpenID Connect (Authorization Code + PKCE).
	ProtocolOIDC = "oidc"
	// ProtocolSAML is SAML 2.0 Web Browser SSO.
	ProtocolSAML = "saml"
)

// AuthParams are the single-use, core-generated parameters for the start
// (authorization-request) leg of an SSO login. The CORE generates and persists
// them (state for CSRF, nonce for OIDC replay binding, the PKCE verifier — only
// the S256 challenge is handed to the provider — and the SAML AuthnRequest id);
// the provider only assembles the IdP redirect from them. Keeping the PKCE
// verifier and nonce in core means go-oidc/gosaml2 handle protocol crypto while
// the secret-bearing flow state never leaves the engine.
type AuthParams struct {
	// State is the opaque CSRF token echoed back on the callback.
	State string
	// Nonce is the OIDC nonce bound into the ID token (ignored for SAML).
	Nonce string
	// PKCEChallenge is the S256 code challenge (the core holds the verifier).
	PKCEChallenge string
	// RedirectURI is the exact callback URL (RFC 9700 exact-match).
	RedirectURI string
	// RequestID is the SAML AuthnRequest id the response's InResponseTo must echo
	// (ignored for OIDC).
	RequestID string
}

// Assertion is the data the provider validates on the callback (finish) leg. The
// core fills the verification context (Nonce/PKCEVerifier/RedirectURI/RequestID)
// from the persisted flow state; the provider performs the protocol crypto
// (OIDC: exchange the code with the verifier, verify the ID token's signature and
// nonce; SAML: verify the response signature, conditions and InResponseTo).
type Assertion struct {
	// Protocol is ProtocolOIDC or ProtocolSAML.
	Protocol string
	// Raw is the protocol payload: the OIDC authorization code, or the base64
	// SAML Response.
	Raw string
	// Nonce is the expected OIDC nonce (from the persisted flow state).
	Nonce string
	// PKCEVerifier is the OIDC PKCE code_verifier (from the persisted flow state).
	PKCEVerifier string
	// RedirectURI is the exact redirect_uri used at the start leg.
	RedirectURI string
	// RequestID is the expected SAML InResponseTo (from the persisted flow state).
	RequestID string
}

// FederatedIdentity is the result of validating an Assertion: the external
// subject and the email it maps to a local user by.
type FederatedIdentity struct {
	// Protocol is the verifier's protocol, also pinned by the callback to the
	// selected provider. OIDC email bootstrap requires explicit verification.
	Protocol string
	// Subject is the IdP's stable subject identifier.
	Subject string
	// Issuer is the VERIFIED issuing IdP identity (U3): the OIDC `iss` the
	// verifier enforced against discovery, or the SAML IdP entityID gosaml2 enforced
	// against the trusted metadata. It qualifies Subject so a bare subject value can
	// never select the wrong account across IdPs. Empty only for a provider that does
	// not surface one (then correlation falls back to email, the pre-U3 behavior).
	Issuer string
	// Email is the provider-asserted address used to find/provision the local user.
	Email string
	// EmailVerified carries an explicit true OIDC email_verified claim for this
	// address. Omission is false; it never affects an exact qualified-subject match.
	EmailVerified bool
	// DisplayName is a human label, when the IdP provides one.
	DisplayName string
	// Groups are the directory group identifiers the IdP asserted for this
	// subject (OIDC groups claim / SAML group attribute), verbatim as sent — an
	// externalId or a displayName depending on the IdP (Entra sends object-ids,
	// Okta/Auth0 send names). Empty when the config declares no groups claim/attr
	// or the IdP asserted none. OPEN-CORE only CARRIES them across the seam
	// (U1): the base build has no capability to turn a group into a grant, so
	// they confer nothing without the reserved enterprise GroupMapper (U2) —
	// the honest cap, symmetric with MultiIDP. Never trusted for anything until an
	// operator-owned mapping resolves them (the IdP names the groups, the operator
	// decides what they mean).
	Groups []string
	// AAL is at most AAL2, based only on the selected provider's verified
	// assertion and its operator-owned mapping. Zero is legacy AAL1.
	AAL int
	// AuthenticatedAt is the upstream authentication event, never the token's
	// issue time or the callback time. Missing/stale events confer no elevation.
	AuthenticatedAt time.Time
}

// ssoSubjectSep joins the issuer and subject into the stored SsoSubject key. U+001F
// (ASCII Unit Separator) never appears in an OIDC issuer URL, a SAML entityID, or a
// subject identifier, so the two components join without ambiguity and the result is
// not parseable back into a spoofable "<issuer>/<subject>"-looking value.
const ssoSubjectSep = "\x1f"

// QualifiedSubject returns the issuer-qualified subject key used to correlate this
// identity to a local user (U3), or "" when either component is missing — in
// which case the login path MUST fall back to email correlation (the pre-U3
// behavior) rather than match on a bare, un-namespaced subject. Both components are
// trimmed but NOT case-folded: OIDC iss/sub and SAML entityID/NameID are all
// case-sensitive per spec, and the verifiers already enforced them exactly.
func (i FederatedIdentity) QualifiedSubject() string {
	issuer := strings.TrimSpace(i.Issuer)
	subject := strings.TrimSpace(i.Subject)
	if issuer == "" || subject == "" {
		return ""
	}
	return issuer + ssoSubjectSep + subject
}

// QualifiedKey is an issuer-qualified subject key: the issuer, U+001F, then the
// subject. Correlation compares these keys exactly; it never trims them.
type QualifiedKey string

// ErrOwnerUnkeyable refuses an issuer or subject that cannot form a qualified key.
var ErrOwnerUnkeyable = errors.New("auth: the issuer or subject cannot form a qualified key")

// OwnerUnkeyableError names why a part cannot form a qualified key: empty,
// control or whitespace.
type OwnerUnkeyableError struct {
	Reason string
}

func (e OwnerUnkeyableError) Error() string { return ErrOwnerUnkeyable.Error() + ": " + e.Reason }

// Unwrap makes errors.Is(err, ErrOwnerUnkeyable) hold.
func (e OwnerUnkeyableError) Unwrap() error { return ErrOwnerUnkeyable }

// QualifiedSubjectKey returns the qualified key of subject at issuer. It trims
// nothing. It refuses an empty part; a part with a C0 control character or DEL
// anywhere, U+001F included; and a part with leading or trailing Unicode
// whitespace. A canonical pair gives the same key QualifiedSubject stores.
func QualifiedSubjectKey(issuer, subject string) (QualifiedKey, error) {
	for _, part := range []string{issuer, subject} {
		if reason := unkeyablePart(part); reason != "" {
			return "", OwnerUnkeyableError{Reason: reason}
		}
	}
	return QualifiedKey(issuer + ssoSubjectSep + subject), nil
}

// The reasons of an OwnerUnkeyableError. Each is a wire code,
// owner_unkeyable:<reason>, and none is retryable.
const (
	UnkeyableEmpty      = "empty"
	UnkeyableControl    = "control"
	UnkeyableWhitespace = "whitespace"
)

// unkeyablePart returns why part cannot form a qualified key, or "".
func unkeyablePart(part string) string {
	if part == "" {
		return UnkeyableEmpty
	}
	for _, r := range part {
		if r < 0x20 || r == 0x7f {
			return UnkeyableControl
		}
	}
	first, _ := utf8.DecodeRuneInString(part)
	last, _ := utf8.DecodeLastRuneInString(part)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return UnkeyableWhitespace
	}
	return ""
}

// Federation is the SSO seam. The open-core single-IdP OIDC (go-oidc) / SAML
// (gosaml2) provider implements it in core/auth/federation and links into the
// base build, so the default binary does real single-IdP login;
// NoFederation is the unconfigured default. Validating a JWT/SAML assertion happens
// behind this seam, NEVER for first-party sessions (which are opaque tokens).
type Federation interface {
	// Protocol reports which protocol this provider speaks (ProtocolOIDC /
	// ProtocolSAML), or "" when unconfigured.
	Protocol() string
	// BeginAuth assembles the IdP authorization redirect URL from the core's
	// single-use AuthParams. It returns ErrSSONotConfigured when unconfigured.
	BeginAuth(ctx context.Context, p AuthParams) (redirectURL string, err error)
	// ValidateAssertion verifies an external assertion and returns the federated
	// identity, or an error (ErrSSONotConfigured when no provider is wired).
	ValidateAssertion(ctx context.Context, a Assertion) (FederatedIdentity, error)
}

// NoFederation is the default provider: SSO is not configured. Cause is set when an
// IdP is configured but could not be resolved (see ErrSSOProviderUnavailable), with
// that IdP's protocol in ConfiguredProtocol when known: login still fails closed as
// not configured, and the SP metadata endpoint reports the cause for a SAML IdP.
type NoFederation struct {
	Cause              error
	ConfiguredProtocol string
}

// Protocol reports the unconfigured protocol.
func (NoFederation) Protocol() string { return "" }

// BeginAuth always reports ErrSSONotConfigured.
func (NoFederation) BeginAuth(context.Context, AuthParams) (string, error) {
	return "", ErrSSONotConfigured
}

// ValidateAssertion always reports ErrSSONotConfigured.
func (NoFederation) ValidateAssertion(context.Context, Assertion) (FederatedIdentity, error) {
	return FederatedIdentity{}, ErrSSONotConfigured
}

// Compile-time proof the default satisfies the seam.
var _ Federation = NoFederation{}
