// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// The one-effect approval module through its interface: the bridge shape with
// the in-memory pinBridge, the queue shape with the in-memory effectFakeQueue.

func TestEffectApprovalGateSpendsOnceAndReusesOnlyInItsWindow(t *testing.T) {
	broken := errors.New("bridge broke")
	effect := gatedEffect{Tenant: "t", Action: "mcp.tool.call", SubjectKind: "tool", SubjectRef: "db.drop", PlanHash: "plan", Consumer: "round-trip-1", PolicyVersion: "v1"}
	for _, tc := range []struct {
		name    string
		bridge  *pinBridge
		spend   approvalSpend
		named   string
		outcome effectOutcome
		spent   bool
		failed  effectStep
		spends  string
	}{
		{"round trip spends", &pinBridge{ref: "appr-1", status: nbApproved, bound: "plan", granted: true}, spendOnce, "", effectAllowed, true, 0, "appr-1|round-trip-1|v1"},
		{"legacy retry reuses unspent", &pinBridge{ref: "appr-1", status: nbApproved, bound: "plan", granted: true}, reuseInWindow, "", effectAllowed, false, 0, ""},
		{"replay", &pinBridge{ref: "appr-1", status: nbApproved, bound: "plan", replay: true}, spendOnce, "", effectReplay, false, 0, "appr-1|round-trip-1|v1"},
		{"no longer spendable", &pinBridge{ref: "appr-1", status: nbApproved, bound: "plan"}, spendOnce, "", effectUnspendable, false, 0, "appr-1|round-trip-1|v1"},
		{"break-glass is spent", &pinBridge{ref: "breakglass:g", status: nbBreakGlass, bound: "plan"}, spendOnce, "", effectAllowed, true, 0, ""},
		{"break-glass reused unspent", &pinBridge{ref: "breakglass:g", status: nbBreakGlass, bound: "plan"}, reuseInWindow, "", effectAllowed, false, 0, ""},
		{"pending", &pinBridge{ref: "appr-1", status: nbPending, bound: "plan"}, spendOnce, "", effectPending, false, 0, ""},
		{"rejected", &pinBridge{ref: "appr-1", status: nbRejected, bound: "plan"}, spendOnce, "", effectRefused, false, 0, ""},
		{"no gate", &pinBridge{ref: "no-gate:plan", status: nbNoGate, bound: "plan"}, reuseInWindow, "", effectRefused, false, 0, ""},
		{"open error", &pinBridge{openErr: broken}, spendOnce, "", effectRefused, false, stepOpen, ""},
		{"spend error", &pinBridge{ref: "appr-1", status: nbApproved, bound: "plan", spendErr: broken}, spendOnce, "", effectRefused, false, stepSpend, "appr-1|round-trip-1|v1"},
		{"named approval", &pinBridge{status: nbApproved, bound: "plan", granted: true}, spendOnce, "appr-held", effectAllowed, true, 0, "appr-held|round-trip-1|v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := effect
			e.Spend, e.Ref = tc.spend, tc.named
			a, err := gateEffect(context.Background(), tc.bridge, e)
			if a.Outcome != tc.outcome || a.Spent != tc.spent || a.Failed != tc.failed || (err != nil) != (tc.failed != 0) {
				t.Fatalf("answer = %+v err=%v", a, err)
			}
			if tc.failed != 0 && !errors.Is(err, broken) {
				t.Fatalf("the bridge error was lost: %v", err)
			}
			if tc.failed != stepOpen && (a.Status != tc.bridge.status || a.BoundHash != tc.bridge.bound) {
				t.Fatalf("status %q bound %q, want the bridge's %q %q", a.Status, a.BoundHash, tc.bridge.status, tc.bridge.bound)
			}
			if tc.named != "" && (len(tc.bridge.opened) != 0 || strings.Join(tc.bridge.scoped, ",") != "appr-held|plan|mcp.tool.call|tool|db.drop" || a.Ref != "appr-held") {
				t.Fatalf("a named approval must be read scope-checked, not opened: opened=%v scoped=%v ref=%q", tc.bridge.opened, tc.bridge.scoped, a.Ref)
			}
			assertPinSpend(t, tc.bridge.spent, tc.spends)
		})
	}
}

// Without a consumer, every spend gets its own fresh nonce: strict single use.
func TestEffectApprovalGateMintsAFreshConsumer(t *testing.T) {
	first, second := &pinBridge{ref: "appr-1", status: nbApproved, granted: true}, &pinBridge{ref: "appr-1", status: nbApproved, granted: true}
	e := gatedEffect{Tenant: "t", Action: "a", SubjectKind: "k", SubjectRef: "s", PlanHash: "p"}
	for _, b := range []*pinBridge{first, second} {
		if a, err := gateEffect(context.Background(), b, e); err != nil || a.Outcome != effectAllowed || !a.Spent {
			t.Fatalf("answer = %+v err=%v", a, err)
		}
		assertPinSpend(t, b.spent, "nonce|")
	}
	if first.spent[0] == second.spent[0] {
		t.Fatalf("two spends shared one consumer: %v", first.spent)
	}
}

// effectFakeQueue is an in-memory engine approval queue. Wait answers status for
// the request it was asked about (mutated by answer), or blocks until ctx ends.
// Cancel, like the engine's, does nothing under a context that already ended.
type effectFakeQueue struct {
	requestErr, waitErr, consumeErr error
	expiresAt                       string
	status                          string
	block                           bool
	answer                          func(*governance.Approval)
	consume                         governance.ApprovalConsumption

	asked              governance.ApprovalRequest
	canceled, consumed []string
}

func (q *effectFakeQueue) Request(_ context.Context, _ model.TenantID, _ auth.Principal, in governance.ApprovalRequest) (governance.Approval, error) {
	q.asked = in
	if q.requestErr != nil {
		return governance.Approval{}, q.requestErr
	}
	return governance.Approval{ID: "appr-q", Status: nbPending, Reason: in.Reason, ExpiresAt: q.expiresAt}, nil
}

func (q *effectFakeQueue) Wait(ctx context.Context, _ model.TenantID, ref string) (governance.Approval, error) {
	if q.block {
		<-ctx.Done()
		return governance.Approval{}, ctx.Err()
	}
	if q.waitErr != nil {
		return governance.Approval{}, q.waitErr
	}
	answer := governance.Approval{ID: ref, Status: q.status, SessionRef: q.asked.SessionRef, Action: q.asked.Action, SubjectKind: q.asked.SubjectKind, SubjectRef: q.asked.SubjectRef}
	if q.answer != nil {
		q.answer(&answer)
	}
	return answer, nil
}

func (q *effectFakeQueue) Cancel(ctx context.Context, _ model.TenantID, _ auth.Principal, ref string) (governance.Approval, error) {
	if err := ctx.Err(); err != nil {
		return governance.Approval{}, err
	}
	q.canceled = append(q.canceled, ref)
	return governance.Approval{ID: ref, Status: nbCanceled}, nil
}

func (q *effectFakeQueue) Consume(_ context.Context, _ model.TenantID, ref, consumerID, policyVersion string) (governance.ApprovalConsumption, error) {
	q.consumed = append(q.consumed, ref+"|"+consumerID+"|"+policyVersion)
	return q.consume, q.consumeErr
}

func TestEffectApprovalHoldOutcomes(t *testing.T) {
	broken := errors.New("queue broke")
	recheckErr := errors.New("authority changed")
	granted := governance.ApprovalConsumption{Granted: true}
	for _, tc := range []struct {
		name      string
		queue     *effectFakeQueue
		recheck   error
		shown     bool
		outcome   effectOutcome
		failed    effectStep
		canceled  bool // withdrawn: the call stopped waiting without an answer
		rechecked bool
		spends    bool
	}{
		{"approved is spent", &effectFakeQueue{status: nbApproved, consume: granted}, nil, true, effectAllowed, 0, false, true, true},
		{"replay", &effectFakeQueue{status: nbApproved, consume: governance.ApprovalConsumption{Replay: true}}, nil, true, effectReplay, 0, false, true, true},
		{"no longer spendable", &effectFakeQueue{status: nbApproved}, nil, true, effectUnspendable, 0, false, true, true},
		{"rejected", &effectFakeQueue{status: nbRejected}, nil, true, effectRefused, 0, false, false, false},
		{"answer for another session", &effectFakeQueue{status: nbApproved, consume: granted, answer: func(a *governance.Approval) { a.SessionRef = "other" }}, nil, true, effectForeign, 0, false, false, false},
		{"answer for another action", &effectFakeQueue{status: nbApproved, consume: granted, answer: func(a *governance.Approval) { a.Action = "other" }}, nil, true, effectForeign, 0, false, false, false},
		{"answer for another subject kind", &effectFakeQueue{status: nbApproved, consume: granted, answer: func(a *governance.Approval) { a.SubjectKind = "other" }}, nil, true, effectForeign, 0, false, false, false},
		{"answer for another subject", &effectFakeQueue{status: nbApproved, consume: granted, answer: func(a *governance.Approval) { a.SubjectRef += "x" }}, nil, true, effectForeign, 0, false, false, false},
		{"recheck fails after the wait", &effectFakeQueue{status: nbApproved, consume: granted}, recheckErr, true, effectRefused, stepRecheck, false, true, false},
		{"queued request not shown as reviewed", &effectFakeQueue{status: nbApproved, consume: granted}, nil, false, effectUnshown, 0, true, false, false},
		{"request error", &effectFakeQueue{requestErr: broken}, nil, true, effectRefused, stepOpen, false, false, false},
		{"read error withdraws", &effectFakeQueue{waitErr: broken}, nil, true, effectRefused, stepWait, true, false, false},
		{"spend error", &effectFakeQueue{status: nbApproved, consumeErr: broken}, nil, true, effectRefused, stepSpend, false, true, true},
		{"unreadable policy expiry withdraws", &effectFakeQueue{status: nbApproved, consume: granted, expiresAt: "tomorrow"}, nil, true, effectRefused, stepRegister, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			rechecks := 0
			e := effectHold(tc.shown)
			e.Recheck = func(context.Context) error { rechecks++; return tc.recheck }
			a, err := holdEffect(ctx, tc.queue, e)
			if a.Outcome != tc.outcome || a.Failed != tc.failed || (err != nil) != (tc.failed != 0) || a.Spent != (tc.outcome == effectAllowed) {
				t.Fatalf("answer = %+v err=%v", a, err)
			}
			if tc.failed == stepRecheck && !errors.Is(err, recheckErr) || tc.failed == stepWait && !errors.Is(err, broken) {
				t.Fatalf("the cause was lost: %v", err)
			}
			wantStatus, wantRef := tc.queue.status, "appr-q"
			switch {
			case tc.failed == stepOpen:
				wantStatus, wantRef = "", ""
			case tc.canceled:
				wantStatus = nbPending
			}
			if a.Status != wantStatus || a.Ref != wantRef {
				t.Fatalf("status %q ref %q, want %q %q", a.Status, a.Ref, wantStatus, wantRef)
			}
			if got := len(tc.queue.canceled) == 1; got != tc.canceled {
				t.Fatalf("withdrawn = %v, want %v", tc.queue.canceled, tc.canceled)
			}
			if (rechecks == 1) != tc.rechecked || rechecks > 1 {
				t.Fatalf("rechecked %d times, want %v", rechecks, tc.rechecked)
			}
			want := ""
			if tc.spends {
				want = "appr-q|consumer-1|v2"
			}
			if got := strings.Join(tc.queue.consumed, ","); got != want {
				t.Fatalf("spent %q, want %q", got, want)
			}
		})
	}
}

// effectHold is a held effect whose subject carries its consumer.
func effectHold(shown bool) heldEffect {
	return heldEffect{
		Tenant: "t", Principal: auth.Principal{SessionIdentity: "sid"},
		Request:  governance.ApprovalRequest{SessionRef: "sid", Action: "mcp.tool.call", SubjectKind: "tool", SubjectRef: "run#server:plan:consumer-1", Reason: "review me"},
		Consumer: "consumer-1", PolicyVersion: "v2",
		Wait:  func(context.Context, auth.Principal, string, time.Time) (func(), error) { return func() {}, nil },
		Shown: func(a governance.Approval) bool { return shown && a.Reason == "review me" },
	}
}

// An interrupted wait withdraws the request, under a context detached from the
// interrupt, and keeps the cause. At the wait deadline the engine's Wait records
// the expiry, so nothing is withdrawn.
func TestEffectApprovalHoldWithdrawsAnInterruptedRequest(t *testing.T) {
	for _, interrupt := range []bool{true, false} {
		q := &effectFakeQueue{block: true}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		if interrupt {
			cancel()
		}
		a, err := holdEffect(ctx, q, effectHold(true))
		cancel()
		want, withdrawn := error(context.DeadlineExceeded), ""
		if interrupt {
			want, withdrawn = context.Canceled, "appr-q"
		}
		if a.Failed != stepWait || !errors.Is(err, want) || a.Outcome != effectRefused {
			t.Fatalf("interrupt=%v: answer = %+v err=%v", interrupt, a, err)
		}
		if strings.Join(q.canceled, ",") != withdrawn || len(q.consumed) != 0 {
			t.Fatalf("interrupt=%v: canceled=%v consumed=%v", interrupt, q.canceled, q.consumed)
		}
	}
}

// The wait projected onto the run ends at the earlier of the live deadline and
// the request's policy expiry; a hold bounded by neither is refused.
func TestEffectApprovalHoldClampsItsWaitToThePolicyExpiry(t *testing.T) {
	soon := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	later := soon.Add(time.Hour)
	for _, tc := range []struct {
		name      string
		deadline  time.Time // zero: no context deadline
		expiresAt string
		want      time.Time // zero: refused
	}{
		{"policy expiry first", later, model.NewTimestamp(soon).String(), soon},
		{"deadline first", soon, model.NewTimestamp(later).String(), soon},
		{"no policy expiry", soon, "", soon},
		{"no deadline", time.Time{}, model.NewTimestamp(soon).String(), soon},
		{"neither", time.Time{}, "", time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.Background(), context.CancelFunc(func() {})
			if !tc.deadline.IsZero() {
				ctx, cancel = context.WithDeadline(ctx, tc.deadline)
			}
			defer cancel()
			q := &effectFakeQueue{status: nbApproved, consume: governance.ApprovalConsumption{Granted: true}, expiresAt: tc.expiresAt}
			var got time.Time
			ended := 0
			e := effectHold(true)
			e.Wait = func(_ context.Context, p auth.Principal, ref string, until time.Time) (func(), error) {
				if p.SessionIdentity != "sid" || ref != "appr-q" {
					t.Fatalf("wait registered for %q %q", p.SessionIdentity, ref)
				}
				got = until
				return func() { ended++ }, nil
			}
			a, err := holdEffect(ctx, q, e)
			if tc.want.IsZero() {
				if a.Failed != stepRegister || err == nil || len(q.canceled) != 1 {
					t.Fatalf("an unbounded hold was not refused: %+v err=%v canceled=%v", a, err, q.canceled)
				}
				return
			}
			if err != nil || a.Outcome != effectAllowed || !got.Equal(tc.want) || ended != 1 {
				t.Fatalf("answer = %+v err=%v until=%v want=%v ended=%d", a, err, got, tc.want, ended)
			}
		})
	}
}
