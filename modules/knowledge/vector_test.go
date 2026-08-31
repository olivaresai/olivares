// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"testing"
)

func TestEmbeddingRoundTrip(t *testing.T) {
	v := []float32{0.5, -0.25, 0, 1.5, 3.0}
	blob := encodeEmbedding(v)
	got, err := decodeEmbedding(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(v) {
		t.Fatalf("dim mismatch: %d vs %d", len(got), len(v))
	}
	for i := range v {
		if got[i] != v[i] {
			t.Errorf("v[%d] = %v, want %v", i, got[i], v[i])
		}
	}
}

func TestDecodeEmbeddingRejectsGarbage(t *testing.T) {
	if _, err := decodeEmbedding([]byte{0, 1, 2}); err == nil {
		t.Error("short blob should error")
	}
	if _, err := decodeEmbedding([]byte("XXXX\x00\x00\x00\x00")); err == nil {
		t.Error("bad magic should error")
	}
	good := encodeEmbedding([]float32{1, 2, 3})
	if _, err := decodeEmbedding(good[:len(good)-1]); err == nil {
		t.Error("truncated blob (dim/len mismatch) should error")
	}
}

func TestLocalEmbedderIsLexicalAndDeterministicAndLocal(t *testing.T) {
	e := LocalHashEmbedder{}
	if e.AllowsEgress() {
		t.Fatal("local embedder must never report egress")
	}
	if e.ModelRef() != LocalHashModelRef {
		t.Fatalf("ModelRef = %q", e.ModelRef())
	}
	vecs, _, err := e.Embed(context.Background(), "", []string{
		"governed retrieval and data lineage",
		"governed retrieval and data lineage",
		"completely unrelated cooking recipe",
	})
	if err != nil || len(vecs) != 3 {
		t.Fatalf("embed: %v / %d", err, len(vecs))
	}
	// Deterministic: identical text → identical vector → similarity 1.
	if s := cosine(vecs[0], vecs[1]); s < 0.999 {
		t.Errorf("identical texts should have cosine ~1, got %v", s)
	}
	// Lexical: the unrelated text shares fewer tokens → lower similarity.
	if cosine(vecs[0], vecs[2]) >= cosine(vecs[0], vecs[1]) {
		t.Error("unrelated text should be less similar than an identical one")
	}
}

func TestClassificationClearanceFailsClosed(t *testing.T) {
	cases := []struct {
		chunk, clearance string
		want             bool
	}{
		{"public", "public", true},
		{"internal", "public", false}, // chunk above clearance
		{"public", "secret", true},    // clearance above chunk
		{"confidential", "secret", true},
		{"secret", "confidential", false},
		{"", "public", true},       // empty chunk class = public
		{"bogus", "secret", false}, // unknown chunk label fails closed
		{"public", "bogus", false}, // unknown clearance fails closed
	}
	for _, c := range cases {
		if got := classificationAllowed(c.chunk, c.clearance); got != c.want {
			t.Errorf("classificationAllowed(%q,%q) = %v, want %v", c.chunk, c.clearance, got, c.want)
		}
	}
}

func TestACLFilter(t *testing.T) {
	if !aclAllows(nil, nil) {
		t.Error("empty ACL is unrestricted")
	}
	if !aclAllows([]string{"anyone"}, nil) {
		t.Error("'anyone' is unrestricted")
	}
	if aclAllows([]string{"group:hr"}, []string{"group:eng"}) {
		t.Error("disjoint groups must not match")
	}
	if !aclAllows([]string{"group:hr", "group:eng"}, []string{"group:eng"}) {
		t.Error("shared group must match")
	}
}

func TestCosineIndexRanksAndCaps(t *testing.T) {
	q := []float32{1, 0, 0}
	cands := []VectorCandidate{
		{ChunkID: "a", Vector: []float32{1, 0, 0}},     // identical
		{ChunkID: "b", Vector: []float32{0, 1, 0}},     // orthogonal
		{ChunkID: "c", Vector: []float32{0.9, 0.1, 0}}, // close
	}
	scored, err := cosineIndex{}.Rank(context.Background(), q, cands, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(scored) != 2 {
		t.Fatalf("topK cap failed: %d", len(scored))
	}
	if scored[0].ChunkID != "a" {
		t.Errorf("most similar should rank first, got %q", scored[0].ChunkID)
	}
}

func TestChunkText(t *testing.T) {
	if chunkText("") != nil {
		t.Error("empty body yields no chunks")
	}
	chunks := chunkText("Para one.\n\nPara two.\n\nPara three.")
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	for _, c := range chunks {
		if c == "" {
			t.Error("no empty chunk")
		}
	}
}
