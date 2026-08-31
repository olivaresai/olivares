// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

// rs_s151_test.go audits the RS-side issuer hardening against the specs it
// implements:
//
//   - RFC 9068 §4 (JWT access tokens at the RS): typ at+jwt, iss MUST exactly match a
//     trusted issuer (no normalization), aud MUST contain this RS's resource URI, the
//     signature MUST verify against THE ISSUER'S OWN keys, exp with small leeway; every
//     failure → 401 invalid_token.
//   - SEP-2352 (issuer-keyed credentials, 2026-07-28 RC): trust anchors are keyed by
//     the issuer that owns them — a key configured for issuer A can never verify a
//     token claiming issuer B, even on a kid collision.
//   - RFC 7662 (introspection for opaque tokens): per-issuer endpoints with the RS's
//     OWN credentials; the first ACTIVE answer is authoritative and terminal.
//   - SEP-2207: offline_access never appears in PRM scopes_supported nor as a tool's
//     required scope (enforced as a constructor error).

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

// rsIssuerB is a SECOND trusted issuer for the multi-issuer (Issuers[]) cases.
const rsIssuerB = "https://auth-b.olivares.example"

// s151Key generates a fresh P-256 signing key and the matching public JWKS under kid,
// so each test controls WHICH issuer owns WHICH key (the SEP-2352 keying under test).
func s151Key(t *testing.T, kid string) (*ecdsa.PrivateKey, []byte) {
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
	return key, blob
}

// s151Mint signs an at+jwt access token with an EXPLICIT key and issuer (unlike
// mintAccessToken, which pins rsIssuer and a fresh key). An empty iss leaves the claim
// OUT of the token entirely (jwt.Claims marshals iss with omitempty) — the RFC 9068 §4
// missing-iss case.
func s151Mint(t *testing.T, key *ecdsa.PrivateKey, kid, iss, sub, aud, scope string, exp time.Time) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", kid))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	std := jwt.Claims{
		Issuer:   iss, // "" ⇒ claim omitted
		Subject:  sub,
		Audience: jwt.Audience{aud},
		IssuedAt: jwt.NewNumericDate(rsClock().Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(exp),
	}
	raw, err := jwt.Signed(signer).Claims(std).Claims(map[string]any{"scope": scope}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

// s151RS builds an RS whose token trust uses the issuer-keyed Issuers form,
// with the same toolset/gate/clock conventions as newRS (rs_test.go).
func s151RS(t *testing.T, issuers []IssuerTrust, up Upstream) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:             rsResource,
		AuthorizationServers: []string{rsIssuer},
		Issuers:              issuers,
		Toolset:              ts,
		Gate:                 fakeToolGate{StatusApproved},
		Upstream:             up,
		DurableTaskStore:     newMemoryDurableTaskStore(),
		Auditor:              &fakeEvidenceJournal{}, // granting journal (enforcement pinned by evidence_test.go)
		Clock:                rsClock,
		// Tests in this file send 2025-11-25 style requests; opt out of the
		// 2026-07-28 header gate so they can focus on multi-issuer trust.
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

// introspectionStub is an RFC 7662 endpoint answering a fixed JSON body, recording
// hits and the credentials/token it was handed (handlers run on the httptest server
// goroutine, hence the mutex). hits makes "the second endpoint was never consulted"
// (no-fallthrough) provable.
type introspectionStub struct {
	mu       sync.Mutex
	hits     int
	gotAuth  string
	gotToken string
	body     string
	srv      *httptest.Server
}

func newIntrospectionStub(t *testing.T, body string) *introspectionStub {
	t.Helper()
	st := &introspectionStub{body: body}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		st.mu.Lock()
		st.hits++
		st.gotAuth = r.Header.Get("Authorization")
		st.gotToken = r.PostFormValue("token")
		st.mu.Unlock()
		writeJSON(w, st.body)
	}))
	t.Cleanup(st.srv.Close)
	return st
}

func (st *introspectionStub) snapshot() (hits int, auth, token string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.hits, st.gotAuth, st.gotToken
}

// activeIntrospection builds an RFC 7662 ACTIVE response for this RS's resource. iss
// is omitted from the response when empty (RFC 7662 §2.2 makes iss OPTIONAL).
func activeIntrospection(iss, sub string) string {
	body := fmt.Sprintf(`{"active":true,"aud":%q,"sub":%q,"scope":"tools:read","exp":%d`,
		rsResource, sub, validExp().Unix())
	if iss != "" {
		body += fmt.Sprintf(`,"iss":%q`, iss)
	}
	return body + "}"
}

// --- RFC 9068 §4: iss is mandatory and issuer-keyed --------------------------------

// TestSEP2352RFC9068MissingIssRejected: RFC 9068 §4 makes iss validation mandatory at
// the RS; a JWT access token carrying NO iss claim cannot select a trust anchor and is
// refused fail-closed → 401 invalid_token (OAuth 2.1 §5.2), upstream never reached.
func TestSEP2352RFC9068MissingIssRejected(t *testing.T) {
	key, jwks := s151Key(t, "k1")
	up := &fakeUpstream{}
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, up)
	// Signed by the TRUSTED key, valid aud/exp/scope — the ONLY defect is the absent iss.
	tok := s151Mint(t, key, "k1", "", "agent:claude", rsResource, "tools:read", validExp())
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(tok, "search", "{}"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing-iss status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Errorf("401 must carry an invalid_token challenge, got %q", w.Header().Get("WWW-Authenticate"))
	}
	if up.called {
		t.Error("a token without iss must NEVER reach the upstream")
	}
	// The validator refuses on the missing iss itself (before any signature work).
	if _, err := rs.validator.validate(context.Background(), tok); err == nil || !strings.Contains(err.Error(), "no iss claim") {
		t.Errorf("want the RFC 9068 missing-iss rejection, got %v", err)
	}
}

// TestSEP2352RFC9068UnknownIssuerRejected: RFC 9068 §4 — the iss MUST exactly match a
// configured trusted issuer. A token signed with the TRUSTED key but claiming an
// unconfigured issuer is refused BEFORE the signature is consulted (trust is keyed by
// iss, never by which key happens to verify) → 401.
func TestSEP2352RFC9068UnknownIssuerRejected(t *testing.T) {
	key, jwks := s151Key(t, "k1")
	up := &fakeUpstream{}
	rs := newRS(t, jwks, fakeToolGate{StatusApproved}, up)
	tok := s151Mint(t, key, "k1", "https://rogue.olivares.example", "agent:claude", rsResource, "tools:read", validExp())
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(tok, "search", "{}"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown-issuer status = %d, want 401", w.Code)
	}
	if up.called {
		t.Error("a foreign-issuer token must NEVER reach the upstream")
	}
	if _, err := rs.validator.validate(context.Background(), tok); err == nil || !strings.Contains(err.Error(), "not a trusted issuer") {
		t.Errorf("want the untrusted-issuer rejection, got %v", err)
	}
}

// TestSEP2352LegacyConfigWithoutIssuerFailsConstruction: Removed the skip-the-iss
// mode — a legacy config (shape) that sets trust anchors but NO issuer
// identifier must refuse to construct (RFC 9068 §4: iss validation is mandatory, so
// every anchor must be keyed by its issuer).
func TestSEP2352LegacyConfigWithoutIssuerFailsConstruction(t *testing.T) {
	_, jwks := s151Key(t, "k1")
	ts, _ := NewToolset(nil)
	_, err := NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer},
		IssuerJWKS: jwks, // anchor present, Issuer EMPTY: pre this skipped the iss check
		Toolset:    ts, Clock: rsClock,
	})
	if err == nil {
		t.Fatal("legacy anchors without an issuer identifier must not construct (no skip-the-iss mode)")
	}
	if !strings.Contains(err.Error(), "no issuer identifier") {
		t.Errorf("error must name the missing issuer identifier, got %q", err)
	}
	// A config with NO trusted issuer at all cites the RFC 9068 mandate explicitly.
	_, err = NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer}, Toolset: ts, Clock: rsClock,
	})
	if err == nil || !strings.Contains(err.Error(), "RFC 9068") {
		t.Errorf("an RS with no trusted issuer must cite the RFC 9068 iss mandate, got %v", err)
	}
}

// TestSEP2352IssuerKeyringConstructionFailClosed: issuer-keyed trust (SEP-2352
// semantics at the RS) must resolve every anchor to EXACTLY ONE issuer at build time —
// a duplicate issuer (ambiguous trust) or an issuer with no anchor (unverifiable
// trust) is a configuration error, never a runtime surprise.
func TestSEP2352IssuerKeyringConstructionFailClosed(t *testing.T) {
	_, jwksA := s151Key(t, "ka")
	_, jwksA2 := s151Key(t, "ka2")
	ts, _ := NewToolset(nil)
	cases := []struct {
		name    string
		legacy  string // legacy cfg.Issuer (folded in as the FIRST keyring entry)
		jwks    []byte // legacy cfg.IssuerJWKS
		issuers []IssuerTrust
		wantSub string
	}{
		{
			name:    "duplicate issuer across Issuers entries",
			issuers: []IssuerTrust{{Issuer: rsIssuer, JWKS: jwksA}, {Issuer: rsIssuer, JWKS: jwksA2}},
			wantSub: "duplicate trusted issuer",
		},
		{
			name:    "legacy entry duplicated by an Issuers entry",
			legacy:  rsIssuer,
			jwks:    jwksA,
			issuers: []IssuerTrust{{Issuer: rsIssuer, JWKS: jwksA2}},
			wantSub: "duplicate trusted issuer",
		},
		{
			name:    "issuer with no trust anchor",
			issuers: []IssuerTrust{{Issuer: rsIssuer}},
			wantSub: "declares no trust anchor",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewResourceServer(ResourceServerConfig{
				Resource: rsResource, AuthorizationServers: []string{rsIssuer},
				Issuer: tc.legacy, IssuerJWKS: tc.jwks, Issuers: tc.issuers,
				Toolset: ts, Clock: rsClock,
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("want construction error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

// TestSEP2352CrossIssuerKidCollisionRejected: the issuer-keyed isolation itself.
// RFC 9068 §4: "The resource server MUST use the keys provided by the authorization
// server" — the keys of THE issuer the token names. Two trusted issuers share a kid
// but hold DIFFERENT keys; a token claiming iss=B signed with A's key MUST fail, even
// though A's key would verify the signature (no cross-issuer kid collisions, SEP-2352).
func TestSEP2352CrossIssuerKidCollisionRejected(t *testing.T) {
	keyA, jwksA := s151Key(t, "shared")
	keyB, jwksB := s151Key(t, "shared") // SAME kid, DIFFERENT key
	up := &fakeUpstream{}
	rs := s151RS(t, []IssuerTrust{
		{Issuer: rsIssuer, JWKS: jwksA},
		{Issuer: rsIssuerB, JWKS: jwksB},
	}, up)

	// iss=B, signed with A's key: B's keyring is the ONLY one consulted → reject.
	forged := s151Mint(t, keyA, "shared", rsIssuerB, "agent:claude", rsResource, "tools:read", validExp())
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(forged, "search", "{}"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cross-issuer kid-collision status = %d, want 401", w.Code)
	}
	if up.called {
		t.Error("a token signed with another issuer's key must NEVER reach the upstream")
	}
	// The rejection point is the SIGNATURE under B's own keyring (iss lookup and kid
	// resolution both succeed) — proof of isolation, not an unknown-issuer miss.
	if _, err := rs.validator.validate(context.Background(), forged); err == nil || !strings.Contains(err.Error(), "token signature") {
		t.Errorf("want a signature failure under issuer B's own keyring, got %v", err)
	}
	// Sanity: the SAME claims signed with B's OWN key are accepted (the 401 above was
	// the issuer keying, not the claims).
	genuine := s151Mint(t, keyB, "shared", rsIssuerB, "agent:claude", rsResource, "tools:read", validExp())
	w2 := httptest.NewRecorder()
	rs.ServeHTTP(w2, toolsCallReq(genuine, "search", "{}"))
	if w2.Code != http.StatusOK {
		t.Fatalf("issuer B's own token must be accepted, got %d (%s)", w2.Code, w2.Body.String())
	}
}

// TestSEP2352MultiIssuerHappyPath: two trusted issuers, each with its OWN JWKS in
// Issuers[] (RFC 9068 §4 multi-AS deployment). A valid audience-bound token from
// EITHER issuer is accepted, attributed to ITS issuer, with subject and scopes intact.
func TestSEP2352MultiIssuerHappyPath(t *testing.T) {
	keyA, jwksA := s151Key(t, "ka")
	keyB, jwksB := s151Key(t, "kb")
	up := &fakeUpstream{}
	rs := s151RS(t, []IssuerTrust{
		{Issuer: rsIssuer, JWKS: jwksA},
		{Issuer: rsIssuerB, JWKS: jwksB},
	}, up)
	cases := []struct {
		iss string
		key *ecdsa.PrivateKey
		kid string
		sub string
	}{
		{rsIssuer, keyA, "ka", "agent:from-a"},
		{rsIssuerB, keyB, "kb", "agent:from-b"},
	}
	for _, tc := range cases {
		tok := s151Mint(t, tc.key, tc.kid, tc.iss, tc.sub, rsResource, "tools:read", validExp())
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, toolsCallReq(tok, "search", "{}"))
		if w.Code != http.StatusOK {
			t.Fatalf("issuer %s token status = %d, want 200 (%s)", tc.iss, w.Code, w.Body.String())
		}
		if up.gotReq.Subject != tc.sub {
			t.Errorf("issuer %s: upstream subject = %q, want %q", tc.iss, up.gotReq.Subject, tc.sub)
		}
		// Attribution: the validated token names ITS OWN issuer and carries its scopes.
		vt, err := rs.validator.validate(context.Background(), tok)
		if err != nil {
			t.Fatalf("issuer %s token must validate: %v", tc.iss, err)
		}
		if vt.Issuer != tc.iss {
			t.Errorf("validated issuer = %q, want %q", vt.Issuer, tc.iss)
		}
		if !vt.hasScope("tools:read") {
			t.Errorf("issuer %s token must carry scope tools:read, got %v", tc.iss, vt.Scopes)
		}
	}
}

// --- RFC 7662: opaque tokens, issuer-keyed introspection ----------------------------

// TestSEP2352RFC7662IntrospectionFanOut: an opaque token does not say who minted it, so
// the RS introspects (RFC 7662) at each trusted issuer's OWN endpoint with the RS's OWN
// per-issuer credential, in declaration order. Inactive at the first issuer → the
// second is consulted; ACTIVE with aud=this resource → accepted and attributed to the
// SECOND issuer.
func TestSEP2352RFC7662IntrospectionFanOut(t *testing.T) {
	const issA = "https://as-one.olivares.example"
	const issB = "https://as-two.olivares.example"
	inactive := newIntrospectionStub(t, `{"active":false}`)
	active := newIntrospectionStub(t, activeIntrospection(issB, "agent:opaque"))
	up := &fakeUpstream{}
	// Doer nil ⇒ the default SSRF-guarded client; httptest URLs are loopback http,
	// which validateOutboundURL allows (local development exemption).
	rs := s151RS(t, []IssuerTrust{
		{Issuer: issA, IntrospectionURL: inactive.srv.URL, IntrospectionAuth: "Basic cnM6b25l"},
		{Issuer: issB, IntrospectionURL: active.srv.URL, IntrospectionAuth: "Basic cnM6dHdv"},
	}, up)

	vt, err := rs.validator.validate(context.Background(), "opaque-token-1")
	if err != nil {
		t.Fatalf("opaque token active at the second trusted issuer must validate: %v", err)
	}
	if vt.Issuer != issB {
		t.Errorf("validated issuer = %q, want attribution to the SECOND issuer %q", vt.Issuer, issB)
	}
	if vt.Subject != "agent:opaque" || !vt.hasScope("tools:read") {
		t.Errorf("subject/scopes = %q/%v, want agent:opaque with tools:read", vt.Subject, vt.Scopes)
	}
	if vt.TokenType != "opaque" {
		t.Errorf("token type = %q, want opaque", vt.TokenType)
	}
	// Fan-out order + credentials: the FIRST issuer was consulted (and moved past),
	// each endpoint with ITS OWN RS credential (RFC 7662 §2.1) — never the inbound
	// bearer as authorization.
	if hits, auth, tok := inactive.snapshot(); hits != 1 || auth != "Basic cnM6b25l" || tok != "opaque-token-1" {
		t.Errorf("first endpoint saw hits=%d auth=%q token=%q", hits, auth, tok)
	}
	if _, auth, _ := active.snapshot(); auth != "Basic cnM6dHdv" {
		t.Errorf("second endpoint must receive ITS OWN credential, got %q", auth)
	}
	// End-to-end: the same bearer is admitted by the RS (200 on a scoped tools/call).
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq("opaque-token-1", "search", "{}"))
	if w.Code != http.StatusOK {
		t.Fatalf("opaque-token tools/call status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
}

// TestSEP2352RFC7662CrossIssuerActiveHardReject: an ACTIVE introspection answer is
// authoritative and TERMINAL. When the first endpoint answers active but its response
// iss names a DIFFERENT issuer than the endpoint was configured for, that is
// cross-issuer confusion (an attack or a misconfiguration) → 401, with NO fallthrough
// to a second endpoint that would have said active (RFC 9068 §4 exact-match semantics
// applied to the RFC 7662 response).
func TestSEP2352RFC7662CrossIssuerActiveHardReject(t *testing.T) {
	const issA = "https://as-one.olivares.example"
	const issB = "https://as-two.olivares.example"
	// First endpoint (configured for issA) claims the token belongs to issB.
	confused := newIntrospectionStub(t, activeIntrospection(issB, "agent:x"))
	// Second endpoint would answer ACTIVE for its own issuer — it must never be asked.
	wouldAccept := newIntrospectionStub(t, activeIntrospection(issB, "agent:x"))
	up := &fakeUpstream{}
	rs := s151RS(t, []IssuerTrust{
		{Issuer: issA, IntrospectionURL: confused.srv.URL},
		{Issuer: issB, IntrospectionURL: wouldAccept.srv.URL},
	}, up)

	if _, err := rs.validator.validate(context.Background(), "opaque-token-2"); err == nil || !strings.Contains(err.Error(), "cross-issuer") {
		t.Errorf("want the cross-issuer hard reject, got %v", err)
	}
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq("opaque-token-2", "search", "{}"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cross-issuer introspection status = %d, want 401", w.Code)
	}
	if up.called {
		t.Error("a cross-issuer introspection answer must NEVER admit the call")
	}
	// No fallthrough after an ACTIVE answer: across BOTH validations above, the second
	// endpoint was never consulted.
	if hits, _, _ := wouldAccept.snapshot(); hits != 0 {
		t.Errorf("second endpoint consulted %d times after an ACTIVE answer, want 0 (terminal reject)", hits)
	}
}

// TestSEP2352RFC7662MissingIssAttributedToEndpoint: RFC 7662 §2.2 makes iss OPTIONAL in
// an introspection response. An ACTIVE answer without iss (but with aud naming this
// resource) is accepted and attributed to the issuer the ENDPOINT was configured for —
// the endpoint itself is the trust anchor.
func TestSEP2352RFC7662MissingIssAttributedToEndpoint(t *testing.T) {
	const issA = "https://as-one.olivares.example"
	st := newIntrospectionStub(t, activeIntrospection("", "agent:noiss")) // no iss in the response
	rs := s151RS(t, []IssuerTrust{{Issuer: issA, IntrospectionURL: st.srv.URL}}, &fakeUpstream{})
	vt, err := rs.validator.validate(context.Background(), "opaque-token-3")
	if err != nil {
		t.Fatalf("active answer without iss (aud matches) must validate: %v", err)
	}
	if vt.Issuer != issA {
		t.Errorf("validated issuer = %q, want the endpoint's configured issuer %q", vt.Issuer, issA)
	}
	if vt.Subject != "agent:noiss" || vt.TokenType != "opaque" {
		t.Errorf("subject/type = %q/%q, want agent:noiss/opaque", vt.Subject, vt.TokenType)
	}
}

// TestSEP2352RFC7662EmptySubjectRejected is the OPAQUE-path twin (H3) of the JWT-path
// TestF06EmptySubjectTokenRejected: an ACTIVE introspection answer whose sub is empty or
// whitespace is unattributable and must be a TERMINAL 401 reject (mirror idjag.go and the
// JWT path), never admitted as an anonymous principal that every downstream PEP decision
// keys off. The reject is code-present already (introspectAt); this pins it against a
// regression on the opaque path.
func TestSEP2352RFC7662EmptySubjectRejected(t *testing.T) {
	const issA = "https://as-one.olivares.example"
	for _, tc := range []struct {
		name string
		sub  string
	}{
		{"empty sub", ""},
		{"whitespace sub", "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newIntrospectionStub(t, activeIntrospection(issA, tc.sub))
			up := &fakeUpstream{}
			rs := s151RS(t, []IssuerTrust{{Issuer: issA, IntrospectionURL: st.srv.URL}}, up)

			// The validator rejects on the missing subject (terminal — an ACTIVE answer,
			// so not a fallthrough to another issuer).
			if _, err := rs.validator.validate(context.Background(), "opaque-nosub"); err == nil || !strings.Contains(err.Error(), "subject is mandatory") {
				t.Errorf("want the subject-mandatory rejection, got %v", err)
			}
			// End-to-end: 401 invalid_token, upstream never reached.
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, toolsCallReq("opaque-nosub", "search", "{}"))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("subject-less opaque token status = %d, want 401", w.Code)
			}
			if up.called {
				t.Error("a subject-less opaque token must NEVER reach the upstream")
			}
		})
	}
}

// --- SEP-2207: offline_access is never a resource requirement -----------------------

// TestSEP2207OfflineAccessConfigRejected: SEP-2207 — MCP servers SHOULD NOT include
// offline_access in WWW-Authenticate scope or PRM scopes_supported (refresh tokens are
// a client↔AS concern, never a resource permission). This RS enforces the SHOULD as a
// constructor error, both for the ADVERTISED scopes and for a tool that would DEMAND it
// (which would surface in a step-up challenge).
func TestSEP2207OfflineAccessConfigRejected(t *testing.T) {
	_, jwks := s151Key(t, "k1")
	base := func(scopes []string, ts *Toolset) (*ResourceServer, error) {
		return NewResourceServer(ResourceServerConfig{
			Resource: rsResource, AuthorizationServers: []string{rsIssuer},
			Issuer: rsIssuer, IssuerJWKS: jwks,
			ScopesSupported: scopes, Toolset: ts, Clock: rsClock,
		})
	}

	// (a) offline_access in scopes_supported → refused loudly, citing SEP-2207.
	ts, _ := NewToolset(nil)
	if _, err := base([]string{"tools:read", "offline_access"}, ts); err == nil || !strings.Contains(err.Error(), "SEP-2207") {
		t.Errorf("scopes_supported with offline_access must be a SEP-2207 constructor error, got %v", err)
	}

	// (b) a ToolPolicy DEMANDING offline_access → refused (it would otherwise leak into
	// the PRM scope union and the 403 step-up challenge).
	tsBad, terr := NewToolset([]ToolPolicy{{Name: "sync", RequiredScope: scopeOfflineAccess}})
	if terr != nil {
		t.Fatalf("toolset: %v", terr)
	}
	if _, err := base(nil, tsBad); err == nil || !strings.Contains(err.Error(), "SEP-2207") {
		t.Errorf("a tool policy requiring offline_access must be a SEP-2207 constructor error, got %v", err)
	}

	// Sanity: the same config WITHOUT offline_access constructs.
	if _, err := base([]string{"tools:read"}, ts); err != nil {
		t.Errorf("clean config must construct: %v", err)
	}
}
