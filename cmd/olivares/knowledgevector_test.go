// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"math"
	"sort"
	"testing"

	"github.com/olivaresai/olivares/connectors/vectorindex"
	"github.com/olivaresai/olivares/modules/knowledge"
)

type fakeBackend struct {
	rank func(query []float32, candidates []vectorindex.Candidate, topK int) ([]vectorindex.Scored, error)
}

func (f fakeBackend) Rank(_ context.Context, q []float32, c []vectorindex.Candidate, k int) ([]vectorindex.Scored, error) {
	return f.rank(q, c, k)
}
func (f fakeBackend) Close() error { return nil }

// cosineRankRef is a reference exact-cosine ranker with the SAME tie-break as the
// module's in-process cosineIndex (score desc, then id asc) — so a backend that ranks
// by cosine and the in-process index agree, and the adapter must preserve that order.
func cosineRankRef(q []float32, cands []vectorindex.Candidate, k int) []vectorindex.Scored {
	out := make([]vectorindex.Scored, 0, len(cands))
	for _, c := range cands {
		out = append(out, vectorindex.Scored{ID: c.ID, Score: cosineRef(q, c.Vector)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}

func cosineRef(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// TestVectorAdapterParityWithCosine proves the adapter faithfully relays a cosine-
// ranking backend's top-K order (the DoD parity guarantee at the seam level; the real
// ANN-vs-cosine parity on a live pgvector is the env-gated connector integration test).
func TestVectorAdapterParityWithCosine(t *testing.T) {
	cands := []knowledge.VectorCandidate{
		{ChunkID: "a", Vector: []float32{1, 0, 0}},
		{ChunkID: "b", Vector: []float32{0, 1, 0}},
		{ChunkID: "c", Vector: []float32{0.9, 0.1, 0}},
	}
	a := &vectorIndexAdapter{backend: fakeBackend{rank: func(q []float32, c []vectorindex.Candidate, k int) ([]vectorindex.Scored, error) {
		return cosineRankRef(q, c, k), nil
	}}}
	got, err := a.Rank(context.Background(), []float32{1, 0, 0}, cands, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ChunkID != "a" || got[1].ChunkID != "c" {
		t.Fatalf("rank = %+v, want [a c] (most similar first, topK cap)", got)
	}
}

// TestVectorAdapterDropsNonCandidates proves the governance invariant is enforced on
// the MODULE side of the seam too: even a buggy/compromised backend that returns an id
// outside the candidate set never surfaces it.
func TestVectorAdapterDropsNonCandidates(t *testing.T) {
	a := &vectorIndexAdapter{backend: fakeBackend{rank: func(_ []float32, _ []vectorindex.Candidate, _ int) ([]vectorindex.Scored, error) {
		return []vectorindex.Scored{{ID: "ghost", Score: 0.99}, {ID: "a", Score: 0.5}}, nil
	}}}
	got, err := a.Rank(context.Background(), []float32{1, 0}, []knowledge.VectorCandidate{{ChunkID: "a", Vector: []float32{1, 0}}}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChunkID != "a" {
		t.Fatalf("a non-candidate id must be dropped, got %+v", got)
	}
}

// TestVectorAdapterDenyClosed proves a backend error fails the rank (the module then
// returns 502) — never empty results from a degraded index.
func TestVectorAdapterDenyClosed(t *testing.T) {
	a := &vectorIndexAdapter{backend: fakeBackend{rank: func(_ []float32, _ []vectorindex.Candidate, _ int) ([]vectorindex.Scored, error) {
		return nil, errors.New("backend down")
	}}}
	if _, err := a.Rank(context.Background(), []float32{1, 0}, []knowledge.VectorCandidate{{ChunkID: "a", Vector: []float32{1, 0}}}, 5); err == nil {
		t.Fatal("a backend error must fail the rank (deny-closed)")
	}
}

func TestVectorAdapterEmptyCandidates(t *testing.T) {
	a := &vectorIndexAdapter{backend: fakeBackend{rank: func(_ []float32, _ []vectorindex.Candidate, _ int) ([]vectorindex.Scored, error) {
		t.Fatal("backend must not be called for an empty candidate set")
		return nil, nil
	}}}
	got, err := a.Rank(context.Background(), []float32{1, 0}, nil, 5)
	if err != nil || got != nil {
		t.Fatalf("empty candidates → (nil,nil), got %+v %v", got, err)
	}
}
