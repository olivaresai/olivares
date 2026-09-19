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

	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/sessions"
)

// TestSessionLaunchGate_UnreachableLedgerIsRecordedInBothPostures pins the record a
// launch leaves when the budget ledger cannot be read. Both postures need it and for
// opposite reasons: fail-closed refuses an operator's launch and owes an explanation,
// fail-open LAUNCHES past a control that is not enforcing anything and owes a louder
// one — that is the one nobody would otherwise notice.
//
// It is ERROR in both, it names the posture, and it names what the gate did with the
// refusal, because "budget check failed" alone does not say whether the session
// started. The outcome is the field an operator greps to tell a community install
// spending uncapped from an enterprise one that stopped.
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
				// The fake answers exactly as the module does on an unreachable ledger:
				// a deny with a NIL error, which is the shape that used to break out of
				// the gate without a word.
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

// TestSessionLaunchGate_AnAllowedLaunchIsNotRecordedAsAFailure is the control
// positive for the test above: a launch the budget control actually admitted must
// leave no ERROR, or "budget check failed" stops meaning anything.
func TestSessionLaunchGate_AnAllowedLaunchIsNotRecordedAsAFailure(t *testing.T) {
	var buf bytes.Buffer
	g := &sessionLaunchGate{
		fin:             fakeBudget{chk: finops.BudgetCheck{Allowed: true}},
		budgetPosture:   availabilityFailOpen,
		recordAvailable: true,
		log:             slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{PermissionMode: "default"})
	if err != nil || !dec.Allowed {
		t.Fatalf("an admitted launch: %+v err=%v", dec, err)
	}
	if strings.Contains(buf.String(), "budget check failed") {
		t.Fatalf("an admitted launch was recorded as a budget failure:\n%s", buf.String())
	}
}
