// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Tests cover the OpenRouter catalog (live per-token pricing → USD/MTok), account
// posture, the approved-model policy drift, and Meter — all air-gapped against
// recorded fixtures (no network). Shapes verified against openrouter.ai/docs.
package openrouter

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

// fixtureDoer multiplexes by request path to the recorded fixtures and records
// every request so a test can assert the connector is read-only (GET-only).
type fixtureDoer struct {
	t            *testing.T
	reqs         []*http.Request
	bodyOverride map[string]string // path suffix -> body
	unavailable  map[string]int    // path suffix -> status
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	p := req.URL.Path
	for suffix, status := range d.unavailable {
		if strings.HasSuffix(p, suffix) {
			return resp(status, `{"error":{"message":"unavailable"}}`), nil
		}
	}
	for suffix, body := range d.bodyOverride {
		if strings.HasSuffix(p, suffix) {
			return resp(200, body), nil
		}
	}
	var file string
	switch {
	case strings.HasSuffix(p, "/models"):
		file = "models.json"
	case strings.HasSuffix(p, "/auth/key"):
		file = "key.json"
	default:
		d.t.Fatalf("unexpected request path %q", p)
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

func newSource(t *testing.T, cfg map[string]string, doer *fixtureDoer) *Source {
	t.Helper()
	s := New()
	s.doer = doer
	s.now = func() time.Time { return time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func findWithSubject(fs []model.FindingReport, subjectRef string) (model.FindingReport, bool) {
	for _, f := range fs {
		if f.SubjectRef == subjectRef {
			return f, true
		}
	}
	return model.FindingReport{}, false
}

func TestSnapshotLivePricingAndCaps(t *testing.T) {
	s := newSource(t, map[string]string{"api_key": "sk-or-test"}, &fixtureDoer{t: t})
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if cat.Provider.Ref != modelprovider.ProviderOpenRouter || cat.Provider.Kind != modelprovider.KindGateway {
		t.Fatalf("provider = %+v", cat.Provider)
	}
	byID := map[string]modelprovider.Model{}
	for _, m := range cat.Models {
		byID[m.Ref] = m
	}
	sonnet := byID["anthropic/claude-sonnet-4"]
	if sonnet.Pricing == nil {
		t.Fatal("claude-sonnet-4 has no pricing")
	}
	// per-token 0.000003 -> 3.0 USD/MTok; 0.000015 -> 15.0.
	if sonnet.Pricing.InputPerMTokUSD != 3.0 || sonnet.Pricing.OutputPerMTokUSD != 15.0 {
		t.Fatalf("sonnet pricing = %+v, want 3.0/15.0 per MTok", sonnet.Pricing)
	}
	if sonnet.CapabilitySource != "live" || sonnet.ContextWindow != 200000 || sonnet.MaxOutputTokens != 64000 {
		t.Fatalf("sonnet fields = source=%q ctx=%d out=%d", sonnet.CapabilitySource, sonnet.ContextWindow, sonnet.MaxOutputTokens)
	}
	if !hasCap(sonnet.Capabilities, modelprovider.CapToolUse) ||
		!hasCap(sonnet.Capabilities, modelprovider.CapStructuredOutputs) ||
		!hasCap(sonnet.Capabilities, modelprovider.CapExtendedThinking) {
		t.Fatalf("sonnet caps = %v", sonnet.Capabilities)
	}
	// A free model (prompt/completion "0") keeps nil pricing — never a guess.
	if free := byID["somevendor/free-model"]; free.Pricing != nil {
		t.Fatalf("free model should have nil pricing, got %+v", free.Pricing)
	}
	// Read-only: every request is a GET.
	for _, r := range s.doer.(*fixtureDoer).reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s %s", r.Method, r.URL.Path)
		}
	}
}

func TestAccountPosture(t *testing.T) {
	s := newSource(t, map[string]string{"api_key": "sk-or-test"}, &fixtureDoer{t: t})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	f, ok := findWithSubject(sink.findings(), "account")
	if !ok {
		t.Fatal("no account posture finding")
	}
	if f.SubjectKind != subjectAccount || f.Severity != model.SeverityInfo {
		t.Fatalf("account finding = kind=%q sev=%s", f.SubjectKind, f.Severity)
	}
}

func TestAccountCreditExhausted(t *testing.T) {
	doer := &fixtureDoer{t: t, bodyOverride: map[string]string{
		"/auth/key": `{"data":{"label":"k","usage":100.0,"limit":100.0,"is_free_tier":false}}`,
	}}
	s := newSource(t, map[string]string{"api_key": "sk-or-test"}, doer)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	f, _ := findWithSubject(sink.findings(), "account")
	if f.Severity != model.SeverityLow {
		t.Fatalf("exhausted account severity = %s, want low", f.Severity)
	}
}

func TestAccountUnavailableDenyClosed(t *testing.T) {
	doer := &fixtureDoer{t: t, unavailable: map[string]int{"/auth/key": 401}}
	s := newSource(t, map[string]string{"api_key": "sk-or-bad"}, doer)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather should surface a finding, not error: %v", err)
	}
	f, ok := findWithSubject(sink.findings(), "account")
	if !ok || f.Severity != model.SeverityMedium || !strings.Contains(f.Title, "unavailable") {
		t.Fatalf("expected a Medium account-unavailable finding, got %+v", f)
	}
}

func TestModelPolicyDrift(t *testing.T) {
	s := newSource(t, map[string]string{
		"api_key":         "sk-or-test",
		"denied_models":   "openai/gpt-4o",
		"approved_models": "anthropic/claude-sonnet-4, nonexistent/model",
	}, &fixtureDoer{t: t})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()

	denied, ok := findWithSubject(fs, "denied/openai/gpt-4o")
	if !ok || denied.Severity != model.SeverityHigh {
		t.Fatalf("expected High denied-reachable for gpt-4o, got %+v ok=%v", denied, ok)
	}
	missing, ok := findWithSubject(fs, "approved-missing/nonexistent/model")
	if !ok || missing.Severity != model.SeverityLow {
		t.Fatalf("expected Low approved-missing for nonexistent/model, got %+v ok=%v", missing, ok)
	}
	summary, ok := findWithSubject(fs, "summary")
	if !ok || !strings.Contains(summary.Title, "1 denied-reachable") || !strings.Contains(summary.Title, "1 approved-missing") {
		t.Fatalf("policy summary wrong: %q", summary.Title)
	}
	// An approved model that IS present must NOT produce an approved-missing finding.
	if _, ok := findWithSubject(fs, "approved-missing/anthropic/claude-sonnet-4"); ok {
		t.Fatal("a present approved model was wrongly flagged missing")
	}
}

func TestModelPolicyDisabledEmitsNoPolicyFindings(t *testing.T) {
	s := newSource(t, map[string]string{"api_key": "sk-or-test"}, &fixtureDoer{t: t})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectPolicy {
			t.Fatalf("unexpected policy finding with no policy configured: %q", f.SubjectRef)
		}
	}
}

func TestMeterCallBilledCostAndVerdict(t *testing.T) {
	s := newSource(t, map[string]string{
		"denied_models":   "openai/gpt-4o",
		"approved_models": "anthropic/claude-sonnet-4",
	}, &fixtureDoer{t: t})

	sample, verdict := s.MeterCall("anthropic/claude-sonnet-4", "user:alice", 1000, 500, 0.012, time.Time{})
	if verdict != VerdictApproved {
		t.Fatalf("verdict = %s, want approved", verdict)
	}
	if sample.ProviderRef != modelprovider.ProviderOpenRouter || sample.CostType != costTypeOpenRouter {
		t.Fatalf("sample provider/costtype = %q/%q", sample.ProviderRef, sample.CostType)
	}
	if sample.CostMicroUSD != 12000 { // 0.012 USD
		t.Fatalf("cost = %d micro-USD, want 12000", sample.CostMicroUSD)
	}
	if sample.Actor != "user:alice" {
		t.Fatalf("actor = %q, want user:alice", sample.Actor)
	}

	// A denied model returns VerdictDenied at the point of use.
	if _, v := s.MeterCall("openai/gpt-4o", "", 10, 10, 0.001, time.Time{}); v != VerdictDenied {
		t.Fatalf("denied model verdict = %s, want denied", v)
	}
	// An unlisted model under an allowlist is unapproved.
	if _, v := s.MeterCall("random/model", "", 1, 1, 0, time.Time{}); v != VerdictUnapproved {
		t.Fatalf("unlisted model verdict = %s, want unapproved", v)
	}
}

func TestOfflineModeNoCredential(t *testing.T) {
	s := newSource(t, map[string]string{}, &fixtureDoer{t: t})
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot offline: %v", err)
	}
	if len(cat.Models) == 0 {
		t.Fatal("offline catalog should return the declared model list")
	}
	for _, m := range cat.Models {
		if m.CapabilitySource != "declared" || m.Pricing != nil {
			t.Fatalf("offline model should be declared with nil pricing: %+v", m)
		}
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather offline: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("offline Gather emitted %d observations, want 0", len(sink.obs))
	}
	// No request should have been made offline.
	if n := len(s.doer.(*fixtureDoer).reqs); n != 0 {
		t.Fatalf("offline made %d requests, want 0", n)
	}
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Version != "0.1.0" {
		t.Fatalf("descriptor = %q %q", d.Name, d.Version)
	}
	var keys []string
	for _, f := range d.ConfigFields {
		keys = append(keys, f.Key)
	}
	for _, want := range []string{"api_key", "base_url", "approved_models", "denied_models"} {
		if !contains(keys, want) {
			t.Fatalf("descriptor fields %v missing %s", keys, want)
		}
	}
}

func TestPerMTokRejectsNonFinite(t *testing.T) {
	// strconv.ParseFloat accepts "NaN"/"Inf"/"Infinity" without error, and NaN slips
	// past a v<=0 guard (every comparison with NaN is false). A hostile/misbehaving
	// upstream must never poison ModelPricing with a non-finite value.
	for _, bad := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "Infinity", "1e400", "-0.01", "0", ""} {
		if got := perMTok(bad); got != 0 {
			t.Fatalf("perMTok(%q) = %v, want 0 (non-finite/non-positive rejected)", bad, got)
		}
	}
	// A finite value whose ×1e6 overflows float64 is rejected too.
	if got := perMTok("1e305"); got != 0 {
		t.Fatalf("perMTok(1e305) = %v, want 0 (×1e6 overflow rejected)", got)
	}
	// A normal per-token price still converts to USD/MTok.
	if got := perMTok("0.000003"); got != 3.0 {
		t.Fatalf("perMTok(0.000003) = %v, want 3.0 USD/MTok", got)
	}
	// pricingFrom yields no pricing when both sides are non-finite (never a poisoned price).
	if pc, ok := pricingFrom(&pricing{Prompt: "NaN", Completion: "Inf"}, "2026-07-12"); ok {
		t.Fatalf("pricingFrom(NaN/Inf) returned pricing %+v, want no pricing", pc)
	}
}

func hasCap(caps []modelprovider.Capability, want modelprovider.Capability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
