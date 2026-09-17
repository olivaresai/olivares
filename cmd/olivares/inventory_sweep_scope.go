// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
)

// inventorySweepScopeSource is the composition root's private answer to
// inventory.SweepScopeSource (D08-C2a): which tenants the durable freshness
// sweep should visit.
//
// It lives HERE, and not in the module, because reading the whole tenant
// directory is privileged and the module must stay unable to do it. The
// composition root already holds the composed store; this adapter keeps that
// handle to itself and hands the module ids and nothing else — no Store, no
// SystemScope, no SourceStore, no Auth. The module therefore gains no ability to
// open a scope it was not already given.
//
// What it returns is a SNAPSHOT OF CANDIDATES, never an authorization. Each id
// is then used to open the module's ordinary tenant-scoped ModuleData.Mutate,
// which still runs behind the residency guard (core/residency/store.go:141-166),
// the service-withdrawal guard (core/suspension/store.go:257-295) and the
// leadership write gate — so a tenant that was active when the snapshot was
// taken and suspended, re-pinned or dropped before its turn is refused there,
// by the store, not by this list being fresh.
type inventorySweepScopeSource struct {
	st  store.Store
	reg *residency.Registry
}

// ListSweepTenants enumerates the sweepable tenants of this instance.
//
// Three refusals, all of them deny-closed:
//
//   - Not the serving leader: a standby has no business sweeping, and its writes
//     would fail at the store's gate one transaction later anyway. Checking here
//     spends nothing on an estate this node is not serving. It is NOT a promise of
//     instant demotion — it is a cheap early stop, and the store's own gate remains
//     the enforcement.
//   - Any error from System or from the enumeration: the WHOLE result is dropped,
//     including the rows that came back with it. SystemScope.ListOrgs deliberately
//     returns the rows the pool can see ALONGSIDE ErrEnumerationNotAuthoritative
//     (core/internal/store/sqlstore/system.go:527-536), and on Postgres without a
//     BYPASSRLS admin pool those rows are whatever RLS happened to leave visible.
//     Sweeping them would report a partial pass as a complete one.
//   - ListOrgsVisible is never used here, and neither is any other source of
//     tenants. The named tolerance for a partial census exists for work that is
//     legitimately best-effort per tenant; "this estate's freshness is up to date"
//     is not that, so this call takes the authoritative door or none.
func (s inventorySweepScopeSource) ListSweepTenants(ctx context.Context) ([]model.TenantID, error) {
	if s.st == nil {
		return nil, fmt.Errorf("inventory sweep scope: no store is composed for the tenant directory")
	}
	if !s.st.Leader().Active() {
		return nil, fmt.Errorf("inventory sweep scope: %w", store.ErrNotLeader)
	}
	var orgs []model.Org
	if err := s.st.System(ctx, func(sys store.SystemScope) error {
		listed, err := sys.ListOrgs(ctx)
		if err != nil {
			return err
		}
		orgs = listed
		return nil
	}); err != nil {
		// orgs stays nil: it is only assigned on the successful path, so an error
		// from either the System container or the enumeration discards everything.
		return nil, fmt.Errorf("inventory sweep scope: enumerate the tenant directory: %w", err)
	}
	return sweepableTenants(orgs, s.reg)
}

// sweepableTenants filters an authoritative org census down to the tenants this
// instance may sweep: business tenants (never zero, never the system partition)
// that are active and whose residency pin this instance serves, deduplicated and
// in a stable order.
//
// A malformed business row is an ERROR rather than a skip, following the same
// rule as the promotion census (preparePDPReloadTenants, boot.go:126-140): a row
// this process cannot read is a tenant it cannot account for, and silently
// dropping it would turn an incomplete census into a clean-looking one.
//
// The ordering is imposed here rather than inherited. The store's query already
// orders by id ASC (system.go:631-638), but "stable and deduplicated" is part of
// what SweepScopeSource promises its caller, so it is established at the seam
// that promises it.
func sweepableTenants(orgs []model.Org, reg *residency.Registry) ([]model.TenantID, error) {
	seen := make(map[model.TenantID]struct{}, len(orgs))
	out := make([]model.TenantID, 0, len(orgs))
	for _, org := range orgs {
		tenant, parseErr := model.ParseTenantID(org.ID.String())
		if parseErr != nil || tenant.IsZero() {
			return nil, fmt.Errorf(
				"inventory sweep scope: the tenant directory contains an invalid tenant id %q: %w",
				org.ID, errors.Join(parseErr, store.ErrEnumerationNotAuthoritative),
			)
		}
		if org.TenantID != tenant {
			return nil, fmt.Errorf(
				"inventory sweep scope: org id %q carries a mismatched tenant id %q: %w",
				org.ID, org.TenantID, store.ErrEnumerationNotAuthoritative,
			)
		}
		// The system partition holds no discoverable estate, and the inventory
		// never writes to it (tenantOf, modules/inventory/inventory.go).
		if tenant.IsSystem() {
			continue
		}
		// A suspended org's service is withdrawn; the store would refuse its turn
		// anyway. Sweeping during suspension would need a narrow custody-like door
		// this work does not justify — it reappears in the directory, with its
		// cursor, when service is restored.
		if org.Status != model.StatusActive {
			continue
		}
		// Region-scoped instance: a tenant pinned elsewhere is not ours to sweep.
		// In single-region mode Serves is true for everything (region.go:129-138).
		if !reg.Serves(org.DataRegion) {
			continue
		}
		if _, dup := seen[tenant]; dup {
			continue
		}
		seen[tenant] = struct{}{}
		out = append(out, tenant)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
