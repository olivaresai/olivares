// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"testing"

	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/sdk"
)

// evidenceprecedence_test.go drives the precedence rule itself. The fail-mode matrix in
// modules/inferenceproxy DECLARES it; a declaration is not an implementation, and the two
// drifting apart is exactly how a matrix becomes decoration.
//
// The rule: A DEFAULT MUST NOT OVERRIDE AN EXPLICIT OPERATOR CHOICE. Mandatory recording is the
// right default — a tenant that configured nothing is the one nobody reasoned about — but an
// operator who set the audit spool to `degrade` already said what should happen when it is
// exhausted. Measured by the contrast: with mandatory defaulted on, `degrade` quietly stopped
// degrading for every unconfigured tenant. A posture nobody chose was canceling one somebody
// did.
//
// The yield is bounded by BOTH conditions, and the vectors below exist to keep it that way:
// the tenant must never have configured a posture, AND the fault must be the operator-declared
// degrade. Every other fault is a failure, and a failure is not a decision.
//
// MUTATIONS THAT MUST TURN THIS RED:
//
//  1. Drop the `pol.RecordMandatoryChosen` check from defaultMandatoryYieldsTo. Red in `a
//     tenant that CHOSE mandatory does not yield`.
//     1b. Put `pol.Configured` back in its place. Red in `a tenant with a config row that never
//     mentioned evidence still yields` — the case that exists because the first version of
//     this rule used that signal and refused to yield for somebody who had chosen nothing.
//  2. Widen the fault test to any errEvidenceRefused. Red in the spool_full, ledger_unavailable,
//     ledger_unwired and write_error vectors at once.
//  3. Delete the errors.As guard. Red in `a plain error carries no declared policy at all`.
func TestADefaultMandatoryYieldsOnlyToADeclaredDegrade(t *testing.T) {
	degraded := errEvidenceRefused{fault: sdk.EvidenceFaultSpoolDegraded}
	cases := []struct {
		name       string
		configured bool // there IS a config row
		chosen     bool // and somebody explicitly set the evidence posture
		err        error
		wantYield  bool
	}{
		{"an unconfigured tenant yields to the operator's declared degrade", false, false, degraded, true},
		{"a tenant with a config row that never mentioned evidence still yields", true, false, degraded, true},
		{"a tenant that CHOSE mandatory does not yield", true, true, degraded, false},
		{"spool_full is `block`, which is the operator choosing deny-closed", false, false, errEvidenceRefused{fault: sdk.EvidenceFaultSpoolFull}, false},
		{"an unavailable ledger is a failure, not a decision", false, false, errEvidenceRefused{fault: sdk.EvidenceFaultLedgerUnavailable}, false},
		{"an unwired ledger is a failure, not a decision", false, false, errEvidenceRefused{fault: sdk.EvidenceFaultLedgerUnwired}, false},
		{"a write error is a failure, not a decision", false, false, errEvidenceRefused{fault: sdk.EvidenceFaultWriteError}, false},
		{"a plain error carries no declared policy at all", false, false, errors.New("beginTx: connection reset"), false},
	}
	if len(cases) != 8 {
		t.Fatalf("this test expects 8 vectors and carries %d: a fault added without a decision here is a fault whose precedence nobody chose", len(cases))
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := defaultMandatoryYieldsTo(inferenceproxy.ProxyPolicy{
				Configured: c.configured, RecordMandatoryChosen: c.chosen, RecordMandatory: true,
			}, c.err)
			if got != c.wantYield {
				t.Fatalf("yield = %v, want %v — the rule is bounded by BOTH conditions: an unconfigured tenant AND an operator-declared degrade",
					got, c.wantYield)
			}
		})
	}
}

// TestTheYieldIsWrappedErrorSafe: the deny sites receive whatever the anchor helpers return, and
// those wrap. A rule that only recognized the bare sentinel would deny a degrade it was supposed
// to yield to as soon as somebody added a `fmt.Errorf("...: %w", err)` on the way out.
func TestTheYieldIsWrappedErrorSafe(t *testing.T) {
	wrapped := errors.Join(errors.New("anchor intent"), errEvidenceRefused{fault: sdk.EvidenceFaultSpoolDegraded})
	if !defaultMandatoryYieldsTo(inferenceproxy.ProxyPolicy{RecordMandatory: true}, wrapped) {
		t.Fatal("a wrapped spool_degraded was not recognized: the rule must unwrap, or a later error-wrapping edit silently turns the yield back into a denial")
	}
}
