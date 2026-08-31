// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package claudeadoption

import (
	"context"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// agg accumulates one bucket's productivity measures while scanning the read-model.
type agg struct {
	sessions, linesAdded, linesRemoved, commits, prs, activeMs int64
	accepted, rejected, inputTokens, outputTokens, tokens      int64
	byModel                                                    map[string]int64
	byTool                                                     map[string]*toolTally
	developers                                                 map[string]struct{}
	teams                                                      map[string]struct{}
}

type toolTally struct{ accepted, rejected int64 }

func newAgg() *agg {
	return &agg{
		byModel: map[string]int64{}, byTool: map[string]*toolTally{},
		developers: map[string]struct{}{}, teams: map[string]struct{}{},
	}
}

// fold adds one read-model row to the bucket, switching on the metric name + dimensions.
func (a *agg) fold(r model.Record) {
	v := r.Int(colValue)
	switch r.String(colMetricName) {
	case metricSessionCount:
		a.sessions += v
	case metricLinesOfCode:
		if r.String(colDimType) == typeRemoved {
			a.linesRemoved += v
		} else {
			a.linesAdded += v
		}
	case metricCommit:
		a.commits += v
	case metricPullRequest:
		a.prs += v
	case metricActiveTime:
		a.activeMs += v
	case metricCodeEditDecision:
		tool := r.String(colDimTool)
		tt := a.byTool[tool]
		if tt == nil {
			tt = &toolTally{}
			a.byTool[tool] = tt
		}
		if r.String(colDimDecision) == decReject {
			a.rejected += v
			tt.rejected += v
		} else {
			a.accepted += v
			tt.accepted += v
		}
	case metricTokenUsage:
		a.tokens += v
		// cacheRead / cacheCreation are INPUT-side tiers — a subset of input, per the
		// CostSample convention (observation.go: InputTokens folds all input tiers). Fold
		// them into inputTokens so input + output == total; an unknown type contributes to
		// the total only (honest, never miscategorized).
		switch r.String(colDimType) {
		case "input", "cacheRead", "cacheCreation":
			a.inputTokens += v
		case "output":
			a.outputTokens += v
		}
		if mdl := r.String(colDimModel); mdl != "" {
			a.byModel[mdl] += v
		}
	}
	if r.String(colSubjectKind) == subjectDeveloper {
		a.developers[r.String(colSubjectRef)] = struct{}{}
	}
	if tm := r.String(colTeam); tm != "" {
		a.teams[tm] = struct{}{}
	}
}

// totals renders the bucket as the wire DTO, computing the net LoC and acceptance rate.
func (a *agg) totals() adoptionTotals {
	return adoptionTotals{
		Sessions: a.sessions, LinesAdded: a.linesAdded, LinesRemoved: a.linesRemoved,
		LinesNet: a.linesAdded - a.linesRemoved, Commits: a.commits, PullRequests: a.prs,
		ActiveTimeMs: a.activeMs, ToolsAccepted: a.accepted, ToolsRejected: a.rejected,
		AcceptanceRate: rate(a.accepted, a.rejected),
		InputTokens:    a.inputTokens, OutputTokens: a.outputTokens, Tokens: a.tokens,
	}
}

// modelMix renders the per-model token split, descending by tokens, capped at top-N.
func (a *agg) modelMix(topN int) []modelMixDTO {
	out := make([]modelMixDTO, 0, len(a.byModel))
	for mdl, tok := range a.byModel {
		out = append(out, modelMixDTO{Model: mdl, Tokens: tok})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens != out[j].Tokens {
			return out[i].Tokens > out[j].Tokens
		}
		return out[i].Model < out[j].Model
	})
	if topN > 0 && len(out) > topN {
		out = out[:topN]
	}
	return out
}

// toolBreakdown renders the per-tool accept/reject tally with its acceptance rate, in a
// stable order (descending by total actions, then tool name).
func (a *agg) toolBreakdown() []toolBreakdownDTO {
	out := make([]toolBreakdownDTO, 0, len(a.byTool))
	for tool, tt := range a.byTool {
		out = append(out, toolBreakdownDTO{
			Tool: tool, Accepted: tt.accepted, Rejected: tt.rejected,
			AcceptanceRate: rate(tt.accepted, tt.rejected),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].Accepted+out[i].Rejected, out[j].Accepted+out[j].Rejected
		if ti != tj {
			return ti > tj
		}
		return out[i].Tool < out[j].Tool
	})
	return out
}

func (a *agg) lens(topN int) lensDTO {
	return lensDTO{Totals: a.totals(), ByModel: a.modelMix(topN), ByTool: a.toolBreakdown()}
}

// rate returns accepted/(accepted+rejected) as a 0..1 fraction, or nil when there were no
// decisions (honest: an absent denominator is null, never a fabricated 0% or 100%).
func rate(accepted, rejected int64) *float64 {
	total := accepted + rejected
	if total <= 0 {
		return nil
	}
	r := float64(accepted) / float64(total)
	return &r
}

// --- scanning ----------------------------------------------------------------

// windowFilters builds the time-window (+ extra) filters over the day column. The window
// is matched on the calendar-day bucket, so a since/until is normalized to its UTC day:
// the bucket grain is the day, and a half-open intra-day window would silently exclude a
// day's rows whose stamp is midnight.
func windowFilters(extra []model.Filter, since time.Time, hasSince bool, until time.Time, hasUntil bool) []model.Filter {
	f := append([]model.Filter(nil), extra...)
	if hasSince {
		f = append(f, model.Filter{Column: colDay, Op: model.OpGte, Value: since.UTC().Format(dayLayout)})
	}
	if hasUntil {
		f = append(f, model.Filter{Column: colDay, Op: model.OpLte, Value: until.UTC().Format(dayLayout)})
	}
	return f
}

// scanAdoption pages the read-model under filters, calling fn for each row. It returns
// truncated=true if the window exceeds the scan cap (rather than scanning unboundedly).
func scanAdoption(ctx context.Context, sc store.Scope, filters []model.Filter, fn func(model.Record)) (bool, error) {
	repo, err := sc.Ext(adoptionMetricKind)
	if err != nil {
		return false, err
	}
	q := model.Query{Filters: filters, Limit: listCap}
	for pages := 0; ; pages++ {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return false, err
		}
		for _, r := range recs {
			fn(r)
		}
		if !page.HasMore {
			return false, nil
		}
		if pages+1 >= maxScanPages {
			return true, nil
		}
		q.Cursor = page.Cursor
	}
}

// aggregateBy scans the window and folds rows into per-key buckets (key = ""/day/team/
// developer per the caller). It returns the buckets and whether the scan was truncated.
func aggregateBy(ctx context.Context, sc store.Scope, filters []model.Filter, key func(model.Record) string) (map[string]*agg, bool, error) {
	buckets := map[string]*agg{}
	trunc, err := scanAdoption(ctx, sc, filters, func(r model.Record) {
		k := key(r)
		b := buckets[k]
		if b == nil {
			b = newAgg()
			buckets[k] = b
		}
		b.fold(r)
	})
	return buckets, trunc, err
}

// subjectFilter restricts a scan to one lens (developer | session).
func subjectFilter(kind string) model.Filter { return eq(colSubjectKind, kind) }
