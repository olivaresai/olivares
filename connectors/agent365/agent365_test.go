// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agent365

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

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// recordedRequest captures what the connector sent so a test can assert the
// auth header, method, URL and that no secret was placed on the wire.
type recordedRequest struct {
	Method string
	URL    string
	Auth   string
	Body   string
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

// on queues a 200 JSON response for a method + match key. match is compared as
// a substring of the request URL path (or the full URL).
func (d *stubDoer) on(method, match, body string) *stubDoer {
	key := method + " " + match
	d.routes[key] = append(d.routes[key], route{status: 200, body: body})
	return d
}

func (d *stubDoer) onStatus(method, match string, status int, body string) *stubDoer {
	key := method + " " + match
	d.routes[key] = append(d.routes[key], route{status: status, body: body})
	return d
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			d.t.Fatalf("read request body: %v", err)
		}
		body = string(b)
	}
	d.calls = append(d.calls, recordedRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Auth:   req.Header.Get("Authorization"),
		Body:   body,
	})
	bestKey := ""
	bestMatchLen := -1
	for key, queued := range d.routes {
		parts := strings.SplitN(key, " ", 2)
		if parts[0] != req.Method || len(queued) == 0 {
			continue
		}
		if strings.Contains(req.URL.Path, parts[1]) || strings.Contains(req.URL.String(), parts[1]) {
			if len(parts[1]) > bestMatchLen {
				bestKey = key
				bestMatchLen = len(parts[1])
			}
		}
	}
	if bestKey != "" {
		queued := d.routes[bestKey]
		r := queued[0]
		d.routes[bestKey] = queued[1:]
		h := http.Header{}
		h.Set("Content-Type", "application/json")
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

const testToken = "eyJdelegated.supersecret.token"

const (
	testTokenURL     = "https://login.test.local/tenant/oauth2/v2.0/token"
	testClientSecret = "client-secret-supersecret"
	mintedToken      = "minted-agent365-token"
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

func TestSnapshotMapsPackages(t *testing.T) {
	d := newStub(t)
	// Page 1 carries an @odata.nextLink; page 2 does not. Both are queued under
	// the same path key and consumed in order, so the second GET (the absolute
	// nextLink URL) receives page 2.
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", d.fixture("packages_page1.json"))
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", d.fixture("packages_page2.json"))

	fixed := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	s := openSource(t, d, map[string]string{"access_token": testToken})
	s.now = func() time.Time { return fixed }

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceAgent365 {
		t.Errorf("source = %q, want agent365", g.Source)
	}
	if !g.CapturedAt.Equal(fixed) {
		t.Errorf("CapturedAt = %v, want %v", g.CapturedAt, fixed)
	}
	if !sawPath(d, "$skiptoken=PAGE2") {
		t.Error("connector did not follow @odata.nextLink to page 2")
	}

	// 2 packages on page 1 + 1 on page 2 (the id-less row is skipped) = 3.
	if got := len(g.Identities); got != 3 {
		t.Fatalf("identities = %d, want 3 (id-less row skipped)", got)
	}
	if len(g.Collections) != 0 || len(g.Memberships) != 0 {
		t.Errorf("registry packages carry no collections/memberships: %d/%d",
			len(g.Collections), len(g.Memberships))
	}

	sales, ok := g.FindIdentity("P_19ae1b6cf2a04a5d8d9f1b3c4e5f6a7b")
	if !ok {
		t.Fatal("Sales Research Agent missing")
	}
	if sales.Type != identitysource.PrincipalNHI || sales.Kind != "registered_agent" {
		t.Errorf("type/kind = %q/%q, want nhi/registered_agent", sales.Type, sales.Kind)
	}
	if sales.DisplayName != "Sales Research Agent" {
		t.Errorf("displayName = %q", sales.DisplayName)
	}
	if sales.Disabled {
		t.Error("unblocked package must not be Disabled")
	}
	want := map[string]string{
		"type":              "custom",
		"publisher":         "Contoso IT",
		"version":           "1.4.0",
		"app_id":            "aaaa1111-2222-3333-4444-555566667777",
		"manifest_id":       "manifest-sales-research",
		"asset_id":          "asset-sales-research",
		"short_description": "Researches sales accounts",
		"available_to":      "all",
		"deployed_to":       "some",
		"element_types":     "DeclarativeAgent",
		"last_modified":     "2026-05-20T11:04:00Z",
	}
	for k, v := range want {
		if sales.Attributes[k] != v {
			t.Errorf("attribute %q = %q, want %q", k, sales.Attributes[k], v)
		}
	}
	if len(sales.Attributes) != len(want) {
		t.Errorf("attributes = %v, want exactly %v", sales.Attributes, want)
	}

	// isBlocked => Disabled; multi-valued elementTypes are comma-joined.
	helpdesk, ok := g.FindIdentity("P_2b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e")
	if !ok {
		t.Fatal("Legacy Helpdesk Bot missing")
	}
	if !helpdesk.Disabled {
		t.Error("isBlocked package must be Disabled")
	}
	if helpdesk.Attributes["element_types"] != "Bots,CustomEngineAgent" {
		t.Errorf("element_types = %q", helpdesk.Attributes["element_types"])
	}

	// Page-2 row with only id/displayName/type/isBlocked: attributes pruned to
	// the non-empty ones only.
	bare, ok := g.FindIdentity("P_3c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f")
	if !ok {
		t.Fatal("page-2 package missing — @odata.nextLink not followed/merged")
	}
	if got := len(bare.Attributes); got != 1 || bare.Attributes["type"] != "custom" {
		t.Errorf("bare attributes = %v, want only {type:custom}", bare.Attributes)
	}
}

// TestPaginationStopsWhenNextLinkAbsent proves the connector terminates after a
// page with no @odata.nextLink (pagination is undocumented on this v1.0
// endpoint, so absence is the common case): exactly one GET, no phantom page.
func TestPaginationStopsWhenNextLinkAbsent(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", d.fixture("packages_page2.json"))

	s := openSource(t, d, map[string]string{"access_token": testToken})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(d.calls) != 1 {
		t.Fatalf("calls = %d, want exactly 1 (no nextLink => no second page)", len(d.calls))
	}
	if sawPath(d, "skiptoken") {
		t.Error("connector fetched a phantom second page")
	}
	if got := len(g.Identities); got != 1 {
		t.Fatalf("identities = %d, want 1", got)
	}
}

func TestBearerAuthHeader(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", `{"value":[]}`)

	s := openSource(t, d, map[string]string{"access_token": testToken})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(d.calls) == 0 {
		t.Fatal("no calls recorded")
	}
	for _, c := range d.calls {
		if c.Auth != "Bearer "+testToken {
			t.Errorf("auth header = %q, want exactly Bearer <token>", c.Auth)
		}
	}
	if !sawPath(d, "/v1.0/copilot/admin/catalog/packages") {
		t.Error("connector did not call the Graph v1.0 package path")
	}
	if sawPath(d, "/beta/copilot/admin/catalog/packages") {
		t.Error("connector must not call the retired beta package path")
	}
}

func TestClientCredentialsTokenPOST(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", `{"value":[]}`)

	s := openSource(t, d, map[string]string{
		"tenant_id":       "tenant-1",
		"client_id":       "client-1",
		"client_secret":   testClientSecret,
		"oauth_token_url": testTokenURL,
	})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := len(d.calls); got != 2 {
		t.Fatalf("calls = %d, want token POST + package GET", got)
	}
	post := d.calls[0]
	if post.Method != http.MethodPost || post.URL != testTokenURL {
		t.Fatalf("first call = %s %s, want POST %s", post.Method, post.URL, testTokenURL)
	}
	form, err := url.ParseQuery(post.Body)
	if err != nil {
		t.Fatalf("parse token form: %v", err)
	}
	wantForm := map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     "client-1",
		"client_secret": testClientSecret,
		"scope":         "https://graph.microsoft.com/.default",
	}
	for k, want := range wantForm {
		if got := form.Get(k); got != want {
			t.Errorf("token form %s = %q, want %q", k, got, want)
		}
	}
	get := d.calls[1]
	if get.Method != http.MethodGet {
		t.Fatalf("second call method = %s, want GET", get.Method)
	}
	if get.Auth != "Bearer "+mintedToken {
		t.Errorf("GET auth = %q, want minted bearer", get.Auth)
	}
	if strings.Contains(get.URL+get.Auth+get.Body, testClientSecret) {
		t.Error("client_secret leaked onto a Graph GET")
	}
}

func TestAccessTokenPrecedenceOverClientCredentials(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", `{"value":[]}`)

	s := openSource(t, d, map[string]string{
		"tenant_id":       "tenant-1",
		"client_id":       "client-1",
		"client_secret":   testClientSecret,
		"oauth_token_url": testTokenURL,
		"access_token":    testToken,
	})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, c := range d.calls {
		if c.Method == http.MethodPost {
			t.Fatalf("access_token must take precedence; unexpected token POST to %s", c.URL)
		}
		if c.Auth != "Bearer "+testToken {
			t.Errorf("auth header = %q, want delegated token", c.Auth)
		}
	}
}

func TestTokenErrorRedactsClientSecret(t *testing.T) {
	d := newStub(t)
	d.onStatus(http.MethodPost, testTokenURL, http.StatusBadRequest, `{"error":"bad client_secret=`+testClientSecret+`"}`)

	s := openSource(t, d, map[string]string{
		"tenant_id":       "tenant-1",
		"client_id":       "client-1",
		"client_secret":   testClientSecret,
		"oauth_token_url": testTokenURL,
	})
	_, err := s.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected token error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should carry status: %v", err)
	}
	if strings.Contains(err.Error(), testClientSecret) {
		t.Fatalf("token error leaked client_secret: %v", err)
	}
}

func TestAPIErrorCarriesStatusNotToken(t *testing.T) {
	d := newStub(t)
	d.routes["GET /v1.0/copilot/admin/catalog/packages"] = []route{
		{status: 403, body: `{"error":{"code":"Forbidden","message":"Insufficient privileges (delegated CopilotPackages.Read.All required)"}}`},
	}
	s := openSource(t, d, map[string]string{"access_token": testToken})
	_, err := s.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should carry status: %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error must not contain the token: %v", err)
	}
}

func TestOfflineEmptyGraph(t *testing.T) {
	s := New() // no doer: the wire must not be touched
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open without credential must not fail: %v", err)
	}
	fixed := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("offline Snapshot should not error: %v", err)
	}
	if g.Source != identitysource.SourceAgent365 {
		t.Errorf("offline source = %q", g.Source)
	}
	if !g.CapturedAt.Equal(fixed) {
		t.Errorf("offline CapturedAt = %v, want set", g.CapturedAt)
	}
	if len(g.Identities) != 0 || len(g.Collections) != 0 || len(g.Memberships) != 0 {
		t.Errorf("offline graph must be empty: %+v", g)
	}
	if err := s.Gather(context.Background(), sinkFunc(func() error {
		t.Fatal("offline Gather must not emit observations")
		return nil
	})); err != nil {
		t.Fatalf("offline Gather should not error: %v", err)
	}
}

func TestNoSecretLeaksIntoGraph(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", d.fixture("packages_page1.json"))
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", d.fixture("packages_page2.json"))
	s := openSource(t, d, map[string]string{"access_token": testToken})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	assertNoSecret(t, g, testToken)
}

func TestExpandDetailsOffByDefault(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", d.fixture("packages_details.json"))

	s := openSource(t, d, map[string]string{"access_token": testToken})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := len(d.calls); got != 1 {
		t.Fatalf("calls = %d, want only the list GET when expand_details=false", got)
	}
	detail, ok := g.FindIdentity("P_detail")
	if !ok {
		t.Fatal("P_detail missing")
	}
	if _, ok := detail.Attributes["sensitivity"]; ok {
		t.Errorf("detail attributes should be absent by default: %v", detail.Attributes)
	}
}

func TestExpandDetailsEnrichesTolerates404AndSkipsDefinitions(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", d.fixture("packages_details.json"))
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages/P_detail", d.fixture("package_detail.json"))
	d.onStatus(http.MethodGet, "/v1.0/copilot/admin/catalog/packages/P_missing", http.StatusNotFound, `{"error":{"code":"NotFound"}}`)

	s := openSource(t, d, map[string]string{"access_token": testToken, "expand_details": "true"})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !sawPath(d, "/v1.0/copilot/admin/catalog/packages/P_detail") {
		t.Fatal("detail GET for P_detail was not issued")
	}
	if !sawPath(d, "/v1.0/copilot/admin/catalog/packages/P_missing") {
		t.Fatal("detail GET for P_missing was not issued")
	}

	detail, ok := g.FindIdentity("P_detail")
	if !ok {
		t.Fatal("P_detail missing")
	}
	want := map[string]string{
		"sensitivity":              "confidential",
		"categories":               "Sales,Productivity",
		"allowed_users_and_groups": "2",
		"acquire_users_and_groups": "1",
		"element_types":            "DeclarativeAgent,Bots",
		"asset_id":                 "asset-detail",
		"short_description":        "Needs detail expansion",
		"available_to":             "some",
		"deployed_to":              "none",
	}
	for k, v := range want {
		if detail.Attributes[k] != v {
			t.Errorf("attribute %q = %q, want %q", k, detail.Attributes[k], v)
		}
	}

	missing, ok := g.FindIdentity("P_missing")
	if !ok {
		t.Fatal("P_missing missing")
	}
	if _, ok := missing.Attributes["sensitivity"]; ok {
		t.Errorf("404 detail should not enrich P_missing: %v", missing.Attributes)
	}
	assertNoSecret(t, g, "SECRET-INSTRUCTIONS")
}

// assertNoSecret walks every string field of the Graph and fails if any secret
// appears, proving the connector carries only registry metadata.
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
	if desc.Name != Name || desc.Type != sdk.TypeSource || desc.APIVersion != sdk.APIVersion || desc.Version != "0.2.0" {
		t.Errorf("descriptor header wrong: %+v", desc)
	}
	secret := map[string]bool{}
	for _, f := range desc.ConfigFields {
		secret[f.Key] = f.Secret
	}
	if !secret["access_token"] {
		t.Error("access_token must be Secret")
	}
	if !secret["client_secret"] {
		t.Error("client_secret must be Secret")
	}
}

func TestOpenDefaults(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"max_pages": "-3"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.baseURL != defaultBaseURL {
		t.Errorf("base_url = %q, want default %q", s.baseURL, defaultBaseURL)
	}
	if s.maxPages != defaultMaxPages {
		t.Errorf("max_pages = %d, want default %d (non-positive rejected)", s.maxPages, defaultMaxPages)
	}
}

func TestGatherPostureFindings(t *testing.T) {
	fixed := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	findings := runPostureGather(t, fixed)
	if got := len(findings); got != 2 {
		t.Fatalf("findings = %d, want 2: %+v", got, findings)
	}

	first := findings[0]
	if first.Kind != findingBlockedDeployed || first.Severity != model.SeverityMedium {
		t.Fatalf("first finding kind/severity = %s/%s", first.Kind, first.Severity)
	}
	if first.SubjectKind != "identity" || first.SubjectRef != "P_blocked_deployed" {
		t.Errorf("first subject = %s/%s", first.SubjectKind, first.SubjectRef)
	}
	if first.Title != "blocked package still deployed: Blocked Deployed Agent" {
		t.Errorf("first title = %q", first.Title)
	}
	if want := redact.Hash("agent365_blocked_deployed|agent365|P_blocked_deployed|some"); first.DetailHash != want {
		t.Errorf("first DetailHash = %q, want %q", first.DetailHash, want)
	}
	if !first.OccurredAt.Equal(fixed) {
		t.Errorf("first OccurredAt = %v, want %v", first.OccurredAt, fixed)
	}

	second := findings[1]
	if second.Kind != findingExternalBroadDeployment || second.Severity != model.SeverityLow {
		t.Fatalf("second finding kind/severity = %s/%s", second.Kind, second.Severity)
	}
	if second.SubjectKind != "identity" || second.SubjectRef != "P_external_all" {
		t.Errorf("second subject = %s/%s", second.SubjectKind, second.SubjectRef)
	}
	if second.Title != "external package deployed to all users: External Everywhere Agent" {
		t.Errorf("second title = %q", second.Title)
	}
	if want := redact.Hash("agent365_external_broad_deployment|agent365|P_external_all"); second.DetailHash != want {
		t.Errorf("second DetailHash = %q, want %q", second.DetailHash, want)
	}
}

func TestGatherPostureDeterministic(t *testing.T) {
	fixed := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	a := summarizeFindings(runPostureGather(t, fixed))
	b := summarizeFindings(runPostureGather(t, fixed))
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("finding order/content changed between runs:\n%v\n%v", a, b)
	}
}

func runPostureGather(t *testing.T, fixed time.Time) []model.FindingReport {
	t.Helper()
	d := newStub(t)
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", d.fixture("posture_packages.json"))
	s := openSource(t, d, map[string]string{"access_token": testToken})
	s.now = func() time.Time { return fixed }
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.findings(t)
}

type findingSummary struct {
	Kind       string
	Severity   model.Severity
	SubjectRef string
	Title      string
	DetailHash string
}

func summarizeFindings(findings []model.FindingReport) []findingSummary {
	out := make([]findingSummary, 0, len(findings))
	for _, f := range findings {
		out = append(out, findingSummary{
			Kind:       f.Kind,
			Severity:   f.Severity,
			SubjectRef: f.SubjectRef,
			Title:      f.Title,
			DetailHash: f.DetailHash,
		})
	}
	return out
}

type collectSink struct {
	obs []model.Observation
}

func (s *collectSink) Emit(_ context.Context, obs model.Observation) error {
	s.obs = append(s.obs, obs)
	return nil
}

func (s *collectSink) findings(t *testing.T) []model.FindingReport {
	t.Helper()
	out := make([]model.FindingReport, 0, len(s.obs))
	for _, obs := range s.obs {
		f, ok := obs.(model.FindingReport)
		if !ok {
			t.Fatalf("observation %T, want FindingReport", obs)
		}
		out = append(out, f)
	}
	return out
}

// sinkFunc adapts a func to sdk.Sink for no-emit assertions.
type sinkFunc func() error

func (f sinkFunc) Emit(context.Context, model.Observation) error { return f() }

// TestPaginationBoundedByMaxPages: a server echoing @odata.nextLink forever is
// stopped by max_pages — the third queued page stays unconsumed (the stub
// fatals on any unexpected request, so an unbounded regression fails fast).
func TestPaginationBoundedByMaxPages(t *testing.T) {
	d := newStub(t)
	page := `{"value":[],"@odata.nextLink":"https://graph.test.local/v1.0/copilot/admin/catalog/packages?$skiptoken=MORE"}`
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", page)
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", page)
	d.on(http.MethodGet, "/v1.0/copilot/admin/catalog/packages", page)
	s := openSource(t, d, map[string]string{
		"access_token": testToken, "base_url": "https://graph.test.local", "max_pages": "2",
	})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := len(d.calls); got != 2 {
		t.Errorf("requests = %d, want exactly max_pages=2", got)
	}
}
