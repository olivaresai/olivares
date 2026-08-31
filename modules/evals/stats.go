// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import "math"

// This file is the statistics layer adds (docs/EVAL-METHODOLOGY.md): closed-form
// 95% confidence intervals for the scorecard/run aggregates, and the judge↔human
// agreement statistics the calibration report carries. Everything here is pure and
// deterministic — no sampling, no randomness — so a re-run reproduces the numbers
// bit-for-bit.
//
// Method choices (defensible, sourced in the methodology doc):
//   - pass-rate (a binomial proportion): WILSON score interval — well-behaved at
//     small n and at p near 0/1, where the naive Wald interval degenerates.
//   - mean score (a bounded continuous mean): Student t interval on the sample mean.
//   - judge↔human agreement: percent agreement (with its own Wilson interval) PLUS
//     Cohen's kappa — agreement alone is not defensible under class imbalance (a
//     judge that always says "fail" on a 90%-fail set scores 90% agreement, kappa≈0).
//   - judge error profile: sensitivity/specificity vs the human reference, the
//     inputs of the Rogan–Gladen bias-corrected prevalence estimator (Lee et al.,
//     ICML 2026) the gate surfaces alongside the raw pass-rate.

// z95 is the two-sided 95% normal critical value.
const z95 = 1.959963985

// wilsonInterval returns the 95% Wilson score interval for successes/n. n<=0 yields
// the degenerate full interval [0,1] — the honest "no information" answer.
func wilsonInterval(successes, n int) (lo, hi float64) {
	if n <= 0 {
		return 0, 1
	}
	nf := float64(n)
	p := float64(successes) / nf
	z2 := z95 * z95
	denom := 1 + z2/nf
	center := (p + z2/(2*nf)) / denom
	half := z95 / denom * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf))
	lo = center - half
	hi = center + half
	if lo < 0 {
		lo = 0
	}
	if hi > 1 {
		hi = 1
	}
	return lo, hi
}

// tCritical95 returns the two-sided 95% Student t critical value for df degrees of
// freedom (a fixed table — no dependency, fully deterministic). df beyond the table
// converges to the normal value.
func tCritical95(df int) float64 {
	table := []float64{
		12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228, // 1..10
		2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086, // 11..20
		2.080, 2.074, 2.069, 2.064, 2.060, 2.056, 2.052, 2.048, 2.045, 2.042, // 21..30
	}
	switch {
	case df <= 0:
		return math.NaN()
	case df <= len(table):
		return table[df-1]
	case df <= 40:
		return 2.021
	case df <= 60:
		return 2.000
	case df <= 120:
		return 1.980
	default:
		return z95
	}
}

// meanInterval returns the 95% Student t interval for the mean of values, clamped to
// [0,1] (every score in this module is bounded). ok is false when n < 2 — a single
// observation carries no spread information, and claiming an interval would be
// fabricated precision.
func meanInterval(values []float64) (lo, hi float64, ok bool) {
	n := len(values)
	if n < 2 {
		return 0, 0, false
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(n)
	var ss float64
	for _, v := range values {
		d := v - mean
		ss += d * d
	}
	sd := math.Sqrt(ss / float64(n-1))
	half := tCritical95(n-1) * sd / math.Sqrt(float64(n))
	lo, hi = mean-half, mean+half
	if lo < 0 {
		lo = 0
	}
	if hi > 1 {
		hi = 1
	}
	return lo, hi, true
}

// cohenKappa returns Cohen's kappa for two binary raters over the same items
// (judge[i] vs human[i]). ok is false on the degenerate case where chance agreement
// is 1 (both raters constant) — kappa is undefined there and reporting a number
// would be fabrication; callers report agreement only and say so.
func cohenKappa(judge, human []bool) (kappa float64, ok bool) {
	n := len(judge)
	if n == 0 || n != len(human) {
		return 0, false
	}
	var agree, jPos, hPos int
	for i := range judge {
		if judge[i] == human[i] {
			agree++
		}
		if judge[i] {
			jPos++
		}
		if human[i] {
			hPos++
		}
	}
	nf := float64(n)
	po := float64(agree) / nf
	pj, ph := float64(jPos)/nf, float64(hPos)/nf
	pe := pj*ph + (1-pj)*(1-ph)
	if 1-pe < 1e-12 {
		return 0, false
	}
	return (po - pe) / (1 - pe), true
}

// sensSpec returns the judge's sensitivity (P(judge pass | human pass)) and
// specificity (P(judge fail | human fail)) vs the human reference, with the
// denominators so a caller can show the n behind each rate. A zero denominator
// yields rate 0 with n 0 — the caller must report it as unmeasured, not as zero.
func sensSpec(judge, human []bool) (sens float64, nPos int, spec float64, nNeg int) {
	var tp, fn, tn, fp int
	for i := range judge {
		switch {
		case human[i] && judge[i]:
			tp++
		case human[i] && !judge[i]:
			fn++
		case !human[i] && !judge[i]:
			tn++
		default:
			fp++
		}
	}
	nPos, nNeg = tp+fn, tn+fp
	if nPos > 0 {
		sens = float64(tp) / float64(nPos)
	}
	if nNeg > 0 {
		spec = float64(tn) / float64(nNeg)
	}
	return sens, nPos, spec, nNeg
}

// roganGladen returns the bias-corrected prevalence estimate for an observed
// proportion p given the classifier's sensitivity and specificity (Rogan–Gladen
// 1978; the plug-in estimator Lee et al. ICML 2026 prescribe for judge-measured
// pass-rates): θ = (p + spec − 1) / (sens + spec − 1), clamped to [0,1]. ok is false
// when sens+spec−1 is ~0 (an uninformative judge — no correction is possible).
func roganGladen(p, sens, spec float64) (theta float64, ok bool) {
	denom := sens + spec - 1
	if math.Abs(denom) < 1e-9 {
		return 0, false
	}
	theta = (p + spec - 1) / denom
	if theta < 0 {
		theta = 0
	}
	if theta > 1 {
		theta = 1
	}
	return theta, true
}

// pearson returns the Pearson correlation between x and y. ok is false when n < 3 or
// either side has (near-)zero variance — the coefficient is undefined/meaningless
// there and the calibration report marks it unmeasured instead.
func pearson(x, y []float64) (r float64, ok bool) {
	n := len(x)
	if n < 3 || n != len(y) {
		return 0, false
	}
	var sx, sy float64
	for i := range x {
		sx += x[i]
		sy += y[i]
	}
	mx, my := sx/float64(n), sy/float64(n)
	var sxy, sxx, syy float64
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx < 1e-12 || syy < 1e-12 {
		return 0, false
	}
	return sxy / math.Sqrt(sxx*syy), true
}
