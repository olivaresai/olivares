// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

// #2774: sqlstore wraps a lost/refused connection or a Postgres SQLSTATE class-08
// failure in store.ErrStoreUnavailable (generic.go wrapUnavailableErr), and
// statusFor had no arm for it, so a dead database was answered 500 "internal
// error" — blamed on the server, counted against the 5xx SLI and never retried —
// when the condition is transient and retryable, exactly like ErrNotLeader.
func TestStatusFor_StoreUnavailable(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"sentinel", store.ErrStoreUnavailable},
		{"wrapped", fmt.Errorf("select orgs: %w", store.ErrStoreUnavailable)},
		// The store wraps cause-preserving multi-%w (generic.go:846); errors.Is must
		// see the sentinel through the driver's own error in the middle.
		{"cause-preserving double wrap", fmt.Errorf("%w: %w", store.ErrStoreUnavailable, fmt.Errorf("dial tcp: connection refused"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := statusFor(tc.err)
			if status != http.StatusServiceUnavailable || code != "store_unavailable" {
				t.Fatalf("statusFor(%v) = (%d, %q), want (%d, %q)",
					tc.err, status, code, http.StatusServiceUnavailable, "store_unavailable")
			}
		})
	}
}

// The commit-outcome sentinel must OUTRANK the availability sentinel. sqlstore
// wraps an unknown COMMIT as fmt.Errorf("%w: %w", ErrCommitOutcomeUnknown,
// wrapUnavailableErr(err)) (commitoutcome.go:105), so the chain carries BOTH and
// errors.Is finds both. Answering store_unavailable here would hand the client
// "the request can be retried" for a write that may already be durable — the
// exact blind re-send the sentinel exists to prevent (core/store errors.go).
// The wire shape is the one modules/sessions already answers
// (communication_http.go: 503 commit_outcome_unknown).
func TestStatusFor_CommitOutcomeUnknownOutranksStoreUnavailable(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"bare sentinel", store.ErrCommitOutcomeUnknown},
		{"real producer shape: unknown commit over a lost connection",
			fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown,
				fmt.Errorf("%w: %w", store.ErrStoreUnavailable, fmt.Errorf("dial tcp: connection reset"))),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := statusFor(tc.err)
			if status != http.StatusServiceUnavailable || code != "commit_outcome_unknown" {
				t.Fatalf("statusFor(%v) = (%d, %q), want (%d, %q)",
					tc.err, status, code, http.StatusServiceUnavailable, "commit_outcome_unknown")
			}
		})
	}
}

// The envelope must not turn the deliberate 503 back into "internal error":
// writeError's honest-seam band (status > 500) carries the curated sentence and
// no-store.
func TestWriteError_StoreUnavailableIsNotInternal(t *testing.T) {
	s := &Server{log: slog.Default()}
	rec := httptest.NewRecorder()
	s.writeError(rec, httptest.NewRequest(http.MethodGet, "/x", nil),
		fmt.Errorf("list workspaces: %w", store.ErrStoreUnavailable))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	code, msg := decodeErrorBody(t, rec)
	if code != "store_unavailable" {
		t.Fatalf("code = %q, want %q", code, "store_unavailable")
	}
	if msg == "internal error" {
		t.Fatalf("a retryable backend outage is reported as %q — the operator is told their server is broken", msg)
	}
	// The curated sentence, not the humanisedCode fallback ("Store unavailable."):
	// it tells the caller to retry, which is the whole point of answering 503.
	if want := honestSeamMessage["store_unavailable"]; msg != want {
		t.Fatalf("message = %q, want the curated sentence %q", msg, want)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store (the outage is transient; the answer must not be replayed)", got)
	}
}

// The undetermined-commit 503 tells the caller not to re-send automatically, and
// that promise is the curated sentence, so it is pinned here rather than left to
// humanisedCode's "Commit outcome unknown." fallback.
func TestWriteError_CommitOutcomeUnknownForbidsAutomaticRetry(t *testing.T) {
	s := &Server{log: slog.Default()}
	rec := httptest.NewRecorder()
	s.writeError(rec, httptest.NewRequest(http.MethodPost, "/x", nil),
		fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown,
			fmt.Errorf("%w: %w", store.ErrStoreUnavailable, fmt.Errorf("dial tcp: connection reset"))))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	code, msg := decodeErrorBody(t, rec)
	if code != "commit_outcome_unknown" {
		t.Fatalf("code = %q, want %q", code, "commit_outcome_unknown")
	}
	if want := honestSeamMessage["commit_outcome_unknown"]; msg != want {
		t.Fatalf("message = %q, want the curated sentence %q", msg, want)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

// A sign-in withdrawn because its asserted groups could not be stored is a retryable 503
// with a fixed sentence: a 2xx would read as a success, and the store's own error text
// must not reach the person signing in even when something wraps it into the error.
func TestWriteError_SSOGroupReconcileFailureIsAFixedRetryable503(t *testing.T) {
	s := &Server{log: slog.Default()}
	rec := httptest.NewRecorder()
	cause := fmt.Errorf("%w: store detail that must stay in the log", auth.ErrSSOGroupReconcile)
	s.writeError(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/federation/callback", nil), cause)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	code, msg := decodeErrorBody(t, rec)
	if code != "sso_group_reconcile_failed" {
		t.Fatalf("code = %q, want sso_group_reconcile_failed", code)
	}
	if want := honestSeamMessage["sso_group_reconcile_failed"]; msg != want {
		t.Fatalf("message = %q, want the curated sentence %q", msg, want)
	}
	if strings.Contains(msg, "store detail") || !strings.Contains(msg, "Try again") {
		t.Fatalf("message = %q, want the fixed retry sentence without the wrapped cause", msg)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

// A client that hangs up mid-commit keeps the 408 it always had. The older
// context.Canceled arm answers first, and the two store arms only take errors that
// used to fall through to the 500 default, so no existing status moves.
func TestStatusFor_CanceledChainKeepsItsAnswer(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"canceled over an unknown commit", fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown, context.Canceled)},
		{"canceled over a lost connection", fmt.Errorf("%w: %w", store.ErrStoreUnavailable, context.Canceled)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStatus, code := statusFor(tc.err)
			if gotStatus != http.StatusRequestTimeout || code != "request_abandoned" {
				t.Fatalf("statusFor(%v) = (%d, %q), want (%d, %q)",
					tc.err, gotStatus, code, http.StatusRequestTimeout, "request_abandoned")
			}
		})
	}
}

// gRPC clients and mesh retry policies re-send on Unavailable. An undetermined
// commit must not reach them as Unavailable, while a plain backend outage still
// does, because it is safe to retry.
func TestGRPCError_CommitOutcomeUnknownIsNotRetried(t *testing.T) {
	if got := status.Code(grpcError(fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown, store.ErrStoreUnavailable))); got != codes.Unknown {
		t.Fatalf("grpc code = %v, want %v for an undetermined commit", got, codes.Unknown)
	}
	if got := status.Code(grpcError(fmt.Errorf("select: %w", store.ErrStoreUnavailable))); got != codes.Unavailable {
		t.Fatalf("grpc code = %v, want %v for a retryable backend outage", got, codes.Unavailable)
	}
}
