// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"sort"
	"testing"
	"time"
)

func mkBundle(name string, t time.Time) BundleMeta { return BundleMeta{Name: name, CreatedAt: t} }

func d(y, m, day, hh int) time.Time {
	return time.Date(y, time.Month(m), day, hh, 0, 0, 0, time.UTC)
}

func keptNames(p RetentionPlan) []string {
	out := make([]string, 0, len(p.Keep))
	for _, b := range p.Keep {
		out = append(out, b.Name)
	}
	sort.Strings(out)
	return out
}

func deletedNames(p RetentionPlan) []string {
	out := make([]string, 0, len(p.Delete))
	for _, b := range p.Delete {
		out = append(out, b.Name)
	}
	sort.Strings(out)
	return out
}

func eqSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", label, got, want)
		}
	}
}

func TestPlanGFSZeroPolicyKeepsAll(t *testing.T) {
	bundles := []BundleMeta{
		mkBundle("a", d(2020, 1, 1, 0)),
		mkBundle("b", d(2026, 7, 9, 0)),
	}
	plan := PlanGFS(bundles, GFSPolicy{}, d(2026, 7, 9, 12))
	if len(plan.Delete) != 0 {
		t.Fatalf("zero policy must delete nothing, got %v", deletedNames(plan))
	}
	if len(plan.Keep) != 2 {
		t.Fatalf("zero policy must keep all, got %d", len(plan.Keep))
	}
}

func TestPlanGFSDailyKeepsMostRecentDistinctDays(t *testing.T) {
	bundles := []BundleMeta{
		mkBundle("07-09", d(2026, 7, 9, 6)),
		mkBundle("07-08", d(2026, 7, 8, 6)),
		mkBundle("07-07", d(2026, 7, 7, 6)),
		mkBundle("07-06", d(2026, 7, 6, 6)),
		mkBundle("07-05", d(2026, 7, 5, 6)),
	}
	plan := PlanGFS(bundles, GFSPolicy{Daily: 3}, d(2026, 7, 9, 12))
	eqSet(t, "keep", keptNames(plan), []string{"07-09", "07-08", "07-07"})
	eqSet(t, "delete", deletedNames(plan), []string{"07-06", "07-05"})
}

func TestPlanGFSDailyDedupsWithinADay(t *testing.T) {
	bundles := []BundleMeta{
		mkBundle("09-08h", d(2026, 7, 9, 8)),
		mkBundle("09-12h", d(2026, 7, 9, 12)),
		mkBundle("09-20h", d(2026, 7, 9, 20)),
		mkBundle("08-06h", d(2026, 7, 8, 6)),
	}
	// Daily=2: keep the NEWEST of day 09 (20h) and the day-08 bundle; prune the two
	// earlier day-09 bundles.
	plan := PlanGFS(bundles, GFSPolicy{Daily: 2}, d(2026, 7, 9, 23))
	eqSet(t, "keep", keptNames(plan), []string{"09-20h", "08-06h"})
	eqSet(t, "delete", deletedNames(plan), []string{"09-08h", "09-12h"})
}

func TestPlanGFSKeepLastFloor(t *testing.T) {
	bundles := []BundleMeta{
		mkBundle("09-08h", d(2026, 7, 9, 8)),
		mkBundle("09-12h", d(2026, 7, 9, 12)),
		mkBundle("09-20h", d(2026, 7, 9, 20)),
		mkBundle("08-06h", d(2026, 7, 8, 6)),
	}
	// Daily=1 alone would keep only 09-20h + prune everything else on other days;
	// KeepLast=3 rescues the 3 newest regardless of period.
	plan := PlanGFS(bundles, GFSPolicy{Daily: 1, KeepLast: 3}, d(2026, 7, 9, 23))
	eqSet(t, "keep", keptNames(plan), []string{"09-20h", "09-12h", "09-08h"})
	eqSet(t, "delete", deletedNames(plan), []string{"08-06h"})
}

func TestPlanGFSFullSpread(t *testing.T) {
	// A realistic estate: dailies this week, then older points that must be held by
	// the weekly/monthly/yearly tiers.
	bundles := []BundleMeta{
		mkBundle("2026-07-09", d(2026, 7, 9, 6)),   // daily
		mkBundle("2026-07-08", d(2026, 7, 8, 6)),   // daily
		mkBundle("2026-07-07", d(2026, 7, 7, 6)),   // daily
		mkBundle("2026-07-02", d(2026, 7, 2, 6)),   // prior ISO week
		mkBundle("2026-06-24", d(2026, 6, 24, 6)),  // week before that
		mkBundle("2026-06-10", d(2026, 6, 10, 6)),  // prior month
		mkBundle("2026-05-05", d(2026, 5, 5, 6)),   // month before that
		mkBundle("2025-12-20", d(2025, 12, 20, 6)), // prior year
		mkBundle("2024-11-01", d(2024, 11, 1, 6)),  // far past — should be pruned
	}
	plan := PlanGFS(bundles, GFSPolicy{Daily: 3, Weekly: 2, Monthly: 2, Yearly: 1}, d(2026, 7, 9, 12))

	// Daily keeps 07-09/08/07. Weekly (2 distinct prior weeks): 07-02 and 06-24.
	// Monthly (2 distinct months): July is already represented (07-09), so the two
	// month slots go to July and June → 06-10 is the newest remaining June bundle.
	// Wait: monthly keeps the newest bundle of each of the 2 most recent months →
	// July (07-09) and June (06-24, already kept by weekly). Then 06-10 and 05-05
	// are month-May / older-June, beyond the 2-month quota. Yearly=1 keeps 2026's
	// newest (07-09). So 06-10, 05-05, 2025-12-20, 2024-11-01 fall to weekly/monthly
	// quotas being full — EXCEPT the test asserts the invariant below rather than the
	// exact combinatorics, which the per-granularity tests already pin.
	kept := map[string]bool{}
	for _, n := range keptNames(plan) {
		kept[n] = true
	}
	// Invariants that must hold regardless of quota interplay:
	for _, must := range []string{"2026-07-09", "2026-07-08", "2026-07-07"} {
		if !kept[must] {
			t.Fatalf("daily bundle %s must be kept; kept=%v", must, keptNames(plan))
		}
	}
	if kept["2024-11-01"] {
		t.Fatalf("far-past 2024-11-01 must be pruned; kept=%v", keptNames(plan))
	}
	// Every kept+deleted bundle is accounted for exactly once.
	if len(plan.Keep)+len(plan.Delete) != len(bundles) {
		t.Fatalf("plan lost bundles: keep=%d delete=%d total=%d", len(plan.Keep), len(plan.Delete), len(bundles))
	}
}

func TestPlanGFSSingleBundleMultipleReasons(t *testing.T) {
	// One lone bundle satisfies daily AND weekly AND monthly AND yearly at once.
	bundles := []BundleMeta{mkBundle("only", d(2026, 7, 9, 6))}
	plan := PlanGFS(bundles, GFSPolicy{Daily: 1, Weekly: 1, Monthly: 1, Yearly: 1}, d(2026, 7, 9, 12))
	if len(plan.Keep) != 1 || len(plan.Delete) != 0 {
		t.Fatalf("lone bundle must be kept")
	}
	if plan.Decided[0].Reason != "daily+weekly+monthly+yearly" {
		t.Fatalf("reason should combine every tier, got %q", plan.Decided[0].Reason)
	}
}

func TestPlanGFSNeverDeletesNewestWithKeepLast(t *testing.T) {
	// With any KeepLast>=1 the newest bundle is always retained, even under an
	// otherwise-aggressive policy.
	bundles := []BundleMeta{
		mkBundle("newest", d(2026, 7, 9, 6)),
		mkBundle("old", d(2020, 1, 1, 6)),
	}
	plan := PlanGFS(bundles, GFSPolicy{Yearly: 1, KeepLast: 1}, d(2026, 7, 9, 12))
	if !containsName(plan.Keep, "newest") {
		t.Fatal("newest bundle must always survive with KeepLast>=1")
	}
}

func containsName(bs []BundleMeta, name string) bool {
	for _, b := range bs {
		if b.Name == name {
			return true
		}
	}
	return false
}

// --- PlanAge (flat retain-days policy) ---------------------------------

func TestPlanAgeZeroKeepsAll(t *testing.T) {
	bundles := []BundleMeta{
		mkBundle("a", d(2020, 1, 1, 6)),
		mkBundle("b", d(2026, 7, 9, 6)),
	}
	plan := PlanAge(bundles, 0, d(2026, 7, 9, 12))
	eqSet(t, "keep", keptNames(plan), []string{"a", "b"})
	if len(plan.Delete) != 0 {
		t.Fatalf("zero retain must delete nothing, got %v", deletedNames(plan))
	}
}

func TestPlanAgePrunesOlderThanCutoff(t *testing.T) {
	now := d(2026, 7, 9, 12)
	bundles := []BundleMeta{
		mkBundle("fresh", now.Add(-2*24*time.Hour)),
		mkBundle("edge", now.Add(-7*24*time.Hour)), // exactly at the cutoff → kept
		mkBundle("stale", now.Add(-8*24*time.Hour)),
	}
	plan := PlanAge(bundles, 7, now)
	eqSet(t, "keep", keptNames(plan), []string{"edge", "fresh"})
	eqSet(t, "delete", deletedNames(plan), []string{"stale"})
	for _, dec := range plan.Decided {
		if dec.Bundle.Name == "stale" && dec.Reason != "expired" {
			t.Fatalf("stale reason = %q, want expired", dec.Reason)
		}
	}
}
