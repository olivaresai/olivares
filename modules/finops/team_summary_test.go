// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// tsScopedData is a minimal api.ScopedData implementation for handler tests.
type tsScopedData struct {
	st     store.Store
	tenant model.TenantID
}

func (d tsScopedData) View(ctx context.Context, fn func(store.Scope) error) error {
	return d.st.View(ctx, d.tenant, fn)
}

// Export mirrors View: these doubles model a tenant that IS in service, and the
// portability door reaches the same data. Written out rather than panicking so a
// route that legitimately exports keeps working under the double.
func (d tsScopedData) Export(ctx context.Context, fn func(store.ExportScope) error) error {
	return d.st.Export(ctx, d.tenant, fn)
}

func (d tsScopedData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.st.Mutate(ctx, d.tenant, fn)
}

// mkTeamCost builds a CostSample tagged with team and project labels.
func mkTeamCost(provider, modelRef, session, team, project string, in, out, cost int64, at time.Time) sdkmodel.CostSample {
	c := mkCost(provider, modelRef, session, in, out, cost, at)
	labels := map[string]string{}
	if team != "" {
		labels["team"] = team
	}
	if project != "" {
		labels["project"] = project
	}
	if len(labels) > 0 {
		c.Labels = labels
	}
	return c
}

func TestTeamSummaryAggregatesByTeam(t *testing.T) {
	m, st, tenant, _ := newFin(t)

	day0 := baseTime.Truncate(24 * time.Hour)
	day1 := day0.Add(24 * time.Hour)

	// Platform team: 2 samples, different projects and models.
	m.ingest(t, tenant, mkTeamCost("anthropic", "claude-opus-4-8", "sess-1", "platform", "atlas", 100, 50, 300, day0))
	m.ingest(t, tenant, mkTeamCost("anthropic", "claude-haiku-4", "sess-2", "platform", "hermes", 20, 10, 50, day1))
	// Payments team: 1 sample.
	m.ingest(t, tenant, mkTeamCost("anthropic", "claude-opus-4-8", "sess-3", "payments", "ledger", 200, 80, 500, day0))
	// Untagged sample (no team label).
	m.ingest(t, tenant, mkCost("anthropic", "claude-haiku-4", "sess-4", 10, 5, 20, day0))

	since := day0.Add(-time.Hour)
	until := day1.Add(time.Hour)

	var out teamSummaryResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		out, e = teamSummary(context.Background(), sc, since, until)
		return e
	}); err != nil {
		t.Fatal(err)
	}

	// Expect 3 team entries: platform, payments, (untagged).
	if len(out.Teams) != 3 {
		t.Fatalf("teams count = %d, want 3; teams: %v", len(out.Teams), teamNames(out.Teams))
	}

	// Teams are sorted by cost descending: payments(500) > platform(350) > untagged(20).
	if out.Teams[0].Team != "payments" {
		t.Errorf("top team = %q, want payments", out.Teams[0].Team)
	}
	if out.Teams[0].CostMicroUSD != 500 {
		t.Errorf("payments cost = %d, want 500", out.Teams[0].CostMicroUSD)
	}
	if out.Teams[1].Team != "platform" {
		t.Errorf("second team = %q, want platform", out.Teams[1].Team)
	}
	if out.Teams[1].CostMicroUSD != 350 {
		t.Errorf("platform cost = %d, want 350 (300+50)", out.Teams[1].CostMicroUSD)
	}
	if out.Teams[2].Team != "(untagged)" {
		t.Errorf("third team = %q, want (untagged)", out.Teams[2].Team)
	}

	// Platform team: verify sessions, tokens, project and model breakdowns.
	platform := out.Teams[1]
	if platform.Sessions != 2 {
		t.Errorf("platform sessions = %d, want 2", platform.Sessions)
	}
	if platform.InputTokens != 120 || platform.OutputTokens != 60 {
		t.Errorf("platform tokens: in=%d out=%d, want in=120 out=60", platform.InputTokens, platform.OutputTokens)
	}
	if len(platform.Projects) != 2 {
		t.Errorf("platform projects = %d, want 2", len(platform.Projects))
	}
	if len(platform.Models) != 2 {
		t.Errorf("platform models = %d, want 2", len(platform.Models))
	}

	// Period string format: YYYY-MM-DD/YYYY-MM-DD.
	if !strings.Contains(out.Period, "/") {
		t.Errorf("period = %q, want YYYY-MM-DD/YYYY-MM-DD format", out.Period)
	}
}

func TestTeamSummaryTrendZeroFilled(t *testing.T) {
	m, st, tenant, _ := newFin(t)

	day0 := baseTime.Truncate(24 * time.Hour)
	day2 := day0.Add(48 * time.Hour)

	// One sample on day0, none on day1, one on day2.
	m.ingest(t, tenant, mkTeamCost("anthropic", "claude-opus-4-8", "", "eng", "", 100, 50, 100, day0))
	m.ingest(t, tenant, mkTeamCost("anthropic", "claude-opus-4-8", "", "eng", "", 100, 50, 200, day2))

	since := day0
	until := day2

	var out teamSummaryResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		out, e = teamSummary(context.Background(), sc, since, until)
		return e
	}); err != nil {
		t.Fatal(err)
	}

	if len(out.Teams) != 1 {
		t.Fatalf("teams = %d, want 1", len(out.Teams))
	}
	trend := out.Teams[0].Trend
	// 3 days: day0, day1, day2 → [100, 0, 200].
	if len(trend) != 3 {
		t.Fatalf("trend len = %d, want 3", len(trend))
	}
	if trend[0] != 100 || trend[1] != 0 || trend[2] != 200 {
		t.Errorf("trend = %v, want [100 0 200]", trend)
	}
}

func TestTeamSummaryHandlerInvalidPeriod(t *testing.T) {
	m := New()
	req := httptest.NewRequest(http.MethodGet, "/analytics/team-summary?period=1y", nil)
	rec := httptest.NewRecorder()
	// mc.Data is nil — the handler must reject before any data access.
	m.handleTeamSummary(rec, req, api.ModuleContext{})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body parse: %v", err)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["message"] == "" {
		t.Errorf("expected non-empty error message, got: %v", body)
	}
}

func TestTeamSummaryHandlerDefaultPeriod30d(t *testing.T) {
	m, st, tenant, _ := newFin(t)

	req := httptest.NewRequest(http.MethodGet, "/analytics/team-summary", nil)
	rec := httptest.NewRecorder()
	mc := api.ModuleContext{Data: tsScopedData{st: st, tenant: tenant}}
	m.handleTeamSummary(rec, req, mc)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var out teamSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Period string must span 30 days: two dates separated by "/".
	parts := strings.SplitN(out.Period, "/", 2)
	if len(parts) != 2 {
		t.Fatalf("period = %q, expected YYYY-MM-DD/YYYY-MM-DD", out.Period)
	}
	since, err1 := time.Parse(time.DateOnly, parts[0])
	until, err2 := time.Parse(time.DateOnly, parts[1])
	if err1 != nil || err2 != nil {
		t.Fatalf("period dates unparseable: %v / %v", err1, err2)
	}
	diff := until.Sub(since)
	const wantDays = 30
	if got := int(diff.Hours() / 24); got != wantDays {
		t.Errorf("period spans %d days, want %d", got, wantDays)
	}
}

// teamNames extracts just the team name strings for error messages.
func teamNames(teams []teamSummaryDTO) []string {
	names := make([]string, len(teams))
	for i, t := range teams {
		names[i] = t.Team
	}
	return names
}
