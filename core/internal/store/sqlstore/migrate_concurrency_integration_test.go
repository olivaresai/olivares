// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// TestConcurrentMigrateApplyPostgres proves the cluster-wide migration advisory
// lock serializes schema application across nodes: several stores opened
// concurrently against ONE Postgres all succeed — no node races the DDL of
// another. Before the lock, two nodes booting against the same database could run
// the additive reconcile / module migrations simultaneously and one would error.
// DSN-gated, like the rest of the Postgres suite.
func TestConcurrentMigrateApplyPostgres(t *testing.T) {
	// its own database. The olivares.migrate.v1 DDL lock is shared with
	// core/api/ratelimit/pgstore, whose test binary runs CONCURRENTLY with this one
	// under `go test ./...`; on the shared database that contention was real.
	dsn := isolatedPG(t).App
	ctx := context.Background()
	const nodes = 4

	var wg sync.WaitGroup
	errs := make([]error, nodes)
	stores := make([]store.Store, nodes)
	wg.Add(nodes)
	for i := 0; i < nodes; i++ {
		go func(i int) {
			defer wg.Done()
			st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 6}, registerWidget)
			stores[i], errs[i] = st, err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Open #%d failed (migration race?): %v", i, err)
			continue
		}
		_ = stores[i].Close()
	}
}
