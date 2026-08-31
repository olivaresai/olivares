// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
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

// productionShapedEnumerationError reproduces the error the engine ACTUALLY
// returns, rather than a bare sentinel a double could satisfy and production could
// not: sqlstore.systemScope.ListOrgs wraps store.ErrEnumerationNotAuthoritative
// with the operator remedy, so everything under test here must survive a WRAPPED
// sentinel, not a naked one.
func productionShapedEnumerationError() error {
	return fmt.Errorf("%w: engine %q holds no BYPASSRLS admin pool, so this System read is RLS-limited to the cleared tenant GUC and returned %d row(s) that CANNOT be read as the whole estate; provision a NOSUPERUSER BYPASSRLS role (deploy/postgres/01-app-role.sql) and pass --admin-dsn",
		store.ErrEnumerationNotAuthoritative, "postgres", 0)
}

// A FIRST BOOT THAT FAILS BY CONFIGURATION MUST SAY SO. Measured against
// Postgres 16.14 on 2026-08-08, before this fix: an engine opened on an
// application-only pool (no --admin-dsn) answered
//
//	POST /v1/setup → 500 {"error":{"code":"internal","message":"internal error"}}
//
// while the sentence naming the role file and the flag sat in the server log. The
// operator configuring the install is not reading that log — it is the first thing
// they ever see from this product.
//
// store.ErrEnumerationNotAuthoritative had no case in statusFor, so it fell to
// `default:` and was treated as an internal invariant violation. It is not one:
// the store answered correctly and deliberately, and what is missing is a
// provisioning step the operator can take.
func TestAdminPoolRefusalIsActionableNotInternal(t *testing.T) {
	err := productionShapedEnumerationError()

	gotStatus, gotCode := statusFor(err)
	if gotStatus != http.StatusNotImplemented {
		t.Errorf("statusFor = %d, want %d: a configuration condition the operator can fix is not an internal fault", gotStatus, http.StatusNotImplemented)
	}
	if gotCode == "internal" {
		t.Errorf("code = %q: the refusal is still indistinguishable from a crash", gotCode)
	}
	if gotCode != "cross_tenant_admin_pool_not_configured" {
		t.Errorf("code = %q, want cross_tenant_admin_pool_not_configured", gotCode)
	}

	s := &Server{log: slog.Default()}
	rec := httptest.NewRecorder()
	s.writeError(rec, httptest.NewRequest(http.MethodPost, "/v1/setup", nil), err)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("response status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
	code, msg := decodeErrorBody(t, rec)
	if code != "cross_tenant_admin_pool_not_configured" {
		t.Errorf("body code = %q", code)
	}
	if msg == "internal error" {
		t.Fatal("the operator is still told `internal error` and has nothing to act on")
	}
	// The two things the operator has to DO. Without both, the message is sympathy
	// rather than a remedy: one names what to provision, the other how to pass it.
	if !strings.Contains(msg, "olivares db init") || !strings.Contains(msg, "--admin-role") {
		t.Errorf("message does not name a command that provisions the role: %q", msg)
	}
	if !strings.Contains(msg, "--admin-dsn") {
		t.Errorf("message does not name the flag to pass: %q", msg)
	}
}

// A REFUSAL THAT NAMES A DEPLOYMENT STATE MUST NOT BE REPLAYED FROM A CACHE
// (raised by the external contrast). 501 is heuristically cacheable
// (RFC 9110 §15.6.2) and writeJSON sets no cache directives, so a private cache
// could serve "not configured" to an operator who has just configured it. The
// whole deliberate-refusal band is covered, not only the new code — every message
// in it describes something the operator is being told to change.
func TestDeliberateRefusalsAreNotCacheable(t *testing.T) {
	s := &Server{log: slog.Default()}
	for _, err := range []error{
		productionShapedEnumerationError(),            // 501, the new code
		fmt.Errorf("x: %w", auth.ErrSSONotConfigured), // 501, a pre-existing one
		fmt.Errorf("x: %w", store.ErrAuditSpoolFull),  // 503, the other half of the band
	} {
		rec := httptest.NewRecorder()
		s.writeError(rec, httptest.NewRequest(http.MethodGet, "/v1/system/orgs", nil), err)
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%v: Cache-Control = %q, want exactly no-store (status %d may be stored and replayed; a substring check would have accepted x-no-store)", err, got, rec.Code)
		}
	}
}

// THE ANTI-LEAK HALF, MADE EXPLICIT FOR THIS PATH. The seam's safety
// property is that the message is keyed on the CODE statusFor curated, so it can
// never echo a wrapped error's text. This path is where that matters most: the
// error travels through boot code holding DSNs, and the honest answer is one
// sentence away from being a credential disclosure on an UNAUTHENTICATED endpoint
// — /v1/setup takes a one-time token, not a session.
func TestAdminPoolRefusalNeverEchoesWrappedContext(t *testing.T) {
	const (
		secretDSN  = "postgres://olivares_app:hunter2@db.internal:5432/olivares"
		secretHost = "db.internal"
	)
	err := fmt.Errorf("open store at %s: %w", secretDSN, productionShapedEnumerationError())

	s := &Server{log: slog.Default()}
	rec := httptest.NewRecorder()
	s.writeError(rec, httptest.NewRequest(http.MethodPost, "/v1/setup", nil), err)
	_, msg := decodeErrorBody(t, rec)

	for _, leak := range []string{"hunter2", secretHost, secretDSN, "postgres://"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("the wrapped error's context reached the client (%q): %q", leak, msg)
		}
	}
	// And the wrap must not COST the actionable message either — a fix that goes
	// mute again the moment a caller adds context is not a fix.
	if msg == "internal error" || !strings.Contains(msg, "--admin-dsn") {
		t.Fatalf("wrapping the sentinel cost the remedy: %q", msg)
	}
}

// THE TWIN SURFACE. grpcError claims in its own comment to use "the same
// status table as REST (so the two surfaces never diverge)". That was true of the
// status and false of the message: it kept `>= 500 && != 503` — the shape
// writeError carried until 2026-08-05 — so every honest-seam 501 came out as
// "internal error" here, and 503 echoed err.Error() verbatim.
//
// Both halves are asserted: the refusal is legible, and a wrapper cannot leak.
func TestGRPCAdminPoolRefusalMatchesRESTAndNeverEchoesWrappedContext(t *testing.T) {
	const secret = "postgres://olivares_app:hunter2@db.internal:5432/olivares"
	err := fmt.Errorf("open store at %s: %w", secret, productionShapedEnumerationError())

	st, ok := status.FromError(grpcError(err))
	if !ok {
		t.Fatalf("grpcError did not produce a status: %v", err)
	}
	if st.Code() != codes.Unimplemented {
		t.Errorf("gRPC code = %v, want %v: a deliberate refusal is not an internal fault", st.Code(), codes.Unimplemented)
	}
	msg := st.Message()
	if strings.Contains(msg, "internal error") {
		t.Errorf("the gRPC surface still reports a deliberate refusal as an internal error: %q", msg)
	}
	for _, leak := range []string{"hunter2", "db.internal", secret} {
		if strings.Contains(msg, leak) {
			t.Fatalf("the wrapped error's context reached the gRPC client (%q): %q", leak, msg)
		}
	}
	if !strings.Contains(msg, "--admin-dsn") || !strings.Contains(msg, "olivares db init") {
		t.Errorf("the gRPC message carries no remedy: %q", msg)
	}
}

// The 503 leg of the same change, kept separate because it is a DIFFERENT branch
// of the old condition: 503 was the arm that preserved err.Error(), so it is where
// a wrapper actually leaked rather than merely going mute.
func TestGRPCDenyClosedRefusalNeverEchoesWrappedContext(t *testing.T) {
	const secret = "postgres://olivares_app:hunter2@db.internal:5432/olivares"
	err := fmt.Errorf("append to ledger at %s: %w", secret, store.ErrAuditSpoolFull)

	st, _ := status.FromError(grpcError(err))
	if st.Code() != codes.Unavailable {
		t.Errorf("gRPC code = %v, want %v", st.Code(), codes.Unavailable)
	}
	for _, leak := range []string{"hunter2", "db.internal", secret} {
		if strings.Contains(st.Message(), leak) {
			t.Fatalf("a wrapped 503 sentinel leaked its wrapper over gRPC (%q): %q", leak, st.Message())
		}
	}
	if !strings.Contains(st.Message(), "audit spool is full") {
		t.Errorf("the deny-closed reason was lost: %q", st.Message())
	}
}

// The control for the gRPC change, mirroring TestGenuineInternalErrorsStayGeneric
// on the REST side: narrowing the branch must not open it. A genuine 500 still has
// nothing safe to say on either surface.
func TestGRPCGenuineInternalErrorsStayGeneric(t *testing.T) {
	st, _ := status.FromError(grpcError(errors.New("some invariant blew up with /etc/olivares/secrets/db.dsn in it")))
	if st.Code() != codes.Internal {
		t.Errorf("gRPC code = %v, want %v", st.Code(), codes.Internal)
	}
	if strings.Contains(st.Message(), "db.dsn") || !strings.Contains(st.Message(), "internal error") {
		t.Fatalf("a genuine 500 leaked its message over gRPC: %q", st.Message())
	}
}
