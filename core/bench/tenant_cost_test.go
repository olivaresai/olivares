// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package bench

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const rlsBenchmarkTenants = 100

// BenchmarkTenantProvision measures complete tenant provisioning, including the
// tenant audit genesis and default workspace. openBench has already called
// EnsureSystemTenant once before this measured loop.
func BenchmarkTenantProvision(b *testing.B) {
	eachBackend(b, func(b *testing.B, st store.Store, _ model.TenantID) {
		ctx := context.Background()
		lat := make([]time.Duration, b.N)
		prefix := time.Now().UnixNano()
		start := time.Now()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			t0 := time.Now()
			slug := fmt.Sprintf("bench-tenant-%d-%d", prefix, i)
			err := st.System(ctx, func(sys store.SystemScope) error {
				_, err := sys.CreateOrg(ctx, model.Org{
					Name: slug, Slug: slug, Status: model.StatusActive,
				})
				return err
			})
			if err != nil {
				b.Fatalf("provision tenant %d: %v", i, err)
			}
			lat[i] = time.Since(t0)
		}
		b.StopTimer()
		elapsed := time.Since(start)
		reportLatency(b, lat, elapsed)
		b.ReportMetric(float64(len(lat))/elapsed.Seconds(), "provisions/sec")
	})
}

// BenchmarkRLSOverhead compares tenant-scoped reads rotating across many
// tenants with the same query against one hot tenant. BindTenant applies the
// tenant predicate inside every View, so both paths include the real RLS cost.
func BenchmarkRLSOverhead(b *testing.B) {
	eachBackend(b, func(b *testing.B, st store.Store, first model.TenantID) {
		tenants := provisionRLSBenchmarkTenants(b, st, first, rlsBenchmarkTenants)
		b.Run("distinct", func(b *testing.B) {
			benchmarkScopedAgentList(b, st, tenants, true)
		})
		b.Run("hot", func(b *testing.B) {
			benchmarkScopedAgentList(b, st, tenants, false)
		})
	})
}

func provisionRLSBenchmarkTenants(b *testing.B, st store.Store, first model.TenantID, n int) []model.TenantID {
	b.Helper()
	ctx := context.Background()
	tenants := make([]model.TenantID, 0, n)
	tenants = append(tenants, first)
	prefix := time.Now().UnixNano()
	for i := 1; i < n; i++ {
		slug := fmt.Sprintf("bench-rls-%d-%d", prefix, i)
		var tenant model.TenantID
		if err := st.System(ctx, func(sys store.SystemScope) error {
			org, err := sys.CreateOrg(ctx, model.Org{
				Name: slug, Slug: slug, Status: model.StatusActive,
			})
			if err == nil {
				tenant = org.TenantID
			}
			return err
		}); err != nil {
			b.Fatalf("provision RLS tenant %d: %v", i, err)
		}
		tenants = append(tenants, tenant)
	}
	return tenants
}

func benchmarkScopedAgentList(b *testing.B, st store.Store, tenants []model.TenantID, distinct bool) {
	b.Helper()
	ctx := context.Background()
	lat := make([]time.Duration, b.N)
	start := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tenant := tenants[0]
		if distinct {
			tenant = tenants[i%len(tenants)]
		}
		t0 := time.Now()
		err := st.View(ctx, tenant, func(sc store.Scope) error {
			_, _, err := sc.Agents().List(ctx, model.Query{})
			return err
		})
		if err != nil {
			b.Fatalf("scoped agent list %d: %v", i, err)
		}
		lat[i] = time.Since(t0)
	}
	b.StopTimer()
	reportLatency(b, lat, time.Since(start))
	b.ReportMetric(float64(len(tenants)), "tenants")
}
