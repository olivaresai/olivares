// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import "testing"

// canon decodes (UseNumber) then JCS-canonicalizes a JSON literal, as the connector
// does for an Agent Card.
func canon(t *testing.T, jsonLit string) string {
	t.Helper()
	m, err := decodeGeneric([]byte(jsonLit))
	if err != nil {
		t.Fatalf("decode %q: %v", jsonLit, err)
	}
	b, err := jcsCanonical(m)
	if err != nil {
		t.Fatalf("jcs %q: %v", jsonLit, err)
	}
	return string(b)
}

func TestJCSSortsObjectKeys(t *testing.T) {
	if got := canon(t, `{"b":1,"a":2,"c":3}`); got != `{"a":2,"b":1,"c":3}` {
		t.Errorf("key sorting: got %s", got)
	}
	// Nested objects sorted; array element ORDER preserved (only object keys sort).
	if got := canon(t, `{"z":[3,1,2],"a":{"y":1,"x":2}}`); got != `{"a":{"x":2,"y":1},"z":[3,1,2]}` {
		t.Errorf("nested sort: got %s", got)
	}
}

func TestJCSMinimalStringEscaping(t *testing.T) {
	// RFC 8785 escapes ONLY ", \, and controls — NOT the HTML-safe < > & that Go's
	// encoding/json escapes by default. A divergence here breaks signature verification.
	if got := canon(t, `{"k":"a<b>c&d"}`); got != `{"k":"a<b>c&d"}` {
		t.Errorf("must not HTML-escape: got %s", got)
	}
	if got := canon(t, `{"k":"line1\nline2\ttab\"q\\bs"}`); got != `{"k":"line1\nline2\ttab\"q\\bs"}` {
		t.Errorf("control/quote escaping: got %s", got)
	}
	// A JSON  escape decodes to U+0001 and re-serializes to the same lowercase
	// 6-char escape (RFC 8785). Both input and expectation are built from bytes to
	// avoid source-literal normalization of the escape sequence.
	esc := string([]byte{'\\', 'u', '0', '0', '0', '1'}) // the 6 chars: backslash u 0 0 0 1
	ctrl := `{"k":"` + esc + `"}`
	wantCtrl := `{"k":"` + esc + `"}`
	if got := canon(t, ctrl); got != wantCtrl {
		t.Errorf("control escape: got %q, want %q", got, wantCtrl)
	}
}

func TestJCSNumbers(t *testing.T) {
	cases := map[string]string{
		`{"n":100}`:               `{"n":100}`,
		`{"n":0}`:                 `{"n":0}`,
		`{"n":-5}`:                `{"n":-5}`,
		`{"n":4.5}`:               `{"n":4.5}`,
		`{"n":1e30}`:              `{"n":1e+30}`, // ECMAScript Number form
		`{"n":2e-3}`:              `{"n":0.002}`,
		`{"n":1.50}`:              `{"n":1.5}`, // trailing zero dropped
		`{"big":123456789012345}`: `{"big":123456789012345}`,
	}
	for in, want := range cases {
		if got := canon(t, in); got != want {
			t.Errorf("canon(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestJCSWholeObjectStable(t *testing.T) {
	// Re-canonicalizing canonical output is idempotent.
	once := canon(t, `{"b":"x","a":["m",{"q":1,"p":2}],"c":true,"d":null}`)
	twice := canon(t, once)
	if once != twice {
		t.Errorf("JCS not idempotent: %s != %s", once, twice)
	}
}
