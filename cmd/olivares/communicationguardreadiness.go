// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

type communicationGuardEstateReconciler interface {
	ReconcileCommunicationGuards(
		context.Context,
		model.TenantID,
		sessions.CommunicationGuardReconcileMode,
	) error
	VerifyCommunicationGuards(context.Context, model.TenantID) error
}

// communicationGuardStoreWitness owns the global assertion that every business
// tenant has passed staged repair followed by an enforced, verify-only pass.
// Tenant inventory remains engine-owned: the module
// receives tenant IDs one at a time and never receives System or a raw Store.
//
// A failed attempt is retained as UNKNOWN evidence. It is deliberately not a
// boot-fatal error while WP-3 is absent: the rest of the product may serve, but
// K3's complete readiness conjunction stays OFF.
type communicationGuardStoreWitness struct {
	listOrgs   sessionOrgLister
	residency  *residency.Registry
	reconciler communicationGuardEstateReconciler
	isLeader   func() bool

	runMu   sync.Mutex
	mu      sync.RWMutex
	ready   bool
	lastErr error
}

func newCommunicationGuardStoreWitness(
	listOrgs sessionOrgLister,
	residencyRegistry *residency.Registry,
	reconciler communicationGuardEstateReconciler,
	isLeader func() bool,
) *communicationGuardStoreWitness {
	return &communicationGuardStoreWitness{
		listOrgs: listOrgs, residency: residencyRegistry,
		reconciler: reconciler, isLeader: isLeader,
	}
}

// ReconcileAndVerify runs at the leadership-promotion barrier, after schema
// migration and default-workspace backfill but before leadership is published.
// It snapshots and validates the complete authoritative org inventory outside
// every tenant transaction, repairs every tenant with bounded workspace
// mutations, takes a second authoritative snapshot, then verifies every
// tenant without repair. Only the final successful edge publishes CLEAN.
func (w *communicationGuardStoreWitness) ReconcileAndVerify(ctx context.Context) (retErr error) {
	if w == nil || w.listOrgs == nil || w.reconciler == nil {
		return store.ErrStoreUnavailable
	}
	w.runMu.Lock()
	defer w.runMu.Unlock()
	w.beginAttempt()
	defer func() {
		if retErr != nil {
			w.finishAttempt(false, retErr)
		}
	}()
	// A region-scoped node cannot yet prove that a tenant repin was covered on
	// both sides of the move. Filtering the authoritative inventory to the local
	// region would turn that missing ceremony into a partial CLEAN assertion.
	// Until the region-move protocol exists, every enforcing registry therefore
	// stays UNKNOWN before the first tenant write, regardless of current pins.
	if w.residency != nil && w.residency.Enforces() {
		return fmt.Errorf(
			"%w: region-scoped communication guard readiness requires the region-move ceremony",
			store.ErrEnumerationNotAuthoritative,
		)
	}

	stagedTenants, err := w.localTenants(ctx)
	if err != nil {
		return fmt.Errorf("enumerate tenants for communication guard reconcile: %w", err)
	}
	for _, tenant := range stagedTenants {
		if err := w.reconciler.ReconcileCommunicationGuards(
			ctx, tenant, sessions.CommunicationGuardReconcileStaged,
		); err != nil {
			return fmt.Errorf("tenant %s communication guard reconcile: %w", tenant, err)
		}
	}

	// Re-enumerate authoritatively between the mutating and verify-only phases.
	// Fresh tenants/workspaces are initialized atomically, but this second snapshot
	// also proves admin visibility remained available for the CLEAN assertion.
	enforcedTenants, err := w.localTenants(ctx)
	if err != nil {
		return fmt.Errorf("enumerate tenants for communication guard verification: %w", err)
	}
	for _, tenant := range enforcedTenants {
		if err := w.reconciler.VerifyCommunicationGuards(ctx, tenant); err != nil {
			return fmt.Errorf("tenant %s communication guard verification: %w", tenant, err)
		}
	}
	w.finishAttempt(true, nil)
	return nil
}

// CommunicationStoreReady implements sessions.CommunicationStoreReadinessWitness.
// A cached successful proof is usable only while this node is the published
// leader. Demotion immediately makes the witness false without mutating module
// state; the next promotion resets and rebuilds the proof.
func (w *communicationGuardStoreWitness) CommunicationStoreReady(
	ctx context.Context,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if w == nil {
		return false, nil
	}
	if w.isLeader != nil && !w.isLeader() {
		return false, nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.ready && w.lastErr == nil, w.lastErr
}

func (w *communicationGuardStoreWitness) beginAttempt() {
	w.mu.Lock()
	w.ready = false
	w.lastErr = nil
	w.mu.Unlock()
}

func (w *communicationGuardStoreWitness) finishAttempt(ready bool, err error) {
	w.mu.Lock()
	w.ready = ready && err == nil
	w.lastErr = err
	w.mu.Unlock()
}

func (w *communicationGuardStoreWitness) localTenants(
	ctx context.Context,
) ([]model.TenantID, error) {
	orgs, err := w.listOrgs(ctx)
	if err != nil {
		return nil, err
	}
	validated := make([]model.TenantID, 0, len(orgs))
	seen := make(map[model.TenantID]struct{}, len(orgs))
	// Validate the COMPLETE inventory before the first tenant ID is returned. A
	// malformed or duplicate row is not something a coverage proof may skip.
	for _, org := range orgs {
		tenant, parseErr := model.ParseTenantID(org.ID.String())
		if parseErr != nil || tenant.IsZero() || org.TenantID != tenant {
			return nil, fmt.Errorf(
				"communication guard inventory contains invalid tenant lineage %q/%q: %w",
				org.ID, org.TenantID,
				errors.Join(parseErr, store.ErrEnumerationNotAuthoritative),
			)
		}
		if _, duplicate := seen[tenant]; duplicate {
			return nil, fmt.Errorf(
				"%w: communication guard inventory repeats tenant %s",
				store.ErrEnumerationNotAuthoritative, tenant,
			)
		}
		seen[tenant] = struct{}{}
		if tenant.IsSystem() {
			continue
		}
		validated = append(validated, tenant)
	}

	// Status is intentionally not filtered. This path uses the pre-suspension
	// tenant seam so an inactive/suspended tenant is initialized before a later
	// service restoration can expose it. The copy is a spread rather than a loop
	//: with no filter in the body, a loop invited the reader to look for one.
	local := make([]model.TenantID, 0, len(validated))
	local = append(local, validated...)
	sort.Slice(local, func(i, j int) bool { return local[i].String() < local[j].String() })
	return local, nil
}

var _ sessions.CommunicationStoreReadinessWitness = (*communicationGuardStoreWitness)(nil)
