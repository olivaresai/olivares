// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import "testing"

func TestMicroUSDToDecimal(t *testing.T) {
	cases := map[int64]string{
		0:          "0.000000",
		1_000_000:  "1.000000",
		1_234_500:  "1.234500", // $1.2345 billed cost
		500_000:    "0.500000",
		-2_000_000: "-2.000000",
		1:          "0.000001",
	}
	for micro, want := range cases {
		if got := microUSDToDecimal(micro); got != want {
			t.Errorf("microUSDToDecimal(%d) = %q, want %q", micro, got, want)
		}
	}
}

func TestHostProviderMapping(t *testing.T) {
	cases := map[string]string{
		"bedrock-mantle":      "Amazon Web Services",
		"claude-platform-aws": "Amazon Web Services",
		"vertex":              "Google Cloud",
		"foundry":             "Microsoft Azure",
		"direct":              "anthropic", // falls back to the model provider
		"":                    "anthropic",
	}
	for gw, want := range cases {
		if got := hostProvider(gw, "anthropic"); got != want {
			t.Errorf("hostProvider(%q) = %q, want %q", gw, got, want)
		}
	}
}

func TestFocusRowProvenanceCostColumns(t *testing.T) {
	// Billed row → BilledCost; estimated row → ListCost. (Column order matches focusColumns.)
	billedIdx := indexOf(focusColumns, "BilledCost")
	listIdx := indexOf(focusColumns, "ListCost")
	effIdx := indexOf(focusColumns, "EffectiveCost")

	billed := sampleRecord("k1", "", attribution{
		ProviderRef: "anthropic", ModelRef: "claude-opus-4-8", CostMicroUSD: 1_234_500,
		Provenance: provenanceBilled,
	})
	est := sampleRecord("k2", "", attribution{
		ProviderRef: "anthropic", ModelRef: "claude-opus-4-8", CostMicroUSD: 500_000,
		Provenance: provenanceEstimated,
	})

	// Single-stream export (default/estimated or billed-only): every row's cost is its
	// EffectiveCost (sum-safe because only one stream is present).
	row := focusRow(billed, false)
	if row[billedIdx] != "1.234500" || row[listIdx] != "" || row[effIdx] != "1.234500" {
		t.Errorf("billed (single) cols = billed %q list %q eff %q", row[billedIdx], row[listIdx], row[effIdx])
	}
	row = focusRow(est, false)
	if row[billedIdx] != "" || row[listIdx] != "0.500000" || row[effIdx] != "0.500000" {
		t.Errorf("estimated (single) cols = billed %q list %q eff %q", row[billedIdx], row[listIdx], row[effIdx])
	}

	// Mixed "all" export: only the BILLED row contributes EffectiveCost; the estimated
	// row leaves it empty (its figure stays in ListCost) so SUM(EffectiveCost) is not
	// doubled across the two views of the same spend.
	row = focusRow(billed, true)
	if row[effIdx] != "1.234500" {
		t.Errorf("billed (all) EffectiveCost = %q, want 1.234500", row[effIdx])
	}
	row = focusRow(est, true)
	if row[effIdx] != "" || row[listIdx] != "0.500000" {
		t.Errorf("estimated (all) cols = eff %q list %q, want eff empty / list 0.500000", row[effIdx], row[listIdx])
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
