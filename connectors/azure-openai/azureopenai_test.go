// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureopenai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// fakeSink collects every emitted observation for assertion.
type fakeSink struct {
	mu  sync.Mutex
	obs []model.Observation
}

func (f *fakeSink) Emit(_ context.Context, o model.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obs = append(f.obs, o)
	return nil
}

func (f *fakeSink) costs() []model.CostSample {
	var out []model.CostSample
	for _, o := range f.obs {
		if c, ok := o.(model.CostSample); ok {
			out = append(out, c)
		}
	}
	return out
}

func fixedClock() time.Time { return time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC) }

// recordingServer routes ARM requests by path/method to fixtures and records every request
// so a test can assert the connector is read-only.
type recordingServer struct {
	t    *testing.T
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []string
}

func newServer(t *testing.T) *recordingServer {
	t.Helper()
	rs := &recordingServer{t: t}
	rs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.reqs = append(rs.reqs, r.Method+" "+r.URL.Path)
		rs.mu.Unlock()

		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/Microsoft.CostManagement/query"):
			writeFixture(w, "cost_query.json")
		case strings.HasSuffix(p, "/Microsoft.Insights/metrics"):
			writeFixture(w, "metrics.json")
		case strings.HasSuffix(p, "/deployments"):
			writeFixture(w, "deployments.json")
		case strings.HasSuffix(p, "/models"):
			writeFixture(w, "account_models.json")
		case strings.HasSuffix(p, "/Microsoft.CognitiveServices/accounts"):
			writeFixture(w, "accounts.json")
		case p == "/subscriptions":
			writeFixture(w, "subscriptions.json")
		default:
			http.Error(w, "unexpected path "+p, http.StatusNotFound)
		}
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *recordingServer) requests() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]string(nil), rs.reqs...)
}

func writeFixture(w http.ResponseWriter, name string) {
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func openSource(t *testing.T, srv *recordingServer, extra map[string]string) *Source {
	t.Helper()
	settings := map[string]string{
		cfgAccessToken:        "test-token",
		cfgSubscriptions:      "sub-1",
		cfgManagementEndpoint: srv.srv.URL,
	}
	for k, v := range extra {
		settings[k] = v
	}
	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestSnapshotOffline(t *testing.T) {
	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if cat.Provider.Ref != modelprovider.ProviderAzureOpenAI {
		t.Errorf("provider ref = %q, want %q", cat.Provider.Ref, modelprovider.ProviderAzureOpenAI)
	}
	if len(cat.Models) != 0 || len(cat.Workspaces) != 0 {
		t.Errorf("offline catalog should be empty, got %d models / %d workspaces", len(cat.Models), len(cat.Workspaces))
	}
}

func TestSnapshotLive(t *testing.T) {
	srv := newServer(t)
	s := openSource(t, srv, nil)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// One LLM-hosting account (speechy filtered out by kind).
	if len(cat.Workspaces) != 1 || cat.Workspaces[0].Name != "acct1" {
		t.Fatalf("workspaces = %+v, want one (acct1)", cat.Workspaces)
	}
	if cat.Workspaces[0].Geo != "eastus" {
		t.Errorf("workspace geo = %q, want eastus", cat.Workspaces[0].Geo)
	}
	// Two deployments → two models, keyed by deployment name.
	byRef := map[string]modelprovider.Model{}
	for _, m := range cat.Models {
		byRef[m.Ref] = m
	}
	if len(byRef) != 2 {
		t.Fatalf("models = %d, want 2: %+v", len(byRef), cat.Models)
	}
	gpt := byRef["gpt4o-prod"]
	if gpt.Pricing == nil || gpt.Pricing.InputPerMTokUSD != 2.50 {
		t.Errorf("gpt4o-prod pricing = %+v, want input 2.50 (gpt-4o family)", gpt.Pricing)
	}
	if !gpt.Deprecated {
		t.Errorf("gpt4o-prod should be Deprecated (lifecycleStatus Deprecating)")
	}
	if len(gpt.Retirements) != 1 || gpt.Retirements[0].Surface != model.GatewayFoundry || gpt.Retirements[0].RetiresOn != "2026-12-01" {
		t.Errorf("gpt4o-prod retirements = %+v, want one foundry 2026-12-01", gpt.Retirements)
	}
	claude := byRef["claude-sonnet"]
	if claude.Pricing == nil || claude.Pricing.OutputPerMTokUSD != 15.00 {
		t.Errorf("claude-sonnet pricing = %+v, want output 15.00 (claude-sonnet family)", claude.Pricing)
	}
	if !modelprovider.Has(claude.Capabilities, modelprovider.CapExtendedThinking) {
		t.Errorf("claude-sonnet should declare extended thinking")
	}
}

func TestGatherUsage(t *testing.T) {
	srv := newServer(t)
	s := openSource(t, srv, nil) // usage on by default; cost off
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	costs := sink.costs()
	if len(costs) != 1 {
		t.Fatalf("usage costs = %d, want 1: %+v", len(costs), costs)
	}
	c := costs[0]
	if c.ModelRef != "gpt4o-prod" || c.InputTokens != 1000 || c.OutputTokens != 200 {
		t.Errorf("sample = %+v, want gpt4o-prod in=1000 out=200", c)
	}
	// gpt-4o list price input 2.50, output 10.00 per MTok → 1000*2.50 + 200*10.00 = 4500 µUSD.
	if c.CostMicroUSD != 4500 {
		t.Errorf("derived cost = %d µUSD, want 4500", c.CostMicroUSD)
	}
	if c.Gateway != model.GatewayFoundry || c.Provenance != model.ProvenanceEstimated {
		t.Errorf("gateway/provenance = %q/%q, want foundry/estimated", c.Gateway, c.Provenance)
	}
	if c.ProviderRef != modelprovider.ProviderAzureOpenAI {
		t.Errorf("provider ref = %q, want %q", c.ProviderRef, modelprovider.ProviderAzureOpenAI)
	}
	if !strings.HasSuffix(c.WorkspaceRef, "/accounts/acct1") {
		t.Errorf("workspace ref = %q, want the account ARM id", c.WorkspaceRef)
	}
}

func TestGatherCost(t *testing.T) {
	srv := newServer(t)
	s := openSource(t, srv, map[string]string{
		cfgEnableUsage: "false",
		cfgEnableCost:  "true",
	})
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	costs := sink.costs()
	// Two non-zero rows (billed + negative credit); the exactly-zero row is skipped.
	if len(costs) != 2 {
		t.Fatalf("billed costs = %d, want 2: %+v", len(costs), costs)
	}
	var pos, credit *model.CostSample
	for i := range costs {
		switch {
		case costs[i].CostMicroUSD > 0:
			pos = &costs[i]
		case costs[i].CostMicroUSD < 0:
			credit = &costs[i]
		}
	}
	if pos == nil || pos.CostMicroUSD != 12_500_000 {
		t.Errorf("billed = %+v, want 12500000 µUSD", pos)
	}
	if pos != nil && pos.ModelRef != "gpt-4o-Input-glbl" {
		t.Errorf("billed ModelRef = %q, want the meter (gpt-4o-Input-glbl)", pos.ModelRef)
	}
	if pos != nil && pos.Provenance != model.ProvenanceEstimated {
		t.Errorf("recent-day provenance = %q, want estimated (within finalization lag)", pos.Provenance)
	}
	if credit == nil || credit.CostMicroUSD != -1_250_000 {
		t.Errorf("credit = %+v, want -1250000 µUSD (kept)", credit)
	}
	for _, c := range costs {
		if c.InputTokens != 0 || c.OutputTokens != 0 {
			t.Errorf("billed sample carries tokens %d/%d, want 0/0 (separate lens)", c.InputTokens, c.OutputTokens)
		}
		if c.Gateway != model.GatewayFoundry {
			t.Errorf("billed gateway = %q, want foundry", c.Gateway)
		}
		if !strings.HasSuffix(c.WorkspaceRef, "/accounts/acct1") {
			t.Errorf("billed workspace = %q, want the account ARM id", c.WorkspaceRef)
		}
	}
}

func TestGatherCostBilledWhenFinalized(t *testing.T) {
	srv := newServer(t)
	s := openSource(t, srv, map[string]string{
		cfgEnableUsage:         "false",
		cfgEnableCost:          "true",
		cfgCostFinalizationLag: "12h", // the 1.5-day-old row is now past the window → billed
	})
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, c := range sink.costs() {
		if c.CostMicroUSD > 0 && c.Provenance != model.ProvenanceBilled {
			t.Errorf("finalized day provenance = %q, want billed", c.Provenance)
		}
	}
}

func TestGatherReadOnly(t *testing.T) {
	srv := newServer(t)
	s := openSource(t, srv, map[string]string{cfgEnableCost: "true"})
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, r := range srv.requests() {
		switch {
		case strings.HasPrefix(r, http.MethodGet+" "):
			// reads are fine
		case strings.HasPrefix(r, http.MethodPost+" ") && strings.HasSuffix(r, "/query"):
			// the Cost Management Query is a POST action that is a READ
		default:
			t.Errorf("non-read request %q — the connector must be read-only (only GET + the Cost Management query POST)", r)
		}
	}
}

func TestAutoListSubscriptions(t *testing.T) {
	srv := newServer(t)
	s := openSource(t, srv, map[string]string{cfgSubscriptions: ""}) // force auto-list
	subs, err := s.resolveSubscriptions(context.Background())
	if err != nil {
		t.Fatalf("resolveSubscriptions: %v", err)
	}
	if len(subs) != 1 || subs[0] != "sub-auto" {
		t.Errorf("auto-listed subs = %v, want [sub-auto]", subs)
	}
}

func TestDescriptorShape(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource {
		t.Errorf("descriptor = %q/%q, want %q/source", d.Name, d.Type, Name)
	}
	if Name != "olivares.azure-openai" {
		t.Errorf("Name = %q, want olivares.azure-openai", Name)
	}
}

func TestCostDayAndParse(t *testing.T) {
	d := costDay("20260619", fixedClock())
	if d.Year() != 2026 || d.Month() != 6 || d.Day() != 19 {
		t.Errorf("costDay = %v, want 2026-06-19", d)
	}
	if got, _ := parseDecimalUSDToMicros("12.5"); got != 12_500_000 {
		t.Errorf("parse 12.5 = %d, want 12500000", got)
	}
	if got, _ := parseDecimalUSDToMicros("-1.25"); got != -1_250_000 {
		t.Errorf("parse -1.25 = %d, want -1250000", got)
	}
}
