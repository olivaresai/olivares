// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package pgstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/api/ratelimit/pgstore"
	"github.com/olivaresai/olivares/core/engine/enginetest"
)

// testDSN provisions an ISOLATED Postgres database for t and returns its
// application DSN; the database is dropped in t.Cleanup.
//
// these tests previously shared the one workspace-wide database, where
// ratelimit_buckets is a GLOBAL, non-tenant-scoped table and TestSweepEvictsIdleOnly
// issues an UNSCOPED delete of every idle bucket — so they could destroy each
// other's rows, and their Open contended with the engine's schema migration on the
// same olivares.migrate.v1 advisory lock (pgstore.go:56, sqlstore/store.go:389).
//
// Call it ONCE per test: two calls provision two DIFFERENT databases. A test that
// needs a second node on the SAME database must thread this DSN through
// openStoreOn.
func testDSN(t *testing.T) string {
	t.Helper()
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s not set; skipping Postgres rate-limit store tests", enginetest.EnvSuperuserDSN)
	}
	return enginetest.IsolatedPostgres(t).App
}

// openStore opens a Store (no sweeper) plus a raw inspection connection on a
// freshly provisioned, private database.
func openStore(t *testing.T) (*pgstore.Store, *sql.DB) {
	t.Helper()
	return openStoreOn(t, testDSN(t))
}

// openStoreOn is openStore against an ALREADY provisioned database, so a test can
// put two simulated nodes on one database.
func openStoreOn(t *testing.T, dsn string) (*pgstore.Store, *sql.DB) {
	t.Helper()
	st, err := pgstore.Open(context.Background(), dsn, pgstore.Options{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return st, db
}

func reqs(id string, clsRate, clsBurst, totRate, totBurst float64) []ratelimit.StoreRequest {
	return []ratelimit.StoreRequest{
		{Key: id + "|read", Limit: ratelimit.Limit{Rate: clsRate, Burst: clsBurst}},
		{Key: id + "|*", Limit: ratelimit.Limit{Rate: totRate, Burst: totBurst}},
	}
}

func take(t *testing.T, st *pgstore.Store, r []ratelimit.StoreRequest) (bool, []ratelimit.StoreState) {
	t.Helper()
	ok, states, err := st.Take(context.Background(), r)
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	if len(states) != len(r) {
		t.Fatalf("take returned %d states for %d reqs", len(states), len(r))
	}
	return ok, states
}

// rewindLastTake makes a bucket look idle for d (the test's clock lever — the
// function's clock is the PG host's, deliberately not injectable).
func rewindLastTake(t *testing.T, db *sql.DB, key string, d time.Duration) {
	t.Helper()
	if _, err := db.Exec(`UPDATE ratelimit_buckets SET last_take = last_take - make_interval(secs => $1) WHERE key = $2`, d.Seconds(), key); err != nil {
		t.Fatalf("rewind: %v", err)
	}
}

func bucketRow(t *testing.T, db *sql.DB, key string) (tokens float64, lastTake time.Time) {
	t.Helper()
	if err := db.QueryRow(`SELECT tokens, last_take FROM ratelimit_buckets WHERE key = $1`, key).Scan(&tokens, &lastTake); err != nil {
		t.Fatalf("bucket row %s: %v", key, err)
	}
	return tokens, lastTake
}

// TestBurstThenDenyThenRefill is the basic token-bucket contract on the shared
// store: exactly burst admits, then denial; rewinding last_take refills at rate.
func TestBurstThenDenyThenRefill(t *testing.T) {
	st, db := openStore(t)
	id := fmt.Sprintf("t1-%d", time.Now().UnixNano())
	// rate 0.001 ("refill ~never", the idiom the rest of this file already uses)
	// instead of 1/s. At 1 token per second the burst phase raced the WALL CLOCK:
	// four round trips to Postgres take microseconds on an idle box and seconds on
	// a loaded one, so the bucket refilled mid-test and the 4th take was admitted.
	// Measured on the shared CI host (run 30652967521, job 91240533286): this test
	// took 22.97s and failed as "4th take should deny". The refill half below stays
	// exact because it does not wait — it rewinds last_take.
	r := reqs(id, 0.001, 3, 0.001, 100)

	for i := 0; i < 3; i++ {
		ok, _ := take(t, st, r)
		if !ok {
			t.Fatalf("take %d should admit (burst 3)", i)
		}
	}
	ok, states := take(t, st, r)
	if ok {
		t.Fatal("4th take should deny")
	}
	if states[0].Tokens >= 1 {
		t.Fatalf("denied class bucket should be depleted, got %v", states[0].Tokens)
	}

	// Refill: 2000 idle seconds at rate 0.001 ⇒ ~2 tokens back. Rewound, not waited,
	// so the arithmetic is exact regardless of how loaded the machine is.
	rewindLastTake(t, db, id+"|read", 2000*time.Second)
	ok, states = take(t, st, r)
	if !ok {
		t.Fatalf("after refill the take should admit (tokens %v)", states[0].Tokens)
	}
}

// TestDenialDecrementsNothingAndAdvancesLastTake pins the two invariants the
// in-proc limiter tests pin: a denial consumes no tokens from ANY bucket, and
// last_take still advances (so the sweep can never reap an actively-denied
// bucket and hand the attacker a fresh burst).
func TestDenialDecrementsNothingAndAdvancesLastTake(t *testing.T) {
	st, db := openStore(t)
	id := fmt.Sprintf("t2-%d", time.Now().UnixNano())
	r := reqs(id, 0.001, 1, 0.001, 1) // one token, refill ~never

	if ok, _ := take(t, st, r); !ok {
		t.Fatal("first take should admit")
	}
	tokensBefore, _ := bucketRow(t, db, id+"|*")
	rewindLastTake(t, db, id+"|read", 30*time.Second)
	rewindLastTake(t, db, id+"|*", 30*time.Second)
	_, oldLast := bucketRow(t, db, id+"|read")

	if ok, _ := take(t, st, r); ok {
		t.Fatal("second take should deny")
	}
	tokensAfter, newLast := bucketRow(t, db, id+"|read")
	if !newLast.After(oldLast) {
		t.Fatal("last_take must advance on a DENIED take (sweep safety)")
	}
	aggAfter, _ := bucketRow(t, db, id+"|*")
	// The denied take refilled ~0.03 tokens (30s × 0.001/s) and decremented
	// nothing; both buckets must still be under 1 token.
	if tokensAfter >= 1 || aggAfter >= 1 {
		t.Fatalf("denied take must not decrement; class=%v agg=%v (before agg=%v)", tokensAfter, aggAfter, tokensBefore)
	}
}

// TestAllOrNothing: the aggregate bucket binds — a depleted aggregate denies
// even when the class bucket has tokens, and the class bucket is NOT charged.
func TestAllOrNothing(t *testing.T) {
	st, db := openStore(t)
	id := fmt.Sprintf("t3-%d", time.Now().UnixNano())
	r := reqs(id, 0.001, 10, 0.001, 2) // aggregate is the ceiling

	for i := 0; i < 2; i++ {
		if ok, _ := take(t, st, r); !ok {
			t.Fatalf("take %d should admit", i)
		}
	}
	classBefore, _ := bucketRow(t, db, id+"|read")
	if ok, _ := take(t, st, r); ok {
		t.Fatal("aggregate exhausted: take should deny")
	}
	classAfter, _ := bucketRow(t, db, id+"|read")
	if classAfter < classBefore {
		t.Fatalf("denied take charged the class bucket: %v -> %v", classBefore, classAfter)
	}
}

// TestRefillCapsAtBurst: a long-idle bucket refills to exactly full, never
// beyond (and restart-equivalence: stale last_take ⇒ one fresh full bucket).
func TestRefillCapsAtBurst(t *testing.T) {
	st, db := openStore(t)
	id := fmt.Sprintf("t4-%d", time.Now().UnixNano())
	r := reqs(id, 100, 5, 100, 50)

	take(t, st, r) // create (4 left)
	rewindLastTake(t, db, id+"|read", 3600*time.Second)
	_, states := take(t, st, r)
	// Hours idle at rate 100 would be 360k tokens; the cap is burst (5), minus
	// the one this take consumed.
	if states[0].Tokens != 4 {
		t.Fatalf("refill must cap at burst: want 4 tokens after take, got %v", states[0].Tokens)
	}
}

// TestTwoNodesShareBuckets is the HA property exists for: two Store
// instances (two simulated nodes) draw from ONE bucket — burst 4 admits 4
// takes TOTAL across nodes, not 4 per node.
func TestTwoNodesShareBuckets(t *testing.T) {
	// ONE isolated database, two nodes on it — that is the property under test.
	dsn := testDSN(t)
	stA, _ := openStoreOn(t, dsn)
	stB, err := pgstore.Open(context.Background(), dsn, pgstore.Options{})
	if err != nil {
		t.Fatalf("open node B: %v", err)
	}
	defer stB.Close()

	id := fmt.Sprintf("t5-%d", time.Now().UnixNano())
	r := reqs(id, 0.001, 4, 0.001, 100)

	admitted := 0
	for i := 0; i < 4; i++ {
		node := stA
		if i%2 == 1 {
			node = stB
		}
		if ok, _ := take(t, node, r); ok {
			admitted++
		}
	}
	if admitted != 4 {
		t.Fatalf("burst 4 should admit 4 across both nodes, got %d", admitted)
	}
	if okA, _ := take(t, stA, r); okA {
		t.Fatal("node A must be denied: the budget is GLOBAL, not per node")
	}
	if okB, _ := take(t, stB, r); okB {
		t.Fatal("node B must be denied: the budget is GLOBAL, not per node")
	}
}

// TestConcurrentTakesAdmitExactlyBurst mirrors the in-proc race test: under
// concurrency, exactly burst takes admit (no over- or under-admission).
func TestConcurrentTakesAdmitExactlyBurst(t *testing.T) {
	st, _ := openStore(t)
	id := fmt.Sprintf("t6-%d", time.Now().UnixNano())
	r := reqs(id, 0.001, 10, 0.001, 1000)

	const workers = 8
	const perWorker = 4 // 32 attempts against burst 10
	var (
		mu       sync.Mutex
		admitted int
		wg       sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				ok, _, err := st.Take(context.Background(), r)
				if err != nil {
					t.Errorf("take: %v", err)
					return
				}
				if ok {
					mu.Lock()
					admitted++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if admitted != 10 {
		t.Fatalf("exactly burst (10) must admit under concurrency, got %d", admitted)
	}
}

// TestSweepEvictsIdleOnly: the sweeper deletes buckets idle past the TTL and
// leaves fresh ones alone.
func TestSweepEvictsIdleOnly(t *testing.T) {
	dsn := testDSN(t)
	st, err := pgstore.Open(context.Background(), dsn, pgstore.Options{IdleTTL: 60 * time.Second})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("raw: %v", err)
	}
	defer db.Close()

	idOld := fmt.Sprintf("t7old-%d", time.Now().UnixNano())
	idNew := fmt.Sprintf("t7new-%d", time.Now().UnixNano())
	take(t, st, reqs(idOld, 1, 5, 1, 5))
	take(t, st, reqs(idNew, 1, 5, 1, 5))
	rewindLastTake(t, db, idOld+"|read", 2*time.Hour)
	rewindLastTake(t, db, idOld+"|*", 2*time.Hour)

	st.SweepOnceForTest()

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ratelimit_buckets WHERE key IN ($1, $2)`, idOld+"|read", idOld+"|*").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("idle buckets should be swept, %d remain", n)
	}
	if err := db.QueryRow(`SELECT count(*) FROM ratelimit_buckets WHERE key IN ($1, $2)`, idNew+"|read", idNew+"|*").Scan(&n); err != nil {
		t.Fatalf("count fresh: %v", err)
	}
	if n != 2 {
		t.Fatalf("fresh buckets must survive the sweep, got %d of 2", n)
	}
}

// TestLimiterOverStore drives the REAL Limiter through the store: in-proc
// semantics (429 decision shape, report-only) against shared global buckets.
func TestLimiterOverStore(t *testing.T) {
	st, _ := openStore(t)
	id := fmt.Sprintf("tn:t8-%d", time.Now().UnixNano())
	tiers := map[string]ratelimit.TierLimits{
		"default": {
			PerClass: map[ratelimit.EndpointClass]ratelimit.Limit{
				ratelimit.ClassRead:  {Rate: 0.001, Burst: 2},
				ratelimit.ClassWrite: {Rate: 0.001, Burst: 2},
			},
			Total: ratelimit.Limit{Rate: 0.001, Burst: 3},
		},
	}
	// This test asserts the limiter's SEMANTICS over a shared store, not how fast
	// the Postgres behind it happens to be. The default 250ms StoreTimeout is a
	// PRODUCTION latency budget: when a take overruns it the limiter degrades to
	// its per-node fallback exactly as designed (store.go, "Failure posture"), the
	// fresh local bucket still holds its full burst, and the 3rd read is ADMITTED.
	// Measured on the shared CI host (run 30645446605, job 91205974739): this
	// package took 393s with three Go jobs competing for the box, a take overran
	// the budget, and the failure surfaced as "3rd read should be limited" — a
	// message that says nothing about the real cause. So: give the round trip room,
	// and make any degradation fail LOUDLY as itself.
	var mu sync.Mutex
	var degraded []string
	l := ratelimit.New(ratelimit.Config{
		Tiers: tiers, DefaultTier: "default", Store: st,
		StoreTimeout: 30 * time.Second,
		LogWarn: func(msg string, err error) {
			mu.Lock()
			defer mu.Unlock()
			degraded = append(degraded, fmt.Sprintf("%s: %v", msg, err))
		},
	}, nil)
	// Checked as the FIRST assertion after the drive, before any conclusion is
	// drawn from a decision that may have come from the local shards.
	assertServedByStore := func() {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(degraded) > 0 {
			t.Fatalf("the limiter fell back to per-node buckets, so what follows measured the "+
				"local shards and NOT the shared store: %v", degraded)
		}
	}

	for i := 0; i < 2; i++ {
		if d := l.Allow(context.Background(), id, "default", ratelimit.ClassRead); !d.OK {
			t.Fatalf("read %d should admit", i)
		}
	}
	d := l.Allow(context.Background(), id, "default", ratelimit.ClassRead)
	assertServedByStore()
	if d.OK || !d.Limited {
		t.Fatal("3rd read should be limited (class burst 2)")
	}
	if d.RetryAfter < 1 {
		t.Fatalf("Retry-After must be >= 1, got %d", d.RetryAfter)
	}
	// The aggregate (burst 3) has 1 left after two reads: one write admits, the
	// next is denied by the AGGREGATE even though the write class has burst 2.
	if d := l.Allow(context.Background(), id, "default", ratelimit.ClassWrite); !d.OK {
		t.Fatal("first write should admit (aggregate has 1 left)")
	}
	if d := l.Allow(context.Background(), id, "default", ratelimit.ClassWrite); d.OK {
		t.Fatal("second write must be denied by the aggregate ceiling")
	}
	assertServedByStore()
}
