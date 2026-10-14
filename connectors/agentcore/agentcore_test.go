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

func TestOpenRejectsMalformedEndpoint(t *testing.T) {
	clearAWSEnv(t)
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"endpoint": "not a url"}})
	if err == nil {
		t.Fatal("a malformed endpoint override must fail Open")
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
