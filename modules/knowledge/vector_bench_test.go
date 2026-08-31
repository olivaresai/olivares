// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"strconv"
	"testing"
)

const (
	benchmarkVectorCandidates = 100_000
	benchmarkVectorDimension  = 256
)

var benchmarkRankedChunks []ScoredChunk

// BenchmarkCosineIndexRank100K anchors the ~10^5 chunks/tenant claim with the
// exact in-memory ranker and its cosine calculation; it is not end-to-end retrieval p99.
func BenchmarkCosineIndexRank100K(b *testing.B) {
	query := deterministicBenchmarkVector(1)
	candidates := make([]VectorCandidate, benchmarkVectorCandidates)
	for i := range candidates {
		candidates[i] = VectorCandidate{
			ChunkID: "chunk-" + strconv.Itoa(i),
			Vector:  deterministicBenchmarkVector(i + 2),
		}
	}

	ctx := context.Background()
	index := cosineIndex{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var err error
		benchmarkRankedChunks, err = index.Rank(ctx, query, candidates, 10)
		if err != nil {
			b.Fatal(err)
		}
		if len(benchmarkRankedChunks) != 10 {
			b.Fatalf("ranked %d chunks, want 10", len(benchmarkRankedChunks))
		}
	}
	b.ReportMetric(benchmarkVectorCandidates, "candidates/op")
}

func deterministicBenchmarkVector(index int) []float32 {
	vector := make([]float32, benchmarkVectorDimension)
	for dimension := range vector {
		value := ((index+1)*(dimension+3) + dimension*dimension) % 257
		vector[dimension] = float32(value-128) / 128
	}
	return vector
}
