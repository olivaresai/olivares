// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import "testing"

// TestAPIUsageTiers pins the published spend-tier ladder: five rows, unique
// tier names, AsOf-stamped, and the no-monthly-limit invariant (a zero ceiling is
// legal ONLY on the invoiced tier).
func TestAPIUsageTiers(t *testing.T) {
	tiers := APIUsageTiers()
	if len(tiers) != 5 {
		t.Fatalf("tiers = %d, want 5", len(tiers))
	}
	seen := map[string]bool{}
	for _, tr := range tiers {
		if seen[tr.Tier] {
			t.Fatalf("duplicate tier %q", tr.Tier)
		}
		seen[tr.Tier] = true
		if tr.AsOf != limitsAsOf {
			t.Errorf("%s: missing AsOf stamp", tr.Tier)
		}
		if tr.MonthlySpendLimitUSD == 0 && !tr.NoMonthlyLimit {
			t.Errorf("%s: zero monthly limit without NoMonthlyLimit", tr.Tier)
		}
	}
	// Spot-pin the post-2026-05-06 ladder boundaries (rate-limits page).
	if tiers[0].Tier != "tier1" || tiers[0].MonthlySpendLimitUSD != 500 {
		t.Errorf("tier1 = %+v, want $500 monthly limit", tiers[0])
	}
	if tiers[3].Tier != "tier4" || tiers[3].MonthlySpendLimitUSD != 200000 {
		t.Errorf("tier4 = %+v, want $200,000 monthly limit", tiers[3])
	}
	// Returned slice is a copy: mutating it must not touch package state.
	tiers[0].Tier = "mutated"
	if APIUsageTiers()[0].Tier != "tier1" {
		t.Fatal("APIUsageTiers returned package state, not a copy")
	}
}

// TestModelClassRateLimits pins the published default rate-limit table:
// 5 classes x 4 tiers, ITPM monotonic non-decreasing per class across the tier
// ladder, the Fable 5 rows verbatim, and the Haiku 3.5 cache-read caveat carried on
// exactly that class.
func TestModelClassRateLimits(t *testing.T) {
	rows := ModelClassRateLimits()
	if len(rows) != 20 {
		t.Fatalf("rows = %d, want 20 (5 classes x 4 tiers)", len(rows))
	}
	classes := map[string][]ModelClassRateLimit{}
	for _, r := range rows {
		if r.AsOf != limitsAsOf {
			t.Errorf("%s/%s: missing AsOf stamp", r.Tier, r.ModelClass)
		}
		classes[r.ModelClass] = append(classes[r.ModelClass], r)
	}
	if len(classes) != 5 {
		t.Fatalf("classes = %d, want 5", len(classes))
	}
	for class, rs := range classes {
		if len(rs) != 4 {
			t.Fatalf("%s: tiers = %d, want 4", class, len(rs))
		}
		for i := 1; i < len(rs); i++ {
			if rs[i].ITPM < rs[i-1].ITPM {
				t.Errorf("%s: ITPM regresses %s(%d) -> %s(%d)", class, rs[i-1].Tier, rs[i-1].ITPM, rs[i].Tier, rs[i].ITPM)
			}
		}
	}
	// Fable 5 verbatim (post-2026-05-06 rate-limits page).
	fable := classes["claude-fable-5"]
	want := []struct{ rpm, itpm, otpm int64 }{
		{50, 100_000, 20_000}, {1000, 500_000, 100_000}, {2000, 1_500_000, 300_000}, {4000, 4_000_000, 800_000},
	}
	for i, w := range want {
		if fable[i].RPM != w.rpm || fable[i].ITPM != w.itpm || fable[i].OTPM != w.otpm {
			t.Errorf("fable %s = %d/%d/%d, want %d/%d/%d",
				fable[i].Tier, fable[i].RPM, fable[i].ITPM, fable[i].OTPM, w.rpm, w.itpm, w.otpm)
		}
	}
	// The one published per-class caveat rides ONLY on Haiku 3.5.
	for class, rs := range classes {
		for _, r := range rs {
			hasNote := r.Note != ""
			if (class == "claude-haiku-3-5") != hasNote {
				t.Errorf("%s/%s: note = %q (caveat belongs to claude-haiku-3-5 only)", r.Tier, class, r.Note)
			}
		}
	}
}

// TestSubscriptionLimitChanges pins the 2026-05-06 subscription change as DATA: the
// published multiplier and removed peak-hours reduction — never absolute ceilings
// the authority did not publish.
func TestSubscriptionLimitChanges(t *testing.T) {
	chs := SubscriptionLimitChanges()
	if len(chs) != 1 {
		t.Fatalf("changes = %d, want 1", len(chs))
	}
	c := chs[0]
	if c.EffectiveOn != "2026-05-06" || c.FiveHourLimitMultiplier != 2.0 || !c.PeakHoursReductionRemoved {
		t.Fatalf("change = %+v, want 2026-05-06 / x2.0 / peak-hours reduction removed", c)
	}
	if len(c.Plans) != 4 {
		t.Fatalf("plans = %v, want 4 seat plans", c.Plans)
	}
	// Deep copy: mutating the returned Plans must not touch package state.
	c.Plans[0] = "mutated"
	if SubscriptionLimitChanges()[0].Plans[0] != "pro" {
		t.Fatal("SubscriptionLimitChanges returned package state, not a copy")
	}
}
