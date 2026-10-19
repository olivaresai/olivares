// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package effectgate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// lookupBridge records which lookup Gate used. Both lookups answer an approved grant.
type lookupBridge struct{ opened []string }

func (b *lookupBridge) GateOnceForSession(context.Context, model.TenantID, string, string, string, string, string, string, string) (string, string, string, error) {
	b.opened = append(b.opened, "reusable")
	return "appr-1", governance.GateStatusApproved, "plan", nil
}

func (b *lookupBridge) GateUnspentForSession(context.Context, model.TenantID, string, string, string, string, string, string, string) (string, string, string, error) {
	b.opened = append(b.opened, "unspent")
	return "appr-2", governance.GateStatusApproved, "plan", nil
}

func (b *lookupBridge) StatusScoped(context.Context, model.TenantID, string, string, string, string, string) (string, string, error) {
	return governance.GateStatusApproved, "plan", nil
}

func (b *lookupBridge) ConsumeApproval(context.Context, model.TenantID, string, string, string) (bool, bool, error) {
	return true, false, nil
}

// A single-use spend under a fresh consumer can never match an earlier spend, so it asks for
// an unspent grant. A spend under the caller's own consumer id (a tool-call retrying inside
// the engine's retry window), an approval reused in its time box and a named approval keep
// the lookup they always had.
func TestGateAsksForAnUnspentGrantOnlyUnderAFreshConsumer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		effect   GatedEffect
		wantOpen string
	}{
		{"fresh consumer spends once", GatedEffect{}, "unspent"},
		{"its own consumer id retries", GatedEffect{Consumer: "toolu_1"}, "reusable"},
		{"an approval reused inside its time box", GatedEffect{Spend: ReuseInWindow}, "reusable"},
		{"a named approval is read, not looked up", GatedEffect{Ref: "appr-9"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &lookupBridge{}
			answer, err := Gate(context.Background(), b, tc.effect)
			if err != nil || answer.Outcome != Allowed {
				t.Fatalf("answer=%+v err=%v, want allowed", answer, err)
			}
			if len(b.opened) > 1 {
				t.Fatalf("opened %v, want at most one lookup", b.opened)
			}
			got := ""
			if len(b.opened) == 1 {
				got = b.opened[0]
			}
			if got != tc.wantOpen {
				t.Fatalf("lookup = %q, want %q", got, tc.wantOpen)
			}
		})
	}
}

// interruptedQueue ends the caller's context while Wait reads the store and fails the
// read the way SQLite does when a context interrupts its statement.
type interruptedQueue struct {
	end      func()
	canceled int
}

func (q *interruptedQueue) Request(context.Context, model.TenantID, auth.Principal, governance.ApprovalRequest) (governance.Approval, error) {
	return governance.Approval{ID: "ap-1", Status: "pending"}, nil
}

func (q *interruptedQueue) Wait(ctx context.Context, _ model.TenantID, _ string) (governance.Approval, error) {
	q.end()
	<-ctx.Done()
	return governance.Approval{}, errors.New("interrupted (9)")
}

func (q *interruptedQueue) Cancel(context.Context, model.TenantID, auth.Principal, string) (governance.Approval, error) {
	q.canceled++
	return governance.Approval{ID: "ap-1", Status: "canceled"}, nil
}

func (q *interruptedQueue) Consume(context.Context, model.TenantID, string, string, string) (governance.ApprovalConsumption, error) {
	return governance.ApprovalConsumption{}, errors.New("not reached")
}

// TestHoldKeepsTheContextCauseOfAnInterruptedWait: a wait the caller's context ended
// returns that context's cause even when the store reports its own interruption. A
// cancellation still withdraws the request; a deadline leaves it to expire.
func TestHoldKeepsTheContextCauseOfAnInterruptedWait(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cause    error
		canceled int
	}{
		{"cancel", context.Canceled, 1},
		{"deadline", context.DeadlineExceeded, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			q := &interruptedQueue{end: cancel}
			if tc.cause == context.DeadlineExceeded {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(t.Context(), time.Now().Add(20*time.Millisecond))
				defer stop()
				q.end = func() {}
			}
			answer, err := Hold(ctx, q, HeldEffect{Request: governance.ApprovalRequest{Action: "sessions.provider.approval"}})
			if !errors.Is(err, tc.cause) || answer.Failed != Wait || answer.Outcome == Allowed {
				t.Fatalf("interrupted wait = %+v %v, want failed wait with %v", answer, err, tc.cause)
			}
			if q.canceled != tc.canceled {
				t.Fatalf("withdrawals = %d, want %d", q.canceled, tc.canceled)
			}
		})
	}
}
