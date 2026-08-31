// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/sdk"
)

const (
	Name      = "olivares.reporting"
	Namespace = "reporting"
)

// Module is the report-generation module.
type Module struct {
	log  *slog.Logger
	data api.ModuleData

	compliance ComplianceSource

	engine *Engine
	cache  *Cache

	scheduler  ReportScheduler
	branding   BrandingProvider
	customTmpl CustomTemplateProvider
	enterprise EnterpriseReportSource
}

// Option configures the module.
type Option func(*Module)

func WithComplianceSource(s ComplianceSource) Option { return func(m *Module) { m.compliance = s } }
func WithScheduler(s ReportScheduler) Option         { return func(m *Module) { m.scheduler = s } }
func WithBranding(b BrandingProvider) Option         { return func(m *Module) { m.branding = b } }
func WithCustomTemplates(t CustomTemplateProvider) Option {
	return func(m *Module) { m.customTmpl = t }
}

// New constructs a reporting module.
func New(opts ...Option) *Module {
	m := &Module{}
	for _, o := range opts {
		o(m)
	}
	return m
}

var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Report Generation",
		Description: "Professional PDF/HTML report generation from compliance, audit and FinOps data.",
	}
}

func (m *Module) UseData(d api.ModuleData) {
	m.data = d
	// Propagate the late-bound data handle to any wired provider or the
	// enterprise report source (they are constructed before the store exists).
	for _, p := range []any{m.scheduler, m.branding, m.customTmpl, m.enterprise} {
		if b, ok := p.(dataBinder); ok {
			b.bindData(d)
		}
	}
}

func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger().With("module", Name)
	m.engine = NewEngine(m.log)
	m.cache = NewCache(m.log)
	return nil
}

func (m *Module) Start(_ context.Context) error {
	if m.compliance == nil {
		m.log.Warn("compliance data source not wired; compliance-evidence reports will be empty")
	}
	if m.data == nil {
		m.log.Warn("data handle not wired; report data gathering will fail")
	}
	return nil
}

func (m *Module) Stop(_ context.Context) error {
	if m.cache != nil {
		m.cache.Close()
	}
	return nil
}

func (m *Module) APINamespace() string { return Namespace }

func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{
		permReportRead,
		permReportWrite,
	}
}
