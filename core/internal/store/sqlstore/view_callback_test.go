// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// TestViewCallbackErrorsKeepTheirOwnClassification pins that a View callback's
// own error is not reclassified as backend unavailability (the regression review).
// view() wraps only the SQL-boundary errors (BeginTx, BindTenant): a caller
// deadline or a timeout from a call the callback makes is a caller fault, not a
// 503 for the database, and must reach the caller with its chain intact.
func TestViewCallbackErrorsKeepTheirOwnClassification(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "view-callback")

	cases := []struct {
		name string
		err  error
	}{
		{"context deadline", context.DeadlineExceeded},
		{"timeout net.Error", &net.OpError{Op: "read", Err: timeoutErr{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := st.View(context.Background(), tenant, func(store.Scope) error {
				return tc.err
			})
			if errors.Is(err, store.ErrStoreUnavailable) {
				t.Fatalf("View callback error %v was reclassified as ErrStoreUnavailable: %v", tc.err, err)
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("View lost the callback error chain: got %v, want %v", err, tc.err)
			}
		})
	}
}

// timeoutErr is a net.Error that reports a timeout, the shape of a callback's
// own network call that ran out of time.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// TestViewBeginDeadlineIsACallerFault pins that a caller whose own deadline has
// passed before View starts its transaction gets the deadline back, not
// store.ErrStoreUnavailable. database/sql returns ctx.Err() from BeginTx, and
// context.DeadlineExceeded satisfies net.Error, so without the caller-context
// check in viewBeginErr the wrap would report a caller fault as a database outage.
func TestViewBeginDeadlineIsACallerFault(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "view-begin-deadline")

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	err := st.View(ctx, tenant, func(store.Scope) error {
		t.Fatal("View ran its callback after the caller's deadline had passed")
		return nil
	})
	if errors.Is(err, store.ErrStoreUnavailable) {
		t.Fatalf("expired caller context reported as ErrStoreUnavailable: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("View lost the caller's deadline: got %v, want context.DeadlineExceeded", err)
	}
}
