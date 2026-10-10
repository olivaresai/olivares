// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package paperclip

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	acmeAgents = "/api/companies/" + companyAcme + "/agents"
	acmeRuns   = "/api/companies/" + companyAcme + "/heartbeat-runs"
	acmeCosts  = "/api/companies/" + companyAcme + "/costs/by-agent-model"
)

func gather(s *Source) ([]model.Observation, error) {
	sink := &collectSink{}
	err := s.Gather(context.Background(), sink)
	return sink.obs, err
}

func coverageFindings(obs []model.Observation) []model.FindingReport {
	var out []model.FindingReport
	for _, o := range obs {
		if fr, ok := o.(model.FindingReport); ok && fr.Kind == "coverage" {
			out = append(out, fr)
		}
	}
	return out
}

func runMetrics(obs []model.Observation) map[string]int64 {
	out := map[string]int64{}
	for _, o := range obs {
		if m, ok := o.(model.MetricSample); ok {
			out[fmt.Sprintf("%s|%s|%s", m.SubjectRef, m.OccurredAt.Format("2006-01-02"), m.Dimensions["status"])] = m.Value
		}
	}
	return out
}

func TestSnapshotRejectsAPartialRoster(t *testing.T) {
	f := &fake{t: t, status: map[string]int{"/api/companies/" + companySide + "/agents": http.StatusForbidden}}
	g, err := open(t, f, nil).Snapshot(context.Background())
	if err == nil {
		t.Fatal("a refused agents leg must fail the snapshot, not return a partial roster")
	}
	if len(g.Identities) != 0 || len(g.Collections) != 0 {
		t.Errorf("graph on error = %+v", g)
	}
}

func TestServerFaultsAreHardErrorsNotCoverage(t *testing.T) {
	legs := map[string]string{"agents": acmeAgents, "runs": acmeRuns, "costs": acmeCosts}
	faults := map[string]func(f *fake, path string){
		"500":              func(f *fake, p string) { f.status = map[string]int{p: http.StatusInternalServerError} },
		"401":              func(f *fake, p string) { f.status = map[string]int{p: http.StatusUnauthorized} },
		"truncated json":   func(f *fake, p string) { f.body = map[string]string{p: `[{"id":`} },
		"object for array": func(f *fake, p string) { f.body = map[string]string{p: `{"error":"x"}`} },
	}
	for leg, path := range legs {
		for name, mutate := range faults {
			t.Run(leg+"/"+name, func(t *testing.T) {
				f := &fake{t: t}
				mutate(f, path)
				obs, err := gather(open(t, f, nil))
				if err == nil {
					t.Fatal("the fault must fail the pass")
				}
				if cov := coverageFindings(obs); len(cov) != 0 {
					t.Errorf("a fault is not a refused leg, got coverage %+v", cov)
				}
			})
		}
	}
}

func TestRefusedAgentsAndRunsLegsAreReported(t *testing.T) {
	f := &fake{t: t, status: map[string]int{
		"/api/companies/" + companySide + "/agents":         http.StatusForbidden,
		"/api/companies/" + companySide + "/heartbeat-runs": http.StatusNotFound,
	}}
	obs, err := gather(open(t, f, nil))
	if err != nil {
		t.Fatalf("a refused leg must not fail the pass: %v", err)
	}
	var legs []string
	for _, c := range coverageFindings(obs) {
		legs = append(legs, c.Title)
	}
	joined := strings.Join(legs, "\n")
	if len(legs) != 2 || !strings.Contains(joined, "agents not readable") || !strings.Contains(joined, "runs not readable") || !strings.Contains(joined, "403") || !strings.Contains(joined, "404") {
		t.Errorf("coverage = %q", legs)
	}
	var inventory string
	for _, o := range obs {
		if fr, ok := o.(model.FindingReport); ok && fr.Kind == "inventory" {
			inventory = fr.Title
		}
	}
	if !strings.Contains(inventory, "companies-with-unreadable-agents=1") {
		t.Errorf("inventory must not present an unreadable agents leg as zero agents: %q", inventory)
	}
}

func TestOpenRejectsBadConfig(t *testing.T) {
	cases := map[string]map[string]string{
		"lookback 0":  {"lookback_days": "0"},
		"lookback 32": {"lookback_days": "32"},
		"ftp scheme":  {"base_url": "ftp://paperclip.example"},
		"no host":     {"base_url": "http://"},
		"userinfo":    {"base_url": "http://user:hunter2@paperclip.example"},
		"query":       {"base_url": "http://paperclip.example/?a=b"},
		"fragment":    {"base_url": "http://paperclip.example/#x"},
	}
	for name, settings := range cases {
		t.Run(name, func(t *testing.T) {
			err := New().Open(context.Background(), sdk.Config{Settings: settings})
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "paperclip.example") {
				t.Errorf("the error repeats the configured URL: %v", err)
			}
		})
	}
}

func TestTrailingSlashBaseURLBuildsCleanPaths(t *testing.T) {
	f := &fake{t: t}
	srv := httptest.NewServer(f)
	defer srv.Close()
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"base_url": srv.URL + "/", "api_key": testKey}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.paths()[0]; got != "GET /api/companies" {
		t.Errorf("first request = %q", got)
	}
}

func TestDayBoundaries(t *testing.T) {
	// Window at 2026-10-06T10:00Z with 3 days: 2026-10-03 00:00Z up to 2026-10-06 00:00Z (exclusive).
	runs := `[
	 {"agentId":"` + agentAda + `","status":"succeeded","startedAt":"2026-10-02T23:59:59.999Z"},
	 {"agentId":"` + agentAda + `","status":"succeeded","startedAt":"2026-10-03T00:00:00.000Z"},
	 {"agentId":"` + agentAda + `","status":"succeeded","startedAt":"2026-10-04T01:30:00+02:00"},
	 {"agentId":"` + agentAda + `","status":"failed","startedAt":"2026-10-05T23:59:59.999Z"},
	 {"agentId":"` + agentAda + `","status":"succeeded","startedAt":"2026-10-06T00:00:00.000Z"},
	 {"agentId":"` + agentAda + `","status":"running","startedAt":"2026-10-04T10:00:00Z"},
	 {"agentId":"` + agentAda + `","status":"queued","startedAt":null}
	]`
	f := &fake{t: t, body: map[string]string{acmeRuns: runs}}
	obs, err := gather(open(t, f, nil))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		"paperclip/agent/" + agentAda + "|2026-10-03|succeeded": 2, // 00:00Z and 23:30Z (+02:00 offset)
		"paperclip/agent/" + agentAda + "|2026-10-05|failed":    1,
	}
	if fmt.Sprint(runMetrics(obs)) != fmt.Sprint(want) {
		t.Errorf("run metrics\n got %v\nwant %v", runMetrics(obs), want)
	}
}

func TestTruncatedListDropsTheOldestPossiblyPartialDay(t *testing.T) {
	runs := `[
	 {"agentId":"` + agentAda + `","status":"succeeded","startedAt":"2026-10-03T05:00:00Z"},
	 {"agentId":"` + agentLinus + `","status":"succeeded","startedAt":"2026-10-04T05:00:00Z"},
	 {"agentId":"` + agentLinus + `","status":"succeeded","startedAt":"2026-10-04T06:00:00Z"}
	]`
	f := &fake{t: t, body: map[string]string{acmeRuns: runs}}
	s := open(t, f, nil)
	s.maxRuns = 3
	obs, err := gather(s)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"paperclip/agent/" + agentLinus + "|2026-10-04|succeeded": 2}
	if fmt.Sprint(runMetrics(obs)) != fmt.Sprint(want) {
		t.Errorf("run metrics\n got %v\nwant %v", runMetrics(obs), want)
	}
	if cov := coverageFindings(obs); len(cov) != 1 || !strings.Contains(cov[0].Title, "truncated") {
		t.Errorf("coverage = %+v", cov)
	}
}

func TestRunShapeErrorsAreLoud(t *testing.T) {
	bodies := map[string]string{
		"finished run without startedAt": `[{"agentId":"` + agentAda + `","status":"succeeded","startedAt":null}]`,
		"run without agent id":           `[{"status":"succeeded","startedAt":"2026-10-04T05:00:00Z"}]`,
		"run with hostile agent id":      `[{"agentId":"../x","status":"succeeded","startedAt":"2026-10-04T05:00:00Z"}]`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			_, err := gather(open(t, &fake{t: t, body: map[string]string{acmeRuns: body}}, nil))
			if !errors.Is(err, errShape) {
				t.Errorf("err = %v, want a shape error", err)
			}
		})
	}
}

func TestGatherIsIdempotent(t *testing.T) {
	s := open(t, &fake{t: t}, nil)
	first, err := gather(s)
	if err != nil {
		t.Fatal(err)
	}
	second, err := gather(s)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%+v", first) != fmt.Sprintf("%+v", second) {
		t.Error("two passes over the same data must emit the same observations in the same order")
	}
}

func TestRosterIsSortedAndDeduplicated(t *testing.T) {
	co := func(id string) string { return `{"id":"` + id + `","name":"n","status":"active"}` }
	ag := func(id string) string {
		return `{"id":"` + id + `","name":"n","role":"general","status":"idle","adapterType":"process"}`
	}
	f := &fake{t: t, body: map[string]string{
		"/api/companies": "[" + co(companySide) + "," + co(companyAcme) + "," + co(companyAcme) + "]",
		acmeAgents:       "[" + ag(agentLinus) + "," + ag(agentAda) + "," + ag(agentAda) + "]",
		"/api/companies/" + companySide + "/agents": "[" + ag(agentAda) + "," + ag(agentSolo) + "]",
	}}
	g, err := open(t, f, nil).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var cols, ids []string
	for _, c := range g.Collections {
		cols = append(cols, c.Ref)
	}
	for _, i := range g.Identities {
		ids = append(ids, i.Ref)
	}
	wantCols := []string{"paperclip/company/" + companyAcme, "paperclip/company/" + companySide}
	// agentAda is listed under both companies: the first (sorted) company keeps it.
	wantIDs := []string{"paperclip/agent/" + agentAda, "paperclip/agent/" + agentLinus, "paperclip/agent/" + agentSolo}
	if fmt.Sprint(cols) != fmt.Sprint(wantCols) || fmt.Sprint(ids) != fmt.Sprint(wantIDs) || len(g.Memberships) != 3 {
		t.Errorf("collections %v identities %v memberships %d", cols, ids, len(g.Memberships))
	}
}

func TestRemoteTextIsSanitizedBoundedAndTokenized(t *testing.T) {
	long := strings.Repeat("x", 300)
	agents := `[
	 {"id":"` + agentAda + `","name":"\u001b[31mred\u001b[0m\nline","role":"` + long + `","title":"` + long + `","status":"IDLE ","adapterType":""},
	 {"id":"` + agentLinus + `","name":"` + long + `","status":"idle; drop table","adapterType":"../../x"}
	]`
	f := &fake{t: t, body: map[string]string{acmeAgents: agents}}
	g, err := open(t, f, nil).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ada, _ := g.FindIdentity("paperclip/agent/" + agentAda)
	linus, _ := g.FindIdentity("paperclip/agent/" + agentLinus)
	if strings.ContainsAny(ada.DisplayName, "\x1b\n") {
		t.Errorf("control characters survived: %q", ada.DisplayName)
	}
	if n := len([]rune(linus.DisplayName)); n > maxTextLen {
		t.Errorf("display name is %d runes, cap is %d", n, maxTextLen)
	}
	if n := len([]rune(ada.Attributes["title"])); n > maxTextLen {
		t.Errorf("title is %d runes, cap is %d", n, maxTextLen)
	}
	if ada.Kind != "paperclip/unknown" || linus.Kind != "paperclip/unknown" {
		t.Errorf("kinds = %q, %q; an empty or off-shape adapterType is unknown", ada.Kind, linus.Kind)
	}
	if ada.Attributes["status"] != "idle" || linus.Attributes["status"] != "unknown" {
		t.Errorf("statuses = %q, %q", ada.Attributes["status"], linus.Attributes["status"])
	}
}

func TestHostileIDsAreRejected(t *testing.T) {
	cases := map[string]map[string]string{
		"dot-dot company id": {"/api/companies": `[{"id":"..","name":"x"}]`},
		"empty company id":   {"/api/companies": `[{"name":"x"}]`},
		"slash company id":   {"/api/companies": `[{"id":"a/b","name":"x"}]`},
		"empty agent id":     {acmeAgents: `[{"name":"x","adapterType":"process"}]`},
		"hostile reportsTo":  {acmeAgents: `[{"id":"` + agentAda + `","name":"x","reportsTo":"../x","adapterType":"process"}]`},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fake{t: t, body: body}
			s := open(t, f, nil)
			if _, err := s.Snapshot(context.Background()); !errors.Is(err, errShape) {
				t.Errorf("Snapshot err = %v, want a shape error", err)
			}
			if _, err := gather(s); !errors.Is(err, errShape) {
				t.Errorf("Gather err = %v, want a shape error", err)
			}
			for _, line := range f.paths() {
				if strings.Contains(line, "..") {
					t.Errorf("a hostile id reached a URL path: %q", line)
				}
			}
		})
	}
}

func TestCostRowsAreMergedValidatedAndNeverSilentlyZero(t *testing.T) {
	row := func(agent, provider, billing, cents, in, out string) string {
		return `{"agentId":"` + agent + `","provider":"` + provider + `","biller":"` + provider + `","billingType":"` + billing + `","model":"m","costCents":` + cents + `,"inputTokens":` + in + `,"outputTokens":` + out + `}`
	}
	t.Run("rows split by billing type share one ledger key", func(t *testing.T) {
		body := "[" + row(agentLinus, "openai", "metered_api", "100", "1000", "100") + "," +
			row(agentLinus, "openai", "subscription_included", "0.5", "10", "1") + "," +
			row(agentAda, "anthropic", "metered_api", "0", "0", "0") + "]"
		obs, err := gather(open(t, &fake{t: t, body: map[string]string{acmeCosts: body}}, nil))
		if err != nil {
			t.Fatal(err)
		}
		var costs []model.CostSample
		for _, o := range obs {
			if c, ok := o.(model.CostSample); ok {
				costs = append(costs, c)
			}
		}
		if len(costs) != 3 { // one merged sample per completed day; the all-zero row is dropped
			t.Fatalf("cost samples = %d", len(costs))
		}
		if c := costs[0]; c.CostMicroUSD != 1_005_000 || c.InputTokens != 1010 || c.OutputTokens != 101 {
			t.Errorf("merged sample = %+v", c)
		}
	})
	bad := map[string]string{
		"missing costCents":  `[{"agentId":"` + agentLinus + `","provider":"openai","model":"m","inputTokens":1,"outputTokens":1}]`,
		"missing tokens":     `[{"agentId":"` + agentLinus + `","provider":"openai","model":"m","costCents":1}]`,
		"renamed costCents":  `[{"agentId":"` + agentLinus + `","provider":"openai","model":"m","cost_cents":5,"inputTokens":1,"outputTokens":1}]`,
		"negative cost":      "[" + row(agentLinus, "openai", "x", "-5", "1", "1") + "]",
		"overflowing cost":   "[" + row(agentLinus, "openai", "x", "1e30", "1", "1") + "]",
		"overflowing tokens": "[" + row(agentLinus, "openai", "x", "1", "1e30", "1") + "]",
		"missing provider":   `[{"agentId":"` + agentLinus + `","model":"m","costCents":1,"inputTokens":1,"outputTokens":1}]`,
		"missing agent":      `[{"provider":"openai","model":"m","costCents":1,"inputTokens":1,"outputTokens":1}]`,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := gather(open(t, &fake{t: t, body: map[string]string{acmeCosts: body}}, nil))
			if !errors.Is(err, errShape) {
				t.Errorf("err = %v, want a shape error", err)
			}
		})
	}
}

func TestRequestsCarryTheDocumentedQuery(t *testing.T) {
	f := &fake{t: t}
	if _, err := gather(open(t, f, nil)); err != nil {
		t.Fatal(err)
	}
	runs, _ := url.ParseQuery(f.queries[acmeRuns])
	if runs.Get("summary") != "true" || runs.Get("limit") != "1000" {
		t.Errorf("heartbeat-runs query = %v", runs)
	}
	costs, _ := url.ParseQuery(f.queries[acmeCosts]) // the last request is the newest completed day
	if costs.Get("from") != "2026-10-05T00:00:00.000Z" || costs.Get("to") != "2026-10-05T23:59:59.999Z" {
		t.Errorf("costs query = %v", costs)
	}
}
