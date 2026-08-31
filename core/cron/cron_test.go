// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package cron

import (
	"testing"
	"time"
)

func TestParseRejects(t *testing.T) {
	for _, spec := range []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *",
		"* * 0 * *", "* * 32 * *", "* * * 13 *", "* * * * 7",
		"a * * * *", "*/0 * * * *", "1-5 * * * *", "@daily",
	} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("spec %q accepted; want error", spec)
		}
	}
}

func TestCronMatches(t *testing.T) {
	at := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	cases := []struct {
		spec string
		when string
		want bool
	}{
		{"0 2 * * *", "2026-07-07T02:00:00Z", true},   // daily at 02:00
		{"0 2 * * *", "2026-07-07T02:01:00Z", false},  // one minute late
		{"30 6 * * 1", "2026-07-06T06:30:00Z", true},  // Monday 06:30 (2026-07-06 is a Monday)
		{"30 6 * * 1", "2026-07-07T06:30:00Z", false}, // Tuesday
		{"*/15 * * * *", "2026-07-07T10:45:00Z", true},
		{"*/15 * * * *", "2026-07-07T10:50:00Z", false},
		{"0 0 1 * *", "2026-08-01T00:00:00Z", true}, // monthly on the 1st
		{"0 0 1 * *", "2026-08-02T00:00:00Z", false},
		{"0 9 * 7 *", "2026-07-07T09:00:00Z", true},  // July only
		{"0 9 * 8 *", "2026-07-07T09:00:00Z", false}, // August only
		{"0 8 1 * 1", "2026-07-06T08:00:00Z", true},  // dom OR dow: Monday matches
		{"0 8 1 * 1", "2026-07-01T08:00:00Z", true},  // dom OR dow: the 1st matches
		{"0 8 1 * 1", "2026-07-07T08:00:00Z", false}, // neither
		{"0,30 12 * * *", "2026-07-07T12:30:00Z", true},
	}
	for _, c := range cases {
		spec, err := Parse(c.spec)
		if err != nil {
			t.Fatalf("parse %q: %v", c.spec, err)
		}
		if got := spec.Matches(at(c.when)); got != c.want {
			t.Errorf("%q at %s = %t, want %t", c.spec, c.when, got, c.want)
		}
	}
}

func TestCronDueSince(t *testing.T) {
	spec, err := Parse("0 2 * * *") // daily at 02:00 UTC
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	// Ran this morning after the trigger: not due.
	if spec.DueSince(time.Date(2026, 7, 7, 2, 0, 0, 0, time.UTC), now) {
		t.Fatal("already ran at today's trigger; must not be due")
	}
	// Last ran yesterday: today's 02:00 has passed, due.
	if !spec.DueSince(time.Date(2026, 7, 6, 2, 0, 0, 0, time.UTC), now) {
		t.Fatal("yesterday's run + past trigger must be due")
	}
	// Never ran: due iff a trigger fell in the last 24h (02:00 today did).
	if !spec.DueSince(time.Time{}, now) {
		t.Fatal("never-ran schedule with a trigger in the last 24h must be due")
	}
	// Never ran, checked at 01:00: no trigger in the lookback yet... except
	// yesterday's 02:00 IS within 24h — so due. Verify the tight window: at
	// 01:59 the previous trigger (yesterday 02:00) is 23h59m ago ⇒ due.
	if !spec.DueSince(time.Time{}, time.Date(2026, 7, 7, 1, 59, 0, 0, time.UTC)) {
		t.Fatal("previous day's trigger within the 24h lookback must be due")
	}
	// Ran one minute after the last trigger: not due until tomorrow.
	if spec.DueSince(time.Date(2026, 7, 7, 2, 1, 0, 0, time.UTC), now) {
		t.Fatal("run after the trigger must not re-fire")
	}
}

// The vectors ABOVE moved verbatim from modules/reporting/cron_test.go when the
// grammar was hoisted into /core. They are the COMPATIBILITY CONTRACT:
// reporting's accepted/rejected syntax and its DOM/DOW OR rule must not drift
// now that the same parser decides a governance allow/deny.

func TestCanonicalNormalizesWhitespaceOnly(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"0 * * * *", "0 * * * *"},
		{"  0   *  * *   * ", "0 * * * *"},
		{"0\t*  *\t* *", "0 * * * *"},
		// Semantics are deliberately NOT normalized: an allowlist is a list of
		// AUTHORED patterns, so these stay distinct spellings.
		{"*/1 * * * *", "*/1 * * * *"},
		{"0,30 * * * *", "0,30 * * * *"},
	} {
		if got := Canonical(c.in); got != c.want {
			t.Errorf("Canonical(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if Canonical("30,0 * * * *") == Canonical("0,30 * * * *") {
		t.Fatal("Canonical must not reorder a comma list (it would widen an allowlist)")
	}
}

func TestMinGap(t *testing.T) {
	for _, c := range []struct {
		spec   string
		want   time.Duration
		proven bool
	}{
		{"* * * * *", time.Minute, true},
		{"*/15 * * * *", 15 * time.Minute, true},
		{"0 * * * *", time.Hour, true},
		{"0 2 * * *", 24 * time.Hour, true},
		{"0,30 12 * * *", 30 * time.Minute, true}, // the SHORTEST gap, not the daily one
		{"30 6 * * 1", 7 * 24 * time.Hour, true},  // weekly
		// Yearly: the window must span TWO pairs. From a leap-year epoch the
		// FIRST pair is 366 days and the next is 365 — a window that stopped
		// after the first would report 366 and admit this spec under a
		// 365.5-day floor. 365 is the true minimum.
		{"0 0 1 1 *", 365 * 24 * time.Hour, true},
		{"0 0 1 * *", 28 * 24 * time.Hour, true}, // monthly: the shortest gap is a NON-leap February
		{"0 8 1 * 1", 24 * time.Hour, true},      // dom OR dow: the 1st landing on a Monday
		// A spec that names 29 February fires 28→29 Feb, 24h apart, but ONLY in
		// a leap year. A scan window without one reports 365 DAYS and would
		// admit it under any floor — the window must span both kinds of year.
		{"0 0 28,29 2 *", 24 * time.Hour, true},
		{"0 0 29,30 4 *", 24 * time.Hour, true},
	} {
		s, err := Parse(c.spec)
		if err != nil {
			t.Fatalf("parse %q: %v", c.spec, err)
		}
		gap, proven := s.MinGap()
		if proven != c.proven || gap != c.want {
			t.Errorf("%q MinGap = (%v, %t), want (%v, %t)", c.spec, gap, proven, c.want, c.proven)
		}
	}
}

// A spec whose gap EXCEEDS the horizon reports unbounded — which a floor check
// reads as "above any admissible floor", never as an unknown it must guess at.
func TestMinGapUnboundedAboveHorizon(t *testing.T) {
	for _, spec := range []string{
		"0 0 30 2 *", // February 30th: never fires at all
	} {
		s, err := Parse(spec)
		if err != nil {
			t.Fatal(err)
		}
		gap, bounded := s.MinGap()
		if bounded {
			t.Fatalf("%q reported bounded with gap %v; want unbounded", spec, gap)
		}
		if gap != 0 {
			t.Fatalf("%q unbounded MinGap must return 0, got %v", spec, gap)
		}
	}
}
