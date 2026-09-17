// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import "math"

// Money in this module is integer micro-USD in an int64, and so is the reservation
// ledger's sequence number. A total that leaves the int64 range is not "a very big
// number": Go WRAPS it, silently, to the other end of the range. math.MaxInt64+1 is
// negative — and a negative "already reserved" figure reads as headroom nobody
// holds, so the ceiling comparison inverts and admits precisely the largest request.
//
// These helpers make that wrap a fact the caller must answer for instead of a
// comparison that quietly flips. They deliberately do NOT saturate: returning
// math.MaxInt64 would replace a wrong small amount with a wrong large one and
// present it as if it were the amount. The caller gets "not representable" and
// treats it as an inability to establish the bound — never as a total.

// checkedSum is an int64 addition that reports whether the true result stayed
// inside the int64 range and, when it did not, which side it left from.
type checkedSum struct {
	// Value is the result. It is meaningful ONLY when OK.
	Value int64
	// OK reports that the true result is representable as an int64.
	OK bool
	// Above reports, when !OK, that the true result is greater than math.MaxInt64
	// rather than below math.MinInt64. A result above the range exceeds EVERY
	// int64 ceiling, which is a fact a caller may act on; one below it is not.
	Above bool
}

// addInt64 adds a and b, reporting representability instead of wrapping. Two
// operands can only overflow when they share a sign, and then the machine result
// lands on the opposite side of zero from both — which is exactly what is tested,
// so the check needs no wider type.
func addInt64(a, b int64) checkedSum {
	s := a + b
	if a > 0 && b > 0 && s < 0 {
		return checkedSum{Above: true}
	}
	if a < 0 && b < 0 && s >= 0 {
		return checkedSum{}
	}
	return checkedSum{Value: s, OK: true}
}

// sumInt64 adds vals left to right. A PARTIAL sum that leaves the range fails the
// whole addition, even when a later term would bring the true total back inside
// it: the ledger cannot hold the intermediate value, so the total is not
// established. The bias is one-directional by construction — sumInt64 can refuse a
// representable total, it can never hand back a wrapped one.
func sumInt64(vals ...int64) checkedSum {
	total := checkedSum{OK: true}
	for _, v := range vals {
		total = addInt64(total.Value, v)
		if !total.OK {
			return total
		}
	}
	return total
}

// subInt64 subtracts b from a, reporting representability. math.MinInt64 is
// handled on its own because its negation is not an int64 at all: a-MinInt64 is
// a+2^63, which is representable exactly when a is negative.
func subInt64(a, b int64) checkedSum {
	if b == math.MinInt64 {
		if a < 0 {
			// a+2^63 lands in [0, math.MaxInt64]; two's-complement subtraction
			// computes it exactly, so nothing is lost here.
			return checkedSum{Value: a - math.MinInt64, OK: true}
		}
		return checkedSum{Above: true}
	}
	return addInt64(a, -b)
}
