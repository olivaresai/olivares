// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package connectv1

import (
	"errors"
	"fmt"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrStrictJSON reports a document the strict reader refuses.
var ErrStrictJSON = errors.New("connectv1: strict JSON refused")

func strictErr(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrStrictJSON}, args...)...)
}

// ParseStrictObject reads one JSON object with the rules of canonical.ts parseStrictObject:
// duplicate keys, prototype-named keys, trailing commas, trailing bytes, unescaped control
// characters, invalid escapes and non-canonical or unsafe numbers are refused. Numbers decode to
// int64. Two differences, both refusals: max counts bytes rather than UTF-16 units, and invalid
// UTF-8 or an unpaired surrogate escape is refused because it has no Go string form.
func ParseStrictObject(data []byte, max int) (map[string]any, error) {
	if len(data) > max {
		return nil, strictErr("document exceeds %d bytes", max)
	}
	if !utf8.Valid(data) {
		return nil, strictErr("document is not valid UTF-8")
	}
	p := &strictParser{s: data}
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.pos != len(p.s) {
		return nil, strictErr("trailing bytes after the JSON value")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, strictErr("document must be a JSON object")
	}
	return obj, nil
}

// maxStrictDepth bounds nesting so a hostile document cannot exhaust the stack.
const maxStrictDepth = 32

type strictParser struct {
	s   []byte
	pos int
}

func (p *strictParser) ws() {
	for p.pos < len(p.s) {
		switch p.s[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *strictParser) peek() byte {
	if p.pos < len(p.s) {
		return p.s[p.pos]
	}
	return 0
}

func (p *strictParser) value(depth int) (any, error) {
	if depth > maxStrictDepth {
		return nil, strictErr("nesting exceeds %d levels", maxStrictDepth)
	}
	p.ws()
	switch c := p.peek(); {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"':
		return p.str()
	case c == 't':
		return p.literal("true", true)
	case c == 'f':
		return p.literal("false", false)
	case c == 'n':
		return p.literal("null", nil)
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	return nil, strictErr("unexpected token")
}

func (p *strictParser) literal(lit string, v any) (any, error) {
	if len(p.s)-p.pos < len(lit) || string(p.s[p.pos:p.pos+len(lit)]) != lit {
		return nil, strictErr("unexpected token")
	}
	p.pos += len(lit)
	return v, nil
}

func (p *strictParser) number() (any, error) {
	start := p.pos
	if p.peek() == '-' {
		p.pos++
	}
	switch c := p.peek(); {
	case c == '0':
		p.pos++
		if n := p.peek(); n >= '0' && n <= '9' {
			return nil, strictErr("canonical numbers have no leading zeros")
		}
	case c >= '1' && c <= '9':
		for p.peek() >= '0' && p.peek() <= '9' {
			p.pos++
		}
	default:
		return nil, strictErr("canonical number expected")
	}
	if c := p.peek(); c == '.' || c == 'e' || c == 'E' {
		return nil, strictErr("connect-v1 numbers are canonical integers")
	}
	raw := string(p.s[start:p.pos])
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n > MaxSafeInteger || n < -MaxSafeInteger {
		return nil, strictErr("connect-v1 numbers must be safe integers")
	}
	return n, nil
}

func (p *strictParser) str() (string, error) {
	p.pos++ // opening quote
	var out []byte
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		switch {
		case c == '"':
			p.pos++
			return string(out), nil
		case c == '\\':
			p.pos++
			if p.pos >= len(p.s) {
				return "", strictErr("invalid escape")
			}
			e := p.s[p.pos]
			p.pos++
			switch e {
			case '"', '\\', '/':
				out = append(out, e)
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'u':
				r, err := p.hex4()
				if err != nil {
					return "", err
				}
				if utf16.IsSurrogate(r) {
					if r >= 0xdc00 || len(p.s)-p.pos < 6 || p.s[p.pos] != '\\' || p.s[p.pos+1] != 'u' {
						return "", strictErr("unpaired surrogate escape")
					}
					p.pos += 2
					lo, err := p.hex4()
					if err != nil {
						return "", err
					}
					r = utf16.DecodeRune(r, lo)
					if r == utf8.RuneError {
						return "", strictErr("unpaired surrogate escape")
					}
				}
				out = utf8.AppendRune(out, r)
			default:
				return "", strictErr("invalid escape")
			}
		case c < 0x20:
			return "", strictErr("unescaped control in string")
		default:
			out = append(out, c)
			p.pos++
		}
	}
	return "", strictErr("unterminated string")
}

func (p *strictParser) hex4() (rune, error) {
	if len(p.s)-p.pos < 4 {
		return 0, strictErr("invalid unicode escape")
	}
	v, err := strconv.ParseUint(string(p.s[p.pos:p.pos+4]), 16, 32)
	if err != nil {
		return 0, strictErr("invalid unicode escape")
	}
	p.pos += 4
	return rune(v), nil
}

func (p *strictParser) object(depth int) (map[string]any, error) {
	p.pos++ // '{'
	p.ws()
	out := map[string]any{}
	if p.peek() == '}' {
		p.pos++
		return out, nil
	}
	for {
		p.ws()
		if p.peek() != '"' {
			return nil, strictErr("object keys must be strings")
		}
		key, err := p.str()
		if err != nil {
			return nil, err
		}
		if _, dup := out[key]; dup {
			return nil, strictErr("duplicate field %q", key)
		}
		if key == "__proto__" || key == "constructor" || key == "prototype" {
			return nil, strictErr("prototype field names are refused")
		}
		p.ws()
		if p.peek() != ':' {
			return nil, strictErr("expected ':'")
		}
		p.pos++
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		out[key] = v
		p.ws()
		switch p.peek() {
		case ',':
			p.pos++
			p.ws()
			if p.peek() == '}' {
				return nil, strictErr("trailing comma")
			}
		case '}':
			p.pos++
			return out, nil
		default:
			return nil, strictErr("expected comma or object end")
		}
	}
}

func (p *strictParser) array(depth int) ([]any, error) {
	p.pos++ // '['
	p.ws()
	out := []any{}
	if p.peek() == ']' {
		p.pos++
		return out, nil
	}
	for {
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.ws()
		switch p.peek() {
		case ',':
			p.pos++
			p.ws()
			if p.peek() == ']' {
				return nil, strictErr("trailing comma")
			}
		case ']':
			p.pos++
			return out, nil
		default:
			return nil, strictErr("expected comma or array end")
		}
	}
}

// RequireExactFields refuses unknown fields and requires every required field.
func RequireExactFields(obj map[string]any, required, optional []string) error {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, k := range required {
		allowed[k] = struct{}{}
	}
	for _, k := range optional {
		allowed[k] = struct{}{}
	}
	for k := range obj {
		if _, ok := allowed[k]; !ok {
			return strictErr("unknown field %q", k)
		}
	}
	for _, k := range required {
		if _, ok := obj[k]; !ok {
			return strictErr("missing field %q", k)
		}
	}
	return nil
}

// String returns a required non-empty string field of at most max bytes.
func String(obj map[string]any, key string, max int) (string, error) {
	s, ok := obj[key].(string)
	if !ok || s == "" || len(s) > max {
		return "", strictErr("field %s must be a non-empty string", key)
	}
	return s, nil
}

// NullableString returns a field that is a string of at most max bytes or null. Absent is refused.
func NullableString(obj map[string]any, key string, max int) (string, bool, error) {
	v, present := obj[key]
	if !present {
		return "", false, strictErr("missing field %q", key)
	}
	if v == nil {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok || len(s) > max {
		return "", false, strictErr("field %s must be a string or null", key)
	}
	return s, true, nil
}

// Int returns a required integer field within [min, MaxSafeInteger].
func Int(obj map[string]any, key string, min int64) (int64, error) {
	n, ok := obj[key].(int64)
	if !ok || n < min {
		return 0, strictErr("field %s must be an integer >= %d", key, min)
	}
	return n, nil
}

// NullableInt returns a field that is an integer >= min or null. Absent is refused.
func NullableInt(obj map[string]any, key string, min int64) (int64, bool, error) {
	v, present := obj[key]
	if !present {
		return 0, false, strictErr("missing field %q", key)
	}
	if v == nil {
		return 0, false, nil
	}
	n, ok := v.(int64)
	if !ok || n < min {
		return 0, false, strictErr("field %s must be an integer >= %d or null", key, min)
	}
	return n, true, nil
}
