// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package paperclip

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	testKey      = "pcp_board_0123456789abcdef"
	leakedSecret = "SECRET-IN-ADAPTER-CONFIG-DO-NOT-LEAK"

	companyAcme = "11111111-1111-4111-8111-111111111111"
	companySide = "22222222-2222-4222-8222-222222222222"
	agentAda    = "aaaaaaa1-0000-4000-8000-000000000001"
	agentLinus  = "aaaaaaa2-0000-4000-8000-000000000002"
	agentRetire = "aaaaaaa3-0000-4000-8000-000000000003"
	agentSolo   = "bbbbbbb1-0000-4000-8000-000000000001"
)

var fixedNow = time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)

var costsPath = regexp.MustCompile(`^/api/companies/([^/]+)/costs/by-agent-model$`)

// fake serves the recorded fixtures as a read-only Paperclip. It fails the test
// on any non-GET request or a request without the board key, and records every
// request line so a test can prove what the connector asked for.
type fake struct {
	t        *testing.T
	mu       sync.Mutex
	requests []string
	// status overrides the answer for a path (exact match on r.URL.Path).
	status map[string]int
	// echoKeyOnError makes an error answer reproduce the Authorization header.
	echoKeyOnError bool
	// body overrides the 200 answer for a path with a raw body.
	body map[string]string
	// queries holds the raw query of the last request per path.
	queries map[string]string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if f.queries == nil {
		f.queries = map[string]string{}
	}
	f.queries[r.URL.Path] = r.URL.RawQuery
	f.mu.Unlock()
	if r.Method != http.MethodGet {
		f.t.Errorf("connector sent %s %s; it must be read-only", r.Method, r.URL.Path)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+testKey {
		http.Error(w, "unauthorized: "+got, http.StatusUnauthorized)
		return
	}
	if code, ok := f.status[r.URL.Path]; ok {
		body := "denied"
		if f.echoKeyOnError {
			body = "denied for " + r.Header.Get("Authorization")
		}
		http.Error(w, body, code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if b, ok := f.body[r.URL.Path]; ok {
		_, _ = w.Write([]byte(b))
		return
	}
	file := f.fixtureFor(r)
	data, err := os.ReadFile(file)
	if err != nil {
		// Only a day with no recorded spend is empty; any other missing fixture is a typo.
		if !costsPath.MatchString(r.URL.Path) {
			f.t.Errorf("no fixture %s for %s", file, r.URL.Path)
		}
		data = []byte("[]")
	}
	_, _ = w.Write(data)
}

func (f *fake) fixtureFor(r *http.Request) string {
	p := r.URL.Path
	switch {
	case p == "/api/companies":
		return filepath.Join("testdata", "companies.json")
	case strings.HasSuffix(p, "/agents"):
		return filepath.Join("testdata", "agents-"+strings.Split(p, "/")[3]+".json")
	case strings.HasSuffix(p, "/heartbeat-runs"):
		return filepath.Join("testdata", "runs-"+strings.Split(p, "/")[3]+".json")
	}
	if m := costsPath.FindStringSubmatch(p); m != nil {
		day := strings.SplitN(r.URL.Query().Get("from"), "T", 2)[0]
		return filepath.Join("testdata", fmt.Sprintf("costs-%s-%s.json", m[1], day))
	}
	return filepath.Join("testdata", "missing.json")
}

func (f *fake) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

type collectSink struct{ obs []model.Observation }

func (c *collectSink) Emit(_ context.Context, o model.Observation) error {
	c.obs = append(c.obs, o)
	return nil
}

func open(t *testing.T, f *fake, extra map[string]string) *Source {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	settings := map[string]string{"base_url": srv.URL, "api_key": testKey}
	for k, v := range extra {
		settings[k] = v
	}
	s := New()
	s.now = func() time.Time { return fixedNow }
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestGatherMapsRunsAndCostsToObservations(t *testing.T) {
	f := &fake{t: t}
	s := open(t, f, nil)
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var costs []model.CostSample
	var metrics []model.MetricSample
	var findings []model.FindingReport
	for _, o := range sink.obs {
		switch v := o.(type) {
		case model.CostSample:
			costs = append(costs, v)
		case model.MetricSample:
			metrics = append(metrics, v)
		case model.FindingReport:
			findings = append(findings, v)
		default:
			t.Errorf("unexpected observation %T", o)
		}
	}

	if len(costs) != 3 {
		t.Fatalf("cost samples = %+v", costs)
	}
	var gpt model.CostSample
	for _, c := range costs {
		if c.ModelRef == "gpt-5" && c.OccurredAt.Day() == 4 {
			gpt = c
		}
	}
	want := model.CostSample{
		ProviderRef:  "openai",
		ModelRef:     "gpt-5",
		InputTokens:  120000,
		OutputTokens: 4000,
		CostMicroUSD: 1_250_000,
		OccurredAt:   time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
		WorkspaceRef: "paperclip/company/" + companyAcme,
		Actor:        "agent:paperclip/agent/" + agentLinus,
		Gateway:      model.GatewayDirect,
		Provenance:   model.ProvenanceEstimated,
		CostType:     "paperclip",
	}
	if fmt.Sprintf("%+v", gpt) != fmt.Sprintf("%+v", want) {
		t.Errorf("cost sample\n got %+v\nwant %+v", gpt, want)
	}

	got := map[string]int64{}
	for _, m := range metrics {
		if m.Name != "paperclip.heartbeat_runs" || m.SubjectKind != "agent" || m.Additive || m.Unit != "runs" {
			t.Errorf("metric = %+v", m)
		}
		key := fmt.Sprintf("%s|%s|%s", m.SubjectRef, m.OccurredAt.Format("2006-01-02"), m.Dimensions["status"])
		got[key] = m.Value
	}
	wantRuns := map[string]int64{
		"paperclip/agent/" + agentLinus + "|2026-10-04|succeeded": 2,
		"paperclip/agent/" + agentLinus + "|2026-10-04|failed":    1,
		"paperclip/agent/" + agentAda + "|2026-10-03|succeeded":   1,
	}
	if fmt.Sprint(got) != fmt.Sprint(wantRuns) {
		t.Errorf("run metrics\n got %v\nwant %v", got, wantRuns)
	}

	if len(findings) == 0 || findings[0].Kind != "inventory" || !strings.Contains(findings[0].Title, "companies=2") || !strings.Contains(findings[0].Title, "agents=4") {
		t.Errorf("inventory finding = %+v", findings)
	}

	for _, o := range sink.obs {
		if strings.Contains(fmt.Sprintf("%+v", o), leakedSecret) || strings.Contains(fmt.Sprintf("%+v", o), testKey) {
			t.Errorf("observation carries credential material: %+v", o)
		}
	}
}

func TestOnlyReadsAndOnlyCompletedDays(t *testing.T) {
	f := &fake{t: t}
	s := open(t, f, nil)
	if err := s.Gather(context.Background(), &collectSink{}); err != nil {
		t.Fatal(err)
	}
	for _, line := range f.paths() {
		if !strings.HasPrefix(line, "GET /api/") {
			t.Errorf("request %q is not a read of the Paperclip API", line)
		}
	}
	// Three lookback days (Oct 3, 4, 5) per company; today (Oct 6) is still open.
	costReads := 0
	for _, line := range f.paths() {
		if strings.Contains(line, "/costs/by-agent-model") {
			costReads++
		}
	}
	if costReads != 6 {
		t.Errorf("cost reads = %d, want 6 (3 completed days x 2 companies)", costReads)
	}
}

func TestUnreadableLegIsReportedNotSilent(t *testing.T) {
	f := &fake{t: t, status: map[string]int{
		"/api/companies/" + companySide + "/costs/by-agent-model": http.StatusForbidden,
	}}
	s := open(t, f, nil)
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("a 403 on one leg must not fail the gather: %v", err)
	}
	var coverage []model.FindingReport
	for _, o := range sink.obs {
		if fr, ok := o.(model.FindingReport); ok && fr.Kind == "coverage" {
			coverage = append(coverage, fr)
		}
	}
	if len(coverage) != 1 || !strings.Contains(coverage[0].Title, "costs") || !strings.Contains(coverage[0].Title, "403") || coverage[0].SubjectRef != "paperclip/company/"+companySide {
		t.Fatalf("coverage findings = %+v", coverage)
	}
	// The other company still yields its costs.
	costs := 0
	for _, o := range sink.obs {
		if _, ok := o.(model.CostSample); ok {
			costs++
		}
	}
	if costs != 3 {
		t.Errorf("cost samples = %d, want 3", costs)
	}
}

func TestRunListAtTheCapIsReportedAsTruncated(t *testing.T) {
	f := &fake{t: t}
	s := open(t, f, nil)
	s.maxRuns = 7 // the fixture holds exactly 7 runs for the first company
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range sink.obs {
		if fr, ok := o.(model.FindingReport); ok && fr.Kind == "coverage" && strings.Contains(fr.Title, "runs") && strings.Contains(fr.Title, "truncated") {
			found = true
		}
	}
	if !found {
		t.Error("a run list that reached its cap must be reported as possibly truncated")
	}
}

func TestDescriptorDeclaresTheKeyAsSecret(t *testing.T) {
	d := New().Descriptor()
	if d.Type != sdk.TypeSource || d.Name != Name {
		t.Errorf("descriptor = %+v", d)
	}
	for _, f := range d.ConfigFields {
		if f.Key == "api_key" && !f.Secret {
			t.Error("api_key must be a Secret config field")
		}
	}
}
