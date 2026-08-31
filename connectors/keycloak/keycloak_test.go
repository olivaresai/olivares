// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package keycloak

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// stubDoer answers Keycloak Admin REST + token requests from in-memory fixtures
// and records every request so a test can assert no client secret reached an
// Admin call. It matches by method + URL path (ignoring the first/max query).
type stubDoer struct {
	t     *testing.T
	calls []recorded
}

type recorded struct {
	method string
	path   string
	auth   string
	body   string
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
	}
	d.calls = append(d.calls, recorded{
		method: req.Method, path: req.URL.Path, auth: req.Header.Get("Authorization"), body: body,
	})
	status, payload := d.route(req.Method, req.URL.Path)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(payload)),
		Header:     http.Header{"Content-Type": {"application/json"}},
	}, nil
}

func (d *stubDoer) route(method, path string) (int, string) {
	switch {
	case method == http.MethodPost && strings.HasSuffix(path, "/protocol/openid-connect/token"):
		return 200, `{"access_token":"test-token","token_type":"Bearer","expires_in":60}`
	case method == http.MethodGet && path == "/admin/realms/corp/users":
		return 200, `[
			{"id":"u-alice","username":"alice","email":"alice@corp.example","firstName":"Alice","lastName":"Stone","enabled":true},
			{"id":"u-svc","username":"service-account-ci-bot","enabled":true,"serviceAccountClientId":"ci-bot"},
			{"id":"u-bob","username":"bob","enabled":false}
		]`
	case method == http.MethodGet && path == "/admin/realms/corp/clients":
		return 200, `[
			{"id":"c-ci","clientId":"ci-bot","name":"CI Bot","enabled":true,"serviceAccountsEnabled":true},
			{"id":"c-spa","clientId":"web-spa","name":"Web SPA","enabled":true,"serviceAccountsEnabled":false},
			{"id":"c-old","clientId":"legacy","enabled":false,"serviceAccountsEnabled":false}
		]`
	case method == http.MethodGet && path == "/admin/realms/corp/roles":
		return 200, `[{"id":"r-adm","name":"admins","description":"realm admins"}]`
	case method == http.MethodGet && path == "/admin/realms/corp/roles/admins/users":
		return 200, `[{"id":"u-alice","username":"alice","enabled":true}]`
	case method == http.MethodGet && path == "/admin/realms/corp/groups":
		return 200, `[{"id":"g-eng","name":"engineering","path":"/engineering","subGroups":[
			{"id":"g-be","name":"backend","path":"/engineering/backend","subGroups":[]}
		]}]`
	case method == http.MethodGet && path == "/admin/realms/corp/groups/g-eng/members":
		return 200, `[{"id":"u-alice","username":"alice","enabled":true}]`
	case method == http.MethodGet && path == "/admin/realms/corp/groups/g-be/members":
		return 200, `[{"id":"u-bob","username":"bob","enabled":false}]`
	default:
		d.t.Fatalf("unexpected request: %s %s", method, path)
		return 500, ""
	}
}

func newSource(t *testing.T, d *stubDoer, cfg map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = d
	s.now = func() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestSnapshotClassifiesHumanNHIUnknown(t *testing.T) {
	d := &stubDoer{t: t}
	s := newSource(t, d, map[string]string{
		"base_url": "https://kc.corp.example", "realm": "corp",
		"client_id": "olivares-reader", "client_secret": "s3cr3t-shared",
	})

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceKeycloak {
		t.Fatalf("Source = %q, want keycloak", g.Source)
	}

	want := []struct {
		ref      string
		typ      identitysource.PrincipalType
		kind     string
		disabled bool
	}{
		{"u-alice", identitysource.PrincipalHuman, "user", false},
		{"u-svc", identitysource.PrincipalNHI, "service_account", false},
		{"u-bob", identitysource.PrincipalHuman, "user", true},
		{"c-ci", identitysource.PrincipalNHI, "service_account_client", false},
		{"c-spa", identitysource.PrincipalUnknown, "client", false},
		{"c-old", identitysource.PrincipalUnknown, "client", true},
	}
	for _, w := range want {
		id, ok := g.FindIdentity(w.ref)
		if !ok {
			t.Fatalf("identity %q not found", w.ref)
		}
		if id.Type != w.typ {
			t.Errorf("%s: Type = %q, want %q (nature must never be guessed)", w.ref, id.Type, w.typ)
		}
		if id.Kind != w.kind {
			t.Errorf("%s: Kind = %q, want %q", w.ref, id.Kind, w.kind)
		}
		if id.Disabled != w.disabled {
			t.Errorf("%s: Disabled = %v, want %v", w.ref, id.Disabled, w.disabled)
		}
	}

	// The service-account user carries its client link in metadata (not guessed).
	if svc, _ := g.FindIdentity("u-svc"); svc.Attributes["service_account_client"] != "ci-bot" {
		t.Errorf("u-svc service_account_client = %q, want ci-bot", svc.Attributes["service_account_client"])
	}

	assertCollection(t, g, "corp/role:admins", identitysource.KindRole)
	assertCollection(t, g, "g-eng", identitysource.KindGroup)
	assertCollection(t, g, "g-be", identitysource.KindGroup)

	assertMembership(t, g, "u-alice", identitysource.MemberIdentity, "corp/role:admins")
	assertMembership(t, g, "g-be", identitysource.MemberCollection, "g-eng") // nested group
	assertMembership(t, g, "u-alice", identitysource.MemberIdentity, "g-eng")
	assertMembership(t, g, "u-bob", identitysource.MemberIdentity, "g-be")
}

func TestNoSecretReachesAdminAPI(t *testing.T) {
	d := &stubDoer{t: t}
	s := newSource(t, d, map[string]string{
		"base_url": "https://kc.corp.example", "realm": "corp",
		"client_id": "olivares-reader", "client_secret": "s3cr3t-shared",
	})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, c := range d.calls {
		if c.method == http.MethodGet {
			if strings.Contains(c.body, "s3cr3t-shared") {
				t.Errorf("GET %s body leaked the client secret", c.path)
			}
			if c.auth != "Bearer test-token" {
				t.Errorf("GET %s Authorization = %q, want Bearer test-token", c.path, c.auth)
			}
		}
	}
	// The secret only ever appears in the single token POST body.
	tokenPosts := 0
	for _, c := range d.calls {
		if c.method == http.MethodPost && strings.Contains(c.path, "/token") {
			tokenPosts++
			if !strings.Contains(c.body, "grant_type=client_credentials") {
				t.Errorf("token POST missing client_credentials grant")
			}
		}
	}
	if tokenPosts != 1 {
		t.Errorf("token POSTs = %d, want exactly 1", tokenPosts)
	}
}

func TestOfflineEmptyGraphNoError(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Unix(0, 0).UTC() }
	// No client_secret => offline.
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"base_url": "https://kc.corp.example", "realm": "corp", "client_id": "olivares-reader",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("offline Snapshot returned error: %v", err)
	}
	if len(g.Identities) != 0 || len(g.Collections) != 0 || len(g.Memberships) != 0 {
		t.Errorf("offline graph not empty: %+v", g)
	}
	if g.Source != identitysource.SourceKeycloak {
		t.Errorf("offline Source = %q, want keycloak", g.Source)
	}
}

func TestProviderValidation(t *testing.T) {
	// Implemented providers Open cleanly (they snapshot offline without a credential).
	for _, p := range []string{"keycloak", "pingone", "ping", "forgerock", "Keycloak", "PINGONE"} {
		s := New()
		if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"provider": p, "base_url": "https://x"}}); err != nil {
			t.Errorf("provider %q: Open failed, want success: %v", p, err)
		}
	}
	// PingFederate is a verified category error (no user store of its own); an unknown
	// provider is refused. Neither invents an API.
	for _, p := range []string{"pingfederate", "auth0", "bogus"} {
		s := New()
		if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"provider": p, "base_url": "https://x"}}); err == nil {
			t.Errorf("provider %q: Open succeeded, want refusal", p)
		}
	}
}

func TestGatherEmitsNothing(t *testing.T) {
	s := New()
	if err := s.Gather(context.Background(), failSink{t}); err != nil {
		t.Fatalf("Gather: %v", err)
	}
}

// failSink fails the test if anything is emitted (a roster is reference data, not
// an observation — Gather must be a no-op).
type failSink struct{ t *testing.T }

func (f failSink) Emit(context.Context, model.Observation) error {
	f.t.Fatal("Gather emitted an observation; it must be a no-op")
	return nil
}

func assertCollection(t *testing.T, g identitysource.Graph, ref string, kind identitysource.CollectionKind) {
	t.Helper()
	for _, c := range g.Collections {
		if c.Ref == ref {
			if c.Kind != kind {
				t.Errorf("collection %q Kind = %q, want %q", ref, c.Kind, kind)
			}
			return
		}
	}
	t.Errorf("collection %q not found", ref)
}

func assertMembership(t *testing.T, g identitysource.Graph, member string, mk identitysource.MemberKind, coll string) {
	t.Helper()
	for _, m := range g.Memberships {
		if m.MemberRef == member && m.MemberKind == mk && m.CollectionRef == coll {
			return
		}
	}
	t.Errorf("membership %s (%s) ∈ %s not found", member, mk, coll)
}
