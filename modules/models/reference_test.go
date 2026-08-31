// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"sort"
	"testing"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
)

func TestLookupReferenceLongestPrefix(t *testing.T) {
	cases := []struct {
		ref        string
		wantFamily string
		wantFound  bool
	}{
		{"claude-opus-4-8", "claude-opus", true},
		{"claude-sonnet-5", "claude-sonnet-5", true},
		{"claude-sonnet-4-5", "claude-sonnet", true},
		{"claude-haiku-4-5", "claude-haiku", true},
		{"gpt-4o-mini", "gpt-4o-mini", true}, // longer prefix beats gpt-4o
		{"gpt-4o-2024-11", "gpt-4o", true},   // gpt-4o, not gpt-4o-mini
		{"gemini-1.5-flash-8b", "gemini-1.5-flash", true},
		{"gemini-1.5-pro-002", "gemini-1.5-pro", true},
		{"o1-preview", "o1", true},
		{"CLAUDE-OPUS-4-8", "claude-opus", true}, // case-insensitive
		{"llama-3.1-70b", "", false},             // local: no declared family
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := lookupReference(c.ref)
		if ok != c.wantFound {
			t.Errorf("lookupReference(%q) found = %v, want %v", c.ref, ok, c.wantFound)
			continue
		}
		if ok && got.Family != c.wantFamily {
			t.Errorf("lookupReference(%q) family = %q, want %q", c.ref, got.Family, c.wantFamily)
		}
	}
}

func TestReferencePricingDeclared(t *testing.T) {
	opus, ok := lookupReference("claude-opus-4-8")
	if !ok || opus.Pricing == nil {
		t.Fatal("opus must have declared pricing")
	}
	if opus.Pricing.Source != mp.PricingList || opus.Pricing.AsOf != referencePricingAsOf {
		t.Errorf("opus pricing provenance = %v/%v", opus.Pricing.Source, opus.Pricing.AsOf)
	}
	if opus.Pricing.InputPerMTokUSD != 5 || opus.Pricing.OutputPerMTokUSD != 25 {
		t.Errorf("opus pricing = %v/%v, want 5/25 (current Opus)", opus.Pricing.InputPerMTokUSD, opus.Pricing.OutputPerMTokUSD)
	}
	sonnet5, ok := lookupReference("claude-sonnet-5")
	if !ok || sonnet5.Pricing == nil {
		t.Fatal("sonnet-5 must have declared pricing")
	}
	// ⛔ THIS ASSERTION CERTIFIED A WRONG PRICE FOR 55 DAYS. It pinned 3/15 as of
	// 2026-07-03 — a figure that was never a price but a PREDICTION that the
	// introductory $2/$10 would lapse on 2026-09-01. Anthropic cancelled that
	// increase. So the suite went green on every run while the console billed
	// Sonnet 5 at 1.5x, and anyone correcting the entry would have been told by
	// this test that they had broken it.
	//
	// A hardcoded expectation certifies whatever drift it was written to catch.
	// It stays — a change-detector on a money figure is worth having — but the
	// staleness control below is what makes it honest: pinning digits without a
	// freshness bound is how this survived.
	if sonnet5.Pricing.AsOf != "2026-08-27" || sonnet5.Pricing.InputPerMTokUSD != 2 || sonnet5.Pricing.OutputPerMTokUSD != 10 {
		t.Errorf("sonnet-5 pricing = %+v, want 2/10 as of 2026-08-27 "+
			"(platform.claude.com/docs/en/about-claude/pricing, verified live)", sonnet5.Pricing)
	}
	if sonnet5.ContextWindow != 1_000_000 || sonnet5.MaxOutputTokens != 128_000 {
		t.Errorf("sonnet-5 window/output = %d/%d, want 1M/128K", sonnet5.ContextWindow, sonnet5.MaxOutputTokens)
	}
	// Only opus/sonnet declare computer use; haiku does not.
	if !mp.Has(opus.Capabilities, mp.CapComputerUse) {
		t.Error("opus must declare computer_use")
	}
	haiku, _ := lookupReference("claude-haiku-4-5")
	if mp.Has(haiku.Capabilities, mp.CapComputerUse) {
		t.Error("haiku must not declare computer_use")
	}
}

func TestMaxCoveredRetentionDays(t *testing.T) {
	days, families := MaxCoveredRetentionDays()
	// The provider floor: Fable 5 / Mythos 5 are Covered Models with forced
	// 30-day retention (verified 2026-06-10, effective 2026-06-09).
	if days != 30 {
		t.Errorf("days = %d, want 30", days)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f] = true
	}
	if !got["claude-fable"] || !got["claude-mythos-5"] {
		t.Errorf("families = %v, want claude-fable and claude-mythos-5", families)
	}
	if !sort.StringsAreSorted(families) {
		t.Errorf("families not sorted (deterministic order): %v", families)
	}
	// A covered entry without a positive forced period would silently lower the
	// floor — the designation always carries its days.
	for _, e := range referenceTable {
		if e.RetentionClass == retentionCovered && e.RetentionDays <= 0 {
			t.Errorf("covered family %q declares RetentionDays %d", e.Family, e.RetentionDays)
		}
	}
}

func TestPerTokenMicroUSD(t *testing.T) {
	// $15/MTok = 15 µUSD/token; sub-µUSD models floor to 0 (the coarse field).
	if got := perTokenMicroUSD(15); got != 15 {
		t.Errorf("perTokenMicroUSD(15) = %d, want 15", got)
	}
	if got := perTokenMicroUSD(0.15); got != 0 {
		t.Errorf("perTokenMicroUSD(0.15) = %d, want 0 (coarse field)", got)
	}
	if got := perTokenMicroUSD(0.8); got != 1 {
		t.Errorf("perTokenMicroUSD(0.8) = %d, want 1 (rounded)", got)
	}
	if got := perTokenMicroUSD(0); got != 0 {
		t.Errorf("perTokenMicroUSD(0) = %d, want 0", got)
	}
}

// TestOpus5ResolvesToItsOwnStampedEntry guards a PROVENANCE fix, not a figure.
//
// claude-opus-5's price equals the generic claude-opus entry, so no number moved.
// What moved is whether the cost carries a verification stamp: before this entry
// existed, claude-opus-5 resolved through the generic "claude-opus" prefix and
// the console billed it from a family default with no AsOf of its own.
//
// The reason that mattered is structural: lookupReference documents that a model
// "stays unpriced rather than getting an invented price" when nothing matches —
// but the generic prefix matches EVERY Opus id, so that guard can never fire for
// this family. A guard whose predicate cannot be false protects nothing. The only
// remedy available per-model is an explicit dated entry.
func TestOpus5ResolvesToItsOwnStampedEntry(t *testing.T) {
	t.Parallel()

	got, ok := lookupReference("claude-opus-5")
	if !ok || got.Pricing == nil {
		t.Fatal("claude-opus-5 must resolve with declared pricing")
	}
	// Its OWN entry, not the generic family it used to fall through to.
	if got.Family != "claude-opus-5" {
		t.Errorf("claude-opus-5 resolved to family %q — it is falling through to the "+
			"generic prefix again, which is the provenance hole this entry closes", got.Family)
	}
	if got.Pricing.AsOf != "2026-08-27" {
		t.Errorf("claude-opus-5 AsOf = %q, want 2026-08-27 (verified live against the "+
			"provider pricing page); an unstamped price is a price nobody checked", got.Pricing.AsOf)
	}
	// The figure is unchanged on purpose: this fix must not move money.
	if got.Pricing.InputPerMTokUSD != 5 || got.Pricing.OutputPerMTokUSD != 25 {
		t.Errorf("claude-opus-5 pricing = %v/%v, want 5/25 — this entry exists for "+
			"provenance and must NOT change the figure",
			got.Pricing.InputPerMTokUSD, got.Pricing.OutputPerMTokUSD)
	}
	// Control: the generic still answers for ids that have no entry of their own,
	// so this change narrowed nothing it should not have.
	generic, gok := lookupReference("claude-opus-4-5")
	if !gok || generic.Family != "claude-opus" {
		t.Errorf("claude-opus-4-5 resolved to %q/%v, want the generic claude-opus "+
			"family — the new entry must not steal ids it does not own", generic.Family, gok)
	}
}

// A family that needs more than one prefix (an id form that does not begin with
// the alias — claude-opus-4-20250514 does not start with "claude-opus-4-0")
// declares its tariff and its lifecycle dates TWICE, and nothing made the copies
// agree. The tests that touch these families use the alias id, which resolves to
// the FIRST row by longest-prefix, so a drift in the dated row would be invisible
// to every existing control: the same model would bill two prices depending on
// which id form arrived, or retire on one id and stay live on the other.
//
// This is the same shape as the Sonnet 5 list price that sat 1.5x high for 55
// days: a figure with nothing comparing it to anything.
func TestRowsSharingAFamilyDeclareTheSameMoneyAndTheSameDates(t *testing.T) {
	byFamily := map[string][]referenceModel{}
	for _, e := range referenceTable {
		byFamily[e.Family] = append(byFamily[e.Family], e)
	}
	shared := 0
	for fam, rows := range byFamily {
		if len(rows) < 2 {
			continue
		}
		shared++
		first := rows[0]
		for _, other := range rows[1:] {
			if first.Prefix == other.Prefix {
				t.Errorf("%s: two rows share the prefix %q — the longest-prefix match is a strict >, so the second is unreachable", fam, first.Prefix)
			}
			switch {
			case (first.Pricing == nil) != (other.Pricing == nil):
				t.Errorf("%s: one row prices the family and the other does not (%q vs %q)", fam, first.Prefix, other.Prefix)
			case first.Pricing != nil && *first.Pricing != *other.Pricing:
				t.Errorf("%s: the SAME family declares two tariffs — %q has %+v, %q has %+v",
					fam, first.Prefix, *first.Pricing, other.Prefix, *other.Pricing)
			}
			if first.DeprecatedOn != other.DeprecatedOn || first.RetiredOn != other.RetiredOn {
				t.Errorf("%s: lifecycle dates differ between rows — %q is deprecated %q/retired %q, %q is %q/%q",
					fam, first.Prefix, first.DeprecatedOn, first.RetiredOn,
					other.Prefix, other.DeprecatedOn, other.RetiredOn)
			}
			if first.ReplacementRef != other.ReplacementRef {
				t.Errorf("%s: replacement differs between rows — %q -> %q, %q -> %q",
					fam, first.Prefix, first.ReplacementRef, other.Prefix, other.ReplacementRef)
			}
		}
	}
	// Positive control: if the table ever stops having multi-prefix families this
	// test silently checks nothing, and a guard that cannot fail is not a guard.
	if shared == 0 {
		t.Fatal("no family carries more than one row: this guard is now vacuous and must be re-aimed or removed")
	}
	t.Logf("families carrying more than one prefix row: %d", shared)
}
