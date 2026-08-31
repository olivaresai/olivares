// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"bytes"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf16"
)

// JSON Canonicalization Scheme (RFC 8785). A2A signs an Agent Card by
// canonicalizing it with JCS, then producing a JWS over those bytes (the
// signatures field excluded). To VERIFY a card we must reproduce the exact same
// canonical bytes, so this is a faithful JCS implementation:
//
//   - object members are sorted by the UTF-16 code units of their names,
//   - strings use minimal JSON escaping (only ", \, and control chars < U+0020;
//     never the HTML-safe escaping Go's encoding/json applies to < > & U+2028/9),
//   - numbers use the ECMAScript Number-to-String form (integers exact),
//   - no insignificant whitespace.
//
// Decode input with json.Decoder.UseNumber() so numbers arrive as json.Number and
// are re-serialized canonically here (not passed through verbatim).
//
// Fail-safe: any value this cannot canonicalize to the exact bytes the signer used
// makes the signature not verify, which the caller treats as UNTRUSTED — the
// connector never upgrades trust on an uncertain canonicalization.

// jcsCanonical returns the RFC 8785 canonical JSON encoding of v (decoded with
// UseNumber).
func jcsCanonical(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeJCS(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeJCS(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeJCSString(b, x)
	case jsonNumberLike:
		return writeJCSNumber(b, x.String())
	case float64:
		// Present only if the input was decoded without UseNumber; format via ES rules.
		return writeJCSNumber(b, strconv.FormatFloat(x, 'g', -1, 64))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJCS(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sortByUTF16(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJCSString(b, k)
			b.WriteByte(':')
			if err := writeJCS(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("a2a jcs: unsupported value type %T", v)
	}
	return nil
}

// jsonNumberLike matches encoding/json's json.Number (which has String()) without
// importing it into the type switch as a named type alias quirk.
type jsonNumberLike interface{ String() string }

// writeJCSNumber serializes a JSON number literal in ECMAScript Number form. An
// integer literal is normalized exactly (no leading zeros / plus / "-0"); a
// non-integer is formatted with Go's shortest round-trip, which matches
// ECMAScript's significant digits for the values an Agent Card realistically
// carries. (Exotic floats that diverge from ES formatting simply fail to verify —
// fail-safe.)
func writeJCSNumber(b *bytes.Buffer, lit string) error {
	lit = strings.TrimSpace(lit)
	if lit == "" {
		return fmt.Errorf("a2a jcs: empty number")
	}
	// Integer literal (no fraction or exponent): normalize via big.Int.
	if !strings.ContainsAny(lit, ".eE") {
		n, ok := new(big.Int).SetString(lit, 10)
		if !ok {
			return fmt.Errorf("a2a jcs: bad integer %q", lit)
		}
		b.WriteString(n.String()) // big.Int has no -0 and no leading zeros
		return nil
	}
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil {
		return fmt.Errorf("a2a jcs: bad number %q: %w", lit, err)
	}
	if f == 0 {
		b.WriteString("0")
		return nil
	}
	b.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
	return nil
}

// writeJCSString writes s as a JSON string with RFC 8785 minimal escaping: only
// the two-character escapes and \u00xx for the remaining controls; every other
// rune (including all non-ASCII) is emitted as UTF-8 verbatim.
func writeJCSString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// sortByUTF16 sorts keys lexicographically by their UTF-16 code units, as RFC 8785
// requires (NOT by Go's UTF-8 byte order, which differs for code points outside the
// BMP). For ASCII keys the two orders coincide.
func sortByUTF16(keys []string) {
	enc := make(map[string][]uint16, len(keys))
	for _, k := range keys {
		enc[k] = utf16.Encode([]rune(k))
	}
	// insertion sort is fine: Agent Cards have few members; keeps it dependency-free
	// and stable without pulling sort.Slice closures over the encode map.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && less16(enc[keys[j-1]], enc[keys[j]]); j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
}

// less16 reports whether a should come AFTER b (used by the descending-swap loop
// above): it returns true when a > b in UTF-16 code-unit order.
func less16(a, b []uint16) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return len(a) > len(b)
}
