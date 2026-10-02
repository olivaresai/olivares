// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

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
//
// It reads ListOrgs, so it refuses on the default PostgreSQL install (no
// BYPASSRLS admin pool). Only the jobs that certify coverage of every tenant use
// it now (the retention sweep, the long-horizon legal hold): they must not claim
// a pass over tenants they cannot see. The best-effort pumps use
// servedWorkTenants, which every install can read.
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

// servedWorkTenants enumerates the tenants for per-tenant work that is best
// effort and must run on every supported install: the session approval recovery
// at start and the sessions work outbox.
//
// servedBusinessTenants reads ListOrgs. On the default PostgreSQL install (the
// application and owner roles, no BYPASSRLS admin pool) that read is RLS-limited
// to nothing and refuses with store.ErrEnumerationNotAuthoritative. This one
// reads what every install can:
//
//   - a store that can enumerate the estate (SQLite, or PostgreSQL with the admin
//     pool) gives its org rows (ListOrgsVisible, authoritative);
//   - otherwise, the tenants the auth partition grants access to: every business
//     tenant a membership names. A user acts in a tenant through a membership, so
//     a tenant that has sessions has one.
//
// Each candidate is then confirmed by its own org row in a tenant-scoped read,
// which the application role may do. A tenant this node does not serve (another
// region, service withdrawn, no longer present) is skipped; any other read error
// is returned.
//
// ListOrgs keeps its guard: a ceremony that certifies coverage calls it and
// fails closed.
func servedWorkTenants(ctx context.Context, st store.Store) ([]model.TenantID, error) {
	seen := map[model.TenantID]bool{}
	authoritative := false
	err := st.System(ctx, func(sys store.SystemScope) error {
		orgs, complete, err := sys.ListOrgsVisible(ctx)
		if err != nil {
			return err
		}
		authoritative = complete
		for _, o := range orgs {
			seen[o.TenantID] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the visible orgs: %w", err)
	}
	if !authoritative {
		if err := st.AuthView(ctx, func(as store.AuthScope) error {
			q := model.Query{Limit: 500}
			for {
				rows, page, err := as.Memberships().List(ctx, q)
				if err != nil {
					return err
				}
				for _, m := range rows {
					seen[m.TargetTenantID] = true
				}
				if !page.HasMore || page.Cursor == "" || page.Cursor == q.Cursor {
					return nil
				}
				q.Cursor = page.Cursor
			}
		}); err != nil {
			return nil, fmt.Errorf("read the tenants the auth partition grants access to: %w", err)
		}
	}
	candidates := slices.Sorted(maps.Keys(seen))
	var tenants []model.TenantID
	for _, tenant := range candidates {
		served, err := servesTenant(ctx, st, tenant)
		if err != nil {
			return nil, err
		}
		if served {
			tenants = append(tenants, tenant)
		}
	}
	return tenants, nil
}

// servesTenant reports whether this node serves ONE business tenant, by that
// tenant's own org row: the check servedWorkTenants makes for each candidate. A
// request that names its tenant (a tool's login home, the local Ollama's
// registration) asks this instead of enumerating every tenant, so it works on the
// default PostgreSQL install, where ListOrgs refuses. The zero and system tenants,
// a tenant of another region, a withdrawn or absent one are not served; any other
// read error is returned.
func servesTenant(ctx context.Context, st store.Store, tenant model.TenantID) (bool, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return false, nil
	}
	var status model.LifecycleStatus
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		status = org.Status
		return err
	})
	switch {
	case errors.Is(err, store.ErrResidencyViolation), errors.Is(err, store.ErrTenantSuspended), errors.Is(err, store.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read the org of tenant %s: %w", tenant, err)
	}
	return status == model.StatusActive, nil
}
