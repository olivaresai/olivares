// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package coworkanalytics

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.cowork-analytics"

// Finding kinds. analytics_summary is the per-Gather engagement headline; posture
// is the once-per-Gather coverage record (distinct so it never pollutes an
// engagement count a consumer keys on).
const (
	findingKindEngagement = "analytics_summary"
	findingKindCoverage   = "posture"
)

// Default configuration values.
const (
	defaultBaseURL          = "https://api.anthropic.com"
	defaultAnthropicVersion = "2023-06-01"
	defaultOrgRef           = "anthropic-org"
	defaultMaxPages         = 50
	defaultPageLimit        = "1000" // analytics limit max is 1000

	summariesPath  = "/v1/organizations/analytics/summaries"
	usersPath      = "/v1/organizations/analytics/users"
	skillsPath     = "/v1/organizations/analytics/skills"
	connectorsPath = "/v1/organizations/analytics/connectors"
	dateLayout     = "2006-01-02"

	// topBreakdownNames bounds how many skill/connector names ride a breakdown
	// finding's Title (the full ordered list is bound by the DetailHash).
	topBreakdownNames = 5
)

// Source is the Cowork engagement connector. It satisfies sdk.SourceConnector:
// Gather polls the analytics endpoints and emits a coverage finding plus one
// engagement-summary finding per run.
type Source struct {
	apiKey   string
	baseURL  string
	version  string
	orgRef   string
	date     string // YYYY-MM-DD; empty = current UTC day at Gather
	maxPages int

	client *modelprovider.Client
	doer   modelprovider.Doer // injected transport (tests); nil => default
	now    func() time.Time   // injectable clock (tests); nil => time.Now
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Cowork analytics connector with default configuration.
func New() *Source {
	return &Source{baseURL: defaultBaseURL, version: defaultAnthropicVersion, orgRef: defaultOrgRef, maxPages: defaultMaxPages}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.2.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Claude Cowork Analytics (engagement)",
		Description: "Read-only: polls the Claude Enterprise Analytics API and emits the Cowork activity signal — org-wide Cowork DAU/WAU/MAU, aggregate per-user engagement (sessions, messages, actions, dispatch turns, skill/connector invocations), and the per-skill/per-connector Cowork breakdowns (distinct-session use counts, top names) — to the dashboards/FinOps surfaces, plus an honest coverage finding. Cowork COST flows via the OTEL connector (not re-ingested here, to avoid double-counting). Enterprise-gated: offline without an API key (no fabricated metrics).",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "Anthropic Admin API key reference (read:analytics scope; read-only; never persisted). Empty = offline (no engagement emitted)."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Anthropic API base URL."},
			{Key: "anthropic_version", Type: sdk.FieldString, Default: defaultAnthropicVersion, Description: "anthropic-version header value."},
			{Key: "org_ref", Type: sdk.FieldString, Default: defaultOrgRef, Description: "Stable reference for the governed org (the engagement subject)."},
			{Key: "date", Type: sdk.FieldString, Default: "", Description: "Analytics day to query (YYYY-MM-DD). Empty = current UTC day at Gather."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound for the per-user feed."},
		},
	}
}

// Open reads configuration and, when an API key is present, builds the read-only
// (GET-only) analytics client. It contacts no network.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.apiKey = cfg.Get("api_key")
	if b := strings.TrimRight(cfg.Get("base_url"), "/"); b != "" {
		s.baseURL = b
	}
	if v := cfg.Get("anthropic_version"); v != "" {
		s.version = v
	}
	if o := strings.TrimSpace(cfg.Get("org_ref")); o != "" {
		s.orgRef = o
	}
	s.date = strings.TrimSpace(cfg.Get("date"))
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	if s.apiKey != "" {
		s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthAnthropicKey, s.apiKey,
			map[string]string{"anthropic-version": s.version})
	}
	return nil
}

// Gather emits the coverage posture finding, then polls the summaries and per-user
// endpoints for the target day and emits ONE engagement-summary finding, then the
// per-skill and per-connector Cowork breakdowns (one finding per family, in that
// order: coverage → engagement → skills → connectors). With no key it is a no-op
// (offline → no engagement, an honest absence). A transport error stops the run and
// is returned (the engine retries).
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.apiKey == "" || s.client == nil {
		return nil // offline
	}
	at := s.clock().UTC()
	if err := sink.Emit(ctx, s.coverageFinding(at)); err != nil {
		return err
	}
	day := s.date
	if day == "" {
		day = at.Format(dateLayout)
	}

	sum, refreshedAt, err := s.fetchSummary(ctx, day)
	if err != nil {
		return err
	}
	agg, usersRefreshedAt, err := s.fetchUserAggregate(ctx, day)
	if err != nil {
		return err
	}
	if refreshedAt == "" {
		refreshedAt = usersRefreshedAt
	}
	if err := sink.Emit(ctx, engagementFinding(s.orgRef, day, sum, agg, refreshedAt, at)); err != nil {
		return err
	}

	skills, skillsRefreshedAt, err := s.fetchSkillsBreakdown(ctx, day)
	if err != nil {
		return err
	}
	if err := sink.Emit(ctx, breakdownFinding(s.orgRef, day, "skills", skills, skillsRefreshedAt, at)); err != nil {
		return err
	}
	conns, connsRefreshedAt, err := s.fetchConnectorsBreakdown(ctx, day)
	if err != nil {
		return err
	}
	return sink.Emit(ctx, breakdownFinding(s.orgRef, day, "connectors", conns, connsRefreshedAt, at))
}

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(context.Context) error { return nil }

// fetchSummary pulls the activity summary for the day and returns the (single) row
// plus the data_refreshed_at watermark. The summaries feed is point-in-time per
// date, so the first row for the day is authoritative; an empty feed yields a zero
// row (an honest "no data yet", never a fabricated count).
func (s *Source) fetchSummary(ctx context.Context, day string) (summaryRow, string, error) {
	var resp summariesResponse
	// The summaries endpoint is a DATE-RANGE query — starting_date (required) and
	// ending_date (optional, EXCLUSIVE), with no pagination — so request exactly the one
	// day [day, day+1). This is distinct from the per-user feed, which is keyed by a
	// single `date` (verified against the Enterprise Analytics API reference).
	q := url.Values{"starting_date": {day}, "ending_date": {nextDay(day)}}
	if err := s.client.GetJSON(ctx, summariesPath, q, &resp); err != nil {
		return summaryRow{}, "", err
	}
	// Prefer the row for the requested day; fall back to the first row only if the API
	// widened the range or omitted the date (never fabricate a row). REF (AsOf
	// 2026-06-10) keys the row by starting_date (+ exclusive ending_date), so match
	// that first; Date is kept as a tolerance fallback for an older/shifted emitter.
	for _, r := range resp.Data {
		if r.StartingDate == day || r.Date == day {
			return r, resp.DataRefreshedAt, nil
		}
	}
	if len(resp.Data) == 0 {
		return summaryRow{}, resp.DataRefreshedAt, nil
	}
	return resp.Data[0], resp.DataRefreshedAt, nil
}

// nextDay returns day+1 (YYYY-MM-DD), the EXCLUSIVE ending_date for a single-day
// summaries query. An unparseable day is returned unchanged (the API then applies its
// own range; the connector never fabricates a date).
func nextDay(day string) string {
	t, err := time.Parse(dateLayout, day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, 1).Format(dateLayout)
}

// fetchUserAggregate paginates the per-user feed for the day and folds the
// cowork_metrics into a single aggregate (bounded by max_pages). It returns the
// aggregate and the data_refreshed_at watermark.
func (s *Source) fetchUserAggregate(ctx context.Context, day string) (userAggregate, string, error) {
	var agg userAggregate
	refreshedAt := ""
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return agg, refreshedAt, err
		}
		var resp usersResponse
		q := url.Values{"date": {day}, "limit": {defaultPageLimit}}
		if page != "" {
			q.Set("page", page)
		}
		if err := s.client.GetJSON(ctx, usersPath, q, &resp); err != nil {
			return agg, refreshedAt, err
		}
		if resp.DataRefreshedAt != "" {
			refreshedAt = resp.DataRefreshedAt
		}
		agg.add(resp.Data)
		if !resp.HasMore || resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}
	return agg, refreshedAt, nil
}

// fetchSkillsBreakdown paginates the per-skill feed for the day and folds the
// Cowork slice (distinct_session_skill_used_count) into a breakdownAggregate. REF
// (AsOf 2026-06-10) documents cursor pagination via next_page (null when exhausted)
// for this endpoint but not has_more, so the loop follows next_page alone (bounded
// by max_pages). Rows whose cowork_metrics are all-zero (skill used only on other
// Claude surfaces) are not counted.
func (s *Source) fetchSkillsBreakdown(ctx context.Context, day string) (breakdownAggregate, string, error) {
	var agg breakdownAggregate
	refreshedAt := ""
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return agg, refreshedAt, err
		}
		var resp skillsResponse
		q := url.Values{"date": {day}, "limit": {defaultPageLimit}}
		if page != "" {
			q.Set("page", page)
		}
		if err := s.client.GetJSON(ctx, skillsPath, q, &resp); err != nil {
			return agg, refreshedAt, err
		}
		if resp.DataRefreshedAt != "" {
			refreshedAt = resp.DataRefreshedAt
		}
		for _, r := range resp.Data {
			agg.add(r.SkillName, r.CoworkMetrics.DistinctSessionSkillUsedCount)
		}
		if resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}
	return agg, refreshedAt, nil
}

// fetchConnectorsBreakdown is the per-connector analog of fetchSkillsBreakdown
// (distinct_session_connector_used_count; same envelope/pagination caveat).
func (s *Source) fetchConnectorsBreakdown(ctx context.Context, day string) (breakdownAggregate, string, error) {
	var agg breakdownAggregate
	refreshedAt := ""
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return agg, refreshedAt, err
		}
		var resp connectorsResponse
		q := url.Values{"date": {day}, "limit": {defaultPageLimit}}
		if page != "" {
			q.Set("page", page)
		}
		if err := s.client.GetJSON(ctx, connectorsPath, q, &resp); err != nil {
			return agg, refreshedAt, err
		}
		if resp.DataRefreshedAt != "" {
			refreshedAt = resp.DataRefreshedAt
		}
		for _, r := range resp.Data {
			agg.add(r.ConnectorName, r.CoworkMetrics.DistinctSessionConnectorUsedCount)
		}
		if resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}
	return agg, refreshedAt, nil
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// userAggregate folds per-user Cowork engagement into org-wide totals — all eight
// verified cowork_metrics fields (REF, AsOf 2026-06-10). It is the dashboard signal
// the sealed observation contract has no numeric type for, so it rides the
// engagement finding's non-sensitive aggregate counts.
type userAggregate struct {
	ActiveUsers          int64
	Sessions             int64
	Messages             int64
	Actions              int64
	DispatchTurns        int64
	SkillInvocations     int64
	DistinctSkills       int64
	ConnectorInvocations int64
	DistinctConnectors   int64
}

// add folds a page of user rows into the aggregate, counting a user as active only
// if the row shows any Cowork activity.
func (a *userAggregate) add(rows []userRow) {
	for _, r := range rows {
		m := r.CoworkMetrics
		if m.active() {
			a.ActiveUsers++
		}
		a.Sessions += m.DistinctSessionCount
		a.Messages += m.MessageCount
		a.Actions += m.ActionCount
		a.DispatchTurns += m.DispatchTurnCount
		a.SkillInvocations += m.SkillsUsedCount
		a.DistinctSkills += m.DistinctSkillsUsedCount
		a.ConnectorInvocations += m.ConnectorsUsedCount
		a.DistinctConnectors += m.DistinctConnectorsUsedCount
	}
}

// engagementFinding builds the per-Gather Cowork engagement headline. The
// non-sensitive aggregate counts (Cowork DAU/WAU/MAU + assigned seats + total
// an internal design note (not shipped) invocations) ride the Title so the
// ledger and dashboards surface them; the summed distinct_skills/
// distinct_connectors variants are bound only by the detail hash (they are sums of
// per-user distinct counts — useful for tamper-evidence, too easily misread as an
// org-wide distinct count to headline). The detail hash binds the full count set.
// Info severity — this is engagement evidence, not an alert.
func engagementFinding(orgRef, day string, sum summaryRow, agg userAggregate, refreshedAt string, at time.Time) model.FindingReport {
	title := fmt.Sprintf(
		"Cowork engagement %s: DAU=%d WAU=%d MAU=%d seats=%d; active=%d sessions=%d messages=%d actions=%d dispatch=%d skill_inv=%d connector_inv=%d",
		day,
		sum.CoworkDailyActiveUserCount, sum.CoworkWeeklyActiveUserCount, sum.CoworkMonthlyActiveUserCount,
		sum.AssignedSeatCount,
		agg.ActiveUsers, agg.Sessions, agg.Messages, agg.Actions, agg.DispatchTurns, agg.SkillInvocations, agg.ConnectorInvocations,
	)
	detail := strings.Join([]string{
		orgRef, day, refreshedAt,
		itoa(sum.CoworkDailyActiveUserCount), itoa(sum.CoworkWeeklyActiveUserCount), itoa(sum.CoworkMonthlyActiveUserCount),
		itoa(sum.AssignedSeatCount), itoa(agg.ActiveUsers), itoa(agg.Sessions), itoa(agg.Messages), itoa(agg.Actions),
		itoa(agg.DispatchTurns), itoa(agg.SkillInvocations), itoa(agg.DistinctSkills),
		itoa(agg.ConnectorInvocations), itoa(agg.DistinctConnectors),
	}, "|")
	return model.FindingReport{
		Kind:        findingKindEngagement,
		Severity:    model.SeverityInfo,
		SubjectKind: "cowork_analytics",
		SubjectRef:  orgRef,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

// nameCount is one skill/connector name with its distinct-Cowork-session use count.
type nameCount struct {
	Name  string
	Count int64
}

// breakdownAggregate folds a per-skill or per-connector feed into the Cowork slice:
// how many rows showed Cowork use, the total distinct-session use count, and the
// full name:count list (kept whole so the finding's DetailHash binds it; the Title
// shows only the top names). Skill/connector names are normalized, org-level
// product identifiers (REF: connector_name is "The normalized name of the
// connector") — NOT PII — so carrying them in a finding Title is within the
// minimal-data rules (docs/SECURITY-HARDENING.md).
type breakdownAggregate struct {
	RowsWithCoworkUse   int64
	TotalCoworkSessions int64
	entries             []nameCount // only rows with Cowork use; sorted lazily
}

// add folds one row. A row whose distinct-Cowork-session count is zero (the
// skill/connector was used only on other Claude surfaces) is NOT counted — the
// breakdown reports Cowork use, never another product's.
func (a *breakdownAggregate) add(name string, coworkSessions int64) {
	if coworkSessions <= 0 {
		return
	}
	a.RowsWithCoworkUse++
	a.TotalCoworkSessions += coworkSessions
	a.entries = append(a.entries, nameCount{Name: name, Count: coworkSessions})
}

// sorted returns the Cowork-used entries ordered by count descending, ties broken
// by name ascending — deterministic regardless of API page/row order.
func (a *breakdownAggregate) sorted() []nameCount {
	out := append([]nameCount(nil), a.entries...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// breakdownFinding builds the per-family (skills/connectors) Cowork breakdown
// headline: row count + total distinct-session uses + the top names ride the Title;
// the DetailHash binds orgRef|day|refreshedAt|the FULL ordered name:count list for
// tamper-evidence. Info severity — engagement evidence, not an alert.
func breakdownFinding(orgRef, day, family string, agg breakdownAggregate, refreshedAt string, at time.Time) model.FindingReport {
	ordered := agg.sorted()
	title := fmt.Sprintf("Cowork %s %s: %d %s used in Cowork sessions (distinct-session uses=%d)",
		family, day, agg.RowsWithCoworkUse, family, agg.TotalCoworkSessions)
	if len(ordered) > 0 {
		top := ordered
		if len(top) > topBreakdownNames {
			top = top[:topBreakdownNames]
		}
		names := make([]string, 0, len(top))
		for _, e := range top {
			names = append(names, e.Name)
		}
		title += "; top: " + strings.Join(names, ", ")
	}
	parts := make([]string, 0, 3+len(ordered))
	parts = append(parts, orgRef, day, refreshedAt)
	for _, e := range ordered {
		parts = append(parts, e.Name+":"+itoa(e.Count))
	}
	return model.FindingReport{
		Kind:        findingKindEngagement,
		Severity:    model.SeverityInfo,
		SubjectKind: "cowork_analytics",
		SubjectRef:  orgRef,
		Title:       title,
		DetailHash:  redact.Hash(strings.Join(parts, "|")),
		OccurredAt:  at,
	}
}

// itoa is strconv.FormatInt base 10 (a local shorthand for the detail join).
func itoa(n int64) string { return strconv.FormatInt(n, 10) }
