// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package json5

import "testing"

func TestUnmarshalQuirks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]any
	}{
		{
			name: "comments trailing commas unquoted keys",
			in: `{
				// line comment
				foo: 'bar',
				nested: {a: 1,}, /* block */
			}`,
			want: map[string]any{"foo": "bar", "nested": map[string]any{"a": float64(1)}},
		},
		{
			name: "single quotes and line continuation",
			in:   "{msg: 'hello\\\nworld'}",
			want: map[string]any{"msg": "helloworld"},
		},
		{
			name: "hex plus leading dot and trailing dot",
			in:   "{hex: 0x10, plus: +2, dot: .5, trail: 4.}",
			want: map[string]any{"hex": float64(16), "plus": float64(2), "dot": 0.5, "trail": float64(4)},
		},
		{
			name: "non finite numbers become null",
			in:   "{nan: NaN, inf: Infinity, ninf: -Infinity}",
			want: map[string]any{"nan": nil, "inf": nil, "ninf": nil},
		},
		{
			name: "comment markers inside strings",
			in:   `{a: "https://example.test/a//b", b: 'literal /* not comment */ text'}`,
			want: map[string]any{"a": "https://example.test/a//b", "b": "literal /* not comment */ text"},
		},
		{
			name: "escapes unicode and surrogate pair",
			in:   `{quote: 'it\'s', slash: "\x2f", unicode: "\u00f1", pair: "\uD83D\uDE00"}`,
			want: map[string]any{"quote": "it's", "slash": "/", "unicode": "ñ", "pair": "😀"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[string]any
			if err := Unmarshal([]byte(tt.in), &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			assertMap(t, got, tt.want)
		})
	}
}

func TestUnmarshalTypedStruct(t *testing.T) {
	var got struct {
		Model string   `json:"model"`
		Allow []string `json:"allow"`
	}
	if err := Unmarshal([]byte(`{model: 'anthropic/claude', allow: ['a', 'b',],}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "anthropic/claude" || len(got.Allow) != 2 || got.Allow[1] != "b" {
		t.Fatalf("unexpected struct decode: %#v", got)
	}
}

func TestUnmarshalRejectsInvalid(t *testing.T) {
	var got map[string]any
	if err := Unmarshal([]byte(`{a: [1, 2}`), &got); err == nil {
		t.Fatal("expected error")
	}
}

func assertMap(t *testing.T, got, want map[string]any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d; got %#v", len(got), len(want), got)
	}
	for k, wantV := range want {
		gotV, ok := got[k]
		if !ok {
			t.Fatalf("missing key %q in %#v", k, got)
		}
		if wantM, ok := wantV.(map[string]any); ok {
			gotM, ok := gotV.(map[string]any)
			if !ok {
				t.Fatalf("%s = %#v, want map", k, gotV)
			}
			assertMap(t, gotM, wantM)
			continue
		}
		if gotV != wantV {
			t.Fatalf("%s = %#v, want %#v", k, gotV, wantV)
		}
	}
}
