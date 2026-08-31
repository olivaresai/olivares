// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package bench

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// openBench opens a real store for cfg, wired with a per-event Ed25519 signer
// exactly as the boot path does (so an audited write pays the real hash-chain +
// signature cost), and provisions one tenant to write into. The store is opened
// ONCE per benchmark; opening runs migrations + the boot self-test, which must
// not be inside the measured loop.
func openBench(b *testing.B, cfg store.Config) (store.Store, model.TenantID) {
	b.Helper()
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		b.Fatal(err)
	}
	cfg.SignEvent = signer.SignEvent
	st, err := engine.Open(ctx, cfg, nil)
	if err != nil {
		b.Fatalf("open store (%s): %v", cfg.Engine, err)
	}
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		// The slug must be unique PER OPEN: the benchmark framework re-invokes a
		// benchmark function while calibrating b.N, and a persistent Postgres
		// target keeps the previous invocation's org — a fixed "bench" slug
		// collides on the second invocation (latent until first ran this
		// harness against a real PG; SQLite never sees it, every open is a fresh
		// temp file).
		slug := fmt.Sprintf("bench-%d", time.Now().UnixNano())
		org, e := sys.CreateOrg(ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		b.Fatalf("provision tenant: %v", err)
	}
	return st, tenant
}

// sqliteCfg returns a Config for an on-disk SQLite file (NOT :memory:) so the WAL
// fsync cost a production commit pays is included in the measurement.
func sqliteCfg(b *testing.B) store.Config {
	return store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(b.TempDir(), "bench.db")}
}

// eachBackend runs run against SQLite always, and against Postgres too when
// OLIVARES_TEST_POSTGRES_DSN is set — the same harness measures both so the
// SQLite→Postgres comparison is apples-to-apples.
func eachBackend(b *testing.B, run func(b *testing.B, st store.Store, tenant model.TenantID)) {
	b.Run("sqlite", func(b *testing.B) {
		st, tenant := openBench(b, sqliteCfg(b))
		defer func() { _ = st.Close() }()
		run(b, st, tenant)
	})
	if dsn := os.Getenv("OLIVARES_TEST_POSTGRES_DSN"); dsn != "" {
		b.Run("postgres", func(b *testing.B) {
			st, tenant := openBench(b, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 16})
			defer func() { _ = st.Close() }()
			run(b, st, tenant)
		})
	}
}

// reportLatency sorts the collected per-op latencies and reports throughput plus
// the p50/p95/p99/max percentiles (in ms) as custom benchmark metrics — these are
// the numbers the SLO doc and sizing guide cite, measured not assumed.
func reportLatency(b *testing.B, lat []time.Duration, elapsed time.Duration) {
	if len(lat) == 0 {
		return
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }
	q := func(p float64) time.Duration {
		idx := int(p*float64(len(lat)-1) + 0.5)
		return lat[idx]
	}
	b.ReportMetric(float64(len(lat))/elapsed.Seconds(), "events/sec")
	b.ReportMetric(ms(q(0.50)), "p50_ms")
	b.ReportMetric(ms(q(0.95)), "p95_ms")
	b.ReportMetric(ms(q(0.99)), "p99_ms")
	b.ReportMetric(ms(lat[len(lat)-1]), "max_ms")
}

// benchSeqWrite runs op b.N times sequentially in its own transaction, collecting
// per-op latency. Sequential is the honest single-writer measurement: it is the
// rate one node sustains, which is exactly what bounds ingest on SQLite.
func benchSeqWrite(b *testing.B, st store.Store, tenant model.TenantID, op func(ctx context.Context, sc store.Scope, i int) error) {
	ctx := context.Background()
	lat := make([]time.Duration, b.N)
	start := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error { return op(ctx, sc, i) }); err != nil {
			b.Fatalf("write %d: %v", i, err)
		}
		lat[i] = time.Since(t0)
	}
	b.StopTimer()
	reportLatency(b, lat, time.Since(start))
}

// BenchmarkAuditAppend measures the signed, hash-chained audit-append path — the
// heaviest universal write (every governed mutation appends one) and the ledger
// throughput ceiling. It is per-tenant serialized by construction.
func BenchmarkAuditAppend(b *testing.B) {
	eachBackend(b, func(b *testing.B, st store.Store, tenant model.TenantID) {
		benchSeqWrite(b, st, tenant, func(ctx context.Context, sc store.Scope, _ int) error {
			_, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "bench", ActorKind: model.ActorSystem, Action: "bench.append",
				Meta: map[string]any{"src": "capacity-bench"},
			})
			return err
		})
	})
}

// BenchmarkAccessEdgeUpsert measures the observation→edge write: each ingested
// observation materializes (or merges) an access edge. A fresh OriginID per op
// makes every write an INSERT (the conservative, new-edge case).
func BenchmarkAccessEdgeUpsert(b *testing.B) {
	resource := model.NewID()
	eachBackend(b, func(b *testing.B, st store.Store, tenant model.TenantID) {
		benchSeqWrite(b, st, tenant, func(ctx context.Context, sc store.Scope, _ int) error {
			now := model.NewTimestamp(time.Now())
			_, err := sc.AccessEdges().Upsert(ctx, model.AccessEdge{
				OriginKind: "agent", OriginID: model.NewID(), ResourceID: resource,
				Mode: sdkmodel.ModeRead, SignalSource: sdkmodel.SignalOTEL,
				Confidence: sdkmodel.ConfidenceAttributed, Observed: true,
				FirstSeen: now, LastSeen: now,
			})
			return err
		})
	})
}

// BenchmarkAgentCreate measures a typical entity INSERT (an inventory write).
func BenchmarkAgentCreate(b *testing.B) {
	eachBackend(b, func(b *testing.B, st store.Store, tenant model.TenantID) {
		benchSeqWrite(b, st, tenant, func(ctx context.Context, sc store.Scope, i int) error {
			id := fmt.Sprintf("bench-agent-%d", i)
			_, err := sc.Agents().Create(ctx, model.Agent{
				Name: id, Kind: "api", ExternalID: id, Status: model.StatusActive,
			})
			return err
		})
	})
}

// BenchmarkWriteScaling drives edge upserts from a growing number of concurrent
// writers. On SQLite (a single shared connection, by design) aggregate throughput
// does NOT scale with concurrency — that flat curve IS the single-writer ceiling
// and the empirical trigger to move to Postgres. With OLIVARES_TEST_POSTGRES_DSN
// set, the postgres sub-benchmarks show throughput climbing with writers.
func BenchmarkWriteScaling(b *testing.B) {
	resource := model.NewID()
	upsert := func(ctx context.Context, sc store.Scope) error {
		now := model.NewTimestamp(time.Now())
		_, err := sc.AccessEdges().Upsert(ctx, model.AccessEdge{
			OriginKind: "agent", OriginID: model.NewID(), ResourceID: resource,
			Mode: sdkmodel.ModeRead, SignalSource: sdkmodel.SignalOTEL,
			Confidence: sdkmodel.ConfidenceAttributed, Observed: true,
			FirstSeen: now, LastSeen: now,
		})
		return err
	}
	run := func(b *testing.B, st store.Store, tenant model.TenantID, writers int) {
		ctx := context.Background()
		per := b.N / writers
		if per == 0 {
			per = 1
		}
		var wg sync.WaitGroup
		start := time.Now()
		b.ResetTimer()
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < per; i++ {
					if err := st.Mutate(ctx, tenant, func(sc store.Scope) error { return upsert(ctx, sc) }); err != nil {
						b.Error(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		b.StopTimer()
		b.ReportMetric(float64(per*writers)/time.Since(start).Seconds(), "events/sec")
	}
	for _, writers := range []int{1, 4, 16} {
		w := writers
		b.Run(fmt.Sprintf("sqlite/writers=%d", w), func(b *testing.B) {
			st, tenant := openBench(b, sqliteCfg(b))
			defer func() { _ = st.Close() }()
			run(b, st, tenant, w)
		})
		if dsn := os.Getenv("OLIVARES_TEST_POSTGRES_DSN"); dsn != "" {
			b.Run(fmt.Sprintf("postgres/writers=%d", w), func(b *testing.B) {
				st, tenant := openBench(b, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 32})
				defer func() { _ = st.Close() }()
				run(b, st, tenant, w)
			})
		}
	}
}

// BenchmarkBusFanout measures the in-process event bus delivering to N subscribers
// (publish → drained by every subscriber). It is intentionally store-free: it shows
// the bus moves events far faster than any backend persists them, so the durable
// write — not the bus — is the per-node ceiling the other benchmarks characterize.
func BenchmarkBusFanout(b *testing.B) {
	for _, subs := range []int{1, 4} {
		s := subs
		b.Run(fmt.Sprintf("subscribers=%d", s), func(b *testing.B) {
			bus := eventbus.NewInProc(eventbus.Options{})
			defer func() { _ = bus.Close() }()
			target := int64(b.N) * int64(s)
			var processed int64
			done := make(chan struct{})
			var once sync.Once
			for i := 0; i < s; i++ {
				if _, err := bus.Subscribe(nil, func(_ context.Context, _ event.Event) error {
					if atomic.AddInt64(&processed, 1) == target {
						once.Do(func() { close(done) })
					}
					return nil
				}); err != nil {
					b.Fatal(err)
				}
			}
			ev := event.Event{Type: event.TypeEdgeObserved}
			ctx := context.Background()
			start := time.Now()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := bus.Publish(ctx, ev); err != nil {
					b.Fatal(err)
				}
			}
			<-done
			b.StopTimer()
			b.ReportMetric(float64(b.N)/time.Since(start).Seconds(), "events/sec")
		})
	}
}
