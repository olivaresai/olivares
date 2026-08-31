// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package scim

import (
	"errors"
	"strings"
)

// ErrUnsupportedFilter is returned for a syntactically-valid but unsupported
// filter (e.g. a value-path expression); the handler maps it to 400 invalidFilter.
var ErrUnsupportedFilter = errors.New("scim: unsupported filter")

// ErrBadFilter is returned for a malformed filter; the handler maps it to 400
// invalidFilter.
var ErrBadFilter = errors.New("scim: malformed filter")

// Filter is a parsed SCIM filter expression evaluable against a resource's
// attribute getter. get returns an attribute's string value and whether it is
// present; string comparisons are case-insensitive (caseExact=false attributes).
type Filter interface {
	Match(get func(attr string) (string, bool)) bool
}

// ParseFilter parses a SCIM filter (RFC 7644 §3.4.2.2). It supports the
// attribute operators eq ne co sw ew gt ge lt le pr, the logical and/or/not, and
// parenthesised grouping. Value-path ([...]) expressions are rejected as
// unsupported.
func ParseFilter(input string) (Filter, error) {
	toks, err := lexFilter(input)
	if err != nil {
		return nil, err
	}
	p := &filterParser{toks: toks}
	f, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, ErrBadFilter
	}
	return f, nil
}

// SingleEq reports whether f is exactly one `attr eq value` comparison and, if
// so, returns the (lowercased) attribute and the value — the IdP pre-create
// existence-check fast path.
func SingleEq(f Filter) (attr, value string, ok bool) {
	c, isCmp := f.(*compareNode)
	if !isCmp || c.op != "eq" {
		return "", "", false
	}
	return c.attr, c.value, true
}

// --- AST ----------------------------------------------------------------------

type andNode struct{ l, r Filter }
type orNode struct{ l, r Filter }
type notNode struct{ inner Filter }
type presentNode struct{ attr string }
type compareNode struct {
	attr  string
	op    string // eq ne co sw ew gt ge lt le
	value string
}

func (n *andNode) Match(g func(string) (string, bool)) bool { return n.l.Match(g) && n.r.Match(g) }
func (n *orNode) Match(g func(string) (string, bool)) bool  { return n.l.Match(g) || n.r.Match(g) }
func (n *notNode) Match(g func(string) (string, bool)) bool { return !n.inner.Match(g) }

func (n *presentNode) Match(g func(string) (string, bool)) bool {
	v, ok := g(n.attr)
	return ok && v != ""
}

func (n *compareNode) Match(g func(string) (string, bool)) bool {
	got, ok := g(n.attr)
	if !ok {
		return false
	}
	a := strings.ToLower(got)
	b := strings.ToLower(n.value)
	switch n.op {
	case "eq":
		return a == b
	case "ne":
		return a != b
	case "co":
		return strings.Contains(a, b)
	case "sw":
		return strings.HasPrefix(a, b)
	case "ew":
		return strings.HasSuffix(a, b)
	case "gt":
		return a > b
	case "ge":
		return a >= b
	case "lt":
		return a < b
	case "le":
		return a <= b
	default:
		return false
	}
}

// --- lexer --------------------------------------------------------------------

type ftok struct {
	kind string // "ident", "string", "lparen", "rparen"
	val  string
}

func lexFilter(s string) ([]ftok, error) {
	var toks []ftok
	i := 0
	for i < len(s) {
		c := s[i]
		switch c {
		case ' ', '\t':
			i++
		case '(':
			toks = append(toks, ftok{kind: "lparen"})
			i++
		case ')':
			toks = append(toks, ftok{kind: "rparen"})
			i++
		case '[', ']':
			return nil, ErrUnsupportedFilter // value-path not supported
		case '"':
			j := i + 1
			var sb strings.Builder
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				sb.WriteByte(s[j])
				j++
			}
			if j >= len(s) {
				return nil, ErrBadFilter // unterminated string
			}
			toks = append(toks, ftok{kind: "string", val: sb.String()})
			i = j + 1
		default:
			j := i
			for j < len(s) && !isFilterDelim(s[j]) {
				j++
			}
			if j == i {
				return nil, ErrBadFilter
			}
			toks = append(toks, ftok{kind: "ident", val: s[i:j]})
			i = j
		}
	}
	return toks, nil
}

func isFilterDelim(c byte) bool {
	return c == ' ' || c == '\t' || c == '(' || c == ')' || c == '[' || c == ']' || c == '"'
}

// --- parser -------------------------------------------------------------------

type filterParser struct {
	toks []ftok
	pos  int
}

func (p *filterParser) peek() (ftok, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return ftok{}, false
}

func (p *filterParser) parseOr() (Filter, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.matchKeyword("or") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &orNode{l: left, r: right}
	}
	return left, nil
}

func (p *filterParser) parseAnd() (Filter, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.matchKeyword("and") {
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &andNode{l: left, r: right}
	}
	return left, nil
}

func (p *filterParser) parseNot() (Filter, error) {
	if p.matchKeyword("not") {
		if !p.matchKind("lparen") {
			return nil, ErrBadFilter
		}
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if !p.matchKind("rparen") {
			return nil, ErrBadFilter
		}
		return &notNode{inner: inner}, nil
	}
	return p.parsePrimary()
}

func (p *filterParser) parsePrimary() (Filter, error) {
	if p.matchKind("lparen") {
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if !p.matchKind("rparen") {
			return nil, ErrBadFilter
		}
		return inner, nil
	}
	return p.parseCompare()
}

func (p *filterParser) parseCompare() (Filter, error) {
	t, ok := p.peek()
	if !ok || t.kind != "ident" {
		return nil, ErrBadFilter
	}
	attr := strings.ToLower(t.val)
	p.pos++
	opTok, ok := p.peek()
	if !ok || opTok.kind != "ident" {
		return nil, ErrBadFilter
	}
	op := strings.ToLower(opTok.val)
	p.pos++
	if op == "pr" {
		return &presentNode{attr: attr}, nil
	}
	if !isCompareOp(op) {
		return nil, ErrBadFilter
	}
	valTok, ok := p.peek()
	if !ok || (valTok.kind != "string" && valTok.kind != "ident") {
		return nil, ErrBadFilter
	}
	p.pos++
	return &compareNode{attr: attr, op: op, value: valTok.val}, nil
}

func (p *filterParser) matchKeyword(kw string) bool {
	t, ok := p.peek()
	if ok && t.kind == "ident" && strings.EqualFold(t.val, kw) {
		p.pos++
		return true
	}
	return false
}

func (p *filterParser) matchKind(kind string) bool {
	t, ok := p.peek()
	if ok && t.kind == kind {
		p.pos++
		return true
	}
	return false
}

func isCompareOp(op string) bool {
	switch op {
	case "eq", "ne", "co", "sw", "ew", "gt", "ge", "lt", "le":
		return true
	default:
		return false
	}
}
