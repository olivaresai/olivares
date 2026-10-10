// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"io"
	"log/slog"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

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
