// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// launchKeyOf matches the admission key of a launch of run "run-resumed": the run
// reference, then a digest of the launch's dimensions.
var launchKeyOf = regexp.MustCompile(`^session_launch/run-resumed/[0-9a-f]{64}$`)

// TestSessionLaunchGate_AResumeUnderHeadroomIsStillAllowed is the positive oracle of the
// re-evaluation at the caller that owns a stable key. This gate's key is the run
// reference with a digest of the launch's dimensions, so the resume of a run with the same
// dimensions asks the ledger the question the launch asked, and the answer must be the
// ledger's in BOTH directions. The refusal over a blown cap has its
// own test; this one asserts that a resume under a cap with headroom keeps being allowed,
// so a module that refused every hold-free resume with the deny-closed 503 fails here.
//
// It drives the real module on both engines, so the second answer comes from the ledger
// and not from a recorded call. The run keeps one admission row, published with no hold.
func TestSessionLaunchGate_AResumeUnderHeadroomIsStillAllowed(t *testing.T) {
	forEachFinOpsEngine(t, func(t *testing.T, cfg store.Config) {
		fin, st, tenant := openFinOpsEngineOn(t, cfg)
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

		if rows := listFinOpsRows(t, st, tenant, finopsReservationKind); len(rows) != 0 {
			t.Fatalf("a launch and its resume left %d reservation row(s): neither holds anything anyone can settle", len(rows))
		}
		adm := listFinOpsRows(t, st, tenant, finopsAdmissionKind)
		if len(adm) != 1 {
			t.Fatalf("admission rows = %d, want the run's one row: the resume did not reuse the run's key", len(adm))
		}
		if r := adm[0]; !launchKeyOf.MatchString(r.String("idempotency_key")) ||
			r.String("state") != "reserved" || r.String("handle") != "" {
			t.Fatalf("the run's admission row = key %q state %q handle %q, want the run's key with its dimensions' digest, reserved, no hold",
				r.String("idempotency_key"), r.String("state"), r.String("handle"))
		}
	})
}

// TestSessionLaunchGate_AResumeWithOtherDimsIsEvaluatedAfresh: a resume can carry other
// dimensions than its launch, since it takes the agent identity of the principal that
// resumes it and the model its template resolves to now. Such a resume is a new question,
// and the ledger answers it: under headroom it is allowed, and over the cap of its own
// agent it is refused 402. Neither answer is an unreachable ledger, so neither is recorded
// as one, whatever the posture.
func TestSessionLaunchGate_AResumeWithOtherDimsIsEvaluatedAfresh(t *testing.T) {
	forEachFinOpsEngine(t, func(t *testing.T, cfg store.Config) {
		fin, st, tenant := openFinOpsEngineOn(t, cfg)
		createBudgetPolicy(t, st, tenant, "resume-cap", map[string]any{
			"dimension": "global", "period": "monthly",
			"limit_micro_usd": int64(5_000_000), "action": "block",
		})
		// Committed capacity past the limit: every launch of this agent is over its cap.
		createBudgetPolicy(t, st, tenant, "capped-agent", map[string]any{
			"dimension": "agent", "key": "agent-capped", "period": "monthly",
			"limit_micro_usd": int64(1_000_000), "reserved_micro_usd": int64(2_000_000), "action": "block",
		})
		for _, posture := range []availabilityPosture{availabilityFailOpen, availabilityFailClosed} {
			for _, leg := range []struct {
				name        string
				resumeAgent string
				wantAllowed bool
				wantStatus  int
			}{
				{name: "another agent under headroom", resumeAgent: "agent-other", wantAllowed: true},
				{name: "another agent over its cap", resumeAgent: "agent-capped", wantAllowed: false, wantStatus: http.StatusPaymentRequired},
			} {
				t.Run(posture.String()+"/"+leg.name, func(t *testing.T) {
					var buf bytes.Buffer
					g := &sessionLaunchGate{
						fin:             fin,
						budgetPosture:   posture,
						recordAvailable: true,
						log:             slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
					}
					run := "run-" + posture.String() + "-" + leg.resumeAgent
					launch := sessions.LaunchIntent{PermissionMode: "default", RunRef: run, AgentRef: "agent-1"}
					if dec, err := g.Authorize(context.Background(), tenant, launch); err != nil || !dec.Allowed {
						t.Fatalf("the launch: %+v err=%v", dec, err)
					}
					resume := launch
					resume.AgentRef = leg.resumeAgent
					dec, err := g.Authorize(context.Background(), tenant, resume)
					if err != nil {
						t.Fatalf("the resume: %v", err)
					}
					if dec.Allowed != leg.wantAllowed {
						t.Fatalf("the resume as %s: Allowed = %v (status %d, reason %q), want %v",
							leg.resumeAgent, dec.Allowed, dec.DeniedStatus, dec.Reason, leg.wantAllowed)
					}
					if !leg.wantAllowed && dec.DeniedStatus != leg.wantStatus {
						t.Fatalf("the resume as %s was refused %d (%q), want %d", leg.resumeAgent, dec.DeniedStatus, dec.Reason, leg.wantStatus)
					}
					if strings.Contains(buf.String(), "budget check failed") {
						t.Fatalf("an evaluated resume was recorded as an unreachable budget control:\n%s", buf.String())
					}
					var keys []string
					for _, r := range listFinOpsRows(t, st, tenant, finopsAdmissionKind) {
						if k := r.String("idempotency_key"); strings.HasPrefix(k, "session_launch/"+run+"/") {
							keys = append(keys, k)
						}
					}
					if len(keys) != 2 || keys[0] == keys[1] {
						t.Fatalf("the run's admission keys = %v, want one for the launch and one for the resume with other dimensions", keys)
					}
				})
			}
		}
		if rows := listFinOpsRows(t, st, tenant, finopsReservationKind); len(rows) != 0 {
			t.Fatalf("launches and resumes left %d reservation row(s): none holds anything anyone can settle", len(rows))
		}
	})
}
