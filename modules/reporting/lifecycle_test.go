// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

func TestLifecycleDescriptorNamespacePermissionsAndOptions(t *testing.T) {
	scheduler := stubScheduler{}
	branding := stubBranding{}
	templates := stubTemplates{}
	compliance := &e3bComplianceSource{}
	m := New(
		WithComplianceSource(compliance),
		WithScheduler(scheduler),
		WithBranding(branding),
		WithCustomTemplates(templates),
	)

	desc := m.Descriptor()
	if desc.Name != Name || desc.Type != sdk.TypeModule || desc.APIVersion != sdk.APIVersion {
		t.Fatalf("Descriptor = %+v", desc)
	}
	if m.APINamespace() != Namespace {
		t.Fatalf("APINamespace = %q, want %q", m.APINamespace(), Namespace)
	}
	perms := m.Permissions()
	if len(perms) != 2 || perms[0] != permReportRead || perms[1] != permReportWrite {
		t.Fatalf("Permissions = %+v", perms)
	}
	if m.compliance != compliance || m.scheduler == nil || m.branding == nil || m.customTmpl == nil {
		t.Fatalf("options not wired: %+v", m)
	}

	data := fakeModuleData{}
	m.UseData(data)
	if m.data == nil {
		t.Fatal("UseData did not wire data handle")
	}
	t.Setenv("OLIVARES_REPORT_CACHE_DIR", t.TempDir())
	if err := m.Init(context.Background(), testHost{}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if m.log == nil || m.engine == nil || m.cache == nil {
		t.Fatalf("Init did not wire logger/engine/cache: log=%v engine=%v cache=%v", m.log, m.engine, m.cache)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(m.cache.entries) != 0 {
		t.Fatalf("cache entries after Stop = %d, want 0", len(m.cache.entries))
	}
}

type testHost struct{}

func (testHost) Publish(context.Context, event.Event) error { return nil }
func (testHost) Subscribe([]event.Type, event.Handler) (func(), error) {
	return func() {}, nil
}
func (testHost) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
func (testHost) Config() sdk.Config { return sdk.Config{} }

type fakeModuleData struct{}

func (fakeModuleData) View(context.Context, model.TenantID, func(store.Scope) error) error {
	return nil
}

func (fakeModuleData) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	return nil
}

type stubScheduler struct{}

func (stubScheduler) ScheduleReport(context.Context, model.TenantID, ScheduleConfig) error {
	return nil
}
func (stubScheduler) ListSchedules(context.Context, model.TenantID) ([]ScheduleConfig, error) {
	return nil, nil
}
func (stubScheduler) DeleteSchedule(context.Context, model.TenantID, string) error { return nil }
func (stubScheduler) RecordRun(context.Context, model.TenantID, ScheduleRun) error { return nil }
func (stubScheduler) ListRuns(context.Context, model.TenantID, string) ([]ScheduleRun, error) {
	return nil, nil
}

type stubBranding struct{}

func (stubBranding) GetBranding(context.Context, model.TenantID) (BrandingConfig, error) {
	return BrandingConfig{}, nil
}
func (stubBranding) SetBranding(context.Context, model.TenantID, BrandingConfig) error {
	return nil
}

type stubTemplates struct{}

func (stubTemplates) GetTemplate(context.Context, model.TenantID, ReportType) (string, bool, error) {
	return "", false, nil
}
func (stubTemplates) SetTemplate(context.Context, model.TenantID, ReportType, string) error {
	return nil
}
func (stubTemplates) DeleteTemplate(context.Context, model.TenantID, ReportType) error {
	return nil
}
