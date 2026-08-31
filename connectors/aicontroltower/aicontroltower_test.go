// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aicontroltower

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// recordedRequest captures what the connector sent so a test can assert the
// auth header, method, URL and that no secret was placed on the wire.
type recordedRequest struct {
	Method string
	URL    string
	Auth   string
}

// route is one programmed response for a match key (consumed in order).
type route struct {
	status int
	body   string
}

// stubDoer routes requests by (method, path-substring), returning the next
// queued route for each key and recording every request.
type stubDoer struct {
	t       *testing.T
	routes  map[string][]route // key: METHOD + " " + pathSubstring
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

// on queues a 200 JSON response for a method + match key. match is compared as
// a substring of the request URL path (or the full URL).
func (d *stubDoer) on(method, match, body string) *stubDoer {
	key := method + " " + match
	d.routes[key] = append(d.routes[key], route{status: 200, body: body})
	return d
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls = append(d.calls, recordedRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Auth:   req.Header.Get("Authorization"),
	})
	for key, queued := range d.routes {
		parts := strings.SplitN(key, " ", 2)
		if parts[0] != req.Method || len(queued) == 0 {
			continue
		}
		if strings.Contains(req.URL.Path, parts[1]) || strings.Contains(req.URL.String(), parts[1]) {
			r := queued[0]
			d.routes[key] = queued[1:]
			h := http.Header{}
			h.Set("Content-Type", "application/json")
			return &http.Response{
				StatusCode: r.status,
				Header:     h,
				Body:       io.NopCloser(bytes.NewBufferString(r.body)),
				Request:    req,
			}, nil
		}
	}
	d.t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
	return nil, fmt.Errorf("unreachable")
}

const (
	testInstance = "https://acme.service-now.com"
	testUser     = "olivares.reader"
	testPassword = "sn-very-secret-password"
	testToken    = "sn-oauth-bearer-secret"
)

func openSource(t *testing.T, d *stubDoer, settings map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = d
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
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

// TestSnapshotMultiTable drives both default tables with page_size=2: the AI
// table pages (full page 1 → page 2, short → stop), the MCP table pages (full
// page 1 → empty page 2 → stop). It covers the tolerant row mapping, the
// retired mappings, the display-name fallbacks and the sys_id-required skip.
func TestSnapshotMultiTable(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/api/now/table/alm_ai_system_digital_asset", d.fixture("ai_assets_page1.json"))
	d.on(http.MethodGet, "/api/now/table/alm_ai_system_digital_asset", d.fixture("ai_assets_page2.json"))
	d.on(http.MethodGet, "/api/now/table/alm_mcp_digital_asset", d.fixture("mcp_assets_page1.json"))
	d.on(http.MethodGet, "/api/now/table/alm_mcp_digital_asset", `{"result":[]}`)

	fixed := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	s := openSource(t, d, map[string]string{
		"instance_url": testInstance,
		"username":     testUser,
		"password":     testPassword,
		"page_size":    "2",
	})
	s.now = func() time.Time { return fixed }

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceAIControlTower {
		t.Errorf("source = %q, want ai-control-tower", g.Source)
	}
	if !g.CapturedAt.Equal(fixed) {
		t.Errorf("CapturedAt = %v, want %v", g.CapturedAt, fixed)
	}

	// Pagination advanced to offset=2 on both tables and stopped there.
	if !sawPath(d, "sysparm_offset=2") {
		t.Error("connector did not fetch the second page (sysparm_offset=2)")
	}
	if sawPath(d, "sysparm_offset=4") {
		t.Error("connector over-fetched past the short/empty page")
	}
	if !sawPath(d, "sysparm_exclude_reference_link=true") {
		t.Error("sysparm_exclude_reference_link=true must be sent")
	}

	// 2 valid AI rows (the sys_id-less row skipped) + 2 MCP rows = 4.
	if got := len(g.Identities); got != 4 {
		t.Fatalf("identities = %d, want 4 (sys_id-less row skipped)", got)
	}
	if len(g.Collections) != 0 || len(g.Memberships) != 0 {
		t.Errorf("asset inventory carries no collections/memberships: %d/%d",
			len(g.Collections), len(g.Memberships))
	}

	// Full AI row: display_name wins, managed lifted, managed_by NOT lifted,
	// non-string fields tolerated, active status not disabled.
	fraud, ok := g.FindIdentity("a1f4b2c3d4e5f60718293a4b5c6d7e8f")
	if !ok {
		t.Fatal("Fraud Scoring Model missing")
	}
	if fraud.Type != identitysource.PrincipalNHI || fraud.Kind != "ai_system_asset" {
		t.Errorf("type/kind = %q/%q, want nhi/ai_system_asset", fraud.Type, fraud.Kind)
	}
	if fraud.DisplayName != "Fraud Scoring Model" {
		t.Errorf("displayName = %q, want display_name to win", fraud.DisplayName)
	}
	if fraud.Disabled {
		t.Error("install_status=1 must not be Disabled")
	}
	wantAttrs := map[string]string{
		"table":        "alm_ai_system_digital_asset",
		"state":        "1",
		"stage":        "Operational",
		"stage_status": "In use",
		"managed":      "true",
	}
	for k, v := range wantAttrs {
		if fraud.Attributes[k] != v {
			t.Errorf("attribute %q = %q, want %q", k, fraud.Attributes[k], v)
		}
	}
	if len(fraud.Attributes) != len(wantAttrs) {
		t.Errorf("attributes = %v, want exactly %v (managed_by must not be lifted)", fraud.Attributes, wantAttrs)
	}

	// Retired by install_status 7; display falls back to name.
	legacy, ok := g.FindIdentity("b2c3d4e5f60718293a4b5c6d7e8fa1f4")
	if !ok {
		t.Fatal("Legacy Classifier (page 2) missing — pagination did not append")
	}
	if !legacy.Disabled {
		t.Error("install_status=7 (Retired) must be Disabled")
	}
	if legacy.DisplayName != "Legacy Classifier" {
		t.Errorf("displayName = %q, want name fallback", legacy.DisplayName)
	}

	// Retired by stage_status (case-insensitive "End Of Life"); display falls
	// back to number; kind follows the MCP table.
	eol, ok := g.FindIdentity("c3d4e5f60718293a4b5c6d7e8fa1f4b2")
	if !ok {
		t.Fatal("EOL MCP asset missing")
	}
	if eol.Kind != "mcp_asset" {
		t.Errorf("kind = %q, want mcp_asset", eol.Kind)
	}
	if !eol.Disabled {
		t.Error("life_cycle_stage_status 'End Of Life' must be Disabled (case-insensitive)")
	}
	if eol.DisplayName != "MCP0001001" {
		t.Errorf("displayName = %q, want number fallback", eol.DisplayName)
	}

	// Minimal row (sys_id only): display falls back to sys_id, attributes prune
	// to the table only.
	bare, ok := g.FindIdentity("d4e5f60718293a4b5c6d7e8fa1f4b2c3")
	if !ok {
		t.Fatal("minimal MCP row missing")
	}
	if bare.DisplayName != "d4e5f60718293a4b5c6d7e8fa1f4b2c3" {
		t.Errorf("displayName = %q, want sys_id fallback", bare.DisplayName)
	}
	if got := len(bare.Attributes); got != 1 || bare.Attributes["table"] != "alm_mcp_digital_asset" {
		t.Errorf("bare attributes = %v, want only {table}", bare.Attributes)
	}
	if bare.Disabled {
		t.Error("a row without status fields must not be guessed Disabled")
	}
}

// TestPaginationStopsOnPartialFirstPage proves a short first page terminates
// the table immediately (one GET per table).
func TestPaginationStopsOnPartialFirstPage(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/api/now/table/alm_ai_system_digital_asset", d.fixture("ai_assets_page2.json"))

	s := openSource(t, d, map[string]string{
		"instance_url": testInstance,
		"username":     testUser,
		"password":     testPassword,
		"tables":       "alm_ai_system_digital_asset",
		// default page_size 200 >> 1 row => short page
	})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(d.calls) != 1 {
		t.Fatalf("calls = %d, want exactly 1 (short page => stop)", len(d.calls))
	}
	if len(g.Identities) != 1 {
		t.Fatalf("identities = %d, want 1", len(g.Identities))
	}
}

// TestCustomTableGenericKind proves an operator-configured table outside the
// known set maps to the generic ai_asset kind.
func TestCustomTableGenericKind(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/api/now/table/u_custom_agent_asset",
		`{"result":[{"sys_id":"e5f60718","name":"Custom Agent"}]}`)

	s := openSource(t, d, map[string]string{
		"instance_url": testInstance,
		"auth_mode":    "bearer",
		"token":        testToken,
		"tables":       " u_custom_agent_asset , ",
	})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	id, ok := g.FindIdentity("e5f60718")
	if !ok {
		t.Fatal("custom-table row missing")
	}
	if id.Kind != "ai_asset" {
		t.Errorf("kind = %q, want generic ai_asset", id.Kind)
	}
	if id.Attributes["table"] != "u_custom_agent_asset" {
		t.Errorf("table attribute = %q", id.Attributes["table"])
	}
}

func TestBasicAuthHeader(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/api/now/table/alm_ai_system_digital_asset", `{"result":[]}`)
	d.on(http.MethodGet, "/api/now/table/alm_mcp_digital_asset", `{"result":[]}`)

	s := openSource(t, d, map[string]string{
		"instance_url": testInstance,
		"auth_mode":    "basic",
		"username":     testUser,
		"password":     testPassword,
	})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(d.calls) == 0 {
		t.Fatal("no calls recorded")
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(testUser+":"+testPassword))
	for _, c := range d.calls {
		if c.Auth != want {
			t.Errorf("basic auth header = %q, want exactly %q", c.Auth, want)
		}
		if strings.HasPrefix(c.Auth, "Bearer") {
			t.Errorf("basic mode must not send Bearer: %q", c.Auth)
		}
	}
}

func TestBearerAuthHeader(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/api/now/table/alm_ai_system_digital_asset", `{"result":[]}`)
	d.on(http.MethodGet, "/api/now/table/alm_mcp_digital_asset", `{"result":[]}`)

	s := openSource(t, d, map[string]string{
		"instance_url": testInstance,
		"auth_mode":    "bearer",
		"token":        testToken,
	})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(d.calls) == 0 {
		t.Fatal("no calls recorded")
	}
	for _, c := range d.calls {
		if c.Auth != "Bearer "+testToken {
			t.Errorf("bearer auth header = %q, want exactly Bearer <token>", c.Auth)
		}
		if strings.HasPrefix(c.Auth, "Basic") {
			t.Errorf("bearer mode must not send Basic: %q", c.Auth)
		}
	}
}

func TestAPIErrorCarriesStatusNotCredential(t *testing.T) {
	d := newStub(t)
	d.routes["GET /api/now/table/alm_ai_system_digital_asset"] = []route{
		{status: 401, body: `{"error":{"message":"User Not Authenticated","detail":"Required to provide Auth information"},"status":"failure"}`},
	}
	s := openSource(t, d, map[string]string{
		"instance_url": testInstance,
		"username":     testUser,
		"password":     testPassword,
	})
	_, err := s.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should carry status: %v", err)
	}
	basic := base64.StdEncoding.EncodeToString([]byte(testUser + ":" + testPassword))
	for _, secret := range []string{testPassword, testToken, basic} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error must not contain the credential: %v", err)
		}
	}
}

// Offline: missing instance URL, or missing credential for the configured auth
// mode, yields an empty graph with Source+CapturedAt set and a NIL error — and
// the wire is never touched (nil doer would panic the test otherwise).
func TestOfflineEmptyGraph(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]string
	}{
		{"no instance_url", map[string]string{"username": testUser, "password": testPassword}},
		{"basic missing password", map[string]string{"instance_url": testInstance, "username": testUser}},
		{"basic missing username", map[string]string{"instance_url": testInstance, "password": testPassword}},
		{"bearer missing token", map[string]string{"instance_url": testInstance, "auth_mode": "bearer"}},
	}
	fixed := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New() // no doer: the wire must not be touched
			if err := s.Open(context.Background(), sdk.Config{Settings: tc.settings}); err != nil {
				t.Fatalf("Open with missing credential must not fail: %v", err)
			}
			s.now = func() time.Time { return fixed }
			g, err := s.Snapshot(context.Background())
			if err != nil {
				t.Fatalf("offline Snapshot should not error: %v", err)
			}
			if g.Source != identitysource.SourceAIControlTower {
				t.Errorf("offline source = %q", g.Source)
			}
			if !g.CapturedAt.Equal(fixed) {
				t.Errorf("offline CapturedAt = %v, want set", g.CapturedAt)
			}
			if len(g.Identities) != 0 || len(g.Collections) != 0 || len(g.Memberships) != 0 {
				t.Errorf("offline graph must be empty: %+v", g)
			}
		})
	}
}

func TestOpenRejectsUnknownAuthMode(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"instance_url": testInstance,
		"auth_mode":    "oauth2-mtls",
	}})
	if err == nil {
		t.Fatal("expected error for unknown auth_mode (malformed config)")
	}
}

func TestNoSecretLeaksIntoGraph(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/api/now/table/alm_ai_system_digital_asset", d.fixture("ai_assets_page1.json"))
	d.on(http.MethodGet, "/api/now/table/alm_mcp_digital_asset", d.fixture("mcp_assets_page1.json"))
	s := openSource(t, d, map[string]string{
		"instance_url": testInstance,
		"username":     testUser,
		"password":     testPassword,
		"page_size":    "200", // both fixtures are short pages => one page each
	})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	basic := base64.StdEncoding.EncodeToString([]byte(testUser + ":" + testPassword))
	assertNoSecret(t, g, testPassword, testToken, basic)
}

// assertNoSecret walks every string field of the Graph and fails if any secret
// appears, proving the connector carries only asset metadata.
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

func TestDescriptorShape(t *testing.T) {
	desc := New().Descriptor()
	if desc.Name != Name || desc.Type != sdk.TypeSource || desc.APIVersion != sdk.APIVersion || desc.Version != "0.1.0" {
		t.Errorf("descriptor header wrong: %+v", desc)
	}
	secret := map[string]bool{}
	for _, f := range desc.ConfigFields {
		secret[f.Key] = f.Secret
	}
	if !secret["password"] {
		t.Error("password must be Secret")
	}
	if !secret["token"] {
		t.Error("token must be Secret")
	}
}

func TestOpenDefaults(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"instance_url": testInstance + "/",
		"page_size":    "0",
		"max_pages":    "-1",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.instanceURL != testInstance {
		t.Errorf("instance_url = %q, want trailing slash trimmed", s.instanceURL)
	}
	if s.authMode != authBasic {
		t.Errorf("auth_mode = %q, want default basic", s.authMode)
	}
	if got := strings.Join(s.tables, ","); got != defaultTables {
		t.Errorf("tables = %q, want default %q", got, defaultTables)
	}
	if s.pageSize != defaultPageSize || s.maxPages != defaultMaxPages {
		t.Errorf("page_size/max_pages = %d/%d, want defaults (non-positive rejected)", s.pageSize, s.maxPages)
	}
}

func TestGatherEmitsNothing(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"instance_url": testInstance,
		"username":     testUser,
		"password":     testPassword,
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	err := s.Gather(context.Background(), sinkFunc(func() error {
		t.Fatal("aicontroltower Gather must not emit observations")
		return nil
	}))
	if err != nil {
		t.Fatalf("Gather should return nil, got %v", err)
	}
}

// sinkFunc adapts a func to sdk.Sink for the no-emit assertion.
type sinkFunc func() error

func (f sinkFunc) Emit(context.Context, model.Observation) error { return f() }

// TestPaginationBoundedByMaxPages: a table whose every page comes back full
// (page_size rows) is stopped by max_pages — sysparm_offset never advances past
// the bound and the third queued page stays unconsumed.
func TestPaginationBoundedByMaxPages(t *testing.T) {
	d := newStub(t)
	full := `{"result":[{"sys_id":"r1","display_name":"A"},{"sys_id":"r2","display_name":"B"}]}`
	d.on(http.MethodGet, "/api/now/table/alm_ai_system_digital_asset", full)
	d.on(http.MethodGet, "/api/now/table/alm_ai_system_digital_asset", full)
	d.on(http.MethodGet, "/api/now/table/alm_ai_system_digital_asset", full)

	s := openSource(t, d, map[string]string{
		"instance_url": testInstance,
		"username":     testUser,
		"password":     testPassword,
		"page_size":    "2",
		"max_pages":    "2",
		"tables":       "alm_ai_system_digital_asset",
	})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := len(d.calls); got != 2 {
		t.Errorf("requests = %d, want exactly max_pages=2", got)
	}
	if sawPath(d, "sysparm_offset=4") {
		t.Error("connector must stop at max_pages, not request offset=4")
	}
}
