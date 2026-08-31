// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vectorindex

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgvector backend — the self-host/air-gap default. It keeps a persistent HNSW
// (cosine) index in Postgres and ANN-searches it RESTRICTED to the candidate ids the
// module hands it. The vectors are lazily synced from the candidate set: chunk ids
// are immutable UUIDv7, so each row is inserted at most once and never rewritten
// (`ON CONFLICT DO NOTHING`). It targets the pgvector extension >= 0.8.0 (iterative
// index scans, so a selective candidate filter still returns enough rows).
//
// Honest limit: a re-ingested/deleted chunk is a NEW id, so its old vector becomes an
// orphan row that is never returned again (it is no longer a candidate). A periodic
// vacuum of rows whose id is absent from the core store is a follow-up; for pgvector
// on the core's own Postgres the disk is in-perimeter and bounded.
type pgBackend struct {
	pool      *pgxpool.Pool
	tbl       string
	to        time.Duration
	configDim int // operator-fixed dimension (0 = infer); IMMUTABLE after construction

	mu    sync.Mutex // guards ready+dim (the built-schema state)
	ready bool
	dim   int
}

func openPgvector(cfg Config) (Backend, error) {
	pool, err := pgxpool.New(context.Background(), cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("vectorindex: pgvector pool: %w", err)
	}
	return &pgBackend{pool: pool, tbl: cfg.Namespace, to: cfg.Timeout, configDim: cfg.Dim}, nil
}

func (b *pgBackend) Close() error {
	if b.pool != nil {
		b.pool.Close()
	}
	return nil
}

// ensureSchema creates the pgvector extension, the vector table and the HNSW cosine
// index once, for the dimension first observed. A later dimension change is an error
// (a corpus's embedder dimension is stable; a mismatch means a misconfiguration, not
// a silent re-rank). The DDL is idempotent (`IF NOT EXISTS`).
func (b *pgBackend) ensureSchema(ctx context.Context, dim int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ready {
		if b.dim != dim {
			return fmt.Errorf("vectorindex: pgvector index %q built for dim %d, got %d", b.tbl, b.dim, dim)
		}
		return nil
	}
	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		// id is the module chunk id (UUIDv7 text); embedding is the fixed-dim vector.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (id text PRIMARY KEY, embedding vector(%d) NOT NULL)`, b.tbl, dim),
		// HNSW with the cosine ops class — the production ANN index (vector matrix).
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_hnsw ON %s USING hnsw (embedding vector_cosine_ops)`, b.tbl, b.tbl),
	}
	for _, s := range stmts {
		if _, err := b.pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("vectorindex: pgvector ensure schema: %w", err)
		}
	}
	b.ready, b.dim = true, dim
	return nil
}

func (b *pgBackend) Rank(ctx context.Context, query []float32, candidates []Candidate, topK int) ([]Scored, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	dim, err := vectorDim(query, candidates)
	if err != nil {
		return nil, err
	}
	if b.configDim > 0 {
		dim = b.configDim // operator-fixed dimension wins; immutable field, race-free read
	}
	ctx, cancel := context.WithTimeout(ctx, b.to)
	defer cancel()
	if err := b.ensureSchema(ctx, dim); err != nil {
		return nil, err
	}
	if err := b.syncMissing(ctx, candidates); err != nil {
		return nil, err
	}

	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	// ANN search RESTRICTED to the candidate ids (the governance allow-list), run in a
	// single transaction so `SET LOCAL hnsw.iterative_scan` actually applies to the
	// SELECT (SET LOCAL is scoped to its transaction — a separate pooled Exec would not
	// affect a later Query). relaxed_order keeps recall when the id filter is selective
	// vs the whole index (pgvector >= 0.8.0; the pinned 0.8.2). The read-only tx is
	// rolled back (no writes), which is fine for the SELECT.
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("vectorindex: pgvector begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
		return nil, fmt.Errorf("vectorindex: pgvector set iterative_scan (needs pgvector >= 0.8.0): %w", err)
	}
	q := fmt.Sprintf(
		`SELECT id, 1 - (embedding <=> $1::vector) AS score FROM %s WHERE id = ANY($2::text[]) ORDER BY embedding <=> $1::vector LIMIT $3`,
		b.tbl)
	rows, err := tx.Query(ctx, q, encodeVector(query), ids, capN(topK))
	if err != nil {
		return nil, fmt.Errorf("vectorindex: pgvector rank: %w", err)
	}
	defer rows.Close()
	out := make([]Scored, 0, capN(topK))
	for rows.Next() {
		var id string
		var score float64
		if err := rows.Scan(&id, &score); err != nil {
			return nil, fmt.Errorf("vectorindex: pgvector scan: %w", err)
		}
		out = append(out, Scored{ID: id, Score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vectorindex: pgvector rows: %w", err)
	}
	return out, nil
}

// syncMissing inserts the candidate vectors not already present (lazy sync). It first
// reads which candidate ids exist, then bulk-inserts only the missing (id, vector)
// pairs — so a warm corpus writes nothing, and the wire never carries a vector that
// is already indexed. `ON CONFLICT DO NOTHING` makes a concurrent insert race a no-op.
func (b *pgBackend) syncMissing(ctx context.Context, candidates []Candidate) error {
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	have := make(map[string]bool, len(candidates))
	rows, err := b.pool.Query(ctx, fmt.Sprintf(`SELECT id FROM %s WHERE id = ANY($1::text[])`, b.tbl), ids)
	if err != nil {
		return fmt.Errorf("vectorindex: pgvector sync-probe: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("vectorindex: pgvector sync-probe scan: %w", err)
		}
		have[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("vectorindex: pgvector sync-probe rows: %w", err)
	}

	var missingIDs, missingVecs []string
	for _, c := range candidates {
		if !have[c.ID] {
			missingIDs = append(missingIDs, c.ID)
			missingVecs = append(missingVecs, encodeVector(c.Vector))
		}
	}
	if len(missingIDs) == 0 {
		return nil
	}
	// Insert the missing vectors as a single batch round-trip. Per-row $2::vector casts
	// the pgvector text literal (avoiding a text[]→vector[] array cast that pgvector
	// does not reliably support); ON CONFLICT DO NOTHING makes a concurrent insert a
	// no-op (chunk ids are immutable, so a row is never rewritten).
	batch := &pgx.Batch{}
	ins := fmt.Sprintf(`INSERT INTO %s (id, embedding) VALUES ($1, $2::vector) ON CONFLICT (id) DO NOTHING`, b.tbl)
	for i := range missingIDs {
		batch.Queue(ins, missingIDs[i], missingVecs[i])
	}
	br := b.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range missingIDs {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("vectorindex: pgvector sync-insert: %w", err)
		}
	}
	return nil
}
