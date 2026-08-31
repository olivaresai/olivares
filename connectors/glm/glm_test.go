// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package glm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// fixtureDoer records every request and returns a configured probe status. The GLM
// connector must discard the response body, so no /models body fixture is needed.
type fixtureDoer struct {
	t      *testing.T
	reqs   []*http.Request
	status int
	err    error
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	if d.err != nil {
		return nil, d.err
	}
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	body := `{"object":"list","data":[]}`
	if status < 200 || status >= 300 {
		body = `{"error":"not_entitled"}`
	}
	return resp(status, body), nil
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

func fixedClock() time.Time { return time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC) }

func newSource(t *testing.T, doer *fixtureDoer, over map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{"api_key": "sk-glm-test"}
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
	var sawSecret, sawDefaultBase bool
	for _, f := range d.ConfigFields {
		if f.Key == "api_key" && f.Secret {
			sawSecret = true
		}
		if f.Key == "base_url" && f.Default == defaultBaseURL {
			sawDefaultBase = true
		}
	}
	if !sawSecret {
		t.Fatal("api_key must be declared as a secret config field")
	}
	if !sawDefaultBase {
		t.Fatalf("base_url must default to %q", defaultBaseURL)
	}
}

func TestSnapshot_Offline(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("Snapshot must not call /models; got %d requests", len(doer.reqs))
	}
	if cat.Provider.Ref != modelprovider.ProviderGLM || cat.Provider.Kind != modelprovider.KindHostedAPI {
		t.Fatalf("provider = %+v", cat.Provider)
	}
	if cat.Provider.Title != "Zhipu GLM (Z.ai)" || cat.Provider.BaseURL != defaultBaseURL {
		t.Fatalf("provider metadata = %+v", cat.Provider)
	}
	if len(cat.Models) != len(declaredModels) {
		t.Fatalf("declared models = %d, want %d", len(cat.Models), len(declaredModels))
	}
	for _, m := range cat.Models {
		if m.ProviderRef != modelprovider.ProviderGLM || m.CapabilitySource != "declared" {
			t.Fatalf("declared model metadata = %+v", m)
		}
	}

	must := func(ref string) modelprovider.Model {
		t.Helper()
		m, ok := cat.FindModel(ref)
		if !ok {
			t.Fatalf("missing declared model %s", ref)
		}
		return m
	}

	glm52 := must("glm-5.2")
	if glm52.Pricing == nil ||
		glm52.Pricing.Currency != "USD" ||
		glm52.Pricing.AsOf != pricingAsOf ||
		glm52.Pricing.Source != modelprovider.PricingList ||
		glm52.Pricing.InputPerMTokUSD != 1.40 ||
		glm52.Pricing.OutputPerMTokUSD != 4.40 ||
		glm52.Pricing.CacheReadPerMTokUSD != 0.26 {
		t.Fatalf("glm-5.2 pricing = %+v", glm52.Pricing)
	}
	if glm52.ContextWindow != 1_000_000 || glm52.MaxOutputTokens != 0 {
		t.Fatalf("glm-5.2 limits = context %d output %d", glm52.ContextWindow, glm52.MaxOutputTokens)
	}
	for _, want := range []modelprovider.Capability{
		modelprovider.CapStreaming,
		modelprovider.CapToolUse,
		modelprovider.CapStructuredOutputs,
		modelprovider.CapPromptCaching,
		modelprovider.CapExtendedThinking,
	} {
		if !glm52.HasCapability(want) {
			t.Fatalf("glm-5.2 missing capability %q (caps=%v)", want, glm52.Capabilities)
		}
	}

	flash := must("glm-4.7-flash")
	if flash.Pricing == nil ||
		flash.Pricing.Currency != "USD" ||
		flash.Pricing.InputPerMTokUSD != 0 ||
		flash.Pricing.OutputPerMTokUSD != 0 ||
		flash.Pricing.CacheReadPerMTokUSD != 0 {
		t.Fatalf("free glm-4.7-flash pricing = %+v", flash.Pricing)
	}
	if !flash.HasCapability(modelprovider.CapPromptCaching) ||
		flash.HasCapability(modelprovider.CapExtendedThinking) {
		t.Fatalf("glm-4.7-flash capabilities = %v", flash.Capabilities)
	}

	vision := must("glm-4.6v")
	if vision.Pricing == nil ||
		vision.Pricing.InputPerMTokUSD != 0.30 ||
		vision.Pricing.OutputPerMTokUSD != 0.90 ||
		vision.Pricing.CacheReadPerMTokUSD != 0.05 ||
		!vision.HasCapability(modelprovider.CapVision) {
		t.Fatalf("glm-4.6v = %+v", vision)
	}

	basic := must("glm-4-32b-0414-128k")
	if basic.Pricing == nil ||
		basic.Pricing.InputPerMTokUSD != 0.10 ||
		basic.Pricing.OutputPerMTokUSD != 0.10 ||
		basic.Pricing.CacheReadPerMTokUSD != 0 ||
		basic.HasCapability(modelprovider.CapPromptCaching) {
		t.Fatalf("glm-4-32b-0414-128k = %+v", basic)
	}

	for _, ref := range []string{"glm-4-plus", "glm-4-flashx"} {
		m := must(ref)
		if m.Pricing != nil {
			t.Fatalf("%s must remain unpriced because USD price is UNVERIFIED: %+v", ref, m.Pricing)
		}
		if m.ContextWindow != 128_000 {
			t.Fatalf("%s context = %d, want 128000", ref, m.ContextWindow)
		}
	}
	if got := must("glm-4-plus").MaxOutputTokens; got != 4096 {
		t.Fatalf("glm-4-plus max output = %d, want 4096", got)
	}
	if got := must("glm-4-flashx").MaxOutputTokens; got != 16_384 {
		t.Fatalf("glm-4-flashx max output = %d, want 16384", got)
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
		t.Fatal("GLM Gather must emit no cost samples (no usage API; cost is via Meter)")
	}
	fs := sink.findings()
	var sovereignty, entitlement int
	for _, f := range fs {
		switch f.SubjectKind {
		case subjectSovereignty:
			sovereignty++
			if f.Severity != model.SeverityHigh {
				t.Fatalf("sovereignty severity = %q, want high", f.Severity)
			}
			title := strings.ToLower(f.Title)
			if !strings.Contains(title, "prc") || !strings.Contains(title, "entity-listed") {
				t.Fatalf("sovereignty title must mention PRC and Entity-Listed parent: %q", f.Title)
			}
			if f.SubjectRef != "hosted_api" {
				t.Fatalf("sovereignty subject ref = %q, want hosted_api", f.SubjectRef)
			}
		case subjectEntitlement:
			entitlement++
		}
	}
	if sovereignty != 1 || entitlement != 1 {
		t.Fatalf("findings = sovereignty %d entitlement %d, want 1/1 (%+v)", sovereignty, entitlement, fs)
	}
	if len(doer.reqs) != 1 || doer.reqs[0].URL.Path != "/api/paas/v4/models" {
		t.Fatalf("gather requests = %d (%+v), want one /api/paas/v4/models request", len(doer.reqs), doer.reqs)
	}
}

func TestGather_EntitlementValid(t *testing.T) {
	doer := &fixtureDoer{t: t, status: http.StatusOK}
	s := newSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	entitlement := findFinding(t, sink, subjectEntitlement)
	if entitlement.Severity != model.SeverityInfo || !strings.Contains(entitlement.Title, "valid") {
		t.Fatalf("entitlement valid finding = %+v", entitlement)
	}
}

func TestGather_EntitlementUnavailableDegrades(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			doer := &fixtureDoer{t: t, status: status}
			s := newSource(t, doer, nil)
			sink := &captureSink{}
			if err := s.Gather(context.Background(), sink); err != nil {
				t.Fatalf("Gather must NOT fail on %d entitlement posture; got %v", status, err)
			}
			entitlement := findFinding(t, sink, subjectEntitlement)
			if entitlement.Severity != model.SeverityMedium ||
				!strings.Contains(entitlement.Title, "unverified") ||
				!strings.Contains(entitlement.Title, "not entitled") {
				t.Fatalf("entitlement unavailable finding = %+v", entitlement)
			}
			if len(sink.costs()) != 0 {
				t.Fatal("Gather must not emit cost samples")
			}
		})
	}
}

func TestGather_EntitlementProbeErrorDegrades(t *testing.T) {
	doer := &fixtureDoer{t: t, err: errors.New("network down")}
	s := newSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather must degrade a best-effort probe error, got %v", err)
	}
	entitlement := findFinding(t, sink, subjectEntitlement)
	if entitlement.Severity != model.SeverityMedium || !strings.Contains(entitlement.Title, "unavailable") {
		t.Fatalf("probe failed finding = %+v", entitlement)
	}
}

func TestGather_OfflineNoCredentialEmitsNothing(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := New()
	s.doer = doer
	s.now = fixedClock
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
	if len(doer.reqs) != 0 {
		t.Fatalf("offline gather issued %d requests, want 0", len(doer.reqs))
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
	if len(doer.reqs) != 1 {
		t.Fatalf("requests = %d, want exactly one Gather probe", len(doer.reqs))
	}
	r := doer.reqs[0]
	if r.Method != http.MethodGet {
		t.Fatalf("non-GET request: %s %s", r.Method, r.URL.Path)
	}
	if r.URL.Path != "/api/paas/v4/models" || r.URL.String() != defaultBaseURL+defaultModelsPath {
		t.Fatalf("probe URL = %s, want %s", r.URL.String(), defaultBaseURL+defaultModelsPath)
	}
	if r.Header.Get("Authorization") != "Bearer sk-glm-test" {
		t.Fatalf("bearer credential not sent: %q", r.Header.Get("Authorization"))
	}
}

func TestMeter(t *testing.T) {
	s := New()
	s.now = fixedClock
	at := fixedClock()

	// glm-4.5-air: input 1M * $0.20/M + output 1M * $1.10/M +
	// cache-read 1M * $0.03/M = 1330000 micro-USD.
	cs, ok := s.Meter("glm-4.5-air", 1_000_000, 1_000_000, 1_000_000, at)
	if !ok || cs.CostMicroUSD != 1_330_000 {
		t.Fatalf("glm-4.5-air meter = %d ok=%v, want 1330000 true", cs.CostMicroUSD, ok)
	}
	if cs.ProviderRef != modelprovider.ProviderGLM || cs.CostType != costTypeGLM {
		t.Fatalf("meter attribution = %+v", cs)
	}
	if cs.Gateway != model.GatewayDirect || cs.Provenance != model.ProvenanceEstimated {
		t.Fatalf("meter surface/provenance = gateway %q provenance %q", cs.Gateway, cs.Provenance)
	}
	if cs.InputTokens != 2_000_000 || cs.OutputTokens != 1_000_000 || cs.CacheReadTokens != 1_000_000 {
		t.Fatalf("meter tokens = input %d output %d cache %d", cs.InputTokens, cs.OutputTokens, cs.CacheReadTokens)
	}

	cs, ok = s.Meter("glm-4.5-flash", 1_000_000, 1_000_000, 1_000_000, at)
	if !ok || cs.CostMicroUSD != 0 {
		t.Fatalf("free glm-4.5-flash must price as listed zero: %d ok=%v", cs.CostMicroUSD, ok)
	}

	for _, ref := range []string{"glm-4-plus", "glm-4-flashx", "some-unknown-model"} {
		cs, ok = s.Meter(ref, 1_000_000, 1_000_000, 1_000_000, at)
		if ok || cs.CostMicroUSD != 0 {
			t.Fatalf("%s must not be priced: %d ok=%v", ref, cs.CostMicroUSD, ok)
		}
	}

	cs, ok = s.Meter("glm-4.5-air", -100, -200, -50, at)
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
		{"glm-5.2", 1.40, true},
		{"glm-5.2-20260708", 1.40, true},
		{"glm-5", 1.00, true},
		{"glm-4.7-flashx", 0.07, true},
		{"glm-4.7-flash", 0, true},
		{"glm-4.7", 0.60, true},
		{"glm-4.6v", 0.30, true},
		{"glm-4.6", 0.60, true},
		{"glm-4.5-airx", 1.10, true},
		{"glm-4.5-air", 0.20, true},
		{"glm-4.5", 0.60, true},
		{"glm-4-flash", 0, true},
		{"glm-4-flashx", 0, false},
		{"glm-4-flashx-20260708", 0, false},
		{"glm-4-plus", 0, false},
		{"gpt-4o", 0, false},
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
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)
	secret := "sk-glm-test"
	prompt := "user prompt"
	completion := "model completion"

	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, m := range cat.Models {
		for _, field := range []string{m.Ref, m.DisplayName, m.CapabilitySource} {
			if strings.Contains(field, secret) ||
				strings.Contains(field, prompt) ||
				strings.Contains(field, completion) {
				t.Fatalf("model field leaked sensitive text: %q", field)
			}
		}
	}

	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range sink.findings() {
		for _, field := range []string{f.SubjectKind, f.SubjectRef, f.Title, f.DetailHash} {
			if strings.Contains(field, secret) ||
				strings.Contains(field, prompt) ||
				strings.Contains(field, completion) {
				t.Fatalf("finding leaked sensitive text: %+v", f)
			}
		}
	}

	cs, _ := s.Meter("glm-4.5-air", 100, 200, 50, fixedClock())
	if cs.Actor != "" || cs.APIKeyRef != "" || cs.SessionRef != "" {
		t.Fatalf("meter CostSample carries non-minimal attribution: actor=%q key=%q session=%q",
			cs.Actor, cs.APIKeyRef, cs.SessionRef)
	}
}

func findFinding(t *testing.T, sink *captureSink, subject string) model.FindingReport {
	t.Helper()
	for _, f := range sink.findings() {
		if f.SubjectKind == subject {
			return f
		}
	}
	t.Fatalf("missing finding subject %s in %+v", subject, sink.findings())
	return model.FindingReport{}
}
