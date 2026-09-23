// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

func TestLifecycleUseDataPreservesLegacyProviders(t *testing.T) {
	scheduler := &countedStoreScheduler{storeScheduler: NewStoreScheduler().(*storeScheduler)}
	branding := &countedStoreBranding{storeBranding: NewStoreBranding().(*storeBranding)}
	templates := &countedStoreTemplates{storeTemplates: NewStoreCustomTemplates().(*storeTemplates)}
	m := New(WithScheduler(scheduler), WithBranding(branding), WithCustomTemplates(templates))
	data := &dataBindingHandle{ModuleData: fakeModuleData{}}
	m.UseData(data)
	if m.data != data {
		t.Fatal("module did not retain its original data handle")
	}
	for _, tt := range []struct {
		name  string
		data  api.ModuleData
		calls int
	}{
		{"scheduler", scheduler.data, scheduler.calls},
		{"branding", branding.data, branding.calls},
		{"templates", templates.data, templates.calls},
	} {
		if tt.data != data || tt.calls != 1 {
			t.Errorf("%s: original handle=%v, binds=%d; want true, 1", tt.name, tt.data == data, tt.calls)
		}
	}
}

func TestLifecycleUseDataKeepsLegacyBindingFirst(t *testing.T) {
	provider := &dualBindingBranding{
		countedStoreBranding: &countedStoreBranding{storeBranding: NewStoreBranding().(*storeBranding)},
	}
	m := New(WithBranding(provider))
	data := &dataBindingHandle{ModuleData: fakeModuleData{}}
	m.UseData(data)
	if provider.data != data || provider.calls != 1 || provider.publicCalls != 0 {
		t.Fatalf("dual provider: original handle=%v, legacy binds=%d, public binds=%d; want true, 1, 0", provider.data == data, provider.calls, provider.publicCalls)
	}
}

func TestLifecycleUseDataLeavesStatelessProvidersUsable(t *testing.T) {
	provider := stubBranding{}
	m := New(WithBranding(provider))
	m.UseData(&fakeModuleData{})
	got, err := m.branding.GetBranding(context.Background(), model.TenantID("018f0000-0000-7000-8000-000000000001"))
	if err != nil || got != (BrandingConfig{}) {
		t.Fatalf("stateless provider changed after data binding: %+v, %v", got, err)
	}
}

// A nonzero-size pointer gives handle-identity assertions a distinct witness.
type dataBindingHandle struct{ api.ModuleData }

type countedStoreScheduler struct {
	*storeScheduler
	calls int
}

func (p *countedStoreScheduler) bindData(data api.ModuleData) {
	p.calls++
	p.storeScheduler.bindData(data)
}

type countedStoreBranding struct {
	*storeBranding
	calls int
}

func (p *countedStoreBranding) bindData(data api.ModuleData) {
	p.calls++
	p.storeBranding.bindData(data)
}

type countedStoreTemplates struct {
	*storeTemplates
	calls int
}

func (p *countedStoreTemplates) bindData(data api.ModuleData) {
	p.calls++
	p.storeTemplates.bindData(data)
}

type dualBindingBranding struct {
	*countedStoreBranding
	publicCalls int
}

func (p *dualBindingBranding) UseData(api.ModuleData) { p.publicCalls++ }
