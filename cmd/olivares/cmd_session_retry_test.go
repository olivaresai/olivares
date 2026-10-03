// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"strings"
	"testing"
)

// HU2-13 (CLI side): with an API key the provider refused, Claude Code retried up to
// ten times and `session follow` printed "· api retry" each time, never the 401 every
// frame carried. The refusal is said once with what fixes it; another cause is one
// warning with its status, not one line per attempt.
func TestSessionViewSaysWhyTheToolRetriesOnce(t *testing.T) {
	retry := func(attempt int, status int, cause string) string {
		return fmt.Sprintf(`{"type":"system","subtype":"api_retry","attempt":%d,"max_retries":10,"retry_delay_ms":500,`+
			`"error_status":%d,"error":%q,"session_id":"s1","uuid":"u%d"}`, attempt, status, cause, attempt)
	}
	var b strings.Builder
	v := newSessionView(&b)
	v.driver = "claude"
	for attempt := 1; attempt <= 10; attempt++ {
		v.render(retry(attempt, 401, "authentication_failed"))
	}
	want := "✗ Claude Code's provider refused its credential (HTTP 401). Replace the API key in AI tools › API keys, " +
		"or sign it in: olivares tool login claude\n"
	if got := b.String(); got != want {
		t.Fatalf("refused key =\n%q\nwant one line\n%q", got, want)
	}

	b.Reset()
	v = newSessionView(&b)
	v.driver = "claude"
	for attempt := 1; attempt <= 3; attempt++ {
		v.render(retry(attempt, 529, "server_error"))
	}
	v.render(retry(4, 429, "rate_limit"))
	want = "! Claude Code: server error (HTTP 529); retrying, up to 10 times\n" +
		"! Claude Code: rate limit (HTTP 429); retrying, up to 10 times\n"
	if got := b.String(); got != want {
		t.Fatalf("retries =\n%q\nwant\n%q", got, want)
	}
}
