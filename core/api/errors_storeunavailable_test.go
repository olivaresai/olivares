// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"bytes"
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

// the regression: store.ErrStoreUnavailable is the database being unreachable right
// now, and the answer is a retryable 503, never the default 500 "internal" and
// never an echo of the driver's text. The sentinel arrives wrapped, with the
// driver's own error behind it, so the wrapped shape is the one under test.
func storeUnavailableError() error {
	return fmt.Errorf("view agents at postgres://olivares_app:hunter2@db.internal:5432/olivares: %w: %w",
		store.ErrStoreUnavailable, fmt.Errorf("conn closed"))
}

// A credential refusal that also names the outage stays what it says: the 503
// arm sits below the 401 one.
func TestStatusForCredentialRefusalOutranksStoreUnavailable(t *testing.T) {
	both := fmt.Errorf("%w: %w", auth.ErrUnauthenticated, store.ErrStoreUnavailable)
	if status, code := statusFor(both); status != http.StatusUnauthorized || code != "unauthenticated" {
		t.Errorf("statusFor(unauthenticated + unavailable) = (%d, %q), want (401, unauthenticated)", status, code)
	}
}

// The driver's own text carries the DSN and the server's words: it goes to the
// log, never to the client, on REST and on gRPC.
func TestStoreUnavailableNeverEchoesTheDriver(t *testing.T) {
	s := &Server{log: slog.New(slog.DiscardHandler)}
	rec := httptest.NewRecorder()
	s.writeError(rec, httptest.NewRequest(http.MethodGet, "/v1/agents", nil), storeUnavailableError())
	for _, leak := range []string{"hunter2", "db.internal", "conn closed", "postgres://"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatalf("the 503 body carries %q: %s", leak, rec.Body.String())
		}
	}

	st, _ := status.FromError(grpcError(storeUnavailableError()))
	if st.Code() != codes.Unavailable {
		t.Errorf("gRPC code = %v, want %v", st.Code(), codes.Unavailable)
	}
	for _, leak := range []string{"hunter2", "db.internal", "conn closed"} {
		if strings.Contains(st.Message(), leak) {
			t.Errorf("the gRPC status carries %q: %q", leak, st.Message())
		}
	}
}

// A COMMIT whose answer was lost carries both sentinels (commitoutcome.go): the
// write may be durable, so the client is not told to retry. statusFor already
// answers it commit_outcome_unknown (#2774); the token endpoint keeps it a plain
// server error for the same reason — no blind re-send.
func TestClassifyExchangeErrorCommitOutcomeUnknownIsNotARetryable503(t *testing.T) {
	err := fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown, storeUnavailableError())
	if status, code, _ := classifyExchangeError(err); status != http.StatusInternalServerError || code != "server_error" {
		t.Fatalf("classifyExchangeError(commit outcome unknown + unavailable) = (%d, %q), want (500, server_error)", status, code)
	}
}

// The token endpoint's own vocabulary: an outage is temporarily_unavailable and
// is logged, never described to the client.
func TestClassifyExchangeErrorStoreUnavailable(t *testing.T) {
	status, code, internal := classifyExchangeError(storeUnavailableError())
	if status != http.StatusServiceUnavailable || code != "temporarily_unavailable" || !internal {
		t.Fatalf("classifyExchangeError(unavailable) = (%d, %q, internal=%t), want (503, temporarily_unavailable, true)", status, code, internal)
	}
}

// An outage was logged at Error ("api: request failed") before it had its own
// code; the 503 must not move it to the Info level of the refusals by design, or
// an alert on Error goes quiet during a real outage.
func TestStoreUnavailableIsLoggedAtError(t *testing.T) {
	var logs bytes.Buffer
	s := &Server{log: slog.New(slog.NewTextHandler(&logs, nil))}
	s.writeError(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/agents", nil), storeUnavailableError())
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "conn closed") {
		t.Fatalf("the outage log line = %q, want level=ERROR carrying the cause", logs.String())
	}
	logs.Reset()
	s.writeError(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/agents", nil), store.ErrNotLeader)
	if !strings.Contains(logs.String(), "level=INFO") {
		t.Fatalf("a refusal by design = %q, want level=INFO unchanged", logs.String())
	}
}
