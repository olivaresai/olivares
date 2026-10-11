// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// exportScales are the seeded ledger sizes for read-loop and Business export costs. They
// are deliberately small enough to run inside a normal bench budget and far
// enough apart to show whether per-event cost is flat or grows with the ledger
// — the question the cost model actually asks. The drill's L scale is a
// multiple of these, extrapolated from a measured per-event figure rather than
// guessed (an internal design note (not shipped)).
var exportScales = []int{1_000, 10_000, 50_000}

func scaleName(events int) string {
	switch {
	case events >= 1_000_000:
		return "1M"
	case events >= 1_000:
		return itoaK(events)
	default:
		return itoa(events)
	}
}

func itoaK(events int) string { return itoa(events/1_000) + "k" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// seedAuditEvents appends n signed, hash-chained events to the tenant's ledger.
// It runs OUTSIDE any timer: the cost of writing the ledger is measured by
// BenchmarkStorageGrowthAuditAppend, not here.
func seedAuditEvents(b *testing.B, st store.Store, tenant model.TenantID, n int) {
	b.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor:     "bench",
				ActorKind: model.ActorSystem,
				Action:    "bench.export.seed",
				Meta:      map[string]any{"src": "export-cost-bench", "i": i},
			})
			return err
		})
		if err != nil {
			b.Fatalf("seed audit event %d: %v", i, err)
		}
	}
}

// BenchmarkReadLoopCost measures the OTHER denial-of-wallet vector of the cost
// drill: a caller streaming the whole ledger through the read path in a loop
// instead of asking for an archive. It reports CPU per event and the serialized
// bytes an API would have to put on the wire, so the byte budget can be shared
// between this vector and the export — a cap counted in requests is trivially
// evaded by ranging, a cap counted in BYTES is not.
func BenchmarkReadLoopCost(b *testing.B) {
	for _, events := range exportScales {
		b.Run(scaleName(events)+"/sqlite", func(b *testing.B) {
			dir := b.TempDir()
			st, tenant := openBench(b, store.Config{
				Engine: store.EngineSQLite,
				DSN:    filepath.Join(dir, "bench.db"),
			})
			defer func() { _ = st.Close() }()
			seedAuditEvents(b, st, tenant, events)
			benchmarkReadLoop(b, st, tenant, events)
		})

		if dsn := os.Getenv("OLIVARES_TEST_POSTGRES_DSN"); dsn != "" {
			b.Run(scaleName(events)+"/postgres", func(b *testing.B) {
				st, tenant := openBench(b, store.Config{
					Engine:   store.EnginePostgres,
					DSN:      dsn,
					MaxConns: 16,
				})
				defer func() { _ = st.Close() }()
				seedAuditEvents(b, st, tenant, events)
				benchmarkReadLoop(b, st, tenant, events)
			})
		}
	}
}

// benchmarkReadLoop times a full ledger walk and sizes what serving it would
// cost on the wire. The JSON encoding is the honest proxy for the API surface:
// it is what a read endpoint returns, and it is measured per event rather than
// assumed equal to the archive's segment encoding — the two differ, and the
// difference is exactly what a byte budget has to account for.
func benchmarkReadLoop(b *testing.B, st store.Store, tenant model.TenantID, events int) {
	b.Helper()
	ctx := context.Background()
	var wireBytes int64
	var seen int

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var total int64
		var n int
		err := st.View(ctx, tenant, func(sc store.Scope) error {
			return sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
				enc, err := json.Marshal(ev)
				if err != nil {
					return err
				}
				total += int64(len(enc))
				n++
				return nil
			})
		})
		if err != nil {
			b.Fatalf("walk ledger: %v", err)
		}
		wireBytes, seen = total, n
	}
	b.StopTimer()

	if seen == 0 {
		b.Fatalf("walk returned no events; the ledger seed did not land")
	}
	b.ReportMetric(float64(wireBytes), "wire_bytes")
	b.ReportMetric(float64(wireBytes)/float64(seen), "wire_bytes/event")
	b.ReportMetric(float64(seen), "events_walked")
}
