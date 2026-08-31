// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// servedBusinessTenants enumerates the tenants the background pumps may work
// for: every org EXCEPT the reserved system tenant and any tenant whose service
// has been withdrawn.
//
// It replaces eight byte-identical private copies (eventing, orchestration
// cadence + workflow, guardian, ledger-forward, notify, report-schedule,
// retention sweep), each of which filtered ONLY the zero and system tenants.
// Every caller keeps the boundary it documented — the reserved SYSTEM tenant is
// skipped because platform/auth events and schedules are tenant-scoped facts —
// and gains the service-state filter.
//
// The service filter is NOT the enforcement boundary; core/suspension is. Each
// pump's per-tenant work re-enters the store through View/Mutate, so the guard
// already refuses a suspended tenant no matter what this returns — that is the
// deny-closed property, and it is what makes a suspended tenant unreachable by
// EVERY path rather than just by the API. What this filter adds is that the
// pumps stop ATTEMPTING work they will be denied: without it, each suspended
// tenant would draw a denied unit of work and an error log line from eight loops
// on every tick, turning a deliberate commercial decision into a log storm that
// looks like a fault.
//
// A tenant whose org read fails is not silently dropped: the error propagates, so
// a pump skips its tick loudly instead of quietly pumping a subset of the estate.
func servedBusinessTenants(ctx context.Context, st store.Store) ([]model.TenantID, error) {
	var tenants []model.TenantID
	err := st.System(ctx, func(sys store.SystemScope) error {
		orgs, err := sys.ListOrgs(ctx)
		if err != nil {
			return err
		}
		for _, o := range orgs {
			if o.TenantID.IsZero() || o.TenantID.IsSystem() {
				continue
			}
			if o.Status != model.StatusActive {
				continue // service withdrawn: not served, not pumped
			}
			tenants = append(tenants, o.TenantID)
		}
		return nil
	})
	return tenants, err
}
