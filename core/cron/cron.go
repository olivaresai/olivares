// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package cron is the engine's shared 5-field cron grammar (minute hour
// day-of-month month day-of-week, UTC). Supported syntax per field: "*",
// "*/n", a number, or a comma list of numbers. Seconds, names and ranges
// are unsupported.
//
// Authoring, policy enforcement and scheduling use the same frozen grammar.
// Connector estimates must not decide policy; accepting new syntax changes
// the authority an operator can grant.
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

// DueSince reports a matching minute after last and at or before now.
// A zero last uses a 24-hour lookback; other scans are capped at 31 days.
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

// Canonical normalizes whitespace for authored-pattern allowlists. It preserves
// field spellings: reordering lists or folding "*/1" to "*" would widen authority.
func Canonical(spec string) string { return strings.Join(strings.Fields(strings.TrimSpace(spec)), " ") }

// MinGapHorizon bounds the scan after the first firing. It covers consecutive
// annual gaps across leap and non-leap years and exceeds the 366-day cadence cap.
const MinGapHorizon = 800 * 24 * time.Hour

// firstFiringSearch bounds the initial search across a leap-day cycle.
const firstFiringSearch = 4 * 366 * 24 * time.Hour

// minGapEpoch fixes the scan origin in a leap year so both February lengths occur.
var minGapEpoch = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

// MinGap returns the smallest observed gap between consecutive firings in the
// bounded scan, or (0, false) when fewer than two are observed. A gap below a
// caller's floor contradicts it; a bounded scan cannot prove compliance outside
// its window. Callers must also enforce the declared interval.
//
// The UTC scan skips non-matching dates and stops at the minimum possible gap.
func (s Spec) MinGap() (gap time.Duration, bounded bool) {
	var prev time.Time
	min := time.Duration(0)
	// Pin the deadline once at the first firing; moving it on every firing
	// would prevent recurring specs from terminating.
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
