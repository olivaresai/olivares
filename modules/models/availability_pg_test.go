// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestAvailableModelsPG16AppRole(t *testing.T) {
	dsn := os.Getenv("OLIVARES_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PostgreSQL app-role fixture is not configured")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("could not connect to the app-role fixture")
	}
	var serverVersion int
	var superuser, bypassRLS bool
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int, rolsuper, rolbypassrls FROM pg_roles WHERE rolname=current_user").Scan(&serverVersion, &superuser, &bypassRLS); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)
	if serverVersion < 160000 || serverVersion >= 170000 || superuser || bypassRLS {
		t.Fatalf("expected PG16 app role: version=%d superuser=%t bypassrls=%t", serverVersion, superuser, bypassRLS)
	}
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant, other model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		one, err := sys.CreateOrg(ctx, model.Org{Name: "Models PG one", Slug: "models-pg-one", Status: model.StatusActive})
		if err != nil {
			return err
		}
		tenant = one.TenantID
		two, err := sys.CreateOrg(ctx, model.Org{Name: "Models PG two", Slug: "models-pg-two", Status: model.StatusActive})
		other = two.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	f := &availabilityFixture{sources: []AvailabilitySource{{Ref: "prv_pg_fixture", ProviderRef: "prv_pg_fixture", ProviderKind: "ollama", Revision: "v1"}}, ids: []string{"qwen3:8b"}}
	now := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
	m.UseAvailabilitySource(f, func(context.Context) ([]model.TenantID, error) { return nil, nil })
	m.availability.now = func() time.Time { return now }
	_ = m.Start(ctx)
	t.Cleanup(func() { _ = m.Stop(ctx) })
	if err := m.RefreshAvailability(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	first, err := m.AvailableModels(ctx, tenant)
	if err != nil || len(first) != 1 || first[0].State != "fresh" {
		t.Fatalf("app-role catalog: %+v %v", first, err)
	}
	now = now.Add(availabilityRefreshInterval)
	if err := m.RefreshAvailability(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	second, err := m.AvailableModels(ctx, tenant)
	if err != nil || second[0].Version != first[0].Version {
		t.Fatalf("idle write: %+v %v", second, err)
	}
	foreign, err := m.AvailableModels(ctx, other)
	if err != nil || len(foreign) != 1 || foreign[0].SeenAt != "" {
		t.Fatalf("tenant leaked snapshot: %+v %v", foreign, err)
	}
}
