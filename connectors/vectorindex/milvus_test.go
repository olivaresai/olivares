// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vectorindex

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func newMilvus(t *testing.T, fn func(method, path string, body map[string]any) (int, any)) (*milvus, *fakeDoer) {
	t.Helper()
	d := &fakeDoer{fn: fn}
	b, err := openMilvus(Config{Kind: "milvus", DSN: "http://milvus:19530", Namespace: DefaultNamespace, Timeout: defaultTimeout, Doer: d})
	if err != nil {
		t.Fatal(err)
	}
	return b.(*milvus), d
}

func TestMilvusRankHonorsCandidateSetAndMapsScores(t *testing.T) {
	m, d := newMilvus(t, func(method, path string, body map[string]any) (int, any) {
		switch {
		case method == http.MethodPost && path == "/v2/vectordb/collections/create":
			return http.StatusOK, map[string]any{"code": 0}
		case method == http.MethodPost && path == "/v2/vectordb/entities/get":
			return http.StatusOK, map[string]any{"code": 0, "data": []any{}}
		case method == http.MethodPost && path == "/v2/vectordb/entities/upsert":
			return http.StatusOK, map[string]any{"code": 0}
		case method == http.MethodPost && path == "/v2/vectordb/entities/search":
			return http.StatusOK, map[string]any{
				"code": 0,
				"data": []any{
					map[string]any{"id": "a", "distance": 0.98},
					map[string]any{"id": "c", "distance": 0.71},
				},
			}
		}
		t.Fatalf("unexpected %s %s", method, path)
		return 500, nil
	})

	cands := []Candidate{
		{ID: "a", Vector: []float32{1, 0, 0}},
		{ID: "b", Vector: []float32{0, 1, 0}},
		{ID: "c", Vector: []float32{0.9, 0.1, 0}},
	}
	got, err := m.Rank(context.Background(), []float32{1, 0, 0}, cands, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("rank = %+v, want [a c]", got)
	}
	if got[0].Score != 0.98 {
		t.Fatalf("score not mapped through: %v", got[0].Score)
	}

	// Verify the search carried a governance filter restricting to candidate IDs.
	for i := len(d.reqs) - 1; i >= 0; i-- {
		if d.reqs[i].path == "/v2/vectordb/entities/search" {
			filter, ok := d.reqs[i].body["filter"].(string)
			if !ok || filter == "" {
				t.Fatal("search request carried no filter — governance candidate set not enforced")
			}
			if !strings.Contains(filter, `"a"`) || !strings.Contains(filter, `"b"`) || !strings.Contains(filter, `"c"`) {
				t.Fatalf("filter %q does not contain all candidate IDs", filter)
			}
			if !strings.HasPrefix(filter, "id in [") {
				t.Fatalf("filter %q does not start with 'id in ['", filter)
			}
			break
		}
	}
}

func TestMilvusRankUpsertsOnlyMissing(t *testing.T) {
	var upsertedCount int
	m, _ := newMilvus(t, func(method, path string, body map[string]any) (int, any) {
		switch {
		case path == "/v2/vectordb/collections/create":
			return http.StatusOK, map[string]any{"code": 65535, "message": "collection already exists"}
		case path == "/v2/vectordb/entities/get":
			return http.StatusOK, map[string]any{
				"code": 0,
				"data": []any{map[string]any{"id": "a"}},
			}
		case path == "/v2/vectordb/entities/upsert":
			if data, ok := body["data"].([]any); ok {
				upsertedCount = len(data)
			}
			return http.StatusOK, map[string]any{"code": 0}
		case path == "/v2/vectordb/entities/search":
			return http.StatusOK, map[string]any{
				"code": 0,
				"data": []any{map[string]any{"id": "a", "distance": 1.0}},
			}
		}
		t.Fatalf("unexpected %s %s", method, path)
		return 500, nil
	})
	_, err := m.Rank(context.Background(), []float32{1, 0}, []Candidate{
		{ID: "a", Vector: []float32{1, 0}}, {ID: "b", Vector: []float32{0, 1}},
	}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if upsertedCount != 1 {
		t.Fatalf("upserted %d vectors, want 1 (only b — a already exists)", upsertedCount)
	}
}

func TestMilvusDenyClosedOnBackendError(t *testing.T) {
	m, _ := newMilvus(t, func(method, path string, _ map[string]any) (int, any) {
		switch {
		case path == "/v2/vectordb/collections/create":
			return http.StatusOK, map[string]any{"code": 0}
		case path == "/v2/vectordb/entities/get":
			return http.StatusOK, map[string]any{"code": 0, "data": []any{}}
		case path == "/v2/vectordb/entities/upsert":
			return http.StatusOK, map[string]any{"code": 0}
		case path == "/v2/vectordb/entities/search":
			return http.StatusOK, map[string]any{"code": 1, "message": "internal error"}
		}
		return 200, nil
	})
	if _, err := m.Rank(context.Background(), []float32{1, 0}, []Candidate{{ID: "a", Vector: []float32{1, 0}}}, 5); err == nil {
		t.Fatal("a backend error must fail the rank (deny-closed), not return empty results")
	}
}

func TestMilvusEmptyCandidates(t *testing.T) {
	m, _ := newMilvus(t, func(_, _ string, _ map[string]any) (int, any) {
		t.Fatal("no API call expected for empty candidates")
		return 500, nil
	})
	got, err := m.Rank(context.Background(), []float32{1, 0}, nil, 5)
	if err != nil || got != nil {
		t.Fatalf("empty candidates should return nil, nil; got %+v, %v", got, err)
	}
}

func TestMilvusIDFilterEscaping(t *testing.T) {
	cands := []Candidate{
		{ID: `normal-id`},
		{ID: `id"with"quotes`},
		{ID: `id\with\backslash`},
	}
	got := milvusIDFilter(cands)
	want := `id in ["normal-id","id\"with\"quotes","id\\with\\backslash"]`
	if got != want {
		t.Fatalf("milvusIDFilter =\n  %s\nwant\n  %s", got, want)
	}
}
