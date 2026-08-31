// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

// oauth_s151_test.go audits the CLIENT side of the auth-currency work, SEP by
// SEP against the 2026-07-28 RC authorization SEPs:
//
//	SEP-2351  AS metadata discovery candidate order + RFC 8414 §3.3 issuer check
//	SEP-2468  RFC 9207 authorization-response iss validation (mix-up defense)
//	SEP-2352  credentials keyed by issuer, never reused across ASes
//	SEP-837   DCR application_type MUST + meaningful RFC 7591 rejection errors
//	SEP-2207  refresh tokens parsed/stored/rotated by issuer; offline_access gating
//	SEP-2350  client-side scope accumulation + one step-up retry (with SEP-835)
//
// Every fake AS/RS is an httptest server (loopback HTTP is the one non-HTTPS form
// validateOutboundURL admits), following the wiring pattern of
// TestMCPOAuthPhase2Authorized in oauth_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// newTestOAuthClient builds an oauthClient for serverURL or fails the test.
func newTestOAuthClient(t *testing.T, serverURL string, auth *serverAuth) *oauthClient {
	t.Helper()
	c, err := newOAuthClient(serverURL, auth, nil)
	if err != nil {
		t.Fatalf("newOAuthClient: %v", err)
	}
	return c
}

// cloneForm copies a parsed form so it can be asserted after the handler returns.
func cloneForm(in url.Values) url.Values {
	out := url.Values{}
	for k, vs := range in {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// --- SEP-2351: AS metadata discovery ---------------------------------------------

// TestSEP2351ASMetadataDiscoveryFallsThroughCandidatesInOrder: SEP-2351 / RFC 8414
// §3 + OIDC Discovery — for a path-bearing issuer the client MUST try the candidates
// in the exact order RFC 8414 insertion → OIDC insertion → OIDC appending, moving on
// when a candidate fails to FETCH. The AS here 404s the first candidate and serves
// the document at the second (the openid-configuration insertion); the recorded
// request paths prove both the order and that the third candidate was never needed.
func TestSEP2351ASMetadataDiscoveryFallsThroughCandidatesInOrder(t *testing.T) {
	var (
		mu    sync.Mutex
		paths []string
	)
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/.well-known/openid-configuration/tenant" {
			writeJSON(w, fmt.Sprintf(`{"issuer":%q,"token_endpoint":%q}`, base+"/tenant", base+"/tenant/token"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	base = srv.URL
	defer srv.Close()

	issuer := base + "/tenant"
	as, err := discoverASMetadata(context.Background(), srv.Client(), issuer)
	if err != nil {
		t.Fatalf("discoverASMetadata: %v", err)
	}
	if as.Issuer != issuer {
		t.Errorf("issuer = %q, want %q", as.Issuer, issuer)
	}
	want := []string{
		"/.well-known/oauth-authorization-server/tenant", // RFC 8414 insertion, first
		"/.well-known/openid-configuration/tenant",       // OIDC insertion, second (served)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != len(want) {
		t.Fatalf("requests = %v, want exactly %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("candidate[%d] = %q, want %q (SEP-2351 exact order)", i, paths[i], want[i])
		}
	}
}

// TestSEP2351ASMetadataIssuerMismatchIsTerminal: RFC 8414 §3.3 (a MUST in the RC) —
// a retrieved metadata document whose issuer is not IDENTICAL to the issuer the URL
// was built from MUST NOT be used. A mismatch is an impersonation signal, so it is
// TERMINAL: the remaining candidates are not tried (only one request must be seen).
func TestSEP2351ASMetadataIssuerMismatchIsTerminal(t *testing.T) {
	var (
		mu    sync.Mutex
		paths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/.well-known/oauth-authorization-server/tenant" {
			// Answers the first candidate but declares a different issuer.
			writeJSON(w, `{"issuer":"https://impostor.example","token_endpoint":"https://impostor.example/token"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := discoverASMetadata(context.Background(), srv.Client(), srv.URL+"/tenant")
	if err == nil {
		t.Fatal("metadata with a mismatched issuer must be refused (RFC 8414 §3.3)")
	}
	if !strings.Contains(err.Error(), "RFC 8414") {
		t.Errorf("error should cite the RFC 8414 §3.3 reject, got: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 {
		t.Errorf("issuer mismatch must be terminal — no further candidates; requests = %v", paths)
	}
}

// --- SEP-2468: RFC 9207 authorization-response iss validation ---------------------

// TestSEP2468AuthorizationResponseIssDecisionTable: SEP-2468 / RFC 9207 §2.4 — the
// full client decision table, driven through beginAuthorization (which records the
// validated issuer + the AS's advertised support) → validateAuthorizationResponse:
//
//	flag=true  + iss present → simple string comparison (byte-for-byte)
//	flag=true  + iss absent  → reject (the AS promised the parameter)
//	flag=false + iss present → compare anyway
//	flag=false + iss absent  → proceed
//
// plus: case-folded issuers MUST mismatch (no normalization of any kind), and on a
// mismatch the response's error/error_description MUST NOT be acted on — only a
// MATCHING iss lets an error code surface.
func TestSEP2468AuthorizationResponseIssDecisionTable(t *testing.T) {
	const (
		issAS = "https://as.example"
		state = "st-9207"
	)
	c := newTestOAuthClient(t, "https://mcp.example.com/mcp", &serverAuth{ClientID: "cid"})
	identity := clientIdentity{method: identityPreRegistered, clientID: "cid", clientSecret: "csec"}

	cases := []struct {
		name      string
		issuer    string // recorded from validated AS metadata
		supported bool   // authorization_response_iss_parameter_supported
		query     url.Values
		wantCode  string // non-empty → the row must be accepted with this code
		wantErr   string // substring the rejection must carry
		forbidErr string // substring the rejection must NOT carry (attacker-controlled)
	}{
		{
			name: "supported + iss present + matching: accepted", issuer: issAS, supported: true,
			query:    url.Values{"iss": {issAS}, "state": {state}, "code": {"c-1"}},
			wantCode: "c-1",
		},
		{
			name: "supported + iss absent: rejected (the AS promised it)", issuer: issAS, supported: true,
			query:   url.Values{"state": {state}, "code": {"c-2"}},
			wantErr: "RFC 9207",
		},
		{
			name: "unsupported + iss present + matching: compared anyway, accepted", issuer: issAS, supported: false,
			query:    url.Values{"iss": {issAS}, "state": {state}, "code": {"c-3"}},
			wantCode: "c-3",
		},
		{
			name: "unsupported + iss present + mismatching: compared anyway, rejected", issuer: issAS, supported: false,
			query:   url.Values{"iss": {"https://attacker.example"}, "state": {state}, "code": {"c-4"}},
			wantErr: "does not match",
		},
		{
			name: "unsupported + iss absent: proceeds", issuer: issAS, supported: false,
			query:    url.Values{"state": {state}, "code": {"c-5"}},
			wantCode: "c-5",
		},
		{
			// RFC 9207 §2.4: simple string comparison — no scheme/host case folding.
			name: "case-folded issuer is NOT identical: rejected", issuer: "https://AS.example", supported: true,
			query:   url.Values{"iss": {"https://as.example"}, "state": {state}, "code": {"c-6"}},
			wantErr: "does not match",
		},
		{
			// Mismatch on an ERROR response: error/error_description are the
			// attacker's — they must not appear in what the client surfaces.
			name: "error response with mismatched iss: error params suppressed", issuer: issAS, supported: true,
			query: url.Values{"iss": {"https://attacker.example"}, "error": {"access_denied"},
				"error_description": {"free text from the AS"}},
			wantErr:   "does not match",
			forbidErr: "access_denied",
		},
		{
			// An honest AS's denial: the CODE is surfaced, the free text never is.
			name: "error response with matching iss: error code surfaced", issuer: issAS, supported: true,
			query: url.Values{"iss": {issAS}, "state": {state}, "error": {"access_denied"},
				"error_description": {"free text from the AS"}},
			wantErr:   "access_denied",
			forbidErr: "free text from the AS",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			as := authServerMetadata{
				Issuer:                    tc.issuer,
				AuthorizationEndpoint:     "https://as.example/authorize",
				TokenEndpoint:             "https://as.example/token",
				AuthzResponseIssSupported: tc.supported,
			}
			_, p, err := c.beginAuthorization(as, identity, "https://app/cb", state, []string{"read"})
			if err != nil {
				t.Fatalf("beginAuthorization: %v", err)
			}
			code, err := p.validateAuthorizationResponse(tc.query)
			if tc.wantCode != "" {
				if err != nil {
					t.Fatalf("row must be accepted, got error: %v", err)
				}
				if code != tc.wantCode {
					t.Errorf("code = %q, want %q", code, tc.wantCode)
				}
				return
			}
			if err == nil {
				t.Fatalf("row must be rejected, got code %q", code)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should contain %q", err, tc.wantErr)
			}
			if tc.forbidErr != "" && strings.Contains(err.Error(), tc.forbidErr) {
				t.Errorf("error %q must NOT carry the response's error params (%q)", err, tc.forbidErr)
			}
		})
	}
}

// TestSEP2468RedeemCodeRequiresValidatedAuthorizationResponse: SEP-2468 — the iss
// validation happens BEFORE the code reaches any token endpoint; the ordering is
// structural: redeemCode refuses to run when validateAuthorizationResponse never
// accepted the response. After a valid response the redemption POSTs the PKCE
// code_verifier (RFC 7636) AND the resource indicator (RFC 8707) to the token
// endpoint, asserted server-side.
func TestSEP2468RedeemCodeRequiresValidatedAuthorizationResponse(t *testing.T) {
	var (
		mu    sync.Mutex
		forms []url.Values
	)
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		forms = append(forms, cloneForm(r.PostForm))
		mu.Unlock()
		writeJSON(w, `{"access_token":"at-code","token_type":"Bearer"}`)
	}))
	defer tokenSrv.Close()

	c := newTestOAuthClient(t, "https://mcp.example.com/mcp", &serverAuth{ClientID: "cid", ClientSecret: "csec"})
	identity := clientIdentity{method: identityPreRegistered, clientID: "cid", clientSecret: "csec"}
	as := authServerMetadata{
		Issuer:                    "https://as.example",
		AuthorizationEndpoint:     "https://as.example/authorize",
		TokenEndpoint:             tokenSrv.URL + "/token",
		AuthzResponseIssSupported: true,
	}
	_, p, err := c.beginAuthorization(as, identity, "https://app/cb", "st-1", []string{"read"})
	if err != nil {
		t.Fatalf("beginAuthorization: %v", err)
	}

	// Structural refusal: the response was never validated → no token-endpoint call.
	if _, err := c.redeemCode(context.Background(), &p, "code-1"); err == nil {
		t.Fatal("redeemCode must refuse an unvalidated authorization response (RFC 9207)")
	} else if !strings.Contains(err.Error(), "not validated") {
		t.Errorf("refusal should say the response was not validated, got: %v", err)
	}
	mu.Lock()
	if len(forms) != 0 {
		t.Fatalf("the token endpoint must not have been contacted, saw %d requests", len(forms))
	}
	mu.Unlock()

	// A valid response (matching iss + state) unlocks the redemption.
	code, err := p.validateAuthorizationResponse(url.Values{
		"iss": {"https://as.example"}, "state": {"st-1"}, "code": {"code-1"},
	})
	if err != nil {
		t.Fatalf("validateAuthorizationResponse: %v", err)
	}
	tok, err := c.redeemCode(context.Background(), &p, code)
	if err != nil {
		t.Fatalf("redeemCode after validation: %v", err)
	}
	if tok.AccessToken != "at-code" {
		t.Errorf("access token = %q", tok.AccessToken)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(forms) != 1 {
		t.Fatalf("token requests = %d, want 1", len(forms))
	}
	form := forms[0]
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "code-1" {
		t.Errorf("token form = %v", form)
	}
	if p.pkce.verifier == "" || form.Get("code_verifier") != p.pkce.verifier {
		t.Errorf("code_verifier = %q, want the PKCE verifier %q (RFC 7636)", form.Get("code_verifier"), p.pkce.verifier)
	}
	if form.Get("resource") != "https://mcp.example.com/mcp" {
		t.Errorf("resource = %q, want the canonical server URI (RFC 8707)", form.Get("resource"))
	}
}

// --- SEP-2352: credentials keyed by issuer ----------------------------------------

// TestSEP2352DCRCredentialsKeyedByIssuer: SEP-2352 (MUST) — credentials obtained via
// DCR are stored keyed by the issuer that minted them and never presented to another
// AS: resolving the identity against a SECOND issuer triggers a FRESH registration
// (two registration calls, two distinct stored ids), and re-resolving the first
// issuer reuses its own registration without a third call.
func TestSEP2352DCRCredentialsKeyedByIssuer(t *testing.T) {
	newRegServer := func(clientID string, calls *int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/register" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			*calls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, fmt.Sprintf(`{"client_id":%q,"client_secret":"sec"}`, clientID))
		}))
	}
	var callsA, callsB int
	srvA := newRegServer("cid-a", &callsA)
	defer srvA.Close()
	srvB := newRegServer("cid-b", &callsB)
	defer srvB.Close()

	c := newTestOAuthClient(t, "https://mcp.example.com/mcp", &serverAuth{DynamicRegistration: true})
	asA := authServerMetadata{Issuer: "https://as-a.example", RegistrationEndpoint: srvA.URL + "/register"}
	asB := authServerMetadata{Issuer: "https://as-b.example", RegistrationEndpoint: srvB.URL + "/register"}

	ctx := context.Background()
	idA, err := c.clientIdentityFor(ctx, asA)
	if err != nil {
		t.Fatalf("identity for AS-A: %v", err)
	}
	idB, err := c.clientIdentityFor(ctx, asB)
	if err != nil {
		t.Fatalf("identity for AS-B: %v", err)
	}
	if idA.clientID != "cid-a" || idB.clientID != "cid-b" || idA.clientID == idB.clientID {
		t.Errorf("ids = %q / %q — a second issuer must get a FRESH registration", idA.clientID, idB.clientID)
	}
	if callsA != 1 || callsB != 1 {
		t.Errorf("registration calls = A:%d B:%d, want one each", callsA, callsB)
	}

	// The store keys strictly by issuer: each registration only reachable by its own.
	regA, okA := c.store.registrationFor("https://as-a.example")
	regB, okB := c.store.registrationFor("https://as-b.example")
	if !okA || regA.clientID != "cid-a" || !okB || regB.clientID != "cid-b" {
		t.Errorf("stored registrations = %+v (%v) / %+v (%v)", regA, okA, regB, okB)
	}

	// Re-resolving issuer A reuses ITS registration — no third registration call.
	idA2, err := c.clientIdentityFor(ctx, asA)
	if err != nil {
		t.Fatalf("re-resolve AS-A: %v", err)
	}
	if idA2.clientID != "cid-a" || callsA != 1 {
		t.Errorf("re-resolution must reuse the issuer-keyed registration (id %q, calls %d)", idA2.clientID, callsA)
	}
}

// TestSEP2352PreRegisteredCredentialsRefuseForeignIssuer: SEP-2352 — pre-registered
// credentials pinned (auth.issuer) to AS-A are NEVER presented to a different
// discovered issuer: the flow fails loudly, citing the SEP. The unpinned variant
// TOFU-binds to the first discovered issuer and refuses a later switch the same way.
func TestSEP2352PreRegisteredCredentialsRefuseForeignIssuer(t *testing.T) {
	ctx := context.Background()

	// Explicit pin: configured for AS-A, discovery resolved AS-B.
	pinned := newTestOAuthClient(t, "https://mcp.example.com/mcp",
		&serverAuth{ClientID: "cid", ClientSecret: "csec", Issuer: "https://as-a.example"})
	_, err := pinned.clientIdentityFor(ctx, authServerMetadata{Issuer: "https://as-b.example"})
	if err == nil {
		t.Fatal("pinned credentials must not be presented to a different issuer")
	}
	if !strings.Contains(err.Error(), "SEP-2352") {
		t.Errorf("error should cite SEP-2352, got: %v", err)
	}

	// No pin: the first discovered issuer wins (TOFU); a later issuer is refused.
	tofu := newTestOAuthClient(t, "https://mcp.example.com/mcp",
		&serverAuth{ClientID: "cid", ClientSecret: "csec"})
	if _, err := tofu.clientIdentityFor(ctx, authServerMetadata{Issuer: "https://as-a.example"}); err != nil {
		t.Fatalf("first issuer must bind: %v", err)
	}
	if _, err := tofu.clientIdentityFor(ctx, authServerMetadata{Issuer: "https://as-b.example"}); err == nil {
		t.Fatal("a mid-run issuer switch must be refused (SEP-2352 TOFU bind)")
	} else if !strings.Contains(err.Error(), "SEP-2352") {
		t.Errorf("error should cite SEP-2352, got: %v", err)
	}
}

// --- SEP-837: DCR application_type + meaningful registration errors ----------------

// TestSEP837DCRApplicationType: SEP-837 (MUST) — every DCR request carries
// application_type: "web" by default (this connector is a server-side service) and
// "native" when the operator overrides it. The request BODY is asserted server-side.
func TestSEP837DCRApplicationType(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, raw)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"client_id":"cid-dcr","client_secret":"sec-dcr"}`)
	}))
	defer srv.Close()

	as := authServerMetadata{Issuer: "https://as.example", RegistrationEndpoint: srv.URL + "/register"}
	for _, tc := range []struct{ name, appType, want string }{
		{"default is web", "", "web"},
		{"native override", "native", "native"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestOAuthClient(t, "https://mcp.example.com/mcp",
				&serverAuth{DynamicRegistration: true, ApplicationType: tc.appType})
			id, err := c.registerClient(context.Background(), as)
			if err != nil {
				t.Fatalf("registerClient: %v", err)
			}
			if id.clientID != "cid-dcr" {
				t.Errorf("client id = %q", id.clientID)
			}
			mu.Lock()
			raw := bodies[len(bodies)-1]
			mu.Unlock()
			var req map[string]any
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatalf("decode registration body: %v", err)
			}
			got, present := req["application_type"]
			if !present {
				t.Fatal("registration request must carry application_type (SEP-837 MUST)")
			}
			if got != tc.want {
				t.Errorf("application_type = %v, want %q", got, tc.want)
			}
		})
	}
}

// TestSEP837RegistrationErrorSurfacesRFC7591Code: SEP-837 — a rejected registration
// surfaces the RFC 7591 §3.2.2 error CODE as a meaningful error, not a bare HTTP
// status, so the operator can fix application_type/redirect_uris. The free-text
// error_description is third-party input and MUST NOT pass through (minimal data —
// the same rule authzresponse.go applies to authorization error responses).
func TestSEP837RegistrationErrorSurfacesRFC7591Code(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_redirect_uri","error_description":"redirect_uris must be https"}`)
	}))
	defer srv.Close()

	c := newTestOAuthClient(t, "https://mcp.example.com/mcp",
		&serverAuth{DynamicRegistration: true, RedirectURIs: []string{"http://app.example/cb"}})
	as := authServerMetadata{Issuer: "https://as.example", RegistrationEndpoint: srv.URL + "/register"}
	_, err := c.registerClient(context.Background(), as)
	if err == nil {
		t.Fatal("a 400 registration must fail")
	}
	if !strings.Contains(err.Error(), "invalid_redirect_uri") {
		t.Errorf("error should surface the RFC 7591 code, got: %v", err)
	}
	if strings.Contains(err.Error(), "redirect_uris must be https") {
		t.Errorf("error must NOT surface the third-party error_description, got: %v", err)
	}
}

// --- SEP-2207: refresh tokens -----------------------------------------------------

// TestSEP2207RefreshTokenStoredRotatedKeyedByIssuer: SEP-2207 — a refresh_token in a
// token response is parsed and stored KEYED BY ISSUER; refreshGrant redeems it with
// grant_type=refresh_token + the SAME resource indicator (the token stays
// audience-bound) and ROTATES the stored value when the AS issues a new one.
func TestSEP2207RefreshTokenStoredRotatedKeyedByIssuer(t *testing.T) {
	var (
		mu    sync.Mutex
		forms []url.Values
	)
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource":
			writeJSON(w, fmt.Sprintf(`{"resource":%q,"authorization_servers":[%q]}`, base+"/mcp", base))
		case "/.well-known/oauth-authorization-server":
			writeJSON(w, fmt.Sprintf(`{"issuer":%q,"token_endpoint":%q}`, base, base+"/token"))
		case "/token":
			_ = r.ParseForm()
			mu.Lock()
			forms = append(forms, cloneForm(r.PostForm))
			mu.Unlock()
			if r.PostForm.Get("grant_type") == "refresh_token" {
				writeJSON(w, `{"access_token":"at-2","token_type":"Bearer","refresh_token":"rt-2"}`)
				return
			}
			writeJSON(w, `{"access_token":"at-1","token_type":"Bearer","refresh_token":"rt-1"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	base = srv.URL
	defer srv.Close()

	c := newTestOAuthClient(t, base+"/mcp",
		&serverAuth{ClientID: "cid", ClientSecret: "csec", Scopes: []string{"tools:read"}})
	wwwAuth := `Bearer resource_metadata="` + base + `/.well-known/oauth-protected-resource"`

	tok, err := c.bearer(context.Background(), wwwAuth)
	if err != nil {
		t.Fatalf("bearer: %v", err)
	}
	if tok != "at-1" {
		t.Errorf("access token = %q", tok)
	}
	// Stored under THIS issuer — and unreachable under any other (issuer-keyed).
	rt, ok := c.store.refreshTokenFor(base)
	if !ok || rt != "rt-1" {
		t.Fatalf("refresh token for issuer = %q (%v), want rt-1", rt, ok)
	}
	if _, ok := c.store.refreshTokenFor("https://other.example"); ok {
		t.Error("a refresh token must not be reachable under a different issuer (SEP-2352/SEP-2207)")
	}

	identity := clientIdentity{method: identityPreRegistered, clientID: "cid", clientSecret: "csec"}
	tok2, err := c.refreshGrant(context.Background(), base+"/token", base, identity, rt)
	if err != nil {
		t.Fatalf("refreshGrant: %v", err)
	}
	if tok2.AccessToken != "at-2" {
		t.Errorf("refreshed access token = %q", tok2.AccessToken)
	}

	mu.Lock()
	if len(forms) != 2 {
		t.Fatalf("token requests = %d, want 2", len(forms))
	}
	refresh := forms[1]
	mu.Unlock()
	if refresh.Get("grant_type") != "refresh_token" || refresh.Get("refresh_token") != "rt-1" {
		t.Errorf("refresh form = %v", refresh)
	}
	if refresh.Get("resource") != base+"/mcp" {
		t.Errorf("refresh resource = %q — the refreshed token must stay audience-bound (RFC 8707)", refresh.Get("resource"))
	}
	// Rotation: the new refresh token replaced the old one under the same issuer.
	if rt2, ok := c.store.refreshTokenFor(base); !ok || rt2 != "rt-2" {
		t.Errorf("stored refresh token after rotation = %q (%v), want rt-2", rt2, ok)
	}
}

// TestSEP2207OfflineAccessScopeGating: SEP-2207 (MAY) — the offline_access scope is
// requested ONLY when the operator opted in (auth.offline_access) AND the AS
// advertises it in scopes_supported; either condition missing withholds it. The
// scope form field actually sent to the token endpoint is asserted both ways.
func TestSEP2207OfflineAccessScopeGating(t *testing.T) {
	cases := []struct {
		name       string
		optIn      bool
		advertised string // scopes_supported JSON array
		wantScope  string
	}{
		{"opted in + advertised: appended", true, `["tools:read","offline_access"]`, "tools:read offline_access"},
		{"opted in, not advertised: withheld", true, `["tools:read"]`, "tools:read"},
		{"advertised, not opted in: withheld", false, `["tools:read","offline_access"]`, "tools:read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu       sync.Mutex
				gotScope string
			)
			var base string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/oauth-protected-resource":
					writeJSON(w, fmt.Sprintf(`{"resource":%q,"authorization_servers":[%q]}`, base+"/mcp", base))
				case "/.well-known/oauth-authorization-server":
					writeJSON(w, fmt.Sprintf(`{"issuer":%q,"token_endpoint":%q,"scopes_supported":%s}`, base, base+"/token", tc.advertised))
				case "/token":
					_ = r.ParseForm()
					mu.Lock()
					gotScope = r.PostForm.Get("scope")
					mu.Unlock()
					writeJSON(w, `{"access_token":"at-1","token_type":"Bearer"}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			base = srv.URL
			defer srv.Close()

			c := newTestOAuthClient(t, base+"/mcp", &serverAuth{
				ClientID: "cid", ClientSecret: "csec",
				Scopes: []string{"tools:read"}, OfflineAccess: tc.optIn,
			})
			wwwAuth := `Bearer resource_metadata="` + base + `/.well-known/oauth-protected-resource"`
			if _, err := c.bearer(context.Background(), wwwAuth); err != nil {
				t.Fatalf("bearer: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if gotScope != tc.wantScope {
				t.Errorf("requested scope = %q, want %q (SEP-2207 gating)", gotScope, tc.wantScope)
			}
		})
	}
}

// --- SEP-2350: client-side scope accumulation -------------------------------------

// TestSEP2350AccumulateScopes: SEP-2350 — the step-up scope set is the CLIENT-side
// union of previously requested + challenged scopes: previous order preserved,
// challenged appended, duplicates and blanks dropped (deterministic result).
func TestSEP2350AccumulateScopes(t *testing.T) {
	cases := []struct {
		name                 string
		previous, challenged []string
		want                 []string
	}{
		{"previous order preserved, challenge appended", []string{"read", "write"}, []string{"write", "admin"}, []string{"read", "write", "admin"}},
		{"duplicates collapse", []string{"read", "read"}, []string{"read"}, []string{"read"}},
		{"blank scopes dropped", []string{"", "read"}, []string{"  ", "write"}, []string{"read", "write"}},
		{"nothing previous", nil, []string{"write"}, []string{"write"}},
		{"empty challenge keeps previous", []string{"read"}, nil, []string{"read"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := accumulateScopes(tc.previous, tc.challenged)
			if len(got) != len(tc.want) {
				t.Fatalf("union = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("union[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestSEP2350InsufficientScopeChallenge: SEP-835/SEP-2350 — only a Bearer challenge
// with error="insufficient_scope" is a step-up trigger; its scope list is parsed
// space-delimited. Any other challenge (invalid_token, non-Bearer schemes) is not.
func TestSEP2350InsufficientScopeChallenge(t *testing.T) {
	cases := []struct {
		name, header string
		wantScopes   []string
		wantOK       bool
	}{
		{"scope list parsed", `Bearer error="insufficient_scope", scope="tools:read tools:admin"`, []string{"tools:read", "tools:admin"}, true},
		{"rs-style challenge with resource_metadata", `Bearer error="insufficient_scope", scope="write", resource_metadata="https://h/prm"`, []string{"write"}, true},
		{"challenge without scope list still a step-up", `Bearer error="insufficient_scope"`, nil, true},
		{"invalid_token is not a scope challenge", `Bearer error="invalid_token", resource_metadata="https://h/prm"`, nil, false},
		{"non-bearer scheme refused", `Basic realm="x"`, nil, false},
		{"empty header", ``, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scopes, ok := insufficientScopeChallenge(tc.header)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if len(scopes) != len(tc.wantScopes) {
				t.Fatalf("scopes = %v, want %v", scopes, tc.wantScopes)
			}
			for i := range tc.wantScopes {
				if scopes[i] != tc.wantScopes[i] {
					t.Errorf("scopes[%d] = %q, want %q", i, scopes[i], tc.wantScopes[i])
				}
			}
		})
	}
}

// TestSEP2350StepUpRetryAccumulatesScopesEndToEnd: SEP-2350/SEP-835 end-to-end
// through httpTransport.roundTrip. The MCP endpoint requires read+write; the AS
// grants exactly what is requested. Sequence: unauthenticated 401 → token #1 (scope
// "read") → 403 insufficient_scope challenging "write" → ONE step-up whose token
// request carries the UNION "read write" (client-side accumulation: the stateless
// server only named the missing scope) → 200. Exactly two token requests.
func TestSEP2350StepUpRetryAccumulatesScopesEndToEnd(t *testing.T) {
	var (
		mu          sync.Mutex
		tokenScopes []string              // scope form field per token request, in order
		granted     = map[string]string{} // access token → granted scope set
	)
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource":
			writeJSON(w, fmt.Sprintf(`{"resource":%q,"authorization_servers":[%q]}`, base+"/mcp", base))
		case "/.well-known/oauth-authorization-server":
			writeJSON(w, fmt.Sprintf(`{"issuer":%q,"token_endpoint":%q}`, base, base+"/token"))
		case "/token":
			_ = r.ParseForm()
			mu.Lock()
			scope := r.PostForm.Get("scope")
			tok := fmt.Sprintf("tok-%d", len(tokenScopes)+1)
			tokenScopes = append(tokenScopes, scope)
			granted[tok] = scope // the AS grants exactly the requested scope
			mu.Unlock()
			writeJSON(w, fmt.Sprintf(`{"access_token":%q,"token_type":"Bearer"}`, tok))
		case "/mcp":
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			mu.Lock()
			scope, authed := granted[tok]
			mu.Unlock()
			if !authed {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			held := strings.Fields(scope)
			if !containsScope(held, "read") || !containsScope(held, "write") {
				// The rs.go challengeScope shape: the challenge names only the
				// missing scope and carries the PRM URL (RFC 9728 §5.1).
				w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="write", resource_metadata="`+base+`/.well-known/oauth-protected-resource"`)
				w.WriteHeader(http.StatusForbidden)
				return
			}
			mcpHTTPHandler(false)(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	base = srv.URL
	defer srv.Close()

	tr, err := newHTTPTransport(serverSpec{
		Name: "s", Transport: transportHTTP, URL: base + "/mcp",
		Auth: &serverAuth{ClientID: "cid", ClientSecret: "csec", Scopes: []string{"read"}},
	})
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}
	raw, err := tr.roundTrip(context.Background(), rpcRequest{Method: "tools/list"})
	if err != nil {
		t.Fatalf("roundTrip must succeed after ONE step-up retry: %v", err)
	}
	if !strings.Contains(string(raw), "read_file") {
		t.Errorf("result after step-up = %s", raw)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(tokenScopes) != 2 {
		t.Fatalf("token requests = %d (%v), want exactly 2 (initial grant + one step-up)", len(tokenScopes), tokenScopes)
	}
	if tokenScopes[0] != "read" {
		t.Errorf("first token request scope = %q, want %q", tokenScopes[0], "read")
	}
	if tokenScopes[1] != "read write" {
		t.Errorf("step-up token request scope = %q, want the UNION %q (SEP-2350 client-side accumulation)", tokenScopes[1], "read write")
	}
}
