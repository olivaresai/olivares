// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The defect this test closes: the CLI printed the transport envelope and buried
// the engine's own sentence inside it. These bodies are the ones the first-hour
// walk captured.

func TestDescribeAPIRefusalLeadsWithWhatHappened(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			// The exact 422 the walk hit creating a session. The engine had
			// written a usable instruction; the CLI wrapped it in "request
			// failed" and two levels of JSON.
			"the instruction survives, first", http.StatusUnprocessableEntity,
			`{"error":{"code":"invalid_argument","message":"set auth_source to provider_account_home or managed_injection"}}`,
			"the engine rejected this request: set auth_source to provider_account_home or managed_injection (HTTP 422 invalid_argument)",
		},
		{
			// A code that only repeats the message adds nothing: this is the
			// engine's plain refusal, and "forbidden (HTTP 403 forbidden)" would
			// be three sayings of one fact.
			"a code that repeats the message is dropped", http.StatusForbidden,
			`{"error":{"code":"forbidden","message":"forbidden"}}`,
			"the engine refused this request: forbidden (HTTP 403)",
		},
		{
			"a missing record is not a refusal", http.StatusNotFound,
			`{"error":{"code":"not_found","message":"no such run"}}`,
			"the engine has no such record: no such run (HTTP 404 not_found)",
		},
		{
			"a conflict says what kind it is", http.StatusConflict,
			`{"error":{"code":"conflict","message":"the configuration home is already bound"}}`,
			"the engine refused this request because it conflicts with what is already there: the configuration home is already bound (HTTP 409 conflict)",
		},
		{
			"a server fault is the engine's, not the request's", http.StatusInternalServerError,
			`{"error":{"code":"internal","message":"store unavailable"}}`,
			"the engine failed to handle this request: store unavailable (HTTP 500 internal)",
		},
		{
			// Not the envelope: keep every byte. An engine answering with
			// something else is exactly when the raw body matters.
			"a body that is not the envelope is kept verbatim", http.StatusBadGateway,
			"upstream proxy: 502 Bad Gateway",
			"the engine failed to handle this request (HTTP 502): upstream proxy: 502 Bad Gateway",
		},
		{
			"an empty body says so instead of trailing off", http.StatusForbidden, "",
			"the engine refused this request, and said nothing about why (HTTP 403)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeAPIRefusal(tc.status, []byte(tc.body)); got != tc.want {
				t.Fatalf("describeAPIRefusal:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestHTTPErrKeepsItsExitCodes: the shape of the message changed; what a script
// branches on did not.
func TestHTTPErrKeepsItsExitCodes(t *testing.T) {
	for status, want := range map[int]int{
		http.StatusUnauthorized:        exitcode.Auth,
		http.StatusForbidden:           exitcode.Auth,
		http.StatusNotFound:            exitcode.NotFound,
		http.StatusConflict:            exitcode.Conflict,
		http.StatusInternalServerError: exitcode.Server,
	} {
		err := httpErr(status, []byte(`{"error":{"code":"c","message":"m"}}`))
		if got := exitcode.From(err); got != want {
			t.Fatalf("HTTP %d: exit code %d, want %d", status, got, want)
		}
	}
}

// TestHTTPErrNoLongerLeadsWithTheTransport is the defect, stated as a rule: the
// least informative words must not come first.
func TestHTTPErrNoLongerLeadsWithTheTransport(t *testing.T) {
	err := httpErr(http.StatusUnprocessableEntity,
		[]byte(`{"error":{"code":"invalid_argument","message":"set auth_source to provider_account_home"}}`))
	msg := err.Error()
	if strings.HasPrefix(msg, "request failed") {
		t.Fatalf("the message still leads with the transport: %q", msg)
	}
	if !strings.Contains(msg, "set auth_source to provider_account_home") {
		t.Fatalf("the engine's own instruction was lost: %q", msg)
	}
	// And nothing an operator needs was dropped on the way.
	for _, keep := range []string{"422", "invalid_argument"} {
		if !strings.Contains(msg, keep) {
			t.Fatalf("%q is gone from %q", keep, msg)
		}
	}
	if strings.Contains(msg, `{"error"`) {
		t.Fatalf("the raw envelope is still being printed: %q", msg)
	}
}
