// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"math"
	"testing"
)

// TestAddInt64ReportsTheWrapInsteadOfPerformingIt walks the boundary itself: the
// largest representable total is still a total, and one micro-USD past it is not a
// negative amount — it is no amount at all. The direction matters because a total
// ABOVE the range exceeds every ceiling (a fact a budget may act on) while one
// below it does not.
func TestAddInt64ReportsTheWrapInsteadOfPerformingIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		a, b    int64
		want    int64
		wantOK  bool
		above   bool
		comment string
	}{
		{name: "the ceiling itself is representable", a: math.MaxInt64 - 1, b: 1, want: math.MaxInt64, wantOK: true},
		{name: "one past the ceiling is not an amount", a: math.MaxInt64, b: 1, above: true},
		{name: "one past the floor is not an amount", a: math.MinInt64, b: -1},
		{name: "opposite signs can never overflow", a: math.MaxInt64, b: math.MinInt64, want: -1, wantOK: true},
		{name: "the floor itself is representable", a: math.MinInt64 + 1, b: -1, want: math.MinInt64, wantOK: true},
		{name: "ordinary money", a: 3 * oneUSD, b: 4 * oneUSD, want: 7 * oneUSD, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := addInt64(tc.a, tc.b)
			if got.OK != tc.wantOK || got.Above != tc.above {
				t.Fatalf("addInt64(%d, %d) = %+v, want OK=%v above=%v", tc.a, tc.b, got, tc.wantOK, tc.above)
			}
			if tc.wantOK && got.Value != tc.want {
				t.Fatalf("addInt64(%d, %d) = %d, want %d", tc.a, tc.b, got.Value, tc.want)
			}
			if !tc.wantOK && got.Value != 0 {
				t.Fatalf("a refused addition returned the value %d; a wrapped figure must never leave this helper", got.Value)
			}
		})
	}
}

// TestSumInt64RefusesAnIntermediateItCannotHold pins the deliberate conservatism:
// the terms are added in order and the ledger cannot hold the intermediate, so the
// sum is refused even though the true total would fit. The bias is one-directional
// — a refusal costs a denial, a wrapped total costs the ceiling.
func TestSumInt64RefusesAnIntermediateItCannotHold(t *testing.T) {
	if got := sumInt64(1, 2, 3); !got.OK || got.Value != 6 {
		t.Fatalf("sumInt64(1,2,3) = %+v, want 6", got)
	}
	if got := sumInt64(math.MaxInt64, 5, -5); got.OK {
		t.Fatalf("sumInt64 accepted a total whose intermediate left the range: %+v", got)
	}
	if got := sumInt64(math.MaxInt64, 5, -5); !got.Above {
		t.Fatalf("the refused intermediate left the range from ABOVE; direction lost: %+v", got)
	}
	if got := sumInt64(); !got.OK || got.Value != 0 {
		t.Fatalf("the empty sum must be a representable zero: %+v", got)
	}
}

// TestSubInt64HandlesTheUnnegatableFloor: math.MinInt64 has no positive twin, so
// "limit - effective" cannot be computed by negating the subtrahend. Remaining
// headroom is exactly that subtraction, which is why the case is pinned here.
func TestSubInt64HandlesTheUnnegatableFloor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		a, b   int64
		want   int64
		wantOK bool
		above  bool
	}{
		{name: "ordinary remaining headroom", a: 10 * oneUSD, b: 4 * oneUSD, want: 6 * oneUSD, wantOK: true},
		{name: "a positive limit minus the floor exceeds the range", a: 10, b: math.MinInt64, above: true},
		{name: "a negative minus the floor stays representable", a: -1, b: math.MinInt64, want: math.MaxInt64, wantOK: true},
		{name: "below the floor is refused, and not from above", a: math.MinInt64, b: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := subInt64(tc.a, tc.b)
			if got.OK != tc.wantOK || got.Above != tc.above {
				t.Fatalf("subInt64(%d, %d) = %+v, want OK=%v above=%v", tc.a, tc.b, got, tc.wantOK, tc.above)
			}
			if tc.wantOK && got.Value != tc.want {
				t.Fatalf("subInt64(%d, %d) = %d, want %d", tc.a, tc.b, got.Value, tc.want)
			}
		})
	}
}
