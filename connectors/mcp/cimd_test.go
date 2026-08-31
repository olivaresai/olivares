// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

// cimd_test.go audits the CIMD client identity (SEP-991 /
// draft-ietf-oauth-client-id-metadata-document-01) and the MCP client
// identification priority order (2025-11-25 spec, SHOULD): pre-registered → CIMD →
// DCR (RFC 7591, deprecated in the 2026-07-28 RC) → prompt (a loud error for this
// headless client).

// cimdDocURL is a draft-§3-valid client_id URL (https + non-empty path).
const cimdDocURL = "https://plane.olivares.example/oauth/client-metadata.json"

// cimdOAuthClient builds an oauthClient for a fixed MCP server URL with the given
// auth config (doer nil → the default SSRF-guarded client; loopback is allowed, so
// httptest endpoints work).
func cimdOAuthClient(t *testing.T, auth *serverAuth, doer httpDoer) *oauthClient {
	t.Helper()
	c, err := newOAuthClient("https://mcp.example.com/mcp", auth, doer)
	if err != nil {
		t.Fatalf("newOAuthClient: %v", err)
	}
	return c
}

// cimdTestJWKS returns a one-key EC key set: the PUBLIC half when public is true
// (what a CIMD document may publish, draft §6.2), the PRIVATE key otherwise (what it
// must never publish).
func cimdTestJWKS(t *testing.T, public bool) *jose.JSONWebKeySet {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	k := jose.JSONWebKey{Key: key, KeyID: "cimd-k1", Algorithm: "ES256", Use: "sig"}
	if public {
		k.Key = key.Public()
	}
	return &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{k}}
}

// newAssertionJWK mints a PRIVATE EC JWK (alg ES256, with kid) as the operator would
// configure in auth.client_assertion_jwk for private_key_jwt (RFC 7523 §2.2), plus
// the public key an AS would use to verify the resulting client assertion.
func newAssertionJWK(t *testing.T) (privJSON string, pub *ecdsa.PublicKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	jwk := jose.JSONWebKey{Key: key, KeyID: "plane-assert-k1", Algorithm: "ES256", Use: "sig"}
	blob, err := json.Marshal(jwk)
	if err != nil {
		t.Fatalf("marshal private jwk: %v", err)
	}
	return string(blob), &key.PublicKey
}

// TestSEP991ClientIDMetadataURLRules: the CIMD draft §3 client_id URL rules (SEP-991)
// — https scheme, a non-empty path, no dot path segments, no fragment, no
// username/password — plus this implementation's deliberate hardening of the draft's
// "SHOULD NOT include a query string" into a refusal.
func TestSEP991ClientIDMetadataURLRules(t *testing.T) {
	cases := map[string]string{ // raw URL → "" (accepted) or a substring of the refusal
		cimdDocURL:                         "",
		"https://plane.olivares.example/c": "",
		// §3 MUST use https.
		"http://plane.olivares.example/client": "https",
		// §3 MUST contain a path component — neither an empty path nor bare "/".
		"https://plane.olivares.example":  "path component",
		"https://plane.olivares.example/": "path component",
		// §3 MUST NOT contain a fragment.
		"https://plane.olivares.example/client#frag": "fragment",
		// §3 MUST NOT contain a username or password.
		"https://user:pw@plane.olivares.example/client": "username",
		// Draft SHOULD NOT carry a query — refused here (it is our OWN identity URL).
		"https://plane.olivares.example/client?x=1": "query",
		// §3 MUST NOT contain single-dot or double-dot path segments.
		"https://plane.olivares.example/a/../client": "dot path segments",
		"https://plane.olivares.example/./client":    "dot path segments",
	}
	for raw, want := range cases {
		err := validateClientIDMetadataURL(raw)
		if want == "" {
			if err != nil {
				t.Errorf("validateClientIDMetadataURL(%q) unexpected error: %v", raw, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("validateClientIDMetadataURL(%q) = %v, want refusal mentioning %q", raw, err, want)
		}
	}
}

// TestSEP991NewClientMetadataDocument: construction enforces the draft §4.1 hard
// rules — client_id equals the document URL by simple string comparison, no
// shared-symmetric token_endpoint_auth_method (client_secret_basic/post/jwt), and
// private_key_jwt only with a published PUBLIC jwks (§6.2; a private key in the
// document would be a credential leak).
func TestSEP991NewClientMetadataDocument(t *testing.T) {
	redirects := []string{"https://plane.olivares.example/oauth/callback"}

	// Happy path: client_id == documentURL verbatim (§4.1 simple string comparison).
	doc, err := NewClientMetadataDocument(cimdDocURL, "Olivares Plane", redirects, "none", nil)
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if doc.ClientID != cimdDocURL {
		t.Errorf("client_id = %q, must equal the document URL %q (draft §4.1)", doc.ClientID, cimdDocURL)
	}
	if doc.ClientName != "Olivares Plane" || len(doc.RedirectURIs) != 1 || doc.RedirectURIs[0] != redirects[0] {
		t.Errorf("document fields = %+v", doc)
	}

	cases := []struct {
		name      string
		docName   string
		redirects []string
		method    string
		keys      *jose.JSONWebKeySet
		wantErr   string // "" = accepted
	}{
		{"empty client_name refused", "  ", redirects, "none", nil, "client_name"},
		{"no redirect_uris refused", "Plane", nil, "none", nil, "redirect_uri"},
		// Draft §4.1: shared-symmetric client auth methods are forbidden — a CIMD
		// document is public, so a secret registered through it can't be confidential.
		{"client_secret_basic refused", "Plane", redirects, "client_secret_basic", nil, "not allowed"},
		{"client_secret_post refused", "Plane", redirects, "client_secret_post", nil, "not allowed"},
		{"client_secret_jwt refused", "Plane", redirects, "client_secret_jwt", nil, "not allowed"},
		// Unknown/unvetted method: deny-closed, not pass-through.
		{"mtls refused", "Plane", redirects, "mtls", nil, "not allowed"},
		{"none accepted", "Plane", redirects, "none", nil, ""},
		{"private_key_jwt with public jwks accepted", "Plane", redirects, "private_key_jwt", cimdTestJWKS(t, true), ""},
		// §6.2: private_key_jwt is meaningless without published verification keys.
		{"private_key_jwt without jwks refused", "Plane", redirects, "private_key_jwt", nil, "requires a published jwks"},
		// A PRIVATE key in the published document is a credential leak — refused even
		// when the auth method itself is fine.
		{"private key in jwks refused", "Plane", redirects, "none", cimdTestJWKS(t, false), "PUBLIC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClientMetadataDocument(cimdDocURL, tc.docName, tc.redirects, tc.method, tc.keys)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want refusal mentioning %q", err, tc.wantErr)
			}
		})
	}
}

// TestSEP991ClientMetadataDocumentServeHTTP: the hosted document is what an AS
// fetches at the client_id URL (draft §5): GET answers application/json whose
// client_id round-trips to the document URL; any other method is 405.
func TestSEP991ClientMetadataDocumentServeHTTP(t *testing.T) {
	doc, err := NewClientMetadataDocument(cimdDocURL, "Olivares Plane",
		[]string{"https://plane.olivares.example/oauth/callback"}, "none", nil)
	if err != nil {
		t.Fatalf("build document: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, cimdDocURL, nil)
	w := httptest.NewRecorder()
	doc.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got ClientMetadataDocument
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("served document is not valid JSON: %v", err)
	}
	// What the AS will compare (draft §4.1 simple string comparison) is the SERVED
	// client_id — it must survive the round trip unchanged.
	if got.ClientID != cimdDocURL {
		t.Errorf("served client_id = %q, want %q", got.ClientID, cimdDocURL)
	}
	if got.ClientName != doc.ClientName || len(got.RedirectURIs) != 1 {
		t.Errorf("served document = %+v, want round-trip of %+v", got, doc)
	}

	w2 := httptest.NewRecorder()
	doc.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, cimdDocURL, nil))
	if w2.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405 (the document is read-only)", w2.Code)
	}
}

// newRegistrationServer is an RFC 7591 registration endpoint double: it decodes each
// registration request into *got, counts calls into *calls, and answers §3.2.1 with
// a fixed client_id.
func newRegistrationServer(t *testing.T, got *dcrRequest, calls *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, got); err != nil {
			t.Errorf("registration request is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"client_id":"dcr-client-1","client_secret":"dcr-secret"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSEP991ClientIdentitySelectionOrder: the MCP spec's client identification
// priority (2025-11-25, SHOULD; SEP-991 makes CIMD the RECOMMENDED mechanism):
// 1. pre-registered, 2. CIMD when the AS advertises
// client_id_metadata_document_supported, 3. DCR (RFC 7591, explicit opt-in here)
// when a registration_endpoint exists, 4. prompt — which this headless client
// surfaces as an error naming why every option was unavailable.
func TestSEP991ClientIdentitySelectionOrder(t *testing.T) {
	ctx := context.Background()
	const issuer = "https://as.olivares.example"

	t.Run("pre-registered wins even when the AS advertises CIMD", func(t *testing.T) {
		// Step 1 outranks step 2: operator-issued credentials are used even though
		// both CIMD inputs (AS support + configured URL) are present.
		c := cimdOAuthClient(t, &serverAuth{
			ClientID: "cid-pre", ClientSecret: "csec",
			ClientIDMetadataURL: cimdDocURL,
		}, nil)
		id, err := c.clientIdentityFor(ctx, authServerMetadata{Issuer: issuer, ClientIDMetadataSupported: true})
		if err != nil {
			t.Fatalf("clientIdentityFor: %v", err)
		}
		if id.method != identityPreRegistered || id.clientID != "cid-pre" || id.clientSecret != "csec" {
			t.Errorf("identity = %+v, want pre-registered cid-pre", id)
		}
	})

	t.Run("CIMD when advertised and configured", func(t *testing.T) {
		// SEP-991: the CIMD URL is the client_id, verbatim.
		c := cimdOAuthClient(t, &serverAuth{ClientIDMetadataURL: cimdDocURL}, nil)
		id, err := c.clientIdentityFor(ctx, authServerMetadata{Issuer: issuer, ClientIDMetadataSupported: true})
		if err != nil {
			t.Fatalf("clientIdentityFor: %v", err)
		}
		if id.method != identityCIMD || id.clientID != cimdDocURL {
			t.Errorf("identity = %+v, want cimd with client_id %q", id, cimdDocURL)
		}
	})

	t.Run("DCR when CIMD advertised but not configured", func(t *testing.T) {
		// AS supports CIMD but the operator hosts no document → step 2 is unavailable
		// and the explicit DCR opt-in (RFC 7591, deprecated in the RC) takes over.
		var got dcrRequest
		var calls int
		reg := newRegistrationServer(t, &got, &calls)
		c := cimdOAuthClient(t, &serverAuth{DynamicRegistration: true}, reg.Client())
		as := authServerMetadata{Issuer: issuer, ClientIDMetadataSupported: true, RegistrationEndpoint: reg.URL}
		id, err := c.clientIdentityFor(ctx, as)
		if err != nil {
			t.Fatalf("clientIdentityFor: %v", err)
		}
		if id.method != identityDCR || id.clientID != "dcr-client-1" {
			t.Errorf("identity = %+v, want dcr dcr-client-1", id)
		}
		// SEP-837 MUST: the registration request carries an application_type.
		if got.ApplicationType != "web" {
			t.Errorf("registration application_type = %q, want the default \"web\" (SEP-837)", got.ApplicationType)
		}
		// SEP-2352: the registration is keyed by issuer and reused, not re-registered.
		if _, err := c.clientIdentityFor(ctx, as); err != nil {
			t.Fatalf("second clientIdentityFor: %v", err)
		}
		if calls != 1 {
			t.Errorf("registration endpoint called %d times, want 1 (issuer-keyed reuse)", calls)
		}
	})

	t.Run("CIMD skipped when the AS does not advertise it", func(t *testing.T) {
		// A configured CIMD URL is inert without client_id_metadata_document_supported
		// — selection falls through to DCR rather than presenting an unsupported
		// client_id to the AS.
		var got dcrRequest
		var calls int
		reg := newRegistrationServer(t, &got, &calls)
		c := cimdOAuthClient(t, &serverAuth{ClientIDMetadataURL: cimdDocURL, DynamicRegistration: true}, reg.Client())
		as := authServerMetadata{Issuer: issuer, ClientIDMetadataSupported: false, RegistrationEndpoint: reg.URL}
		id, err := c.clientIdentityFor(ctx, as)
		if err != nil {
			t.Fatalf("clientIdentityFor: %v", err)
		}
		if id.method != identityDCR {
			t.Errorf("identity method = %q, want dcr (CIMD must be skipped without AS support)", id.method)
		}
	})

	t.Run("exhausted options error names why each was unavailable", func(t *testing.T) {
		// Step 4 ("prompt the user") for a headless client: a loud, diagnosable error.
		c := cimdOAuthClient(t, &serverAuth{}, nil)
		_, err := c.clientIdentityFor(ctx, authServerMetadata{Issuer: issuer})
		if err == nil {
			t.Fatal("expected an error when no identification option is available")
		}
		for _, want := range []string{"no client identification available", "not advertised by the AS", "no registration_endpoint"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q must mention %q", err, want)
			}
		}
		// Registration endpoint present but no opt-in: the error says so.
		_, err = c.clientIdentityFor(ctx, authServerMetadata{Issuer: issuer, RegistrationEndpoint: "https://as.olivares.example/register"})
		if err == nil || !strings.Contains(err.Error(), "not opted in") {
			t.Errorf("error %v must mention the missing dynamic_registration opt-in", err)
		}
	})

	t.Run("invalid CIMD URL is terminal, not a silent fallback", func(t *testing.T) {
		// An http:// client_id violates draft §3 — and even with DCR available the
		// misconfiguration surfaces instead of silently downgrading past it.
		var got dcrRequest
		var calls int
		reg := newRegistrationServer(t, &got, &calls)
		c := cimdOAuthClient(t, &serverAuth{
			ClientIDMetadataURL: "http://plane.olivares.example/client",
			DynamicRegistration: true,
		}, reg.Client())
		as := authServerMetadata{Issuer: issuer, ClientIDMetadataSupported: true, RegistrationEndpoint: reg.URL}
		_, err := c.clientIdentityFor(ctx, as)
		if err == nil || !strings.Contains(err.Error(), "https") {
			t.Fatalf("err = %v, want a draft §3 https refusal", err)
		}
		if calls != 0 {
			t.Errorf("registration endpoint called %d times, want 0 (no silent DCR fallback)", calls)
		}
	})
}

// TestSEP991CIMDClientCredentialsRequiresAssertionKey: a CIMD document cannot carry
// a shared secret (draft §4.1), so without a private_key_jwt key the identity is a
// PUBLIC client — and the client-credentials grant requires client authentication.
// The refusal happens before any token request leaves the process.
func TestSEP991CIMDClientCredentialsRequiresAssertionKey(t *testing.T) {
	ctx := context.Background()
	c := cimdOAuthClient(t, &serverAuth{ClientIDMetadataURL: cimdDocURL}, nil)
	id, err := c.clientIdentityFor(ctx, authServerMetadata{Issuer: "https://as.olivares.example", ClientIDMetadataSupported: true})
	if err != nil {
		t.Fatalf("clientIdentityFor: %v", err)
	}
	if id.assertionKey != nil {
		t.Fatal("no client_assertion_jwk configured — the identity must carry no key")
	}
	_, err = c.grantClientCredentials(ctx, "https://as.olivares.example/token", id, nil)
	if err == nil || !strings.Contains(err.Error(), "no client_assertion_jwk") {
		t.Fatalf("err = %v, want a refusal explaining the CIMD client has no client_assertion_jwk", err)
	}
	if !strings.Contains(err.Error(), "public") {
		t.Errorf("err %q should explain a key-less CIMD client is public", err)
	}
}

// TestRFC7523CIMDClientAssertion: with a private EC JWK configured, a CIMD identity
// authenticates the client-credentials grant via private_key_jwt (RFC 7523 §2.2,
// the only client authentication a CIMD identity may carry): the token request
// carries the jwt-bearer client_assertion_type and a verifiable JWS whose
// iss == sub == the CIMD URL and aud == the token endpoint — and no HTTP basic auth.
func TestRFC7523CIMDClientAssertion(t *testing.T) {
	ctx := context.Background()
	privJSON, pub := newAssertionJWK(t)

	var gotForm url.Values
	var gotAuthz string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token request form: %v", err)
		}
		gotForm = r.PostForm
		gotAuthz = r.Header.Get("Authorization")
		writeJSON(w, `{"access_token":"tok-cimd","token_type":"Bearer","expires_in":600}`)
	}))
	defer srv.Close()
	tokenEndpoint := srv.URL + "/token"

	c := cimdOAuthClient(t, &serverAuth{ClientIDMetadataURL: cimdDocURL, ClientAssertionJWK: privJSON}, srv.Client())
	c.now = rsClock // deterministic iat/exp on the assertion
	id, err := c.clientIdentityFor(ctx, authServerMetadata{Issuer: "https://as.olivares.example", ClientIDMetadataSupported: true})
	if err != nil {
		t.Fatalf("clientIdentityFor: %v", err)
	}
	if id.assertionKey == nil {
		t.Fatal("a configured client_assertion_jwk must be attached to the CIMD identity")
	}

	tok, err := c.grantClientCredentials(ctx, tokenEndpoint, id, []string{"tools:read"})
	if err != nil {
		t.Fatalf("grantClientCredentials: %v", err)
	}
	if tok.AccessToken != "tok-cimd" {
		t.Errorf("access_token = %q, want tok-cimd", tok.AccessToken)
	}

	// RFC 7523 §2.2 wire shape, asserted on the LITERAL values an AS would see.
	if got := gotForm.Get("grant_type"); got != "client_credentials" {
		t.Errorf("grant_type = %q", got)
	}
	if got := gotForm.Get("client_assertion_type"); got != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
		t.Errorf("client_assertion_type = %q, want the RFC 7523 jwt-bearer URN", got)
	}
	if got := gotForm.Get("client_id"); got != cimdDocURL {
		t.Errorf("client_id = %q, want the CIMD URL %q", got, cimdDocURL)
	}
	// RFC 8707: the resource indicator still audience-binds the token.
	if got := gotForm.Get("resource"); got != "https://mcp.example.com/mcp" {
		t.Errorf("resource = %q", got)
	}
	if gotAuthz != "" {
		t.Errorf("Authorization = %q — private_key_jwt must not be combined with basic auth", gotAuthz)
	}

	// The assertion verifies against the published PUBLIC key (what the AS resolves
	// from the CIMD document's jwks) and asserts the RFC 7523 claims.
	assertion := gotForm.Get("client_assertion")
	if assertion == "" {
		t.Fatal("token request carried no client_assertion")
	}
	parsed, err := jwt.ParseSigned(assertion, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatalf("client_assertion is not a parseable JWS: %v", err)
	}
	if kid := parsed.Headers[0].KeyID; kid != "plane-assert-k1" {
		t.Errorf("assertion kid = %q, want plane-assert-k1 (the AS needs it for key lookup)", kid)
	}
	var cl jwt.Claims
	if err := parsed.Claims(pub, &cl); err != nil {
		t.Fatalf("client_assertion signature/claims: %v", err)
	}
	if cl.Issuer != cimdDocURL || cl.Subject != cimdDocURL {
		t.Errorf("iss = %q, sub = %q; both must be the CIMD client_id %q (RFC 7523 §3)", cl.Issuer, cl.Subject, cimdDocURL)
	}
	if !cl.Audience.Contains(tokenEndpoint) {
		t.Errorf("aud = %v, must contain the token endpoint %q", cl.Audience, tokenEndpoint)
	}
	if cl.ID == "" {
		t.Error("assertion must carry a jti (replay protection)")
	}
	// Short-lived and clocked off the injected clock.
	if !cl.IssuedAt.Time().Equal(rsClock()) {
		t.Errorf("iat = %v, want the injected clock %v", cl.IssuedAt.Time(), rsClock())
	}
	if d := cl.Expiry.Time().Sub(cl.IssuedAt.Time()); d != time.Minute {
		t.Errorf("assertion lifetime = %v, want 60s", d)
	}
}

// TestRFC7523ConfiguredButInvalidAssertionKeyIsAnError: a client_assertion_jwk that
// is CONFIGURED but unparseable, public, or invalid must fail identity selection —
// never be silently treated as absent (which would let a pre-registered identity
// with no client_secret downgrade to an empty-secret basic-auth token request).
func TestRFC7523ConfiguredButInvalidAssertionKeyIsAnError(t *testing.T) {
	pubJWKS := cimdTestJWKS(t, true)
	pubJSON, err := json.Marshal(pubJWKS.Keys[0])
	if err != nil {
		t.Fatalf("marshal public jwk: %v", err)
	}
	cases := map[string]string{
		"not json at all": "not a parseable JWK",
		`{"kty":"oct"}`:   "not a parseable JWK", // go-jose refuses an oct key with no material
		string(pubJSON):   "PRIVATE key",         // a public key configured as the assertion key
	}
	for raw, wantSub := range cases {
		c := cimdOAuthClient(t, &serverAuth{
			ClientID: "pre-reg", ClientAssertionJWK: raw,
		}, nil)
		_, err := c.clientIdentityFor(context.Background(), authServerMetadata{Issuer: "https://as.example"})
		if err == nil || !strings.Contains(err.Error(), wantSub) {
			t.Errorf("clientIdentityFor with assertion jwk %.20q: err = %v, want substring %q", raw, err, wantSub)
		}
	}
	// And the same guard on the CIMD branch.
	c := cimdOAuthClient(t, &serverAuth{
		ClientIDMetadataURL: cimdDocURL, ClientAssertionJWK: "garbage",
	}, nil)
	_, err = c.clientIdentityFor(context.Background(), authServerMetadata{Issuer: "https://as.example", ClientIDMetadataSupported: true})
	if err == nil || !strings.Contains(err.Error(), "parseable JWK") {
		t.Errorf("CIMD identity with garbage assertion jwk: err = %v, want a parse refusal", err)
	}
}
