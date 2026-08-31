// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON (%v): %s", err, rec.Body.String())
	}
	return body.Error.Code, body.Error.Message
}

// A REFUSAL BY DESIGN IS NOT AN INTERNAL ERROR (2026-08-05). writeError generified
// on `status >= 500`, which swept in all fifteen 501 honest-seam and 503
// deny-closed branches of statusFor: the client was told `{"code":
// "sso_not_configured","message":"internal error"}`. In a product whose posture is
// saying out loud what it will not do, the one sentence that says it was the one
// being deleted.
func TestHonestSeamRefusalsKeepAnActionableMessage(t *testing.T) {
	s := &Server{log: slog.Default()}
	cases := []struct {
		err        error
		wantStatus int
		wantCode   string
		wantIn     string
	}{
		{auth.ErrSSONotConfigured, http.StatusNotImplemented, "sso_not_configured", "SSO is not configured"},
		{auth.ErrNoFederationSealer, http.StatusServiceUnavailable, "sso_unavailable", "no secret sealer"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.writeError(rec, httptest.NewRequest(http.MethodGet, "/x", nil), c.err)
		if rec.Code != c.wantStatus {
			t.Errorf("%v: status = %d, want %d", c.err, rec.Code, c.wantStatus)
		}
		code, msg := decodeErrorBody(t, rec)
		if code != c.wantCode {
			t.Errorf("%v: code = %q, want %q", c.err, code, c.wantCode)
		}
		if msg == "internal error" {
			t.Errorf("%v: a deliberate refusal still reports %q — the operator has nothing to act on", c.err, msg)
		}
		if !strings.Contains(msg, c.wantIn) {
			t.Errorf("%v: message %q does not mention %q", c.err, msg, c.wantIn)
		}
	}
}

// The message must come from the CURATED CODE, never from the error's own text: a
// handler may wrap a sentinel with context that carries a DSN, a path or a host.
// This is the half that keeps the fix from turning into a leak.
func TestHonestSeamMessageNeverEchoesWrappedContext(t *testing.T) {
	s := &Server{log: slog.Default()}
	const secret = "postgres://app:hunter2@db.internal:5432/olivares"
	wrapped := fmt.Errorf("open federation store at %s: %w", secret, auth.ErrSSONotConfigured)

	rec := httptest.NewRecorder()
	s.writeError(rec, httptest.NewRequest(http.MethodGet, "/x", nil), wrapped)
	_, msg := decodeErrorBody(t, rec)
	if strings.Contains(msg, "hunter2") || strings.Contains(msg, "db.internal") {
		t.Fatalf("the wrapped error's context reached the client: %q", msg)
	}
	if msg == "internal error" {
		t.Fatalf("wrapping a sentinel must not cost the actionable message: %q", msg)
	}
}

// A genuine 500 keeps the generic message: an internal invariant violation has
// nothing safe OR useful to say to a client. This is the control that proves the
// change above narrowed the branch rather than removing it.
func TestGenuineInternalErrorsStayGeneric(t *testing.T) {
	s := &Server{log: slog.Default()}
	rec := httptest.NewRecorder()
	s.writeError(rec, httptest.NewRequest(http.MethodGet, "/x", nil),
		errors.New("some invariant blew up with /etc/olivares/secrets/db.dsn in it"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	_, msg := decodeErrorBody(t, rec)
	if msg != "internal error" {
		t.Fatalf("a genuine 500 leaked its message: %q", msg)
	}
}

// Every 5xx code statusFor can produce must yield a sentence — the map is not
// required to be complete, the FALLBACK is required to work. A sentinel added
// tomorrow must not silently reintroduce "internal error".
func TestEveryHonestSeamCodeHumanisesEvenWithoutAMapEntry(t *testing.T) {
	if got := humanisedCode("some_future_capability_unavailable"); got != "Some future capability unavailable." {
		t.Errorf("humanisedCode fallback = %q", got)
	}
	if got := humanisedCode(""); !strings.Contains(got, "unavailable") {
		t.Errorf("an empty code must still produce a sentence, got %q", got)
	}
	for code, msg := range honestSeamMessage {
		if strings.TrimSpace(msg) == "" {
			t.Errorf("code %q maps to an empty message", code)
		}
		if msg == "internal error" {
			t.Errorf("code %q maps to the generic message the fix exists to remove", code)
		}
	}
}
