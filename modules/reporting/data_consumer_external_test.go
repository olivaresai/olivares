// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/reporting"
)

func TestLifecycleUseDataReachesExternalProviderReads(t *testing.T) {
	tests := []struct {
		name string
		wire func(*externalDataBinding) (reporting.Option, func(context.Context, model.TenantID) error)
	}{
		{"scheduler", func(b *externalDataBinding) (reporting.Option, func(context.Context, model.TenantID) error) {
			p := &externalScheduler{externalDataBinding: b}
			return reporting.WithScheduler(p), func(ctx context.Context, tenant model.TenantID) error {
				_, err := p.ListSchedules(ctx, tenant)
				return err
			}
		}},
		{"branding", func(b *externalDataBinding) (reporting.Option, func(context.Context, model.TenantID) error) {
			p := &externalBranding{externalDataBinding: b}
			return reporting.WithBranding(p), func(ctx context.Context, tenant model.TenantID) error {
				_, err := p.GetBranding(ctx, tenant)
				return err
			}
		}},
		{"templates", func(b *externalDataBinding) (reporting.Option, func(context.Context, model.TenantID) error) {
			p := &externalTemplates{externalDataBinding: b}
			return reporting.WithCustomTemplates(p), func(ctx context.Context, tenant model.TenantID) error {
				_, _, err := p.GetTemplate(ctx, tenant, reporting.ReportAuditSummary)
				return err
			}
		}},
		{"enterprise reports", func(b *externalDataBinding) (reporting.Option, func(context.Context, model.TenantID) error) {
			p := &externalReportSource{externalDataBinding: b}
			return reporting.WithEnterpriseReports(p), func(ctx context.Context, tenant model.TenantID) error {
				_, err := p.RiskSummary(ctx, tenant)
				return err
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binding := &externalDataBinding{}
			option, read := tt.wire(binding)
			m := reporting.New(option)
			readErr := errors.New("data handle refused the tenant read")
			data := &externalReadData{err: readErr}
			m.UseData(data)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			for _, tenant := range []model.TenantID{
				"018f0000-0000-7000-8000-000000000001",
				"018f0000-0000-7000-8000-000000000002",
			} {
				if err := read(ctx, tenant); !errors.Is(err, readErr) {
					t.Fatalf("provider read did not reach the supplied data handle: got %v, want %v", err, readErr)
				}
				if data.ctx != ctx || data.tenant != tenant {
					t.Fatalf("read changed request context or tenant: context preserved=%v, tenant=%q, want %q", data.ctx == ctx, data.tenant, tenant)
				}
			}
			if binding.data != data || binding.calls != 1 || data.views != 2 {
				t.Fatalf("binding/read lifecycle: original handle=%v, binds=%d, reads=%d; want true, 1, 2", binding.data == data, binding.calls, data.views)
			}
		})
	}
}

// These external providers cannot implement reporting's private binding method.
type externalDataBinding struct {
	data  api.ModuleData
	calls int
}

func (b *externalDataBinding) UseData(data api.ModuleData) {
	b.data = data
	b.calls++
}

func (b *externalDataBinding) read(ctx context.Context, tenant model.TenantID) error {
	if b.data == nil {
		return errors.New("provider has no data handle")
	}
	return b.data.View(ctx, tenant, func(store.Scope) error { return nil })
}

// Only the read methods exercised here are implemented; an unexpected call to
// another provider method fails through the nil embedded interface.
type externalScheduler struct {
	reporting.ReportScheduler
	*externalDataBinding
}

func (p *externalScheduler) ListSchedules(ctx context.Context, tenant model.TenantID) ([]reporting.ScheduleConfig, error) {
	return nil, p.read(ctx, tenant)
}

type externalBranding struct {
	reporting.BrandingProvider
	*externalDataBinding
}

func (p *externalBranding) GetBranding(ctx context.Context, tenant model.TenantID) (reporting.BrandingConfig, error) {
	return reporting.BrandingConfig{}, p.read(ctx, tenant)
}

type externalTemplates struct {
	reporting.CustomTemplateProvider
	*externalDataBinding
}

func (p *externalTemplates) GetTemplate(ctx context.Context, tenant model.TenantID, _ reporting.ReportType) (string, bool, error) {
	return "", false, p.read(ctx, tenant)
}

type externalReportSource struct {
	reporting.EnterpriseReportSource
	*externalDataBinding
}

func (p *externalReportSource) RiskSummary(ctx context.Context, tenant model.TenantID) (any, error) {
	return nil, p.read(ctx, tenant)
}

type externalReadData struct {
	ctx    context.Context
	tenant model.TenantID
	views  int
	err    error
}

func (d *externalReadData) View(ctx context.Context, tenant model.TenantID, _ func(store.Scope) error) error {
	d.ctx, d.tenant = ctx, tenant
	d.views++
	return d.err
}

func (*externalReadData) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	panic("report read attempted a mutation")
}
