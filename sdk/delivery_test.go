// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"errors"
	"fmt"
	"testing"
)

// TestClassifyCountJudgesAgainstWhatWasSent pins the rule that a fixed
// "one rejection is total, more than one is an anomaly" test gets wrong.
//
// That shortcut only behaves while every request carries exactly one record, which
// is true of these connectors today and is precisely why the bug would be invisible
// until the day batching arrives — at which point a partially accepted batch would
// be classified as a total refusal and re-sent whole, duplicating everything that
// had landed.
func TestClassifyCountJudgesAgainstWhatWasSent(t *testing.T) {
	for _, tc := range []struct {
		sent, rejected int
		want           DeliveryOutcome
	}{
		{1, 0, OutcomeDelivered},
		{1, 1, OutcomeRejected},            // one of one: total
		{100, 1, OutcomePartial},           // one of a hundred: NOT total
		{100, 100, OutcomeRejected},        // all of them: total
		{100, 99, OutcomePartial},          // the boundary below total
		{100, 101, OutcomeProtocolAnomaly}, // more refused than sent
		{1, -1, OutcomeProtocolAnomaly},    // a negative count is not a value to clamp
		{0, 5, OutcomeIndeterminate},       // a refusal we cannot size
	} {
		if got := ClassifyCount(tc.sent, tc.rejected); got != tc.want {
			t.Fatalf("ClassifyCount(sent=%d, rejected=%d) = %s, want %s",
				tc.sent, tc.rejected, got, tc.want)
		}
	}
}

// TestRetryabilityIsNarrow pins which outcomes may be put back on a retry ladder.
// Only a transient condition qualifies: retrying a refusal re-sends bytes already
// refused, retrying a partial duplicates what landed, and retrying a verdict we
// could not trust manufactures either a silent duplicate or a silent loss.
func TestRetryabilityIsNarrow(t *testing.T) {
	retryable := map[DeliveryOutcome]bool{
		OutcomeUnavailable: true,
		// Indeterminate is retryable on purpose: it is what an unmodified connector
		// produces for a transient network failure, and refusing to retry it would
		// dead-letter every unreachable host on the first attempt.
		OutcomeIndeterminate: true,
	}
	for _, o := range []DeliveryOutcome{
		OutcomeIndeterminate, OutcomeDelivered, OutcomeDeliveredWithWarning,
		OutcomePartial, OutcomeRejected, OutcomeUnavailable, OutcomeProtocolAnomaly,
	} {
		if o.Retryable() != retryable[o] {
			t.Fatalf("%s.Retryable() = %v, want %v", o, o.Retryable(), retryable[o])
		}
	}
	// Acceptance is a separate axis from retryability, and conflating them is what
	// turns a capacity warning into a duplicated event.
	if !OutcomeDeliveredWithWarning.Accepted() {
		t.Fatal("a warning accompanies an ACCEPTED payload; treating it as a failure duplicates data")
	}
	if OutcomeIndeterminate.Accepted() {
		t.Fatal("an unreadable verdict must never count as an acceptance")
	}
	// The two axes are independent, and this is the pair that used to contradict
	// itself: the SDK claimed indeterminate was not retryable while the engine
	// retried it, so the documented contract and the running behavior disagreed.
	if !OutcomeIndeterminate.Retryable() {
		t.Fatal("indeterminate must be retryable: it is what an unmodified connector returns for a transient failure")
	}
}

// TestAnUnwrappedErrorStaysIndeterminate is what makes the contract adoptable: a
// connector written before it existed keeps working and is read as "I do not know",
// which preserves the previous behavior (retry) rather than inventing a verdict.
func TestAnUnwrappedErrorStaysIndeterminate(t *testing.T) {
	got := ReportFor(fmt.Errorf("some transport failure"))
	if got.Outcome != OutcomeIndeterminate {
		t.Fatalf("a plain error resolved to %s; an unmodified connector must not be assumed to have succeeded or failed deterministically", got.Outcome)
	}
	// And a wrapped report survives being wrapped again by an intermediate layer.
	de := NewDeliveryError(DeliveryReport{Outcome: OutcomeRejected, Sent: 1, Rejected: 1}, errors.New("refused"))
	if got := ReportFor(fmt.Errorf("dispatch: %w", de)); got.Outcome != OutcomeRejected {
		t.Fatalf("a wrapped report resolved to %s, want rejected", got.Outcome)
	}
}

// TestZeroValueIsTheUnsafeAssumption guards the choice of zero value. A connector
// that builds a report and forgets to set the outcome must say "I do not know",
// never "delivered".
func TestZeroValueIsTheUnsafeAssumption(t *testing.T) {
	var r DeliveryReport
	if r.Outcome != OutcomeIndeterminate {
		t.Fatalf("the zero outcome is %s; it must be indeterminate so a forgotten classification cannot read as success", r.Outcome)
	}
	if r.Outcome.Accepted() {
		t.Fatal("the zero outcome must not count as an acceptance")
	}
}
