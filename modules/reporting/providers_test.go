// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// es07Harness stands up a real in-memory store with the reporting module's
// schema registered and wires in-memory providers, so these tests exercise
// the module's routes, cache and schedule execution. The store-backed providers
// are a Business capability; their persistence is tested where they live.
type es07Harness struct {
	t      *testing.T
	store  store.Store
	module *Module
	tenant model.TenantID
	mc     api.ModuleContext
}

func newES07Harness(t *testing.T) *es07Harness {
	t.Helper()
	ctx := context.Background()

	m := New(
		WithScheduler(&memScheduler{}),
		WithBranding(&memBranding{}),
		WithCustomTemplates(&memTemplates{}),
	)
	for _, option := range compliancePacksTestOptions() {
		option(m)
	}
	if err := m.Init(ctx, testHost{}); err != nil {
		t.Fatalf("init: %v", err)
	}

	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		if err != nil {
			return err
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}

	m.UseData(api.NewModuleData(st))
	return &es07Harness{
		t: t, store: st, module: m, tenant: tenant,
		mc: api.ModuleContext{Tenant: tenant, Data: api.NewScopedData(st, tenant)},
	}
}

// memScheduler keeps schedules and runs per tenant in memory.
type memScheduler struct {
	mu        sync.Mutex
	next      int
	schedules map[model.TenantID][]ScheduleConfig
	runs      map[model.TenantID][]ScheduleRun
}

func (s *memScheduler) ScheduleReport(_ context.Context, tenant model.TenantID, cfg ScheduleConfig) error {
	if _, err := ParseCronSpec(cfg.Cron); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.schedules == nil {
		s.schedules = map[model.TenantID][]ScheduleConfig{}
	}
	for i, existing := range s.schedules[tenant] {
		if cfg.ID != "" && existing.ID == cfg.ID {
			s.schedules[tenant][i] = cfg
			return nil
		}
	}
	s.next++
	cfg.ID = fmt.Sprintf("schedule-%d", s.next)
	s.schedules[tenant] = append(s.schedules[tenant], cfg)
	return nil
}

func (s *memScheduler) ListSchedules(_ context.Context, tenant model.TenantID) ([]ScheduleConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ScheduleConfig(nil), s.schedules[tenant]...), nil
}

func (s *memScheduler) DeleteSchedule(_ context.Context, tenant model.TenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.schedules[tenant] {
		if existing.ID == id {
			s.schedules[tenant] = append(s.schedules[tenant][:i], s.schedules[tenant][i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *memScheduler) RecordRun(_ context.Context, tenant model.TenantID, run ScheduleRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs == nil {
		s.runs = map[model.TenantID][]ScheduleRun{}
	}
	s.next++
	run.ID = fmt.Sprintf("run-%d", s.next)
	s.runs[tenant] = append(s.runs[tenant], run)
	return nil
}

func (s *memScheduler) ListRuns(_ context.Context, tenant model.TenantID, scheduleID string) ([]ScheduleRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ScheduleRun
	for _, run := range s.runs[tenant] {
		if run.ScheduleID == scheduleID {
			out = append(out, run)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RanAt < out[j].RanAt })
	return out, nil
}

// memBranding keeps one branding per tenant in memory.
type memBranding struct {
	mu  sync.Mutex
	cfg map[model.TenantID]BrandingConfig
}

func (b *memBranding) GetBranding(_ context.Context, tenant model.TenantID) (BrandingConfig, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg[tenant], nil
}

func (b *memBranding) SetBranding(_ context.Context, tenant model.TenantID, cfg BrandingConfig) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cfg == nil {
		b.cfg = map[model.TenantID]BrandingConfig{}
	}
	b.cfg[tenant] = cfg
	return nil
}

// memTemplates keeps custom templates per tenant and report type in memory.
type memTemplates struct {
	mu   sync.Mutex
	html map[model.TenantID]map[ReportType]string
}

func (t *memTemplates) GetTemplate(_ context.Context, tenant model.TenantID, rt ReportType) (string, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	html, ok := t.html[tenant][rt]
	return html, ok, nil
}

func (t *memTemplates) SetTemplate(_ context.Context, tenant model.TenantID, rt ReportType, html string) error {
	if err := ValidateCustomTemplate(html); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.html == nil {
		t.html = map[model.TenantID]map[ReportType]string{}
	}
	if t.html[tenant] == nil {
		t.html[tenant] = map[ReportType]string{}
	}
	t.html[tenant][rt] = html
	return nil
}

func (t *memTemplates) DeleteTemplate(_ context.Context, tenant model.TenantID, rt ReportType) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.html[tenant][rt]; !ok {
		return fmt.Errorf("no custom template for %s", rt)
	}
	delete(t.html[tenant], rt)
	return nil
}
