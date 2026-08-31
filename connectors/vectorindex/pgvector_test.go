// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vectorindex

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestPgvectorDenyClosedWhenUnreachable proves the deny-closed contract WITHOUT a live
// Postgres: a configured-but-unreachable backend fails the rank (never returns empty
// or stale results). pgxpool connects lazily, so the error surfaces at Rank time.
func TestPgvectorDenyClosedWhenUnreachable(t *testing.T) {
	b, err := Open(Config{Kind: "pgvector", DSN: "postgres://nobody@127.0.0.1:1/none", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("open (lazy) should not fail on an unreachable host: %v", err)
	}
	defer b.Close()
	_, err = b.Rank(context.Background(), []float32{1, 0, 0}, []Candidate{{ID: "a", Vector: []float32{1, 0, 0}}}, 5)
	if err == nil {
		t.Fatal("an unreachable backend must fail the rank (deny-closed), not return results")
	}
}

func TestPgvectorOpenRejectsMalformedDSN(t *testing.T) {
	if _, err := Open(Config{Kind: "pgvector", DSN: "://not-a-dsn"}); err == nil {
		t.Fatal("a malformed DSN must error at Open")
	}
}

// TestPgvectorIntegration runs the REAL pgvector backend when OLIVARES_TEST_VECTOR_DSN
// points at a Postgres with the pgvector extension (mirrors the repo's
// OLIVARES_TEST_POSTGRES_DSN gating). It proves: (1) top-K parity with exact cosine on
// a small corpus, and (2) the governance invariant — a chunk OUTSIDE the candidate set
// is never returned, even though it is the most similar vector in the table.
func TestPgvectorIntegration(t *testing.T) {
	dsn := os.Getenv("OLIVARES_TEST_VECTOR_DSN")
	if dsn == "" {
		t.Skip("set OLIVARES_TEST_VECTOR_DSN (a Postgres with pgvector) to run the pgvector integration test")
	}
	b, err := Open(Config{Kind: "pgvector", DSN: dsn, Namespace: "knowledge_ann_test", Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()

	// A small corpus: "best" is the closest to the query, "intruder" is closer still
	// but is deliberately EXCLUDED from the candidate set on the second query.
	all := []Candidate{
		{ID: "best", Vector: []float32{1, 0, 0}},
		{ID: "mid", Vector: []float32{0.7, 0.7, 0}},
		{ID: "far", Vector: []float32{0, 0, 1}},
		{ID: "intruder", Vector: []float32{0.99, 0.01, 0}},
	}
	query := []float32{1, 0, 0}

	got, err := b.Rank(ctx, query, all, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Parity with exact cosine: intruder (≈1.0) then best (1.0) — both top-2, with
	// best/intruder the two most similar. Order of the ~tied pair may vary; assert the
	// set is the two closest.
	if len(got) != 2 {
		t.Fatalf("want top-2, got %d", len(got))
	}
	top := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !top["best"] || !top["intruder"] {
		t.Fatalf("top-2 = %v, want {best,intruder} (the two most similar)", got)
	}

	// Governance invariant: with "intruder" EXCLUDED from the candidate set, it must
	// never appear — even though it is the most similar vector in the index.
	governed := []Candidate{all[0], all[1], all[2]} // best, mid, far — NO intruder
	got2, err := b.Rank(ctx, query, governed, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got2 {
		if s.ID == "intruder" {
			t.Fatalf("intruder leaked past the candidate-set filter: %+v", got2)
		}
	}
	if len(got2) == 0 || got2[0].ID != "best" {
		t.Fatalf("governed rank = %+v, want best first", got2)
	}
}
