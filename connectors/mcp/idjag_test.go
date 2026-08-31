// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"

	"github.com/olivaresai/olivares/sdk"
)

// idjag_test.go is the SEP-by-SEP audit of the ID-JAG validator (idjag.go): MCP
// Enterprise-Managed Authorization (SEP-990) profiling IETF
// draft-ietf-oauth-identity-assertion-authz-grant-04, plus the registry
// policy input (EnterpriseAuthPolicyInput). Each test cites the clause it audits.

const (
	jagIDP    = "https://idp.corp.example"   // the enterprise IdP issuing ID-JAGs
	jagIDPB   = "https://idp-b.corp.example" // a second trusted IdP (multi-IdP deployments, ext-auth §5.1)
	jagClient = "mcp-client-7"               // the client authenticated on the token request
)

// idpKey is one fake enterprise IdP signing identity: an ES256 key published as a
// JWKS under kid, minting ID-JAGs for that issuer. Distinct instances with the SAME
// kid exercise the issuer-keyed trust isolation (issuertrust.go).
type idpKey struct {
	issuer string
	kid    string
	key    *ecdsa.PrivateKey
	jwks   []byte
}

func newIDPKey(t *testing.T, issuer, kid string) *idpKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	ks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: kid, Algorithm: "ES256", Use: "sig"}}}
	blob, err := json.Marshal(ks)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return &idpKey{issuer: issuer, kid: kid, key: key, jwks: blob}
}

// trust renders this IdP as the operator's IssuerTrust entry (inline JWKS anchor).
func (p *idpKey) trust() IssuerTrust { return IssuerTrust{Issuer: p.issuer, JWKS: p.jwks} }

// mintIDJAG signs an ID-JAG (typ oauth-id-jag+jwt, ext-auth §4.3 / RFC 8725 §3.11)
// with every claim the profile REQUIRES, against the shared rsClock. overrides
// replaces a claim by name; a nil value DELETES the claim (missing-claim negatives).
func (p *idpKey) mintIDJAG(t *testing.T, overrides map[string]any) string {
	t.Helper()
	return p.mintIDJAGTyped(t, idjagTyp, overrides)
}

// mintIDJAGTyped is mintIDJAG with an explicit JOSE typ (the typed-JWT negative).
func (p *idpKey) mintIDJAGTyped(t *testing.T, typ string, overrides map[string]any) string {
	t.Helper()
	js, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: p.key},
		(&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("kid", p.kid))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	// The draft-04 §3.1 REQUIRED set: iss/sub/aud/client_id/jti/exp/iat, plus the MCP
	// profile's REQUIRED resource (RFC 9728 resource id of the target server) and the
	// optional space-separated scope. aud is the RECEIVING AS's issuer identifier.
	claims := map[string]any{
		"iss":       p.issuer,
		"sub":       "user-7",
		"aud":       rsIssuer,
		"client_id": jagClient,
		"jti":       "jag-1",
		"exp":       jwt.NewNumericDate(rsClock().Add(10 * time.Minute)),
		"iat":       jwt.NewNumericDate(rsClock().Add(-time.Minute)),
		"resource":  rsResource,
		"scope":     "a b",
	}
	for k, v := range overrides {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	raw, err := jwt.Signed(js).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

// newJAGValidator builds a receiving-AS validator with audience rsIssuer (this AS's
// issuer identifier) and the given approved-resource policy input, on rsClock.
func newJAGValidator(t *testing.T, approved []string, idps ...IssuerTrust) *IDJAGValidator {
	t.Helper()
	v, err := NewIDJAGValidator(IDJAGValidatorConfig{
		TrustedIDPs:       idps,
		Audience:          rsIssuer,
		ApprovedResources: approved,
		Clock:             rsClock,
	})
	if err != nil {
		t.Fatalf("NewIDJAGValidator: %v", err)
	}
	return v
}

// wantIDJAGReject asserts a rejection: non-nil, carrying the named substring, AND
// errors.Is(err, ErrIDJAGInvalidGrant) — draft §4.4.1 maps EVERY validation failure
// to the OAuth invalid_grant token error, and the sentinel is the API contract a
// token endpoint relies on (RFC 6749 §5.2).
func wantIDJAGReject(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want invalid_grant containing %q, got nil error", substr)
	}
	if !errors.Is(err, ErrIDJAGInvalidGrant) {
		t.Errorf("every rejection must wrap ErrIDJAGInvalidGrant (draft §4.4.1): %v", err)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Errorf("error %q does not contain %q", err, substr)
	}
}

// --- tests ------------------------------------------------------------------------

// TestSEP990IDJAGHappyPath: a grant from a trusted IdP, aud (string form) naming this
// AS, client_id matching the authenticated client, and an approved resource is
// accepted with every claim plumbed through (SEP-990 / draft-04 §4.4.1; scope is the
// optional space-separated list of §3.1).
func TestSEP990IDJAGHappyPath(t *testing.T) {
	idp := newIDPKey(t, jagIDP, "k1")
	v := newJAGValidator(t, []string{rsResource}, idp.trust())
	got, err := v.Validate(context.Background(), idp.mintIDJAG(t, nil), jagClient)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Issuer != jagIDP {
		t.Errorf("Issuer = %q, want %q", got.Issuer, jagIDP)
	}
	if got.Subject != "user-7" {
		t.Errorf("Subject = %q, want user-7", got.Subject)
	}
	if got.ClientID != jagClient {
		t.Errorf("ClientID = %q, want %q", got.ClientID, jagClient)
	}
	if got.Audience != rsIssuer {
		t.Errorf("Audience = %q, want %q", got.Audience, rsIssuer)
	}
	if !reflect.DeepEqual(got.Resources, []string{rsResource}) {
		t.Errorf("Resources = %v, want [%s]", got.Resources, rsResource)
	}
	if !reflect.DeepEqual(got.Scopes, []string{"a", "b"}) {
		t.Errorf("Scopes = %v, want [a b] split on spaces (draft §3.1)", got.Scopes)
	}
	if got.JTI != "jag-1" {
		t.Errorf("JTI = %q, want jag-1", got.JTI)
	}
	if !got.Expiry.Equal(rsClock().Add(10 * time.Minute)) {
		t.Errorf("Expiry = %v", got.Expiry)
	}
	if !got.IssuedAt.Equal(rsClock().Add(-time.Minute)) {
		t.Errorf("IssuedAt = %v", got.IssuedAt)
	}
}

// TestIDJAGDraft04OptionalClaimsTolerated: draft -04 added optional sub_id and
// formalized XAA terminology without changing the REQUIRED claim set. Optional
// claims such as sub_id, act, tenant and auth_time must not become hidden
// enforcement gates; the grant validates like the baseline grant, with tenant only
// carried through as an audit/linking hint.
func TestIDJAGDraft04OptionalClaimsTolerated(t *testing.T) {
	idp := newIDPKey(t, jagIDP, "k1")
	v := newJAGValidator(t, []string{rsResource}, idp.trust())
	base, err := v.Validate(context.Background(), idp.mintIDJAG(t, map[string]any{"jti": "jag-base"}), jagClient)
	if err != nil {
		t.Fatalf("baseline Validate: %v", err)
	}
	got, err := v.Validate(context.Background(), idp.mintIDJAG(t, map[string]any{
		"jti":       "jag-optional",
		"sub_id":    "urn:oasis:names:tc:SAML:2.0:nameid-format:persistent:user-7",
		"act":       map[string]any{"sub": "agent:claude"},
		"tenant":    "tenant-a",
		"auth_time": rsClock().Add(-2 * time.Minute).Unix(),
	}), jagClient)
	if err != nil {
		t.Fatalf("Validate with draft -04 optional claims: %v", err)
	}
	if got.Issuer != base.Issuer || got.Subject != base.Subject || got.ClientID != base.ClientID ||
		got.Audience != base.Audience || !reflect.DeepEqual(got.Resources, base.Resources) ||
		!reflect.DeepEqual(got.Scopes, base.Scopes) {
		t.Fatalf("optional claims changed enforced grant fields: base=%+v got=%+v", base, got)
	}
	if got.Tenant != "tenant-a" {
		t.Fatalf("tenant should remain a tolerated audit hint, got %q", got.Tenant)
	}
}

// TestSEP990IDJAGAudienceArity: draft-04 §4.4.1 — aud MUST be the receiving AS's
// issuer identifier, as a string or an array of EXACTLY one element; two elements or
// a foreign value are invalid_grant.
func TestSEP990IDJAGAudienceArity(t *testing.T) {
	idp := newIDPKey(t, jagIDP, "k1")
	cases := []struct {
		name    string
		aud     any
		wantErr string // "" => accepted
	}{
		{"string match", rsIssuer, ""},
		{"array of one match", []string{rsIssuer}, ""},
		{"array of two", []string{rsIssuer, "https://other-as.example"}, "exactly one element"},
		{"foreign string", "https://other-as.example", "not this authorization server"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Fresh validator per case: the replay cache must not bleed across cases.
			v := newJAGValidator(t, []string{rsResource}, idp.trust())
			_, err := v.Validate(context.Background(), idp.mintIDJAG(t, map[string]any{"aud": tc.aud}), jagClient)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			wantIDJAGReject(t, err, tc.wantErr)
		})
	}
}

// TestSEP990IDJAGWrongTypRejected: ext-auth §4.3 / RFC 8725 §3.11 — the typ MUST be
// oauth-id-jag+jwt. An at+jwt signed by the TRUSTED key is refused, and the error
// names the typ (the rejection happens BEFORE any signature concern: the same key
// minting the correct typ is accepted).
func TestSEP990IDJAGWrongTypRejected(t *testing.T) {
	idp := newIDPKey(t, jagIDP, "k1")
	v := newJAGValidator(t, []string{rsResource}, idp.trust())
	_, err := v.Validate(context.Background(), idp.mintIDJAGTyped(t, "at+jwt", nil), jagClient)
	wantIDJAGReject(t, err, "typ is not")
	// Sanity: the SAME key with the correct typ passes — the rejection above was the
	// typ, not the trust chain.
	if _, err := v.Validate(context.Background(), idp.mintIDJAG(t, map[string]any{"jti": "jag-typ-ok"}), jagClient); err != nil {
		t.Fatalf("correct-typ assertion from the same key must be accepted: %v", err)
	}
}

// TestSEP990IDJAGUntrustedIssuerRejected: ext-auth §5.1 — only assertions from a
// CONFIGURED trusted enterprise IdP are accepted; an unknown iss is refused before
// any signature work.
func TestSEP990IDJAGUntrustedIssuerRejected(t *testing.T) {
	idp := newIDPKey(t, jagIDP, "k1")
	rogue := newIDPKey(t, "https://rogue-idp.example", "k1")
	v := newJAGValidator(t, []string{rsResource}, idp.trust())
	_, err := v.Validate(context.Background(), rogue.mintIDJAG(t, nil), jagClient)
	wantIDJAGReject(t, err, "not a trusted enterprise IdP")
}

// TestSEP990IDJAGWrongKeyRejected: an assertion claiming the trusted iss but signed
// by a DIFFERENT key than that IdP's JWKS fails signature verification (draft §4.4.1:
// the signature is validated against the issuer's own keys).
func TestSEP990IDJAGWrongKeyRejected(t *testing.T) {
	idp := newIDPKey(t, jagIDP, "k1")
	// Same issuer claim, same kid — but a different private key.
	impostor := newIDPKey(t, jagIDP, "k1")
	v := newJAGValidator(t, []string{rsResource}, idp.trust())
	_, err := v.Validate(context.Background(), impostor.mintIDJAG(t, nil), jagClient)
	wantIDJAGReject(t, err, "assertion signature")
}

// TestSEP2352IDJAGIssuerKeyedIsolation: the issuer-keyed trust rule (SEP-2352
// semantics applied to inbound assertions; RFC 9068 §4 "the keys provided by the
// authorization server"): two trusted IdPs share a kid with DIFFERENT keys, and an
// assertion claiming iss=B signed with A's key must be rejected — a kid collision
// never resolves cross-issuer.
func TestSEP2352IDJAGIssuerKeyedIsolation(t *testing.T) {
	idpA := newIDPKey(t, jagIDP, "shared-kid")
	idpB := newIDPKey(t, jagIDPB, "shared-kid")
	v := newJAGValidator(t, []string{rsResource}, idpA.trust(), idpB.trust())
	// Signed by A, claiming B: the iss selects B's keyring entry, where A's key for
	// the same kid is unreachable by construction.
	forged := idpA.mintIDJAG(t, map[string]any{"iss": jagIDPB})
	_, err := v.Validate(context.Background(), forged, jagClient)
	wantIDJAGReject(t, err, "assertion signature")
	// Sanity: each IdP's own assertion verifies under the same validator (so the
	// rejection above was the cross-issuer key, not the dual-IdP config).
	if _, err := v.Validate(context.Background(), idpB.mintIDJAG(t, nil), jagClient); err != nil {
		t.Fatalf("IdP B's own assertion must be accepted: %v", err)
	}
	if _, err := v.Validate(context.Background(), idpA.mintIDJAG(t, map[string]any{"jti": "jag-a"}), jagClient); err != nil {
		t.Fatalf("IdP A's own assertion must be accepted: %v", err)
	}
}

// TestSEP990IDJAGClientBinding: draft-04 §4.4.1 — the assertion's client_id MUST
// identify the same client as the request's client authentication; a mismatch, or an
// UNAUTHENTICATED request (empty client), is invalid_grant.
func TestSEP990IDJAGClientBinding(t *testing.T) {
	idp := newIDPKey(t, jagIDP, "k1")
	v := newJAGValidator(t, []string{rsResource}, idp.trust())
	tok := idp.mintIDJAG(t, nil)
	_, err := v.Validate(context.Background(), tok, "some-other-client")
	wantIDJAGReject(t, err, "does not identify the authenticated client")
	_, err = v.Validate(context.Background(), tok, "")
	wantIDJAGReject(t, err, "does not identify the authenticated client")
	// Sanity: both rejections happen BEFORE replay registration — the very same
	// assertion presented by the RIGHT client still redeems.
	if _, err := v.Validate(context.Background(), tok, jagClient); err != nil {
		t.Fatalf("matching client must redeem the grant: %v", err)
	}
}

// TestSEP990IDJAGRequiredClaims: draft-04 §3.1/§4.4.1 — sub, jti, exp, iat and
// client_id are REQUIRED; an exp beyond the 60s skew leeway is expired. One fresh
// validator per case (the shared jti must not trip the replay cache instead).
func TestSEP990IDJAGRequiredClaims(t *testing.T) {
	idp := newIDPKey(t, jagIDP, "k1")
	cases := []struct {
		name      string
		overrides map[string]any
		wantErr   string
	}{
		{"expired beyond leeway", map[string]any{"exp": jwt.NewNumericDate(rsClock().Add(-2 * time.Minute))}, "expired"},
		{"missing jti", map[string]any{"jti": nil}, "jti is REQUIRED"},
		{"missing sub", map[string]any{"sub": nil}, "sub is REQUIRED"},
		{"missing exp", map[string]any{"exp": nil}, "exp and iat are REQUIRED"},
		{"missing iat", map[string]any{"iat": nil}, "exp and iat are REQUIRED"},
		{"missing client_id", map[string]any{"client_id": nil}, "client_id is REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newJAGValidator(t, []string{rsResource}, idp.trust())
			_, err := v.Validate(context.Background(), idp.mintIDJAG(t, tc.overrides), jagClient)
			wantIDJAGReject(t, err, tc.wantErr)
		})
	}
}

// TestSEP990IDJAGReplayRejected: jti replay defense (defense-in-depth on top of
// RFC 7521 §5.2/RFC 7523 assertion processing): the SAME assertion redeemed twice
// fails the second time, naming the replay.
func TestSEP990IDJAGReplayRejected(t *testing.T) {
	idp := newIDPKey(t, jagIDP, "k1")
	v := newJAGValidator(t, []string{rsResource}, idp.trust())
	tok := idp.mintIDJAG(t, nil)
	if _, err := v.Validate(context.Background(), tok, jagClient); err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	_, err := v.Validate(context.Background(), tok, jagClient)
	wantIDJAGReject(t, err, "replay")
}

// TestSEP990IDJAGCnfWithoutProofRejected: draft-04 §9.8 — a cnf (proof-of-possession)
// claim demands a DPoP proof; a validator that consumes no proof MUST reject the
// assertion rather than silently dropping the sender-constraint. Fail closed.
func TestSEP990IDJAGCnfWithoutProofRejected(t *testing.T) {
	idp := newIDPKey(t, jagIDP, "k1")
	v := newJAGValidator(t, []string{rsResource}, idp.trust())
	tok := idp.mintIDJAG(t, map[string]any{"cnf": map[string]any{"jkt": "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I"}})
	_, err := v.Validate(context.Background(), tok, jagClient)
	wantIDJAGReject(t, err, "cnf")
}

// TestSEP990IDJAGResourcePolicy: the MCP profile (SEP-990) makes resource REQUIRED —
// the RFC 9728 resource identifier of the target MCP server — and the policy
// input is DENY-CLOSED: a resource outside the approved set (or any resource at all,
// under an empty set) refuses the grant, naming the offender.
func TestSEP990IDJAGResourcePolicy(t *testing.T) {
	idp := newIDPKey(t, jagIDP, "k1")

	t.Run("missing resource", func(t *testing.T) {
		v := newJAGValidator(t, []string{rsResource}, idp.trust())
		_, err := v.Validate(context.Background(), idp.mintIDJAG(t, map[string]any{"resource": nil}), jagClient)
		wantIDJAGReject(t, err, "resource is REQUIRED")
	})
	t.Run("unapproved resource named", func(t *testing.T) {
		v := newJAGValidator(t, []string{rsResource}, idp.trust())
		_, err := v.Validate(context.Background(), idp.mintIDJAG(t, map[string]any{"resource": "https://evil.example/mcp"}), jagClient)
		wantIDJAGReject(t, err, `"https://evil.example/mcp"`)
	})
	t.Run("array with one unapproved entry", func(t *testing.T) {
		// resource may be an array (base draft §3.1); EVERY entry must be approved.
		v := newJAGValidator(t, []string{rsResource}, idp.trust())
		tok := idp.mintIDJAG(t, map[string]any{"resource": []string{rsResource, "https://evil.example/mcp"}})
		_, err := v.Validate(context.Background(), tok, jagClient)
		wantIDJAGReject(t, err, `"https://evil.example/mcp"`)
	})
	t.Run("empty approved set refuses everything", func(t *testing.T) {
		// Deny-closed: with NO approved resources, even the otherwise-good grant fails.
		v := newJAGValidator(t, nil, idp.trust())
		_, err := v.Validate(context.Background(), idp.mintIDJAG(t, nil), jagClient)
		wantIDJAGReject(t, err, "not an approved MCP server")
	})
}

// TestSEP990EnterpriseAuthPolicyInput: the policy seam — the connector's
// internal registry rendered as the Enterprise-Managed Authorization policy
// input. Only HTTP servers the registry RECOGNIZES (approved entry or org-owned
// namespace) become delegation targets, with canonical RFC 8707 resource URIs
// (lowercased host, no trailing slash), sorted by resource; an unrecognized HTTP
// server and a stdio server never appear — a shadow server is unreachable by
// construction (OWASP MCP09).
func TestSEP990EnterpriseAuthPolicyInput(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		// Declaration order puts the approved entry FIRST so the sorted output proves
		// the deterministic by-resource ordering.
		"servers": `[
			{"name":"approved-srv","transport":"http","url":"https://Tools.Example.com/approved/"},
			{"name":"owned-srv","transport":"http","url":"https://MCP.Example.com/owned/","registry_name":"com.acme/owned-srv"},
			{"name":"shadow-srv","transport":"http","url":"https://shadow.example/mcp"},
			{"name":"local-stdio","transport":"stdio","command":"echo"}
		]`,
		"owned_namespaces": "com.acme",
		"internal_servers": `[{"name":"approved-srv","version":"3.1.4"}]`,
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := s.EnterpriseAuthPolicyInput()
	want := []ApprovedResource{
		{Resource: "https://mcp.example.com/owned", Name: "owned-srv", RegistryName: "com.acme/owned-srv"},
		{Resource: "https://tools.example.com/approved", Name: "approved-srv", PinnedVersion: "3.1.4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EnterpriseAuthPolicyInput = %+v, want %+v", got, want)
	}

	// End-to-end: feed the derived policy into the validator — a grant for a
	// recognized server redeems; the unrecognized (shadow) server is refused even
	// though it IS configured on the connector (SEP-990 + deny-closed).
	approved := make([]string, 0, len(got))
	for _, r := range got {
		approved = append(approved, r.Resource)
	}
	idp := newIDPKey(t, jagIDP, "k1")
	v := newJAGValidator(t, approved, idp.trust())
	if _, err := v.Validate(context.Background(),
		idp.mintIDJAG(t, map[string]any{"resource": "https://mcp.example.com/owned"}), jagClient); err != nil {
		t.Fatalf("grant for a recognized server must redeem: %v", err)
	}
	_, err := v.Validate(context.Background(),
		idp.mintIDJAG(t, map[string]any{"resource": "https://shadow.example/mcp", "jti": "jag-shadow"}), jagClient)
	wantIDJAGReject(t, err, `"https://shadow.example/mcp"`)
}

// TestIDJAGIssuerClaimedDomainsAndSoleIssuer proves the validator plumbs the operator's
// per-issuer domain claim (NORMALIZED, deduped) and the sole-issuer signal onto the
// validated IDJAG, so a cmd/ bridge can gate the EMA verified-email fallback on domain
// authority (F1). The domains are trust-anchor config, never read off the wire.
func TestIDJAGIssuerClaimedDomainsAndSoleIssuer(t *testing.T) {
	idp := newIDPKey(t, jagIDP, "k1")

	// Single trusted issuer claiming domains in mixed case / with an '@' prefix, a
	// trailing dot and a duplicate — exercises normalizeClaimedDomains.
	trust := idp.trust()
	trust.ClaimedDomains = []string{"Corp.Example", "@partner.example.", "corp.example"}
	v := newJAGValidator(t, []string{rsResource}, trust)

	got, err := v.Validate(context.Background(), idp.mintIDJAG(t, nil), jagClient)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	wantDomains := []string{"corp.example", "partner.example"}
	if !reflect.DeepEqual(got.IssuerClaimedDomains, wantDomains) {
		t.Errorf("IssuerClaimedDomains = %v, want %v (normalized, deduped)", got.IssuerClaimedDomains, wantDomains)
	}
	if !got.SoleTrustedIssuer {
		t.Error("SoleTrustedIssuer = false, want true for a single-issuer keyring")
	}

	// A SECOND trusted issuer flips the sole-issuer signal: an unconstrained co-trusted
	// issuer can no longer vouch by bare email engine-side.
	idp2 := newIDPKey(t, "https://idp2.example", "k2")
	v2 := newJAGValidator(t, []string{rsResource}, trust, idp2.trust())

	got2, err := v2.Validate(context.Background(), idp.mintIDJAG(t, map[string]any{"jti": "jag-multi"}), jagClient)
	if err != nil {
		t.Fatalf("Validate (multi-issuer): %v", err)
	}
	if got2.SoleTrustedIssuer {
		t.Error("SoleTrustedIssuer = true, want false for a multi-issuer keyring")
	}
	if !reflect.DeepEqual(got2.IssuerClaimedDomains, wantDomains) {
		t.Errorf("IssuerClaimedDomains (multi) = %v, want %v", got2.IssuerClaimedDomains, wantDomains)
	}

	// The co-trusted issuer with NO claimed domains carries none — the engine denies its
	// bare-email fallback in this multi-issuer keyring.
	got3, err := v2.Validate(context.Background(), idp2.mintIDJAG(t, map[string]any{"jti": "jag-idp2"}), jagClient)
	if err != nil {
		t.Fatalf("Validate (idp2): %v", err)
	}
	if len(got3.IssuerClaimedDomains) != 0 {
		t.Errorf("idp2 IssuerClaimedDomains = %v, want empty", got3.IssuerClaimedDomains)
	}
	if got3.SoleTrustedIssuer {
		t.Error("idp2 SoleTrustedIssuer = true, want false in a multi-issuer keyring")
	}
}
