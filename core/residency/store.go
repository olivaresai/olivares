// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package residency

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Guard wraps a store.Store with deny-closed cross-region residency enforcement
// for a region-scoped instance. When reg does not enforce (single-region mode,
// or a nil registry) it returns inner unchanged — zero overhead and no behavior
// change for the default single-region deployment. Otherwise it returns a
// decorator that, on every tenant-scoped View/Mutate, verifies the bound
// tenant's residency pin (orgs.data_region) is served by reg's home region and
// fails with store.ErrResidencyViolation otherwise. The System and Auth paths
// (cross-tenant provisioning, the system-tenant credential partition) pass
// through untouched: they are local to the instance by construction.
func Guard(inner store.Store, reg *Registry, log *slog.Logger) store.Store {
	if inner == nil || !reg.Enforces() {
		return inner
	}
	g := &guardStore{inner: inner, reg: reg, log: log}
	// Capability-preserving. Forwarding AuditSpoolStatuser alone was not enough:
	// callers also type-assert store.RolloutStater off the Store, and swallowing it
	// made a region-scoped instance UNBOOTABLE — `olivares serve --region eu` died
	// with "eventing: the store does not expose durable rollout state, so the
	// egress destination control cannot establish whether it is in force"
	// (reproduced against a built binary). The decorator must expose the
	// capability when — and only when — the wrapped store really has it, so the
	// composition root's own deny-closed check stays triggerable.
	if _, ok := inner.(store.RolloutStater); ok {
		return &guardStoreWithRollout{guardStore: g}
	}
	return g
}

// guardStore is the deny-closed residency decorator over store.Store. It adds a
// single bound-tenant org read in front of each tenant-scoped unit of work; the
// read is folded into the same transaction as the caller's fn (one transaction,
// an indexed primary-key lookup), so there is no second round trip and the check
// and the work commit or roll back together.
type guardStore struct {
	inner store.Store
	reg   *Registry
	log   *slog.Logger
}

// View runs fn read-only, denied closed if the tenant is not resident here.
func (g *guardStore) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if g.passThrough(tenant) {
		return g.inner.View(ctx, tenant, fn)
	}
	return g.inner.View(ctx, tenant, g.wrap(ctx, tenant, fn))
}

// Mutate runs fn read-write, denied closed if the tenant is not resident here.
func (g *guardStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if g.passThrough(tenant) {
		return g.inner.Mutate(ctx, tenant, fn)
	}
	return g.inner.Mutate(ctx, tenant, g.wrap(ctx, tenant, fn))
}

// Custody runs fn over the tenant's evidence ledger, denied closed if the tenant
// is not resident here. Residency DOES gate custody, unlike suspension: keeping a
// customer's evidence anchored is never a reason to read their data from a region
// that is not allowed to serve it. The check is the same bound-tenant org read,
// folded into the same transaction.
func (g *guardStore) Custody(ctx context.Context, tenant model.TenantID, fn func(store.CustodyScope) error) error {
	if g.passThrough(tenant) {
		return g.inner.Custody(ctx, tenant, fn)
	}
	return g.inner.Custody(ctx, tenant, func(cs store.CustodyScope) error {
		org, err := cs.Org(ctx)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return g.deny(tenant, "", fmt.Sprintf("tenant %s is not resident in region %q", tenant, g.reg.home))
			}
			return err
		}
		if !g.reg.Serves(org.DataRegion) {
			return g.deny(tenant, org.DataRegion,
				fmt.Sprintf("tenant %s is pinned to region %q, not served by region %q", tenant, org.DataRegion, g.reg.home))
		}
		return fn(cs)
	})
}

// Export runs fn over the tenant's exportable data, denied closed if the tenant is
// not resident here. Residency gates portability exactly as it gates everything
// else: getting your data out is not a reason to read it from a region that may
// not serve it — the copy would cross the border the pin exists to hold.
func (g *guardStore) Export(ctx context.Context, tenant model.TenantID, fn func(store.ExportScope) error) error {
	if g.passThrough(tenant) {
		return g.inner.Export(ctx, tenant, fn)
	}
	return g.inner.Export(ctx, tenant, func(es store.ExportScope) error {
		org, err := es.Org(ctx)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return g.deny(tenant, "", fmt.Sprintf("tenant %s is not resident in region %q", tenant, g.reg.home))
			}
			return err
		}
		if !g.reg.Serves(org.DataRegion) {
			return g.deny(tenant, org.DataRegion,
				fmt.Sprintf("tenant %s is pinned to region %q, not served by region %q", tenant, org.DataRegion, g.reg.home))
		}
		return fn(es)
	})
}

// passThrough reports tenants the guard never gates: the zero tenant (the inner
// store rejects it with ErrNoTenant) and the reserved system tenant (the local
// auth/provisioning partition, which every instance holds for itself).
func (g *guardStore) passThrough(tenant model.TenantID) bool {
	return tenant.IsZero() || tenant.IsSystem()
}

// wrap returns fn prefixed with the deny-closed residency check, reading the
// bound tenant's org inside the same scope/transaction. A tenant with no org row
// in this instance (the cross-region case: data resident elsewhere) reads as
// ErrNotFound and is denied closed — never run as if it had no data.
func (g *guardStore) wrap(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) func(store.Scope) error {
	return func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return g.deny(tenant, "", fmt.Sprintf("tenant %s is not resident in region %q", tenant, g.reg.home))
			}
			return err
		}
		if !g.reg.Serves(org.DataRegion) {
			return g.deny(tenant, org.DataRegion,
				fmt.Sprintf("tenant %s is pinned to region %q, not served by region %q", tenant, org.DataRegion, g.reg.home))
		}
		return fn(sc)
	}
}

// deny logs the cross-region attempt (a security-relevant event) and returns the
// typed, wrapped error. The detail is for the operator log/API body; the
// sentinel store.ErrResidencyViolation is what callers match with errors.Is.
func (g *guardStore) deny(tenant model.TenantID, pin, detail string) error {
	if g.log != nil {
		g.log.Warn("residency: denied cross-region access (deny-closed)",
			"tenant", tenant.String(), "pinned_region", pin, "home_region", g.reg.home.String())
	}
	return fmt.Errorf("%w: %s", store.ErrResidencyViolation, detail)
}

// The remaining methods delegate unchanged: System/Auth are local to the
// instance, and Engine/Ping/Close are pool-level.

func (g *guardStore) System(ctx context.Context, fn func(store.SystemScope) error) error {
	return g.inner.System(ctx, fn)
}

func (g *guardStore) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	return g.inner.AuthView(ctx, fn)
}

func (g *guardStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return g.inner.AuthMutate(ctx, fn)
}

// Leader delegates to the wrapped store's leadership elector: residency is a
// per-region routing concern layered ABOVE leadership, which stays a property of the
// underlying regional store. The HA write-gate therefore continues to apply unchanged
// inside the wrapped View/Mutate/System.
func (g *guardStore) Leader() store.LeaderElector { return g.inner.Leader() }

// AuditSpoolStatus forwards the optional observability capability when the
// wrapped store provides it. Store decorators must not swallow optional
// capabilities at the observability edge.
func (g *guardStore) AuditSpoolStatus(ctx context.Context) (store.AuditSpoolStatus, bool, error) {
	statuser, ok := g.inner.(store.AuditSpoolStatuser)
	if !ok {
		return store.AuditSpoolStatus{}, false, nil
	}
	return statuser.AuditSpoolStatus(ctx)
}

// DirectoryStatus forwards the optional directory-fence witness. Its own
// supported bit makes unconditional forwarding honest: a wrapped store without
// the capability remains unsupported and can never be mistaken for K3-ready.
func (g *guardStore) DirectoryStatus(ctx context.Context) (store.DirectoryStatus, bool, error) {
	statuser, ok := g.inner.(store.DirectoryStatuser)
	if !ok {
		return store.DirectoryStatus{}, false, nil
	}
	return statuser.DirectoryStatus(ctx)
}

// guardStoreWithRollout is the guard over a store that exposes durable rollout
// state. Guard returns it only in that case, so the capability is preserved
// without ever being fabricated. Rollout controls are deployment-wide state with
// no tenant bound, so residency never gates them: they carry no tenant whose pin
// could be checked. These delegate straight through.
type guardStoreWithRollout struct {
	*guardStore
}

func (g *guardStoreWithRollout) RolloutState(ctx context.Context, key string) (store.RolloutState, error) {
	return g.inner.(store.RolloutStater).RolloutState(ctx, key)
}

func (g *guardStoreWithRollout) RolloutHistory(ctx context.Context, key string) ([]store.RolloutTransitionRecord, error) {
	return g.inner.(store.RolloutStater).RolloutHistory(ctx, key)
}

func (g *guardStoreWithRollout) SetRolloutMode(ctx context.Context, t store.RolloutTransition) (store.RolloutState, error) {
	return g.inner.(store.RolloutStater).SetRolloutMode(ctx, t)
}

func (g *guardStore) Engine() store.Engine { return g.inner.Engine() }

func (g *guardStore) Ping(ctx context.Context) error { return g.inner.Ping(ctx) }

func (g *guardStore) Close() error { return g.inner.Close() }
