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

// pinecone backend — Pinecone Serverless and pod-based indexes via the REST API.
// It follows the same lazy-sync + candidate-id-restricted model as pgvector/Qdrant:
// candidate vectors are upserted on first encounter (chunk ids are immutable UUIDv7),
// then queries are ANN-searched RESTRICTED to the candidate set via a metadata
// filter ("_cid" $in candidateIDs).
//
// WHY A METADATA FILTER: Pinecone's /query endpoint does not support an "id IN list"
// filter on the primary vector ID. The governance invariant (the backend must never
// return a chunk outside the candidate set) requires a pre-filter, so each vector is
// stored with metadata {"_cid": "<chunkID>"} and the query carries a filter
// {"_cid": {"$in": [...]}} — Pinecone applies metadata filters BEFORE the ANN scan,
// making this governance-safe. The _cid key is reserved (underscore-prefixed).
//
// Honest limit: Pinecone metadata filter $in has a practical limit (~1000 values); for
// candidate sets larger than that, this backend falls back to a chunked strategy or
// the caller should use a backend with native id-based filtering (pgvector, Qdrant).
type pinecone struct {
	host   string // data-plane host (e.g. https://my-index-abc.svc.us-east1.pinecone.io)
	apiKey string
	ns     string // Pinecone namespace (default "")
	doer   Doer
	to     time.Duration

	mu    sync.Mutex
	ready bool
}

func openPinecone(cfg Config) (Backend, error) {
	doer := cfg.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	return &pinecone{
		host:   strings.TrimRight(cfg.DSN, "/"),
		apiKey: cfg.APIKey,
		ns:     cfg.Namespace,
		doer:   doer,
		to:     cfg.Timeout,
	}, nil
}

func (p *pinecone) Close() error { return nil }

func (p *pinecone) Rank(ctx context.Context, query []float32, candidates []Candidate, topK int) ([]Scored, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if _, err := vectorDim(query, candidates); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, p.to)
	defer cancel()
	if err := p.syncMissing(ctx, candidates); err != nil {
		return nil, err
	}

	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	body := map[string]any{
		"vector":          query,
		"topK":            capN(topK),
		"includeMetadata": false,
		"includeValues":   false,
		"filter":          map[string]any{"_cid": map[string]any{"$in": ids}},
	}
	if p.ns != "" && p.ns != DefaultNamespace {
		body["namespace"] = p.ns
	}
	var resp struct {
		Matches []struct {
			ID    string  `json:"id"`
			Score float64 `json:"score"`
		} `json:"matches"`
	}
	if err := p.call(ctx, http.MethodPost, "/query", body, &resp); err != nil {
		return nil, err
	}
	out := make([]Scored, 0, len(resp.Matches))
	for _, m := range resp.Matches {
		out = append(out, Scored{ID: m.ID, Score: m.Score})
	}
	return out, nil
}

// syncMissing fetches which candidate IDs already exist in the index, then upserts
// only the missing ones — so a warm corpus writes nothing.
func (p *pinecone) syncMissing(ctx context.Context, candidates []Candidate) error {
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	have := make(map[string]bool, len(candidates))

	// Fetch existing vectors by ID (Pinecone /vectors/fetch).
	fetchBody := map[string]any{"ids": ids}
	if p.ns != "" && p.ns != DefaultNamespace {
		fetchBody["namespace"] = p.ns
	}
	var fetchResp struct {
		Vectors map[string]any `json:"vectors"`
	}
	if err := p.call(ctx, http.MethodPost, "/vectors/fetch", fetchBody, &fetchResp); err != nil {
		return err
	}
	for id := range fetchResp.Vectors {
		have[id] = true
	}

	var vectors []map[string]any
	for _, c := range candidates {
		if !have[c.ID] {
			v := map[string]any{
				"id":       c.ID,
				"values":   c.Vector,
				"metadata": map[string]any{"_cid": c.ID},
			}
			vectors = append(vectors, v)
		}
	}
	if len(vectors) == 0 {
		return nil
	}
	upsertBody := map[string]any{"vectors": vectors}
	if p.ns != "" && p.ns != DefaultNamespace {
		upsertBody["namespace"] = p.ns
	}
	return p.call(ctx, http.MethodPost, "/vectors/upsert", upsertBody, nil)
}

// call issues one JSON request to the Pinecone data-plane and decodes the response.
func (p *pinecone) call(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("vectorindex: pinecone marshal %s: %w", path, err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.host+path, rdr)
	if err != nil {
		return fmt.Errorf("vectorindex: pinecone build %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Api-Key", p.apiKey)
	}
	resp, err := p.doer.Do(req)
	if err != nil {
		return fmt.Errorf("vectorindex: pinecone %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("vectorindex: pinecone %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(excerpt)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("vectorindex: pinecone decode %s: %w", path, err)
	}
	return nil
}
