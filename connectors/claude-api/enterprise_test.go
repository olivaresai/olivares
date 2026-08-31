// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// analyticsDoer answers the summaries endpoint and records the auth header + method so a
// test can assert the DISTINCT read:analytics key is used and the call is read-only.
type analyticsDoer struct {
	reqs     []*http.Request
	apiKeys  []string
	body     string
	notFound bool
}

func (d *analyticsDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	d.apiKeys = append(d.apiKeys, req.Header.Get("x-api-key"))
	if d.notFound {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{"error":"no"}`)), Header: make(http.Header)}, nil
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(d.body)), Header: make(http.Header)}, nil
}

const summaryFixture = `{
  "summaries": [
    {
      "starting_at": "2026-05-29T00:00:00Z",
      "ending_at": "2026-05-30T00:00:00Z",
      "assigned_seat_count": 118,
      "pending_invite_count": 4,
      "daily_active_user_count": 75,
      "weekly_active_user_count": 95,
      "monthly_active_user_count": 105,
      "daily_adoption_rate": 63.5,
      "weekly_adoption_rate": 80.5,
      "monthly_adoption_rate": 89.0,
      "cowork_daily_active_user_count": 9,
      "cowork_weekly_active_user_count": 14,
      "cowork_monthly_active_user_count": 19,
      "claude_code_daily_active_user_count": 20,
      "claude_code_weekly_active_user_count": 30,
      "claude_code_monthly_active_user_count": 0
    },
    {
      "starting_at": "2026-05-30T00:00:00Z",
      "ending_at": "2026-05-31T00:00:00Z",
      "assigned_seat_count": 120,
      "pending_invite_count": 3,
      "daily_active_user_count": 80,
      "weekly_active_user_count": 100,
      "monthly_active_user_count": 110,
      "daily_adoption_rate": 66.7,
      "weekly_adoption_rate": 83.3,
      "monthly_adoption_rate": 91.7,
      "cowork_daily_active_user_count": 10,
      "cowork_weekly_active_user_count": 15,
      "cowork_monthly_active_user_count": 20,
      "chat_daily_active_user_count": 70,
      "chat_weekly_active_user_count": 90,
      "chat_monthly_active_user_count": 100,
      "claude_code_daily_active_user_count": 25,
      "claude_code_weekly_active_user_count": 35,
      "claude_code_monthly_active_user_count": 40
    }
  ]
}`

func newAnalytics(t *testing.T, doer *analyticsDoer, gateway string) *Source {
	t.Helper()
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{"analytics_key": "sk-ant-analytics-test"}
	if gateway != "" {
		settings["gateway"] = gateway
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// TestEnterpriseAnalytics_DenyClosedWithoutKey proves the ingest is off (no call, no
// emission) when no analytics_key is configured — an honest absence, never a fabricated
// zero.
func TestEnterpriseAnalytics_DenyClosedWithoutKey(t *testing.T) {
	doer := &analyticsDoer{body: summaryFixture}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.gatherEnterpriseAnalytics(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("no analytics_key must make NO call, got %d", len(doer.reqs))
	}
	if len(sink.obs) != 0 {
		t.Fatalf("no analytics_key must emit nothing, got %d", len(sink.obs))
	}
}

// TestEnterpriseAnalytics_EmitsRollupWithDistinctKey proves the verified summaries
// envelope is ingested as engagement evidence (never a CostSample — the double-count
// boundary), carries only the latest returned day in the FindingReport, emits the
// official org-level Claude Code active-user MetricSamples for every returned day with
// present non-zero counts, authenticates with the DISTINCT read:analytics key, and is
// read-only (GET).
func TestEnterpriseAnalytics_EmitsRollupWithDistinctKey(t *testing.T) {
	doer := &analyticsDoer{body: summaryFixture}
	s := newAnalytics(t, doer, "")
	sink := &captureSink{}
	if err := s.gatherEnterpriseAnalytics(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("want 1 summaries call, got %d", len(doer.reqs))
	}
	if doer.reqs[0].Method != http.MethodGet {
		t.Errorf("method = %s, want GET (read-only)", doer.reqs[0].Method)
	}
	if doer.reqs[0].URL.Path != analyticsSummariesPath {
		t.Errorf("path = %s, want %s", doer.reqs[0].URL.Path, analyticsSummariesPath)
	}
	q := doer.reqs[0].URL.Query()
	if got := q.Get("starting_date"); got != "2026-05-26" {
		t.Errorf("starting_date = %q, want trailing window start 2026-05-26", got)
	}
	if got := q.Get("ending_date"); got != "" {
		t.Errorf("ending_date = %q, want omitted so server picks newest available day", got)
	}
	if doer.apiKeys[0] != "sk-ant-analytics-test" {
		t.Errorf("auth = %q, want the DISTINCT analytics key (not the admin key)", doer.apiKeys[0])
	}

	var finding *model.FindingReport
	var samples []model.MetricSample
	for _, o := range sink.obs {
		switch v := o.(type) {
		case model.FindingReport:
			f := v
			finding = &f
		case model.MetricSample:
			if v.Name == metricClaudeCodeActiveUsers {
				samples = append(samples, v)
			}
		case model.CostSample:
			t.Fatalf("Enterprise Analytics must never emit cost (would double-count Usage&Cost): %+v", v)
		}
	}
	if finding == nil {
		t.Fatal("Enterprise Analytics must emit one FindingReport for the latest returned day")
	}
	if finding.Kind != "analytics" || finding.SubjectKind != subjectEnterpriseAnalytics {
		t.Errorf("finding = %s/%s", finding.Kind, finding.SubjectKind)
	}
	for _, want := range []string{
		"2026-05-30", "DAU=80", "WAU=100", "MAU=110",
		"adoption 66.7%/83.3%/91.7%", "Cowork DAU=10",
		"Chat DAU=70", "Claude Code DAU=25",
	} {
		if !strings.Contains(finding.Title, want) {
			t.Errorf("title %q missing %q", finding.Title, want)
		}
	}
	if strings.Contains(finding.Title, "to-confirm") {
		t.Errorf("verified schema title must not carry to-confirm marker: %q", finding.Title)
	}
	if len(samples) != 5 {
		t.Fatalf("active-user samples = %d, want 5 (two old + three latest; zero skipped)", len(samples))
	}
	wantSamples := map[string]int64{
		"2026-05-29T00:00:00Z|daily":   20,
		"2026-05-29T00:00:00Z|weekly":  30,
		"2026-05-30T00:00:00Z|daily":   25,
		"2026-05-30T00:00:00Z|weekly":  35,
		"2026-05-30T00:00:00Z|monthly": 40,
	}
	for _, sample := range samples {
		if sample.Unit != "users" || sample.Additive ||
			sample.SubjectKind != "organization" || sample.SubjectRef != "organization" {
			t.Errorf("sample shape = %+v", sample)
		}
		if sample.Dimensions["plane"] != "official_enterprise" {
			t.Errorf("sample plane = %q, want official_enterprise", sample.Dimensions["plane"])
		}
		key := sample.OccurredAt.Format(time.RFC3339) + "|" + sample.Dimensions["window"]
		want, ok := wantSamples[key]
		if !ok {
			t.Errorf("unexpected active-user sample %+v", sample)
			continue
		}
		if sample.Value != want {
			t.Errorf("%s value = %d, want %d", key, sample.Value, want)
		}
		delete(wantSamples, key)
	}
	if len(wantSamples) != 0 {
		t.Fatalf("missing active-user samples: %+v", wantSamples)
	}
}

// TestEnterpriseAnalytics_EmptySummariesEmitsNothing proves freshness lag is treated as
// honest absence: the connector does not fabricate a zero finding when no day is
// currently queryable.
func TestEnterpriseAnalytics_EmptySummariesEmitsNothing(t *testing.T) {
	doer := &analyticsDoer{body: `{"summaries":[]}`}
	s := newAnalytics(t, doer, "")
	sink := &captureSink{}
	if err := s.gatherEnterpriseAnalytics(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("want 1 summaries call, got %d", len(doer.reqs))
	}
	if len(sink.obs) != 0 {
		t.Fatalf("empty summaries must emit nothing, got %d", len(sink.obs))
	}
}

// TestEnterpriseAnalytics_SurfacesAPIError proves a non-2xx from the analytics endpoint
// is surfaced as an error (the engine retries), never swallowed or faked as an empty
// roll-up.
func TestEnterpriseAnalytics_SurfacesAPIError(t *testing.T) {
	doer := &analyticsDoer{notFound: true}
	s := newAnalytics(t, doer, "")
	sink := &captureSink{}
	if err := s.gatherEnterpriseAnalytics(context.Background(), sink); err == nil {
		t.Fatal("a 404 from the analytics endpoint must surface as an error")
	}
	if len(sink.obs) != 0 {
		t.Fatalf("an API error must emit no (fabricated) roll-up, got %d", len(sink.obs))
	}
}

// TestEnterpriseAnalytics_SkippedOnPartnerSurface proves the ingest is skipped on a
// surface without the Anthropic-operated admin plane (Bedrock/Vertex/Foundry) — the key
// would not be provisioned there, so it never polls an endpoint that does not exist.
func TestEnterpriseAnalytics_SkippedOnPartnerSurface(t *testing.T) {
	doer := &analyticsDoer{body: summaryFixture}
	s := newAnalytics(t, doer, string(model.GatewayBedrockMantle))
	sink := &captureSink{}
	if err := s.gatherEnterpriseAnalytics(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(doer.reqs) != 0 || len(sink.obs) != 0 {
		t.Fatalf("partner surface must skip Enterprise Analytics, got %d calls / %d emissions", len(doer.reqs), len(sink.obs))
	}
}
