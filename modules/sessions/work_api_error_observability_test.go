// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// writeWorkError is the single exit for every error on the work-command plane --
// 86 call sites across four files. An error it cannot classify becomes
// `500 internal_error`, and the body deliberately carries nothing else: a client
// must not be handed internals. That half is right and stays.
//
// What was wrong is that the error did not survive ANYWHERE. Measured on
// 2026-08-24 against a running engine: POST .../lease/acquire?mode=apply answered
// 500 and the request log said `status=500 dur_ms=7` and nothing more. The only
// copy of the cause was the error value, and this function dropped it.
//
// Both halves are pinned here, because fixing one by breaking the other would be
// worse than the defect.
func TestUnclassifiedWorkErrorIsLoggedButNeverDisclosed(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const secret = "dial tcp 10.9.9.9:5432: connect: connection refused for user olivares_app"
	rec := httptest.NewRecorder()
	writeWorkError(rec, errors.New(secret))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	// 1 · the server keeps the cause.
	if !strings.Contains(logged.String(), "unclassified work-plane error") {
		t.Fatalf("nothing was logged for an unclassified error; the only copy of the cause is "+
			"gone and an operator has 500 with an empty log.\n  log: %q", logged.String())
	}
	if !strings.Contains(logged.String(), "connection refused") {
		t.Fatalf("the log line does not carry the error text, so it names a problem without "+
			"saying which.\n  log: %q", logged.String())
	}

	// 2 · THE NON-FIRING DIRECTION, and it is the one that keeps the fix honest: the
	// RESPONSE must still disclose nothing. A change that logged the cause by putting
	// it in the body would satisfy every assertion above and leak the DSN to any caller.
	if strings.Contains(rec.Body.String(), "connection refused") ||
		strings.Contains(rec.Body.String(), "olivares_app") {
		t.Fatalf("the response body leaked the internal error to the caller: %s", rec.Body.String())
	}

	// 3 · A CALLER THAT WENT AWAY IS NOT AN INCIDENT EITHER, and this arm is the one a
	// review caught me missing: context.Canceled/DeadlineExceeded are classified by
	// nobody, so before this they fell into the ERROR arm and every console page change
	// that abandoned a request became an ERROR line. That is the same noise the next
	// assertion prevents for classified errors -- I had guarded one half and opened the
	// other in the same commit.
	logged.Reset()
	recCancel := httptest.NewRecorder()
	writeWorkError(recCancel, fmt.Errorf("work item read: %w", context.Canceled))
	if strings.Contains(logged.String(), "unclassified work-plane error") {
		t.Fatalf("an ABANDONED request was logged as an unclassified ERROR; a client "+
			"navigating away is not an incident.\n  log: %q", logged.String())
	}

	// 4 · A CLASSIFIED error is not an incident and must not be logged as one --
	// otherwise every ordinary 404 and 409 becomes ERROR noise and the real ones drown.
	logged.Reset()
	rec2 := httptest.NewRecorder()
	writeWorkError(rec2, broken(http.StatusNotFound, "not_found"))
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("classified status = %d, want 404", rec2.Code)
	}
	if logged.Len() != 0 {
		t.Fatalf("a CLASSIFIED error was logged at ERROR; ordinary refusals are not incidents "+
			"and this turns the log into noise.\n  log: %q", logged.String())
	}
}
