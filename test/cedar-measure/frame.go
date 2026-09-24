// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	cedarast "github.com/cedar-policy/cedar-go/ast"
)

// Bytes the framer and the tail rule compare against.
const (
	byteTab       = 0x09
	byteLF        = 0x0a
	byteCR        = 0x0d
	byteSpace     = 0x20
	byteQuote     = 0x22
	byteStar      = 0x2a
	byteSlash     = 0x2f
	byteSemicolon = 0x3b
	byteBackslash = 0x5c
)

// canary follows a non-blank tail: it must parse at exactly the tail's length
// plus the separating line feed, which proves the tail holds no token.
const canary = "permit(principal, action, resource);"

// segment is one proposed statement and its byte offset in the document.
type segment struct {
	start int
	text  string
}

// frameStatements proposes statement ends: the byte after each ';' outside
// strings and comments. It never accepts or refuses; the library decides.
// This reproduces the proposed framer so its agreement with the library can be
// measured; it is not the product's admission code.
func frameStatements(doc string) ([]segment, string) {
	const (
		inCode = iota
		inString
		inLineComment
		inBlockComment
	)
	var segments []segment
	start, state := 0, inCode
	for i := 0; i < len(doc); i++ {
		c := doc[i]
		switch state {
		case inCode:
			switch {
			case c == byteQuote:
				state = inString
			case c == byteSlash && i+1 < len(doc) && (doc[i+1] == byteSlash || doc[i+1] == byteStar):
				if doc[i+1] == byteSlash {
					state = inLineComment
				} else {
					state = inBlockComment
				}
				i++
			case c == byteSemicolon:
				segments = append(segments, segment{start: start, text: doc[start : i+1]})
				start = i + 1
			}
		case inString:
			if c == byteBackslash {
				i++
			} else if c == byteQuote {
				state = inCode
			}
		case inLineComment:
			if c == byteLF {
				state = inCode
			}
		case inBlockComment:
			if c == byteStar && i+1 < len(doc) && doc[i+1] == byteSlash {
				state = inCode
				i++
			}
		}
	}
	return segments, doc[start:]
}

func tailIsBlank(tail string) bool {
	for i := 0; i < len(tail); i++ {
		switch tail[i] {
		case byteTab, byteLF, byteCR, byteSpace:
		default:
			return false
		}
	}
	return true
}

// framed is the framer's decision for one document.
type framed struct {
	admitted bool
	policies []cedarast.Policy
	offsets  []int
}

// admitFramed applies the library checks to every proposed segment: (a) the
// segment parses as one policy, (b) the segment without its final ';' does
// not, and the tail holds no token.
func admitFramed(doc string) framed {
	segments, tail := frameStatements(doc)
	var out framed
	for _, s := range segments {
		var p cedarast.Policy
		if err := p.UnmarshalCedar([]byte(s.text)); err != nil {
			return framed{}
		}
		var shorter cedarast.Policy
		if err := shorter.UnmarshalCedar([]byte(s.text[:len(s.text)-1])); err == nil {
			return framed{}
		}
		out.policies = append(out.policies, p)
		out.offsets = append(out.offsets, s.start+p.Position.Offset)
	}
	if !tailIsBlank(tail) {
		var p cedarast.Policy
		if err := p.UnmarshalCedar([]byte(tail + string(rune(byteLF)) + canary)); err != nil || p.Position.Offset != len(tail)+1 {
			return framed{}
		}
	}
	out.admitted = true
	return out
}
