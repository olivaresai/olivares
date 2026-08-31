// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"math"
	"testing"
)

func approx(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestWilsonInterval(t *testing.T) {
	// n=0: the honest "no information" interval.
	if lo, hi := wilsonInterval(0, 0); lo != 0 || hi != 1 {
		t.Errorf("n=0 = [%v,%v], want [0,1]", lo, hi)
	}
	// Hand-checked reference: 8/10 → Wilson 95% ≈ [0.4901, 0.9433].
	lo, hi := wilsonInterval(8, 10)
	if !approx(lo, 0.4901, 0.001) || !approx(hi, 0.9433, 0.001) {
		t.Errorf("8/10 = [%v,%v], want ≈[0.4901,0.9433]", lo, hi)
	}
	// The interval contains the point estimate and stays within [0,1] at the edges.
	lo, hi = wilsonInterval(10, 10)
	if lo <= 0.6 || hi != 1 {
		t.Errorf("10/10 = [%v,%v], want lo>0.6 hi=1", lo, hi)
	}
	lo, hi = wilsonInterval(0, 10)
	if lo != 0 || hi >= 0.4 {
		t.Errorf("0/10 = [%v,%v], want lo=0 hi<0.4", lo, hi)
	}
	// More evidence narrows the interval.
	lo1, hi1 := wilsonInterval(80, 100)
	lo2, hi2 := wilsonInterval(800, 1000)
	if (hi2 - lo2) >= (hi1 - lo1) {
		t.Errorf("interval did not narrow with n: n=100 width %v, n=1000 width %v", hi1-lo1, hi2-lo2)
	}
}

func TestMeanInterval(t *testing.T) {
	// n<2 carries no spread information: no interval, never a fabricated one.
	if _, _, ok := meanInterval([]float64{0.5}); ok {
		t.Error("n=1 must not yield an interval")
	}
	// Constant values: a (numerically) zero-width interval at the mean.
	lo, hi, ok := meanInterval([]float64{0.7, 0.7, 0.7})
	if !ok || !approx(lo, 0.7, 1e-9) || !approx(hi, 0.7, 1e-9) {
		t.Errorf("constant = [%v,%v] ok=%v, want ≈[0.7,0.7] true", lo, hi, ok)
	}
	// Hand-checked: {0,1} → mean 0.5, sd≈0.7071, t(1)=12.706 → half≈6.35, clamped [0,1].
	lo, hi, ok = meanInterval([]float64{0, 1})
	if !ok || lo != 0 || hi != 1 {
		t.Errorf("{0,1} = [%v,%v] ok=%v, want clamped [0,1] true", lo, hi, ok)
	}
}

func TestCohenKappa(t *testing.T) {
	// Perfect agreement on a balanced set: kappa 1.
	k, ok := cohenKappa([]bool{true, false, true, false}, []bool{true, false, true, false})
	if !ok || !approx(k, 1.0, 1e-9) {
		t.Errorf("perfect = %v ok=%v, want 1 true", k, ok)
	}
	// Chance-level agreement: po=0.5, pe=0.5 → kappa 0.
	k, ok = cohenKappa([]bool{true, true, false, false}, []bool{true, false, true, false})
	if !ok || !approx(k, 0.0, 1e-9) {
		t.Errorf("chance = %v ok=%v, want 0 true", k, ok)
	}
	// Both raters constant: pe=1 → undefined, must be flagged, not faked.
	if _, ok := cohenKappa([]bool{true, true}, []bool{true, true}); ok {
		t.Error("constant raters must yield kappa_defined=false")
	}
	// The class-imbalance trap kappa exists for: a judge that always says true on a
	// 90%-true set has 90% agreement but kappa must be ~0.
	judge := make([]bool, 10)
	human := make([]bool, 10)
	for i := range judge {
		judge[i] = true
		human[i] = i != 0 // 9 true, 1 false
	}
	k, ok = cohenKappa(judge, human)
	if !ok || k > 0.01 {
		t.Errorf("always-true judge kappa = %v ok=%v, want ≈0 true", k, ok)
	}
}

func TestSensSpec(t *testing.T) {
	judge := []bool{true, true, false, false, true}
	human := []bool{true, false, false, true, true}
	// TP=2 (i0,i4), FP=1 (i1), TN=1 (i2), FN=1 (i3).
	sens, nPos, spec, nNeg := sensSpec(judge, human)
	if nPos != 3 || nNeg != 2 {
		t.Fatalf("denominators = %d/%d, want 3/2", nPos, nNeg)
	}
	if !approx(sens, 2.0/3.0, 1e-9) || !approx(spec, 0.5, 1e-9) {
		t.Errorf("sens/spec = %v/%v, want 0.667/0.5", sens, spec)
	}
	// No human-fail items: specificity is unmeasured (n=0), not zero-and-claimed.
	_, _, spec, nNeg = sensSpec([]bool{true}, []bool{true})
	if nNeg != 0 || spec != 0 {
		t.Errorf("no-negatives spec = %v n=%d, want 0 with n=0", spec, nNeg)
	}
}

func TestRoganGladen(t *testing.T) {
	// A perfect judge needs no correction.
	theta, ok := roganGladen(0.8, 1, 1)
	if !ok || !approx(theta, 0.8, 1e-9) {
		t.Errorf("perfect judge = %v ok=%v, want 0.8 true", theta, ok)
	}
	// Known correction: p=0.8, sens=0.9, spec=0.9 → (0.8+0.9-1)/0.8 = 0.875.
	theta, ok = roganGladen(0.8, 0.9, 0.9)
	if !ok || !approx(theta, 0.875, 1e-9) {
		t.Errorf("corrected = %v ok=%v, want 0.875 true", theta, ok)
	}
	// An uninformative judge (sens+spec=1) admits no correction.
	if _, ok := roganGladen(0.5, 0.5, 0.5); ok {
		t.Error("uninformative judge must yield ok=false")
	}
	// Clamping.
	theta, ok = roganGladen(0.05, 0.95, 0.8)
	if !ok || theta != 0 {
		t.Errorf("clamp-low = %v, want 0", theta)
	}
}

func TestPearson(t *testing.T) {
	r, ok := pearson([]float64{1, 2, 3, 4}, []float64{2, 4, 6, 8})
	if !ok || !approx(r, 1.0, 1e-9) {
		t.Errorf("perfect positive = %v ok=%v, want 1 true", r, ok)
	}
	r, ok = pearson([]float64{1, 2, 3}, []float64{3, 2, 1})
	if !ok || !approx(r, -1.0, 1e-9) {
		t.Errorf("perfect negative = %v ok=%v, want -1 true", r, ok)
	}
	// Zero variance on one side: undefined, flagged.
	if _, ok := pearson([]float64{1, 1, 1}, []float64{1, 2, 3}); ok {
		t.Error("zero-variance side must yield ok=false")
	}
	if _, ok := pearson([]float64{1, 2}, []float64{1, 2}); ok {
		t.Error("n<3 must yield ok=false")
	}
}

func TestDeterministicSample(t *testing.T) {
	cases := []caseDTO{{CaseKey: "a"}, {CaseKey: "b"}, {CaseKey: "c"}, {CaseKey: "d"}, {CaseKey: "e"}}
	// k<=0 or k>=len returns everything.
	if got := sampleCases(cases, "s", 0); len(got) != 5 {
		t.Errorf("k=0 = %d cases, want all", len(got))
	}
	if got := sampleCases(cases, "s", 9); len(got) != 5 {
		t.Errorf("k>len = %d cases, want all", len(got))
	}
	// Same seed → same subset (the fixed CI subset of decision 3); the result is
	// case_key ordered.
	s1 := sampleCases(cases, "seed-1", 2)
	s2 := sampleCases(cases, "seed-1", 2)
	if len(s1) != 2 || len(s2) != 2 || s1[0].CaseKey != s2[0].CaseKey || s1[1].CaseKey != s2[1].CaseKey {
		t.Errorf("same seed diverged: %v vs %v", s1, s2)
	}
	if s1[0].CaseKey > s1[1].CaseKey {
		t.Errorf("sample not case_key ordered: %v", s1)
	}
	// A different seed picks a different subset for SOME seed (probe a few).
	diff := false
	for _, seed := range []string{"seed-2", "seed-3", "seed-4", "seed-5"} {
		alt := sampleCases(cases, seed, 2)
		if alt[0].CaseKey != s1[0].CaseKey || alt[1].CaseKey != s1[1].CaseKey {
			diff = true
			break
		}
	}
	if !diff {
		t.Error("no probed seed changed the subset — sampling looks seed-independent")
	}
}
