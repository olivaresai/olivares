// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package json5 decodes the JSON5 subset used by local agent configuration files.
// It normalizes JSON5 into ordinary encoding/json semantics without adding an
// external dependency.
package json5

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Unmarshal decodes JSON5 data into v. Non-finite JSON5 numbers are normalized to
// null because this package is used for governance config reads, where preserving
// NaN/Infinity numeric identity is not meaningful.
func Unmarshal(data []byte, v any) error {
	p := parser{data: data}
	x, err := p.parse()
	if err != nil {
		return err
	}
	normalized, err := json.Marshal(x)
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, v)
}

type parser struct {
	data []byte
	pos  int
}

func (p *parser) parse() (any, error) {
	v, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if err := p.skip(); err != nil {
		return nil, err
	}
	if p.pos != len(p.data) {
		return nil, p.err("unexpected trailing data")
	}
	return v, nil
}

func (p *parser) parseValue() (any, error) {
	if err := p.skip(); err != nil {
		return nil, err
	}
	if p.pos >= len(p.data) {
		return nil, p.err("unexpected end of input")
	}
	switch p.data[p.pos] {
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case '\'', '"':
		return p.parseString()
	case '+', '-', '.', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return p.parseNumber()
	default:
		ident, ok := p.readIdentifier()
		if !ok {
			return nil, p.err("unexpected token")
		}
		switch ident {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null", "NaN", "Infinity":
			return nil, nil
		default:
			return nil, p.err("unexpected identifier " + strconv.Quote(ident))
		}
	}
}

func (p *parser) parseObject() (map[string]any, error) {
	p.pos++
	out := map[string]any{}
	if err := p.skip(); err != nil {
		return nil, err
	}
	if p.consume('}') {
		return out, nil
	}
	for {
		if err := p.skip(); err != nil {
			return nil, err
		}
		key, err := p.parseKey()
		if err != nil {
			return nil, err
		}
		if err := p.skip(); err != nil {
			return nil, err
		}
		if !p.consume(':') {
			return nil, p.err("expected ':' after object key")
		}
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		out[key] = val
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.consume('}') {
			return out, nil
		}
		if !p.consume(',') {
			return nil, p.err("expected ',' or '}' in object")
		}
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.consume('}') {
			return out, nil
		}
	}
}

func (p *parser) parseArray() ([]any, error) {
	p.pos++
	var out []any
	if err := p.skip(); err != nil {
		return nil, err
	}
	if p.consume(']') {
		return out, nil
	}
	for {
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		out = append(out, val)
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.consume(']') {
			return out, nil
		}
		if !p.consume(',') {
			return nil, p.err("expected ',' or ']' in array")
		}
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.consume(']') {
			return out, nil
		}
	}
}

func (p *parser) parseKey() (string, error) {
	if p.pos >= len(p.data) {
		return "", p.err("unexpected end of input")
	}
	if p.data[p.pos] == '\'' || p.data[p.pos] == '"' {
		return p.parseString()
	}
	key, ok := p.readIdentifier()
	if !ok {
		return "", p.err("expected object key")
	}
	return key, nil
}

func (p *parser) parseString() (string, error) {
	quote := p.data[p.pos]
	p.pos++
	var b strings.Builder
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		p.pos++
		if c == quote {
			return b.String(), nil
		}
		if c != '\\' {
			r, size := utf8.DecodeRune(p.data[p.pos-1:])
			if r == utf8.RuneError && size == 1 {
				return "", p.err("invalid utf-8 in string")
			}
			if size > 1 {
				p.pos += size - 1
			}
			b.WriteRune(r)
			continue
		}
		if p.pos >= len(p.data) {
			return "", p.err("unterminated escape")
		}
		r, err := p.parseEscape()
		if err != nil {
			return "", err
		}
		if r >= 0 {
			b.WriteRune(r)
		}
	}
	return "", p.err("unterminated string")
}

func (p *parser) parseEscape() (rune, error) {
	c := p.data[p.pos]
	p.pos++
	switch c {
	case '\n':
		return -1, nil
	case '\r':
		if p.pos < len(p.data) && p.data[p.pos] == '\n' {
			p.pos++
		}
		return -1, nil
	case '\'', '"', '\\', '/':
		return rune(c), nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'v':
		return '\v', nil
	case '0':
		if p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			return 0, p.err("invalid nul escape")
		}
		return 0, nil
	case 'x':
		v, err := p.readHexRune(2)
		return v, err
	case 'u':
		hi, err := p.readHexRune(4)
		if err != nil {
			return 0, err
		}
		if utf16.IsSurrogate(hi) && hi >= 0xD800 && hi <= 0xDBFF &&
			p.pos+6 <= len(p.data) && p.data[p.pos] == '\\' && p.data[p.pos+1] == 'u' {
			save := p.pos
			p.pos += 2
			lo, err := p.readHexRune(4)
			if err == nil && lo >= 0xDC00 && lo <= 0xDFFF {
				return utf16.DecodeRune(hi, lo), nil
			}
			p.pos = save
		}
		return hi, nil
	default:
		r, size := utf8.DecodeRune(p.data[p.pos-1:])
		if r == utf8.RuneError && size == 1 {
			return 0, p.err("invalid escape")
		}
		if size > 1 {
			p.pos += size - 1
		}
		return r, nil
	}
}

func (p *parser) readHexRune(n int) (rune, error) {
	if p.pos+n > len(p.data) {
		return 0, p.err("short hex escape")
	}
	var v rune
	for i := 0; i < n; i++ {
		c := p.data[p.pos+i]
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, p.err("invalid hex escape")
		}
		v = v*16 + rune(d)
	}
	p.pos += n
	return v, nil
}

func (p *parser) parseNumber() (any, error) {
	start := p.pos
	if p.data[p.pos] == '+' || p.data[p.pos] == '-' {
		p.pos++
	}
	if p.matchWord("Infinity") || p.matchWord("NaN") {
		return nil, nil
	}
	if p.pos >= len(p.data) {
		return nil, p.err("invalid number")
	}
	if p.pos+1 < len(p.data) && p.data[p.pos] == '0' && (p.data[p.pos+1] == 'x' || p.data[p.pos+1] == 'X') {
		p.pos += 2
		hexStart := p.pos
		for p.pos < len(p.data) && isHex(p.data[p.pos]) {
			p.pos++
		}
		if p.pos == hexStart {
			return nil, p.err("invalid hex number")
		}
		token := string(p.data[start:p.pos])
		sign := ""
		if strings.HasPrefix(token, "-") {
			sign = "-"
			token = token[1:]
		} else if strings.HasPrefix(token, "+") {
			token = token[1:]
		}
		n, err := strconv.ParseUint(token[2:], 16, 64)
		if err != nil {
			return nil, err
		}
		return json.Number(sign + strconv.FormatUint(n, 10)), nil
	}
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			p.pos++
			continue
		}
		break
	}
	token := normalizeNumber(string(p.data[start:p.pos]))
	if token == "" {
		return nil, p.err("invalid number")
	}
	if _, err := strconv.ParseFloat(token, 64); err != nil {
		return nil, err
	}
	return json.Number(token), nil
}

func normalizeNumber(token string) string {
	token = strings.TrimPrefix(token, "+")
	switch {
	case strings.HasPrefix(token, "."):
		token = "0" + token
	case strings.HasPrefix(token, "-."):
		token = "-0" + token[1:]
	}
	if strings.HasSuffix(token, ".") {
		token += "0"
	}
	return token
}

func (p *parser) readIdentifier() (string, bool) {
	if p.pos >= len(p.data) {
		return "", false
	}
	r, size := utf8.DecodeRune(p.data[p.pos:])
	if r == utf8.RuneError && size == 1 {
		return "", false
	}
	if !isIdentStart(r) {
		return "", false
	}
	start := p.pos
	p.pos += size
	for p.pos < len(p.data) {
		r, size = utf8.DecodeRune(p.data[p.pos:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		if !isIdentPart(r) {
			break
		}
		p.pos += size
	}
	return string(p.data[start:p.pos]), true
}

func isIdentStart(r rune) bool {
	return r == '$' || r == '_' || unicode.IsLetter(r)
}

func isIdentPart(r rune) bool {
	return isIdentStart(r) || unicode.IsDigit(r)
}

func (p *parser) skip() error {
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v' {
			p.pos++
			continue
		}
		if c != '/' || p.pos+1 >= len(p.data) {
			return nil
		}
		switch p.data[p.pos+1] {
		case '/':
			p.pos += 2
			for p.pos < len(p.data) && p.data[p.pos] != '\n' && p.data[p.pos] != '\r' {
				p.pos++
			}
		case '*':
			p.pos += 2
			end := bytes.Index(p.data[p.pos:], []byte("*/"))
			if end < 0 {
				return p.err("unterminated block comment")
			}
			p.pos += end + 2
		default:
			return nil
		}
	}
	return nil
}

func (p *parser) consume(c byte) bool {
	if p.pos < len(p.data) && p.data[p.pos] == c {
		p.pos++
		return true
	}
	return false
}

func (p *parser) matchWord(word string) bool {
	if !bytes.HasPrefix(p.data[p.pos:], []byte(word)) {
		return false
	}
	end := p.pos + len(word)
	if end < len(p.data) {
		r, _ := utf8.DecodeRune(p.data[end:])
		if isIdentPart(r) {
			return false
		}
	}
	p.pos = end
	return true
}

func (p *parser) err(msg string) error {
	if p.pos > len(p.data) {
		p.pos = len(p.data)
	}
	line := 1 + bytes.Count(p.data[:p.pos], []byte{'\n'})
	col := p.pos + 1
	if idx := bytes.LastIndexByte(p.data[:p.pos], '\n'); idx >= 0 {
		col = p.pos - idx
	}
	return fmt.Errorf("json5: %s at line %d column %d", msg, line, col)
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
