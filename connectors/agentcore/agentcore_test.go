// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agentcore

import (
	"bytes"
	"context"
	"io"
	"net/http"
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
	testAKID   = "AKIAIOSFODNN7EXAMPLE"
	testSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	testToken  = "FQoGZXIvYXdzEXAMPLESESSIONTOKEN"
)

var testNow = time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)

// recordedReq captures one request the stub served, for wire assertions.
type recordedReq struct {
	method, url, auth, secTok, body string
}

// stubResp is one queued stub response (body + status, FIFO per route key).
type stubResp struct {
	body   string
	status int
}

// stubDoer routes requests by the LONGEST "METHOD path-substring" key that
// matches (the entra-agent refinement of the idp convention — needed because
// "/identities/GetWorkloadIdentity" style paths share prefixes), consuming each
// key's responses FIFO. It records every request so tests assert the exact wire.
type stubDoer struct {
	t      *testing.T
	routes map[string][]stubResp // key -> FIFO of responses
	reqs   []recordedReq
}

func newStub(t *testing.T) *stubDoer {
	return &stubDoer{t: t, routes: map[string][]stubResp{}}
}

func (d *stubDoer) on(method, sub, body string) {
	d.onStatus(method, sub, http.StatusOK, body)
}

func (d *stubDoer) onStatus(method, sub string, status int, body string) {
	key := method + " " + sub
	d.routes[key] = append(d.routes[key], stubResp{body: body, status: status})
}

func (d *stubDoer) fixture(name string) string {
	d.t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		d.t.Fatalf("fixture %s: %v", name, err)
	}
	return string(b)
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
	}
	d.reqs = append(d.reqs, recordedReq{
		method: req.Method, url: req.URL.String(),
		auth: req.Header.Get("Authorization"), secTok: req.Header.Get("X-Amz-Security-Token"),
		body: body,
	})
	target := req.Method + " " + req.URL.String()
	best := ""
	for key, queue := range d.routes {
		if len(queue) == 0 {
			continue
		}
		parts := strings.SplitN(key, " ", 2)
		if req.Method == parts[0] && strings.Contains(target, parts[1]) && len(parts[1]) > len(best) {
			best = parts[1]
		}
	}
	if best == "" {
		d.t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
	}
	key := req.Method + " " + best
	resp := d.routes[key][0]
	d.routes[key] = d.routes[key][1:]
	return &http.Response{StatusCode: resp.status, Body: io.NopCloser(bytes.NewBufferString(resp.body)), Header: http.Header{}}, nil
}

// sinkFunc adapts a func to sdk.Sink (the idp test convention).
type sinkFunc func(ctx context.Context, o model.Observation) error

func (f sinkFunc) Emit(ctx context.Context, o model.Observation) error { return f(ctx, o) }

// clearAWSEnv isolates the test from ambient AWS credentials (Open reads the
// env fallbacks; an empty value does not satisfy the offline check's non-empty
// requirement).
func clearAWSEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envAccessKeyID, "")
	t.Setenv(envSecretAccessKey, "")
	t.Setenv(envSessionToken, "")
}

func openSource(t *testing.T, d *stubDoer, settings map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = d
	s.now = func() time.Time { return testNow }
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// onlineSettings is the canonical online test config (endpoint override so no
// real AWS host is ever touched).
func onlineSettings(extra map[string]string) map[string]string {
	m := map[string]string{
		"region":            "eu-west-1",
		"access_key_id":     testAKID,
		"secret_access_key": testSecret,
		"endpoint":          "https://agentcore.test.local",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// stubFullRoster wires every Snapshot route with the golden fixtures.
func stubFullRoster(d *stubDoer) {
	d.on(http.MethodPost, "/identities/ListWorkloadIdentities", d.fixture("workload_identities_page1.json"))
	d.on(http.MethodPost, "/identities/ListWorkloadIdentities", d.fixture("workload_identities_page2.json"))
	d.on(http.MethodPost, "/identities/ListOauth2CredentialProviders", d.fixture("oauth2_providers.json"))
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", d.fixture("apikey_providers.json"))
	d.on(http.MethodGet, "/policy-engines?", d.fixture("policy_engines_page1.json"))
	d.on(http.MethodGet, "/policy-engines?", d.fixture("policy_engines_page2.json"))
	d.on(http.MethodGet, "/policy-engines/pe-alpha/policies", d.fixture("policies_pe_alpha.json"))
	d.on(http.MethodGet, "/policy-engines/pe-beta/policies", d.fixture("policies_pe_beta.json"))
	d.on(http.MethodGet, "/registries?", d.fixture("registries.json"))
	d.on(http.MethodGet, "/registries/reg-alpha/records?", d.fixture("registry_records.json"))
}

// stubFullGather wires the routes Gather needs beyond drift (registry + policy posture).
func stubFullGather(d *stubDoer) {
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", d.fixture("apikey_providers.json"))
	d.on(http.MethodGet, "/registries?", d.fixture("registries.json"))
	d.on(http.MethodGet, "/registries/reg-alpha/records?", d.fixture("registry_records.json"))
	d.on(http.MethodGet, "/policy-engines?", d.fixture("policy_engines_page1.json"))
	d.on(http.MethodGet, "/policy-engines?", d.fixture("policy_engines_page2.json"))
	d.on(http.MethodGet, "/gateways?", d.fixture("gateways.json"))
	d.on(http.MethodGet, "/gateways/gw-prod", d.fixture("gateway_detail_prod.json"))
	d.on(http.MethodGet, "/gateways/gw-unattached", d.fixture("gateway_detail_unattached.json"))
	d.on(http.MethodGet, "/policy-engines/pe-alpha/policies", d.fixture("policies_pe_alpha.json"))
	d.on(http.MethodGet, "/policy-engines/pe-beta/policies", d.fixture("policies_pe_beta.json"))
}

func TestSnapshotMapsRoster(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	stubFullRoster(d)
	s := openSource(t, d, onlineSettings(map[string]string{"enable_registry": "false"}))

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceAgentCore {
		t.Errorf("source = %q", g.Source)
	}
	if !g.CapturedAt.Equal(testNow) {
		t.Errorf("captured_at = %v", g.CapturedAt)
	}

	// 3 workload identities (2 pages) + 1 oauth2 + 2 api-key providers.
	if got := len(g.Identities); got != 6 {
		t.Fatalf("identities = %d, want 6", got)
	}
	wi, ok := g.FindIdentity("arn:aws:bedrock-agentcore:eu-west-1:123456789012:workload-identity-directory/default/workload-identity/triage-agent")
	if !ok {
		t.Fatal("triage-agent (page 2) missing — nextToken not followed")
	}
	if wi.Kind != identitysource.KindWorkloadIdentity || wi.Type != identitysource.PrincipalNHI {
		t.Errorf("workload identity kind/type = %q/%q", wi.Kind, wi.Type)
	}
	if wi.DisplayName != "triage-agent" || wi.Attributes["region"] != "eu-west-1" {
		t.Errorf("workload identity row = %+v", wi)
	}
	if _, present := wi.Attributes["created_at"]; present {
		t.Error("detail=false must not carry timestamps (no N+1 happened)")
	}
	// The second list request carried the continuation token in the RPC body;
	// the REST policy half carries it as a query parameter — single-encoded
	// (url.Values wire form): base64 '+/=' must appear exactly once-encoded so
	// the SigV4 canonical form (awssig decode-then-encode) matches what the
	// server recomputes.
	tokenSeen, peTokenSeen := false, false
	for _, r := range d.reqs {
		if strings.Contains(r.url, "ListWorkloadIdentities") && strings.Contains(r.body, "WI-PAGE-2") {
			tokenSeen = true
		}
		if strings.Contains(r.url, "/policy-engines?") && strings.Contains(r.url, "nextToken=PE%2BPAGE%2F2%3D") {
			peTokenSeen = true
		}
		if strings.Contains(r.url, "ListWorkloadIdentities") && !strings.Contains(r.body, `"maxResults":20`) {
			t.Errorf("ListWorkloadIdentities must request maxResults 20, body=%s", r.body)
		}
		if strings.Contains(r.url, "ListApiKeyCredentialProviders") && !strings.Contains(r.body, `"maxResults":100`) {
			t.Errorf("ListApiKeyCredentialProviders must request maxResults 100, body=%s", r.body)
		}
		if strings.Contains(r.url, "GetWorkloadIdentity") {
			t.Error("detail=false must not call GetWorkloadIdentity")
		}
	}
	if !tokenSeen {
		t.Error("nextToken must travel in the RPC body of the follow-up page")
	}
	if !peTokenSeen {
		t.Error("the policy-engines nextToken must travel single-encoded in the query (PE%2BPAGE%2F2%3D)")
	}

	oauth, ok := g.FindIdentity("arn:aws:acps:eu-west-1:123456789012:token-vault/default/oauth2credentialprovider/github-oauth")
	if !ok {
		t.Fatal("oauth2 credential provider missing (acps ARN namespace)")
	}
	if oauth.Kind != "credential_provider" || oauth.Attributes["vendor"] != "GithubOauth2" {
		t.Errorf("oauth provider row = %+v", oauth)
	}
	apik, ok := g.FindIdentity("arn:aws:acps:eu-west-1:123456789012:token-vault/default/apikeycredentialprovider/serpapi-key")
	if !ok {
		t.Fatal("api-key credential provider missing")
	}
	if apik.Kind != "apikey_credential_provider" {
		t.Errorf("api-key provider kind = %q", apik.Kind)
	}

	// 2 engines (paged) + 2 policies of pe-alpha; pe-beta has none.
	if got := len(g.Collections); got != 4 {
		t.Fatalf("collections = %d, want 4: %+v", got, g.Collections)
	}
	var cedar, generated, engines int
	for _, c := range g.Collections {
		switch {
		case c.Kind == identitysource.KindPolicy && c.Attributes["definition"] == "cedar":
			cedar++
		case c.Kind == identitysource.KindPolicy && c.Attributes["definition"] == "generated":
			generated++
		case c.Kind == identitysource.KindGroup && c.Attributes["object"] == "policy_engine":
			engines++
		}
	}
	if cedar != 1 || generated != 1 || engines != 2 {
		t.Errorf("collection split cedar/generated/engines = %d/%d/%d", cedar, generated, engines)
	}
	// Each policy belongs to its engine.
	if len(g.Memberships) != 2 {
		t.Fatalf("memberships = %d, want 2", len(g.Memberships))
	}
	for _, m := range g.Memberships {
		if m.MemberKind != identitysource.MemberCollection ||
			m.CollectionRef != "arn:aws:bedrock-agentcore:eu-west-1:123456789012:policy-engine/pe-alpha" {
			t.Errorf("membership = %+v", m)
		}
	}
}

func TestSnapshotDetailMode(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	stubFullRoster(d)
	// detail=true resolves each of the three identities, in list order.
	d.on(http.MethodPost, "/identities/GetWorkloadIdentity", d.fixture("get_workload_identity_support.json"))
	d.on(http.MethodPost, "/identities/GetWorkloadIdentity", d.fixture("get_workload_identity_billing.json"))
	d.on(http.MethodPost, "/identities/GetWorkloadIdentity", d.fixture("get_workload_identity_triage.json"))
	s := openSource(t, d, onlineSettings(map[string]string{"detail": "true", "enable_registry": "false"}))

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	wi, ok := g.FindIdentity("arn:aws:bedrock-agentcore:eu-west-1:123456789012:workload-identity-directory/default/workload-identity/support-agent")
	if !ok {
		t.Fatal("support-agent missing")
	}
	// Epoch seconds 1748995200 / 1749081600 rendered RFC3339 UTC.
	if wi.Attributes["created_at"] != "2025-06-04T00:00:00Z" || wi.Attributes["updated_at"] != "2025-06-05T00:00:00Z" {
		t.Errorf("detail timestamps = %v", wi.Attributes)
	}
	gets := 0
	for _, r := range d.reqs {
		if strings.Contains(r.url, "GetWorkloadIdentity") {
			gets++
		}
	}
	if gets != 3 {
		t.Errorf("GetWorkloadIdentity calls = %d, want exactly one per identity", gets)
	}
}

func TestSnapshotDetailDegradesOn404(t *testing.T) {
	// The list/get TOCTOU: an identity deleted between list and get (404), or an
	// IAM policy granting List but not Get (403), degrades to the listed row
	// WITHOUT timestamps — never a snapshot failure. Any other status still fails.
	clearAWSEnv(t)
	d := newStub(t)
	stubFullRoster(d)
	d.on(http.MethodPost, "/identities/GetWorkloadIdentity", d.fixture("get_workload_identity_support.json"))
	d.onStatus(http.MethodPost, "/identities/GetWorkloadIdentity", http.StatusNotFound, `{"message":"ResourceNotFoundException"}`)
	d.on(http.MethodPost, "/identities/GetWorkloadIdentity", d.fixture("get_workload_identity_triage.json"))
	s := openSource(t, d, onlineSettings(map[string]string{"detail": "true", "enable_registry": "false"}))

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("a per-identity 404 in detail mode must not fail the snapshot: %v", err)
	}
	if got := len(g.Identities); got != 6 {
		t.Fatalf("identities = %d, want 6 (the 404'd row stays, without timestamps)", got)
	}
	billing, _ := g.FindIdentity("arn:aws:bedrock-agentcore:eu-west-1:123456789012:workload-identity-directory/default/workload-identity/billing-agent")
	if _, present := billing.Attributes["created_at"]; present {
		t.Error("the 404'd identity must carry no timestamps")
	}
	support, _ := g.FindIdentity("arn:aws:bedrock-agentcore:eu-west-1:123456789012:workload-identity-directory/default/workload-identity/support-agent")
	if support.Attributes["created_at"] == "" {
		t.Error("the resolved identity must keep its timestamps")
	}

	// A 500 on the same call is NOT tolerated.
	d2 := newStub(t)
	stubFullRoster(d2)
	d2.onStatus(http.MethodPost, "/identities/GetWorkloadIdentity", http.StatusInternalServerError, `{"message":"boom"}`)
	s2 := openSource(t, d2, onlineSettings(map[string]string{"detail": "true", "enable_registry": "false"}))
	if _, err := s2.Snapshot(context.Background()); err == nil {
		t.Fatal("a 500 in detail mode must fail the snapshot")
	}
}

func TestPaginationBoundedByMaxPages(t *testing.T) {
	// A server that echoes a nextToken forever must be stopped by max_pages —
	// the third queued page must stay unconsumed (the stub fatals on any
	// unexpected request, so an unbounded regression fails fast).
	clearAWSEnv(t)
	d := newStub(t)
	for i := 0; i < 3; i++ {
		d.on(http.MethodPost, "/identities/ListWorkloadIdentities", `{"workloadIdentities":[],"nextToken":"MORE"}`)
	}
	d.on(http.MethodPost, "/identities/ListOauth2CredentialProviders", `{"credentialProviders":[]}`)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", `{"credentialProviders":[]}`)
	s := openSource(t, d, onlineSettings(map[string]string{"max_pages": "2", "include_policies": "false", "enable_registry": "false"}))
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	calls := 0
	for _, r := range d.reqs {
		if strings.Contains(r.url, "ListWorkloadIdentities") {
			calls++
		}
	}
	if calls != 2 {
		t.Errorf("ListWorkloadIdentities calls = %d, want exactly max_pages=2", calls)
	}
}

func TestSnapshotExcludesPoliciesWhenDisabled(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodPost, "/identities/ListWorkloadIdentities", d.fixture("workload_identities_page2.json"))
	d.on(http.MethodPost, "/identities/ListOauth2CredentialProviders", `{"credentialProviders":[]}`)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", `{"credentialProviders":[]}`)
	s := openSource(t, d, onlineSettings(map[string]string{"include_policies": "false", "enable_registry": "false"}))

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(g.Collections) != 0 || len(g.Memberships) != 0 {
		t.Errorf("include_policies=false must emit no collections/memberships: %+v", g.Collections)
	}
	for _, r := range d.reqs {
		if strings.Contains(r.url, "/policy-engines") {
			t.Error("include_policies=false must not call the policy API")
		}
	}
}

func TestSigV4Signing(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	stubFullRoster(d)
	s := openSource(t, d, onlineSettings(map[string]string{"session_token": testToken}))
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, r := range d.reqs {
		if !strings.HasPrefix(r.auth, "AWS4-HMAC-SHA256 ") {
			t.Fatalf("request not SigV4-signed: %q", r.auth)
		}
		// The credential scope must use the SIGNING NAME bedrock-agentcore (the
		// botocore trap: the endpoint prefix bedrock-agentcore-control is wrong).
		if !strings.Contains(r.auth, "/eu-west-1/bedrock-agentcore/aws4_request") {
			t.Errorf("credential scope must sign for bedrock-agentcore: %q", r.auth)
		}
		if !strings.Contains(r.auth, "Credential="+testAKID+"/") {
			t.Errorf("credential must carry the access key id: %q", r.auth)
		}
		if r.secTok != testToken {
			t.Errorf("X-Amz-Security-Token = %q, want the session token", r.secTok)
		}
		if strings.Contains(r.auth, testSecret) || strings.Contains(r.url, testSecret) || strings.Contains(r.body, testSecret) {
			t.Error("the secret access key must never travel on the wire")
		}
	}
}

func TestEnvCredentialFallback(t *testing.T) {
	t.Setenv(envAccessKeyID, testAKID)
	t.Setenv(envSecretAccessKey, testSecret)
	t.Setenv(envSessionToken, "")
	d := newStub(t)
	stubFullRoster(d)
	s := openSource(t, d, map[string]string{"region": "eu-west-1", "endpoint": "https://agentcore.test.local"})
	if s.offline() {
		t.Fatal("env credentials must bring the connector online")
	}
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(d.reqs) == 0 || !strings.Contains(d.reqs[0].auth, testAKID) {
		t.Error("requests must be signed with the env access key id")
	}
}

func TestOfflineEmptyGraph(t *testing.T) {
	clearAWSEnv(t)
	cases := []map[string]string{
		{},                          // nothing configured
		{"region": "eu-west-1"},     // region without credentials
		{"access_key_id": testAKID}, // key without secret/region
		{"access_key_id": testAKID, "secret_access_key": testSecret}, // no region
	}
	for _, settings := range cases {
		d := newStub(t)
		s := openSource(t, d, settings)
		g, err := s.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("offline Snapshot must not error: %v", err)
		}
		if g.Source != identitysource.SourceAgentCore || g.CapturedAt.IsZero() {
			t.Errorf("offline graph must carry Source and CapturedAt: %+v", g)
		}
		if len(g.Identities)+len(g.Collections)+len(g.Memberships) != 0 {
			t.Errorf("offline graph must be empty: %+v", g)
		}
		if err := s.Gather(context.Background(), sinkFunc(func(context.Context, model.Observation) error {
			t.Error("offline Gather must emit nothing")
			return nil
		})); err != nil {
			t.Fatalf("offline Gather must return nil: %v", err)
		}
		if len(d.reqs) != 0 {
			t.Errorf("offline mode must never touch the network: %d requests", len(d.reqs))
		}
	}
}

func TestOpenRejectsMalformedEndpoint(t *testing.T) {
	clearAWSEnv(t)
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"endpoint": "not a url"}})
	if err == nil {
		t.Fatal("a malformed endpoint override must fail Open")
	}
}

func TestAPIErrorCarriesStatusNeverTheSecret(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.onStatus(http.MethodPost, "/identities/ListWorkloadIdentities", http.StatusForbidden, `{"message":"AccessDeniedException"}`)
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_export_drift": "false",
		"enable_eval_posture": "false",
	}))
	_, err := s.Snapshot(context.Background())
	if err == nil {
		t.Fatal("a 403 must surface as an error")
	}
	if !strings.Contains(err.Error(), "status 403") || !strings.Contains(err.Error(), "AccessDeniedException") {
		t.Errorf("error must carry status + excerpt: %v", err)
	}
	if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), testAKID) {
		t.Errorf("error must never carry the credential: %v", err)
	}
}

func TestGatherDriftFindings(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", d.fixture("apikey_providers.json"))
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_registry":       "false",
		"enable_policy_posture": "false",
		"enable_export_drift":   "false",
		"enable_eval_posture":   "false",
	}))

	var got []model.FindingReport
	if err := s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
		f, ok := o.(model.FindingReport)
		if !ok {
			t.Fatalf("unexpected observation type %T", o)
		}
		got = append(got, f)
		return nil
	})); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("findings = %d, want one per api-key provider", len(got))
	}
	f := got[0]
	if f.Kind != identitysource.FindingLongLivedCredential || f.Severity != model.SeverityMedium {
		t.Errorf("finding kind/severity = %q/%q", f.Kind, f.Severity)
	}
	if f.SubjectKind != "identity" || !strings.Contains(f.SubjectRef, "apikeycredentialprovider/serpapi-key") {
		t.Errorf("finding subject = %q/%q", f.SubjectKind, f.SubjectRef)
	}
	wantHash := redact.Hash(identitysource.FindingLongLivedCredential + "|agentcore|" + f.SubjectRef)
	if f.DetailHash != wantHash {
		t.Errorf("DetailHash must be the stable dedup key, got %q want %q", f.DetailHash, wantHash)
	}
	if !f.OccurredAt.Equal(testNow) {
		t.Errorf("OccurredAt = %v", f.OccurredAt)
	}
}

func TestNoSecretLeaksIntoGraphOrFindings(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	stubFullRoster(d)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", d.fixture("apikey_providers.json"))
	s := openSource(t, d, onlineSettings(map[string]string{
		"session_token":         testToken,
		"enable_registry":       "false",
		"enable_policy_posture": "false",
		"enable_export_drift":   "false",
		"enable_eval_posture":   "false",
	}))

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var emitted []string
	for _, id := range g.Identities {
		emitted = append(emitted, id.Ref, id.DisplayName, id.Kind, string(id.Type))
		for k, v := range id.Attributes {
			emitted = append(emitted, k, v)
		}
	}
	for _, c := range g.Collections {
		emitted = append(emitted, c.Ref, c.DisplayName)
		for k, v := range c.Attributes {
			emitted = append(emitted, k, v)
		}
	}
	_ = s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
		if f, ok := o.(model.FindingReport); ok {
			emitted = append(emitted, f.Title, f.SubjectRef, f.DetailHash)
		}
		return nil
	}))
	for _, v := range emitted {
		for _, secret := range []string{testAKID, testSecret, testToken} {
			if strings.Contains(v, secret) {
				t.Fatalf("credential material leaked into an emitted value: %q", v)
			}
		}
	}
}

func TestDescriptorShape(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource {
		t.Errorf("descriptor name/type = %q/%q", d.Name, d.Type)
	}
	secret := map[string]bool{}
	for _, f := range d.ConfigFields {
		secret[f.Key] = f.Secret
	}
	for _, key := range []string{"access_key_id", "secret_access_key", "session_token"} {
		if !secret[key] {
			t.Errorf("config field %q must be declared Secret", key)
		}
	}
	for _, key := range []string{"region", "endpoint", "max_pages", "detail", "include_policies", "timeout",
		"enable_registry", "enable_policy_posture", "enable_export_drift", "enable_eval_posture",
		"max_registries", "max_records", "account_id"} {
		if secret[key] {
			t.Errorf("config field %q must not be Secret", key)
		}
	}
	defaults := map[string]string{}
	for _, f := range d.ConfigFields {
		defaults[f.Key] = f.Default
	}
	for _, key := range []string{"enable_export_drift", "enable_eval_posture"} {
		if defaults[key] != "true" {
			t.Errorf("config field %q default = %q, want true", key, defaults[key])
		}
	}
}

// ---------------------------------------------------------------------------
// Registry sub-source tests
// ---------------------------------------------------------------------------

func TestGatherRegistryEdgesAndFindings(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	stubFullGather(d)
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_export_drift": "false",
		"enable_eval_posture": "false",
	}))

	var edges []model.EdgeObservation
	var findings []model.FindingReport
	if err := s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
		switch v := o.(type) {
		case model.EdgeObservation:
			edges = append(edges, v)
		case model.FindingReport:
			findings = append(findings, v)
		}
		return nil
	})); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// 2 APPROVED records → 2 edges (support-mcp-tool MCP + billing-a2a-agent A2A).
	if got := len(edges); got != 2 {
		t.Fatalf("edges = %d, want 2 (one per APPROVED record)", got)
	}
	for _, e := range edges {
		if e.Source != model.SignalAgentCore {
			t.Errorf("edge Source = %q, want agentcore", e.Source)
		}
		if e.OriginKind != "agent" {
			t.Errorf("edge OriginKind = %q, want agent", e.OriginKind)
		}
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("edge Confidence = %q, want attributed", e.Confidence)
		}
	}
	if edges[0].Labels["descriptor_type"] != "MCP" || edges[1].Labels["descriptor_type"] != "A2A" {
		t.Errorf("edge descriptor types = %q / %q", edges[0].Labels["descriptor_type"], edges[1].Labels["descriptor_type"])
	}

	// 2 CREATE_FAILED + 1 DEPRECATED → 2 registry_posture findings + drift + policy findings.
	var registryFindings []model.FindingReport
	for _, f := range findings {
		if f.Kind == "registry_posture" {
			registryFindings = append(registryFindings, f)
		}
	}
	if got := len(registryFindings); got != 2 {
		t.Fatalf("registry posture findings = %d, want 2 (CREATE_FAILED + DEPRECATED)", got)
	}
	for _, f := range registryFindings {
		if f.SubjectKind != subjectRegistryRecord {
			t.Errorf("registry finding SubjectKind = %q", f.SubjectKind)
		}
	}
}

func TestGatherRegistryEmptyEmitsNoEdges(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", `{"credentialProviders":[]}`)
	d.on(http.MethodGet, "/registries?", d.fixture("registries_empty.json"))
	d.on(http.MethodGet, "/policy-engines?", `{"policyEngines":[]}`)
	d.on(http.MethodGet, "/gateways?", `{"gateways":[]}`)
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_export_drift": "false",
		"enable_eval_posture": "false",
	}))

	var findings []model.FindingReport
	if err := s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
		if f, ok := o.(model.FindingReport); ok {
			findings = append(findings, f)
		}
		return nil
	})); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var registryInfos int
	for _, f := range findings {
		if f.Kind == "registry_posture" && strings.Contains(f.Title, "No AgentCore registries") {
			registryInfos++
		}
	}
	if registryInfos != 1 {
		t.Errorf("empty registry should emit exactly one info finding, got %d", registryInfos)
	}
}

func TestGatherRegistryDisabled(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", `{"credentialProviders":[]}`)
	d.on(http.MethodGet, "/policy-engines?", `{"policyEngines":[]}`)
	d.on(http.MethodGet, "/gateways?", `{"gateways":[]}`)
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_registry":     "false",
		"enable_export_drift": "false",
		"enable_eval_posture": "false",
	}))

	if err := s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
		return nil
	})); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, r := range d.reqs {
		if strings.Contains(r.url, "/registries") {
			t.Error("enable_registry=false must not call the registry API")
		}
	}
}

func TestRegistryRecordSeverityMapping(t *testing.T) {
	cases := []struct {
		status string
		want   model.Severity
	}{
		{"CREATE_FAILED", model.SeverityHigh},
		{"UPDATE_FAILED", model.SeverityHigh},
		{"DEPRECATED", model.SeverityMedium},
		{"REJECTED", model.SeverityMedium},
		{"DRAFT", model.SeverityLow},
		{"PENDING_APPROVAL", model.SeverityLow},
		{"CREATING", model.SeverityLow},
		{"UPDATING", model.SeverityLow},
		{"UNKNOWN_STATUS", model.SeverityInfo},
	}
	for _, tc := range cases {
		if got := registryRecordSeverity(tc.status); got != tc.want {
			t.Errorf("registryRecordSeverity(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Policy posture sub-source tests
// ---------------------------------------------------------------------------

func TestGatherPolicyPostureFindings(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	stubFullGather(d)
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_registry":     "false",
		"enable_export_drift": "false",
		"enable_eval_posture": "false",
		"account_id":          "123456789012",
	}))

	var findings []model.FindingReport
	if err := s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
		if f, ok := o.(model.FindingReport); ok {
			findings = append(findings, f)
		}
		return nil
	})); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var policyFindings []model.FindingReport
	for _, f := range findings {
		if f.Kind == "policy_posture" {
			policyFindings = append(policyFindings, f)
		}
	}
	if len(policyFindings) == 0 {
		t.Fatal("policy posture must emit at least one finding")
	}

	// Verify gateway-without-policy finding exists.
	var ungoverned int
	for _, f := range policyFindings {
		if strings.Contains(f.Title, "no policy engine attached") {
			ungoverned++
		}
	}
	if ungoverned != 1 {
		t.Errorf("should find exactly 1 ungoverned gateway, got %d", ungoverned)
	}

	// Verify engine status findings.
	var engineFindings int
	for _, f := range policyFindings {
		if f.SubjectKind == subjectPolicyEngine {
			engineFindings++
		}
	}
	if engineFindings == 0 {
		t.Error("should emit at least one engine status finding")
	}

	// Verify Cedar policy findings exist.
	var cedarFindings int
	for _, f := range policyFindings {
		if f.SubjectKind == subjectCedarPolicy {
			cedarFindings++
		}
	}
	if cedarFindings == 0 {
		t.Error("should emit at least one Cedar policy finding")
	}
}

func TestGatherPolicyPostureNoEnginesWarning(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", `{"credentialProviders":[]}`)
	d.on(http.MethodGet, "/policy-engines?", `{"policyEngines":[]}`)
	d.on(http.MethodGet, "/gateways?", `{"gateways":[]}`)
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_registry":     "false",
		"enable_export_drift": "false",
		"enable_eval_posture": "false",
	}))

	var findings []model.FindingReport
	if err := s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
		if f, ok := o.(model.FindingReport); ok {
			findings = append(findings, f)
		}
		return nil
	})); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var noEngines int
	for _, f := range findings {
		if f.Kind == "policy_posture" && strings.Contains(f.Title, "No AgentCore policy engines") {
			noEngines++
			if f.Severity != model.SeverityMedium {
				t.Errorf("no-engines finding severity = %q, want medium", f.Severity)
			}
		}
	}
	if noEngines != 1 {
		t.Errorf("should emit exactly 1 no-engines finding, got %d", noEngines)
	}
}

func TestGatherPolicyPostureDisabled(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", `{"credentialProviders":[]}`)
	d.on(http.MethodGet, "/registries?", d.fixture("registries_empty.json"))
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_policy_posture": "false",
		"enable_export_drift":   "false",
		"enable_eval_posture":   "false",
	}))

	if err := s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
		return nil
	})); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, r := range d.reqs {
		if strings.Contains(r.url, "/gateways") {
			t.Error("enable_policy_posture=false must not call the gateway API")
		}
	}
}

func TestCedarPolicyContentExtraction(t *testing.T) {
	s := New()
	cases := []struct {
		name string
		item policyItem
		want string
	}{
		{
			"cedar policy extracts statement",
			policyItem{
				Definition: policyDefinition{
					Cedar: []byte(`{"statement":"permit(principal, action, resource);"}`),
				},
			},
			"permit(principal, action, resource);",
		},
		{
			"generated policy returns empty",
			policyItem{
				Definition: policyDefinition{
					Generated: []byte(`{"description":"baseline"}`),
				},
			},
			"",
		},
		{
			"empty definition returns empty",
			policyItem{},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.getCedarPolicyContent(tc.item); got != tc.want {
				t.Errorf("getCedarPolicyContent = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Export drift, evaluations and guardrail posture
// ---------------------------------------------------------------------------

func TestGatherExportDriftFindings(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", `{"credentialProviders":[]}`)
	d.on(http.MethodGet, "/policy-engines?", d.fixture("export_drift_policy_engines.json"))
	d.on(http.MethodGet, "/policy-engines/pe-export/policies", d.fixture("export_drift_policies.json"))
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_registry":       "false",
		"enable_policy_posture": "false",
		"enable_eval_posture":   "false",
	}))

	var findings []model.FindingReport
	if err := s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
		if f, ok := o.(model.FindingReport); ok {
			findings = append(findings, f)
		}
		return nil
	})); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var drift, failed int
	for _, f := range findings {
		switch f.Kind {
		case "export_drift":
			drift++
			if !strings.Contains(f.SubjectRef, "pol-drift") {
				t.Errorf("export_drift subject = %q, want pol-drift", f.SubjectRef)
			}
		case "export_apply_failed":
			failed++
			if !strings.Contains(f.SubjectRef, "pol-failed") {
				t.Errorf("export_apply_failed subject = %q, want pol-failed", f.SubjectRef)
			}
		}
		if strings.Contains(f.SubjectRef, "pol-manual") || strings.Contains(f.SubjectRef, "pol-foreign") {
			t.Errorf("unmanaged/foreign non-drift policy must not emit a finding: %+v", f)
		}
	}
	if drift != 1 || failed != 1 {
		t.Fatalf("export drift/apply failed findings = %d/%d, want 1/1; all=%+v", drift, failed, findings)
	}
}

func TestGatherExportDriftDisabled(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", `{"credentialProviders":[]}`)
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_registry":       "false",
		"enable_policy_posture": "false",
		"enable_export_drift":   "false",
		"enable_eval_posture":   "false",
	}))
	if err := s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
		if f, ok := o.(model.FindingReport); ok && strings.HasPrefix(f.Kind, "export_") {
			t.Fatalf("export drift disabled but emitted %+v", f)
		}
		return nil
	})); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, r := range d.reqs {
		if strings.Contains(r.url, "/policy-engines") {
			t.Error("enable_export_drift=false must not call the policy API")
		}
	}
}

func TestGatherEvalPostureFindings(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", `{"credentialProviders":[]}`)
	d.on(http.MethodPost, "/evaluators?", d.fixture("eval_evaluators.json"))
	d.on(http.MethodPost, "/online-evaluation-configs?", d.fixture("eval_online_configs.json"))
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_registry":       "false",
		"enable_policy_posture": "false",
		"enable_export_drift":   "false",
	}))

	var findings []model.FindingReport
	if err := s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
		if f, ok := o.(model.FindingReport); ok {
			findings = append(findings, f)
		}
		return nil
	})); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var evaluator, online int
	for _, f := range findings {
		switch f.Kind {
		case "evaluator_unhealthy":
			evaluator++
			if !strings.Contains(f.SubjectRef, "eval-custom") {
				t.Errorf("custom evaluator subject = %q", f.SubjectRef)
			}
		case "online_evaluation_failed":
			online++
			if !strings.Contains(f.SubjectRef, "cfg-failed") {
				t.Errorf("online evaluation subject = %q", f.SubjectRef)
			}
		}
		if strings.Contains(f.SubjectRef, "eval-builtin") {
			t.Errorf("builtin evaluator must be skipped: %+v", f)
		}
	}
	if evaluator != 1 || online != 1 {
		t.Fatalf("eval/online findings = %d/%d, want 1/1; all=%+v", evaluator, online, findings)
	}
}

func TestGatherEvalPostureDisabled(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", `{"credentialProviders":[]}`)
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_registry":       "false",
		"enable_policy_posture": "false",
		"enable_export_drift":   "false",
		"enable_eval_posture":   "false",
	}))
	if err := s.Gather(context.Background(), sinkFunc(func(context.Context, model.Observation) error { return nil })); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, r := range d.reqs {
		if strings.Contains(r.url, "/evaluators") || strings.Contains(r.url, "/online-evaluation-configs") {
			t.Error("enable_eval_posture=false must not call evaluations APIs")
		}
	}
}

func TestGatherGuardrailCoverage(t *testing.T) {
	cases := []struct {
		name          string
		region        string
		targetFixture string
		policyFixture string
		wantFinding   int
		wantPolicyAPI bool
	}{
		{
			name:          "supported region without guardrail policy",
			region:        "us-east-1",
			targetFixture: "guardrail_targets.json",
			policyFixture: "guardrail_policies_none.json",
			wantFinding:   1,
			wantPolicyAPI: true,
		},
		{
			name:          "supported region with guardrail policy",
			region:        "us-east-1",
			targetFixture: "guardrail_targets.json",
			policyFixture: "guardrail_policies_with.json",
			wantFinding:   0,
			wantPolicyAPI: true,
		},
		{
			name:        "unsupported region",
			region:      "eu-west-1",
			wantFinding: 0,
		},
		{
			name:          "zero targets",
			region:        "us-east-1",
			targetFixture: "guardrail_targets_empty.json",
			wantFinding:   0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearAWSEnv(t)
			d := newStub(t)
			d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", `{"credentialProviders":[]}`)
			d.on(http.MethodPost, "/evaluators?", d.fixture("eval_empty.json"))
			d.on(http.MethodPost, "/online-evaluation-configs?", d.fixture("online_eval_empty.json"))
			if guardrailsSupportedRegion(tc.region) {
				d.on(http.MethodGet, "/policy-engines?", d.fixture("guardrail_policy_engines.json"))
				d.on(http.MethodGet, "/gateways?", d.fixture("guardrail_gateways.json"))
				d.on(http.MethodGet, "/gateways/gw-guard", d.fixture("guardrail_gateway_detail.json"))
				d.on(http.MethodGet, "/gateways/gw-guard/targets/?", d.fixture(tc.targetFixture))
				if tc.policyFixture != "" {
					d.on(http.MethodGet, "/policy-engines/pe-guard/policies", d.fixture(tc.policyFixture))
				}
			}
			s := openSource(t, d, onlineSettings(map[string]string{
				"region":                tc.region,
				"enable_registry":       "false",
				"enable_policy_posture": "false",
				"enable_export_drift":   "false",
			}))

			var guardrailFindings int
			if err := s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
				if f, ok := o.(model.FindingReport); ok && f.Kind == "gateway_without_guardrails" {
					guardrailFindings++
					if f.SubjectKind != subjectPolicyGateway || !strings.Contains(f.SubjectRef, "gw-guard") {
						t.Errorf("guardrail finding subject = %q/%q", f.SubjectKind, f.SubjectRef)
					}
				}
				return nil
			})); err != nil {
				t.Fatalf("Gather: %v", err)
			}
			if guardrailFindings != tc.wantFinding {
				t.Fatalf("gateway_without_guardrails = %d, want %d", guardrailFindings, tc.wantFinding)
			}
			var policyCalls int
			for _, r := range d.reqs {
				if strings.Contains(r.url, "/policy-engines/pe-guard/policies") {
					policyCalls++
				}
				if !guardrailsSupportedRegion(tc.region) && strings.Contains(r.url, "/gateways") {
					t.Error("unsupported guardrail region must not call gateway coverage APIs")
				}
			}
			if tc.wantPolicyAPI && policyCalls == 0 {
				t.Error("expected guardrail policy scan call")
			}
			if !tc.wantPolicyAPI && policyCalls != 0 {
				t.Errorf("policy scan calls = %d, want 0", policyCalls)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Snapshot with registry records
// ---------------------------------------------------------------------------

func TestSnapshotIncludesRegistryRecords(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	stubFullRoster(d)
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_export_drift": "false",
		"enable_eval_posture": "false",
	}))

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// 3 workload identities + 1 oauth2 + 2 api-key + 2 APPROVED registry records = 8.
	if got := len(g.Identities); got != 8 {
		t.Fatalf("identities = %d, want 8 (6 base + 2 approved records)", got)
	}

	rec, ok := g.FindIdentity("arn:aws:bedrock-agentcore:eu-west-1:123456789012:registry/reg-alpha/record/rec-mcp-1")
	if !ok {
		t.Fatal("APPROVED MCP registry record missing from identity roster")
	}
	if rec.Kind != identitysource.KindWorkloadIdentity || rec.Type != identitysource.PrincipalNHI {
		t.Errorf("record kind/type = %q/%q", rec.Kind, rec.Type)
	}
	if rec.DisplayName != "support-mcp-tool" {
		t.Errorf("record DisplayName = %q", rec.DisplayName)
	}
	if rec.Attributes["descriptor_type"] != "MCP" || rec.Attributes["registry"] != "prod-registry" {
		t.Errorf("record attributes = %v", rec.Attributes)
	}

	// Non-APPROVED records (CREATE_FAILED, DEPRECATED) should NOT be in the roster.
	if _, ok := g.FindIdentity("arn:aws:bedrock-agentcore:eu-west-1:123456789012:registry/reg-alpha/record/rec-fail-1"); ok {
		t.Error("CREATE_FAILED record must not appear in identity roster")
	}
	if _, ok := g.FindIdentity("arn:aws:bedrock-agentcore:eu-west-1:123456789012:registry/reg-alpha/record/rec-dep-1"); ok {
		t.Error("DEPRECATED record must not appear in identity roster")
	}
}

// ---------------------------------------------------------------------------
// Export stubs (compilation-only — no runtime behavior to test)
// ---------------------------------------------------------------------------

func TestExportNamespaceConstants(t *testing.T) {
	if NamespaceAgentCore != "AgentCore" {
		t.Errorf("NamespaceAgentCore = %q", NamespaceAgentCore)
	}
	if NamespaceOlivares != "Olivares" {
		t.Errorf("NamespaceOlivares = %q", NamespaceOlivares)
	}
}

func TestExportItemStruct(t *testing.T) {
	item := ExportItem{
		Kind:        "grant",
		Tenant:      "tenant-a",
		SubjectKind: "role",
		SubjectRef:  "secops",
		Workspace:   "workspace-prod",
		Effect:      "permit",
		Perms:       []string{"registry:read"},
	}
	if item.Kind == "" || item.Tenant == "" || len(item.Perms) != 1 {
		t.Errorf("ExportItem must carry the structured row fields: %+v", item)
	}
}

func TestAccountScope(t *testing.T) {
	clearAWSEnv(t)
	cases := []struct {
		accountID string
		region    string
		want      string
	}{
		{"123456789012", "us-east-1", "123456789012/us-east-1"},
		{"", "eu-west-1", "aws/eu-west-1"},
	}
	for _, tc := range cases {
		s := &Source{accountID: tc.accountID, region: tc.region}
		if got := s.accountScope(); got != tc.want {
			t.Errorf("accountScope(%q, %q) = %q, want %q", tc.accountID, tc.region, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Health finding on sub-source failure
// ---------------------------------------------------------------------------

func TestGatherRegistryFailureEmitsHealthFinding(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodPost, "/identities/ListApiKeyCredentialProviders", `{"credentialProviders":[]}`)
	d.onStatus(http.MethodGet, "/registries?", http.StatusForbidden, `{"message":"AccessDeniedException"}`)
	d.on(http.MethodGet, "/policy-engines?", `{"policyEngines":[]}`)
	d.on(http.MethodGet, "/gateways?", `{"gateways":[]}`)
	s := openSource(t, d, onlineSettings(map[string]string{
		"enable_export_drift": "false",
		"enable_eval_posture": "false",
	}))

	var healthFindings []model.FindingReport
	if err := s.Gather(context.Background(), sinkFunc(func(_ context.Context, o model.Observation) error {
		if f, ok := o.(model.FindingReport); ok && f.Kind == "health" {
			healthFindings = append(healthFindings, f)
		}
		return nil
	})); err != nil {
		t.Fatalf("Gather must continue after a sub-source failure: %v", err)
	}
	if len(healthFindings) == 0 {
		t.Error("a registry API failure must produce a health finding")
	}
	var registryHealth int
	for _, f := range healthFindings {
		if f.SubjectKind == "agentcore.registry" {
			registryHealth++
		}
	}
	if registryHealth != 1 {
		t.Errorf("should see exactly 1 registry health finding, got %d", registryHealth)
	}
}
