// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package cron is the engine's single 5-field cron grammar (minute hour
// day-of-month month day-of-week, UTC). Supported syntax per field: "*",
// "*/n", a number, or a comma list of numbers. No external dependency, no
// seconds field, no named months/days, no ranges.
//
// WHY IT LIVES IN /core. The grammar was duplicated: modules/reporting
// owned one copy and core/api's DR scheduler an unexported twin, while the
// Apache-licensed claude-routines connector carries a deliberately rough
// ESTIMATOR that must never decide an allow/deny. Once a cron expression became
// an ENFORCEMENT input — the routine-policy cadence floor and cron
// allowlist, enforced in modules/orchestration — a second parser at the policy
// boundary would be a permanent bypass: any expression the enforcing parser
// spells differently from the authoring parser is a hole.
//
// HONEST SCOPE: this is the one grammar on the GOVERNANCE path (authoring in
// modules/governance, enforcement in modules/orchestration, matching in
// modules/reporting). core/api/dr_schedule.go still carries its own unexported
// twin for the DR scheduler; folding that in touches DR semantics and is a
// separate change. Copies went from three to two, not to one.
//
// DELIBERATELY FROZEN. The accepted syntax is exactly what modules/reporting
// accepted before the move (its test vectors moved with it, unchanged, and are
// the compatibility contract). Broadening the grammar is a governance decision,
// not a convenience: a spelling the enforcing parser accepts but an authoring
// operator did not intend is the same bypass in the other direction.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Spec is a parsed 5-field cron expression.
type Spec struct {
	minute, hour, dom, month, dow field
	raw                           string
}

type field struct {
	any  bool
	step int   // 0 = no step; otherwise "*/step"
	set  []int // explicit values (empty when any/step)
}

// fieldBound describes one field's valid range.
type fieldBound struct {
	name     string
	min, max int
}

var bounds = [5]fieldBound{
	{"minute", 0, 59},
	{"hour", 0, 23},
	{"day-of-month", 1, 31},
	{"month", 1, 12},
	{"day-of-week", 0, 6}, // 0 = Sunday
}

// Parse parses and validates a 5-field cron expression.
func Parse(spec string) (Spec, error) {
	fields := strings.Fields(strings.TrimSpace(spec))
	if len(fields) != 5 {
		return Spec{}, fmt.Errorf("want 5 fields (minute hour day-of-month month day-of-week), got %d", len(fields))
	}
	var parsed [5]field
	for i, f := range fields {
		cf, err := parseField(f, bounds[i])
		if err != nil {
			return Spec{}, err
		}
		parsed[i] = cf
	}
	return Spec{
		minute: parsed[0], hour: parsed[1], dom: parsed[2], month: parsed[3], dow: parsed[4],
		raw: spec,
	}, nil
}

func parseField(f string, b fieldBound) (field, error) {
	if f == "*" {
		return field{any: true}, nil
	}
	if rest, ok := strings.CutPrefix(f, "*/"); ok {
		n, err := strconv.Atoi(rest)
		if err != nil || n <= 0 || n > b.max {
			return field{}, fmt.Errorf("%s: invalid step %q", b.name, f)
		}
		return field{step: n}, nil
	}
	parts := strings.Split(f, ",")
	set := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < b.min || n > b.max {
			return field{}, fmt.Errorf("%s: value %q out of range [%d, %d]", b.name, p, b.min, b.max)
		}
		set = append(set, n)
	}
	return field{set: set}, nil
}

func (f field) matches(v int) bool {
	if f.any {
		return true
	}
	if f.step > 0 {
		return v%f.step == 0
	}
	for _, n := range f.set {
		if n == v {
			return true
		}
	}
	return false
}

// Matches reports whether the instant (truncated to the minute, UTC) satisfies
// the spec. Day-of-month and day-of-week combine with OR when BOTH are
// restricted (the traditional cron rule); otherwise the restricted one applies.
func (s Spec) Matches(t time.Time) bool {
	t = t.UTC()
	if !s.minute.matches(t.Minute()) || !s.hour.matches(t.Hour()) || !s.month.matches(int(t.Month())) {
		return false
	}
	domOK := s.dom.matches(t.Day())
	dowOK := s.dow.matches(int(t.Weekday()))
	switch {
	case s.dom.any && s.dow.any:
		return true
	case s.dom.any:
		return dowOK
	case s.dow.any:
		return domOK
	default:
		return domOK || dowOK
	}
}

// DueSince reports whether the spec has a matching instant AFTER last and at
// or before now — i.e. the schedule is due. A zero last means "never ran": the
// schedule is due iff a matching instant exists in the lookback window (24h),
// which keeps a fresh schedule from firing for arbitrary history. The scan is
// minute-granular and bounded to 31 days — beyond that a due schedule fires on
// the next matching instant instead of replaying a long outage backlog (an
// operational report is about NOW; the run history records the gap honestly).
func (s Spec) DueSince(last time.Time, now time.Time) bool {
	now = now.UTC().Truncate(time.Minute)
	start := last.UTC().Truncate(time.Minute).Add(time.Minute)
	if last.IsZero() {
		start = now.Add(-24 * time.Hour)
	}
	if floor := now.Add(-31 * 24 * time.Hour); start.Before(floor) {
		start = floor
	}
	for t := start; !t.After(now); t = t.Add(time.Minute) {
		if s.Matches(t) {
			return true
		}
	}
	return false
}

// String returns the original spec text.
func (s Spec) String() string { return s.raw }

// Canonical returns the whitespace-normalized spelling of a cron expression:
// the five fields joined by exactly one space. It is what an allow-LIST must
// compare, so that "0 *  * * *" and "0 * * * *" are the same authored pattern
// and a caller cannot slip past a literal allowlist by re-spacing the text.
//
// It deliberately does NOT normalize SEMANTICS (a comma list is not reordered,
// "*/1" is not folded to "*"): an allowlist is a list of authored patterns, and
// silently widening it to "anything that happens to mean the same thing" would
// admit expressions no operator wrote. A caller that wants both spellings
// allow-lists both.
func Canonical(spec string) string { return strings.Join(strings.Fields(strings.TrimSpace(spec)), " ") }

// MinGapHorizon is how far past a spec's FIRST firing MinGap keeps looking.
//
// 800 days, not the ~366 that bounding the floor alone would suggest, because a
// yearly spec has TWO different gaps: from a leap-year epoch "0 0 1 1 *" spans
// 366 days to the next firing and 365 to the one after. A window that stops
// after the first pair reports 366 and would admit that spec under a 365.5-day
// floor. Covering two consecutive pairs reports the true 365.
//
// It still exceeds the longest interval this engine admits on a schedule
// (modules/orchestration caps expected_interval_seconds at 31,622,400s ≈ 366
// days, and max_cadence_seconds is bounded to the same ceiling), so "no second
// firing inside the horizon" remains a proof that the minimum gap is above any
// admissible cadence floor.
const MinGapHorizon = 800 * 24 * time.Hour

// firstFiringSearch bounds the hunt for the first firing before the horizon
// starts. Four years covers the leap-day cycle, so a spec that fires at all
// (however rarely) is found; one that never fires — "0 0 30 2 *", February 30th
// — is reported as unbounded, which is SAFE for a floor check: a spec that
// never fires cannot fire too often.
const firstFiringSearch = 4 * 366 * 24 * time.Hour

// minGapEpoch is a fixed, deterministic scan origin: 2000-01-01, a Saturday
// that begins a LEAP year. Together with a MinGapHorizon of 430 days the window
// contains BOTH a leap February (2000) and a non-leap one (2001), which is what
// the floor check needs — each catches a case the other misses:
//
//	"0 0 1 * *"      the shortest gap is Feb→Mar in a NON-leap year (28d).
//	                 A leap-only window reports 29d, overstating by a day.
//	"0 0 28,29 2 *"  the shortest gap is 28 Feb→29 Feb, 24h, and it exists ONLY
//	                 in a leap year. A non-leap-only window sees one firing per
//	                 February and reports 365 DAYS — overstating by a factor of
//	                 365, which would admit a routine that fires twice a day
//	                 under a two-day floor.
//
// The earlier "non-leap is the conservative direction" reasoning was wrong: it
// held for month-length specs and inverted for day-of-month specs that name 29
// February. Only a window spanning both kinds of year is safe.
var minGapEpoch = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

// MinGap returns the smallest interval between two consecutive firings of the
// spec. It is TOTAL — every spec gets an answer a floor check can act on:
//
//   - (d, true)  — at least two firings were observed, and d is the minimum gap
//     OVER THE SCAN WINDOW. A caller enforcing a floor denies when d < floor.
//     It is a DETECTOR of a cron that fires faster than the floor, not a proof
//     of compliance: the window is bounded, so a pattern whose shortest gap
//     falls outside it is reported larger than it truly is. The known case is a
//     yearly cron, where the observed pair spans a leap year (366d) while the
//     true minimum is 365d — which only misleads a floor in (365d, 366d]. The
//     binding scalar control is the DECLARED interval, which is compared
//     separately; this check catches a cadence that contradicts it.
//   - (0, false) — the spec fires at most once within MinGapHorizon of its
//     first firing (or never fires at all). Its minimum gap therefore EXCEEDS
//     MinGapHorizon, which is above any interval this engine admits, so a
//     caller enforcing a floor treats it as satisfying the floor. This is a
//     proof of a lower bound, not an "unknown" — there is deliberately no
//     branch where a caller has to guess.
//
// The scan is UTC and minute-granular, matching Matches. It skips whole days
// whose date fields cannot match and returns as soon as the smallest possible
// gap (one minute) is found, so even "* * * * *" and a yearly spec are cheap.
func (s Spec) MinGap() (gap time.Duration, bounded bool) {
	var prev time.Time
	min := time.Duration(0)
	// limit is the scan deadline. Until the FIRST firing is seen it is the
	// leap-cycle hunt; the first firing then pins it to first+MinGapHorizon and
	// it is NEVER moved again. (Advancing it on every firing would make the
	// deadline chase the scan and never terminate for a recurring spec.)
	limit := minGapEpoch.Add(firstFiringSearch)
	// Scan day by day: the date fields (month/DOM/DOW) are constant within a
	// day, so a non-matching day costs one check instead of 1440.
	for day := minGapEpoch; day.Before(limit); day = day.AddDate(0, 0, 1) {
		if !s.matchesDate(day) {
			continue
		}
		for t := day; t.Before(day.AddDate(0, 0, 1)); t = t.Add(time.Minute) {
			if !s.minute.matches(t.Minute()) || !s.hour.matches(t.Hour()) {
				continue
			}
			if prev.IsZero() {
				limit = t.Add(MinGapHorizon) // pinned once, by the first firing
			} else if d := t.Sub(prev); min == 0 || d < min {
				min = d
				if min == time.Minute {
					return min, true // nothing can be smaller
				}
			}
			prev = t
		}
	}
	if min == 0 {
		return 0, false
	}
	return min, true
}

// matchesDate reports whether the DATE fields (month, day-of-month,
// day-of-week) admit t's day, applying the same DOM/DOW OR rule as Matches.
func (s Spec) matchesDate(t time.Time) bool {
	t = t.UTC()
	if !s.month.matches(int(t.Month())) {
		return false
	}
	domOK := s.dom.matches(t.Day())
	dowOK := s.dow.matches(int(t.Weekday()))
	switch {
	case s.dom.any && s.dow.any:
		return true
	case s.dom.any:
		return dowOK
	case s.dow.any:
		return domOK
	default:
		return domOK || dowOK
	}
}
