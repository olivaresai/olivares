// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package claudeadoption

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// dayLayout is the UTC calendar-day bucket key (the trend/window grain).
const dayLayout = "2006-01-02"

// onMetric folds one MetricSample into the per-(subject, metric, day, dimensions) read-
// model row. The natural key is the day-bucketed (subject, name, dimensions) tuple, so
// repeated samples collapse onto ONE row:
//   - an Additive sample (OTLP delta counter) ADDS its value, but ONLY when its instant
//     is newer than the row's high-water (colLastAt) — a re-delivered or out-of-order
//     delta is dropped, so at-least-once delivery never double-counts;
//   - a snapshot sample (the Analytics daily total) keeps the MAX, idempotent under a
//     re-pull whose total only grew within the day.
//
// It ignores an unrecognized metric name, an empty subject, a zero instant, or a non-
// positive value (never a fabricated zero row). The whole upsert is one Mutate so it
// commits atomically; bus ingestion is system, not a principal action, so it is unaudited.
func (m *Module) onMetric(ctx context.Context, tenant model.TenantID, source string, ms sdkmodel.MetricSample) error {
	if !isAdoptionMetricName(ms.Name) || ms.SubjectRef == "" || ms.OccurredAt.IsZero() || ms.Value <= 0 {
		return nil
	}
	day := ms.OccurredAt.UTC().Format(dayLayout)
	nk := naturalKey(ms.SubjectKind, ms.SubjectRef, ms.Name, day, ms.Dimensions)
	atStr := model.NewTimestamp(ms.OccurredAt).String()

	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(adoptionMetricKind)
		if err != nil {
			return err
		}
		existing, found, err := findByNK(ctx, repo, nk)
		if err != nil {
			return err
		}
		if !found {
			_, err := repo.Create(ctx, newRow(nk, day, atStr, source, ms))
			if errors.Is(err, store.ErrConflict) {
				return nil // raced with a concurrent insert of the same bucket
			}
			return err
		}
		newValue, advance := foldValue(existing, ms.Value, ms.Additive, atStr)
		if !advance {
			return nil // duplicate/older delta, or a snapshot that did not grow: no-op
		}
		existing[colValue] = newValue
		existing[colLastAt] = maxTS(existing.String(colLastAt), atStr)
		_, err = repo.Update(ctx, existing)
		return err
	}); err != nil {
		return err
	}

	if ms.SubjectKind == subjectDeveloper {
		// The admin Analytics plane completes by UTC day. When a developer/day
		// snapshot for D arrives, the previous day D-1 is the deterministic point at
		// which both official and OTLP planes should be complete enough to compare.
		m.evaluatePreviousDiscrepancyDay(ctx, tenant, ms.OccurredAt.UTC())
	}
	return nil
}

func previousDay(t time.Time) string { return t.AddDate(0, 0, -1).UTC().Format(dayLayout) }

// foldValue computes the new stored value for an existing row and whether to write it.
// Additive: add only when the sample is strictly newer than the high-water (dedup older/
// duplicate deltas). Snapshot: keep the max, writing only when it grows (or the instant
// advances, so the high-water tracks the latest pull).
func foldValue(existing model.Record, value int64, additive bool, atStr string) (newValue int64, advance bool) {
	cur := existing.Int(colValue)
	if additive {
		if atStr <= existing.String(colLastAt) {
			return cur, false
		}
		return cur + value, true
	}
	if value <= cur && atStr <= existing.String(colLastAt) {
		return cur, false
	}
	if value > cur {
		return value, true
	}
	return cur, true // value unchanged but the instant advanced — refresh the high-water
}

// newRow builds the read-model row for a sample's first occurrence in a bucket.
func newRow(nk, day, atStr, source string, ms sdkmodel.MetricSample) model.Record {
	additive := int64(0)
	if ms.Additive {
		additive = 1
	}
	return model.Record{
		colNK:          nk,
		colSubjectKind: ms.SubjectKind,
		colSubjectRef:  ms.SubjectRef,
		colMetricName:  ms.Name,
		colDay:         day,
		colDimType:     ms.Dimensions[dimType],
		colDimTool:     ms.Dimensions[dimTool],
		colDimDecision: ms.Dimensions[dimDecision],
		colDimModel:    ms.Dimensions[dimModel],
		colTeam:        ms.Labels[labelTeam],
		colSource:      source,
		colUnit:        ms.Unit,
		colValue:       ms.Value,
		colAdditive:    additive,
		colLastAt:      atStr,
	}
}

// findByNK returns the row for a natural key, or found=false.
func findByNK(ctx context.Context, repo store.GenericRepo, nk string) (model.Record, bool, error) {
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colNK, nk)}, Limit: 1})
	if err != nil || len(rows) == 0 {
		return nil, false, err
	}
	return rows[0], true, nil
}

// naturalKey is the dedup key: the day-bucketed (subject, metric, dimensions) tuple. The
// VALUE and the operator team label are excluded — excluding the value is what makes a
// re-pulled day / re-delivered delta upsert instead of inserting a second row; excluding
// the team mirrors the labels-never-join-a-natural-key rule (a session's team is stable).
func naturalKey(subjectKind, subjectRef, name, day string, dims map[string]string) string {
	parts := []string{subjectKind, subjectRef, name, day, canonicalDims(dims)}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// canonicalDims renders a dimension map as a deterministic "k=v;" string (sorted keys).
func canonicalDims(dims map[string]string) string {
	if len(dims) == 0 {
		return ""
	}
	keys := make([]string, 0, len(dims))
	for k := range dims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(dims[k])
		b.WriteByte(';')
	}
	return b.String()
}

// maxTS returns the lexicographically-greater of two canonical RFC3339 UTC timestamps
// (which sort chronologically), so the high-water never regresses.
func maxTS(a, b string) string {
	if b > a {
		return b
	}
	return a
}

// eq builds an equality filter.
func eq(col, val string) model.Filter { return model.Filter{Column: col, Op: model.OpEq, Value: val} }
