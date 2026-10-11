// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestLifecycleUseDataLeavesStatelessProvidersUsable(t *testing.T) {
	provider := stubBranding{}
	m := New(WithBranding(provider))
	m.UseData(&fakeModuleData{})
	got, err := m.branding.GetBranding(context.Background(), model.TenantID("018f0000-0000-7000-8000-000000000001"))
	if err != nil || got != (BrandingConfig{}) {
		t.Fatalf("stateless provider changed after data binding: %+v, %v", got, err)
	}
}
