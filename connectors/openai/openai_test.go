// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openai

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

// fixtureDoer multiplexes by request path to the recorded OpenAI org API fixtures,
// and records every request so a test can assert the connector is read-only.
type fixtureDoer struct {
	t    *testing.T
	reqs []*http.Request
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	var file string
	switch {
	case req.URL.Path == "/v1/organization/usage/completions":
		file = "usage_completions.json"
	case req.URL.Path == "/v1/organization/usage/moderations":
		file = "usage_moderations.json"
	case req.URL.Path == "/v1/models":
		file = "models.json"
	case req.URL.Path == "/v1/organization/admin_api_keys":
		file = "admin_api_keys.json"
	case req.URL.Path == "/v1/organization/projects":
		file = "projects.json"
	case req.URL.Path == "/v1/organization/users":
		file = "org_users.json"
	case req.URL.Path == "/v1/organization/audit_logs":
		file = "audit_logs.json"
	case req.URL.Path == "/v1/organization/data_retention":
		file = "data_retention.json"
	case strings.HasPrefix(req.URL.Path, "/v1/organization/projects/") && strings.HasSuffix(req.URL.Path, "/data_retention"):
		parts := strings.Split(req.URL.Path, "/")
		projID := parts[4]
		file = "data_retention_" + projID + ".json"
		body, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			// Project-specific fixture not found: return same as org (no diff).
			body, err = os.ReadFile(filepath.Join("testdata", "data_retention.json"))
			if err != nil {
				d.t.Fatalf("read fixture: %v", err)
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	case req.URL.Path == "/v1/organization/costs":
		file = "costs.json"
	default:
		d.t.Fatalf("unexpected request path %q", req.URL.Path)
	}
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		d.t.Fatalf("read fixture %s: %v", file, err)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
}

// captureSink records emitted observations.
type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func fixedClock() time.Time { return time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC) }

func newLive(t *testing.T) (*Source, *fixtureDoer) {
	doer := &fixtureDoer{t: t}
	s := New()
	s.doer = doer
	s.now = fixedClock
	cfg := sdk.Config{Settings: map[string]string{"api_key": "sk-openai-admin-test", "lookback": "24h"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, doer
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
	}
	if !sawSecret {
		t.Fatal("api_key must be declared as a secret config field")
	}
}

func TestGather_EmitsDerivedCostSamples(t *testing.T) {
	s, doer := newLive(t)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// Emitted: 3 CostSamples (gpt-4o, gpt-4o-mini, o3-pro; the empty-model row is
	// skipped) + FindingReports (safety_posture + governance: org_users inventory,
	// audit_log external_activity, data_retention posture).
	byModel := map[string]model.CostSample{}
	costs := 0
	var findings []model.FindingReport
	for _, o := range sink.obs {
		switch v := o.(type) {
		case model.CostSample:
			costs++
			if v.ProviderRef != modelprovider.ProviderOpenAI {
				t.Fatalf("ProviderRef = %q", v.ProviderRef)
			}
			if v.WorkspaceRef != "proj_test1" {
				t.Fatalf("WorkspaceRef = %q, want proj_test1", v.WorkspaceRef)
			}
			byModel[v.ModelRef] = v
		case model.FindingReport:
			findings = append(findings, v)
		default:
			t.Fatalf("observation is %T, want CostSample or FindingReport", o)
		}
	}
	if costs != 3 {
		t.Fatalf("emitted %d cost samples, want 3", costs)
	}
	// Verify we have at least the moderation posture finding.
	var sawModeration bool
	for _, f := range findings {
		if f.Kind == "safety_posture" && f.SubjectKind == "openai.moderation" {
			sawModeration = true
		}
	}
	if !sawModeration {
		t.Fatal("missing safety_posture / openai.moderation finding")
	}

	// Golden gpt-4o: input_tokens=1000 (includes 200 cached), output=500.
	// uncached = 1000-200 = 800; cacheRead = 200.
	// cost = 800*2.50 + 200*1.25 + 500*10.00 = 2000 + 250 + 5000 = 7250 micro-USD.
	// folded InputTokens = uncached(800) + cacheRead(200) = 1000.
	gpt4o := byModel["gpt-4o"]
	if gpt4o.CostMicroUSD != 7250 {
		t.Fatalf("gpt-4o cost = %d, want 7250", gpt4o.CostMicroUSD)
	}
	if gpt4o.InputTokens != 1000 || gpt4o.OutputTokens != 500 {
		t.Fatalf("gpt-4o tokens = in %d out %d, want 1000/500", gpt4o.InputTokens, gpt4o.OutputTokens)
	}
	if gpt4o.ServiceTier != "default" {
		t.Fatalf("gpt-4o service tier = %q, want default", gpt4o.ServiceTier)
	}
	// start_time 1717200000 = 2024-06-01T00:00:00Z.
	if !gpt4o.OccurredAt.Equal(time.Unix(1717200000, 0).UTC()) {
		t.Fatalf("gpt-4o OccurredAt = %v", gpt4o.OccurredAt)
	}

	// gpt-4o-mini: input=4000, cached=0, output=1000.
	// cost = 4000*0.15 + 1000*0.60 = 600 + 600 = 1200 micro-USD.
	mini := byModel["gpt-4o-mini"]
	if mini.CostMicroUSD != 1200 {
		t.Fatalf("gpt-4o-mini cost = %d, want 1200", mini.CostMicroUSD)
	}
	if mini.InputTokens != 4000 {
		t.Fatalf("gpt-4o-mini InputTokens = %d, want 4000", mini.InputTokens)
	}
	if mini.ServiceTier != "flex" {
		t.Fatalf("gpt-4o-mini service tier = %q, want flex", mini.ServiceTier)
	}

	// o3-pro has no family match: usage recorded with an underived (0) cost.
	o3 := byModel["o3-pro"]
	if o3.CostMicroUSD != 0 {
		t.Fatalf("o3-pro cost = %d, want 0 (unknown model not guessed)", o3.CostMicroUSD)
	}
	if o3.InputTokens != 800 || o3.OutputTokens != 300 {
		t.Fatalf("o3-pro tokens = in %d out %d, want 800/300", o3.InputTokens, o3.OutputTokens)
	}

	// Read-only: every request was a GET with the bearer admin auth header.
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer sk-openai-admin-test" {
			t.Fatalf("bearer admin key header not sent, got %q", r.Header.Get("Authorization"))
		}
	}
	// The usage request carried a start_time and project/model/service_tier group_by dimensions.
	uReq := doer.reqs[0]
	if uReq.URL.Query().Get("start_time") == "" {
		t.Fatal("usage request missing start_time")
	}
	groups := uReq.URL.Query()["group_by[]"]
	if len(groups) != 3 || groups[0] != "model" || groups[1] != "project_id" || groups[2] != "service_tier" {
		t.Fatalf("usage request group_by[] = %v, want [model project_id service_tier]", groups)
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

func TestSnapshot_LiveCatalog(t *testing.T) {
	s, _ := newLive(t)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if cat.Provider.Ref != modelprovider.ProviderOpenAI || cat.Provider.Kind != modelprovider.KindHostedAPI {
		t.Fatalf("provider = %+v", cat.Provider)
	}
	if len(cat.Models) != 6 {
		t.Fatalf("models = %d, want 6", len(cat.Models))
	}
	gpt4o, ok := cat.FindModel("gpt-4o")
	if !ok || gpt4o.Pricing == nil || gpt4o.Pricing.InputPerMTokUSD != 2.50 {
		t.Fatalf("gpt-4o model not enriched: %+v", gpt4o)
	}
	if gpt4o.Deprecated {
		t.Fatalf("gpt-4o should not be deprecated: %+v", gpt4o.Retirements)
	}
	if gpt4o.Pricing.CacheReadPerMTokUSD != 1.25 || gpt4o.Pricing.CacheWritePerMTokUSD != 0 {
		t.Fatalf("gpt-4o cache pricing = read %v write %v, want 1.25/0", gpt4o.Pricing.CacheReadPerMTokUSD, gpt4o.Pricing.CacheWritePerMTokUSD)
	}
	if !gpt4o.HasCapability(modelprovider.CapVision) || !gpt4o.HasCapability(modelprovider.CapPromptCaching) {
		t.Fatal("gpt-4o missing a declared capability")
	}
	// CreatedAt from the Unix-seconds models fixture.
	if !gpt4o.CreatedAt.Equal(time.Unix(1715000000, 0).UTC()) {
		t.Fatalf("gpt-4o CreatedAt = %v", gpt4o.CreatedAt)
	}
	// An unknown model keeps nil pricing rather than a guessed price.
	o3, _ := cat.FindModel("o3-pro")
	if o3.Pricing != nil {
		t.Fatalf("unknown model got a guessed price: %+v", o3.Pricing)
	}
	if len(o3.Capabilities) != 0 {
		t.Fatalf("unknown model got guessed capabilities: %+v", o3.Capabilities)
	}
	gpt55, ok := cat.FindModel("gpt-5.5-2026-04-23")
	if !ok || gpt55.Pricing == nil {
		t.Fatalf("gpt-5.5 snapshot model missing pricing: %+v", gpt55)
	}
	if gpt55.Pricing.InputPerMTokUSD != 5.00 || gpt55.Pricing.CacheReadPerMTokUSD != 0.50 ||
		gpt55.Pricing.OutputPerMTokUSD != 30.00 {
		t.Fatalf("gpt-5.5 pricing = %+v, want 5.00/0.50/30.00", gpt55.Pricing)
	}
	if gpt55.ContextWindow != 1050000 || gpt55.MaxOutputTokens != 128000 {
		t.Fatalf("gpt-5.5 limits = ctx %d out %d, want 1050000/128000", gpt55.ContextWindow, gpt55.MaxOutputTokens)
	}
	dep, ok := cat.FindModel("o3-2025-04-16")
	if !ok || !dep.Deprecated || len(dep.Retirements) != 1 {
		t.Fatalf("deprecated model missing retirement metadata: %+v", dep)
	}
	r := dep.Retirements[0]
	if r.Surface != model.GatewayDirect || r.DeprecatedOn != "2026-06-11" ||
		r.RetiresOn != "2026-12-11" || r.ReplacementRef != "gpt-5.5" || r.AsOf != pricingAsOf {
		t.Fatalf("o3 retirement = %+v", r)
	}

	// Inventory carries metadata only — a masked hint, never a usable key value.
	if len(cat.Keys) != 2 {
		t.Fatalf("keys = %d, want 2", len(cat.Keys))
	}
	for _, k := range cat.Keys {
		if k.Hint == "" {
			t.Fatalf("key %s missing hint", k.ID)
		}
		if !strings.Contains(k.Hint, "...") && !strings.Contains(k.Hint, "…") {
			t.Fatalf("key hint looks unmasked: %q", k.Hint)
		}
	}
	if len(cat.Workspaces) != 2 {
		t.Fatalf("workspaces = %d, want 2", len(cat.Workspaces))
	}
	var archived int
	for _, w := range cat.Workspaces {
		if w.Archived {
			archived++
		}
	}
	if archived != 1 {
		t.Fatalf("archived workspaces = %d, want 1", archived)
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
	if len(cat.Keys) != 0 {
		t.Fatal("offline catalog must not contain key inventory")
	}
	// The offline gpt-4.1 is enriched with the gpt-4.1 family list price.
	m, ok := cat.FindModel("gpt-4.1")
	if !ok || m.Pricing == nil || m.Pricing.InputPerMTokUSD != 2.00 {
		t.Fatalf("offline gpt-4.1 not enriched: %+v", m)
	}
}

func TestSnapshot_AzureProviderRef(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"provider": modelprovider.ProviderAzureOpenAI,
		"base_url": "https://my-resource.openai.azure.com",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if cat.Provider.Ref != modelprovider.ProviderAzureOpenAI {
		t.Fatalf("provider ref = %q, want azure-openai", cat.Provider.Ref)
	}
	if cat.Provider.BaseURL != "https://my-resource.openai.azure.com" {
		t.Fatalf("provider base URL = %q", cat.Provider.BaseURL)
	}
	// Offline (no api_key): declared models carry the azure provider ref.
	for _, m := range cat.Models {
		if m.ProviderRef != modelprovider.ProviderAzureOpenAI {
			t.Fatalf("model %s ProviderRef = %q, want azure-openai", m.Ref, m.ProviderRef)
		}
	}
}

func TestPricingFor_LongestPrefixMatch(t *testing.T) {
	cases := []struct {
		id        string
		wantIn    float64
		wantMatch bool
	}{
		{"gpt-5.5", 5.00, true},
		{"gpt-5.5-2026-04-23", 5.00, true},
		{"gpt-4o", 2.50, true},
		{"gpt-4o-2024-08-06", 2.50, true},
		{"gpt-4o-mini", 0.15, true},            // mini beats gpt-4o
		{"gpt-4o-mini-2024-07-18", 0.15, true}, // versioned mini still mini
		{"gpt-4.1", 2.00, true},
		{"gpt-4.1-mini", 0.40, true},  // mini beats gpt-4.1
		{"gpt-4.1-nano", 0.10, true},  // nano beats gpt-4.1
		{"o3-pro", 0, false},          // o-series: no family match
		{"claude-opus-4-8", 0, false}, // other vendor: no match
	}
	for _, c := range cases {
		p, _, _, _, ok := pricingFor(c.id)
		if ok != c.wantMatch {
			t.Fatalf("%s: match = %v, want %v", c.id, ok, c.wantMatch)
		}
		if ok && p.InputPerMTokUSD != c.wantIn {
			t.Fatalf("%s: input price = %v, want %v", c.id, p.InputPerMTokUSD, c.wantIn)
		}
	}
}
