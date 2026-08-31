// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vectorindex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// qdrant backend (optional) — the same persistent-HNSW + candidate-id-filter model as
// pgvector, over Qdrant's REST API. It ensures a cosine collection, lazily upserts the
// candidate vectors not already present, then searches the query RESTRICTED to the
// candidate ids via a `has_id` filter — so a point the identity may not retrieve is
// never returned. It speaks plain HTTP (an injectable Doer) so it is fully unit-
// testable without a live Qdrant. It never logs the api-key.
type qdrant struct {
	base       string
	collection string
	apiKey     string
	doer       Doer
	to         time.Duration

	mu    sync.Mutex
	ready bool
	dim   int
}

func openQdrant(cfg Config) (Backend, error) {
	doer := cfg.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	return &qdrant{
		base:       strings.TrimRight(cfg.DSN, "/"),
		collection: cfg.Namespace,
		apiKey:     cfg.APIKey,
		doer:       doer,
		to:         cfg.Timeout,
		dim:        cfg.Dim,
	}, nil
}

func (q *qdrant) Close() error { return nil }

func (q *qdrant) Rank(ctx context.Context, query []float32, candidates []Candidate, topK int) ([]Scored, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	dim, err := vectorDim(query, candidates)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, q.to)
	defer cancel()
	if err := q.ensureCollection(ctx, dim); err != nil {
		return nil, err
	}
	if err := q.syncMissing(ctx, candidates); err != nil {
		return nil, err
	}

	ids := make([]any, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	body := map[string]any{
		"vector":       query,
		"limit":        capN(topK),
		"with_payload": false,
		"with_vector":  false,
		// Restrict the search to the governance candidate set (the allow-list).
		"filter": map[string]any{"must": []any{map[string]any{"has_id": ids}}},
	}
	var resp struct {
		Result []struct {
			ID    json.RawMessage `json:"id"`
			Score float64         `json:"score"`
		} `json:"result"`
	}
	if err := q.call(ctx, http.MethodPost, "/collections/"+q.collection+"/points/search", body, &resp); err != nil {
		return nil, err
	}
	out := make([]Scored, 0, len(resp.Result))
	for _, r := range resp.Result {
		out = append(out, Scored{ID: unquoteID(r.ID), Score: r.Score})
	}
	return out, nil
}

// ensureCollection idempotently creates the cosine collection on first use. Qdrant
// returns 200 on create and 409 if it already exists; both are success here.
func (q *qdrant) ensureCollection(ctx context.Context, dim int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ready {
		if q.dim != dim {
			return fmt.Errorf("vectorindex: qdrant collection %q built for dim %d, got %d", q.collection, q.dim, dim)
		}
		return nil
	}
	body := map[string]any{
		"vectors": map[string]any{"size": dim, "distance": "Cosine"},
	}
	if err := q.call(ctx, http.MethodPut, "/collections/"+q.collection, body, nil); err != nil {
		// A 409 (already exists) is reported by call as an error; tolerate it.
		if !strings.Contains(err.Error(), "status 409") {
			return err
		}
	}
	q.ready, q.dim = true, dim
	return nil
}

// syncMissing upserts the candidate vectors not already present. It retrieves which
// candidate ids exist, then upserts only the missing points — so a warm corpus writes
// nothing and the wire never re-sends an indexed vector.
func (q *qdrant) syncMissing(ctx context.Context, candidates []Candidate) error {
	ids := make([]any, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	var got struct {
		Result []struct {
			ID json.RawMessage `json:"id"`
		} `json:"result"`
	}
	if err := q.call(ctx, http.MethodPost, "/collections/"+q.collection+"/points",
		map[string]any{"ids": ids, "with_payload": false, "with_vector": false}, &got); err != nil {
		return err
	}
	have := make(map[string]bool, len(got.Result))
	for _, r := range got.Result {
		have[unquoteID(r.ID)] = true
	}

	var points []map[string]any
	for _, c := range candidates {
		if !have[c.ID] {
			points = append(points, map[string]any{"id": c.ID, "vector": c.Vector})
		}
	}
	if len(points) == 0 {
		return nil
	}
	return q.call(ctx, http.MethodPut, "/collections/"+q.collection+"/points?wait=true",
		map[string]any{"points": points}, nil)
}

// call issues one JSON request and decodes the response into out (out may be nil). A
// non-2xx status is an error carrying a bounded body excerpt — never the api-key.
func (q *qdrant) call(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("vectorindex: qdrant marshal %s: %w", path, err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, q.base+path, rdr)
	if err != nil {
		return fmt.Errorf("vectorindex: qdrant build %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if q.apiKey != "" {
		req.Header.Set("api-key", q.apiKey)
	}
	resp, err := q.doer.Do(req)
	if err != nil {
		return fmt.Errorf("vectorindex: qdrant %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("vectorindex: qdrant %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(excerpt)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("vectorindex: qdrant decode %s: %w", path, err)
	}
	return nil
}

// unquoteID renders a Qdrant point id (a JSON string or number) back to its string
// form (the module's chunk id is a UUID string).
func unquoteID(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var out string
		if err := json.Unmarshal(raw, &out); err == nil {
			return out
		}
		return s[1 : len(s)-1]
	}
	return s
}
