// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// fixtureDoer multiplexes by request path to the recorded Codex fixtures (and, for the
// Compliance Logs Platform, by the log_type query to the per-stream JSONL files). It
// records every request so a test can assert the connector is read-only, and can be
// told to return 403/404 for a given path (the sales-gated/UNVERIFIED degrade test).
type fixtureDoer struct {
	t           *testing.T
	reqs        []*http.Request
	unavailable map[string]bool // path -> return 404
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	if d.unavailable[req.URL.Path] {
		return resp(404, `{"error":"not_found"}`), nil
	}
	var file string
	analyticsPath := workspacePath(defaultAnalyticsPath, "ws_eng")
	compliancePath := workspacePath(defaultCompliancePath, "ws_eng")
	switch req.URL.Path {
	case analyticsPath:
		file = "analytics.json"
	case compliancePath:
		lt := req.URL.Query().Get("event_type")
		file = "compliance_" + lt + ".json"
	case compliancePath + "/log_codex_1":
		file = "compliance_log_codex_1.jsonl"
	case compliancePath + "/log_security_1":
		file = "compliance_log_security_1.jsonl"
	case costsPath:
		file = "costs.json"
	case auditLogsPath:
		file = "audit_logs.json"
	case projectsPath:
		file = "projects.json"
	case adminKeysPath:
		file = "admin_api_keys.json"
	default:
		d.t.Fatalf("unexpected request path %q", req.URL.Path)
	}
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		d.t.Fatalf("read fixture %s: %v", file, err)
	}
	return resp(200, string(body)), nil
}

func resp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// captureSink records emitted observations.
type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func (s *captureSink) costs() []model.CostSample {
	var out []model.CostSample
	for _, o := range s.obs {
		if c, ok := o.(model.CostSample); ok {
			out = append(out, c)
		}
	}
	return out
}

func (s *captureSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range s.obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return out
}

func fixedClock() time.Time { return time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC) }

// newSource builds a credentialed Codex source over the fixture doer, applying any
// config overrides on top of a base that turns every stream OFF (a test enables only
// the stream it asserts on, for clean counts).
func newSource(t *testing.T, doer *fixtureDoer, over map[string]string) *Source {
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{
		"api_key":      "sk-codex-test",
		"workspace_id": "ws_eng",
		"analytics":    "false",
		"compliance":   "false",
		"audit":        "false",
		"costs":        "false",
	}
	for k, v := range over {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource {
		t.Fatalf("descriptor = %+v", d)
	}
	var sawSecret bool
	for _, f := range d.ConfigFields {
		if f.Key == "api_key" && f.Secret {
			sawSecret = true
		}
		// No subscription credential is ever accepted (ToS): there must be no such field.
		if strings.Contains(strings.ToLower(f.Key), "subscription") {
			t.Fatalf("descriptor must not expose a subscription credential field: %q", f.Key)
		}
	}
	if !sawSecret {
		t.Fatal("api_key must be declared as a secret config field")
	}
}

func TestGather_Analytics_EmitsCodexCostAndAdoption(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"analytics": "true"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	costs := sink.costs()
	if len(costs) != 1 {
		t.Fatalf("analytics emitted %d cost samples, want 1 (the zero-activity row is skipped)", len(costs))
	}
	c := costs[0]
	if c.ProviderRef != modelprovider.ProviderOpenAICodex {
		t.Fatalf("ProviderRef = %q, want openai-codex", c.ProviderRef)
	}
	// estimated_cost 1.25 USD -> 1_250_000 micro-USD (provider's own estimate, not derived).
	if c.CostMicroUSD != 1_250_000 {
		t.Fatalf("cost = %d micro-USD, want 1250000", c.CostMicroUSD)
	}
	if c.Provenance != model.ProvenanceEstimated {
		t.Fatalf("provenance = %q, want estimated", c.Provenance)
	}
	if c.CostType != costTypeCodex {
		t.Fatalf("cost_type = %q, want codex", c.CostType)
	}
	if c.Actor != "user_alice" { // attribute_email defaults false -> stable id, not PII
		t.Fatalf("actor = %q, want user_alice (id, not email)", c.Actor)
	}
	if strings.Contains(c.Actor, "@") {
		t.Fatalf("actor leaked an email: %q", c.Actor)
	}
	if c.WorkspaceRef != "ws_eng" {
		t.Fatalf("workspace = %q, want ws_eng", c.WorkspaceRef)
	}
	// folded input = input(120000) + cacheRead(30000) = 150000; output 45000.
	if c.InputTokens != 150000 || c.OutputTokens != 45000 {
		t.Fatalf("tokens = in %d out %d, want 150000/45000", c.InputTokens, c.OutputTokens)
	}
	if c.ModelRef != "gpt-5-codex" {
		t.Fatalf("model = %q, want gpt-5-codex", c.ModelRef)
	}
	if len(doer.reqs) == 0 || doer.reqs[0].URL.Path != workspacePath(defaultAnalyticsPath, "ws_eng") {
		t.Fatalf("analytics path = %q, want per-workspace path", doer.reqs[0].URL.Path)
	}
	q := doer.reqs[0].URL.Query()
	if q.Get("group_by") != "day" || q.Get("start_time") == "" || q.Get("end_time") == "" {
		t.Fatalf("analytics query = %s, want start_time/end_time/group_by=day", doer.reqs[0].URL.RawQuery)
	}

	// One adoption finding (the active row); the zero-activity row emits none.
	fs := sink.findings()
	if len(fs) != 1 {
		t.Fatalf("adoption findings = %d, want 1", len(fs))
	}
	if fs[0].SubjectKind != subjectAdoption || fs[0].Kind != "inventory" {
		t.Fatalf("adoption finding = %+v", fs[0])
	}
	if strings.Contains(fs[0].Title, "@") {
		t.Fatalf("adoption title leaked PII: %q", fs[0].Title)
	}
}

func TestGather_Analytics_AttributeEmailOptIn(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"analytics": "true", "attribute_email": "true"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if got := sink.costs()[0].Actor; got != "alice@example.com" {
		t.Fatalf("actor = %q, want alice@example.com when attribute_email=true", got)
	}
}

func TestGather_Compliance_EmitsHashedEvidence(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"compliance": "true"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()
	// 2 CODEX_LOG + 1 CODEX_SECURITY_LOG = 3 evidence findings.
	if len(fs) != 3 {
		t.Fatalf("compliance findings = %d, want 3", len(fs))
	}
	for _, f := range fs {
		if f.Kind != findingKindActivity {
			t.Fatalf("compliance finding kind = %q, want external_activity", f.Kind)
		}
		if f.SubjectKind != subjectCompliance {
			t.Fatalf("compliance subject kind = %q", f.SubjectKind)
		}
		if len(f.DetailHash) != 64 { // hex sha-256, never raw PII
			t.Fatalf("detail hash not a sha-256 hex: %q", f.DetailHash)
		}
		if strings.Contains(f.Title, "@") || strings.Contains(f.DetailHash, "@") {
			t.Fatalf("compliance evidence leaked PII: title=%q", f.Title)
		}
	}
}

func TestGather_WorkspaceIDMissingSkipsWorkspaceSurfaces(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"workspace_id": "", "analytics": "true", "compliance": "true"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].Severity != model.SeverityMedium || !strings.Contains(fs[0].Title, "workspace_id not configured") {
		t.Fatalf("workspace_id missing finding = %+v", fs)
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("workspace surfaces must be skipped without workspace_id, got %d requests", len(doer.reqs))
	}
}

func TestGather_CompliancePromptScanStructuralOnly(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"compliance": "true", "compliance_prompt_scan": "true"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var medium, low bool
	rawSecret := "sk-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	rawInjection := "ignore previous instructions"
	for _, f := range sink.findings() {
		for _, field := range []string{f.Title, f.DetailHash, f.SubjectRef, f.SubjectKind, f.Kind} {
			if strings.Contains(field, rawSecret) || strings.Contains(field, rawInjection) {
				t.Fatalf("prompt scan leaked raw content in finding %+v", f)
			}
		}
		if f.SubjectKind != subjectComplianceContent {
			continue
		}
		if strings.Contains(f.Title, "secret-shape=1") && f.Severity == model.SeverityMedium {
			medium = true
		}
		if strings.Contains(f.Title, "injection-markers=") && f.Severity == model.SeverityLow {
			low = true
		}
	}
	if !medium || !low {
		t.Fatalf("expected MEDIUM secret-shape and LOW textscan findings, got %+v", sink.findings())
	}
}

func TestGather_Audit_EmitsEvidence(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"audit": "true"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()
	if len(fs) != 2 {
		t.Fatalf("audit findings = %d, want 2", len(fs))
	}
	for _, f := range fs {
		if f.Kind != findingKindActivity || f.SubjectKind != subjectAudit {
			t.Fatalf("audit finding = %+v", f)
		}
		if !strings.HasPrefix(f.SubjectRef, "audit_log-") {
			t.Fatalf("audit subject ref = %q", f.SubjectRef)
		}
		if strings.Contains(f.Title, "@") {
			t.Fatalf("audit title leaked PII: %q", f.Title)
		}
	}
}

func TestGather_Costs_OptInBilled(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"costs": "true", "project_id": "proj_codex"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	costs := sink.costs()
	if len(costs) != 1 {
		t.Fatalf("billed cost samples = %d, want 1", len(costs))
	}
	c := costs[0]
	if c.Provenance != model.ProvenanceBilled {
		t.Fatalf("provenance = %q, want billed", c.Provenance)
	}
	if c.CostMicroUSD != 3_500_000 { // $3.50
		t.Fatalf("billed cost = %d, want 3500000", c.CostMicroUSD)
	}
	if c.CostType != "Codex" || c.WorkspaceRef != "proj_codex" {
		t.Fatalf("billed sample attribution = %+v", c)
	}
	if c.ProviderRef != modelprovider.ProviderOpenAICodex {
		t.Fatalf("provider = %q", c.ProviderRef)
	}
}

func TestGather_OfflineNoCredentialEmitsNothing(t *testing.T) {
	s := New()
	s.doer = &fixtureDoer{t: t}
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("offline gather emitted %d observations, want 0", len(sink.obs))
	}
}

func TestGather_UnavailableSurfaceDegradesHonestly(t *testing.T) {
	doer := &fixtureDoer{t: t, unavailable: map[string]bool{workspacePath(defaultAnalyticsPath, "ws_eng"): true}}
	s := newSource(t, doer, map[string]string{"analytics": "true"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather must NOT fail on a sales-gated surface; got %v", err)
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].SubjectKind != subjectSurface {
		t.Fatalf("want 1 posture finding for the unavailable surface, got %+v", fs)
	}
	if fs[0].Severity != model.SeverityMedium || !strings.Contains(fs[0].Title, "unavailable") {
		t.Fatalf("posture finding = %+v", fs[0])
	}
	if len(sink.costs()) != 0 {
		t.Fatal("an unavailable analytics surface must emit no cost samples")
	}
}

func TestGather_ReadOnlyBearerAuth(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"analytics": "true", "compliance": "true", "audit": "true", "costs": "true"})
	if err := s.Gather(context.Background(), &captureSink{}); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(doer.reqs) == 0 {
		t.Fatal("no requests issued")
	}
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-codex-test" {
			t.Fatalf("bearer credential not sent on %s: %q", r.URL.Path, r.Header.Get("Authorization"))
		}
	}
}

func TestSnapshot_LiveCatalog(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if cat.Provider.Ref != modelprovider.ProviderOpenAICodex || cat.Provider.Kind != modelprovider.KindHostedAPI {
		t.Fatalf("provider = %+v", cat.Provider)
	}
	if len(cat.Models) != len(declaredModelIDs) {
		t.Fatalf("models = %d, want %d", len(cat.Models), len(declaredModelIDs))
	}
	m, ok := cat.FindModel("gpt-5-codex")
	if !ok || m.Pricing == nil || m.Pricing.InputPerMTokUSD != 1.25 {
		t.Fatalf("gpt-5-codex not enriched: %+v", m)
	}
	if m.CapabilitySource != "declared" {
		t.Fatalf("capability source = %q, want declared", m.CapabilitySource)
	}
	if !m.HasCapability(modelprovider.CapToolUse) {
		t.Fatal("gpt-5-codex missing tool_use capability")
	}
	// Workspace + key inventory (the access-token identity surface), metadata only.
	if len(cat.Workspaces) != 2 {
		t.Fatalf("workspaces = %d, want 2", len(cat.Workspaces))
	}
	if len(cat.Keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(cat.Keys))
	}
	if k := cat.Keys[0]; k.Hint == "" || !strings.Contains(k.Hint, "...") {
		t.Fatalf("key hint looks unmasked: %q", k.Hint)
	}
}

func TestSnapshot_OfflineCatalog(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(cat.Models) != len(declaredModelIDs) {
		t.Fatalf("offline models = %d, want %d", len(cat.Models), len(declaredModelIDs))
	}
	if len(cat.Keys) != 0 || len(cat.Workspaces) != 0 {
		t.Fatal("offline catalog must not contain key/workspace inventory")
	}
}

func TestPricingFor_LongestPrefixMatch(t *testing.T) {
	cases := []struct {
		id        string
		wantIn    float64
		wantMatch bool
	}{
		{"gpt-5-codex", 1.25, true},
		{"gpt-5-codex-2026-05-01", 1.25, true},
		{"gpt-5-codex-mini", 0.25, true}, // mini beats gpt-5-codex
		{"codex-mini-latest", 1.50, true},
		{"gpt-4o", 0, false}, // not a codex model
	}
	for _, c := range cases {
		p, _, ok := pricingFor(c.id)
		if ok != c.wantMatch {
			t.Fatalf("%s: match = %v, want %v", c.id, ok, c.wantMatch)
		}
		if ok && p.InputPerMTokUSD != c.wantIn {
			t.Fatalf("%s: input price = %v, want %v", c.id, p.InputPerMTokUSD, c.wantIn)
		}
	}
}

func TestGPT56PricingAndCodexRetirement(t *testing.T) {
	p, _, ok := pricingFor("gpt-5.6-sol")
	if !ok {
		t.Fatal("pricingFor(gpt-5.6-sol) did not match")
	}
	if p.InputPerMTokUSD != 5.00 || p.OutputPerMTokUSD != 30.00 || p.CacheWritePerMTokUSD != 6.25 {
		t.Fatalf("gpt-5.6-sol pricing = %+v, want input/output/cache-write 5.00/30.00/6.25", p)
	}

	deprecated := buildModel("gpt-5-codex", "GPT-5 Codex")
	if !deprecated.Deprecated || len(deprecated.Retirements) != 1 {
		t.Fatalf("gpt-5-codex retirement not applied: %+v", deprecated)
	}
	retirement := deprecated.Retirements[0]
	if retirement.RetiresOn != "2026-07-23" || retirement.ReplacementRef != "gpt-5.6" {
		t.Fatalf("gpt-5-codex retirement = %+v, want retirement 2026-07-23 and replacement gpt-5.6", retirement)
	}

	current := buildModel("gpt-5.6-sol", "GPT-5.6 Sol")
	if current.Deprecated {
		t.Fatalf("gpt-5.6-sol must not be deprecated: %+v", current.Retirements)
	}
}

func TestDollarsToMicroUSD(t *testing.T) {
	cases := []struct {
		in   float64
		want int64
	}{
		{1.25, 1_250_000}, {0, 0}, {-5, 0}, {0.000001, 1}, {3.5, 3_500_000},
	}
	for _, c := range cases {
		if got := dollarsToMicroUSD(c.in); got != c.want {
			t.Fatalf("dollarsToMicroUSD(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
