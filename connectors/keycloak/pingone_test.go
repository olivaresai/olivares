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

// pingStub answers PingOne worker-token + Management-API requests from in-memory
// HAL fixtures and records every request (so a test asserts no secret leaked to the
// API and the bearer is applied). It matches on method + URL path (ignoring query).
type pingStub struct {
	t     *testing.T
	calls []recorded
}

func (d *pingStub) Do(req *http.Request) (*http.Response, error) {
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

func (d *pingStub) route(method, path string) (int, string) {
	const env = "/v1/environments/env-1"
	switch {
	case method == http.MethodPost && strings.HasSuffix(path, "/as/token"):
		return 200, `{"access_token":"ping-token","token_type":"Bearer","expires_in":3600}`
	case method == http.MethodGet && path == env+"/groups":
		return 200, `{"_embedded":{"groups":[
			{"id":"grp-eng","name":"Engineering","description":"eng","externalId":"EXT-ENG"},
			{"id":"grp-sec","name":"Security"}
		]},"_links":{"self":{"href":"x"}}}`
	case method == http.MethodGet && path == env+"/users":
		return 200, `{"_embedded":{"users":[
			{"id":"usr-alice","username":"alice","email":"alice@corp.example","enabled":true,
			 "name":{"given":"Alice","family":"Stone"},"population":{"id":"pop-1"},
			 "memberOfGroupIDs":["grp-eng","grp-sec"]},
			{"id":"usr-bob","username":"bob","enabled":false,"memberOfGroupIDs":["grp-eng"]}
		]},"_links":{"self":{"href":"x"}}}`
	case method == http.MethodGet && path == env+"/applications":
		return 200, `{"_embedded":{"applications":[
			{"id":"app-worker","name":"CI Worker","type":"WORKER","enabled":true},
			{"id":"app-svc","name":"Sync Service","type":"CUSTOM","grantTypes":["CLIENT_CREDENTIALS"]},
			{"id":"app-web","name":"Web App","type":"WEB_APP","grantTypes":["AUTHORIZATION_CODE"]}
		]},"_links":{"self":{"href":"x"}}}`
	case method == http.MethodGet && path == env+"/users/usr-alice/roleAssignments":
		return 200, `{"_embedded":{"roleAssignments":[
			{"id":"ra-1","role":{"id":"role-ida","name":"Identity Data Admin"},"scope":{"id":"env-1","type":"ENVIRONMENT"}}
		]},"_links":{"self":{"href":"x"}}}`
	case method == http.MethodGet && path == env+"/users/usr-bob/roleAssignments":
		return 200, `{"_embedded":{"roleAssignments":[]},"_links":{"self":{"href":"x"}}}`
	case method == http.MethodGet && path == env+"/groups/grp-eng/roleAssignments":
		return 200, `{"_embedded":{"roleAssignments":[
			{"id":"ra-2","role":{"id":"role-env"},"scope":{"id":"env-1","type":"ENVIRONMENT"}}
		]},"_links":{"self":{"href":"x"}}}`
	case method == http.MethodGet && path == env+"/groups/grp-sec/roleAssignments":
		return 200, `{"_embedded":{"roleAssignments":[]},"_links":{"self":{"href":"x"}}}`
	default:
		d.t.Fatalf("unexpected pingone request: %s %s", method, path)
		return 500, ""
	}
}

func newPingSource(t *testing.T, d *pingStub, cfg map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = d
	s.now = func() time.Time { return time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestPingOneSnapshot(t *testing.T) {
	d := &pingStub{t: t}
	s := newPingSource(t, d, map[string]string{
		"provider": "pingone", "environment_id": "env-1",
		"client_id": "worker-1", "client_secret": "worker-secret",
	})

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourcePingOne {
		t.Fatalf("Source = %q, want pingone", g.Source)
	}

	// Humans from /users; WORKER + client-credentials apps from /applications as NHI;
	// the plain web app is NOT a directory principal.
	want := []struct {
		ref      string
		typ      identitysource.PrincipalType
		kind     string
		disabled bool
	}{
		{"usr-alice", identitysource.PrincipalHuman, "user", false},
		{"usr-bob", identitysource.PrincipalHuman, "user", true},
		{"app-worker", identitysource.PrincipalNHI, "worker_application", false},
		{"app-svc", identitysource.PrincipalNHI, "worker_application", false},
	}
	for _, w := range want {
		id, ok := g.FindIdentity(w.ref)
		if !ok {
			t.Fatalf("identity %q not found", w.ref)
		}
		if id.Type != w.typ || id.Kind != w.kind || id.Disabled != w.disabled {
			t.Errorf("%s: got {%s,%s,disabled=%v} want {%s,%s,disabled=%v}", w.ref, id.Type, id.Kind, id.Disabled, w.typ, w.kind, w.disabled)
		}
	}
	if _, ok := g.FindIdentity("app-web"); ok {
		t.Error("plain WEB_APP must not be rostered as a directory principal")
	}

	// Groups and discovered roles are collections; group/role links are memberships.
	assertCollection(t, g, pingGroupRef("grp-eng"), identitysource.KindGroup)
	assertCollection(t, g, pingRoleRef("role-ida"), identitysource.KindRole)
	assertCollection(t, g, pingRoleRef("role-env"), identitysource.KindRole)
	assertMembership(t, g, "usr-alice", identitysource.MemberIdentity, pingGroupRef("grp-eng"))
	assertMembership(t, g, "usr-alice", identitysource.MemberIdentity, pingGroupRef("grp-sec"))
	assertMembership(t, g, "usr-alice", identitysource.MemberIdentity, pingRoleRef("role-ida"))
	assertMembership(t, g, "grp-eng", identitysource.MemberCollection, pingRoleRef("role-env"))

	// The discovered role keeps the name from the assignment; the id-only one falls back.
	for _, c := range g.Collections {
		if c.Ref == pingRoleRef("role-ida") && c.DisplayName != "Identity Data Admin" {
			t.Errorf("role-ida DisplayName = %q, want the assignment's role name", c.DisplayName)
		}
		if c.Ref == pingRoleRef("role-env") && c.DisplayName != "role-env" {
			t.Errorf("role-env DisplayName = %q, want the id fallback", c.DisplayName)
		}
	}
}

func TestPingOneNoSecretReachesAPI(t *testing.T) {
	d := &pingStub{t: t}
	s := newPingSource(t, d, map[string]string{
		"provider": "pingone", "environment_id": "env-1",
		"client_id": "worker-1", "client_secret": "worker-secret",
	})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	tokenPosts := 0
	for _, c := range d.calls {
		if c.method == http.MethodGet {
			if strings.Contains(c.body, "worker-secret") {
				t.Errorf("GET %s leaked the worker secret", c.path)
			}
			if c.auth != "Bearer ping-token" {
				t.Errorf("GET %s Authorization = %q, want Bearer ping-token", c.path, c.auth)
			}
		}
		if c.method == http.MethodPost && strings.HasSuffix(c.path, "/as/token") {
			tokenPosts++
			// The worker secret rides the Basic header, never the body.
			if strings.Contains(c.body, "worker-secret") {
				t.Errorf("token POST body leaked the secret (must be in the Basic header)")
			}
			if c.auth == "" || !strings.HasPrefix(c.auth, "Basic ") {
				t.Errorf("token POST Authorization = %q, want a Basic header", c.auth)
			}
		}
	}
	if tokenPosts != 1 {
		t.Errorf("token POSTs = %d, want exactly 1", tokenPosts)
	}
}

func TestPingOneReadRoleAssignmentsOptOut(t *testing.T) {
	d := &pingStub{t: t}
	s := newPingSource(t, d, map[string]string{
		"provider": "pingone", "environment_id": "env-1",
		"client_id": "worker-1", "client_secret": "worker-secret",
		"read_role_assignments": "false",
	})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// With the opt-out, no per-USER role-assignment GET is issued (group assignments
	// are still read — bounded by #groups).
	for _, c := range d.calls {
		if strings.Contains(c.path, "/users/") && strings.HasSuffix(c.path, "/roleAssignments") {
			t.Errorf("per-user role-assignment read %s issued despite read_role_assignments=false", c.path)
		}
	}
}

// TestPingOneCrossEnvToken pins the fix for the worker-vs-target environment split:
// the token endpoint embeds the WORKER app's environment id (auth_environment_id),
// while the Management-API path uses the TARGET environment id.
func TestPingOneCrossEnvToken(t *testing.T) {
	d := &pingStub{t: t}
	s := newPingSource(t, d, map[string]string{
		"provider": "pingone", "environment_id": "env-1", "auth_environment_id": "admin-9",
		"client_id": "worker-1", "client_secret": "worker-secret",
	})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var tokenPath string
	for _, c := range d.calls {
		if c.method == http.MethodPost && strings.HasSuffix(c.path, "/as/token") {
			tokenPath = c.path
		}
	}
	if tokenPath != "/admin-9/as/token" {
		t.Errorf("token path = %q, want /admin-9/as/token (the worker env, not the target env-1)", tokenPath)
	}
}

// pingPageStub serves a TWO-page /users collection and records the raw query of each
// request, so a test can assert include=memberOfGroupIDs is carried onto page 2.
type pingPageStub struct {
	t       *testing.T
	queries []string
}

func (d *pingPageStub) Do(req *http.Request) (*http.Response, error) {
	d.queries = append(d.queries, req.URL.Path+"?"+req.URL.RawQuery)
	path, q := req.URL.Path, req.URL.Query()
	const env = "/v1/environments/env-1"
	switch {
	case req.Method == http.MethodPost && strings.HasSuffix(path, "/as/token"):
		return pingResp(`{"access_token":"ping-token","token_type":"Bearer"}`), nil
	case path == env+"/groups", path == env+"/applications":
		return pingResp(`{"_embedded":{},"_links":{"self":{"href":"x"}}}`), nil
	case path == env+"/users" && q.Get("cursor") == "":
		// page 1: one user + a next cursor link (absolute, same-origin).
		return pingResp(`{"_embedded":{"users":[{"id":"u1","username":"u1","enabled":true,"memberOfGroupIDs":["g1"]}]},
			"_links":{"next":{"href":"https://api.pingone.com` + env + `/users?cursor=PAGE2&limit=100"}}}`), nil
	case path == env+"/users" && q.Get("cursor") == "PAGE2":
		// page 2: the connector MUST have re-appended include=memberOfGroupIDs here.
		if q.Get("include") != "memberOfGroupIDs" {
			d.t.Errorf("page 2 dropped include=memberOfGroupIDs (query=%q)", req.URL.RawQuery)
		}
		return pingResp(`{"_embedded":{"users":[{"id":"u2","username":"u2","enabled":true,"memberOfGroupIDs":["g2"]}]},
			"_links":{"self":{"href":"x"}}}`), nil
	case strings.HasSuffix(path, "/roleAssignments"):
		return pingResp(`{"_embedded":{"roleAssignments":[]},"_links":{"self":{"href":"x"}}}`), nil
	default:
		d.t.Fatalf("unexpected request: %s?%s", path, req.URL.RawQuery)
		return nil, nil
	}
}

func pingResp(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": {"application/json"}},
	}
}

func TestPingOnePaginationCarriesInclude(t *testing.T) {
	d := &pingPageStub{t: t}
	s := New()
	s.doer = d
	s.now = func() time.Time { return time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"provider": "pingone", "environment_id": "env-1",
		"client_id": "worker-1", "client_secret": "worker-secret",
		"read_role_assignments": "false",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// Both pages' users (and their memberships) must be present.
	for _, ref := range []string{"u1", "u2"} {
		if _, ok := g.FindIdentity(ref); !ok {
			t.Errorf("user %q from a paginated scan is missing", ref)
		}
	}
	assertMembership(t, g, "u2", identitysource.MemberIdentity, pingGroupRef("g2"))
}

func TestPingOneOfflineNoCredential(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Unix(0, 0).UTC() }
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"provider": "pingone", "environment_id": "env-1", "client_id": "worker-1",
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
	if g.Source != identitysource.SourcePingOne {
		t.Errorf("offline Source = %q, want pingone", g.Source)
	}
}
