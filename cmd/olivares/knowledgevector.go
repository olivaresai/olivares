// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/vectorindex"
	"github.com/olivaresai/olivares/modules/knowledge"
)

// This is the AGPL composition-root bridge that puts a production ANN vector backend
// (connectors/vectorindex, Apache — pgvector or Qdrant) behind module VIII's
// knowledge.VectorIndex seam. It lives in /cmd because it bridges the AGPL
// module port (knowledge.VectorCandidate / ScoredChunk) to the Apache connector — a
// connector never imports /modules, so the bridge cannot live in the connector.
//
// GOVERNANCE INVARIANT (enforced on BOTH sides): the module hands the backend ONLY the
// classification/ACL/residency-filtered candidate set; the backend restricts its
// search to those ids; and this adapter ADDITIONALLY drops any result id not in the
// candidate set — so even a buggy/compromised backend can never surface a chunk the
// identity may not retrieve. Swapping the index never changes the governance contract.
//
// DENY-CLOSED: a configured-but-unreachable backend returns an error from Rank, which
// the module turns into a 502 (retrieval.go) — never a silent fallback to a different,
// untrusted index. The in-process cosineIndex remains ONLY as the air-gap default when
// NO backend is configured (loadVectorIndex returns ok=false).
type vectorIndexAdapter struct {
	backend vectorindex.Backend
}

var _ knowledge.VectorIndex = (*vectorIndexAdapter)(nil)

func (a *vectorIndexAdapter) Rank(ctx context.Context, query []float32, candidates []knowledge.VectorCandidate, topK int) ([]knowledge.ScoredChunk, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	allowed := make(map[string]bool, len(candidates))
	cands := make([]vectorindex.Candidate, len(candidates))
	for i, c := range candidates {
		cands[i] = vectorindex.Candidate{ID: c.ChunkID, Vector: c.Vector}
		allowed[c.ChunkID] = true
	}
	scored, err := a.backend.Rank(ctx, query, cands, topK)
	if err != nil {
		return nil, err
	}
	out := make([]knowledge.ScoredChunk, 0, len(scored))
	for _, s := range scored {
		if !allowed[s.ID] {
			continue // belt-and-suspenders: never return a chunk outside the candidate set
		}
		out = append(out, knowledge.ScoredChunk{ChunkID: s.ID, Score: s.Score})
	}
	return out, nil
}

// Close releases the backend (its connection pool). Best-effort on shutdown.
func (a *vectorIndexAdapter) Close() error {
	if a.backend != nil {
		return a.backend.Close()
	}
	return nil
}

// loadVectorIndex builds the ANN VectorIndex adapter from env, or returns ok=false so
// the module keeps its in-process exact cosineIndex (the correct self-host/air-gap
// default). A configured-but-invalid backend WARNS and stays unwired — never a boot
// failure, and never a silent ungoverned index.
//
//	OLIVARES_VECTOR_BACKEND   "pgvector" | "qdrant" | "pinecone" | "weaviate" | "milvus"
//	                          (empty = in-process cosine)
//	OLIVARES_VECTOR_DSN       pgvector: a libpq/pgx URL (may be the core's Postgres);
//	                          qdrant: the REST base URL (e.g. http://qdrant:6333);
//	                          pinecone: the index data-plane URL;
//	                          weaviate: the REST base URL (e.g. http://weaviate:8080);
//	                          milvus: the REST base URL (e.g. http://milvus:19530)
//	OLIVARES_VECTOR_NAMESPACE table/collection name (default "knowledge_ann");
//	                          weaviate: used as tenant name for multi-tenancy
//	OLIVARES_VECTOR_API_KEY   qdrant api-key, pinecone api-key, weaviate bearer token,
//	                          or milvus bearer token (optional per backend)
//	OLIVARES_VECTOR_DIM       embedding dimension (optional; inferred otherwise)
//	OLIVARES_VECTOR_TIMEOUT   per-operation timeout (Go duration; optional)
func loadVectorIndex(getenv func(string) string, log *slog.Logger) (*vectorIndexAdapter, bool) {
	kind := strings.TrimSpace(getenv("OLIVARES_VECTOR_BACKEND"))
	if kind == "" {
		return nil, false
	}
	dsn := strings.TrimSpace(getenv("OLIVARES_VECTOR_DSN"))
	if dsn == "" {
		log.Warn("knowledge: OLIVARES_VECTOR_BACKEND set without OLIVARES_VECTOR_DSN; keeping the in-process cosine index", "backend", kind)
		return nil, false
	}
	dim, _ := strconv.Atoi(strings.TrimSpace(getenv("OLIVARES_VECTOR_DIM")))
	to, _ := time.ParseDuration(strings.TrimSpace(getenv("OLIVARES_VECTOR_TIMEOUT")))
	backend, err := vectorindex.Open(vectorindex.Config{
		Kind:      kind,
		DSN:       dsn,
		Namespace: strings.TrimSpace(getenv("OLIVARES_VECTOR_NAMESPACE")),
		APIKey:    strings.TrimSpace(getenv("OLIVARES_VECTOR_API_KEY")),
		Dim:       dim,
		Timeout:   to,
	})
	if err != nil {
		log.Warn("knowledge: invalid OLIVARES_VECTOR_BACKEND config; keeping the in-process cosine index (never a silent ungoverned index)", "backend", kind, "err", err)
		return nil, false
	}
	log.Info("knowledge: wired ANN vector backend; a configured-but-down backend fails retrieval deny-closed (no silent fallback to the in-process index)", "backend", kind)
	return &vectorIndexAdapter{backend: backend}, true
}

// knowledgeVectorOptions returns the WithVectorIndex option to apply, or nil when no
// backend is configured (so buildModules keeps the in-process cosineIndex).
func knowledgeVectorOptions(getenv func(string) string, log *slog.Logger) ([]knowledge.Option, *vectorIndexAdapter) {
	idx, ok := loadVectorIndex(getenv, log)
	if !ok {
		return nil, nil
	}
	return []knowledge.Option{knowledge.WithVectorIndex(idx)}, idx
}
