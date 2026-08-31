// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
}

// TestLifecycleOf pins the declared lifecycle evaluation: the published
// schedule drives deprecated/retired around the boundary dates; unknown refs and
// dateless entries stay deny-closed on the SAFE side (active / deprecated, never a
// guessed retired); prefix collisions never leak a schedule onto a current id.
func TestLifecycleOf(t *testing.T) {
	cases := []struct {
		ref  string
		now  time.Time
		want string
	}{
		// Current generation: no lifecycle hit.
		{"claude-opus-4-8", day(2026, 6, 10), lifecycleActive},
		{"claude-sonnet-4-6", day(2026, 6, 10), lifecycleActive},
		{"claude-fable-5", day(2026, 6, 10), lifecycleActive},
		{"claude-mythos-5", day(2026, 6, 10), lifecycleActive},
		// Unknown ref: active for lifecycle purposes (absence of data never denies).
		{"acme-own-model", day(2026, 6, 10), lifecycleActive},
		// Opus 4 / Sonnet 4 (dated + alias): deprecated now, retired ON 2026-06-15.
		{"claude-opus-4-20250514", day(2026, 6, 10), lifecycleDeprecated},
		{"claude-opus-4-20250514", day(2026, 6, 15), lifecycleRetired},
		{"claude-opus-4-0", day(2026, 6, 14), lifecycleDeprecated},
		{"claude-sonnet-4-20250514", day(2026, 6, 15), lifecycleRetired},
		{"claude-sonnet-4-0", day(2026, 6, 10), lifecycleDeprecated},
		// Opus 4.1: deprecated 2026-06-05, retires 2026-08-05.
		{"claude-opus-4-1-20250805", day(2026, 6, 10), lifecycleDeprecated},
		{"claude-opus-4-1-20250805", day(2026, 8, 5), lifecycleRetired},
		// Before its deprecation date the family is still active.
		{"claude-opus-4-1", day(2026, 6, 4), lifecycleActive},
		// Retired claude-3 generation.
		{"claude-3-5-sonnet-20241022", day(2026, 6, 10), lifecycleRetired},
		{"claude-3-haiku-20240307", day(2026, 4, 19), lifecycleDeprecated},
		{"claude-3-haiku-20240307", day(2026, 4, 20), lifecycleRetired},
		// Mythos preview: deprecated 2026-06-09, retirement unpublished — NEVER
		// escalates to retired without a date (deny-closed on the safe side).
		{"claude-mythos-preview", day(2027, 1, 1), lifecycleDeprecated},
	}
	for _, c := range cases {
		if got := lifecycleOf(c.ref, c.now); got.State != c.want {
			t.Errorf("lifecycleOf(%q, %s) = %s, want %s", c.ref, c.now.Format("2006-01-02"), got.State, c.want)
		}
	}

	// The verdict carries the published replacement and the governance dimensions.
	st := lifecycleOf("claude-opus-4-20250514", day(2026, 6, 10))
	if st.Replacement != "claude-opus-4-8" || !st.Known {
		t.Errorf("opus-4 status = %+v, want replacement claude-opus-4-8", st)
	}
	if st.DaysToRetirement != 5 {
		t.Errorf("opus-4 days to retirement on 2026-06-10 = %d, want 5", st.DaysToRetirement)
	}
}

// TestReferenceGovernanceDimensions pins the declared retention/access dimensions
//: Fable 5 / Mythos 5 are Covered Models (30-day forced retention, no ZDR),
// Mythos rides the Glasswing restricted tier, standard Claude models are
// zdr_eligible, and cross-vendor families stay unverified ("").
func TestReferenceGovernanceDimensions(t *testing.T) {
	cases := []struct {
		ref       string
		retention string
		days      int
		tier      string
	}{
		{"claude-fable-5", retentionCovered, 30, ""},
		{"claude-mythos-5", retentionCovered, 30, accessTierGlasswing},
		{"claude-mythos-preview", "", 0, accessTierGlasswing}, // preview retention NOT verified
		{"claude-opus-4-8", retentionZDREligible, 0, ""},
		{"claude-sonnet-5", retentionZDREligible, 0, ""},
		{"claude-sonnet-4-6", retentionZDREligible, 0, ""},
		{"gpt-4o", "", 0, ""}, // cross-vendor: unverified, deny-closed under require_zdr
	}
	for _, c := range cases {
		ref, ok := lookupReference(c.ref)
		if !ok {
			t.Fatalf("%s: no reference family", c.ref)
		}
		if ref.RetentionClass != c.retention || ref.RetentionDays != c.days || ref.AccessTier != c.tier {
			t.Errorf("%s: retention/days/tier = %q/%d/%q, want %q/%d/%q",
				c.ref, ref.RetentionClass, ref.RetentionDays, ref.AccessTier, c.retention, c.days, c.tier)
		}
	}

	// Fable 5 launch pricing rides the family ($10/$50, cache 12.50/20/1), stamped
	// with the governance AsOf, while mythos-preview stays unpriced.
	fable, _ := lookupReference("claude-fable-5")
	if fable.Pricing == nil || fable.Pricing.InputPerMTokUSD != 10 || fable.Pricing.OutputPerMTokUSD != 50 ||
		fable.Pricing.CacheWritePerMTokUSD != 12.50 || fable.Pricing.CacheWrite1hPerMTokUSD != 20 ||
		fable.Pricing.CacheReadPerMTokUSD != 1 {
		t.Fatalf("fable pricing = %+v, want 10/50 + cache 12.50/20/1", fable.Pricing)
	}
	if fable.Pricing.AsOf != referenceGovernanceAsOf {
		t.Errorf("fable pricing AsOf = %q, want %q (launch verification date)", fable.Pricing.AsOf, referenceGovernanceAsOf)
	}
	if fable.ContextWindow != 1_000_000 || fable.MaxOutputTokens != 128_000 {
		t.Errorf("fable window/output = %d/%d, want 1M/128K", fable.ContextWindow, fable.MaxOutputTokens)
	}
	if preview, _ := lookupReference("claude-mythos-preview"); preview.Pricing != nil {
		t.Error("mythos-preview must stay unpriced (no published list price)")
	}
}

// TestGovernanceDeny pins the per-candidate policy verdict: access tier is
// deny-closed regardless of flags; deny_retired/deny_deprecated are opt-in;
// require_zdr denies covered AND unverified retention (deny-closed).
func TestGovernanceDeny(t *testing.T) {
	now := day(2026, 6, 10)
	type verdict struct {
		kind   string
		denied bool
	}
	cases := []struct {
		name      string
		spec      routingSpec
		ref       string
		suspended []string
		want      verdict
	}{
		// No flags: nothing denied — even a retired model (opt-in).
		{"no flags retired", routingSpec{}, "claude-3-5-sonnet-20241022", nil, verdict{"", false}},
		// deny_retired: retired denied, deprecated NOT.
		{"deny_retired on retired", routingSpec{DenyRetired: true}, "claude-3-5-sonnet-20241022", nil, verdict{govDenyRetired, true}},
		{"deny_retired on deprecated", routingSpec{DenyRetired: true}, "claude-opus-4-1", nil, verdict{"", false}},
		{"deny_retired on active", routingSpec{DenyRetired: true}, "claude-opus-4-8", nil, verdict{"", false}},
		// deny_deprecated is strictly stronger: denies deprecated AND retired.
		{"deny_deprecated on deprecated", routingSpec{DenyDeprecated: true}, "claude-opus-4-1", nil, verdict{govDenyDeprecated, true}},
		{"deny_deprecated on retired", routingSpec{DenyDeprecated: true}, "claude-3-5-sonnet-20241022", nil, verdict{govDenyRetired, true}},
		// require_zdr: covered and unverified BOTH deny; verified zdr_eligible serves.
		{"require_zdr on covered", routingSpec{RequireZDR: true}, "claude-fable-5", nil, verdict{govDenyZDR, true}},
		{"require_zdr on eligible", routingSpec{RequireZDR: true}, "claude-sonnet-4-6", nil, verdict{"", false}},
		{"require_zdr on unverified", routingSpec{RequireZDR: true}, "gpt-4o", nil, verdict{govDenyZDR, true}},
		{"require_zdr on unknown ref", routingSpec{RequireZDR: true}, "acme-own-model", nil, verdict{govDenyZDR, true}},
		// Access tier: deny-closed with NO flags; enrollment opens it.
		{"glasswing unenrolled", routingSpec{}, "claude-mythos-5", nil, verdict{govDenyAccessTier, true}},
		{"glasswing enrolled", routingSpec{AccessTiers: []string{"glasswing"}}, "claude-mythos-5", nil, verdict{"", false}},
		{"glasswing suspended but unenrolled", routingSpec{}, "claude-mythos-5", []string{"glasswing"}, verdict{govDenyAccessTier, true}},
		{"glasswing enrolled but suspended", routingSpec{AccessTiers: []string{"glasswing"}}, "claude-mythos-5", []string{"glasswing"}, verdict{govDenyEntitlement, true}},
		{"unknown suspended tier no-op", routingSpec{AccessTiers: []string{"glasswing"}}, "claude-mythos-5", []string{"other"}, verdict{"", false}},
		// Tier enrollment does not bypass the OTHER deny-closed checks.
		{"glasswing enrolled but zdr", routingSpec{AccessTiers: []string{"glasswing"}, RequireZDR: true}, "claude-mythos-5", nil, verdict{govDenyZDR, true}},
	}
	for _, c := range cases {
		kind, reason, _, denied := c.spec.governanceDeny(c.ref, now, c.suspended)
		if denied != c.want.denied || kind != c.want.kind {
			t.Errorf("%s: deny = (%q, %v), want (%q, %v)", c.name, kind, denied, c.want.kind, c.want.denied)
		}
		if denied && reason == "" {
			t.Errorf("%s: a deny must carry a generic reason", c.name)
		}
	}

	// A lifecycle deny carries the PUBLISHED replacement (actionable, non-sensitive).
	_, _, repl, denied := routingSpec{DenyRetired: true}.governanceDeny("claude-3-5-sonnet-20241022", now, nil)
	if !denied || repl != "claude-sonnet-4-6" {
		t.Fatalf("retired deny replacement = %q (denied=%v), want claude-sonnet-4-6", repl, denied)
	}
}
