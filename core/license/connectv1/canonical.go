// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package connectv1

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"unicode/utf8"
)

// MaxSafeInteger is the largest integer both implementations represent exactly.
const MaxSafeInteger = 1<<53 - 1

// PopMessage is the signed proof-of-possession message. Its canonical form lists the fields in
// lexicographic order with the domain fixed to Domain (canonical.ts encodePopMessage).
type PopMessage struct {
	BindingEpoch   int64
	BodySHA256     string
	Challenge      string
	Exp            int64
	IdempotencyKey string
	KID            string
	Method         string
	Operation      string
	Origin         string
	Path           string
	Target         string
}

// ErrCanonical reports a message that has no canonical encoding.
var ErrCanonical = errors.New("connectv1: message has no canonical encoding")

var bodySHAPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// EncodePopMessage returns the exact bytes both implementations sign. It refuses what the
// TypeScript encoder refuses, and additionally refuses strings that are not valid UTF-8 and
// integers beyond MaxSafeInteger, which the TypeScript side cannot represent.
func EncodePopMessage(m PopMessage) ([]byte, error) {
	if m.BindingEpoch < 0 || m.BindingEpoch > MaxSafeInteger {
		return nil, fmt.Errorf("%w: binding_epoch must be a canonical integer >= 0", ErrCanonical)
	}
	if m.Exp < 1 || m.Exp > MaxSafeInteger {
		return nil, fmt.Errorf("%w: exp must be a canonical unix-seconds integer", ErrCanonical)
	}
	if !bodySHAPattern.MatchString(m.BodySHA256) {
		return nil, fmt.Errorf("%w: body_sha256 must be 64 lowercase hex", ErrCanonical)
	}
	for _, f := range []struct{ name, value string }{
		{"challenge", m.Challenge}, {"idempotency_key", m.IdempotencyKey}, {"kid", m.KID},
		{"method", m.Method}, {"operation", m.Operation}, {"origin", m.Origin},
		{"path", m.Path}, {"target", m.Target},
	} {
		if f.value == "" {
			return nil, fmt.Errorf("%w: %s is empty", ErrCanonical, f.name)
		}
		if !utf8.ValidString(f.value) {
			return nil, fmt.Errorf("%w: %s is not valid UTF-8", ErrCanonical, f.name)
		}
	}
	out := make([]byte, 0, 512)
	out = append(out, '{')
	appendField := func(key string, value []byte, first bool) {
		if !first {
			out = append(out, ',')
		}
		out = appendJSONString(out, key)
		out = append(out, ':')
		out = append(out, value...)
	}
	str := func(s string) []byte { return appendJSONString(nil, s) }
	appendField("binding_epoch", []byte(strconv.FormatInt(m.BindingEpoch, 10)), true)
	appendField("body_sha256", str(m.BodySHA256), false)
	appendField("challenge", str(m.Challenge), false)
	appendField("domain", str(Domain), false)
	appendField("exp", []byte(strconv.FormatInt(m.Exp, 10)), false)
	appendField("idempotency_key", str(m.IdempotencyKey), false)
	appendField("kid", str(m.KID), false)
	appendField("method", str(m.Method), false)
	appendField("operation", str(m.Operation), false)
	appendField("origin", str(m.Origin), false)
	appendField("path", str(m.Path), false)
	appendField("target", str(m.Target), false)
	out = append(out, '}')
	return out, nil
}

const hexDigits = "0123456789abcdef"

// appendJSONString escapes exactly as canonical.ts jsonString: quote, backslash, \n, \r and \t
// by name, other bytes below 0x20 as \u00xx in lowercase hex, and everything else verbatim.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			dst = append(dst, '\\', '"')
		case c == '\\':
			dst = append(dst, '\\', '\\')
		case c == '\n':
			dst = append(dst, '\\', 'n')
		case c == '\r':
			dst = append(dst, '\\', 'r')
		case c == '\t':
			dst = append(dst, '\\', 't')
		case c < 0x20:
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
		default:
			dst = append(dst, c)
		}
	}
	return append(dst, '"')
}
