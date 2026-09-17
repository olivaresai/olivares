// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import (
	"math"
	"strconv"
	"strings"
)

// rebindPositional rewrites '?' placeholders to '$1','$2',… for engines that use
// numbered placeholders (Postgres). It is quote-aware: a '?' inside a
// single-quoted string literal is left untouched, and the standard SQL escape
// of a quote (two single quotes ”) inside a literal is handled. The engine
// authors all SQL with '?' and never interpolates values, so this is the only
// placeholder transformation needed.
func rebindPositional(query string) string {
	r := Rebinder{positional: true}
	var b strings.Builder
	b.Grow(len(query) + 8)
	r.Write(&b, query)
	return b.String()
}

// Rebinder applies a dialect's placeholder rewrite to one statement written as
// consecutive fragments. Len and Write advance the same quote-aware state, so a
// caller can count the exact length of the rewritten statement without building
// it, and then build exactly that statement. Writing a complete query as one
// fragment produces what the dialect's Rebind returns. The zero value is the
// identity rewrite. A Rebinder carries one statement's state; copy a fresh one
// for each statement.
type Rebinder struct {
	positional   bool
	placeholders uint64
	inLiteral    bool
	// quotePending marks a quote inside a literal whose meaning depends on the
	// next byte: another quote is an escaped quote, anything else ends the literal.
	quotePending bool
}

// NewRebinder returns the statement rebinder of the SQLite or PostgreSQL
// dialect; ok is false for any other Dialect implementation.
func NewRebinder(d Dialect) (Rebinder, bool) {
	switch d.(type) {
	case sqliteDialect, *sqliteDialect:
		return Rebinder{}, true
	case postgresDialect, *postgresDialect:
		return Rebinder{positional: true}, true
	}
	return Rebinder{}, false
}

// Len returns the rewritten byte length of fragment and advances r exactly as
// Write would. It allocates nothing. ok is false if the length overflows.
func (r *Rebinder) Len(fragment string) (n uint64, ok bool) {
	return r.scan(nil, fragment)
}

// Write appends the rewritten fragment to b and advances r.
func (r *Rebinder) Write(b *strings.Builder, fragment string) {
	r.scan(b, fragment)
}

// scan is the single placeholder interpretation behind Len, Write and Rebind.
// With b == nil it only counts.
func (r *Rebinder) scan(b *strings.Builder, fragment string) (uint64, bool) {
	if !r.positional {
		if b != nil {
			b.WriteString(fragment)
		}
		return uint64(len(fragment)), true
	}
	n, ok := uint64(0), true
	plain := 0 // start of the bytes copied unchanged since the last placeholder
	for i := 0; i < len(fragment); i++ {
		c := fragment[i]
		if r.quotePending {
			r.quotePending = false
			if c == '\'' {
				continue // an escaped quote keeps the literal open
			}
			r.inLiteral = false
		}
		switch {
		case c == '\'':
			if r.inLiteral {
				r.quotePending = true
			} else {
				r.inLiteral = true
			}
		case c == '?' && !r.inLiteral:
			r.placeholders++
			var buf [20]byte
			digits := strconv.AppendUint(buf[:0], r.placeholders, 10)
			if b != nil {
				b.WriteString(fragment[plain:i])
				b.WriteByte('$')
				b.Write(digits)
			}
			n, ok = addLen(n, ok, uint64(i-plain)+1+uint64(len(digits)))
			plain = i + 1
		}
	}
	if b != nil {
		b.WriteString(fragment[plain:])
	}
	return addLen(n, ok, uint64(len(fragment)-plain))
}

func addLen(n uint64, ok bool, k uint64) (uint64, bool) {
	if !ok || n > math.MaxUint64-k {
		return math.MaxUint64, false
	}
	return n + k, true
}
