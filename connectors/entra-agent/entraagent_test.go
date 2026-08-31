// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package entraagent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	testTokenURL    = "https://login.test.local/token"
	testSecret      = "verysecretclientsecret"
	testAccessToken = "eyJ0eXAfake.graph.access.token"
)

// recordedRequest captures what the connector sent so a test can assert auth
// headers, method, URL and (crucially) that no secret was placed on the wire.
type recordedRequest struct {
	Method string
	URL    string
	Auth   string
	Prefer string
	Body   string
}

// route is a programmed response: the body (and status) to return for the i-th
// matching call.
type route struct {
	status int
	body   string
}

// stubDoer routes requests by (method, longest matching path-substring),
// returning the next queued route for each key and recording every request.
// Longest-match keeps the Graph cast endpoints deterministic: a request for
// .../microsoft.graph.agentIdentityBlueprintPrincipal also CONTAINS the
// .../microsoft.graph.agentIdentity match string, so the most specific queued
// key must win regardless of map iteration order.
type stubDoer struct {
	t       *testing.T
	routes  map[string][]route // key: METHOD + " " + pathOrURLSubstring
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

// on queues a 200 JSON response for a method + match key.
func (d *stubDoer) on(method, match, body string) *stubDoer {
	return d.onStatus(method, match, 200, body)
}

// onStatus queues a response with an explicit status.
func (d *stubDoer) onStatus(method, match string, status int, body string) *stubDoer {
	key := method + " " + match
	d.routes[key] = append(d.routes[key], route{status: status, body: body})
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
		Prefer: req.Header.Get("Prefer"),
		Body:   bodyStr,
	})

	// Pick the LONGEST matching key with a remaining queued route.
	bestKey, bestMatch := "", ""
	for key, queued := range d.routes {
		if len(queued) == 0 {
			continue
		}
		parts := strings.SplitN(key, " ", 2)
		if parts[0] != req.Method {
			continue
		}
		match := parts[1]
		if !strings.Contains(req.URL.Path, match) && !strings.Contains(req.URL.String(), match) {
			continue
		}
		if len(match) > len(bestMatch) {
			bestKey, bestMatch = key, match
		}
	}
	if bestKey == "" {
		d.t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		return nil, fmt.Errorf("unreachable")
	}
	r := d.routes[bestKey][0]
	d.routes[bestKey] = d.routes[bestKey][1:]
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: r.status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewBufferString(r.body)),
		Request:    req,
	}, nil
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

// openSource opens a connector against the stub with the given settings merged
// over a complete online configuration.
func openSource(t *testing.T, d *stubDoer, overrides map[string]string) *Source {
	t.Helper()
	settings := map[string]string{
		"tenant_id":       "tenant-abc",
		"client_id":       "client-xyz",
		"client_secret":   testSecret,
		"oauth_token_url": testTokenURL,
	}
	for k, v := range overrides {
		if v == "" {
			delete(settings, k)
			continue
		}
		settings[k] = v
	}
	s := New()
	if d != nil {
		s.doer = d
	}
	s.now = func() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// stubOwnershipEmpty queues empty owners/sponsors pages for the given agent ids.
func stubOwnershipEmpty(d *stubDoer, ids ...string) {
	for _, id := range ids {
		d.on(http.MethodGet, "/servicePrincipals/"+id+"/microsoft.graph.agentIdentity/owners", `{"value":[]}`)
		d.on(http.MethodGet, "/servicePrincipals/"+id+"/microsoft.graph.agentIdentity/sponsors", `{"value":[]}`)
	}
}

func stubAgentUsersEmpty(d *stubDoer) {
	d.on(http.MethodGet, "/v1.0/users/microsoft.graph.agentUser", `{"value":[]}`)
}

// collectSink gathers emitted observations.
type collectSink struct{ obs []model.Observation }

func (c *collectSink) Emit(_ context.Context, o model.Observation) error {
	c.obs = append(c.obs, o)
	return nil
}

// ---------------------------------------------------------------------------
// Snapshot: fixture-driven mapping (agents, blueprints, principals, ownership,
// orphan computation, pagination follows).
// ---------------------------------------------------------------------------

func TestSnapshot(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", d.fixture("blueprints.json"))
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", d.fixture("agents.json"))
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", d.fixture("agents_page2.json"))
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal", d.fixture("blueprint_principals.json"))
	d.on(http.MethodGet, "/servicePrincipals/aid-1111/microsoft.graph.agentIdentity/owners", d.fixture("owners_aid1.json"))
	d.on(http.MethodGet, "/servicePrincipals/aid-1111/microsoft.graph.agentIdentity/sponsors", d.fixture("sponsors_aid1.json"))
	stubOwnershipEmpty(d, "aid-2222", "aid-3333")
	stubAgentUsersEmpty(d)

	s := openSource(t, d, nil)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceEntraAgent {
		t.Errorf("source = %q, want entra-agent", g.Source)
	}
	if !g.CapturedAt.Equal(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("CapturedAt = %v, want injected clock", g.CapturedAt)
	}

	// 3 agents (2 page1 + 1 page2, @odata.nextLink followed) + 1 blueprint principal.
	if got := len(g.Identities); got != 4 {
		t.Fatalf("identities = %d, want 4", got)
	}
	if !sawPath(d, "$skiptoken=PAGE2") {
		t.Error("connector did not follow the @odata.nextLink agents page")
	}

	invoice, ok := g.FindIdentity("aid-1111")
	if !ok {
		t.Fatal("aid-1111 missing")
	}
	if invoice.Type != identitysource.PrincipalNHI || invoice.Kind != identitysource.KindAgentIdentity {
		t.Errorf("agent type/kind = %q/%q, want nhi/%s", invoice.Type, invoice.Kind, identitysource.KindAgentIdentity)
	}
	if invoice.DisplayName != "Invoice Copilot" || invoice.Disabled {
		t.Errorf("aid-1111 displayName/disabled = %q/%v", invoice.DisplayName, invoice.Disabled)
	}
	wantAttrs := map[string]string{
		"blueprint_id":      "bp-app-1",
		"created_by_app_id": "creator-app-9",
		"created_at":        "2026-05-20T10:00:00Z",
		"owner_ref":         "user-owner-1", // FIRST member of @odata.type user (sp-x skipped)
		"sponsor_ref":       "user-sponsor-1",
	}
	for k, v := range wantAttrs {
		if invoice.Attributes[k] != v {
			t.Errorf("aid-1111 attr %q = %q, want %q", k, invoice.Attributes[k], v)
		}
	}
	if invoice.Attributes["orphaned"] != "" {
		t.Errorf("aid-1111 must not be orphaned (live blueprint): %v", invoice.Attributes)
	}
	if len(invoice.Attributes) != len(wantAttrs) {
		t.Errorf("aid-1111 attrs not pruned to present metadata: %v", invoice.Attributes)
	}

	// Orphan: blueprint bp-gone is not among the live blueprints' appIds.
	orphan, _ := g.FindIdentity("aid-2222")
	if orphan.Attributes["orphaned"] != "true" {
		t.Errorf("aid-2222 must be orphaned: %v", orphan.Attributes)
	}
	if orphan.Disabled {
		t.Error("aid-2222 (enabled, NotDisabled) must not be disabled")
	}

	// Microsoft-side disablement.
	banned, ok := g.FindIdentity("aid-3333")
	if !ok {
		t.Fatal("aid-3333 (page 2) missing — pagination did not append")
	}
	if !banned.Disabled {
		t.Error("aid-3333 (DisabledDueToViolationOfServicesAgreement) must be disabled")
	}

	// Blueprint principal: governed NHI, deliberately NOT KindAgentIdentity.
	bpp, ok := g.FindIdentity("bpp-1")
	if !ok {
		t.Fatal("bpp-1 missing")
	}
	if bpp.Kind != "blueprint_principal" || bpp.Kind == identitysource.KindAgentIdentity {
		t.Errorf("blueprint principal kind = %q", bpp.Kind)
	}
	if !bpp.Disabled {
		t.Error("bpp-1 (accountEnabled=false) must be disabled")
	}
	if bpp.Attributes["app_id"] != "bp-app-1" {
		t.Errorf("bpp-1 attrs = %v", bpp.Attributes)
	}

	// Blueprints are collections keyed by appId.
	if len(g.Collections) != 2 {
		t.Fatalf("collections = %+v", g.Collections)
	}
	for _, c := range g.Collections {
		if c.Kind != identitysource.KindGroup || c.Attributes["object"] != "agent_blueprint" {
			t.Errorf("bad collection: %+v", c)
		}
	}
	if g.Collections[0].Ref != "bp-app-1" || g.Collections[0].DisplayName != "Finance Blueprint" {
		t.Errorf("blueprint collection = %+v", g.Collections[0])
	}

	// Memberships: aid-1111→bp-app-1, aid-3333→bp-app-1, bpp-1→bp-app-1. The
	// orphan (blueprint gone) gets none.
	if len(g.Memberships) != 3 {
		t.Fatalf("memberships = %+v", g.Memberships)
	}
	members := map[string]string{}
	for _, m := range g.Memberships {
		if m.MemberKind != identitysource.MemberIdentity || m.Source != identitysource.SourceEntraAgent {
			t.Errorf("bad membership: %+v", m)
		}
		members[m.MemberRef] = m.CollectionRef
	}
	for _, ref := range []string{"aid-1111", "aid-3333", "bpp-1"} {
		if members[ref] != "bp-app-1" {
			t.Errorf("membership %s → %q, want bp-app-1", ref, members[ref])
		}
	}
	if _, ok := members["aid-2222"]; ok {
		t.Error("orphan aid-2222 must not have a blueprint membership")
	}

	assertNoSecret(t, g, testSecret, testAccessToken)
}

func TestSnapshotAgentUsers(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", `{"value":[]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", `{"value":[]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal", `{"value":[]}`)
	d.on(http.MethodGet, "/v1.0/users/microsoft.graph.agentUser", d.fixture("agent_users_page1.json"))
	d.on(http.MethodGet, "$skiptoken=AGENTUSER2", d.fixture("agent_users_page2.json"))

	s := openSource(t, d, map[string]string{"expand_ownership": "false"})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !sawPath(d, "$skiptoken=AGENTUSER2") {
		t.Error("connector did not follow the @odata.nextLink agentUser page")
	}
	if got := len(g.Identities); got != 2 {
		t.Fatalf("identities = %d, want 2 agentUser rows", got)
	}
	enabled, ok := g.FindIdentity("au-1111")
	if !ok {
		t.Fatal("au-1111 missing")
	}
	if enabled.Type != identitysource.PrincipalNHI || enabled.Kind != KindAgentUser {
		t.Errorf("agentUser type/kind = %q/%q, want nhi/%s", enabled.Type, enabled.Kind, KindAgentUser)
	}
	if enabled.Disabled {
		t.Error("enabled agentUser must not be disabled")
	}
	wantAttrs := map[string]string{
		"identity_parent_id": "aid-1111",
		"upn":                "invoice-agent@contoso.example",
		"created_at":         "2026-06-01T10:00:00Z",
		"object":             "agent_user",
	}
	for k, v := range wantAttrs {
		if enabled.Attributes[k] != v {
			t.Errorf("au-1111 attr %q = %q, want %q", k, enabled.Attributes[k], v)
		}
	}
	if len(enabled.Attributes) != len(wantAttrs) {
		t.Errorf("au-1111 attrs not pruned to present metadata: %v", enabled.Attributes)
	}

	disabled, ok := g.FindIdentity("au-2222")
	if !ok {
		t.Fatal("au-2222 missing")
	}
	if !disabled.Disabled {
		t.Error("accountEnabled=false agentUser must be disabled")
	}
	if disabled.Attributes["identity_parent_id"] != "aid-2222" {
		t.Errorf("au-2222 attrs = %v", disabled.Attributes)
	}
}

func TestSnapshotAgentUsers403Tolerated(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", `{"value":[{"id":"o1","appId":"bp-app-1","displayName":"BP"}]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", `{"value":[
		{"id":"a-1","displayName":"One","accountEnabled":true,"agentIdentityBlueprintId":"bp-app-1","servicePrincipalType":"ServiceIdentity"}
	]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal", `{"value":[]}`)
	d.onStatus(http.MethodGet, "/v1.0/users/microsoft.graph.agentUser", 403, `{"error":{"code":"Authorization_RequestDenied"}}`)

	s := openSource(t, d, map[string]string{"expand_ownership": "false"})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot must tolerate agentUser 403: %v", err)
	}
	if _, ok := g.FindIdentity("a-1"); !ok {
		t.Fatal("existing agentIdentity row missing after tolerated agentUser 403")
	}
	if _, ok := g.FindIdentity("au-1111"); ok {
		t.Error("agentUser rows must be skipped when the leg is denied")
	}
}

// TestOrphanComputation focuses the in-snapshot orphan diff: there is no
// orphan-list API, so an agent whose agentIdentityBlueprintId is not among the
// live blueprints' appIds is marked — and one whose blueprint is live is not.
func TestOrphanComputation(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", `{"value":[{"id":"o1","appId":"bp-live","displayName":"Live"}]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", `{"value":[
		{"id":"a-live","displayName":"Lives","accountEnabled":true,"agentIdentityBlueprintId":"bp-live","servicePrincipalType":"ServiceIdentity"},
		{"id":"a-orphan","displayName":"Orphan","accountEnabled":true,"agentIdentityBlueprintId":"bp-deleted","servicePrincipalType":"ServiceIdentity"}
	]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal", `{"value":[]}`)
	stubAgentUsersEmpty(d)

	s := openSource(t, d, map[string]string{"expand_ownership": "false"})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	live, _ := g.FindIdentity("a-live")
	if live.Attributes["orphaned"] != "" {
		t.Errorf("a-live must not be orphaned: %v", live.Attributes)
	}
	orphan, _ := g.FindIdentity("a-orphan")
	if orphan.Attributes["orphaned"] != "true" {
		t.Errorf("a-orphan must be orphaned: %v", orphan.Attributes)
	}
}

// TestDisabledMapping is the table over accountEnabled × disabledByMicrosoftStatus.
func TestDisabledMapping(t *testing.T) {
	cases := []struct {
		name     string
		enabled  bool
		status   string
		disabled bool
	}{
		{"enabled, status null", true, "", false},
		{"enabled, NotDisabled", true, notDisabled, false},
		{"enabled, microsoft-disabled", true, "DisabledDueToViolationOfServicesAgreement", true},
		{"disabled, status null", false, "", true},
		{"disabled, NotDisabled", false, notDisabled, true},
	}
	s := &Source{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := s.agentRow(agentIdentity{
				ID:                        "a-1",
				AccountEnabled:            tc.enabled,
				AgentIdentityBlueprintID:  "bp",
				DisabledByMicrosoftStatus: tc.status,
			}, map[string]bool{"bp": true}, "", "", false)
			if row.Disabled != tc.disabled {
				t.Errorf("Disabled = %v, want %v", row.Disabled, tc.disabled)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Ownership expansion: per-identity 403/404 tolerated, never a snapshot failure.
// ---------------------------------------------------------------------------

func TestSponsors403Tolerated(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", `{"value":[{"id":"o1","appId":"bp-app-1","displayName":"BP"}]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", `{"value":[
		{"id":"a-1","displayName":"One","accountEnabled":true,"agentIdentityBlueprintId":"bp-app-1","servicePrincipalType":"ServiceIdentity"},
		{"id":"a-2","displayName":"Two","accountEnabled":true,"agentIdentityBlueprintId":"bp-app-1","servicePrincipalType":"ServiceIdentity"}
	]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal", `{"value":[]}`)
	stubAgentUsersEmpty(d)
	// a-1: owners resolve; sponsors 403 (the documented app-only least-priv for
	// the sponsors list is AgentIdentity.ReadWrite.All — a read-only registration
	// may be denied). a-2: owners 404, sponsors resolve.
	d.on(http.MethodGet, "/servicePrincipals/a-1/microsoft.graph.agentIdentity/owners", `{"value":[{"@odata.type":"#microsoft.graph.user","id":"user-1"}]}`)
	d.onStatus(http.MethodGet, "/servicePrincipals/a-1/microsoft.graph.agentIdentity/sponsors", 403, `{"error":{"code":"Authorization_RequestDenied"}}`)
	d.onStatus(http.MethodGet, "/servicePrincipals/a-2/microsoft.graph.agentIdentity/owners", 404, `{"error":{"code":"Request_ResourceNotFound"}}`)
	d.on(http.MethodGet, "/servicePrincipals/a-2/microsoft.graph.agentIdentity/sponsors", `{"value":[{"@odata.type":"#microsoft.graph.user","id":"user-2"}]}`)

	s := openSource(t, d, nil)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot must tolerate per-identity 403/404: %v", err)
	}
	a1, _ := g.FindIdentity("a-1")
	if a1.Attributes["owner_ref"] != "user-1" {
		t.Errorf("a-1 owner_ref = %q, want user-1", a1.Attributes["owner_ref"])
	}
	if _, ok := a1.Attributes["sponsor_ref"]; ok {
		t.Errorf("a-1 sponsor_ref must be absent on 403: %v", a1.Attributes)
	}
	a2, _ := g.FindIdentity("a-2")
	if _, ok := a2.Attributes["owner_ref"]; ok {
		t.Errorf("a-2 owner_ref must be absent on 404: %v", a2.Attributes)
	}
	if a2.Attributes["sponsor_ref"] != "user-2" {
		t.Errorf("a-2 sponsor_ref = %q, want user-2", a2.Attributes["sponsor_ref"])
	}
}

func TestOwnershipFollowsNextLink(t *testing.T) {
	// The first owners page can hold only non-user members (service principals,
	// groups); the user owner on page 2 must still resolve via @odata.nextLink.
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", `{"value":[{"id":"o1","appId":"bp-app-1","displayName":"BP"}]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", `{"value":[
		{"id":"a-1","displayName":"One","accountEnabled":true,"agentIdentityBlueprintId":"bp-app-1","servicePrincipalType":"ServiceIdentity"}
	]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal", `{"value":[]}`)
	stubAgentUsersEmpty(d)
	// The nextLink is same-origin with the configured Graph base: httpx
	// refuses a cross-origin pagination link, exactly as it would in production.
	d.on(http.MethodGet, "/servicePrincipals/a-1/microsoft.graph.agentIdentity/owners",
		`{"value":[{"@odata.type":"#microsoft.graph.servicePrincipal","id":"sp-only"}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/servicePrincipals/a-1/microsoft.graph.agentIdentity/owners?$skiptoken=OWNERS2"}`)
	d.on(http.MethodGet, "$skiptoken=OWNERS2", `{"value":[{"@odata.type":"#microsoft.graph.user","id":"user-page2"}]}`)
	d.on(http.MethodGet, "/servicePrincipals/a-1/microsoft.graph.agentIdentity/sponsors", `{"value":[]}`)

	s := openSource(t, d, nil)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	a1, _ := g.FindIdentity("a-1")
	if a1.Attributes["owner_ref"] != "user-page2" {
		t.Errorf("owner_ref = %q, want the page-2 user (nextLink not followed)", a1.Attributes["owner_ref"])
	}
}

// ---------------------------------------------------------------------------
// Soft-deleted (include_deleted): client-side ServiceIdentity filter.
// ---------------------------------------------------------------------------

func TestIncludeDeletedFilter(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", d.fixture("blueprints.json"))
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", `{"value":[]}`)
	d.on(http.MethodGet, "/v1.0/directory/deletedItems/microsoft.graph.servicePrincipal", d.fixture("deleted_items.json"))
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal", `{"value":[]}`)
	stubAgentUsersEmpty(d)

	s := openSource(t, d, map[string]string{"include_deleted": "true", "expand_ownership": "false"})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// The non-ServiceIdentity deleted row (sp-other, "Application") is dropped.
	if got := len(g.Identities); got != 1 {
		t.Fatalf("identities = %d, want 1 (client-side ServiceIdentity filter)", got)
	}
	if _, ok := g.FindIdentity("sp-other"); ok {
		t.Error("non-ServiceIdentity deleted row must be filtered out")
	}
	del, ok := g.FindIdentity("aid-9999")
	if !ok {
		t.Fatal("deleted agent aid-9999 missing")
	}
	if !del.Disabled {
		t.Error("soft-deleted agent must be Disabled")
	}
	if del.Attributes["soft_deleted"] != "true" || del.Attributes["deleted_at"] != "2026-06-01T08:00:00Z" {
		t.Errorf("deleted agent attrs = %v", del.Attributes)
	}
	// Its blueprint was permanently deleted with it: orphaned per the inventory.
	if del.Attributes["orphaned"] != "true" {
		t.Errorf("deleted agent of a gone blueprint must be orphaned: %v", del.Attributes)
	}
	if del.Kind != identitysource.KindAgentIdentity {
		t.Errorf("deleted agent kind = %q", del.Kind)
	}
}

func TestDeletedItemsNotFetchedByDefault(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", `{"value":[]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", `{"value":[]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal", `{"value":[]}`)
	stubAgentUsersEmpty(d)

	s := openSource(t, d, nil)
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if sawPath(d, "deletedItems") {
		t.Error("deletedItems must not be read unless include_deleted=true (needs Application.Read.All)")
	}
}

// ---------------------------------------------------------------------------
// Pagination follows AND stops.
// ---------------------------------------------------------------------------

func TestPaginationStopsWhenNextLinkAbsent(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", `{"value":[]}`)
	// Page 1 has NO @odata.nextLink; the queued phantom page 2 must stay unread.
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", `{"value":[
		{"id":"a-only","displayName":"Only","accountEnabled":true,"agentIdentityBlueprintId":"","servicePrincipalType":"ServiceIdentity"}
	]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", d.fixture("agents_page2.json"))
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal", `{"value":[]}`)
	stubAgentUsersEmpty(d)

	s := openSource(t, d, map[string]string{"expand_ownership": "false"})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := len(g.Identities); got != 1 {
		t.Fatalf("identities = %d, want 1 (no phantom second page)", got)
	}
	if _, ok := g.FindIdentity("aid-3333"); ok {
		t.Error("page-2 row must NOT appear — page 2 was never linked")
	}
}

func TestPaginationBoundedByMaxPages(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", `{"value":[]}`)
	// Every agents page links to a next one; max_pages=2 must stop after two.
	loop := `{"@odata.nextLink":"https://graph.microsoft.com/v1.0/servicePrincipals/microsoft.graph.agentIdentity?$skiptoken=MORE","value":[]}`
	for i := 0; i < 5; i++ {
		d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", loop)
	}
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal", `{"value":[]}`)
	stubAgentUsersEmpty(d)

	s := openSource(t, d, map[string]string{"max_pages": "2", "expand_ownership": "false"})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	agentPages := 0
	for _, c := range d.calls {
		if strings.Contains(c.URL, "servicePrincipals/microsoft.graph.agentIdentity") &&
			!strings.Contains(c.URL, "BlueprintPrincipal") {
			agentPages++
		}
	}
	if agentPages != 2 {
		t.Errorf("agents pages fetched = %d, want exactly max_pages=2", agentPages)
	}
}

// ---------------------------------------------------------------------------
// Auth: token POST shape, Bearer scheme on every GET, offline sends nothing.
// ---------------------------------------------------------------------------

func TestTokenPostAndBearerAuth(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", `{"value":[]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", `{"value":[]}`)
	d.on(http.MethodGet, "/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal", `{"value":[]}`)
	stubAgentUsersEmpty(d)

	s := openSource(t, d, nil)
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if d.calls[0].Method != http.MethodPost || d.calls[0].URL != testTokenURL {
		t.Fatalf("first call = %s %s, want POST %s", d.calls[0].Method, d.calls[0].URL, testTokenURL)
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

	graphCalls := 0
	for _, c := range d.calls {
		if c.Method != http.MethodGet {
			continue
		}
		graphCalls++
		if c.Auth != "Bearer "+testAccessToken {
			t.Errorf("Graph auth = %q, want Bearer <token>", c.Auth)
		}
		if strings.Contains(c.URL, testSecret) {
			t.Errorf("client secret on a URL: %s", c.URL)
		}
	}
	if graphCalls == 0 {
		t.Fatal("no Graph GET calls recorded")
	}
}

func TestOfflineEmptyGraphAndNoCalls(t *testing.T) {
	for _, missing := range []string{"tenant_id", "client_id", "client_secret"} {
		t.Run("missing_"+missing, func(t *testing.T) {
			d := newStub(t) // any request would t.Fatal (no routes queued)
			s := openSource(t, d, map[string]string{missing: ""})
			g, err := s.Snapshot(context.Background())
			if err != nil {
				t.Fatalf("offline Snapshot must not error: %v", err)
			}
			if g.Source != identitysource.SourceEntraAgent {
				t.Errorf("offline source = %q", g.Source)
			}
			if g.CapturedAt.IsZero() {
				t.Error("offline graph must still carry CapturedAt")
			}
			if len(g.Identities) != 0 || len(g.Collections) != 0 || len(g.Memberships) != 0 {
				t.Errorf("offline graph must be empty: %+v", g)
			}
			sink := &collectSink{}
			if err := s.Gather(context.Background(), sink); err != nil {
				t.Fatalf("offline Gather must return nil: %v", err)
			}
			if len(sink.obs) != 0 {
				t.Errorf("offline Gather emitted %d observations", len(sink.obs))
			}
			if len(d.calls) != 0 {
				t.Errorf("offline connector made %d HTTP calls", len(d.calls))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Errors carry the status, never the credential.
// ---------------------------------------------------------------------------

func TestTokenErrorCarriesStatusNotSecret(t *testing.T) {
	d := newStub(t)
	d.onStatus(http.MethodPost, testTokenURL, 401, `{"error":"invalid_client","error_description":"bad secret"}`)
	s := openSource(t, d, nil)
	_, err := s.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected token error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should carry status: %v", err)
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Errorf("token error must not leak the client secret: %v", err)
	}
}

func TestAPIErrorCarriesStatusNotSecret(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.onStatus(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", 500, `{"error":{"code":"InternalServerError"}}`)
	s := openSource(t, d, nil)
	_, err := s.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected API error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should carry status: %v", err)
	}
	if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), testAccessToken) {
		t.Errorf("API error must not carry a credential: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Gather: nhi_longlived_credential drift over blueprint passwordCredentials.
// ---------------------------------------------------------------------------

func TestGatherDriftFindings(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/applications/microsoft.graph.agentIdentityBlueprint", d.fixture("drift_blueprints.json"))

	s := openSource(t, d, map[string]string{
		"ca_posture":         "false",
		"risk_posture":       "false",
		"governance_posture": "false",
	})
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// bp-app-1 (2 expiring secrets) => medium; bp-app-2 (1 secret without
	// endDateTime) => high; bp-app-3 (certificates only) => NO finding —
	// cert-based auth is the recommended replacement, never flagged.
	if got := len(sink.obs); got != 2 {
		t.Fatalf("findings = %d, want 2", got)
	}
	byRef := map[string]model.FindingReport{}
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			t.Fatalf("observation %T, want FindingReport", o)
		}
		byRef[f.SubjectRef] = f
	}

	medium := byRef["bp-app-1"]
	if medium.Kind != identitysource.FindingLongLivedCredential {
		t.Errorf("kind = %q", medium.Kind)
	}
	if medium.Severity != model.SeverityMedium {
		t.Errorf("bp-app-1 severity = %q, want medium (all secrets expire)", medium.Severity)
	}
	if medium.SubjectKind != "identity" {
		t.Errorf("subject kind = %q", medium.SubjectKind)
	}
	if medium.Title != "agent blueprint holds 2 static client secret(s)" {
		t.Errorf("title = %q", medium.Title)
	}
	wantHash := redact.Hash("nhi_longlived_credential|entra-agent|bp-app-1|password_credentials=2")
	if medium.DetailHash != wantHash {
		t.Errorf("DetailHash = %q, want %q", medium.DetailHash, wantHash)
	}
	if !medium.OccurredAt.Equal(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("OccurredAt = %v, want injected clock", medium.OccurredAt)
	}

	high := byRef["bp-app-2"]
	if high.Severity != model.SeverityHigh {
		t.Errorf("bp-app-2 severity = %q, want high (a secret without endDateTime never expires)", high.Severity)
	}
	if high.Title != "agent blueprint holds 1 static client secret(s)" {
		t.Errorf("title = %q", high.Title)
	}

	if _, ok := byRef["bp-app-3"]; ok {
		t.Error("certificate-only blueprint must not be flagged")
	}

	// No emitted string carries a credential or credential metadata the fixture
	// planted (hints, keyIds): the finding is count + expiry presence only.
	for _, f := range byRef {
		for _, field := range []string{f.Kind, f.SubjectKind, f.SubjectRef, f.Title, f.DetailHash} {
			for _, leak := range []string{testSecret, testAccessToken, "Qx3", "Zp9", "Aa1", "k1", "k2", "k3"} {
				if strings.Contains(field, leak) {
					t.Errorf("finding field %q leaks %q", field, leak)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Security invariant: no secret/token ever appears in any emitted Graph field.
// ---------------------------------------------------------------------------

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
// Descriptor + defaults.
// ---------------------------------------------------------------------------

func TestDescriptorShape(t *testing.T) {
	desc := New().Descriptor()
	if desc.Name != Name || desc.Type != sdk.TypeSource || desc.APIVersion != sdk.APIVersion || desc.Version != "0.2.0" {
		t.Errorf("descriptor header wrong: %+v", desc)
	}
	secret := map[string]bool{}
	keys := map[string]bool{}
	for _, f := range desc.ConfigFields {
		secret[f.Key] = f.Secret
		keys[f.Key] = true
	}
	if !secret["client_secret"] {
		t.Error("client_secret must be Secret")
	}
	for _, k := range []string{"tenant_id", "client_id", "base_url", "oauth_token_url", "max_pages", "include_deleted", "expand_ownership", "ca_posture", "risk_posture", "governance_posture", "ingest_signins", "signin_filter", "signin_lookback", "timeout"} {
		if !keys[k] {
			t.Errorf("config field %q missing", k)
		}
	}
}

func TestOpenDefaults(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open with empty config must not fail (offline, not malformed): %v", err)
	}
	if s.baseURL != defaultBaseURL {
		t.Errorf("base_url = %q, want %q", s.baseURL, defaultBaseURL)
	}
	if s.maxPages != defaultMaxPages || s.includeDeleted || !s.expandOwners || !s.caPosture || !s.riskPosture || !s.govPosture || s.ingestSignIns || s.signInFilter != defaultSignInFilter || s.signInLookback != defaultSignInLookback || s.timeout != defaultTimeout {
		t.Errorf("defaults wrong: maxPages=%d includeDeleted=%v expandOwners=%v caPosture=%v riskPosture=%v govPosture=%v ingestSignIns=%v signInFilter=%q signInLookback=%v timeout=%v",
			s.maxPages, s.includeDeleted, s.expandOwners, s.caPosture, s.riskPosture, s.govPosture, s.ingestSignIns, s.signInFilter, s.signInLookback, s.timeout)
	}
	if !s.offline() {
		t.Error("unconfigured connector must be offline")
	}
}
