// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package foundryagents

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

type recordedRequest struct {
	Method string
	URL    string
	Auth   string
	Body   string
}

type route struct {
	status int
	body   string
}

type stubDoer struct {
	t      *testing.T
	routes map[string][]route
	calls  []recordedRequest
}

func newStub(t *testing.T) *stubDoer {
	t.Helper()
	return &stubDoer{t: t, routes: map[string][]route{}}
}

func (d *stubDoer) fixture(name string) string {
	d.t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		d.t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func (d *stubDoer) on(method, match, body string) *stubDoer {
	return d.onStatus(method, match, http.StatusOK, body)
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
	if bestKey == "" {
		d.t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
	}
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

const (
	testTokenURL     = "https://login.test.local/tenant/oauth2/v2.0/token"
	testClientSecret = "client-secret-supersecret"
)

func baseSettings(extra map[string]string) map[string]string {
	out := map[string]string{
		"tenant_id":           "tenant-1",
		"client_id":           "client-1",
		"client_secret":       testClientSecret,
		"oauth_token_url":     testTokenURL,
		"subscription_id":     "sub-1",
		"management_endpoint": "https://management.test",
		"data_plane_base":     "https://data.test",
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func openTestSource(t *testing.T, d *stubDoer, extra map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = d
	if err := s.Open(context.Background(), sdk.Config{Settings: baseSettings(extra)}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.now = func() time.Time { return fixedTime() }
	return s
}

func fixedTime() time.Time { return time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC) }

func setupTokens(d *stubDoer, dataPlane bool) {
	d.on(http.MethodPost, testTokenURL, d.fixture("token_arm.json"))
	if dataPlane {
		d.on(http.MethodPost, testTokenURL, d.fixture("token_ai.json"))
	}
}

func setupFullARM(d *stubDoer) {
	d.on(http.MethodGet, "page=2&api-version=2024-10-01", d.fixture("accounts_page2.json"))
	d.on(http.MethodGet, "/subscriptions/sub-1/providers/Microsoft.CognitiveServices/accounts", d.fixture("accounts_page1.json"))
	d.on(http.MethodGet, "/accounts/acct1/projects", d.fixture("projects_acct1.json"))
	d.on(http.MethodGet, "/projects/projA/applications", d.fixture("applications_projA.json"))
	d.on(http.MethodGet, "/applications/appNoIdentity/agentDeployments", d.fixture("deployments_no_identity.json"))
	d.on(http.MethodGet, "/applications/appFailed/agentDeployments", d.fixture("deployments_failed.json"))
	d.on(http.MethodGet, "/applications/appWithIdentity/agentDeployments", d.fixture("deployments_running.json"))
	d.on(http.MethodGet, "/projects/projB/applications", d.fixture("applications_projB.json"))
	d.on(http.MethodGet, "/applications/appDisabled/agentDeployments", d.fixture("deployments_empty.json"))
}

func setupDataPlane(d *stubDoer) {
	d.on(http.MethodGet, "/api/projects/projA/agents", d.fixture("agents_data_page1.json"))
	d.on(http.MethodGet, "/api/projects/projA/agents", d.fixture("agents_data_page2.json"))
	d.on(http.MethodGet, "/api/projects/projB/agents", d.fixture("agents_value.json"))
}

func TestSnapshotMapsARMAndDataPlaneGraph(t *testing.T) {
	d := newStub(t)
	setupTokens(d, true)
	setupFullARM(d)
	setupDataPlane(d)
	s := openTestSource(t, d, nil)

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceFoundry || !g.CapturedAt.Equal(fixedTime()) {
		t.Fatalf("graph source/capturedAt = %q/%v", g.Source, g.CapturedAt)
	}
	assertTokenPosts(t, d)
	assertNoSecretOnGET(t, d, testClientSecret)
	assertSawURL(t, d, "page=2&api-version=2024-10-01")
	assertSawURL(t, d, "/api/projects/projA/agents?after=agent-1&api-version=v1")

	if got := len(g.Collections); got != 2 {
		t.Fatalf("collections = %d, want 2: %+v", got, g.Collections)
	}
	if g.Collections[0].Ref != "/subscriptions/sub-1/resourceGroups/rg-main/providers/Microsoft.CognitiveServices/accounts/acct1/projects/projA" ||
		g.Collections[0].Kind != identitysource.KindGroup ||
		g.Collections[0].Attributes["object"] != objectFoundryProject ||
		g.Collections[0].Attributes["account"] != "acct1" {
		t.Fatalf("first project collection wrong: %+v", g.Collections[0])
	}

	if got := len(g.Identities); got != 7 {
		t.Fatalf("identities = %d, want 4 apps + 3 agents: %+v", got, g.Identities)
	}
	noID := mustIdentity(t, g, "/subscriptions/sub-1/resourceGroups/rg-main/providers/Microsoft.CognitiveServices/accounts/acct1/projects/projA/applications/appNoIdentity")
	if noID.Kind != kindFoundryAgentApplication || noID.Type != identitysource.PrincipalNHI || noID.Disabled {
		t.Fatalf("appNoIdentity shape wrong: %+v", noID)
	}
	wantAppAttrs := map[string]string{
		"provisioning_state": "Succeeded",
		"base_url":           "https://invoke.example/noid",
		"agents":             "Alpha Agent,Helper Agent",
		"deployment_states":  "Running",
		"deployment_types":   "Managed",
		"object":             objectFoundryAgentApplication,
	}
	for k, want := range wantAppAttrs {
		if got := noID.Attributes[k]; got != want {
			t.Errorf("appNoIdentity attr %s = %q, want %q", k, got, want)
		}
	}

	failed := mustIdentity(t, g, "/subscriptions/sub-1/resourceGroups/rg-main/providers/Microsoft.CognitiveServices/accounts/acct1/projects/projA/applications/appFailed")
	if failed.Attributes["entra_blueprint_client_id"] != "blueprint-client" ||
		failed.Attributes["entra_blueprint_principal_id"] != "blueprint-principal" ||
		failed.Attributes["deployment_states"] != "Failed" ||
		failed.Attributes["deployment_types"] != "Hosted" {
		t.Errorf("failed app attrs wrong: %+v", failed.Attributes)
	}
	disabled := mustIdentity(t, g, "/subscriptions/sub-1/resourceGroups/rg-main/providers/Microsoft.CognitiveServices/accounts/acct1/projects/projB/applications/appDisabled")
	if !disabled.Disabled {
		t.Error("isEnabled=false application must map to Disabled")
	}

	agent1 := mustIdentity(t, g, "acct1/projA/agent-1")
	if agent1.Kind != kindFoundryAgent || agent1.DisplayName != "Prompt Agent" || agent1.Disabled {
		t.Fatalf("agent1 shape wrong: %+v", agent1)
	}
	if agent1.Attributes["created_at"] != time.Unix(1783276800, 0).UTC().Format(time.RFC3339) ||
		agent1.Attributes["definition_kind"] != "prompt" ||
		agent1.Attributes["model"] != "gpt-4.1" ||
		agent1.Attributes["object"] != objectFoundryAgent {
		t.Errorf("agent1 attrs wrong: %+v", agent1.Attributes)
	}
	agent2 := mustIdentity(t, g, "acct1/projA/agent-2")
	if !agent2.Disabled || agent2.Attributes["draft"] != "true" || agent2.Attributes["created_at"] != "not-a-unix" {
		t.Errorf("agent2 disabled/draft/raw created_at wrong: %+v", agent2)
	}
	agent3 := mustIdentity(t, g, "acct1/projB/agent-3")
	if agent3.Attributes["definition_kind"] != "workflow" || agent3.Attributes["version"] != "3" {
		t.Errorf("value-envelope agent attrs wrong: %+v", agent3.Attributes)
	}
	if got := len(g.Memberships); got != 7 {
		t.Fatalf("memberships = %d, want 4 app + 3 agent memberships", got)
	}
	assertMembership(t, g, noID.Ref, g.Collections[0].Ref)
	assertMembership(t, g, "acct1/projA/agent-1", g.Collections[0].Ref)
	assertNoGraphSecret(t, g, "SENTINEL-SYSTEM-PROMPT", testClientSecret)
}

func TestResourceGroupFilter(t *testing.T) {
	d := newStub(t)
	setupTokens(d, false)
	d.on(http.MethodGet, "/subscriptions/sub-1/providers/Microsoft.CognitiveServices/accounts", d.fixture("accounts_resource_groups.json"))
	d.on(http.MethodGet, "/accounts/acctkeep/projects", d.fixture("projects_acctkeep.json"))
	d.on(http.MethodGet, "/projects/projKeep/applications", d.fixture("applications_empty.json"))
	s := openTestSource(t, d, map[string]string{"resource_group": "rg-keep", "data_plane": "false"})

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := len(g.Collections); got != 1 {
		t.Fatalf("collections = %d, want only rg-keep account project: %+v", got, g.Collections)
	}
	if strings.Contains(g.Collections[0].Ref, "acctskip") {
		t.Fatalf("resource_group filter allowed skipped account: %+v", g.Collections[0])
	}
	for _, c := range d.calls {
		if strings.Contains(c.URL, "acctskip") {
			t.Fatalf("resource_group filter still scanned skipped account: %s", c.URL)
		}
	}
}

func TestPerProjectDataPlane403Tolerated(t *testing.T) {
	d := newStub(t)
	setupTokens(d, true)
	setupFullARM(d)
	d.onStatus(http.MethodGet, "/api/projects/projA/agents", http.StatusForbidden, `{"error":"missing Foundry User"}`)
	d.on(http.MethodGet, "/api/projects/projB/agents", d.fixture("agents_value.json"))
	s := openTestSource(t, d, nil)

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(g.Collections) != 2 {
		t.Fatalf("ARM project rows should remain intact on data-plane 403: %+v", g.Collections)
	}
	if _, ok := g.FindIdentity("acct1/projA/agent-1"); ok {
		t.Fatal("403 project agents should be skipped")
	}
	if _, ok := g.FindIdentity("acct1/projB/agent-3"); !ok {
		t.Fatal("agents from the permitted project should still land")
	}
	if countKind(g, kindFoundryAgentApplication) != 4 {
		t.Fatalf("ARM application rows should remain intact: %+v", g.Identities)
	}
}

func TestDataPlaneDisabledIssuesNoDataPlaneGET(t *testing.T) {
	d := newStub(t)
	setupTokens(d, false)
	setupFullARM(d)
	s := openTestSource(t, d, map[string]string{"data_plane": "false"})

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if countKind(g, kindFoundryAgent) != 0 {
		t.Fatalf("data_plane=false should emit no data-plane agents: %+v", g.Identities)
	}
	for _, c := range d.calls {
		if strings.Contains(c.URL, "/api/projects/") || strings.Contains(c.Auth, "ai-token") {
			t.Fatalf("data_plane=false issued data-plane request: %+v", c)
		}
	}
}

func TestDataPlanePaginationBounded(t *testing.T) {
	d := newStub(t)
	setupTokens(d, true)
	setupFullARM(d)
	setupDataPlane(d)
	s := openTestSource(t, d, map[string]string{"max_pages": "1"})

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, ok := g.FindIdentity("acct1/projA/agent-1"); !ok {
		t.Fatal("first data-plane page agent missing")
	}
	if _, ok := g.FindIdentity("acct1/projA/agent-2"); ok {
		t.Fatal("max_pages=1 should not follow data-plane has_more cursor")
	}
	for _, c := range d.calls {
		if strings.Contains(c.URL, "after=agent-1") {
			t.Fatalf("max_pages=1 followed data-plane cursor: %s", c.URL)
		}
	}
}

func TestGatherPostureFindings(t *testing.T) {
	findings := runGather(t)
	if got := len(findings); got != 2 {
		t.Fatalf("findings = %d, want 2: %+v", got, findings)
	}
	first := findings[0]
	noIDRef := "/subscriptions/sub-1/resourceGroups/rg-main/providers/Microsoft.CognitiveServices/accounts/acct1/projects/projA/applications/appNoIdentity"
	if first.Kind != findingAppNoAgentIdentity || first.Severity != model.SeverityMedium ||
		first.SubjectKind != findingSubjectIdentity || first.SubjectRef != noIDRef ||
		first.Title != "published agent application without Entra agent identity: No Identity App" ||
		first.DetailHash != redact.Hash("foundry_app_no_agent_identity|foundry-agents|"+noIDRef) ||
		!first.OccurredAt.Equal(fixedTime()) {
		t.Fatalf("first finding wrong: %+v", first)
	}
	second := findings[1]
	failedRef := "/subscriptions/sub-1/resourceGroups/rg-main/providers/Microsoft.CognitiveServices/accounts/acct1/projects/projA/applications/appFailed"
	if second.Kind != findingAppDeploymentFailed || second.Severity != model.SeverityLow ||
		second.SubjectKind != findingSubjectIdentity || second.SubjectRef != failedRef ||
		second.Title != "enabled agent application has a failed deployment: Failed Deploy App" ||
		second.DetailHash != redact.Hash("foundry_app_deployment_failed|foundry-agents|"+failedRef) {
		t.Fatalf("second finding wrong: %+v", second)
	}
}

func TestDeterministicGraphAndFindings(t *testing.T) {
	graphA := graphSequence(runSnapshot(t))
	graphB := graphSequence(runSnapshot(t))
	if !reflect.DeepEqual(graphA, graphB) {
		t.Fatalf("graph order/content changed between runs:\n%v\n%v", graphA, graphB)
	}
	findA := findingSequence(runGather(t))
	findB := findingSequence(runGather(t))
	if !reflect.DeepEqual(findA, findB) {
		t.Fatalf("finding order/content changed between runs:\n%v\n%v", findA, findB)
	}
}

func TestOfflineEmptyGraphAndGather(t *testing.T) {
	d := newStub(t)
	s := New()
	s.doer = d
	s.now = fixedTime
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("offline Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceFoundry || !g.CapturedAt.Equal(fixedTime()) {
		t.Fatalf("offline graph source/capturedAt wrong: %+v", g)
	}
	if len(g.Identities) != 0 || len(g.Collections) != 0 || len(g.Memberships) != 0 {
		t.Fatalf("offline graph must be empty: %+v", g)
	}
	if err := s.Gather(context.Background(), sinkFunc(func(context.Context, model.Observation) error {
		t.Fatal("offline Gather must not emit")
		return nil
	})); err != nil {
		t.Fatalf("offline Gather: %v", err)
	}
	if len(d.calls) != 0 {
		t.Fatalf("offline connector touched HTTP transport: %+v", d.calls)
	}
}

func TestDescriptorShape(t *testing.T) {
	desc := New().Descriptor()
	if desc.Name != Name || desc.Version != version || desc.Type != sdk.TypeSource || desc.APIVersion != sdk.APIVersion {
		t.Fatalf("descriptor header wrong: %+v", desc)
	}
	secret := map[string]bool{}
	defaults := map[string]string{}
	for _, f := range desc.ConfigFields {
		secret[f.Key] = f.Secret
		defaults[f.Key] = f.Default
	}
	if !secret["client_secret"] {
		t.Error("client_secret must be declared Secret")
	}
	if defaults["data_plane"] != "true" || defaults["projects_api_version"] != defaultProjectsAPIVersion ||
		defaults["applications_api_version"] != defaultApplicationsAPIVersion {
		t.Errorf("descriptor defaults wrong: %+v", defaults)
	}
}

func TestTokenErrorRedactsClientSecret(t *testing.T) {
	d := newStub(t)
	d.onStatus(http.MethodPost, testTokenURL, http.StatusBadRequest, `{"error":"bad `+testClientSecret+`"}`)
	s := openTestSource(t, d, map[string]string{"data_plane": "false"})

	_, err := s.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected token error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("token error should carry status: %v", err)
	}
	if strings.Contains(err.Error(), testClientSecret) {
		t.Fatalf("token error leaked client secret: %v", err)
	}
}

func runSnapshot(t *testing.T) identitysource.Graph {
	t.Helper()
	d := newStub(t)
	setupTokens(d, true)
	setupFullARM(d)
	setupDataPlane(d)
	s := openTestSource(t, d, nil)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return g
}

func runGather(t *testing.T) []model.FindingReport {
	t.Helper()
	d := newStub(t)
	setupTokens(d, false)
	setupFullARM(d)
	s := openTestSource(t, d, nil)
	sink := &findingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, c := range d.calls {
		if strings.Contains(c.URL, "/api/projects/") || strings.Contains(c.Auth, "ai-token") {
			t.Fatalf("Gather must not read data plane: %+v", c)
		}
	}
	return sink.findings
}

type findingSink struct {
	findings []model.FindingReport
}

func (s *findingSink) Emit(_ context.Context, obs model.Observation) error {
	f, ok := obs.(model.FindingReport)
	if !ok {
		return fmt.Errorf("unexpected observation %T", obs)
	}
	s.findings = append(s.findings, f)
	return nil
}

type sinkFunc func(context.Context, model.Observation) error

func (f sinkFunc) Emit(ctx context.Context, obs model.Observation) error { return f(ctx, obs) }

func assertTokenPosts(t *testing.T, d *stubDoer) {
	t.Helper()
	var posts []recordedRequest
	for _, c := range d.calls {
		if c.Method == http.MethodPost {
			posts = append(posts, c)
		}
	}
	if got := len(posts); got != 2 {
		t.Fatalf("token POSTs = %d, want management + data-plane: %+v", got, posts)
	}
	wantScopes := []string{managementScope, dataPlaneScope}
	for i, post := range posts {
		if post.URL != testTokenURL {
			t.Fatalf("token post %d URL = %q, want %q", i, post.URL, testTokenURL)
		}
		form, err := url.ParseQuery(post.Body)
		if err != nil {
			t.Fatalf("parse token form %d: %v", i, err)
		}
		want := map[string]string{
			"grant_type":    "client_credentials",
			"client_id":     "client-1",
			"client_secret": testClientSecret,
			"scope":         wantScopes[i],
		}
		for k, v := range want {
			if got := form.Get(k); got != v {
				t.Errorf("token form %d %s = %q, want %q", i, k, got, v)
			}
		}
	}
}

func assertNoSecretOnGET(t *testing.T, d *stubDoer, secret string) {
	t.Helper()
	for _, c := range d.calls {
		if c.Method != http.MethodGet {
			continue
		}
		if strings.Contains(c.URL+c.Auth+c.Body, secret) {
			t.Fatalf("client secret leaked onto GET: %+v", c)
		}
	}
}

func assertSawURL(t *testing.T, d *stubDoer, sub string) {
	t.Helper()
	for _, c := range d.calls {
		if strings.Contains(c.URL, sub) {
			return
		}
	}
	t.Fatalf("did not see URL containing %q; calls=%+v", sub, d.calls)
}

func mustIdentity(t *testing.T, g identitysource.Graph, ref string) identitysource.Identity {
	t.Helper()
	id, ok := g.FindIdentity(ref)
	if !ok {
		t.Fatalf("identity %q missing; got %+v", ref, g.Identities)
	}
	return id
}

func assertMembership(t *testing.T, g identitysource.Graph, memberRef, collectionRef string) {
	t.Helper()
	for _, m := range g.Memberships {
		if m.MemberRef == memberRef && m.CollectionRef == collectionRef &&
			m.MemberKind == identitysource.MemberIdentity && m.Source == identitysource.SourceFoundry {
			return
		}
	}
	t.Fatalf("membership %q -> %q missing: %+v", memberRef, collectionRef, g.Memberships)
}

func assertNoGraphSecret(t *testing.T, g identitysource.Graph, secrets ...string) {
	t.Helper()
	var fields []string
	for _, id := range g.Identities {
		fields = append(fields, id.Ref, id.Kind, id.DisplayName, string(id.Type), string(id.Source))
		for k, v := range id.Attributes {
			fields = append(fields, k, v)
		}
	}
	for _, c := range g.Collections {
		fields = append(fields, c.Ref, c.DisplayName, string(c.Kind), string(c.Source))
		for k, v := range c.Attributes {
			fields = append(fields, k, v)
		}
	}
	for _, m := range g.Memberships {
		fields = append(fields, m.MemberRef, m.CollectionRef, string(m.MemberKind), string(m.Source))
	}
	for _, field := range fields {
		for _, secret := range secrets {
			if secret != "" && strings.Contains(field, secret) {
				t.Fatalf("secret %q leaked into graph field %q", secret, field)
			}
		}
	}
}

func countKind(g identitysource.Graph, kind string) int {
	var n int
	for _, id := range g.Identities {
		if id.Kind == kind {
			n++
		}
	}
	return n
}

func graphSequence(g identitysource.Graph) []string {
	out := []string{"source:" + string(g.Source)}
	for _, c := range g.Collections {
		out = append(out, "collection:"+c.Ref+":"+c.DisplayName)
	}
	for _, id := range g.Identities {
		out = append(out, "identity:"+id.Ref+":"+id.Kind+":"+id.DisplayName)
	}
	for _, m := range g.Memberships {
		out = append(out, "membership:"+m.MemberRef+"->"+m.CollectionRef)
	}
	return out
}

func findingSequence(rows []model.FindingReport) []string {
	out := make([]string, 0, len(rows))
	for _, f := range rows {
		out = append(out, f.Kind+":"+f.SubjectRef+":"+f.DetailHash)
	}
	return out
}
