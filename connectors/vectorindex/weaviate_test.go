// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vectorindex

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func newWeaviate(t *testing.T, fn func(method, path string, body map[string]any) (int, any)) (*weaviate, *fakeDoer) {
	t.Helper()
	d := &fakeDoer{fn: fn}
	b, err := openWeaviate(Config{Kind: "weaviate", DSN: "http://weaviate:8080", Namespace: DefaultNamespace, Timeout: defaultTimeout, Doer: d})
	if err != nil {
		t.Fatal(err)
	}
	return b.(*weaviate), d
}

func TestWeaviateRankHonorsCandidateSetAndMapsScores(t *testing.T) {
	w, d := newWeaviate(t, func(method, path string, body map[string]any) (int, any) {
		switch {
		case method == http.MethodPost && path == "/v1/schema":
			return http.StatusOK, map[string]any{"class": weaviateCollection}
		case method == http.MethodPost && path == "/v1/graphql":
			q, _ := body["query"].(string)
			if strings.Contains(q, "nearVector") && strings.Contains(q, "cid") {
				// Check if this is the existence check (no nearVector) or the search.
				if strings.Contains(q, "distance") {
					// Search query.
					return http.StatusOK, map[string]any{
						"data": map[string]any{
							"Get": map[string]any{
								weaviateCollection: []any{
									map[string]any{
										"cid":         "a",
										"_additional": map[string]any{"id": "uuid-a", "distance": 0.02},
									},
									map[string]any{
										"cid":         "c",
										"_additional": map[string]any{"id": "uuid-c", "distance": 0.29},
									},
								},
							},
						},
					}
				}
			}
			// Existence check query.
			return http.StatusOK, map[string]any{
				"data": map[string]any{
					"Get": map[string]any{
						weaviateCollection: []any{},
					},
				},
			}
		case method == http.MethodPost && path == "/v1/batch/objects":
			return http.StatusOK, []any{}
		}
		t.Fatalf("unexpected %s %s", method, path)
		return 500, nil
	})

	cands := []Candidate{
		{ID: "a", Vector: []float32{1, 0, 0}},
		{ID: "b", Vector: []float32{0, 1, 0}},
		{ID: "c", Vector: []float32{0.9, 0.1, 0}},
	}
	got, err := w.Rank(context.Background(), []float32{1, 0, 0}, cands, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("rank = %+v, want [a c]", got)
	}
	// Weaviate returns distance 0.02; score should be 1 - 0.02 = 0.98.
	if got[0].Score != 0.98 {
		t.Fatalf("score = %v, want 0.98 (1 - distance 0.02)", got[0].Score)
	}

	// Verify the search query carried a governance where filter with candidate IDs.
	found := false
	for _, r := range d.reqs {
		if r.path == "/v1/graphql" {
			q, _ := r.body["query"].(string)
			if strings.Contains(q, "nearVector") && strings.Contains(q, "distance") {
				if !strings.Contains(q, "where") {
					t.Fatal("search query carried no where filter — governance candidate set not enforced")
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no nearVector search query recorded")
	}
}

func TestWeaviateRankUpsertsOnlyMissing(t *testing.T) {
	var batchCount int
	w, _ := newWeaviate(t, func(method, path string, body map[string]any) (int, any) {
		switch {
		case method == http.MethodPost && path == "/v1/schema":
			return http.StatusUnprocessableEntity, map[string]any{"error": []any{map[string]any{"message": "already exists"}}}
		case method == http.MethodPost && path == "/v1/graphql":
			q, _ := body["query"].(string)
			if strings.Contains(q, "nearVector") {
				return http.StatusOK, map[string]any{
					"data": map[string]any{"Get": map[string]any{
						weaviateCollection: []any{
							map[string]any{"cid": "a", "_additional": map[string]any{"id": "uuid-a", "distance": 0.0}},
						},
					}},
				}
			}
			// Existence check: "a" already exists.
			return http.StatusOK, map[string]any{
				"data": map[string]any{"Get": map[string]any{
					weaviateCollection: []any{map[string]any{"cid": "a"}},
				}},
			}
		case method == http.MethodPost && path == "/v1/batch/objects":
			if objs, ok := body["objects"].([]any); ok {
				batchCount = len(objs)
			}
			return http.StatusOK, []any{}
		}
		t.Fatalf("unexpected %s %s", method, path)
		return 500, nil
	})
	_, err := w.Rank(context.Background(), []float32{1, 0}, []Candidate{
		{ID: "a", Vector: []float32{1, 0}}, {ID: "b", Vector: []float32{0, 1}},
	}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if batchCount != 1 {
		t.Fatalf("batch upserted %d objects, want 1 (only b — a already exists)", batchCount)
	}
}

func TestWeaviateDenyClosedOnBackendError(t *testing.T) {
	w, _ := newWeaviate(t, func(method, path string, _ map[string]any) (int, any) {
		switch {
		case path == "/v1/schema":
			return http.StatusOK, map[string]any{"class": weaviateCollection}
		case path == "/v1/graphql":
			return http.StatusOK, map[string]any{
				"data":   nil,
				"errors": []any{map[string]any{"message": "internal server error"}},
			}
		}
		return 200, nil
	})
	if _, err := w.Rank(context.Background(), []float32{1, 0}, []Candidate{{ID: "a", Vector: []float32{1, 0}}}, 5); err == nil {
		t.Fatal("a backend error must fail the rank (deny-closed), not return empty results")
	}
}

func TestWeaviateEmptyCandidates(t *testing.T) {
	w, _ := newWeaviate(t, func(_, _ string, _ map[string]any) (int, any) {
		t.Fatal("no API call expected for empty candidates")
		return 500, nil
	})
	got, err := w.Rank(context.Background(), []float32{1, 0}, nil, 5)
	if err != nil || got != nil {
		t.Fatalf("empty candidates should return nil, nil; got %+v, %v", got, err)
	}
}
