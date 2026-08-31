// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vectorindex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeDoer is an in-memory Qdrant REST endpoint: it records every request and returns
// a programmable (status, payload) per (method, path). It lets the qdrant backend be
// fully unit-tested without a live Qdrant.
type fakeDoer struct {
	reqs []recordedReq
	fn   func(method, path string, body map[string]any) (int, any)
}

type recordedReq struct {
	method string
	path   string
	query  string
	body   map[string]any
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	var body map[string]any
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(b, &body)
	}
	f.reqs = append(f.reqs, recordedReq{method: req.Method, path: req.URL.Path, query: req.URL.RawQuery, body: body})
	status, payload := f.fn(req.Method, req.URL.Path, body)
	pb, _ := json.Marshal(payload)
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(pb)), Header: make(http.Header)}, nil
}

func (f *fakeDoer) lastSearchFilterIDs(t *testing.T) []string {
	t.Helper()
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if strings.HasSuffix(f.reqs[i].path, "/points/search") {
			filter, _ := f.reqs[i].body["filter"].(map[string]any)
			must, _ := filter["must"].([]any)
			if len(must) == 0 {
				t.Fatal("search request carried no has_id filter — governance candidate set not enforced")
			}
			cond, _ := must[0].(map[string]any)
			ids, _ := cond["has_id"].([]any)
			out := make([]string, len(ids))
			for j, id := range ids {
				out[j], _ = id.(string)
			}
			return out
		}
	}
	t.Fatal("no search request recorded")
	return nil
}

func newQdrant(t *testing.T, fn func(method, path string, body map[string]any) (int, any)) (*qdrant, *fakeDoer) {
	t.Helper()
	d := &fakeDoer{fn: fn}
	b, err := openQdrant(Config{Kind: "qdrant", DSN: "http://qdrant:6333", Namespace: DefaultNamespace, Timeout: defaultTimeout, Doer: d})
	if err != nil {
		t.Fatal(err)
	}
	return b.(*qdrant), d
}

func TestQdrantRankHonorsCandidateSetAndMapsScores(t *testing.T) {
	// The fake reports no existing points (so the backend upserts all), then returns a
	// ranked subset for search. The test asserts the search RESTRICTED to the candidate
	// ids (has_id) and that scores/order map straight through.
	q, d := newQdrant(t, func(method, path string, _ map[string]any) (int, any) {
		switch {
		case method == http.MethodPut && path == "/collections/"+DefaultNamespace:
			return http.StatusOK, map[string]any{"result": true}
		case method == http.MethodPost && path == "/collections/"+DefaultNamespace+"/points":
			return http.StatusOK, map[string]any{"result": []any{}} // none exist yet
		case method == http.MethodPut && path == "/collections/"+DefaultNamespace+"/points":
			return http.StatusOK, map[string]any{"result": map[string]any{"status": "completed"}}
		case method == http.MethodPost && path == "/collections/"+DefaultNamespace+"/points/search":
			return http.StatusOK, map[string]any{"result": []any{
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
	got, err := q.Rank(context.Background(), []float32{1, 0, 0}, cands, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("rank = %+v, want [a c]", got)
	}
	if got[0].Score != 0.98 {
		t.Fatalf("score not mapped through: %v", got[0].Score)
	}
	// The search was restricted to exactly the candidate set (governance allow-list).
	ids := d.lastSearchFilterIDs(t)
	if len(ids) != 3 {
		t.Fatalf("has_id filter = %v, want the 3 candidates", ids)
	}
}

func TestQdrantRankUpsertsOnlyMissing(t *testing.T) {
	// "a" already exists; only "b" should be upserted (lazy sync, write-light).
	var upserted []string
	q, _ := newQdrant(t, func(method, path string, body map[string]any) (int, any) {
		switch {
		case method == http.MethodPut && path == "/collections/"+DefaultNamespace:
			return http.StatusConflict, map[string]any{"status": map[string]any{"error": "already exists"}}
		case method == http.MethodPost && path == "/collections/"+DefaultNamespace+"/points":
			return http.StatusOK, map[string]any{"result": []any{map[string]any{"id": "a"}}}
		case method == http.MethodPut && path == "/collections/"+DefaultNamespace+"/points":
			for _, p := range body["points"].([]any) {
				upserted = append(upserted, p.(map[string]any)["id"].(string))
			}
			return http.StatusOK, map[string]any{"result": map[string]any{"status": "completed"}}
		case method == http.MethodPost && path == "/collections/"+DefaultNamespace+"/points/search":
			return http.StatusOK, map[string]any{"result": []any{map[string]any{"id": "a", "score": 1.0}}}
		}
		t.Fatalf("unexpected %s %s", method, path)
		return 500, nil
	})
	_, err := q.Rank(context.Background(), []float32{1, 0}, []Candidate{
		{ID: "a", Vector: []float32{1, 0}}, {ID: "b", Vector: []float32{0, 1}},
	}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(upserted) != 1 || upserted[0] != "b" {
		t.Fatalf("upserted = %v, want only [b] (a already indexed)", upserted)
	}
}

func TestQdrantDenyClosedOnBackendError(t *testing.T) {
	// A 5xx from search must surface as an error (deny-closed) — never empty results.
	q, _ := newQdrant(t, func(method, path string, _ map[string]any) (int, any) {
		switch {
		case strings.HasSuffix(path, "/points/search"):
			return http.StatusInternalServerError, map[string]any{"status": map[string]any{"error": "boom"}}
		case method == http.MethodPost && strings.HasSuffix(path, "/points"):
			return http.StatusOK, map[string]any{"result": []any{}}
		default:
			return http.StatusOK, map[string]any{"result": true}
		}
	})
	if _, err := q.Rank(context.Background(), []float32{1, 0}, []Candidate{{ID: "a", Vector: []float32{1, 0}}}, 5); err == nil {
		t.Fatal("a backend error must fail the rank (deny-closed), not return empty results")
	}
}
