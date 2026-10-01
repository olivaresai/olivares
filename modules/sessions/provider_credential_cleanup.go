// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Retire only a locator no longer published by a provider record. Bounded
// compensation survives client cancellation without acquiring module write locks.
func (m *Module) withdrawProviderCredential(ctx context.Context, actor auth.Principal, tenant model.TenantID, ref, locator string) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := m.rt.providerVault.Revoke(cleanup, actor, tenant, locator); err != nil && m.log != nil {
		m.log.Warn("sessions: could not withdraw an unpublished provider credential", "provider_ref", ref)
	}
}
