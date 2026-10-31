// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/store"
)

// TestWrapBeginErrClassifiesPoolReset pins which failures to start a store
// transaction are store.ErrStoreUnavailable. The first request after
// a pool reset fails here with a closed-connection error or a 57Pxx code; the
// request after it runs on a fresh connection. Statement-level errors stay
// write faults.
func TestWrapBeginErrClassifiesPoolReset(t *testing.T) {
	terminated := &pgconn.PgError{Code: "57P01", Severity: "FATAL", Message: "terminating connection due to administrator command"}
	unavailable := []error{
		pgconn.ErrConnClosed,
		fmt.Errorf("begin: %w", pgconn.ErrConnClosed),
		terminated,
		fmt.Errorf("begin: %w", terminated),
		&pgconn.PgError{Code: "57P02", Message: "terminating connection due to crash of server process"},
		&pgconn.PgError{Code: "57P03", Message: "cannot connect now"},
		&pgconn.PgError{Code: "57P05", Message: "terminating connection due to idle-session timeout"},
		&pgconn.PgError{Code: "08006", Message: "connection failure"},
		driver.ErrBadConn,
	}
	for _, e := range unavailable {
		err := wrapBeginErr(e)
		if !errors.Is(err, store.ErrStoreUnavailable) {
			t.Errorf("wrapBeginErr(%v) = %v, want store.ErrStoreUnavailable", e, err)
		}
		if !errors.Is(err, e) {
			t.Errorf("wrapBeginErr(%v) = %v, lost the original error in its chain", e, err)
		}
	}

	notUnavailable := []error{
		&pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"},
		&pgconn.PgError{Code: "57P04", Message: "cannot connect to database that has been dropped"},
		&pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"},
		&pgconn.PgError{Code: "42501", Message: "permission denied for table agents"},
		errors.New("boom"),
	}
	for _, e := range notUnavailable {
		if err := wrapBeginErr(e); errors.Is(err, store.ErrStoreUnavailable) {
			t.Errorf("wrapBeginErr(%v) = %v, want the error unwrapped", e, err)
		}
	}

	if err := wrapBeginErr(nil); err != nil {
		t.Errorf("wrapBeginErr(nil) = %v, want nil", err)
	}
}

// An error that already carries the sentinel is not wrapped a second time: the
// message would repeat "store backend unavailable" and the chain would grow.
func TestWrapBeginErrDoesNotWrapTwice(t *testing.T) {
	once := wrapBeginErr(pgconn.ErrConnClosed)
	twice := wrapBeginErr(once)
	if twice.Error() != once.Error() {
		t.Errorf("wrapBeginErr of an already wrapped error = %q, want it unchanged (%q)", twice, once)
	}
}

// viewBeginErr is the begin classifier of View, System, the audit reader and
// rollout transitions. Once the caller's own context is done, database/sql
// returns that context's error (or the pool's), which is a caller fault and not
// an outage, whatever shape the error has. A live context takes wrapBeginErr.
func TestViewBeginErrKeepsADoneContextUnwrapped(t *testing.T) {
	poolReset := fmt.Errorf("begin: %w", pgconn.ErrConnClosed)

	if err := viewBeginErr(t.Context(), poolReset); !errors.Is(err, store.ErrStoreUnavailable) {
		t.Errorf("live context: viewBeginErr = %v, want store.ErrStoreUnavailable", err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	for name, ctx := range map[string]context.Context{"canceled": canceled, "deadline exceeded": expired} {
		for _, e := range []error{context.Canceled, context.DeadlineExceeded, poolReset} {
			if err := viewBeginErr(ctx, e); errors.Is(err, store.ErrStoreUnavailable) {
				t.Errorf("%s context: viewBeginErr(%v) = %v, want the caller's error unwrapped", name, e, err)
			}
		}
	}
}
