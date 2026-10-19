// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package otlptime

import (
	"math"
	"testing"
	"time"
)

// The edges of the instant Sender believes: MaxInt64 nanoseconds is the last
// representable one, one more wraps negative.
func TestSenderBoundaries(t *testing.T) {
	for _, c := range []struct {
		ns     uint64
		wantOK bool
	}{
		{0, false},
		{1, true},
		{math.MaxInt64, true},
		{math.MaxInt64 + 1, false},
		{math.MaxUint64, false},
	} {
		if _, ok := Sender(c.ns); ok != c.wantOK {
			t.Errorf("Sender(%d) ok = %v, want %v", c.ns, ok, c.wantOK)
		}
	}
}

// A time is kept while it is not after the clock; a missing or future one reads as the clock.
func TestStampKeepsOnlyTimesNotAfterTheClock(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		t    time.Time
		want time.Time
	}{
		{"zero", time.Time{}, now},
		{"past", now.Add(-time.Hour), now.Add(-time.Hour)},
		{"equal", now, now},
		{"future", now.Add(time.Nanosecond), now},
		{"year 2262", time.Date(2262, 4, 1, 0, 0, 0, 0, time.UTC), now},
	} {
		if got := Stamp(c.t, now); !got.Equal(c.want) {
			t.Errorf("%s: Stamp = %v, want %v", c.name, got, c.want)
		}
	}
}
