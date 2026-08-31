// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gemini

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

// usagePath is the operator usage-export path the tests wire into usage_url and
// the fixtureDoer maps to the usage fixture.
const usagePath = "/usage/export"

// fixtureDoer multiplexes by request path to the recorded fixtures, and records
// every request so a test can assert the connector is read-only.
type fixtureDoer struct {
	t    *testing.T
	reqs []*http.Request
}

func (d *fixtureDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	var file string
	switch req.URL.Path {
	case "/v1beta/models":
		file = "models.json"
	case usagePath:
		file = "usage_export.json"
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
	cfg := sdk.Config{Settings: map[string]string{"api_key": "test-goog-key", "usage_url": usagePath}}
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
	if len(sink.obs) != 2 {
		t.Fatalf("emitted %d observations, want 2", len(sink.obs))
	}

	byModel := map[string]model.CostSample{}
	for _, o := range sink.obs {
		cs, ok := o.(model.CostSample)
		if !ok {
			t.Fatalf("observation is %T, want model.CostSample", o)
		}
		if cs.ProviderRef != modelprovider.ProviderGoogle {
			t.Fatalf("ProviderRef = %q", cs.ProviderRef)
		}
		byModel[cs.ModelRef] = cs
	}

	pro := byModel["gemini-2.5-pro"]
	// Golden cost, computed by hand from the declared base-tier list prices:
	//   input  1000 tok * 1.25 $/MTok =  1000 * 1.25 = 1250 micro-USD
	//   cache   400 tok * 0.31 $/MTok =   400 * 0.31  =  124 micro-USD
	//   output  200 tok * 10.00 $/MTok =  200 * 10.00 = 2000 micro-USD
	//   total = 1250 + 124 + 2000 = 3374 micro-USD.
	if pro.CostMicroUSD != 3374 {
		t.Fatalf("pro cost = %d, want 3374", pro.CostMicroUSD)
	}
	// InputTokens folds the cache tiers into the total: 1000 + 400 = 1400.
	if pro.InputTokens != 1400 || pro.OutputTokens != 200 {
		t.Fatalf("pro tokens = in %d out %d, want 1400/200", pro.InputTokens, pro.OutputTokens)
	}
	if !pro.OccurredAt.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("pro OccurredAt = %v", pro.OccurredAt)
	}

	// An unknown model is metered with a 0 (underived) cost, never a guessed one.
	exp := byModel["gemini-experimental-x"]
	if exp.CostMicroUSD != 0 {
		t.Fatalf("unknown-model cost = %d, want 0", exp.CostMicroUSD)
	}
	if exp.InputTokens != 5000 || exp.OutputTokens != 1000 {
		t.Fatalf("unknown-model tokens = in %d out %d, want 5000/1000", exp.InputTokens, exp.OutputTokens)
	}

	// Read-only: every request was a GET carrying the Google API-key header.
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s", r.Method)
		}
		if r.Header.Get("x-goog-api-key") != "test-goog-key" {
			t.Fatalf("x-goog-api-key header not sent")
		}
	}
}

func TestGather_OfflineNoUsageURLEmitsNothing(t *testing.T) {
	// With an api_key but no usage_url, Gather has no export to read (Gemini has no
	// native usage report) and must emit nothing.
	s := New()
	s.doer = &fixtureDoer{t: t}
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"api_key": "test-goog-key"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("gather without usage_url emitted %d observations, want 0", len(sink.obs))
	}
}

func TestSnapshot_LiveCatalog(t *testing.T) {
	s, doer := newLive(t)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if cat.Provider.Ref != modelprovider.ProviderGoogle || cat.Provider.Kind != modelprovider.KindHostedAPI {
		t.Fatalf("provider = %+v", cat.Provider)
	}
	if len(cat.Models) != 3 {
		t.Fatalf("models = %d, want 3", len(cat.Models))
	}

	pro, ok := cat.FindModel("gemini-2.5-pro")
	if !ok || pro.Pricing == nil || pro.Pricing.InputPerMTokUSD != 1.25 {
		t.Fatalf("pro model not enriched: %+v", pro)
	}
	// Live limits map from the models API; "models/" prefix is trimmed off the ref.
	if pro.ContextWindow != 1048576 || pro.MaxOutputTokens != 65536 {
		t.Fatalf("pro limits = ctx %d out %d, want 1048576/65536", pro.ContextWindow, pro.MaxOutputTokens)
	}
	// streamGenerateContent in supportedGenerationMethods => CapStreaming.
	if !pro.HasCapability(modelprovider.CapStreaming) {
		t.Fatal("pro missing CapStreaming despite streamGenerateContent")
	}
	if !pro.HasCapability(modelprovider.CapToolUse) {
		t.Fatal("pro missing a declared base capability")
	}

	// An unknown model keeps nil pricing rather than a guessed price; and with no
	// streamGenerateContent method it must NOT advertise streaming.
	exp, _ := cat.FindModel("gemini-experimental-x")
	if exp.Pricing != nil {
		t.Fatalf("unknown model got a guessed price: %+v", exp.Pricing)
	}
	if exp.HasCapability(modelprovider.CapStreaming) {
		t.Fatal("non-streaming model wrongly advertises CapStreaming")
	}

	// The generativelanguage API exposes no org key/workspace inventory.
	if len(cat.Keys) != 0 || len(cat.Workspaces) != 0 {
		t.Fatalf("keys/workspaces must be empty, got %d/%d", len(cat.Keys), len(cat.Workspaces))
	}

	// Read-only: the models request was a GET with the API-key header and pageSize.
	mReq := doer.reqs[0]
	if mReq.Method != http.MethodGet {
		t.Fatalf("models request was %s, want GET", mReq.Method)
	}
	if mReq.Header.Get("x-goog-api-key") != "test-goog-key" {
		t.Fatal("models request missing x-goog-api-key header")
	}
	if mReq.URL.Query().Get("pageSize") != "100" {
		t.Fatalf("models request pageSize = %q, want 100", mReq.URL.Query().Get("pageSize"))
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
	// Offline declared models still get family pricing + the declared capabilities.
	pro, ok := cat.FindModel("gemini-2.5-pro")
	if !ok || pro.Pricing == nil || pro.Pricing.OutputPerMTokUSD != 10.00 {
		t.Fatalf("offline pro not enriched: %+v", pro)
	}
	if len(cat.Keys) != 0 || len(cat.Workspaces) != 0 {
		t.Fatal("offline catalog must not contain key/workspace inventory")
	}
}

func TestPricingFor_LongestPrefix(t *testing.T) {
	cases := []struct {
		id        string
		wantIn    float64
		wantMatch bool
	}{
		{"gemini-2.5-pro", 1.25, true},
		{"gemini-2.5-pro-preview-05-06", 1.25, true},
		{"gemini-2.5-flash", 0.30, true},
		{"gemini-2.0-flash", 0.10, true},
		{"gemini-1.5-pro", 1.25, true},
		{"gemini-1.5-flash", 0.075, true},
		{"gemini-experimental-x", 0, false},
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
