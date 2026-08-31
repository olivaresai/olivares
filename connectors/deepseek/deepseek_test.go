// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Tests in this file cover DeepSeek v4 catalog and balance API currency verified against
// api-docs.deepseek.com on 2026-07-04, including legacy retirement rows.
package deepseek

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

// fixtureDoer multiplexes by request path to the recorded DeepSeek fixtures. It records
// every request so a test can assert the connector is read-only.
type fixtureDoer struct {
	t            *testing.T
	reqs         []*http.Request
	bodyOverride map[string]string
	unavailable  map[string]bool
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	if d.unavailable[req.URL.Path] {
		return resp(404, `{"error":"not_found"}`), nil
	}
	if body, ok := d.bodyOverride[req.URL.Path]; ok {
		return resp(200, body), nil
	}
	var file string
	switch req.URL.Path {
	case defaultModelND:
		file = "models.json"
	case defaultBalancePath:
		file = "balance.json"
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

type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
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

func (s *captureSink) costs() []model.CostSample {
	var out []model.CostSample
	for _, o := range s.obs {
		if c, ok := o.(model.CostSample); ok {
			out = append(out, c)
		}
	}
	return out
}

// fixedClock pins the clock for deterministic test assertions.
func fixedClock() time.Time { return time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) }

func newSource(t *testing.T, doer *fixtureDoer, over map[string]string) *Source {
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{"api_key": "sk-deepseek-test"}
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
	}
	if !sawSecret {
		t.Fatal("api_key must be declared as a secret config field")
	}
}

func TestSnapshot_Live(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if cat.Provider.Ref != modelprovider.ProviderDeepSeek || cat.Provider.Kind != modelprovider.KindHostedAPI {
		t.Fatalf("provider = %+v", cat.Provider)
	}
	if len(cat.Models) != 4 {
		t.Fatalf("models = %d, want 4", len(cat.Models))
	}

	flash, ok := cat.FindModel("deepseek-v4-flash")
	if !ok || flash.Pricing == nil {
		t.Fatalf("deepseek-v4-flash not present/priced: %+v", flash)
	}
	if flash.ContextWindow != 1_000_000 || flash.MaxOutputTokens != 384_000 {
		t.Fatalf("deepseek-v4-flash limits = context %d output %d", flash.ContextWindow, flash.MaxOutputTokens)
	}
	if flash.Pricing.InputPerMTokUSD != 0.14 || flash.Pricing.CacheReadPerMTokUSD != 0.0028 || flash.Pricing.OutputPerMTokUSD != 0.28 {
		t.Fatalf("deepseek-v4-flash pricing = %+v", flash.Pricing)
	}
	if flash.Pricing.AsOf != "2026-07-04" {
		t.Fatalf("deepseek-v4-flash pricing AsOf = %q", flash.Pricing.AsOf)
	}

	pro, ok := cat.FindModel("deepseek-v4-pro")
	if !ok || pro.Pricing == nil ||
		pro.Pricing.InputPerMTokUSD != 0.435 ||
		pro.Pricing.CacheReadPerMTokUSD != 0.003625 ||
		pro.Pricing.OutputPerMTokUSD != 0.87 ||
		pro.ContextWindow != 1_000_000 ||
		pro.MaxOutputTokens != 384_000 {
		t.Fatalf("deepseek-v4-pro = %+v", pro)
	}

	chat, ok := cat.FindModel("deepseek-chat")
	if !ok || chat.Pricing == nil || chat.Pricing.InputPerMTokUSD != 0.27 || chat.Pricing.OutputPerMTokUSD != 1.10 {
		t.Fatalf("deepseek-chat not enriched: %+v", chat)
	}
	if chat.Pricing.CacheReadPerMTokUSD != 0.07 {
		t.Fatalf("deepseek-chat cache-read = %v, want 0.07", chat.Pricing.CacheReadPerMTokUSD)
	}
	if chat.Pricing.Source != modelprovider.PricingList {
		t.Fatalf("pricing source = %q, want list", chat.Pricing.Source)
	}
	if chat.CapabilitySource != "live" {
		t.Fatalf("capability source = %q, want live", chat.CapabilitySource)
	}
	if chat.ContextWindow != 65536 {
		t.Fatalf("context window = %d, want 65536", chat.ContextWindow)
	}
	if !chat.Deprecated || len(chat.Retirements) != 1 {
		t.Fatalf("deepseek-chat retirement metadata = deprecated %v retirements %+v", chat.Deprecated, chat.Retirements)
	}
	wantRetirement := modelprovider.ModelRetirement{
		Surface:        model.GatewayDirect,
		DeprecatedOn:   "2026-04-24",
		RetiresOn:      "2026-07-24",
		ReplacementRef: "deepseek-v4-flash",
		AsOf:           "2026-07-04",
	}
	if chat.Retirements[0] != wantRetirement {
		t.Fatalf("deepseek-chat retirement = %+v, want %+v", chat.Retirements[0], wantRetirement)
	}
	if chat.DisplayName != "DeepSeek Chat (V3)" {
		t.Fatalf("display name = %q, want DeepSeek Chat (V3)", chat.DisplayName)
	}
	// Chat model capabilities: streaming, tool_use, structured_outputs.
	for _, want := range []modelprovider.Capability{
		modelprovider.CapStreaming, modelprovider.CapToolUse, modelprovider.CapStructuredOutputs,
	} {
		if !chat.HasCapability(want) {
			t.Fatalf("deepseek-chat missing capability %q (caps=%v)", want, chat.Capabilities)
		}
	}

	reasoner, ok := cat.FindModel("deepseek-reasoner")
	if !ok || reasoner.Pricing == nil || reasoner.Pricing.InputPerMTokUSD != 0.55 || reasoner.Pricing.OutputPerMTokUSD != 2.19 {
		t.Fatalf("deepseek-reasoner not enriched: %+v", reasoner)
	}
	if reasoner.Pricing.CacheReadPerMTokUSD != 0.14 {
		t.Fatalf("deepseek-reasoner cache-read = %v, want 0.14", reasoner.Pricing.CacheReadPerMTokUSD)
	}
	// Reasoner capabilities: streaming, tool_use, extended_thinking.
	if !reasoner.HasCapability(modelprovider.CapExtendedThinking) {
		t.Fatalf("deepseek-reasoner missing extended_thinking (caps=%v)", reasoner.Capabilities)
	}
	if !reasoner.Deprecated || len(reasoner.Retirements) != 1 || reasoner.Retirements[0] != wantRetirement {
		t.Fatalf("deepseek-reasoner retirement metadata = deprecated %v retirements %+v", reasoner.Deprecated, reasoner.Retirements)
	}

	// All requests must be GET (read-only).
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s %s", r.Method, r.URL.Path)
		}
	}
}

func TestSnapshot_Offline(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(cat.Models) != len(declaredModels) {
		t.Fatalf("offline models = %d, want %d", len(cat.Models), len(declaredModels))
	}
	for _, m := range cat.Models {
		if m.CapabilitySource != "declared" {
			t.Fatalf("offline model %s capability source = %q, want declared", m.Ref, m.CapabilitySource)
		}
	}
	// A declared chat model is priced from the list table.
	chat, ok := cat.FindModel("deepseek-chat")
	if !ok || chat.Pricing == nil || chat.Pricing.InputPerMTokUSD != 0.27 {
		t.Fatalf("offline deepseek-chat not priced: %+v", chat)
	}
	flash, ok := cat.FindModel("deepseek-v4-flash")
	if !ok || flash.Pricing == nil || flash.ContextWindow != 1_000_000 || flash.MaxOutputTokens != 384_000 {
		t.Fatalf("offline deepseek-v4-flash not declared correctly: %+v", flash)
	}
}

func TestGather_SovereigntyPosture(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.costs()) != 0 {
		t.Fatal("DeepSeek Gather must emit no cost samples (no usage API; cost is via Meter)")
	}
	fs := sink.findings()
	var sovereignty, balance int
	for _, f := range fs {
		switch f.SubjectKind {
		case subjectSovereignty:
			sovereignty++
			if f.Severity != model.SeverityHigh {
				t.Fatalf("sovereignty severity = %q, want high (PRC data-access laws)", f.Severity)
			}
			if !strings.Contains(strings.ToLower(f.Title), "prc") {
				t.Fatalf("sovereignty title must mention PRC: %q", f.Title)
			}
			if f.SubjectRef != "hosted_api" {
				t.Fatalf("sovereignty subject ref = %q, want hosted_api", f.SubjectRef)
			}
		case subjectBalance:
			balance++
		}
	}
	if sovereignty != 1 || balance != 1 {
		t.Fatalf("findings = sovereignty %d balance %d, want 1/1 (%+v)", sovereignty, balance, fs)
	}
	if len(doer.reqs) != 1 || doer.reqs[0].URL.Path != defaultBalancePath {
		t.Fatalf("gather requests = %d (%+v), want one balance request", len(doer.reqs), doer.reqs)
	}
}

func TestGather_BalanceAvailable(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var balance *model.FindingReport
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectBalance {
			balance = &f
		}
	}
	if balance == nil {
		t.Fatal("missing balance finding")
	}
	if balance.Severity != model.SeverityInfo || !strings.Contains(balance.Title, "2 currency bucket(s)") {
		t.Fatalf("balance finding = %+v", balance)
	}
	for _, amount := range []string{"12.34", "5.00", "7.34", "88.00"} {
		if strings.Contains(balance.Title, amount) || strings.Contains(balance.SubjectRef, amount) {
			t.Fatalf("balance amount %q leaked outside DetailHash: %+v", amount, balance)
		}
	}
}

func TestGather_BalanceExhausted(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "balance_exhausted.json"))
	if err != nil {
		t.Fatalf("read exhausted balance fixture: %v", err)
	}
	doer := &fixtureDoer{t: t, bodyOverride: map[string]string{defaultBalancePath: string(body)}}
	s := newSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var balance *model.FindingReport
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectBalance {
			balance = &f
		}
	}
	if balance == nil || balance.Severity != model.SeverityLow || !strings.Contains(balance.Title, "exhausted") {
		t.Fatalf("exhausted balance finding = %+v", balance)
	}
	if strings.Contains(balance.Title, "0.00") {
		t.Fatalf("exhausted balance title leaked amount: %q", balance.Title)
	}
}

func TestGather_BalanceUnavailableDegrades(t *testing.T) {
	doer := &fixtureDoer{t: t, unavailable: map[string]bool{defaultBalancePath: true}}
	s := newSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather must NOT fail on a 403/404 balance surface; got %v", err)
	}
	var balance *model.FindingReport
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectBalance {
			balance = &f
		}
	}
	if balance == nil || balance.Severity != model.SeverityMedium || !strings.Contains(balance.Title, "unavailable") {
		t.Fatalf("balance unavailable finding = %+v", balance)
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

func TestGather_ReadOnlyBearerAuth(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
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
		if r.Header.Get("Authorization") != "Bearer sk-deepseek-test" {
			t.Fatalf("bearer credential not sent on %s: %q", r.URL.Path, r.Header.Get("Authorization"))
		}
	}
}

func TestMeter(t *testing.T) {
	s := New()
	s.now = fixedClock
	at := fixedClock()

	// deepseek-chat: input 1M * $0.27/M + output 1M * $1.10/M + cache-read 1M * $0.07/M
	// = 270000 + 1100000 + 70000 = 1440000 micro-USD.
	cs, ok := s.Meter("deepseek-chat", 1_000_000, 1_000_000, 1_000_000, at)
	if !ok || cs.CostMicroUSD != 1_440_000 {
		t.Fatalf("chat meter = %d ok=%v, want 1440000 true", cs.CostMicroUSD, ok)
	}
	if cs.ProviderRef != modelprovider.ProviderDeepSeek || cs.CostType != costTypeDeepSeek {
		t.Fatalf("meter attribution = %+v", cs)
	}
	if cs.Provenance != model.ProvenanceEstimated {
		t.Fatalf("provenance = %q, want estimated (no billing API)", cs.Provenance)
	}
	if cs.InputTokens != 2_000_000 { // TotalInputTokens = input + cacheRead
		t.Fatalf("input tokens = %d, want 2000000 (input + cache-read)", cs.InputTokens)
	}
	if cs.OutputTokens != 1_000_000 {
		t.Fatalf("output tokens = %d, want 1000000", cs.OutputTokens)
	}

	// deepseek-reasoner: input 2M * $0.55/M + output 1M * $2.19/M + no cache
	// = 1100000 + 2190000 = 3290000 micro-USD.
	cs, ok = s.Meter("deepseek-reasoner", 2_000_000, 1_000_000, 0, at)
	if !ok || cs.CostMicroUSD != 3_290_000 {
		t.Fatalf("reasoner meter = %d ok=%v, want 3290000 true", cs.CostMicroUSD, ok)
	}

	// Uncataloged model: recorded with cost 0, not priced (never a guessed price).
	cs, ok = s.Meter("some-unknown-model", 1_000_000, 1_000_000, 0, at)
	if ok || cs.CostMicroUSD != 0 {
		t.Fatalf("unknown model must not be priced: %d ok=%v", cs.CostMicroUSD, ok)
	}

	// Negative token counts are clamped to 0 (defensive).
	cs, ok = s.Meter("deepseek-chat", -100, -200, -50, at)
	if !ok || cs.CostMicroUSD != 0 {
		t.Fatalf("negative tokens should clamp to 0 cost: %d ok=%v", cs.CostMicroUSD, ok)
	}
}

func TestFamilyFor_LongestPrefix(t *testing.T) {
	cases := []struct {
		id        string
		wantIn    float64
		wantMatch bool
	}{
		{"deepseek-v4-flash", 0.14, true},
		{"deepseek-v4-pro", 0.435, true},
		{"deepseek-chat", 0.27, true},
		{"deepseek-chat-20260601", 0.27, true}, // dated id resolves via prefix
		{"deepseek-reasoner", 0.55, true},
		{"gpt-4o", 0, false}, // not a deepseek model
	}
	for _, c := range cases {
		f, ok := familyFor(c.id)
		if ok != c.wantMatch {
			t.Fatalf("%s: match = %v, want %v", c.id, ok, c.wantMatch)
		}
		if ok && f.pricing.InputPerMTokUSD != c.wantIn {
			t.Fatalf("%s: input price = %v, want %v", c.id, f.pricing.InputPerMTokUSD, c.wantIn)
		}
	}
}

func TestMinimalData(t *testing.T) {
	// Verify the connector never carries prompts, completions, or key values in any
	// output (the minimal-data contract, docs/SECURITY-HARDENING.md).
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)

	// Check Snapshot output: models carry only refs, display names, pricing, capabilities.
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, m := range cat.Models {
		// No model field should contain the API key.
		for _, field := range []string{m.Ref, m.DisplayName, m.CapabilitySource} {
			if strings.Contains(field, "sk-deepseek-test") {
				t.Fatalf("model field leaked API key: %q", field)
			}
		}
	}

	// Check Gather output: the sovereignty finding must not contain the API key.
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range sink.findings() {
		if strings.Contains(f.Title, "sk-") {
			t.Fatalf("finding title leaked a key hint: %q", f.Title)
		}
		if strings.Contains(f.DetailHash, "sk-") {
			t.Fatalf("finding detail hash leaked a key hint: %q", f.DetailHash)
		}
		for _, amount := range []string{"12.34", "5.00", "7.34", "88.00"} {
			if strings.Contains(f.Kind, amount) ||
				strings.Contains(string(f.Severity), amount) ||
				strings.Contains(f.SubjectKind, amount) ||
				strings.Contains(f.SubjectRef, amount) ||
				strings.Contains(f.Title, amount) {
				t.Fatalf("balance amount %q leaked outside DetailHash in finding: %+v", amount, f)
			}
		}
	}

	// Check Meter output: CostSample must not carry prompts or key values.
	cs, _ := s.Meter("deepseek-chat", 100, 200, 50, fixedClock())
	if cs.Actor != "" || cs.APIKeyRef != "" || cs.SessionRef != "" {
		t.Fatalf("meter CostSample carries non-minimal attribution: actor=%q key=%q session=%q",
			cs.Actor, cs.APIKeyRef, cs.SessionRef)
	}
}
