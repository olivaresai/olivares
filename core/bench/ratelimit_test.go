// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package bench

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/api/ratelimit/pgstore"
)

// benchTiers is one generous tier so the benchmark measures the TAKE path, not
// denials (denials are cheaper: no decrement statement).
func benchTiers() map[string]ratelimit.TierLimits {
	return map[string]ratelimit.TierLimits{
		"bench": {
			PerClass: map[ratelimit.EndpointClass]ratelimit.Limit{
				ratelimit.ClassRead:  {Rate: 1e9, Burst: 1e9},
				ratelimit.ClassWrite: {Rate: 1e9, Burst: 1e9},
			},
			Total: ratelimit.Limit{Rate: 1e9, Burst: 1e9},
		},
	}
}

// BenchmarkRateLimitTake quantifies the shared-store decision with real
// numbers: what one rate-limit admission costs in-proc (the single-node
// default) vs against the shared Postgres store (the HA mode, one plpgsql
// round trip per take) — both the uncontended shape (every request a distinct
// tenant) and the adversarial one (every request ONE hot identity, whose
// aggregate row serializes all of them). These are the numbers the
// contract delta cites, and the evidence basis for ever adding a Redis Store
// implementation (interface yes, Redis only if data demands it).
func BenchmarkRateLimitTake(b *testing.B) {
	run := func(b *testing.B, l *ratelimit.Limiter, workers int, hotIdentity bool) {
		ctx := context.Background()
		per := b.N / workers
		if per == 0 {
			per = 1
		}
		var (
			wg  sync.WaitGroup
			mu  sync.Mutex
			lat []time.Duration
		)
		start := time.Now()
		b.ResetTimer()
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				id := fmt.Sprintf("tn:bench-%d", w)
				if hotIdentity {
					id = "tn:bench-hot"
				}
				local := make([]time.Duration, 0, per)
				for i := 0; i < per; i++ {
					t0 := time.Now()
					d := l.Allow(ctx, id, "bench", ratelimit.ClassRead)
					local = append(local, time.Since(t0))
					if !d.OK {
						b.Error("bench take denied: the tier is sized to never deny")
						return
					}
				}
				mu.Lock()
				lat = append(lat, local...)
				mu.Unlock()
			}(w)
		}
		wg.Wait()
		b.StopTimer()
		reportLatency(b, lat, time.Since(start))
	}

	// A store take that overruns the 250ms production budget degrades to the
	// in-proc shards by design — which for a BENCHMARK means the "postgres" row
	// would report the cost of the LOCAL path under a Postgres label. That is a
	// fabricated number, so the degradation is caught and reported here. The
	// timeout stays at the production default on purpose: this measures what
	// production does, not what a generous fixture could do.
	newLimiter := func(b *testing.B, st ratelimit.Store) *ratelimit.Limiter {
		b.Helper()
		var mu sync.Mutex
		var degraded []string
		l := ratelimit.New(ratelimit.Config{
			Tiers: benchTiers(), DefaultTier: "bench", Store: st,
			LogWarn: func(msg string, err error) {
				mu.Lock()
				defer mu.Unlock()
				degraded = append(degraded, fmt.Sprintf("%s: %v", msg, err))
			},
		}, nil)
		if st != nil {
			b.Cleanup(func() {
				mu.Lock()
				defer mu.Unlock()
				if len(degraded) > 0 {
					b.Errorf("the shared store degraded to per-node buckets during this benchmark, "+
						"so the numbers above measured the LOCAL shards, not Postgres: %v", degraded)
				}
			})
		}
		return l
	}

	for _, workers := range []int{1, 8} {
		w := workers
		b.Run(fmt.Sprintf("inproc/workers=%d", w), func(b *testing.B) {
			run(b, newLimiter(b, nil), w, false)
		})
	}
	dsn := os.Getenv("OLIVARES_TEST_POSTGRES_DSN")
	if dsn == "" {
		return
	}
	openStore := func(b *testing.B) *pgstore.Store {
		b.Helper()
		st, err := pgstore.Open(context.Background(), dsn, pgstore.Options{})
		if err != nil {
			b.Fatalf("open pgstore: %v", err)
		}
		b.Cleanup(func() { _ = st.Close() })
		return st
	}
	for _, workers := range []int{1, 8} {
		w := workers
		b.Run(fmt.Sprintf("postgres/workers=%d", w), func(b *testing.B) {
			run(b, newLimiter(b, openStore(b)), w, false)
		})
	}
	// The adversarial shape: 8 workers hammering ONE identity — its aggregate
	// row serializes every take (the per-identity ceiling the contract delta
	// documents; the plpgsql single-round-trip design exists to keep the lock
	// hold to server-side microseconds).
	b.Run("postgres/hot-identity/workers=8", func(b *testing.B) {
		run(b, newLimiter(b, openStore(b)), 8, true)
	})
}
