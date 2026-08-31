// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// es07Harness stands up a real in-memory store WITH the reporting module's
// schema registered, wires the store-backed providers, and binds the data handle
// exactly as the composition root does — so these tests exercise the real
// persistence path, not a fake.
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
		WithScheduler(NewStoreScheduler()),
		WithBranding(NewStoreBranding()),
		WithCustomTemplates(NewStoreCustomTemplates()),
	)
	if err := m.Init(ctx, stubHost{}); err != nil {
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

type stubHost struct{}

func (stubHost) Logger() *slog.Logger                       { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func (stubHost) Publish(context.Context, event.Event) error { return nil }
func (stubHost) Subscribe([]event.Type, event.Handler) (func(), error) {
	return func() {}, nil
}
func (stubHost) Config() sdk.Config { return sdk.Config{} }

func TestES07SchedulerCRUDAndRuns(t *testing.T) {
	h := newES07Harness(t)
	ctx := context.Background()

	// Reject an invalid cron at creation.
	if err := h.module.scheduler.ScheduleReport(ctx, h.tenant, ScheduleConfig{
		ReportType: ReportAuditSummary, Cron: "not a cron", Enabled: true,
	}); err == nil {
		t.Fatal("invalid cron must be rejected")
	}

	if err := h.module.scheduler.ScheduleReport(ctx, h.tenant, ScheduleConfig{
		ReportType: ReportAuditSummary, Format: FormatHTML, Cron: "0 2 * * *", Enabled: true,
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	schedules, err := h.module.scheduler.ListSchedules(ctx, h.tenant)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("list schedules = %+v (%v); want 1", schedules, err)
	}
	id := schedules[0].ID
	if schedules[0].Cron != "0 2 * * *" || !schedules[0].Enabled {
		t.Fatalf("schedule roundtrip = %+v", schedules[0])
	}

	// Record two runs; ListRuns returns them newest-last.
	for _, ts := range []string{"2026-07-01T02:00:00Z", "2026-07-02T02:00:00Z"} {
		if err := h.module.scheduler.RecordRun(ctx, h.tenant, ScheduleRun{
			ScheduleID: id, ReportType: string(ReportAuditSummary), Format: "html", RanAt: ts, Status: "ok",
			Output: []byte("<html>ok</html>"),
		}); err != nil {
			t.Fatalf("record run: %v", err)
		}
	}
	runs, err := h.module.scheduler.ListRuns(ctx, h.tenant, id)
	if err != nil || len(runs) != 2 {
		t.Fatalf("list runs = %d (%v); want 2", len(runs), err)
	}
	if runs[0].RanAt > runs[1].RanAt {
		t.Fatal("runs must be ordered oldest-first")
	}

	// Delete cascades the run history.
	if err := h.module.scheduler.DeleteSchedule(ctx, h.tenant, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if left, _ := h.module.scheduler.ListSchedules(ctx, h.tenant); len(left) != 0 {
		t.Fatalf("schedule not deleted: %+v", left)
	}
	if runs, _ := h.module.scheduler.ListRuns(ctx, h.tenant, id); len(runs) != 0 {
		t.Fatalf("runs not cascaded: %d", len(runs))
	}
}

func TestES07RunDueSchedulesFiresAndDoesNotDoubleFire(t *testing.T) {
	h := newES07Harness(t)
	ctx := context.Background()

	// An always-due schedule (every minute) for a store-only report (audit-summary
	// needs no compliance source).
	if err := h.module.scheduler.ScheduleReport(ctx, h.tenant, ScheduleConfig{
		ReportType: ReportAuditSummary, Format: FormatHTML, Cron: "* * * * *", Enabled: true,
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	ran, err := h.module.RunDueSchedules(ctx, h.mc, now)
	if err != nil {
		t.Fatalf("run due: %v", err)
	}
	if ran != 1 {
		t.Fatalf("ran = %d; want 1 due schedule fired", ran)
	}
	schedules, _ := h.module.scheduler.ListSchedules(ctx, h.tenant)
	runs, _ := h.module.scheduler.ListRuns(ctx, h.tenant, schedules[0].ID)
	if len(runs) != 1 || runs[0].Status != "ok" || len(runs[0].Output) == 0 {
		t.Fatalf("run not recorded with output: %+v", runs)
	}

	// Same instant again: the last run == now, so nothing re-fires.
	ran2, err := h.module.RunDueSchedules(ctx, h.mc, now)
	if err != nil {
		t.Fatalf("run due 2: %v", err)
	}
	if ran2 != 0 {
		t.Fatalf("ran2 = %d; a schedule already run at this instant must not re-fire", ran2)
	}
}

func TestES07BrandingRoundtrip(t *testing.T) {
	h := newES07Harness(t)
	ctx := context.Background()

	// Absent branding is the zero config, not an error.
	if cfg, err := h.module.branding.GetBranding(ctx, h.tenant); err != nil || cfg.CompanyName != "" {
		t.Fatalf("default branding = %+v (%v)", cfg, err)
	}
	want := BrandingConfig{PrimaryColor: "#123456", CompanyName: "Acme Corp", FooterText: "Confidential"}
	if err := h.module.branding.SetBranding(ctx, h.tenant, want); err != nil {
		t.Fatalf("set branding: %v", err)
	}
	got, err := h.module.branding.GetBranding(ctx, h.tenant)
	if err != nil || got != want {
		t.Fatalf("branding roundtrip = %+v (%v); want %+v", got, err, want)
	}
	// Update replaces the single row (no duplicate).
	want.CompanyName = "Acme Inc"
	if err := h.module.branding.SetBranding(ctx, h.tenant, want); err != nil {
		t.Fatalf("update branding: %v", err)
	}
	if got, _ := h.module.branding.GetBranding(ctx, h.tenant); got.CompanyName != "Acme Inc" {
		t.Fatalf("branding update = %+v", got)
	}
}

func TestES07CustomTemplateRoundtripAndValidation(t *testing.T) {
	h := newES07Harness(t)
	ctx := context.Background()

	// A malformed template is rejected at upload.
	if err := h.module.customTmpl.SetTemplate(ctx, h.tenant, ReportAuditSummary, "{{ .Report"); err == nil {
		t.Fatal("malformed template must be rejected")
	}

	tmpl := `<html><body>Total events: {{ .Report.TotalEvents }}</body></html>`
	if err := h.module.customTmpl.SetTemplate(ctx, h.tenant, ReportAuditSummary, tmpl); err != nil {
		t.Fatalf("set template: %v", err)
	}
	got, ok, err := h.module.customTmpl.GetTemplate(ctx, h.tenant, ReportAuditSummary)
	if err != nil || !ok || got != tmpl {
		t.Fatalf("template roundtrip = %q ok=%t (%v)", got, ok, err)
	}

	// renderWithCustomTemplate uses the stored template.
	out, err := h.module.renderWithCustomTemplate(ctx, h.tenant, ReportAuditSummary, AuditData{TotalEvents: 42}, "en", BrandingConfig{})
	if err != nil {
		t.Fatalf("render custom: %v", err)
	}
	if !strings.Contains(string(out), "Total events: 42") {
		t.Fatalf("custom template not applied: %s", out)
	}

	// Delete falls back to the built-in template.
	if err := h.module.customTmpl.DeleteTemplate(ctx, h.tenant, ReportAuditSummary); err != nil {
		t.Fatalf("delete template: %v", err)
	}
	if _, ok, _ := h.module.customTmpl.GetTemplate(ctx, h.tenant, ReportAuditSummary); ok {
		t.Fatal("template not deleted")
	}
}
