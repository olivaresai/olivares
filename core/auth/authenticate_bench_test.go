// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// B9 (2026-10-01, CORE-B-SPEC): authenticate reads Users().Get + loadGrants +
// loadStanding in one AuthView on EVERY request. It protects data (revocation
// is immediate), so it stays uncached unless this measurement says the read
// matters: p50/p99 of Authenticate on SQLite and PostgreSQL 16 with 1 and
// 1,000 grants. No cache in 26.10.1 regardless — this is the evidence for the
// 26.11 decision.

func seedGrants(tb *testing.T, st store.Store, userID model.ID, tenant model.TenantID, n int) {
	tb.Helper()
	ctx := context.Background()
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		if _, err := as.Memberships().Create(ctx, model.Membership{
			UserID: userID, TargetTenantID: tenant, Role: auth.RoleViewer,
		}); err != nil {
			return err
		}
		for i := 1; i < n; i++ {
			g, err := as.Groups().Create(ctx, model.UserGroup{
				TargetTenantID: tenant, DisplayName: fmt.Sprintf("grp-%04d", i), MappedRole: auth.RoleViewer,
			})
			if err != nil {
				return err
			}
			if _, err := as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: g.ID, UserID: userID}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		tb.Fatalf("seed %d grants: %v", n, err)
	}
}

func benchAuthenticate(tb *testing.T, st store.Store, grants int) (p50, p99 time.Duration) {
	ctx := context.Background()
	tenant := provisionTenant(tb, st, "bench")
	userID := seedUser(tb, st, "bench@x.io", "supersecret1")
	seedGrants(tb, st, userID, tenant, grants)
	a := auth.NewAuthenticator(st, nil)
	token, _, err := a.Login(ctx, "bench@x.io", "supersecret1", "10.0.0.1")
	if err != nil {
		tb.Fatalf("login: %v", err)
	}

	const reps = 2000
	lats := make([]time.Duration, 0, reps)
	// One warm call outside the timing: the first connect/plan is not the property.
	if _, err := a.Authenticate(ctx, token); err != nil {
		tb.Fatalf("warm authenticate: %v", err)
	}
	for i := 0; i < reps; i++ {
		start := time.Now()
		if _, err := a.Authenticate(ctx, token); err != nil {
			tb.Fatalf("authenticate: %v", err)
		}
		lats = append(lats, time.Since(start))
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	return lats[reps*50/100], lats[reps*99/100]
}

func openSQLiteBench(tb *testing.T) store.Store {
	st, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		tb.Fatalf("open sqlite: %v", err)
	}
	tb.Cleanup(func() { _ = st.Close() })
	return st
}

func openPostgresBench(tb *testing.T) store.Store {
	dsns := pgtest.Isolate(tb, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
	st, err := sqlstore.Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin,
	}, nil)
	if err != nil {
		tb.Fatalf("open postgres: %v", err)
	}
	tb.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAuthenticateLatencyB9(t *testing.T) {
	if testing.Short() {
		t.Skip("a latency measurement does not run in -short")
	}
	for _, leg := range []struct {
		name string
		open func(*testing.T) store.Store
	}{
		{"sqlite", openSQLiteBench},
		{"postgres16", openPostgresBench},
	} {
		for _, grants := range []int{1, 1000} {
			t.Run(fmt.Sprintf("%s/grants=%d", leg.name, grants), func(t *testing.T) {
				p50, p99 := benchAuthenticate(t, leg.open(t), grants)
				t.Logf("B9 %s grants=%d: p50=%s p99=%s", leg.name, grants, p50, p99)
			})
		}
	}
}
