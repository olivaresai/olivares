// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vectorindex

import "testing"

func TestEncodeVector(t *testing.T) {
	got := encodeVector([]float32{1.5, -2, 0, 3.25})
	want := "[1.5,-2,0,3.25]"
	if got != want {
		t.Fatalf("encodeVector = %q, want %q", got, want)
	}
	if encodeVector(nil) != "[]" {
		t.Fatalf("empty vector should encode to []")
	}
}

func TestVectorDimValidates(t *testing.T) {
	if _, err := vectorDim(nil, []Candidate{{ID: "a", Vector: []float32{1}}}); err == nil {
		t.Error("empty query must error")
	}
	if _, err := vectorDim([]float32{1, 2}, []Candidate{{ID: "a", Vector: []float32{1, 2}}, {ID: "b", Vector: []float32{1}}}); err == nil {
		t.Error("a dimension mismatch must error (never a meaningless cosine)")
	}
	d, err := vectorDim([]float32{1, 2, 3}, []Candidate{{ID: "a", Vector: []float32{4, 5, 6}}})
	if err != nil || d != 3 {
		t.Fatalf("dim = %d, err = %v; want 3, nil", d, err)
	}
}

func TestSafeIdent(t *testing.T) {
	for _, ok := range []string{"knowledge_ann", "k1", "Vec_2"} {
		if !safeIdent(ok) {
			t.Errorf("%q should be a safe identifier", ok)
		}
	}
	for _, bad := range []string{"", "1abc", "drop table", "a;b", "a-b", "a.b", "naïve"} {
		if safeIdent(bad) {
			t.Errorf("%q must be rejected (injection surface)", bad)
		}
	}
}

func TestOpenValidates(t *testing.T) {
	if _, err := Open(Config{Kind: "redis", DSN: "x"}); err == nil {
		t.Error("an unknown backend must error")
	}
	if _, err := Open(Config{Kind: "pgvector", DSN: ""}); err == nil {
		t.Error("a missing DSN must error")
	}
	if _, err := Open(Config{Kind: "qdrant", DSN: "http://x", Namespace: "bad name"}); err == nil {
		t.Error("an unsafe namespace must error")
	}
}
