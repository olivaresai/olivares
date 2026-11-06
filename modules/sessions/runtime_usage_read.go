// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// GET /usage totals what the runs the caller may read used: one row per model, account,
// provider or tool, optionally split by the day the run was created. It reads the same run
// rows GET /runs lists, through the same scope, so a person totals only the runs they may
// read. The sums are of what each run's tool reported (runtime_usage.go).
//
// ⛔ UNKNOWN IS NOT ZERO. A row whose runs reported no tokens, or no cost, carries null
// there, and runs_without_tokens / runs_without_cost count the runs that reported none, so a
// small cost from a group where half the runs never reported one says so. A run that
// reported a free turn (0) is a value and is summed.
const (
	usageByModel    = "model"
	usageByAccount  = "account"
	usageByProvider = "provider"
	usageByTool     = "tool"
	usageBucketDay  = "day"

	// runUsageScanPage is the store's largest page.
	runUsageScanPage = 1000
	// runUsageMaxRuns is the most runs one read totals. Past it the read is refused: a
	// total over "the first N runs" would look complete and not be.
	runUsageMaxRuns = 100_000
)

// runUsageRow is one total. Key is absent when the runs recorded no value for the
// dimension; Day is present only with bucket=day.
type runUsageRow struct {
	Key               string `json:"key,omitempty"`
	Day               string `json:"day,omitempty"`
	Runs              int64  `json:"runs"`
	InputTokens       *int64 `json:"input_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	CostMicroUSD      *int64 `json:"cost_micro_usd"`
	RunsWithoutTokens int64  `json:"runs_without_tokens"`
	RunsWithoutCost   int64  `json:"runs_without_cost"`
}

type runUsageResponse struct {
	GroupBy string                     `json:"group_by"`
	Bucket  string                     `json:"bucket,omitempty"`
	Since   string                     `json:"since,omitempty"`
	Until   string                     `json:"until,omitempty"`
	Items   api.JSONArray[runUsageRow] `json:"items"`
}

// runUsageQuery is a validated request.
type runUsageQuery struct {
	by, bucket   string
	since, until string
	filters      []model.Filter
}

func parseRunUsageQuery(v url.Values) (runUsageQuery, error) {
	q := runUsageQuery{by: v.Get("group_by"), bucket: v.Get("bucket")}
	switch q.by {
	case usageByModel, usageByAccount, usageByProvider, usageByTool:
	default:
		return q, badRequest("group_by must be model, account, provider or tool")
	}
	if q.bucket != "" && q.bucket != usageBucketDay {
		return q, badRequest("bucket must be day")
	}
	var from, to time.Time
	for _, bound := range []struct {
		name string
		text *string
		at   *time.Time
		op   model.Op
	}{{"since", &q.since, &from, model.OpGte}, {"until", &q.until, &to, model.OpLt}} {
		if !v.Has(bound.name) {
			continue
		}
		at, err := time.Parse(time.RFC3339, v.Get(bound.name))
		if err != nil {
			return q, badRequest(bound.name + " must be an RFC 3339 time")
		}
		*bound.text, *bound.at = at.UTC().Format(time.RFC3339), at
		q.filters = append(q.filters, model.Filter{Column: model.ColCreatedAt, Op: bound.op, Value: model.NewTimestamp(at).String()})
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return q, badRequest("since must be before until")
	}
	return q, nil
}

// runUsageTool is the run's tool: its profile's driver, and Claude Code for a run that
// predates profiles (the rule claudeTextTurn and the CLI apply).
func runUsageTool(rec model.Record) string {
	if driver := rec.String(colRunProfileDriver); driver != "" {
		return driver
	}
	return providerDriverClaude
}

// runUsageKeyOf is the dimension's value for one run, "" when the run recorded none.
// The model is the one the provider reported using, else the one the launch asked for.
// The provider is the kind of the record the run's credential came from, else the vendor
// of the tool's own login; kinds maps record refs to kinds.
func runUsageKeyOf(by string, kinds map[string]string) func(model.Record) string {
	return func(rec model.Record) string {
		switch by {
		case usageByModel:
			if ref := rec.String(colRunUsageModelRef); ref != "" {
				return ref
			}
			return rec.String(colRunModelRef)
		case usageByAccount:
			return rec.String(colRunProfileID)
		case usageByTool:
			return runUsageTool(rec)
		default:
			if ref := rec.String(colRunProviderRecordRef); ref != "" {
				return kinds[ref]
			}
			facts, _ := driverfacts.Lookup(runUsageTool(rec))
			return facts.OwnKind
		}
	}
}

// runUsageFold sums runs into rows.
type runUsageFold struct {
	key  func(model.Record) string
	day  bool
	runs int
	rows map[[2]string]*runUsageRow
}

// addReported adds the column to sum when the run reported it, and says whether it did.
func addReported(sum **int64, rec model.Record, col string) bool {
	if rec.IsNull(col) {
		return false
	}
	if *sum == nil {
		*sum = new(int64)
	}
	**sum += rec.Int(col)
	return true
}

func (f *runUsageFold) add(rec model.Record) {
	k := [2]string{"", f.key(rec)}
	if created := rec.String(model.ColCreatedAt); f.day && len(created) >= len("2006-01-02") {
		k[0] = created[:len("2006-01-02")]
	}
	row := f.rows[k]
	if row == nil {
		row = &runUsageRow{Day: k[0], Key: k[1]}
		f.rows[k] = row
	}
	f.runs++
	row.Runs++
	in := addReported(&row.InputTokens, rec, colRunInputTokens)
	out := addReported(&row.OutputTokens, rec, colRunOutputTokens)
	if !in && !out {
		row.RunsWithoutTokens++
	}
	if !addReported(&row.CostMicroUSD, rec, colRunCostMicroUSD) {
		row.RunsWithoutCost++
	}
}

// result is the rows by day, then most runs first, then key: a stable order.
func (f *runUsageFold) result() []runUsageRow {
	out := make([]runUsageRow, 0, len(f.rows))
	for _, row := range f.rows {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Day != b.Day {
			return a.Day < b.Day
		}
		if a.Runs != b.Runs {
			return a.Runs > b.Runs
		}
		return a.Key < b.Key
	})
	return out
}

// providerKinds maps every provider record ref of the tenant to its kind.
func (m *Module) providerKinds(ctx context.Context, tenant model.TenantID) (map[string]string, error) {
	kinds := map[string]string{}
	q := model.Query{Limit: runUsageScanPage}
	for {
		records, page, err := m.ListProviderRecords(ctx, tenant, "", "", q)
		if err != nil {
			return nil, err
		}
		for _, rec := range records {
			kinds[rec.Ref] = rec.Kind
		}
		if !page.HasMore {
			return kinds, nil
		}
		if page.Cursor == "" {
			return nil, errors.New("sessions: provider record page has more rows and no cursor")
		}
		q.Cursor = page.Cursor
	}
}

// handleRunUsage totals the runs the caller may read, one row per model, account, provider or tool, over an optional time range. A total is null when no run in the row reported it, and the row counts the runs that reported no tokens and no cost.
func (m *Module) handleRunUsage(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q, err := parseRunUsageQuery(r.URL.Query())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	var kinds map[string]string
	if q.by == usageByProvider {
		if kinds, err = m.providerKinds(r.Context(), mc.Tenant); err != nil {
			writeRunErr(w, err)
			return
		}
	}
	fold := &runUsageFold{key: runUsageKeyOf(q.by, kinds), day: q.bucket == usageBucketDay, rows: map[[2]string]*runUsageRow{}}
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		scan := model.Query{Filters: q.filters, Limit: runUsageScanPage}
		for {
			recs, page, err := repo.List(r.Context(), scan)
			if err != nil {
				return err
			}
			for _, rec := range recs {
				fold.add(rec)
			}
			if fold.runs > runUsageMaxRuns {
				return badRequest(fmt.Sprintf("more than %d runs in that time: narrow it with since and until", runUsageMaxRuns))
			}
			if !page.HasMore {
				return nil
			}
			if page.Cursor == "" {
				// An id-ordered page always carries one. Stopping here would total rows never read.
				return errors.New("sessions: run usage page has more rows and no cursor")
			}
			scan.Cursor = page.Cursor
		}
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runUsageResponse{GroupBy: q.by, Bucket: q.bucket, Since: q.since, Until: q.until, Items: fold.result()})
}
