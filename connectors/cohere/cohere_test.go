// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cohere

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

// fixtureDoer serves recorded Cohere fixtures by request path. It records every request
// so a test can assert the connector is read-only and uses GET-only.
type fixtureDoer struct {
	t    *testing.T
	reqs []*http.Request
	file string // override fixture file name
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	file := d.file
	if file == "" {
		file = "models.json"
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

func fixedClock() time.Time { return time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC) }

func newSource(t *testing.T, doer *fixtureDoer, over map[string]string) *Source {
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{"api_key": "test-cohere-key"}
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
	if cat.Provider.Ref != modelprovider.ProviderCohere || cat.Provider.Kind != modelprovider.KindHostedAPI {
		t.Fatalf("provider = %+v", cat.Provider)
	}
	if len(cat.Models) != 6 {
		t.Fatalf("models = %d, want 6", len(cat.Models))
	}

	// command-r-plus: chat model with pricing
	crp, ok := cat.FindModel("command-r-plus")
	if !ok || crp.Pricing == nil || crp.Pricing.InputPerMTokUSD != 2.5 || crp.Pricing.OutputPerMTokUSD != 10.0 {
		t.Fatalf("command-r-plus not enriched: %+v", crp)
	}
	if crp.Pricing.Source != modelprovider.PricingList {
		t.Fatalf("pricing source = %q, want list", crp.Pricing.Source)
	}
	if crp.CapabilitySource != "live" {
		t.Fatalf("capability source = %q, want live", crp.CapabilitySource)
	}
	if crp.ContextWindow != 128000 {
		t.Fatalf("context window = %d, want 128000", crp.ContextWindow)
	}
	// Live endpoints "chat" -> streaming + tool_use; declared family adds structured_outputs.
	for _, want := range []modelprovider.Capability{
		modelprovider.CapStreaming, modelprovider.CapToolUse, modelprovider.CapStructuredOutputs,
	} {
		if !crp.HasCapability(want) {
			t.Fatalf("command-r-plus missing capability %q (caps=%v)", want, crp.Capabilities)
		}
	}

	// command-a: high-context chat model
	ca, ok := cat.FindModel("command-a")
	if !ok || ca.Pricing == nil || ca.Pricing.InputPerMTokUSD != 2.5 {
		t.Fatalf("command-a not enriched: %+v", ca)
	}
	if ca.ContextWindow != 256000 {
		t.Fatalf("command-a context window = %d, want 256000", ca.ContextWindow)
	}

	// embed-english-v3.0: embeddings model, no chat capabilities
	emb, ok := cat.FindModel("embed-english-v3.0")
	if !ok || emb.Pricing == nil || emb.Pricing.InputPerMTokUSD != 0.10 {
		t.Fatalf("embed-english-v3.0 not priced: %+v", emb)
	}
	if emb.Pricing.OutputPerMTokUSD != 0 {
		t.Fatalf("embed model has non-zero output price: %v", emb.Pricing.OutputPerMTokUSD)
	}
	// embed endpoint does not map to chat capabilities
	if emb.HasCapability(modelprovider.CapToolUse) {
		t.Fatalf("embed model should not have tool_use capability (caps=%v)", emb.Capabilities)
	}

	// The deprecated command-light model carries the Deprecated flag.
	dep, ok := cat.FindModel("command-light")
	if !ok {
		t.Fatal("command-light not in catalog")
	}
	if !dep.Deprecated {
		t.Fatal("command-light should be marked Deprecated")
	}
	// command-light has no declared family -> no pricing (never a guessed price).
	if dep.Pricing != nil {
		t.Fatalf("uncataloged deprecated model must not carry a guessed price: %+v", dep.Pricing)
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
	if len(cat.Models) != len(declaredModelIDs) {
		t.Fatalf("offline models = %d, want %d", len(cat.Models), len(declaredModelIDs))
	}
	for _, m := range cat.Models {
		if m.CapabilitySource != "declared" {
			t.Fatalf("offline model %s capability source = %q, want declared", m.Ref, m.CapabilitySource)
		}
	}
	// A declared chat model is priced from the list table.
	crp, ok := cat.FindModel("command-r-plus")
	if !ok || crp.Pricing == nil || crp.Pricing.InputPerMTokUSD != 2.5 {
		t.Fatalf("offline command-r-plus not priced: %+v", crp)
	}
}

// pagingDoer serves page 1 then page 2 of a paginated model response.
type pagingDoer struct {
	t    *testing.T
	reqs []*http.Request
}

func (d *pagingDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	pageToken := req.URL.Query().Get("page_token")
	var file string
	if pageToken == "" {
		file = "models_page1.json"
	} else {
		file = "models_page2.json"
	}
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		d.t.Fatalf("read fixture %s: %v", file, err)
	}
	return resp(200, string(body)), nil
}

func TestSnapshot_Pagination(t *testing.T) {
	doer := &pagingDoer{t: t}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"api_key": "test-key"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// Page 1: command-r-plus + command-r. Page 2: embed-english-v3.0.
	if len(cat.Models) != 3 {
		t.Fatalf("paginated models = %d, want 3 (2 from page1 + 1 from page2)", len(cat.Models))
	}
	if len(doer.reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (one per page)", len(doer.reqs))
	}
	// Second request must carry the page_token from the first response.
	if got := doer.reqs[1].URL.Query().Get("page_token"); got != "page2cursor" {
		t.Fatalf("page-2 cursor = %q, want page2cursor", got)
	}
	// Verify all three models are present.
	for _, name := range []string{"command-r-plus", "command-r", "embed-english-v3.0"} {
		if _, ok := cat.FindModel(name); !ok {
			t.Fatalf("model %q not found after pagination", name)
		}
	}
}

func TestGather_PostureFinding(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.costs()) != 0 {
		t.Fatal("Cohere Gather must emit no cost samples (no usage API; cost is via Meter)")
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].SubjectKind != subjectCoverage {
		t.Fatalf("want only the coverage caveat, got %+v", fs)
	}
	if fs[0].Severity != model.SeverityInfo {
		t.Fatalf("coverage caveat severity = %q, want info", fs[0].Severity)
	}
	if !strings.Contains(fs[0].Title, "dashboard-only") {
		t.Fatalf("coverage caveat title missing 'dashboard-only': %q", fs[0].Title)
	}
	if !strings.Contains(fs[0].Title, "Model Vault") {
		t.Fatalf("coverage caveat title missing 'Model Vault': %q", fs[0].Title)
	}
	// The doer is never called during Gather (no inventory to pull).
	if len(doer.reqs) != 0 {
		t.Fatalf("caveat-only gather issued %d requests, want 0", len(doer.reqs))
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
	// Trigger a Snapshot to issue requests (Gather itself doesn't call the API).
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(doer.reqs) == 0 {
		t.Fatal("no requests issued")
	}
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-cohere-key" {
			t.Fatalf("bearer credential not sent on %s: %q", r.URL.Path, r.Header.Get("Authorization"))
		}
	}
}

func TestMeter(t *testing.T) {
	s := New()
	s.now = fixedClock
	at := fixedClock()

	// command-r-plus: input 2M * $2.5/M + output 1M * $10.0/M = $5.0 + $10.0 = $15.0
	// = 15_000_000 micro-USD.
	cs, ok := s.Meter("command-r-plus", 2_000_000, 1_000_000, at)
	if !ok || cs.CostMicroUSD != 15_000_000 {
		t.Fatalf("command-r-plus meter = %d ok=%v, want 15000000 true", cs.CostMicroUSD, ok)
	}
	if cs.ProviderRef != modelprovider.ProviderCohere || cs.CostType != costTypeCohere {
		t.Fatalf("meter attribution = %+v", cs)
	}
	if cs.Provenance != model.ProvenanceEstimated {
		t.Fatalf("provenance = %q, want estimated (no billing API)", cs.Provenance)
	}
	if cs.InputTokens != 2_000_000 || cs.OutputTokens != 1_000_000 {
		t.Fatalf("tokens = in %d out %d", cs.InputTokens, cs.OutputTokens)
	}

	// embed-english-v3.0: input only. 1M * $0.10/M = $0.10 = 100_000 micro-USD.
	cs, ok = s.Meter("embed-english-v3.0", 1_000_000, 0, at)
	if !ok || cs.CostMicroUSD != 100_000 {
		t.Fatalf("embed meter = %d ok=%v, want 100000 true", cs.CostMicroUSD, ok)
	}

	// Uncataloged model: recorded with cost 0, not priced (never a guessed price).
	cs, ok = s.Meter("some-unknown-model", 1_000_000, 1_000_000, at)
	if ok || cs.CostMicroUSD != 0 {
		t.Fatalf("unknown model must not be priced: %d ok=%v", cs.CostMicroUSD, ok)
	}
}

func TestFamilyFor_LongestPrefix(t *testing.T) {
	cases := []struct {
		id        string
		wantIn    float64
		wantMatch bool
	}{
		{"command-r-plus", 2.5, true},
		{"command-r-plus-08-2024", 2.5, true}, // more specific dated id still matches
		{"command-r", 0.15, true},
		{"command-a", 2.5, true},
		{"embed-english-v3.0", 0.10, true},
		{"embed-multilingual-v3.0", 0.10, true},
		{"embed-v4.0", 0.10, true},
		{"rerank-v3.5", 2.0, true},
		{"gpt-4o", 0, false}, // not a cohere model
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

	// Snapshot
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, m := range cat.Models {
		// No model carries prompt or completion content.
		if strings.Contains(m.Ref, "prompt") || strings.Contains(m.Ref, "completion") {
			t.Fatalf("model ref contains sensitive content: %q", m.Ref)
		}
	}
	// Gather
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range sink.findings() {
		// No finding leaks a key value.
		if strings.Contains(f.Title, "test-cohere-key") {
			t.Fatalf("finding title leaked a key value: %q", f.Title)
		}
		if strings.Contains(f.DetailHash, "test-cohere-key") {
			t.Fatalf("finding detail leaked a key value (hash should not contain raw key)")
		}
	}
	// All requests are GET-only.
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s %s", r.Method, r.URL.Path)
		}
	}
}
