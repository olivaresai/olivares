// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package bench

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const auditTable = "audit_events"

// BenchmarkStorageGrowthAuditAppend measures committed on-disk growth per
// signed, hash-chained audit event. Both engines report a delta, never their
// absolute accumulated size, plus the corresponding GiB per million events.
func BenchmarkStorageGrowthAuditAppend(b *testing.B) {
	b.Run("sqlite", func(b *testing.B) {
		dir := b.TempDir()
		path := filepath.Join(dir, "bench.db")
		st, tenant := openBench(b, store.Config{Engine: store.EngineSQLite, DSN: path})
		defer func() { _ = st.Close() }()

		before := sqliteOnDiskBytes(b, path)
		appendStorageGrowthEvents(b, st, tenant)
		after := sqliteOnDiskBytes(b, path)
		reportStorageGrowth(b, after-before)
	})

	if dsn := os.Getenv("OLIVARES_TEST_POSTGRES_DSN"); dsn != "" {
		b.Run("postgres", func(b *testing.B) {
			st, tenant := openBench(b, store.Config{
				Engine:   store.EnginePostgres,
				DSN:      dsn,
				MaxConns: 16,
			})
			defer func() { _ = st.Close() }()

			db, err := sql.Open("pgx", dsn)
			if err != nil {
				b.Fatalf("open postgres size probe: %v", err)
			}
			defer func() { _ = db.Close() }()

			before := postgresRelationBytes(b, db)
			appendStorageGrowthEvents(b, st, tenant)
			after := postgresRelationBytes(b, db)
			reportStorageGrowth(b, after-before)
		})
	}
}

func appendStorageGrowthEvents(b *testing.B, st store.Store, tenant model.TenantID) {
	b.Helper()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor:     "bench",
				ActorKind: model.ActorSystem,
				Action:    "bench.append",
				Meta:      map[string]any{"src": "storage-growth-bench"},
			})
			return err
		})
		if err != nil {
			b.Fatalf("append audit event %d: %v", i, err)
		}
	}
	b.StopTimer()
}

// sqliteOnDiskBytes returns the committed on-disk size of the SQLite database.
// The store keeps WAL mode on with a single connection open, so freshly committed
// rows can sit in bench.db-wal and the -wal file's size swings with the checkpoint
// cycle — summing main+WAL therefore over-counts and is noisy run to run. To get a
// stable committed figure we checkpoint (TRUNCATE) on a second connection — safe
// because the caller invokes this OUTSIDE the write loop, when the store's single
// connection is idle — which folds the WAL into the main file and truncates it,
// then stat the main file alone.
func sqliteOnDiskBytes(b *testing.B, path string) int64 {
	b.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		b.Fatalf("open sqlite size probe: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		b.Fatalf("checkpoint sqlite wal: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		b.Fatalf("stat sqlite db %s: %v", path, err)
	}
	return info.Size()
}

func postgresRelationBytes(b *testing.B, db *sql.DB) int64 {
	b.Helper()
	var size int64
	query := "SELECT pg_total_relation_size('" + auditTable + "')"
	if err := db.QueryRowContext(context.Background(), query).Scan(&size); err != nil {
		b.Fatalf("read postgres %s size: %v", auditTable, err)
	}
	return size
}

func reportStorageGrowth(b *testing.B, delta int64) {
	b.Helper()
	if delta < 0 {
		b.Fatalf("storage delta is negative: %d bytes", delta)
	}
	bytesPerEvent := float64(delta) / float64(b.N)
	b.ReportMetric(bytesPerEvent, "bytes/event")
	b.ReportMetric(bytesPerEvent*1e6/(1024*1024*1024), "gib_per_million")
}
