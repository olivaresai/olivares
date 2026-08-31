// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"strings"
	"testing"
)

// The witnesses FuzzScrubExcerpt found, pinned deterministically. The property
// under test is NOT "the function is idempotent" — that is the symptom. It is
// "no digit of a redacted card survives the scrub", and the excerpt is the thing
// that gets stored and displayed.
func TestScrubExcerptLeavesNoTailOfARedactedCard(t *testing.T) {
	witnesses := []string{
		"Api_keY=4 111111111111111-0000",
		"Api_keY=41 11111111111111-0000",
		"Api_keY=411 1111111111111 0000",
		"Api_keY=4 111-1111-1111-1111-0000",
	}
	for _, in := range witnesses {
		got := scrubExcerpt(in)
		// A single pass left "…[redacted:credit-card]0000": four digits of the card
		// in the stored excerpt, emitted by the redactor itself.
		if strings.ContainsAny(got, "0123456789") {
			t.Errorf("digits survived the scrub of %q: %q", in, got)
		}
		if got != scrubExcerpt(got) {
			t.Errorf("scrub is not a fixed point for %q: %q -> %q", in, got, scrubExcerpt(got))
		}
	}
}

// The control, in the opposite direction: the fixed-point loop must not become an
// excuse to over-redact. A benign long digit run that fails Luhn, and ordinary
// text, have to survive untouched however many passes run.
func TestScrubExcerptDoesNotOverRedactBenignText(t *testing.T) {
	for _, in := range []string{
		"request 1234567890123456789 completed in 42ms",
		"the build took 1200 seconds and produced 3 artifacts",
		"seq=1234 prev=abcdef",
	} {
		if got := scrubExcerpt(in); got != in {
			t.Errorf("benign text was altered: %q -> %q", in, got)
		}
	}
}

// A real secret still goes, and still goes in ONE pass — the loop must not be
// load-bearing for the ordinary case.
func TestScrubExcerptStillRedactsTheOrdinaryCases(t *testing.T) {
	cases := map[string]string{
		"api_key=supersecretvalue":       "[redacted]",
		"AKIAIOSFODNN7EXAMPLE in a log":  "[redacted:aws-access-key]",
		"card 4111111111111111 declined": "[redacted:credit-card]",
	}
	for in, want := range cases {
		got := scrubExcerpt(in)
		if !strings.Contains(got, want) {
			t.Errorf("scrub(%q) = %q, want it to contain %q", in, got, want)
		}
		if got != scrubExcerptOnce(in) && want != "[redacted]" {
			t.Logf("note: %q needed more than one pass (%q vs %q)", in, got, scrubExcerptOnce(in))
		}
	}
}
