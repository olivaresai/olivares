// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"testing"
)

// These tests pin the ONE property the whole list contract rests on: an empty
// collection is [] on the wire, never null. They are deliberately in the internal
// test package so they can also assert that the engine's own `listResponse` is
// the exported ListResponse (an alias, not a look-alike copy) — a re-declared
// struct would compile and pass every other test in this package while shipping
// `{"items":null}` again.

type nullArrayProbe struct {
	N int `json:"n"`
}

func TestListResponseEmptyPageMarshalsAsEmptyArray(t *testing.T) {
	// The zero envelope: no handler assigned Items at all. This is EXACTLY the
	// state 12 compliance endpoints served on a clean install.
	b, err := json.Marshal(ListResponse[nullArrayProbe]{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(b), `{"items":[],"has_more":false}`; got != want {
		t.Errorf("zero envelope = %s, want %s", got, want)
	}

	// An explicitly nil slice, the same defect written out.
	var nilItems []nullArrayProbe
	b, err = json.Marshal(ListResponse[nullArrayProbe]{Items: nilItems})
	if err != nil {
		t.Fatalf("marshal nil items: %v", err)
	}
	if got, want := string(b), `{"items":[],"has_more":false}`; got != want {
		t.Errorf("nil items = %s, want %s", got, want)
	}

	// A populated page is unchanged: the guarantee costs nothing but the nil case.
	b, err = json.Marshal(ListResponse[nullArrayProbe]{
		Items: []nullArrayProbe{{N: 1}, {N: 2}}, Cursor: "c1", HasMore: true,
	})
	if err != nil {
		t.Fatalf("marshal page: %v", err)
	}
	if got, want := string(b), `{"items":[{"n":1},{"n":2}],"cursor":"c1","has_more":true}`; got != want {
		t.Errorf("page = %s, want %s", got, want)
	}
}

func TestJSONArrayNeverMarshalsNull(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want string
	}{
		{"nil", JSONArray[string](nil), `[]`},
		{"empty", JSONArray[string]{}, `[]`},
		{"populated", JSONArray[string]{"a", "b"}, `["a","b"]`},
		{"nil struct element type", JSONArray[nullArrayProbe](nil), `[]`},
		{"nested nil", struct {
			A JSONArray[int] `json:"a"`
		}{}, `{"a":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.val)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(b) != tc.want {
				t.Errorf("= %s, want %s", b, tc.want)
			}
		})
	}
}

// TestListResponseStillDecodes guards the other half of the contract: adding
// MarshalJSON must not cost the ability to DECODE a list response, which every
// module test, the Go SDK and the CLI do. null decodes to a nil slice, which is
// correct and lossless — the guarantee is about what we EMIT.
func TestListResponseStillDecodes(t *testing.T) {
	var out ListResponse[nullArrayProbe]
	if err := json.Unmarshal([]byte(`{"items":[{"n":7}],"cursor":"c","has_more":true}`), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].N != 7 || out.Cursor != "c" || !out.HasMore {
		t.Errorf("decoded = %+v", out)
	}
	var empty ListResponse[nullArrayProbe]
	if err := json.Unmarshal([]byte(`{"items":null,"has_more":false}`), &empty); err != nil {
		t.Fatalf("unmarshal null items: %v", err)
	}
	if len(empty.Items) != 0 {
		t.Errorf("null items decoded to %d elements", len(empty.Items))
	}
}

// TestEngineListEnvelopeIsTheSharedType fails if core/api ever re-declares its own
// envelope instead of aliasing the shared one: the assignment only compiles while
// `listResponse` and `ListResponse` are the same type.
func TestEngineListEnvelopeIsTheSharedType(t *testing.T) {
	var alias listResponse[nullArrayProbe]
	var shared ListResponse[nullArrayProbe] = alias
	b, err := json.Marshal(shared)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(b), `{"items":[],"has_more":false}`; got != want {
		t.Errorf("engine envelope = %s, want %s", got, want)
	}
}
