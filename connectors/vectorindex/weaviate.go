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

// weaviate backend — Weaviate v4 via REST (batch CRUD) and GraphQL (nearVector
// search). It follows the same lazy-sync + candidate-id-restricted model as the
// other backends: candidate vectors are batch-upserted into a collection as
// objects whose deterministic UUID is derived from the chunk id, then queries are
// ANN-searched RESTRICTED to the candidate set via a GraphQL where filter.
//
// GOVERNANCE FILTER: each object stores the chunk id as a text property "cid".
// The nearVector query carries a where filter with operator "Or" over individual
// "Equal" conditions on "cid" (one per candidate). Weaviate applies this as a
// pre-filter (inverted index on text properties), so the ANN search only
// considers governance-allowed vectors. This is correct for candidate sets up to
// the thousands (typical for governance-filtered retrieval); for extremely large
// sets a backend with native has_id filtering (pgvector, Qdrant) is preferable.
//
// MULTI-TENANCY: if Config.Namespace is set, it is passed as the tenant header
// (X-Weaviate-Tenant) on every request, enabling Weaviate's native multi-tenant
// isolation. The collection name is fixed to "KnowledgeAnn" (Weaviate requires
// PascalCase class names).
type weaviate struct {
	base       string
	collection string
	apiKey     string
	oidcToken  string
	tenant     string
	doer       Doer
	to         time.Duration

	mu    sync.Mutex
	ready bool
	dim   int
}

const weaviateCollection = "KnowledgeAnn"

func openWeaviate(cfg Config) (Backend, error) {
	doer := cfg.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	tenant := ""
	if cfg.Namespace != "" && cfg.Namespace != DefaultNamespace {
		tenant = cfg.Namespace
	}
	return &weaviate{
		base:       strings.TrimRight(cfg.DSN, "/"),
		collection: weaviateCollection,
		apiKey:     cfg.APIKey,
		tenant:     tenant,
		doer:       doer,
		to:         cfg.Timeout,
		dim:        cfg.Dim,
	}, nil
}

func (w *weaviate) Close() error { return nil }

func (w *weaviate) Rank(ctx context.Context, query []float32, candidates []Candidate, topK int) ([]Scored, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	dim, err := vectorDim(query, candidates)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, w.to)
	defer cancel()
	if err := w.ensureCollection(ctx, dim); err != nil {
		return nil, err
	}
	if err := w.syncMissing(ctx, candidates); err != nil {
		return nil, err
	}

	// Build GraphQL nearVector query with governance where filter.
	orConds := make([]map[string]any, len(candidates))
	for i, c := range candidates {
		orConds[i] = map[string]any{
			"path":      []string{"cid"},
			"operator":  "Equal",
			"valueText": c.ID,
		}
	}
	var whereFilter map[string]any
	if len(orConds) == 1 {
		whereFilter = orConds[0]
	} else {
		whereFilter = map[string]any{
			"operator": "Or",
			"operands": orConds,
		}
	}

	gql := map[string]any{
		"query": fmt.Sprintf(`{
			Get {
				%s(
					nearVector: {vector: %s}
					limit: %d
					where: %s
				) {
					cid
					_additional { id distance }
				}
			}
		}`, w.collection, encodeVector(query), capN(topK), mustJSON(whereFilter)),
	}

	var resp struct {
		Data struct {
			Get map[string][]struct {
				Cid        string `json:"cid"`
				Additional struct {
					ID       string  `json:"id"`
					Distance float64 `json:"distance"`
				} `json:"_additional"`
			} `json:"Get"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := w.call(ctx, http.MethodPost, "/v1/graphql", gql, &resp); err != nil {
		return nil, err
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("vectorindex: weaviate graphql: %s", resp.Errors[0].Message)
	}

	results := resp.Data.Get[w.collection]
	out := make([]Scored, 0, len(results))
	for _, r := range results {
		// Weaviate returns cosine DISTANCE (0 = identical); convert to SIMILARITY
		// (1 - distance) to match the seam's score semantics.
		out = append(out, Scored{ID: r.Cid, Score: 1 - r.Additional.Distance})
	}
	return out, nil
}

// ensureCollection creates the Weaviate class with a cosine vectorizer config
// and the "cid" text property, idempotently. A 422 (class exists) is tolerated.
func (w *weaviate) ensureCollection(ctx context.Context, dim int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ready {
		if w.dim != 0 && w.dim != dim {
			return fmt.Errorf("vectorindex: weaviate collection %q built for dim %d, got %d", w.collection, w.dim, dim)
		}
		return nil
	}
	body := map[string]any{
		"class": w.collection,
		"vectorIndexConfig": map[string]any{
			"distance": "cosine",
		},
		"properties": []map[string]any{
			{
				"name":     "cid",
				"dataType": []string{"text"},
			},
		},
	}
	if w.tenant != "" {
		body["multiTenancyConfig"] = map[string]any{"enabled": true}
	}
	if err := w.call(ctx, http.MethodPost, "/v1/schema", body, nil); err != nil {
		if !strings.Contains(err.Error(), "status 422") {
			return err
		}
	}
	w.ready = true
	if dim > 0 {
		w.dim = dim
	}
	return nil
}

// syncMissing checks which candidate chunk IDs already exist and batch-upserts
// only the missing ones.
func (w *weaviate) syncMissing(ctx context.Context, candidates []Candidate) error {
	// Check which IDs exist via a GraphQL aggregate or a batch get. For simplicity
	// and consistency with the other backends, we do a targeted GraphQL query for
	// existing cids.
	orConds := make([]map[string]any, len(candidates))
	for i, c := range candidates {
		orConds[i] = map[string]any{
			"path":      []string{"cid"},
			"operator":  "Equal",
			"valueText": c.ID,
		}
	}
	var whereFilter map[string]any
	if len(orConds) == 1 {
		whereFilter = orConds[0]
	} else {
		whereFilter = map[string]any{
			"operator": "Or",
			"operands": orConds,
		}
	}

	gql := map[string]any{
		"query": fmt.Sprintf(`{
			Get {
				%s(
					limit: %d
					where: %s
				) {
					cid
				}
			}
		}`, w.collection, len(candidates), mustJSON(whereFilter)),
	}
	var resp struct {
		Data struct {
			Get map[string][]struct {
				Cid string `json:"cid"`
			} `json:"Get"`
		} `json:"data"`
	}
	if err := w.call(ctx, http.MethodPost, "/v1/graphql", gql, &resp); err != nil {
		return err
	}
	have := make(map[string]bool, len(resp.Data.Get[w.collection]))
	for _, r := range resp.Data.Get[w.collection] {
		have[r.Cid] = true
	}

	var objects []map[string]any
	for _, c := range candidates {
		if !have[c.ID] {
			obj := map[string]any{
				"class":      w.collection,
				"properties": map[string]any{"cid": c.ID},
				"vector":     c.Vector,
			}
			if w.tenant != "" {
				obj["tenant"] = w.tenant
			}
			objects = append(objects, obj)
		}
	}
	if len(objects) == 0 {
		return nil
	}
	return w.call(ctx, http.MethodPost, "/v1/batch/objects", map[string]any{"objects": objects}, nil)
}

// call issues one JSON request to Weaviate and decodes the response.
func (w *weaviate) call(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("vectorindex: weaviate marshal %s: %w", path, err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.base+path, rdr)
	if err != nil {
		return fmt.Errorf("vectorindex: weaviate build %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if w.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+w.apiKey)
	}
	if w.tenant != "" {
		req.Header.Set("X-Weaviate-Tenant", w.tenant)
	}
	resp, err := w.doer.Do(req)
	if err != nil {
		return fmt.Errorf("vectorindex: weaviate %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("vectorindex: weaviate %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(excerpt)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("vectorindex: weaviate decode %s: %w", path, err)
	}
	return nil
}

// mustJSON marshals v to a JSON string, panicking on error (only used for
// compile-time-safe map literals in GraphQL query interpolation).
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic("vectorindex: mustJSON: " + err.Error())
	}
	return string(b)
}
