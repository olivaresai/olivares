// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vectorindex

import (
	"context"
	"strings"
	"testing"
)

func newPinecone(t *testing.T, fn func(method, path string, body map[string]any) (int, any)) (*pinecone, *fakeDoer) {
	t.Helper()
	d := &fakeDoer{fn: fn}
	b, err := openPinecone(Config{Kind: "pinecone", DSN: "https://my-index.svc.us-east1.pinecone.io", Namespace: DefaultNamespace, Timeout: defaultTimeout, Doer: d})
	if err != nil {
		t.Fatal(err)
	}
	return b.(*pinecone), d
}

func TestPineconeRankHonorsCandidateSetAndMapsScores(t *testing.T) {
	p, d := newPinecone(t, func(method, path string, body map[string]any) (int, any) {
		switch {
		case method == "POST" && path == "/vectors/fetch":
			return 200, map[string]any{"vectors": map[string]any{}}
		case method == "POST" && path == "/vectors/upsert":
			return 200, map[string]any{"upsertedCount": 3}
		case method == "POST" && path == "/query":
			return 200, map[string]any{"matches": []any{
				map[string]any{"id": "a", "score": 0.98},
				map[string]any{"id": "c", "score": 0.71},
			}}
		}
		t.Fatalf("unexpected %s %s", method, path)
		return 500, nil
	})

	cands := []Candidate{
		{ID: "a", Vector: []float32{1, 0, 0}},
		{ID: "b", Vector: []float32{0, 1, 0}},
		{ID: "c", Vector: []float32{0.9, 0.1, 0}},
	}
	got, err := p.Rank(context.Background(), []float32{1, 0, 0}, cands, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("rank = %+v, want [a c]", got)
	}
	if got[0].Score != 0.98 {
		t.Fatalf("score not mapped through: %v", got[0].Score)
	}

	// Verify the query carried a governance filter restricting to candidate IDs.
	for i := len(d.reqs) - 1; i >= 0; i-- {
		if d.reqs[i].path == "/query" {
			filter, ok := d.reqs[i].body["filter"].(map[string]any)
			if !ok {
				t.Fatal("query carried no filter — governance candidate set not enforced")
			}
			cidFilter, ok := filter["_cid"].(map[string]any)
			if !ok {
				t.Fatal("filter missing _cid field")
			}
			inList, ok := cidFilter["$in"].([]any)
			if !ok || len(inList) != 3 {
				t.Fatalf("$in filter = %v, want the 3 candidate IDs", cidFilter)
			}
			break
		}
	}
}

func TestPineconeRankUpsertsOnlyMissing(t *testing.T) {
	var upsertedCount int
	p, _ := newPinecone(t, func(method, path string, body map[string]any) (int, any) {
		switch {
		case method == "POST" && path == "/vectors/fetch":
			return 200, map[string]any{"vectors": map[string]any{
				"a": map[string]any{"id": "a"},
			}}
		case method == "POST" && path == "/vectors/upsert":
			if vecs, ok := body["vectors"].([]any); ok {
				upsertedCount = len(vecs)
			}
			return 200, map[string]any{"upsertedCount": 1}
		case method == "POST" && path == "/query":
			return 200, map[string]any{"matches": []any{
				map[string]any{"id": "a", "score": 1.0},
			}}
		}
		t.Fatalf("unexpected %s %s", method, path)
		return 500, nil
	})
	_, err := p.Rank(context.Background(), []float32{1, 0}, []Candidate{
		{ID: "a", Vector: []float32{1, 0}}, {ID: "b", Vector: []float32{0, 1}},
	}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if upsertedCount != 1 {
		t.Fatalf("upserted %d vectors, want 1 (only b — a already exists)", upsertedCount)
	}
}

func TestPineconeDenyClosedOnBackendError(t *testing.T) {
	p, _ := newPinecone(t, func(method, path string, _ map[string]any) (int, any) {
		switch {
		case path == "/vectors/fetch":
			return 200, map[string]any{"vectors": map[string]any{}}
		case path == "/vectors/upsert":
			return 200, map[string]any{"upsertedCount": 1}
		case path == "/query":
			return 500, map[string]any{"message": "internal error"}
		}
		return 200, nil
	})
	if _, err := p.Rank(context.Background(), []float32{1, 0}, []Candidate{{ID: "a", Vector: []float32{1, 0}}}, 5); err == nil {
		t.Fatal("a backend error must fail the rank (deny-closed), not return empty results")
	}
}

func TestPineconeEmptyCandidates(t *testing.T) {
	p, _ := newPinecone(t, func(_, _ string, _ map[string]any) (int, any) {
		t.Fatal("no API call expected for empty candidates")
		return 500, nil
	})
	got, err := p.Rank(context.Background(), []float32{1, 0}, nil, 5)
	if err != nil || got != nil {
		t.Fatalf("empty candidates should return nil, nil; got %+v, %v", got, err)
	}
}

func TestPineconeDimensionMismatchErrors(t *testing.T) {
	p, _ := newPinecone(t, func(_, _ string, _ map[string]any) (int, any) {
		return 200, nil
	})
	_, err := p.Rank(context.Background(), []float32{1, 0, 0}, []Candidate{
		{ID: "a", Vector: []float32{1, 0}},
	}, 5)
	if err == nil || !strings.Contains(err.Error(), "dimension") {
		t.Fatalf("dimension mismatch should error; got %v", err)
	}
}
