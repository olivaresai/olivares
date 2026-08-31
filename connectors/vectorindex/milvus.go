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

// milvus backend — Milvus v2 (2.3+) via the HTTP REST API. It follows the same
// lazy-sync + candidate-id-restricted model as the other backends: candidate
// vectors are upserted into a collection (schema: varchar PK "id" + float vector
// "embedding"), then queries are ANN-searched RESTRICTED to the candidate set via
// a boolean filter expression "id in [...]".
//
// WHY HTTP REST (not the gRPC SDK): consistency with the Pinecone/Weaviate
// backends (HTTP-only, no extra SDK/protobuf/gRPC deps); the REST API is stable
// since Milvus 2.3 and covers all operations this backend needs (create
// collection, upsert, search, get). The governance filter "id in [...]" is
// applied as a boolean expression in the Search request, which Milvus evaluates
// DURING the ANN scan (filtered search), so the backend never returns a chunk
// outside the candidate set.
//
// Honest limit: the "id in [...]" filter expression has a practical length limit
// (the expression is a string; very large candidate sets produce long expressions
// that may hit Milvus's expression parser limits). For candidate sets larger than
// ~10K, a backend with native array-based ID filtering is preferable.
type milvus struct {
	base       string
	collection string
	apiKey     string
	doer       Doer
	to         time.Duration

	mu    sync.Mutex
	ready bool
	dim   int
}

func openMilvus(cfg Config) (Backend, error) {
	doer := cfg.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	return &milvus{
		base:       strings.TrimRight(cfg.DSN, "/"),
		collection: cfg.Namespace,
		apiKey:     cfg.APIKey,
		doer:       doer,
		to:         cfg.Timeout,
		dim:        cfg.Dim,
	}, nil
}

func (m *milvus) Close() error { return nil }

func (m *milvus) Rank(ctx context.Context, query []float32, candidates []Candidate, topK int) ([]Scored, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	dim, err := vectorDim(query, candidates)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, m.to)
	defer cancel()
	if err := m.ensureCollection(ctx, dim); err != nil {
		return nil, err
	}
	if err := m.syncMissing(ctx, candidates); err != nil {
		return nil, err
	}

	// Build the governance filter: id in ["a","b","c"]
	filter := milvusIDFilter(candidates)

	body := map[string]any{
		"collectionName": m.collection,
		"data":           [][]float32{query},
		"annsField":      "embedding",
		"limit":          capN(topK),
		"outputFields":   []string{"id"},
		"filter":         filter,
	}
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    []struct {
			ID       string  `json:"id"`
			Distance float64 `json:"distance"`
		} `json:"data"`
	}
	if err := m.call(ctx, http.MethodPost, "/v2/vectordb/entities/search", body, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("vectorindex: milvus search: code %d: %s", resp.Code, resp.Message)
	}
	out := make([]Scored, 0, len(resp.Data))
	for _, d := range resp.Data {
		// Milvus COSINE metric returns a similarity score in [0,1] (1 = identical)
		// when using the COSINE metric type. With IP or L2 the semantics differ,
		// but this backend creates collections with COSINE.
		out = append(out, Scored{ID: d.ID, Score: d.Distance})
	}
	return out, nil
}

// ensureCollection creates the Milvus collection with a varchar PK and a float
// vector field, idempotently. The collection uses the COSINE metric and
// AUTOINDEX (Milvus selects the appropriate index type).
func (m *milvus) ensureCollection(ctx context.Context, dim int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ready {
		if m.dim != 0 && m.dim != dim {
			return fmt.Errorf("vectorindex: milvus collection %q built for dim %d, got %d", m.collection, m.dim, dim)
		}
		return nil
	}
	body := map[string]any{
		"collectionName": m.collection,
		"schema": map[string]any{
			"autoId": false,
			"fields": []map[string]any{
				{
					"fieldName": "id",
					"dataType":  "VarChar",
					"isPrimary": true,
					"elementTypeParams": map[string]any{
						"max_length": "128",
					},
				},
				{
					"fieldName": "embedding",
					"dataType":  "FloatVector",
					"elementTypeParams": map[string]any{
						"dim": fmt.Sprintf("%d", dim),
					},
				},
			},
		},
		"indexParams": []map[string]any{
			{
				"fieldName":  "embedding",
				"indexName":  "embedding_idx",
				"metricType": "COSINE",
			},
		},
	}
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := m.call(ctx, http.MethodPost, "/v2/vectordb/collections/create", body, &resp); err != nil {
		return err
	}
	// Code 0 = success; code 65535 = "collection already exists" — both OK.
	if resp.Code != 0 && !strings.Contains(strings.ToLower(resp.Message), "already exist") {
		return fmt.Errorf("vectorindex: milvus create collection: code %d: %s", resp.Code, resp.Message)
	}
	m.ready = true
	if dim > 0 {
		m.dim = dim
	}
	return nil
}

// syncMissing checks which candidate IDs already exist (via get-by-id) and
// upserts only the missing ones.
func (m *milvus) syncMissing(ctx context.Context, candidates []Candidate) error {
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	var getResp struct {
		Code int `json:"code"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := m.call(ctx, http.MethodPost, "/v2/vectordb/entities/get", map[string]any{
		"collectionName": m.collection,
		"id":             ids,
		"outputFields":   []string{"id"},
	}, &getResp); err != nil {
		return err
	}
	have := make(map[string]bool, len(getResp.Data))
	for _, d := range getResp.Data {
		have[d.ID] = true
	}

	var data []map[string]any
	for _, c := range candidates {
		if !have[c.ID] {
			data = append(data, map[string]any{
				"id":        c.ID,
				"embedding": c.Vector,
			})
		}
	}
	if len(data) == 0 {
		return nil
	}
	var upsertResp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := m.call(ctx, http.MethodPost, "/v2/vectordb/entities/upsert", map[string]any{
		"collectionName": m.collection,
		"data":           data,
	}, &upsertResp); err != nil {
		return err
	}
	if upsertResp.Code != 0 {
		return fmt.Errorf("vectorindex: milvus upsert: code %d: %s", upsertResp.Code, upsertResp.Message)
	}
	return nil
}

// call issues one JSON request to the Milvus REST API and decodes the response.
func (m *milvus) call(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("vectorindex: milvus marshal %s: %w", path, err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, rdr)
	if err != nil {
		return fmt.Errorf("vectorindex: milvus build %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}
	resp, err := m.doer.Do(req)
	if err != nil {
		return fmt.Errorf("vectorindex: milvus %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("vectorindex: milvus %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(excerpt)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("vectorindex: milvus decode %s: %w", path, err)
	}
	return nil
}

// milvusIDFilter builds a Milvus boolean filter expression that restricts the
// search to the supplied candidate IDs: id in ["a","b","c"]. The chunk ids are
// quoted and escaped to prevent expression injection.
func milvusIDFilter(candidates []Candidate) string {
	var b strings.Builder
	b.WriteString("id in [")
	for i, c := range candidates {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(milvusEscape(c.ID))
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return b.String()
}

// milvusEscape escapes a string for safe inclusion in a Milvus boolean expression
// string literal (double-quoted). It escapes backslashes and double quotes.
func milvusEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
