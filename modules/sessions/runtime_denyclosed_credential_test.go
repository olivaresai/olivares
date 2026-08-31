// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A launch refused because no inference credential source is wired is a DECISION
// with a remedy, and it must not reach the operator as an internal error.
//
// Measured on a real `serve` before this was fixed: POST /v1/m/sessions/runs with
// transport stream-json answered 500 {"error":{"message":"internal error"}} and
// logged nothing, on a boot whose own log line said the launches were deny-closed.
//
// The three sibling issuers of this module already answer 503 for an unwired
// dependency; this was the fourth and the only one that fell through.
func TestDenyClosedNamesTheUnwiredCredentialSourceInsteadOf500(t *testing.T) {
	err := denyClosedErr("inference credential unavailable", errNoCredential)

	var re *runErr
	if !errors.As(err, &re) {
		t.Fatalf("denyClosedErr(errNoCredential) = %T (%v), want a *runErr carrying a status; "+
			"an unclassified error is what writeRunErr turns into 500 internal error", err, err)
	}
	if re.status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: an unwired dependency is the 503 this module "+
			"already uses for its other three issuers", re.status, http.StatusServiceUnavailable)
	}
	// The remedy has to be IN the message. A 503 that does not say what to wire
	// moves the operator from "it broke" to "it is unavailable" and no further.
	for _, want := range []string{"OLIVARES_SESSION_RUNTIME_WIF", "OLIVARES_SESSION_RUNTIME_TOKEN_FILE"} {
		if !strings.Contains(re.msg, want) {
			t.Fatalf("message %q does not name %s: the operator is told it is unavailable "+
				"and not how to make it available", re.msg, want)
		}
	}

	// THE NON-FIRING DIRECTION, and it is the one that matters here: the comment at
	// the call site protects a mint backend that is merely UNREACHABLE, because that
	// is not a decision about this launcher. A rule that turned every mint failure
	// into 503 would erase exactly that distinction, and would still pass every
	// assertion above.
	unreachable := denyClosedErr("inference credential unavailable",
		fmt.Errorf("dial tcp 10.0.0.9:443: connect: connection refused"))
	var re2 *runErr
	if errors.As(unreachable, &re2) {
		t.Fatalf("an UNREACHABLE mint backend was classified as %d %q; it must stay "+
			"unclassified, because 'I could not reach it' is not 'I refuse'", re2.status, re2.msg)
	}
}
