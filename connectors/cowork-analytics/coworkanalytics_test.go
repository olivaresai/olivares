// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package coworkanalytics

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func fixedClock() time.Time { return time.Date(2026, 6, 9, 9, 0, 0, 0, time.UTC) }

// routeDoer answers GETs from a path-keyed handler, recording every request so a test
// can assert the connector is GET-only.
type routeDoer struct {
	reqs    []*http.Request
	handler func(*http.Request) (int, string)
}

func (d *routeDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	st, body := d.handler(req)
	return &http.Response{StatusCode: st, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
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

const secretEmail = "alice@corp.example"

// summariesJSON keys the rows by starting_date/ending_date — the shape REF (AsOf
// 2026-06-10) documents for /summaries — with NO legacy "date" field, so the test
// proves fetchSummary matches the requested day via starting_date. The 2026-06-08
// decoy row (absurd counts) simulates a widened range: the connector must pick the
// requested day's row, never blindly data[0].
func summariesJSON() string {
	return `{"data":[
		{"starting_date":"2026-06-08","ending_date":"2026-06-09",
		 "daily_active_user_count":999,"weekly_active_user_count":999,"monthly_active_user_count":999,
		 "assigned_seat_count":999,"pending_invite_count":999,
		 "cowork_daily_active_user_count":999,"cowork_weekly_active_user_count":999,"cowork_monthly_active_user_count":999},
		{"starting_date":"2026-06-09","ending_date":"2026-06-10",
		 "daily_active_user_count":50,"weekly_active_user_count":120,"monthly_active_user_count":300,
		 "assigned_seat_count":400,"pending_invite_count":5,
		 "cowork_daily_active_user_count":12,"cowork_weekly_active_user_count":40,"cowork_monthly_active_user_count":90}],
		"has_more":false,"data_refreshed_at":"2026-06-09T08:00:00Z"}`
}

// usersJSON carries all EIGHT verified cowork_metrics fields (REF, AsOf 2026-06-10).
// user_01A is fully active; user_01B is all-zero (inactive); user_01C carries ONLY
// message_count — REF now documents message_count as a Cowork metric ("Total user
// messages sent in Cowork"), so user_01C must count as ACTIVE (an earlier revision
// treated it as an unverified passthrough and counted the user inactive).
func usersJSON() string {
	return `{"data":[
		{"user":{"id":"user_01A","email_address":"` + secretEmail + `"},
		 "cowork_metrics":{"distinct_session_count":3,"message_count":20,"action_count":15,"dispatch_turn_count":8,
		   "skills_used_count":4,"distinct_skills_used_count":2,"connectors_used_count":6,"distinct_connectors_used_count":3}},
		{"user":{"id":"user_01B","email_address":"b@corp.example"},
		 "cowork_metrics":{"distinct_session_count":0,"message_count":0,"action_count":0,"dispatch_turn_count":0,
		   "skills_used_count":0,"distinct_skills_used_count":0,"connectors_used_count":0,"distinct_connectors_used_count":0}},
		{"user":{"id":"user_01C","email_address":"c@corp.example"},
		 "cowork_metrics":{"message_count":99}}
		],"has_more":false,"data_refreshed_at":"2026-06-09T08:00:00Z"}`
}

// skillsPage1JSON deliberately OMITS has_more (REF does not document it for this
// endpoint; the loop must follow next_page alone), lists rows out of count order
// (sorting must be the connector's), and carries an unread chat_metrics block
// (forward-compatible decode).
func skillsPage1JSON() string {
	return `{"data":[
		{"skill_name":"xlsx","distinct_user_count":11,"cowork_metrics":{"distinct_session_skill_used_count":9},
		 "chat_metrics":{"skill_used_count":40}},
		{"skill_name":"search","distinct_user_count":8,"cowork_metrics":{"distinct_session_skill_used_count":5}},
		{"skill_name":"pdf","distinct_user_count":15,"cowork_metrics":{"distinct_session_skill_used_count":12}},
		{"skill_name":"docx","distinct_user_count":6,"cowork_metrics":{"distinct_session_skill_used_count":5}}
		],"next_page":"page_sk_2"}`
}

// skillsPage2JSON ends the cursor (next_page null). legacy-macro has users on other
// surfaces (distinct_user_count=7) but an all-zero cowork_metrics block — REF says
// the block is always present, all-zero without Cowork usage — so it must NOT count.
func skillsPage2JSON() string {
	return `{"data":[
		{"skill_name":"legacy-macro","distinct_user_count":7,"cowork_metrics":{"distinct_session_skill_used_count":0}},
		{"skill_name":"pptx","distinct_user_count":3,"cowork_metrics":{"distinct_session_skill_used_count":3}},
		{"skill_name":"ocr","distinct_user_count":1,"cowork_metrics":{"distinct_session_skill_used_count":1}}
		],"has_more":false,"next_page":null,"data_refreshed_at":"2026-06-09T08:05:00Z"}`
}

// connectorsJSON is a single page; slack is chat-only (zero Cowork sessions) and
// must not count as a Cowork-used connector.
func connectorsJSON() string {
	return `{"data":[
		{"connector_name":"gdrive","distinct_user_count":4,"cowork_metrics":{"distinct_session_connector_used_count":4}},
		{"connector_name":"slack","distinct_user_count":5,"cowork_metrics":{"distinct_session_connector_used_count":0}},
		{"connector_name":"github","distinct_user_count":6,"cowork_metrics":{"distinct_session_connector_used_count":8}}
		],"has_more":false,"next_page":null,"data_refreshed_at":"2026-06-09T08:06:00Z"}`
}

// TestGatherEmitsEngagement verifies the connector polls (GET-only) the summaries,
// per-user, per-skill and per-connector endpoints and emits — in order — a coverage
// posture finding, one engagement-summary finding with the correct aggregated Cowork
// counts, and one breakdown finding per skills/connectors family — and never leaks
// user email.
func TestGatherEmitsEngagement(t *testing.T) {
	const day = "2026-06-09" // fixedClock's UTC day
	var skillsQueries, connectorsQueries []url.Values
	doer := &routeDoer{handler: func(req *http.Request) (int, string) {
		if req.Method != http.MethodGet {
			t.Errorf("non-GET request %s — connector must be read-only", req.Method)
		}
		q := req.URL.Query()
		switch req.URL.Path {
		case summariesPath:
			// summaries is a DATE-RANGE query: starting_date=day, ending_date=day+1 (exclusive).
			if q.Get("starting_date") != day || q.Get("ending_date") != "2026-06-10" {
				t.Errorf("summaries query = %q, want starting_date=%s ending_date=2026-06-10", req.URL.RawQuery, day)
			}
			if q.Has("date") || q.Has("limit") {
				t.Errorf("summaries must not carry date/limit (it is a date-range, non-paginated endpoint): %q", req.URL.RawQuery)
			}
			return http.StatusOK, summariesJSON()
		case usersPath:
			// the per-user feed is keyed by a single `date`.
			if q.Get("date") != day {
				t.Errorf("users query = %q, want date=%s", req.URL.RawQuery, day)
			}
			return http.StatusOK, usersJSON()
		case skillsPath:
			skillsQueries = append(skillsQueries, q)
			if len(skillsQueries) == 1 {
				return http.StatusOK, skillsPage1JSON()
			}
			return http.StatusOK, skillsPage2JSON()
		case connectorsPath:
			connectorsQueries = append(connectorsQueries, q)
			return http.StatusOK, connectorsJSON()
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
			return http.StatusNotFound, `{}`
		}
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"api_key": "sk-ant-admin01-test", "org_ref": "acme"}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}

	// Skills pagination: first page carries date+limit and NO cursor; the follow-up
	// must echo the next_page cursor in `page` and keep date+limit.
	if len(skillsQueries) != 2 {
		t.Fatalf("expected 2 skills pages, got %d", len(skillsQueries))
	}
	if q := skillsQueries[0]; q.Get("date") != day || q.Get("limit") != defaultPageLimit || q.Has("page") {
		t.Errorf("skills page-1 query = %v, want date=%s limit=%s and no page", q, day, defaultPageLimit)
	}
	if q := skillsQueries[1]; q.Get("date") != day || q.Get("limit") != defaultPageLimit || q.Get("page") != "page_sk_2" {
		t.Errorf("skills page-2 query = %v, want date=%s limit=%s page=page_sk_2", q, day, defaultPageLimit)
	}
	if len(connectorsQueries) != 1 {
		t.Fatalf("expected 1 connectors page, got %d", len(connectorsQueries))
	}
	if q := connectorsQueries[0]; q.Get("date") != day || q.Get("limit") != defaultPageLimit || q.Has("page") {
		t.Errorf("connectors query = %v, want date=%s limit=%s and no page", q, day, defaultPageLimit)
	}

	// Emission order: coverage → engagement → skills → connectors.
	findings := sink.findings()
	if len(findings) != 4 {
		t.Fatalf("expected coverage + engagement + skills + connectors findings, got %d: %+v", len(findings), findings)
	}
	if findings[0].Kind != findingKindCoverage {
		t.Errorf("finding[0].Kind = %q, want coverage first", findings[0].Kind)
	}
	if findings[1].Kind != findingKindEngagement || !strings.HasPrefix(findings[1].Title, "Cowork engagement ") {
		t.Errorf("finding[1] = %+v, want the engagement summary second", findings[1])
	}
	if !strings.HasPrefix(findings[2].Title, "Cowork skills ") {
		t.Errorf("finding[2].Title = %q, want the skills breakdown third", findings[2].Title)
	}
	if !strings.HasPrefix(findings[3].Title, "Cowork connectors ") {
		t.Errorf("finding[3].Title = %q, want the connectors breakdown fourth", findings[3].Title)
	}

	// Engagement: DAU/WAU/MAU from the day's summaries row (matched by starting_date,
	// not the 2026-06-08 decoy); active=2 (user_01A + the message-only user_01C — REF
	// AsOf 2026-06-10 verifies message_count as a Cowork metric), sessions=3,
	// messages=20+99=119, actions=15, dispatch=8, skill_inv=4, connector_inv=6.
	engagement := findings[1]
	if engagement.Severity != model.SeverityInfo || engagement.SubjectKind != "cowork_analytics" || engagement.SubjectRef != "acme" {
		t.Errorf("engagement finding = %+v", engagement)
	}
	for _, want := range []string{
		"DAU=12", "WAU=40", "MAU=90", "seats=400",
		"active=2", "sessions=3", "messages=119", "actions=15", "dispatch=8", "skill_inv=4", "connector_inv=6",
	} {
		if !strings.Contains(engagement.Title, want) {
			t.Errorf("engagement title missing %q: %s", want, engagement.Title)
		}
	}
	// The detail hash binds every aggregated field — including the summed
	// distinct_skills (2) / distinct_connectors (3) variants that do not headline.
	wantEngagementDetail := strings.Join([]string{
		"acme", day, "2026-06-09T08:00:00Z",
		"12", "40", "90", "400", "2", "3", "119", "15", "8", "4", "2", "6", "3",
	}, "|")
	if engagement.DetailHash != redact.Hash(wantEngagementDetail) {
		t.Errorf("engagement detail hash = %q, want hash of %q", engagement.DetailHash, wantEngagementDetail)
	}

	// Skills breakdown, field by field: 6 Cowork-used skills (legacy-macro's all-zero
	// cowork block does NOT count even with 7 users on other surfaces), 35 total
	// distinct-session uses, top-5 bounded (ocr cut) and deterministic (docx/search
	// tie at 5 broken by name).
	skillsF := findings[2]
	wantSkillsTitle := "Cowork skills 2026-06-09: 6 skills used in Cowork sessions (distinct-session uses=35); top: pdf, xlsx, docx, search, pptx"
	if skillsF.Kind != findingKindEngagement || skillsF.Severity != model.SeverityInfo ||
		skillsF.SubjectKind != "cowork_analytics" || skillsF.SubjectRef != "acme" {
		t.Errorf("skills finding = %+v", skillsF)
	}
	if skillsF.Title != wantSkillsTitle {
		t.Errorf("skills title = %q, want %q", skillsF.Title, wantSkillsTitle)
	}
	wantSkillsDetail := "acme|2026-06-09|2026-06-09T08:05:00Z|pdf:12|xlsx:9|docx:5|search:5|pptx:3|ocr:1"
	if skillsF.DetailHash != redact.Hash(wantSkillsDetail) {
		t.Errorf("skills detail hash = %q, want hash of %q", skillsF.DetailHash, wantSkillsDetail)
	}
	if !skillsF.OccurredAt.Equal(fixedClock()) {
		t.Errorf("skills occurredAt = %v", skillsF.OccurredAt)
	}

	// Connectors breakdown: slack's zero Cowork count does not count.
	connF := findings[3]
	wantConnTitle := "Cowork connectors 2026-06-09: 2 connectors used in Cowork sessions (distinct-session uses=12); top: github, gdrive"
	if connF.Kind != findingKindEngagement || connF.Severity != model.SeverityInfo ||
		connF.SubjectKind != "cowork_analytics" || connF.SubjectRef != "acme" {
		t.Errorf("connectors finding = %+v", connF)
	}
	if connF.Title != wantConnTitle {
		t.Errorf("connectors title = %q, want %q", connF.Title, wantConnTitle)
	}
	wantConnDetail := "acme|2026-06-09|2026-06-09T08:06:00Z|github:8|gdrive:4"
	if connF.DetailHash != redact.Hash(wantConnDetail) {
		t.Errorf("connectors detail hash = %q, want hash of %q", connF.DetailHash, wantConnDetail)
	}
	if !connF.OccurredAt.Equal(fixedClock()) {
		t.Errorf("connectors occurredAt = %v", connF.OccurredAt)
	}

	// Minimal-data: no user email may appear in any emitted finding's clear fields.
	for _, f := range findings {
		if strings.Contains(f.Title, secretEmail) || strings.Contains(f.SubjectRef, secretEmail) {
			t.Errorf("user email leaked into a finding: %+v", f)
		}
	}
}

// TestGatherOfflineNoKey proves the connector is a silent no-op without an API key
// (honest absence, no fabricated metric).
func TestGatherOfflineNoKey(t *testing.T) {
	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Errorf("offline Gather must emit nothing, got %d", len(sink.obs))
	}
}

// TestFetchSummaryDateKeyTolerance proves the legacy `date` row key still matches
// (summaryRow keeps Date for tolerance even though REF AsOf 2026-06-10 keys the row
// by starting_date).
func TestFetchSummaryDateKeyTolerance(t *testing.T) {
	doer := &routeDoer{handler: func(req *http.Request) (int, string) {
		if req.URL.Path != summariesPath {
			t.Errorf("unexpected path %s", req.URL.Path)
			return http.StatusNotFound, `{}`
		}
		return http.StatusOK, `{"data":[
			{"date":"2026-06-08","cowork_daily_active_user_count":999},
			{"date":"2026-06-09","cowork_daily_active_user_count":7}],
			"has_more":false,"data_refreshed_at":"2026-06-09T08:00:00Z"}`
	}}
	s := New()
	s.doer = doer
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"api_key": "sk-ant-admin01-test"}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	row, refreshedAt, err := s.fetchSummary(context.Background(), "2026-06-09")
	if err != nil {
		t.Fatalf("fetchSummary: %v", err)
	}
	if row.Date != "2026-06-09" || row.CoworkDailyActiveUserCount != 7 {
		t.Errorf("matched row = %+v, want the date-keyed 2026-06-09 row (DAU=7)", row)
	}
	if refreshedAt != "2026-06-09T08:00:00Z" {
		t.Errorf("refreshedAt = %q", refreshedAt)
	}
}

// TestSkillsPaginationBoundedByMaxPages proves a never-ending next_page cursor stops
// at max_pages (the pagination safety bound) instead of looping forever.
func TestSkillsPaginationBoundedByMaxPages(t *testing.T) {
	calls := 0
	doer := &routeDoer{handler: func(req *http.Request) (int, string) {
		if req.URL.Path != skillsPath {
			t.Errorf("unexpected path %s", req.URL.Path)
			return http.StatusNotFound, `{}`
		}
		calls++
		return http.StatusOK, `{"data":[{"skill_name":"pdf","distinct_user_count":1,
			"cowork_metrics":{"distinct_session_skill_used_count":1}}],"next_page":"again"}`
	}}
	s := New()
	s.doer = doer
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"api_key": "sk-ant-admin01-test", "max_pages": "2"}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	agg, _, err := s.fetchSkillsBreakdown(context.Background(), "2026-06-09")
	if err != nil {
		t.Fatalf("fetchSkillsBreakdown: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected exactly max_pages=2 requests, got %d", calls)
	}
	if agg.RowsWithCoworkUse != 2 || agg.TotalCoworkSessions != 2 {
		t.Errorf("aggregate = %+v, want the two fetched pages folded", agg)
	}
}

// TestUserAggregate folds all EIGHT verified cowork_metrics fields and counts a
// message_count-only user as ACTIVE (REF AsOf 2026-06-10: message_count is "Total
// user messages sent in Cowork" — a verified Cowork metric; it previously did not
// gate activity only because its name was unverified).
func TestUserAggregate(t *testing.T) {
	var agg userAggregate
	agg.add([]userRow{
		{CoworkMetrics: coworkMetrics{DistinctSessionCount: 2, MessageCount: 10, ActionCount: 5, DispatchTurnCount: 1,
			SkillsUsedCount: 3, DistinctSkillsUsedCount: 2, ConnectorsUsedCount: 4, DistinctConnectorsUsedCount: 1}},
		{CoworkMetrics: coworkMetrics{}},                // all-zero — inactive, not counted
		{CoworkMetrics: coworkMetrics{MessageCount: 7}}, // message-only — ACTIVE
		{CoworkMetrics: coworkMetrics{DistinctSessionCount: 1, ActionCount: 2}},
	})
	want := userAggregate{
		ActiveUsers:          3,
		Sessions:             3,
		Messages:             17,
		Actions:              7,
		DispatchTurns:        1,
		SkillInvocations:     3,
		DistinctSkills:       2,
		ConnectorInvocations: 4,
		DistinctConnectors:   1,
	}
	if agg != want {
		t.Errorf("aggregate = %+v, want %+v", agg, want)
	}
}

// TestCoworkMetricsActive proves each of the eight verified metrics — alone — makes
// a user Cowork-active, and an all-zero block does not.
func TestCoworkMetricsActive(t *testing.T) {
	if (coworkMetrics{}).active() {
		t.Error("all-zero cowork_metrics must be inactive")
	}
	cases := map[string]coworkMetrics{
		"distinct_session_count":         {DistinctSessionCount: 1},
		"message_count":                  {MessageCount: 1},
		"action_count":                   {ActionCount: 1},
		"dispatch_turn_count":            {DispatchTurnCount: 1},
		"skills_used_count":              {SkillsUsedCount: 1},
		"distinct_skills_used_count":     {DistinctSkillsUsedCount: 1},
		"connectors_used_count":          {ConnectorsUsedCount: 1},
		"distinct_connectors_used_count": {DistinctConnectorsUsedCount: 1},
	}
	for name, m := range cases {
		if !m.active() {
			t.Errorf("a user with only %s > 0 must count as Cowork-active", name)
		}
	}
}

func TestEngagementFindingMapping(t *testing.T) {
	sum := summaryRow{CoworkDailyActiveUserCount: 1, CoworkWeeklyActiveUserCount: 2, CoworkMonthlyActiveUserCount: 3, AssignedSeatCount: 9}
	agg := userAggregate{ActiveUsers: 1, Sessions: 5, Messages: 4, Actions: 6, DispatchTurns: 7,
		SkillInvocations: 8, DistinctSkills: 2, ConnectorInvocations: 9, DistinctConnectors: 3}
	f := engagementFinding("acme", "2026-06-09", sum, agg, "2026-06-09T08:00:00Z", fixedClock())
	if f.Kind != findingKindEngagement || f.Severity != model.SeverityInfo ||
		f.SubjectKind != "cowork_analytics" || f.SubjectRef != "acme" {
		t.Errorf("finding = %+v", f)
	}
	wantTitle := "Cowork engagement 2026-06-09: DAU=1 WAU=2 MAU=3 seats=9; active=1 sessions=5 messages=4 actions=6 dispatch=7 skill_inv=8 connector_inv=9"
	if f.Title != wantTitle {
		t.Errorf("title = %q, want %q", f.Title, wantTitle)
	}
	wantDetail := "acme|2026-06-09|2026-06-09T08:00:00Z|1|2|3|9|1|5|4|6|7|8|2|9|3"
	if f.DetailHash != redact.Hash(wantDetail) {
		t.Errorf("detail hash = %q, want hash of %q", f.DetailHash, wantDetail)
	}
	if !f.OccurredAt.Equal(fixedClock()) {
		t.Errorf("occurredAt = %v", f.OccurredAt)
	}
}

// TestBreakdownAggregateFold proves a zero-Cowork row is not folded and the ordering
// is count-descending with ties broken by name (deterministic regardless of API
// row order).
func TestBreakdownAggregateFold(t *testing.T) {
	var agg breakdownAggregate
	agg.add("slack", 0) // other-surface only — must NOT count
	agg.add("github", 8)
	agg.add("gdrive", 2)
	agg.add("asana", 2) // ties with gdrive; name order puts asana first
	if agg.RowsWithCoworkUse != 3 || agg.TotalCoworkSessions != 12 {
		t.Errorf("aggregate = %+v, want 3 rows / 12 sessions", agg)
	}
	got := agg.sorted()
	want := []nameCount{{Name: "github", Count: 8}, {Name: "asana", Count: 2}, {Name: "gdrive", Count: 2}}
	if len(got) != len(want) {
		t.Fatalf("sorted = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sorted[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestBreakdownFindingTopFiveBound proves the Title carries at most five names while
// the DetailHash binds the FULL ordered name:count list.
func TestBreakdownFindingTopFiveBound(t *testing.T) {
	var agg breakdownAggregate
	for _, e := range []nameCount{{Name: "a", Count: 1}, {Name: "b", Count: 2}, {Name: "c", Count: 3},
		{Name: "d", Count: 4}, {Name: "e", Count: 5}, {Name: "f", Count: 6}} {
		agg.add(e.Name, e.Count)
	}
	f := breakdownFinding("acme", "2026-06-09", "skills", agg, "2026-06-09T08:05:00Z", fixedClock())
	wantTitle := "Cowork skills 2026-06-09: 6 skills used in Cowork sessions (distinct-session uses=21); top: f, e, d, c, b"
	if f.Title != wantTitle {
		t.Errorf("title = %q, want %q (top-5 bounded, 'a' cut)", f.Title, wantTitle)
	}
	wantDetail := "acme|2026-06-09|2026-06-09T08:05:00Z|f:6|e:5|d:4|c:3|b:2|a:1"
	if f.DetailHash != redact.Hash(wantDetail) {
		t.Errorf("detail hash = %q, want hash of %q (full list, not top-5)", f.DetailHash, wantDetail)
	}
}

// TestBreakdownFindingEmpty proves a day with no Cowork skill/connector use yields an
// honest zero headline with no dangling "top:" suffix.
func TestBreakdownFindingEmpty(t *testing.T) {
	var agg breakdownAggregate
	f := breakdownFinding("acme", "2026-06-09", "connectors", agg, "", fixedClock())
	wantTitle := "Cowork connectors 2026-06-09: 0 connectors used in Cowork sessions (distinct-session uses=0)"
	if f.Title != wantTitle {
		t.Errorf("title = %q, want %q", f.Title, wantTitle)
	}
	if f.DetailHash != redact.Hash("acme|2026-06-09|") {
		t.Errorf("detail hash = %q, want hash of the empty list binding", f.DetailHash)
	}
}

func TestAnalyticsGaps(t *testing.T) {
	gaps := AnalyticsGaps()
	if len(gaps) < 3 {
		t.Fatalf("expected the documented gap inventory, got %d", len(gaps))
	}
	var cost *AnalyticsGap
	for i := range gaps {
		if gaps[i].ID == "cost-via-otel-not-here" {
			cost = &gaps[i]
		}
	}
	if cost == nil {
		t.Fatal("gaps must document that cost is ingested via OTEL, not here")
	}
	// The gap must be honest about the 2026-06-10 state: the user_cost_report row
	// shape IS published; exclusion is a deliberate double-count guard owned by the
	// FinOps reconciliation follow-up.
	if !strings.Contains(cost.Detail, "user_cost_report") || !strings.Contains(cost.Detail, "double-count") {
		t.Errorf("cost gap detail must name user_cost_report and the double-count rationale: %q", cost.Detail)
	}
	if !strings.Contains(cost.Owner, "FinOps") {
		t.Errorf("cost gap owner must be the FinOps reconciliation follow-up: %q", cost.Owner)
	}
}

func TestDescriptorShape(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource {
		t.Errorf("descriptor = %+v", d)
	}
	if d.Version != "0.2.0" {
		t.Errorf("version = %q, want 0.2.0 (per-skill/per-connector breakdowns added)", d.Version)
	}
	if !strings.Contains(d.Description, "per-skill/per-connector") {
		t.Errorf("description must mention the Cowork breakdowns: %q", d.Description)
	}
}
