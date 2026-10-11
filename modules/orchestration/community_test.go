// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise || !addon_ids

package orchestration

import (
	"context"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"net/http"
	"net/http/httptest"
	"testing"
)

type unavailableRouteProbe struct {
	t     *testing.T
	count int
	mc    api.ModuleContext
}

func (p *unavailableRouteProbe) Handle(method, path string, _ auth.Permission, h api.ModuleHandler) {
	p.count++
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(method, path, nil), p.mc)
	if w.Code != http.StatusNotImplemented {
		p.t.Errorf("%s %s: got %d, want 501", method, path, w.Code)
	}
}

func (p *unavailableRouteProbe) HandleEntity(method, path string, perm auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	p.Handle(method, path, perm, h)
}

func TestCommunityOrchestrationUnavailable(t *testing.T) {
	m := New()
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tenant := model.SystemTenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	p := &unavailableRouteProbe{t: t, mc: api.ModuleContext{Tenant: tenant, Data: api.NewScopedData(st, tenant)}}
	m.APIRoutes(p)
	if p.count == 0 {
		t.Fatal("published orchestration routes must report unavailability")
	}
}
