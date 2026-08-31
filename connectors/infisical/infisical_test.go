// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package infisical

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

const (
	testBase     = "https://infisical.test"
	testLoginURL = testBase + "/api/v1/auth/universal-auth/login"
	testOrg      = "org-acme"
)

var fixedTime = time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)

func fixedClock() time.Time { return fixedTime }

// fixture reads a recorded JSON body from testdata.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// stubDoer routes requests by method+path to recorded fixtures, records the
// universal-auth login body and the bearer header observed on read calls, and so
// stands in for a live Infisical with no network. It hard-fails on any path that
// touches secret material: the connector reads membership METADATA only.
type stubDoer struct {
	t *testing.T

	loginBody    []byte            // the JSON the connector POSTed to the login endpoint
	loginCalls   int               // how many times login was hit
	authHeaders  []string          // Authorization header seen on each GET (read) call
	loginStatus  int               // override login status (0 => 200)
	failPath     string            // a path whose GET returns 500 (error-path test)
	requestPaths []string          // every path requested, in order
	overrides    map[string]string // path → fixture name, served before the default routing

	// paged serves the org identity-memberships listing from the two paginated
	// fixtures, keyed on the offset query param, and records each page's query.
	paged            bool
	orgIdentityPages []string // RawQuery of each org identity-memberships request
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	p := req.URL.Path
	d.requestPaths = append(d.requestPaths, p)

	// Tripwire: the connector must NEVER fetch a secret path (/api/*/secrets*) —
	// membership listings only.
	if strings.Contains(strings.ToLower(p), "secret") {
		d.t.Fatalf("connector fetched a secret path %s; it must never read secret material", p)
	}

	// The login is the only POST; everything else is a read-only GET.
	if req.Method == http.MethodPost {
		if req.URL.String() != testLoginURL {
			d.t.Fatalf("unexpected POST to %s (only the login endpoint may be POSTed)", req.URL.String())
		}
		d.loginCalls++
		body, _ := io.ReadAll(req.Body)
		d.loginBody = body
		status := d.loginStatus
		if status == 0 {
			status = http.StatusOK
		}
		if status != http.StatusOK {
			return jsonResp(status, []byte(`{"message":"Unauthorized"}`)), nil
		}
		return jsonResp(http.StatusOK, fixture(d.t, "login.json")), nil
	}

	if req.Method != http.MethodGet {
		d.t.Fatalf("connector issued a %s to %s; only GET (and the login POST) are allowed", req.Method, p)
	}
	d.authHeaders = append(d.authHeaders, req.Header.Get("Authorization"))

	if d.failPath != "" && p == d.failPath {
		return jsonResp(http.StatusInternalServerError, []byte(`{"message":"boom"}`)), nil
	}
	if name, ok := d.overrides[p]; ok {
		return jsonResp(http.StatusOK, fixture(d.t, name)), nil
	}

	switch p {
	case "/api/v2/organizations/" + testOrg + "/identity-memberships":
		if d.paged {
			d.orgIdentityPages = append(d.orgIdentityPages, req.URL.RawQuery)
			q := req.URL.Query()
			if q.Get("limit") != "100" {
				d.t.Fatalf("identity-memberships limit = %q, want 100", q.Get("limit"))
			}
			switch q.Get("offset") {
			case "0":
				return jsonResp(http.StatusOK, fixture(d.t, "org_identity_memberships_page1.json")), nil
			case "100":
				return jsonResp(http.StatusOK, fixture(d.t, "org_identity_memberships_page2.json")), nil
			default:
				d.t.Fatalf("unexpected identity-memberships offset %q", q.Get("offset"))
				return nil, nil
			}
		}
		return jsonResp(http.StatusOK, fixture(d.t, "org_identity_memberships.json")), nil
	case "/api/v2/organizations/" + testOrg + "/memberships":
		return jsonResp(http.StatusOK, fixture(d.t, "org_memberships.json")), nil
	case "/api/v1/projects":
		return jsonResp(http.StatusOK, fixture(d.t, "projects.json")), nil
	case "/api/v2/workspace/proj-platform/identity-memberships":
		return jsonResp(http.StatusOK, fixture(d.t, "project_platform_identity_memberships.json")), nil
	case "/api/v1/projects/proj-platform/memberships":
		return jsonResp(http.StatusOK, fixture(d.t, "project_platform_memberships.json")), nil
	case "/api/v2/workspace/proj-billing/identity-memberships":
		return jsonResp(http.StatusOK, fixture(d.t, "project_billing_identity_memberships.json")), nil
	case "/api/v1/projects/proj-billing/memberships":
		return jsonResp(http.StatusOK, fixture(d.t, "project_billing_memberships.json")), nil
	default:
		d.t.Fatalf("unexpected GET path %s", p)
		return nil, nil
	}
}

func jsonResp(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

// openSource builds a Source wired to the stub and a fixed clock, with the test
// login_url override.
func openSource(t *testing.T, d *stubDoer, extra map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = d
	s.now = fixedClock
	settings := map[string]string{
		"base_url":      testBase,
		"org_id":        testOrg,
		"client_id":     "machine-client-id",
		"client_secret": "machine-client-secret-SHHH",
		"login_url":     testLoginURL,
	}
	for k, v := range extra {
		if v == "" {
			delete(settings, k)
			continue
		}
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// captureSink records emitted observations.
type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

// splitObs partitions captured observations into edges and findings, failing on
// any other observation kind.
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

func TestSnapshotBuildsGraph(t *testing.T) {
	d := &stubDoer{t: t}
	s := openSource(t, d, nil)

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceInfisical {
		t.Errorf("source = %q, want infisical", g.Source)
	}

	// 2 machine identities + 3 human members.
	if len(g.Identities) != 5 {
		t.Fatalf("want 5 identities, got %d", len(g.Identities))
	}

	ci, ok := g.FindIdentity("id-ci-deployer")
	if !ok {
		t.Fatal("ci-deployer identity missing")
	}
	if ci.Type != identitysource.PrincipalNHI {
		t.Errorf("ci-deployer Type = %q, want nhi", ci.Type)
	}
	if ci.Kind != "machine_identity" {
		t.Errorf("ci-deployer Kind = %q, want machine_identity", ci.Kind)
	}
	if ci.DisplayName != "ci-deployer" {
		t.Errorf("ci-deployer DisplayName = %q", ci.DisplayName)
	}

	alice, ok := g.FindIdentity("user-alice")
	if !ok {
		t.Fatal("alice identity missing")
	}
	if alice.Type != identitysource.PrincipalHuman {
		t.Errorf("alice Type = %q, want human", alice.Type)
	}
	if alice.Kind != "user" {
		t.Errorf("alice Kind = %q, want user", alice.Kind)
	}
	if alice.DisplayName != "Alice Smith" {
		t.Errorf("alice DisplayName = %q, want 'Alice Smith'", alice.DisplayName)
	}
	if alice.Attributes["email"] != "alice@corp.example" {
		t.Errorf("alice email attr = %v", alice.Attributes)
	}
	if alice.Disabled {
		t.Error("alice is isActive=true; must not be Disabled")
	}

	// Bob has empty first/last (display falls back to email) and isActive=false:
	// the roster carries him Disabled — the account is never dropped.
	bob, ok := g.FindIdentity("user-bob")
	if !ok {
		t.Fatal("bob identity missing")
	}
	if bob.DisplayName != "bob@corp.example" {
		t.Errorf("bob DisplayName = %q, want email fallback", bob.DisplayName)
	}
	if !bob.Disabled {
		t.Error("bob is isActive=false; roster must carry Disabled=true")
	}

	// Carol's row omits isActive entirely: absence is not deactivation.
	carol, ok := g.FindIdentity("user-carol")
	if !ok {
		t.Fatal("carol identity missing")
	}
	if carol.Disabled {
		t.Error("carol has no isActive flag; absence must not read as Disabled")
	}

	// 2 projects.
	if len(g.Collections) != 2 {
		t.Fatalf("want 2 collections, got %d", len(g.Collections))
	}
	for _, c := range g.Collections {
		if c.Kind != identitysource.KindGroup {
			t.Errorf("collection %s Kind = %q, want group", c.Ref, c.Kind)
		}
		if c.Attributes["kind"] != "project" {
			t.Errorf("collection %s missing project kind label: %v", c.Ref, c.Attributes)
		}
	}

	// Memberships: platform has ci-deployer + alice; billing has backup-agent +
	// bob + carol (carol's no-access role still IS a project membership).
	want := map[string]map[string]bool{
		"proj-platform": {"id-ci-deployer": true, "user-alice": true},
		"proj-billing":  {"id-backup-agent": true, "user-bob": true, "user-carol": true},
	}

	// Exact total guard: the per-project maps below dedup by member ref, so a
	// duplicated or cross-project edge would not show up there. Assert the raw edge
	// count equals the sum across projects (2 platform + 3 billing = 5), which locks
	// out duplicate/phantom membership edges.
	wantTotal := 0
	for _, members := range want {
		wantTotal += len(members)
	}
	if len(g.Memberships) != wantTotal {
		t.Fatalf("len(g.Memberships) = %d, want exactly %d (sum across projects, no duplicate/cross-project edges)", len(g.Memberships), wantTotal)
	}

	got := map[string]map[string]bool{}
	for _, m := range g.Memberships {
		if m.Source != identitysource.SourceInfisical {
			t.Errorf("membership source = %q", m.Source)
		}
		if m.MemberKind != identitysource.MemberIdentity {
			t.Errorf("membership %s kind = %q, want identity", m.MemberRef, m.MemberKind)
		}
		if got[m.CollectionRef] == nil {
			got[m.CollectionRef] = map[string]bool{}
		}
		got[m.CollectionRef][m.MemberRef] = true
	}
	for proj, members := range want {
		for ref := range members {
			if !got[proj][ref] {
				t.Errorf("project %s missing membership for %s; got %v", proj, ref, got[proj])
			}
		}
		if len(got[proj]) != len(members) {
			t.Errorf("project %s membership count = %d, want %d (%v)", proj, len(got[proj]), len(members), got[proj])
		}
	}
}

// TestLoginTokenExchange asserts the universal-auth handshake: the connector POSTs
// the client_id/client_secret to the override login_url, parses {accessToken},
// and uses that minted token (not the secret) as the bearer on every read.
func TestLoginTokenExchange(t *testing.T) {
	d := &stubDoer{t: t}
	s := openSource(t, d, nil)

	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if d.loginCalls != 1 {
		t.Fatalf("login called %d times, want exactly 1", d.loginCalls)
	}
	var lr loginRequest
	if err := json.Unmarshal(d.loginBody, &lr); err != nil {
		t.Fatalf("login body not JSON: %v (%s)", err, d.loginBody)
	}
	if lr.ClientID != "machine-client-id" || lr.ClientSecret != "machine-client-secret-SHHH" {
		t.Errorf("login body did not carry the configured credential: %+v", lr)
	}

	if len(d.authHeaders) == 0 {
		t.Fatal("no read calls were authenticated")
	}
	for _, h := range d.authHeaders {
		if h != "Bearer ist.minted.abc123" {
			t.Errorf("read auth header = %q, want the minted token bearer", h)
		}
	}
}

// TestTokenCached asserts the minted token is reused across reads — the login
// endpoint is hit once, not once per request.
func TestTokenCached(t *testing.T) {
	d := &stubDoer{t: t}
	s := openSource(t, d, nil)
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// Many read calls happened (org + projects), but exactly one login.
	if d.loginCalls != 1 {
		t.Errorf("login hit %d times; token should be cached after the first", d.loginCalls)
	}
	if len(d.authHeaders) < 6 {
		t.Errorf("expected several authenticated reads, got %d", len(d.authHeaders))
	}
}

// TestPreIssuedAccessToken asserts that with access_token set the connector skips
// the login entirely and bears that token directly.
func TestPreIssuedAccessToken(t *testing.T) {
	d := &stubDoer{t: t}
	s := openSource(t, d, map[string]string{
		"access_token":  "ist.preissued.xyz",
		"client_id":     "",
		"client_secret": "",
	})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if d.loginCalls != 0 {
		t.Errorf("login should not be hit when access_token is set; got %d", d.loginCalls)
	}
	for _, h := range d.authHeaders {
		if h != "Bearer ist.preissued.xyz" {
			t.Errorf("read auth header = %q, want pre-issued bearer", h)
		}
	}
}

// TestOfflineNoCredential asserts an empty graph (no error, no network) when no
// credential is configured.
func TestOfflineNoCredential(t *testing.T) {
	d := &stubDoer{t: t}
	s := New()
	s.doer = d
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"base_url": testBase, "org_id": testOrg, // org but no credential
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("offline Snapshot should not error: %v", err)
	}
	if len(g.Identities) != 0 || len(g.Collections) != 0 || len(g.Memberships) != 0 {
		t.Errorf("offline graph should be empty: %+v", g)
	}
	if len(d.requestPaths) != 0 {
		t.Errorf("offline mode must make no network calls; saw %v", d.requestPaths)
	}
}

// TestOfflineNoOrg asserts an empty graph when a credential is present but no org.
func TestOfflineNoOrg(t *testing.T) {
	d := &stubDoer{t: t}
	s := openSource(t, d, map[string]string{"org_id": ""})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(g.Identities) != 0 {
		t.Errorf("no-org graph should be empty, got %d identities", len(g.Identities))
	}
	if len(d.requestPaths) != 0 {
		t.Errorf("no-org mode must make no network calls; saw %v", d.requestPaths)
	}
}

// TestLoginFailure asserts a non-2xx login surfaces an error AND never leaks the
// client secret into that error.
func TestLoginFailure(t *testing.T) {
	d := &stubDoer{t: t, loginStatus: http.StatusUnauthorized}
	s := openSource(t, d, nil)
	_, err := s.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected login error on 401")
	}
	if strings.Contains(err.Error(), "machine-client-secret-SHHH") {
		t.Errorf("login error leaked the client secret: %v", err)
	}
}

// TestReadErrorPropagates asserts a 5xx on a read call propagates as an error.
func TestReadErrorPropagates(t *testing.T) {
	d := &stubDoer{t: t, failPath: "/api/v1/projects"}
	s := openSource(t, d, nil)
	if _, err := s.Snapshot(context.Background()); err == nil {
		t.Fatal("expected error when /api/v1/projects returns 500")
	}
}

// TestSnapshotPaginationCollectsAll asserts the offset/limit loop on the org
// identity-memberships listing: 102 identities arrive across two pages (100+2,
// totalCount-driven) instead of being silently truncated at the server default.
func TestSnapshotPaginationCollectsAll(t *testing.T) {
	d := &stubDoer{t: t, paged: true}
	s := openSource(t, d, nil)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// 102 machine identities + 3 humans.
	if len(g.Identities) != 105 {
		t.Fatalf("want 105 identities across two pages, got %d", len(g.Identities))
	}
	if _, ok := g.FindIdentity("id-bulk-101"); !ok {
		t.Error("identity from the offset=100 page missing; pagination not followed")
	}
	if len(d.orgIdentityPages) != 2 {
		t.Errorf("org identity-memberships requested %d times, want 2 pages: %v", len(d.orgIdentityPages), d.orgIdentityPages)
	}
}

// TestPaginationBoundedByMaxPages asserts the loop respects the max_pages safety
// bound instead of trusting totalCount unconditionally.
func TestPaginationBoundedByMaxPages(t *testing.T) {
	d := &stubDoer{t: t, paged: true}
	s := openSource(t, d, map[string]string{"max_pages": "1"})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(d.orgIdentityPages) != 1 {
		t.Fatalf("max_pages=1 must stop after one page; saw %d requests", len(d.orgIdentityPages))
	}
	if len(g.Identities) != 103 { // 100 machine (first page only) + 3 humans
		t.Errorf("want 103 identities under max_pages=1, got %d", len(g.Identities))
	}
}

// TestNoSecretInGraph is the security guard: no credential value (client secret,
// access token, minted token, login_url) may appear anywhere in the produced
// graph — only identity/collection/membership metadata.
func TestNoSecretInGraph(t *testing.T) {
	d := &stubDoer{t: t}
	s := openSource(t, d, nil)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	secrets := []string{"machine-client-secret-SHHH", "machine-client-id", "ist.minted.abc123"}
	var blob strings.Builder
	write := func(parts ...string) {
		for _, p := range parts {
			blob.WriteString(p)
			blob.WriteByte('\n')
		}
	}
	for _, id := range g.Identities {
		write(id.Ref, id.Kind, id.DisplayName, string(id.Type))
		for k, v := range id.Attributes {
			write(k, v)
		}
	}
	for _, c := range g.Collections {
		write(c.Ref, c.DisplayName, string(c.Kind))
		for k, v := range c.Attributes {
			write(k, v)
		}
	}
	for _, m := range g.Memberships {
		write(m.MemberRef, m.CollectionRef, string(m.MemberKind))
	}
	hay := blob.String()
	for _, sec := range secrets {
		if strings.Contains(hay, sec) {
			t.Errorf("graph leaked secret material %q", sec)
		}
	}
}

// TestGatherOffline asserts the first-line offline guard: with no credential (or
// no org) Gather returns nil, emits nothing and never touches the network.
func TestGatherOffline(t *testing.T) {
	cases := map[string]map[string]string{
		"no credential": {"client_id": "", "client_secret": ""},
		"no org":        {"org_id": ""},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			d := &stubDoer{t: t}
			s := openSource(t, d, extra)
			sink := &captureSink{}
			if err := s.Gather(context.Background(), sink); err != nil {
				t.Fatalf("offline Gather should return nil, got %v", err)
			}
			if len(sink.obs) != 0 {
				t.Errorf("offline Gather must emit nothing; got %d observations", len(sink.obs))
			}
			if len(d.requestPaths) != 0 {
				t.Errorf("offline Gather must make no network calls; saw %v", d.requestPaths)
			}
		})
	}
}

// TestGatherEmitsProjectGrants is the happy path: one PERMITTED edge per project
// membership, role-classified, single-clock stamped. Bob is org-disabled but his
// viewer grant IS emitted (a disabled account still holds its grant); carol's
// no-access-only membership contributes no edge.
func TestGatherEmitsProjectGrants(t *testing.T) {
	d := &stubDoer{t: t}
	s := openSource(t, d, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	edges, findings := splitObs(t, sink.obs)
	if len(findings) != 0 {
		t.Fatalf("happy path must emit no findings, got %+v", findings)
	}
	if len(edges) != 4 {
		t.Fatalf("want 4 edges, got %d: %+v", len(edges), edges)
	}

	byKey := map[string]model.EdgeObservation{}
	for _, e := range edges {
		byKey[e.OriginRef+"→"+e.ResourceRef] = e
	}
	want := []struct {
		origin, resource string
		mode             model.AccessMode
		toolRef          string
	}{
		{"id-ci-deployer", "proj-platform", model.ModeUnknown, "deploy-bot"}, // custom role: never guessed
		{"user-alice", "proj-platform", model.ModeReadWrite, "admin,viewer"}, // union of known roles
		{"id-backup-agent", "proj-billing", model.ModeReadWrite, "member"},
		{"user-bob", "proj-billing", model.ModeRead, "viewer"}, // disabled, still holds the grant
	}
	for _, w := range want {
		e, ok := byKey[w.origin+"→"+w.resource]
		if !ok {
			t.Errorf("missing edge %s→%s; got %v", w.origin, w.resource, byKey)
			continue
		}
		if e.OriginKind != "identity" {
			t.Errorf("%s OriginKind = %q, want identity", w.origin, e.OriginKind)
		}
		if e.ResourceKind != "infisical.project" {
			t.Errorf("%s ResourceKind = %q, want infisical.project", w.origin, e.ResourceKind)
		}
		if e.Mode != w.mode {
			t.Errorf("%s Mode = %q, want %q", w.origin, e.Mode, w.mode)
		}
		if e.Source != model.SignalPolicy {
			t.Errorf("%s Source = %q, want policy", w.origin, e.Source)
		}
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("%s Confidence = %q, want attributed", w.origin, e.Confidence)
		}
		if e.ToolRef != w.toolRef {
			t.Errorf("%s ToolRef = %q, want %q", w.origin, e.ToolRef, w.toolRef)
		}
		if !e.ObservedAt.Equal(fixedTime) {
			t.Errorf("%s ObservedAt = %v, want the single per-run clock capture %v", w.origin, e.ObservedAt, fixedTime)
		}
	}
	for k := range byKey {
		if strings.HasPrefix(k, "user-carol") {
			t.Errorf("carol's only role is no-access; no edge may be emitted, got %s", k)
		}
	}
}

// TestGrantModeTable pins the role→mode classification: built-ins map, known
// modes union vault-style, custom is never guessed, no-access alone grants
// nothing nameable (no edge).
func TestGrantModeTable(t *testing.T) {
	r := func(role string) membershipRole { return membershipRole{Role: role} }
	custom := membershipRole{Role: "custom", CustomRoleSlug: "deploy-bot"}
	cases := []struct {
		name  string
		roles []membershipRole
		mode  model.AccessMode
		ok    bool
	}{
		{"admin", []membershipRole{r("admin")}, model.ModeReadWrite, true},
		{"member", []membershipRole{r("member")}, model.ModeReadWrite, true},
		{"viewer", []membershipRole{r("viewer")}, model.ModeRead, true},
		{"union admin+viewer", []membershipRole{r("admin"), r("viewer")}, model.ModeReadWrite, true},
		{"known mode wins over custom", []membershipRole{r("viewer"), custom}, model.ModeRead, true},
		{"custom only", []membershipRole{custom}, model.ModeUnknown, true},
		{"unrecognized slug", []membershipRole{r("auditor-2027")}, model.ModeUnknown, true},
		{"no-access + custom", []membershipRole{r("no-access"), custom}, model.ModeUnknown, true},
		{"no-access only", []membershipRole{r("no-access")}, "", false},
		{"no roles", nil, "", false},
	}
	for _, c := range cases {
		mode, ok := grantMode(c.roles)
		if mode != c.mode || ok != c.ok {
			t.Errorf("%s: grantMode = (%q, %v), want (%q, %v)", c.name, mode, ok, c.mode, c.ok)
		}
	}
	if got := mergeMode(model.ModeRead, model.ModeWrite); got != model.ModeReadWrite {
		t.Errorf("mergeMode(read, write) = %q, want readwrite", got)
	}
	if got := roleSlugs([]membershipRole{custom, r("admin"), r("admin")}); got != "admin,deploy-bot" {
		t.Errorf("roleSlugs = %q, want sorted deduped 'admin,deploy-bot'", got)
	}
	if got := roleSlugs([]membershipRole{{Role: "custom"}}); got != "custom" {
		t.Errorf("roleSlugs custom-without-slug = %q, want 'custom'", got)
	}
}

// TestGatherPaginationFollowed asserts Gather's roster fetch follows the
// offset/limit loop: the project-bound identities ride the paginated org listing
// (ci-deployer/backup-agent on page 1), so all four edges emit with no finding.
func TestGatherPaginationFollowed(t *testing.T) {
	d := &stubDoer{t: t, paged: true}
	s := openSource(t, d, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(d.orgIdentityPages) != 2 {
		t.Fatalf("org identity-memberships requested %d times, want 2 pages: %v", len(d.orgIdentityPages), d.orgIdentityPages)
	}
	edges, findings := splitObs(t, sink.obs)
	if len(edges) != 4 || len(findings) != 0 {
		t.Errorf("want 4 edges / 0 findings over the paged roster, got %d / %d", len(edges), len(findings))
	}
}

// TestGatherConvergence is the hard invariant: every emitted OriginRef appears as
// an Identity.Ref in this connector's own Snapshot over the same fixtures.
func TestGatherConvergence(t *testing.T) {
	g, err := openSource(t, &stubDoer{t: t}, nil).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	rostered := map[string]bool{}
	for _, id := range g.Identities {
		rostered[id.Ref] = true
	}

	sink := &captureSink{}
	if err := openSource(t, &stubDoer{t: t}, nil).Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	edges, _ := splitObs(t, sink.obs)
	if len(edges) == 0 {
		t.Fatal("expected edges to check convergence against")
	}
	for _, e := range edges {
		if !rostered[e.OriginRef] {
			t.Errorf("edge origin %q is not in the Snapshot roster %v", e.OriginRef, rostered)
		}
	}
}

// TestGatherStrayOriginCoverageFinding asserts the never-silent half of the
// convergence invariant: an origin the org roster would not contain is suppressed
// (no edge) and surfaces as exactly ONE Info coverage finding counting distinct
// stray origins — a stray whose only role is no-access held no grant and does not
// count.
func TestGatherStrayOriginCoverageFinding(t *testing.T) {
	d := &stubDoer{t: t, overrides: map[string]string{
		"/api/v2/workspace/proj-billing/identity-memberships": "project_billing_identity_memberships_stray.json",
	}}
	s := openSource(t, d, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	edges, findings := splitObs(t, sink.obs)

	for _, e := range edges {
		if strings.HasPrefix(e.OriginRef, "id-ghost") {
			t.Errorf("stray origin %q must never be emitted as an edge", e.OriginRef)
		}
	}
	if len(edges) != 3 { // platform 2 + billing bob (backup-agent replaced by the stray fixture)
		t.Errorf("want 3 edges, got %d: %+v", len(edges), edges)
	}

	if len(findings) != 1 {
		t.Fatalf("want exactly one coverage finding per run, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Kind != "coverage" {
		t.Errorf("finding Kind = %q, want coverage", f.Kind)
	}
	if f.Severity != model.SeverityInfo {
		t.Errorf("finding Severity = %q, want info", f.Severity)
	}
	if f.SubjectKind != "identity_source" || f.SubjectRef != Name {
		t.Errorf("finding subject = %q/%q, want identity_source/%s", f.SubjectKind, f.SubjectRef, Name)
	}
	if want := "1 permitted-grant origins outside the rostered identity set were not emitted"; f.Title != want {
		t.Errorf("finding Title = %q, want %q", f.Title, want)
	}
	if !f.OccurredAt.Equal(fixedTime) {
		t.Errorf("finding OccurredAt = %v, want %v", f.OccurredAt, fixedTime)
	}
}

// TestGatherCredentialNeverInError asserts a failing Gather login surfaces an
// error that never carries the client secret.
func TestGatherCredentialNeverInError(t *testing.T) {
	d := &stubDoer{t: t, loginStatus: http.StatusUnauthorized}
	s := openSource(t, d, nil)
	err := s.Gather(context.Background(), &captureSink{})
	if err == nil {
		t.Fatal("expected login error on 401")
	}
	if strings.Contains(err.Error(), "machine-client-secret-SHHH") {
		t.Errorf("Gather error leaked the client secret: %v", err)
	}
}

// TestGatherReadErrorPropagates asserts a transport failure mid-run returns
// immediately (the engine retries; partial re-emission is dedup-safe).
func TestGatherReadErrorPropagates(t *testing.T) {
	d := &stubDoer{t: t, failPath: "/api/v1/projects"}
	s := openSource(t, d, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err == nil {
		t.Fatal("expected error when /api/v1/projects returns 500")
	}
	if len(sink.obs) != 0 {
		t.Errorf("nothing should emit before the failing fetch; got %d", len(sink.obs))
	}
}

// TestGatherEmitErrorStopsRun asserts a sink error returns immediately, after
// exactly the failing Emit.
func TestGatherEmitErrorStopsRun(t *testing.T) {
	d := &stubDoer{t: t}
	s := openSource(t, d, nil)
	sink := &failSink{}
	err := s.Gather(context.Background(), sink)
	if !errors.Is(err, errSinkFull) {
		t.Fatalf("Gather must surface the sink error, got %v", err)
	}
	if sink.calls != 1 {
		t.Errorf("Gather must stop on the first Emit error; saw %d calls", sink.calls)
	}
}

func TestDescriptorSecretFields(t *testing.T) {
	s := New()
	d := s.Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource || d.APIVersion != sdk.APIVersion {
		t.Fatalf("descriptor identity wrong: %+v", d)
	}
	secret := map[string]bool{}
	for _, f := range d.ConfigFields {
		secret[f.Key] = f.Secret
	}
	for _, k := range []string{"client_secret", "access_token"} {
		if !secret[k] {
			t.Errorf("config field %q must be marked Secret", k)
		}
	}
}

func TestTransportError(t *testing.T) {
	s := openSource(t, &stubDoer{t: t}, nil)
	// Replace the doer with one that always fails the network call.
	s.doer = doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
	s.token = "" // force a fresh login attempt
	if _, err := s.Snapshot(context.Background()); err == nil {
		t.Fatal("expected transport error from a failing login")
	}
}

// doerFunc adapts a func to httpx.Doer.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// failSink rejects every Emit, counting attempts.
var errSinkFull = errors.New("sink full")

type failSink struct{ calls int }

func (f *failSink) Emit(context.Context, model.Observation) error {
	f.calls++
	return errSinkFull
}
