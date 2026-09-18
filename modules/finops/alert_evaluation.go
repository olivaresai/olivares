// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"math"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// THE ONE FINANCIAL EVALUATION, shared by the alert path and by status.
//
// Before this file the two disagreed by construction: `evaluateBudgets` summed the
// old aggregate with int64 and compared with float64, while budgetStatus computed
// its own figures. Both could turn an unread page into a total. This evaluation
// runs the A4.1 helpers — the strict cost read, the checked wide sum, the rational
// threshold — once per budget, and hands the same classified result to whoever
// asks. Nothing here replaces or certifies scanSamples, aggregatePeriod,
// CheckSpendLimit, the forecast or the preventive reservation path; those keep the
// behavior root already accepted and remain outside this increment.
//
// The rules it must not bend:
//
//   - unknown is not zero and not "within budget". It cannot produce a crossing.
//   - a lower bound proves the crossings it REACHES and nothing else. Below the
//     target it is unproven, never "not reached".
//   - a real read/storage error stays an error and aborts its caller's transaction.
//     It is never downgraded to a classification, a warning or a zero.
//   - the policy is an INPUT and is validated: a present but malformed money cell
//     does not become an exact zero via specInt64's silent default.

// budgetConfigFault names, in a closed vocabulary, a policy field this evaluation
// could not accept. It is not a storage error: the read succeeded and the value is
// unusable, which is a different fact and is reported as one.
type budgetConfigFault string

const (
	configFaultNone            budgetConfigFault = ""
	configFaultLimitMalformed  budgetConfigFault = "budget_limit_malformed"
	configFaultStaticMalformed budgetConfigFault = "budget_static_reserved_malformed"
	configFaultStaticNegative  budgetConfigFault = "budget_static_reserved_negative"
	// The two NULL faults are the A4.2 correction. An optional field that is ABSENT
	// takes its documented default; a field that is PRESENT and null is a stored
	// representation this evaluation cannot use, and the two were being collapsed.
	configFaultLimitNull  budgetConfigFault = "budget_limit_null"
	configFaultStaticNull budgetConfigFault = "budget_static_reserved_null"
)

// staticComponent is the budget's configured reserved capacity as an EVALUATION
// input: known only when the policy carried a usable value.
type staticComponent struct {
	Known    bool
	MicroUSD int64
	Fault    budgetConfigFault
}

// thresholdEvaluation is one configured threshold, decided exactly.
type thresholdEvaluation struct {
	// Configured is the threshold as the policy stored it.
	Configured float64
	// Decision carries the normalized decimal, the exact rational target and the
	// proven/not_reached/unproven outcome from the A4.1 helper.
	Decision thresholdDecision
	// LegacyPct is the integer percent the historical alert identity uses, and
	// LegacyPctOK reports that the conversion is representable. A threshold whose
	// percent cannot be formed does not silently overflow into another alert's
	// identity: the decision is kept and the row is not written.
	LegacyPct   int
	LegacyPctOK bool
}

// budgetEvaluation is the whole financial answer for one budget at one instant.
type budgetEvaluation struct {
	PolicyID      model.ID
	PolicyName    string
	PolicyVersion int64
	Spec          budgetSpec
	Config        budgetConfigFault

	// Window is the half-open period this evaluation used, built from the same
	// period arithmetic the rest of the module uses.
	Window      strictCostWindow
	PeriodStart time.Time
	HasPeriod   bool
	PeriodEnd   time.Time

	Cost    strictCostTotal
	Static  staticComponent
	Dynamic dynamicComponent
	Amount  effectiveAmount

	Thresholds []thresholdEvaluation

	// EvaluatedAt is when this evaluation ran; SampleAt is the instant that drove
	// the period selection (a sample's OccurredAt for the alert path, the clock for
	// status). They are different facts and are recorded separately.
	EvaluatedAt time.Time
	SampleAt    time.Time
}

// established reports that the effective amount is exact.
func (e budgetEvaluation) established() bool { return e.Amount.Class == amountExact }

// causes collects the closed causes behind a non-exact amount, in a stable order.
func (e budgetEvaluation) causes() []amountCause {
	out := append([]amountCause(nil), e.Amount.Causes...)
	if e.Config != configFaultNone {
		out = append(out, amountCause(e.Config))
	}
	return out
}

// proven returns the threshold evaluations whose crossing is PROVEN, in the order
// the policy configured them.
func (e budgetEvaluation) proven() []thresholdEvaluation {
	var out []thresholdEvaluation
	for _, t := range e.Thresholds {
		if t.Decision.Result == crossingProven {
			out = append(out, t)
		}
	}
	return out
}

// evaluateBudgetAmount is the shared evaluation. `at` selects the period (a
// sample's OccurredAt on the alert path, the clock for status) and `now` is the
// evaluation instant used for reservation validity and recorded as such.
//
// It returns an error ONLY for a real read/storage failure. Everything else — an
// incomplete enumeration, a malformed row, an unusable policy field — is carried in
// the result with its closed cause, because those are classifications, not outages.
func evaluateBudgetAmount(ctx context.Context, sc store.Scope, p model.Policy, spec budgetSpec, at, now time.Time) (budgetEvaluation, error) {
	pStart, hasLower := periodStart(spec.Period, at)
	eval := budgetEvaluation{
		PolicyID: p.ID, PolicyName: p.Name, PolicyVersion: p.Version, Spec: spec,
		PeriodStart: pStart, HasPeriod: hasLower, PeriodEnd: periodEnd(spec.Period, pStart),
		EvaluatedAt: now, SampleAt: at,
	}
	eval.Window = strictCostWindowForBudget(sc.Tenant(), spec, at)

	// The policy is an input, and a present-but-malformed money cell must not become
	// an exact zero through specInt64's silent default. A present NULL is not the
	// absent field either: only absence takes the documented default.
	limit, limitState := readSpecInt64(p.Spec, "limit_micro_usd")
	switch limitState {
	case specIntNull:
		eval.Config = configFaultLimitNull
	case specIntMalformed:
		eval.Config = configFaultLimitMalformed
	}
	eval.Spec.LimitMicroUSD = limit
	eval.Static = staticFromSpec(p.Spec)
	if eval.Config == configFaultNone && eval.Static.Fault != configFaultNone {
		eval.Config = eval.Static.Fault
	}

	eval.Cost = readStrictCostTotal(ctx, sc, eval.Window)
	if eval.Cost.Err != nil {
		// A real outage: the caller's transaction decides what to do with it, and the
		// ingest path aborts. It is not a classification.
		return eval, eval.Cost.Err
	}

	// The version-aware hold reader over the evaluation's OWN window. The
	// window is already computed above as eval.PeriodStart/PeriodEnd/HasPeriod, so
	// the evaluator no longer asks a reader that had to assume one.
	reserved, err := heldReservedForWindow(ctx, sc, p.ID, spec.Key, pStart, eval.PeriodEnd, hasLower, now)
	if err != nil {
		return eval, err
	}
	eval.Dynamic = dynamicFromReservedTotal(reserved)
	if reserved.UnallocatedHistorical {
		// The enumeration completed and every row was clean; what is not
		// established is that a held obligation with NO accounting instant belongs to
		// this window, which has already closed. dynamicFromReservedTotal would name
		// that "scan incomplete" — the only reason it has for the non-negative class —
		// and a durable evidence envelope must not carry a reason that is not the one.
		//
		// unknownDynamic is the EXISTING component for an obligation whose figure was
		// not established while nothing contradicts the invariant, with the existing
		// dynamic_reservation_unknown cause. No new vocabulary, no invented date, no
		// zero: the lower bound still rests on cost and static capacity, so a crossing
		// those alone prove is still proven, and one that needs the undated hold is
		// not.
		eval.Dynamic = unknownDynamic()
	}

	eval.Amount = classifyEffectiveAmount(effectiveAmountInputs{
		Cost:           eval.Cost,
		StaticKnown:    eval.Static.Known,
		StaticMicroUSD: eval.Static.MicroUSD,
		Dynamic:        eval.Dynamic,
	})
	for _, thr := range spec.Thresholds {
		te := thresholdEvaluation{
			Configured: thr,
			Decision:   evaluateThresholdCrossing(eval.Amount, eval.Spec.LimitMicroUSD, thr),
		}
		te.LegacyPct, te.LegacyPctOK = legacyThresholdPct(thr)
		eval.Thresholds = append(eval.Thresholds, te)
	}
	return eval, nil
}

// staticFromSpec reads the configured reserved capacity as an evaluation input.
// Absent is the documented default of an optional field and stays a known zero; a
// present but unusable representation is NOT zero, and a negative one is malformed
// configuration rather than a credit against the budget.
//
// A4.2 CORRECTION: a PRESENT null was landing in the absent branch, so a policy
// storing `reserved_micro_usd: null` produced a known, exact reserve of zero and the
// evaluator could record an exact alert resting on a reserve nobody configured. The
// contract's exception is for the field that is NOT THERE; a present representation
// this reader cannot use is a typed configuration fault, and stays one.
func staticFromSpec(spec map[string]any) staticComponent {
	value, state := readSpecInt64(spec, "reserved_micro_usd")
	switch state {
	case specIntNull:
		return staticComponent{Fault: configFaultStaticNull}
	case specIntMalformed:
		return staticComponent{Fault: configFaultStaticMalformed}
	}
	if value < 0 {
		return staticComponent{Fault: configFaultStaticNegative}
	}
	return staticComponent{Known: true, MicroUSD: value}
}

// specIntState is what a policy money cell WAS, kept apart from what it was worth.
// Absent and null are two different stored states and only the first has a
// documented default; collapsing them is the defect this type exists to prevent.
type specIntState int

const (
	// specIntAbsent: the key is not in the spec at all. The optional field takes its
	// documented default, which is a usable zero.
	specIntAbsent specIntState = iota
	// specIntValue: a finite, integral, representable number.
	specIntValue
	// specIntNull: the key IS present and its value is null. A stored null is a
	// representation, not an omission, and this evaluation cannot read a number from
	// it.
	specIntNull
	// specIntMalformed: present and unusable — a string, a fraction, a NaN/Inf, or a
	// magnitude that does not round-trip through int64.
	specIntMalformed
)

// readSpecInt64 reads a policy money field without specInt64's silent default: it
// reports the stored STATE beside the value, so a caller can tell an absent optional
// field from a present one it cannot use.
func readSpecInt64(spec map[string]any, key string) (int64, specIntState) {
	raw, present := spec[key]
	if !present {
		return 0, specIntAbsent
	}
	if raw == nil {
		return 0, specIntNull
	}
	switch v := raw.(type) {
	case int64:
		return v, specIntValue
	case int:
		return int64(v), specIntValue
	case int32:
		return int64(v), specIntValue
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
			return 0, specIntMalformed
		}
		// float64 cannot represent every int64; refuse the values where the
		// conversion would not round-trip rather than record a shifted amount.
		if v < -9.223372036854775e18 || v > 9.223372036854775e18 {
			return 0, specIntMalformed
		}
		return int64(v), specIntValue
	}
	return 0, specIntMalformed
}

// legacyThresholdPct forms the integer percent the historical alert identity uses,
// and reports whether that conversion is representable. The identity itself is not
// changed here: a threshold that cannot form a percent keeps its decision in the
// evaluation and does not overflow into some other alert's row.
func legacyThresholdPct(thr float64) (int, bool) {
	if math.IsNaN(thr) || math.IsInf(thr, 0) || thr <= 0 {
		return 0, false
	}
	scaled := thr*100 + 0.5
	if scaled < 1 || scaled > float64(math.MaxInt32) {
		return 0, false
	}
	return int(scaled), true
}
