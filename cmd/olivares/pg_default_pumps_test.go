// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/reporting"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// reportTestHost is the module host a stand-alone reporting module needs.
type reportTestHost struct{}

func (reportTestHost) Publish(context.Context, event.Event) error { return nil }
func (reportTestHost) Subscribe([]event.Type, event.Handler) (func(), error) {
	return func() {}, nil
}
func (reportTestHost) Logger() *slog.Logger { return discardLogger() }
func (reportTestHost) Config() sdk.Config   { return sdk.Config{} }

// On the default PostgreSQL install (application and owner roles, no BYPASSRLS
// admin pool) a due report schedule fires: the schedule pump enumerates the
// tenants every install can read instead of skipping each tick. The report
// schedule pump stands for every best-effort pump moved with it.
func TestDefaultPostgresReportScheduleFires(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "postgres", DSN: pg.App, OwnerDSN: pg.Owner,
		Version: version, Logger: discardLogger(), ApplyModuleProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	tok, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	code, setup, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodPost, "/v1/setup", "", "", map[string]any{"token": tok, "email": "root@x.io", "password": "supersecret1"})
	if code != http.StatusCreated {
		t.Fatalf("setup = %d: %s", code, raw)
	}
	org, _ := setup["organization"].(map[string]any)
	tenantID, _ := org["tenant_id"].(string)
	tenant := model.TenantID(tenantID)

	// The store-backed scheduler the commercial build wires, on this engine's store.
	sched := reporting.NewStoreScheduler()
	rep := reporting.New(reporting.WithScheduler(sched))
	if err := rep.Init(ctx, reportTestHost{}); err != nil {
		t.Fatal(err)
	}
	rep.UseData(api.NewModuleData(eng.store))
	if err := sched.ScheduleReport(ctx, tenant, reporting.ScheduleConfig{
		ReportType: reporting.ReportAuditSummary, Format: reporting.FormatHTML, Cron: "* * * * *", Enabled: true,
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	pump := newReportSchedulePump(func(string) string { return "" }, eng.store, rep, discardLogger())
	if pump == nil {
		t.Fatal("no report schedule pump with a wired scheduler")
	}
	if !eng.store.Leader().Active() {
		t.Fatal("precondition: this engine is not the active writer, so the pump would not tick")
	}
	pump.clock = func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
	if err := pump.runOnce(ctx); err != nil {
		t.Fatalf("pump tick = %v", err)
	}
	schedules, err := sched.ListSchedules(ctx, tenant)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("schedules = %v (%v)", schedules, err)
	}
	runs, err := sched.ListRuns(ctx, tenant, schedules[0].ID)
	if err != nil || len(runs) != 1 || runs[0].Status != "ok" {
		t.Fatalf("runs after one tick = %+v (%v), want one ok run", runs, err)
	}
}
