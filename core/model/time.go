// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import (
	"fmt"
	"time"
)

// tsLayout is the canonical timestamp format: RFC3339-shaped in UTC with a
// fixed nine digits of fractional seconds and a literal "Z". For four-digit
// years — the entire operational domain, since OccurredAt is stamped from the
// store clock — the text is fixed-width, so lexical (byte) ordering equals
// chronological ordering: ORDER BY on the text column is correct on both
// engines with no function call, and the exact stored bytes are the bytes
// hashed into the audit chain (docs/SECURITY-HARDENING.md). Go's layout width is a MINIMUM,
// not a cap: an instant outside years 1-9999 (reachable only through
// NewTimestamp with a crafted value, never from the clock) renders wider
// ("-0001-…", "10000-…"), is not RFC 3339, does not re-parse (ParseTimestamp
// rejects it), and breaks the lexical-order property; the export layer proves
// such text still frames safely in every SIEM dialect
// (core/audit TestExtremeYearsStayFramingSafeInEveryDialect).
const tsLayout = "2006-01-02T15:04:05.000000000Z07:00"

// Timestamp is a UTC instant persisted as the canonical layout text (see
// tsLayout: fixed-width RFC3339 for four-digit years, the operational domain).
// Storing time as text gives identical bytes on SQLite and Postgres and a
// single canonical form for hashing; the type makes that canonical form the
// only one a caller can produce.
type Timestamp struct {
	t time.Time
}

// NewTimestamp returns a Timestamp for t, normalized to UTC.
func NewTimestamp(t time.Time) Timestamp {
	return Timestamp{t: t.UTC()}
}

// ParseTimestamp parses canonical timestamp text (as produced by String).
func ParseTimestamp(s string) (Timestamp, error) {
	t, err := time.Parse(tsLayout, s)
	if err != nil {
		return Timestamp{}, fmt.Errorf("parse timestamp: %w", err)
	}
	return Timestamp{t: t.UTC()}, nil
}

// String returns the canonical UTC text form (see tsLayout: fixed-width
// RFC3339 for four-digit years, wider outside them). The zero Timestamp
// formats to its canonical epoch-zero text, never an empty string, so a
// NOT NULL column always receives well-formed bytes.
func (ts Timestamp) String() string { return ts.t.UTC().Format(tsLayout) }

// Time returns the underlying UTC time.Time.
func (ts Timestamp) Time() time.Time { return ts.t.UTC() }

// IsZero reports whether the timestamp is the zero instant.
func (ts Timestamp) IsZero() bool { return ts.t.IsZero() }

// Before reports whether ts is strictly before other.
func (ts Timestamp) Before(other Timestamp) bool { return ts.t.Before(other.t) }

// Clock yields the current time. It is injected into the store so tests can use
// a deterministic clock; production uses the system UTC clock.
type Clock interface {
	// Now returns the current instant.
	Now() Timestamp
}

// SystemClock is the default Clock, returning the real wall-clock time in UTC.
type SystemClock struct{}

// Now returns the current system time in UTC.
func (SystemClock) Now() Timestamp { return NewTimestamp(time.Now()) }
