// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vertex

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

func (f *fakeSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range f.obs {
		if fr, ok := o.(model.FindingReport); ok {
			out = append(out, fr)
		}
	}
	return out
}

// fixedClock is the deterministic pass time used in tests.
func fixedClock() time.Time { return time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC) }

// recordingServer routes requests by path to fixtures and records every request method/path
// so a test can assert the connector is read-only.
type recordingServer struct {
	t    *testing.T
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []string // "METHOD path"
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
		case strings.Contains(p, "/publishers/") && strings.Contains(p, "/models/"):
			rs.publisherModel(w, p)
		case strings.HasSuffix(p, "/timeSeries"):
			writeFixture(w, "timeseries_token_count.json")
		case strings.HasSuffix(p, "/billing-export"):
			writeFixture(w, "cost_export.json")
		case strings.HasSuffix(p, "/templates"):
			writeFixture(w, "armor_templates.json")
		case strings.HasSuffix(p, "/floorSetting"):
			writeFixture(w, "floor_setting.json")
		default:
			http.Error(w, "unexpected path "+p, http.StatusNotFound)
		}
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

// publisherModel returns a model resource keyed by the requested id: a 404 for
// claude-3-5-haiku (the tolerated per-model miss), a DEPRECATED stage for gemini-2.0-flash,
// else a GA stable model.
func (rs *recordingServer) publisherModel(w http.ResponseWriter, path string) {
	id := path[strings.LastIndex(path, "/")+1:]
	switch {
	case id == "claude-3-5-haiku":
		http.Error(w, "not found", http.StatusNotFound)
	case id == "gemini-2.0-flash":
		_, _ = w.Write([]byte(`{"name":"publishers/google/models/gemini-2.0-flash","versionId":"001","launchStage":"DEPRECATED","versionState":"PUBLISHER_MODEL_VERSION_STATE_DEPRECATED"}`))
	default:
		_, _ = w.Write([]byte(`{"name":"` + path + `","versionId":"001","launchStage":"GA","versionState":"PUBLISHER_MODEL_VERSION_STATE_STABLE"}`))
	}
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

// openSource builds a connector with a static token and all endpoints pointed at the test
// server, plus the given extra settings.
func openSource(t *testing.T, srv *recordingServer, extra map[string]string) *Source {
	t.Helper()
	settings := map[string]string{
		cfgAccessToken:         "test-token",
		cfgProject:             "test-proj",
		cfgAIPlatformEndpoint:  srv.srv.URL,
		cfgMonitoringEndpoint:  srv.srv.URL,
		cfgModelArmorEndpoint:  srv.srv.URL,
		cfgModelArmorGlobalURL: srv.srv.URL,
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
	if cat.Provider.Ref != modelprovider.ProviderGoogle {
		t.Errorf("provider ref = %q, want %q", cat.Provider.Ref, modelprovider.ProviderGoogle)
	}
	if len(cat.Models) != len(declaredModels) {
		t.Fatalf("offline models = %d, want %d", len(cat.Models), len(declaredModels))
	}
	for _, m := range cat.Models {
		if m.CapabilitySource != "declared" {
			t.Errorf("%s CapabilitySource = %q, want declared", m.Ref, m.CapabilitySource)
		}
		if m.Pricing == nil {
			t.Errorf("%s has nil pricing (every declared model has a family price)", m.Ref)
		}
	}
}

func TestSnapshotLiveEnrich(t *testing.T) {
	srv := newServer(t)
	s := openSource(t, srv, nil)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	byRef := map[string]modelprovider.Model{}
	for _, m := range cat.Models {
		byRef[m.Ref] = m
	}
	// gemini-2.0-flash enriched and marked deprecated.
	if m := byRef["gemini-2.0-flash"]; !m.Deprecated || m.CapabilitySource != "live" {
		t.Errorf("gemini-2.0-flash: deprecated=%v source=%q, want deprecated=true source=live", m.Deprecated, m.CapabilitySource)
	}
	// claude-3-5-haiku 404 tolerated: kept as declared.
	if m, ok := byRef["claude-3-5-haiku"]; !ok {
		t.Errorf("claude-3-5-haiku dropped on 404; want kept as declared")
	} else if m.CapabilitySource != "declared" {
		t.Errorf("claude-3-5-haiku source = %q, want declared (404 tolerated)", m.CapabilitySource)
	}
}

func TestGatherUsage(t *testing.T) {
	srv := newServer(t)
	s := openSource(t, srv, nil) // usage on by default; cost/armor off
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	costs := sink.costs()
	if len(costs) != 1 {
		t.Fatalf("usage costs = %d, want 1: %+v", len(costs), costs)
	}
	c := costs[0]
	if c.ModelRef != "gemini-2.0-flash" || c.InputTokens != 1000 || c.OutputTokens != 200 {
		t.Errorf("sample = %+v, want gemini-2.0-flash in=1000 out=200", c)
	}
	// gemini-2.0-flash list price: input 0.10, output 0.40 per MTok → 1000*0.10 + 200*0.40 = 180 µUSD.
	if c.CostMicroUSD != 180 {
		t.Errorf("derived cost = %d µUSD, want 180", c.CostMicroUSD)
	}
	if c.Gateway != model.GatewayVertex || c.Provenance != model.ProvenanceEstimated {
		t.Errorf("gateway/provenance = %q/%q, want vertex/estimated", c.Gateway, c.Provenance)
	}
	if c.ProviderRef != modelprovider.ProviderGoogle {
		t.Errorf("provider ref = %q, want %q", c.ProviderRef, modelprovider.ProviderGoogle)
	}
}

func TestGatherCost(t *testing.T) {
	srv := newServer(t)
	s := openSource(t, srv, map[string]string{
		cfgEnableUsage:   "false",
		cfgCostExportURL: srv.srv.URL + "/billing-export",
	})
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	costs := sink.costs()
	// Two non-zero rows (one billed, one negative credit); the exactly-zero row is skipped.
	if len(costs) != 2 {
		t.Fatalf("billed costs = %d, want 2: %+v", len(costs), costs)
	}
	var billed, credit *model.CostSample
	for i := range costs {
		switch costs[i].ModelRef {
		case "gemini-2.0-flash":
			billed = &costs[i]
		case "claude-sonnet-4-5":
			credit = &costs[i]
		}
	}
	if billed == nil || billed.CostMicroUSD != 12_500_000 || billed.Provenance != model.ProvenanceBilled {
		t.Errorf("billed = %+v, want 12500000 µUSD billed", billed)
	}
	if credit == nil || credit.CostMicroUSD != -1_250_000 {
		t.Errorf("credit = %+v, want -1250000 µUSD (kept so net spend reconciles)", credit)
	}
	for _, c := range costs {
		if c.InputTokens != 0 || c.OutputTokens != 0 {
			t.Errorf("billed sample %s carries tokens %d/%d, want 0/0 (separate lens)", c.ModelRef, c.InputTokens, c.OutputTokens)
		}
		if c.Gateway != model.GatewayVertex {
			t.Errorf("billed gateway = %q, want vertex", c.Gateway)
		}
	}
}

func TestGatherModelArmor(t *testing.T) {
	srv := newServer(t)
	s := openSource(t, srv, map[string]string{
		cfgEnableUsage:         "false",
		cfgEnableModelArmor:    "true",
		cfgModelArmorLocations: "us-central1",
	})
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()
	// two templates + one floor = three posture findings.
	if len(fs) != 3 {
		t.Fatalf("posture findings = %d, want 3: %+v", len(fs), fs)
	}
	var weak, strict, floor *model.FindingReport
	for i := range fs {
		switch {
		case fs[i].SubjectKind == subjectArmorFloor:
			floor = &fs[i]
		case strings.Contains(fs[i].SubjectRef, "weak-tmpl"):
			weak = &fs[i]
		case strings.Contains(fs[i].SubjectRef, "strict-tmpl"):
			strict = &fs[i]
		}
	}
	if weak == nil || weak.Severity != model.SeverityMedium {
		t.Errorf("weak template = %+v, want Medium (no RAI + PI disabled)", weak)
	}
	if strict == nil || strict.Severity != model.SeverityInfo {
		t.Errorf("strict template = %+v, want Info (RAI + PI enforced)", strict)
	}
	if floor == nil || floor.Severity != model.SeverityInfo {
		t.Errorf("floor = %+v, want Info (enforced, binds AI_PLATFORM, inspectAndBlock)", floor)
	}
	for _, f := range fs {
		if f.Kind != safetyPostureKind {
			t.Errorf("finding kind = %q, want %q", f.Kind, safetyPostureKind)
		}
		if f.DetailHash == "" {
			t.Errorf("finding %s has empty DetailHash", f.SubjectRef)
		}
	}
}

func TestGatherReadOnly(t *testing.T) {
	srv := newServer(t)
	s := openSource(t, srv, map[string]string{
		cfgEnableModelArmor:    "true",
		cfgModelArmorLocations: "us-central1",
		cfgCostExportURL:       srv.srv.URL + "/billing-export",
	})
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, r := range srv.requests() {
		if !strings.HasPrefix(r, http.MethodGet+" ") {
			t.Errorf("non-GET request %q — the connector must be read-only", r)
		}
	}
}

func TestDescriptorShape(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource {
		t.Errorf("descriptor = %q/%q, want %q/source", d.Name, d.Type, Name)
	}
	if len(d.ConfigFields) == 0 {
		t.Error("descriptor has no config fields")
	}
}

func TestParseDecimalUSDToMicros(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"12.50", 12_500_000},
		{"0.000001", 1},
		{"-1.25", -1_250_000},
		{"1.2345678", 1_234_568}, // round half-up on the 7th digit
		{"5", 5_000_000},
	}
	for _, c := range cases {
		got, err := parseDecimalUSDToMicros(c.in)
		if err != nil {
			t.Errorf("parse %q: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parse %q = %d, want %d", c.in, got, c.want)
		}
	}
	if _, err := parseDecimalUSDToMicros("1.2x"); err == nil {
		t.Error("parse of non-digit fraction should error")
	}
}
