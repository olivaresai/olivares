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
