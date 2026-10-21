// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package otlptime is the one rule the OTLP receivers share for a time a producer sent.
// A sender time is evidence of when something happened, not a clock, so it is believed
// only while a signed instant can hold it and it is not after the receiver clock.
package otlptime

import (
	"math"
	"time"
)

// Sender reads an OTLP TimeUnixNano. Zero means the producer left it unset, and a value
// past MaxInt64 would wrap negative in an int64 and read as 1677-1969, which is "before"
// every clock: neither is an instant. When ok is false the returned time is the zero Time,
// which Stamp reads as "missing", so a caller that drops ok still records the clock.
func Sender(unixNano uint64) (time.Time, bool) {
	if unixNano == 0 || unixNano > math.MaxInt64 {
		return time.Time{}, false
	}
	return time.Unix(0, int64(unixNano)).UTC(), true
}

// Stamp is the time a signal is recorded at: the sender time t while it is not after now,
// and now for one that is missing (zero) or from the future (clock skew, or a lie). One
// record that claims the year 2262 must not pin a session ahead of the clock for good.
func Stamp(t, now time.Time) time.Time {
	if t.IsZero() || t.After(now) {
		return now
	}
	return t
}
