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
