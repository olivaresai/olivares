// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// B2 (CUTS 2026-10-01): the launch gate must ask a cheap question — does this
// tenant have ANY admission target (an enabled budget, a spend-limit policy, or
// the lifecycle activation frontier) — and skip the full reserve when there is
// none. The probe reads live state on every call: a wrong "no" loses budget
// enforcement, which is the one answer that must be impossible.

func openTargetsStore(t testing.TB, m *Module) (store.Store, model.TenantID) {
	t.Helper()
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	return st, tenant
}

func createPolicy(t *testing.T, m *Module, st store.Store, tenant model.TenantID, kind string, enabled bool) model.ID {
	t.Helper()
	var id model.ID
	// Through the module's write funnel, exactly like the HTTP handlers, so the
	// targets cache is invalidated the way production invalidates it.
	err := m.mutate(context.Background(), tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: "p", Kind: kind, Enabled: enabled, Spec: map[string]any{},
		})
		id = p.ID
		return err
	})
	if err != nil {
		t.Fatalf("create %s policy: %v", kind, err)
	}
	return id
}

func TestHasAdmissionTargets(t *testing.T) {
	ctx := context.Background()
	m := New()
	st, tenant := openTargetsStore(t, m)

	probe := func() bool {
		t.Helper()
		has, err := m.HasAdmissionTargets(ctx, tenant)
		if err != nil {
			t.Fatalf("HasAdmissionTargets: %v", err)
		}
		return has
	}

	// A default tenant has no admission target: the gate skips the reserve.
	if probe() {
		t.Fatal("a tenant with nothing configured must answer false")
	}

	// A DISABLED budget alone is not a target: it never denies.
	createPolicy(t, m, st, tenant, policyKindBudget, false)
	if probe() {
		t.Fatal("a disabled budget is not an admission target")
	}

	// An enabled budget is.
	createPolicy(t, m, st, tenant, policyKindBudget, true)
	if !probe() {
		t.Fatal("an enabled budget must answer true")
	}

	// The lifecycle activation frontier is a target even with no budget: the
	// legacy reserve must still run so its guard can refuse.
	m2 := New()
	_, tenant2 := openTargetsStore(t, m2)
	// The canonical seed the t0 lifecycle tests use: the module's real
	// activation entry (wired with their fixture verifier), which creates the
	// quiescing frontier row.
	t0Wire(m2, tenant2)
	if _, err := m2.BeginLifecycleActivation(ctx, tenant2, LifecycleActivationRequest{
		Evidence: []EvidenceRef{labEvidence("jd-b2-frontier")},
	}); err != nil {
		t.Fatalf("seed lifecycle frontier: %v", err)
	}
	has, err := m2.HasAdmissionTargets(ctx, tenant2)
	if err != nil || !has {
		t.Fatalf("a tenant under the lifecycle frontier = %v, %v — the reserve must run so the guard can refuse", has, err)
	}
}

// B2 measurement for the commit body: the full zero-target reserve (what every
// launch paid before) against the probe (what it pays now), same empty tenant.
func BenchmarkReserveWithoutTargets(b *testing.B) {
	ctx := context.Background()
	m := New()
	_, tenant := openTargetsStore(b, m)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.Reserve(ctx, tenant, AdmissionRequest{
			Scope: AdmissionScopeSessionLaunch, IdempotencyKey: "bench-" + strconv.Itoa(i),
			EstimateMicroUSD: 1, ActorRef: "user:bench",
		}); err != nil {
			b.Fatalf("Reserve: %v", err)
		}
	}
}

func BenchmarkHasAdmissionTargets(b *testing.B) {
	ctx := context.Background()
	m := New()
	_, tenant := openTargetsStore(b, m)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.HasAdmissionTargets(ctx, tenant); err != nil {
			b.Fatalf("HasAdmissionTargets: %v", err)
		}
	}
}
