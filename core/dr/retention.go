// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

// Retention decides which DR bundles to keep and which to prune. A flat
// "delete older than N days" (the existing --retain-days) is coarse: it either
// keeps too much (storage cost) or drops the long-horizon restore points an audit
// or a slow-burn corruption needs. GFS — Grandfather-Father-Son — keeps a
// geometric spread instead: the last few DAYS, then one per WEEK, one per MONTH,
// one per YEAR. That is the retention shape backup tools (borg, restic, Veeam) and
// auditors expect, and it is what the offsite mirror should hold.
//
// PlanGFS is a PURE function of (bundles, policy, now): no I/O, fully deterministic,
// so the same decision runs identically over a local directory listing and an
// offsite ListObjectsV2 result, and is exhaustively testable.

import (
	"sort"
	"time"
)

// BundleMeta is the minimum a retention decision needs about one bundle: a stable
// identity (Name — a filename locally, an object name offsite) and when it was
// taken. CreatedAt should be the manifest's CreatedAt (the RPO instant); the caller
// falls back to the file mtime when a manifest cannot be read.
type BundleMeta struct {
	Name      string
	CreatedAt time.Time
}

// GFSPolicy is a Grandfather-Father-Son retention policy. Each field is a COUNT of
// distinct periods to keep the newest bundle of. Zero for every field means "keep
// everything" (retention disabled — the safe default). KeepLast always keeps the N
// newest bundles regardless of period, so a burst of same-day backups near a
// disaster is never pruned below a floor.
type GFSPolicy struct {
	Daily    int
	Weekly   int
	Monthly  int
	Yearly   int
	KeepLast int
}

// IsZero reports whether the policy prunes nothing (all limits zero).
func (p GFSPolicy) IsZero() bool {
	return p.Daily == 0 && p.Weekly == 0 && p.Monthly == 0 && p.Yearly == 0 && p.KeepLast == 0
}

// RetentionDecision is one bundle's outcome.
type RetentionDecision struct {
	Bundle BundleMeta
	Keep   bool
	// Reason is the rule that kept it ("daily", "weekly", "monthly", "yearly",
	// "keep-last", or a combination) or "expired" when pruned.
	Reason string
}

// RetentionPlan is the full decision over a bundle set.
type RetentionPlan struct {
	Keep    []BundleMeta
	Delete  []BundleMeta
	Decided []RetentionDecision
}

// PlanGFS applies a GFS policy to bundles as of now. It never deletes when the
// policy IsZero (retention disabled). Bundles are considered newest-first; for each
// granularity it keeps the newest bundle of each of the most recent N distinct
// periods that HAVE a bundle (the borg/restic semantics). A bundle kept by any
// granularity survives. Ties on CreatedAt are broken by Name so the plan is stable.
func PlanGFS(bundles []BundleMeta, p GFSPolicy, now time.Time) RetentionPlan {
	ordered := make([]BundleMeta, len(bundles))
	copy(ordered, bundles)
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].CreatedAt.After(ordered[j].CreatedAt) // newest first
		}
		return ordered[i].Name > ordered[j].Name
	})

	plan := RetentionPlan{}
	if p.IsZero() {
		// Retention disabled: keep everything.
		for _, b := range ordered {
			plan.Keep = append(plan.Keep, b)
			plan.Decided = append(plan.Decided, RetentionDecision{Bundle: b, Keep: true, Reason: "retention-disabled"})
		}
		return plan
	}

	// Per-granularity: track how many distinct periods kept, and which period keys
	// have already been satisfied (only the newest bundle of a period is kept).
	type gran struct {
		limit int
		count int
		seen  map[string]bool
		keyOf func(time.Time) string
	}
	grans := []*gran{
		{limit: p.Daily, seen: map[string]bool{}, keyOf: dayKey},
		{limit: p.Weekly, seen: map[string]bool{}, keyOf: weekKey},
		{limit: p.Monthly, seen: map[string]bool{}, keyOf: monthKey},
		{limit: p.Yearly, seen: map[string]bool{}, keyOf: yearKey},
	}
	labels := []string{"daily", "weekly", "monthly", "yearly"}

	for idx, b := range ordered {
		reasons := make([]string, 0, 4)
		if p.KeepLast > 0 && idx < p.KeepLast {
			reasons = append(reasons, "keep-last")
		}
		for gi, g := range grans {
			if g.limit <= 0 {
				continue
			}
			k := g.keyOf(b.CreatedAt)
			if g.seen[k] {
				continue // an earlier (newer) bundle already represents this period
			}
			if g.count >= g.limit {
				continue // this granularity's quota is full
			}
			g.seen[k] = true
			g.count++
			reasons = append(reasons, labels[gi])
		}
		if len(reasons) > 0 {
			plan.Keep = append(plan.Keep, b)
			plan.Decided = append(plan.Decided, RetentionDecision{Bundle: b, Keep: true, Reason: joinReasons(reasons)})
		} else {
			plan.Delete = append(plan.Delete, b)
			plan.Decided = append(plan.Decided, RetentionDecision{Bundle: b, Keep: false, Reason: "expired"})
		}
	}
	return plan
}

// PlanAge applies the flat age-based policy a retain-days knob promises (the
// console backup schedule, the CLI --retain-days): keep every bundle whose
// CreatedAt is within retainDays days of now, prune the rest. retainDays <= 0
// keeps everything (retention disabled — the safe default). Like PlanGFS it is
// a PURE function of (bundles, retainDays, now): no I/O, fully deterministic.
func PlanAge(bundles []BundleMeta, retainDays int, now time.Time) RetentionPlan {
	plan := RetentionPlan{}
	if retainDays <= 0 {
		for _, b := range bundles {
			plan.Keep = append(plan.Keep, b)
			plan.Decided = append(plan.Decided, RetentionDecision{Bundle: b, Keep: true, Reason: "retention-disabled"})
		}
		return plan
	}
	cutoff := now.Add(-time.Duration(retainDays) * 24 * time.Hour)
	for _, b := range bundles {
		if b.CreatedAt.Before(cutoff) {
			plan.Delete = append(plan.Delete, b)
			plan.Decided = append(plan.Decided, RetentionDecision{Bundle: b, Keep: false, Reason: "expired"})
			continue
		}
		plan.Keep = append(plan.Keep, b)
		plan.Decided = append(plan.Decided, RetentionDecision{Bundle: b, Keep: true, Reason: "age"})
	}
	return plan
}

func dayKey(t time.Time) string   { return t.UTC().Format("2006-01-02") }
func monthKey(t time.Time) string { return t.UTC().Format("2006-01") }
func yearKey(t time.Time) string  { return t.UTC().Format("2006") }
func weekKey(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	// Zero-pad so string ordering matches chronological ordering.
	return isoWeekString(y, w)
}

func isoWeekString(y, w int) string {
	// %04d-W%02d without fmt import churn on the hot path.
	ys := itoa4(y)
	ws := itoa2(w)
	return ys + "-W" + ws
}

func itoa4(n int) string {
	b := []byte("0000")
	for i := 3; i >= 0 && n > 0; i-- {
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b)
}

func itoa2(n int) string {
	b := []byte("00")
	for i := 1; i >= 0 && n > 0; i-- {
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b)
}

func joinReasons(rs []string) string {
	out := ""
	for i, r := range rs {
		if i > 0 {
			out += "+"
		}
		out += r
	}
	return out
}
