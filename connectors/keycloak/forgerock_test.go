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
)

// frStub answers PingIDM/Identity Cloud CREST requests from in-memory fixtures and
// records each request (so a test asserts the auth headers and the API version).
// noGroup makes the managed/{prefix}group object 404 (the self-managed shape).
type frStub struct {
	t        *testing.T
	prefix   string
	noGroup  bool
	group400 bool
	calls    []frCall
}

type frCall struct {
	method string
	path   string
	user   string
	pass   string
	bearer string
	apiVer string
}

func (d *frStub) Do(req *http.Request) (*http.Response, error) {
	d.calls = append(d.calls, frCall{
		method: req.Method, path: req.URL.Path,
		user: req.Header.Get("X-OpenIDM-Username"), pass: req.Header.Get("X-OpenIDM-Password"),
		bearer: req.Header.Get("Authorization"), apiVer: req.Header.Get("Accept-API-Version"),
	})
	status, payload := d.route(req.URL.Path)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(payload)),
		Header:     http.Header{"Content-Type": {"application/json"}},
	}, nil
}

func (d *frStub) route(path string) (int, string) {
	m := "/openidm/managed/" + d.prefix
	switch path {
	case m + "user":
		return 200, `{"result":[
			{"_id":"u-alice","userName":"alice","givenName":"Alice","sn":"Stone","mail":"alice@corp.example","accountStatus":"active"},
			{"_id":"u-bob","userName":"bob","accountStatus":"inactive"}
		],"pagedResultsCookie":null,"totalPagedResults":-1}`
	case m + "role":
		return 200, `{"result":[
			{"_id":"r-admin","name":"app-admins","description":"administrators"}
		],"pagedResultsCookie":null}`
	case m + "role/r-admin/members":
		return 200, `{"result":[
			{"_ref":"managed/` + d.prefix + `user/u-alice","_refResourceId":"u-alice"}
		],"pagedResultsCookie":null}`
	case m + "group":
		if d.group400 {
			return 400, `{"code":400,"reason":"Bad Request","message":"malformed _queryFilter"}`
		}
		if d.noGroup {
			return 404, `{"code":404,"reason":"Not Found","message":"managed/group not found"}`
		}
		return 200, `{"result":[
			{"_id":"g-eng","name":"Engineering","description":"eng"}
		],"pagedResultsCookie":null}`
	default:
		d.t.Fatalf("unexpected forgerock request: %s", path)
		return 500, ""
	}
}

func newFRSource(t *testing.T, d *frStub, cfg map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = d
	s.now = func() time.Time { return time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// TestForgeRockSelfManaged reads a self-managed PingIDM (no object prefix, no
// managed/group) via X-OpenIDM header auth.
func TestForgeRockSelfManaged(t *testing.T) {
	d := &frStub{t: t, noGroup: true}
	s := newFRSource(t, d, map[string]string{
		"provider": "forgerock", "base_url": "https://idm.corp.example",
		"username": "openidm-admin", "password": "idm-secret",
	})

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceForgeRock {
		t.Fatalf("Source = %q, want forgerock", g.Source)
	}

	alice, ok := g.FindIdentity("u-alice")
	if !ok || alice.Type != identitysource.PrincipalHuman || alice.Disabled {
		t.Errorf("u-alice = %+v, want human/enabled", alice)
	}
	if alice.DisplayName != "Alice Stone" {
		t.Errorf("u-alice DisplayName = %q, want 'Alice Stone'", alice.DisplayName)
	}
	bob, _ := g.FindIdentity("u-bob")
	if !bob.Disabled {
		t.Error("u-bob accountStatus=inactive must map to Disabled")
	}

	assertCollection(t, g, frRoleRef("r-admin"), identitysource.KindRole)
	assertMembership(t, g, "u-alice", identitysource.MemberIdentity, frRoleRef("r-admin"))

	// Self-managed has no managed/group: the 404 is absorbed, no group collection.
	for _, c := range g.Collections {
		if c.Kind == identitysource.KindGroup {
			t.Errorf("unexpected group collection on self-managed PingIDM: %q", c.Ref)
		}
	}

	// Every call carried the X-OpenIDM credential + the pinned API version, never a bearer.
	for _, c := range d.calls {
		if c.user != "openidm-admin" || c.pass != "idm-secret" {
			t.Errorf("%s missing X-OpenIDM headers (user=%q)", c.path, c.user)
		}
		if c.apiVer != "resource=1.0" {
			t.Errorf("%s Accept-API-Version = %q, want resource=1.0", c.path, c.apiVer)
		}
		if c.bearer != "" {
			t.Errorf("%s sent a bearer header under password auth", c.path)
		}
	}
}

// TestForgeRockIdentityCloud reads an Identity Cloud realm (alpha_ prefix) with a
// pre-obtained bearer token, and resolves a group collection.
func TestForgeRockIdentityCloud(t *testing.T) {
	d := &frStub{t: t, prefix: "alpha_"}
	s := newFRSource(t, d, map[string]string{
		"provider": "forgerock", "base_url": "https://tenant.forgeblocks.com",
		"object_prefix": "alpha_", "bearer_token": "ic-bearer",
	})

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, ok := g.FindIdentity("u-alice"); !ok {
		t.Error("alpha_user not read under the realm prefix")
	}
	assertCollection(t, g, frGroupRef("g-eng"), identitysource.KindGroup)
	assertMembership(t, g, "u-alice", identitysource.MemberIdentity, frRoleRef("r-admin"))

	// The member _ref carried the realm-prefixed path; refTail resolved the bare id.
	for _, c := range d.calls {
		if c.bearer != "Bearer ic-bearer" {
			t.Errorf("%s Authorization = %q, want Bearer ic-bearer", c.path, c.bearer)
		}
		if c.user != "" {
			t.Errorf("%s sent X-OpenIDM headers under bearer auth", c.path)
		}
	}
}

// TestForgeRockGroupErrorSurfaces proves only a 404 (object type absent) is absorbed
// on the group read; a 400 (a real query bug) must NOT be silently swallowed.
func TestForgeRockGroupErrorSurfaces(t *testing.T) {
	d := &frStub{t: t, group400: true}
	s := newFRSource(t, d, map[string]string{
		"provider": "forgerock", "base_url": "https://idm.corp.example",
		"username": "openidm-admin", "password": "idm-secret",
	})
	if _, err := s.Snapshot(context.Background()); err == nil {
		t.Error("a 400 on the managed/group read must surface as an error, not be absorbed like a 404")
	}
}

func TestForgeRockOfflineNoCredential(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Unix(0, 0).UTC() }
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"provider": "forgerock", "base_url": "https://idm.corp.example", "username": "openidm-admin",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("offline Snapshot returned error: %v", err)
	}
	if len(g.Identities) != 0 || len(g.Collections) != 0 {
		t.Errorf("offline graph not empty: %+v", g)
	}
	if g.Source != identitysource.SourceForgeRock {
		t.Errorf("offline Source = %q, want forgerock", g.Source)
	}
}

func TestRefTail(t *testing.T) {
	cases := map[string]string{
		"managed/alpha_user/u-1": "u-1",
		"managed/user/u-2":       "u-2",
		"/managed/user/u-3/":     "u-3",
		"u-4":                    "u-4",
		"":                       "",
	}
	for in, want := range cases {
		if got := refTail(in); got != want {
			t.Errorf("refTail(%q) = %q, want %q", in, got, want)
		}
	}
}
