// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// GET /scorecards is the ON-READ quality aggregate for (contract §2.5/§5): per
// (suite, subject, variant) it computes pass_rate, mean_score, run count, last_score,
// the time-ordered trend and whether the latest run regressed. The drift over time is
// the trend slope — computed here, not a stored entity. Read-tier; ?format=csv|json.

type trendPoint struct {
	At       string  `json:"at"`
	Score    float64 `json:"score"`
	PassRate float64 `json:"pass_rate"`
}

type scorecardDTO struct {
	Suite      string `json:"suite_ref"`
	SubjectRef string `json:"subject_ref"`
	// SubjectKind identifies WHAT the subject is (agent|model|prompt|session|
	// sandbox_run — schema.go colSubjKind). A subject_ref alone does not
	// identify a subject: the same free-form ref can name an agent in one
	// suite and a model in another. Taken from the group's most recent run,
	// like LastScore and Regressed, so the card describes one coherent latest
	// state. Never omitempty: a card whose kind is unknown must say so with an
	// empty string, because an ABSENT field is what made the console render
	// the raw i18n key `subjectKind.undefined` (finding 1).
	SubjectKind string  `json:"subject_kind"`
	Variant     string  `json:"prompt_variant,omitempty"`
	Runs        int     `json:"runs"`
	PassRate    float64 `json:"pass_rate"`
	MeanScore   float64 `json:"mean_score"`
	// The 95% intervals. PassRateCI/MeanScoreCI are t-intervals over the
	// RUN-level series — they pair with PassRate/MeanScore, which are means of
	// runs — and are absent below 2 runs (no spread information, no interval).
	// PooledPassRate is the case-weighted view: passes/scored summed over every
	// run, with its Wilson interval and denominator.
	PassRateCI     *ciDTO        `json:"pass_rate_ci,omitempty"`
	MeanScoreCI    *ciDTO        `json:"mean_score_ci,omitempty"`
	PooledPassRate *measuredRate `json:"pooled_pass_rate,omitempty"`
	LastScore      float64       `json:"last_score"`
	Regressed      bool          `json:"regressed"`
	Trend          []trendPoint  `json:"trend"`
}

type scorecardsResponse struct {
	Items api.JSONArray[scorecardDTO] `json:"items"`
}

// handleScorecards aggregates the tenant's runs into scorecards. It supports optional
// suite_ref / subject_ref filters and a format=csv|json (json is the default).
func (m *Module) handleScorecards(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := model.Query{Limit: listCap}
	if v := strings.TrimSpace(r.URL.Query().Get("suite_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colSuiteRef, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("subject_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colSubjectRef, v))
	}

	var recs []model.Record
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		all, lerr := listAll(r.Context(), repo, q.Filters...)
		recs = all
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	cards := aggregateScorecards(recs)
	if strings.EqualFold(r.URL.Query().Get("format"), "csv") {
		writeScorecardsCSV(w, cards)
		return
	}
	writeJSON(w, http.StatusOK, scorecardsResponse{Items: cards})
}

// scorecardKey groups runs by (suite, subject, variant).
type scorecardKey struct{ suite, subject, variant string }

// aggregateScorecards folds run rows into per-(suite,subject,variant) scorecards with
// a time-ordered trend; only scored runs (completed/degraded with a denominator) feed
// the means, but every run feeds the run count + trend.
func aggregateScorecards(recs []model.Record) []scorecardDTO {
	type acc struct {
		runs int
		// scored counts only the runs that actually produced a denominator. The
		// means divide by THIS, not by runs: a run that scored nothing has no
		// score and no pass rate to contribute, and folding its zero in drags
		// both means toward zero while claiming the subject got worse.
		scored              int
		scoreSum, prSum     float64
		scores, passRates   []float64
		pooledPass, pooledN int
		points              []model.Record
	}
	groups := map[scorecardKey]*acc{}
	order := []scorecardKey{}
	for _, rec := range recs {
		k := scorecardKey{suite: rec.String(colSuiteRef), subject: rec.String(colSubjectRef), variant: rec.String(colVariant)}
		a := groups[k]
		if a == nil {
			a = &acc{}
			groups[k] = a
			order = append(order, k)
		}
		a.runs++
		// The doc comment above already said only scored runs feed the means; the
		// code did not check, so an unscored run entered every mean as a zero.
		scoredN := int(rec.Int(colPassed) + rec.Int(colFailed))
		if scoredN > 0 {
			a.scored++
			a.scoreSum += rec.Float(colScore)
			a.prSum += rec.Float(colPassRate)
			a.scores = append(a.scores, rec.Float(colScore))
			a.passRates = append(a.passRates, rec.Float(colPassRate))
		}
		a.pooledPass += int(rec.Int(colPassed))
		a.pooledN += scoredN
		a.points = append(a.points, rec)
	}

	out := make([]scorecardDTO, 0, len(order))
	for _, k := range order {
		a := groups[k]
		// Order this group's runs chronologically by started_at for the trend.
		sort.Slice(a.points, func(i, j int) bool {
			return a.points[i].String(colStartedAt) < a.points[j].String(colStartedAt)
		})
		card := scorecardDTO{
			Suite: k.suite, SubjectRef: k.subject, Variant: k.variant, Runs: a.runs,
			Trend: make([]trendPoint, 0, len(a.points)),
		}
		// The kind of the most recent run: a.points is already sorted
		// chronologically just above, and a run may override the suite's kind,
		// so the newest row is the one the card is describing.
		if n := len(a.points); n > 0 {
			card.SubjectKind = a.points[n-1].String(colSubjKind)
		}
		if a.scored > 0 {
			card.MeanScore = a.scoreSum / float64(a.scored)
			card.PassRate = a.prSum / float64(a.scored)
		}
		if lo, hi, ok := meanInterval(a.scores); ok {
			card.MeanScoreCI = &ciDTO{Lo: lo, Hi: hi}
		}
		if lo, hi, ok := meanInterval(a.passRates); ok {
			card.PassRateCI = &ciDTO{Lo: lo, Hi: hi}
		}
		if a.pooledN > 0 {
			lo, hi := wilsonInterval(a.pooledPass, a.pooledN)
			card.PooledPassRate = &measuredRate{
				Rate: float64(a.pooledPass) / float64(a.pooledN), N: a.pooledN, CI: ciDTO{Lo: lo, Hi: hi},
			}
		}
		for _, p := range a.points {
			card.Trend = append(card.Trend, trendPoint{
				At: p.String(colStartedAt), Score: p.Float(colScore), PassRate: p.Float(colPassRate),
			})
		}
		if n := len(a.points); n > 0 {
			last := a.points[n-1]
			card.LastScore = last.Float(colScore)
			card.Regressed = last.Bool(colRegressed)
		}
		out = append(out, card)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Suite != out[j].Suite {
			return out[i].Suite < out[j].Suite
		}
		return out[i].SubjectRef < out[j].SubjectRef
	})
	return out
}

// writeScorecardsCSV writes the scorecards as a flat CSV (one row per scorecard) so
// Can offer an export without parsing nested JSON.
func writeScorecardsCSV(w http.ResponseWriter, cards []scorecardDTO) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	var b strings.Builder
	// subject_kind rides next to subject_ref for the same reason it is in the
	// JSON: the ref alone does not identify the subject.
	b.WriteString("suite_ref,subject_ref,subject_kind,prompt_variant,runs,pass_rate,mean_score,last_score,regressed," +
		"n_scored,pooled_pass_rate,pooled_pass_rate_lo,pooled_pass_rate_hi\n")
	for _, c := range cards {
		b.WriteString(csvField(c.Suite))
		b.WriteByte(',')
		b.WriteString(csvField(c.SubjectRef))
		b.WriteByte(',')
		b.WriteString(csvField(c.SubjectKind))
		b.WriteByte(',')
		b.WriteString(csvField(c.Variant))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(c.Runs))
		b.WriteByte(',')
		b.WriteString(strconv.FormatFloat(c.PassRate, 'f', 4, 64))
		b.WriteByte(',')
		b.WriteString(strconv.FormatFloat(c.MeanScore, 'f', 4, 64))
		b.WriteByte(',')
		b.WriteString(strconv.FormatFloat(c.LastScore, 'f', 4, 64))
		b.WriteByte(',')
		b.WriteString(strconv.FormatBool(c.Regressed))
		b.WriteByte(',')
		// The pooled columns stay EMPTY (not zero) when nothing was scored — an
		// empty cell is "unmeasured", a 0.0 would be a fabricated rate.
		if c.PooledPassRate != nil {
			b.WriteString(strconv.Itoa(c.PooledPassRate.N))
			b.WriteByte(',')
			b.WriteString(strconv.FormatFloat(c.PooledPassRate.Rate, 'f', 4, 64))
			b.WriteByte(',')
			b.WriteString(strconv.FormatFloat(c.PooledPassRate.CI.Lo, 'f', 4, 64))
			b.WriteByte(',')
			b.WriteString(strconv.FormatFloat(c.PooledPassRate.CI.Hi, 'f', 4, 64))
		} else {
			b.WriteString(",,,")
		}
		b.WriteByte('\n')
	}
	_, _ = w.Write([]byte(b.String()))
}

// csvField quotes a CSV field when it contains a comma, quote or newline (RFC 4180).
func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\n\r") {
		return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	return s
}
