// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

// TestSessionLaunchGate_AResumeUnderHeadroomIsStillAllowed is the positive oracle
// of the re-evaluation at the caller that owns the stable key. This gate's key is
// the run reference, so a resume of a run asks the ledger the same question the
// launch asked — and the answer must be the ledger's, in BOTH directions. The
// refusal over a blown cap already has its test; nothing asserted that a resume
// under a cap with headroom keeps being allowed, so a module that refused every
// handle-less resume with the deny-closed 503 would have passed.
//
// It drives the real FinOps module over a real store, so the second answer comes
// from the ledger and not from a recorded call.
func TestSessionLaunchGate_AResumeUnderHeadroomIsStillAllowed(t *testing.T) {
	fin, st, tenant := openFinOpsEngine(t)
	createBudgetPolicy(t, st, tenant, "resume-cap", map[string]any{
		"dimension": "global", "period": "monthly",
		"limit_micro_usd": int64(5_000_000), "action": "block",
	})
	g := &sessionLaunchGate{
		fin:             fin,
		budgetPosture:   availabilityFailClosed,
		recordAvailable: true,
		log:             slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}
	intent := sessions.LaunchIntent{PermissionMode: "default", RunRef: "run-resumed", AgentRef: "agent-1"}

	for attempt, label := range []string{"launch", "resume"} {
		dec, err := g.Authorize(context.Background(), tenant, intent)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if !dec.Allowed {
			t.Fatalf("the %s of a run under a cap with headroom was refused: reason=%q status=%d (attempt %d)",
				label, dec.Reason, dec.DeniedStatus, attempt+1)
		}
		if dec.DeniedStatus == http.StatusServiceUnavailable {
			t.Fatalf("the %s carried the deny-closed status while the ledger answered: %+v", label, dec)
		}
	}

	report, err := fin.InspectReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if report.Active != 0 || report.Drift {
		t.Fatalf("a launch and its resume left %+v: neither holds anything anyone can settle", report)
	}
}
