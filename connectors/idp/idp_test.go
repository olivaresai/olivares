// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package idp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// recordedRequest captures what the connector sent so a test can assert auth
// headers, method, URL and (crucially) that no secret was placed on the wire.
type recordedRequest struct {
	Method string
	URL    string
	Auth   string // Authorization header
	Body   string
}

// route is a programmed response: the body to return for the i-th matching call,
// plus optional response headers (used for the Okta Link pagination header).
type route struct {
	status  int
	body    string
	headers map[string]string
}

// stubDoer routes requests by (method, path-prefix), returning the next queued
// route for each key and recording every request. Absolute next-page URLs are
// matched by their path so pagination follows transparently.
type stubDoer struct {
	t       *testing.T
	routes  map[string][]route // key: METHOD + " " + pathOrHost
	calls   []recordedRequest
	fixture func(name string) string
}

func newStub(t *testing.T) *stubDoer {
	t.Helper()
	return &stubDoer{
		t:      t,
		routes: map[string][]route{},
		fixture: func(name string) string {
			b, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatalf("read fixture %s: %v", name, err)
			}
			return string(b)
		},
	}
}

// on queues a JSON-body response for a method + match key. match is compared
// against the request URL path (and, for the token POST, against the full URL).
func (d *stubDoer) on(method, match string, body string) *stubDoer {
	key := method + " " + match
	d.routes[key] = append(d.routes[key], route{status: 200, body: body})
	return d
}

// onWithHeaders is like on but also sets response headers (Okta Link pagination).
func (d *stubDoer) onWithHeaders(method, match, body string, headers map[string]string) *stubDoer {
	key := method + " " + match
	d.routes[key] = append(d.routes[key], route{status: 200, body: body, headers: headers})
	return d
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	var bodyStr string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		bodyStr = string(b)
		_ = req.Body.Close()
	}
	d.calls = append(d.calls, recordedRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Auth:   req.Header.Get("Authorization"),
		Body:   bodyStr,
	})

	// Find the route whose match is a substring of the path (or full URL for
	// POST). Among matches with responses left, the LONGEST key wins, so a nested
	// resource path (/servicePrincipals/{id}/appRoleAssignedTo) never resolves to
	// its prefix route (/servicePrincipals) by map iteration order.
	bestKey, bestLen := "", -1
	for key, queued := range d.routes {
		parts := strings.SplitN(key, " ", 2)
		if parts[0] != req.Method || len(queued) == 0 {
			continue
		}
		match := parts[1]
		if (strings.Contains(req.URL.Path, match) || strings.Contains(req.URL.String(), match)) && len(match) > bestLen {
			bestKey, bestLen = key, len(match)
		}
	}
	if bestKey != "" {
		queued := d.routes[bestKey]
		r := queued[0]
		d.routes[bestKey] = queued[1:]
		h := http.Header{}
		h.Set("Content-Type", "application/json")
		for k, v := range r.headers {
			h.Add(k, v)
		}
		return &http.Response{
			StatusCode: r.status,
			Header:     h,
			Body:       io.NopCloser(bytes.NewBufferString(r.body)),
			Request:    req,
		}, nil
	}
	d.t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
	return nil, fmt.Errorf("unreachable")
}

func openSource(t *testing.T, d *stubDoer, settings map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = d
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// Okta
// ---------------------------------------------------------------------------

func TestOktaSnapshot(t *testing.T) {
	d := newStub(t)
	const base = "https://acme.okta.com"
	const token = "00SSWSsupersecrettoken"

	// /api/v1/users paginates: page 1 carries a Link rel="next" to page 2.
	d.onWithHeaders(http.MethodGet, "/api/v1/users", d.fixture("okta_users.json"), map[string]string{
		"Link": `<` + base + `/api/v1/users?after=PAGE2>; rel="next"`,
	})
	d.on(http.MethodGet, "/api/v1/users?after=PAGE2", d.fixture("okta_users_page2.json"))
	d.on(http.MethodGet, "/api/v1/apps", d.fixture("okta_apps.json"))
	d.on(http.MethodGet, "/api/v1/groups", d.fixture("okta_groups.json"))
	d.on(http.MethodGet, "/api/v1/groups/00g1eng/users", d.fixture("okta_group_members.json"))

	s := openSource(t, d, map[string]string{
		"provider":  "okta",
		"base_url":  base,
		"api_token": token,
	})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceOkta {
		t.Errorf("source = %q, want okta", g.Source)
	}

	// 3 users (2 page1 + 1 page2) + 2 apps = 5 identities. Pagination must have run.
	if got := len(g.Identities); got != 5 {
		t.Fatalf("identities = %d, want 5 (pagination + apps)", got)
	}
	if !sawPath(d, "/api/v1/users?after=PAGE2") {
		t.Error("connector did not follow the Okta Link rel=next page")
	}

	alice, ok := g.FindIdentity("00u1alice")
	if !ok {
		t.Fatal("Alice missing")
	}
	if alice.Type != identitysource.PrincipalHuman {
		t.Errorf("Alice type = %q, want human", alice.Type)
	}
	if alice.Disabled {
		t.Error("Alice (ACTIVE) must not be disabled")
	}
	if alice.Attributes["login"] != "alice@corp.example" || alice.Attributes["email"] != "alice@corp.example" {
		t.Errorf("Alice attributes wrong: %v", alice.Attributes)
	}
	if alice.DisplayName != "Alice Smith" {
		t.Errorf("Alice displayName = %q", alice.DisplayName)
	}

	bob, _ := g.FindIdentity("00u2bob")
	if !bob.Disabled {
		t.Error("Bob (SUSPENDED) must be disabled")
	}

	carol, ok := g.FindIdentity("00u3carol")
	if !ok {
		t.Fatal("Carol (page 2) missing — pagination did not append")
	}
	if !carol.Disabled {
		t.Error("Carol (STAGED) must be disabled")
	}
	if carol.DisplayName != "Carol Lee" { // falls back to displayName, has no email attr
		t.Errorf("Carol displayName = %q", carol.DisplayName)
	}

	// Apps are NHI service_app.
	deploy, ok := g.FindIdentity("0oa1deploy")
	if !ok {
		t.Fatal("Deploy Service app missing")
	}
	if deploy.Type != identitysource.PrincipalNHI || deploy.Kind != "service_app" {
		t.Errorf("deploy app type/kind = %q/%q, want nhi/service_app", deploy.Type, deploy.Kind)
	}
	if deploy.Disabled {
		t.Error("ACTIVE app must not be disabled")
	}
	legacy, _ := g.FindIdentity("0oa2legacy")
	if !legacy.Disabled {
		t.Error("INACTIVE app must be disabled")
	}

	// Group + memberships.
	if len(g.Collections) != 1 || g.Collections[0].Ref != "00g1eng" || g.Collections[0].Kind != identitysource.KindGroup {
		t.Fatalf("collections = %+v", g.Collections)
	}
	if g.Collections[0].DisplayName != "Engineers" {
		t.Errorf("group displayName = %q", g.Collections[0].DisplayName)
	}
	if len(g.Memberships) != 2 {
		t.Fatalf("memberships = %d, want 2", len(g.Memberships))
	}
	for _, m := range g.Memberships {
		if m.CollectionRef != "00g1eng" || m.MemberKind != identitysource.MemberIdentity || m.Source != identitysource.SourceOkta {
			t.Errorf("bad membership: %+v", m)
		}
	}
}

func TestOktaSSWSAuthHeader(t *testing.T) {
	d := newStub(t)
	const base = "https://acme.okta.com"
	const token = "00SSWSsupersecrettoken"
	d.on(http.MethodGet, "/api/v1/users", `[]`)
	d.on(http.MethodGet, "/api/v1/apps", `[]`)
	d.on(http.MethodGet, "/api/v1/groups", `[]`)

	s := openSource(t, d, map[string]string{"provider": "okta", "base_url": base, "api_token": token})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(d.calls) == 0 {
		t.Fatal("no calls recorded")
	}
	for _, c := range d.calls {
		if c.Auth != "SSWS "+token {
			t.Errorf("Okta auth header = %q, want SSWS <token>", c.Auth)
		}
		if strings.HasPrefix(c.Auth, "Bearer") {
			t.Errorf("Okta must not use Bearer auth: %q", c.Auth)
		}
	}
}

func TestOktaApiError(t *testing.T) {
	d := newStub(t)
	d.routes["GET /api/v1/users"] = []route{{status: 403, body: `{"errorCode":"E0000006","errorSummary":"insufficient permissions"}`}}
	s := openSource(t, d, map[string]string{"provider": "okta", "base_url": "https://acme.okta.com", "api_token": "tok"})
	_, err := s.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected error on Okta 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should carry status: %v", err)
	}
	if strings.Contains(err.Error(), "tok") {
		t.Errorf("error must not contain the credential: %v", err)
	}
}

// TestOktaPaginationStopsWhenLinkAbsent proves the connector respects the ABSENCE
// of a Link rel="next" header: a /api/v1/users response with no next link must
// terminate after page 1 — the connector must not over-fetch a phantom second page
// nor loop. The page-1 fixture carries two users (alice, bob); the roster must hold
// exactly those, and no request for the synthetic page-2 cursor may be made.
func TestOktaPaginationStopsWhenLinkAbsent(t *testing.T) {
	d := newStub(t)
	const base = "https://acme.okta.com"

	// /api/v1/users returns page 1 with NO Link header (no rel="next").
	d.on(http.MethodGet, "/api/v1/users", d.fixture("okta_users.json"))
	// Queue a page-2 response under the cursor key the paginated test uses. If the
	// connector wrongly fetched a second page it would consume this; we assert it
	// does NOT (sawPath below), so this stays unconsumed.
	d.on(http.MethodGet, "/api/v1/users?after=PAGE2", d.fixture("okta_users_page2.json"))
	d.on(http.MethodGet, "/api/v1/apps", `[]`)
	d.on(http.MethodGet, "/api/v1/groups", `[]`)

	s := openSource(t, d, map[string]string{
		"provider":  "okta",
		"base_url":  base,
		"api_token": "00SSWSsupersecrettoken",
	})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// The connector must NOT have requested a second users page.
	if sawPath(d, "after=PAGE2") {
		t.Error("connector fetched a second users page despite no Link rel=next on page 1")
	}

	// Roster holds exactly the two page-1 users (no apps, no groups).
	if got := len(g.Identities); got != 2 {
		t.Fatalf("identities = %d, want exactly 2 (page-1 users only)", got)
	}
	if _, ok := g.FindIdentity("00u1alice"); !ok {
		t.Error("Alice (page 1) missing")
	}
	if _, ok := g.FindIdentity("00u2bob"); !ok {
		t.Error("Bob (page 1) missing")
	}
	if _, ok := g.FindIdentity("00u3carol"); ok {
		t.Error("Carol (page 2) must NOT appear — page 2 was never linked")
	}
}

// ---------------------------------------------------------------------------
// Entra
// ---------------------------------------------------------------------------

func TestEntraSnapshot(t *testing.T) {
	d := newStub(t)
	const tokenURL = "https://login.test.local/token"

	d.on(http.MethodPost, tokenURL, d.fixture("entra_token.json"))
	// /users paginates via @odata.nextLink.
	d.on(http.MethodGet, "/users", d.fixture("entra_users.json"))
	d.on(http.MethodGet, "/users", d.fixture("entra_users_page2.json")) // page 2 (nextLink)
	d.on(http.MethodGet, "/servicePrincipals", d.fixture("entra_service_principals.json"))
	d.on(http.MethodGet, "/groups", d.fixture("entra_groups.json"))
	d.on(http.MethodGet, "/groups/99999999-9999-9999-9999-999999999999/members", d.fixture("entra_group_members.json"))

	s := openSource(t, d, map[string]string{
		"provider":        "entra",
		"tenant_id":       "tenant-abc",
		"client_id":       "client-xyz",
		"client_secret":   "verysecretclientsecret",
		"oauth_token_url": tokenURL,
	})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceEntra {
		t.Errorf("source = %q, want entra", g.Source)
	}

	// 2 users (page1 + page2) + 2 service principals = 4 identities.
	if got := len(g.Identities); got != 4 {
		t.Fatalf("identities = %d, want 4", got)
	}

	dana, ok := g.FindIdentity("11111111-1111-1111-1111-111111111111")
	if !ok {
		t.Fatal("Dana missing")
	}
	if dana.Type != identitysource.PrincipalHuman || dana.Kind != "user" {
		t.Errorf("Dana type/kind = %q/%q", dana.Type, dana.Kind)
	}
	if dana.Disabled {
		t.Error("Dana (accountEnabled=true) must not be disabled")
	}
	if dana.Attributes["upn"] != "dana@contoso.onmicrosoft.com" || dana.Attributes["mail"] != "dana@contoso.com" {
		t.Errorf("Dana attributes wrong: %v", dana.Attributes)
	}

	erin, ok := g.FindIdentity("22222222-2222-2222-2222-222222222222")
	if !ok {
		t.Fatal("Erin (page 2) missing — @odata.nextLink not followed")
	}
	if !erin.Disabled {
		t.Error("Erin (accountEnabled=false) must be disabled")
	}

	ci, ok := g.FindIdentity("33333333-3333-3333-3333-333333333333")
	if !ok {
		t.Fatal("ci-pipeline-sp missing")
	}
	if ci.Type != identitysource.PrincipalNHI || ci.Kind != "service_principal" {
		t.Errorf("SP type/kind = %q/%q, want nhi/service_principal", ci.Type, ci.Kind)
	}
	retired, _ := g.FindIdentity("44444444-4444-4444-4444-444444444444")
	if !retired.Disabled {
		t.Error("retired SP (accountEnabled=false) must be disabled")
	}
	// an Entra Agent ID agent identity (servicePrincipalType "ServiceIdentity")
	// is the entra-agent connector's row — emitting it here too would make the
	// converged identity's Provider flap between entra and entra-agent per sync.
	if _, found := g.FindIdentity("55555555-5555-5555-5555-555555555555"); found {
		t.Error("agent identity (ServiceIdentity) must be skipped — owned by the entra-agent connector")
	}
	// ...and the skip is COUNTED, never silent: the roster sync surfaces it.
	if g.DeferredAgentIdentities != 1 {
		t.Errorf("DeferredAgentIdentities = %d, want 1", g.DeferredAgentIdentities)
	}

	if len(g.Collections) != 1 || g.Collections[0].Kind != identitysource.KindGroup {
		t.Fatalf("collections = %+v", g.Collections)
	}
	if g.Collections[0].DisplayName != "Platform Team" {
		t.Errorf("group displayName = %q", g.Collections[0].DisplayName)
	}

	// Members are classified by their @odata.type: the user and the service
	// principal are identities, the NESTED GROUP is a collection (a group of
	// groups walks as transitive membership, never as a principal), and the
	// device is skipped — the roster has no counterpart row for it.
	wantKinds := map[string]identitysource.MemberKind{
		"11111111-1111-1111-1111-111111111111": identitysource.MemberIdentity,
		"33333333-3333-3333-3333-333333333333": identitysource.MemberIdentity,
		"88888888-8888-8888-8888-888888888888": identitysource.MemberCollection,
	}
	if len(g.Memberships) != len(wantKinds) {
		t.Fatalf("memberships = %d, want %d (device member must be skipped): %+v", len(g.Memberships), len(wantKinds), g.Memberships)
	}
	for _, m := range g.Memberships {
		if m.CollectionRef != "99999999-9999-9999-9999-999999999999" || m.Source != identitysource.SourceEntra {
			t.Errorf("bad membership: %+v", m)
		}
		want, ok := wantKinds[m.MemberRef]
		if !ok {
			t.Errorf("unexpected member %q (devices/contacts must be skipped)", m.MemberRef)
			continue
		}
		if m.MemberKind != want {
			t.Errorf("member %q kind = %q, want %q", m.MemberRef, m.MemberKind, want)
		}
		delete(wantKinds, m.MemberRef)
	}
	if len(wantKinds) != 0 {
		t.Errorf("missing memberships for: %v", wantKinds)
	}
}

func TestEntraTokenPostAndBearerAuth(t *testing.T) {
	d := newStub(t)
	const tokenURL = "https://login.test.local/token"
	d.on(http.MethodPost, tokenURL, d.fixture("entra_token.json"))
	d.on(http.MethodGet, "/users", `{"value":[]}`)
	d.on(http.MethodGet, "/servicePrincipals", `{"value":[]}`)
	d.on(http.MethodGet, "/groups", `{"value":[]}`)

	s := openSource(t, d, map[string]string{
		"provider":        "entra",
		"tenant_id":       "tenant-abc",
		"client_id":       "client-xyz",
		"client_secret":   "verysecretclientsecret",
		"oauth_token_url": tokenURL,
	})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// The first call must be the token POST with a form-encoded client-credentials body.
	if d.calls[0].Method != http.MethodPost || d.calls[0].URL != tokenURL {
		t.Fatalf("first call = %s %s, want POST %s", d.calls[0].Method, d.calls[0].URL, tokenURL)
	}
	form, err := url.ParseQuery(d.calls[0].Body)
	if err != nil {
		t.Fatalf("token body not form-encoded: %v", err)
	}
	if form.Get("grant_type") != "client_credentials" {
		t.Errorf("grant_type = %q", form.Get("grant_type"))
	}
	if form.Get("scope") != "https://graph.microsoft.com/.default" {
		t.Errorf("scope = %q", form.Get("scope"))
	}
	if form.Get("client_id") != "client-xyz" {
		t.Errorf("client_id = %q", form.Get("client_id"))
	}

	// Every Graph GET must carry the bearer token from the token response, never SSWS.
	const wantBearer = "Bearer eyJ0eXAfake.graph.access.token"
	graphCalls := 0
	for _, c := range d.calls {
		if c.Method != http.MethodGet {
			continue
		}
		graphCalls++
		if c.Auth != wantBearer {
			t.Errorf("Graph auth = %q, want %q", c.Auth, wantBearer)
		}
		if strings.HasPrefix(c.Auth, "SSWS") {
			t.Errorf("Entra must not use SSWS auth: %q", c.Auth)
		}
	}
	if graphCalls == 0 {
		t.Fatal("no Graph GET calls recorded")
	}
}

func TestEntraTokenError(t *testing.T) {
	d := newStub(t)
	const tokenURL = "https://login.test.local/token"
	d.routes["POST "+tokenURL] = []route{{status: 401, body: `{"error":"invalid_client","error_description":"bad secret"}`}}
	s := openSource(t, d, map[string]string{
		"provider": "entra", "tenant_id": "t", "client_id": "c",
		"client_secret": "topsecretvalue", "oauth_token_url": tokenURL,
	})
	_, err := s.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected token error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should carry status: %v", err)
	}
	if strings.Contains(err.Error(), "topsecretvalue") {
		t.Errorf("token error must not leak the client secret: %v", err)
	}
}

// TestEntraPaginationMergesPages proves the connector MERGES @odata.nextLink pages
// rather than overwriting earlier ones. Page 1 returns two distinct users WITH a
// nextLink; page 2 returns two MORE distinct users with NO nextLink. All four must
// appear in the final Graph — if the second page replaced (rather than appended to)
// the accumulator, only the two page-2 users would survive.
func TestEntraPaginationMergesPages(t *testing.T) {
	d := newStub(t)
	const tokenURL = "https://login.test.local/token"

	const page1 = `{
		"@odata.nextLink": "https://graph.microsoft.com/v1.0/users?$skiptoken=PAGE2",
		"value": [
			{"id":"u-1","displayName":"User One","userPrincipalName":"one@contoso.onmicrosoft.com","accountEnabled":true,"mail":"one@contoso.com"},
			{"id":"u-2","displayName":"User Two","userPrincipalName":"two@contoso.onmicrosoft.com","accountEnabled":true,"mail":"two@contoso.com"}
		]
	}`
	const page2 = `{
		"value": [
			{"id":"u-3","displayName":"User Three","userPrincipalName":"three@contoso.onmicrosoft.com","accountEnabled":true,"mail":"three@contoso.com"},
			{"id":"u-4","displayName":"User Four","userPrincipalName":"four@contoso.onmicrosoft.com","accountEnabled":false,"mail":"four@contoso.com"}
		]
	}`

	// Token POST first (as the other Entra tests do), then the two users pages, then
	// empty service principals and groups so Snapshot completes.
	d.on(http.MethodPost, tokenURL, d.fixture("entra_token.json"))
	d.on(http.MethodGet, "/users", page1) // page 1: has @odata.nextLink
	d.on(http.MethodGet, "/users", page2) // page 2: no nextLink
	d.on(http.MethodGet, "/servicePrincipals", `{"value":[]}`)
	d.on(http.MethodGet, "/groups", `{"value":[]}`)

	s := openSource(t, d, map[string]string{
		"provider":        "entra",
		"tenant_id":       "tenant-abc",
		"client_id":       "client-xyz",
		"client_secret":   "verysecretclientsecret",
		"oauth_token_url": tokenURL,
	})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// All four users from both pages must be present (pages merged, not overwritten).
	if got := len(g.Identities); got != 4 {
		t.Fatalf("identities = %d, want 4 (2 from page1 + 2 from page2 merged)", got)
	}
	for _, ref := range []string{"u-1", "u-2", "u-3", "u-4"} {
		if _, ok := g.FindIdentity(ref); !ok {
			t.Errorf("user %q missing — pages were overwritten instead of merged", ref)
		}
	}
}

// ---------------------------------------------------------------------------
// Security invariant: no secret/token ever appears in any emitted Graph field.
// ---------------------------------------------------------------------------

func TestNoSecretLeaksIntoGraph(t *testing.T) {
	const oktaToken = "00SSWSsupersecrettoken"
	const clientSecret = "verysecretclientsecret"
	const accessToken = "eyJ0eXAfake.graph.access.token"

	t.Run("okta", func(t *testing.T) {
		d := newStub(t)
		d.on(http.MethodGet, "/api/v1/users", d.fixture("okta_users.json"))
		d.on(http.MethodGet, "/api/v1/apps", d.fixture("okta_apps.json"))
		d.on(http.MethodGet, "/api/v1/groups", d.fixture("okta_groups.json"))
		d.on(http.MethodGet, "/api/v1/groups/00g1eng/users", d.fixture("okta_group_members.json"))
		s := openSource(t, d, map[string]string{"provider": "okta", "base_url": "https://acme.okta.com", "api_token": oktaToken})
		g, err := s.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		assertNoSecret(t, g, oktaToken)
	})

	t.Run("entra", func(t *testing.T) {
		d := newStub(t)
		const tokenURL = "https://login.test.local/token"
		d.on(http.MethodPost, tokenURL, d.fixture("entra_token.json"))
		d.on(http.MethodGet, "/users", d.fixture("entra_users.json"))
		d.on(http.MethodGet, "/users", d.fixture("entra_users_page2.json"))
		d.on(http.MethodGet, "/servicePrincipals", d.fixture("entra_service_principals.json"))
		d.on(http.MethodGet, "/groups", d.fixture("entra_groups.json"))
		d.on(http.MethodGet, "/groups/99999999-9999-9999-9999-999999999999/members", d.fixture("entra_group_members.json"))
		s := openSource(t, d, map[string]string{
			"provider": "entra", "tenant_id": "t", "client_id": "c",
			"client_secret": clientSecret, "oauth_token_url": tokenURL,
		})
		g, err := s.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		assertNoSecret(t, g, clientSecret, accessToken)
	})
}

// assertNoSecret walks every string field of the Graph and fails if any secret
// appears, proving the connector carries only identity metadata.
func assertNoSecret(t *testing.T, g identitysource.Graph, secrets ...string) {
	t.Helper()
	var fields []string
	for _, id := range g.Identities {
		fields = append(fields, id.Ref, id.Kind, id.DisplayName, string(id.Type))
		for k, v := range id.Attributes {
			fields = append(fields, k, v)
		}
	}
	for _, c := range g.Collections {
		fields = append(fields, c.Ref, c.DisplayName)
		for k, v := range c.Attributes {
			fields = append(fields, k, v)
		}
	}
	for _, m := range g.Memberships {
		fields = append(fields, m.MemberRef, m.CollectionRef)
	}
	for _, f := range fields {
		for _, secret := range secrets {
			if secret != "" && strings.Contains(f, secret) {
				t.Errorf("secret leaked into Graph field %q", f)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Offline + config + Gather
// ---------------------------------------------------------------------------

func TestOktaOfflineEmptyGraph(t *testing.T) {
	s := New() // no doer; must not be touched
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"provider": "okta", "base_url": "https://acme.okta.com"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("offline Snapshot should not error: %v", err)
	}
	if g.Source != identitysource.SourceOkta {
		t.Errorf("offline source = %q", g.Source)
	}
	if len(g.Identities) != 0 || len(g.Collections) != 0 {
		t.Errorf("offline graph must be empty, got %d ids %d cols", len(g.Identities), len(g.Collections))
	}
}

func TestEntraOfflineEmptyGraph(t *testing.T) {
	s := New()
	// client_secret missing => offline, no token POST attempted.
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"provider": "entra", "client_id": "c"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("offline Snapshot should not error: %v", err)
	}
	if g.Source != identitysource.SourceEntra || len(g.Identities) != 0 {
		t.Errorf("offline entra graph wrong: %+v", g)
	}
}

func TestOpenRejectsUnknownProvider(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"provider": "ping"}}); err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestEntraDefaultBaseURL(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"provider": "entra"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.baseURL != defaultEntraBase {
		t.Errorf("entra base = %q, want %q", s.baseURL, defaultEntraBase)
	}
}

func TestDescriptorShape(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource || d.APIVersion != sdk.APIVersion || d.Version != "0.1.0" {
		t.Errorf("descriptor header wrong: %+v", d)
	}
	// api_token and client_secret must be declared Secret.
	secret := map[string]bool{}
	for _, f := range d.ConfigFields {
		secret[f.Key] = f.Secret
	}
	if !secret["api_token"] {
		t.Error("api_token must be Secret")
	}
	if !secret["client_secret"] {
		t.Error("client_secret must be Secret")
	}
}

// captureSink records every observation Gather emits.
type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

// fixedNow is the injected Gather clock; every observation of one run must
// carry exactly this instant (the at-least-once dedup natural key).
func fixedNow() time.Time { return time.Date(2026, 6, 11, 9, 30, 0, 0, time.UTC) }

// splitObs separates captured observations into edges and findings, failing on
// any other kind (the idp Gather emits nothing else).
func splitObs(t *testing.T, obs []model.Observation) ([]model.EdgeObservation, []model.FindingReport) {
	t.Helper()
	var edges []model.EdgeObservation
	var findings []model.FindingReport
	for _, o := range obs {
		switch v := o.(type) {
		case model.EdgeObservation:
			edges = append(edges, v)
		case model.FindingReport:
			findings = append(findings, v)
		default:
			t.Fatalf("unexpected observation type %T", o)
		}
	}
	return edges, findings
}

// obsStrings flattens every string field of an emitted observation, so a test
// can prove no fixture credential/PII surfaced anywhere.
func obsStrings(o model.Observation) []string {
	switch v := o.(type) {
	case model.EdgeObservation:
		out := []string{v.OriginKind, v.OriginRef, v.ResourceKind, v.ResourceRef, string(v.Mode), string(v.Source), string(v.Confidence), v.ToolRef}
		for k, val := range v.Labels {
			out = append(out, k, val)
		}
		return out
	case model.FindingReport:
		return []string{v.Kind, string(v.Severity), v.SubjectKind, v.SubjectRef, v.Title, v.DetailHash}
	default:
		return nil
	}
}

// oktaGatherStub programs the full Okta Gather surface: the user roster (two
// pages via Link), the app listing, and the ACTIVE app's user assignments (two
// pages via Link). The INACTIVE app 0oa2legacy has no assignment route on
// purpose: scanning it would fail the test as an unexpected request.
func oktaGatherStub(t *testing.T) *stubDoer {
	t.Helper()
	d := newStub(t)
	const base = "https://acme.okta.com"
	d.onWithHeaders(http.MethodGet, "/api/v1/users", d.fixture("okta_users.json"), map[string]string{
		"Link": `<` + base + `/api/v1/users?after=PAGE2>; rel="next"`,
	})
	d.on(http.MethodGet, "/api/v1/users?after=PAGE2", d.fixture("okta_users_page2.json"))
	d.on(http.MethodGet, "/api/v1/apps", d.fixture("okta_apps.json"))
	d.onWithHeaders(http.MethodGet, "/api/v1/apps/0oa1deploy/users", d.fixture("okta_app_users.json"), map[string]string{
		"Link": `<` + base + `/api/v1/apps/0oa1deploy/users?after=AU2>; rel="next"`,
	})
	d.on(http.MethodGet, "/api/v1/apps/0oa1deploy/users", d.fixture("okta_app_users_page2.json"))
	return d
}

var oktaGatherSettings = map[string]string{
	"provider":  "okta",
	"base_url":  "https://acme.okta.com",
	"api_token": "00SSWSsupersecrettoken",
}

func TestOktaGatherEmitsAssignmentGrants(t *testing.T) {
	d := oktaGatherStub(t)
	s := openSource(t, d, oktaGatherSettings)
	s.now = fixedNow
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	edges, findings := splitObs(t, sink.obs)

	// Live assignments: alice + bob (page 1) + carol (page 2). Carol's ACCOUNT
	// is STAGED — disabled in the roster — but her assignment is live: a
	// disabled account still HOLDS its grant. 00u4dead/00u5gone are dead
	// ASSIGNMENTS (DEPROVISIONED/REVOKED), skipped; 00u9ghost is not rostered,
	// counted into the coverage finding instead of emitted.
	if len(edges) != 3 {
		t.Fatalf("edges = %d, want 3: %+v", len(edges), edges)
	}
	want := model.EdgeObservation{
		OriginKind:   "identity",
		OriginRef:    "00u1alice",
		ResourceKind: "okta.app",
		ResourceRef:  "0oa1deploy",
		Mode:         model.ModeUnknown,
		Source:       model.SignalPolicy,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   fixedNow(),
	}
	if !reflect.DeepEqual(edges[0], want) {
		t.Errorf("edge[0] = %+v, want %+v", edges[0], want)
	}
	for i, origin := range []string{"00u1alice", "00u2bob", "00u3carol"} {
		e := edges[i]
		if e.OriginRef != origin || e.ResourceRef != "0oa1deploy" || e.ResourceKind != "okta.app" ||
			e.Mode != model.ModeUnknown || e.ToolRef != "" || !e.ObservedAt.Equal(fixedNow()) {
			t.Errorf("edge[%d] wrong: %+v", i, e)
		}
	}

	// Exactly ONE coverage finding for the single unrostered origin.
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Kind != "coverage" || f.Severity != model.SeverityInfo || f.SubjectKind != "identity_source" || f.SubjectRef != Name {
		t.Errorf("coverage finding header wrong: %+v", f)
	}
	if f.Title != "1 permitted-grant origins outside the rostered identity set were not emitted" {
		t.Errorf("coverage title = %q", f.Title)
	}
	if !f.OccurredAt.Equal(fixedNow()) {
		t.Errorf("coverage OccurredAt = %v", f.OccurredAt)
	}

	// Pagination followed on the assignment endpoint, and the documented page
	// maxima requested (apps: limit=200; app users: limit=500, default is 50).
	if !sawPath(d, "after=AU2") {
		t.Error("connector did not follow the app-users Link rel=next page")
	}
	assertQueryParam(t, d, "/api/v1/apps?", "limit", "200")
	assertQueryParam(t, d, "/api/v1/apps/0oa1deploy/users?limit", "limit", "500")
	// The INACTIVE app serves nothing and must not be scanned.
	if sawPath(d, "0oa2legacy") {
		t.Error("inactive app was scanned")
	}
}

// assertQueryParam finds the first recorded request whose URL contains urlSub
// and asserts the named query parameter's value.
func assertQueryParam(t *testing.T, d *stubDoer, urlSub, param, want string) {
	t.Helper()
	for _, c := range d.calls {
		if !strings.Contains(c.URL, urlSub) {
			continue
		}
		u, err := url.Parse(c.URL)
		if err != nil {
			t.Fatalf("unparseable recorded URL %q: %v", c.URL, err)
		}
		if got := u.Query().Get(param); got != want {
			t.Errorf("%s: query %s = %q, want %q", c.URL, param, got, want)
		}
		return
	}
	t.Errorf("no recorded request matched %q", urlSub)
}

// TestOktaGatherNeverDecodesAssignmentCredentials proves the minimal-data
// decode: the okta_app_users.json fixture deliberately carries a
// credentials{userName,password{value}} object and a PII-bearing profile, and
// nothing from either may surface in any emitted field.
func TestOktaGatherNeverDecodesAssignmentCredentials(t *testing.T) {
	d := oktaGatherStub(t)
	s := openSource(t, d, oktaGatherSettings)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.obs) == 0 {
		t.Fatal("vacuous: nothing emitted")
	}
	sentinels := []string{"hunter2-fixture-password", "123-45-6789", "ally", "alice@corp.example"}
	for _, o := range sink.obs {
		for _, field := range obsStrings(o) {
			for _, sec := range sentinels {
				if strings.Contains(field, sec) {
					t.Errorf("fixture credential/PII %q surfaced in emitted field %q", sec, field)
				}
			}
		}
	}
}

// entraGatherStub programs the full Entra Gather surface: token, user roster
// (two pages), service principals, the two non-deferred SPs' assignment scans
// (3333 paginates via @odata.nextLink; 4444 is empty), the assigned group's
// typed members, and the org-wide delegated grants. The DEFERRED ServiceIdentity
// 5555 has no assignment route on purpose: scanning it would fail the test.
func entraGatherStub(t *testing.T) *stubDoer {
	t.Helper()
	d := newStub(t)
	const tokenURL = "https://login.test.local/token"
	d.on(http.MethodPost, tokenURL, d.fixture("entra_token.json"))
	d.on(http.MethodGet, "/users", d.fixture("entra_users.json"))
	d.on(http.MethodGet, "/users", d.fixture("entra_users_page2.json"))
	d.on(http.MethodGet, "/servicePrincipals", d.fixture("entra_service_principals.json"))
	d.on(http.MethodGet, "/servicePrincipals/33333333-3333-3333-3333-333333333333/appRoleAssignedTo", d.fixture("entra_app_role_assignments.json"))
	d.on(http.MethodGet, "/servicePrincipals/33333333-3333-3333-3333-333333333333/appRoleAssignedTo", d.fixture("entra_app_role_assignments_page2.json"))
	d.on(http.MethodGet, "/servicePrincipals/44444444-4444-4444-4444-444444444444/appRoleAssignedTo", `{"value":[]}`)
	d.on(http.MethodGet, "/groups/99999999-9999-9999-9999-999999999999/members", d.fixture("entra_group_members.json"))
	d.on(http.MethodGet, "/oauth2PermissionGrants", d.fixture("entra_oauth2_grants.json"))
	return d
}

var entraGatherSettings = map[string]string{
	"provider":        "entra",
	"tenant_id":       "tenant-abc",
	"client_id":       "client-xyz",
	"client_secret":   "verysecretclientsecret",
	"oauth_token_url": "https://login.test.local/token",
}

func TestEntraGatherEmitsAssignmentAndScopeGrants(t *testing.T) {
	d := entraGatherStub(t)
	s := openSource(t, d, entraGatherSettings)
	s.now = fixedNow
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	edges, findings := splitObs(t, sink.obs)

	// Three grants resolve: Dana's direct assignment, Dana again via the
	// group's direct user-typed members (the SP/nested-group/device members are
	// excluded from expansion), and the org-wide delegated scope of the
	// non-deferred client SP. The deferred ServiceIdentity 5555 (as assignment
	// principal AND as oauth2 client) and the unknown user aaaaaaaa-… are
	// counted, never emitted.
	wantEdges := []model.EdgeObservation{
		{
			OriginKind:   "identity",
			OriginRef:    "11111111-1111-1111-1111-111111111111",
			ResourceKind: "entra.app",
			ResourceRef:  "33333333-3333-3333-3333-333333333333",
			Mode:         model.ModeUnknown,
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   fixedNow(),
		},
		{
			OriginKind:   "identity",
			OriginRef:    "11111111-1111-1111-1111-111111111111",
			ResourceKind: "entra.app",
			ResourceRef:  "33333333-3333-3333-3333-333333333333",
			Mode:         model.ModeUnknown,
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ToolRef:      "group:99999999-9999-9999-9999-999999999999",
			ObservedAt:   fixedNow(),
		},
		{
			OriginKind:   "identity",
			OriginRef:    "33333333-3333-3333-3333-333333333333",
			ResourceKind: "entra.app",
			ResourceRef:  "00000003-0000-0000-c000-000000000000",
			Mode:         model.ModeUnknown,
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ToolRef:      "User.Read Mail.Read",
			ObservedAt:   fixedNow(),
		},
	}
	if !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("edges = %+v\nwant %+v", edges, wantEdges)
	}

	// Exactly ONE coverage finding: deferred SP principal + unknown user +
	// deferred oauth2 client = 3 withheld origins.
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Kind != "coverage" || f.Severity != model.SeverityInfo || f.SubjectKind != "identity_source" || f.SubjectRef != Name {
		t.Errorf("coverage finding header wrong: %+v", f)
	}
	if f.Title != "3 permitted-grant origins outside the rostered identity set were not emitted" {
		t.Errorf("coverage title = %q", f.Title)
	}

	// The deferred ServiceIdentity is never scanned as a resource.
	if sawPath(d, "55555555-5555-5555-5555-555555555555/appRoleAssignedTo") {
		t.Error("deferred ServiceIdentity SP was scanned for assignments")
	}
	// The nested group is NOT expanded (Entra does not cascade app roles).
	if sawPath(d, "88888888-8888-8888-8888-888888888888") {
		t.Error("nested group was expanded")
	}
	// Assignment pagination followed; minimal-data selects and the org-wide
	// consent filter sent.
	if !sawPath(d, "ARA2") {
		t.Error("appRoleAssignedTo @odata.nextLink not followed")
	}
	assertQueryParam(t, d, "/servicePrincipals?", "$select", "id,displayName,accountEnabled,servicePrincipalType,appRoleAssignmentRequired")
	assertQueryParam(t, d, "/appRoleAssignedTo?", "$select", "principalId,principalType")
	assertQueryParam(t, d, "/oauth2PermissionGrants", "$filter", "consentType eq 'AllPrincipals'")
}

func TestEntraGatherTruncatesScopeToolRef(t *testing.T) {
	d := newStub(t)
	longScope := strings.Repeat("á", 300) // rune count ≠ byte count on purpose
	grants := fmt.Sprintf(`{"value":[{"clientId":"33333333-3333-3333-3333-333333333333","consentType":"AllPrincipals","resourceId":"00000003-0000-0000-c000-000000000000","scope":%q}]}`, longScope)
	d.on(http.MethodPost, "https://login.test.local/token", d.fixture("entra_token.json"))
	d.on(http.MethodGet, "/users", `{"value":[]}`)
	d.on(http.MethodGet, "/servicePrincipals", `{"value":[{"id":"33333333-3333-3333-3333-333333333333","displayName":"sp","accountEnabled":true,"servicePrincipalType":"Application"}]}`)
	d.on(http.MethodGet, "/servicePrincipals/33333333-3333-3333-3333-333333333333/appRoleAssignedTo", `{"value":[]}`)
	d.on(http.MethodGet, "/oauth2PermissionGrants", grants)

	s := openSource(t, d, entraGatherSettings)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	edges, _ := splitObs(t, sink.obs)
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	want := strings.Repeat("á", 256) + "…"
	if edges[0].ToolRef != want {
		t.Errorf("ToolRef = %d runes %q…, want 256 runes + ellipsis", utf8.RuneCountInString(edges[0].ToolRef), edges[0].ToolRef[:24])
	}
}

func TestGatherMaxAppsTruncationFinding(t *testing.T) {
	t.Run("okta", func(t *testing.T) {
		d := newStub(t)
		d.on(http.MethodGet, "/api/v1/users", `[]`)
		d.on(http.MethodGet, "/api/v1/apps", `[{"id":"0oa1deploy","label":"A","status":"ACTIVE"},{"id":"0oa3second","label":"B","status":"ACTIVE"}]`)
		d.on(http.MethodGet, "/api/v1/apps/0oa1deploy/users", `[]`)
		settings := map[string]string{"max_apps": "1"}
		for k, v := range oktaGatherSettings {
			settings[k] = v
		}
		s := openSource(t, d, settings)
		sink := &captureSink{}
		if err := s.Gather(context.Background(), sink); err != nil {
			t.Fatalf("Gather: %v", err)
		}
		edges, findings := splitObs(t, sink.obs)
		if len(edges) != 0 {
			t.Errorf("edges = %d, want 0", len(edges))
		}
		if len(findings) != 1 {
			t.Fatalf("findings = %d, want 1: %+v", len(findings), findings)
		}
		f := findings[0]
		if f.Kind != "coverage" || f.SubjectRef != Name {
			t.Errorf("truncation finding header wrong: %+v", f)
		}
		if !strings.Contains(f.Title, "truncated at 1") || !strings.Contains(f.Title, "max_apps") {
			t.Errorf("truncation title = %q", f.Title)
		}
		if sawPath(d, "0oa3second") {
			t.Error("app beyond the max_apps cap was scanned")
		}
	})
	t.Run("entra", func(t *testing.T) {
		d := newStub(t)
		d.on(http.MethodPost, "https://login.test.local/token", d.fixture("entra_token.json"))
		d.on(http.MethodGet, "/users", `{"value":[]}`)
		d.on(http.MethodGet, "/servicePrincipals", `{"value":[
			{"id":"33333333-3333-3333-3333-333333333333","displayName":"a","accountEnabled":true,"servicePrincipalType":"Application"},
			{"id":"44444444-4444-4444-4444-444444444444","displayName":"b","accountEnabled":true,"servicePrincipalType":"Application"}]}`)
		d.on(http.MethodGet, "/servicePrincipals/33333333-3333-3333-3333-333333333333/appRoleAssignedTo", `{"value":[]}`)
		d.on(http.MethodGet, "/oauth2PermissionGrants", `{"value":[]}`)
		settings := map[string]string{"max_apps": "1"}
		for k, v := range entraGatherSettings {
			settings[k] = v
		}
		s := openSource(t, d, settings)
		sink := &captureSink{}
		if err := s.Gather(context.Background(), sink); err != nil {
			t.Fatalf("Gather: %v", err)
		}
		edges, findings := splitObs(t, sink.obs)
		if len(edges) != 0 || len(findings) != 1 {
			t.Fatalf("edges/findings = %d/%d, want 0/1: %+v", len(edges), len(findings), findings)
		}
		if !strings.Contains(findings[0].Title, "truncated at 1") || !strings.Contains(findings[0].Title, "max_apps") {
			t.Errorf("truncation title = %q", findings[0].Title)
		}
		if sawPath(d, "44444444-4444-4444-4444-444444444444/appRoleAssignedTo") {
			t.Error("SP beyond the max_apps cap was scanned")
		}
	})
}

// TestGatherConvergesOnSnapshotRoster is the hard invariant: every emitted
// OriginRef must appear as an Identity.Ref in a Snapshot taken over the same
// fixtures.
func TestGatherConvergesOnSnapshotRoster(t *testing.T) {
	t.Run("okta", func(t *testing.T) {
		snap := newStub(t)
		const base = "https://acme.okta.com"
		snap.onWithHeaders(http.MethodGet, "/api/v1/users", snap.fixture("okta_users.json"), map[string]string{
			"Link": `<` + base + `/api/v1/users?after=PAGE2>; rel="next"`,
		})
		snap.on(http.MethodGet, "/api/v1/users?after=PAGE2", snap.fixture("okta_users_page2.json"))
		snap.on(http.MethodGet, "/api/v1/apps", snap.fixture("okta_apps.json"))
		snap.on(http.MethodGet, "/api/v1/groups", snap.fixture("okta_groups.json"))
		snap.on(http.MethodGet, "/api/v1/groups/00g1eng/users", snap.fixture("okta_group_members.json"))
		g, err := openSource(t, snap, oktaGatherSettings).Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		assertConverged(t, g, gatherEdges(t, oktaGatherStub(t), oktaGatherSettings))
	})
	t.Run("entra", func(t *testing.T) {
		snap := newStub(t)
		snap.on(http.MethodPost, "https://login.test.local/token", snap.fixture("entra_token.json"))
		snap.on(http.MethodGet, "/users", snap.fixture("entra_users.json"))
		snap.on(http.MethodGet, "/users", snap.fixture("entra_users_page2.json"))
		snap.on(http.MethodGet, "/servicePrincipals", snap.fixture("entra_service_principals.json"))
		snap.on(http.MethodGet, "/groups", snap.fixture("entra_groups.json"))
		snap.on(http.MethodGet, "/groups/99999999-9999-9999-9999-999999999999/members", snap.fixture("entra_group_members.json"))
		g, err := openSource(t, snap, entraGatherSettings).Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		assertConverged(t, g, gatherEdges(t, entraGatherStub(t), entraGatherSettings))
	})
}

// gatherEdges runs Gather against the stub and returns the emitted edges.
func gatherEdges(t *testing.T, d *stubDoer, settings map[string]string) []model.EdgeObservation {
	t.Helper()
	s := openSource(t, d, settings)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	edges, _ := splitObs(t, sink.obs)
	return edges
}

// assertConverged fails on any edge whose origin the Snapshot roster lacks.
func assertConverged(t *testing.T, g identitysource.Graph, edges []model.EdgeObservation) {
	t.Helper()
	if len(edges) == 0 {
		t.Fatal("vacuous: Gather emitted no edges")
	}
	roster := make(map[string]bool, len(g.Identities))
	for _, id := range g.Identities {
		roster[id.Ref] = true
	}
	for _, e := range edges {
		if !roster[e.OriginRef] {
			t.Errorf("emitted origin %q is not in the Snapshot roster", e.OriginRef)
		}
	}
}

func TestGatherOfflineEmitsNothing(t *testing.T) {
	t.Run("okta", func(t *testing.T) {
		d := newStub(t) // zero routes: any request fails the test
		s := openSource(t, d, map[string]string{"provider": "okta", "base_url": "https://acme.okta.com"})
		sink := &captureSink{}
		if err := s.Gather(context.Background(), sink); err != nil {
			t.Fatalf("offline Gather must return nil, got %v", err)
		}
		if len(sink.obs) != 0 || len(d.calls) != 0 {
			t.Errorf("offline Gather: %d emissions, %d requests; want 0/0", len(sink.obs), len(d.calls))
		}
	})
	t.Run("entra", func(t *testing.T) {
		d := newStub(t)
		// client_secret missing => offline, not even the token POST.
		s := openSource(t, d, map[string]string{"provider": "entra", "client_id": "c"})
		sink := &captureSink{}
		if err := s.Gather(context.Background(), sink); err != nil {
			t.Fatalf("offline Gather must return nil, got %v", err)
		}
		if len(sink.obs) != 0 || len(d.calls) != 0 {
			t.Errorf("offline Gather: %d emissions, %d requests; want 0/0", len(sink.obs), len(d.calls))
		}
	})
}

func TestGatherErrorNeverCarriesCredential(t *testing.T) {
	t.Run("okta", func(t *testing.T) {
		d := newStub(t)
		d.routes["GET /api/v1/users"] = []route{{status: 403, body: `{"errorCode":"E0000006"}`}}
		s := openSource(t, d, oktaGatherSettings)
		err := s.Gather(context.Background(), &captureSink{})
		if err == nil {
			t.Fatal("expected error on 403")
		}
		if strings.Contains(err.Error(), oktaGatherSettings["api_token"]) {
			t.Errorf("error carries the credential: %v", err)
		}
	})
	t.Run("entra", func(t *testing.T) {
		d := newStub(t)
		d.routes["POST https://login.test.local/token"] = []route{{status: 401, body: `{"error":"invalid_client"}`}}
		s := openSource(t, d, entraGatherSettings)
		err := s.Gather(context.Background(), &captureSink{})
		if err == nil {
			t.Fatal("expected token error")
		}
		if strings.Contains(err.Error(), entraGatherSettings["client_secret"]) {
			t.Errorf("error carries the client secret: %v", err)
		}
	})
}

// sawPath reports whether any recorded request URL contained sub.
func sawPath(d *stubDoer, sub string) bool {
	for _, c := range d.calls {
		if strings.Contains(c.URL, sub) {
			return true
		}
	}
	return false
}
