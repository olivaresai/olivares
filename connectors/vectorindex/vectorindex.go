// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package vectorindex is the Apache-licensed ANN (approximate nearest neighbor)
// backend client behind module VIII's knowledge.VectorIndex seam. It ranks a
// GOVERNANCE-PRE-FILTERED candidate set against a query vector using a production
// vector engine — pgvector (the self-host/air-gap default, on the core's Postgres),
// Qdrant, Pinecone, Weaviate or Milvus — instead of the module's in-process exact
// cosine scan.
//
// LICENSE BOUNDARY (LICENSING.md): this package is Apache-2.0 and imports ONLY third-party
// drivers and the standard library — never /core or /modules. The AGPL bridge that
// adapts it to the module's knowledge.VectorIndex port (which speaks core/model
// types) lives in /cmd/olivares (the same place claudeEmbedderAdapter lives).
//
// GOVERNANCE INVARIANT (non-negotiable): the module pre-filters candidates by
// classification/ACL/residency BEFORE ranking and hands this backend ONLY the
// retrievable chunks. Every backend RESTRICTS its result to the supplied candidate
// set (pgvector: `WHERE id = ANY($ids)`; Qdrant: a `has_id` filter), so an external
// engine NEVER returns a chunk the identity may not retrieve. Swapping the index does
// not change the governance contract.
//
// SEAM HONESTY: the knowledge.VectorIndex seam hands over the candidate vectors
// in-memory per query (the module already loaded and governance-filtered them). This
// backend therefore (1) keeps a persistent HNSW index that it LAZILY syncs from the
// candidate vectors it is handed (chunk ids are immutable UUIDv7 — a re-ingested
// chunk is a NEW id, so a row is written at most once and never rewritten), and (2)
// ANN-searches that index restricted to the candidate ids. The acceleration is over
// RANKING; the candidate LOAD still happens in the module (the seam's design). At
// extreme scale (»10^5 chunks/tenant) the honest win requires pushing the governance
// filter into the vector query — a different seam, documented as a follow-up.
package vectorindex

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Candidate is one governance-filtered chunk: its id and its embedding vector. The
// id is the module's chunk id (a globally-unique UUIDv7), so a single shared index
// keyed by id is multi-tenant-safe — the module's candidate set is already tenant-
// scoped, and ids never collide across tenants.
type Candidate struct {
	ID     string
	Vector []float32
}

// Scored is one ranked result: a chunk id and its cosine SIMILARITY (1 = identical,
// −1 = opposite), matching the in-process cosineIndex's score semantics so swapping
// the backend does not change the meaning of the score.
type Scored struct {
	ID    string
	Score float64
}

// Backend ranks a governance-pre-filtered candidate set against a query vector.
type Backend interface {
	// Rank returns up to topK candidate ids ordered by descending cosine similarity
	// to query, RESTRICTED to the supplied candidate set. It honors ctx. An error
	// fails the rank (the module then fails the request deny-closed — never a silent
	// fallback to a different, untrusted index).
	Rank(ctx context.Context, query []float32, candidates []Candidate, topK int) ([]Scored, error)
	// Close releases the backend's resources (pools, connections). Idempotent.
	Close() error
}

// Doer is the HTTP transport the Qdrant backend uses (mirrors connectors/modelprovider.
// Doer). Tests inject a fake; production passes http.DefaultClient (nil).
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config selects and configures a backend.
type Config struct {
	// Kind selects the backend: "pgvector" or "qdrant".
	Kind string
	// DSN is the backend address. pgvector: a libpq/pgx connection URL (may point at
	// the core's own Postgres for air-gap). qdrant: the REST base URL (e.g.
	// http://qdrant:6333).
	DSN string
	// Namespace is the physical table (pgvector) / collection (qdrant) name. Defaults
	// to "knowledge_ann".
	Namespace string
	// APIKey is an optional credential (qdrant api-key header). Never logged.
	APIKey string
	// Dim optionally fixes the embedding dimension. 0 = infer from the first candidates
	// (the embedder's Dim()); a later candidate of a different dimension is an error.
	Dim int
	// Timeout bounds a single backend operation. 0 = a sane backend default.
	Timeout time.Duration
	// Doer is the HTTP transport for the qdrant backend (test injection); nil =
	// http.DefaultClient. Ignored by pgvector.
	Doer Doer
}

// DefaultNamespace is the table/collection name used when Config.Namespace is empty.
const DefaultNamespace = "knowledge_ann"

// defaultTimeout bounds one backend operation when Config.Timeout is unset.
const defaultTimeout = 10 * time.Second

// Open constructs the configured backend. It validates the config but defers the
// actual connection (lazy): a backend that is configured-but-unreachable surfaces as
// a Rank error at query time (deny-closed), not a boot failure — wiring a vector
// backend can never take down the control plane (the composition root keeps the
// in-process cosineIndex as the fallback only when NO backend is configured).
func Open(cfg Config) (Backend, error) {
	if strings.TrimSpace(cfg.Namespace) == "" {
		cfg.Namespace = DefaultNamespace
	}
	if !safeIdent(cfg.Namespace) {
		return nil, fmt.Errorf("vectorindex: invalid namespace %q (letters, digits, underscore; must start with a letter)", cfg.Namespace)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, fmt.Errorf("vectorindex: %s backend requires a DSN", cfg.Kind)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Kind)) {
	case "pgvector":
		return openPgvector(cfg)
	case "qdrant":
		return openQdrant(cfg)
	case "pinecone":
		return openPinecone(cfg)
	case "weaviate":
		return openWeaviate(cfg)
	case "milvus":
		return openMilvus(cfg)
	default:
		return nil, fmt.Errorf("vectorindex: unknown backend %q (want pgvector, qdrant, pinecone, weaviate or milvus)", cfg.Kind)
	}
}

// vectorDim validates that a query and its candidates share one dimension and returns
// it. A zero-length query, an empty candidate set, or a dimension disagreement is an
// error (never a silent mis-rank — the cosine of mismatched dimensions is meaningless).
func vectorDim(query []float32, candidates []Candidate) (int, error) {
	d := len(query)
	if d == 0 {
		return 0, fmt.Errorf("vectorindex: empty query vector")
	}
	for i, c := range candidates {
		if len(c.Vector) != d {
			return 0, fmt.Errorf("vectorindex: candidate %d (%s) has dimension %d, query has %d", i, c.ID, len(c.Vector), d)
		}
	}
	return d, nil
}

// encodeVector formats a float32 vector as pgvector's text literal form "[f1,f2,…]"
// (also the JSON array Qdrant accepts). Deterministic, minimal-width float32 repr.
func encodeVector(v []float32) string {
	var b strings.Builder
	b.Grow(len(v)*8 + 2)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// safeIdent reports whether s is a safe SQL/collection identifier (no injection
// surface): a letter followed by letters, digits or underscores. The namespace is
// the only operator-supplied string that reaches a DDL/identifier position.
func safeIdent(s string) bool {
	if s == "" || len(s) > 48 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case (r >= '0' && r <= '9' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// capN bounds a topK to a sane positive range; 0/negative becomes a default.
func capN(topK int) int {
	if topK <= 0 {
		return 10
	}
	if topK > 1000 {
		return 1000
	}
	return topK
}
