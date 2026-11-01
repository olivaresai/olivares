// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"
	"time"
)

func TestEventuallyDeadlineStopsBeforeTheBinaryTimeout(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		testDeadline time.Time
		hasDeadline  bool
		want         time.Time
	}{
		{"default ten minute timeout", now.Add(10 * time.Minute), true, now.Add(10*time.Minute - eventuallyDeadlineReserve)},
		{"forty minute timeout", now.Add(40 * time.Minute), true, now.Add(40*time.Minute - eventuallyDeadlineReserve)},
		{"deadline closer than the reserve", now.Add(5 * time.Second), true, now.Add(5*time.Second - eventuallyDeadlineReserve)},
		{"timeout 0 has no deadline", time.Time{}, false, now.Add(eventuallyNoDeadlineWait)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := eventuallyDeadline(now, tc.testDeadline, tc.hasDeadline)
			if !got.Equal(tc.want) {
				t.Fatalf("eventuallyDeadline = %s, want %s", got, tc.want)
			}
			if tc.hasDeadline && !got.Before(tc.testDeadline) {
				t.Fatalf("eventuallyDeadline %s is not before the binary's timeout %s", got, tc.testDeadline)
			}
		})
	}
}

// TestEventuallyOutlastsAConditionReadyAfterTwelveSeconds is the loaded-host case: the
// condition holds only after the 10 s wall clock eventually used to give up at. The wait
// is bounded by the test binary's own deadline, not by a fixed number of seconds.
func TestEventuallyOutlastsAConditionReadyAfterTwelveSeconds(t *testing.T) {
	t.Parallel()
	ready := time.Now().Add(12 * time.Second)
	eventually(t, "a condition that holds after twelve seconds", func() bool {
		return !time.Now().Before(ready)
	})
	if time.Now().Before(ready) {
		t.Fatal("eventually returned before the condition held")
	}
}

func TestWaitUntilReportsWhetherTheConditionHeld(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Minute)
	calls := 0
	tests := []struct {
		name     string
		deadline time.Time
		cond     func() bool
		want     bool
	}{
		{"never holds, deadline passed", past, func() bool { return false }, false},
		{"holds at once, deadline passed", past, func() bool { return true }, true},
		{"holds on the third check", future, func() bool { calls++; return calls == 3 }, true},
	}
	for _, tc := range tests {
		if got := waitUntil(tc.deadline, tc.cond); got != tc.want {
			t.Errorf("%s: waitUntil = %t, want %t", tc.name, got, tc.want)
		}
	}
}
