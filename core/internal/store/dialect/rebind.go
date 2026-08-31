// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import (
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
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	inLiteral := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'':
			// Toggle literal state; a doubled '' inside a literal is an escaped
			// quote and stays inside the literal.
			if inLiteral && i+1 < len(query) && query[i+1] == '\'' {
				b.WriteByte(c)
				b.WriteByte(query[i+1])
				i++
				continue
			}
			inLiteral = !inLiteral
			b.WriteByte(c)
		case c == '?' && !inLiteral:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
