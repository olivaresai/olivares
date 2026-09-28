// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/sessions"
)

// TestSessionLaunchGate_UnreachableLedgerIsRecordedInBothPostures pins the record a launch
// leaves when the budget ledger cannot be read. Both postures need it, for opposite
// reasons: fail-closed refuses an operator's launch and owes an explanation; fail-open
// LAUNCHES past a control that is not enforcing anything and owes a louder one, because
// nobody would otherwise notice.
//
// It is ERROR in both, it names the posture, and it names what the gate did with the
// refusal, because "budget check failed" alone does not say whether the session started.
// The outcome is the field an operator searches to tell an install spending uncapped from
// one that stopped.
func TestSessionLaunchGate_UnreachableLedgerIsRecordedInBothPostures(t *testing.T) {
	cases := []struct {
		posture     availabilityPosture
		wantAllowed bool
		wantOutcome string
		wantStatus  int
	}{
		{posture: availabilityFailOpen, wantAllowed: true, wantOutcome: "launched"},
		{posture: availabilityFailClosed, wantAllowed: false, wantOutcome: "refused", wantStatus: http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		t.Run(c.posture.String(), func(t *testing.T) {
			var buf bytes.Buffer
			g := &sessionLaunchGate{
				// The fake answers exactly as the module does on an unreachable ledger
				// under the deny posture: a refusal with a NIL error, the shape that
				// otherwise leaves the gate without a word.
				fin:             fakeBudget{err: errors.New("dial budget store: connection refused")},
				budgetPosture:   c.posture,
				recordAvailable: true,
				log:             slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
			}

			dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{PermissionMode: "default"})
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if dec.Allowed != c.wantAllowed {
				t.Fatalf("Allowed = %v under %s, want %v", dec.Allowed, c.posture, c.wantAllowed)
			}
			if c.wantStatus != 0 && dec.DeniedStatus != c.wantStatus {
				t.Fatalf("DeniedStatus = %d, want %d", dec.DeniedStatus, c.wantStatus)
			}

			out := buf.String()
			if out == "" {
				t.Fatalf("%s launch over an unreachable ledger left 0 log bytes", c.posture)
			}
			for _, want := range []string{
				"level=ERROR",
				"session launch-gate: budget check failed",
				"posture=" + c.posture.String(),
				"outcome=" + c.wantOutcome,
			} {
				if !strings.Contains(out, want) {
					t.Fatalf("the record does not carry %q; got:\n%s", want, out)
				}
			}
		})
	}
}

// TestSessionLaunchGate_AnAllowedLaunchIsNotRecordedAsAFailure is the positive control of
// the test above: a launch the budget control admitted leaves no failure record, or
// "budget check failed" stops meaning anything.
func TestSessionLaunchGate_AnAllowedLaunchIsNotRecordedAsAFailure(t *testing.T) {
	var buf bytes.Buffer
	spy := &spyAdmissionBudget{chk: finops.BudgetCheck{Allowed: true}}
	g := &sessionLaunchGate{
		fin:             spy,
		budgetPosture:   availabilityFailOpen,
		recordAvailable: true,
		log:             slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{PermissionMode: "default"})
	if err != nil || !dec.Allowed {
		t.Fatalf("an admitted launch: %+v err=%v", dec, err)
	}
	if len(spy.seen) != 1 {
		t.Fatalf("Reserve called %d time(s), want exactly 1: the launch was not admitted by the ledger", len(spy.seen))
	}
	if strings.Contains(buf.String(), "budget check failed") {
		t.Fatalf("an admitted launch was recorded as a budget failure:\n%s", buf.String())
	}
}

// TestSessionLaunchGate_AnAdmissionErrorIsNotTheUnreachableOutcome: the availability
// posture answers one outcome only, a ledger admission could not reach. An error from
// admission itself (a request it refused to evaluate) is not that outcome, so even the
// fail-open posture does not launch past it: the launch is refused deny-closed.
func TestSessionLaunchGate_AnAdmissionErrorIsNotTheUnreachableOutcome(t *testing.T) {
	for _, posture := range []availabilityPosture{availabilityFailOpen, availabilityFailClosed} {
		t.Run(posture.String(), func(t *testing.T) {
			g := &sessionLaunchGate{
				fin:             &spyAdmissionBudget{reserveErr: finops.ErrAdmissionConflict},
				budgetPosture:   posture,
				recordAvailable: true,
				log:             slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
			}
			dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{PermissionMode: "default", RunRef: "run-1"})
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if dec.Allowed || dec.DeniedStatus != http.StatusServiceUnavailable {
				t.Fatalf("a launch admission could not answer = %+v under %s, want refused 503", dec, posture)
			}
		})
	}
}

// TestSessionLaunchGate_AnIntegrityRefusalIsA503InEveryPosture: admission refuses a key whose
// row failed its integrity check in every posture, because the ledger answered. The launch
// gate shows that refusal as what it is, a 503 with admission's reason, never as a budget
// cap reached, and never as an unreachable ledger the posture could launch past.
func TestSessionLaunchGate_AnIntegrityRefusalIsA503InEveryPosture(t *testing.T) {
	for _, posture := range []availabilityPosture{availabilityFailOpen, availabilityFailClosed} {
		t.Run(posture.String(), func(t *testing.T) {
			var buf bytes.Buffer
			g := &sessionLaunchGate{
				fin: fakeBudget{chk: finops.BudgetCheck{
					Allowed: false, Action: "block", Reason: finops.ReasonAdmissionIntegrity,
				}},
				budgetPosture:   posture,
				recordAvailable: true,
				log:             slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
			}
			dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{PermissionMode: "default", RunRef: "run-1"})
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if dec.Allowed || dec.DeniedStatus != http.StatusServiceUnavailable || dec.Reason != finops.ReasonAdmissionIntegrity {
				t.Fatalf("an integrity refusal under %s = %+v, want refused 503 %q", posture, dec, finops.ReasonAdmissionIntegrity)
			}
			if strings.Contains(buf.String(), "budget check failed") {
				t.Fatalf("an integrity refusal was recorded as an unreachable budget control:\n%s", buf.String())
			}
		})
	}
}

// TestSessionLaunchGate_RecordsTheClassOfAnAdmissionError: the launch record names why the
// budget control did not answer. A store fault is the ledger being unreachable: it is
// recorded as such and the posture decides the launch. Any other error from admission is
// recorded as an admission failure, with its error, and refuses the launch; it is never
// reported as an unreachable ledger.
func TestSessionLaunchGate_RecordsTheClassOfAnAdmissionError(t *testing.T) {
	storeFault := errors.Join(errors.New("dial budget store: connection refused"), store.ErrStoreUnavailable)
	cases := []struct {
		name        string
		cause       error
		posture     availabilityPosture
		wantAllowed bool
		wantReason  string
		wantOutcome string
	}{
		{"store fault, fail-open", storeFault, availabilityFailOpen, true, finops.ReasonStoreUnreachable, "launched"},
		{"store fault, fail-closed", storeFault, availabilityFailClosed, false, finops.ReasonStoreUnreachable, "refused"},
		{"conflict, fail-open", finops.ErrAdmissionConflict, availabilityFailOpen, false, "admission failed", "refused"},
		{"invalid request, fail-closed", finops.ErrInvalidAdmission, availabilityFailClosed, false, "admission failed", "refused"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			g := &sessionLaunchGate{
				fin:             &spyAdmissionBudget{reserveErr: c.cause},
				budgetPosture:   c.posture,
				recordAvailable: true,
				log:             slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
			}
			dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{PermissionMode: "default", RunRef: "run-1"})
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if dec.Allowed != c.wantAllowed {
				t.Fatalf("Allowed = %v (%+v), want %v", dec.Allowed, dec, c.wantAllowed)
			}
			out := buf.String()
			for _, want := range []string{"level=ERROR", "reason=\"" + c.wantReason, "outcome=" + c.wantOutcome, "err="} {
				if !strings.Contains(out, want) {
					t.Fatalf("the record does not carry %q; got:\n%s", want, out)
				}
			}
			if c.wantReason != finops.ReasonStoreUnreachable && strings.Contains(out, finops.ReasonStoreUnreachable) {
				t.Fatalf("an admission error was recorded as an unreachable ledger:\n%s", out)
			}
		})
	}
}
