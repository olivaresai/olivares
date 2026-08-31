// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

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

// fixtureDoer multiplexes by request path to the recorded Admin API fixtures, and
// records every request so a test can assert the connector is read-only.
type fixtureDoer struct {
	t    *testing.T
	reqs []*http.Request
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	var file string
	switch {
	case req.URL.Path == "/v1/organizations/cost_report":
		file = "cost_report.json"
	case req.URL.Path == "/v1/organizations/usage_report/claude_code":
		file = "claude_code.json"
	case strings.HasPrefix(req.URL.Path, "/v1/organizations/usage_report"):
		file = "usage_report.json"
	case req.URL.Path == "/v1/models":
		file = "models.json"
	case req.URL.Path == "/v1/organizations/api_keys":
		file = "api_keys.json"
	case req.URL.Path == "/v1/organizations/workspaces":
		file = "workspaces.json"
	case req.URL.Path == "/v1/organizations/external_keys":
		file = "external_keys.json"
	case req.URL.Path == "/v1/organizations/rate_limits":
		file = "rate_limits.json"
	case req.URL.Path == "/v1/organizations/workspaces/wrkspc_01/rate_limits":
		file = "rate_limits_workspace_wrkspc_01.json"
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
	cfg := sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test", "lookback": "24h"}}
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
		if f.Key == "admin_key" && f.Secret {
			sawSecret = true
		}
	}
	if !sawSecret {
		t.Fatal("admin_key must be declared as a secret config field")
	}
}

func TestGather_EmitsDerivedCostSamples(t *testing.T) {
	s, doer := newLive(t)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// The cost stream is two derived (estimated) usage rows + two billed cost_report
	// rows. Gather also emits governance/deprecation FindingReports (ANT2-03/05/16);
	// the cost assertions filter to the CostSamples so they are robust to those.
	estByModel := map[string]model.CostSample{}
	var billed []model.CostSample
	var costCount int
	for _, o := range sink.obs {
		cs, ok := o.(model.CostSample)
		if !ok {
			continue // a governance/deprecation finding — asserted separately below
		}
		costCount++
		if cs.ProviderRef != modelprovider.ProviderAnthropic {
			t.Fatalf("ProviderRef = %q", cs.ProviderRef)
		}
		switch cs.Provenance {
		case model.ProvenanceEstimated:
			estByModel[cs.ModelRef] = cs
		case model.ProvenanceBilled:
			billed = append(billed, cs)
		default:
			t.Fatalf("sample has no provenance: %+v", cs)
		}
	}
	if costCount != 4 {
		t.Fatalf("emitted %d cost samples, want 4", costCount)
	}

	sonnet := estByModel["claude-sonnet-4-6"]
	// 1000*3 + 800*15 + 400*3.75 (5m) + 100*6 (1h) + 2000*0.30 = 17700 micro-USD.
	// The 1h write tier is now priced at 2× base (6/MTok), not the 5m 1.25× rate.
	if sonnet.CostMicroUSD != 17700 {
		t.Fatalf("sonnet cost = %d, want 17700 (1h write priced distinctly)", sonnet.CostMicroUSD)
	}
	if sonnet.InputTokens != 3500 || sonnet.OutputTokens != 800 {
		t.Fatalf("sonnet tokens = in %d out %d, want 3500/800", sonnet.InputTokens, sonnet.OutputTokens)
	}
	// Cache breakdown is carried distinctly per TTL.
	if sonnet.CacheReadTokens != 2000 || sonnet.CacheCreation5mTokens != 400 || sonnet.CacheCreation1hTokens != 100 {
		t.Fatalf("sonnet cache split = read %d / 5m %d / 1h %d, want 2000/400/100",
			sonnet.CacheReadTokens, sonnet.CacheCreation5mTokens, sonnet.CacheCreation1hTokens)
	}
	// Attribution dimensions flow through.
	if sonnet.WorkspaceRef != "wrkspc_01" || sonnet.APIKeyRef != "apikey_01" {
		t.Fatalf("sonnet attribution = ws %q key %q, want wrkspc_01/apikey_01", sonnet.WorkspaceRef, sonnet.APIKeyRef)
	}
	if sonnet.Gateway != model.GatewayDirect {
		t.Fatalf("sonnet gateway = %q, want direct", sonnet.Gateway)
	}
	if !sonnet.OccurredAt.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("sonnet OccurredAt = %v", sonnet.OccurredAt)
	}

	haiku := estByModel["claude-haiku-4-5"]
	// Haiku 4.5 = $1/$5: 10000*1 + 2000*5 = 20000 micro-USD.
	if haiku.CostMicroUSD != 20000 {
		t.Fatalf("haiku cost = %d, want 20000", haiku.CostMicroUSD)
	}

	// Billed cost_report: amounts are decimal cents → micro-USD (1 cent = 10_000 µUSD).
	billedByType := map[string]model.CostSample{}
	for _, b := range billed {
		billedByType[b.CostType] = b
	}
	tok := billedByType["tokens"]
	if tok.CostMicroUSD != 12_345_000 { // "1234.50" cents = $12.345 = 12_345_000 µUSD
		t.Fatalf("billed token cost = %d, want 12345000", tok.CostMicroUSD)
	}
	if tok.ModelRef != "claude-sonnet-4-6" || tok.WorkspaceRef != "wrkspc_01" || tok.ServiceTier != "standard" {
		t.Fatalf("billed token attribution = %+v", tok)
	}
	ws := billedByType["web_search"]
	if ws.CostMicroUSD != 500_000 { // "50.00" cents = $0.50 = 500_000 µUSD
		t.Fatalf("billed web_search cost = %d, want 500000", ws.CostMicroUSD)
	}

	// Read-only: every request was a GET with the admin auth + version headers.
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s", r.Method)
		}
		if r.Header.Get("x-api-key") != "sk-ant-admin-test" {
			t.Fatalf("admin key header not sent")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Fatalf("anthropic-version header not sent")
		}
	}
	// The usage request carried a starting_at and the full attribution group_by; the
	// cost request used daily granularity. Locate them by path (governance pulls —
	// external_keys/workspaces/rate_limits — now precede the usage pull).
	var uReq, cReq *http.Request
	for _, r := range doer.reqs {
		switch r.URL.Path {
		case "/v1/organizations/usage_report/messages":
			uReq = r
		case "/v1/organizations/cost_report":
			cReq = r
		}
	}
	if uReq == nil || cReq == nil {
		t.Fatalf("missing usage (%v) or cost (%v) request", uReq != nil, cReq != nil)
	}
	if uReq.URL.Query().Get("starting_at") == "" {
		t.Fatal("usage request missing starting_at")
	}
	gb := uReq.URL.Query()["group_by[]"]
	if !containsAll(gb, "model", "workspace_id", "api_key_id", "service_tier", "context_window", "inference_geo") {
		t.Fatalf("usage group_by missing dimensions: %v", gb)
	}
	if cReq.URL.Query().Get("bucket_width") != "1d" {
		t.Fatalf("cost_report bucket_width = %q, want 1d", cReq.URL.Query().Get("bucket_width"))
	}
}

// containsAll reports whether haystack contains every needle.
func containsAll(haystack []string, needles ...string) bool {
	set := map[string]bool{}
	for _, h := range haystack {
		set[h] = true
	}
	for _, n := range needles {
		if !set[n] {
			return false
		}
	}
	return true
}

func TestGather_ClaudeCodeAnalyticsCostFeed(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := New()
	s.doer = doer
	s.now = fixedClock
	// Enable the Claude Code feed; disable cost_report to isolate this assertion.
	cfg := sdk.Config{Settings: map[string]string{
		"admin_key": "sk-ant-admin-test", "cost_report": "false", "claude_code": "true",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	byActor := map[string]model.CostSample{}
	for _, o := range sink.obs {
		if cs, ok := o.(model.CostSample); ok && cs.Actor != "" {
			byActor[cs.Actor] = cs
		}
	}
	dev := byActor["dev@example.com"]
	if dev.ModelRef != "claude-sonnet-4-6" || dev.Provenance != model.ProvenanceEstimated {
		t.Fatalf("dev sample = %+v", dev)
	}
	// estimated_cost.amount 186 cents = $1.86 = 1_860_000 µUSD.
	if dev.CostMicroUSD != 1_860_000 {
		t.Errorf("dev cost = %d, want 1860000", dev.CostMicroUSD)
	}
	// InputTokens folds input+cache (1000+200+100); cache split carried.
	if dev.InputTokens != 1300 || dev.CacheReadTokens != 200 || dev.CacheCreation5mTokens != 100 {
		t.Errorf("dev tokens = in %d cr %d cc5m %d, want 1300/200/100", dev.InputTokens, dev.CacheReadTokens, dev.CacheCreation5mTokens)
	}
	if dev.Gateway != model.GatewayDirect {
		t.Errorf("dev gateway = %q, want direct", dev.Gateway)
	}
	var ccReq *http.Request
	for _, r := range doer.reqs {
		if r.URL.Path == claudeCodePath {
			ccReq = r
			break
		}
	}
	if ccReq == nil {
		t.Fatal("missing Claude Code Analytics request")
	}
	if got := ccReq.URL.Query().Get("limit"); got != "1000" {
		t.Fatalf("claude_code limit = %q, want 1000", got)
	}
	// The api customer (ci-bot) is NOT emitted: its spend is already in usage_report,
	// so emitting it here would double-count the estimated stream.
	if _, ok := byActor["ci-bot"]; ok {
		t.Error("api-customer Claude Code cost must NOT be emitted (avoids double-count vs usage_report)")
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
	if cat.Provider.Ref != modelprovider.ProviderAnthropic || cat.Provider.Kind != modelprovider.KindHostedAPI {
		t.Fatalf("provider = %+v", cat.Provider)
	}
	if len(cat.Models) != 4 {
		t.Fatalf("models = %d, want 4", len(cat.Models))
	}
	sonnet, ok := cat.FindModel("claude-sonnet-4-6")
	if !ok || sonnet.Pricing == nil || sonnet.Pricing.InputPerMTokUSD != 3 {
		t.Fatalf("sonnet model not enriched: %+v", sonnet)
	}
	if !sonnet.HasCapability(modelprovider.CapComputerUse) {
		t.Fatal("sonnet missing a declared Claude-stack capability")
	}
	// An unknown model keeps nil pricing rather than a guessed price.
	exp, _ := cat.FindModel("claude-experimental-x")
	if exp.Pricing != nil {
		t.Fatalf("unknown model got a guessed price: %+v", exp.Pricing)
	}

	// Inventory carries metadata only — a masked hint, never a usable key value.
	if len(cat.Keys) != 2 {
		t.Fatalf("keys = %d, want 2", len(cat.Keys))
	}
	for _, k := range cat.Keys {
		if k.Hint == "" {
			t.Fatalf("key %s missing hint", k.ID)
		}
		if strings.Count(k.Hint, "...") == 0 && !strings.Contains(k.Hint, "…") {
			// hints are masked; a full-looking key would be a leak
			if len(k.Hint) > 24 {
				t.Fatalf("key hint looks unmasked: %q", k.Hint)
			}
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
}

func TestPricingFor_FamilyPrefix(t *testing.T) {
	cases := []struct {
		id        string
		wantIn    float64
		wantMatch bool
	}{
		{"claude-opus-4-8", 5, true},  // current Opus = $5 (not the deprecated $15)
		{"claude-opus-4-1", 15, true}, // deprecated Opus 4.1 keeps $15
		{"claude-opus-4-0", 15, true}, // deprecated Opus 4.0 keeps $15
		{"claude-sonnet-5", 3, true},  // Sonnet 5 keeps the durable Sonnet $3/$15 list price
		{"claude-sonnet-4-6", 3, true},
		{"claude-haiku-4-5", 1, true},             // current Haiku 4.5 = $1 (not retired $0.80)
		{"claude-3-5-haiku-20241022", 0.80, true}, // retired Haiku 3.5 keeps $0.80
		{"claude-3-opus-20240229", 15, true},
		{"claude-fable-5", 10, true},        // Fable 5 = $10/$50 (launch 2026-06-09)
		{"claude-mythos-5", 10, true},       // Mythos 5 shares the published Fable 5 pricing
		{"claude-mythos-preview", 0, false}, // preview has NO published list price — never guessed
		{"gpt-4o", 0, false},
	}
	for _, c := range cases {
		p, _, _, ok := pricingFor(c.id)
		if ok != c.wantMatch {
			t.Fatalf("%s: match = %v, want %v", c.id, ok, c.wantMatch)
		}
		if ok && p.InputPerMTokUSD != c.wantIn {
			t.Fatalf("%s: input price = %v, want %v", c.id, p.InputPerMTokUSD, c.wantIn)
		}
	}
}

func TestPricingFor_Sonnet5(t *testing.T) {
	p, ctxWin, maxOut, ok := pricingFor("claude-sonnet-5")
	if !ok {
		t.Fatal("claude-sonnet-5 has no pricing family")
	}
	if ctxWin != 1_000_000 || maxOut != 128_000 {
		t.Fatalf("sonnet-5 window/output = %d/%d, want 1M/128K", ctxWin, maxOut)
	}
	if p.InputPerMTokUSD != 3 || p.OutputPerMTokUSD != 15 ||
		p.CacheWritePerMTokUSD != 3.75 || p.CacheWrite1hPerMTokUSD != 6 ||
		p.CacheReadPerMTokUSD != 0.30 || p.AsOf != "2026-07-03" {
		t.Fatalf("sonnet-5 pricing = %+v, want 3/15 + cache 3.75/6/0.30 as of 2026-07-03", p)
	}
}

// TestPricingFor_FableExactDerivation pins the Fable 5 launch price book end to end
// ($10/$50, cache write $12.50 5m / $20 1h, cache read $1 — pricing page,
// 2026-06-09): every tier exercised once, exact micro-USD ($P/MTok == P µUSD/token).
func TestPricingFor_FableExactDerivation(t *testing.T) {
	p, ctxWin, maxOut, ok := pricingFor("claude-fable-5")
	if !ok {
		t.Fatal("claude-fable-5 has no pricing family")
	}
	if ctxWin != 1_000_000 || maxOut != 128_000 {
		t.Fatalf("fable window/output = %d/%d, want 1M/128K", ctxWin, maxOut)
	}
	cost := p.DeriveCostMicroUSD(modelprovider.Usage{
		ModelRef:    "claude-fable-5",
		InputTokens: 1000, OutputTokens: 2000,
		CacheCreation5mTokens: 400, CacheCreation1hTokens: 100, CacheReadTokens: 1000,
	})
	// 1000*10 + 2000*50 + 400*12.50 + 100*20 + 1000*1 = 10000+100000+5000+2000+1000.
	if want := int64(118_000); cost != want {
		t.Fatalf("fable derivation = %d µUSD, want %d", cost, want)
	}
}

// TestGather_ShadowAuthDetector proves the shadow-auth detector: the
// api-billed developer (ci-bot) yields ONE Medium governance finding naming the
// actor — never usage figures — and the subscription developer yields none. The
// toggle suppresses it for orgs that intentionally run Claude Code on API billing.
func TestGather_ShadowAuthDetector(t *testing.T) {
	gather := func(t *testing.T, settings map[string]string) *captureSink {
		t.Helper()
		s := New()
		s.doer = &fixtureDoer{t: t}
		s.now = fixedClock
		if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
			t.Fatalf("Open: %v", err)
		}
		sink := &captureSink{}
		if err := s.Gather(context.Background(), sink); err != nil {
			t.Fatalf("Gather: %v", err)
		}
		return sink
	}

	shadowFindings := func(sink *captureSink) []model.FindingReport {
		var out []model.FindingReport
		for _, o := range sink.obs {
			if f, ok := o.(model.FindingReport); ok && strings.Contains(f.Title, "shadow auth") {
				out = append(out, f)
			}
		}
		return out
	}

	// Default: detector ON with the feed.
	sink := gather(t, map[string]string{
		"admin_key": "sk-ant-admin-test", "cost_report": "false", "claude_code": "true",
	})
	fs := shadowFindings(sink)
	if len(fs) != 1 {
		t.Fatalf("shadow-auth findings = %d, want 1 (the api-billed ci-bot)", len(fs))
	}
	f := fs[0]
	if f.Severity != model.SeverityMedium || f.Kind != findingKindGovernance ||
		f.SubjectKind != subjectClaudeCodeDeveloper || f.SubjectRef != "ci-bot" {
		t.Errorf("shadow-auth finding = %+v", f)
	}
	if !strings.Contains(f.Title, "ci-bot") || strings.Contains(f.Title, "5000") {
		t.Errorf("title must name the actor and never usage figures: %q", f.Title)
	}

	// Toggle off: no findings (the feed still flows for cost/productivity).
	sink = gather(t, map[string]string{
		"admin_key": "sk-ant-admin-test", "cost_report": "false", "claude_code": "true",
		"claude_code_shadow_auth": "false",
	})
	if fs := shadowFindings(sink); len(fs) != 0 {
		t.Errorf("detector off must emit nothing, got %+v", fs)
	}
}
