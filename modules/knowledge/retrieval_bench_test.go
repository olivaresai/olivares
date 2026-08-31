// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const benchmarkEmbeddingVariants = 256

type retrievalBenchmarkHarness struct {
	st     store.Store
	mod    *Module
	tenant model.TenantID
	mc     api.ModuleContext
	kbID   model.ID
}

// BenchmarkRetrievalEndToEnd measures the complete Query pipeline: governed
// store reads, local embedding, classification/ACL filtering, exact cosine
// ranking, and the lineage plus audit writes that every query performs. 100k is
// the supported ceiling for the built-in linear index.
func BenchmarkRetrievalEndToEnd(b *testing.B) {
	for _, corpusSize := range []int{10_000, maxChunksPerKB} {
		corpusSize := corpusSize
		b.Run(fmt.Sprintf("chunks=%d", corpusSize), func(b *testing.B) {
			h := newRetrievalBenchmarkHarness(b, corpusSize)
			req := QueryRequest{
				KBID:       h.kbID.String(),
				Query:      "scale envelope retrieval benchmark",
				TopK:       10,
				SessionRef: "session-retrieval-bench",
			}
			if result, err := h.mod.Query(context.Background(), h.mc, req); err != nil || result.Count != 10 {
				b.Fatalf("warm-up Query = (count=%v, err=%v), want 10 results", queryResultCount(result), err)
			}

			ctx := context.Background()
			lat := make([]time.Duration, b.N)
			start := time.Now()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				t0 := time.Now()
				result, err := h.mod.Query(ctx, h.mc, req)
				lat[i] = time.Since(t0)
				if err != nil {
					b.Fatalf("Query %d: %v", i, err)
				}
				if result.Count != 10 {
					b.Fatalf("Query %d returned %d chunks, want 10", i, result.Count)
				}
			}
			b.StopTimer()
			reportQueryLatency(b, lat, time.Since(start))
			b.ReportMetric(float64(corpusSize), "chunks/tenant")
		})
	}
}

// BenchmarkCosineIndexRankCurve extends the exact in-memory ranker through one
// million candidates. The 1e6 point is outside the supported linear regime and
// needs an external ANN backend (pgvector/Qdrant/etc.) in production. Because
// cosineIndex evaluates every candidate exactly, recall@k is 1.0 by
// construction; ANN trades some recall for latency above the 100k ceiling.
func BenchmarkCosineIndexRankCurve(b *testing.B) {
	for _, candidatesCount := range []int{10_000, 100_000, 1_000_000} {
		candidatesCount := candidatesCount
		b.Run(fmt.Sprintf("candidates=%d", candidatesCount), func(b *testing.B) {
			query := deterministicBenchmarkVector(1)
			vectors := make([][]float32, benchmarkEmbeddingVariants)
			for i := range vectors {
				vectors[i] = deterministicBenchmarkVector(i + 2)
			}
			candidates := make([]VectorCandidate, candidatesCount)
			for i := range candidates {
				candidates[i] = VectorCandidate{
					ChunkID: "chunk-" + strconv.Itoa(i),
					Vector:  vectors[i%len(vectors)],
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
			b.ReportMetric(float64(candidatesCount), "candidates/op")
			b.ReportMetric(1, "recall@10")
		})
	}
}

// newRetrievalBenchmarkHarness is the testing.B adapter for newHarnessWith,
// whose concrete *testing.T parameter cannot accept a benchmark. It reproduces
// the harness's real engine, schema, signer, tenant, local embedder, permissive
// guard, and ModuleData wiring; HTTP/runtime setup is intentionally omitted
// because the measured programmatic entry point is Module.Query.
func newRetrievalBenchmarkHarness(b *testing.B, chunks int) *retrievalBenchmarkHarness {
	b.Helper()
	ctx := context.Background()
	mod := New(WithRetrievalGuard(fixedGuard{grants: Grants{
		Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret,
	}}))
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatalf("generate audit key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		b.Fatalf("build audit signer: %v", err)
	}
	st, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", SignEvent: signer.SignEvent,
	}, mod.RegisterSchema)
	if err != nil {
		b.Fatalf("open retrieval benchmark store: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "retrieval-bench", Slug: "retrieval-bench", Status: model.StatusActive,
		})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		b.Fatalf("provision retrieval benchmark tenant: %v", err)
	}
	mod.UseData(api.NewModuleData(st))
	kbID := seedRetrievalBenchmarkCorpus(b, st, tenant, chunks)
	principal := auth.ScopedPrincipal(model.NewID(), "retrieval bench", tenant, auth.RoleEditor)
	return &retrievalBenchmarkHarness{
		st: st, mod: mod, tenant: tenant, kbID: kbID,
		mc: api.ModuleContext{
			Principal: principal,
			Tenant:    tenant,
			Data:      api.NewScopedData(st, tenant),
		},
	}
}

// seedRetrievalBenchmarkCorpus bypasses the HTTP ingest cap and writes the real
// knowledge-base, document, and chunk entities through a tenant-bound store
// scope. The embedding pool is deterministic and shared only during setup;
// SQLite persists each chunk's encoded vector independently.
func seedRetrievalBenchmarkCorpus(b *testing.B, st store.Store, tenant model.TenantID, chunks int) model.ID {
	b.Helper()
	ctx := context.Background()
	encoded := make([][]byte, benchmarkEmbeddingVariants)
	for i := range encoded {
		encoded[i] = encodeEmbedding(deterministicBenchmarkVector(i + 2))
	}

	var kbID model.ID
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		kbRepo, err := sc.Ext(baseKind)
		if err != nil {
			return err
		}
		kb, err := kbRepo.Create(ctx, model.Record{
			colName: "scale-envelope", colClassif: classPublic, colResidency: "",
			colEmbedPolicy: embedLocalOnly, colEmbedModel: LocalHashModelRef, colDim: int64(benchmarkVectorDimension),
			colDefaultACL: marshalStrings(nil), colOwnerRef: "benchmark", colStatus: kbActive,
			colDocCount: int64(1), colChunkCount: int64(chunks),
		})
		if err != nil {
			return err
		}
		kbID = model.ID(kb.String(model.ColID))

		docRepo, err := sc.Ext(documentKind)
		if err != nil {
			return err
		}
		doc, err := docRepo.Create(ctx, model.Record{
			colKBRef: kbID.String(), colSourceKind: "benchmark", colSourceRef: "scale-envelope",
			colSourceMode: sourceModeDirect, colSourceDocID: "benchmark-document", colTitle: "Scale envelope corpus",
			colContentType: "text/plain", colClassif: classPublic, colResidency: "", colACL: marshalStrings(nil),
			colContentHash: hashHex("scale-envelope-corpus"), colRedactCount: int64(0), colSpaceRef: "",
			colDocChunkCnt: int64(chunks), colStatus: docIndexed,
		})
		if err != nil {
			return err
		}
		docID := model.ID(doc.String(model.ColID))

		chunkRepo, err := sc.Ext(chunkKind)
		if err != nil {
			return err
		}
		acl := marshalStrings(nil)
		for i := 0; i < chunks; i++ {
			_, err := chunkRepo.Create(ctx, model.Record{
				colKBRef: kbID.String(), colDocRef: docID.String(), colChunkIndex: int64(i),
				colText: "scale envelope retrieval benchmark chunk", colEmbedding: encoded[i%len(encoded)],
				colEmbedModel: LocalHashModelRef, colDim: int64(benchmarkVectorDimension), colTokenCount: int64(5),
				colContentHash: hashHex(strconv.Itoa(i)), colClassif: classPublic, colACL: acl, colIndexed: true,
			})
			if err != nil {
				return fmt.Errorf("create chunk %d: %w", i, err)
			}
		}
		return nil
	})
	if err != nil {
		b.Fatalf("seed %d retrieval chunks: %v", chunks, err)
	}
	return kbID
}

func queryResultCount(result *QueryResult) int {
	if result == nil {
		return 0
	}
	return result.Count
}

func reportQueryLatency(b *testing.B, lat []time.Duration, elapsed time.Duration) {
	b.Helper()
	if len(lat) == 0 {
		return
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }
	q := func(p float64) time.Duration {
		idx := int(p*float64(len(lat)-1) + 0.5)
		return lat[idx]
	}
	b.ReportMetric(float64(len(lat))/elapsed.Seconds(), "queries/sec")
	b.ReportMetric(ms(q(0.50)), "p50_ms")
	b.ReportMetric(ms(q(0.95)), "p95_ms")
	b.ReportMetric(ms(q(0.99)), "p99_ms")
	b.ReportMetric(ms(lat[len(lat)-1]), "max_ms")
}
